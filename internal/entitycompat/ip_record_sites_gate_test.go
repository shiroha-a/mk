package entitycompat

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 「利用者の IP を記録する呼び出しはここだけ」という不変条件を固定する (#3135)。
//
// **これは関連アカウント検索 (#3105) の前提。** あちらの関連度は `user_ip` の観測
// だけを見るので、**失敗したサインインの IP がここに入ると第三者が他人の関連候補を
// 作れる** — 攻撃者が対象アカウントの ID で自分の IP から失敗を繰り返せば、その IP が
// 対象の「使用した IP」として記録され、攻撃者自身のアカウントが候補に並ぶ。
// #3066 が完了条件に挙げているのはこの攻撃面。
//
// **振る舞いテストでは守れない。** 守りたいのは「どこからも呼ばれていない」という
// 構造的な性質で、endpoint ごとのテストは叩いた経路しか見ない。実際、`/api/signin`
// だけを叩くテストは `SigninFlow` (同梱フロントが実際に使うほう) や
// `signin-with-passkey` に記録を足す変異を素通りさせた (実測)。非同期化
// (`go h.ipRecorder.Record(...)`) も振る舞い側からは観測が難しいが、こちらなら
// call site が増えた時点で落ちる。
//
// **allowlist には「なぜ許されるか」を書く。** 2 引数の `.Record(` は IP 記録とは
// 限らない (配送健全性など) ので、増えたときに人が判断できるようにする。
func TestIPRecordCallSitesAreAllowlisted(t *testing.T) {
	root := filepath.Join(repoRoot(t), "internal")
	sites := scanTwoArgRecordCalls(t, root)

	// **下限を持たせる。** 違反 0 件が正常な状態なので、抽出が壊れても
	// 「検出 0 件」と区別が付かない。
	require.GreaterOrEqualf(t, len(sites), len(ipRecordAllowlist),
		"2 引数の `.Record(` を %d 件しか拾えていない。抽出が壊れている", len(sites))

	for _, s := range sites {
		reason, ok := ipRecordAllowlist[s.key()]
		assert.Truef(t, ok,
			"%s の %s に 2 引数の `.Record(` がある。**利用者の IP を記録する呼び出しなら、"+
				"それは失敗したサインインの経路から届いていないか確認すること** "+
				"(#3135)。IP と無関係な Record なら allowlist に理由付きで足す。",
			s.File, s.Func)
		if ok {
			assert.NotEmptyf(t, reason, "%s の allowlist に理由が無い", s.key())
		}
	}

	// allowlist の死んだ entry も落とす (移動・削除に気付けるように)。
	found := map[string]bool{}
	for _, s := range sites {
		found[s.key()] = true
	}
	for key := range ipRecordAllowlist {
		assert.Truef(t, found[key], "allowlist の %q が実在しない。移動したか消えた", key)
	}
}

// ipRecordAllowlist は 2 引数の `.Record(` を許す場所と、その理由。
var ipRecordAllowlist = map[string]string{
	"internal/api/signin/handler.go#RecordSuccessfulSignin": "" +
		"サインイン成功の副作用をまとめた関数。ここが唯一の「成功したから記録する」入口で、" +
		"失敗経路 `fail()` からは呼ばれない。",
	"internal/api/signin/passkey.go#finishPasskeySignin": "" +
		"パスキーの成功経路。`RecordSuccessfulSignin` を経由せず直接呼ぶ (署名検証の後に " +
		"独自の後処理があるため)。**passwordless 無効の `fail()` より後ろにあること**を " +
		"下の順序テストが固定する。",
	"internal/server/middleware/client_ip.go#RecordClientIP": "" +
		"認証済みリクエストの IP を記録する middleware。**認証を通った後でしか動かない** " +
		"(未認証は `TestRecordClientIP_SkipsAnonymous` が固定)。",
	"internal/core/deliveryhealth/service.go#RecordDelivery": "" +
		"IP とは無関係。配送先ホストごとの成否を記録する (`RecordDelivery` の中の `Record(host, outcome)`)。",
}

// パスキーの記録が「失敗を返し切った後」にあることを固定する。
//
// **call site の列挙だけでは足りない。** `Record` を `fail()` より前へ動かしても
// 場所は変わらないので allowlist は通る。実際にその変異は endpoint の振る舞い
// テストも素通りした (実測)。passwordless 無効の利用者に対してパスキーの署名検証を
// 通した時点で記録してしまうと、**本人でない誰かが試した IP** が入りうる。
func TestPasskeyIPRecordComesAfterFailures(t *testing.T) {
	path := filepath.Join(repoRoot(t), "internal/api/signin/passkey.go")
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	require.NoError(t, err)

	var fn *ast.FuncDecl
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == "finishPasskeySignin" {
			fn = fd
			break
		}
	}
	require.NotNil(t, fn, "finishPasskeySignin が見つからない (rename した?)")

	var lastFail, recordAt token.Pos
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		switch sel.Sel.Name {
		case "fail":
			if call.Pos() > lastFail {
				lastFail = call.Pos()
			}
		case "Record":
			if len(call.Args) == 2 {
				recordAt = call.Pos()
			}
		}
		return true
	})

	require.NotEqual(t, token.NoPos, lastFail, "`fail(` を 1 つも拾えていない (検査が空振りしている)")
	require.NotEqual(t, token.NoPos, recordAt, "IP を記録する `Record(` を拾えていない")
	assert.Greaterf(t, int(recordAt), int(lastFail),
		"IP の記録が `fail(` より前にある (%s)。失敗を返し切ってから記録すること",
		fset.Position(recordAt))
}

type recordSite struct {
	File string // repo 相対
	Func string
}

func (s recordSite) key() string { return s.File + "#" + s.Func }

// scanTwoArgRecordCalls は `X.Record(a, b)` の呼び出しを、それを含む関数ごとに拾う。
//
// **引数の数で絞るのが要点。** 監査ログの `Record(iplookuplog.Entry{...})` は 1 引数
// なので入らない。型は見ていないので IP 以外の 2 引数 Record も拾うが、それは
// allowlist に理由を書いて分ける (`internal/core/deliveryhealth` が該当)。
func scanTwoArgRecordCalls(t *testing.T, root string) []recordSite {
	t.Helper()
	repo := repoRoot(t)
	var sites []recordSite

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return fmt.Errorf("parse %s: %w", path, perr)
		}
		rel, rerr := filepath.Rel(repo, path)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || len(call.Args) != 2 {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "Record" {
					return true
				}
				sites = append(sites, recordSite{File: rel, Func: fn.Name.Name})
				return true
			})
		}
		return nil
	})
	require.NoError(t, err, "walk %s", root)

	sort.Slice(sites, func(i, j int) bool { return sites[i].key() < sites[j].key() })
	return sites
}
