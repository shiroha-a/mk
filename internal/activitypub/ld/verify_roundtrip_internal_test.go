package ld

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// signActivity produces a valid RsaSignature2017 signatureValue for the given
// activity + signature options, using the exact same createVerifyData path the
// verifier consumes. これは upstream JsonLdService.signRsaSignature2017 と等価な
// 署名生成 (= createVerifyData → sha256 → RSA-PKCS1v15 SHA-256 sign)。
//
// production には sign 経路がまだ無いため、本 internal test 内で createVerifyData
// を直接呼んで round-trip を成立させる。これにより「有効な署名が verify を通る」
// positive case と「改竄で必ず mismatch する」case を単体で検証できる。
func signActivity(t *testing.T, p *Processor, priv *rsa.PrivateKey, activity, sigOptions map[string]any) string {
	t.Helper()
	verifyData, err := p.createVerifyData(activity, sigOptions)
	require.NoError(t, err)
	finalHash := sha256.Sum256([]byte(verifyData))
	sigBytes, err := rsa.SignPKCS1v15(rand.Reader, priv, crypto.SHA256, finalHash[:])
	require.NoError(t, err)
	return base64.StdEncoding.EncodeToString(sigBytes)
}

func rsaPubPEM(t *testing.T, priv *rsa.PrivateKey) string {
	t.Helper()
	pubDER, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	require.NoError(t, err)
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER}))
}

// baseSignedActivity returns a minimal AS activity carrying a fully-populated
// RsaSignature2017 signed with priv. caller は返り値を改竄して tampered case を
// 作れる。
func baseSignedActivity(t *testing.T, p *Processor, priv *rsa.PrivateKey) map[string]any {
	t.Helper()
	activity := map[string]any{
		"@context":     "https://www.w3.org/ns/activitystreams",
		"id":           "https://example.com/notes/n1",
		"type":         "Note",
		"content":      "hello world",
		"attributedTo": "https://example.com/users/alice",
	}
	sigOptions := map[string]any{
		"type":    LDSignatureType,
		"creator": "https://example.com/users/alice#main-key",
		"created": "2026-05-21T00:00:00Z",
	}
	sigValue := signActivity(t, p, priv, activity, sigOptions)

	signature := make(map[string]any, len(sigOptions)+1)
	for k, v := range sigOptions {
		signature[k] = v
	}
	signature["signatureValue"] = sigValue
	activity["signature"] = signature
	return activity
}

// 有効な署名が verify を通って nil を返す positive round-trip。既存 verify_test.go
// は異常系のみで、成功パスの単体検証が欠けていたギャップを埋める。
func TestVerifyRsaSignature2017_ValidSignatureRoundTrip(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	p := NewProcessor()
	activity := baseSignedActivity(t, p, priv)

	// 検証は別 Processor instance で行う (= inbox 経路と同じく sign/verify が
	// 独立した instance になる状況を再現)。
	err = NewProcessor().VerifyRsaSignature2017(activity, rsaPubPEM(t, priv))
	require.NoError(t, err, "valid RsaSignature2017 must verify successfully")
}

// 署名後に document 本体の field を改竄すると document hash が変わり、verify が
// ErrSignatureMismatch で弾くこと。
func TestVerifyRsaSignature2017_TamperedDocumentRejected(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	p := NewProcessor()
	activity := baseSignedActivity(t, p, priv)

	// signatureValue はそのままに content を書き換える (= MITM が本文を差し替え
	// た状況)。
	activity["content"] = "tampered content"

	err = NewProcessor().VerifyRsaSignature2017(activity, rsaPubPEM(t, priv))
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrSignatureMismatch)
}

// 署名後に signature options (created) を改竄すると options hash が変わり、verify
// が ErrSignatureMismatch で弾くこと。
func TestVerifyRsaSignature2017_TamperedOptionsRejected(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	p := NewProcessor()
	activity := baseSignedActivity(t, p, priv)

	sig := activity["signature"].(map[string]any)
	sig["created"] = "2099-01-01T00:00:00Z"

	err = NewProcessor().VerifyRsaSignature2017(activity, rsaPubPEM(t, priv))
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrSignatureMismatch)
}

// 正しい署名でも別の鍵で verify すると弾かれること (= 鍵すり替え検知)。既存の
// WrongKeyRejected は不正 signatureValue での縮退版だったが、本 test は「有効な
// 署名 × 別鍵」という本来の鍵不一致シナリオを検証する。
func TestVerifyRsaSignature2017_ValidSignatureWrongKeyRejected(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	other, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	p := NewProcessor()
	activity := baseSignedActivity(t, p, priv)

	err = NewProcessor().VerifyRsaSignature2017(activity, rsaPubPEM(t, other))
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrSignatureMismatch)
}
