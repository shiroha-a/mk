package i

import (
	"net/http"
	"regexp"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/api/apierr"
	"github.com/shiroha-a/mk/internal/server/middleware"
)

// registryScopeElemRe mirrors upstream registry paramDef scope items pattern
// ^[a-zA-Z0-9_]+$ (#1546)。各 registry endpoint で scope 要素を検証する。
var registryScopeElemRe = regexp.MustCompile(`^[a-zA-Z0-9_]+$`)

// validRegistryScope reports whether every scope element matches the upstream
// pattern. Empty scope ([]) is valid.
func validRegistryScope(scope []string) bool {
	for _, s := range scope {
		if !registryScopeElemRe.MatchString(s) {
			return false
		}
	}
	return true
}

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
	if !validRegistryScope(req.Scope) {
		return apierr.JSONInvalidParam(c)
	}
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
	if !validRegistryScope(req.Scope) {
		return apierr.JSONInvalidParam(c)
	}
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
	// upstream getAllScopeAndDomains: domain ごとに集約し scopes は string[][]
	// (1 domain につき複数 scope を束ねる、#1546)。repo は DISTINCT (scope,domain)
	// を返すので domain 内の scope は既に一意。domain は null (main 空間) を保つ。
	type scopeEntry struct {
		Domain *string    `json:"domain"`
		Scopes [][]string `json:"scopes"`
	}
	out := make([]*scopeEntry, 0, len(pairs))
	byDomain := make(map[string]*scopeEntry, len(pairs))
	for _, p := range pairs {
		// nil domain と空文字 domain を区別するため sentinel を付与する。
		key := "\x00nil"
		if p.Domain != nil {
			key = "d:" + *p.Domain
		}
		sc := p.Scope
		if sc == nil {
			sc = []string{}
		}
		if e, ok := byDomain[key]; ok {
			e.Scopes = append(e.Scopes, sc)
		} else {
			e := &scopeEntry{Domain: p.Domain, Scopes: [][]string{sc}}
			byDomain[key] = e
			out = append(out, e)
		}
	}
	return c.JSON(http.StatusOK, out)
}
