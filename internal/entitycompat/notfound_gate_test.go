package entitycompat

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// notFoundGateDirs are the trees this gate walks.
//
// **`internal/core` も見る** (#2799)。service 層で同じ潰し方をしていると、
// handler の lookup を service へ移すだけで gate を回避できる。判定は
// `bodyReturnsCollapse` が層ごとに切り替える (api / server は 4xx、core は
// domain sentinel)。
//
// **変数にしてあるのはテストから固定するため。** allowlist が空になった今、
// gate は「検出 0 件なら PASS」の向きなので、walk 対象を減らす変更は検出数を
// 0 にするだけで黙って通る。
var notFoundGateDirs = []string{"internal/api", "internal/server", "internal/core"}

// isRepoLookupMethod reports whether a method name looks like a single-row
// lookup whose error can mean either "no such row" or "the database is broken".
//
// **列挙ではなく形で拾う。** 最初 6 個を並べたところ、`FindRoomByID` (11 件) /
// `FindMessageByID` / `FindByToken` など**列挙外で同じ collapse をしている箇所が
// 39 件**あった。`FindByToken` を使う新 endpoint で同じ形を書いても素通りする。
//
// `Create` / `Update` / `Delete` は対象外 — 失敗をそもそも 4xx にしないので、
// 数えると偽陽性になる。`List` / `Count` 系も複数行なので除く。
func isRepoLookupMethod(name string) bool {
	// **`Get` は完全一致だけ採る。** プレフィックスにすると
	// `middleware.GetUser(c)` が 176 件引っかかり、検出の 6 割が偽陽性になる
	// (あれは context から読むだけで DB を触らない)。一方
	// `registryRepo.Get(...)` は本物の lookup で、実際に 2 件取りこぼしていた。
	if !strings.HasPrefix(name, "Find") && name != "Get" {
		return false
	}
	// 複数件を返すものは「無い」が正常なので対象外。
	//
	// **`String` / `Submatch` / `Index` も除く。** `regexp` の
	// `FindStringSubmatch` / `FindString` が DB の lookup として扱われ、
	// 正規表現でパラメータを検証して 400 を返す handler が誤検出される
	// (実際に検出された。`internal/api/app/handler.go:30` に前例がある)。
	for _, p := range []string{"List", "All", "Many", "Recent", "Search", "String", "Submatch", "Index"} {
		if strings.Contains(name, p) {
			return false
		}
	}
	return true
}

// notFoundGateAllowlist counts call sites that still collapse every repository
// error into a 4xx, keyed by "<path>:<func>".
//
// **#2792 の移行中の一覧。** 新規の流入をここで止めつつ、既存は段階的に潰す。
//
// **値は件数。** key を `<file>:<func>` にしているので、同じ関数に新しい
// collapse を足しても key は変わらない。件数で持たないと**その関数に 1 つでも
// 残っていれば何個足しても素通りする** (実際に素通りした)。
//
// 件数が増えたら落ちる (新規流入)。減っても落ちる (陳腐化 — 直したら数を
// 減らし、0 になったら行ごと消す)。
//
// **エントリを増やさないこと。** 足すのは、その endpoint で「DB 障害が 4xx に
// 化ける」ことを意図的に受け入れる場合だけで、そのときは理由をコメントで書く。
//
// key は行番号を含めない。無関係な編集で動いてしまうため。
var notFoundGateAllowlist = map[string]int{
	// **#2792 で全件を潰した。** 残っているのは「意図的に受け入れる」ものだけを
	// 入れる場所で、現在は空。
	//
	// 足すときは理由をコメントで書くこと。増やす前に
	// `repository.IsNotFound(err)` で分けられないかを先に考える。
}

// TestRepoErrorsAreNotCollapsed fails when a handler turns every repository
// lookup error into a 4xx response.
//
// upstream は `.findOneBy` の結果が `null` かどうかで not-found を判定するので、
// DB 障害は例外として 500 になる。mk-go は repository が GORM の error をその
// まま返すため、`err != nil` をまとめて not-found 扱いにすると**接続断や syntax
// error まで「そんなノートは無い」になる**。クライアントからは区別できず、
// 監視でも 5xx が立たない (#2792)。
//
// 判定は「lookup の直後の `if` が err を見て 4xx を返し、その条件が
// `repository.IsNotFound` / `errors.Is` を通っていない」こと。
func TestRepoErrorsAreNotCollapsed(t *testing.T) {
	root := repoRoot(t)
	var found []string

	for _, dir := range notFoundGateDirs {
		err := filepath.Walk(filepath.Join(root, dir), func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			found = append(found, scanCollapsedLookups(t, root, path)...)
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", dir, err)
		}
	}

	sort.Strings(found)

	// key ごとの件数で突き合わせる。
	got := map[string]int{}
	detail := map[string][]string{}
	for _, f := range found {
		parts := strings.SplitN(f, "\t", 2)
		got[parts[0]]++
		detail[parts[0]] = append(detail[parts[0]], f)
	}

	var over, under []string
	for k, n := range got {
		if allowed := notFoundGateAllowlist[k]; n > allowed {
			over = append(over, fmt.Sprintf("%s: %d 件 (allowlist は %d)\n    %s",
				k, n, allowed, strings.Join(detail[k], "\n    ")))
		}
	}
	for k, allowed := range notFoundGateAllowlist {
		if got[k] < allowed {
			under = append(under, fmt.Sprintf("%s: %d 件 (allowlist は %d)", k, got[k], allowed))
		}
	}
	sort.Strings(over)
	sort.Strings(under)

	if len(over) > 0 {
		t.Errorf("repository の lookup error を種別を見ずに 4xx にしている箇所が増えている:\n%s\n\n"+
			"  `repository.IsNotFound(err)` で分岐し、それ以外は 500 を返すこと。\n"+
			"  DB 障害が 4xx に化けると、クライアントから区別できず監視でも 5xx が立たない (#2792)。",
			strings.Join(over, "\n"))
	}
	// **陳腐化も落とす。** 直したのに件数が残ると、次に同じ関数へ足したときに
	// 素通りする。
	if len(under) > 0 {
		t.Errorf("notFoundGateAllowlist の件数が実態より多い (直したなら減らすこと):\n%s",
			strings.Join(under, "\n"))
	}

	// silent-zero guard: 抽出が壊れると found が 0 件になり、gate が無意味に
	// PASS する。lookup 呼び出し自体は必ず存在するので下限を置く。
	// **下限は実測に対して近く取る。** 検出 0 件は PASS と区別が付かないので、
	// 抽出器が黙って空振りしたことに気付く必要がある。実測 334 件に対して
	// 80 だと、述語を 4 分の 1 まで壊しても通ってしまう (#2799)。
	if n := countRepoLookups(t, root); n < 300 {
		t.Fatalf("repository lookup の呼び出しを %d 件しか見つけられなかった (期待 >=300)。抽出が壊れている", n)
	}
}

// scanCollapsedLookups returns "<relpath>:<func>\t<detail>" for each lookup
// whose error is collapsed into a 4xx.
func scanCollapsedLookups(t *testing.T, root, path string) []string {
	t.Helper()
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		rel = path
	}
	// `internal/core` は HTTP status を持たないので、判定を domain sentinel に
	// 切り替える (#2799)。
	isCore := strings.HasPrefix(filepath.ToSlash(rel), "internal/core/")

	var out []string
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		name := fn.Name.Name
		// report records one collapsed lookup. guarded が true なら、その
		// lookup は手前で既に not-found 判定を通っている。
		//
		// **正しい直し方は 2 形ある。** 入れ子形
		//
		//	if err != nil {
		//		if !repository.IsNotFound(err) { return 500 }
		//		return 404
		//	}
		//
		// と、前段 guard 形
		//
		//	if err != nil && !repository.IsNotFound(err) { return 500 }
		//	if err != nil || x == nil { return 404 }
		//
		// で、前者は判定が body の中、後者は**別の if** にある。片方しか見ないと
		// もう一方を検出し続ける (どちらも実際に検出し続けた)。
		report := func(as *ast.AssignStmt, ifs *ast.IfStmt, guarded bool) bool {
			if !condChecksErrNonNil(ifs.Cond, as) || guarded || checksNotFound(ifs) ||
				!bodyReturnsCollapse(ifs.Body, isCore, assignedErrName(as)) {
				return false
			}
			out = append(out, fmt.Sprintf("%s:%s\t%s (line %d)",
				rel, name, lookupMethodName(as), fset.Position(as.Pos()).Line))
			return true
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			// **`if x, err := repo.Find(...); err != nil` の形も見る。**
			// init 部分での代入を落とすと、この書き方に切り替えるだけで gate を
			// 素通りする (実際に素通りした)。
			if ifs, ok := n.(*ast.IfStmt); ok && ifs.Init != nil {
				if as, ok := ifs.Init.(*ast.AssignStmt); ok && isRepoLookup(as) {
					report(as, ifs, false)
				}
			}
			blk, ok := n.(*ast.BlockStmt)
			if !ok {
				return true
			}
			// `x, err := repo.Find(...)` の**後ろにある最初の if** が対象。
			//
			// **隣接だけを見ない。** 間に 1 文挟むだけで素通りする
			// (`owner := err == nil` のような形。実際に
			// `drive/handler.go:FilesAttachedNotes` が取りこぼされていた)。
			// err が再代入されたらそこで打ち切る — 別の err の判定になるため。
			for i := 0; i < len(blk.List)-1; i++ {
				as, ok := blk.List[i].(*ast.AssignStmt)
				if !ok || !isRepoLookup(as) {
					continue
				}
				guarded := false
				for j := i + 1; j < len(blk.List) && j <= i+lookupIfLookahead; j++ {
					if ifs, ok := blk.List[j].(*ast.IfStmt); ok {
						// **報告できたときだけ打ち切る。** 無条件に break すると、
						// `if h.repo == nil { ... }` のような nil-wiring guard が
						// 1 つ挟まっただけで err の判定を見逃す。
						if report(as, ifs, guarded) {
							break
						}
						// 前段 guard を通ったら、以降の if は判定済みとみなす。
						if condMentionsErr(ifs.Cond, as) && checksNotFound(ifs) {
							guarded = true
						}
						continue
					}
					if next, ok := blk.List[j].(*ast.AssignStmt); ok && assignsErr(next) {
						break
					}
				}
			}
			return true
		})
	}
	return out
}

// lookupIfLookahead is how far past a lookup we look for the `if` that handles
// its error.
//
// **広げすぎない。** 離れるほど「別の理由の if」を拾って偽陽性になる。
// 実測では 3 文あれば in-tree の形は全部拾える。
const lookupIfLookahead = 3

// assignsErr reports whether an assignment writes to an error-looking variable.
//
// lookup と if の間にこれが挟まったら、以降の if は別の err の判定なので
// 打ち切る。
func assignsErr(as *ast.AssignStmt) bool {
	for _, lhs := range as.Lhs {
		if id, ok := lhs.(*ast.Ident); ok && strings.Contains(strings.ToLower(id.Name), "err") {
			return true
		}
	}
	return false
}

// isRepoLookup reports whether an assignment calls a single-row lookup.
func isRepoLookup(as *ast.AssignStmt) bool { return lookupMethodName(as) != "" }

func lookupMethodName(as *ast.AssignStmt) string {
	if len(as.Rhs) != 1 {
		return ""
	}
	call, ok := as.Rhs[0].(*ast.CallExpr)
	if !ok {
		return ""
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	if !isRepoLookupMethod(sel.Sel.Name) {
		return ""
	}
	// **レシーバが repo らしくないものを除く。** `Get` を採ると
	// `c.Request().Header.Get("User-Agent")` まで拾う。DB を触らないので
	// not-found の概念が無い。
	if sel.Sel.Name == "Get" && !looksLikeRepoReceiver(sel.X) {
		return ""
	}
	return sel.Sel.Name
}

// looksLikeRepoReceiver reports whether an expression looks like a repository
// or store rather than an HTTP header / map / config object.
func looksLikeRepoReceiver(x ast.Expr) bool {
	sel, ok := x.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	n := sel.Sel.Name
	return strings.HasSuffix(n, "Repo") || strings.HasSuffix(n, "Repository") ||
		strings.HasSuffix(n, "Store") || strings.HasSuffix(n, "Cache")
}

// condChecksErrNonNil reports whether the condition tests the lookup's error
// for being non-nil.
//
// **`err == nil` の枝は潰しではない。** `if x, err := repo.Find(...); err == nil
// && x != nil` は成功したときの分岐なので、そこで別の error を返していても
// lookup error の潰しではない (実際に `chat.EnsureRoomViaAP` の
// `ErrRoomOwnerMismatch` を誤検出した)。
func condChecksErrNonNil(cond ast.Expr, as *ast.AssignStmt) bool {
	if !condMentionsErr(cond, as) {
		return false
	}
	name := assignedErrName(as)
	if name == "" {
		return false
	}
	found := false
	ast.Inspect(cond, func(n ast.Node) bool {
		bin, ok := n.(*ast.BinaryExpr)
		if !ok || bin.Op != token.NEQ {
			return true
		}
		x, okX := bin.X.(*ast.Ident)
		y, okY := bin.Y.(*ast.Ident)
		if okX && x.Name == name && okY && y.Name == "nil" {
			found = true
		}
		return !found
	})
	return found
}

// assignedErrName returns the name the lookup assigned its error to.
//
// Go の慣習で error は最後に返るので、最後の左辺を採る (`condMentionsErr` と
// 同じ規則)。`_` や非 ident のときは空文字。
func assignedErrName(as *ast.AssignStmt) string {
	if len(as.Lhs) == 0 {
		return ""
	}
	id, ok := as.Lhs[len(as.Lhs)-1].(*ast.Ident)
	if !ok || id.Name == "_" {
		return ""
	}
	return id.Name
}

// condMentionsErr reports whether the condition reads the error variable that
// the lookup assigned.
//
// **代入の左辺と突き合わせる。** 名前のパターンで見ると、`err2` /
// `errFind` を拾うために部分一致にしても `n, e := repo.Find(...)` のような
// 1 文字名が漏れる (どちらも実際に素通りした)。lookup が何に書いたかは
// AST から分かるので、そちらを正とする。
func condMentionsErr(cond ast.Expr, as *ast.AssignStmt) bool {
	// **最後の左辺だけを見る。** Go の慣習で error は最後に返る。左辺すべてを
	// 見ると値側の変数 (`ticket, err := ...` の `ticket`) を err と誤認し、
	// **err を一切見ていない権限チェックの if を拾う** (実際に拾った)。
	if len(as.Lhs) == 0 {
		return false
	}
	errID, ok := as.Lhs[len(as.Lhs)-1].(*ast.Ident)
	if !ok || errID.Name == "_" {
		return false
	}
	found := false
	ast.Inspect(cond, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && id.Name == errID.Name {
			found = true
		}
		return !found
	})
	return found
}

// checksNotFound reports whether the branch routes through a not-found
// predicate rather than treating every error the same.
//
// 条件と body の**両方**を見る。正しい直し方は body の先頭で
// `if !repository.IsNotFound(err) { return 500 }` と分けるので、条件だけを
// 見ると直したものを検出し続ける。
func checksNotFound(ifs *ast.IfStmt) bool {
	return mentionsNotFoundPredicate(ifs.Cond) || mentionsNotFoundPredicate(ifs.Body)
}

func mentionsNotFoundPredicate(n ast.Node) bool {
	found := false
	ast.Inspect(n, func(x ast.Node) bool {
		sel, ok := x.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		switch sel.Sel.Name {
		case "IsNotFound", "Is", "As":
			found = true
		}
		return !found
	})
	return found
}

// bodyReturnsCollapse reports whether the block collapses the error.
//
// api / server 層は 4xx を返す形、core 層は **domain sentinel を返す**形。
// core には HTTP status が無いので、同じ述語では検出できない (#2799)。
func bodyReturnsCollapse(b *ast.BlockStmt, isCore bool, errName string) bool {
	if isCore {
		return bodyReturnsNotFoundSentinel(b, errName)
	}
	return bodyReturns4xx(b)
}

// bodyReturnsNotFoundSentinel reports whether the block returns a not-found
// style sentinel instead of the original error.
//
// **命名の列挙ではなく「not-found を意味するか」で見る。** suffix を並べる形に
// したところ `ErrNotFollowing` / `ErrNotBlocking` / `ErrNotMuting` / `ErrNoPoll`
// が漏れ、実在する 8 サイトを見逃した (#2799 のレビューで発覚)。
//
// **`fmt.Errorf("%w: ...", ErrXxxNotFound)` も見る。** ident と selector だけを
// 見ると包んだ形を落とす。`emojiimport` が実際にこの形で、DB 瞬断中のジョブが
// `SkipRetry` で恒久破棄されていた。
//
// **`Err` で始まらない名前も拾う。** `notFoundErr` のような引数名で渡される
// 形がある (`user_service.go` の `applyMediaUpdate`)。
func bodyReturnsNotFoundSentinel(b *ast.BlockStmt, errName string) bool {
	found := false
	ast.Inspect(b, func(n ast.Node) bool {
		ret, ok := n.(*ast.ReturnStmt)
		if !ok {
			return true
		}
		for _, r := range ret.Results {
			// 包んだ形も辿る (`fmt.Errorf("%w", ErrXxx)`)。
			ast.Inspect(r, func(x ast.Node) bool {
				// **呼び出す関数の名前は sentinel ではない。**
				// `fmt.Errorf(...)` の `Errorf` を拾うと、raw error をラップして
				// 返す正しい形が sentinel に見える。引数だけ辿る。
				if call, ok := x.(*ast.CallExpr); ok {
					for _, a := range call.Args {
						ast.Inspect(a, func(y ast.Node) bool {
							return sentinelWalk(y, errName, &found)
						})
					}
					return false
				}
				return sentinelWalk(x, errName, &found)
			})
		}
		return !found
	})
	return found
}

// sentinelWalk is the per-node test used by bodyReturnsNotFoundSentinel.
func sentinelWalk(x ast.Node, errName string, found *bool) bool {
	if *found {
		return false
	}
	if call, ok := x.(*ast.CallExpr); ok {
		for _, a := range call.Args {
			ast.Inspect(a, func(y ast.Node) bool { return sentinelWalk(y, errName, found) })
		}
		return false
	}
	name := ""
	switch v := x.(type) {
	case *ast.Ident:
		name = v.Name
	case *ast.SelectorExpr:
		// **`X` 側は辿らない。** `errors.New(...)` の `errors` を拾うと、
		// raw error を組み立てて返す正しい形が全部 sentinel に見える
		// (実測で 28 件の偽陽性)。
		name = v.Sel.Name
	}
	// **lookup が代入した err をそのまま (or ラップして) 返す形は潰しではない。**
	// `return fmt.Errorf("...: %w", err)` は種別を保つので、これを検出すると
	// 正しい形を落とし続ける。
	if name == "" || name == errName {
		return false
	}
	if looksLikeNotFoundSentinel(name) {
		*found = true
	}
	return false
}

// looksLikeNotFoundSentinel reports whether an identifier names a "the row is
// not there" error.
//
// **列挙を反転させてある。** 「not-found を意味する語」を並べる形にすると、
// 新しい sentinel が別の名前 (`ErrThingMissing` / `ErrThingGone` など) で
// 入ったときに黙って素通りする — gate の目的そのものが果たせない。実際、
// 語の列挙にしていた版は 8 件を取りこぼしていた。
//
// lookup の err を受けた直後に返す error は、**not-found でない理由の方が
// 例外的**なので、そちらを除外語として持つ。除外に足すときは「その名前が
// lookup 失敗と無関係か」で判断すること。
func looksLikeNotFoundSentinel(name string) bool {
	lower := strings.ToLower(name)
	// **sentinel は package-level の `Err*` か、not-found を名前で言っているもの。**
	// 「err を含む」だけにすると `cerr` / `perr` / `kerr` のようなローカルの
	// error 変数を返す形が全部 sentinel に見える (実測で 4 件の偽陽性)。
	if !strings.HasPrefix(name, "Err") &&
		!strings.Contains(lower, "notfound") && !strings.Contains(lower, "nosuch") {
		return false
	}
	// lookup の失敗と無関係な error。ここに挙げたものは潰しとみなさない。
	//
	// **除外リストとして持つのが要点。** not-found を意味する語を並べる形に
	// すると、新しい sentinel が別の名前 (`ErrThingMissing` / `ErrThingGone`
	// など) で入ったときに黙って素通りする — gate の目的そのものが果たせない。
	// 実際、語の列挙にしていた版は 8 件を取りこぼしていた。
	for _, kw := range []string{
		"accessdenied", "forbidden", "permission", "unauthorized", "denied",
		"invalid", "malformed", "already", "duplicate", "conflict",
		"limit", "toomany", "ratelimit", "expired", "timeout", "canceled",
		"unsupported", "notimplemented", "internal", "unavailable",
	} {
		if strings.Contains(lower, kw) {
			return false
		}
	}
	return true
}

// bodyReturns4xx reports whether the block returns a 4xx response.
//
// **`apierr.JSON*` ヘルパーも見る。** このリポジトリの慣用形は
// `return apierr.JSONNoSuchUser(c)` で、`http.Status*` が現れない。
// セレクタ名の直書きだけを見ると**この形が丸ごと素通りする** (実測で
// production code に 328 箇所ある)。
func bodyReturns4xx(b *ast.BlockStmt) bool {
	found := false
	ast.Inspect(b, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		switch sel.Sel.Name {
		case "StatusBadRequest", "StatusNotFound", "StatusForbidden", "StatusUnauthorized":
			found = true
		}
		// **`len > 4` が要る。** `c.JSON(...)` 自体が `JSON` にマッチするので、
		// 500 を返す分岐まで 4xx とみなしてしまう。
		if strings.HasPrefix(sel.Sel.Name, "JSON") && len(sel.Sel.Name) > 4 &&
			!strings.Contains(sel.Sel.Name, "Internal") {
			found = true
		}
		return !found
	})
	return found
}

// countRepoLookups counts every repoLookupMethods call under the gated dirs.
func countRepoLookups(t *testing.T, root string) int {
	t.Helper()
	n := 0
	for _, dir := range notFoundGateDirs {
		_ = filepath.Walk(filepath.Join(root, dir), func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			for _, m := range []string{"FindByID", "FindByURI", "FindRoomByID"} {
				n += strings.Count(string(src), "."+m+"(")
			}
			return nil
		})
	}
	return n
}
