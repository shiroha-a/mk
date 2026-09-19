package ipnorm

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNormalize(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want string
		ok   bool
	}{
		{"IPv4 はそのまま", "192.0.2.1", "192.0.2.1", true},
		{"IPv6 は小文字・ゼロ圧縮へ", "2001:DB8::0001", "2001:db8::1", true},
		{"IPv6 の展開形も畳む", "2001:db8:0:0:0:0:0:1", "2001:db8::1", true},
		// **これが #3066 の要求。** IPv4-mapped を別の IP として重複集計しない。
		{"IPv4-mapped は IPv4 へ", "::ffff:192.0.2.1", "192.0.2.1", true},
		{"IPv4-mapped の大文字", "::FFFF:192.0.2.1", "192.0.2.1", true},
		{"IPv4-mapped の 16 進表記", "::ffff:c000:201", "192.0.2.1", true},
		// zone はこのホストのインタフェース名なので落とす。
		{"zone は落とす", "fe80::1%eth0", "fe80::1", true},
		{"loopback v6", "::1", "::1", true},
		// port 付きで届く経路がある (X-Forwarded-For)。
		{"IPv4 host:port", "192.0.2.1:5678", "192.0.2.1", true},
		{"IPv6 host:port", "[2001:db8::1]:443", "2001:db8::1", true},
		{"前後の空白は落とす", "  192.0.2.1  ", "192.0.2.1", true},
		{"空", "", "", false},
		{"空白のみ", "   ", "", false},
		{"IP でない", "not-an-ip", "", false},
		{"octet が多い", "1.2.3.4.5", "", false},
		{"範囲外", "256.0.0.1", "", false},
		{"CIDR は受けない", "192.0.2.0/24", "", false},
		{"ホスト名", "example.com", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := Normalize(tc.in)
			assert.Equal(t, tc.ok, ok)
			assert.Equal(t, tc.want, got)
		})
	}
}

// 畳んだ結果は再度畳んでも変わらない。記録時と検索時で 2 回通る経路があるので、
// 冪等でないと同じ値が別物になる。
func TestNormalize_IsIdempotent(t *testing.T) {
	for _, in := range []string{
		"192.0.2.1", "2001:DB8::0001", "::ffff:192.0.2.1", "fe80::1%eth0", "::1",
		"192.0.2.1:5678", "[2001:db8::1]:443",
	} {
		once, ok := Normalize(in)
		if !assert.True(t, ok, in) {
			continue
		}
		twice, ok := Normalize(once)
		assert.True(t, ok, in)
		assert.Equal(t, once, twice, "2 回通すと値が変わる: %q", in)
	}
}

// IPv4 と、それを IPv4-mapped で書いたものが同じ文字列になること。これが崩れると
// 同じ端末の観測が 2 行に分かれ、関連アカウントの候補として数えられなくなる。
func TestNormalize_MappedEqualsPlainIPv4(t *testing.T) {
	plain, ok := Normalize("203.0.113.9")
	assert.True(t, ok)
	mapped, ok := Normalize("::ffff:203.0.113.9")
	assert.True(t, ok)
	assert.Equal(t, plain, mapped)
}
