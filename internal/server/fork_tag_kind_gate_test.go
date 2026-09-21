package server

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fork タグの採番が規則どおりかを、現在の pin に対して検査する (#3141)。
//
// 規則は `docs/upstream-catch-up.md` の「fork タグの採番規則」節 (#3139):
// **数字を進めるのは新機能、英字を付けるのは既存機能の改修・バグ修正。**
//
// **doc に書くだけでは守れない。** 形式の連番は `assertForkTagSequence` が強制して
// いるが、あれは「数字 +1 か、同じ数字への次の英字」しか見ておらず、**どちらを選ぶ
// かは見ていない**。実際にそこを通り抜けて、`2026.9.0` の数字タグ 40 件のうち 13 件が
// `fix` という状態になっていた。
//
// **タグは読まない。** CI の submodule は shallow checkout なのでタグが降りてこない
// (`submodulepin-check` が「tag から SHA を解くにはネットワークか checkout が要る」
// として pin 行に短縮 SHA を併記する形にしたのと同じ制約)。代わりに **pin 行の tag の
// 形**と、**その SHA が指すコミットの件名の prefix** を突き合わせる。採番を誤るのは
// pin を上げる瞬間なので、そこを押さえれば足りる。
//
// **submodule を checkout する job でしか動かせない。** `test-shards` は
// `third_party/misskey` を取らないので、そこでは skip する。skip は成功として扱われる
// ので、submodule がある前提の job では `MK_FRONTEND_GATES_REQUIRE_SUBMODULE` を渡して
// skip を禁じる (#2892 と同じ形)。`make frontend-check` がそれを渡す。
func TestForkTagKindMatchesCommitPrefix(t *testing.T) {
	root := repoRootDir(t)
	tag, sha := forkPinFromDoc(t, root)

	num, letter, ok := splitForkTagRevision(tag)
	require.Truef(t, ok, "pin 行の tag %q が `<upstream release>-mk.<N>[<英字>]` の形になっていない", tag)

	subject, err := forkCommitSubject(root, sha)
	if err != nil {
		if os.Getenv("MK_FRONTEND_GATES_REQUIRE_SUBMODULE") != "" {
			require.NoErrorf(t, err, "submodule を要求する job なのに %s のコミットを読めない", sha)
		}
		t.Skipf("third_party/misskey の %s を読めない (submodule を checkout する job でのみ検査する): %v", sha, err)
	}

	want := wantForkTagPrefix(num, letter)
	if want == "" {
		return // `-mk.0` は upstream の取り込み / 載せ替えなので prefix を問わない
	}
	if reason, allowed := forkTagPrefixAllowlist[tag]; allowed {
		require.NotEmptyf(t, reason, "allowlist の %q に理由が無い", tag)
		return
	}

	assert.Truef(t, strings.HasPrefix(subject, want+"("),
		"pin %s のコミット件名が %q で始まっていない (実際: %q)。\n"+
			"**数字を進めるのは新機能、英字を付けるのは既存機能の改修・バグ修正** "+
			"(`docs/upstream-catch-up.md` の「fork タグの採番規則」)。\n"+
			"タグの側が正しいならコミット件名を直し、件名の側が正しいならタグを打ち直して "+
			"pin 行・§4-2 の表・冒頭サマリを揃えること。どうしても例外にするなら "+
			"forkTagPrefixAllowlist に理由付きで足す。",
		tag, want, subject)
}

// forkTagPrefixAllowlist は採番と commit prefix が食い違ってよい pin と、その理由。
// **いまは空。**
var forkTagPrefixAllowlist = map[string]string{}

// allowlist の中身そのものを固定する。**entry を足すのは規則を曲げるときだけ**なので、
// 黙って増やせないようにする (死んだ entry を落とす検査は、空のうちは何も守らない)。
func TestForkTagPrefixAllowlistMatchesExpected(t *testing.T) {
	var got []string
	for k := range forkTagPrefixAllowlist {
		got = append(got, k)
	}
	sort.Strings(got)
	assert.Emptyf(t, got, "採番の例外が増えている。規則 (docs/upstream-catch-up.md) を曲げる判断なので、理由を確かめること")
}

// 採番と prefix の対応そのものを固定する。
//
// **実データは pin 1 件しか見ないので、これが無いと判定の枝がほとんど実行されない。**
// 現在の pin が数字なら英字側の枝は一度も通らず、逆も同じ。
func TestWantForkTagPrefix(t *testing.T) {
	for _, c := range []struct {
		tag  string
		num  int
		let  string
		want string
	}{
		{"2026.9.0-mk.0", 0, "", ""},      // upstream の取り込み / 載せ替え
		{"2026.9.0-mk.0a", 0, "a", "fix"}, // 取り込み直後の修正は英字
		{"2026.9.0-mk.39", 39, "", "feat"},
		{"2026.9.0-mk.39a", 39, "a", "fix"},
		{"2026.7.0-mk.22j", 22, "j", "fix"},
	} {
		num, letter, ok := splitForkTagRevision(c.tag)
		require.Truef(t, ok, "%s を分解できない", c.tag)
		assert.Equalf(t, c.num, num, "%s の数字", c.tag)
		assert.Equalf(t, c.let, letter, "%s の英字", c.tag)
		assert.Equalf(t, c.want, wantForkTagPrefix(num, letter), "%s に要求する prefix", c.tag)
	}

	// 形になっていないものは分解できない。
	for _, bad := range []string{"2026.9.0", "2026.9.0-mk.", "2026.9.0-mk.a", "mk.1", "2026.9.0-mk.1-2"} {
		_, _, ok := splitForkTagRevision(bad)
		assert.Falsef(t, ok, "%q を分解できてしまっている", bad)
	}
}

// wantForkTagPrefix returns the commit-message prefix the tag form requires,
// or "" when the form puts no requirement on it.
func wantForkTagPrefix(num int, letter string) string {
	switch {
	case letter != "":
		return "fix"
	case num == 0:
		// upstream release の取り込みと載せ替え。prefix は cherry-pick した
		// 中身次第なので問わない。
		return ""
	default:
		return "feat"
	}
}

var forkTagRevisionRe = regexp.MustCompile(`^[0-9]+(?:\.[0-9]+)*-mk\.([0-9]+)([a-z]*)$`)

// splitForkTagRevision splits `2026.9.0-mk.39a` into 39 and "a".
func splitForkTagRevision(tag string) (int, string, bool) {
	m := forkTagRevisionRe.FindStringSubmatch(tag)
	if m == nil {
		return 0, "", false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, "", false
	}
	return n, m[2], true
}

// forkPinFromDoc reads the pin line of docs/divergence.md.
//
// **ちょうど 1 件であることを要求する。** 書式の例を前方に書いた瞬間に本物の pin 行が
// 検査対象から外れるため (`submodulepin-check` と同じ判断。あちらと同じ行を読むので、
// 書式を変えるときは両方を直すことになる)。
func forkPinFromDoc(t *testing.T, root string) (tag, sha string) {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(root, "docs/divergence.md"))
	require.NoError(t, err)
	re := regexp.MustCompile("\\*\\*現在の pin は `([^`]+)` \\(`([0-9a-f]{7,40})`\\)")
	ms := re.FindAllStringSubmatch(string(body), -1)
	require.Lenf(t, ms, 1, "docs/divergence.md の pin 行が %d 件。ちょうど 1 件であること", len(ms))
	return ms[0][1], ms[0][2]
}

// forkCommitSubject returns the subject line of the given commit in the fork.
func forkCommitSubject(root, sha string) (string, error) {
	cmd := exec.Command("git", "-C", filepath.Join(root, "third_party", "misskey"),
		"log", "-1", "--format=%s", sha)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
