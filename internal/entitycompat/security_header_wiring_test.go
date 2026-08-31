package entitycompat

import (
	"os"
	"path/filepath"
	"strings"
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
// 文字列一致の限界で、#2762 の `TestTimelineTogglesAreWired` も同じ。
var securityHeaderMiddlewares = []string{
	"e.Use(middleware.FrameGuard(",        // X-Frame-Options (upstream 相当)
	"e.Use(middleware.ReferrerPolicy(",    // Referrer-Policy (#2404、mk-go 独自)
	"e.Use(middleware.HSTS(",              // Strict-Transport-Security (upstream 相当)
	"e.Use(middleware.NoSniff(",           // X-Content-Type-Options (#2782、mk-go 独自)
	"e.Use(middleware.PermissionsPolicy(", // Permissions-Policy (#2782、mk-go 独自)
	"e.Use(middleware.COOP(",              // Cross-Origin-Opener-Policy (既定 off)
}

// #2782: `X-Content-Type-Options` は drive のファイル配信とプラグイン proxy に
// しか付いておらず、SPA shell も API も素通しだった。global へ移したので、
// 配線が外れたら落ちるようにする。
func TestSecurityHeadersAreWired(t *testing.T) {
	path := filepath.Join(repoRoot(t), "internal/server/server.go")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read server.go: %v", err)
	}
	// コメント行は数えない。コメントアウトして残すのは消すのと同じ。
	var live []string
	for _, line := range strings.Split(string(src), "\n") {
		if t := strings.TrimSpace(line); !strings.HasPrefix(t, "//") {
			live = append(live, t)
		}
	}
	body := strings.Join(live, "\n")

	for _, mw := range securityHeaderMiddlewares {
		if !strings.Contains(body, mw) {
			t.Errorf("internal/server/server.go に %q の配線が無い (コメントは数えない)。"+
				"security header が全レスポンスから静かに消える (#2782)", mw)
		}
	}
}
