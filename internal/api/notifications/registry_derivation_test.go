package notifications

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/shiroha-a/mk/internal/core/notification"
)

// TestTypeListsAreDerivedFromRegistry asserts the API-level type lists are
// derived from the core registry instead of being re-declared as literals.
//
// **値の一致だけでは足りない (#2898)。** リテラルに書き戻しても、書いた時点では
// 中身が同じなので値比較は通る。落ちるのは core 側に型を足した後 = 一番検出
// したい瞬間に検出できない。導出している「形」そのものを固定する。
func TestTypeListsAreDerivedFromRegistry(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "handler.go", nil, 0)
	require.NoError(t, err)

	// var 名 -> 呼び出している関数名 (`notification.Xxx()` の Xxx)。
	derivedFrom := map[string]string{}
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.VAR {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range vs.Names {
				if i >= len(vs.Values) {
					continue
				}
				call, ok := vs.Values[i].(*ast.CallExpr)
				if !ok {
					continue
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					continue
				}
				pkg, ok := sel.X.(*ast.Ident)
				if !ok || pkg.Name != "notification" {
					continue
				}
				derivedFrom[name.Name] = sel.Sel.Name
			}
		}
	}

	require.Equal(t, "FilterableTypeNames", derivedFrom["notificationTypeList"],
		"notificationTypeList must be derived from notification.FilterableTypeNames(), not declared as a literal")
	require.Equal(t, "ObsoleteTypeNames", derivedFrom["obsoleteNotificationTypeList"],
		"obsoleteNotificationTypeList must be derived from notification.ObsoleteTypeNames(), not declared as a literal")

	// 導出元と実際の値も突き合わせる (形だけ合っていて中身が空でないこと)。
	require.Equal(t, notification.FilterableTypeNames(), notificationTypeList)
	require.Equal(t, notification.ObsoleteTypeNames(), obsoleteNotificationTypeList)
	require.NotEmpty(t, notificationTypeList)
	require.NotEmpty(t, obsoleteNotificationTypeList)
}

// TestExcludeAllUpstreamTypesKeepsMkGoTypes pins the behaviour the derivation
// exists for: excluding every upstream type must not silently drop mk-go
// specific notifications.
func TestExcludeAllUpstreamTypesKeepsMkGoTypes(t *testing.T) {
	var upstream []string
	var mkgo []string
	for _, d := range notification.Descriptors() {
		switch d.Kind {
		case notification.KindUpstream:
			upstream = append(upstream, string(d.Type))
		case notification.KindMkGo:
			mkgo = append(mkgo, string(d.Type))
		}
	}
	require.NotEmpty(t, upstream)
	require.NotEmpty(t, mkgo, "no mk-go specific types; this gate is inspecting nothing")

	// upstream の全種を excludeTypes に並べても「全部除外」にはならない。
	require.False(t, emptyByTypeFilter(ListRequest{ExcludeTypes: upstream}),
		"excluding every upstream type must not be treated as excluding everything")

	// 固有型まで並べて初めて全部除外になる。
	require.True(t, emptyByTypeFilter(ListRequest{ExcludeTypes: append(append([]string{}, upstream...), mkgo...)}),
		"excluding upstream + mk-go types must be treated as excluding everything")
}
