package notification

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestRegistryCoversEveryTypeConstant asserts the registry and the `Type`
// constants are in 1:1 correspondence.
//
// **これが土台の要。** 型の一覧を 2 箇所に持つと片側更新が起きる — 実際に
// `importCompleted` は core の定数にだけあり、API の enum には無かった
// (#2898)。API 側は registry から導出するようにしたので、あとは
// 「定数を足して registry に入れ忘れる」形だけが残る。それをここで塞ぐ。
//
// 定数側は AST で読む。registry は Go の値として読めるが、定数は
// `Type = "..."` という宣言そのものが一覧なので、ソースを見ないと
// 「宣言したのに登録していない」を検出できない。
func TestRegistryCoversEveryTypeConstant(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "notification_service.go", nil, 0)
	require.NoError(t, err)

	declared := map[string]string{} // value -> constant name
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			// `TypeFoo Type = "foo"` のみを拾う。型注記が無い定数 (iota 等) は
			// 通知タイプではない。
			ident, ok := vs.Type.(*ast.Ident)
			if !ok || ident.Name != "Type" {
				continue
			}
			for i, name := range vs.Names {
				if i >= len(vs.Values) {
					continue
				}
				lit, ok := vs.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				declared[lit.Value[1:len(lit.Value)-1]] = name.Name
			}
		}
	}

	// **1 つも拾えなかったら落とす。** 宣言の書式が変わって正規表現ならぬ
	// AST 照合が空振りすると、検査していないのに緑になる。
	require.NotEmpty(t, declared, "no `Type` constants found; the gate is not inspecting anything")

	registered := map[string]bool{}
	for _, d := range Descriptors() {
		registered[string(d.Type)] = true
	}

	for value, name := range declared {
		require.True(t, registered[value],
			"constant %s (%q) is not in the registry; add a Descriptor for it in registry.go", name, value)
	}
	for value := range registered {
		_, ok := declared[value]
		require.True(t, ok,
			"registry has %q but no `Type` constant declares it; add one to notification_service.go", value)
	}
}

// TestRegistryKindsAreConsistent asserts the derived lists partition the
// registry: every type is either filterable or obsolete, never both, never
// neither.
func TestRegistryKindsAreConsistent(t *testing.T) {
	filterable := FilterableTypeNames()
	obsolete := ObsoleteTypeNames()
	require.NotEmpty(t, filterable)
	require.NotEmpty(t, obsolete)

	seen := map[string]int{}
	for _, n := range filterable {
		seen[n]++
	}
	for _, n := range obsolete {
		seen[n]++
	}
	for _, d := range Descriptors() {
		require.Equal(t, 1, seen[string(d.Type)],
			"type %q must appear in exactly one of FilterableTypeNames/ObsoleteTypeNames", d.Type)
	}
	require.Len(t, seen, len(Descriptors()), "derived lists must not invent types")
}

// TestFilterableIncludesMkGoTypes pins the reason mk-go specific types are
// counted by the excludeTypes "covers everything" check.
//
// 含めないと upstream の 20 種を全て excludeTypes に並べただけで「全部除外」と
// 判定され、除外指定していない固有型の通知まで返らなくなる。
func TestFilterableIncludesMkGoTypes(t *testing.T) {
	filterable := map[string]bool{}
	for _, n := range FilterableTypeNames() {
		filterable[n] = true
	}
	var mkgo int
	for _, d := range Descriptors() {
		if d.Kind != KindMkGo {
			continue
		}
		mkgo++
		require.True(t, filterable[string(d.Type)],
			"mk-go specific type %q must be filterable", d.Type)
	}
	require.NotZero(t, mkgo, "no KindMkGo types registered; this gate is inspecting nothing")
}
