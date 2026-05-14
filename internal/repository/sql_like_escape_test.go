package repository

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestEscapeSQLLikePattern covers the 3 escapeable characters (\ % _) and
// confirms identity behavior for plain inputs (#1054).
func TestEscapeSQLLikePattern(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		want string
	}{
		{name: "plain ASCII unchanged", in: "remote.example", want: "remote.example"},
		{name: "empty string unchanged", in: "", want: ""},
		{name: "percent escaped", in: "fo%o", want: `fo\%o`},
		{name: "underscore escaped", in: "fo_o", want: `fo\_o`},
		{name: "backslash escaped first", in: `fo\o`, want: `fo\\o`},
		{name: "multiple specials mixed", in: `a%b_c\d`, want: `a\%b\_c\\d`},
		// 既存 backslash + `_` の組み合わせで二重 escape されないこと
		// (StringReplacer は left-to-right one-pass で各文字を独立処理する)。
		{name: "underscore preceded by literal slash", in: `a/_b`, want: `a/\_b`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, escapeSQLLikePattern(tc.in))
		})
	}
}
