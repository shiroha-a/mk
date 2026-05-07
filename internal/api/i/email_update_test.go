package i

import (
	"net/http"
	"testing"

	"github.com/shiroha-a/mk/internal/model"
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
