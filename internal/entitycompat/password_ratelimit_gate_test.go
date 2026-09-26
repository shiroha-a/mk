package entitycompat

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/shiroha-a/mk/internal/server/middleware"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const modulePath = "github.com/shiroha-a/mk/"

// passwordVerifiers are the (import path, function) pairs that compare a
// user-supplied password against the stored hash.
var passwordVerifiers = map[string]string{
	"golang.org/x/crypto/bcrypt":          "CompareHashAndPassword",
	modulePath + "internal/misc/password": "Verify",
}

// パスワードを照合する route にはレート制限が要る。
//
// **native token を盗んだ攻撃者は、パスワードを照合する endpoint を総当たりの
// オラクルに使える。** 当たればパスワードそのものが手に入るので、token を
// 失効させても signin し直せる。limiter は `DefaultEndpointLimits` に無い
// endpoint を素通しするので、`i/delete-account` / `i/regenerate-token` /
// `i/2fa/*` の 7 本が無制限だった (upstream にも上限は無い)。
//
// **一覧を手で持たない。** handler の AST から「パスワードを照合する関数」を
// 同じ package 内で推移的に導出し (`requireWebAuthn` のような共通 helper
// 経由を拾うため)、router.go の登録と突き合わせる。新しい照合経路を足して
// 上限を忘れると落ちる。
//
// **既知の取りこぼし**: 別 package (core のサービス) の中で照合する形、
// handler を変数ではなく式で渡す登録、パスワード以外の秘密 (TOTP など) は
// 見ない。
func TestPasswordCheckingRoutesHaveRateLimits(t *testing.T) {
	routes := passwordCheckingRoutes(t)

	// **抽出の下限を名指しで持つ。** 違反 0 件が正常な検査なので、抽出が
	// 壊れると「検出 0 件」と区別が付かない。直接照合 (delete-account)、
	// 共通 helper 経由 (key-done)、別 package (signin) を 1 つずつ置く。
	for _, want := range []string{"i/change-password", "i/delete-account", "i/2fa/key-done", "signin"} {
		require.Contains(t, routes, want, "パスワード照合 route の抽出が壊れている")
	}

	for _, ep := range routes {
		t.Run(ep, func(t *testing.T) {
			limit, ok := middleware.DefaultEndpointLimits[ep]
			require.True(t, ok,
				"%s はパスワードを照合するのに DefaultEndpointLimits に上限が無い。\n"+
					"limiter は未登録の endpoint を素通しするので、token を持つ攻撃者が\n"+
					"パスワードを無制限に総当たりできる。i/change-password と同じ\n"+
					"{Duration: time.Hour, Max: 10, MinInterval: time.Second} を置くこと", ep)
			require.Positive(t, limit.Duration, "%s の窓が 0 で、実質無制限", ep)
			require.Positive(t, limit.Max, "%s の上限が 0 で、実質無制限", ep)
			// 1 時間あたりの試行回数で比べる (i/move は 1 日 5 回)。
			perHour := float64(limit.Max) * float64(time.Hour) / float64(limit.Duration)
			assert.LessOrEqual(t, perHour, 10.0,
				"%s の上限が 1 時間あたり 10 回より緩い", ep)
		})
	}
}

// passwordCheckingRoutes returns the router.go endpoints (without the `/api/`
// prefix) whose handler method verifies a password, directly or through a
// helper in the same package.
func passwordCheckingRoutes(t *testing.T) []string {
	t.Helper()
	root := filepath.Join("..", "..")
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filepath.Join(root, routerGo), nil, 0)
	require.NoError(t, err)

	imports := map[string]string{} // alias -> import path
	for _, imp := range f.Imports {
		p, _ := strconv.Unquote(imp.Path.Value)
		name := filepath.Base(p)
		if imp.Name != nil {
			name = imp.Name.Name
		}
		imports[name] = p
	}

	// handler 変数 -> package の import path。`x := pkg.NewHandler(...)` と
	// `x := &pkg.Handler{...}` を拾う。
	handlerPkg := map[string]string{}
	ast.Inspect(f, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok || len(as.Lhs) != len(as.Rhs) {
			return true
		}
		for i, lhs := range as.Lhs {
			id, ok := lhs.(*ast.Ident)
			if !ok {
				continue
			}
			if p := pkgOfExpr(as.Rhs[i], imports); p != "" {
				handlerPkg[id.Name] = p
			}
		}
		return true
	})

	verifying := map[string]map[string]bool{} // import path -> method names
	var out []string
	resolved := 0
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) < 2 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || (sel.Sel.Name != "POST" && sel.Sel.Name != "GET") {
			return true
		}
		if x, ok := sel.X.(*ast.Ident); !ok || x.Name != "api" {
			return true
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		path, err := strconv.Unquote(lit.Value)
		if err != nil {
			return true
		}
		h, ok := call.Args[1].(*ast.SelectorExpr)
		if !ok {
			return true
		}
		hv, ok := h.X.(*ast.Ident)
		if !ok {
			return true
		}
		pkg, ok := handlerPkg[hv.Name]
		if !ok || !strings.HasPrefix(pkg, modulePath+"internal/api/") {
			return true
		}
		resolved++
		if _, done := verifying[pkg]; !done {
			verifying[pkg] = passwordVerifyingFuncs(t, filepath.Join(root, strings.TrimPrefix(pkg, modulePath)))
		}
		if verifying[pkg][h.Sel.Name] {
			out = append(out, strings.TrimPrefix(path, "/"))
		}
		return true
	})
	// handler 変数の解決が壊れると全 route が素通りになる。
	require.Greater(t, resolved, 300, "router.go の handler 変数を package へ解決できていない")
	sort.Strings(out)
	return out
}

// pkgOfExpr returns the import path of `pkg.F(...)` / `&pkg.T{...}`.
func pkgOfExpr(e ast.Expr, imports map[string]string) string {
	if u, ok := e.(*ast.UnaryExpr); ok && u.Op == token.AND {
		e = u.X
	}
	var fun ast.Expr
	switch v := e.(type) {
	case *ast.CallExpr:
		fun = v.Fun
	case *ast.CompositeLit:
		fun = v.Type
	default:
		return ""
	}
	sel, ok := fun.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	x, ok := sel.X.(*ast.Ident)
	if !ok {
		return ""
	}
	return imports[x.Name]
}

// passwordVerifyingFuncs returns the names of the functions and methods in
// the package at dir that call a password verifier, directly or through
// another function of the same package.
//
// 名前だけで追う (レシーバの型は見ない) ので、同名メソッドがあると広めに拾う。
// 取りこぼすより安全側。
func passwordVerifyingFuncs(t *testing.T, dir string) map[string]bool {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	fset := token.NewFileSet()
	calls := map[string]map[string]bool{} // func name -> same-package callees
	found := map[string]bool{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		require.NoError(t, err)
		aliases := map[string]string{} // local alias -> verifier func name
		for _, imp := range f.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			fn, ok := passwordVerifiers[p]
			if !ok {
				continue
			}
			alias := filepath.Base(p)
			if imp.Name != nil {
				alias = imp.Name.Name
			}
			aliases[alias] = fn
		}
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			fname := fd.Name.Name
			if calls[fname] == nil {
				calls[fname] = map[string]bool{}
			}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				switch fun := call.Fun.(type) {
				case *ast.Ident:
					calls[fname][fun.Name] = true
				case *ast.SelectorExpr:
					if x, ok := fun.X.(*ast.Ident); ok {
						if want, ok := aliases[x.Name]; ok && want == fun.Sel.Name {
							found[fname] = true
							return true
						}
					}
					calls[fname][fun.Sel.Name] = true
				}
				return true
			})
		}
	}
	for changed := true; changed; {
		changed = false
		for fn, callees := range calls {
			if found[fn] {
				continue
			}
			for c := range callees {
				if found[c] {
					found[fn] = true
					changed = true
					break
				}
			}
		}
	}
	return found
}
