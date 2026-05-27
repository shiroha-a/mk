package notes

import (
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/api/apierr"
	"github.com/shiroha-a/mk/internal/api/pagination"
	"github.com/shiroha-a/mk/internal/core/reaction"
	"github.com/shiroha-a/mk/internal/entity"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/server/middleware"
)

// ReactionCreateRequest is the request body for notes/reactions/create.
type ReactionCreateRequest struct {
	NoteID   string `json:"noteId"`
	Reaction string `json:"reaction"`
}

// ReactionsCreate handles POST /api/notes/reactions/create.
func (h *Handler) ReactionsCreate(c echo.Context) error {
	user := middleware.GetUser(c)

	var req ReactionCreateRequest
	if err := c.Bind(&req); err != nil || req.NoteID == "" {
		return apierr.JSONInvalidParam(c)
	}

	_, err := h.reactionService.Create(user, req.NoteID, req.Reaction)
	if err != nil {
		switch {
		case errors.Is(err, reaction.ErrNoteNotFound):
			return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_NOTE", "No such note.", "033d0620-5bfe-4027-965d-980b0c85a3ea"))
		case errors.Is(err, reaction.ErrNoteNotVisible):
			return c.JSON(http.StatusForbidden, apierr.Error("ACCESS_DENIED", "You can not see this note.", "fe8d7103-0ea8-4ec3-814d-f8b401dc69e9"))
		case errors.Is(err, reaction.ErrAlreadyReacted):
			return c.JSON(http.StatusBadRequest, apierr.Error("ALREADY_REACTED", "You are already reacting to that note.", "71efcf98-86d6-4e2b-b2ad-9d032369366b"))
		case errors.Is(err, reaction.ErrCannotReactToPureRenote):
			return c.JSON(http.StatusBadRequest, apierr.Error("CANNOT_REACT_TO_RENOTE", "You can not react to a pure Renote.", "eaccdc08-ddef-43fe-908f-d108faad57f5"))
		case errors.Is(err, reaction.ErrBlocked):
			return c.JSON(http.StatusForbidden, apierr.Error("BLOCKED", "You are blocked by that user.", "e70412a4-7197-4726-8e74-f3e0deb92aa7"))
		}
		return apierr.JSONInternalError(c)
	}
	return c.NoContent(http.StatusNoContent)
}

// ReactionDeleteRequest is the request body for notes/reactions/delete.
type ReactionDeleteRequest struct {
	NoteID string `json:"noteId"`
}

// ReactionsDelete handles POST /api/notes/reactions/delete.
func (h *Handler) ReactionsDelete(c echo.Context) error {
	user := middleware.GetUser(c)

	var req ReactionDeleteRequest
	if err := c.Bind(&req); err != nil || req.NoteID == "" {
		return apierr.JSONInvalidParam(c)
	}

	if err := h.reactionService.Delete(user, req.NoteID); err != nil {
		switch {
		case errors.Is(err, reaction.ErrNoteNotFound):
			return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_NOTE", "No such note.", "764d9fce-f9f2-4a0e-92b1-6ceac9a7ad37"))
		case errors.Is(err, reaction.ErrReactionNotFound):
			return c.JSON(http.StatusNotFound, apierr.Error("NOT_REACTED", "You are not reacting to that note.", "92f4426d-4196-4125-aa5b-02943e2ec8fc"))
		}
		return apierr.JSONInternalError(c)
	}
	return c.NoContent(http.StatusNoContent)
}

// ReactionsListRequest is the request body for notes/reactions.
type ReactionsListRequest struct {
	NoteID    string `json:"noteId" query:"noteId"`
	Type      string `json:"type" query:"type"`
	Limit     int    `json:"limit" query:"limit"`
	SinceID   string `json:"sinceId" query:"sinceId"`
	UntilID   string `json:"untilId" query:"untilId"`
	SinceDate *int64 `json:"sinceDate" query:"sinceDate"`
	UntilDate *int64 `json:"untilDate" query:"untilDate"`
}

// Reactions handles POST /api/notes/reactions.
func (h *Handler) Reactions(c echo.Context) error {
	var req ReactionsListRequest
	if err := c.Bind(&req); err != nil || req.NoteID == "" {
		return apierr.JSONInvalidParam(c)
	}
	req.Limit = pagination.ClampLimit(req.Limit, 10, 100)

	// sinceDate / untilDate を aidx prefix に正規化 (#1166)。
	sinceID, untilID := id.NormalizeCursor(req.SinceID, req.UntilID, req.SinceDate, req.UntilDate)
	viewer := middleware.GetUser(c)
	rows, err := h.reactionService.List(viewer, req.NoteID, untilID, sinceID, req.Limit, req.Type)
	if err != nil {
		switch {
		case errors.Is(err, reaction.ErrNoteNotFound), errors.Is(err, reaction.ErrNoteNotVisible):
			return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_NOTE", "No such note.", "263fff3d-d0e1-4af4-bea7-8408059b451a"))
		}
		return apierr.JSONInternalError(c)
	}

	// リモート user の instance を 1 回の batch fetch で resolve する。
	reactionUsers := make([]*model.User, 0, len(rows))
	for _, r := range rows {
		if r.User != nil {
			reactionUsers = append(reactionUsers, r.User)
		}
	}
	resolver := entity.NewInstanceResolver(h.instanceLookup(), reactionUsers...)

	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		createdAt := ""
		if t, err := h.idGen.ParseTime(r.ID); err == nil {
			createdAt = t.UTC().Format("2006-01-02T15:04:05.000Z")
		}
		var userField any = map[string]any{"id": r.UserID}
		if r.User != nil {
			lite := entity.PackUserLite(r.User)
			resolver.FillUserLite(&lite)
			userField = lite
		}
		out = append(out, map[string]any{
			"id":        r.ID,
			"createdAt": createdAt,
			"user":      userField,
			"type":      r.Reaction,
		})
	}
	return c.JSON(http.StatusOK, out)
}
