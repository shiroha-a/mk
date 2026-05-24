package clips

import (
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/api/apierr"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/server/middleware"
)

// ClipFavoriteRepository is the interface for clip favorite operations.
type ClipFavoriteRepository interface {
	Create(fav *model.ClipFavorite) error
	Delete(userID, clipID string) error
	ListByUser(userID string) ([]*model.ClipFavorite, error)
	Exists(userID, clipID string) (bool, error)
}

// SetFavoriteRepo attaches a ClipFavoriteRepository for favorite endpoints.
func (h *Handler) SetFavoriteRepo(r ClipFavoriteRepository) {
	h.favoriteRepo = r
}

// Favorite handles POST /api/clips/favorite.
func (h *Handler) Favorite(c echo.Context) error {
	user := middleware.GetUser(c)
	var req struct {
		ClipID string `json:"clipId"`
	}
	if err := c.Bind(&req); err != nil || req.ClipID == "" {
		return apierr.JSONInvalidParam(c)
	}
	if h.favoriteRepo == nil {
		return c.NoContent(http.StatusNoContent)
	}
	// クリップの存在確認
	if _, err := h.svc.Show(user.ID, req.ClipID); err != nil {
		return notFound(c)
	}
	already, _ := h.favoriteRepo.Exists(user.ID, req.ClipID)
	if already {
		return c.NoContent(http.StatusNoContent)
	}
	fav := &model.ClipFavorite{
		ID:     h.idGen.Generate(time.Now()),
		UserID: user.ID,
		ClipID: req.ClipID,
	}
	if err := h.favoriteRepo.Create(fav); err != nil {
		return apierr.JSONInternalError(c)
	}
	return c.NoContent(http.StatusNoContent)
}

// Unfavorite handles POST /api/clips/unfavorite.
func (h *Handler) Unfavorite(c echo.Context) error {
	user := middleware.GetUser(c)
	var req struct {
		ClipID string `json:"clipId"`
	}
	if err := c.Bind(&req); err != nil || req.ClipID == "" {
		return apierr.JSONInvalidParam(c)
	}
	if h.favoriteRepo == nil {
		return c.NoContent(http.StatusNoContent)
	}
	if err := h.favoriteRepo.Delete(user.ID, req.ClipID); err != nil {
		return apierr.JSONInternalError(c)
	}
	return c.NoContent(http.StatusNoContent)
}

// MyFavorites handles POST /api/clips/my-favorites.
func (h *Handler) MyFavorites(c echo.Context) error {
	user := middleware.GetUser(c)
	if h.favoriteRepo == nil {
		return c.JSON(http.StatusOK, []any{})
	}
	favs, err := h.favoriteRepo.ListByUser(user.ID)
	if err != nil {
		return apierr.JSONInternalError(c)
	}
	out := make([]map[string]any, 0, len(favs))
	for _, f := range favs {
		cl, err := h.svc.Show(user.ID, f.ClipID)
		if err != nil {
			continue
		}
		out = append(out, h.clipToMap(cl))
	}
	return c.JSON(http.StatusOK, out)
}
