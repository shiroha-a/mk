// Package wellknown provides /.well-known/* discovery endpoints.
package wellknown

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/activitypub"
	"github.com/shiroha-a/mk/internal/core/user"
	"github.com/shiroha-a/mk/internal/misc/permissions"
)

// Handler handles webfinger / host-meta / nodeinfo discovery.
type Handler struct {
	urls        *activitypub.URLBuilder
	userService *user.Service
	host        string
	origin      string // scheme + host (e.g. "https://example.com")
}

// NewHandler constructs a Handler.
// origin は config.URL 相当の完全URL (例: "https://example.com")。
func NewHandler(urls *activitypub.URLBuilder, userService *user.Service, host, origin string) *Handler {
	return &Handler{urls: urls, userService: userService, host: host, origin: origin}
}

// setDiscoveryCORS sets CORS headers on discovery endpoints.
// TS本家は well-known ルートに preHandler hook で無条件に
// CORSヘッダーを設定しているため、Echo の CORS middleware (Originヘッダー
// がある場合のみ発動) とは別に明示的に付与する。
func setDiscoveryCORS(c echo.Context) {
	h := c.Response().Header()
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	h.Set("Access-Control-Allow-Headers", "Accept")
	h.Set("Access-Control-Expose-Headers", "Vary")
}

// Webfinger handles GET /.well-known/webfinger.
//
// クエリパラメータ resource は acct:username@host または actor URI 形式。
// マッチするローカルユーザーが存在しない場合は404を返す。
func (h *Handler) Webfinger(c echo.Context) error {
	resource := c.QueryParam("resource")
	if resource == "" {
		return c.NoContent(http.StatusBadRequest)
	}

	username, ok := h.parseResource(resource)
	if !ok {
		return c.NoContent(http.StatusBadRequest)
	}

	bundle, err := h.userService.ShowByUsername(username, nil)
	if err != nil {
		return c.NoContent(http.StatusNotFound)
	}

	uri := h.urls.UserURI(bundle.User.ID)
	profileURL := h.origin + "/@" + bundle.User.Username
	resp := map[string]any{
		"subject": "acct:" + bundle.User.Username + "@" + h.host,
		"links": []map[string]any{
			{
				"rel":  "self",
				"type": "application/activity+json",
				"href": uri,
			},
			{
				"rel":  "http://webfinger.net/rel/profile-page",
				"type": "text/html",
				"href": profileURL,
			},
			{
				"rel":      "http://ostatus.org/schema/1.0/subscribe",
				"template": h.origin + "/authorize-follow?acct={uri}",
			},
		},
	}
	setDiscoveryCORS(c)
	c.Response().Header().Set("Vary", "Accept")
	return c.JSON(http.StatusOK, resp)
}

// HostMeta handles GET /.well-known/host-meta.
func (h *Handler) HostMeta(c echo.Context) error {
	setDiscoveryCORS(c)
	xml := `<?xml version="1.0" encoding="UTF-8"?>
<XRD xmlns="http://docs.oasis-open.org/ns/xri/xrd-1.0">
  <Link rel="lrdd" type="application/xrd+xml" template="` + h.origin + `/.well-known/webfinger?resource={uri}"/>
</XRD>`
	return c.Blob(http.StatusOK, "application/xrd+xml; charset=utf-8", []byte(xml))
}

// NodeInfoDiscovery handles GET /.well-known/nodeinfo.
func (h *Handler) NodeInfoDiscovery(c echo.Context) error {
	setDiscoveryCORS(c)
	resp := map[string]any{
		"links": []map[string]any{
			{
				"rel":  "http://nodeinfo.diaspora.software/ns/schema/2.1",
				"href": h.origin + "/nodeinfo/2.1",
			},
			{
				"rel":  "http://nodeinfo.diaspora.software/ns/schema/2.0",
				"href": h.origin + "/nodeinfo/2.0",
			},
		},
	}
	return c.JSON(http.StatusOK, resp)
}

// HostMetaJSON handles GET /.well-known/host-meta.json.
// host-metaのJSON版。TS本家と同じレスポンスを返す。
func (h *Handler) HostMetaJSON(c echo.Context) error {
	setDiscoveryCORS(c)
	resp := map[string]any{
		"links": []map[string]any{
			{
				"rel":      "lrdd",
				"type":     "application/jrd+json",
				"template": h.origin + "/.well-known/webfinger?resource={uri}",
			},
		},
	}
	return c.JSON(http.StatusOK, resp)
}

// OAuthAuthorizationServer handles GET /.well-known/oauth-authorization-server.
// RFC 8414 OAuth 2.0 Authorization Server Metadata。
func (h *Handler) OAuthAuthorizationServer(c echo.Context) error {
	setDiscoveryCORS(c)
	resp := map[string]any{
		"issuer":                                         h.origin,
		"authorization_endpoint":                         h.origin + "/oauth/authorize",
		"token_endpoint":                                 h.origin + "/oauth/token",
		"scopes_supported":                               permissions.All,
		"response_types_supported":                       []string{"code"},
		"grant_types_supported":                          []string{"authorization_code"},
		"service_documentation":                          "https://misskey-hub.net",
		"code_challenge_methods_supported":               []string{"S256"},
		"authorization_response_iss_parameter_supported": true,
	}
	return c.JSON(http.StatusOK, resp)
}

// parseResource extracts the local username from a webfinger resource string.
// 受け付ける形式: acct:user@host, acct:user, https://host/users/<id>
func (h *Handler) parseResource(resource string) (string, bool) {
	if acct, ok := strings.CutPrefix(resource, "acct:"); ok {
		parts := strings.Split(acct, "@")
		switch len(parts) {
		case 1:
			return parts[0], true
		case 2:
			if parts[1] != h.host {
				return "", false
			}
			return parts[0], true
		}
		return "", false
	}
	if strings.HasPrefix(resource, "https://") || strings.HasPrefix(resource, "http://") {
		u, err := url.Parse(resource)
		if err != nil {
			return "", false
		}
		if u.Host != h.host {
			return "", false
		}
		// expecting /users/<id>
		parts := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
		if len(parts) == 2 && parts[0] == "users" {
			return parts[1], true
		}
		return "", false
	}
	return "", false
}
