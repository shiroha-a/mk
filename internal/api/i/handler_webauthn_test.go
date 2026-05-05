package i

import (
	"context"
	"log"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/lib/pq"
	"github.com/pquerna/otp/totp"
	"github.com/shiroha-a/mk/internal/core/twofactor"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// enableTwoFactorWithBackupCodes marks 2FA as enabled and registers a known
// backup code list so test callers can pass `"backup1"` as the 2FA token
// without dealing with TOTP (which depends on real time windows). For tests
// that need to exercise the TOTP code path specifically, use
// enableTwoFactorWithTOTP instead. (#698)
func enableTwoFactorWithBackupCodes(repo *testutil.MockUserRepository, uid string) {
	p := repo.Profiles[uid]
	p.TwoFactorEnabled = true
	p.TwoFactorBackupSecret = pq.StringArray{"backup1", "backup2"}
}

// enableTwoFactorWithTOTP generates a fresh TOTP secret, stores it on the
// profile, and returns it so the caller can compute valid codes via
// totp.GenerateCode. backup codes are not populated, so the verify path is
// guaranteed to go through TOTP.Validate (no backup-code shortcut).
func enableTwoFactorWithTOTP(t *testing.T, repo *testutil.MockUserRepository, uid string) string {
	t.Helper()
	secret, _, err := twofactor.GenerateSecret("Misskey", uid)
	require.NoError(t, err)
	p := repo.Profiles[uid]
	p.TwoFactorEnabled = true
	p.TwoFactorSecret = &secret
	p.TwoFactorBackupSecret = nil
	return secret
}

// この test file は WebAuthn 関連 5 ハンドラの validation / wiring を網羅する。
// FinishRegistration / FinishLogin の本物の attestation 検証は
// internal/core/twofactor の test に任せる (W3C spec vector を使用)。
// ここでは:
//   - パラメータ検証 (空 body / password 欠落)
//   - 認証チェック (wrong password / no profile)
//   - WebAuthn 未注入時の 503
//   - register-key / remove-key / update-key / password-less の正常系 wiring
//
// 共通: TestMain で testcontainers の Redis を立ち上げて WebAuthnService を
// 注入できる状態にしておく。

var iTestRedis *testutil.TestRedis

func TestMain(m *testing.M) {
	ctx := context.Background()
	tr, err := testutil.SetupRedis(ctx)
	if err != nil {
		log.Printf("api/i: redis testcontainer unavailable: %v", err)
		os.Exit(m.Run())
	}
	iTestRedis = tr
	code := m.Run()
	iTestRedis.Teardown(ctx)
	os.Exit(code)
}

// inMemorySecurityKeyRepo は最小限の repository.UserSecurityKeyRepository 実装。
// DB を立てずに handler のロジックを exercise したい用途。
type inMemorySecurityKeyRepo struct {
	keys map[string]*model.UserSecurityKey
}

func newInMemSKRepo() *inMemorySecurityKeyRepo {
	return &inMemorySecurityKeyRepo{keys: map[string]*model.UserSecurityKey{}}
}

func (r *inMemorySecurityKeyRepo) Create(k *model.UserSecurityKey) error {
	r.keys[k.ID] = k
	return nil
}

func (r *inMemorySecurityKeyRepo) FindByID(id string) (*model.UserSecurityKey, error) {
	if k, ok := r.keys[id]; ok {
		return k, nil
	}
	return nil, testutil.ErrNotFound
}

func (r *inMemorySecurityKeyRepo) ListByUser(userID string) ([]*model.UserSecurityKey, error) {
	var out []*model.UserSecurityKey
	for _, k := range r.keys {
		if k.UserID == userID {
			out = append(out, k)
		}
	}
	return out, nil
}

func (r *inMemorySecurityKeyRepo) UpdateName(id, userID, name string) error {
	k, ok := r.keys[id]
	if !ok || k.UserID != userID {
		return testutil.ErrNotFound
	}
	k.Name = name
	return nil
}

func (r *inMemorySecurityKeyRepo) UpdateCounter(id string, counter int64) error {
	k, ok := r.keys[id]
	if !ok {
		return testutil.ErrNotFound
	}
	k.Counter = counter
	return nil
}

func (r *inMemorySecurityKeyRepo) Delete(id, userID string) error {
	k, ok := r.keys[id]
	if !ok || k.UserID != userID {
		return testutil.ErrNotFound
	}
	delete(r.keys, id)
	return nil
}

func (r *inMemorySecurityKeyRepo) CountByUser(userID string) (int64, error) {
	var n int64
	for _, k := range r.keys {
		if k.UserID == userID {
			n++
		}
	}
	return n, nil
}

// 静的に interface を満たすことを確認
var _ repository.UserSecurityKeyRepository = (*inMemorySecurityKeyRepo)(nil)

// newWebAuthnHandler builds an extra Handler with WebAuthn dependencies wired
// up against the testcontainer Redis instance.
func newWebAuthnHandler(t *testing.T) (*Handler, *testutil.MockUserRepository, *inMemorySecurityKeyRepo) {
	t.Helper()
	if iTestRedis == nil {
		t.Skip("redis testcontainer not available")
	}
	h, repo := newExtraHandler(t)
	skRepo := newInMemSKRepo()
	svc, err := twofactor.NewWebAuthnService("https://example.com", "Misskey", iTestRedis.Client)
	require.NoError(t, err)
	h.SetWebAuthn(svc, skRepo)
	return h, repo, skRepo
}

// --- TwoFARegisterKey ---

func TestTwoFARegisterKey_NotConfigured(t *testing.T) {
	h, repo := newExtraHandler(t)
	user := setupUserWithPassword(repo, "u1", "pass")
	rec := postExtra(h.TwoFARegisterKey, `{"password":"pass"}`, user)
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func TestTwoFARegisterKey_NoPassword(t *testing.T) {
	h, _, _ := newWebAuthnHandler(t)
	rec := postExtra(h.TwoFARegisterKey, `{}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestTwoFARegisterKey_WrongPassword(t *testing.T) {
	h, repo, _ := newWebAuthnHandler(t)
	user := setupUserWithPassword(repo, "u1", "correct")
	rec := postExtra(h.TwoFARegisterKey, `{"password":"wrong"}`, user)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestTwoFARegisterKey_NoProfile(t *testing.T) {
	h, repo, _ := newWebAuthnHandler(t)
	repo.Users["u1"] = &model.User{ID: "u1", Username: "u1"}
	rec := postExtra(h.TwoFARegisterKey, `{"password":"pass"}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestTwoFARegisterKey_TwoFactorNotEnabled(t *testing.T) {
	h, repo, _ := newWebAuthnHandler(t)
	user := setupUserWithPassword(repo, "u1", "pass")
	// 2FA 未有効化なら upstream と同じく TWO_FACTOR_NOT_ENABLED で 403
	rec := postExtra(h.TwoFARegisterKey, `{"password":"pass"}`, user)
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "TWO_FACTOR_NOT_ENABLED")
}

func TestTwoFARegisterKey_InvalidToken(t *testing.T) {
	h, repo, _ := newWebAuthnHandler(t)
	user := setupUserWithPassword(repo, "u1", "pass")
	enableTwoFactorWithBackupCodes(repo, "u1")
	rec := postExtra(h.TwoFARegisterKey, `{"password":"pass","token":"wrong"}`, user)
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "INVALID_TOKEN")
}

func TestTwoFARegisterKey_Success(t *testing.T) {
	h, repo, _ := newWebAuthnHandler(t)
	user := setupUserWithPassword(repo, "u1", "pass")
	enableTwoFactorWithBackupCodes(repo, "u1")
	rec := postExtra(h.TwoFARegisterKey, `{"password":"pass","token":"backup1"}`, user)
	assert.Equal(t, http.StatusOK, rec.Code)
	// upstream 互換: PublicKeyCredentialCreationOptions そのものを返す
	// (challenge / rp / pubKeyCredParams が top-level に並ぶ)
	assert.Contains(t, rec.Body.String(), "challenge")
	assert.Contains(t, rec.Body.String(), "pubKeyCredParams")
	// sessionId / creation という旧フィールドが残っていないこと
	assert.NotContains(t, rec.Body.String(), "sessionId")
	// backup code が消費されている
	assert.Equal(t, pq.StringArray{"backup2"}, repo.Profiles["u1"].TwoFactorBackupSecret)
}

func TestTwoFARegisterKey_TOTPSuccess(t *testing.T) {
	// TOTP 直接検証経路 (verify2FAToken の non-backup-code 分岐) のカバレッジを確保。
	h, repo, _ := newWebAuthnHandler(t)
	user := setupUserWithPassword(repo, "u1", "pass")
	secret := enableTwoFactorWithTOTP(t, repo, "u1")
	code, err := totp.GenerateCode(secret, time.Now())
	require.NoError(t, err)
	rec := postExtra(h.TwoFARegisterKey, `{"password":"pass","token":"`+code+`"}`, user)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "challenge")
}

// --- TwoFAKeyDone ---

func TestTwoFAKeyDone_MissingFields(t *testing.T) {
	h, repo, _ := newWebAuthnHandler(t)
	user := setupUserWithPassword(repo, "u1", "pass")
	rec := postExtra(h.TwoFAKeyDone, `{"password":"pass"}`, user)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestTwoFAKeyDone_WrongPassword(t *testing.T) {
	h, repo, _ := newWebAuthnHandler(t)
	user := setupUserWithPassword(repo, "u1", "correct")
	rec := postExtra(h.TwoFAKeyDone, `{"password":"wrong","credential":{}}`, user)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestTwoFAKeyDone_TwoFactorNotEnabled(t *testing.T) {
	h, repo, _ := newWebAuthnHandler(t)
	user := setupUserWithPassword(repo, "u1", "pass")
	rec := postExtra(h.TwoFAKeyDone, `{"password":"pass","credential":{"id":"x"}}`, user)
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "TWO_FACTOR_NOT_ENABLED")
}

func TestTwoFAKeyDone_InvalidToken(t *testing.T) {
	h, repo, _ := newWebAuthnHandler(t)
	user := setupUserWithPassword(repo, "u1", "pass")
	enableTwoFactorWithBackupCodes(repo, "u1")
	rec := postExtra(h.TwoFAKeyDone, `{"password":"pass","token":"wrong","credential":{"id":"x"}}`, user)
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "INVALID_TOKEN")
}

func TestTwoFAKeyDone_FailureBranch(t *testing.T) {
	h, repo, _ := newWebAuthnHandler(t)
	user := setupUserWithPassword(repo, "u1", "pass")
	enableTwoFactorWithBackupCodes(repo, "u1")
	// 不正な attestation → registration session も無いので FinishRegistration が失敗 → 403
	rec := postExtra(h.TwoFAKeyDone, `{"password":"pass","token":"backup1","credential":{"id":"x","rawId":"x","type":"public-key","response":{"attestationObject":"","clientDataJSON":""}}}`, user)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestTwoFAKeyDone_NameTooLong(t *testing.T) {
	// upstream は paramDef で maxLength=30 を強制している。defense-in-depth で
	// API 直叩き経路でも backend 側が拒否することを確認。
	h, repo, _ := newWebAuthnHandler(t)
	user := setupUserWithPassword(repo, "u1", "pass")
	longName := "this-name-is-far-longer-than-the-maximum-allowed-30-chars"
	body := `{"password":"pass","token":"backup1","name":"` + longName + `","credential":{"id":"x"}}`
	rec := postExtra(h.TwoFAKeyDone, body, user)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "INVALID_PARAM")
}

// --- TwoFARemoveKey ---

func TestTwoFARemoveKey_MissingFields(t *testing.T) {
	h, repo, _ := newWebAuthnHandler(t)
	user := setupUserWithPassword(repo, "u1", "pass")
	rec := postExtra(h.TwoFARemoveKey, `{"password":"pass"}`, user)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestTwoFARemoveKey_NotFound(t *testing.T) {
	h, repo, _ := newWebAuthnHandler(t)
	user := setupUserWithPassword(repo, "u1", "pass")
	rec := postExtra(h.TwoFARemoveKey, `{"password":"pass","credentialId":"ghost"}`, user)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestTwoFARemoveKey_Success(t *testing.T) {
	h, repo, skRepo := newWebAuthnHandler(t)
	user := setupUserWithPassword(repo, "u1", "pass")
	require.NoError(t, skRepo.Create(&model.UserSecurityKey{
		ID: "key1", UserID: "u1", Name: "k", PublicKey: "pk",
	}))
	rec := postExtra(h.TwoFARemoveKey, `{"password":"pass","credentialId":"key1"}`, user)
	assert.Equal(t, http.StatusNoContent, rec.Code)
	// 残り 0 件なので securityKeysAvailable=false にされる
	assert.False(t, repo.Profiles["u1"].SecurityKeysAvailable)
	assert.False(t, repo.Profiles["u1"].UsePasswordLessLogin)
}

// --- TwoFAUpdateKey ---

func TestTwoFAUpdateKey_MissingFields(t *testing.T) {
	h, repo, _ := newWebAuthnHandler(t)
	user := setupUserWithPassword(repo, "u1", "pass")
	rec := postExtra(h.TwoFAUpdateKey, `{"password":"pass","credentialId":"k"}`, user)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestTwoFAUpdateKey_NotFound(t *testing.T) {
	h, repo, _ := newWebAuthnHandler(t)
	user := setupUserWithPassword(repo, "u1", "pass")
	rec := postExtra(h.TwoFAUpdateKey, `{"password":"pass","credentialId":"ghost","name":"x"}`, user)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestTwoFAUpdateKey_Success(t *testing.T) {
	h, repo, skRepo := newWebAuthnHandler(t)
	user := setupUserWithPassword(repo, "u1", "pass")
	require.NoError(t, skRepo.Create(&model.UserSecurityKey{
		ID: "key1", UserID: "u1", Name: "old", PublicKey: "pk",
	}))
	rec := postExtra(h.TwoFAUpdateKey, `{"password":"pass","credentialId":"key1","name":"renamed"}`, user)
	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Equal(t, "renamed", skRepo.keys["key1"].Name)
}

// --- TwoFAPasswordLess ---
//
// upstream Misskey TS は paramDef を `{value: boolean}` で password を
// 要求しない (#758)。本 endpoint は session で認証済み前提なので mk-go も
// 合わせている。

// value 未指定 (空 body) は default false 扱いで no-content 成功する。
// upstream の paramDef は required:['value'] だが mk-go の echo Bind は
// JSON body 欠落を default 値で埋めるので、ここで bad request にしない。
func TestTwoFAPasswordLess_EmptyBodyDefaultsFalse(t *testing.T) {
	h, repo, _ := newWebAuthnHandler(t)
	user := setupUserWithPassword(repo, "u1", "pass")
	repo.Profiles["u1"].UsePasswordLessLogin = true
	rec := postExtra(h.TwoFAPasswordLess, `{}`, user)
	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.False(t, repo.Profiles["u1"].UsePasswordLessLogin)
}

// value=true で security key 0 件 → noKey error + profile が
// usePasswordLessLogin=false に巻き戻される (upstream 互換)。
func TestTwoFAPasswordLess_EnableNoKeys(t *testing.T) {
	h, repo, _ := newWebAuthnHandler(t)
	user := setupUserWithPassword(repo, "u1", "pass")
	repo.Profiles["u1"].UsePasswordLessLogin = true
	rec := postExtra(h.TwoFAPasswordLess, `{"value":true}`, user)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.False(t, repo.Profiles["u1"].UsePasswordLessLogin,
		"upstream rolls back the flag before throwing noKey")
}

func TestTwoFAPasswordLess_EnableWithKey(t *testing.T) {
	h, repo, skRepo := newWebAuthnHandler(t)
	user := setupUserWithPassword(repo, "u1", "pass")
	require.NoError(t, skRepo.Create(&model.UserSecurityKey{ID: "k1", UserID: "u1"}))
	rec := postExtra(h.TwoFAPasswordLess, `{"value":true}`, user)
	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.True(t, repo.Profiles["u1"].UsePasswordLessLogin)
}

func TestTwoFAPasswordLess_Disable(t *testing.T) {
	h, repo, _ := newWebAuthnHandler(t)
	user := setupUserWithPassword(repo, "u1", "pass")
	repo.Profiles["u1"].UsePasswordLessLogin = true
	rec := postExtra(h.TwoFAPasswordLess, `{"value":false}`, user)
	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.False(t, repo.Profiles["u1"].UsePasswordLessLogin)
}

// publishMeUpdatedPartial 経由で frontend の $i に partial merge される
// payload が main stream に送られていることを assert する (#758)。
func TestTwoFAPasswordLess_PublishesPartialMeUpdated(t *testing.T) {
	h, repo, skRepo := newWebAuthnHandler(t)
	user := setupUserWithPassword(repo, "u1", "pass")
	require.NoError(t, skRepo.Create(&model.UserSecurityKey{ID: "k1", UserID: "u1"}))
	pub := &stubIMainStreamPublisher{}
	h.SetMainStreamPublisher(pub)

	rec := postExtra(h.TwoFAPasswordLess, `{"value":true}`, user)
	require.Equal(t, http.StatusNoContent, rec.Code)

	require.Len(t, pub.calls, 1)
	assert.Equal(t, "u1", pub.calls[0].userID)
	assert.Equal(t, "meUpdated", pub.calls[0].eventType)
	body, ok := pub.calls[0].body.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, true, body["usePasswordLessLogin"])
}

// publishMeUpdatedPartial: mainStreamPublisher が wire されていない場合は
// no-op (production の fallback path 確保 + test の panic 防止)。
func TestPublishMeUpdatedPartial_NoPublisherIsNoop(t *testing.T) {
	h, _, _ := newWebAuthnHandler(t)
	// SetMainStreamPublisher を呼ばずそのまま invoke しても panic しない。
	h.publishMeUpdatedPartial("u1", map[string]any{"x": 1})
}

// publishMeUpdatedPartial: 空 fields は publish しない (no-op)。
func TestPublishMeUpdatedPartial_EmptyFieldsIsNoop(t *testing.T) {
	h, _, _ := newWebAuthnHandler(t)
	pub := &stubIMainStreamPublisher{}
	h.SetMainStreamPublisher(pub)
	h.publishMeUpdatedPartial("u1", nil)
	h.publishMeUpdatedPartial("u1", map[string]any{})
	assert.Empty(t, pub.calls, "no PublishMainEvent for empty fields")
}

// publishMeUpdated (full UserDetailed): publisher が wire されていれば
// userService.ShowByID 経由で User+Profile を packed publish する。
func TestPublishMeUpdated_FullPublishWhenWired(t *testing.T) {
	h, repo, _ := newWebAuthnHandler(t)
	setupUserWithPassword(repo, "u1", "pass")
	pub := &stubIMainStreamPublisher{}
	h.SetMainStreamPublisher(pub)

	h.publishMeUpdated("u1")
	require.Len(t, pub.calls, 1)
	assert.Equal(t, "u1", pub.calls[0].userID)
	assert.Equal(t, "meUpdated", pub.calls[0].eventType)
	assert.NotNil(t, pub.calls[0].body)
}

// publishMeUpdated: 存在しない userID は ShowByID で err → publish せず
// log だけ残す。
func TestPublishMeUpdated_UnknownUserIsNoop(t *testing.T) {
	h, _, _ := newWebAuthnHandler(t)
	pub := &stubIMainStreamPublisher{}
	h.SetMainStreamPublisher(pub)

	h.publishMeUpdated("nonexistent")
	assert.Empty(t, pub.calls, "no publish for unknown user")
}

// publishMeUpdated: publisher 未配線は no-op (production の fallback path)。
func TestPublishMeUpdated_NoPublisherIsNoop(t *testing.T) {
	h, _, _ := newWebAuthnHandler(t)
	// SetMainStreamPublisher を呼ばずに invoke しても panic しない。
	h.publishMeUpdated("u1")
}
