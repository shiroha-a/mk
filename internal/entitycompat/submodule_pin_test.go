package entitycompat

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// **fork frontend の pin が doc と食い違ったまま緑になるのを止める。**
//
// `third_party/misskey` に commit して fork へ push したあと、**親リポの
// gitlink を上げ忘れる**片側更新が実際に起きた (#2963)。doc には新しい tag を
// 書き、fork の branch と tag も push 済みなのに gitlink だけ古い、という状態で
// **CI 28 チェックが全て緑のままマージされた** (#2965 で解消)。
//
// 実害の経路もある。`Makefile` の `REVISION_LDFLAGS` は
// `git -C third_party/misskey describe --tags` で `MkGoFrontendVersion` を作る
// が、これは **submodule の working tree** を見るので、gitlink が遅れている窓に
// develop からビルドしたバイナリは古い tag を名乗りつつ doc は新しい tag を
// 書いている、という食い違いになる。
//
// **SHA で突き合わせるのが要点。** doc に書いてあるのは tag 名なので、tag から
// SHA を解くには submodule の checkout が要る。それだと `make gates` (submodule
// 不要が前提) に入れられず、`frontend-check` でしか回せない。pin 行に短縮 SHA を
// 併記して**親リポだけで完結**させると、submodule 未初期化の worktree でも
// `git ls-tree HEAD third_party/misskey` が gitlink を返すので `make gates` に
// 載る (実測で確認した)。
//
// **tag 名そのものの正しさはここでは見ない。** それは submodule が要るので
// 別の場所 (`frontend-check` 側) の仕事。ここが守るのは「doc に書いた pin と
// 実際の gitlink が同じ commit を指しているか」だけ。
var submodulePinRe = regexp.MustCompile("\\*\\*現在の pin は `([^`]+)` \\(`([0-9a-f]{7,40})`\\)")

const submodulePath = "third_party/misskey"

func TestSubmodulePinMatchesDoc(t *testing.T) {
	root := repoRoot(t)

	docPath := filepath.Join(root, "docs", "divergence.md")
	src, err := os.ReadFile(docPath)
	require.NoError(t, err)

	// 拾えなかったら落とす。書式が変わって正規表現が空振りすると、検査して
	// いないのに緑になる (#2874 / #2828 と同じ判断)。
	m := submodulePinRe.FindSubmatch(src)
	require.NotNilf(t, m, "docs/divergence.md に pin 行が見つからない。\n"+
		"書式は **現在の pin は `<tag>` (`<短縮 SHA>`)。** で、SHA は gitlink と\n"+
		"突き合わせるためにある。変えるならこのゲートも直すこと")
	docTag, docSHA := string(m[1]), string(m[2])

	out, err := exec.Command("git", "-C", root, "ls-tree", "HEAD", submodulePath).Output()
	require.NoErrorf(t, err, "git ls-tree に失敗した (git リポジトリではない?)")

	// `160000 commit <sha>\t<path>` の 4 フィールド。
	fields := strings.Fields(strings.TrimSpace(string(out)))
	require.Lenf(t, fields, 4, "git ls-tree の出力を読めない: %q", string(out))
	require.Equalf(t, "commit", fields[1],
		"%s が gitlink ではない (submodule を外した?)", submodulePath)
	gitSHA := fields[2]

	require.Truef(t, strings.HasPrefix(gitSHA, docSHA),
		"doc の pin と gitlink が食い違う。\n"+
			"  docs/divergence.md : %s (%s)\n"+
			"  gitlink            : %s\n"+
			"submodule に commit したら、**親リポの pointer も同じ PR で上げる**こと。\n"+
			"fork へ push してから `git add %s` する (逆順だと CI の checkout が\n"+
			"not our ref で落ちる)。", docTag, docSHA, gitSHA, submodulePath)
}
