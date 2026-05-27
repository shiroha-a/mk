package i

import (
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/api/apierr"
	"github.com/shiroha-a/mk/internal/server/middleware"
)

// registryScopeDomainRequest is the canonical input shape for registry
// read endpoints. scope は []string、domain は *string (省略可)。
type registryScopeDomainRequest struct {
	Scope  []string `json:"scope"`
	Domain *string  `json:"domain"`
}

// normalizeRegistryScope treats a nil Scope as an empty slice. JSON で
// `"scope"` が省略されると req.Scope は nil になるが、repository 側で
// pq.StringArray(nil) は SQL NULL にシリアライズされ `scope = NULL` で
// どのレコードにも一致しなくなる (registry_item のデフォルト値は
// `'{}'`)。他の registry* ハンドラ (RegistryGet / RegistryGetAll /
// RegistrySet 等) と挙動を揃えるため nil → 空配列に寄せる。
func normalizeRegistryScope(scope []string) []string {
	if scope == nil {
		return []string{}
	}
	return scope
}

// RegistryGetDetail handles POST /api/i/registry/get-detail.
// 指定の (key, scope, domain) に該当する RegistryItem を返す。
func (h *Handler) RegistryGetDetail(c echo.Context) error {
	if h.registryRepo == nil {
		return c.JSON(http.StatusOK, map[string]any{})
	}
	u := middleware.GetUser(c)
	var req struct {
		Key string `json:"key"`
		registryScopeDomainRequest
	}
	if err := c.Bind(&req); err != nil || req.Key == "" {
		return apierr.JSONInvalidParam(c)
	}
	req.Scope = normalizeRegistryScope(req.Scope)
	item, err := h.registryRepo.Get(u.ID, req.Key, req.Scope, req.Domain)
	if err != nil {
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_KEY", "No such key.", "97a1e8e7-c0f7-47d2-957a-92e61256e01a"))
	}
	return c.JSON(http.StatusOK, map[string]any{
		"updatedAt": item.UpdatedAt,
		"value":     item.Value,
		"scope":     []string(item.Scope),
		"domain":    item.Domain,
	})
}

// RegistryKeys handles POST /api/i/registry/keys.
// 指定 scope+domain 下の key 一覧を返す (本家互換で配列)。
func (h *Handler) RegistryKeys(c echo.Context) error {
	if h.registryRepo == nil {
		return c.JSON(http.StatusOK, []any{})
	}
	u := middleware.GetUser(c)
	var req registryScopeDomainRequest
	_ = c.Bind(&req)
	req.Scope = normalizeRegistryScope(req.Scope)
	keysMap, err := h.registryRepo.KeysWithType(u.ID, req.Scope, req.Domain)
	if err != nil {
		return apierr.JSONInternalError(c)
	}
	keys := make([]string, 0, len(keysMap))
	for k := range keysMap {
		keys = append(keys, k)
	}
	return c.JSON(http.StatusOK, keys)
}

// RegistryScopesWithDomain handles POST /api/i/registry/scopes-with-domain.
// ユーザーが保存している (scope, domain) の distinct 一覧を返す。
func (h *Handler) RegistryScopesWithDomain(c echo.Context) error {
	if h.registryRepo == nil {
		return c.JSON(http.StatusOK, []any{})
	}
	u := middleware.GetUser(c)
	pairs, err := h.registryRepo.ScopesWithDomain(u.ID)
	if err != nil {
		return apierr.JSONInternalError(c)
	}
	out := make([]map[string]any, 0, len(pairs))
	for _, p := range pairs {
		out = append(out, map[string]any{
			"scope":  p.Scope,
			"domain": p.Domain,
		})
	}
	return c.JSON(http.StatusOK, out)
}
