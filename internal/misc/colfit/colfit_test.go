package colfit_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/shiroha-a/mk/internal/misc/colfit"
)

func TestFits(t *testing.T) {
	cases := []struct {
		name string
		s    string
		max  int
		want bool
	}{
		{"empty fits", "", 4, true},
		{"exactly max fits", "abcd", 4, true},
		{"over max does not fit", "abcde", 4, false},
		// PostgreSQL の varchar はコードポイントで数えるので、byte 実装なら落ちる。
		{"multibyte counted in runes", strings.Repeat("あ", 4), 4, true},
		{"multibyte over max", strings.Repeat("あ", 5), 4, false},
		{"NUL never fits", "a\x00b", 8, false},
		{"NUL alone never fits", "\x00", 8, false},
		// 不正な UTF-8 は幅に関わらず入らない。
		{"invalid UTF-8 never fits", "a\x80b", 8, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, colfit.Fits(tc.s, tc.max))
		})
	}
}

func TestToStorable(t *testing.T) {
	assert.Equal(t, "ab", colfit.ToStorable("a\x00b"))
	assert.Equal(t, "", colfit.ToStorable("\x00\x00"))
	// NUL も不正な UTF-8 も無い値は同じ文字列がそのまま返る (無駄な再構築を
	// しない)。
	assert.Equal(t, "plain", colfit.ToStorable("plain"))
	// **不正な UTF-8 は U+FFFD へ矯正する。** 落とす (削る) のではないのは、
	// `Text` が rune で数えて切るため。
	assert.Equal(t, "a\uFFFDb", colfit.ToStorable("a\x80b"))
	assert.Equal(t, "a\uFFFDb", colfit.ToStorable("a\xed\xa0\x80b"), "孤立サロゲート")
	assert.Equal(t, "\uFFFD", colfit.ToStorable("\xff"))
	// NUL と不正な UTF-8 が同時に来ても両方処理する。
	assert.Equal(t, "a\uFFFDb", colfit.ToStorable("a\x00\x80b"))
	// **矯正した結果が必ず列に入ること**まで見る。片方しか処理しない実装だと
	// ここで落ちる。
	for _, in := range []string{"a\x00b", "a\x80b", "a\x00\x80b", "\xed\xa0\x80"} {
		assert.True(t, colfit.Storable(colfit.ToStorable(in)), "矯正後も列に入らない: %q", in)
	}
}

func TestTruncateRunes(t *testing.T) {
	assert.Equal(t, "abc", colfit.TruncateRunes("abcdef", 3))
	assert.Equal(t, "abc", colfit.TruncateRunes("abc", 3))
	assert.Equal(t, strings.Repeat("あ", 3), colfit.TruncateRunes(strings.Repeat("あ", 5), 3))
	// max <= 0 は「切らない」。無制限の text 列 (note.text) 用。
	assert.Equal(t, "abcdef", colfit.TruncateRunes("abcdef", 0))
	assert.Equal(t, "abcdef", colfit.TruncateRunes("abcdef", -1))
}

func TestText(t *testing.T) {
	assert.Equal(t, "ab", colfit.Text("a\x00b", 8))
	// NUL を落としてから数えるので、NUL の分で切り詰まらない。
	assert.Equal(t, "abc", colfit.Text("a\x00bc", 3))
	// **合成済みの 1 rune で書く。** 結合文字 (a + U+0301) だと見た目 4 文字でも
	// 5 rune になり、「4 rune を 4 で切る」つもりのケースが「5 rune を 4 で切る」
	// ケースにすり替わる。列はコードポイントで数えるのでどちらも有効な入力だが、
	// テストの意図と一致させる。
	assert.Equal(t, "ábcd", colfit.Text("ábcd", 4))
	assert.Equal(t, "ábcd", colfit.Text("ábcde", 4))
	assert.Equal(t, "ab", colfit.Text("a\x00b", 0))
	assert.Equal(t, "", colfit.Text("\x00", 4))
	// 不正な UTF-8 は U+FFFD 1 rune として数える。byte で数えていると
	// `"a\x80b"` が 3 byte 扱いになり、切る位置がずれる。
	assert.Equal(t, "a\uFFFD", colfit.Text("a\x80b", 2))
	assert.Equal(t, "a\uFFFDb", colfit.Text("a\x80b", 8))
}

// Storable は長さを見ない。**幅を混ぜると、比較できるだけの値まで落とす。**
func TestStorable(t *testing.T) {
	for _, tt := range []struct {
		name string
		in   string
		want bool
	}{
		{"素の文字列", "abc", true},
		{"空文字", "", true},
		{"長い文字列でも幅は見ない", strings.Repeat("x", 10000), true},
		{"非 ASCII", "こんにちは", true},
		{"NUL を含む", "a\x00b", false},
		{"NUL だけ", "\x00", false},
		{"末尾の NUL", "abc\x00", false},
		// **不正な UTF-8 も列に入らない** (SQLSTATE 22021)。以前は NUL しか
		// 見ておらず、クエリパラメータに `%80` を混ぜるだけで 500 を起こせた。
		{"孤立した継続バイト", "a\x80b", false},
		{"UTF-8 で符号化した孤立サロゲート", "a\xed\xa0\x80b", false},
		{"0xFF", "\xff", false},
		{"切れた multibyte", "\xe3\x81", false},
		// 正当な非 ASCII は落とさない。
		{"絵文字", "\U0001F600", true},
		{"結合文字", "a\u0301", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, colfit.Storable(tt.in))
		})
	}
}
