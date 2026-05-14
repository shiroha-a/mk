// Package processors hosts queue task handlers consumed by the asynq worker.
//
// This file implements the scheduled-note publish path (#1040). It is a thin
// orchestration layer: load the draft, materialise it into a note via the
// shared note.CreateService, then delete the draft. Notification on
// success / failure is intentionally out of scope for the first PR and may be
// added in a follow-up once mk-go gains the upstream `scheduledNotePosted` /
// `scheduledNotePostFailed` notification types.
package processors

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/shiroha-a/mk/internal/core/note"
	"github.com/shiroha-a/mk/internal/core/notification"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/queue"
	"github.com/shiroha-a/mk/internal/queue/driver"
)

// ScheduledNoteDraftRepo is the narrow subset of NoteDraftRepository required
// by PostScheduledNoteProcessor. Defined as a separate interface so test
// fixtures can swap a minimal stub without touching the full repository.
type ScheduledNoteDraftRepo interface {
	FindByID(id string) (*model.NoteDraft, error)
	Delete(id, userID string) (int64, error)
}

// ScheduledNoteLock provides app-level idempotency for scheduled note publish.
// asynq の at-least-once delivery で job が二度 fire しても TryAcquire が
// 1 度しか true を返さないことで重複 publish を防止する (#1045 Phase 2-A)。
//
// DB schema 不変な実装 (Redis SETNX + TTL 等) を選ぶことで upstream Misskey
// TS の `note_draft` schema と完全互換のまま重複 publish 耐性を確保する。
//
// 実装は nil 可 (= 失敗時 / 未配線時は TryAcquire が常に true を返す挙動と
// 等価で、Phase 1 の旧経路と互換)。production は redis-backed 実装を必ず
// 配線するべき。
type ScheduledNoteLock interface {
	// TryAcquire returns (true, nil) when this caller is the first to claim
	// the lock for draftID (= publish に進める), or (false, nil) when another
	// retry already holds it (= silent skip). err != nil なら lock backend
	// 自体の障害なので caller は retry に任せる。
	TryAcquire(ctx context.Context, draftID string) (bool, error)
}

// ScheduledNoteUserRepo abstracts the single user lookup needed to call
// note.CreateService.Create with a fully-populated *model.User.
type ScheduledNoteUserRepo interface {
	FindByID(id string) (*model.User, error)
}

// ScheduledNotePublisher abstracts note.CreateService so callers can pass a
// real *note.CreateService in production or a stub in tests.
type ScheduledNotePublisher interface {
	Create(in note.CreateInput) (*model.Note, error)
}

// ScheduledNoteNotifier dispatches the scheduled note publish result to the
// notification system. upstream Misskey TS の \`NotificationService.create\`
// と同 contract で、posted (= 成功時 noteId) / postFailed (= 失敗時
// noteDraftId) の 2 種を発火する (#1045 Phase 2-B)。nil の場合は notification
// skip (= Phase 2-A 時点の slog のみ挙動と互換)。
type ScheduledNoteNotifier interface {
	Create(ctx context.Context, in notification.CreateInput) (*notification.Notification, error)
}

// PostScheduledNoteProcessor publishes a note from a previously-stored
// `note_draft` row whose `isActuallyScheduled=true` and `scheduledAt` has
// arrived. upstream `PostScheduledNoteProcessorService` の Go port (#1040)。
type PostScheduledNoteProcessor struct {
	drafts    ScheduledNoteDraftRepo
	users     ScheduledNoteUserRepo
	publisher ScheduledNotePublisher
	// lock は idempotency 用 (#1045 Phase 2-A)。nil の場合は lock skip = Phase 1
	// と同等の at-least-once 挙動 (= test fixture / 未配線 path)。
	lock ScheduledNoteLock
	// notifier は publish 結果通知用 (#1045 Phase 2-B)。nil で発火 skip。
	notifier ScheduledNoteNotifier
}

// NewPostScheduledNoteProcessor wires the processor with its dependencies.
func NewPostScheduledNoteProcessor(drafts ScheduledNoteDraftRepo, users ScheduledNoteUserRepo, publisher ScheduledNotePublisher) *PostScheduledNoteProcessor {
	return &PostScheduledNoteProcessor{drafts: drafts, users: users, publisher: publisher}
}

// SetLock wires an app-level idempotency lock. nil disables (= Phase 1 互換)。
// production では Redis-backed 実装を配線して重複 publish を防止する
// (#1045 Phase 2-A)。
func (p *PostScheduledNoteProcessor) SetLock(l ScheduledNoteLock) {
	p.lock = l
}

// SetNotifier wires the notification dispatcher used to fire
// \`scheduledNotePosted\` / \`scheduledNotePostFailed\` on publish completion
// (#1045 Phase 2-B)。nil disables (= notification skip)、production では
// core/notification.Service を配線する。
func (p *PostScheduledNoteProcessor) SetNotifier(n ScheduledNoteNotifier) {
	p.notifier = n
}

// Handle implements the asynq task handler signature. It decodes the payload,
// guards against drafts that no longer exist or were unscheduled before the
// trigger fired, materialises the note via the publisher, and finally deletes
// the draft row.
//
// Errors are returned to the queue so asynq retries; transient DB failures
// will be retried automatically. A draft that vanished (= user deleted it
// before fire time) is treated as a no-op (= return nil) — upstream behaves
// the same way (`if (draft == null || ...) return`)。
func (p *PostScheduledNoteProcessor) Handle(ctx context.Context, task driver.Task) error {
	payload, err := queue.DecodePostScheduledNotePayload(task.Payload())
	if err != nil {
		return fmt.Errorf("decode payload: %w", err)
	}
	// idempotency lock 取得 (#1045 Phase 2-A)。asynq の at-least-once
	// delivery で job が二度 fire した場合、最初の retry が lock を保持し、
	// 後続 retry は false を得て silent skip する。lock 未配線なら常に
	// true (= Phase 1 互換挙動、production race 受容)。
	if p.lock != nil {
		ok, lockErr := p.lock.TryAcquire(ctx, payload.NoteDraftID)
		if lockErr != nil {
			// lock backend error は retry に任せる (asynq が exponential
			// backoff で再試行する)。返した error は queue 側で log される。
			return fmt.Errorf("acquire scheduled note lock: %w", lockErr)
		}
		if !ok {
			// 他 retry が処理中 / 完了済み。本 caller は何もしない。
			slog.Info("scheduled note already being published, skipping",
				"noteDraftId", payload.NoteDraftID)
			return nil
		}
	}
	draft, err := p.drafts.FindByID(payload.NoteDraftID)
	if err != nil {
		// Draft が消えていれば user 削除 / 手動 cancel / 既に publish 済み
		// のどれかで、いずれも retry しても解消しない。silent success で
		// 終わる (upstream と等価)。
		slog.Info("scheduled note draft missing, skipping",
			"noteDraftId", payload.NoteDraftID, "err", err)
		return nil
	}
	if draft.ScheduledAt == nil || !draft.IsActuallyScheduled {
		// Draft が unschedule された経路 (= 将来 DraftsUpdate で scheduledAt
		// クリア相当を許容する場合)。何もせず draft を残す。
		return nil
	}
	user, err := p.users.FindByID(draft.UserID)
	if err != nil {
		return fmt.Errorf("load draft user: %w", err)
	}
	if user == nil {
		// 防御的: nil ユーザを Create に流すと panic の元なので skip。
		return errors.New("scheduled note draft has nil user")
	}
	in := note.CreateInput{
		User:           user,
		Text:           draft.Text,
		CW:             draft.CW,
		Visibility:     model.NoteVisibility(draft.Visibility),
		VisibleUserIDs: draft.VisibleUserIDs,
		LocalOnly:      draft.LocalOnly,
		FileIDs:        draft.FileIDs,
		ReplyID:        draft.ReplyID,
		RenoteID:       draft.RenoteID,
		ChannelID:      draft.ChannelID,
	}
	if draft.ReactionAcceptance != nil {
		ra := *draft.ReactionAcceptance
		in.ReactionAcceptance = &ra
	}
	// Poll の復元 (#1045 Phase 2-D)。upstream
	// `PostScheduledNoteProcessorService.process` と同 logic で:
	//   - hasPoll=false なら Poll=nil (= 設定なし)
	//   - hasPoll=true なら PollChoices / PollMultiple / PollExpiresAt または
	//     PollExpiredAfter から `*time.Time` 期限を復元
	//   - PollExpiredAfter (= 経過 ms) 優先、無ければ PollExpiresAt (= 絶対時刻)
	if draft.HasPoll {
		pollInput := &note.PollInput{
			Choices:  draft.PollChoices,
			Multiple: draft.PollMultiple,
		}
		if draft.PollExpiredAfter != nil {
			exp := time.Now().Add(time.Duration(*draft.PollExpiredAfter) * time.Millisecond)
			pollInput.ExpiresAt = &exp
		} else if draft.PollExpiresAt != nil {
			exp := *draft.PollExpiresAt
			pollInput.ExpiresAt = &exp
		}
		in.Poll = pollInput
	}
	publishedNote, err := p.publisher.Create(in)
	if err != nil {
		// publish 失敗時は asynq retry に任せ、draft 行はそのまま残す
		// (= 次回 retry で再試行可能)。同時に user に postFailed 通知を
		// 1 度だけ発火する (= upstream \`scheduledNotePostFailed\` と同 shape、
		// extra に noteDraftId)。
		slog.Warn("scheduled note publish failed",
			"noteDraftId", payload.NoteDraftID, "userId", draft.UserID, "err", err)
		if p.notifier != nil {
			if _, nerr := p.notifier.Create(ctx, notification.CreateInput{
				NotifieeID: draft.UserID,
				Type:       notification.TypeScheduledNotePostFailed,
				Extra:      map[string]any{"noteDraftId": draft.ID},
			}); nerr != nil {
				// notification 失敗は publish error 経路に影響させない (= retry
				// 中に多重通知を避ける best-effort)。
				slog.Warn("scheduled note postFailed notification failed",
					"noteDraftId", payload.NoteDraftID, "err", nerr)
			}
		}
		return fmt.Errorf("publish scheduled note: %w", err)
	}
	// publish 成功通知 (#1045 Phase 2-B)。upstream は \`noteId\` を引数で渡し、
	// frontend が note を embed して表示する。
	if p.notifier != nil {
		if _, nerr := p.notifier.Create(ctx, notification.CreateInput{
			NotifieeID: draft.UserID,
			Type:       notification.TypeScheduledNotePosted,
			NoteID:     publishedNote.ID,
		}); nerr != nil {
			slog.Warn("scheduled note posted notification failed",
				"noteDraftId", payload.NoteDraftID, "err", nerr)
		}
	}
	if _, err := p.drafts.Delete(draft.ID, draft.UserID); err != nil {
		// publish 成功 + draft 削除失敗時は draft が残るだけで二重 publish
		// は起きない (asynq job 自体は 1 回しか発火しない)。log のみ。
		slog.Warn("scheduled note draft delete failed",
			"noteDraftId", payload.NoteDraftID, "err", err)
	}
	return nil
}
