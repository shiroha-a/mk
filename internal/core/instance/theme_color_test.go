package instance_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// themeColor は upstream (`getThemeColor` の tinycolor) と同じく検証して
// `#rrggbb` に正規化する。mk-go は長さと NUL しか見ておらず、`red` や
// `rgb(1,2,3)` をそのまま列に入れていた (#2726)。
//
// 変換そのものは internal/misc/csscolor が実物の tinycolor2 と突き合わせて
// いる。ここは**配線されていること**だけを見る。
func TestFetch_NormalizesThemeColor(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want *string
	}{
		{"hex3 は展開する", `"#f00"`, strp("#ff0000")},
		{"大文字は小文字に揃える", `"#AABBCC"`, strp("#aabbcc")},
		{"色名も受ける", `"red"`, strp("#ff0000")},
		{"rgb() も受ける", `"rgb(0, 255, 0)"`, strp("#00ff00")},
		{"alpha は捨てる", `"rgba(0,0,255,0.5)"`, strp("#0000ff")},
		{"色として読めない値は書かない", `"not-a-color"`, nil},
		{"NUL 入りは書かない", `"#ab\u0000cdef"`, nil},
		{"空文字は書かない", `""`, nil},
		{"string でなければ書かない", `123`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := fetchNodeinfo(t, `{"software":{"name":"misskey"},"metadata":{"themeColor":`+tc.in+`}}`)
			if tc.want == nil {
				assert.Nil(t, got.ThemeColor)
				return
			}
			require.NotNil(t, got.ThemeColor)
			assert.Equal(t, *tc.want, *got.ThemeColor)
		})
	}
}

func strp(s string) *string { return &s }
