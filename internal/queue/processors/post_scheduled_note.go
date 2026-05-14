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

	"github.com/shiroha-a/mk/internal/core/note"
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

// PostScheduledNoteProcessor publishes a note from a previously-stored
// `note_draft` row whose `isActuallyScheduled=true` and `scheduledAt` has
// arrived. upstream `PostScheduledNoteProcessorService` の Go port (#1040)。
type PostScheduledNoteProcessor struct {
	drafts    ScheduledNoteDraftRepo
	users     ScheduledNoteUserRepo
	publisher ScheduledNotePublisher
}

// NewPostScheduledNoteProcessor wires the processor with its dependencies.
func NewPostScheduledNoteProcessor(drafts ScheduledNoteDraftRepo, users ScheduledNoteUserRepo, publisher ScheduledNotePublisher) *PostScheduledNoteProcessor {
	return &PostScheduledNoteProcessor{drafts: drafts, users: users, publisher: publisher}
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
func (p *PostScheduledNoteProcessor) Handle(_ context.Context, task driver.Task) error {
	payload, err := queue.DecodePostScheduledNotePayload(task.Payload())
	if err != nil {
		return fmt.Errorf("decode payload: %w", err)
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
	if _, err := p.publisher.Create(in); err != nil {
		// publish 失敗時は asynq retry に任せ、draft 行はそのまま残す
		// (= 次回 retry で再試行可能)。upstream は notification を発火する
		// が mk-go はまだ scheduledNotePostFailed notification type を持た
		// ないので log のみ。
		slog.Warn("scheduled note publish failed",
			"noteDraftId", payload.NoteDraftID, "userId", draft.UserID, "err", err)
		return fmt.Errorf("publish scheduled note: %w", err)
	}
	if _, err := p.drafts.Delete(draft.ID, draft.UserID); err != nil {
		// publish 成功 + draft 削除失敗時は draft が残るだけで二重 publish
		// は起きない (asynq job 自体は 1 回しか発火しない)。log のみ。
		slog.Warn("scheduled note draft delete failed",
			"noteDraftId", payload.NoteDraftID, "err", err)
	}
	return nil
}
