package entitycompat

import (
	"testing"
)

// securityHeaderMiddlewares must stay wired in the global chain.
//
// **配線を消しても build もテストも通る。** `internal/server` は CI のカバレッジ
// 対象外で (CLAUDE.md Section 4)、server.go を組み立てるテストも無い。header が
// 静かに消えても気付けないので、#2762 の `TestTimelineTogglesAreWired` と同じく
// ソースとして読んで固定する。
//
// ここに並べるのは**全レスポンスに掛ける global middleware だけ**。route ごとに
// 付ける header (drive の CSP 等) は handler 側のテストが見る。
// **`e.Use(` まで含めて照合する。** middleware 名だけを見ると
// `e.Group("/files").Use(middleware.NoSniff())` のように **route ごとに戻す退行**
// が通り抜ける — それはこの gate が防ぎたい漏れそのもの。
//
// **条件付きの配線は検出できない** (`if cond { e.Use(...) }` は素通りする)。
// ソースを読む gate 共通の限界。囲み方には依存しない — 判定は `wiringNodes` の
// AST 照合なので、`//` でも `/* */` でも落ちる (#2856)。
var securityHeaderMiddlewares = []string{
	"e.Use(middleware.FrameGuard())",                      // X-Frame-Options (upstream 相当)
	"e.Use(middleware.ReferrerPolicy())",                  // Referrer-Policy (#2404、mk-go 独自)
	"e.Use(middleware.HSTS(cfg.URL, cfg.DisableHSTS))",    // Strict-Transport-Security (upstream 相当)
	"e.Use(middleware.NoSniff())",                         // X-Content-Type-Options (#2782、mk-go 独自)
	"e.Use(middleware.PermissionsPolicy())",               // Permissions-Policy (#2782、mk-go 独自)
	"e.Use(middleware.COOP(cfg.CrossOriginOpenerPolicy))", // Cross-Origin-Opener-Policy (既定 off)
}

// #2782: `X-Content-Type-Options` は drive のファイル配信とプラグイン proxy に
// しか付いておらず、SPA shell も API も素通しだった。global へ移したので、
// 配線が外れたら落ちるようにする。
func TestSecurityHeadersAreWired(t *testing.T) {
	for _, mw := range securityHeaderMiddlewares {
		assertWired(t, serverGo, mw,
			"security header が全レスポンスから静かに消える (#2782)")
	}
}
