package entitycompat

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// bundledNginxConfigs lists the reverse-proxy configs shipped in this repo.
//
// 一覧は手で持つ。`deploy/` には検証用の設定も置かれうるので、glob で拾うと
// 対象が意図せず増減する (compose-check と同じ判断)。
var bundledNginxConfigs = []string{
	"../../deploy/uds/nginx/mkgo.conf",
}

// queryBearingLogVars are nginx variables that carry the query string.
//
// `$request` はリクエスト行そのもの (= クエリ込み)、`$request_uri` は元の URI
// (= クエリ込み)。どちらもログに出すと `?i=<token>` が平文で残る。
var queryBearingLogVars = []string{"$request", "$request_uri"}

// 同梱の nginx 設定がアクセスログにクエリ文字列を出さないこと。
//
// **同梱フロントは WebSocket を `/streaming?i=<ネイティブログイントークン>` で
// 開く**ので、クエリを出す書式にすると有効な credential がログに残る。
// mk-go 側 (`internal/misc/redact`) はアクセスログと Sentry の両方でこれを
// 伏せており、同梱の参照設定だけが素通しになっていた。
func TestBundledNginxLogFormatOmitsQueryString(t *testing.T) {
	t.Parallel()

	// **検査対象が空だと「違反 0 件」と区別が付かない。**
	require.NotEmpty(t, bundledNginxConfigs, "検査対象の一覧が空になっている")
	require.NotEmpty(t, queryBearingLogVars, "検査する変数の一覧が空になっている")

	// log_format 行は `log_format <name> '...' ...;` の形で複数行に折り返す。
	logFormatRe := regexp.MustCompile(`(?s)log_format\s+\w+\s+(.*?);`)

	checked := 0
	for _, path := range bundledNginxConfigs {
		raw, err := os.ReadFile(path)
		require.NoError(t, err, "同梱 nginx 設定が読めない: %s", path)

		body := stripNginxComments(string(raw))
		matches := logFormatRe.FindAllStringSubmatch(body, -1)
		require.NotEmpty(t, matches,
			"%s: log_format を 1 つも拾えなかった。書式が変わったなら、この検査を直すこと "+
				"(拾えないまま緑になると、検査していないのに通ってしまう)", path)

		for _, m := range matches {
			for _, v := range queryBearingLogVars {
				require.False(t, containsNginxVar(m[1], v),
					"%s: log_format が %s を含む。クエリ文字列にはネイティブ"+
						"ログイントークン (`?i=`) が載るので、$uri を使うこと", path, v)
			}
		}
		checked++
	}
	require.Equal(t, len(bundledNginxConfigs), checked)
}

// stripNginxComments removes `#` comments so that documentation mentioning
// `$request` does not trip the check.
func stripNginxComments(s string) string {
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		if i := strings.Index(line, "#"); i >= 0 {
			line = line[:i]
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

// containsNginxVar reports whether s references the exact nginx variable name.
//
// `$request` と `$request_method` を取り違えないよう、直後が変数名を続けられる
// 文字でないことまで見る。
func containsNginxVar(s, name string) bool {
	for i := 0; ; {
		j := strings.Index(s[i:], name)
		if j < 0 {
			return false
		}
		end := i + j + len(name)
		if end >= len(s) || !isNginxVarChar(s[end]) {
			return true
		}
		i = end
	}
}

func isNginxVarChar(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

// containsNginxVar が変数名の境界を見ていること。
//
// **実データだけでは検出ロジックを固定できない。** 現在の設定は違反を含まない
// ので、判定を常に false にする変異が素通りする (実際にそうなった)。
func TestContainsNginxVarBoundaries(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		hay  string
		want bool
	}{
		{"完全一致", `'$request '`, true},
		{"行末", `'... $request'`, true},
		{"引用符の直前", `"$request"`, true},
		{"より長い変数は別物", `'$request_method $uri'`, false},
		{"$request_uri も別物として扱う (専用の検査がある)", `'$request_uri'`, false},
		{"含まない", `'$remote_addr $status'`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			require.Equal(t, c.want, containsNginxVar(c.hay, "$request"), "haystack=%s", c.hay)
		})
	}

	// `$request_uri` 側も同じ規則で拾えること。
	require.True(t, containsNginxVar(`'$request_uri '`, "$request_uri"))
	require.False(t, containsNginxVar(`'$request_method'`, "$request_uri"))
}

// コメント除去が効いていること (doc が $request に言及しても落ちない)。
func TestStripNginxComments(t *testing.T) {
	t.Parallel()

	in := "# $request は使わない\nlog_format x '$uri'; # 末尾コメント\n"
	out := stripNginxComments(in)
	require.NotContains(t, out, "$request")
	require.Contains(t, out, "$uri")
	require.NotContains(t, out, "末尾コメント")
}

// 同梱の nginx 設定が無認証の管理面を塞いでいること。
//
// `/metrics` は `enableMetrics` を有効にすると無認証で公開される。doc は
// 2 箇所で「LB / nginx の ACL で制限すること」と書いているのに、同梱の参照設定に
// その対策が入っていなかった。
func TestBundledNginxBlocksUnauthenticatedAdminPaths(t *testing.T) {
	t.Parallel()

	mustBlock := []string{"/debug", "/metrics"}
	require.NotEmpty(t, bundledNginxConfigs)
	require.NotEmpty(t, mustBlock)

	for _, path := range bundledNginxConfigs {
		raw, err := os.ReadFile(path)
		require.NoError(t, err)
		body := stripNginxComments(string(raw))
		for _, p := range mustBlock {
			// **block の中身まで見る。** location が在るかだけでは、
			// `return 404;` を `proxy_pass` に書き換える変異 (= 塞いだ状態を
			// そのまま元へ戻す形) を素通りさせる (実測)。
			inner, ok := nginxLocationBody(body, p)
			require.True(t, ok, "%s: %s を塞ぐ location が無い", path, p)
			require.Regexp(t, `(?m)^\s*(return\s+40[34]|deny\s+all)\s*;`, inner,
				"%s: location %s が %s を塞いでいない (`return 404;` か `deny all;` が要る)",
				path, p, p)
		}
	}
}

// nginxLocationBody returns the body of `location <path> { ... }`.
//
// **ネストは想定しない。** 同梱の設定はどの location も 1 階層で、入れ子を
// 扱うには nginx の構文解析が要る。拾えなければ呼び出し側が落とす。
func nginxLocationBody(body, path string) (string, bool) {
	head := "location " + path + " {"
	i := strings.Index(body, head)
	if i < 0 {
		return "", false
	}
	rest := body[i+len(head):]
	j := strings.Index(rest, "}")
	if j < 0 {
		return "", false
	}
	return rest[:j], true
}

// 同梱の nginx 設定が `error_log` の扱いを明示していること。
//
// **`access_log` を直しても `error_log` には残る。** nginx はエラー行に
// `request: "<リクエスト行>"` と `upstream: "<URI>"` を付けるので、
// `/streaming?i=<token>` が stderr に落ちる。本番のログで実測した (直近 168
// 時間で 4 行、いずれも mk-go の再起動中に WebSocket が来たときの `[crit]`。
// レベルを上げても消えない)。
//
// nginx 側に redaction の手段が無いので、ここで検査できるのは「運用者が
// それを知っているか」だけ。**設定に注意書きがあることを要求する** —
// 消したら落ちるので、`access_log` だけ直して塞いだつもりになるのを防ぐ。
func TestBundledNginxDocumentsErrorLogLeak(t *testing.T) {
	t.Parallel()

	require.NotEmpty(t, bundledNginxConfigs)
	for _, path := range bundledNginxConfigs {
		raw, err := os.ReadFile(path)
		require.NoError(t, err)
		src := string(raw)
		if !strings.Contains(stripNginxComments(src), "error_log") {
			// error_log を書いていないなら nginx の既定 (ファイル出力) で、
			// 同梱構成の docker ログには出ない。注意書きは要らない。
			continue
		}
		require.Contains(t, src, "error_log にはクエリが残る",
			"%s: `error_log` を有効にしているのに、そこにトークンが残ることの"+
				"注意書きが無い。`access_log` を直しただけでは塞がらない", path)
	}
}

// 抽出そのものを人工の入力で固定する。
//
// **実設定からは検出できない。** `nginxLocationBody` が `}` を見なくなっても、
// 同梱の設定では隣の location の `return 404;` を拾って通ってしまう
// (`/debug` と `/metrics` が並んでいるため)。抽出が「その block の中だけ」を
// 返すことは、ここで直接見る。
func TestNginxLocationBody(t *testing.T) {
	t.Parallel()

	const src = `
    location /a {
        return 404;
    }
    location /b {
        proxy_pass http://x;
    }
`
	a, ok := nginxLocationBody(src, "/a")
	require.True(t, ok)
	require.Contains(t, a, "return 404;")
	require.NotContains(t, a, "proxy_pass", "隣の location を含めないこと")

	b, ok := nginxLocationBody(src, "/b")
	require.True(t, ok)
	require.Contains(t, b, "proxy_pass")
	require.NotContains(t, b, "return 404;",
		"block の外 (前の location) を含めないこと")

	_, ok = nginxLocationBody(src, "/missing")
	require.False(t, ok, "無い location は見つからないと返すこと")
}
