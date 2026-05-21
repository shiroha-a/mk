package ld_test

import (
	"crypto/ed25519"
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

// PKCS1 ("RSA PUBLIC KEY") 形式の PEM も accept する。Mastodon / Pleroma 等が
// 混在する fediverse で format がまちまちなので両方対応する。
func TestVerifyRsaSignature2017_AcceptsPKCS1PEM(t *testing.T) {
	p := ld.NewProcessor()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	pkcs1Bytes := x509.MarshalPKCS1PublicKey(&priv.PublicKey)
	pubPEM := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PUBLIC KEY", Bytes: pkcs1Bytes}))

	// 不正 signatureValue で必ず fail するが、PEM parse path は通過することを
	// 確認する (= ErrSignatureMismatch、ErrInvalidSignaturePEM ではない)。
	err = p.VerifyRsaSignature2017(map[string]any{
		"@context": "https://www.w3.org/ns/activitystreams",
		"type":     "Note",
		"id":       "https://example.com/notes/n1",
		"signature": map[string]any{
			"type":           "RsaSignature2017",
			"creator":        "https://example.com/users/alice#main-key",
			"created":        "2026-05-21T00:00:00Z",
			"signatureValue": "AAAA",
		},
	}, pubPEM)
	require.Error(t, err)
	assert.ErrorIs(t, err, ld.ErrSignatureMismatch)
}

// PEM block の type が想定外 (= EC PUBLIC KEY 等の RSA 以外) のとき
// ErrInvalidSignaturePEM を返す。
func TestVerifyRsaSignature2017_RejectsUnknownPEMBlockType(t *testing.T) {
	p := ld.NewProcessor()
	pubPEM := string(pem.EncodeToMemory(&pem.Block{Type: "EC PUBLIC KEY", Bytes: []byte("dummy")}))

	err := p.VerifyRsaSignature2017(map[string]any{
		"@context": "https://www.w3.org/ns/activitystreams",
		"type":     "Note",
		"signature": map[string]any{
			"type":           "RsaSignature2017",
			"creator":        "https://example.com/users/alice#main-key",
			"created":        "2026-05-21T00:00:00Z",
			"signatureValue": "AAAA",
		},
	}, pubPEM)
	require.Error(t, err)
	assert.ErrorIs(t, err, ld.ErrInvalidSignaturePEM)
}

// PKCS1 として読めない bytes を持つ PEM block は ErrInvalidSignaturePEM。
func TestVerifyRsaSignature2017_RejectsCorruptPKCS1PEM(t *testing.T) {
	p := ld.NewProcessor()
	pubPEM := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PUBLIC KEY", Bytes: []byte("not a pkcs1 key")}))
	err := p.VerifyRsaSignature2017(map[string]any{
		"@context": "https://www.w3.org/ns/activitystreams",
		"signature": map[string]any{
			"type":           "RsaSignature2017",
			"creator":        "https://example.com/users/alice#main-key",
			"signatureValue": "AAAA",
		},
	}, pubPEM)
	require.Error(t, err)
	assert.ErrorIs(t, err, ld.ErrInvalidSignaturePEM)
}

// PKIX として読めない bytes を持つ PEM block も ErrInvalidSignaturePEM。
func TestVerifyRsaSignature2017_RejectsCorruptPKIXPEM(t *testing.T) {
	p := ld.NewProcessor()
	pubPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: []byte("not a pkix key")}))
	err := p.VerifyRsaSignature2017(map[string]any{
		"@context": "https://www.w3.org/ns/activitystreams",
		"signature": map[string]any{
			"type":           "RsaSignature2017",
			"creator":        "https://example.com/users/alice#main-key",
			"signatureValue": "AAAA",
		},
	}, pubPEM)
	require.Error(t, err)
	assert.ErrorIs(t, err, ld.ErrInvalidSignaturePEM)
}

// PKIX PUBLIC KEY だが中身が RSA 以外 (= Ed25519 等) なら not RSA error。
func TestVerifyRsaSignature2017_RejectsNonRSAPKIXPEM(t *testing.T) {
	p := ld.NewProcessor()
	// Ed25519 で適当な PKIX PUBLIC KEY を作る。実 verify は走らず PEM parse
	// 直後の cast で reject される shape。
	edPub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	der, err := x509.MarshalPKIXPublicKey(edPub)
	require.NoError(t, err)
	pubPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))

	err = p.VerifyRsaSignature2017(map[string]any{
		"@context": "https://www.w3.org/ns/activitystreams",
		"signature": map[string]any{
			"type":           "RsaSignature2017",
			"creator":        "https://example.com/users/alice#main-key",
			"signatureValue": "AAAA",
		},
	}, pubPEM)
	require.Error(t, err)
	assert.ErrorIs(t, err, ld.ErrInvalidSignaturePEM)
	assert.Contains(t, err.Error(), "not an RSA public key")
}

// signatureValue が base64 不正のときの分岐をカバー。
func TestVerifyRsaSignature2017_BadBase64SignatureValue(t *testing.T) {
	p := ld.NewProcessor()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	pubDER, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	require.NoError(t, err)
	pubPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER}))

	err = p.VerifyRsaSignature2017(map[string]any{
		"@context": "https://www.w3.org/ns/activitystreams",
		"signature": map[string]any{
			"type":           "RsaSignature2017",
			"creator":        "https://example.com/users/alice#main-key",
			"signatureValue": "!!!!not-base64!!!!",
		},
	}, pubPEM)
	require.Error(t, err)
	assert.ErrorIs(t, err, ld.ErrInvalidSignature)
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
