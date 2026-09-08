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
// registry: every type appears in exactly one of the three derived lists.
func TestRegistryKindsAreConsistent(t *testing.T) {
	upstream := UpstreamTypeNames()
	obsolete := ObsoleteTypeNames()
	mkgo := MkGoTypeNames()
	require.NotEmpty(t, upstream)
	require.NotEmpty(t, obsolete)
	require.NotEmpty(t, mkgo)

	seen := map[string]int{}
	for _, n := range upstream {
		seen[n]++
	}
	for _, n := range obsolete {
		seen[n]++
	}
	for _, n := range mkgo {
		seen[n]++
	}
	for _, d := range Descriptors() {
		require.Equal(t, 1, seen[string(d.Type)],
			"type %q must appear in exactly one derived list", d.Type)
	}
	require.Len(t, seen, len(Descriptors()), "derived lists must not invent types")
}

// TestMkGoTypesAreNotInUpstreamCoverage pins the fix for the regression the
// first version of this registry introduced (#2898).
//
// **固有型を全指定判定に入れると既読位置が飛ぶ。** upstream 由来のクライアント
// (misskey-js の notificationTypes を送るもの、fork frontend の「すべて無効」を
// 含む) は upstream の 20 種しか送らない。固有型が集合にあると被覆判定が成立
// しなくなり、呼び出し側が早期 return を抜けて既読化まで進む。1 件も返して
// いないのにユーザーが受け取っていない通知まで既読になる
// (#2833 / #2835 が塞いだ害の再オープン)。
func TestMkGoTypesAreNotInUpstreamCoverage(t *testing.T) {
	coverage := map[string]bool{}
	for _, n := range UpstreamTypeNames() {
		coverage[n] = true
	}
	mkgo := MkGoTypeNames()
	require.NotEmpty(t, mkgo, "no KindMkGo types registered; this gate is inspecting nothing")
	for _, n := range mkgo {
		require.False(t, coverage[n],
			"mk-go specific type %q must not be counted by the excludeTypes coverage check", n)
	}
}
