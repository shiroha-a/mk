package ld

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func rsaPrivPEMPKCS1(t *testing.T, priv *rsa.PrivateKey) string {
	t.Helper()
	der := x509.MarshalPKCS1PrivateKey(priv)
	return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: der}))
}

func sampleActivity() map[string]any {
	return map[string]any{
		"@context": "https://www.w3.org/ns/activitystreams",
		"id":       "https://example.com/notes/n1/activity",
		"type":     "Create",
		"actor":    "https://example.com/users/alice",
		"to":       []any{"https://www.w3.org/ns/activitystreams#Public"},
		"object": map[string]any{
			"id":      "https://example.com/notes/n1",
			"type":    "Note",
			"content": "hello world",
		},
	}
}

// SignRsaSignature2017 が生成した署名が production verify path
// (VerifyRsaSignature2017) を通ること = sign/verify が byte 対称であること (#1870)。
func TestSignRsaSignature2017_RoundTrip(t *testing.T) {
	p := NewProcessor()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	activity := sampleActivity()
	creator := "https://example.com/users/alice#main-key"
	created := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)

	signed, err := p.SignRsaSignature2017(activity, rsaPrivPEMPKCS1(t, priv), creator, created)
	require.NoError(t, err)

	// 元 activity は変更されない (signature を持たない)
	_, hadSig := activity["signature"]
	assert.False(t, hadSig, "input activity must not be mutated")

	sig, ok := signed["signature"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, LDSignatureType, sig["type"])
	assert.Equal(t, creator, sig["creator"])
	assert.NotEmpty(t, sig["nonce"], "nonce must be present (upstream parity)")
	assert.NotEmpty(t, sig["created"])
	assert.NotEmpty(t, sig["signatureValue"])

	require.NoError(t, p.VerifyRsaSignature2017(signed, rsaPubPEM(t, priv)),
		"production-signed activity must verify under the production verifier")
}

// 署名後に document を改竄すると verify が必ず失敗する。
func TestSignRsaSignature2017_TamperedFailsVerify(t *testing.T) {
	p := NewProcessor()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	signed, err := p.SignRsaSignature2017(sampleActivity(), rsaPrivPEMPKCS1(t, priv),
		"https://example.com/users/alice#main-key", time.Now())
	require.NoError(t, err)

	signed["actor"] = "https://evil.example/users/mallory"
	assert.Error(t, p.VerifyRsaSignature2017(signed, rsaPubPEM(t, priv)),
		"tampered actor must break verification")
}

// PKCS8 形式の private key でも署名できる。
func TestSignRsaSignature2017_PKCS8Key(t *testing.T) {
	p := NewProcessor()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	require.NoError(t, err)
	privPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))

	signed, err := p.SignRsaSignature2017(sampleActivity(), privPEM,
		"https://example.com/users/alice#main-key", time.Now())
	require.NoError(t, err)
	require.NoError(t, p.VerifyRsaSignature2017(signed, rsaPubPEM(t, priv)))
}

func TestSignRsaSignature2017_BadPrivateKey(t *testing.T) {
	p := NewProcessor()
	_, err := p.SignRsaSignature2017(sampleActivity(), "not a pem",
		"https://example.com/users/alice#main-key", time.Now())
	assert.ErrorIs(t, err, ErrInvalidSignaturePEM)
}

type failingNonceReader struct{}

func (failingNonceReader) Read([]byte) (int, error) { return 0, errors.New("rand failure") }

func TestSignRsaSignature2017_NonceError(t *testing.T) {
	orig := ldNonceRand
	ldNonceRand = failingNonceReader{}
	defer func() { ldNonceRand = orig }()

	p := NewProcessor()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	_, err = p.SignRsaSignature2017(sampleActivity(), rsaPrivPEMPKCS1(t, priv),
		"https://example.com/users/alice#main-key", time.Now())
	assert.Error(t, err)
}
