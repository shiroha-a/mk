package server

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// リモート由来の URL を media proxy に通さず画像として出すのを止める (#2957)。
//
// **mk-go は `img-src 'self' data: blob:` を enforce している** (#2425)。upstream に
// CSP は無いので、upstream のファイルをそのまま使うと**リモートの画像だけが
// 黙って表示されない**。エラーも警告も出ず、開発者の手元 (CSP 無効) では再現
// しないので、本番で初めて気付く。
//
// **同じ型を 3 回踏んでいる**: #2903 (インポートのモーダル)、#2935 (絵文字申請の
// 審査画面)、#2957 (リモート絵文字の一覧)。個別に直すより新規流入を止める。
//
// **一般的な判定はできない。** `publicUrl` はローカル絵文字なら自オリジン、
// リモートなら相手のオリジンで、**静的には区別できない**。そこで #2792 と同じ
// allowlist 方式にする — 通す場合は**理由を書く**。
//
// **既知の取りこぼし。** どれも「リモート由来か」を名前から判定している以上、
// 原理的に残る:
//
//   - **`publicUrl` / `originalUrl` という名前でない**リモート URL は拾えない。
//     実測: 同じ画面のインポートログは `it.item.url` で、これには当たらない。
//   - 変数に入れてから渡す形、`v-bind` でまとめて渡す形。
//
// **カバー率は正直に書く。** 過去 3 件を実測すると、この検査が当たるのは
// **1 件だけ**:
//
//	#2903 モーダル   `const imgUrl = computed(() => props.emoji.url)`  → 当たらない
//	#2935 審査画面   `<img :src="app.url">`                            → 当たらない
//	#2957 一覧       `url: it.publicUrl`                               → 当たる
//
// 当初「3 回とも publicUrl/originalUrl を直接渡す形だった」と書いたが、**裏取り
// していない誤りだった** (上の 2 件は別の名前で受けている)。字面の素直な形しか
// 見ないので、**この検査だけに依存しないこと。**
// **行の断片で持つ (ファイル単位にしない)。** ファイル単位だと、同じファイルの
// **別の行から proxy を外しても素通りする** (実測で修正前の状態を再現しても
// 落ちなかった)。
type allowedRawMedia struct{ file, frag, why string }

var rawRemoteMediaAllowed = []allowedRawMedia{
	// 自サーバーが配る URL なので CSP に入る。**'self' とは限らない** —
	// object storage を使っていると `objectStorageBaseUrl` のオリジンになり、
	// 通るのは `cspMediaExtras` がそれを img-src に足しているから (実測: この
	// 運用サーバーのローカル絵文字は 16 件中 12 件が object storage 側)。
	// **object storage を無効化した / baseUrl を変えた後に古い行が残る運用では
	// img-src から落ちて壊れる。** proxy を通すと無駄な負荷になるので通さない。
	{"pages/admin/custom-emojis-manager.local.list.vue", "\t\turl: it.publicUrl,", "ローカル絵文字なので自オリジン"},
	// MkRemoteEmojiEditDialog へ渡すだけで、あちらが getProxiedImageUrl を
	// 通す (#2903)。ここで二重に通すと proxy の URL を proxy に食わせる。
	{"pages/admin/custom-emojis-manager.remote.vue", "url: target.publicUrl", "編集ダイアログが下流で proxy する"},
}

func allowedRawMediaLine(rel, line string) bool {
	for _, a := range rawRemoteMediaAllowed {
		if a.file == rel && strings.Contains(line, a.frag) {
			return true
		}
	}
	return false
}

// `url: x.publicUrl` / `:src="x.originalUrl"` のように、リモート由来の URL を
// そのまま画像へ渡している形。
var rawRemoteMediaRe = regexp.MustCompile(`(?:url:\s*|:src="[^"]*)\b[\w.]*\.(publicUrl|originalUrl)\b`)

// **免除は「その式を包んでいるか」で見る。** 行に `getProxiedImageUrl` という
// 文字列があるかだけを見ると、**同じ行の無関係な呼び出しで免除される** —
// `url: it.publicUrl, alt: getProxiedImageUrl(it.name, ...)` が素通りすることを
// 実測された。これは「識別子の出現だけを見る」型の、行の粒度での再発。
var proxiedRemoteMediaRe = regexp.MustCompile(`getProxied\w*Url\(\s*[\w.]*\.(publicUrl|originalUrl)\b`)

func TestRemoteMediaGoesThroughProxy(t *testing.T) {
	fe := filepath.Join(repoRootDir(t), "third_party", "misskey", "packages", "frontend", "src")
	if _, err := os.Stat(fe); err != nil {
		if os.Getenv("MK_FRONTEND_GATES_REQUIRE_SUBMODULE") != "" {
			require.NoError(t, err, "submodule を要求する job なのに %s を読めない", fe)
		}
		t.Skipf("submodule が無い (checkout する job でのみ検査する)")
	}

	var scanned int
	var violations []string
	require.NoError(t, filepath.WalkDir(fe, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Ext(path) != ".vue" {
			return err
		}
		src := stripComments(readFileString(t, path))
		rel, relErr := filepath.Rel(fe, path)
		require.NoError(t, relErr)
		rel = filepath.ToSlash(rel)
		// **行単位で見る (実測)。** 「ファイルのどこかに proxy があれば良し」に
		// すると、**同じファイルの別の行で proxy を使っているだけで素通りする**。
		// 実際 `custom-emojis-manager.remote.vue` は 2 箇所で使うので、修正前の
		// 状態を再現しても落ちなかった。
		for _, line := range strings.Split(src, "\n") {
			raw := rawRemoteMediaRe.FindAllStringIndex(line, -1)
			if len(raw) == 0 {
				continue
			}
			scanned++
			if allowedRawMediaLine(rel, line) {
				continue
			}
			// proxy が包んでいる範囲を集め、違反マッチがその中に収まるかを見る。
			wrapped := proxiedRemoteMediaRe.FindAllStringIndex(line, -1)
			for _, m := range raw {
				covered := false
				for _, w := range wrapped {
					if m[0] >= w[0] && m[1] <= w[1] {
						covered = true
						break
					}
				}
				if !covered {
					violations = append(violations, rel+": "+strings.TrimSpace(line))
					break
				}
			}
		}
		return nil
	}))

	// 拾えなかったら落とす。書式が変わって空振りすると、検査していないのに
	// 緑になる (#2874 / #2828 と同じ判断)。
	require.NotZerof(t, scanned, "`publicUrl` / `originalUrl` を画像へ渡す形を 1 つも拾えない (書式が変わった?)")
	require.Emptyf(t, violations,
		"リモート由来の URL を media proxy に通さず画像として出している: %v\n"+
			"`img-src 'self' data: blob:` を enforce している構成では**黙って表示されない** (#2957)。\n"+
			"`getProxiedImageUrl(url, 'emoji', false, true)` を通すか、\n"+
			"自オリジンで proxy が不要なら rawRemoteMediaAllowed に理由付きで登録すること", violations)

	// **allowlist が腐っていないか。** 実在しないパス / 当たらない断片が残ると、
	// 「除外したつもり」のまま検査の形だけが残る。
	for _, a := range rawRemoteMediaAllowed {
		src, err := os.ReadFile(filepath.Join(fe, filepath.FromSlash(a.file)))
		require.NoErrorf(t, err, "allowlist に実在しないパスが残っている: %s", a.file)
		require.Equalf(t, 1, strings.Count(string(src), a.frag),
			"allowlist の断片がちょうど 1 箇所に当たらない: %s の %q (%s)\n"+
				"0 なら除外が空振り、2 以上なら別の行まで除外している", a.file, a.frag, a.why)
	}
}
