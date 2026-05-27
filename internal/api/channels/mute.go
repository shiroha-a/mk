package channels

import (
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/api/apierr"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/server/middleware"
)

// SetMutingRepo attaches a ChannelMutingRepository for mute endpoints.
func (h *Handler) SetMutingRepo(r ChannelMutingRepository) {
	h.mutingRepo = r
}

// MuteCreate handles POST /api/channels/mute/create.
func (h *Handler) MuteCreate(c echo.Context) error {
	user := middleware.GetUser(c)
	var req struct {
		ChannelID string `json:"channelId"`
	}
	if err := c.Bind(&req); err != nil || req.ChannelID == "" {
		return apierr.JSONInvalidParam(c)
	}
	if h.mutingRepo == nil {
		return c.NoContent(http.StatusNoContent)
	}
	if _, err := h.svc.Show(req.ChannelID); err != nil {
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_CHANNEL", "No such channel.", "7174361e-d58f-31d6-2e7c-6fb830786a3f"))
	}
	already, _ := h.mutingRepo.Exists(user.ID, req.ChannelID)
	if already {
		return c.NoContent(http.StatusNoContent)
	}
	mut := &model.ChannelMuting{
		ID:        h.idGen.Generate(time.Now()),
		UserID:    user.ID,
		ChannelID: req.ChannelID,
	}
	if err := h.mutingRepo.Create(mut); err != nil {
		return apierr.JSONInternalError(c)
	}
	return c.NoContent(http.StatusNoContent)
}

// MuteDelete handles POST /api/channels/mute/delete.
func (h *Handler) MuteDelete(c echo.Context) error {
	user := middleware.GetUser(c)
	var req struct {
		ChannelID string `json:"channelId"`
	}
	if err := c.Bind(&req); err != nil || req.ChannelID == "" {
		return apierr.JSONInvalidParam(c)
	}
	if h.mutingRepo == nil {
		return c.NoContent(http.StatusNoContent)
	}
	if err := h.mutingRepo.Delete(user.ID, req.ChannelID); err != nil {
		return apierr.JSONInternalError(c)
	}
	return c.NoContent(http.StatusNoContent)
}

// MuteList handles POST /api/channels/mute/list.
func (h *Handler) MuteList(c echo.Context) error {
	user := middleware.GetUser(c)
	if h.mutingRepo == nil {
		return c.JSON(http.StatusOK, []any{})
	}
	mutings, err := h.mutingRepo.ListByUser(user.ID)
	if err != nil {
		return apierr.JSONInternalError(c)
	}
	rows := make([]*model.Channel, 0, len(mutings))
	for _, m := range mutings {
		ch, err := h.svc.Show(m.ChannelID)
		if err != nil {
			continue
		}
		rows = append(rows, ch)
	}
	// MyFavorites と同じく channelsToList 経由で viewer-aware に
	// 揃える (Devin #530 BUG-1)。
	return c.JSON(http.StatusOK, h.channelsToList(rows, user))
}
