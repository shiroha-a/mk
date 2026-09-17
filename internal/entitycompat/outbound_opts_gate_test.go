package entitycompat

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// **outbound の共通設定を渡し忘れない (#3037、不変条件は #638)。**
//
// `config.proxy` / `outgoingAddress` / `outgoingAddressFamily` は
// `safehttp.Option` として transport に載る。`...safehttp.Option` を受け取る
// constructor を router から呼ぶときに渡し忘れると、**その経路だけがサーバーの
// 素の IP で外へ出る**。運営者は proxy を設定したつもりのままなので、抜けて
// いることに気付けない。
//
// 実際に `emojimeta.NewFetcher` と `oauthDiscoveryTransport` の 2 つが
// 抜けていた。どちらも build もテストも緑のまま。
//
// **この gate が見るのは `internal/server` から呼ぶ側だけ。** 受け取る側
// (constructor) は variadic をそのまま transport へ流すだけなので、渡って
// いれば効く。
func TestOutboundConstructorsReceiveSharedOptions(t *testing.T) {
	qualified := variadicSafehttpOptionFuncs(t)
	require.NotEmpty(t, qualified, "`...safehttp.Option` を受ける関数を 1 つも拾えていない")
	// 代表例が入っていること (抽出が壊れたら空振りする)。
	assert.True(t, qualified["github.com/shiroha-a/mk/internal/core/emojimeta.NewFetcher"],
		"emojimeta.NewFetcher を拾えていない")
	assert.True(t, qualified["github.com/shiroha-a/mk/internal/server.oauthDiscoveryTransport"],
		"oauthDiscoveryTransport を拾えていない")

	const serverPkg = "github.com/shiroha-a/mk/internal/server"
	var missing []string
	for _, file := range serverSourceFiles(t) {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, file, nil, parser.ParseComments)
		require.NoError(t, err)
		imports := importIdents(f)

		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			key := ""
			switch fn := call.Fun.(type) {
			case *ast.Ident:
				key = serverPkg + "." + fn.Name
			case *ast.SelectorExpr:
				pkgIdent, ok := fn.X.(*ast.Ident)
				if !ok {
					return true
				}
				path, ok := imports[pkgIdent.Name]
				if !ok {
					// レシーバ越しの呼び出し (`s.foo()`) は同 package。
					key = serverPkg + "." + fn.Sel.Name
					break
				}
				key = path + "." + fn.Sel.Name
			default:
				return true
			}
			if !qualified[key] || callPassesOutboundOpts(call) {
				return true
			}
			missing = append(missing, filepath.Base(file)+":"+key)
			return true
		})
	}
	sort.Strings(missing)
	assert.Empty(t, missing,
		"`...safehttp.Option` を受ける constructor に `s.outboundOpts()...` を渡していない。"+
			"渡さないとその経路だけ config.proxy / outgoingAddress を通らない (#638)")
}

// variadicSafehttpOptionFuncs collects functions declared with a trailing
// `...safehttp.Option` parameter anywhere under internal/.
func variadicSafehttpOptionFuncs(t *testing.T) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	const modulePrefix = "github.com/shiroha-a/mk/"
	err := filepath.Walk(filepath.Join(".."), func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		fset := token.NewFileSet()
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return nil
		}
		// `internal/...` からの相対パスを import path へ直す。
		rel, rerr := filepath.Rel("..", filepath.Dir(path))
		if rerr != nil {
			return nil
		}
		pkgPath := modulePrefix + "internal/" + filepath.ToSlash(rel)
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Type.Params == nil {
				continue
			}
			for _, p := range fn.Type.Params.List {
				ell, ok := p.Type.(*ast.Ellipsis)
				if !ok {
					continue
				}
				sel, ok := ell.Elt.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "Option" {
					continue
				}
				if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "safehttp" {
					out[pkgPath+"."+fn.Name.Name] = true
				}
			}
		}
		return nil
	})
	require.NoError(t, err)
	// **forwarding する側は対象外。** `safehttp` 自身と、受け取った
	// `opts...` をそのまま流す wrapper (`mediaproxy.NewSSRFSafeTransport`) は
	// 「渡し忘れ」の概念が無い。
	for k := range out {
		if strings.HasSuffix(k, "/internal/core/mediaproxy.NewSSRFSafeTransport") {
			delete(out, k)
		}
	}
	return out
}

// serverSourceFiles lists the non-test Go sources of internal/server.
func serverSourceFiles(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join("..", "server"))
	require.NoError(t, err)
	var out []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		out = append(out, filepath.Join("..", "server", e.Name()))
	}
	require.NotEmpty(t, out)
	return out
}

// callPassesOutboundOpts reports whether the call spreads outboundOpts().
//
// **展開 (`...`) まで見る。** `s.outboundOpts()` を引数に置いただけでは
// 型が合わずコンパイルも通らないが、判定を名前だけにすると将来
// `[]safehttp.Option` を受ける形に変えたときに素通りする。
func callPassesOutboundOpts(call *ast.CallExpr) bool {
	if !call.Ellipsis.IsValid() {
		return false
	}
	for _, arg := range call.Args {
		switch a := arg.(type) {
		case *ast.CallExpr:
			if calleeName(a.Fun) == "outboundOpts" {
				return true
			}
		case *ast.Ident:
			// 受け取った variadic をそのまま流す形 (`opts...`)。
			if a.Name == "opts" || a.Name == "transportOpts" {
				return true
			}
		}
	}
	return false
}

// importIdents maps the local identifier of each import to its path.
func importIdents(f *ast.File) map[string]string {
	out := map[string]string{}
	for _, imp := range f.Imports {
		path := strings.Trim(imp.Path.Value, `"`)
		name := path[strings.LastIndex(path, "/")+1:]
		if imp.Name != nil {
			name = imp.Name.Name
		}
		out[name] = path
	}
	return out
}

// calleeName returns the identifier a call targets (`f` / `x.f`).
func calleeName(fun ast.Expr) string {
	switch t := fun.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.SelectorExpr:
		return t.Sel.Name
	}
	return ""
}
