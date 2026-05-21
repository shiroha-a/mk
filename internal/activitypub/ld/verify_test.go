package ld_test

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"testing"

	"github.com/shiroha-a/mk/internal/activitypub/ld"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVerifyRsaSignature2017_MissingSignature(t *testing.T) {
	p := ld.NewProcessor()
	err := p.VerifyRsaSignature2017(map[string]any{
		"@context": "https://www.w3.org/ns/activitystreams",
		"type":     "Note",
	}, "")
	require.Error(t, err)
	assert.ErrorIs(t, err, ld.ErrNoSignatureField)
}

func TestVerifyRsaSignature2017_NonObjectSignature(t *testing.T) {
	p := ld.NewProcessor()
	err := p.VerifyRsaSignature2017(map[string]any{
		"signature": "i-am-not-an-object",
	}, "")
	require.Error(t, err)
	assert.ErrorIs(t, err, ld.ErrInvalidSignature)
}

func TestVerifyRsaSignature2017_UnsupportedType(t *testing.T) {
	p := ld.NewProcessor()
	err := p.VerifyRsaSignature2017(map[string]any{
		"signature": map[string]any{
			"type":           "Ed25519Signature2020", // unsupported algorithm
			"signatureValue": "AAAA",
		},
	}, "")
	require.Error(t, err)
	assert.ErrorIs(t, err, ld.ErrUnsupportedSigType)
}

func TestVerifyRsaSignature2017_MissingSignatureValue(t *testing.T) {
	p := ld.NewProcessor()
	err := p.VerifyRsaSignature2017(map[string]any{
		"signature": map[string]any{
			"type": "RsaSignature2017",
		},
	}, "")
	require.Error(t, err)
	assert.ErrorIs(t, err, ld.ErrInvalidSignature)
}

func TestVerifyRsaSignature2017_InvalidPEM(t *testing.T) {
	p := ld.NewProcessor()
	err := p.VerifyRsaSignature2017(map[string]any{
		"@context": "https://www.w3.org/ns/activitystreams",
		"type":     "Note",
		"signature": map[string]any{
			"type":           "RsaSignature2017",
			"creator":        "https://example.com/users/alice#main-key",
			"created":        "2026-05-21T00:00:00Z",
			"signatureValue": "AAAA",
		},
	}, "not a pem")
	require.Error(t, err)
	assert.ErrorIs(t, err, ld.ErrInvalidSignaturePEM)
}

// random RSA key で sign しても mk-go の verify が detect することを確認 (= 鍵
// 不一致経路の sanity)。byte-for-byte TS 互換 verify は drop-in e2e で検証する。
func TestVerifyRsaSignature2017_WrongKeyRejected(t *testing.T) {
	p := ld.NewProcessor()
	// 2 つの異なる鍵を生成し、片方で sign しているテスト用 fixture の verify に
	// もう片方の公開鍵を渡すと ErrSignatureMismatch になる、という意図。
	// ただし本 PR では mk-go 側に sign 経路がまだ無いので「empty signature
	// payload で random pubkey に verify を回す」shape の test に縮退させる。
	otherKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	pubDER, err := x509.MarshalPKIXPublicKey(&otherKey.PublicKey)
	require.NoError(t, err)
	pubPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER})

	err = p.VerifyRsaSignature2017(map[string]any{
		"@context": "https://www.w3.org/ns/activitystreams",
		"type":     "Note",
		"id":       "https://example.com/notes/n1",
		"signature": map[string]any{
			"type":           "RsaSignature2017",
			"creator":        "https://example.com/users/alice#main-key",
			"created":        "2026-05-21T00:00:00Z",
			"signatureValue": "AAAA", // 不正な signature value、verify は必ず失敗する
		},
	}, string(pubPEM))
	require.Error(t, err)
	assert.ErrorIs(t, err, ld.ErrSignatureMismatch)
}
