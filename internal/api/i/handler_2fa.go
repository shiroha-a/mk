package i

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/lib/pq"
	"github.com/shiroha-a/mk/internal/api/apierr"
	"github.com/shiroha-a/mk/internal/core/twofactor"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/server/middleware"
	"golang.org/x/crypto/bcrypt"
)

// wrapWebAuthnRequest builds a fresh *http.Request whose body is the
// browser-supplied attestation/assertion JSON. go-webauthn parses the body
// directly off the request, so we cannot pass through the original Echo
// request (its body has already been consumed by Bind()).
func wrapWebAuthnRequest(orig *http.Request, body json.RawMessage) (*http.Request, error) {
	req, err := http.NewRequestWithContext(orig.Context(), http.MethodPost, orig.URL.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header = orig.Header.Clone()
	req.Header.Set("Content-Type", "application/json")
	req.Body = io.NopCloser(bytes.NewReader(body))
	return req, nil
}

// TwoFARegister handles POST /api/i/2fa/register.
// TOTP秘密鍵を生成してtempSecretに保存、QRコードURLを返す。
func (h *Handler) TwoFARegister(c echo.Context) error {
	user := middleware.GetUser(c)
	var req struct {
		Password string `json:"password"`
	}
	if err := c.Bind(&req); err != nil || req.Password == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "password is required.", "ed1d7571-a3ac-4370-899c-0dbe5e230cc8"))
	}
	profile := h.userService.GetProfile(user.ID)
	if profile == nil || profile.Password == nil {
		return c.JSON(http.StatusForbidden, apierr.Error("ACCESS_DENIED", "No password set.", "1fb7cb09-d46a-4fff-b8df-057708cce513"))
	}
	if err := bcrypt.CompareHashAndPassword([]byte(*profile.Password), []byte(req.Password)); err != nil {
		return c.JSON(http.StatusForbidden, apierr.Error("INCORRECT_PASSWORD", "Incorrect password.", "932c904e-9460-45b7-9ce6-7ed33be7eb2c"))
	}

	secret, uri, err := twofactor.GenerateSecret("Misskey", user.Username)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	// frontend (settings/2fa.qrdialog.vue) は `qr` を `<img :src=...>` で
	// 読み込むため、otpauth URI ではなく PNG data URL に変換する必要がある
	// (#697)。Misskey TS upstream の QRCode.toDataURL(url) と同形式。
	qrDataURL, err := twofactor.QRDataURL(uri)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}

	// tempSecretに保存 (doneで確認後にsecretに移動)
	_ = h.userService.UpdateProfileFields(user.ID, map[string]any{"twoFactorTempSecret": secret})

	return c.JSON(http.StatusOK, map[string]any{
		"qr":     qrDataURL,
		"url":    uri,
		"secret": secret,
		"label":  user.Username,
		"issuer": "Misskey",
	})
}

// TwoFADone handles POST /api/i/2fa/done.
// TOTPコードを検証し、2FAを有効化する。同時にバックアップコードを生成して
// 一度だけレスポンスに含めて返す (ユーザーが控えておかないと二度と見られない)。
func (h *Handler) TwoFADone(c echo.Context) error {
	user := middleware.GetUser(c)
	var req struct {
		Token string `json:"token"`
	}
	if err := c.Bind(&req); err != nil || req.Token == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "token is required.", "ed1d7571-a3ac-4370-899c-0dbe5e230cc8"))
	}

	profile := h.userService.GetProfile(user.ID)
	if profile == nil || profile.TwoFactorTempSecret == nil {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "2FA registration not started.", "ed1d7571-a3ac-4370-899c-0dbe5e230cc8"))
	}

	if !twofactor.Validate(req.Token, *profile.TwoFactorTempSecret) {
		return c.JSON(http.StatusForbidden, apierr.Error("INVALID_TOKEN", "Invalid token.", "00000000-0000-0000-0000-000000000000"))
	}

	// バックアップコードを生成。crypto/rand 失敗は実質起きないが、起きた場合は
	// 2FA セットアップ自体を中断して 500 を返す。
	backupCodes, err := twofactor.GenerateBackupCodes()
	if err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Failed to generate backup codes.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}

	// tempSecretをsecretに移動、2FAを有効化、backup codes を保存
	_ = h.userService.UpdateProfileFields(user.ID, map[string]any{
		"twoFactorSecret":       *profile.TwoFactorTempSecret,
		"twoFactorTempSecret":   nil,
		"twoFactorEnabled":      true,
		"twoFactorBackupSecret": pq.StringArray(backupCodes),
	})

	return c.JSON(http.StatusOK, map[string]any{
		"backupCodes": backupCodes,
	})
}

// TwoFAUnregister handles POST /api/i/2fa/unregister.
// 2FAを無効化する。
func (h *Handler) TwoFAUnregister(c echo.Context) error {
	user := middleware.GetUser(c)
	var req struct {
		Password string `json:"password"`
	}
	if err := c.Bind(&req); err != nil || req.Password == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "password is required.", "ed1d7571-a3ac-4370-899c-0dbe5e230cc8"))
	}
	profile := h.userService.GetProfile(user.ID)
	if profile == nil || profile.Password == nil {
		return c.JSON(http.StatusForbidden, apierr.Error("ACCESS_DENIED", "No password set.", "1fb7cb09-d46a-4fff-b8df-057708cce513"))
	}
	if err := bcrypt.CompareHashAndPassword([]byte(*profile.Password), []byte(req.Password)); err != nil {
		return c.JSON(http.StatusForbidden, apierr.Error("INCORRECT_PASSWORD", "Incorrect password.", "932c904e-9460-45b7-9ce6-7ed33be7eb2c"))
	}

	_ = h.userService.UpdateProfileFields(user.ID, map[string]any{
		"twoFactorSecret":       nil,
		"twoFactorEnabled":      false,
		"twoFactorBackupSecret": pq.StringArray(nil),
	})

	return c.NoContent(http.StatusNoContent)
}

// --- WebAuthn handlers ---
//
// 共通プリチェック: パスワード再確認 + WebAuthn 依存性チェック。
// すべてのキー管理エンドポイントは現在のパスワードを要求する (本家 Misskey と
// 同じく直近の意図確認のため)。WebAuthn 依存性が未注入なら 503 を返す。
//
// 第 3 戻り値は「呼び出し側が処理を続行してよいか」のフラグ。false のとき
// レスポンスは既にこの helper 内で書き込まれているので、caller はそのまま
// nil を return する。
func (h *Handler) requireWebAuthn(c echo.Context, password string) (*model.User, *model.UserProfile, bool) {
	if h.webauthnSvc == nil || h.securityKeyRepo == nil {
		_ = c.JSON(http.StatusServiceUnavailable, apierr.Error("UNAVAILABLE", "WebAuthn is not configured.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
		return nil, nil, false
	}
	user := middleware.GetUser(c)
	if user == nil {
		_ = c.JSON(http.StatusForbidden, apierr.Error("ACCESS_DENIED", "Access denied.", "1fb7cb09-d46a-4fff-b8df-057708cce513"))
		return nil, nil, false
	}
	profile := h.userService.GetProfile(user.ID)
	if profile == nil || profile.Password == nil {
		_ = c.JSON(http.StatusForbidden, apierr.Error("ACCESS_DENIED", "No password set.", "1fb7cb09-d46a-4fff-b8df-057708cce513"))
		return nil, nil, false
	}
	if err := bcrypt.CompareHashAndPassword([]byte(*profile.Password), []byte(password)); err != nil {
		_ = c.JSON(http.StatusForbidden, apierr.Error("INCORRECT_PASSWORD", "Incorrect password.", "932c904e-9460-45b7-9ce6-7ed33be7eb2c"))
		return nil, nil, false
	}
	return user, profile, true
}

// TwoFARegisterKey handles POST /api/i/2fa/register-key.
// 1 段階目: パスワード認証 + WebAuthn registration challenge を返す。
// レスポンスには Cypress / フロントエンドが finish 呼出時に必要な
// `sessionId` (mk-go 内部の Redis セッション ID) と
// `creation` (browser に渡す PublicKeyCredentialCreationOptions) が入る。
func (h *Handler) TwoFARegisterKey(c echo.Context) error {
	var req struct {
		Password string `json:"password"`
	}
	if err := c.Bind(&req); err != nil || req.Password == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "password is required.", "ed1d7571-a3ac-4370-899c-0dbe5e230cc8"))
	}
	user, _, ok := h.requireWebAuthn(c, req.Password)
	if !ok {
		return nil
	}

	existing, err := h.securityKeyRepo.ListByUser(user.ID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	creation, sessionID, err := h.webauthnSvc.BeginRegistration(c.Request().Context(), user, existing)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Failed to begin registration.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	return c.JSON(http.StatusOK, map[string]any{
		"sessionId": sessionID,
		"creation":  creation,
	})
}

// TwoFAKeyDone handles POST /api/i/2fa/key-done.
// 2 段階目: ブラウザから返ってきた attestation response を検証して
// UserSecurityKey 行を作成する。リクエストボディは `{ password, name,
// sessionId, response }` で、`response` は browser の認証器 attestation オブジェクト
// (ParsedCredentialCreationData フォーマット)。
func (h *Handler) TwoFAKeyDone(c echo.Context) error {
	var req struct {
		Password  string          `json:"password"`
		Name      string          `json:"name"`
		SessionID string          `json:"sessionId"`
		Response  json.RawMessage `json:"response"`
	}
	if err := c.Bind(&req); err != nil || req.Password == "" || req.SessionID == "" || len(req.Response) == 0 {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "password / sessionId / response are required.", "ed1d7571-a3ac-4370-899c-0dbe5e230cc8"))
	}
	if req.Name == "" {
		req.Name = "Security Key"
	}
	user, _, ok := h.requireWebAuthn(c, req.Password)
	if !ok {
		return nil
	}

	existing, err := h.securityKeyRepo.ListByUser(user.ID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}

	// go-webauthn の FinishRegistration は *http.Request からボディを読むので、
	// JSON-RPC 経由で受け取った response を新しい http.Request にラップして渡す。
	httpReq, err := wrapWebAuthnRequest(c.Request(), req.Response)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "invalid response payload.", "ed1d7571-a3ac-4370-899c-0dbe5e230cc8"))
	}
	cred, err := h.webauthnSvc.FinishRegistration(c.Request().Context(), user, existing, req.SessionID, httpReq)
	if err != nil {
		return c.JSON(http.StatusForbidden, apierr.Error("REGISTRATION_FAILED", "Failed to finish registration.", "00000000-0000-0000-0000-000000000000"))
	}

	key := twofactor.CredentialToModel(cred, user.ID, req.Name)
	if err := h.securityKeyRepo.Create(key); err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Failed to persist key.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}

	// 1 つ以上の鍵が登録されたら user_profile.securityKeysAvailable +
	// twoFactorEnabled を true にする。本家 Misskey と同じ挙動。
	_ = h.userService.UpdateProfileFields(user.ID, map[string]any{
		"securityKeysAvailable": true,
		"twoFactorEnabled":      true,
	})

	return c.JSON(http.StatusOK, map[string]any{
		"id":   key.ID,
		"name": key.Name,
	})
}

// TwoFARemoveKey handles POST /api/i/2fa/remove-key.
// 自分の鍵 1 つを削除する。残り 0 件になったら securityKeysAvailable を false にする。
func (h *Handler) TwoFARemoveKey(c echo.Context) error {
	var req struct {
		Password     string `json:"password"`
		CredentialID string `json:"credentialId"`
	}
	if err := c.Bind(&req); err != nil || req.Password == "" || req.CredentialID == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "password and credentialId are required.", "ed1d7571-a3ac-4370-899c-0dbe5e230cc8"))
	}
	user, _, ok := h.requireWebAuthn(c, req.Password)
	if !ok {
		return nil
	}

	if err := h.securityKeyRepo.Delete(req.CredentialID, user.ID); err != nil {
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_KEY", "Key not found.", "00000000-0000-0000-0000-000000000000"))
	}

	if remaining, err := h.securityKeyRepo.CountByUser(user.ID); err == nil && remaining == 0 {
		_ = h.userService.UpdateProfileFields(user.ID, map[string]any{
			"securityKeysAvailable": false,
			"usePasswordLessLogin":  false,
		})
	}
	return c.NoContent(http.StatusNoContent)
}

// TwoFAUpdateKey handles POST /api/i/2fa/update-key.
// 鍵の表示名を変更する。
func (h *Handler) TwoFAUpdateKey(c echo.Context) error {
	var req struct {
		Password     string `json:"password"`
		CredentialID string `json:"credentialId"`
		Name         string `json:"name"`
	}
	if err := c.Bind(&req); err != nil || req.Password == "" || req.CredentialID == "" || req.Name == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "password / credentialId / name are required.", "ed1d7571-a3ac-4370-899c-0dbe5e230cc8"))
	}
	user, _, ok := h.requireWebAuthn(c, req.Password)
	if !ok {
		return nil
	}
	if err := h.securityKeyRepo.UpdateName(req.CredentialID, user.ID, req.Name); err != nil {
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_KEY", "Key not found.", "00000000-0000-0000-0000-000000000000"))
	}
	return c.NoContent(http.StatusNoContent)
}

// TwoFAPasswordLess handles POST /api/i/2fa/password-less.
// `usePasswordLessLogin` フラグを切り替える。WebAuthn 鍵が登録されていないと
// 有効化できない (パスワードレスログインの前提が成立しないため)。
func (h *Handler) TwoFAPasswordLess(c echo.Context) error {
	var req struct {
		Password string `json:"password"`
		Value    bool   `json:"value"`
	}
	if err := c.Bind(&req); err != nil || req.Password == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "password is required.", "ed1d7571-a3ac-4370-899c-0dbe5e230cc8"))
	}
	user, _, ok := h.requireWebAuthn(c, req.Password)
	if !ok {
		return nil
	}
	if req.Value {
		if n, err := h.securityKeyRepo.CountByUser(user.ID); err != nil || n == 0 {
			return c.JSON(http.StatusBadRequest, apierr.Error("NO_SECURITY_KEYS", "Register at least one security key first.", "00000000-0000-0000-0000-000000000000"))
		}
	}
	_ = h.userService.UpdateProfileFields(user.ID, map[string]any{
		"usePasswordLessLogin": req.Value,
	})
	return c.NoContent(http.StatusNoContent)
}
