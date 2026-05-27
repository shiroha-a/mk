// Package clips provides /api/clips/* endpoints.
package clips

import (
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/api/apierr"
	"github.com/shiroha-a/mk/internal/api/pagination"
	coreclip "github.com/shiroha-a/mk/internal/core/clip"
	"github.com/shiroha-a/mk/internal/core/notesfilter"
	"github.com/shiroha-a/mk/internal/entity"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
	"github.com/shiroha-a/mk/internal/server/middleware"
)

// Handler handles clip-related API endpoints.
type Handler struct {
	svc          *coreclip.Service
	idGen        id.Generator
	favoriteRepo ClipFavoriteRepository
	instanceRepo repository.InstanceRepository
	emojiRepo    repository.EmojiRepository
	bufReader    entity.BufferedReactionsReader
	fieldRes     *entity.NoteFieldResolver
	// userRepo は clips/notes 経由で他人の clip を閲覧する際の hardMutedWords
	// filter (#787) のために viewer profile を引く。未配線時は filter skip。
	userRepo repository.UserRepository
}

// SetUserRepo wires a UserRepository so clips/notes filters out notes that
// match the viewer's hardMutedWords (#787).
func (h *Handler) SetUserRepo(r repository.UserRepository) {
	h.userRepo = r
}

// SetNoteFieldResolver wires the shared resolver that fills Files /
// MyReaction / Channel on packed notes (#426)。
func (h *Handler) SetNoteFieldResolver(r *entity.NoteFieldResolver) {
	h.fieldRes = r
}

// SetInstanceRepo attaches an InstanceRepository so clips/notes populates
// UserLite.Instance for remote users (#277).
func (h *Handler) SetInstanceRepo(r repository.InstanceRepository) {
	h.instanceRepo = r
}

func (h *Handler) instanceLookup() entity.InstanceLookup {
	if h.instanceRepo == nil {
		return nil
	}
	return h.instanceRepo
}

// SetEmojiRepo attaches an EmojiRepository so custom emoji shortcodes in
// note text and user displayNames get resolved to URLs.
func (h *Handler) SetEmojiRepo(r repository.EmojiRepository) {
	h.emojiRepo = r
}

// SetReactionReader wires a BufferedReactionsReader so PackNote / PackNotes
// can merge in-flight buffered reaction deltas (#647)。
func (h *Handler) SetReactionReader(r entity.BufferedReactionsReader) {
	h.bufReader = r
}

func (h *Handler) reactionReader() entity.BufferedReactionsReader {
	return h.bufReader
}

func (h *Handler) emojiLookup() entity.EmojiLookup {
	if h.emojiRepo == nil {
		return nil
	}
	return h.emojiRepo
}

// NewHandler creates a new clips Handler.
func NewHandler(svc *coreclip.Service, idGen id.Generator) *Handler {
	return &Handler{svc: svc, idGen: idGen}
}

// CreateRequest is the request body for clips/create.
type CreateRequest struct {
	Name        string  `json:"name"`
	Description *string `json:"description"`
	IsPublic    bool    `json:"isPublic"`
}

// Create handles POST /api/clips/create.
func (h *Handler) Create(c echo.Context) error {
	user := middleware.GetUser(c)
	var req CreateRequest
	if err := c.Bind(&req); err != nil || req.Name == "" {
		return apierr.JSONInvalidParam(c)
	}
	cl, err := h.svc.Create(coreclip.CreateInput{
		OwnerID:     user.ID,
		Name:        req.Name,
		Description: req.Description,
		IsPublic:    req.IsPublic,
	})
	if err != nil {
		if errors.Is(err, coreclip.ErrTooManyClips) {
			return apierr.JSONTooManyClips(c)
		}
		return apierr.JSONInternalError(c)
	}
	return c.JSON(http.StatusOK, h.clipToMap(cl))
}

// ShowRequest is the request body for clips/show.
type ShowRequest struct {
	ClipID string `json:"clipId"`
}

// Show handles POST /api/clips/show.
func (h *Handler) Show(c echo.Context) error {
	user := middleware.GetUser(c)
	var req ShowRequest
	if err := c.Bind(&req); err != nil || req.ClipID == "" {
		return apierr.JSONInvalidParam(c)
	}
	requesterID := ""
	if user != nil {
		requesterID = user.ID
	}
	cl, err := h.svc.Show(requesterID, req.ClipID)
	if err != nil {
		if errors.Is(err, coreclip.ErrAccessDenied) {
			return apierr.JSONAccessDenied(c)
		}
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_CLIP", "No such clip.", "c3c5fe33-d62c-44d2-9ea5-d997703f5c20"))
	}
	return c.JSON(http.StatusOK, h.clipToMap(cl))
}

// UpdateRequest is the request body for clips/update.
type UpdateRequest struct {
	ClipID      string  `json:"clipId"`
	Name        *string `json:"name"`
	Description *string `json:"description"`
	IsPublic    *bool   `json:"isPublic"`
}

// Update handles POST /api/clips/update.
func (h *Handler) Update(c echo.Context) error {
	user := middleware.GetUser(c)
	var req UpdateRequest
	if err := c.Bind(&req); err != nil || req.ClipID == "" {
		return apierr.JSONInvalidParam(c)
	}
	in := coreclip.UpdateInput{
		Name:     req.Name,
		IsPublic: req.IsPublic,
	}
	if req.Description != nil {
		desc := req.Description
		in.Description = &desc
	}
	cl, err := h.svc.Update(user.ID, req.ClipID, in)
	if err != nil {
		switch {
		case errors.Is(err, coreclip.ErrClipNotFound):
			return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_CLIP", "No such clip.", "b4d92d70-b216-46fa-9a3f-a8c811699257"))
		case errors.Is(err, coreclip.ErrAccessDenied):
			return apierr.JSONAccessDenied(c)
		case errors.Is(err, coreclip.ErrClipNameRequired):
			return apierr.JSONInvalidParam(c)
		}
		return apierr.JSONInternalError(c)
	}
	return c.JSON(http.StatusOK, h.clipToMap(cl))
}

// DeleteRequest is the request body for clips/delete.
type DeleteRequest struct {
	ClipID string `json:"clipId"`
}

// Delete handles POST /api/clips/delete.
func (h *Handler) Delete(c echo.Context) error {
	user := middleware.GetUser(c)
	var req DeleteRequest
	if err := c.Bind(&req); err != nil || req.ClipID == "" {
		return apierr.JSONInvalidParam(c)
	}
	if err := h.svc.Delete(user.ID, req.ClipID); err != nil {
		switch {
		case errors.Is(err, coreclip.ErrClipNotFound):
			return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_CLIP", "No such clip.", "70ca08ba-6865-4630-b6fb-8494759aa754"))
		case errors.Is(err, coreclip.ErrAccessDenied):
			return apierr.JSONAccessDenied(c)
		}
		return apierr.JSONInternalError(c)
	}
	return c.NoContent(http.StatusNoContent)
}

// ListRequest is the request body for clips/list.
type ListRequest struct {
	Limit     int    `json:"limit"`
	Offset    int    `json:"offset"`
	SinceID   string `json:"sinceId"`
	UntilID   string `json:"untilId"`
	SinceDate *int64 `json:"sinceDate"`
	UntilDate *int64 `json:"untilDate"`
}

// List handles POST /api/clips/list.
//
// frontend Paginator は cursor mode (untilId / sinceId) で叩いてくる。
// upstream Misskey TS と同じく cursor 指定時は offset を無視する。
func (h *Handler) List(c echo.Context) error {
	user := middleware.GetUser(c)
	var req ListRequest
	if err := c.Bind(&req); err != nil {
		return apierr.JSONInvalidParam(c)
	}
	// sinceDate / untilDate を aidx prefix に正規化 (#1166)。
	sinceID, untilID := id.NormalizeCursor(req.SinceID, req.UntilID, req.SinceDate, req.UntilDate)
	req.Limit = pagination.ClampLimit(req.Limit, 10, 100)
	rows, err := h.svc.ListByUser(user.ID, sinceID, untilID, req.Limit, req.Offset)
	if err != nil {
		return apierr.JSONInternalError(c)
	}
	out := make([]map[string]any, 0, len(rows))
	for _, cl := range rows {
		out = append(out, h.clipToMap(cl))
	}
	return c.JSON(http.StatusOK, out)
}

// AddNoteRequest is the request body for clips/add-note.
type AddNoteRequest struct {
	ClipID string `json:"clipId"`
	NoteID string `json:"noteId"`
}

// AddNote handles POST /api/clips/add-note.
func (h *Handler) AddNote(c echo.Context) error {
	user := middleware.GetUser(c)
	var req AddNoteRequest
	if err := c.Bind(&req); err != nil || req.ClipID == "" || req.NoteID == "" {
		return apierr.JSONInvalidParam(c)
	}
	if err := h.svc.AddNote(user.ID, req.ClipID, req.NoteID); err != nil {
		switch {
		case errors.Is(err, coreclip.ErrClipNotFound):
			return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_CLIP", "No such clip.", "d6e76cc0-a1b5-4c7c-a287-73fa9c716dcf"))
		case errors.Is(err, coreclip.ErrAccessDenied):
			return apierr.JSONAccessDenied(c)
		case errors.Is(err, coreclip.ErrNoteNotFound):
			return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_NOTE", "No such note.", "fc8c0b49-c7a3-4664-a0a6-b418d386bb8b"))
		case errors.Is(err, coreclip.ErrAlreadyClipped):
			return c.JSON(http.StatusBadRequest, apierr.Error("ALREADY_CLIPPED", "The note has already been clipped.", "734806c4-542c-463a-9311-15c512803965"))
		case errors.Is(err, coreclip.ErrTooManyClipNotes):
			return apierr.JSONTooManyClipNotes(c)
		}
		return apierr.JSONInternalError(c)
	}
	return c.NoContent(http.StatusNoContent)
}

// RemoveNoteRequest is the request body for clips/remove-note.
type RemoveNoteRequest struct {
	ClipID string `json:"clipId"`
	NoteID string `json:"noteId"`
}

// RemoveNote handles POST /api/clips/remove-note.
func (h *Handler) RemoveNote(c echo.Context) error {
	user := middleware.GetUser(c)
	var req RemoveNoteRequest
	if err := c.Bind(&req); err != nil || req.ClipID == "" || req.NoteID == "" {
		return apierr.JSONInvalidParam(c)
	}
	if err := h.svc.RemoveNote(user.ID, req.ClipID, req.NoteID); err != nil {
		switch {
		case errors.Is(err, coreclip.ErrClipNotFound):
			return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_CLIP", "No such clip.", "b80525c6-97f7-49d7-a42d-ebccd49cfd52"))
		case errors.Is(err, coreclip.ErrAccessDenied):
			return apierr.JSONAccessDenied(c)
		case errors.Is(err, coreclip.ErrNotClipped):
			return notClipped(c)
		}
		return apierr.JSONInternalError(c)
	}
	return c.NoContent(http.StatusNoContent)
}

// NotesRequest is the request body for clips/notes.
type NotesRequest struct {
	ClipID    string `json:"clipId"`
	UntilID   string `json:"untilId"`
	SinceID   string `json:"sinceId"`
	SinceDate *int64 `json:"sinceDate"`
	UntilDate *int64 `json:"untilDate"`
	Limit     int    `json:"limit"`
}

// Notes handles POST /api/clips/notes.
func (h *Handler) Notes(c echo.Context) error {
	user := middleware.GetUser(c)
	var req NotesRequest
	if err := c.Bind(&req); err != nil || req.ClipID == "" {
		return apierr.JSONInvalidParam(c)
	}
	requesterID := ""
	if user != nil {
		requesterID = user.ID
	}
	// sinceDate / untilDate を aidx prefix に正規化 (#1166)。
	sinceID, untilID := id.NormalizeCursor(req.SinceID, req.UntilID, req.SinceDate, req.UntilDate)
	req.Limit = pagination.ClampLimit(req.Limit, 10, 100)
	notes, err := h.svc.Notes(requesterID, req.ClipID, untilID, sinceID, req.Limit)
	if err != nil {
		switch {
		case errors.Is(err, coreclip.ErrClipNotFound):
			return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_CLIP", "No such clip.", "1d7645e6-2b6d-4635-b0fe-fe22b0e72e00"))
		case errors.Is(err, coreclip.ErrAccessDenied):
			return apierr.JSONAccessDenied(c)
		}
		return apierr.JSONInternalError(c)
	}
	notes = notesfilter.ApplyHardMute(h.userRepo, user, notes)
	entities := entity.PackNotes(c.Request().Context(), notes, h.idGen, h.instanceLookup(), h.emojiLookup(), h.reactionReader())
	h.fieldRes.Apply(entities, user)
	out := make([]any, 0, len(entities))
	for _, pn := range entities {
		out = append(out, pn)
	}
	return c.JSON(http.StatusOK, out)
}

// clipToMap packs a clip into the misskey_dart-compatible shape via
// entity.PackClip. owner (clip の所有ユーザー) を userRepo から解決して user
// field を埋める。misskey_dart の Clip.fromJson は createdAt / user /
// favoritedCount を非null必須とするため、旧 ad-hoc map では落ちていた (#1245)。
func (h *Handler) clipToMap(cl *model.Clip) map[string]any {
	var owner *model.User
	if h.userRepo != nil {
		owner, _ = h.userRepo.FindByID(cl.UserID)
	}
	return entity.PackClip(cl, h.idGen, owner)
}

func notClipped(c echo.Context) error {
	return c.JSON(http.StatusBadRequest, apierr.Error("NOT_CLIPPED", "The note is not clipped.", "aff017de-190e-434b-893e-33a9ff5049d8"))
}
