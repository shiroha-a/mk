package admin

import (
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/api/apierr"
	"github.com/shiroha-a/mk/internal/core/moderationlog"
	"github.com/shiroha-a/mk/internal/model"
)

// AbuseReportNotificationRecipientCreate handles POST /api/admin/abuse-report/notification-recipient/create.
func (h *Handler) AbuseReportNotificationRecipientCreate(c echo.Context) error {
	if h.recipientRepo == nil {
		return c.NoContent(http.StatusNoContent)
	}
	var req struct {
		Name            string  `json:"name"`
		Method          string  `json:"method"`
		IsActive        *bool   `json:"isActive"`
		UserID          *string `json:"userId"`
		SystemWebhookID *string `json:"systemWebhookId"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierr.InvalidParam("Invalid parameters."))
	}
	// upstream Misskey TS は paramDef で isActive / name / method を required
	// にしている。さらに method='email' のとき userId 必須、method='webhook'
	// のとき systemWebhookId 必須の相関 check を行う (#929)。
	if req.Name == "" || req.Method == "" || req.IsActive == nil {
		return c.JSON(http.StatusBadRequest, apierr.InvalidParam("name / method / isActive are required."))
	}
	if req.Method == "email" && (req.UserID == nil || *req.UserID == "") {
		return c.JSON(http.StatusBadRequest, apierr.Error("CORRELATION_CHECK_EMAIL", "If \"method\" is email, \"userId\" must be set.", "348bb8ae-575a-6fe9-4327-5811999def8f"))
	}
	if req.Method == "webhook" && (req.SystemWebhookID == nil || *req.SystemWebhookID == "") {
		return c.JSON(http.StatusBadRequest, apierr.Error("CORRELATION_CHECK_WEBHOOK", "If \"method\" is webhook, \"systemWebhookId\" must be set.", "b0c15051-de2d-29ef-260c-9585cddd701a"))
	}
	r := &model.AbuseReportNotificationRecipient{
		ID:              h.idGen.Generate(time.Now()),
		Name:            req.Name,
		Method:          req.Method,
		UserID:          req.UserID,
		SystemWebhookID: req.SystemWebhookID,
		IsActive:        *req.IsActive,
	}
	if err := h.recipientRepo.Create(r); err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.InternalError())
	}
	h.logModeration(c, moderationlog.LogCreateAbuseReportNotificationRecipient, map[string]any{
		"recipientId": r.ID,
		"recipient":   r,
	})
	return c.JSON(http.StatusOK, r)
}

// AbuseReportNotificationRecipientDelete handles POST /api/admin/abuse-report/notification-recipient/delete.
func (h *Handler) AbuseReportNotificationRecipientDelete(c echo.Context) error {
	if h.recipientRepo == nil {
		return c.NoContent(http.StatusNoContent)
	}
	var req struct {
		ID string `json:"id"`
	}
	_ = c.Bind(&req)
	if req.ID == "" {
		return c.NoContent(http.StatusNoContent)
	}
	snapshot, _ := h.recipientRepo.FindByID(req.ID)
	if err := h.recipientRepo.Delete(req.ID); err == nil && snapshot != nil {
		h.logModeration(c, moderationlog.LogDeleteAbuseReportNotificationRecipient, map[string]any{
			"recipientId": req.ID,
			"recipient":   snapshot,
		})
	}
	return c.NoContent(http.StatusNoContent)
}

// AbuseReportNotificationRecipientList handles POST /api/admin/abuse-report/notification-recipient/list.
func (h *Handler) AbuseReportNotificationRecipientList(c echo.Context) error {
	if h.recipientRepo == nil {
		return c.JSON(http.StatusOK, []any{})
	}
	rows, err := h.recipientRepo.List()
	if err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.InternalError())
	}
	// nil を明示的に空配列化 (クライアント互換)
	if rows == nil {
		return c.JSON(http.StatusOK, []any{})
	}
	return c.JSON(http.StatusOK, rows)
}

// AbuseReportNotificationRecipientShow handles POST /api/admin/abuse-report/notification-recipient/show.
func (h *Handler) AbuseReportNotificationRecipientShow(c echo.Context) error {
	if h.recipientRepo == nil {
		return c.JSON(http.StatusNotFound, apierr.NotFound())
	}
	var req struct {
		ID string `json:"id"`
	}
	_ = c.Bind(&req)
	r, err := h.recipientRepo.FindByID(req.ID)
	if err != nil {
		return c.JSON(http.StatusNotFound, apierr.NotFound())
	}
	return c.JSON(http.StatusOK, r)
}

// AbuseReportNotificationRecipientUpdate handles POST /api/admin/abuse-report/notification-recipient/update.
func (h *Handler) AbuseReportNotificationRecipientUpdate(c echo.Context) error {
	if h.recipientRepo == nil {
		return c.NoContent(http.StatusNoContent)
	}
	var req struct {
		ID              string  `json:"id"`
		Name            *string `json:"name"`
		Method          *string `json:"method"`
		IsActive        *bool   `json:"isActive"`
		UserID          *string `json:"userId"`
		SystemWebhookID *string `json:"systemWebhookId"`
	}
	if err := c.Bind(&req); err != nil || req.ID == "" {
		return c.JSON(http.StatusBadRequest, apierr.InvalidParam("Invalid parameters."))
	}
	before, _ := h.recipientRepo.FindByID(req.ID)
	fields := map[string]any{}
	if req.Name != nil {
		fields["name"] = *req.Name
	}
	if req.Method != nil {
		fields["method"] = *req.Method
	}
	if req.IsActive != nil {
		fields["isActive"] = *req.IsActive
	}
	if req.UserID != nil {
		fields["userId"] = *req.UserID
	}
	if req.SystemWebhookID != nil {
		fields["systemWebhookId"] = *req.SystemWebhookID
	}
	// GORM Updates(map) は対象なしでも nil を返すため、ここでのエラーは DB 障害
	// 等の真の失敗。NotFound は続く FindByID で検出する。
	if err := h.recipientRepo.Update(req.ID, fields); err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.InternalError())
	}
	r, err := h.recipientRepo.FindByID(req.ID)
	if err != nil {
		return c.JSON(http.StatusNotFound, apierr.NotFound())
	}
	if before != nil {
		h.logModeration(c, moderationlog.LogUpdateAbuseReportNotificationRecipient, map[string]any{
			"recipientId": req.ID,
			"before":      before,
			"after":       r,
		})
	}
	return c.JSON(http.StatusOK, r)
}
