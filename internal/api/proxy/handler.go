package proxy

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/config"
	"github.com/shiroha-a/mk/internal/core/mediaproxy"
)

// Handler handles the /proxy/* media proxy endpoint.
type Handler struct {
	service *mediaproxy.Service
	config  *config.Config
}

// NewHandler creates a new proxy Handler.
func NewHandler(service *mediaproxy.Service, cfg *config.Config) *Handler {
	return &Handler{service: service, config: cfg}
}

// Handle processes GET /proxy/* requests.
//
// URL extraction:
//   - Query param ?url=X takes priority
//   - Otherwise, path after /proxy/ is treated as the URL (prefixed with "https://")
//
// Query params: emoji, avatar, static, preview, badge, origin, fallback, sig
func (h *Handler) Handle(c echo.Context) error {
	rawURL := c.QueryParam("url")
	if rawURL == "" {
		// パスからURLを取り出す: /proxy/image.webp → 無効、/proxy/example.com/img.png → https://example.com/img.png
		pathAfterProxy := strings.TrimPrefix(c.Request().URL.Path, "/proxy/")
		// image.webp, preview.webp, static.webp 等のファイル名パターンはスキップ
		if pathAfterProxy != "" && !isProxyFilename(pathAfterProxy) {
			rawURL = "https://" + pathAfterProxy
		}
	}

	if rawURL == "" {
		return c.NoContent(http.StatusBadRequest)
	}

	// 外部プロキシへのリダイレクト
	origin := c.QueryParam("origin")
	if h.config.ExternalMediaProxyEnabled && origin == "" {
		return h.redirectToExternalProxy(c, rawURL)
	}

	// User-Agent検証
	ua := c.Request().Header.Get("User-Agent")
	if ua == "" {
		return c.String(http.StatusBadRequest, "User-Agent is required")
	}
	// #2106 L43: mk-go の outbound UA は "mk-go/" 始まり (#774 で Misskey/ から rename)。
	// upstream の "misskey/" 判定だけだと自身の proxy 経由リクエストを recursive 検出できず
	// loop/増幅防御が効かないため両方を見る。
	lowerUA := strings.ToLower(ua)
	if strings.Contains(lowerUA, "misskey/") || strings.Contains(lowerUA, "mk-go/") {
		return c.String(http.StatusForbidden, "Proxy is recursive")
	}

	// 認可: HMAC署名 or allowlist
	sig := c.QueryParam("sig")
	if err := h.service.Authorize(c.Request().Context(), rawURL, sig); err != nil {
		if c.QueryParam("fallback") != "" {
			return h.serveFallback(c)
		}
		c.Response().Header().Set("Cache-Control", "max-age=86400")
		return c.NoContent(http.StatusForbidden)
	}

	// 処理モード + 出力フォーマット決定 (#637 M3)
	mode := parseMode(c)
	out := parseOutputFormat(c)

	// Fetch + 画像処理
	result, err := h.service.Fetch(c.Request().Context(), rawURL, mode, out)
	if err != nil {
		if errors.Is(err, mediaproxy.ErrNotFound) {
			if c.QueryParam("fallback") != "" {
				return h.serveFallback(c)
			}
			c.Response().Header().Set("Cache-Control", "max-age=86400")
			return c.NoContent(http.StatusNotFound)
		}
		if errors.Is(err, mediaproxy.ErrTooLarge) {
			return c.NoContent(http.StatusRequestEntityTooLarge)
		}
		if c.QueryParam("fallback") != "" {
			return h.serveFallback(c)
		}
		c.Response().Header().Set("Cache-Control", "max-age=300")
		return c.NoContent(http.StatusInternalServerError)
	}
	defer result.Body.Close()

	c.Response().Header().Set("Cache-Control", "max-age=31536000, immutable")
	c.Response().Header().Set("Content-Type", result.ContentType)
	// Output format depends on the client's Accept header (image/avif → AVIF,
	// otherwise WebP), so shared caches MUST key on Accept to avoid serving
	// AVIF to a Safari 15 / WebP-only client cached behind a CDN, and
	// vice-versa (#637 review UR-012).
	c.Response().Header().Set("Vary", "Accept")
	c.Response().WriteHeader(http.StatusOK)
	_, _ = io.Copy(c.Response(), result.Body)
	return nil
}

// redirectToExternalProxy sends a 301 redirect to the configured external proxy.
func (h *Handler) redirectToExternalProxy(c echo.Context, rawURL string) error {
	pathAfterProxy := strings.TrimPrefix(c.Request().URL.Path, "/proxy/")
	u, err := url.Parse(h.config.MediaProxy + "/" + pathAfterProxy)
	if err != nil {
		return c.NoContent(http.StatusInternalServerError)
	}

	q := u.Query()
	// 元リクエストのクエリパラメータを全て転送
	for key, vals := range c.QueryParams() {
		for _, val := range vals {
			q.Set(key, val)
		}
	}
	// urlが未設定の場合はセット
	if q.Get("url") == "" {
		q.Set("url", rawURL)
	}
	u.RawQuery = q.Encode()

	c.Response().Header().Set("Cache-Control", "public, max-age=259200")
	return c.Redirect(http.StatusMovedPermanently, u.String())
}

// serveFallback returns a 1x1 transparent PNG with short cache.
func (h *Handler) serveFallback(c echo.Context) error {
	dummy := mediaproxy.DummyPNG()
	defer dummy.Body.Close()
	c.Response().Header().Set("Cache-Control", "max-age=300")
	data, _ := io.ReadAll(dummy.Body)
	return c.Blob(http.StatusOK, dummy.ContentType, data)
}

// parseMode determines the processing mode from query parameters.
func parseMode(c echo.Context) mediaproxy.ProxyMode {
	if c.QueryParam("emoji") != "" {
		return mediaproxy.ModeEmoji
	}
	if c.QueryParam("avatar") != "" {
		return mediaproxy.ModeAvatar
	}
	if c.QueryParam("static") != "" {
		return mediaproxy.ModeStatic
	}
	if c.QueryParam("preview") != "" {
		return mediaproxy.ModePreview
	}
	if c.QueryParam("badge") != "" {
		return mediaproxy.ModeBadge
	}
	return mediaproxy.ModeDefault
}

// parseOutputFormat picks the encoder format from `?avif=1` (explicit opt-in,
// matches the existing `?static=1` style flags) or the Accept header (treats
// `image/avif` as a non-zero quality preference). Falls back to WebP.
//
// AVIF は CPU 重めなので「ブラウザが本当に使う」ケースだけに絞る。Misskey
// TS の sharp().avif() 経路と同じ semantics。
func parseOutputFormat(c echo.Context) mediaproxy.OutputFormat {
	if c.QueryParam("avif") != "" {
		return mediaproxy.FormatAVIF
	}
	if acceptsAVIF(c.Request().Header.Get("Accept")) {
		return mediaproxy.FormatAVIF
	}
	return mediaproxy.FormatWebP
}

// acceptsAVIF returns true when the client signalled non-zero preference for
// `image/avif` in its Accept header.
func acceptsAVIF(accept string) bool {
	for _, part := range strings.Split(accept, ",") {
		entry := strings.TrimSpace(part)
		if entry == "" {
			continue
		}
		mt, params := splitAcceptEntry(entry)
		if !strings.EqualFold(mt, "image/avif") {
			continue
		}
		if params["q"] == "0" || params["q"] == "0.0" || params["q"] == "0.00" {
			return false
		}
		return true
	}
	return false
}

func splitAcceptEntry(entry string) (string, map[string]string) {
	parts := strings.Split(entry, ";")
	mt := strings.TrimSpace(parts[0])
	params := map[string]string{}
	for _, p := range parts[1:] {
		kv := strings.SplitN(strings.TrimSpace(p), "=", 2)
		if len(kv) == 2 {
			params[strings.ToLower(strings.TrimSpace(kv[0]))] = strings.TrimSpace(kv[1])
		}
	}
	return mt, params
}

// isProxyFilename returns true if the path segment looks like a proxy output
// filename (e.g., "image.webp", "preview.webp", "static.webp", "emoji.webp").
func isProxyFilename(path string) bool {
	for _, name := range []string{
		"image.webp", "preview.webp", "static.webp",
		"emoji.webp", "emoji.png", "avatar.webp",
	} {
		if path == name {
			return true
		}
	}
	return false
}
