package urlpreview

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

func TestClipRunes(t *testing.T) {
	t.Parallel()

	require.Equal(t, "abc", clipRunes("abc", 10), "上限未満はそのまま")
	require.Equal(t, "abcde", clipRunes("abcde", 5), "上限ちょうどはそのまま")
	require.Equal(t, "abcde...", clipRunes("abcdef", 5), "超えたら切って省略記号")
	require.Equal(t, "", clipRunes("", 5))

	// **マルチバイトを壊さないこと。** バイトで切ると不正な UTF-8 になる。
	got := clipRunes(strings.Repeat("あ", 10), 3)
	require.Equal(t, "あああ...", got)
	require.Equal(t, 6, len([]rune(got)), "rune 単位で切れていること")
	require.True(t, utf8.ValidString(got), "不正な UTF-8 を作らないこと")

	// 3 rune = 9 バイト。バイトで切る実装だと途中で分断されて壊れる。
	require.Equal(t, 9+3, len(got), "先頭 3 文字ぶんのバイト長 + 省略記号")
}

// 取得したページの title / description が上限で切られること。
func TestParseHTML_ClipsAttackerControlledText(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("T", 500000)
	longDesc := strings.Repeat("D", 500000)
	html := fmt.Sprintf(`<html><head>`+
		`<meta property="og:title" content="%s">`+
		`<meta property="og:description" content="%s">`+
		`<meta property="og:site_name" content="%s">`+
		`</head><body>x</body></html>`, long, longDesc, long)

	res := ParseHTML(strings.NewReader(html), "https://example.test/p")
	require.NotNil(t, res.Title)
	require.NotNil(t, res.Description)
	require.NotNil(t, res.Sitename)

	// **期待値はリテラルで書く。** 定数を参照すると、上限を緩める変異と一緒に
	// 期待値まで動いて変異検証が素通りする。値は upstream の summaly
	// (title 100 / description 300) に揃えてある。
	require.LessOrEqual(t, len([]rune(*res.Title)), 103,
		"title が 100 rune + 省略記号で切られていること (実測 %d rune)", len([]rune(*res.Title)))
	require.LessOrEqual(t, len([]rune(*res.Description)), 303,
		"description が 300 rune + 省略記号で切られていること (実測 %d rune)", len([]rune(*res.Description)))
	require.LessOrEqual(t, len([]rune(*res.Sitename)), 103,
		"sitename が 100 rune + 省略記号で切られていること")
}

// 普通の長さは変えずに通すこと。
func TestParseHTML_KeepsShortText(t *testing.T) {
	t.Parallel()

	html := `<html><head><meta property="og:title" content="Hello"><meta property="og:description" content="World"></head><body>x</body></html>`
	res := ParseHTML(strings.NewReader(html), "https://example.test/p")
	require.NotNil(t, res.Title)
	require.Equal(t, "Hello", *res.Title)
	require.NotNil(t, res.Description)
	require.Equal(t, "World", *res.Description)
}

// **URL も取得元が決める文字列。**
//
// title / description を切っている理由 (**24 時間 Redis に載り、クエリ文字列を
// 変えれば URL は無限に作れる**) は同じ entry に載る URL 側にもそのまま当たる。
// 上限が無いと 1 本あたり `urlPreviewMaximumContentLength` (既定 10MiB) まで
// リニアに伸びた。
func TestParseHTML_DropsOversizedURLs(t *testing.T) {
	t.Parallel()

	long := "https://example.test/" + strings.Repeat("a", 300000)
	html := fmt.Sprintf(`<html><head>`+
		`<meta property="og:url" content="%s">`+
		`<meta property="og:image" content="%s">`+
		`<link rel="icon" href="%s">`+
		`<meta property="ap:id" content="%s">`+
		`</head><body>x</body></html>`, long, long, long, long)

	res := ParseHTML(strings.NewReader(html), "https://example.test/p")
	// canonical は空になったら pageURL へ倒れる (既存の挙動)。
	require.Equal(t, "https://example.test/p", res.URL,
		"長すぎる og:url は採らず、ページの URL に倒すこと")
	require.Nil(t, res.Thumbnail, "長すぎる og:image は出さないこと")
	require.Nil(t, res.Icon, "長すぎる icon は出さないこと")
	require.Nil(t, res.ActivityPub, "長すぎる ap:id は出さないこと")
}

// **普通の URL は通すこと** (上が「常に落とす」実装でも緑にならないようにする)。
func TestParseHTML_KeepsOrdinaryURLs(t *testing.T) {
	t.Parallel()

	html := `<html><head>` +
		`<meta property="og:url" content="https://example.test/canonical">` +
		`<meta property="og:image" content="https://example.test/img.png">` +
		`</head><body>x</body></html>`
	res := ParseHTML(strings.NewReader(html), "https://example.test/p")
	require.Equal(t, "https://example.test/canonical", res.URL)
	require.NotNil(t, res.Thumbnail)
	require.Equal(t, "https://example.test/img.png", *res.Thumbnail)
}
