package selfcheck

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// tlsServerWithExpiry starts an HTTPS test server whose leaf certificate
// expires at the given time, and returns a Checker that trusts it.
//
// httptest.NewTLSServer の証明書は有効期限が固定なので、期限の分岐を確かめる
// には自前で発行する必要がある。
func tlsServerWithExpiry(t *testing.T, notAfter time.Time) *Checker {
	t.Helper()

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"links": []any{}})
	}))
	// httptest の自己署名証明書をベースに NotAfter だけ差し替える。
	cert := mustIssueCert(t, notAfter)
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}} //nolint:gosec // テスト用の自己署名
	srv.StartTLS()
	t.Cleanup(srv.Close)

	c := NewChecker(srv.URL)
	// 自己署名を信頼させる。検査対象は期限であって証明書チェーンではない。
	pool := x509.NewCertPool()
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	require.NoError(t, err)
	pool.AddCert(leaf)
	c.client.Transport = &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}
	return c
}

func TestCheckTLS_HealthyCertificate(t *testing.T) {
	c := tlsServerWithExpiry(t, time.Now().Add(90*24*time.Hour))
	got := c.CheckTLS(context.Background())
	require.Equal(t, StatusOK, got.Status, got.Detail)
	assert.Contains(t, got.Detail, "残り")
}

// 期限が近いと warn。証明書が切れると連合は署名検証より手前の TLS で全滞留
// するので、更新の猶予がある時点で出す。
func TestCheckTLS_NearExpiryWarns(t *testing.T) {
	c := tlsServerWithExpiry(t, time.Now().Add(3*24*time.Hour))
	got := c.CheckTLS(context.Background())
	require.Equal(t, StatusWarn, got.Status, got.Detail)
	assert.NotEmpty(t, got.Hint)
}

// 期限切れは fail。連合が TLS の時点で全滞留する状態なので警告では足りない。
func TestCheckTLS_ExpiredFails(t *testing.T) {
	c := tlsServerWithExpiry(t, time.Now().Add(-24*time.Hour))
	// 期限切れ証明書は TLS handshake 自体が失敗するので、検証を緩めて
	// 「証明書は読めるが期限切れ」の状態を作る。
	c.client.Transport = &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}, //nolint:gosec // 期限切れ分岐の検証のため
	}
	got := c.CheckTLS(context.Background())
	require.Equal(t, StatusFail, got.Status, got.Detail)
	assert.Contains(t, got.Hint, "至急")
}

// http なら対象外。
func TestCheckTLS_SkipsForHTTP(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)
	assert.Equal(t, StatusSkip, NewChecker(srv.URL).CheckTLS(context.Background()).Status)
}

// https だが接続できないときは skip (証明書を見に行けないだけで、到達性の
// 失敗は他の検査が報告する)。
func TestCheckTLS_SkipsWhenUnreachable(t *testing.T) {
	got := NewChecker("https://127.0.0.1:1").CheckTLS(context.Background())
	assert.Equal(t, StatusSkip, got.Status)
}

// mustIssueCert generates a self-signed leaf valid until notAfter.
func mustIssueCert(t *testing.T, notAfter time.Time) tls.Certificate {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}
