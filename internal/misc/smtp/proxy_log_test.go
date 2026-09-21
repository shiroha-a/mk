package smtp

import (
	"bytes"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// **パース失敗時に proxySmtp の URL をログへ出さないこと。**
//
// `socks5://user:pass@host` の形を取るので、丸ごと出すと認証情報がログに残る。
// 発火条件は普通の設定ミス (パスワードに `[` や `/` が入る、scheme 書き忘れ)。
// 同じ値を `config_dump` は `redactURLUserinfo` でマスクしている。
func TestDialSMTP_DoesNotLogProxyCredentials(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	// scheme 書き忘れ = `u.Host == ""` でフォールバック経路に入る。
	const secret = "s3cr3t-password"
	_, _ = dialSMTP("127.0.0.1:0", "user:"+secret+"@proxy.example:1080", 10*time.Millisecond)

	out := buf.String()
	require.NotContains(t, out, secret, "パスワードをログに出さないこと")
	require.NotContains(t, out, "proxy.example", "ホストも出さないこと (URL 全体を出さない)")
	require.Contains(t, out, "invalid proxySmtp URL", "警告自体は残すこと")
}
