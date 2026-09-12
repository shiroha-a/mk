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
// **本当の実害は「入れたつもりの frontend の修正が develop のビルドに入って
// いない」こと。** develop を clone して submodule を取れば working tree は
// gitlink (= 古い方) に置かれるので、`Makefile` の `REVISION_LDFLAGS` が
// `git -C third_party/misskey describe --tags` で作る `MkGoFrontendVersion` は
// **古い tag を正しく報告する**。嘘をついているのは doc の側で、version 文字列は
// その食い違いが露見する症状にすぎない。
//
// **SHA で突き合わせるのが要点。** doc に書いてあるのは tag 名だが、tag から
// SHA を解くには**ネットワーク** (`git ls-remote`) か submodule の checkout が
// 要る。`make gates` はどちらも前提にできないので、pin 行に短縮 SHA を併記して
// **親リポだけで完結**させた。submodule 未初期化の worktree でも
// `git ls-files -s -- third_party/misskey` が gitlink を返すので `make gates` に
// 載る (実測で確認した)。
//
// **tag 名そのものの正しさはここでは見ない。** #2963 の事故は「tag だけ直して
// gitlink を忘れた」形だったので、**SHA 側を書き換え忘れるとここは素通りする**。
// それを塞ぐのは CI の `build` job の `Check submodule commit is pushed` step で、
// `git ls-remote` が tag → commit を解いて gitlink と突き合わせる (実測 1.0 秒)。
// ここが守るのは「doc に書いた SHA と実際の gitlink が同じ commit か」だけ。
var submodulePinRe = regexp.MustCompile("\\*\\*現在の pin は `([^`]+)` \\(`([0-9a-f]{7,40})`\\)")

const submodulePath = "third_party/misskey"

func TestSubmodulePinMatchesDoc(t *testing.T) {
	root := repoRoot(t)

	docPath := filepath.Join(root, "docs", "divergence.md")
	src, err := os.ReadFile(docPath)
	require.NoError(t, err)

	// 拾えなかったら落とす。書式が変わって正規表現が空振りすると、検査して
	// いないのに緑になる (#2874 / #2828 と同じ判断)。
	//
	// **ちょうど 1 件であることも要求する。** `FindSubmatch` で最初の一致だけを
	// 採ると、前方に書式の例を書いた瞬間に本物の pin 行が検査対象から外れる
	// (このゲートの失敗メッセージ自身が書式を提示するので、doc へ写す動機がある)。
	ms := submodulePinRe.FindAllSubmatch(src, -1)
	require.Lenf(t, ms, 1, "docs/divergence.md の pin 行が %d 件。ちょうど 1 件であること。\n"+
		"書式は **現在の pin は `<tag>` (`<短縮 SHA>`)。** で、SHA は gitlink と\n"+
		"突き合わせるためにある。変えるならこのゲートも直すこと", len(ms))
	docTag, docSHA := string(ms[0][1]), string(ms[0][2])

	// **index から読む。** doc は working tree (`os.ReadFile`) から読むので、
	// gitlink を `ls-tree HEAD` (= 直前の commit) から読むと**読み元が非対称**に
	// なり、submodule を `git add` して doc も直した**コミット直前の状態で必ず
	// 落ちる**。しかも診断が「`git add` しろ」= もう済ませた操作を指示する。
	// `make check` はコミット前に回す決まりなので、bump のたびに踏む
	// (実測: 直近 30 commit のうち 13 が gitlink を動かしている)。
	// CI は checkout 直後で index == HEAD なので、検査は弱まらない。
	cmd := exec.Command("git", "-C", root, "ls-files", "-s", "--", submodulePath)
	// 呼び出し元の git 環境を持ち込まない (hook 経由などで別リポジトリを指す
	// GIT_DIR が立っていると、ls-files が空を返して診断が実態とずれる)。
	cmd.Env = filterGitEnv(os.Environ())
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	require.NoErrorf(t, err, "git ls-files に失敗した (git リポジトリではない?): %v\n%s", err, stderr.String())

	// `160000 <sha> <stage>\t<path>` の 4 フィールド。
	fields := strings.Fields(strings.TrimSpace(string(out)))
	require.Lenf(t, fields, 4, "git ls-files の出力を読めない: %q (stderr: %s)", string(out), stderr.String())
	require.Equalf(t, "160000", fields[0],
		"%s が gitlink ではない (submodule を外した?)", submodulePath)
	gitSHA := fields[1]

	require.Truef(t, strings.HasPrefix(gitSHA, docSHA),
		"doc の pin と gitlink が食い違う。\n"+
			"  docs/divergence.md : %s (%s)\n"+
			"  gitlink            : %s\n"+
			"submodule に commit したら、**親リポの pointer も同じ PR で上げる**こと。\n"+
			"fork へ push してから `git add %s` する (逆順だと CI の checkout が\n"+
			"not our ref で落ちる)。", docTag, docSHA, gitSHA, submodulePath)
}

// filterGitEnv drops git's environment overrides so that the command always
// resolves against the repository at -C.
func filterGitEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		switch {
		case strings.HasPrefix(kv, "GIT_DIR="),
			strings.HasPrefix(kv, "GIT_WORK_TREE="),
			strings.HasPrefix(kv, "GIT_INDEX_FILE="):
			continue
		}
		out = append(out, kv)
	}
	return out
}

// **pin 行の tag が §4-2 の表の最終行と一致するか。**
//
// 上の `TestSubmodulePinMatchesDoc` は SHA しか見ないので、**tag 名だけが古い
// まま残る形**は素通りする (#2963 の事故はまさにその形だった)。それを確実に
// 捕まえるのは CI の `git ls-remote` だが、doc 内の整合だけならネットワーク
// 無しで取れる — §4-2 の表は「fork の変更を 1 行 1 tag で並べたもの」で、
// `TestDivergenceDoc_ForkFrontendTagsMatchTable` がサマリと件数・範囲を
// 縛っている。pin 行をその輪に入れておくと、tag を足して pin 行を直し忘れた
// (または逆) のを `make gates` の時点で気付ける。
func TestSubmodulePinTagMatchesTable(t *testing.T) {
	lines := readDivergenceDoc(t)

	var pinTag string
	for _, line := range lines {
		if m := submodulePinRe.FindStringSubmatch(line); m != nil {
			pinTag = m[1]
			break
		}
	}
	require.NotEmptyf(t, pinTag, "docs/divergence.md に pin 行が見つからない "+
		"(書式は **現在の pin は `<tag>` (`<短縮 SHA>`)。**)")

	start, _ := findDivergenceHeading(t, lines, "4-2")
	var tags []string
	for _, line := range sectionLines(lines, start) {
		if m := forkTagRowRe.FindStringSubmatch(line); m != nil {
			tags = append(tags, m[1]+"-mk."+m[2])
		}
	}
	require.NotEmptyf(t, tags, "docs/divergence.md §4-2 に tag の行が無い")

	last := tags[len(tags)-1]
	require.Equalf(t, last, pinTag,
		"pin 行の tag (%s) が §4-2 の表の最終行 (%s) と違う。\n"+
			"tag を足したら pin 行も直すこと (逆も同じ)。SHA の側は "+
			"TestSubmodulePinMatchesDoc が gitlink と突き合わせる", pinTag, last)
}
