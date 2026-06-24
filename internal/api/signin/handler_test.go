package signin_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/api/signin"
	"github.com/shiroha-a/mk/internal/core/captcha"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
)

type mockIPLogger struct {
	fn func(userID, ip string) error
}

func (m *mockIPLogger) Upsert(userID, ip string) error {
	if m.fn != nil {
		return m.fn(userID, ip)
	}
	return nil
}

func newTestHandler(t *testing.T) (*signin.Handler, *testutil.MockUserRepository) {
	t.Helper()
	userRepo := testutil.NewMockUserRepository()
	return signin.NewHandler(userRepo), userRepo
}

func doPost(h func(echo.Context) error, body string) *httptest.ResponseRecorder {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/signin", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	_ = h(c)
	return rec
}

func createTestUser(repo *testutil.MockUserRepository, username, password string) *model.User {
	hash, _ := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	token := "testtoken1234567"
	user := &model.User{
		ID:            "u1",
		Username:      username,
		UsernameLower: strings.ToLower(username),
		Token:         &token,
	}
	repo.Users["u1"] = user
	hashStr := string(hash)
	repo.Profiles["u1"] = &model.UserProfile{
		UserID:   "u1",
		Password: &hashStr,
	}
	return user
}

func TestSignin_Step1_NoPassword(t *testing.T) {
	h, repo := newTestHandler(t)
	createTestUser(repo, "admin", "pass123")

	rec := doPost(h.Signin, `{"username":"admin"}`)
	assert.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, false, resp["finished"])
	assert.Equal(t, "password", resp["next"])
}

func TestSignin_Step2_Success(t *testing.T) {
	h, repo := newTestHandler(t)
	createTestUser(repo, "admin", "pass123")

	rec := doPost(h.Signin, `{"username":"admin","password":"pass123"}`)
	assert.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, true, resp["finished"])
	assert.Equal(t, "u1", resp["id"])
	assert.Equal(t, "testtoken1234567", resp["i"])
}

// chanLoginNotifier captures OnLogin asynchronously (#1559)。
type chanLoginNotifier struct{ ch chan string }

func (n *chanLoginNotifier) OnLogin(userID string) { n.ch <- userID }

// #1559 [LOW] signin 成功時に login 通知が発火する。
func TestSignin_FiresLoginNotifier(t *testing.T) {
	h, repo := newTestHandler(t)
	createTestUser(repo, "admin", "pass123")
	notifier := &chanLoginNotifier{ch: make(chan string, 1)}
	h.SetLoginNotifier(notifier)

	rec := doPost(h.Signin, `{"username":"admin","password":"pass123"}`)
	require.Equal(t, http.StatusOK, rec.Code)

	select {
	case uid := <-notifier.ch:
		assert.Equal(t, "u1", uid)
	case <-time.After(2 * time.Second):
		t.Fatal("login notifier was not fired")
	}
}

// #1804: RecordSuccessfulSignin は signin / signup 共有の成功時 side-effect
// entrypoint。signin 履歴 (success:true) + login 通知を発火する。
func TestRecordSuccessfulSignin_FiresHistoryAndNotifier(t *testing.T) {
	h, _ := newTestHandler(t)
	signinRepo := testutil.NewMockSigninRepository()
	idGen, _ := id.NewGenerator("aidx")
	h.SetSigninRepo(signinRepo, idGen)
	notifier := &chanLoginNotifier{ch: make(chan string, 1)}
	h.SetLoginNotifier(notifier)

	h.RecordSuccessfulSignin("u1", "1.2.3.4", http.Header{})

	select {
	case uid := <-notifier.ch:
		assert.Equal(t, "u1", uid)
	case <-time.After(2 * time.Second):
		t.Fatal("login notifier was not fired")
	}
	require.Eventually(t, func() bool { return signinRepo.Len() == 1 }, 2*time.Second, 10*time.Millisecond)
	assert.True(t, signinRepo.Signins[0].Success, "success:true で記録する")
}

func TestSignin_WrongPassword(t *testing.T) {
	h, repo := newTestHandler(t)
	createTestUser(repo, "admin", "pass123")

	rec := doPost(h.Signin, `{"username":"admin","password":"wrongpass"}`)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestSignin_UserNotFound(t *testing.T) {
	h, _ := newTestHandler(t)
	rec := doPost(h.Signin, `{"username":"ghost","password":"x"}`)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestSignin_EmptyUsername(t *testing.T) {
	h, _ := newTestHandler(t)
	rec := doPost(h.Signin, `{}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestSignin_SuspendedUser(t *testing.T) {
	h, repo := newTestHandler(t)
	user := createTestUser(repo, "banned", "pass")
	user.IsSuspended = true

	rec := doPost(h.Signin, `{"username":"banned","password":"pass"}`)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestSignin_NoProfile(t *testing.T) {
	h, repo := newTestHandler(t)
	repo.Users["u1"] = &model.User{
		ID: "u1", Username: "noprof", UsernameLower: "noprof",
	}
	// プロフィールなし

	rec := doPost(h.Signin, `{"username":"noprof","password":"x"}`)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestSignin_NilToken(t *testing.T) {
	h, repo := newTestHandler(t)
	hash, _ := bcrypt.GenerateFromPassword([]byte("pass"), bcrypt.MinCost)
	hashStr := string(hash)
	repo.Users["u1"] = &model.User{
		ID: "u1", Username: "notoken", UsernameLower: "notoken",
		Token: nil, // トークンなし
	}
	repo.Profiles["u1"] = &model.UserProfile{UserID: "u1", Password: &hashStr}

	rec := doPost(h.Signin, `{"username":"notoken","password":"pass"}`)
	assert.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "", resp["i"]) // token is empty string
}

// --- SigninFlow ---

func TestSigninFlow_Step1(t *testing.T) {
	h, repo := newTestHandler(t)
	createTestUser(repo, "admin", "pass123")

	rec := doPost(h.SigninFlow, `{"username":"admin"}`)
	assert.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, false, resp["finished"])
	// 2FA 無効ユーザは TS upstream 互換で常に 'captcha' (#705)。
	assert.Equal(t, "captcha", resp["next"])
}

func TestSigninFlow_Step2_Success(t *testing.T) {
	h, repo := newTestHandler(t)
	createTestUser(repo, "admin", "pass123")

	rec := doPost(h.SigninFlow, `{"username":"admin","password":"pass123"}`)
	assert.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, true, resp["finished"])
	assert.Equal(t, "u1", resp["id"])
	assert.NotEmpty(t, resp["i"])
}

func TestSigninFlow_WrongPassword(t *testing.T) {
	h, repo := newTestHandler(t)
	createTestUser(repo, "admin", "pass123")

	rec := doPost(h.SigninFlow, `{"username":"admin","password":"wrong"}`)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestSigninFlow_UserNotFound(t *testing.T) {
	h, _ := newTestHandler(t)
	rec := doPost(h.SigninFlow, `{"username":"ghost"}`)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestSigninFlow_EmptyUsername(t *testing.T) {
	h, _ := newTestHandler(t)
	rec := doPost(h.SigninFlow, `{}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestSigninFlow_SuspendedUser(t *testing.T) {
	h, repo := newTestHandler(t)
	user := createTestUser(repo, "banned2", "pass")
	user.IsSuspended = true

	rec := doPost(h.SigninFlow, `{"username":"banned2"}`)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestSigninFlow_NoProfile(t *testing.T) {
	h, repo := newTestHandler(t)
	repo.Users["u2"] = &model.User{
		ID: "u2", Username: "noprof2", UsernameLower: "noprof2",
	}

	rec := doPost(h.SigninFlow, `{"username":"noprof2","password":"x"}`)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestSigninFlow_NilToken(t *testing.T) {
	h, repo := newTestHandler(t)
	hash, _ := bcrypt.GenerateFromPassword([]byte("pass"), bcrypt.MinCost)
	hashStr := string(hash)
	repo.Users["u3"] = &model.User{
		ID: "u3", Username: "notoken2", UsernameLower: "notoken2",
		Token: nil,
	}
	repo.Profiles["u3"] = &model.UserProfile{UserID: "u3", Password: &hashStr}

	rec := doPost(h.SigninFlow, `{"username":"notoken2","password":"pass"}`)
	assert.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "", resp["i"])
}

// --- CAPTCHA integration ---

func TestSigninFlow_CaptchaPassesWithCorrectToken(t *testing.T) {
	h, repo := newTestHandler(t)
	createTestUser(repo, "capuser", "pass")

	captchaSvc := captcha.NewService(&model.Meta{EnableTestcaptcha: true})
	h.SetCaptcha(captchaSvc)

	rec := doPost(h.SigninFlow, `{"username":"capuser","password":"pass","testcaptcha-response":"testcaptcha-passed"}`)
	assert.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, true, resp["finished"])
}

func TestSigninFlow_CaptchaBlocksMissingToken(t *testing.T) {
	h, repo := newTestHandler(t)
	createTestUser(repo, "capuser2", "pass")

	captchaSvc := captcha.NewService(&model.Meta{EnableTestcaptcha: true})
	h.SetCaptcha(captchaSvc)

	// testcaptcha-response を送らないので CAPTCHA 検証失敗。upstream は
	// `throw new FastifyReplyError(400, err)` を投げる (SigninApiService.ts)
	// ため、mk-go も #810 で同 Fastify shape に揃える。
	rec := doPost(h.SigninFlow, `{"username":"capuser2","password":"pass"}`)
	testutil.AssertFastifyError(t, rec, http.StatusBadRequest, "CAPTCHA_FAILED")
}

func TestSigninFlow_CaptchaSkippedFor2FAUsers(t *testing.T) {
	h, repo := newTestHandler(t)
	hash, _ := bcrypt.GenerateFromPassword([]byte("pass"), bcrypt.MinCost)
	hashStr := string(hash)
	token := "tok"
	repo.Users["u2fa"] = &model.User{
		ID: "u2fa", Username: "tfa", UsernameLower: "tfa", Token: &token,
	}
	repo.Profiles["u2fa"] = &model.UserProfile{
		UserID:           "u2fa",
		Password:         &hashStr,
		TwoFactorEnabled: true,
	}

	captchaSvc := captcha.NewService(&model.Meta{EnableTestcaptcha: true})
	h.SetCaptcha(captchaSvc)

	// 2FA 有効ユーザーは CAPTCHA をスキップして "totp" ステップへ進む。
	rec := doPost(h.SigninFlow, `{"username":"tfa","password":"pass"}`)
	assert.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "totp", resp["next"])
}

func TestSignin_RecordsSigninHistory(t *testing.T) {
	h, repo := newTestHandler(t)
	createTestUser(repo, "testuser", "password123")

	signinRepo := testutil.NewMockSigninRepository()
	idGen, _ := id.NewGenerator("aidx")
	h.SetSigninRepo(signinRepo, idGen)

	rec := doPost(h.Signin, `{"username":"testuser","password":"password123"}`)
	assert.Equal(t, http.StatusOK, rec.Code)

	// goroutineでsigninレコードが作成されるのを待つ
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, 1, signinRepo.Len())
}

// #1776: 認証失敗 (パスワード不一致) でも signin 履歴を success:false で記録する。
// upstream SigninApiService.fail() は全認証失敗で signins に success:false を insert。
func TestSignin_RecordsFailedSignin(t *testing.T) {
	h, repo := newTestHandler(t)
	createTestUser(repo, "testuser", "password123")

	signinRepo := testutil.NewMockSigninRepository()
	idGen, _ := id.NewGenerator("aidx")
	h.SetSigninRepo(signinRepo, idGen)

	rec := doPost(h.Signin, `{"username":"testuser","password":"WRONG"}`)
	assert.Equal(t, http.StatusForbidden, rec.Code)

	time.Sleep(100 * time.Millisecond)
	require.Equal(t, 1, signinRepo.Len())
	assert.False(t, signinRepo.Signins[0].Success, "失敗ログインは success:false で記録する")
}

// #1776: SigninFlow の 2FA token 失敗でも success:false を記録する。
func TestSigninFlow_RecordsFailed2FA(t *testing.T) {
	h, repo := newTestHandler(t)
	user := createTestUser(repo, "tfauser", "password123")
	repo.Profiles[user.ID].TwoFactorEnabled = true
	secret := "JBSWY3DPEHPK3PXP"
	repo.Profiles[user.ID].TwoFactorSecret = &secret

	signinRepo := testutil.NewMockSigninRepository()
	idGen, _ := id.NewGenerator("aidx")
	h.SetSigninRepo(signinRepo, idGen)

	rec := doPost(h.SigninFlow, `{"username":"tfauser","password":"password123","token":"000000"}`)
	assert.Equal(t, http.StatusForbidden, rec.Code)

	time.Sleep(100 * time.Millisecond)
	require.Equal(t, 1, signinRepo.Len())
	assert.False(t, signinRepo.Signins[0].Success)
}

// #1776: 凍結アカウントの signin 試行は履歴に記録しない (upstream は suspended で
// fail() を呼ばず先に弾く)。user-not-found も同様。
func TestSignin_SuspendedAndNotFoundDoNotRecord(t *testing.T) {
	h, repo := newTestHandler(t)
	user := createTestUser(repo, "suspendeduser", "password123")
	user.IsSuspended = true

	signinRepo := testutil.NewMockSigninRepository()
	idGen, _ := id.NewGenerator("aidx")
	h.SetSigninRepo(signinRepo, idGen)

	rec := doPost(h.Signin, `{"username":"suspendeduser","password":"password123"}`)
	assert.Equal(t, http.StatusForbidden, rec.Code)
	rec = doPost(h.Signin, `{"username":"ghost","password":"x"}`)
	assert.Equal(t, http.StatusNotFound, rec.Code)

	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, 0, signinRepo.Len(), "suspended / user-not-found は失敗履歴を残さない")
}

// stubMainStreamPublisher captures PublishMainEvent calls.
type stubMainStreamPublisher struct {
	mu    sync.Mutex
	calls []mainEventCall
}

type mainEventCall struct {
	userID    string
	eventType string
	body      any
}

func (s *stubMainStreamPublisher) PublishMainEvent(userID, eventType string, body any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, mainEventCall{userID, eventType, body})
}

func TestSignin_PublishesSigninEvent(t *testing.T) {
	h, repo := newTestHandler(t)
	user := createTestUser(repo, "testuser", "password123")

	signinRepo := testutil.NewMockSigninRepository()
	idGen, _ := id.NewGenerator("aidx")
	h.SetSigninRepo(signinRepo, idGen)
	pub := &stubMainStreamPublisher{}
	h.SetMainStreamPublisher(pub)

	rec := doPost(h.Signin, `{"username":"testuser","password":"password123"}`)
	require.Equal(t, http.StatusOK, rec.Code)

	// 非同期goroutineのrecordSignin完了を待つ
	time.Sleep(100 * time.Millisecond)

	pub.mu.Lock()
	defer pub.mu.Unlock()
	require.Len(t, pub.calls, 1)
	assert.Equal(t, user.ID, pub.calls[0].userID)
	assert.Equal(t, "signin", pub.calls[0].eventType)
	// body は PackSignin の map。最低限 id/success が含まれる。
	body, ok := pub.calls[0].body.(map[string]any)
	require.True(t, ok)
	assert.NotEmpty(t, body["id"])
	assert.Equal(t, true, body["success"])
}

func TestSignin_IPLogging(t *testing.T) {
	h, repo := newTestHandler(t)
	createTestUser(repo, "testuser", "password123")

	var logged atomic.Bool
	h.SetIPLogger(&mockIPLogger{fn: func(userID, ip string) error {
		logged.Store(true)
		return nil
	}}, true)

	rec := doPost(h.Signin, `{"username":"testuser","password":"password123"}`)
	assert.Equal(t, http.StatusOK, rec.Code)
	time.Sleep(50 * time.Millisecond)
	assert.True(t, logged.Load())
}

func TestSignin_SanitizesHeaders(t *testing.T) {
	h, repo := newTestHandler(t)
	createTestUser(repo, "testuser", "password123")

	signinRepo := testutil.NewMockSigninRepository()
	idGen, _ := id.NewGenerator("aidx")
	h.SetSigninRepo(signinRepo, idGen)

	// Authorization, Cookieヘッダー付きのリクエスト
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/signin", strings.NewReader(`{"username":"testuser","password":"password123"}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	req.Header.Set("Authorization", "Bearer secret-token")
	req.Header.Set("Cookie", "session=abc123")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	_ = h.Signin(c)
	assert.Equal(t, http.StatusOK, rec.Code)

	time.Sleep(100 * time.Millisecond)
	require.Equal(t, 1, signinRepo.Len())
	stored := string(signinRepo.Signins[0].Headers)
	assert.NotContains(t, stored, "secret-token")
	assert.NotContains(t, stored, "abc123")
}

func TestSigninFlow_RecordsSigninHistory(t *testing.T) {
	h, repo := newTestHandler(t)
	createTestUser(repo, "testuser", "password123")

	signinRepo := testutil.NewMockSigninRepository()
	idGen, _ := id.NewGenerator("aidx")
	h.SetSigninRepo(signinRepo, idGen)

	rec := doPost(h.SigninFlow, `{"username":"testuser","password":"password123"}`)
	assert.Equal(t, http.StatusOK, rec.Code)

	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, 1, signinRepo.Len())
}

// --- Step 1 captcha branching ---

func TestSigninFlow_Step1_CaptchaNext_WhenCaptchaEnabled(t *testing.T) {
	h, repo := newTestHandler(t)
	createTestUser(repo, "capstep", "pass")

	captchaSvc := captcha.NewService(&model.Meta{EnableTestcaptcha: true})
	h.SetCaptcha(captchaSvc)

	// 2FA無効 + captcha有効 → Step 1で "captcha" を返す
	rec := doPost(h.SigninFlow, `{"username":"capstep"}`)
	assert.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, false, resp["finished"])
	assert.Equal(t, "captcha", resp["next"])
}

func TestSigninFlow_Step1_PasswordNext_When2FAEnabled(t *testing.T) {
	h, repo := newTestHandler(t)
	hash, _ := bcrypt.GenerateFromPassword([]byte("pass"), bcrypt.MinCost)
	hashStr := string(hash)
	token := "tok"
	repo.Users["u2fa2"] = &model.User{
		ID: "u2fa2", Username: "tfa2", UsernameLower: "tfa2", Token: &token,
	}
	repo.Profiles["u2fa2"] = &model.UserProfile{
		UserID:           "u2fa2",
		Password:         &hashStr,
		TwoFactorEnabled: true,
	}

	captchaSvc := captcha.NewService(&model.Meta{EnableTestcaptcha: true})
	h.SetCaptcha(captchaSvc)

	// 2FA有効 + captcha有効 → Step 1で "password" を返す
	rec := doPost(h.SigninFlow, `{"username":"tfa2"}`)
	assert.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, false, resp["finished"])
	assert.Equal(t, "password", resp["next"])
}

func TestSigninFlow_Step1_CaptchaNext_WhenNoCaptchaConfigured(t *testing.T) {
	h, repo := newTestHandler(t)
	createTestUser(repo, "nocap", "pass")

	// captchaサービスなしでも、TS upstream は 2FA 無効ユーザに対して常に
	// 'captcha' を返す。フロント (MkSignin.vue) は instance meta で widget の
	// 表示要否を判定するため、サーバ側の captcha 設定とは独立している (#705)。
	rec := doPost(h.SigninFlow, `{"username":"nocap"}`)
	assert.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, false, resp["finished"])
	assert.Equal(t, "captcha", resp["next"])
}

// #2106 H2 (CRITICAL): レガシー /api/signin は 2FA 有効ユーザーに password だけで
// token を発行してはならない。challenge を返し /signin-flow へ誘導する。
func TestSignin_LegacyTwoFactorNotBypassed(t *testing.T) {
	h, repo := newTestHandler(t)
	createTestUser(repo, "admin", "pass123")
	repo.Profiles["u1"].TwoFactorEnabled = true

	rec := doPost(h.Signin, `{"username":"admin","password":"pass123"}`)
	require.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, false, resp["finished"])
	assert.Equal(t, "totp", resp["next"])
	_, hasToken := resp["i"]
	assert.False(t, hasToken, "2FA user must NOT receive a session token via legacy /signin")
}
