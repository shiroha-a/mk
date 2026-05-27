package notes

import (
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/api/apierr"
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
	drafts, err := h.draftRepo.ListByUser(user.ID, 20)
	if err != nil {
		return c.JSON(http.StatusOK, []any{})
	}
	out := make([]map[string]any, len(drafts))
	for i, d := range drafts {
		out[i] = packDraft(d, h.idGen)
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
		Text                *string  `json:"text"`
		CW                  *string  `json:"cw"`
		Visibility          string   `json:"visibility"`
		FileIDs             []string `json:"fileIds"`
		ScheduledAt         *int64   `json:"scheduledAt"`
		IsActuallyScheduled bool     `json:"isActuallyScheduled"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierr.InvalidParam("Invalid parameters."))
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
	draft := &model.NoteDraft{
		ID:                  h.idGen.Generate(now),
		UserID:              user.ID,
		Text:                req.Text,
		CW:                  req.CW,
		Visibility:          req.Visibility,
		FileIDs:             req.FileIDs,
		ScheduledAt:         scheduledAt,
		IsActuallyScheduled: req.IsActuallyScheduled,
	}
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
	return c.JSON(http.StatusOK, packDraft(draft, h.idGen))
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
		DraftID             string   `json:"draftId"`
		Text                *string  `json:"text"`
		CW                  *string  `json:"cw"`
		Visibility          string   `json:"visibility"`
		FileIDs             []string `json:"fileIds"`
		ScheduledAt         *int64   `json:"scheduledAt"`
		IsActuallyScheduled *bool    `json:"isActuallyScheduled"`
	}
	if err := c.Bind(&req); err != nil || req.DraftID == "" {
		return c.JSON(http.StatusBadRequest, apierr.InvalidParam("draftId is required."))
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
	if req.FileIDs != nil {
		draft.FileIDs = req.FileIDs
	}
	// scheduled 関連 field の変更検出 (#1045 Phase 2-C)。リクエストに
	// scheduledAt / isActuallyScheduled が含まれている場合のみ削除 + 再
	// enqueue する。それ以外は既存 draft 状態を維持。
	scheduleChanged := false
	now := time.Now()
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
	return c.JSON(http.StatusOK, packDraft(draft, h.idGen))
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
	if h.draftRepo == nil {
		return c.JSON(http.StatusOK, map[string]any{"count": 0})
	}
	count, _ := h.draftRepo.CountByUser(user.ID)
	return c.JSON(http.StatusOK, map[string]any{"count": count})
}

// ThreadMutingCreate handles POST /api/notes/thread-muting/create.
func (h *Handler) ThreadMutingCreate(c echo.Context) error {
	var req struct {
		NoteID string `json:"noteId"`
	}
	if err := c.Bind(&req); err != nil || req.NoteID == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "noteId is required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	return c.NoContent(http.StatusNoContent)
}

// ThreadMutingDelete handles POST /api/notes/thread-muting/delete.
func (h *Handler) ThreadMutingDelete(c echo.Context) error {
	var req struct {
		NoteID string `json:"noteId"`
	}
	if err := c.Bind(&req); err != nil || req.NoteID == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "noteId is required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	return c.NoContent(http.StatusNoContent)
}

// PollsRecommendation handles POST /api/notes/polls/recommendation.
func (h *Handler) PollsRecommendation(c echo.Context) error {
	return c.JSON(http.StatusOK, []any{})
}

func packDraft(d *model.NoteDraft, idGen interface {
	ParseTime(string) (time.Time, error)
}) map[string]any {
	result := map[string]any{
		"id":         d.ID,
		"userId":     d.UserID,
		"text":       d.Text,
		"cw":         d.CW,
		"visibility": d.Visibility,
		"localOnly":  d.LocalOnly,
		"fileIds":    d.FileIDs,
	}
	if t, err := idGen.ParseTime(d.ID); err == nil {
		result["createdAt"] = t.UTC().Format("2006-01-02T15:04:05.000Z")
	}
	return result
}
