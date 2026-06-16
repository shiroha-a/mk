package signup_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	apisignup "github.com/shiroha-a/mk/internal/api/signup"
	"github.com/shiroha-a/mk/internal/core/captcha"
	coresignup "github.com/shiroha-a/mk/internal/core/signup"
	"github.com/shiroha-a/mk/internal/misc/id"
	miscsmtp "github.com/shiroha-a/mk/internal/misc/smtp"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockTicketStore is a test double for signup.TicketStore.
type mockTicketStore struct {
	tickets  map[string]*model.RegistrationTicket // keyed by code
	markUsed map[string]string                    // ticketID → userID
	markErr  error
}

func newMockTicketStore() *mockTicketStore {
	return &mockTicketStore{
		tickets:  make(map[string]*model.RegistrationTicket),
		markUsed: make(map[string]string),
	}
}

func (m *mockTicketStore) FindByCode(code string) (*model.RegistrationTicket, error) {
	t, ok := m.tickets[code]
	if !ok {
		return nil, errors.New("not found")
	}
	return t, nil
}

func (m *mockTicketStore) MarkUsed(ticketID, userID string) error {
	if m.markErr != nil {
		return m.markErr
	}
	m.markUsed[ticketID] = userID
	return nil
}

func newTestHandler(t *testing.T) (*apisignup.Handler, *testutil.MockUserRepository, *testutil.MockMetaRepository) {
	t.Helper()
	userRepo := testutil.NewMockUserRepository()
	metaRepo := testutil.NewMockMetaRepository()
	metaRepo.Meta = &model.Meta{ID: "x"}
	idGen, _ := id.NewGenerator("aidx")
	signupSvc := coresignup.NewService(userRepo, metaRepo, idGen)
	h := apisignup.NewHandler(signupSvc, metaRepo, idGen)
	return h, userRepo, metaRepo
}

func doPost(h func(echo.Context) error, body string) *httptest.ResponseRecorder {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	_ = h(c)
	return rec
}

func parseResp(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	return resp
}

// --- Success ---

func TestSignup_Success(t *testing.T) {
	h, _, _ := newTestHandler(t)
	rec := doPost(h.Signup, `{"username":"alice","password":"pass1234"}`)
	assert.Equal(t, http.StatusOK, rec.Code)

	resp := parseResp(t, rec)
	assert.NotEmpty(t, resp["id"])
	assert.Equal(t, "alice", resp["username"])
	assert.NotEmpty(t, resp["token"])
	// MeDetailedフィールドが含まれていること
	assert.Equal(t, false, resp["isAdmin"])
	assert.Equal(t, false, resp["isModerator"])
	assert.NotNil(t, resp["policies"])
	assert.Equal(t, false, resp["twoFactorEnabled"])
	assert.Equal(t, true, resp["preventAiLearning"])
}

// --- Validation ---

func TestSignup_EmptyUsername(t *testing.T) {
	h, _, _ := newTestHandler(t)
	rec := doPost(h.Signup, `{"username":"","password":"pass"}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestSignup_EmptyPassword(t *testing.T) {
	h, _, _ := newTestHandler(t)
	rec := doPost(h.Signup, `{"username":"alice","password":""}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestSignup_InvalidJSON(t *testing.T) {
	h, _, _ := newTestHandler(t)
	rec := doPost(h.Signup, `not json`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestSignup_WhitespaceOnlyUsername(t *testing.T) {
	h, _, _ := newTestHandler(t)
	rec := doPost(h.Signup, `{"username":"   ","password":"pass"}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// --- Username conflicts ---

func TestSignup_DuplicateUsername(t *testing.T) {
	h, userRepo, _ := newTestHandler(t)
	userRepo.Users["u1"] = &model.User{ID: "u1", Username: "taken", UsernameLower: "taken"}
	rec := doPost(h.Signup, `{"username":"taken","password":"pass"}`)
	// upstream Misskey TS と整合 (#798 status, #802 shape): 400 +
	// Fastify-style reply error の `Error: DUPLICATED_USERNAME` message。
	testutil.AssertFastifyError(t, rec, http.StatusBadRequest, "DUPLICATED_USERNAME")
}

func TestSignup_ReservedUsername(t *testing.T) {
	h, _, metaRepo := newTestHandler(t)
	metaRepo.Meta.PreservedUsernames = []string{"admin", "root"}
	rec := doPost(h.Signup, `{"username":"admin","password":"pass"}`)
	testutil.AssertFastifyError(t, rec, http.StatusBadRequest, "USED_USERNAME")
}

// 73 byte 以上の password は bcrypt の制限超過で 400 + PASSWORD_TOO_LONG を返す (#1075)。
func TestSignup_PasswordTooLong(t *testing.T) {
	h, _, _ := newTestHandler(t)
	longPw := strings.Repeat("a", 73)
	body := `{"username":"alice","password":"` + longPw + `"}`
	rec := doPost(h.Signup, body)
	testutil.AssertFastifyError(t, rec, http.StatusBadRequest, "PASSWORD_TOO_LONG")
}

// --- Meta errors ---

func TestSignup_MetaFetchError(t *testing.T) {
	h, _, metaRepo := newTestHandler(t)
	metaRepo.Meta = nil
	rec := doPost(h.Signup, `{"username":"alice","password":"pass"}`)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

// emailRequiredForSignup=true で emailAddress 未指定 → 400
func TestSignup_EmailRequired_NoAddress(t *testing.T) {
	h, _, metaRepo := newTestHandler(t)
	metaRepo.Meta.EmailRequiredForSignup = true
	rec := doPost(h.Signup, `{"username":"alice","password":"pass"}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// emailRequiredForSignup=true + 有効 email → user_pending row 作成 + 確認メール送信 + 204
func TestSignup_EmailRequired_CreatesPendingAndSendsEmail(t *testing.T) {
	userRepo := testutil.NewMockUserRepository()
	metaRepo := testutil.NewMockMetaRepository()
	metaRepo.Meta = &model.Meta{ID: "x", EmailRequiredForSignup: true}
	pendingRepo := testutil.NewMockUserPendingRepository()
	idGen, _ := id.NewGenerator("aidx")
	svc := coresignup.NewService(userRepo, metaRepo, idGen)
	svc.SetUserPendingRepo(pendingRepo)
	h := apisignup.NewHandler(svc, metaRepo, idGen)

	var sentTo string
	var sent miscsmtp.Message
	done := make(chan struct{})
	h.SetEmailSender("https://example.test", func(to string, msg miscsmtp.Message) {
		sentTo, sent = to, msg
		close(done)
	})

	rec := doPost(h.Signup, `{"username":"alice","password":"pass1234","emailAddress":"alice@example.com"}`)
	assert.Equal(t, http.StatusNoContent, rec.Code)
	require.Len(t, pendingRepo.Rows, 1)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("emailSender was not invoked")
	}
	assert.Equal(t, "alice@example.com", sentTo)
	assert.Contains(t, sent.Subject, "Confirm")
	assert.Contains(t, sent.Text, "https://example.test/signup-complete/")
	assert.Contains(t, sent.HTML, "https://example.test/signup-complete/", "HTML body にも link が含まれる")
	assert.Contains(t, sent.HTML, "<!doctype html>", "HTML wrapper が適用される (#600 item 4)")
	// confirmation link に pending.code が埋まっている
	for _, row := range pendingRepo.Rows {
		assert.Contains(t, sent.Text, row.Code)
	}
}

// emailRequiredForSignup=true 経路でも 73 byte 以上 password は CreatePending が
// ErrPasswordTooLong を返し、handler が 400 + PASSWORD_TOO_LONG に変換する (#1075)。
// user_pending row は作成されない (early return)。
func TestSignup_EmailRequired_PasswordTooLong(t *testing.T) {
	userRepo := testutil.NewMockUserRepository()
	metaRepo := testutil.NewMockMetaRepository()
	metaRepo.Meta = &model.Meta{ID: "x", EmailRequiredForSignup: true}
	pendingRepo := testutil.NewMockUserPendingRepository()
	idGen, _ := id.NewGenerator("aidx")
	svc := coresignup.NewService(userRepo, metaRepo, idGen)
	svc.SetUserPendingRepo(pendingRepo)
	h := apisignup.NewHandler(svc, metaRepo, idGen)

	longPw := strings.Repeat("a", 73)
	body := `{"username":"alice","password":"` + longPw + `","emailAddress":"alice@example.com"}`
	rec := doPost(h.Signup, body)
	testutil.AssertFastifyError(t, rec, http.StatusBadRequest, "PASSWORD_TOO_LONG")
	assert.Empty(t, pendingRepo.Rows, "pending row は作成されない")
}

// emailRequiredForSignup=true でも username が空なら通常の INVALID_PARAM 経路
func TestSignup_EmailRequired_DuplicateUsername(t *testing.T) {
	userRepo := testutil.NewMockUserRepository()
	require.NoError(t, userRepo.Create(&model.User{ID: "u1", Username: "alice", UsernameLower: "alice"}))
	metaRepo := testutil.NewMockMetaRepository()
	metaRepo.Meta = &model.Meta{ID: "x", EmailRequiredForSignup: true}
	pendingRepo := testutil.NewMockUserPendingRepository()
	idGen, _ := id.NewGenerator("aidx")
	svc := coresignup.NewService(userRepo, metaRepo, idGen)
	svc.SetUserPendingRepo(pendingRepo)
	h := apisignup.NewHandler(svc, metaRepo, idGen)

	rec := doPost(h.Signup, `{"username":"alice","password":"pass","emailAddress":"a@example.com"}`)
	// upstream 整合 (#798): duplicate は 400 + DUPLICATED_USERNAME。
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Empty(t, pendingRepo.Rows)
}

// --- SignupPending (#595) ---

func TestSignupPending_Success(t *testing.T) {
	userRepo := testutil.NewMockUserRepository()
	metaRepo := testutil.NewMockMetaRepository()
	metaRepo.Meta = &model.Meta{ID: "x", EmailRequiredForSignup: true}
	pendingRepo := testutil.NewMockUserPendingRepository()
	idGen, _ := id.NewGenerator("aidx")
	svc := coresignup.NewService(userRepo, metaRepo, idGen)
	svc.SetUserPendingRepo(pendingRepo)
	h := apisignup.NewHandler(svc, metaRepo, idGen)

	row, err := svc.CreatePending("bob", "bob@example.com", "secret", nil)
	require.NoError(t, err)

	rec := doPost(h.SignupPending, `{"code":"`+row.Code+`"}`)
	assert.Equal(t, http.StatusOK, rec.Code)

	resp := parseResp(t, rec)
	assert.NotEmpty(t, resp["id"])
	assert.NotEmpty(t, resp["i"])
	assert.Empty(t, pendingRepo.Rows)
}

// stubSigninRecorder captures RecordSuccessfulSignin calls (#1804).
type stubSigninRecorder struct {
	mu      sync.Mutex
	userIDs []string
}

func (s *stubSigninRecorder) RecordSuccessfulSignin(userID, _ string, _ http.Header) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.userIDs = append(s.userIDs, userID)
}

func (s *stubSigninRecorder) called() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.userIDs...)
}

// #1804: signup-pending 完了時に signin 副作用 (履歴 / login 通知 / main publish) を
// 新規ユーザー ID で発火する。
func TestSignupPending_FiresSigninSideEffects(t *testing.T) {
	userRepo := testutil.NewMockUserRepository()
	metaRepo := testutil.NewMockMetaRepository()
	metaRepo.Meta = &model.Meta{ID: "x", EmailRequiredForSignup: true}
	pendingRepo := testutil.NewMockUserPendingRepository()
	idGen, _ := id.NewGenerator("aidx")
	svc := coresignup.NewService(userRepo, metaRepo, idGen)
	svc.SetUserPendingRepo(pendingRepo)
	h := apisignup.NewHandler(svc, metaRepo, idGen)
	rec := &stubSigninRecorder{}
	h.SetSigninRecorder(rec)

	row, err := svc.CreatePending("bob", "bob@example.com", "secret", nil)
	require.NoError(t, err)

	resp := parseResp(t, doPost(h.SignupPending, `{"code":"`+row.Code+`"}`))
	require.NotEmpty(t, resp["id"])
	called := rec.called()
	require.Len(t, called, 1, "signup-pending 完了で signin 副作用を 1 回発火する")
	assert.Equal(t, resp["id"], called[0], "新規ユーザー ID で発火する")
}

// #1804: 通常 signup 完了でも signin 副作用を発火する。
func TestSignup_FiresSigninSideEffects(t *testing.T) {
	userRepo := testutil.NewMockUserRepository()
	metaRepo := testutil.NewMockMetaRepository()
	metaRepo.Meta = &model.Meta{ID: "x"}
	idGen, _ := id.NewGenerator("aidx")
	svc := coresignup.NewService(userRepo, metaRepo, idGen)
	h := apisignup.NewHandler(svc, metaRepo, idGen)
	h.SetTestMode(true)
	rec := &stubSigninRecorder{}
	h.SetSigninRecorder(rec)

	resp := parseResp(t, doPost(h.Signup, `{"username":"alice","password":"password123"}`))
	require.NotEmpty(t, resp["id"])
	called := rec.called()
	require.Len(t, called, 1)
	assert.Equal(t, resp["id"], called[0])
}

func TestSignupPending_InvalidParam(t *testing.T) {
	h, _, _ := newTestHandler(t)
	rec := doPost(h.SignupPending, `{}`)
	// upstream は handler 全体が try-catch で囲まれていて status 400 +
	// Fastify shape を返す (#809)。code が空の場合も同 shape に揃える。
	testutil.AssertFastifyError(t, rec, http.StatusBadRequest, "INVALID_PARAM")
}

func TestSignupPending_NotFound(t *testing.T) {
	userRepo := testutil.NewMockUserRepository()
	metaRepo := testutil.NewMockMetaRepository()
	metaRepo.Meta = &model.Meta{ID: "x"}
	pendingRepo := testutil.NewMockUserPendingRepository()
	idGen, _ := id.NewGenerator("aidx")
	svc := coresignup.NewService(userRepo, metaRepo, idGen)
	svc.SetUserPendingRepo(pendingRepo)
	h := apisignup.NewHandler(svc, metaRepo, idGen)

	rec := doPost(h.SignupPending, `{"code":"ghost"}`)
	// upstream に揃えて 400 + NO_SUCH_CODE (旧 mk-go は 404)。#809 で
	// status drift も解消。
	testutil.AssertFastifyError(t, rec, http.StatusBadRequest, "NO_SUCH_CODE")
}

// --- Registration disabled (invitation code) ---

func TestSignup_RegistrationDisabled_NoCode(t *testing.T) {
	h, _, metaRepo := newTestHandler(t)
	metaRepo.Meta.DisableRegistration = true
	rec := doPost(h.Signup, `{"username":"alice","password":"pass"}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	resp := parseResp(t, rec)
	errObj := resp["error"].(map[string]any)
	assert.Equal(t, "INVITATION_CODE_INVALID", errObj["code"])
}

func TestSignup_RegistrationDisabled_NoTicketStore(t *testing.T) {
	h, _, metaRepo := newTestHandler(t)
	metaRepo.Meta.DisableRegistration = true
	// ticketStore未設定、codeあり → 検証不可
	rec := doPost(h.Signup, `{"username":"alice","password":"pass","invitationCode":"abc"}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestSignup_RegistrationDisabled_ValidCode(t *testing.T) {
	h, _, metaRepo := newTestHandler(t)
	metaRepo.Meta.DisableRegistration = true
	store := newMockTicketStore()
	store.tickets["valid-code"] = &model.RegistrationTicket{ID: "t1", Code: "valid-code"}
	h.SetTicketStore(store)

	rec := doPost(h.Signup, `{"username":"alice","password":"pass","invitationCode":"valid-code"}`)
	assert.Equal(t, http.StatusOK, rec.Code)

	// ticketが使用済みにされたことを確認
	assert.Equal(t, store.markUsed["t1"], parseResp(t, rec)["id"])
}

func TestSignup_RegistrationDisabled_CodeNotFound(t *testing.T) {
	h, _, metaRepo := newTestHandler(t)
	metaRepo.Meta.DisableRegistration = true
	store := newMockTicketStore()
	h.SetTicketStore(store)

	rec := doPost(h.Signup, `{"username":"alice","password":"pass","invitationCode":"bad-code"}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestSignup_RegistrationDisabled_UsedCode(t *testing.T) {
	h, _, metaRepo := newTestHandler(t)
	metaRepo.Meta.DisableRegistration = true
	store := newMockTicketStore()
	usedBy := "someone"
	store.tickets["used-code"] = &model.RegistrationTicket{ID: "t2", Code: "used-code", UsedByID: &usedBy}
	h.SetTicketStore(store)

	rec := doPost(h.Signup, `{"username":"alice","password":"pass","invitationCode":"used-code"}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestSignup_RegistrationDisabled_ExpiredCode(t *testing.T) {
	h, _, metaRepo := newTestHandler(t)
	metaRepo.Meta.DisableRegistration = true
	store := newMockTicketStore()
	expired := time.Now().Add(-24 * time.Hour)
	store.tickets["expired-code"] = &model.RegistrationTicket{ID: "t3", Code: "expired-code", ExpiresAt: &expired}
	h.SetTicketStore(store)

	rec := doPost(h.Signup, `{"username":"alice","password":"pass","invitationCode":"expired-code"}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// --- CAPTCHA ---

// failingCaptchaVerifier is defined but unused because we use the real testcaptcha.
var _ captcha.Verifier = (*failingCaptchaVerifier)(nil)

type failingCaptchaVerifier struct{}

func (f *failingCaptchaVerifier) Verify(_ context.Context, _ string) error {
	return captcha.ErrVerificationFail
}

func TestSignup_CaptchaFailed(t *testing.T) {
	h, _, metaRepo := newTestHandler(t)
	metaRepo.Meta.EnableTestcaptcha = true
	captchaSvc := captcha.NewService(metaRepo.Meta)
	h.SetCaptcha(captchaSvc)

	// testcaptcha-response が空 → 検証失敗
	rec := doPost(h.Signup, `{"username":"alice","password":"pass"}`)
	// upstream は captcha 失敗を Fastify-style reply error で返す (#802)。
	testutil.AssertFastifyError(t, rec, http.StatusBadRequest, "CAPTCHA_FAILED")
}

func TestSignup_CaptchaSuccess(t *testing.T) {
	h, _, metaRepo := newTestHandler(t)
	metaRepo.Meta.EnableTestcaptcha = true
	captchaSvc := captcha.NewService(metaRepo.Meta)
	h.SetCaptcha(captchaSvc)

	rec := doPost(h.Signup, `{"username":"alice","password":"pass","testcaptcha-response":"testcaptcha-passed"}`)
	assert.Equal(t, http.StatusOK, rec.Code)
}

// --- Internal error from signup service ---

type failingCreateUserRepo struct {
	*testutil.MockUserRepository
}

func (r *failingCreateUserRepo) Create(_ *model.User) error {
	return assert.AnError
}

func TestSignup_InternalError(t *testing.T) {
	userRepo := testutil.NewMockUserRepository()
	metaRepo := testutil.NewMockMetaRepository()
	metaRepo.Meta = &model.Meta{ID: "x"}
	idGen, _ := id.NewGenerator("aidx")
	failRepo := &failingCreateUserRepo{userRepo}
	signupSvc := coresignup.NewService(failRepo, metaRepo, idGen)
	h := apisignup.NewHandler(signupSvc, metaRepo, idGen)

	rec := doPost(h.Signup, `{"username":"alice","password":"pass"}`)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

// --- Response shape ---

func TestSignup_ResponseShape(t *testing.T) {
	h, _, _ := newTestHandler(t)
	rec := doPost(h.Signup, `{"username":"carol","password":"pass1234"}`)
	require.Equal(t, http.StatusOK, rec.Code)

	resp := parseResp(t, rec)

	requiredFields := []string{
		"id", "username", "token", "host", "avatarUrl",
		"isBot", "isCat", "isLocked", "isSuspended", "isSilenced",
		"isAdmin", "isModerator", "followersCount", "followingCount", "notesCount",
		"createdAt", "policies", "twoFactorEnabled", "preventAiLearning",
		"publicReactions", "autoAcceptFollowed",
	}
	for _, f := range requiredFields {
		_, exists := resp[f]
		assert.True(t, exists, "missing field: %s", f)
	}
}

// emailRequiredForSignup=true で email validation 失敗 (banned domain)
func TestSignup_EmailRequired_BannedDomain(t *testing.T) {
	userRepo := testutil.NewMockUserRepository()
	metaRepo := testutil.NewMockMetaRepository()
	metaRepo.Meta = &model.Meta{ID: "x", EmailRequiredForSignup: true, BannedEmailDomains: []string{"bad.example"}}
	pendingRepo := testutil.NewMockUserPendingRepository()
	idGen, _ := id.NewGenerator("aidx")
	svc := coresignup.NewService(userRepo, metaRepo, idGen)
	svc.SetUserPendingRepo(pendingRepo)
	h := apisignup.NewHandler(svc, metaRepo, idGen)

	rec := doPost(h.Signup, `{"username":"alice","password":"pass","emailAddress":"x@bad.example"}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Empty(t, pendingRepo.Rows)
}

// signupConfirmURL fallback (serverURL 未設定)
func TestSignup_EmailRequired_DefaultURL(t *testing.T) {
	userRepo := testutil.NewMockUserRepository()
	metaRepo := testutil.NewMockMetaRepository()
	metaRepo.Meta = &model.Meta{ID: "x", EmailRequiredForSignup: true}
	pendingRepo := testutil.NewMockUserPendingRepository()
	idGen, _ := id.NewGenerator("aidx")
	svc := coresignup.NewService(userRepo, metaRepo, idGen)
	svc.SetUserPendingRepo(pendingRepo)
	h := apisignup.NewHandler(svc, metaRepo, idGen)

	var sent miscsmtp.Message
	done := make(chan struct{})
	// SetEmailSender("", ...) で serverURL 空 → "https://localhost" fallback
	h.SetEmailSender("", func(_ string, msg miscsmtp.Message) {
		sent = msg
		close(done)
	})
	rec := doPost(h.Signup, `{"username":"al","password":"pw","emailAddress":"al@example.com"}`)
	assert.Equal(t, http.StatusNoContent, rec.Code)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("emailSender was not invoked")
	}
	assert.Contains(t, sent.Text, "https://localhost/signup-complete/")
}

// SignupPending: expired path
func TestSignupPending_Expired(t *testing.T) {
	userRepo := testutil.NewMockUserRepository()
	metaRepo := testutil.NewMockMetaRepository()
	metaRepo.Meta = &model.Meta{ID: "x", EmailRequiredForSignup: true}
	pendingRepo := testutil.NewMockUserPendingRepository()
	idGen, _ := id.NewGenerator("aidx")
	svc := coresignup.NewService(userRepo, metaRepo, idGen)
	svc.SetUserPendingRepo(pendingRepo)
	h := apisignup.NewHandler(svc, metaRepo, idGen)

	row, err := svc.CreatePending("expu", "exp@example.com", "pw", nil)
	require.NoError(t, err)
	// 25h 前の ULID に書き換えて expired path を踏ませる
	old := idGen.Generate(time.Now().Add(-25 * time.Hour))
	delete(pendingRepo.Rows, row.ID)
	row.ID = old
	pendingRepo.Rows[old] = row

	rec := doPost(h.SignupPending, `{"code":"`+row.Code+`"}`)
	// upstream に揃えて 400 + EXPIRED (旧 mk-go は 410)。#809 で status
	// drift も解消。
	testutil.AssertFastifyError(t, rec, http.StatusBadRequest, "EXPIRED")
}

// SignupPending: username clash 後の Conflict
func TestSignupPending_UsernameClash(t *testing.T) {
	userRepo := testutil.NewMockUserRepository()
	metaRepo := testutil.NewMockMetaRepository()
	metaRepo.Meta = &model.Meta{ID: "x", EmailRequiredForSignup: true}
	pendingRepo := testutil.NewMockUserPendingRepository()
	idGen, _ := id.NewGenerator("aidx")
	svc := coresignup.NewService(userRepo, metaRepo, idGen)
	svc.SetUserPendingRepo(pendingRepo)
	h := apisignup.NewHandler(svc, metaRepo, idGen)

	row, err := svc.CreatePending("clash2", "c2@example.com", "pw", nil)
	require.NoError(t, err)
	require.NoError(t, userRepo.Create(&model.User{ID: "u_c2", Username: "Clash2", UsernameLower: "clash2"}))

	rec := doPost(h.SignupPending, `{"code":"`+row.Code+`"}`)
	// upstream 整合 (#798 status, #802/#809 shape): duplicate は 400 +
	// Fastify shape DUPLICATED_USERNAME。
	testutil.AssertFastifyError(t, rec, http.StatusBadRequest, "DUPLICATED_USERNAME")
}

// emailSender が未設定でも pending row 自体は作られて 204 を返す (テスト用 setup)
func TestSignup_EmailRequired_NoSender(t *testing.T) {
	userRepo := testutil.NewMockUserRepository()
	metaRepo := testutil.NewMockMetaRepository()
	metaRepo.Meta = &model.Meta{ID: "x", EmailRequiredForSignup: true}
	pendingRepo := testutil.NewMockUserPendingRepository()
	idGen, _ := id.NewGenerator("aidx")
	svc := coresignup.NewService(userRepo, metaRepo, idGen)
	svc.SetUserPendingRepo(pendingRepo)
	h := apisignup.NewHandler(svc, metaRepo, idGen)

	rec := doPost(h.Signup, `{"username":"ns","password":"pw","emailAddress":"ns@example.com"}`)
	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Len(t, pendingRepo.Rows, 1)
}

func TestSetTestMode(t *testing.T) {
	h, _, metaRepo := newTestHandler(t)
	metaRepo.Meta.EmailRequiredForSignup = true
	// testMode で email-required branch をバイパスして通常 path に流す
	h.SetTestMode(true)
	rec := doPost(h.Signup, `{"username":"alice","password":"pass1234"}`)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestSetEmailValidationClient(t *testing.T) {
	h, _, _ := newTestHandler(t)
	// 設定しても normal signup path は壊れない (verifymail / truemail が
	// 無効な meta では呼ばれないので transport が触られない)。
	h.SetEmailValidationClient(&http.Client{})
	rec := doPost(h.Signup, `{"username":"clientset","password":"pass1234"}`)
	assert.Equal(t, http.StatusOK, rec.Code)
}

// 招待制 + email 確認制の併用: signup で ticket.ID が pending row に保存され、
// SignupPending 経由で本登録時に MarkUsed が呼ばれて消費されることを検証
// (#600 item 5)。
func TestSignupPending_MarksInvitationTicketUsed(t *testing.T) {
	userRepo := testutil.NewMockUserRepository()
	metaRepo := testutil.NewMockMetaRepository()
	// 招待制 + email 確認制を同時に有効化
	metaRepo.Meta = &model.Meta{ID: "x", EmailRequiredForSignup: true, DisableRegistration: true}
	pendingRepo := testutil.NewMockUserPendingRepository()
	idGen, _ := id.NewGenerator("aidx")
	svc := coresignup.NewService(userRepo, metaRepo, idGen)
	svc.SetUserPendingRepo(pendingRepo)
	h := apisignup.NewHandler(svc, metaRepo, idGen)

	tickets := newMockTicketStore()
	tickets.tickets["INV1"] = &model.RegistrationTicket{ID: "ticket_inv1"}
	h.SetTicketStore(tickets)

	// signup → email 確認待ちに pending 作成
	rec := doPost(h.Signup, `{"username":"invu","password":"pw","emailAddress":"inv@example.com","invitationCode":"INV1"}`)
	require.Equal(t, http.StatusNoContent, rec.Code)
	require.Len(t, pendingRepo.Rows, 1)
	// pending row に ticket.ID が保存されている
	var stored *model.UserPending
	for _, p := range pendingRepo.Rows {
		stored = p
	}
	require.NotNil(t, stored.InvitationTicketID)
	assert.Equal(t, "ticket_inv1", *stored.InvitationTicketID)

	// signup-pending → user 確定 + ticket 消費
	rec = doPost(h.SignupPending, `{"code":"`+stored.Code+`"}`)
	require.Equal(t, http.StatusOK, rec.Code)

	resp := parseResp(t, rec)
	userID, _ := resp["id"].(string)
	require.NotEmpty(t, userID)
	// MarkUsed が呼ばれて ticket.ID → user.ID が記録される
	assert.Equal(t, userID, tickets.markUsed["ticket_inv1"])
}

// --- coverage 補完 (#739): Signup / SignupPending の error 分岐を network 化 ---

// 129 文字 username → service が ErrInvalidUsername を返す。email-required path
// 経由で 400 INVALID_PARAM が返ることを確認する。
func TestSignup_EmailRequired_InvalidUsername(t *testing.T) {
	h, _, metaRepo := newTestHandler(t)
	metaRepo.Meta.EmailRequiredForSignup = true
	long := strings.Repeat("a", 129)
	rec := doPost(h.Signup, `{"username":"`+long+`","password":"pass","emailAddress":"x@example.com"}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// preserved username が email-required path で USED_USERNAME を返すこと。
func TestSignup_EmailRequired_ReservedUsername(t *testing.T) {
	h, _, metaRepo := newTestHandler(t)
	metaRepo.Meta.EmailRequiredForSignup = true
	metaRepo.Meta.PreservedUsernames = []string{"admin"}
	rec := doPost(h.Signup, `{"username":"admin","password":"pass","emailAddress":"x@example.com"}`)
	testutil.AssertFastifyError(t, rec, http.StatusBadRequest, "USED_USERNAME")
}

// 注: SignupPending の ErrInvitationAlreadyUsed / ErrInvitationRevoked は
// service.PromotePending の **tx 経路** (db + ticketRepo wired) でのみ発火し、
// mock ベース handler test では再現不可。これらの coverage は repository /
// service integration test 側で別途担保する想定で本 handler test では skip。
