package i

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TOTP フルフロー: register → done → unregister。それぞれの happy path と
// 各種エラー (NoProfile / WrongPassword / NoTempSecret / InvalidToken) を
// カバーする。

func TestTwoFARegister_Success(t *testing.T) {
	h, repo := newExtraHandler(t)
	user := setupUserWithPassword(repo, "u1", "pass")
	rec := postExtra(h.TwoFARegister, `{"password":"pass"}`, user)
	require.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.NotEmpty(t, resp["secret"])
	assert.Equal(t, "Misskey", resp["issuer"])
	// `url` は otpauth:// 形式 (authenticator アプリの「アプリで開く」用)、
	// `qr` は PNG data URL (frontend が <img src=...> で読む用) — 両者は
	// 別形式でなければならない (#697)。
	assert.Contains(t, resp["url"], "otpauth://totp/")
	assert.Contains(t, resp["qr"], "data:image/png;base64,",
		"qr field must be a base64 PNG data URL so the frontend <img src=...> renders")

	// tempSecret がプロファイルに書き込まれている
	profile := repo.Profiles["u1"]
	require.NotNil(t, profile.TwoFactorTempSecret)
}

func TestTwoFARegister_WrongPassword(t *testing.T) {
	h, repo := newExtraHandler(t)
	user := setupUserWithPassword(repo, "u1", "correct")
	rec := postExtra(h.TwoFARegister, `{"password":"wrong"}`, user)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestTwoFARegister_NoProfile(t *testing.T) {
	h, repo := newExtraHandler(t)
	repo.Users["u1"] = &model.User{ID: "u1"}
	rec := postExtra(h.TwoFARegister, `{"password":"pass"}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestTwoFADone_Success(t *testing.T) {
	h, repo := newExtraHandler(t)
	user := setupUserWithPassword(repo, "u1", "pass")

	// register で tempSecret を生成
	rec := postExtra(h.TwoFARegister, `{"password":"pass"}`, user)
	require.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	secret := resp["secret"].(string)

	// 現在時刻で有効な TOTP を生成
	token, err := totp.GenerateCode(secret, time.Now())
	require.NoError(t, err)

	// done で有効化。新しい挙動: 200 + body にバックアップコードを含む。
	rec2 := postExtra(h.TwoFADone, `{"token":"`+token+`"}`, user)
	require.Equal(t, http.StatusOK, rec2.Code)
	var doneResp map[string]any
	require.NoError(t, json.Unmarshal(rec2.Body.Bytes(), &doneResp))
	codes, _ := doneResp["backupCodes"].([]any)
	require.Len(t, codes, 5, "TwoFADone should return 5 backup codes")

	profile := repo.Profiles["u1"]
	assert.True(t, profile.TwoFactorEnabled)
	require.NotNil(t, profile.TwoFactorSecret)
	assert.Equal(t, secret, *profile.TwoFactorSecret)
	assert.Nil(t, profile.TwoFactorTempSecret)
	assert.Len(t, profile.TwoFactorBackupSecret, 5)
}

func TestTwoFADone_NoTempSecret(t *testing.T) {
	h, repo := newExtraHandler(t)
	user := setupUserWithPassword(repo, "u1", "pass")
	// register を飛ばして done を呼ぶ
	rec := postExtra(h.TwoFADone, `{"token":"123456"}`, user)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	_ = repo
}

func TestTwoFADone_InvalidToken(t *testing.T) {
	h, repo := newExtraHandler(t)
	user := setupUserWithPassword(repo, "u1", "pass")
	// tempSecret を手動でセット
	tempSecret := "JBSWY3DPEHPK3PXP"
	repo.Profiles["u1"].TwoFactorTempSecret = &tempSecret

	rec := postExtra(h.TwoFADone, `{"token":"000000"}`, user)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestTwoFAUnregister_Success(t *testing.T) {
	h, repo := newExtraHandler(t)
	user := setupUserWithPassword(repo, "u1", "pass")
	// 事前に 2FA を有効化状態にする
	secret := "JBSWY3DPEHPK3PXP"
	repo.Profiles["u1"].TwoFactorSecret = &secret
	repo.Profiles["u1"].TwoFactorEnabled = true

	rec := postExtra(h.TwoFAUnregister, `{"password":"pass"}`, user)
	require.Equal(t, http.StatusNoContent, rec.Code)

	profile := repo.Profiles["u1"]
	assert.False(t, profile.TwoFactorEnabled)
	assert.Nil(t, profile.TwoFactorSecret)
}

func TestTwoFAUnregister_WrongPassword(t *testing.T) {
	h, repo := newExtraHandler(t)
	user := setupUserWithPassword(repo, "u1", "correct")
	rec := postExtra(h.TwoFAUnregister, `{"password":"wrong"}`, user)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestTwoFAUnregister_NoProfile(t *testing.T) {
	h, repo := newExtraHandler(t)
	repo.Users["u1"] = &model.User{ID: "u1"}
	rec := postExtra(h.TwoFAUnregister, `{"password":"pass"}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

// --- TwoFA / WebAuthn parameter validation ---
//
// 空 body での 400 を確認するだけの軽量テスト。flow と一緒に置いて TwoFA*
// 関連の挙動が一覧で見つかるようにする。

func TestTwoFARegister_NoPassword(t *testing.T) {
	h, _ := newExtraHandler(t)
	// パスワード未指定 → BadRequest
	assert.Equal(t, http.StatusBadRequest, postExtra(h.TwoFARegister, `{}`, stubUser).Code)
}

func TestTwoFADone_NoToken(t *testing.T) {
	h, _ := newExtraHandler(t)
	// トークン未指定 → BadRequest
	assert.Equal(t, http.StatusBadRequest, postExtra(h.TwoFADone, `{}`, stubUser).Code)
}

func TestTwoFAUnregister_NoPassword(t *testing.T) {
	h, _ := newExtraHandler(t)
	// パスワード未指定 → BadRequest
	assert.Equal(t, http.StatusBadRequest, postExtra(h.TwoFAUnregister, `{}`, stubUser).Code)
}

// 5 つの WebAuthn handler は実装後はパラメータ必須なので、空 body で 400 を返す。
// password 等を渡したケースでの正常系は別 webauthn テストに追加する。
func TestTwoFARegisterKey(t *testing.T) {
	h, _ := newExtraHandler(t)
	assert.Equal(t, http.StatusBadRequest, postExtra(h.TwoFARegisterKey, `{}`, stubUser).Code)
}

func TestTwoFAKeyDone(t *testing.T) {
	h, _ := newExtraHandler(t)
	assert.Equal(t, http.StatusBadRequest, postExtra(h.TwoFAKeyDone, `{}`, stubUser).Code)
}

func TestTwoFARemoveKey(t *testing.T) {
	h, _ := newExtraHandler(t)
	assert.Equal(t, http.StatusBadRequest, postExtra(h.TwoFARemoveKey, `{}`, stubUser).Code)
}

func TestTwoFAUpdateKey(t *testing.T) {
	h, _ := newExtraHandler(t)
	assert.Equal(t, http.StatusBadRequest, postExtra(h.TwoFAUpdateKey, `{}`, stubUser).Code)
}

func TestTwoFAPasswordLess(t *testing.T) {
	h, _ := newExtraHandler(t)
	assert.Equal(t, http.StatusBadRequest, postExtra(h.TwoFAPasswordLess, `{}`, stubUser).Code)
}
