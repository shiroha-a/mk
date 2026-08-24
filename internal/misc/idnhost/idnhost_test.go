package idnhost

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// **保存されている host は punycode なので、比較の両辺をそこへ揃える** (#2704)。
func TestPuny(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"unicode IDN to punycode", "パイ.example", "xn--eckve.example"},
		{"already punycode", "xn--eckve.example", "xn--eckve.example"},
		{"uppercase punycode", "XN--ECKVE.EXAMPLE", "xn--eckve.example"},
		{"uppercase ascii", "Remote.Example", "remote.example"},
		{"plain ascii", "remote.example", "remote.example"},
		{"empty", "", ""},
		{"host with port", "remote.example:3000", "remote.example:3000"},
		{"unicode with port", "パイ.example:3000", "xn--eckve.example:3000"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, Puny(tc.in))
		})
	}

	t.Run("idna が失敗する入力は小文字化だけして返す", func(t *testing.T) {
		// 不正な punycode ラベル。ここで空文字や panic を返すと、比較の片側が
		// 消えて**別ホストを同一視する**方向に倒れるので、素通しが安全側。
		assert.Equal(t, "xn--0", Puny("XN--0"))
	})

	t.Run("idempotent", func(t *testing.T) {
		for _, in := range []string{"パイ.example", "xn--eckve.example", "remote.example"} {
			assert.Equal(t, Puny(in), Puny(Puny(in)), in)
		}
	})

	t.Run("ideographic dot は畳まない", func(t *testing.T) {
		// Go の idna は U+3002 を `.` にしない。別 authority を同一視しない
		// 安全側なので、この挙動を固定しておく。
		assert.NotEqual(t, "xn--eckve.example", Puny("パイ。example"))
	})
}

func TestPunyPtr(t *testing.T) {
	assert.Nil(t, PunyPtr(nil), "nil はローカル指定なのでそのまま")

	in := "パイ.example"
	got := PunyPtr(&in)
	assert.NotNil(t, got)
	assert.Equal(t, "xn--eckve.example", *got)
	assert.Equal(t, "パイ.example", in, "引数を書き換えないこと")
}
