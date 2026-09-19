package entitycompat

import (
	"testing"
)

// `admin/ip/accounts` (#3104) は router が検索用 repository を渡さないと動かない。
// handler は未配線を 500 で返すので**黙って「候補なし」にはならない**が、
// `internal/server` は CI のカバレッジ対象外 (CLAUDE.md Section 4) で router を
// 組み立てるテストも無いため、配線を落としても build もテストも全部緑になる。
// #2762 (`WireMetaToggles`) / #3053 と同じ形で router.go の呼び出しを固定する。
func TestIPAccountSearchRepoIsWired(t *testing.T) {
	assertWired(t, routerGo,
		"adminHandler.SetIPSearchRepo(repository.NewUserIPSearchRepository(s.db))",
		"admin/ip/accounts が 500 を返し、IP からの関連アカウント検索が使えなくなる (#3104)")
}

// route の登録と**権限の 3 段**を固定する。
//
// **`perm-check` は見てくれない。** あちらは upstream の golden に載っている
// endpoint だけを突き合わせるので、mk-go 独自の endpoint は「実装されていない」
// 扱いで黙って skip される。つまりこの endpoint の認証段は、ここでしか
// 固定されていない。
//
// **`RequireRolePolicy` の 1 行が落ちると、既定でモデレーター全員に開く。**
// upstream の `admin/get-user-ips` は `requireAdmin: true` なので、それは同じ
// 機密情報に対して既存より緩い経路を黙って新設することになる (#3104)。しかも
// 落ちても build は通り、モデレーターから見ると「使えるようになった」だけで
// 誰も異常に気付かない。引数まで照合するので、policy 名を別のものに差し替える
// 形も落ちる。
func TestIPAccountSearchRouteIsRegistered(t *testing.T) {
	assertWired(t, routerGo,
		`api.POST("/admin/ip/accounts", adminHandler.IPAccounts, `+
			`middleware.RequireModerator(roleService), `+
			`middleware.RequireRolePolicy(roleService, corerole.PolicyCanSearchIpHistory), `+
			`middleware.RequireScope("read:admin:user-ips"))`,
		"admin/ip/accounts が 404 になる、あるいは管理者限定の既定が外れて\n"+
			"モデレーター全員が IP から関連アカウントを引けるようになる (#3104)")
}
