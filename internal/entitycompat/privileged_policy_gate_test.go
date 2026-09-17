package entitycompat

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// **`/admin/*` を policy だけで開ける key の一覧が router と一致すること (#3037)。**
//
// `internal/core/role` の `privilegedPolicyKeys` は「条件つきロールがこの
// policy を配るなら、自己付与できる条件と組み合わせてはいけない」という判定に
// 使う。router 側で `RequireRolePolicy` だけで `/admin/*` を開ける policy が
// 増えたのに一覧へ足し忘れると、**その policy 経由の自己付与が無警告で通る**。
//
// `internal/server` はカバレッジ対象外で router を組み立てるテストも無いので、
// この片側更新は build もテストも緑のまま起きる。
func TestPrivilegedPolicyKeysMatchAdminRoutes(t *testing.T) {
	routes := parseRouteRegistrations(t, filepath.Join("..", "server", "router.go"))
	require.Greater(t, len(routes), 400, "route の抽出が壊れている")

	policyRe := regexp.MustCompile(`corerole\.(Policy\w+)`)
	fromRouter := map[string]struct{}{}
	for ep, raw := range routes {
		if !strings.HasPrefix(ep, "admin/") {
			continue
		}
		reg := stripGoComments(raw)
		// **moderator / admin を併用している route は対象外。** そちらは
		// policy が無くてもロールで守られるので、policy 単独では開かない。
		if strings.Contains(reg, "RequireModerator") || strings.Contains(reg, "RequireAdmin") {
			continue
		}
		for _, m := range policyRe.FindAllStringSubmatch(reg, -1) {
			fromRouter[m[1]] = struct{}{}
		}
	}
	require.NotEmpty(t, fromRouter, "policy だけで開く admin route を 1 つも拾えていない")

	declared := privilegedPolicyKeysFromSource(t)
	var missing, stale []string
	for constName := range fromRouter {
		value := policyConstValue(t, constName)
		if _, ok := declared[value]; !ok {
			missing = append(missing, value+" ("+constName+")")
		}
	}
	for value := range declared {
		if !policyValueUsedByAdminRoute(t, value, fromRouter) {
			stale = append(stale, value)
		}
	}
	sort.Strings(missing)
	sort.Strings(stale)

	assert.Empty(t, missing,
		"policy だけで `/admin/*` を開ける key が `privilegedPolicyKeys` に無い。"+
			"足さないと、その policy を配る条件つきロールで自己付与できる")
	assert.Empty(t, stale,
		"`privilegedPolicyKeys` に、もう admin を開けない key が残っている")
}

// privilegedPolicyKeysFromSource reads the map literal from role_service.go.
//
// **ソースとして読む。** `internal/core/role` を import すると、あちらが
// `internal/entitycompat` のテスト専用依存を引き込む形になるため。
func privilegedPolicyKeysFromSource(t *testing.T) map[string]struct{} {
	t.Helper()
	src, err := os.ReadFile(filepath.Join("..", "core", "role", "role_service.go"))
	require.NoError(t, err)

	// **終端は行頭の `}`。** 素の `}` だと最初の entry の `{}` で切れて、
	// 1 件しか読めないまま「足りない」と誤報する。
	body := betweenMarkers(t, string(src), "var privilegedPolicyKeys = map[string]struct{}{", "\n}")
	out := map[string]struct{}{}
	for _, m := range regexp.MustCompile(`"([^"]+)":`).FindAllStringSubmatch(body, -1) {
		out[m[1]] = struct{}{}
	}
	require.NotEmpty(t, out, "privilegedPolicyKeys を読めていない")
	return out
}

// policyConstValue resolves `PolicyFoo` to its string literal.
func policyConstValue(t *testing.T, constName string) string {
	t.Helper()
	src, err := os.ReadFile(filepath.Join("..", "core", "role", "role_service.go"))
	require.NoError(t, err)
	m := regexp.MustCompile(constName + `\s*=\s*"([^"]+)"`).FindStringSubmatch(string(src))
	require.NotNil(t, m, "%s の値を読めない", constName)
	return m[1]
}

func policyValueUsedByAdminRoute(t *testing.T, value string, fromRouter map[string]struct{}) bool {
	t.Helper()
	for constName := range fromRouter {
		if policyConstValue(t, constName) == value {
			return true
		}
	}
	return false
}

func betweenMarkers(t *testing.T, src, start, end string) string {
	t.Helper()
	i := strings.Index(src, start)
	require.GreaterOrEqual(t, i, 0, "開始マーカー %q が無い", start)
	rest := src[i+len(start):]
	j := strings.Index(rest, end)
	require.GreaterOrEqual(t, j, 0, "終了マーカーが無い")
	return rest[:j]
}
