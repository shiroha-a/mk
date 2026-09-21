package entity

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// **設定を毎回読むこと。**
//
// 起動時に焼き込むと、運営者が管理画面で切り替えてもプロセスを再起動するまで
// 反映されない。proxyRemoteFiles は閲覧者の IP がリモートへ漏れるかどうかを
// 決める設定なので、締めたつもりで漏れ続ける形になる。
func TestMediaURLContext_ProxyRemoteFilesIsLive(t *testing.T) {
	t.Parallel()

	c := NewMediaURLContext("https://example.test", "", nil, false, true)
	require.True(t, c.shouldProxyRemote(), "起動時の値を使うこと")

	current := true
	c.SetProxyRemoteFilesLookup(func() bool { return current })
	require.True(t, c.shouldProxyRemote())

	// 運営者が切り替えた。再起動なしで反映されること。
	current = false
	require.False(t, c.shouldProxyRemote(), "live lookup が効いていること")

	current = true
	require.True(t, c.shouldProxyRemote())
}
