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

	for name, href := range map[string]string{
		"別 host":         "https://evil.example/nodeinfo/2.1",
		"別ポート":           "https://remote.example:8443/nodeinfo/2.1",
		"http へ降格":       "http://remote.example/nodeinfo/2.1",
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
