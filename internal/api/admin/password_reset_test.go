package admin_test

import (
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/shiroha-a/mk/internal/core/moderationlog"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubPasswordResetRepo struct {
	created *model.PasswordResetRequest
	err     error
}

func (s *stubPasswordResetRepo) Create(req *model.PasswordResetRequest) error {
	if s.err != nil {
		return s.err
	}
	s.created = req
	return nil
}

func (s *stubPasswordResetRepo) FindByToken(_ string) (*model.PasswordResetRequest, error) {
	return s.created, nil
}

func (s *stubPasswordResetRepo) Delete(_ string) error { return nil }

type capturedEmail struct {
	mu      sync.Mutex
	to      string
	subject string
	body    string
	called  int
}

func (c *capturedEmail) send(to, subject, body string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.called++
	c.to = to
	c.subject = subject
	c.body = body
}

func (c *capturedEmail) snapshot() capturedEmail {
	c.mu.Lock()
	defer c.mu.Unlock()
	return capturedEmail{to: c.to, subject: c.subject, body: c.body, called: c.called}
}

// attachModLog wires a fresh moderation log service backed by the
// shared race-safe testutil mock and returns the mock so the test can
// assert on Snapshot() without touching internals.
func attachModLog(t *testing.T, h modLogServiceSetter) *testutil.MockModerationLogRepository {
	t.Helper()
	repo := testutil.NewMockModerationLogRepository()
	gen, err := id.NewGenerator("aidx")
	require.NoError(t, err)
	h.SetModLogService(moderationlog.New(repo, gen))
	return repo
}

// modLogServiceSetter narrows *apiadmin.Handler to just the setter we
// need so the helper stays compatible with any future handler that
// adopts the same wiring pattern.
type modLogServiceSetter interface {
	SetModLogService(*moderationlog.Service)
}

func TestResetPasswordAdmin_Empty(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	assert.Equal(t, http.StatusBadRequest, doPost(h.ResetPassword, `{}`, adminUser).Code)
}

func TestResetPasswordAdmin_Success(t *testing.T) {
	h, userRepo, _, _ := newTestHandler(t)
	userRepo.Users["u1"] = &model.User{ID: "u1", Username: "testuser"}
	userRepo.Profiles["u1"] = &model.UserProfile{UserID: "u1"}
	rec := doPost(h.ResetPassword, `{"userId":"u1"}`, adminUser)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "password")
}

// #1539: root アカウントのパスワードはリセットできない (upstream root guard)。
func TestResetPasswordAdmin_RootRejected(t *testing.T) {
	h, userRepo, _, _ := newTestHandler(t)
	userRepo.Users["root1"] = &model.User{ID: "root1", Username: "admin", IsRoot: true}
	rec := doPost(h.ResetPassword, `{"userId":"root1"}`, adminUser)
	require.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "ACCESS_DENIED")
}

func TestResetPasswordAdmin_VerifiedEmailSendsResetLink(t *testing.T) {
	h, userRepo, _, _ := newTestHandler(t)
	userRepo.Users["u1"] = &model.User{ID: "u1", Username: "testuser"}
	email := "alice@example.com"
	userRepo.Profiles["u1"] = &model.UserProfile{UserID: "u1", Email: &email, EmailVerified: true}

	resetRepo := &stubPasswordResetRepo{}
	captured := &capturedEmail{}
	h.SetPasswordResetRepo(resetRepo)
	h.SetEmailSender(captured.send)
	h.SetServerURL("https://example.com")

	rec := doPost(h.ResetPassword, `{"userId":"u1"}`, adminUser)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"sent":true`)
	assert.NotContains(t, rec.Body.String(), "password")

	// Email は goroutine で送られるので少し待つ
	deadline := time.Now().Add(500 * time.Millisecond)
	var snap capturedEmail
	for time.Now().Before(deadline) {
		snap = captured.snapshot()
		if snap.called > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	assert.Equal(t, 1, snap.called)
	assert.Equal(t, email, snap.to)
	assert.Equal(t, "Password reset", snap.subject)
	require.NotNil(t, resetRepo.created, "reset token should be persisted")
	assert.Contains(t, snap.body, resetRepo.created.Token,
		"body should contain the persisted token in the reset link")
	assert.Contains(t, snap.body, "https://example.com/reset-password/")
}

func TestResetPasswordAdmin_UnverifiedEmailFallsBack(t *testing.T) {
	h, userRepo, _, _ := newTestHandler(t)
	userRepo.Users["u1"] = &model.User{ID: "u1"}
	email := "alice@example.com"
	userRepo.Profiles["u1"] = &model.UserProfile{UserID: "u1", Email: &email, EmailVerified: false}

	h.SetPasswordResetRepo(&stubPasswordResetRepo{})
	captured := &capturedEmail{}
	h.SetEmailSender(captured.send)

	rec := doPost(h.ResetPassword, `{"userId":"u1"}`, adminUser)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "password")
	assert.Equal(t, 0, captured.called, "unverified email must not trigger sender")
}

func TestResetPasswordAdmin_MissingEmailFallsBack(t *testing.T) {
	h, userRepo, _, _ := newTestHandler(t)
	userRepo.Users["u1"] = &model.User{ID: "u1"}
	userRepo.Profiles["u1"] = &model.UserProfile{UserID: "u1"} // email = nil
	h.SetPasswordResetRepo(&stubPasswordResetRepo{})
	h.SetEmailSender(func(string, string, string) {})

	rec := doPost(h.ResetPassword, `{"userId":"u1"}`, adminUser)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "password")
}

func TestResetPasswordAdmin_NoRepoFallsBack(t *testing.T) {
	h, userRepo, _, _ := newTestHandler(t)
	userRepo.Users["u1"] = &model.User{ID: "u1"}
	email := "a@b.c"
	userRepo.Profiles["u1"] = &model.UserProfile{UserID: "u1", Email: &email, EmailVerified: true}
	h.SetEmailSender(func(string, string, string) {})
	// Repo 未設定 → fallback

	rec := doPost(h.ResetPassword, `{"userId":"u1"}`, adminUser)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "password")
}

func TestResetPasswordAdmin_WritesModerationLog_FallbackPath(t *testing.T) {
	h, userRepo, _, _ := newTestHandler(t)
	userRepo.Users["u1"] = &model.User{ID: "u1", Username: "alice"}
	userRepo.Profiles["u1"] = &model.UserProfile{UserID: "u1"} // email 未設定 → fallback

	repo := attachModLog(t, h)

	rec := doPost(h.ResetPassword, `{"userId":"u1"}`, adminUser)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "password")

	require.Eventually(t, func() bool {
		return len(repo.Snapshot()) == 1
	}, 500*time.Millisecond, 5*time.Millisecond, "moderation log should be written")

	logs := repo.Snapshot()
	assert.Equal(t, "admin1", logs[0].UserID)
	assert.Equal(t, "resetPassword", logs[0].Type)
	var info map[string]any
	require.NoError(t, json.Unmarshal(logs[0].Info, &info))
	assert.Equal(t, "u1", info["userId"])
	assert.Equal(t, "alice", info["userUsername"])
}

func TestResetPasswordAdmin_WritesModerationLog_EmailPath(t *testing.T) {
	h, userRepo, _, _ := newTestHandler(t)
	userRepo.Users["u1"] = &model.User{ID: "u1", Username: "bob"}
	email := "bob@example.com"
	userRepo.Profiles["u1"] = &model.UserProfile{UserID: "u1", Email: &email, EmailVerified: true}

	h.SetPasswordResetRepo(&stubPasswordResetRepo{})
	h.SetEmailSender(func(string, string, string) {})
	repo := attachModLog(t, h)

	rec := doPost(h.ResetPassword, `{"userId":"u1"}`, adminUser)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"sent":true`)

	require.Eventually(t, func() bool {
		return len(repo.Snapshot()) == 1
	}, 500*time.Millisecond, 5*time.Millisecond, "moderation log should be written on email path")

	logs := repo.Snapshot()
	assert.Equal(t, "admin1", logs[0].UserID)
	assert.Equal(t, "resetPassword", logs[0].Type)
	var info map[string]any
	require.NoError(t, json.Unmarshal(logs[0].Info, &info))
	assert.Equal(t, "u1", info["userId"])
	assert.Equal(t, "bob", info["userUsername"])
}

func TestResetPasswordAdmin_NoLogWhenServiceUnwired(t *testing.T) {
	// service が未配線 (production で誤って setter を忘れた等) でも
	// API 自体は機能し続けることを保証する。fire-and-forget の趣旨は
	// 「audit 失敗で本処理を止めない」なので、未配線も同じ扱い。
	h, userRepo, _, _ := newTestHandler(t)
	userRepo.Users["u1"] = &model.User{ID: "u1", Username: "alice"}
	userRepo.Profiles["u1"] = &model.UserProfile{UserID: "u1"}

	rec := doPost(h.ResetPassword, `{"userId":"u1"}`, adminUser)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestResetPasswordAdmin_NoLogWhenTargetMissing(t *testing.T) {
	// targetUserId が DB に存在しないケース。issueTemporaryPassword の
	// UpdateProfile が成功しても logUserAction の FindByID が失敗して
	// log は書かれない。レアケースだが logUserAction の lookup-failure
	// branch を guard する。
	h, _, _, _ := newTestHandler(t)
	// u1 を userRepo に登録しない → FindByID は error 返す
	repo := attachModLog(t, h)

	rec := doPost(h.ResetPassword, `{"userId":"u1"}`, adminUser)
	assert.Equal(t, http.StatusOK, rec.Code)

	require.Never(t, func() bool {
		return len(repo.Snapshot()) > 0
	}, 100*time.Millisecond, 10*time.Millisecond, "target missing → log must not be written")
}

func TestResetPasswordAdmin_NoLogWhenActorMissing(t *testing.T) {
	// RequireModerator middleware が actor を context に載せ忘れる
	// (= 配線ミス) ケース。logUserAction の actor==nil branch を経由して
	// log は書かれず、本処理 (パスワード reset) は通常通り完了する。
	h, userRepo, _, _ := newTestHandler(t)
	userRepo.Users["u1"] = &model.User{ID: "u1", Username: "alice"}
	userRepo.Profiles["u1"] = &model.UserProfile{UserID: "u1"}

	repo := attachModLog(t, h)

	rec := doPost(h.ResetPassword, `{"userId":"u1"}`, nil) // actor 未配線
	assert.Equal(t, http.StatusOK, rec.Code)

	require.Never(t, func() bool {
		return len(repo.Snapshot()) > 0
	}, 100*time.Millisecond, 10*time.Millisecond, "actor missing → log must not be written")
}

func TestResetPasswordAdmin_ResetRepoErrorFallsBack(t *testing.T) {
	h, userRepo, _, _ := newTestHandler(t)
	userRepo.Users["u1"] = &model.User{ID: "u1"}
	email := "a@b.c"
	userRepo.Profiles["u1"] = &model.UserProfile{UserID: "u1", Email: &email, EmailVerified: true}
	h.SetPasswordResetRepo(&stubPasswordResetRepo{err: assertError{}})
	h.SetEmailSender(func(string, string, string) {})

	rec := doPost(h.ResetPassword, `{"userId":"u1"}`, adminUser)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "password",
		"reset repo failure should fall back to legacy temp-password path")
}
