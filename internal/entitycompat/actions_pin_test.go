package entitycompat

import (
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	// usesLineRe captures the value of a `uses:` key in a workflow file.
	//
	// step の先頭 (`- uses:`) と、job 直下の reusable workflow 呼び出し
	// (`uses:`) の両方を拾う。
	usesLineRe = regexp.MustCompile(`^\s*(?:-\s+)?uses:\s*(.+?)\s*$`)

	// pinnedActionRe accepts `owner/repo[/path]@<40 hex> # vX.Y.Z`.
	//
	// **版のコメントも要求する。** SHA だけだと「どの版を固定したのか」が
	// 読めず、dependabot の PR も SHA の差分しか示さないので、更新時に
	// リリースノートと突き合わせられない。
	pinnedActionRe = regexp.MustCompile(
		`^[A-Za-z0-9_.-]+/[A-Za-z0-9_./-]+@[0-9a-f]{40}\s+#\s*v\d+\.\d+\.\d+\S*$`)
)

// TestWorkflowActionsArePinnedToSHA checks that every third-party action and
// reusable workflow referenced from .github/workflows is pinned to a full
// commit SHA with a `# vX.Y.Z` comment.
//
// **tag は付け替えられる。** `actions/checkout@v7` のような参照は、その
// リポジトリ (あるいは乗っ取った第三者) が tag を別 commit へ動かした瞬間に
// 中身が変わる。`docker.yml` / `build-with-plugins.yml` は `packages: write`
// を持って GHCR へ publish するので、そこで動く action が差し替わると配布
// image そのものを書き換えられる。**GitHub 公式 (`actions/*`) も例外にしない**
// — 例外にすると「どれが例外か」を読む側が毎回判断することになる。
//
// 更新は dependabot (`.github/dependabot.yml` の `github-actions`) に任せる。
// 手で上げるときの手順は docs/ci.md。
//
// **拾えなかったら落とす。** 書式が変わって `uses:` を 1 つも拾えないと、
// 検査していないのに緑になる (compose-check と同じ判断)。
func TestWorkflowActionsArePinnedToSHA(t *testing.T) {
	root := repoRoot(t)

	cmd := exec.Command("git", "-C", root, "ls-files", "--",
		".github/workflows/*.yml", ".github/workflows/*.yaml",
		".github/actions/*/action.yml", ".github/actions/*/action.yaml")
	cmd.Env = filterGitEnv(os.Environ())
	out, err := cmd.Output()
	require.NoErrorf(t, err, "git ls-files に失敗した: %v", err)

	files := strings.Fields(string(out))
	require.NotEmpty(t, files, ".github/workflows に tracked な workflow が 1 つも無い")

	checked := 0
	for _, path := range files {
		for _, v := range scanUnpinnedUses(readRepoFile(t, path)) {
			assert.Failf(t, "action が commit SHA で固定されていない",
				"%s:%d: uses: %s\n"+
					"`owner/repo@<40 桁の commit SHA> # vX.Y.Z` の形で書くこと。\n"+
					"SHA は `git ls-remote https://github.com/<owner>/<repo> 'refs/tags/<tag>*'` で解く\n"+
					"(annotated tag は `^{}` の行が commit)。手順は docs/ci.md。",
				path, v.line, v.value)
		}
		checked += countRemoteUses(readRepoFile(t, path))
	}
	require.NotZerof(t, checked,
		"workflow から外部の `uses:` を 1 つも拾えませんでした。書式が変わったならこのゲートも直すこと")
}

// unpinnedUse is one `uses:` value that is not pinned to a commit SHA.
type unpinnedUse struct {
	line  int
	value string
}

// scanUnpinnedUses returns the `uses:` values in body that reference a remote
// action or reusable workflow without a full commit SHA and version comment.
func scanUnpinnedUses(body string) []unpinnedUse {
	var bad []unpinnedUse
	for i, line := range strings.Split(body, "\n") {
		value, ok := remoteUsesValue(line)
		if !ok {
			continue
		}
		if !pinnedActionRe.MatchString(value) {
			bad = append(bad, unpinnedUse{line: i + 1, value: value})
		}
	}
	return bad
}

// countRemoteUses returns how many remote `uses:` references body has.
func countRemoteUses(body string) int {
	n := 0
	for _, line := range strings.Split(body, "\n") {
		if _, ok := remoteUsesValue(line); ok {
			n++
		}
	}
	return n
}

// remoteUsesValue extracts the value of a `uses:` line that points outside the
// repository. Local references (`./...`) and YAML comment lines are skipped.
func remoteUsesValue(line string) (string, bool) {
	// コメント行の例示 (`#   uses: owner/repo/...@main`) は実行されないので見ない。
	if strings.HasPrefix(strings.TrimSpace(line), "#") {
		return "", false
	}
	m := usesLineRe.FindStringSubmatch(line)
	if m == nil {
		return "", false
	}
	value := m[1]
	// 引用符で囲んだ書き方も正規の YAML。外してから判定する (コメントは
	// 引用符の外に残る)。
	if q := value[0]; q == '"' || q == '\'' {
		if end := strings.IndexByte(value[1:], q); end >= 0 {
			value = value[1:end+1] + value[end+2:]
		}
	}
	// 同じリポジトリの reusable workflow / composite action は SHA 固定の
	// 対象外 (checkout した commit そのものを使う)。
	if strings.HasPrefix(value, "./") {
		return "", false
	}
	return value, true
}

// TestScanUnpinnedUses pins the scanner with synthetic workflow lines.
//
// **実ファイルは常に全件固定済み**なので、実ファイルだけを見ていると検出の枝が
// 一度も実行されない (#2792 の secretfield gate と同じ判断)。
func TestScanUnpinnedUses(t *testing.T) {
	const sha = "3d3c42e5aac5ba805825da76410c181273ba90b1"
	tests := []struct {
		name    string
		line    string
		pinned  bool
		skipped bool
	}{
		{name: "pinned step", line: "      - uses: actions/checkout@" + sha + " # v7.0.1", pinned: true},
		{name: "pinned with key", line: "        uses: docker/login-action@" + sha + " # v4.6.0", pinned: true},
		{name: "pinned sub-path", line: "        uses: github/codeql-action/init@" + sha + " # v4.38.2", pinned: true},
		{name: "pinned reusable workflow", line: "    uses: owner/repo/.github/workflows/x.yml@" + sha + " # v1.2.3", pinned: true},
		{name: "quoted", line: `      - uses: "actions/checkout@` + sha + `" # v7.0.1`, pinned: true},
		{name: "major tag", line: "      - uses: actions/checkout@v7"},
		{name: "full tag", line: "      - uses: actions/checkout@v7.0.1"},
		{name: "branch", line: "    uses: owner/repo/.github/workflows/x.yml@main"},
		{name: "sha without comment", line: "      - uses: actions/checkout@" + sha},
		{name: "short sha", line: "      - uses: actions/checkout@3d3c42e # v7.0.1"},
		{name: "comment is not a version", line: "      - uses: actions/checkout@" + sha + " # latest"},
		{name: "docker image", line: "      - uses: docker://alpine:3.21"},
		{name: "local action", line: "      - uses: ./.github/actions/setup", skipped: true},
		{name: "local reusable workflow", line: "    uses: ./.github/workflows/build-with-plugins.yml", skipped: true},
		{name: "commented example", line: "#       uses: shiroha-a/mk/.github/workflows/build-with-plugins.yml@main", skipped: true},
		{name: "not a uses key", line: "        with: actions/checkout@v7", skipped: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bad := scanUnpinnedUses(tt.line)
			n := countRemoteUses(tt.line)
			switch {
			case tt.skipped:
				require.Empty(t, bad)
				require.Zero(t, n)
			case tt.pinned:
				require.Empty(t, bad)
				require.Equal(t, 1, n)
			default:
				require.Len(t, bad, 1)
				require.Equal(t, 1, bad[0].line)
				require.Equal(t, 1, n)
			}
		})
	}
}
