package entitycompat

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// #2762 は「`meta.enableFanoutTimelineDbFallback` の列も admin 公開もあるのに、
// 読み取り経路へ配線されていない」という穴だった。配線は
// `coretimeline.WireMetaToggles` に集約してあるが、**router からの呼び出しごと
// 消しても build は通り、テストも全部緑になる**。`internal/server` は CI の
// カバレッジ対象外 (CLAUDE.md Section 4) で、router を組み立てるテストも無い。
//
// そこで同 package の他の gate (limit_specs / secure_endpoints / permissions) と
// 同じく router.go をソースとして読み、呼び出しが残っていることを固定する。
//
// **文字列一致で十分。** ここが守りたいのは「配線が書かれていること」だけで、
// 何が配線されるかは `internal/core/timeline` の TestWireMetaToggles_Wires* が
// setter ごとに変異検証付きで固定している。
func TestTimelineTogglesAreWired(t *testing.T) {
	path := filepath.Join(repoRoot(t), "internal/server/router.go")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read router.go: %v", err)
	}
	const call = "coretimeline.WireMetaToggles("
	// コメント行は数えない。router.go は gofmt 済みなので行頭 (indent 除去後) が
	// `//` かどうかで足りる。コメントアウトして残すのは「消す」のと同じ。
	found := false
	for _, line := range strings.Split(string(src), "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "//") {
			continue
		}
		if strings.Contains(t, call) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("internal/server/router.go に %q の呼び出しが無い (コメントは数えない)。"+
			"FTT のトグル (enableFanoutTimeline / enableFanoutTimelineDbFallback) が "+
			"admin から切り替えても効かなくなる (#2762)", call)
	}
}
