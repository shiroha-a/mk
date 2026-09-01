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
	// **`Get` プレフィックスは採らない。** `middleware.GetUser(c)` が 176 件
	// 引っかかり、検出の 6 割が偽陽性になった。あれは context から読むだけで
	// DB を触らない。
	if !strings.HasPrefix(name, "Find") {
		return false
	}
	// 複数件を返すものは「無い」が正常なので対象外。
	for _, p := range []string{"List", "All", "Many", "Recent", "Search"} {
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
	"internal/api/admin/abuse_report_notification.go:AbuseReportNotificationRecipientShow":   1,
	"internal/api/admin/abuse_report_notification.go:AbuseReportNotificationRecipientUpdate": 2,
	"internal/api/admin/abuse_report_notification.go:validateEmailAddress":                   1,
	"internal/api/admin/accounts.go:AccountsFindByEmail":                                     2,
	"internal/api/admin/ad.go:AdUpdate":                                                      1,
	"internal/api/admin/avatar_decorations.go:AvatarDecorationsUpdate":                       1,
	"internal/api/admin/drive.go:DriveShowFile":                                              1,
	"internal/api/admin/emoji.go:EmojiCopy":                                                  2,
	"internal/api/admin/handler.go:EmojiAdd":                                                 2,
	"internal/api/admin/handler.go:EmojiDelete":                                              1,
	"internal/api/admin/handler.go:EmojiUpdate":                                              2,
	"internal/api/admin/handler.go:RolesAssign":                                              1,
	"internal/api/admin/handler.go:RolesUnassign":                                            1,
	"internal/api/admin/moderation.go:UpdateAbuseUserReport":                                 1,
	"internal/api/admin/system_webhook.go:SystemWebhookShow":                                 1,
	"internal/api/admin/system_webhook.go:SystemWebhookTest":                                 1,
	"internal/api/admin/system_webhook.go:SystemWebhookUpdate":                               1,
	"internal/api/announcements/handler.go:AdminUpdate":                                      1,
	"internal/api/announcements/handler_show.go:Show":                                        1,
	"internal/api/app/handler.go:Show":                                                       1,
	"internal/api/auth/handler.go:Accept":                                                    1,
	"internal/api/auth/handler.go:SessionGenerate":                                           1,
	"internal/api/auth/handler.go:SessionShow":                                               1,
	"internal/api/auth/handler.go:SessionUserkey":                                            2,
	"internal/api/chat/handler.go:AttachedChatMessages":                                      1,
	"internal/api/chat/handler.go:InvitationsAccept":                                         1,
	"internal/api/chat/handler.go:InvitationsCreate":                                         1,
	"internal/api/chat/handler.go:InvitationsOutbox":                                         1,
	"internal/api/chat/handler.go:InvitationsReject":                                         1,
	"internal/api/chat/handler.go:MembersUpdateMembership":                                   2,
	"internal/api/chat/handler.go:Messages":                                                  1,
	"internal/api/chat/handler.go:MessagesCreate":                                            3,
	"internal/api/chat/handler.go:MessagesDelete":                                            1,
	"internal/api/chat/handler.go:MessagesSearch":                                            1,
	"internal/api/chat/handler.go:MessagesShow":                                              1,
	"internal/api/chat/handler.go:MessagesUpdate":                                            1,
	"internal/api/chat/handler.go:RoomsDelete":                                               1,
	"internal/api/chat/handler.go:RoomsMembers":                                              1,
	"internal/api/chat/handler.go:RoomsMute":                                                 1,
	"internal/api/chat/handler.go:RoomsShow":                                                 1,
	"internal/api/chat/handler.go:RoomsTransferOwnership":                                    1,
	"internal/api/chat/handler.go:RoomsUpdate":                                               1,
	"internal/api/chat/handler.go:RoomTimeline":                                              1,
	"internal/api/chat/handler.go:UserTimeline":                                              1,
	"internal/api/drive/handler.go:FilesAttachedNotes":                                       1,
	"internal/api/drive/handler.go:FilesMoveBulk":                                            1,
	"internal/api/emojis/handler.go:Emoji":                                                   1,
	"internal/api/federation/update_remote_user.go:UpdateRemoteUser":                         1,
	"internal/api/i/email_update.go:VerifyEmail":                                             1,
	"internal/api/i/handler_2fa.go:TwoFAUpdateKey":                                           1,
	"internal/api/i/handler.go:normalizeAvatarDecorations":                                   1,
	"internal/api/i/handler.go:Update":                                                       1,
	"internal/api/i/transfer_handler.go:validateImportRequest":                               1,
	"internal/api/notes/handler_drafts.go:DraftsUpdate":                                      1,
	"internal/api/notes/handler_drafts.go:ThreadMutingCreate":                                1,
	"internal/api/notes/handler_drafts.go:ThreadMutingDelete":                                1,
	"internal/api/notes/handler_drafts.go:validateDraftReplyRenote":                          3,
	"internal/api/notes/handler_drafts.go:validateRenoteChannel":                             1,
	"internal/api/notes/handler_extra.go:Clips":                                              1,
	"internal/api/notes/handler_extra.go:FavoritesDelete":                                    1,
	"internal/api/notes/handler_extra.go:Translate":                                          1,
	"internal/api/notes/handler_extra.go:Unrenote":                                           1,
	"internal/api/notes/handler_extra.go:UserListTimeline":                                   1,
	"internal/api/pages/handler.go:PagePush":                                                 1,
	"internal/api/promo/handler.go:Read":                                                     1,
	"internal/api/resetpassword/handler.go:Reset":                                            1,
	"internal/api/reversi/handler.go:ShowGame":                                               1,
	"internal/api/reversi/handler.go:Surrender":                                              1,
	"internal/api/reversi/handler.go:Verify":                                                 1,
	"internal/api/signin/handler.go:Signin":                                                  2,
	"internal/api/signin/handler.go:SigninFlow":                                              2,
	"internal/api/signin/passkey.go:finishPasskeySignin":                                     1,
	"internal/api/userlists/handler.go:Delete":                                               1,
	"internal/api/userlists/handler.go:List":                                                 1,
	"internal/api/userlists/handler.go:Pull":                                                 2,
	"internal/api/userlists/handler.go:Push":                                                 2,
	"internal/api/userlists/handler.go:Show":                                                 1,
	"internal/api/users/handler_extra.go:Reactions":                                          1,
	"internal/api/users/handler_extra.go:ReportAbuse":                                        1,
	"internal/api/users/lists.go:ListsCreateFromPublic":                                      2,
	"internal/api/users/lists.go:ListsFavorite":                                              1,
	"internal/api/users/lists.go:ListsGetMemberships":                                        1,
	"internal/api/users/lists.go:ListsUnfavorite":                                            1,
	"internal/api/users/lists.go:ListsUpdate":                                                1,
	"internal/api/users/lists.go:ListsUpdateMembership":                                      1,
	"internal/api/webhooks/handler.go:Delete":                                                1,
	"internal/api/webhooks/handler.go:Show":                                                  1,
	"internal/api/webhooks/handler.go:Test":                                                  1,
	"internal/api/webhooks/handler.go:Update":                                                1,
	"internal/server/emoji_redirect.go:emojiRedirectHandler":                                 1,
	"internal/server/feed.go:serve":                                                          1,
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

	for _, dir := range []string{"internal/api", "internal/server"} {
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
	if n := countRepoLookups(t, root); n < 80 {
		t.Fatalf("repository lookup の呼び出しを %d 件しか見つけられなかった (期待 >=80)。抽出が壊れている", n)
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

	var out []string
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		name := fn.Name.Name
		report := func(as *ast.AssignStmt, ifs *ast.IfStmt) {
			// **条件と body の両方で not-found 判定を探す。** 正しい直し方は
			//
			//	if err != nil {
			//		if !repository.IsNotFound(err) { return 500 }
			//		return 404
			//	}
			//
			// で、判定は body の中にある。条件だけ見ると**直したものを検出し
			// 続ける** (実際に検出し続けた)。
			if !condMentionsErr(ifs.Cond, as) || checksNotFound(ifs) || !bodyReturns4xx(ifs.Body) {
				return
			}
			out = append(out, fmt.Sprintf("%s:%s\t%s (line %d)",
				rel, name, lookupMethodName(as), fset.Position(as.Pos()).Line))
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			// **`if x, err := repo.Find(...); err != nil` の形も見る。**
			// init 部分での代入を落とすと、この書き方に切り替えるだけで gate を
			// 素通りする (実際に素通りした)。
			if ifs, ok := n.(*ast.IfStmt); ok && ifs.Init != nil {
				if as, ok := ifs.Init.(*ast.AssignStmt); ok && isRepoLookup(as) {
					report(as, ifs)
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
				for j := i + 1; j < len(blk.List) && j <= i+lookupIfLookahead; j++ {
					if ifs, ok := blk.List[j].(*ast.IfStmt); ok {
						report(as, ifs)
						break
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
	if isRepoLookupMethod(sel.Sel.Name) {
		return sel.Sel.Name
	}
	return ""
}

// condMentionsErr reports whether the condition reads the error variable that
// the lookup assigned.
//
// **代入の左辺と突き合わせる。** 名前のパターンで見ると、`err2` /
// `errFind` を拾うために部分一致にしても `n, e := repo.Find(...)` のような
// 1 文字名が漏れる (どちらも実際に素通りした)。lookup が何に書いたかは
// AST から分かるので、そちらを正とする。
func condMentionsErr(cond ast.Expr, as *ast.AssignStmt) bool {
	names := map[string]bool{}
	for _, lhs := range as.Lhs {
		if id, ok := lhs.(*ast.Ident); ok && id.Name != "_" {
			names[id.Name] = true
		}
	}
	found := false
	ast.Inspect(cond, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && names[id.Name] {
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
	for _, dir := range []string{"internal/api", "internal/server"} {
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
