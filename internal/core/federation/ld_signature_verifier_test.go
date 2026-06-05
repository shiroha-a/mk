package federation_test

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"testing"

	corefederation "github.com/shiroha-a/mk/internal/core/federation"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLDSignatureVerifier_NoSignature_NoOp(t *testing.T) {
	repo := testutil.NewMockUserPublickeyRepository()
	v := corefederation.NewLDSignatureVerifier(repo)

	// signature field 無し → skip。HTTP Signature 経由でしか認証していない
	// 既存挙動の activity でも本 verifier は素通しすること (= 後方互換)。
	err := v.VerifyIfPresent([]byte(`{
		"@context": "https://www.w3.org/ns/activitystreams",
		"type": "Note",
		"id": "https://example.com/notes/n1"
	}`))
	require.NoError(t, err)
}

func TestLDSignatureVerifier_ForbiddenDirective_Rejected(t *testing.T) {
	repo := testutil.NewMockUserPublickeyRepository()
	// pubkey は適当に登録しておく (= forbidden directive で先に落ちて pubkey
	// resolve まで到達しないことを確認したい)。
	v := corefederation.NewLDSignatureVerifier(repo)

	err := v.VerifyIfPresent([]byte(`{
		"@context": "https://www.w3.org/ns/activitystreams",
		"type": "Note",
		"@graph": [{"type": "Note"}],
		"signature": {
			"type": "RsaSignature2017",
			"creator": "https://example.com/users/alice#main-key",
			"created": "2026-05-21T00:00:00Z",
			"signatureValue": "AAAA"
		}
	}`))
	require.Error(t, err)
	// hardening の forbidden directive で reject される (= ld.ErrForbiddenDirective)。
	assert.Contains(t, err.Error(), "forbidden")
}

func TestLDSignatureVerifier_MissingCreator_Rejected(t *testing.T) {
	repo := testutil.NewMockUserPublickeyRepository()
	v := corefederation.NewLDSignatureVerifier(repo)

	err := v.VerifyIfPresent([]byte(`{
		"type": "Note",
		"signature": {
			"type": "RsaSignature2017",
			"signatureValue": "AAAA"
		}
	}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "creator missing")
}

// signature field がオブジェクトでない (例: 文字列) 場合は reject。present=true
// を報告しつつ error を返す (VerifyAndCreator 経路)。
func TestLDSignatureVerifier_SignatureNotObject_Rejected(t *testing.T) {
	repo := testutil.NewMockUserPublickeyRepository()
	v := corefederation.NewLDSignatureVerifier(repo)

	creator, present, err := v.VerifyAndCreator([]byte(`{"type":"Note","signature":"not-an-object"}`))
	require.Error(t, err)
	assert.True(t, present, "signature field exists, so present must be true")
	assert.Empty(t, creator)
	assert.Contains(t, err.Error(), "not an object")
}

func TestLDSignatureVerifier_UnknownCreator_Rejected(t *testing.T) {
	repo := testutil.NewMockUserPublickeyRepository()
	v := corefederation.NewLDSignatureVerifier(repo)

	err := v.VerifyIfPresent([]byte(`{
		"type": "Note",
		"signature": {
			"type": "RsaSignature2017",
			"creator": "https://example.com/users/unknown#main-key",
			"signatureValue": "AAAA"
		}
	}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "public key not found")
}

func TestLDSignatureVerifier_BadSignatureValueRejected(t *testing.T) {
	repo := testutil.NewMockUserPublickeyRepository()
	// 本物の RSA キーを生成して登録するが、signatureValue は invalid base64
	// なので verify は必ず失敗する shape。
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	pubDER, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	require.NoError(t, err)
	pubPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER}))
	keyID := "https://example.com/users/alice#main-key"
	repo.Keys["alice"] = &model.UserPublickey{
		UserID: "alice",
		KeyID:  keyID,
		KeyPEM: pubPEM,
	}

	v := corefederation.NewLDSignatureVerifier(repo)
	err = v.VerifyIfPresent([]byte(`{
		"@context": "https://www.w3.org/ns/activitystreams",
		"type": "Note",
		"id": "https://example.com/notes/n1",
		"signature": {
			"type": "RsaSignature2017",
			"creator": "https://example.com/users/alice#main-key",
			"created": "2026-05-21T00:00:00Z",
			"signatureValue": "AAAA"
		}
	}`))
	require.Error(t, err)
	// rsa.VerifyPKCS1v15 経由の verify mismatch。
	assert.NotContains(t, err.Error(), "public key not found",
		"public key は resolve できた状態で signature verify で fail することを確認")
}

func TestLDSignatureVerifier_NilRepo_NoOp(t *testing.T) {
	// 防御的: pubkeyRepo nil でも panic せず素通し (= verify 経路を skip)。
	v := corefederation.NewLDSignatureVerifier(nil)
	err := v.VerifyIfPresent([]byte(`{"type":"Note"}`))
	require.NoError(t, err)
}

// LDSignatureVerifier interface 互換性 (= inbox processor 経由で wire される
// shape) を最低限 confirm する。
func TestLDSignatureVerifier_InterfaceShape(t *testing.T) {
	repo := testutil.NewMockUserPublickeyRepository()
	v := corefederation.NewLDSignatureVerifier(repo)
	// メソッド存在 (compile time check)
	_ = v.VerifyIfPresent
	assert.NotNil(t, v, "verifier must be non-nil")
}

var _ error = errors.New("compile-time anchor for errors import")
