package entitycompat

import (
	"testing"
)

// roleAssigned 通知の role icon は read 時に router.go の roleNotifLookup が
// media proxy 経由へ差し替える (`internal/server/router.go` の
// `packed["iconUrl"] = entity.ProxyMediaURLPtr(r.IconURL)`)。通知欄
// (MkRolePreview) は iconUrl を `<img src>` に直接載せるので、この 1 行を
// 消すと remote origin の role icon が生のまま通知に混ざり、閲覧者の IP が
// 漏れる / CSP enforce で画像が消える (#1529)。
//
// `internal/api/notifications` のハンドラテストは自前の stub lookup を
// 注入するだけなので、router.go 側のこの行が消えても検出できない
// (#3130 review)。`internal/server` は CI のカバレッジ対象外
// (CLAUDE.md Section 4) で router を組み立てるテストも無いので、
// #2762 / #3032 と同じ形で router.go のソースを直接固定する。
func TestRoleNotificationIconIsProxied(t *testing.T) {
	assertWired(t, routerGo,
		`packed["iconUrl"] = entity.ProxyMediaURLPtr(r.IconURL)`,
		"roleAssigned 通知の role icon が生の remote URL のまま配られ、"+
			"閲覧者の IP が漏れる / CSP enforce で画像が消える (#1529)")
}

// フィード (RSS/Atom/JSON) の avatar は feedAvatarURL (`internal/server/feed.go`)
// が media proxy 経由へ差し替えるが、router.go が feedHandler の avatarURL
// フィールドへそれを渡さないと配線されない。フィードは未認証で取得でき、
// 購読者 (第三者クライアント) は remote origin を直接取得しに行く。
//
// `feedAvatarURL` 自体の単体テスト (feed_test.go) は関数を直接呼ぶだけなので、
// router.go がこの行を PR 前の (proxy を通さない) 生 URL クロージャへ戻しても
// 検出できない (#3130 review)。
func TestFeedAvatarURLIsWired(t *testing.T) {
	assertWired(t, routerGo,
		"avatarURL: feedAvatarURL",
		"フィード (RSS/Atom/JSON) の avatar が生の remote URL のまま配られ、"+
			"購読者の IP が漏れる (#1529)")
}

// reversi の stream game payload (started / update / ended) の avatar は
// router.go が `reversiService.SetAvatarProxy(entity.ProxyAvatarURLString)`
// で REST 側 (entity.PackUserLite) と同じ proxy 関数を渡すことで揃う
// (internal/core/reversi は entity パッケージに依存できないので関数を注入する
// 形。#417 の layer 規約)。
//
// `reversiService.HasAvatarProxy()` の criticalWiring nil チェックは「配線
// されているか」しか見ないので、**別のモードの proxy 関数へ差し替えても
// 素通りする** (`entity.ProxyMediaURL` (image.webp) を渡しても nil ではない
// ので起動時ゲートは緑のまま、REST 側の avatar.webp と食い違う。#3130
// review 3周目)。`TestPackGame_RESTAndStreamAgreeOnProxiedAvatar` はテスト
// 自身が `svc.SetAvatarProxy(entity.ProxyAvatarURLString)` を呼ぶので、
// router.go 側の引数違いは原理的に見えない。既存 gate と同じく引数まで
// 含めて router.go のソースを直接固定する。
func TestReversiAvatarProxyIsWired(t *testing.T) {
	assertWired(t, routerGo,
		"reversiService.SetAvatarProxy(entity.ProxyAvatarURLString)",
		"reversi stream の avatar が REST 側と別モードの proxy 関数を経由し、"+
			"同じゲームを 2 経路で見たときに avatar URL が食い違う (#1529)")
}
