package signin

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/model"
)

// SigninWithPasskey handles POST /api/signin-with-passkey.
//
// Misskey TS upstream の SigninWithPasskeyApiService と互換 (#705)。
// 2 段階呼び出し:
//
//  1. 空 body (or credential 無し): { option, context } を返す。option は
//     PublicKeyCredentialRequestOptionsJSON、context はサーバ側で challenge と
//     結びつける一時 ID (UUID 相当)。
//  2. { credential, context }: 1 で受け取った context を round-trip し、ブラウザ
//     から返ってきた assertion を検証する。成功すると `usePasswordLessLogin` が
//     有効なユーザに対し signinResponse: { finished, id, i } を返す。
func (h *Handler) SigninWithPasskey(c echo.Context) error {
	var req struct {
		Credential json.RawMessage `json:"credential"`
		Context    string          `json:"context"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, errBody("ed1d7571-a3ac-4370-899c-0dbe5e230cc8"))
	}

	if h.webauthnSvc == nil {
		return c.JSON(http.StatusServiceUnavailable, errBody("5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}

	// Step 1: credential が無ければ challenge を発行する。
	if len(req.Credential) == 0 {
		ctxID, err := newPasskeyContext()
		if err != nil {
			return c.JSON(http.StatusInternalServerError, errBody("5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
		}
		assertion, err := h.webauthnSvc.BeginPasskeyLogin(c.Request().Context(), ctxID)
		if err != nil {
			return c.JSON(http.StatusInternalServerError, errBody("5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
		}
		// upstream は { option: PublicKeyCredentialRequestOptionsJSON, context }。
		// go-webauthn の CredentialAssertion は `{publicKey: ...}` で 1 段ラップ
		// されているので Response (= 内側 PublicKeyCredentialRequestOptions) を返す。
		return c.JSON(http.StatusOK, map[string]any{
			"option":  assertion.Response,
			"context": ctxID,
		})
	}

	// Step 2: credential 検証。
	if req.Context == "" {
		return c.JSON(http.StatusBadRequest, errBody("1658cc2e-4495-461f-aee4-d403cdf073c1"))
	}

	httpReq, err := wrapWebAuthnRequest(c.Request(), req.Credential)
	if err != nil {
		return c.JSON(http.StatusBadRequest, errBody("ed1d7571-a3ac-4370-899c-0dbe5e230cc8"))
	}

	user, cred, err := h.webauthnSvc.FinishPasskeyLogin(c.Request().Context(), req.Context, httpReq, h.resolvePasskeyUser)
	if err != nil {
		// frontend には汎用 403 を返すが backend log には何で落ちたかを残す
		// (#707 の調査用)。原因例: challenge mismatch / origin / RPID 不一致 /
		// credential format problem / user_handle 解決失敗。
		slog.Warn("signin: webauthn FinishPasskeyLogin failed", "context", req.Context, "err", err)
		return c.JSON(http.StatusForbidden, errBody("b18c89a7-5b5e-4cec-bb5b-0419f332d430"))
	}
	return h.finishPasskeySignin(c, user, cred)
}

// resolvePasskeyUser is the PasskeyUserResolver passed into
// twofactor.FinishPasskeyLogin. クロージャを named method として切り出してある
// のは、resolver の error 伝搬経路を unit test から直接 exercise しやすくする
// ため。
func (h *Handler) resolvePasskeyUser(_, userHandle []byte) (*model.User, []*model.UserSecurityKey, error) {
	userID := string(userHandle)
	u, ferr := h.userRepo.FindByID(userID)
	if ferr != nil {
		return nil, nil, ferr
	}
	if h.securityKeyRepo == nil {
		return u, nil, nil
	}
	keys, kerr := h.securityKeyRepo.ListByUser(userID)
	if kerr != nil {
		return nil, nil, kerr
	}
	return u, keys, nil
}

// finishPasskeySignin handles the post-verify branch of /api/signin-with-passkey:
// suspended check, passwordless flag check, counter update, and response shape.
// 切り出してあるのは webauthn signature の verify success path をユニットテスト
// しやすくするため。
func (h *Handler) finishPasskeySignin(c echo.Context, user *model.User, cred *webauthn.Credential) error {
	if user == nil {
		return c.JSON(http.StatusForbidden, errBody("932c904e-9460-45b7-9ce6-7ed33be7eb2c"))
	}
	if user.IsSuspended {
		return c.JSON(http.StatusForbidden, errBody("e03a5f46-d309-4865-9b69-56282d94e1eb"))
	}

	profile, err := h.userRepo.FindProfileByUserID(user.ID)
	if err != nil || profile == nil {
		return c.JSON(http.StatusForbidden, errBody("932c904e-9460-45b7-9ce6-7ed33be7eb2c"))
	}
	// passwordless login が有効化されていなければ拒否する。
	// upstream の SigninWithPasskeyApiService と同じ ID を返す。
	if !profile.UsePasswordLessLogin {
		return c.JSON(http.StatusForbidden, errBody("2d84773e-f7b7-4d0b-8f72-bb69b584c912"))
	}

	// counter 更新 (clone 検出)
	if h.securityKeyRepo != nil && cred != nil {
		_ = h.securityKeyRepo.UpdateCounter(encodeCredID(cred.ID), int64(cred.Authenticator.SignCount))
	}

	// upstream は `{ signinResponse: { finished: true, id, i } }` を返す。
	signinResp := h.okBody(user)
	if h.ipLoggingOn && h.ipLogger != nil {
		go h.ipLogger.Upsert(user.ID, c.RealIP())
	}
	if h.signinRepo != nil && h.idGen != nil {
		hdrs := c.Request().Header.Clone()
		go h.recordSignin(user.ID, c.RealIP(), hdrs)
	}
	return c.JSON(http.StatusOK, map[string]any{
		"signinResponse": signinResp,
	})
}

// newPasskeyContext returns a 32-character random hex string used as the
// passkey-challenge context. UUID v4 でも互換性は崩れないが、依存を増やさない
// ため crypto/rand から hex を生成する。
//
// `readRandom` は変数経由でテストから差し替え可能にし、entropy 枯渇時のエラー
// path をユニットテストできるようにする (twofactor.readRandom と同じ seam パターン)。
func newPasskeyContext() (string, error) {
	buf := make([]byte, 16)
	if _, err := readRandom(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// readRandom is the indirection point for crypto/rand.Read. tests swap this
// to exercise rand-failure paths. test-only swapper は export_test.go に置く
// (production binary には test seam を export しない)。
var readRandom = rand.Read

// okBody returns the same shape as Handler.ok but without writing the response.
// Used by SigninWithPasskey which must wrap the body in `{signinResponse: ...}`.
func (h *Handler) okBody(user *model.User) map[string]any {
	token := ""
	if user.Token != nil {
		token = *user.Token
	}
	return map[string]any{
		"finished": true,
		"id":       user.ID,
		"i":        token,
	}
}
