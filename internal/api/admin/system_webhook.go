package admin

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/lib/pq"

	"github.com/shiroha-a/mk/internal/api/apierr"
	"github.com/shiroha-a/mk/internal/core/moderationlog"
	"github.com/shiroha-a/mk/internal/model"
)

// sendWebhookTest sends a test webhook POST request.
//
// Misskey 本家の admin/system-webhook/test 互換。シークレットがあれば
// X-Misskey-Hook-Secret ヘッダに平文を載せる (本家も同仕様)。
//
// client は SSRF-safe transport + forward proxy 経由を期待した HTTP client。
// nil なら 10s timeout の素の Client にフォールバックする (#638)。
func sendWebhookTest(client *http.Client, url, secret, eventType string) {
	body := fmt.Sprintf(`{"type":"%s","body":{"test":true},"createdAt":"%s"}`,
		eventType, time.Now().UTC().Format(time.RFC3339))

	req, err := http.NewRequest("POST", url, strings.NewReader(body))
	if err != nil {
		slog.Warn("webhook test: request creation failed", "error", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if secret != "" {
		req.Header.Set("X-Misskey-Hook-Secret", secret)
	}

	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		slog.Warn("webhook test: request failed", "error", err)
		return
	}
	_ = resp.Body.Close()
	slog.Info("webhook test sent", "url", url, "status", resp.StatusCode)
}

// SystemWebhookCreate handles POST /api/admin/system-webhook/create.
func (h *Handler) SystemWebhookCreate(c echo.Context) error {
	if h.systemWebhookRepo == nil {
		return c.NoContent(http.StatusNoContent)
	}
	var req struct {
		Name     string   `json:"name"`
		URL      string   `json:"url"`
		Secret   string   `json:"secret"`
		On       []string `json:"on"`
		IsActive *bool    `json:"isActive"`
	}
	if err := c.Bind(&req); err != nil || req.Name == "" || req.URL == "" {
		return c.JSON(http.StatusBadRequest, apierr.InvalidParam("Invalid parameters."))
	}
	isActive := true
	if req.IsActive != nil {
		isActive = *req.IsActive
	}
	sw := &model.SystemWebhook{
		ID:        h.idGen.Generate(time.Now()),
		Name:      req.Name,
		URL:       req.URL,
		Secret:    req.Secret,
		On:        req.On,
		IsActive:  isActive,
		UpdatedAt: time.Now(),
	}
	if err := h.systemWebhookRepo.Create(sw); err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.InternalError())
	}
	h.logModeration(c, moderationlog.LogCreateSystemWebhook, map[string]any{
		"systemWebhookId": sw.ID,
		"webhook":         sw,
	})
	return c.JSON(http.StatusOK, sw)
}

// SystemWebhookDelete handles POST /api/admin/system-webhook/delete.
func (h *Handler) SystemWebhookDelete(c echo.Context) error {
	if h.systemWebhookRepo == nil {
		return c.NoContent(http.StatusNoContent)
	}
	var req struct {
		ID string `json:"id"`
	}
	_ = c.Bind(&req)
	if req.ID == "" {
		return c.NoContent(http.StatusNoContent)
	}
	snapshot, _ := h.systemWebhookRepo.FindByID(req.ID)
	if err := h.systemWebhookRepo.Delete(req.ID); err == nil && snapshot != nil {
		h.logModeration(c, moderationlog.LogDeleteSystemWebhook, map[string]any{
			"systemWebhookId": req.ID,
			"webhook":         snapshot,
		})
	}
	return c.NoContent(http.StatusNoContent)
}

// SystemWebhookList handles POST /api/admin/system-webhook/list.
func (h *Handler) SystemWebhookList(c echo.Context) error {
	if h.systemWebhookRepo == nil {
		return c.JSON(http.StatusOK, []any{})
	}
	rows, err := h.systemWebhookRepo.List()
	if err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.InternalError())
	}
	if rows == nil {
		return c.JSON(http.StatusOK, []any{})
	}
	return c.JSON(http.StatusOK, rows)
}

// SystemWebhookShow handles POST /api/admin/system-webhook/show.
func (h *Handler) SystemWebhookShow(c echo.Context) error {
	if h.systemWebhookRepo == nil {
		return c.JSON(http.StatusNotFound, apierr.NotFound())
	}
	var req struct {
		ID string `json:"id"`
	}
	_ = c.Bind(&req)
	sw, err := h.systemWebhookRepo.FindByID(req.ID)
	if err != nil {
		return c.JSON(http.StatusNotFound, apierr.NotFound())
	}
	return c.JSON(http.StatusOK, sw)
}

// SystemWebhookTest handles POST /api/admin/system-webhook/test.
func (h *Handler) SystemWebhookTest(c echo.Context) error {
	var req struct {
		WebhookID string `json:"webhookId"`
		Type      string `json:"type"`
	}
	_ = c.Bind(&req)
	if req.WebhookID == "" || h.systemWebhookRepo == nil {
		return c.NoContent(http.StatusNoContent)
	}
	sw, err := h.systemWebhookRepo.FindByID(req.WebhookID)
	if err != nil {
		return c.NoContent(http.StatusNoContent)
	}
	// テスト送信(非同期)。配送結果は latestStatus 系カラムに反映されないが、
	// Misskey 本家の /system-webhook/test も fire-and-forget 挙動なので整合。
	go sendWebhookTest(h.webhookTestClient, sw.URL, sw.Secret, req.Type)
	return c.NoContent(http.StatusNoContent)
}

// SystemWebhookUpdate handles POST /api/admin/system-webhook/update.
//
// 配送 processor が並行して latestSentAt/latestStatus を書き換えるため、
// FindByID→Save で全列上書きすると配送ステータスを古い値で踏み潰す。partial
// update (UpdateAdminFields) を使い admin 編集可能列のみ触る。
func (h *Handler) SystemWebhookUpdate(c echo.Context) error {
	if h.systemWebhookRepo == nil {
		return c.NoContent(http.StatusNoContent)
	}
	var req struct {
		ID       string    `json:"id"`
		Name     *string   `json:"name"`
		URL      *string   `json:"url"`
		Secret   *string   `json:"secret"`
		On       *[]string `json:"on"`
		IsActive *bool     `json:"isActive"`
	}
	if err := c.Bind(&req); err != nil || req.ID == "" {
		return c.JSON(http.StatusBadRequest, apierr.InvalidParam("Invalid parameters."))
	}
	// 存在確認 (GORM Updates(map) は 0 行影響でも nil を返すため)
	before, err := h.systemWebhookRepo.FindByID(req.ID)
	if err != nil {
		return c.JSON(http.StatusNotFound, apierr.NotFound())
	}
	fields := map[string]any{"updatedAt": time.Now()}
	if req.Name != nil {
		fields["name"] = *req.Name
	}
	if req.URL != nil {
		fields["url"] = *req.URL
	}
	if req.Secret != nil {
		fields["secret"] = *req.Secret
	}
	if req.On != nil {
		// pq.StringArray でラップしないと GORM Updates(map) が空 string[] を
		// NULL 化して NOT NULL 制約違反になる (#932、#931 / #896 と同 class)。
		fields["on"] = pq.StringArray(*req.On)
	}
	if req.IsActive != nil {
		fields["isActive"] = *req.IsActive
	}
	if err := h.systemWebhookRepo.UpdateAdminFields(req.ID, fields); err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.InternalError())
	}
	sw, err := h.systemWebhookRepo.FindByID(req.ID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.InternalError())
	}
	h.logModeration(c, moderationlog.LogUpdateSystemWebhook, map[string]any{
		"systemWebhookId": req.ID,
		"before":          before,
		"after":           sw,
	})
	return c.JSON(http.StatusOK, sw)
}
