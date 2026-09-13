package federation_test

import (
	"testing"

	"github.com/shiroha-a/mk/internal/core/federation"
	"github.com/shiroha-a/mk/internal/core/instance"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// blockedHosts に入るのは host だけ (管理画面が受け取るのもその形)。取り込み /
// 配送側が `:443` 付きの host を作ると `HostMatchesAny` の suffix 一致から外れる
// ので、突き合わせる値は既定ポートを剥がした形でなければならない。
func TestNormalizeGateHost_MatchesBlockedHostsEntry(t *testing.T) {
	blocked := []string{"blocked.example"}

	assert.True(t, instance.HostMatchesAny(blocked, federation.NormalizeGateHost("https://blocked.example:443/inbox")),
		"既定ポートを明記した inbox が blockedHosts から外れている")
	assert.True(t, instance.HostMatchesAny(blocked, federation.NormalizeGateHost("http://sub.blocked.example:80/inbox")),
		"サブドメイン + 既定ポートも blockedHosts に当たる")
	assert.True(t, instance.HostMatchesAny(blocked, federation.NormalizeGateHost("https://blocked.example/inbox")))

	// 非既定ポートは upstream でも別 host 扱い。ここを true にすると
	// blockedHosts の意味が upstream とずれるので、その方向も固定しておく。
	assert.False(t, instance.HostMatchesAny(blocked, federation.NormalizeGateHost("https://blocked.example:8443/inbox")))
}

// 配送側の block 判定が既定ポート付きの inbox でも効くこと。stubBlocker は
// 完全一致の map なので、正規化しないと `bad.example:443` は素通りする。
func TestDeliverActivity_SkipsBlockedHostSpelledWithDefaultPort(t *testing.T) {
	svc, enq, userRepo, _, keypairRepo := newDeliverService(t)
	installLocalSigner(t, userRepo, keypairRepo)
	svc.SetHostBlockChecker(&stubBlocker{blocked: map[string]bool{"bad.example": true}})

	require.NoError(t, svc.DeliverActivity("alice", []byte(`{}`), []string{
		"https://bad.example:443/inbox",
		"http://bad.example:80/inbox",
		"https://good.example:443/inbox",
	}))
	require.Len(t, enq.calls, 1, "既定ポート付きの綴りで block を回避できてはいけない")
	assert.Equal(t, "https://good.example:443/inbox", enq.calls[0].Inbox)
}

// federation: specified (allowlist) 側も同じこと。blocked とは独立した経路。
func TestDeliverActivity_SkipsDisallowedHostSpelledWithDefaultPort(t *testing.T) {
	svc, enq, userRepo, _, keypairRepo := newDeliverService(t)
	installLocalSigner(t, userRepo, keypairRepo)
	svc.SetHostBlockChecker(&stubBlocker{disallowed: map[string]bool{"other.example": true}})

	require.NoError(t, svc.DeliverActivity("alice", []byte(`{}`), []string{
		"https://other.example:443/inbox",
		"https://allowed.example/inbox",
	}))
	require.Len(t, enq.calls, 1)
	assert.Equal(t, "https://allowed.example/inbox", enq.calls[0].Inbox)
}

// 非既定ポートは別 host のまま = block されないこと。剥がす規則を
// 「ポートを全部落とす」に広げると、`bad.example:8443` まで巻き込んで
// upstream と挙動がずれる。
func TestDeliverActivity_NonDefaultPortIsADifferentHost(t *testing.T) {
	svc, enq, userRepo, _, keypairRepo := newDeliverService(t)
	installLocalSigner(t, userRepo, keypairRepo)
	svc.SetHostBlockChecker(&stubBlocker{blocked: map[string]bool{"bad.example": true}})

	require.NoError(t, svc.DeliverActivity("alice", []byte(`{}`), []string{"https://bad.example:8443/inbox"}))
	require.Len(t, enq.calls, 1)
}
