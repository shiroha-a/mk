package entitycompat

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
)

// parseNonTestGoFiles parses every non-test .go file directly under dir.
//
// **`parser.ParseDir` は使わない** (Go 1.25 で非推奨)。非推奨の理由は「build tag を
// 見ないので package とファイルの対応が不正確」だが、ここのゲートは**ディレクトリ内の
// .go を全部見たい**ので、その不正確さがむしろ要件に合う。代替として案内される
// `golang.org/x/tools/go/packages` は `go list` を起動するぶん重く、package 単位で
// 解決するのでディレクトリを直接列挙したいここには合わない (**型チェックは `NeedTypes`
// を渡したときだけ走り、走らせても AST は返る**ので、そこは理由にならない)。
//
// **入れ子のディレクトリは見ない** (`ParseDir` と同じ)。ゲートは対象のディレクトリを
// 自分で列挙する形なので、再帰すると射程が黙って広がる。
func parseNonTestGoFiles(fset *token.FileSet, dir string) ([]*ast.File, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []*ast.File
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, nil
}
