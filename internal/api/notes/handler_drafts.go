package notes

import (
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/api/apierr"
	"github.com/shiroha-a/mk/internal/model"
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
func (h *Handler) DraftsCreate(c echo.Context) error {
	user := middleware.GetUser(c)
	if h.draftRepo == nil {
		return c.NoContent(http.StatusNoContent)
	}
	var req struct {
		Text       *string  `json:"text"`
		CW         *string  `json:"cw"`
		Visibility string   `json:"visibility"`
		FileIDs    []string `json:"fileIds"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierr.InvalidParam("Invalid parameters."))
	}
	if req.Visibility == "" {
		req.Visibility = "public"
	}
	// noteDraftLimit role policy gate (#1029)。policyProvider は #1026 で
	// 配線済 (timeline gate と共用)。CountByUser で current 件数を取得。
	if h.policyProvider != nil {
		if limit, ok := h.policyProvider.GetUserPolicies(user.ID)["noteDraftLimit"].(int); ok && limit >= 0 {
			count, err := h.draftRepo.CountByUser(user.ID)
			if err != nil {
				return apierr.JSONInternalError(c)
			}
			if int(count) >= limit {
				return apierr.JSONTooManyNoteDrafts(c)
			}
		}
	}
	draft := &model.NoteDraft{
		ID:         h.idGen.Generate(time.Now()),
		UserID:     user.ID,
		Text:       req.Text,
		CW:         req.CW,
		Visibility: req.Visibility,
		FileIDs:    req.FileIDs,
	}
	if err := h.draftRepo.Create(draft); err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.InternalError())
	}
	return c.JSON(http.StatusOK, packDraft(draft, h.idGen))
}

// DraftsUpdate handles POST /api/notes/drafts/update.
func (h *Handler) DraftsUpdate(c echo.Context) error {
	user := middleware.GetUser(c)
	if h.draftRepo == nil {
		return c.NoContent(http.StatusNoContent)
	}
	var req struct {
		DraftID    string   `json:"draftId"`
		Text       *string  `json:"text"`
		CW         *string  `json:"cw"`
		Visibility string   `json:"visibility"`
		FileIDs    []string `json:"fileIds"`
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
	_ = h.draftRepo.Update(draft)
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
