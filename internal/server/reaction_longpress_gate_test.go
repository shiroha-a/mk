package server

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"
)

// blockCommentRe matches a `/* ... */` comment.
var blockCommentRe = regexp.MustCompile(`(?s)/\*.*?\*/`)

// htmlCommentRe matches an `<!-- ... -->` comment.
//
// **template を無効化できる唯一の書き方。** Vue の template では `//` も `/* */` も
// コメントにならないので、`@contextmenu` を殺すならこれしかない。開始デリミタだけ
// 落とす実装では中身が残り、この gate の template 側の検査が空振りする。
var htmlCommentRe = regexp.MustCompile(`(?s)<!--.*?-->`)

// lineCommentRe matches a `//` comment to end of line.
//
// 行頭とは限らない (`foo(); // メモ`)。URL の `//` を巻き込むが、この gate が
// 探すのは識別子の有無だけなので害が無い。
var lineCommentRe = regexp.MustCompile(`(?m)//.*$`)

// TestReactionLongPressIsWired asserts that the reaction button still binds the
// long-press handler that makes the emoji menu reachable on iOS (#2932).
//
// **配線を消してもビルドもテストも通る。** `bindLongPress` 自体は単体テストで
// 固定してあるが (`test/unit/long-press.test.ts`)、`MkReactionsViewer.reaction.vue`
// から呼ぶのをやめても vue-tsc も vitest も何も言わない。症状は「iOS でリアクションを
// 長押ししてもメニューが出ない」だけで、エラーもログも出ない。#2762 が
// `wiring-check` を作ったのと同じ型。
//
// **upstream 追従で落ちるのが一番ありうる。** このファイルは upstream のもので、
// fork は差分を載せているだけなので、`git rebase --onto` で衝突を解消するときに
// 3 行が消えても気付けない。実際 2026.9.0 への載せ替えでは同じ frontend の別
// ファイルで衝突が起きている (docs/divergence.md §4-2)。
//
// **コメントは落としてから探す。** コメントアウトして残すのは消すのと同じ
// (#2762 の初版が行頭 `//` しか除外せず、`/* */` で囲んだ配線を素通りさせた)。
// **`<!-- -->` も落とす** — template では `//` も `/* */` もコメントにならないので、
// `@contextmenu` を殺す唯一の書き方がこれ。初版は開始デリミタだけ消しており、
// template 側の検査が空振りしていた。
//
// **どちらの順序でもネストは解けない。** `//` の除去は URL を巻き込むので、
// `'https://…'` を含む行に配線を書くとその行の識別子ごと消えて gate が落ちる。
// 逆に `<!--` を先に落とす今の順序では、文字列や `//` コメントの中に `<!--` が
// 単独で現れ、後方のどこかに `-->` があると、その間の生きた配線が消える。
// どちらも偽陽性 (検査していないのに落ちる) で、現 corpus では起きない。
// 起きたら行を分けること。
func TestReactionLongPressIsWired(t *testing.T) {
	root := filepath.Join(repoRootDir(t), "third_party", "misskey", "packages", "frontend")
	component := filepath.Join(root, "src", "components", "MkReactionsViewer.reaction.vue")
	utility := filepath.Join(root, "src", "utility", "long-press.ts")

	if _, err := os.Stat(component); err != nil {
		if os.Getenv("MK_FRONTEND_GATES_REQUIRE_SUBMODULE") != "" {
			require.NoError(t, err, "submodule を要求する job なのに %s を読めない", component)
		}
		t.Skipf("submodule が無い (checkout する job でのみ検査する)")
	}

	_, err := os.Stat(utility)
	require.NoError(t, err, "%s が無い。リアクションの長押しはこのファイルに実装がある (#2932)", utility)

	raw, err := os.ReadFile(component)
	require.NoError(t, err)
	src := stripComments(string(raw))

	require.Containsf(t, src, `from '@/utility/long-press.js'`,
		"%s が long-press を import していない。iOS ではリアクションからリモート絵文字を"+
			"インポートできなくなる (@contextmenu は iOS Safari では発火しない、#2932)", component)
	require.Containsf(t, src, "bindLongPress(",
		"%s が bindLongPress を呼んでいない。import だけ残っていても配線されていなければ同じこと", component)

	// **右クリックの経路も残っていること。** 長押しに寄せた拍子にデスクトップの
	// 導線を落とすと、今度は PC から開けなくなる。
	require.Containsf(t, src, "@contextmenu",
		"%s から @contextmenu が消えている。デスクトップの右クリックの導線が無くなる", component)

	// **iOS の callout 抑止。** これが無いと、長押しで「画像を保存」の吹き出しが
	// 出てメニューと競合する (ボタンの中身は絵文字の `<img>`)。
	require.Containsf(t, src, "-webkit-touch-callout: none",
		"%s に -webkit-touch-callout: none が無い。iOS で長押しすると画像の callout が出る", component)
}

// stripComments removes `//` and `/* */` comments so that commented-out code is
// not mistaken for live wiring.
func stripComments(src string) string {
	// **HTML コメントを先に落とす。** 中に `//` や `/* */` があると、先に行/ブロック
	// コメントを剥がした結果 `<!--` と `-->` が分断されて対応が取れなくなる。
	src = htmlCommentRe.ReplaceAllString(src, "")
	src = blockCommentRe.ReplaceAllString(src, "")
	src = lineCommentRe.ReplaceAllString(src, "")
	return src
}
