package server

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// 絵文字の登録申請 (#2934) の配線を照合する正規表現。
//
// **識別子と整形を固定しない。** #2939 でリテラル照合にしたところ、変数名を
// 変えただけで落ち、しかも診断が事実と逆を出した (#2940 が High に挙げた型)。
var (
	// 申請の入口。**行単位で見る** — ファイル全文から識別子を探す形だと、
	// create から policy を剥がしても admin 側などファイル内のどこかに
	// 識別子が残っていれば通る (敵対的レビューで実測)。
	// **文単位で取る。** router は引数を複数行に折り返すので、行単位だと
	// policy の行を拾えない (admin 側と同じ形)。
	// **`api.POST(` の直後の改行を許す (レビュー R6)。** 引数を 1 行ずつに割る
	// 整形 (middleware を足すときの普通の形) で `\("` が一致しなくなり、
	// 「登録されていない」と事実と逆の診断を出した。
	//
	// **次の `api.` が来る前で止める。** 貪欲に取ると 3 本が 1 つに繋がり、
	// 件数の検査が壊れる。
	createRouteRe = regexp.MustCompile(`(?s)api\.POST\(\s*"/emoji-application/create"[^\n]*(?:\n\t\t+[^\n]*)*`)
	// **否定側も同じ形に揃える (レビュー R7)。** 行単位のままだと、折り返して
	// 継続行に policy を足されたときに素通りする。肯定側を直したときに揃えて
	// いなかった片側更新。
	listRouteRe   = regexp.MustCompile(`(?s)api\.POST\(\s*"/emoji-application/list-mine"[^\n]*(?:\n\t\t+[^\n]*)*`)
	cancelRouteRe = regexp.MustCompile(`(?s)api\.POST\(\s*"/emoji-application/cancel"[^\n]*(?:\n\t\t+[^\n]*)*`)

	// 審査は canManageCustomEmojis で gate する (承認は実質 admin/emoji/add)。
	//
	// **route の本数だけを数えない。** 数えるだけの形では policy を 3 本とも
	// 剥がしても通った (敵対的レビューで実測)。この機能で最も守るべき配線が
	// 無検査になる。route 行そのものを取り、policy と scope の有無を見る。
	//
	// `(?s)` と `[^;]*` で**次の文まで**取る — router は引数を複数行に折り返す。
	adminRouteRe = regexp.MustCompile(`(?s)api\.POST\(\s*"/admin/emoji-application/(?:list|approve|reject)"[^\n]*(?:\n\t\t+[^\n]*)*`)

	// 通知の read-time 解決。**2 経路あるので両方見る** — HTTP の一覧
	// (notificationsHandler) と realtime (notificationPublisher)。片方だけを
	// 見る形だと、もう片方を外しても素通りした (変異検証で実測)。
	notifHTTPLookupRe   = regexp.MustCompile(`notificationsHandler\.SetEmojiApplicationLookup\(`)
	notifStreamLookupRe = regexp.MustCompile(`notificationPublisher\.SetEmojiApplicationLookup\(`)

	// リモートの承認が broadcast すること。**識別子は固定しない** — `&copied` を
	// リテラルで見ると、無害な rename で「broadcast していない」と事実と逆の
	// 診断を出す (このファイル冒頭で自分が禁じている型)。
	publishRemoteRe = regexp.MustCompile(`publishEmojiAdded\(&\w+\)`)
)

// TestEmojiApplicationIsWired asserts the emoji registration request feature
// stays connected end to end (#2934).
//
// **壊れても誰も気付かない。** 症状は「申請ページが 403 になる」「審査タブが
// 空になる」「通知が出ない」で、エラーもログも出ない。#2762 が wiring-check を
// 作ったのと同じ型。
func TestEmojiApplicationIsWired(t *testing.T) {
	root := repoRootDir(t)
	router := filepath.Join(root, "internal", "server", "router.go")

	raw, err := os.ReadFile(router)
	require.NoError(t, err)
	src := stripComments(string(raw))

	createLine := createRouteRe.FindString(src)
	require.NotEmptyf(t, createLine,
		"%s が emoji-application/create を登録していない。申請できなくなる (#2934)", router)
	require.Containsf(t, createLine, "PolicyCanRequestCustomEmojis",
		"%s の emoji-application/create に canRequestCustomEmojis が付いていない。"+
			"ロールで制御できなくなる (識別子がファイル内の別の行にあっても意味が無い)", router)

	// **policy を付けていないこと**まで見る。付けると、policy を外された人が
	// 自分の申請を確認できなくなる。
	for name, re := range map[string]*regexp.Regexp{
		"list-mine": listRouteRe,
		"cancel":    cancelRouteRe,
	} {
		m := re.FindString(src)
		require.NotEmptyf(t, m, "%s が emoji-application/%s を登録していない", router, name)
		require.NotContainsf(t, m, "PolicyCanRequestCustomEmojis",
			"emoji-application/%s に申請の policy が付いている。policy を外された人が"+
				"自分の申請を確認・取り下げできなくなる", name)
	}

	adminRoutes := adminRouteRe.FindAllString(src, -1)
	require.Lenf(t, adminRoutes, 3,
		"%s の admin/emoji-application が 3 本揃っていない (list / approve / reject)", router)
	for _, route := range adminRoutes {
		require.Containsf(t, route, "PolicyCanManageCustomEmojis",
			"admin/emoji-application の route に canManageCustomEmojis が付いていない。"+
				"承認は実質 admin/emoji/add なので、権限なしで絵文字を登録できてしまう:\n%s", route)
		// **値まで見る。** 存在だけだと approve の scope を read へ降格しても
		// 通る (レビュー Low 5)。3 本が隣接していてコピペしやすい形なので、
		// 読み取りと書き込みを取り違える変更を止める。
		wantScope := "read:admin:emoji"
		if strings.Contains(route, "/approve") || strings.Contains(route, "/reject") {
			wantScope = "write:admin:emoji"
		}
		require.Containsf(t, route, `RequireScope("`+wantScope+`")`,
			"admin/emoji-application の route の scope が %q でない。third-party アプリへ"+
				"渡した狭いトークンで承認まで実行できる:\n%s", wantScope, route)
	}
	require.Regexpf(t, notifHTTPLookupRe, src,
		"%s が通知一覧 (HTTP) の申請 lookup を配線していない。通知欄に結果が出ない", router)
	require.Regexpf(t, notifStreamLookupRe, src,
		"%s が realtime の申請 lookup を配線していない。開いたままだと通知が届かない", router)

	// frontend 側。申請ページと審査タブが実在すること。
	fe := filepath.Join(root, "third_party", "misskey", "packages", "frontend")
	page := filepath.Join(fe, "src", "pages", "emoji-request.vue")
	if _, err := os.Stat(page); err != nil {
		if os.Getenv("MK_FRONTEND_GATES_REQUIRE_SUBMODULE") != "" {
			require.NoError(t, err, "submodule を要求する job なのに %s を読めない", page)
		}
		t.Skipf("submodule が無い (checkout する job でのみ検査する)")
	}

	routes := stripComments(readFileString(t, filepath.Join(fe, "src", "router.definition.ts")))
	require.Containsf(t, routes, "/emoji-request",
		"router.definition.ts に /emoji-request が無い。申請ページへ到達できない")

	manager := stripComments(readFileString(t, filepath.Join(fe, "src", "pages", "admin", "custom-emojis-manager2.vue")))
	require.Containsf(t, manager, "custom-emojis-manager.applications.vue",
		"カスタム絵文字の管理画面が申請タブを読み込んでいない。審査できなくなる")

	// **リモートのインポート申請の導線 (#2935)。** 権限を持つ人はその場で
	// インポートし、持たない人は申請する。条件は同じで押した先だけが違うので、
	// 片方が落ちても画面上は自然に見えてしまう。
	creator := stripComments(readFileString(t, filepath.Join(root, "internal", "api", "admin", "emoji_application.go")))
	emojiMenu := stripComments(readFileString(t, filepath.Join(fe, "src", "components", "global", "MkCustomEmoji.vue")))
	require.Containsf(t, emojiMenu, "requestRemoteEmojiImport(",
		"MkCustomEmoji が申請の導線を出していない。権限の無い人がリモート絵文字を頼めなくなる (#2935)")
	require.Containsf(t, emojiMenu, "canRequestCustomEmojis",
		"MkCustomEmoji が canRequestCustomEmojis を見ていない。ロールで制御できなくなる")

	// **リアクション側も見る (レビュー M4)。** importRemoteEmoji の呼び出し元は
	// 2 箇所あり、片方だけ対応すると「リアクションからは頼めない」という
	// 気付きにくい非対称になる (#2698 が両方を揃えた経緯がある)。
	reactionMenu := stripComments(readFileString(t, filepath.Join(fe, "src", "components", "MkReactionsViewer.reaction.vue")))
	require.Containsf(t, reactionMenu, "requestRemoteEmojiImport(",
		"MkReactionsViewer.reaction が申請の導線を出していない。リアクションから頼めなくなる (#2935)")
	// **policy も見る (レビュー M3)。** 隣の MkCustomEmoji では見ているのに
	// こちらだけ落ちていると、権限の無い人全員に「申請」が出る (server が
	// 403 で弾くので実害は UX だが、片側更新の型)。
	require.Containsf(t, reactionMenu, "canRequestCustomEmojis",
		"MkReactionsViewer.reaction が canRequestCustomEmojis を見ていない。ロールで制御できなくなる")

	// **期間上限のエラーを両方の申請経路で扱うこと (#2958)。** 自作画像は
	// `emoji-request.vue`、リモート絵文字は `request-remote-emoji.ts` が
	// 独立した switch を持つので、片方だけ足すと同じ上限が一方では
	// 「何かがおかしいようです」になる。共有ヘルパを呼んでいるかまで見る
	// (case だけ足して汎用文へ落とす形を通さない)。**ここだけは識別子を固定
	// する** — 冒頭で禁じている形だが、既存の `requestRemoteEmojiImport(` と
	// 同じく「呼び出し契約を跨いで固定する」用途。rename すると偽陽性になる
	// 代わりに、片側だけ直した状態が落ちる。
	pageSrc := stripComments(readFileString(t, page))
	remoteReq := stripComments(readFileString(t, filepath.Join(fe, "src", "utility", "request-remote-emoji.ts")))
	for _, tc := range []struct{ name, src string }{
		{"emoji-request.vue", pageSrc},
		{"request-remote-emoji.ts", remoteReq},
	} {
		require.Containsf(t, tc.src, "EMOJI_APPLICATION_QUOTA_EXCEEDED",
			"%s が期間上限のエラーを扱っていない。汎用の「何かがおかしいようです」になる (#2958)", tc.name)
		require.Containsf(t, tc.src, "emojiApplicationQuotaText(",
			"%s が期間・上限・再試行時刻を出していない。待てば通るのか設定で塞がれているのか分からない (#2958)", tc.name)
	}

	// **審査画面が media proxy を通すこと (レビュー M2 / R2-H3)。**
	// リモートの生 URL は `img-src 'self' data: blob:` を enforce している構成で
	// **黙ってブロックされる**。この 1 行が「モデレーターに画像が見える」と
	// 「何も出ない」を分けており、戻しても型もテストも通ってしまう。
	applications := stripComments(readFileString(t, filepath.Join(fe, "src", "pages", "admin", "custom-emojis-manager.applications.vue")))
	require.Containsf(t, applications, "getProxiedImageUrl(",
		"審査画面がリモートの生 URL を出している。CSP でブロックされて画像が見えない (#2935)")

	// **drive 取り込みはここで見ない (レビュー R2-M4)。**
	// `TestCreateFromRemoteApplicationStoresInDrive` が実挙動で担保しており、
	// リテラル照合を足しても検出力は増えず、`df` を rename しただけで
	// 「取り込んでいない」と**事実と逆の診断**を出すだけになる。
	//
	// broadcast は単体テストが無いのでここで見る。
	require.Regexpf(t, publishRemoteRe, creator,
		"リモートの承認が emojiAdded を broadcast していない。承認した絵文字がピッカーに出ない")

	// **承認が broadcast すること。** 落ちると、承認した絵文字が全クライアントの
	// リロードまでピッカーに出てこない。EmojiAdd との差分として気付きにくい。
	require.Containsf(t, creator, "h.publishEmojiAdded(e)",
		"承認が emojiAdded を broadcast していない。承認した絵文字がピッカーに出ない")

	// **単体テストの存在も見る。** 保存条件や所有者確認の担保はそこにしか無く、
	// 消えても Go のテストは件数が減るだけで緑になる。
	spec := findSpec(t, filepath.Join(root, "internal", "core", "emojiapplication"), "service_test.go")
	require.Containsf(t, readFileString(t, spec), "ErrForbidden",
		"%s が所有者の確認を試していない。他人の申請を取り下げられる回帰が無検査になる", spec)
}

// readFileString reads a file and fails the test when it cannot.
func readFileString(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoErrorf(t, err, "%s を読めない", path)
	return string(raw)
}

var _ fs.DirEntry // findSpec が io/fs を使う
