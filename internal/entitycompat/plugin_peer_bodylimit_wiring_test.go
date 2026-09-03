package entitycompat

import (
	"os"
	"path/filepath"
	"regexp"
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
	// **空白は詰めて比べる。** struct literal のフィールド名は gofmt が揃えるので、
	// 隣の行を足しただけで空白の数が変わる (実際に変わって落ちた)。
	const wiring = "limiter: newPeerRateLimiter(),"
	if !strings.Contains(squeezeSpaces(strings.Join(live, "\n")), wiring) {
		t.Errorf("internal/server/router.go の pluginPeerDeps に %q が無い (コメントは数えない)。"+
			"peer の受け口が無制限になる", wiring)
	}
}

// squeezeSpaces collapses runs of spaces so gofmt's struct alignment does not
// change what these gates match.
func squeezeSpaces(s string) string {
	return regexp.MustCompile(` +`).ReplaceAllString(s, " ")
}

// catchall は名前付き関数を router から参照していないと効かない。
//
// **無名関数に戻しても build もテストも通る。** `apiCatchall` は package-level
// なので使われなくなっても go vet は咎めず、直接呼ぶテストは緑のまま。落ちるのは
// 「受け口の無いプラグインへの POST が 200 + {} に戻る」ところで、症状は
// 相手側にしか出ない (#2822)。
func TestAPICatchallIsWired(t *testing.T) {
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
	const wiring = `api.Any("/*", apiCatchall)`
	if !strings.Contains(squeezeSpaces(strings.Join(live, "\n")), wiring) {
		t.Errorf("internal/server/router.go に %q が無い (コメントは数えない)。"+
			"peer の受け口が無いプラグインへの POST が 200 + {} に戻る", wiring)
	}
}

// プラグイン専用キューの名前は driver へ渡さないと worker が見ない (#2818)。
//
// **第 3 引数を nil にしても build もテストも通る。** mkqConfig を直接叩く
// テストはあるが、newServer がその結果を渡すことは誰も見ていない。落とすと
// mkq が Define しないので、**enqueue が `unknown queue` で全部落ちる** —
// 機能が丸ごと死ぬのに緑になる。
func TestPluginJobQueuesAreWired(t *testing.T) {
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
	joined := squeezeSpaces(strings.Join(live, "\n"))
	for _, wiring := range []string{
		"pluginQueues := pluginJobQueueNames(plugins, cfg.Plugins)",
		"buildQueueDriver(context.Background(), cfg, pluginQueues)",
	} {
		if !strings.Contains(joined, wiring) {
			t.Errorf("internal/server/server.go に %q が無い (コメントは数えない)。"+
				"プラグインのキューが driver に登録されず、enqueue が unknown queue で落ちる", wiring)
		}
	}
}

// peer の送信は queue client を配線しないと積めない (#2819)。
//
// **落としても build もテストも通る。** pluginPeer は enqueuer が nil のとき
// warn を出して捨てるので、落ちるのは「送信が全部消える」ところ。相手側にしか
// 症状が出ないうえ、こちらのログを見ないと気付けない。
func TestPluginPeerEnqueuerIsWired(t *testing.T) {
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
	const wiring = "enqueuer: s.queueClient,"
	if !strings.Contains(squeezeSpaces(strings.Join(live, "\n")), wiring) {
		t.Errorf("internal/server/router.go の pluginPeerDeps に %q が無い (コメントは数えない)。"+
			"peer の送信が積まれず、全部捨てられる", wiring)
	}
}
