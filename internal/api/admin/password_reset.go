package admin

import (
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/api/apierr"
	"github.com/shiroha-a/mk/internal/core/moderationlog"
	"github.com/shiroha-a/mk/internal/misc"
	"golang.org/x/crypto/bcrypt"
)

// ResetPassword handles POST /api/admin/reset-password.
//
// upstream reset-password.ts は常に secureRndstr(8) で 8 文字の英数字パスワードを
// 生成し、その場で対象ユーザーの password を更新して {"password": "..."} を返す
// (res schema は minLength/maxLength=8)。標準管理 UI (admin-user.vue) は返却を
// `const {password} = ...` で分割代入して表示するため、mk-go もこれに揃える (#1825)。
//
// 旧 #186 の verified-email 経路 ({"sent": true} を返し reset link を送る) は標準 UI で
// password が undefined になり drop-in を壊すため撤去した。ユーザー自身が行う
// パスワードリセット (/request-reset-password → /reset-password) は別経路で存続する。
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
	// upstream secureRndstr(8) = 英数字 8 文字。
	newPass := misc.SecureRandomString(8, misc.AlphanumericChars)
	hash, err := bcrypt.GenerateFromPassword([]byte(newPass), bcrypt.DefaultCost)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.InternalError())
	}
	if h.userRepo != nil {
		if err := h.userRepo.UpdateProfile(req.UserID, map[string]any{"password": string(hash)}); err != nil {
			return c.JSON(http.StatusInternalServerError, apierr.InternalError())
		}
	}
	h.logUserAction(c, moderationlog.LogResetPassword, req.UserID)
	return c.JSON(http.StatusOK, map[string]any{"password": newPass})
}
