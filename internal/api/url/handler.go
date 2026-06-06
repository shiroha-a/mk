// Package url provides the /api/url endpoint for URL preview.
package url

import (
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/api/apierr"
	"github.com/shiroha-a/mk/internal/core/urlpreview"
	"github.com/shiroha-a/mk/internal/entity"
)

// Handler handles the URL preview endpoint.
type Handler struct {
	fetcher *urlpreview.Fetcher
}

// NewHandler creates a new URL preview handler.
func NewHandler(fetcher *urlpreview.Fetcher) *Handler {
	return &Handler{fetcher: fetcher}
}

// Preview handles GET /url (matches Misskey's endpoint path).
// フロントエンドがリンクプレビューカード表示のために呼ぶ。
func (h *Handler) Preview(c echo.Context) error {
	rawURL := c.QueryParam("url")
	if rawURL == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "url is required.", apierr.UUIDInvalidParam))
	}

	result, err := h.fetcher.Fetch(c.Request().Context(), rawURL)
	if err != nil {
		return c.JSON(http.StatusOK, map[string]any{
			"title":       nil,
			"description": nil,
			"thumbnail":   nil,
			"icon":        nil,
			"sitename":    nil,
			"url":         rawURL,
			"player":      map[string]any{"url": nil, "width": nil, "height": nil, "allow": []string{}},
			"sensitive":   false,
			"activityPub": nil,
		})
	}

	// thumbnail (OGP og:image) / icon (favicon) は外部サイトの画像。frontend
	// (MkUrlPreview) がこれらを直接描画するため、proxy 経由に書き換えて閲覧者の
	// IP が外部サイトへ漏洩するのを防ぐ (issue #1529)。player.url は iframe embed
	// なので対象外。
	result.Thumbnail = entity.ProxyRemoteMediaURLPtr(result.Thumbnail, "")
	result.Icon = entity.ProxyRemoteMediaURLPtr(result.Icon, "")
	return c.JSON(http.StatusOK, result)
}
