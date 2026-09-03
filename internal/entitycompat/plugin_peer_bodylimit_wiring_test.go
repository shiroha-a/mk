package entitycompat

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// peer の受け口の本文上限は、newServer が BodyLimitByPath へ表を渡さないと効かない。
//
// **第 2 引数を nil にしても build もテストも通る。** middleware 側のテストは表を
// 自分で組み立てて渡すので、呼び出し側を見ていない。落とすと全プラグインの受け口が
// `/api` の 1 MiB に戻り、**宣言した上限が黙って無意味になる** — 署名を検証する前に
// 読める本文が 16 倍になるので、気付ける症状は出ない。
//
// 判定はソースの文字列一致で、#2812 の `TestInviteModeratorCheckerIsWired` と同じ形。
// **コメント行は数えない** (コメントアウトして残すのは消すのと同じ)。
//
// **引数まで含めて照合する。** 関数名だけを見ると `BodyLimitByPath(x, nil)` が
// 通り抜けて、上限が死んだまま緑になる。
func TestPluginPeerBodyLimitIsWired(t *testing.T) {
	path := filepath.Join(repoRoot(t), "internal/server/server.go")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read server.go: %v", err)
	}
	var live []string
	for _, line := range strings.Split(string(src), "\n") {
		if trimmed := strings.TrimSpace(line); !strings.HasPrefix(trimmed, "//") {
			live = append(live, trimmed)
		}
	}
	const wiring = "middleware.BodyLimitByPath(cfg.MaxFileSize, peerBodyLimitsByPath(plugins, cfg.Plugins))"
	if !strings.Contains(strings.Join(live, "\n"), wiring) {
		t.Errorf("internal/server/server.go に %q の配線が無い (コメントは数えない)。"+
			"プラグインごとの peer 本文上限が /api の 1 MiB に戻る", wiring)
	}
}

// peer のレート制限は deps に limiter が入っていないと丸ごと効かない。
//
// **nil は「全部通す」。** テストが組み立てる deps は limiter を持たないので
// nil 許容にしてあり、落としても build もテストも通る。落ちるのは
// 「認証を通った相手 (と署名を持たない相手) が受け口を無制限に叩ける」状態で、
// 症状が出るのは攻撃を受けたときだけ。
func TestPluginPeerRateLimiterIsWired(t *testing.T) {
	path := filepath.Join(repoRoot(t), "internal/server/router.go")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read router.go: %v", err)
	}
	var live []string
	for _, line := range strings.Split(string(src), "\n") {
		if trimmed := strings.TrimSpace(line); !strings.HasPrefix(trimmed, "//") {
			live = append(live, trimmed)
		}
	}
	const wiring = "limiter:  newPeerRateLimiter(),"
	if !strings.Contains(strings.Join(live, "\n"), wiring) {
		t.Errorf("internal/server/router.go の pluginPeerDeps に %q が無い (コメントは数えない)。"+
			"peer の受け口が無制限になる", wiring)
	}
}
