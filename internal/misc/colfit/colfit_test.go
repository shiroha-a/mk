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
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, colfit.Fits(tc.s, tc.max))
		})
	}
}

func TestStripNUL(t *testing.T) {
	assert.Equal(t, "ab", colfit.StripNUL("a\x00b"))
	assert.Equal(t, "", colfit.StripNUL("\x00\x00"))
	// NUL を含まない値は同じ文字列がそのまま返る (無駄な再構築をしない)。
	assert.Equal(t, "plain", colfit.StripNUL("plain"))
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
	assert.Equal(t, "ábc", colfit.Text("ábcd", 4))
	assert.Equal(t, "ab", colfit.Text("a\x00b", 0))
	assert.Equal(t, "", colfit.Text("\x00", 4))
}
