package i

import (
	"net/http"
	"testing"

	"github.com/lib/pq"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
)

func TestUpdateEmail_WrongPassword(t *testing.T) {
	h, repo := newExtraHandler(t)
	pwd := hashPassword("secret")
	repo.Profiles["u1"] = &model.UserProfile{UserID: "u1", Password: &pwd}
	// upstream Misskey TS は ApiError(meta.errors.incorrectPassword) を
	// framework が 400 (= client error) に変換 (#885)。drop-in 互換のため
	// mk-go も 400 に揃える (旧 mk-go は 403)。
	assert.Equal(t, http.StatusBadRequest, postExtra(h.UpdateEmail, `{"password":"wrong"}`, stubUser).Code)
}

func TestUpdateEmail_ClearEmail(t *testing.T) {
	h, repo := newExtraHandler(t)
	pwd := hashPassword("secret")
	email := "old@example.com"
	repo.Profiles["u1"] = &model.UserProfile{UserID: "u1", Password: &pwd, Email: &email}
	// email を null にセットしてクリア
	rec := postExtra(h.UpdateEmail, `{"password":"secret","email":null}`, stubUser)
	assert.Equal(t, http.StatusOK, rec.Code)
}

// #1546: email=null かつ emailRequiredForSignup なら EMAIL_REQUIRED (324c7a88)。
func TestUpdateEmail_ClearEmailRequired(t *testing.T) {
	h, repo := newExtraHandler(t)
	pwd := hashPassword("secret")
	email := "old@example.com"
	repo.Profiles["u1"] = &model.UserProfile{UserID: "u1", Password: &pwd, Email: &email}
	metaRepo := testutil.NewMockMetaRepository()
	metaRepo.Meta = &model.Meta{ID: "x", EmailRequiredForSignup: true}
	h.SetMetaRepo(metaRepo)

	rec := postExtra(h.UpdateEmail, `{"password":"secret","email":null}`, stubUser)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "EMAIL_REQUIRED")
	assert.Contains(t, rec.Body.String(), "324c7a88-59f2-492f-903f-89134f93e47e")
}

func TestUpdateEmail_SetNewEmail(t *testing.T) {
	h, repo := newExtraHandler(t)
	pwd := hashPassword("secret")
	repo.Profiles["u1"] = &model.UserProfile{UserID: "u1", Password: &pwd}
	rec := postExtra(h.UpdateEmail, `{"password":"secret","email":"new@example.com"}`, stubUser)
	assert.Equal(t, http.StatusOK, rec.Code)
	// emailVerifyCode が生成されている
	p := repo.Profiles["u1"]
	assert.NotNil(t, p.EmailVerifyCode)
}

func TestUpdateEmail_NoProfile(t *testing.T) {
	h, _ := newExtraHandler(t)
	// profile がない → 500
	assert.Equal(t, http.StatusInternalServerError, postExtra(h.UpdateEmail, `{"password":"x"}`, stubUser).Code)
}

// TOTP gate (upstream drop-in 互換): 2FA 有効ユーザが token 無しで
// update-email を呼ぶと 403 INVALID_TOKEN で refuse される。email 乗っ取り
// から password reset → account takeover の連鎖を防ぐ最重要 gate の 1 つ。
func TestUpdateEmail_With2FA_RequiresToken(t *testing.T) {
	h, repo := newExtraHandler(t)
	pwd := hashPassword("secret")
	repo.Profiles["u1"] = &model.UserProfile{
		UserID:                "u1",
		Password:              &pwd,
		TwoFactorEnabled:      true,
		TwoFactorBackupSecret: pq.StringArray{"backup1", "backup2"},
	}
	rec := postExtra(h.UpdateEmail, `{"password":"secret","email":"attacker@example.com"}`, stubUser)
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "INVALID_TOKEN")
}

// 2FA 有効でも valid token (backup code) を渡せば成功する。
func TestUpdateEmail_With2FA_AcceptsBackupCode(t *testing.T) {
	h, repo := newExtraHandler(t)
	pwd := hashPassword("secret")
	repo.Profiles["u1"] = &model.UserProfile{
		UserID:                "u1",
		Password:              &pwd,
		TwoFactorEnabled:      true,
		TwoFactorBackupSecret: pq.StringArray{"backup1", "backup2"},
	}
	rec := postExtra(h.UpdateEmail, `{"password":"secret","email":"new@example.com","token":"backup1"}`, stubUser)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestVerifyEmail_InvalidCode(t *testing.T) {
	h, _ := newExtraHandler(t)
	rec := postExtra(h.VerifyEmail, `{"code":"nonexistent"}`, stubUser)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestVerifyEmail_EmptyCodeRejected(t *testing.T) {
	h, _ := newExtraHandler(t)
	rec := postExtra(h.VerifyEmail, `{}`, stubUser)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestVerifyEmail_Success(t *testing.T) {
	h, repo := newExtraHandler(t)
	code := "abc"
	repo.Profiles["u1"] = &model.UserProfile{UserID: "u1", EmailVerifyCode: &code}
	rec := postExtra(h.VerifyEmail, `{"code":"abc"}`, stubUser)
	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.True(t, repo.Profiles["u1"].EmailVerified)
}

// #1551 verify-email 成功時に meUpdated を main stream へ publish する
// (upstream verify-email.ts)。
func TestVerifyEmail_PublishesMeUpdated(t *testing.T) {
	h, repo := newExtraHandler(t)
	user := setupUserWithPassword(repo, "u1", "pass")
	code := "abc"
	repo.Profiles["u1"].EmailVerifyCode = &code
	pub := &stubIMainStreamPublisher{}
	h.SetMainStreamPublisher(pub)

	rec := postExtra(h.VerifyEmail, `{"code":"abc"}`, user)
	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.True(t, repo.Profiles["u1"].EmailVerified)
	requireMeUpdated(t, pub, "u1")
}
