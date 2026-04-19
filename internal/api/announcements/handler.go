package announcements

import (
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/api/apierr"
	"github.com/shiroha-a/mk/internal/entity"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
	"github.com/shiroha-a/mk/internal/server/middleware"
)

// MainStreamPublisher emits events to a user's `main` WebSocket channel.
// Used here to publish `announcementCreated` (per-user announcements) and
// `readAllAnnouncements`. Accepted as an interface to avoid a hard
// dependency on internal/stream (循環依存回避)。
type MainStreamPublisher interface {
	PublishMainEvent(userID, eventType string, body any)
}

// Handler handles announcement-related API endpoints.
type Handler struct {
	repo                repository.AnnouncementRepository
	idGen               id.Generator
	mainStreamPublisher MainStreamPublisher
}

// NewHandler creates a new announcements Handler.
func NewHandler(repo repository.AnnouncementRepository, idGen id.Generator) *Handler {
	return &Handler{repo: repo, idGen: idGen}
}

// SetMainStreamPublisher attaches a publisher used to emit `main` channel
// events (`announcementCreated` / `readAllAnnouncements`). Optional — nil
// disables emit.
func (h *Handler) SetMainStreamPublisher(p MainStreamPublisher) {
	h.mainStreamPublisher = p
}

// List handles POST /api/announcements.
func (h *Handler) List(c echo.Context) error {
	var req struct {
		Limit    int    `json:"limit"`
		Offset   int    `json:"offset"`
		SinceID  string `json:"sinceId"`
		UntilID  string `json:"untilId"`
		IsActive *bool  `json:"isActive"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "Invalid parameters.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	activeOnly := true
	if req.IsActive != nil {
		activeOnly = *req.IsActive
	}
	// 認証ユーザーがいればper-user announcementの対象を自分に限定した
	// ListForUserを使い、他ユーザー宛のannouncementを除外する。未認証は
	// ListGlobal(userId IS NULLのみ)を使い、targetedなannouncementを
	// 漏らさない。
	user := middleware.GetUser(c)
	var items []*model.Announcement
	var err error
	if user != nil {
		items, err = h.repo.ListForUser(user.ID, activeOnly, req.Limit, req.Offset, req.SinceID, req.UntilID)
	} else {
		items, err = h.repo.ListGlobal(activeOnly, req.Limit, req.Offset, req.SinceID, req.UntilID)
	}
	if err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}

	result := make([]map[string]any, 0, len(items))
	for _, a := range items {
		item := packAnnouncement(a, h.idGen)
		if user != nil {
			read, _ := h.repo.IsRead(user.ID, a.ID)
			item["isRead"] = read
		}
		result = append(result, item)
	}
	return c.JSON(http.StatusOK, result)
}

// ReadAnnouncement handles POST /api/i/read-announcement.
func (h *Handler) ReadAnnouncement(c echo.Context) error {
	user := middleware.GetUser(c)
	var req struct {
		AnnouncementID string `json:"announcementId"`
	}
	if err := c.Bind(&req); err != nil || req.AnnouncementID == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "announcementId is required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	a, err := h.repo.FindByID(req.AnnouncementID)
	if err != nil {
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_ANNOUNCEMENT", "No such announcement.", "b57b5e1d-0158-4f8d-bd54-1ab374089a15"))
	}
	// per-user announcementのうち他ユーザー宛ては、そのユーザー以外は
	// 閲覧できないので「存在しない」扱いで弾く(IDから本人宛てか推測
	// されるのを防ぐため)。
	if a.UserID != nil && *a.UserID != user.ID {
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_ANNOUNCEMENT", "No such announcement.", "b57b5e1d-0158-4f8d-bd54-1ab374089a15"))
	}
	already, _ := h.repo.IsRead(user.ID, req.AnnouncementID)
	if already {
		return c.NoContent(http.StatusNoContent)
	}
	read := &model.AnnouncementRead{
		ID:             h.idGen.Generate(time.Now()),
		UserID:         user.ID,
		AnnouncementID: req.AnnouncementID,
	}
	if err := h.repo.MarkRead(read); err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	// TS本家AnnouncementService.read(line 220): 残unread数が0になった時点で
	// mainにreadAllAnnouncementsをpublishする(body: null)。
	if h.mainStreamPublisher != nil {
		if unread, err := h.repo.UnreadForUser(user.ID); err == nil && len(unread) == 0 {
			h.mainStreamPublisher.PublishMainEvent(user.ID, "readAllAnnouncements", nil)
		}
	}
	return c.NoContent(http.StatusNoContent)
}

// --- Admin endpoints ---

// AdminCreate handles POST /api/admin/announcements/create.
func (h *Handler) AdminCreate(c echo.Context) error {
	var req struct {
		Title    string  `json:"title"`
		Text     string  `json:"text"`
		ImageURL *string `json:"imageUrl"`
		Icon     string  `json:"icon"`
		Display  string  `json:"display"`
		UserID   *string `json:"userId"`
	}
	if err := c.Bind(&req); err != nil || req.Title == "" || req.Text == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "title and text are required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	if req.Icon == "" {
		req.Icon = "info"
	}
	if req.Display == "" {
		req.Display = "normal"
	}
	a := &model.Announcement{
		ID:       h.idGen.Generate(time.Now()),
		Title:    req.Title,
		Text:     req.Text,
		ImageURL: req.ImageURL,
		Icon:     req.Icon,
		Display:  req.Display,
		IsActive: true,
		UserID:   req.UserID,
	}
	if err := h.repo.Create(a); err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	// TS本家AnnouncementService.create(line 87): per-user announcement
	// (userId指定)の場合のみmainにpublishする。global announcementは
	// publishBroadcastStream経由だがGo側では別インフラ未実装なのでskip。
	if h.mainStreamPublisher != nil && a.UserID != nil {
		body := map[string]any{"announcement": entity.PackAnnouncement(a, h.idGen, false)}
		h.mainStreamPublisher.PublishMainEvent(*a.UserID, "announcementCreated", body)
	}
	return c.JSON(http.StatusOK, a)
}

// AdminUpdate handles POST /api/admin/announcements/update.
func (h *Handler) AdminUpdate(c echo.Context) error {
	var req struct {
		ID       string  `json:"id"`
		Title    *string `json:"title"`
		Text     *string `json:"text"`
		IsActive *bool   `json:"isActive"`
	}
	if err := c.Bind(&req); err != nil || req.ID == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "id is required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	fields := map[string]any{}
	if req.Title != nil {
		fields["title"] = *req.Title
	}
	if req.Text != nil {
		fields["text"] = *req.Text
	}
	if req.IsActive != nil {
		fields["isActive"] = *req.IsActive
	}
	if err := h.repo.UpdateFields(req.ID, fields); err != nil {
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_ANNOUNCEMENT", "No such announcement.", "b57b5e1d-0158-4f8d-bd54-1ab374089a15"))
	}
	return c.NoContent(http.StatusNoContent)
}

// AdminDelete handles POST /api/admin/announcements/delete.
func (h *Handler) AdminDelete(c echo.Context) error {
	var req struct {
		ID string `json:"id"`
	}
	if err := c.Bind(&req); err != nil || req.ID == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "id is required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	if err := h.repo.Delete(req.ID); err != nil {
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_ANNOUNCEMENT", "No such announcement.", "b57b5e1d-0158-4f8d-bd54-1ab374089a15"))
	}
	return c.NoContent(http.StatusNoContent)
}

// AdminList handles POST /api/admin/announcements/list.
func (h *Handler) AdminList(c echo.Context) error {
	var req struct {
		Limit   int    `json:"limit"`
		Offset  int    `json:"offset"`
		SinceID string `json:"sinceId"`
		UntilID string `json:"untilId"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "Invalid parameters.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	items, err := h.repo.List(false, req.Limit, req.Offset, req.SinceID, req.UntilID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	return c.JSON(http.StatusOK, items)
}

// packAnnouncement wraps entity.PackAnnouncement for handler use. isRead
// defaults to false at this layer; the caller overrides after viewer check.
// Retains `forExistingUsers` / `isActive` which List pre-existing API
// clients may rely on (entity packer targets the event body which TS
// doesn't include these two).
func packAnnouncement(a *model.Announcement, idGen id.Generator) map[string]any {
	out := entity.PackAnnouncement(a, idGen, false)
	out["forExistingUsers"] = a.ForExistingUsers
	out["isActive"] = a.IsActive
	return out
}
