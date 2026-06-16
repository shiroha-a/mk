package signin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// makeStubWebauthnCred returns a minimal webauthn.Credential just to feed
// counter update logic; signature/PublicKey are not consulted here.
func makeStubWebauthnCred() *webauthn.Credential {
	return &webauthn.Credential{
		ID:        []byte{0xaa, 0xbb, 0xcc},
		PublicKey: []byte{0x01, 0x02},
		Authenticator: webauthn.Authenticator{
			SignCount: 17,
		},
	}
}

// encodeCredID は base64url で WebAuthn credential ID を符号化する。
// padding は付かない (RawURLEncoding 仕様)。signin handler と handler_2fa の
// 鍵格納フォーマットを揃える役割。
func TestEncodeCredID(t *testing.T) {
	assert.Equal(t, "AAEC", encodeCredID([]byte{0, 1, 2}))
	assert.Equal(t, "", encodeCredID(nil))
	// padding が付かないこと (URLEncoding ではなく RawURLEncoding)
	assert.NotContains(t, encodeCredID([]byte{0xff, 0xff}), "=")
}

// okBody は signin 成功時のレスポンス map (TS 互換 `{finished, id, i}`) を
// 構築する。Token nil のときは i が空文字列になる。
func TestOkBody_TokenSet(t *testing.T) {
	tok := "tok-abc"
	user := &model.User{ID: "u1", Token: &tok}
	body := (&Handler{}).okBody(user)
	assert.Equal(t, true, body["finished"])
	assert.Equal(t, "u1", body["id"])
	assert.Equal(t, "tok-abc", body["i"])
}

func TestOkBody_TokenNil(t *testing.T) {
	user := &model.User{ID: "u1", Token: nil}
	body := (&Handler{}).okBody(user)
	assert.Equal(t, "", body["i"])
}

// newPasskeyContext は 32 文字の hex を返す (16 byte の random)。複数回呼んで
// 衝突しないこと (= randomness が活きていること) を確認する。
func TestNewPasskeyContext_Random(t *testing.T) {
	a, err := newPasskeyContext()
	assert.NoError(t, err)
	assert.Len(t, a, 32)
	b, _ := newPasskeyContext()
	assert.NotEqual(t, a, b)
}

func TestNewPasskeyContext_RandError(t *testing.T) {
	old := readRandom
	defer func() { readRandom = old }()
	readRandom = func(_ []byte) (int, error) { return 0, assert.AnError }
	_, err := newPasskeyContext()
	assert.Error(t, err)
}

// errSecurityKeyRepo は ListByUser で err を返す stub。resolvePasskeyUser の
// kerr 伝搬経路を exercise するために使う。
type errSecurityKeyRepo struct{}

func (errSecurityKeyRepo) Create(*model.UserSecurityKey) error { return nil }
func (errSecurityKeyRepo) FindByID(string) (*model.UserSecurityKey, error) {
	return nil, testutil.ErrNotFound
}
func (errSecurityKeyRepo) ListByUser(string) ([]*model.UserSecurityKey, error) {
	return nil, assert.AnError
}
func (errSecurityKeyRepo) UpdateName(string, string, string) error { return nil }
func (errSecurityKeyRepo) UpdateCounter(string, int64) error       { return nil }
func (errSecurityKeyRepo) Delete(string, string) error             { return nil }
func (errSecurityKeyRepo) CountByUser(string) (int64, error)       { return 0, nil }

// resolvePasskeyUser は user 不在 / securityKeyRepo nil / ListByUser err / 正常
// の 4 分岐を持つ。webauthn 経由では signature verify を通せないので named method
// として直接 unit test する (#705)。
func TestResolvePasskeyUser_UserNotFound(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	h := NewHandler(repo)
	_, _, err := h.resolvePasskeyUser(nil, []byte("ghost"))
	assert.Error(t, err)
}

func TestResolvePasskeyUser_NoSecurityKeyRepo(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	repo.Users["u1"] = &model.User{ID: "u1"}
	h := NewHandler(repo)
	u, keys, err := h.resolvePasskeyUser(nil, []byte("u1"))
	assert.NoError(t, err)
	assert.NotNil(t, u)
	assert.Nil(t, keys)
}

func TestResolvePasskeyUser_ListByUserError(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	repo.Users["u1"] = &model.User{ID: "u1"}
	h := NewHandler(repo)
	h.SetWebAuthn(nil, errSecurityKeyRepo{})
	_, _, err := h.resolvePasskeyUser(nil, []byte("u1"))
	assert.Error(t, err)
}

func TestResolvePasskeyUser_Success(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	repo.Users["u1"] = &model.User{ID: "u1"}
	h := NewHandler(repo)
	h.SetWebAuthn(nil, &inMemorySKInternal{
		keys: map[string][]*model.UserSecurityKey{
			"u1": {{ID: "AAEC"}},
		},
	})
	u, keys, err := h.resolvePasskeyUser(nil, []byte("u1"))
	assert.NoError(t, err)
	assert.NotNil(t, u)
	assert.Len(t, keys, 1)
}

// inMemorySKInternal: helpers_test.go (内部 package) からも使える簡易 stub。
// handler_2fa_test.go の inMemorySK は signin_test package なので別途定義する。
type inMemorySKInternal struct {
	keys map[string][]*model.UserSecurityKey
}

func (r *inMemorySKInternal) Create(*model.UserSecurityKey) error { return nil }
func (r *inMemorySKInternal) FindByID(string) (*model.UserSecurityKey, error) {
	return nil, testutil.ErrNotFound
}
func (r *inMemorySKInternal) ListByUser(userID string) ([]*model.UserSecurityKey, error) {
	return r.keys[userID], nil
}
func (r *inMemorySKInternal) UpdateName(string, string, string) error { return nil }
func (r *inMemorySKInternal) UpdateCounter(string, int64) error       { return nil }
func (r *inMemorySKInternal) Delete(string, string) error             { return nil }
func (r *inMemorySKInternal) CountByUser(string) (int64, error)       { return 0, nil }

// helper: build an echo.Context with empty body.
func newCtx() echo.Context {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/signin-with-passkey", strings.NewReader(""))
	rec := httptest.NewRecorder()
	return e.NewContext(req, rec)
}

// finishPasskeySignin の各分岐 (success / suspended / no profile / passwordless
// 無効) を直接テストする。webauthn signature verify success path はユニットで
// 再現できないのでこの helper 経由で網羅する (#705)。
func TestFinishPasskeySignin_NilUser(t *testing.T) {
	h := &Handler{}
	c := newCtx()
	rec := c.Response().Writer.(*httptest.ResponseRecorder)
	require.NoError(t, h.finishPasskeySignin(c, nil, nil))
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestFinishPasskeySignin_Suspended(t *testing.T) {
	h := &Handler{}
	c := newCtx()
	rec := c.Response().Writer.(*httptest.ResponseRecorder)
	user := &model.User{ID: "u1", IsSuspended: true}
	require.NoError(t, h.finishPasskeySignin(c, user, nil))
	assert.Equal(t, http.StatusForbidden, rec.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	errMap := resp["error"].(map[string]any)
	assert.Equal(t, "e03a5f46-d309-4865-9b69-56282d94e1eb", errMap["id"])
}

func TestFinishPasskeySignin_NoProfile(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	repo.Users["u1"] = &model.User{ID: "u1"}
	// プロフィールを登録しない → FindProfileByUserID が err
	h := NewHandler(repo)
	c := newCtx()
	rec := c.Response().Writer.(*httptest.ResponseRecorder)
	user := &model.User{ID: "u1"}
	require.NoError(t, h.finishPasskeySignin(c, user, nil))
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestFinishPasskeySignin_PasswordlessNotEnabled(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	repo.Users["u1"] = &model.User{ID: "u1"}
	repo.Profiles["u1"] = &model.UserProfile{UserID: "u1", UsePasswordLessLogin: false}
	h := NewHandler(repo)
	c := newCtx()
	rec := c.Response().Writer.(*httptest.ResponseRecorder)
	user := &model.User{ID: "u1"}
	require.NoError(t, h.finishPasskeySignin(c, user, nil))
	assert.Equal(t, http.StatusForbidden, rec.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	errMap := resp["error"].(map[string]any)
	assert.Equal(t, "2d84773e-f7b7-4d0b-8f72-bb69b584c912", errMap["id"])
}

func TestFinishPasskeySignin_Success(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	tok := "Tk-1"
	repo.Users["u1"] = &model.User{ID: "u1", Token: &tok}
	repo.Profiles["u1"] = &model.UserProfile{UserID: "u1", UsePasswordLessLogin: true}
	h := NewHandler(repo)
	c := newCtx()
	rec := c.Response().Writer.(*httptest.ResponseRecorder)
	user := &model.User{ID: "u1", Token: &tok}
	require.NoError(t, h.finishPasskeySignin(c, user, nil))
	assert.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	signinResp, ok := resp["signinResponse"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, true, signinResp["finished"])
	assert.Equal(t, "u1", signinResp["id"])
	assert.Equal(t, "Tk-1", signinResp["i"])
}

// recordSignin は signinRepo.Create が err を返しても panic しないこと
// (slog warn して return)。mainStreamPublisher が nil なのでそのまま終わる。
type errSigninRepo struct{}

func (errSigninRepo) Create(*model.Signin) error { return assert.AnError }
func (errSigninRepo) ListByUserID(string, int, string, string) ([]*model.Signin, error) {
	return nil, nil
}

type fixedIDGen struct{}

func (fixedIDGen) Generate(_ time.Time) string           { return "test-id" }
func (fixedIDGen) ParseTime(_ string) (time.Time, error) { return time.Time{}, nil }

func TestRecordSignin_RepoError(t *testing.T) {
	h := &Handler{signinRepo: errSigninRepo{}, idGen: fixedIDGen{}}
	hdrs := http.Header{}
	hdrs.Set("X-Test", "1")
	// Create err でも return すること
	h.recordSignin("u1", "1.2.3.4", hdrs, true)
	// 失敗履歴 (success:false) 経路でも Create err を握りつぶす。
	h.recordSignin("u1", "1.2.3.4", hdrs, false)
}

// counter update / ipLogger / signinRepo の hook が全て繋がった success path。
type recCounterRepo struct{ called bool }

func (r *recCounterRepo) Create(*model.UserSecurityKey) error { return nil }
func (r *recCounterRepo) FindByID(string) (*model.UserSecurityKey, error) {
	return nil, testutil.ErrNotFound
}
func (r *recCounterRepo) ListByUser(string) ([]*model.UserSecurityKey, error) { return nil, nil }
func (r *recCounterRepo) UpdateName(string, string, string) error             { return nil }
func (r *recCounterRepo) UpdateCounter(string, int64) error                   { r.called = true; return nil }
func (r *recCounterRepo) Delete(string, string) error                         { return nil }
func (r *recCounterRepo) CountByUser(string) (int64, error)                   { return 0, nil }

type stubIPLogger struct{ logged chan struct{} }

func (s *stubIPLogger) Upsert(_, _ string) error {
	select {
	case s.logged <- struct{}{}:
	default:
	}
	return nil
}

func TestFinishPasskeySignin_HooksFire(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	tok := "Tk-2"
	repo.Users["u1"] = &model.User{ID: "u1", Token: &tok}
	repo.Profiles["u1"] = &model.UserProfile{UserID: "u1", UsePasswordLessLogin: true}
	h := NewHandler(repo)
	skRepo := &recCounterRepo{}
	h.SetWebAuthn(nil, skRepo) // WebAuthn nil でも skRepo だけ注入できる

	logged := make(chan struct{}, 1)
	h.SetIPLogger(&stubIPLogger{logged: logged}, true)
	signinRepo := testutil.NewMockSigninRepository()
	h.SetSigninRepo(signinRepo, fixedIDGen{})

	c := newCtx()
	rec := c.Response().Writer.(*httptest.ResponseRecorder)
	user := &model.User{ID: "u1", Token: &tok}

	// ダミー credential — go-webauthn の Credential 型を使う。
	cred := makeStubWebauthnCred()
	require.NoError(t, h.finishPasskeySignin(c, user, cred))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.True(t, skRepo.called, "counter update must be called when securityKeyRepo set")

	// 非同期 hook の完了を緩く待つ
	select {
	case <-logged:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("ipLogger was not invoked")
	}
}
