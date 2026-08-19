package entitycompat

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// docs/plugins/authoring.md の「公開面の一覧」は「壊さないと約束する範囲のすべて」
// として書かれており、doc 自身が `golden_plugin_surface.txt` から生成されると
// 謳っている。ところが**生成しているわけではなく手で書かれている**ので、実際は
// ずれていた (#2639)。
//
// #2612 で足した `NewCodedStatusError` / `ExtractStatusError` は追随していたが、
// peer 系 (`Definition.Peered` / `Context.Peer()` / `Peer` interface /
// `PeerHandler` / `PeerReplyHandler`) と `Definition.Validate()` が丸ごと抜けて
// いた。**同じ doc の peer の節が `Peered` と `ctx.Peer()` を使っているのに、
// 一覧には載っていない**という状態。
//
// doc は golden の逐語コピーではなく読みやすく整形した形なので、行単位の一致は
// 見られない。**識別子が載っているかどうか**だけを見る。

var (
	// `plugin: func (*StatusError) Error() string` のような 1 行から、
	// 公開識別子を拾う。
	pluginSurfaceIdentRe = regexp.MustCompile(`\b([A-Z][A-Za-z0-9]*)\b`)
	// doc の「### Go (...)」節だけを対象にする (TypeScript 節は別の面)。
	pluginDocGoSectionStart = "### Go (`github.com/shiroha-a/mk/plugin`)"
	pluginDocGoSectionEnd   = "### TypeScript"
)

// pluginSurfaceIgnored are identifiers that appear in the golden but are not
// part of what the doc's list is meant to enumerate.
//
// golden は plugin と plugin/plugintest の両方を含むが、doc の「公開面の一覧」は
// plugin パッケージの面を列挙するもの。plugintest 側は「テスト」の節で扱う。
// 標準ライブラリの型も識別子として拾えてしまうので落とす。
var pluginSurfaceIgnored = map[string]bool{
	// 標準ライブラリ / 外部パッケージの型名
	"Context":    false, // plugin.Context は実在するので落とさない
	"RawMessage": true, "Logger": true, "Time": true, "Duration": true,
	"DB": true, "T": true,
}

// TestPluginDoc_SurfaceListCoversGolden asserts that every exported identifier
// in the plugin surface golden is mentioned in the authoring doc's list.
func TestPluginDoc_SurfaceListCoversGolden(t *testing.T) {
	section := pluginDocGoSection(t)
	golden := readPluginSurfaceGolden(t)

	var missing []string
	seen := map[string]bool{}
	for _, entry := range golden {
		// plugintest 側は doc のこの一覧の対象外。
		if !strings.HasPrefix(entry, "plugin:") {
			continue
		}
		body := strings.TrimSpace(strings.TrimPrefix(entry, "plugin:"))
		for _, ident := range pluginSurfaceIdentRe.FindAllString(body, -1) {
			if pluginSurfaceIgnored[ident] || seen[ident] {
				continue
			}
			seen[ident] = true
			if !strings.Contains(section, ident) {
				missing = append(missing, ident+"  (from: "+body+")")
			}
		}
	}
	sort.Strings(missing)

	if len(missing) > 0 {
		t.Errorf(`docs/plugins/authoring.md の「公開面の一覧」に %d 件の識別子が無い:
  %s

この一覧は「壊さないと約束する範囲のすべて」として書かれているので、
公開面を足したら同時に載せること。**doc は golden から自動生成されていない**
(手で書かれている) ので、生成物のつもりで放置するとずれる (#2639)。`,
			len(missing), strings.Join(missing, "\n  "))
	}
}

func pluginDocGoSection(t *testing.T) string {
	t.Helper()
	blob, err := os.ReadFile(filepath.Join("..", "..", "docs", "plugins", "authoring.md"))
	if err != nil {
		t.Fatalf("read docs/plugins/authoring.md: %v", err)
	}
	doc := string(blob)
	start := strings.Index(doc, pluginDocGoSectionStart)
	if start < 0 {
		t.Fatalf("authoring.md に %q の節が無い", pluginDocGoSectionStart)
	}
	end := strings.Index(doc[start:], pluginDocGoSectionEnd)
	if end < 0 {
		t.Fatalf("authoring.md に %q の節が無い", pluginDocGoSectionEnd)
	}
	return doc[start : start+end]
}

func readPluginSurfaceGolden(t *testing.T) []string {
	t.Helper()
	blob, err := os.ReadFile(filepath.Join("testdata", "golden_plugin_surface.txt"))
	if err != nil {
		t.Fatalf("read golden: %v (run `go run ./tools/pluginspec -write`)", err)
	}
	var out []string
	for _, line := range strings.Split(string(blob), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	if len(out) == 0 {
		t.Fatal("golden が空 (run `go run ./tools/pluginspec -write`)")
	}
	return out
}
