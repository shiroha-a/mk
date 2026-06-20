package notes

import (
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/lib/pq"
	"github.com/shiroha-a/mk/internal/api/apierr"
	"github.com/shiroha-a/mk/internal/api/pagination"
	"github.com/shiroha-a/mk/internal/entity"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/queue"
	"github.com/shiroha-a/mk/internal/queue/driver"
	"github.com/shiroha-a/mk/internal/repository"
	"github.com/shiroha-a/mk/internal/server/middleware"
)

// SetDraftRepo attaches a NoteDraftRepository for draft operations.
func (h *Handler) SetDraftRepo(r repository.NoteDraftRepository) {
	h.draftRepo = r
}

// DraftsList handles POST /api/notes/drafts/list.
func (h *Handler) DraftsList(c echo.Context) error {
	user := middleware.GetUser(c)
	if h.draftRepo == nil {
		return c.JSON(http.StatusOK, []any{})
	}
	// upstream drafts/list.ts は limit(default30/max100) + sinceId/untilId +
	// scheduled(boolean) を受ける (#1538)。sinceDate/untilDate は mk-go の
	// id-based keyset 方針 (NoteDraft に createdAt 列が無い) のため非対応。
	var req struct {
		Limit     int    `json:"limit"`
		SinceID   string `json:"sinceId"`
		UntilID   string `json:"untilId"`
		Scheduled *bool  `json:"scheduled"`
	}
	_ = c.Bind(&req)
	limit := pagination.ClampLimit(req.Limit, 30, 100)
	drafts, err := h.draftRepo.ListByUser(user.ID, req.SinceID, req.UntilID, req.Scheduled, limit)
	if err != nil {
		return c.JSON(http.StatusOK, []any{})
	}
	// 全 draft の fileIds をまとめて 1 query で解決し N+1 を回避する。
	allFileIDs := []string{}
	for _, d := range drafts {
		allFileIDs = append(allFileIDs, []string(d.FileIDs)...)
	}
	fileByID := h.resolveDraftFileMap(allFileIDs)
	out := make([]map[string]any, len(drafts))
	for i, d := range drafts {
		// drafts は viewer 自身のものなので user を埋める (repo は Preload しない)。
		d.User = user
		out[i] = h.packDraft(d, fileByID)
	}
	return c.JSON(http.StatusOK, out)
}

// DraftsCreate handles POST /api/notes/drafts/create.
// upstream `notes/drafts/create.ts` 互換で scheduledAt / isActuallyScheduled
// を受け入れ、scheduledNoteLimit policy gate + 遅延 queue enqueue を行う
// (#1040)。
func (h *Handler) DraftsCreate(c echo.Context) error {
	user := middleware.GetUser(c)
	if h.draftRepo == nil {
		return c.NoContent(http.StatusNoContent)
	}
	var req struct {
		Text                *string    `json:"text"`
		CW                  *string    `json:"cw"`
		Visibility          string     `json:"visibility"`
		VisibleUserIDs      []string   `json:"visibleUserIds"`
		LocalOnly           bool       `json:"localOnly"`
		ReactionAcceptance  *string    `json:"reactionAcceptance"`
		FileIDs             []string   `json:"fileIds"`
		ReplyID             *string    `json:"replyId"`
		RenoteID            *string    `json:"renoteId"`
		ChannelID           *string    `json:"channelId"`
		Hashtag             *string    `json:"hashtag"`
		Poll                *draftPoll `json:"poll"`
		ScheduledAt         *int64     `json:"scheduledAt"`
		IsActuallyScheduled bool       `json:"isActuallyScheduled"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierr.InvalidParam("Invalid parameters."))
	}
	// visibility / reactionAcceptance enum 検証 (notes/create と共有、#1934)。
	// draft 経由でも upstream drafts/create.ts:161,168 の enum 制約に揃え、不正値が
	// scheduled-note 処理経由で投稿されるのを防ぐ。
	if err := validateNoteEnums(req.Visibility, req.ReactionAcceptance); err != nil {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", err.Error(), apierr.UUIDInvalidParam))
	}
	if req.Visibility == "" {
		req.Visibility = "public"
	}
	// scheduledAt の validation (#1040)。upstream は ms epoch (`integer`)
	// で受け取って Date 化、time.Now() より将来であることを要求する。
	// isActuallyScheduled=false なら scheduledAt は単なる「下書きとして保持
	// する希望時刻」扱いで、queue enqueue / future check は行わない。
	now := time.Now()
	var scheduledAt *time.Time
	if req.ScheduledAt != nil {
		t := time.UnixMilli(*req.ScheduledAt)
		scheduledAt = &t
	}
	if req.IsActuallyScheduled {
		// asynq driver は scheduled note 機能を確実に support できないため
		// (= clearSchedule の task ID 仕様制約)、機能無効化する。upstream
		// 互換の \`TOO_MANY_SCHEDULED_NOTES\` で reject し、frontend には
		// 上限到達と同じ UX を返す (#1045 Phase 2-C)。
		if h.scheduledNoteEnqueuer != nil && !h.scheduledNoteEnqueuer.SupportsScheduledNote() {
			return c.JSON(http.StatusBadRequest, apierr.Error("TOO_MANY_SCHEDULED_NOTES", "You cannot create scheduled notes any more.", "22ae69eb-09e3-4541-a850-773cfa45e693"))
		}
		if scheduledAt == nil {
			// Misskey は SCHEDULED_AT_* に create / update で別 id を割り当てるため endpoint 固有 id を inline で返す
			return c.JSON(http.StatusBadRequest, apierr.Error("SCHEDULED_AT_REQUIRED", "scheduledAt is required when isActuallyScheduled is true.", "15e28a55-e74c-4d65-89b7-8880cdaaa87d"))
		}
		if !scheduledAt.After(now) {
			return c.JSON(http.StatusBadRequest, apierr.Error("SCHEDULED_AT_MUST_BE_IN_FUTURE", "scheduledAt must be in the future.", "e4bed6c9-017e-4934-aed0-01c22cc60ec1"))
		}
	}
	// noteDraftLimit role policy gate (#1029)。policyProvider は #1026 で
	// 配線済 (timeline gate と共用)。CountByUser で current 件数を取得。
	if h.policyProvider != nil {
		policies := h.policyProvider.GetUserPolicies(user.ID)
		if limit, ok := policies["noteDraftLimit"].(int); ok && limit >= 0 {
			count, err := h.draftRepo.CountByUser(user.ID)
			if err != nil {
				return apierr.JSONInternalError(c)
			}
			if count >= int64(limit) {
				return apierr.JSONTooManyNoteDrafts(c)
			}
		}
		// scheduledNoteLimit gate (#1040)。isActuallyScheduled draft の数を
		// 集計して上限と比較する。upstream `NoteDraftService.create` と同 logic。
		if req.IsActuallyScheduled {
			if limit, ok := policies["scheduledNoteLimit"].(int); ok && limit >= 0 {
				count, err := h.draftRepo.CountScheduledByUser(user.ID)
				if err != nil {
					return apierr.JSONInternalError(c)
				}
				if count >= int64(limit) {
					return c.JSON(http.StatusBadRequest, apierr.Error("TOO_MANY_SCHEDULED_NOTES", "You cannot create scheduled notes any more.", "22ae69eb-09e3-4541-a850-773cfa45e693"))
				}
			}
		}
	}
	// fileIds は全て呼出ユーザー所有でなければ NO_SUCH_FILE (upstream NoteDraftService)。
	if !h.allDraftFilesOwned(req.FileIDs, user.ID) {
		return c.JSON(http.StatusBadRequest, apierr.Error("NO_SUCH_FILE", "Some files are not found.", "b6992544-63e7-67f0-fa7f-32444b1b5306"))
	}
	// 既に期限切れの poll は作成不可 (CANNOT_CREATE_ALREADY_EXPIRED_POLL)。
	if pollAlreadyExpired(req.Poll, now) {
		return c.JSON(http.StatusBadRequest, apierr.Error("CANNOT_CREATE_ALREADY_EXPIRED_POLL", "Cannot create an already expired poll.", "04da457d-b083-4055-9082-955525eda5a5"))
	}
	draft := &model.NoteDraft{
		ID:                  h.idGen.Generate(now),
		UserID:              user.ID,
		Text:                req.Text,
		CW:                  req.CW,
		Visibility:          req.Visibility,
		VisibleUserIDs:      pq.StringArray(req.VisibleUserIDs),
		LocalOnly:           req.LocalOnly,
		ReactionAcceptance:  req.ReactionAcceptance,
		FileIDs:             pq.StringArray(req.FileIDs),
		ReplyID:             req.ReplyID,
		RenoteID:            req.RenoteID,
		ChannelID:           req.ChannelID,
		Hashtag:             req.Hashtag,
		ScheduledAt:         scheduledAt,
		IsActuallyScheduled: req.IsActuallyScheduled,
	}
	applyDraftPoll(draft, req.Poll)
	if err := h.draftRepo.Create(draft); err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.InternalError())
	}
	// scheduled の場合は遅延 queue に投入 (= upstream の `schedule(draft)`
	// と等価、delay = scheduledAt - now)。enqueue 失敗時は draft を残しつつ
	// 500 を返し、frontend に再試行を促す (draft 残しは upstream と同じく
	// 削除しない方が観測可能で安全)。
	if req.IsActuallyScheduled && h.scheduledNoteEnqueuer != nil {
		delay := scheduledAt.Sub(now)
		if err := h.scheduledNoteEnqueuer.EnqueuePostScheduledNote(
			queue.PostScheduledNotePayload{NoteDraftID: draft.ID},
			driver.WithProcessIn(delay),
		); err != nil {
			return apierr.JSONInternalError(c)
		}
	}
	draft.User = user
	// upstream res は { createdDraft: NoteDraft } で包む。
	return c.JSON(http.StatusOK, map[string]any{"createdDraft": h.packDraft(draft, nil)})
}

// DraftsUpdate handles POST /api/notes/drafts/update.
// scheduledAt / isActuallyScheduled の変更を upstream `notes/drafts/update`
// と同 logic で取り扱い、変更があれば旧 delayed task を clear → 必要なら
// 新 scheduledAt で再 enqueue する (#1045 Phase 2-C)。
func (h *Handler) DraftsUpdate(c echo.Context) error {
	user := middleware.GetUser(c)
	if h.draftRepo == nil {
		return c.NoContent(http.StatusNoContent)
	}
	var req struct {
		DraftID             string     `json:"draftId"`
		Text                *string    `json:"text"`
		CW                  *string    `json:"cw"`
		Visibility          string     `json:"visibility"`
		VisibleUserIDs      *[]string  `json:"visibleUserIds"`
		LocalOnly           *bool      `json:"localOnly"`
		ReactionAcceptance  *string    `json:"reactionAcceptance"`
		FileIDs             []string   `json:"fileIds"`
		ReplyID             *string    `json:"replyId"`
		RenoteID            *string    `json:"renoteId"`
		ChannelID           *string    `json:"channelId"`
		Hashtag             *string    `json:"hashtag"`
		Poll                *draftPoll `json:"poll"`
		ScheduledAt         *int64     `json:"scheduledAt"`
		IsActuallyScheduled *bool      `json:"isActuallyScheduled"`
	}
	if err := c.Bind(&req); err != nil || req.DraftID == "" {
		return c.JSON(http.StatusBadRequest, apierr.InvalidParam("draftId is required."))
	}
	// visibility / reactionAcceptance enum 検証 (drafts/create と共有、#1934)。"" /
	// nil は「変更しない」セマンティクスなので許容し、非空の不正値のみ弾く。paramDef
	// 相当なので draft 不在 (404) より先に 400 を返す。
	if err := validateNoteEnums(req.Visibility, req.ReactionAcceptance); err != nil {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", err.Error(), apierr.UUIDInvalidParam))
	}
	draft, err := h.draftRepo.FindByIDAndUser(req.DraftID, user.ID)
	if err != nil {
		return c.JSON(http.StatusNotFound, apierr.NoSuchNoteDraft())
	}
	if req.Text != nil {
		draft.Text = req.Text
	}
	if req.CW != nil {
		draft.CW = req.CW
	}
	if req.Visibility != "" {
		draft.Visibility = req.Visibility
	}
	if req.VisibleUserIDs != nil {
		draft.VisibleUserIDs = pq.StringArray(*req.VisibleUserIDs)
	}
	if req.LocalOnly != nil {
		draft.LocalOnly = *req.LocalOnly
	}
	if req.ReactionAcceptance != nil {
		draft.ReactionAcceptance = req.ReactionAcceptance
	}
	if req.FileIDs != nil {
		// fileIds 指定時は全て所有 file でなければ NO_SUCH_FILE。
		if !h.allDraftFilesOwned(req.FileIDs, user.ID) {
			return c.JSON(http.StatusBadRequest, apierr.Error("NO_SUCH_FILE", "Some files are not found.", "b6992544-63e7-67f0-fa7f-32444b1b5306"))
		}
		draft.FileIDs = pq.StringArray(req.FileIDs)
	}
	if req.ReplyID != nil {
		draft.ReplyID = req.ReplyID
	}
	if req.RenoteID != nil {
		draft.RenoteID = req.RenoteID
	}
	if req.ChannelID != nil {
		draft.ChannelID = req.ChannelID
	}
	if req.Hashtag != nil {
		draft.Hashtag = req.Hashtag
	}
	// scheduled 関連 field の変更検出 (#1045 Phase 2-C)。リクエストに
	// scheduledAt / isActuallyScheduled が含まれている場合のみ削除 + 再
	// enqueue する。それ以外は既存 draft 状態を維持。
	scheduleChanged := false
	now := time.Now()
	if req.Poll != nil {
		if pollAlreadyExpired(req.Poll, now) {
			return c.JSON(http.StatusBadRequest, apierr.Error("CANNOT_CREATE_ALREADY_EXPIRED_POLL", "Cannot create an already expired poll.", "04da457d-b083-4055-9082-955525eda5a5"))
		}
		applyDraftPoll(draft, req.Poll)
	}
	if req.ScheduledAt != nil {
		t := time.UnixMilli(*req.ScheduledAt)
		draft.ScheduledAt = &t
		scheduleChanged = true
	}
	if req.IsActuallyScheduled != nil {
		draft.IsActuallyScheduled = *req.IsActuallyScheduled
		scheduleChanged = true
	}
	if scheduleChanged && draft.IsActuallyScheduled {
		// 新しく schedule する / 既に scheduled だった draft の scheduledAt が
		// 変わった場合は validation + capability check が必要。
		if h.scheduledNoteEnqueuer != nil && !h.scheduledNoteEnqueuer.SupportsScheduledNote() {
			return c.JSON(http.StatusBadRequest, apierr.Error("TOO_MANY_SCHEDULED_NOTES", "You cannot create scheduled notes any more.", "02f5df79-08ae-4a33-8524-f1503c8f6212"))
		}
		if draft.ScheduledAt == nil {
			// notes/drafts/update は create とは別の SCHEDULED_AT_* id を持つ
			return c.JSON(http.StatusBadRequest, apierr.Error("SCHEDULED_AT_REQUIRED", "scheduledAt is required when isActuallyScheduled is true.", "fe9737d5-cc41-498c-af9d-149207307530"))
		}
		if !draft.ScheduledAt.After(now) {
			return c.JSON(http.StatusBadRequest, apierr.Error("SCHEDULED_AT_MUST_BE_IN_FUTURE", "scheduledAt must be in the future.", "ed1a6673-d0d1-4364-aaae-9bf3f139cbc5"))
		}
	}
	_ = h.draftRepo.Update(draft)
	// schedule 変更時は旧 delayed task を clear して、新 scheduledAt が
	// 立っていれば新 task を enqueue する。clear は best-effort で fail
	// しても draft 更新自体は成功で返す (= log のみ、frontend は draft
	// 更新の永続化を確認できる)。
	if scheduleChanged && h.scheduledNoteEnqueuer != nil {
		_ = h.scheduledNoteEnqueuer.ClearScheduledNote(draft.ID)
		if draft.IsActuallyScheduled && draft.ScheduledAt != nil {
			delay := draft.ScheduledAt.Sub(now)
			if err := h.scheduledNoteEnqueuer.EnqueuePostScheduledNote(
				queue.PostScheduledNotePayload{NoteDraftID: draft.ID},
				driver.WithProcessIn(delay),
			); err != nil {
				return apierr.JSONInternalError(c)
			}
		}
	}
	draft.User = user
	// upstream res は { updatedDraft: NoteDraft } で包む。
	return c.JSON(http.StatusOK, map[string]any{"updatedDraft": h.packDraft(draft, nil)})
}

// draftPoll mirrors the upstream notes/drafts poll param object.
type draftPoll struct {
	Choices      []string `json:"choices"`
	Multiple     bool     `json:"multiple"`
	ExpiresAt    *int64   `json:"expiresAt"`
	ExpiredAfter *int64   `json:"expiredAfter"`
}

// allDraftFilesOwned reports whether every fileID exists and is owned by
// userID (upstream NoteDraftService requires files.length === fileIds.length).
// Unwired driveFileRepo (tests) skips the check.
func (h *Handler) allDraftFilesOwned(fileIDs []string, userID string) bool {
	if len(fileIDs) == 0 || h.driveFileRepo == nil {
		return true
	}
	rows, err := h.driveFileRepo.FindByIDs(fileIDs)
	if err != nil {
		return false
	}
	owned := make(map[string]bool, len(rows))
	for _, f := range rows {
		if f.UserID != nil && *f.UserID == userID {
			owned[f.ID] = true
		}
	}
	for _, fid := range fileIDs {
		if !owned[fid] {
			return false
		}
	}
	return true
}

// pollAlreadyExpired reports whether the poll's expiresAt is in the past.
func pollAlreadyExpired(p *draftPoll, now time.Time) bool {
	return p != nil && p.ExpiresAt != nil && time.UnixMilli(*p.ExpiresAt).Before(now)
}

// applyDraftPoll writes poll fields into the draft. nil clears the poll
// (hasPoll=false); non-nil sets hasPoll=true + the choices/multiple/expiry.
func applyDraftPoll(d *model.NoteDraft, p *draftPoll) {
	if p == nil {
		d.HasPoll = false
		d.PollChoices = pq.StringArray{}
		d.PollMultiple = false
		d.PollExpiresAt = nil
		d.PollExpiredAfter = nil
		return
	}
	d.HasPoll = true
	d.PollChoices = pq.StringArray(p.Choices)
	d.PollMultiple = p.Multiple
	d.PollExpiredAfter = p.ExpiredAfter
	if p.ExpiresAt != nil {
		t := time.UnixMilli(*p.ExpiresAt)
		d.PollExpiresAt = &t
	} else {
		d.PollExpiresAt = nil
	}
}

// DraftsDelete handles POST /api/notes/drafts/delete.
func (h *Handler) DraftsDelete(c echo.Context) error {
	user := middleware.GetUser(c)
	if h.draftRepo == nil {
		return c.NoContent(http.StatusNoContent)
	}
	var req struct {
		DraftID string `json:"draftId"`
	}
	if err := c.Bind(&req); err != nil || req.DraftID == "" {
		return c.JSON(http.StatusBadRequest, apierr.InvalidParam("draftId is required."))
	}
	// upstream notes/drafts/delete.ts は存在チェックして無ければ
	// `NO_SUCH_NOTE_DRAFT` を返す。silent 204 だと frontend が削除確認 UI
	// で「該当 draft が無い」フィードバックを出せないので、本家挙動に合わせて
	// 404 を返すよう修正 (#688 review follow-up)。
	// Delete は RowsAffected を返すので、Find → Delete の 2 query 化を避け
	// つつ TOCTOU race も発生しない (DB 1 文で atomic に「あれば削除」)。
	rowsAffected, err := h.draftRepo.Delete(req.DraftID, user.ID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.InternalError())
	}
	if rowsAffected == 0 {
		return c.JSON(http.StatusNotFound, apierr.NoSuchNoteDraft())
	}
	// delete に成功したら旧 delayed task も best-effort で clear する
	// (#1045 Phase 2-C)。upstream は `clearSchedule(draftId)` を fire
	// and forget で呼ぶ。clear 失敗は 204 を阻害しない (= asynq retry
	// で task が fire してもprocessor 側で draft 不在 silent skip で済む)。
	if h.scheduledNoteEnqueuer != nil {
		_ = h.scheduledNoteEnqueuer.ClearScheduledNote(req.DraftID)
	}
	return c.NoContent(http.StatusNoContent)
}

// DraftsCount handles POST /api/notes/drafts/count.
func (h *Handler) DraftsCount(c echo.Context) error {
	user := middleware.GetUser(c)
	// upstream drafts/count.ts は res type:'number' で裸の数値を返す。旧実装は
	// {"count": N} オブジェクトを返していた (#1538)。
	if h.draftRepo == nil {
		return c.JSON(http.StatusOK, 0)
	}
	count, _ := h.draftRepo.CountByUser(user.ID)
	return c.JSON(http.StatusOK, count)
}

// SetThreadMutingRepo wires the NoteThreadMutingRepository used by the
// thread-muting create/delete endpoints (#1538). The notes/state read path is
// wired separately on the query service.
func (h *Handler) SetThreadMutingRepo(r repository.NoteThreadMutingRepository) {
	h.threadMutingRepo = r
}

// threadIDForMute resolves the thread key a mute row should use: the note's
// threadId when set, else the note id itself (mirrors upstream getNote +
// note.threadId ?? note.id).
func threadIDForMute(n *model.Note) string {
	if n.ThreadID != nil && *n.ThreadID != "" {
		return *n.ThreadID
	}
	return n.ID
}

// ThreadMutingCreate handles POST /api/notes/thread-muting/create.
//
// 旧実装は noteId 検証後 204 を返すだけで note_thread_muting に永続化しておらず
// (notes/state の isMutedThread 読み取り経路は実装済みなのに) スレッドミュートが
// 機能していなかった (#1538)。本家 thread-muting/create.ts 同様 note を解決し
// (可視性 gate 無し)、threadId(無ければ id)で冪等に muting 行を作る。
func (h *Handler) ThreadMutingCreate(c echo.Context) error {
	user := middleware.GetUser(c)
	var req struct {
		NoteID string `json:"noteId"`
	}
	if err := c.Bind(&req); err != nil || req.NoteID == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "noteId is required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	if h.threadMutingRepo == nil || h.noteRepo == nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	note, err := h.noteRepo.FindByID(req.NoteID)
	if err != nil || note == nil {
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_NOTE", "No such note.", "5ff67ada-ed3b-2e71-8e87-a1a421e177d2"))
	}
	threadID := threadIDForMute(note)
	// 冪等: 既存行があれば UNIQUE 制約違反 (500) を避けて成功扱い。
	if exists, _ := h.threadMutingRepo.Exists(user.ID, threadID); !exists {
		if err := h.threadMutingRepo.Create(&model.NoteThreadMuting{
			ID:       h.idGen.Generate(time.Now()),
			UserID:   user.ID,
			ThreadID: threadID,
		}); err != nil {
			return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
		}
	}
	return c.NoContent(http.StatusNoContent)
}

// ThreadMutingDelete handles POST /api/notes/thread-muting/delete.
func (h *Handler) ThreadMutingDelete(c echo.Context) error {
	user := middleware.GetUser(c)
	var req struct {
		NoteID string `json:"noteId"`
	}
	if err := c.Bind(&req); err != nil || req.NoteID == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "noteId is required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	if h.threadMutingRepo == nil || h.noteRepo == nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	note, err := h.noteRepo.FindByID(req.NoteID)
	if err != nil || note == nil {
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_NOTE", "No such note.", "bddd57ac-ceb3-b29d-4334-86ea5fae481a"))
	}
	// Delete は冪等 (該当行が無くてもエラーにしない)。
	if err := h.threadMutingRepo.Delete(user.ID, threadIDForMute(note)); err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	return c.NoContent(http.StatusNoContent)
}

// PollsRecommendation handles POST /api/notes/polls/recommendation.
//
// 旧実装は常に [] を返すスタブだった (#1538)。本家 recommendation.ts 同様、
// ローカル public で未期限切れの poll のうち、自分の投稿でない / 未投票 /
// 投稿者を mute していないものを noteId DESC で返す (excludeChannels で
// チャンネル poll を除外可)。可視性は public 限定なので追加 gate は不要。
func (h *Handler) PollsRecommendation(c echo.Context) error {
	user := middleware.GetUser(c)
	var req struct {
		Limit  int `json:"limit"`
		Offset int `json:"offset"`
		// upstream recommendation.ts:35 は excludeChannels を boolean で受け、
		// true のとき全 channel poll を除外する (#1765。以前は []string だった)。
		ExcludeChannels bool `json:"excludeChannels"`
	}
	_ = c.Bind(&req)
	if h.pollRepo == nil || h.noteRepo == nil {
		return c.JSON(http.StatusOK, []entity.NoteEntity{})
	}
	limit := pagination.ClampLimit(req.Limit, 10, 100)
	var muted []string
	if h.mutingRepo != nil {
		muted, _ = h.mutingRepo.ListMuteeIDs(user.ID)
	}
	noteIDs, err := h.pollRepo.ListRecommendation(user.ID, muted, req.ExcludeChannels, limit, req.Offset)
	if err != nil {
		return apierr.JSONInternalError(c)
	}
	if len(noteIDs) == 0 {
		return c.JSON(http.StatusOK, []entity.NoteEntity{})
	}
	notes, err := h.noteRepo.FindManyByIDsWithUser(noteIDs)
	if err != nil {
		return apierr.JSONInternalError(c)
	}
	// FindManyByIDsWithUser は noteIDs の順序 (noteId DESC) を保持する。
	return c.JSON(http.StatusOK, h.packMany(c.Request().Context(), notes, user))
}

// packDraft renders a NoteDraft into the upstream-compatible shape. 旧実装は
// text/cw/visibility/localOnly/fileIds しか出さず、reactionAcceptance /
// visibleUserIds / replyId / renoteId / channelId / hashtag / poll /
// scheduledAt / isActuallyScheduled / user / files を欠いていた。
//
// reply / renote / channel の解決済みオブジェクトは現状 null で返す (ID は
// 出すので frontend は再取得可能)。files は driveFileRepo から解決する。
// packDraft packs a draft. fileByID, when non-nil, is a pre-resolved DriveFile
// map (List uses it to batch all drafts' files in one query, avoiding N+1).
// When nil, files are resolved per-draft (single create/update/show path).
func (h *Handler) packDraft(d *model.NoteDraft, fileByID map[string]entity.DriveFileEntity) map[string]any {
	const tsFmt = "2006-01-02T15:04:05.000Z"
	visibleUserIDs := []string(d.VisibleUserIDs)
	if visibleUserIDs == nil {
		visibleUserIDs = []string{}
	}
	fileIDs := []string(d.FileIDs)
	if fileIDs == nil {
		fileIDs = []string{}
	}
	result := map[string]any{
		"id":                  d.ID,
		"userId":              d.UserID,
		"text":                d.Text,
		"cw":                  d.CW,
		"visibility":          d.Visibility,
		"localOnly":           d.LocalOnly,
		"reactionAcceptance":  d.ReactionAcceptance,
		"visibleUserIds":      visibleUserIDs,
		"fileIds":             fileIDs,
		"replyId":             d.ReplyID,
		"renoteId":            d.RenoteID,
		"channelId":           d.ChannelID,
		"hashtag":             d.Hashtag,
		"isActuallyScheduled": d.IsActuallyScheduled,
		// 解決済みオブジェクトは未対応 (ID は上で返す)。
		"reply":   nil,
		"renote":  nil,
		"channel": nil,
		"files":   h.draftFiles(fileIDs, fileByID),
	}
	if t, err := h.idGen.ParseTime(d.ID); err == nil {
		result["createdAt"] = t.UTC().Format(tsFmt)
	}
	// upstream schema: scheduledAt は number (ms epoch)。createdAt は ISO 文字列
	// だが scheduledAt は getTime() なので number で返す。
	if d.ScheduledAt != nil {
		result["scheduledAt"] = d.ScheduledAt.UnixMilli()
	} else {
		result["scheduledAt"] = nil
	}
	// poll: hasPoll のときだけ object、それ以外は null (upstream schema)。
	if d.HasPoll {
		choices := []string(d.PollChoices)
		if choices == nil {
			choices = []string{}
		}
		poll := map[string]any{
			"choices":      choices,
			"multiple":     d.PollMultiple,
			"expiredAfter": d.PollExpiredAfter,
		}
		if d.PollExpiresAt != nil {
			poll["expiresAt"] = d.PollExpiresAt.UTC().Format(tsFmt)
		} else {
			poll["expiresAt"] = nil
		}
		result["poll"] = poll
	} else {
		result["poll"] = nil
	}
	if d.User != nil {
		result["user"] = entity.PackUserLite(d.User)
	}
	return result
}

// draftFiles resolves a draft's fileIds to packed DriveFiles in order. When
// fileByID is provided (List batch path) it is used directly; otherwise a
// per-draft FindByIDs query runs. Returns a non-nil slice ([] when empty).
func (h *Handler) draftFiles(fileIDs []string, fileByID map[string]entity.DriveFileEntity) []entity.DriveFileEntity {
	out := make([]entity.DriveFileEntity, 0, len(fileIDs))
	if len(fileIDs) == 0 {
		return out
	}
	if fileByID == nil {
		fileByID = h.resolveDraftFileMap(fileIDs)
	}
	for _, fid := range fileIDs {
		if f, ok := fileByID[fid]; ok {
			out = append(out, f)
		}
	}
	return out
}

// resolveDraftFileMap fetches+packs DriveFiles for the given ids into a map.
func (h *Handler) resolveDraftFileMap(fileIDs []string) map[string]entity.DriveFileEntity {
	m := map[string]entity.DriveFileEntity{}
	if len(fileIDs) == 0 || h.driveFileRepo == nil {
		return m
	}
	rows, err := h.driveFileRepo.FindByIDs(fileIDs)
	if err != nil {
		return m
	}
	for _, f := range rows {
		m[f.ID] = entity.PackDriveFile(f, h.idGen)
	}
	return m
}
