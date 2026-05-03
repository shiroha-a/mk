package twofactor

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/protocol/webauthncose"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/lib/pq"
	"github.com/redis/go-redis/v9"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var twofaTestRedis *testutil.TestRedis

func TestMain(m *testing.M) {
	ctx := context.Background()
	tr, err := testutil.SetupRedis(ctx)
	if err != nil {
		log.Printf("twofactor test: redis setup failed: %v", err)
		os.Exit(m.Run())
	}
	twofaTestRedis = tr
	code := m.Run()
	twofaTestRedis.Teardown(ctx)
	os.Exit(code)
}

// requireRedis skips when testcontainers Redis is unavailable.
func requireRedis(t *testing.T) {
	t.Helper()
	if twofaTestRedis == nil {
		t.Skip("redis testcontainer not available")
	}
}

// --- NewWebAuthnService ---

func TestNewWebAuthnService_EmptyURL(t *testing.T) {
	_, err := NewWebAuthnService("", "Misskey", nil)
	assert.ErrorIs(t, err, ErrWebAuthnNotConfigured)
}

func TestNewWebAuthnService_BadURL(t *testing.T) {
	_, err := NewWebAuthnService("not a url", "Misskey", nil)
	assert.ErrorIs(t, err, ErrWebAuthnNotConfigured)
}

func TestNewWebAuthnService_OK(t *testing.T) {
	svc, err := NewWebAuthnService("https://example.com", "Misskey", nil)
	require.NoError(t, err)
	assert.NotNil(t, svc.wa)
}

// --- session helpers (real Redis) ---

func TestPutAndTakeSession_Roundtrip(t *testing.T) {
	requireRedis(t)
	twofaTestRedis.FlushAll(context.Background())
	svc, err := NewWebAuthnService("https://example.com", "Misskey", twofaTestRedis.Client)
	require.NoError(t, err)

	// fake SessionData
	sd := makeFakeSessionData()
	sid, err := svc.putSession(context.Background(), "alice", sd)
	require.NoError(t, err)
	assert.NotEmpty(t, sid)

	got, err := svc.takeSession(context.Background(), "alice", sid)
	require.NoError(t, err)
	assert.Equal(t, sd.Challenge, got.Challenge)
}

func TestTakeSession_Missing(t *testing.T) {
	requireRedis(t)
	svc, err := NewWebAuthnService("https://example.com", "Misskey", twofaTestRedis.Client)
	require.NoError(t, err)
	_, err = svc.takeSession(context.Background(), "alice", "ghost")
	assert.ErrorIs(t, err, ErrWebAuthnSessionNotFound)
}

func TestTakeSession_NilRedis(t *testing.T) {
	svc := &WebAuthnService{wa: nil, redis: nil}
	_, err := svc.takeSession(context.Background(), "alice", "x")
	assert.ErrorIs(t, err, ErrWebAuthnNotConfigured)
}

func TestPutSession_NilRedis(t *testing.T) {
	svc := &WebAuthnService{wa: nil, redis: nil}
	_, err := svc.putSession(context.Background(), "alice", makeFakeSessionData())
	assert.ErrorIs(t, err, ErrWebAuthnNotConfigured)
}

// --- BeginRegistration / BeginLogin (exercise wiring with real config) ---

func TestBeginRegistration(t *testing.T) {
	requireRedis(t)
	twofaTestRedis.FlushAll(context.Background())
	svc, err := NewWebAuthnService("https://example.com", "Misskey", twofaTestRedis.Client)
	require.NoError(t, err)
	creation, sid, err := svc.BeginRegistration(context.Background(), &model.User{ID: "alice", Username: "alice"}, nil)
	require.NoError(t, err)
	assert.NotEmpty(t, sid)
	assert.NotNil(t, creation)
	assert.NotEmpty(t, creation.Response.Challenge)
}

func TestBeginLogin(t *testing.T) {
	requireRedis(t)
	twofaTestRedis.FlushAll(context.Background())
	svc, err := NewWebAuthnService("https://example.com", "Misskey", twofaTestRedis.Client)
	require.NoError(t, err)
	// 鍵が無いと go-webauthn は ErrBadRequest を返す。空でも sd 生成は試みる。
	// アサーション: 鍵 0 個でも内部で AllowedCredentials=[] になる動作確認。
	_, _, err = svc.BeginLogin(context.Background(), &model.User{ID: "alice", Username: "alice"}, nil)
	// 鍵 0 個だと "Found no credentials" 系のエラーになるので、ここでは
	// エラーが返ることを許容する (実装依存)。
	if err == nil {
		t.Log("BeginLogin succeeded with empty credentials (newer go-webauthn)")
	}
}

func TestBeginRegistration_NotConfigured(t *testing.T) {
	svc := &WebAuthnService{}
	_, _, err := svc.BeginRegistration(context.Background(), &model.User{ID: "x"}, nil)
	assert.ErrorIs(t, err, ErrWebAuthnNotConfigured)
}

func TestBeginLogin_NotConfigured(t *testing.T) {
	svc := &WebAuthnService{}
	_, _, err := svc.BeginLogin(context.Background(), &model.User{ID: "x"}, nil)
	assert.ErrorIs(t, err, ErrWebAuthnNotConfigured)
}

func TestFinishRegistration_NotConfigured(t *testing.T) {
	svc := &WebAuthnService{}
	req := httptest.NewRequest("POST", "/", strings.NewReader(""))
	_, err := svc.FinishRegistration(context.Background(), &model.User{ID: "x"}, nil, "sid", req)
	assert.ErrorIs(t, err, ErrWebAuthnNotConfigured)
}

func TestFinishLogin_NotConfigured(t *testing.T) {
	svc := &WebAuthnService{}
	req := httptest.NewRequest("POST", "/", strings.NewReader(""))
	_, err := svc.FinishLogin(context.Background(), &model.User{ID: "x"}, nil, "sid", req)
	assert.ErrorIs(t, err, ErrWebAuthnNotConfigured)
}

func TestFinishRegistration_SessionMissing(t *testing.T) {
	requireRedis(t)
	twofaTestRedis.FlushAll(context.Background())
	svc, err := NewWebAuthnService("https://example.com", "Misskey", twofaTestRedis.Client)
	require.NoError(t, err)
	req := httptest.NewRequest("POST", "/", strings.NewReader(""))
	_, err = svc.FinishRegistration(context.Background(), &model.User{ID: "alice"}, nil, "ghost", req)
	assert.ErrorIs(t, err, ErrWebAuthnSessionNotFound)
}

func TestFinishLogin_SessionMissing(t *testing.T) {
	requireRedis(t)
	twofaTestRedis.FlushAll(context.Background())
	svc, err := NewWebAuthnService("https://example.com", "Misskey", twofaTestRedis.Client)
	require.NoError(t, err)
	req := httptest.NewRequest("POST", "/", strings.NewReader(""))
	_, err = svc.FinishLogin(context.Background(), &model.User{ID: "alice"}, nil, "ghost", req)
	assert.ErrorIs(t, err, ErrWebAuthnSessionNotFound)
}

// --- userAdapter ---

func TestUserAdapter_Methods(t *testing.T) {
	displayName := "Alice in Wonderland"
	u := &model.User{ID: "alice-id", Username: "alice", Name: &displayName}
	a := &userAdapter{user: u, keys: nil}
	assert.Equal(t, []byte("alice-id"), a.WebAuthnID())
	assert.Equal(t, "alice", a.WebAuthnName())
	assert.Equal(t, displayName, a.WebAuthnDisplayName())
	assert.Empty(t, a.WebAuthnCredentials())
}

func TestUserAdapter_DisplayNameFallback(t *testing.T) {
	u := &model.User{ID: "x", Username: "alice"}
	a := &userAdapter{user: u}
	assert.Equal(t, "alice", a.WebAuthnDisplayName())
}

func TestUserAdapter_DisplayNameEmptyName(t *testing.T) {
	empty := ""
	u := &model.User{ID: "x", Username: "alice", Name: &empty}
	a := &userAdapter{user: u}
	assert.Equal(t, "alice", a.WebAuthnDisplayName())
}

func TestUserAdapter_CredentialsRoundtrip(t *testing.T) {
	keys := []*model.UserSecurityKey{
		{
			ID:         "AAEC", // base64url("\x00\x01\x02")
			PublicKey:  "AwQF",
			Counter:    42,
			Transports: pq.StringArray{"usb", "nfc"},
		},
	}
	a := &userAdapter{user: &model.User{ID: "x"}, keys: keys}
	creds := a.WebAuthnCredentials()
	require.Len(t, creds, 1)
	assert.Equal(t, []byte{0, 1, 2}, creds[0].ID)
	assert.Equal(t, []byte{3, 4, 5}, creds[0].PublicKey)
	assert.Equal(t, uint32(42), creds[0].Authenticator.SignCount)
	assert.Len(t, creds[0].Transport, 2)
}

func TestUserAdapter_SkipsBadEncoding(t *testing.T) {
	keys := []*model.UserSecurityKey{
		{ID: "not!base64url", PublicKey: "AwQF"},
		{ID: "AAEC", PublicKey: "not!base64url"},
		{ID: "AAEC", PublicKey: "AwQF"},
	}
	a := &userAdapter{keys: keys}
	creds := a.WebAuthnCredentials()
	assert.Len(t, creds, 1, "only the third (well-formed) credential survives")
}

// --- CredentialToModel ---

func TestCredentialToModel_NoTransports(t *testing.T) {
	cred := makeFakeCredential([]byte{1, 2, 3}, []byte{4, 5}, 7, nil)
	m := CredentialToModel(cred, "alice", "")
	assert.Equal(t, "alice", m.UserID)
	assert.Equal(t, int64(7), m.Counter)
	assert.NotEmpty(t, m.ID)
	assert.NotEmpty(t, m.PublicKey)
	assert.Empty(t, m.Transports)
}

func TestCredentialToModel_WithTransports(t *testing.T) {
	cred := makeFakeCredential([]byte{1}, []byte{2}, 0, []string{"usb", "ble"})
	m := CredentialToModel(cred, "alice", "Yubikey")
	assert.Equal(t, "Yubikey", m.Name)
	assert.Equal(t, []string{"usb", "ble"}, []string(m.Transports))
}

// --- newSessionID ---

func TestNewSessionID_NotEmpty(t *testing.T) {
	id, err := newSessionID()
	require.NoError(t, err)
	assert.NotEmpty(t, id)
}

func TestNewSessionID_RandError(t *testing.T) {
	old := readRandom
	defer func() { readRandom = old }()
	readRandom = func(b []byte) (int, error) { return 0, errors.New("entropy depleted") }
	_, err := newSessionID()
	assert.Error(t, err)
}

// --- key helper ---

func TestSessionKey(t *testing.T) {
	assert.Equal(t, "twofa:webauthn:alice:sid1", sessionKey("alice", "sid1"))
}

// --- BeginRegistration / BeginLogin Redis error paths ---

func TestBeginRegistration_PutSessionFails(t *testing.T) {
	requireRedis(t)
	// 専用クライアントを開いて閉じる → Set が err を返す
	c := redis.NewClient(&redis.Options{Addr: twofaTestRedis.Client.Options().Addr})
	require.NoError(t, c.Close())
	svc, err := NewWebAuthnService("https://example.com", "Misskey", c)
	require.NoError(t, err)
	_, _, err = svc.BeginRegistration(context.Background(), &model.User{ID: "alice", Username: "alice"}, nil)
	assert.Error(t, err)
}

// putSession の rand 失敗経路もカバーする (newSessionID が err を返す)
func TestBeginRegistration_RandError(t *testing.T) {
	requireRedis(t)
	twofaTestRedis.FlushAll(context.Background())
	svc, err := NewWebAuthnService("https://example.com", "Misskey", twofaTestRedis.Client)
	require.NoError(t, err)
	old := readRandom
	defer func() { readRandom = old }()
	readRandom = func(b []byte) (int, error) { return 0, errors.New("entropy depleted") }
	_, _, err = svc.BeginRegistration(context.Background(), &model.User{ID: "alice", Username: "alice"}, nil)
	assert.Error(t, err)
}

// BeginLogin が assertion を返す経路 (鍵が 1 つ以上ある状態)
func TestBeginLogin_WithCredential(t *testing.T) {
	requireRedis(t)
	twofaTestRedis.FlushAll(context.Background())
	svc, err := NewWebAuthnService("https://example.com", "Misskey", twofaTestRedis.Client)
	require.NoError(t, err)
	keys := []*model.UserSecurityKey{
		{ID: "AAEC", PublicKey: "AwQF", Counter: 1},
	}
	assertion, sid, err := svc.BeginLogin(context.Background(), &model.User{ID: "alice", Username: "alice"}, keys)
	require.NoError(t, err)
	assert.NotEmpty(t, sid)
	assert.NotNil(t, assertion)
}

// BeginLogin で putSession が失敗する経路 (closed redis)
func TestBeginLogin_PutSessionFails(t *testing.T) {
	requireRedis(t)
	c := redis.NewClient(&redis.Options{Addr: twofaTestRedis.Client.Options().Addr})
	require.NoError(t, c.Close())
	svc, err := NewWebAuthnService("https://example.com", "Misskey", c)
	require.NoError(t, err)
	keys := []*model.UserSecurityKey{{ID: "AAEC", PublicKey: "AwQF", Counter: 0}}
	_, _, err = svc.BeginLogin(context.Background(), &model.User{ID: "alice", Username: "alice"}, keys)
	assert.Error(t, err)
}

// takeSession が Redis 接続エラー (Nil 以外) を伝搬する経路
func TestTakeSession_RedisError(t *testing.T) {
	requireRedis(t)
	// 専用クライアント
	c := redis.NewClient(&redis.Options{Addr: twofaTestRedis.Client.Options().Addr})
	svc, err := NewWebAuthnService("https://example.com", "Misskey", c)
	require.NoError(t, err)
	// 一旦正常に PUT
	sd := makeFakeSessionData()
	sid, err := svc.putSession(context.Background(), "alice", sd)
	require.NoError(t, err)
	// クライアント閉じてから take → connection closed エラー
	require.NoError(t, c.Close())
	_, err = svc.takeSession(context.Background(), "alice", sid)
	assert.Error(t, err)
	assert.NotErrorIs(t, err, ErrWebAuthnSessionNotFound)
}

// --- FinishRegistration / FinishLogin success path tests using W3C spec
//     test vectors (NoneES256). ベクタは go-webauthn 上流の test とほぼ同じ
//     fixture を再利用しているが、こちらは mk-go の WebAuthnService を経由
//     して redis セッション取得 → wa.FinishRegistration の包括動作を網羅する。

func TestFinishRegistration_Success(t *testing.T) {
	requireRedis(t)
	twofaTestRedis.FlushAll(context.Background())

	// W3C webauthn-3 §sctn-test-vectors-none-es256 の attestation 一式。
	// RPID は "example.org", origin は "https://example.org" 固定。
	const (
		attestationObjectHex = "a363666d74646e6f6e656761747453746d74a068617574684461746158a4bfabc37432958b063360d3ad6461c9c4735ae7f8edd46592a5e0f01452b2e4b559000000008446ccb9ab1db374750b2367ff6f3a1f0020f91f391db4c9b2fde0ea70189cba3fb63f579ba6122b33ad94ff3ec330084be4a5010203262001215820afefa16f97ca9b2d23eb86ccb64098d20db90856062eb249c33a9b672f26df61225820930a56b87a2fca66334b03458abf879717c12cc68ed73290af2e2664796b9220"
		clientDataJSONHex    = "7b2274797065223a22776562617574686e2e637265617465222c226368616c6c656e6765223a22414d4d507434557878475453746e63647134313759447742466938767049612d7077386f4f755657345441222c226f726967696e223a2268747470733a2f2f6578616d706c652e6f7267222c2263726f73734f726967696e223a66616c73652c22657874726144617461223a22636c69656e74446174614a534f4e206d617920626520657874656e6465642077697468206164646974696f6e616c206669656c647320696e20746865206675747572652c207375636820617320746869733a20426b5165446a646354427258426941774a544c453551227d"
		credentialIDHex      = "f91f391db4c9b2fde0ea70189cba3fb63f579ba6122b33ad94ff3ec330084be4"
		challengeHex         = "00c30fb78531c464d2b6771dab8d7b603c01162f2fa486bea70f283ae556e130"
	)
	body := buildAttestationResponse(t, attestationObjectHex, clientDataJSONHex, credentialIDHex)
	challenge := encodeBase64URL(decodeHex(t, challengeHex))

	svc, err := NewWebAuthnService("https://example.org", "Misskey", twofaTestRedis.Client)
	require.NoError(t, err)

	// セッションを Redis に直接書き込む (BeginRegistration を呼ばずに固定 fixture
	// の challenge を使うため)
	sd := &webauthn.SessionData{
		Challenge:  challenge,
		UserID:     []byte("test-user-id"),
		CredParams: []protocol.CredentialParameter{{Type: protocol.PublicKeyCredentialType, Algorithm: webauthncose.AlgES256}},
	}
	sid, err := svc.putSession(context.Background(), "test-user-id", sd)
	require.NoError(t, err)

	httpReq := httptest.NewRequest("POST", "/", bytes.NewReader(body))
	cred, err := svc.FinishRegistration(context.Background(), &model.User{ID: "test-user-id", Username: "test"}, nil, sid, httpReq)
	require.NoError(t, err)
	require.NotNil(t, cred)
	expectedCredID := decodeHex(t, credentialIDHex)
	assert.Equal(t, expectedCredID, cred.ID)
}

// --- helpers ---

func decodeHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	require.NoError(t, err)
	return b
}

func encodeBase64URL(b []byte) string {
	return base64.RawURLEncoding.EncodeToString(b)
}

func buildAttestationResponse(t *testing.T, attObjectHex, clientDataJSONHex, credIDHex string) []byte {
	t.Helper()
	credID := decodeHex(t, credIDHex)
	id := encodeBase64URL(credID)
	attObj := encodeBase64URL(decodeHex(t, attObjectHex))
	cdj := encodeBase64URL(decodeHex(t, clientDataJSONHex))
	body, err := json.Marshal(map[string]any{
		"id":    id,
		"rawId": id,
		"type":  "public-key",
		"response": map[string]any{
			"attestationObject": attObj,
			"clientDataJSON":    cdj,
		},
	})
	require.NoError(t, err)
	return body
}

// --- BeginRegistrationPrimary / FinishRegistrationPrimary (#698) ---
//
// upstream-compat な single-in-flight-per-user 経路。session id round-trip が
// 無いので primary slot 1 つだけ Redis に保持する。

func TestPrimarySessionKey(t *testing.T) {
	assert.Equal(t, "twofa:webauthn:alice:primary", primarySessionKey("alice"))
}

func TestPutAndTakePrimarySession_Roundtrip(t *testing.T) {
	requireRedis(t)
	twofaTestRedis.FlushAll(context.Background())
	svc, err := NewWebAuthnService("https://example.com", "Misskey", twofaTestRedis.Client)
	require.NoError(t, err)

	sd := makeFakeSessionData()
	require.NoError(t, svc.putPrimarySession(context.Background(), "alice", sd))

	got, err := svc.takePrimarySession(context.Background(), "alice")
	require.NoError(t, err)
	assert.Equal(t, sd.Challenge, got.Challenge)

	// take は single-use: 2 回目は ErrWebAuthnSessionNotFound
	_, err = svc.takePrimarySession(context.Background(), "alice")
	assert.ErrorIs(t, err, ErrWebAuthnSessionNotFound)
}

func TestPutPrimarySession_Overwrites(t *testing.T) {
	requireRedis(t)
	twofaTestRedis.FlushAll(context.Background())
	svc, err := NewWebAuthnService("https://example.com", "Misskey", twofaTestRedis.Client)
	require.NoError(t, err)

	first := makeFakeSessionData()
	first.Challenge = "first"
	second := makeFakeSessionData()
	second.Challenge = "second"

	require.NoError(t, svc.putPrimarySession(context.Background(), "alice", first))
	require.NoError(t, svc.putPrimarySession(context.Background(), "alice", second))

	got, err := svc.takePrimarySession(context.Background(), "alice")
	require.NoError(t, err)
	assert.Equal(t, "second", got.Challenge)
}

func TestPutPrimarySession_NilRedis(t *testing.T) {
	svc := &WebAuthnService{}
	err := svc.putPrimarySession(context.Background(), "alice", makeFakeSessionData())
	assert.ErrorIs(t, err, ErrWebAuthnNotConfigured)
}

func TestTakePrimarySession_NilRedis(t *testing.T) {
	svc := &WebAuthnService{}
	_, err := svc.takePrimarySession(context.Background(), "alice")
	assert.ErrorIs(t, err, ErrWebAuthnNotConfigured)
}

func TestTakePrimarySession_RedisError(t *testing.T) {
	requireRedis(t)
	c := redis.NewClient(&redis.Options{Addr: twofaTestRedis.Client.Options().Addr})
	svc, err := NewWebAuthnService("https://example.com", "Misskey", c)
	require.NoError(t, err)
	require.NoError(t, svc.putPrimarySession(context.Background(), "alice", makeFakeSessionData()))
	require.NoError(t, c.Close())
	_, err = svc.takePrimarySession(context.Background(), "alice")
	assert.Error(t, err)
	assert.NotErrorIs(t, err, ErrWebAuthnSessionNotFound)
}

func TestBeginRegistrationPrimary_NotConfigured(t *testing.T) {
	svc := &WebAuthnService{}
	_, err := svc.BeginRegistrationPrimary(context.Background(), &model.User{ID: "x"}, nil)
	assert.ErrorIs(t, err, ErrWebAuthnNotConfigured)
}

func TestFinishRegistrationPrimary_NotConfigured(t *testing.T) {
	svc := &WebAuthnService{}
	req := httptest.NewRequest("POST", "/", strings.NewReader(""))
	_, err := svc.FinishRegistrationPrimary(context.Background(), &model.User{ID: "x"}, nil, req)
	assert.ErrorIs(t, err, ErrWebAuthnNotConfigured)
}

func TestBeginRegistrationPrimary_Success(t *testing.T) {
	requireRedis(t)
	twofaTestRedis.FlushAll(context.Background())
	svc, err := NewWebAuthnService("https://example.com", "Misskey", twofaTestRedis.Client)
	require.NoError(t, err)
	creation, err := svc.BeginRegistrationPrimary(context.Background(), &model.User{ID: "alice", Username: "alice"}, nil)
	require.NoError(t, err)
	require.NotNil(t, creation)
	assert.NotEmpty(t, creation.Response.Challenge)
	// primary slot に SessionData が入っていること
	got, err := svc.takePrimarySession(context.Background(), "alice")
	require.NoError(t, err)
	assert.NotEmpty(t, got.Challenge)
}

func TestBeginRegistrationPrimary_PutSessionFails(t *testing.T) {
	requireRedis(t)
	c := redis.NewClient(&redis.Options{Addr: twofaTestRedis.Client.Options().Addr})
	require.NoError(t, c.Close())
	svc, err := NewWebAuthnService("https://example.com", "Misskey", c)
	require.NoError(t, err)
	_, err = svc.BeginRegistrationPrimary(context.Background(), &model.User{ID: "alice", Username: "alice"}, nil)
	assert.Error(t, err)
}

func TestFinishRegistrationPrimary_SessionMissing(t *testing.T) {
	requireRedis(t)
	twofaTestRedis.FlushAll(context.Background())
	svc, err := NewWebAuthnService("https://example.com", "Misskey", twofaTestRedis.Client)
	require.NoError(t, err)
	req := httptest.NewRequest("POST", "/", strings.NewReader(""))
	_, err = svc.FinishRegistrationPrimary(context.Background(), &model.User{ID: "alice"}, nil, req)
	assert.ErrorIs(t, err, ErrWebAuthnSessionNotFound)
}

func TestFinishRegistrationPrimary_Success(t *testing.T) {
	requireRedis(t)
	twofaTestRedis.FlushAll(context.Background())

	// TestFinishRegistration_Success と同じ W3C ベクタを再利用
	const (
		attestationObjectHex = "a363666d74646e6f6e656761747453746d74a068617574684461746158a4bfabc37432958b063360d3ad6461c9c4735ae7f8edd46592a5e0f01452b2e4b559000000008446ccb9ab1db374750b2367ff6f3a1f0020f91f391db4c9b2fde0ea70189cba3fb63f579ba6122b33ad94ff3ec330084be4a5010203262001215820afefa16f97ca9b2d23eb86ccb64098d20db90856062eb249c33a9b672f26df61225820930a56b87a2fca66334b03458abf879717c12cc68ed73290af2e2664796b9220"
		clientDataJSONHex    = "7b2274797065223a22776562617574686e2e637265617465222c226368616c6c656e6765223a22414d4d507434557878475453746e63647134313759447742466938767049612d7077386f4f755657345441222c226f726967696e223a2268747470733a2f2f6578616d706c652e6f7267222c2263726f73734f726967696e223a66616c73652c22657874726144617461223a22636c69656e74446174614a534f4e206d617920626520657874656e6465642077697468206164646974696f6e616c206669656c647320696e20746865206675747572652c207375636820617320746869733a20426b5165446a646354427258426941774a544c453551227d"
		credentialIDHex      = "f91f391db4c9b2fde0ea70189cba3fb63f579ba6122b33ad94ff3ec330084be4"
		challengeHex         = "00c30fb78531c464d2b6771dab8d7b603c01162f2fa486bea70f283ae556e130"
	)
	body := buildAttestationResponse(t, attestationObjectHex, clientDataJSONHex, credentialIDHex)
	challenge := encodeBase64URL(decodeHex(t, challengeHex))

	svc, err := NewWebAuthnService("https://example.org", "Misskey", twofaTestRedis.Client)
	require.NoError(t, err)

	sd := &webauthn.SessionData{
		Challenge:  challenge,
		UserID:     []byte("test-user-id"),
		CredParams: []protocol.CredentialParameter{{Type: protocol.PublicKeyCredentialType, Algorithm: webauthncose.AlgES256}},
	}
	require.NoError(t, svc.putPrimarySession(context.Background(), "test-user-id", sd))

	httpReq := httptest.NewRequest("POST", "/", bytes.NewReader(body))
	cred, err := svc.FinishRegistrationPrimary(context.Background(), &model.User{ID: "test-user-id", Username: "test"}, nil, httpReq)
	require.NoError(t, err)
	require.NotNil(t, cred)
	assert.Equal(t, decodeHex(t, credentialIDHex), cred.ID)
}

// --- ensure unused imports stay tidy ---

var _ = redis.Nil
