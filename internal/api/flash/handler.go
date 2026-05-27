// Package flash provides /api/flash/* endpoints.
package flash

import (
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/api/apierr"
	coreflash "github.com/shiroha-a/mk/internal/core/flash"
	"github.com/shiroha-a/mk/internal/entity"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
	"github.com/shiroha-a/mk/internal/server/middleware"
)

// Handler handles flash-related API endpoints.
type Handler struct {
	svc      *coreflash.Service
	userRepo repository.UserRepository
	idGen    id.Generator
}

// NewHandler creates a new flash Handler.
// userRepo / idGen are required by list endpoints to embed the
// `user` (UserLite) and `createdAt` fields that the upstream Misskey
// frontend expects when rendering Play (Flash) cards.
func NewHandler(svc *coreflash.Service, userRepo repository.UserRepository, idGen id.Generator) *Handler {
	return &Handler{svc: svc, userRepo: userRepo, idGen: idGen}
}

// CreateRequest is the request body for flash/create.
type CreateRequest struct {
	Title       string   `json:"title"`
	Summary     string   `json:"summary"`
	Script      string   `json:"script"`
	Permissions []string `json:"permissions"`
	Visibility  string   `json:"visibility"`
}

// Create handles POST /api/flash/create.
func (h *Handler) Create(c echo.Context) error {
	user := middleware.GetUser(c)
	var req CreateRequest
	if err := c.Bind(&req); err != nil || req.Title == "" || req.Script == "" {
		return apierr.JSONInvalidParam(c)
	}
	f, err := h.svc.Create(coreflash.CreateInput{
		OwnerID:     user.ID,
		Title:       req.Title,
		Summary:     req.Summary,
		Script:      req.Script,
		Permissions: req.Permissions,
		Visibility:  req.Visibility,
	})
	if err != nil {
		return apierr.JSONInternalError(c)
	}
	return c.JSON(http.StatusOK, h.flashWithKnownUser(f, user))
}

// ShowRequest is the request body for flash/show.
type ShowRequest struct {
	FlashID string `json:"flashId"`
}

// Show handles POST /api/flash/show.
//
// frontend MkPlay.vue は user / createdAt / isLiked を読んでカード描画
// および「いいね」ボタン状態を決めるので、handler 側で embed する。
func (h *Handler) Show(c echo.Context) error {
	user := middleware.GetUser(c)
	var req ShowRequest
	if err := c.Bind(&req); err != nil || req.FlashID == "" {
		return apierr.JSONInvalidParam(c)
	}
	requesterID := ""
	if user != nil {
		requesterID = user.ID
	}
	f, err := h.svc.Show(requesterID, req.FlashID)
	if err != nil {
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_FLASH", "No such flash.", "f0d34a1a-d29a-401d-90ba-1982122b5630"))
	}
	resp := h.flashesToListWithUser([]*model.Flash{f})[0]
	if user != nil {
		liked, err := h.svc.IsLikedBy(user.ID, f.ID)
		if err == nil {
			resp["isLiked"] = liked
		}
	}
	return c.JSON(http.StatusOK, resp)
}

// UpdateRequest is the request body for flash/update.
type UpdateRequest struct {
	FlashID     string    `json:"flashId"`
	Title       *string   `json:"title"`
	Summary     *string   `json:"summary"`
	Script      *string   `json:"script"`
	Permissions *[]string `json:"permissions"`
	Visibility  *string   `json:"visibility"`
}

// Update handles POST /api/flash/update.
func (h *Handler) Update(c echo.Context) error {
	user := middleware.GetUser(c)
	var req UpdateRequest
	if err := c.Bind(&req); err != nil || req.FlashID == "" {
		return apierr.JSONInvalidParam(c)
	}
	f, err := h.svc.Update(user.ID, req.FlashID, coreflash.UpdateInput{
		Title:       req.Title,
		Summary:     req.Summary,
		Script:      req.Script,
		Permissions: req.Permissions,
		Visibility:  req.Visibility,
	})
	if err != nil {
		switch {
		case errors.Is(err, coreflash.ErrFlashNotFound):
			return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_FLASH", "No such flash.", "611e13d2-309e-419a-a5e4-e0422da39b02"))
		case errors.Is(err, coreflash.ErrAccessDenied):
			return c.JSON(http.StatusForbidden, apierr.Error("ACCESS_DENIED", "Access denied.", "08e60c88-5948-478e-a132-02ec701d67b2"))
		case errors.Is(err, coreflash.ErrFlashTitleRequired):
			return apierr.JSONInvalidParam(c)
		}
		return apierr.JSONInternalError(c)
	}
	return c.JSON(http.StatusOK, h.flashWithKnownUser(f, user))
}

// DeleteRequest is the request body for flash/delete.
type DeleteRequest struct {
	FlashID string `json:"flashId"`
}

// Delete handles POST /api/flash/delete.
func (h *Handler) Delete(c echo.Context) error {
	user := middleware.GetUser(c)
	var req DeleteRequest
	if err := c.Bind(&req); err != nil || req.FlashID == "" {
		return apierr.JSONInvalidParam(c)
	}
	if err := h.svc.Delete(user.ID, req.FlashID); err != nil {
		switch {
		case errors.Is(err, coreflash.ErrFlashNotFound):
			return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_FLASH", "No such flash.", "de1623ef-bbb3-4289-a71e-14cfa83d9740"))
		case errors.Is(err, coreflash.ErrAccessDenied):
			return c.JSON(http.StatusForbidden, apierr.Error("ACCESS_DENIED", "Access denied.", "1036ad7b-9f92-4fff-89c3-0e50dc941704"))
		}
		return apierr.JSONInternalError(c)
	}
	return c.NoContent(http.StatusNoContent)
}

// PaginationRequest is the shared body for list-style endpoints.
type PaginationRequest struct {
	Limit     int    `json:"limit"`
	Offset    int    `json:"offset"`
	SinceID   string `json:"sinceId"`
	UntilID   string `json:"untilId"`
	SinceDate *int64 `json:"sinceDate"`
	UntilDate *int64 `json:"untilDate"`
}

// SearchRequest is the request body for flash/search.
type SearchRequest struct {
	Query     string `json:"query"`
	Limit     int    `json:"limit"`
	Offset    int    `json:"offset"`
	SinceID   string `json:"sinceId"`
	UntilID   string `json:"untilId"`
	SinceDate *int64 `json:"sinceDate"`
	UntilDate *int64 `json:"untilDate"`
}

// My handles POST /api/i/flashs and POST /api/flash/my (own list).
//
// frontend Paginator (cursor mode) は untilId / sinceId を forward する (#493)。
func (h *Handler) My(c echo.Context) error {
	user := middleware.GetUser(c)
	var req PaginationRequest
	if err := c.Bind(&req); err != nil {
		return apierr.JSONInvalidParam(c)
	}
	// sinceDate / untilDate を aidx prefix に正規化 (#1166)。
	sinceID, untilID := id.NormalizeCursor(req.SinceID, req.UntilID, req.SinceDate, req.UntilDate)
	rows, err := h.svc.My(user.ID, sinceID, untilID, req.Limit, req.Offset)
	if err != nil {
		return apierr.JSONInternalError(c)
	}
	return c.JSON(http.StatusOK, h.flashesToListWithUser(rows))
}

// Featured handles POST /api/flash/featured.
func (h *Handler) Featured(c echo.Context) error {
	var req PaginationRequest
	if err := c.Bind(&req); err != nil {
		return apierr.JSONInvalidParam(c)
	}
	// sinceDate / untilDate を aidx prefix に正規化 (#1166)。
	sinceID, untilID := id.NormalizeCursor(req.SinceID, req.UntilID, req.SinceDate, req.UntilDate)
	rows, err := h.svc.Featured(sinceID, untilID, req.Limit, req.Offset)
	if err != nil {
		return apierr.JSONInternalError(c)
	}
	return c.JSON(http.StatusOK, h.flashesToListWithUser(rows))
}

// Search handles POST /api/flash/search.
func (h *Handler) Search(c echo.Context) error {
	var req SearchRequest
	if err := c.Bind(&req); err != nil || req.Query == "" {
		return apierr.JSONInvalidParam(c)
	}
	// sinceDate / untilDate を aidx prefix に正規化 (#1166)。
	sinceID, untilID := id.NormalizeCursor(req.SinceID, req.UntilID, req.SinceDate, req.UntilDate)
	rows, err := h.svc.Search(req.Query, sinceID, untilID, req.Limit, req.Offset)
	if err != nil {
		return apierr.JSONInternalError(c)
	}
	return c.JSON(http.StatusOK, h.flashesToListWithUser(rows))
}

// LikeRequest is the request body for flash/like and flash/unlike.
type LikeRequest struct {
	FlashID string `json:"flashId"`
}

// Like handles POST /api/flash/like.
func (h *Handler) Like(c echo.Context) error {
	user := middleware.GetUser(c)
	var req LikeRequest
	if err := c.Bind(&req); err != nil || req.FlashID == "" {
		return apierr.JSONInvalidParam(c)
	}
	if err := h.svc.Like(user.ID, req.FlashID); err != nil {
		switch {
		case errors.Is(err, coreflash.ErrFlashNotFound):
			return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_FLASH", "No such flash.", "c07c1491-9161-4c5c-9d75-01906f911f73"))
		case errors.Is(err, coreflash.ErrAlreadyLiked):
			return c.JSON(http.StatusBadRequest, apierr.Error("ALREADY_LIKED", "You already liked that flash.", "010065cf-ad43-40df-8067-abff9f4686e3"))
		}
		return apierr.JSONInternalError(c)
	}
	return c.NoContent(http.StatusNoContent)
}

// Unlike handles POST /api/flash/unlike.
func (h *Handler) Unlike(c echo.Context) error {
	user := middleware.GetUser(c)
	var req LikeRequest
	if err := c.Bind(&req); err != nil || req.FlashID == "" {
		return apierr.JSONInvalidParam(c)
	}
	if err := h.svc.Unlike(user.ID, req.FlashID); err != nil {
		switch {
		case errors.Is(err, coreflash.ErrFlashNotFound):
			return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_FLASH", "No such flash.", "afe8424a-a69e-432d-a5f2-2f0740c62410"))
		case errors.Is(err, coreflash.ErrNotLiked):
			return c.JSON(http.StatusBadRequest, apierr.Error("NOT_LIKED", "You have not liked that flash.", "755f25a7-9871-4f65-9f34-51eaad9ae0ac"))
		}
		return apierr.JSONInternalError(c)
	}
	return c.NoContent(http.StatusNoContent)
}

// MyLikes handles POST /api/flash/my-likes.
//
// upstream Misskey TS は `{id, flash: Flash}` を返す (id = flash_like
// row id, frontend は it.flash で MkPlayPreview を描画)。mk-go は元々
// flatten された Flash 配列を返しており frontend が空表示になっていた。
func (h *Handler) MyLikes(c echo.Context) error {
	user := middleware.GetUser(c)
	var req PaginationRequest
	if err := c.Bind(&req); err != nil {
		return apierr.JSONInvalidParam(c)
	}
	// sinceDate / untilDate を aidx prefix に正規化 (#1166)。
	sinceID, untilID := id.NormalizeCursor(req.SinceID, req.UntilID, req.SinceDate, req.UntilDate)
	pairs, err := h.svc.MyLikes(user.ID, sinceID, untilID, req.Limit, req.Offset)
	if err != nil {
		return apierr.JSONInternalError(c)
	}
	flashes := make([]*model.Flash, 0, len(pairs))
	for _, p := range pairs {
		flashes = append(flashes, p.Flash)
	}
	packed := h.flashesToListWithUser(flashes)
	out := make([]map[string]any, 0, len(pairs))
	for i, p := range pairs {
		out = append(out, map[string]any{
			"id":    p.LikeID,
			"flash": packed[i],
		})
	}
	return c.JSON(http.StatusOK, out)
}

func flashToMap(f *model.Flash) map[string]any {
	const tsFormat = "2006-01-02T15:04:05.000Z"
	return map[string]any{
		"id":          f.ID,
		"updatedAt":   f.UpdatedAt.UTC().Format(tsFormat),
		"title":       f.Title,
		"summary":     f.Summary,
		"userId":      f.UserID,
		"script":      f.Script,
		"permissions": []string(f.Permissions),
		"likedCount":  f.LikedCount,
		"visibility":  f.Visibility,
	}
}

// flashWithKnownUser packs a single flash whose author is already in scope
// (typically the authenticated user from middleware on Create / Update).
// Saves one FindManyByIDs round-trip vs flashesToListWithUser.
func (h *Handler) flashWithKnownUser(f *model.Flash, owner *model.User) map[string]any {
	const tsFormat = "2006-01-02T15:04:05.000Z"
	entry := flashToMap(f)
	createdAt := ""
	if h.idGen != nil {
		if t, err := h.idGen.ParseTime(f.ID); err == nil {
			createdAt = t.UTC().Format(tsFormat)
		}
	}
	// flashesToListWithUser と同じく nil/error 時も "" でフィールドを必ず
	// 残す。frontend が `if (item.createdAt)` する場合のため (#520 review)。
	entry["createdAt"] = createdAt
	if owner != nil && owner.ID == f.UserID {
		entry["user"] = entity.PackUserLite(owner)
	}
	return entry
}

// flashesToListWithUser packs flashes including the embedded `user`
// (UserLite) and ISO-formatted `createdAt`, matching upstream Misskey TS
// FlashEntityService output. The frontend Play page reads `flash.user`
// for display so a missing user object causes empty card render.
//
// User lookups are deduped by author and fetched in a single FindManyByIDs
// call to avoid the N+1 pattern.
func (h *Handler) flashesToListWithUser(rows []*model.Flash) []map[string]any {
	const tsFormat = "2006-01-02T15:04:05.000Z"
	userIDs := make([]string, 0, len(rows))
	seen := make(map[string]struct{}, len(rows))
	for _, f := range rows {
		if _, ok := seen[f.UserID]; ok {
			continue
		}
		seen[f.UserID] = struct{}{}
		userIDs = append(userIDs, f.UserID)
	}
	userByID := make(map[string]*model.User, len(userIDs))
	if h.userRepo != nil && len(userIDs) > 0 {
		if users, err := h.userRepo.FindManyByIDs(userIDs); err == nil {
			for _, u := range users {
				userByID[u.ID] = u
			}
		}
	}
	out := make([]map[string]any, 0, len(rows))
	for _, f := range rows {
		entry := flashToMap(f)
		createdAt := ""
		if h.idGen != nil {
			if t, err := h.idGen.ParseTime(f.ID); err == nil {
				createdAt = t.UTC().Format(tsFormat)
			}
		}
		entry["createdAt"] = createdAt
		if u, ok := userByID[f.UserID]; ok {
			entry["user"] = entity.PackUserLite(u)
		}
		out = append(out, entry)
	}
	return out
}
