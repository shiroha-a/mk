package entitycompat

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// **`kind` を宣言できない経路に第三者アプリのトークンを入れない (#3037)。**
//
// upstream の `ApiCallService.ts:412-413` は「`kind` が無く、かつ資格情報を
// 要する endpoint」に対し app token を**一律で拒否**する。mk-go は
// `RequireScope` を route ごとに配線する形なので、この後半の規則にあたるのが
// `RequireSecure` (native token だけ通す) と `RejectAppToken` (app token だけ
// 落とす) の 2 つ。
//
// **どちらも無い credential route は、scope を一度も見ない。** つまり
// `read:account` しか許可していないアプリのトークンでも到達できる。実際に
// `emoji-application/*` の 3 つがその状態だった。
//
// `internal/server` は CI のカバレッジ対象外で router を組み立てるテストも
// 無いので、新しい route を足しても build もテストも緑のまま抜ける。ここで
// 形を固定する。
func TestCredentialRoutesWithoutScopeRejectAppTokens(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "server", "router.go"))
	require.NoError(t, err)

	var missing []string
	for _, call := range routeRegistrations(string(src)) {
		if !strings.Contains(call.mws, "RequireAuth") {
			continue
		}
		if strings.Contains(call.mws, "RequireScope") ||
			strings.Contains(call.mws, "RequireSecure") ||
			strings.Contains(call.mws, "RejectAppToken") {
			continue
		}
		if _, ok := appTokenGateExempt[call.path]; ok {
			continue
		}
		missing = append(missing, call.path)
	}
	sort.Strings(missing)
	assert.Empty(t, missing,
		"scope を見ない credential route がある。`RequireScope` / `RequireSecure` / `RejectAppToken` のいずれかを配線するか、"+
			"理由を添えて appTokenGateExempt に登録すること")
}

// appTokenGateExempt lists credential routes that may be reached with an app
// token, with the reason.
//
// **理由には「upstream がどう扱うか」を書く。** 「見たけど大丈夫だった」では
// 次に読む人が判断をやり直せない。
var appTokenGateExempt = map[string]string{
	// upstream `federation/update-remote-user.ts` は `requireCredential: false`
	// なので、app token でも (むしろ未認証でも) 到達できる。mk-go は認証を
	// 要求する側へ寄せてあるが、そこからさらに app token を落とすと
	// upstream で動くクライアントを mk-go だけが弾くことになる。
	"/federation/update-remote-user": "upstream は requireCredential:false (kind 無しの拒否規則が発動しない)",
}

// **除外の一覧が腐らないようにする。** 実在しない path が残ると、その行が
// 何を守っているのか分からなくなる。
func TestAppTokenGateExemptHasNoDeadEntries(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "server", "router.go"))
	require.NoError(t, err)

	seen := map[string]bool{}
	for _, call := range routeRegistrations(string(src)) {
		seen[call.path] = true
	}
	for path, reason := range appTokenGateExempt {
		assert.True(t, seen[path], "appTokenGateExempt の %q が router.go に無い", path)
		assert.NotEmpty(t, reason, "%q に理由が無い", path)
	}
}

// **抽出そのものが壊れていないこと。** 1 件も拾えなくなると「検査していない
// のに緑」になる (#2857 と同じ型)。件数の下限は実測 (POST/GET 合わせて 700 超)
// より十分低いところに置く。
func TestRouteRegistrationsAreExtracted(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "server", "router.go"))
	require.NoError(t, err)

	calls := routeRegistrations(string(src))
	assert.Greater(t, len(calls), 300, "route の抽出が壊れている")

	// 代表例が拾えていること (path と middleware の両方)。
	var found bool
	for _, c := range calls {
		if c.path == "/i/change-password" {
			found = true
			assert.Contains(t, c.mws, "RequireSecure", "middleware を拾えていない")
		}
	}
	assert.True(t, found, "既知の route を拾えていない")
}

type routeRegistration struct {
	path string
	mws  string
}

var routeCallPattern = regexp.MustCompile(`api\.(?:POST|GET|PUT|DELETE)\(\s*"([^"]+)"([^\n]*)`)

// routeRegistrations extracts `api.<VERB>("/path", handler, mw...)` calls.
//
// **行継続を畳んでから走査する。** 引数を複数行に分けて書いた route を
// 落とすと、そこだけ検査されないまま緑になる。
func routeRegistrations(src string) []routeRegistration {
	// 継続行を 1 行に畳む。`)` が来るまでを 1 つの呼び出しとして扱う。
	flat := foldRouteCalls(src)
	var out []routeRegistration
	for _, m := range routeCallPattern.FindAllStringSubmatch(flat, -1) {
		out = append(out, routeRegistration{path: m[1], mws: m[2]})
	}
	return out
}

// foldRouteCalls joins the continuation lines of a route registration.
func foldRouteCalls(src string) string {
	var b strings.Builder
	lines := strings.Split(src, "\n")
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if !strings.Contains(line, "api.POST(") && !strings.Contains(line, "api.GET(") &&
			!strings.Contains(line, "api.PUT(") && !strings.Contains(line, "api.DELETE(") {
			b.WriteString(line)
			b.WriteString("\n")
			continue
		}
		joined := line
		// 括弧が閉じるまで続きを足す。
		for strings.Count(joined, "(") > strings.Count(joined, ")") && i+1 < len(lines) {
			i++
			joined += " " + strings.TrimSpace(lines[i])
		}
		b.WriteString(joined)
		b.WriteString("\n")
	}
	return b.String()
}
