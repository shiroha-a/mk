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
// として書かれている。doc 自身が golden から生成されると謳っていたが、**生成して
// おらず手で書かれている**ので実際にずれていた (#2639)。
//
// #2612 で足した `NewCodedStatusError` / `ExtractStatusError` は追随していたが、
// peer 系 (`Definition.Peered` / `Context.Peer()` / `Peer` interface /
// `PeerHandler` / `PeerReplyHandler`) と `Definition.Validate()` が丸ごと抜けて
// いた。**同じ doc の peer の節が `Peered` と `ctx.Peer()` を使っているのに、
// 一覧には載っていない**という状態。
//
// 検査は 2 段構え。**名前の有無だけでは足りない** — #2639 の最初の修正は
// `Peer.Has` を `Has(string) bool` と書いており (実際は
// `Has(context.Context, string) (bool, error)`)、名前は載っているので素通り
// した。**doc の一覧は写して使われる**ので、署名が違えばコンパイルできない。
//
//	1. golden の公開識別子が doc に載っているか (載せ忘れの検出)
//	2. doc に書いた method の署名が golden と一致するか (写して壊れるものの検出)

var (
	// 公開識別子。Go の export 規則から大文字始まりで、`_` を含みうる。
	pluginSurfaceIdentRe = regexp.MustCompile(`[A-Z][A-Za-z0-9_]*`)
	// `pkg.Type` の `Type` は他パッケージの型なので公開面ではない。
	pluginSurfaceQualifiedRe = regexp.MustCompile(`\b[a-z][A-Za-z0-9_]*\.([A-Z][A-Za-z0-9_]*)`)
	// golden の `type Peer interface` / `func Errorf(...)` / `const APIVersion`。
	pluginSurfaceDeclRe = regexp.MustCompile(`^(type|func|const) ([A-Z][A-Za-z0-9_]*)\b`)
	// golden の `method Peer.Has(context.Context, string) (bool, error)`。
	pluginSurfaceMethodRe = regexp.MustCompile(`^method ([A-Z][A-Za-z0-9_]*)\.([A-Z][A-Za-z0-9_]*)(\(.*)$`)
	// doc の `type Peer interface` と、その中の `  Has(context.Context, string) (bool, error)`。
	pluginDocTypeRe   = regexp.MustCompile(`^type ([A-Z][A-Za-z0-9_]*) interface$`)
	pluginDocMethodRe = regexp.MustCompile(`^\s+([A-Z][A-Za-z0-9_]*)(\(.*)$`)

	pluginDocGoSectionStart = "### Go (`github.com/shiroha-a/mk/plugin`)"
	pluginDocGoSectionEnd   = "### TypeScript"
)

// TestPluginDoc_SurfaceListCoversGolden asserts that every exported identifier
// in the plugin surface golden is mentioned in the authoring doc's list.
func TestPluginDoc_SurfaceListCoversGolden(t *testing.T) {
	section := pluginDocGoSection(t)
	want := pluginSurfaceIdents(t)

	var missing []string
	for _, ident := range sortedStringMapKeys(want) {
		if !regexp.MustCompile(`\b` + regexp.QuoteMeta(ident) + `\b`).MatchString(section) {
			missing = append(missing, ident+"  (from: "+want[ident]+")")
		}
	}

	if len(missing) > 0 {
		t.Errorf(`docs/plugins/authoring.md の「公開面の一覧」に %d 件の識別子が無い:
  %s

この一覧は「壊さないと約束する範囲のすべて」として書かれているので、
公開面を足したら同時に載せること。**doc は golden から自動生成されていない**
(手で書かれている) ので、生成物のつもりで放置するとずれる (#2639)。`,
			len(missing), strings.Join(missing, "\n  "))
	}
}

// TestPluginDoc_SurfaceDeclarationsArePresent asserts that every type / func /
// const the plugin package declares has a declaration line in the doc's list.
//
// **名前が出ているだけでは足りない。** `type Handler func(Request) (any, error)`
// を一覧から消しても、`Router` の `POST(string, Handler)` が `Handler` という語を
// 含むので、名前の有無だけを見る検査は素通りする。
func TestPluginDoc_SurfaceDeclarationsArePresent(t *testing.T) {
	section := pluginDocGoSection(t)

	var missing []string
	seen := map[string]bool{}
	for _, entry := range readPluginSurfaceGolden(t) {
		if !strings.HasPrefix(entry, "plugin:") {
			continue
		}
		body := strings.TrimSpace(strings.TrimPrefix(entry, "plugin:"))
		m := pluginSurfaceDeclRe.FindStringSubmatch(body)
		if m == nil {
			continue
		}
		kind, name := m[1], m[2]
		if seen[kind+" "+name] {
			continue
		}
		seen[kind+" "+name] = true
		// doc 側も `type X` / `func X(` / `const X` の形で宣言していること。
		want := regexp.MustCompile(`(?m)^` + kind + `\s+` + regexp.QuoteMeta(name) + `\b`)
		if !want.MatchString(section) {
			missing = append(missing, kind+" "+name+"  (from: "+body+")")
		}
	}
	sort.Strings(missing)

	if len(missing) > 0 {
		t.Errorf(`docs/plugins/authoring.md の公開面一覧に宣言が無い (%d 件):
  %s

名前がどこかに出ているだけでは足りない (引数の型として出ているのを宣言と
取り違える)。`+"`type X` / `func X(` / `const X`"+` の形で載せること。`,
			len(missing), strings.Join(missing, "\n  "))
	}
}

// TestPluginDoc_SurfaceSignaturesMatchGolden asserts that the method signatures
// the doc lists are the ones the code actually has.
//
// **doc の一覧は写して使われる**ので、名前が載っていても署名が違えばコンパイル
// できない。#2639 の最初の修正が `Peer.Has` でこれをやった。
func TestPluginDoc_SurfaceSignaturesMatchGolden(t *testing.T) {
	golden := pluginSurfaceMethodSignatures(t)
	doc := pluginDocMethodSignatures(t)

	if len(doc) == 0 {
		t.Fatal("authoring.md の公開面一覧から method を 1 つも読めない (書式が変わった?)")
	}

	var findings []string
	for _, key := range sortedStringMapKeys(doc) {
		wantSig, ok := golden[key]
		if !ok {
			// golden に無いものを doc が書いている。型名の綴り違いか、消えた API。
			findings = append(findings, key+" は golden に無い (doc: "+doc[key]+")")
			continue
		}
		if doc[key] != wantSig {
			findings = append(findings, key+"\n      doc:    "+doc[key]+"\n      golden: "+wantSig)
		}
	}

	if len(findings) > 0 {
		t.Errorf(`docs/plugins/authoring.md の公開面一覧の署名が実装と違う (%d 件):
  %s

**この一覧は読んだ人が写して使う。** 署名が違うとサンプルどおりに書いても
コンパイルできない (#2639 の Peer.Has がこれ)。`,
			len(findings), strings.Join(findings, "\n  "))
	}
}

// pluginSurfaceIdents returns exported identifiers the plugin package declares,
// mapped to the golden line they came from.
//
// `pkg.Type` の `Type` は他パッケージの型なので落とす (`slog.Logger` の `Logger`
// と `Context.Logger()` の `Logger` を取り違えないよう、**修飾の有無で判別する**。
// 素の除外リストにすると本物の公開面まで黙る)。
func pluginSurfaceIdents(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, entry := range readPluginSurfaceGolden(t) {
		if !strings.HasPrefix(entry, "plugin:") {
			continue // plugintest 側は doc のこの一覧の対象外
		}
		body := strings.TrimSpace(strings.TrimPrefix(entry, "plugin:"))

		// **識別子単位で除外してはいけない。** `method Context.Logger() *slog.Logger`
		// のように、同じ行にメソッド名としての Logger と修飾された型としての
		// slog.Logger が両方出る。除外リスト方式だと本物の公開面まで黙る。
		// 出現ごとに潰してから抽出する。
		stripped := pluginSurfaceQualifiedRe.ReplaceAllString(body, "")
		for _, ident := range pluginSurfaceIdentRe.FindAllString(stripped, -1) {
			if _, seen := out[ident]; !seen {
				out[ident] = body
			}
		}
	}
	if len(out) == 0 {
		t.Fatal("golden から `plugin:` の公開識別子を 1 つも読めない (ラベル書式が変わった?)")
	}
	return out
}

// pluginSurfaceMethodSignatures returns `Type.Method` -> `(args) ret` from the golden.
func pluginSurfaceMethodSignatures(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, entry := range readPluginSurfaceGolden(t) {
		if !strings.HasPrefix(entry, "plugin:") {
			continue
		}
		body := strings.TrimSpace(strings.TrimPrefix(entry, "plugin:"))
		if m := pluginSurfaceMethodRe.FindStringSubmatch(body); m != nil {
			out[m[1]+"."+m[2]] = strings.TrimSpace(m[3])
		}
	}
	if len(out) == 0 {
		t.Fatal("golden から method 行を 1 つも読めない (書式が変わった?)")
	}
	return out
}

// pluginDocMethodSignatures returns `Type.Method` -> `(args) ret` from the doc's
// `type X interface` blocks.
func pluginDocMethodSignatures(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	current := ""
	for _, line := range strings.Split(pluginDocGoSection(t), "\n") {
		if m := pluginDocTypeRe.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			current = m[1]
			continue
		}
		// インデントの無い行で interface ブロックを抜ける。
		if strings.TrimSpace(line) == "" || !strings.HasPrefix(line, " ") {
			current = ""
			continue
		}
		if current == "" {
			continue
		}
		if m := pluginDocMethodRe.FindStringSubmatch(line); m != nil {
			out[current+"."+m[1]] = strings.TrimSpace(m[2])
		}
	}
	return out
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

func sortedStringMapKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
