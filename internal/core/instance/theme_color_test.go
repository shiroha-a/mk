package instance_test

import (
	"strings"
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
		// hex 形は NUL が混ざると一致しないので落ちる。**関数形式は違う** —
		// matcher が anchor していないので `rgb(1,2,3)` + NUL は valid のまま
		// 通る。NUL が列に届かないのは、書く値が `#rrggbb` に組み直されている
		// から (#2726)。
		{"hex に NUL が混ざれば書かない", `"#ab\u0000cdef"`, nil},
		{"関数形式の後ろの NUL は通るが列には届かない", `"rgb(1,2,3)\u0000"`, strp("#010203")},
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

// **列が安全なのは入力の長さや可読性ではなく、書く値を組み直しているから。**
// 関数形式 (rgb / hsl / hsv) の matcher は anchor していないので、5000 文字の
// 前置ゴミが付いた `rgb(1,2,3)` は tinycolor でも mk-go でも valid で、保存
// されるのは組み直した 7 文字になる (hex と色名は完全一致なので落ちる)。
// 「長い値は落ちるから安全」と読むと、実際には成り立たない不変条件を信じることに
// なる (#2726)。
func TestFetch_ThemeColorLengthDoesNotMatter(t *testing.T) {
	long := strings.Repeat("あ", 5000) + "rgb(1,2,3)"
	got := fetchNodeinfo(t, `{"software":{"name":"misskey"},"metadata":{"themeColor":"`+long+`"}}`)
	require.NotNil(t, got.ThemeColor)
	assert.Equal(t, "#010203", *got.ThemeColor)
}
