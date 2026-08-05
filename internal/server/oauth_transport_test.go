package server

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
)

// 本番では必ず SSRF-guarded transport を返す。ここが素通しになると、
// attacker-suppliable な client_id URL で内部ネットワークを走査できてしまう。
func TestOAuthDiscoveryTransport_GuardedInProduction(t *testing.T) {
	tr := oauthDiscoveryTransport(nil, false)
	assert.NotSame(t, http.DefaultTransport, tr, "本番で素の transport を返さないこと")
}

// TestMode では本家に揃えて private IP への fetch を許可する。
func TestOAuthDiscoveryTransport_RelaxedInTestMode(t *testing.T) {
	t.Setenv(checkIPRangeEnvKey, "")
	tr := oauthDiscoveryTransport(nil, true)
	assert.Same(t, http.DefaultTransport, tr)
}

// 本家同様、MISSKEY_TEST_CHECK_IP_RANGE=1 ならガードを戻す。
func TestOAuthDiscoveryTransport_EnvRestoresGuard(t *testing.T) {
	t.Setenv(checkIPRangeEnvKey, "1")
	tr := oauthDiscoveryTransport(nil, true)
	assert.NotSame(t, http.DefaultTransport, tr, "環境変数でガードが戻ること")
}

// "1" 以外の値ではガードを戻さない (本家は === '1' で比較する)。
func TestOAuthDiscoveryTransport_EnvOtherValueKeepsRelaxed(t *testing.T) {
	t.Setenv(checkIPRangeEnvKey, "0")
	tr := oauthDiscoveryTransport(nil, true)
	assert.Same(t, http.DefaultTransport, tr)
}
