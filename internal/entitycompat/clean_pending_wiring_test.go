package entitycompat

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// **期限切れ `user_pending` の掃除が配線されていること (#3037 レビュー)。**
//
// `NewCleanProcessor` の最後の引数に `nil` を渡しても**コンパイルは通り**、
// `if p.pending != nil` で掃除が黙って止まる。`internal/server` は CI の
// カバレッジ対象外で router を組み立てるテストも無いので、build もテストも
// 緑のまま巻き戻せる。同じ PR が `captchaSvc` に AST gate を足したのと
// 完全に同じ理由。
func TestCleanProcessorReceivesThePendingPruner(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filepath.Join("..", "server", "router.go"), nil, parser.ParseComments)
	require.NoError(t, err)

	found := false
	ok := false
	ast.Inspect(f, func(n ast.Node) bool {
		call, isCall := n.(*ast.CallExpr)
		if !isCall {
			return true
		}
		sel, isSel := call.Fun.(*ast.SelectorExpr)
		if !isSel || sel.Sel.Name != "NewCleanProcessor" {
			return true
		}
		found = true
		// 最後の引数が nil / 空でないこと。**呼び出しの形まで見る** —
		// 「引数の数が合っている」だけだと nil を渡す変異が素通りする。
		if len(call.Args) == 0 {
			return true
		}
		last := call.Args[len(call.Args)-1]
		if id, isIdent := last.(*ast.Ident); isIdent && id.Name == "nil" {
			return true
		}
		ok = true
		return true
	})

	require.True(t, found, "router.go に NewCleanProcessor の呼び出しが無い (rename した?)")
	assert.True(t, ok,
		"NewCleanProcessor に user_pending の pruner を渡していない。"+
			"渡さないと期限切れの登録 (メールアドレスとパスワードハッシュ) が無期限に貯まる")
}
