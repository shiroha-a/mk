package activitypub

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerateAndParseKeypair(t *testing.T) {
	priv, pub, err := GenerateRSAKeypair()
	require.NoError(t, err)
	assert.Contains(t, priv, "RSA PRIVATE KEY")
	assert.Contains(t, pub, "PUBLIC KEY")

	rsaPriv, err := ParseRSAPrivateKey(priv)
	require.NoError(t, err)
	assert.NotNil(t, rsaPriv)

	rsaPub, err := ParseRSAPublicKey(pub)
	require.NoError(t, err)
	assert.NotNil(t, rsaPub)
}

func TestParseRSAPrivateKey_Invalid(t *testing.T) {
	_, err := ParseRSAPrivateKey("not pem")
	assert.Error(t, err)
}

func TestParseRSAPrivateKey_BadContents(t *testing.T) {
	pem := "-----BEGIN RSA PRIVATE KEY-----\nbm90YWtleQ==\n-----END RSA PRIVATE KEY-----\n"
	_, err := ParseRSAPrivateKey(pem)
	assert.Error(t, err)
}

// TestParseRSAPrivateKey_PKCS8 は Misskey TS が格納する PKCS8 形式の RSA
// 秘密鍵を mk-go が読めることを確認する回帰テスト (#369 drop-in)。
func TestParseRSAPrivateKey_PKCS8(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	// openssl 3.x デフォルトと同じ PKCS8 形式で encode する。
	pkcs8Bytes, err := x509.MarshalPKCS8PrivateKey(priv)
	require.NoError(t, err)
	pemStr := string(pem.EncodeToMemory(&pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: pkcs8Bytes,
	}))

	parsed, err := ParseRSAPrivateKey(pemStr)
	require.NoError(t, err, "PKCS8 形式の RSA 鍵が parse できる")
	assert.Equal(t, priv.N, parsed.N)
}

// TestParseRSAPrivateKey_PKCS8NotRSA は PKCS8 に RSA 以外の鍵 (ed25519)
// が入っていた場合は明示的に reject することを確認する。
func TestParseRSAPrivateKey_PKCS8NotRSA(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	pkcs8Bytes, err := x509.MarshalPKCS8PrivateKey(priv)
	require.NoError(t, err)
	pemStr := string(pem.EncodeToMemory(&pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: pkcs8Bytes,
	}))

	_, err = ParseRSAPrivateKey(pemStr)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not RSA")
}

func TestParseRSAPublicKey_Invalid(t *testing.T) {
	_, err := ParseRSAPublicKey("not pem")
	assert.Error(t, err)
}

func TestParseRSAPublicKey_BadContents(t *testing.T) {
	pem := "-----BEGIN PUBLIC KEY-----\nbm90YWtleQ==\n-----END PUBLIC KEY-----\n"
	_, err := ParseRSAPublicKey(pem)
	assert.Error(t, err)
}

func TestParseRSAPublicKey_NotRSA(t *testing.T) {
	// Ed25519 鍵を生成して PEM にエンコードし、ParseRSAPublicKey の
	// 「not RSA」分岐を踏ませる。
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	derBytes, err := x509.MarshalPKIXPublicKey(pub)
	require.NoError(t, err)
	pemStr := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: derBytes}))

	_, err = ParseRSAPublicKey(pemStr)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not an RSA")
}

// errReader is an io.Reader that always errors.
type errReader struct{}

func (errReader) Read(_ []byte) (int, error) { return 0, assertErr }

var assertErr = newSentinelErr("rand fail")

type sentinelErr struct{ msg string }

func (e *sentinelErr) Error() string { return e.msg }

func newSentinelErr(s string) error { return &sentinelErr{msg: s} }

func TestGenerateRSAKeypair_RandError(t *testing.T) {
	// Go 1.26 で crypto/rsa.GenerateKey の `random` 引数が完全に無視される
	// 仕様に変更された (https://go.dev/doc/go1.26 crypto/rsa セクション、
	// "The random parameter to GenerateKey ... is now ignored.") ため、
	// SetRandReaderForTest で errReader を注入しても rsa.GenerateKey が
	// それを消費せずに getrandom 経由で成功してしまう。entropy source 系
	// のエラーを擬似できなくなったので skip。production の randReader 変数
	// は ed25519.GenerateKey 経路で引き続き使われるため残す。
	t.Skip("Go 1.26+: rsa.GenerateKey ignores random parameter (release notes)")
}
