package roles

import (
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/api/apierr"
	"github.com/shiroha-a/mk/internal/core/notesfilter"
	"github.com/shiroha-a/mk/internal/core/role"
	"github.com/shiroha-a/mk/internal/entity"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
	"github.com/shiroha-a/mk/internal/server/middleware"
)

// RoleNotesQuery provides a way to fetch notes by role.
type RoleNotesQuery interface {
	ListByRole(roleID string, limit int, sinceID, untilID string) ([]*model.Note, error)
}

// Handler handles public role API endpoints.
type Handler struct {
	roleService  *role.Service
	notesQuery   RoleNotesQuery
	idGen        id.Generator
	instanceRepo repository.InstanceRepository
	emojiRepo    repository.EmojiRepository
	bufReader    entity.BufferedReactionsReader
	fieldRes     *entity.NoteFieldResolver
	// userRepo は roles/notes の hardMutedWords filter (#787)。
	userRepo repository.UserRepository
}

// SetUserRepo wires a UserRepository so roles/notes filters out notes that
// match the viewer's hardMutedWords (#787).
func (h *Handler) SetUserRepo(r repository.UserRepository) {
	h.userRepo = r
}

// SetNoteFieldResolver wires the shared resolver that fills Files /
// MyReaction / Channel on packed notes (#426)。
func (h *Handler) SetNoteFieldResolver(r *entity.NoteFieldResolver) {
	h.fieldRes = r
}

// NewHandler creates a new roles Handler. idGen is required for note packing
// under /api/roles/notes.
func NewHandler(roleService *role.Service, idGen id.Generator) *Handler {
	return &Handler{roleService: roleService, idGen: idGen}
}

// SetNotesQuery attaches a RoleNotesQuery for the roles/notes endpoint.
func (h *Handler) SetNotesQuery(q RoleNotesQuery) {
	h.notesQuery = q
}

// SetInstanceRepo attaches an InstanceRepository so roles/notes populates
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

// List handles POST /api/roles/list.
func (h *Handler) List(c echo.Context) error {
	roles, err := h.roleService.List()
	if err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	var result []any
	for _, r := range roles {
		if r.IsPublic {
			result = append(result, packRole(r))
		}
	}
	if result == nil {
		result = []any{}
	}
	return c.JSON(http.StatusOK, result)
}

// Show handles POST /api/roles/show.
func (h *Handler) Show(c echo.Context) error {
	var req struct {
		RoleID string `json:"roleId"`
	}
	if err := c.Bind(&req); err != nil || req.RoleID == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "roleId is required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	r, err := h.roleService.Show(req.RoleID)
	if err != nil || !r.IsPublic {
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_ROLE", "No such role.", "07dc7d34-c0d8-458b-9c04-4b18399b1f46"))
	}
	return c.JSON(http.StatusOK, packRole(r))
}

// Users handles POST /api/roles/users.
func (h *Handler) Users(c echo.Context) error {
	var req struct {
		RoleID string `json:"roleId"`
	}
	if err := c.Bind(&req); err != nil || req.RoleID == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "roleId is required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	if _, err := h.roleService.Show(req.RoleID); err != nil {
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_ROLE", "No such role.", "07dc7d34-c0d8-458b-9c04-4b18399b1f46"))
	}
	return c.JSON(http.StatusOK, []any{})
}

// Notes handles POST /api/roles/notes.
func (h *Handler) Notes(c echo.Context) error {
	var req struct {
		RoleID  string `json:"roleId"`
		Limit   int    `json:"limit"`
		SinceID string `json:"sinceId"`
		UntilID string `json:"untilId"`
	}
	if err := c.Bind(&req); err != nil || req.RoleID == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "roleId is required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}

	r, err := h.roleService.Show(req.RoleID)
	if err != nil {
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_ROLE", "No such role.", "07dc7d34-c0d8-458b-9c04-4b18399b1f46"))
	}
	if !r.IsPublic {
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_ROLE", "No such role.", "07dc7d34-c0d8-458b-9c04-4b18399b1f46"))
	}

	if h.notesQuery == nil {
		return c.JSON(http.StatusOK, []any{})
	}

	limit := req.Limit
	if limit <= 0 {
		limit = 10
	}
	if limit > 100 {
		limit = 100
	}

	notes, err := h.notesQuery.ListByRole(req.RoleID, limit, req.SinceID, req.UntilID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}

	viewer := middleware.GetUser(c)
	notes = notesfilter.ApplyHardMute(h.userRepo, viewer, notes)
	entities := entity.PackNotes(c.Request().Context(), notes, h.idGen, h.instanceLookup(), h.emojiLookup(), h.reactionReader())
	h.fieldRes.Apply(entities, viewer)
	out := make([]any, 0, len(entities))
	for _, pn := range entities {
		out = append(out, pn)
	}
	return c.JSON(http.StatusOK, out)
}

func packRole(r *model.Role) map[string]any {
	return map[string]any{
		"id":              r.ID,
		"name":            r.Name,
		"color":           r.Color,
		"iconUrl":         r.IconURL,
		"description":     r.Description,
		"isModerator":     r.IsModerator,
		"isAdministrator": r.IsAdministrator,
		"isPublic":        r.IsPublic,
		"isExplorable":    r.IsExplorable,
		"asBadge":         r.AsBadge,
		"displayOrder":    r.DisplayOrder,
	}
}
