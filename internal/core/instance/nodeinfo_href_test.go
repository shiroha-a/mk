package instance

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// **discovery が指す先を検証せずに fetch していた。** `/.well-known/nodeinfo`
// はリモートが返す JSON なので、`links[].href` に任意の URL を書ける。到達先は
// SSRF-safe transport なので private IP へは行かないが、任意の public host /
// 任意ポートへの GET リレーは成立し、しかも nodeinfo の取得は content-type を
// 検査しないので、**返ってきた任意の JSON が nodeinfo として instance 行へ
// 書き戻される** (software.name / nodeName / nodeDescription / themeColor)。
func TestSelectNodeinfoHrefRequiresSameHost(t *testing.T) {
	const host = "remote.example"
	link := func(rel, href string) *nodeinfoDiscovery {
		return &nodeinfoDiscovery{Links: []nodeinfoLink{{Rel: rel, Href: href}}}
	}
	rel := preferredRels[0]

	t.Run("同じ host の https は選ぶ", func(t *testing.T) {
		got := selectNodeinfoHref(link(rel, "https://remote.example/nodeinfo/2.1"), host)
		require.Equal(t, "https://remote.example/nodeinfo/2.1", got)
	})

	t.Run("既定ポートの明記は同じ host として扱う", func(t *testing.T) {
		got := selectNodeinfoHref(link(rel, "https://remote.example:443/nodeinfo/2.1"), host)
		require.Equal(t, "https://remote.example:443/nodeinfo/2.1", got)
	})

	t.Run("大文字小文字は無視する", func(t *testing.T) {
		got := selectNodeinfoHref(link(rel, "https://REMOTE.example/nodeinfo/2.1"), host)
		require.Equal(t, "https://REMOTE.example/nodeinfo/2.1", got)
	})

	// **http も選ぶ。** リバースプロキシで TLS を終端していて `url` が http の
	// インスタンスは href を `http://` で advertise する。落とすとその相手の
	// メタデータが永久に取れない (レビュー M4)。
	t.Run("http の href も選ぶ", func(t *testing.T) {
		got := selectNodeinfoHref(link(rel, "http://remote.example/nodeinfo/2.1"), host)
		require.Equal(t, "http://remote.example/nodeinfo/2.1", got)
	})

	for name, href := range map[string]string{
		"別 host":         "https://evil.example/nodeinfo/2.1",
		"別ポート":           "https://remote.example:8443/nodeinfo/2.1",
		"スキーム無し":         "//evil.example/nodeinfo/2.1",
		"file スキーム":      "file:///etc/passwd",
		"host を含む別 host": "https://remote.example.evil.example/nodeinfo/2.1",
		"user info 偽装":   "https://remote.example@evil.example/nodeinfo/2.1",
	} {
		t.Run("選ばない: "+name, func(t *testing.T) {
			require.Empty(t, selectNodeinfoHref(link(rel, href), host),
				"host 外を指す href を選んでいる")
		})
	}

	// 優先度の高い rel が host 外を指していても、次の候補へ落ちる。
	t.Run("host 外は飛ばして次の候補を見る", func(t *testing.T) {
		if len(preferredRels) < 2 {
			t.Skip("rel が 1 つしかない")
		}
		disc := &nodeinfoDiscovery{Links: []nodeinfoLink{
			{Rel: preferredRels[0], Href: "https://evil.example/nodeinfo/2.1"},
			{Rel: preferredRels[1], Href: "https://remote.example/nodeinfo/2.0"},
		}}
		require.Equal(t, "https://remote.example/nodeinfo/2.0", selectNodeinfoHref(disc, host))
	})
}

// **既定ポートは数値で比べ、scheme ごとに剥がす (3 周目レビュー M3 / L5)。**
// 文字列一致だと `:0443` を別 host として扱い、`:443` しか剥がさないと
// `http://h:80` を advertise する相手 (http を許した理由そのもの) を落とす。
func TestNodeinfoHrefBelongsTo_DefaultPorts(t *testing.T) {
	cases := []struct {
		href, host string
		want       bool
	}{
		{"https://remote.example:443/ni", "remote.example", true},
		{"https://remote.example:0443/ni", "remote.example", true},
		{"https://remote.example:00443/ni", "remote.example", true},
		{"http://remote.example:80/ni", "remote.example", true},
		{"http://remote.example:080/ni", "remote.example", true},
		{"https://remote.example/ni", "remote.example:443", true},
		// scheme が違えば既定も違う。
		{"https://remote.example:80/ni", "remote.example", false},
		{"http://remote.example:443/ni", "remote.example", false},
		// 非既定ポートは別 host。
		{"https://remote.example:8443/ni", "remote.example", false},
		// IPv6 リテラル (bracket の有無で食い違わないこと)。
		{"https://[2001:db8::1]:443/ni", "[2001:db8::1]", true},
		{"https://[2001:db8::1]/ni", "[2001:db8::1]", true},
		{"https://[2001:db8::1]:8443/ni", "[2001:db8::1]", false},
	}
	for _, tc := range cases {
		t.Run(tc.href+" vs "+tc.host, func(t *testing.T) {
			require.Equal(t, tc.want, nodeinfoHrefBelongsTo(tc.href, tc.host))
		})
	}
}
