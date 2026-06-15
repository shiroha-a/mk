package admin

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/api/apierr"
	"github.com/shiroha-a/mk/internal/core/moderationlog"
	"github.com/shiroha-a/mk/internal/misc"
	"github.com/shiroha-a/mk/internal/model"
	"golang.org/x/crypto/bcrypt"
)

// ResetPassword handles POST /api/admin/reset-password.
//
// 優先: 対象ユーザーに verified email があれば reset token を発行してメール
// リンクを送り、HTTP 200 で {"sent": true} を返す (ユーザーが自分で新パス
// ワードを設定する本家互換フロー #186)。
//
// Fallback: email が verify されていない、repo / sender が未配線、
// etc. の場合は従来どおりランダムなテンポラリパスワードを生成してその場で
// 更新し、{"password": "..."} を返す。旧管理 UI との互換を保つ最後の砦。
func (h *Handler) ResetPassword(c echo.Context) error {
	var req struct {
		UserID string `json:"userId"`
	}
	if err := c.Bind(&req); err != nil || req.UserID == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "userId is required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	// upstream reset-password.ts は rootUserId===target で 'cannot reset password
	// of root' を投げる。mk-go は他の admin guard (suspend-user) と揃えて root を
	// ACCESS_DENIED で弾く (#1539)。userRepo 未配線時は guard を skip。
	if h.userRepo != nil {
		if user, err := h.userRepo.FindByID(req.UserID); err == nil && user.IsRoot {
			return c.JSON(http.StatusForbidden, apierr.Error("ACCESS_DENIED", "Cannot reset the password of a root account.", "1fb7cb09-d46a-4fff-b8df-057708cce513"))
		}
	}
	if sent := h.sendPasswordResetEmail(req.UserID); sent {
		h.logUserAction(c, moderationlog.LogResetPassword, req.UserID)
		return c.JSON(http.StatusOK, map[string]any{"sent": true})
	}
	return h.issueTemporaryPassword(c, req.UserID)
}

// sendPasswordResetEmail attempts the token+email flow. Returns true only
// when a reset token was persisted and the email handed off to the sender.
// Any missing dependency / unverified email triggers false for the caller
// to pick up the legacy path.
func (h *Handler) sendPasswordResetEmail(userID string) bool {
	if h.resetReqRepo == nil || h.emailSender == nil || h.userRepo == nil {
		return false
	}
	profile, err := h.userRepo.FindProfileByUserID(userID)
	if err != nil || profile.Email == nil || *profile.Email == "" || !profile.EmailVerified {
		return false
	}
	token := misc.SecureRandomHex(64)
	now := time.Now()
	resetReq := &model.PasswordResetRequest{
		ID:     h.idGen.Generate(now),
		Token:  token,
		UserID: userID,
	}
	if err := h.resetReqRepo.Create(resetReq); err != nil {
		return false
	}
	link := h.serverURL + "/reset-password/" + token
	go h.emailSender(*profile.Email, "Password reset",
		"An administrator initiated a password reset for your account.\n"+
			"Use the following link within 30 minutes to set a new password:\n"+link)
	return true
}

// issueTemporaryPassword is the legacy path retained for installations where
// email is not configured or the user has no verified address. Generates a
// random password, updates the profile, and returns it to the admin.
func (h *Handler) issueTemporaryPassword(c echo.Context, userID string) error {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	newPass := hex.EncodeToString(b)
	hash, _ := bcrypt.GenerateFromPassword([]byte(newPass), bcrypt.DefaultCost)
	if h.userRepo != nil {
		if err := h.userRepo.UpdateProfile(userID, map[string]any{"password": string(hash)}); err == nil {
			h.logUserAction(c, moderationlog.LogResetPassword, userID)
		}
	}
	return c.JSON(http.StatusOK, map[string]any{"password": newPass})
}
