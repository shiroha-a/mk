package admin

import (
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/lib/pq"

	"github.com/shiroha-a/mk/internal/api/apierr"
	"github.com/shiroha-a/mk/internal/core/moderationlog"
	"github.com/shiroha-a/mk/internal/model"
)

// AvatarDecorationsCreate handles POST /api/admin/avatar-decorations/create.
func (h *Handler) AvatarDecorationsCreate(c echo.Context) error {
	if h.avatarDecoRepo == nil {
		return c.NoContent(http.StatusNoContent)
	}
	var req struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		URL         string   `json:"url"`
		RoleIDs     []string `json:"roleIdsThatCanBeUsedThisDecoration"`
	}
	if err := c.Bind(&req); err != nil || req.Name == "" || req.URL == "" {
		return c.JSON(http.StatusBadRequest, apierr.InvalidParam("Invalid parameters."))
	}
	d := &model.AvatarDecoration{
		ID:          h.idGen.Generate(time.Now()),
		Name:        req.Name,
		Description: req.Description,
		URL:         req.URL,
		RoleIDs:     req.RoleIDs,
	}
	if err := h.avatarDecoRepo.Create(d); err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.InternalError())
	}
	h.logModeration(c, moderationlog.LogCreateAvatarDecoration, map[string]any{
		"avatarDecorationId": d.ID,
		"avatarDecoration":   d,
	})
	return c.JSON(http.StatusOK, d)
}

// AvatarDecorationsDelete handles POST /api/admin/avatar-decorations/delete.
func (h *Handler) AvatarDecorationsDelete(c echo.Context) error {
	if h.avatarDecoRepo == nil {
		return c.NoContent(http.StatusNoContent)
	}
	var req struct {
		ID string `json:"id"`
	}
	_ = c.Bind(&req)
	if req.ID == "" {
		return c.NoContent(http.StatusNoContent)
	}
	snapshot, _ := h.avatarDecoRepo.FindByID(req.ID)
	if err := h.avatarDecoRepo.Delete(req.ID); err == nil && snapshot != nil {
		h.logModeration(c, moderationlog.LogDeleteAvatarDecoration, map[string]any{
			"avatarDecorationId": req.ID,
			"avatarDecoration":   snapshot,
		})
	}
	return c.NoContent(http.StatusNoContent)
}

// AvatarDecorationsList handles POST /api/admin/avatar-decorations/list.
func (h *Handler) AvatarDecorationsList(c echo.Context) error {
	if h.avatarDecoRepo == nil {
		return c.JSON(http.StatusOK, []any{})
	}
	rows, err := h.avatarDecoRepo.List()
	if err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.InternalError())
	}
	if rows == nil {
		return c.JSON(http.StatusOK, []any{})
	}
	return c.JSON(http.StatusOK, rows)
}

// AvatarDecorationsUpdate handles POST /api/admin/avatar-decorations/update.
func (h *Handler) AvatarDecorationsUpdate(c echo.Context) error {
	if h.avatarDecoRepo == nil {
		return c.NoContent(http.StatusNoContent)
	}
	var req struct {
		ID          string    `json:"id"`
		Name        *string   `json:"name"`
		Description *string   `json:"description"`
		URL         *string   `json:"url"`
		RoleIDs     *[]string `json:"roleIdsThatCanBeUsedThisDecoration"`
	}
	if err := c.Bind(&req); err != nil || req.ID == "" {
		return c.JSON(http.StatusBadRequest, apierr.InvalidParam("Invalid parameters."))
	}
	before, err := h.avatarDecoRepo.FindByID(req.ID)
	if err != nil {
		return c.JSON(http.StatusNotFound, apierr.NotFound())
	}
	fields := map[string]any{"updatedAt": time.Now()}
	if req.Name != nil {
		fields["name"] = *req.Name
	}
	if req.Description != nil {
		fields["description"] = *req.Description
	}
	if req.URL != nil {
		fields["url"] = *req.URL
	}
	if req.RoleIDs != nil {
		// pq.StringArray でラップしないと GORM Updates(map) が空 string[] を
		// NULL 化して NOT NULL 制約違反になる (#931、#896 / #900 / #901 と同 class)。
		fields["roleIdsThatCanBeUsedThisDecoration"] = pq.StringArray(*req.RoleIDs)
	}
	if err := h.avatarDecoRepo.UpdateFields(req.ID, fields); err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.InternalError())
	}
	if after, err := h.avatarDecoRepo.FindByID(req.ID); err == nil && after != nil {
		h.logModeration(c, moderationlog.LogUpdateAvatarDecoration, map[string]any{
			"avatarDecorationId": req.ID,
			"before":             before,
			"after":              after,
		})
	}
	return c.NoContent(http.StatusNoContent)
}
