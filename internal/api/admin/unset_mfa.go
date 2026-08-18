package admin

import (
	"log/slog"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/api/apierr"
	"github.com/shiroha-a/mk/internal/core/moderationlog"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/server/middleware"
)

// UnsetMfa handles POST /api/admin/unset-mfa (upstream 2026.7.0 #17614).
//
// Removes every registered security key (passkey) of the target user and
// disables TOTP / password-less login. Targets that are administrators can
// only be reset by themselves (ACCESS_DENIED otherwise), mirroring the
// upstream fork-merge authorization fix.
func (h *Handler) UnsetMfa(c echo.Context) error {
	var req struct {
		UserID string `json:"userId"`
	}
	if err := c.Bind(&req); err != nil || req.UserID == "" {
		return c.JSON(http.StatusBadRequest, apierr.InvalidParam())
	}
	if h.userRepo == nil {
		return c.JSON(http.StatusInternalServerError, apierr.InternalError())
	}
	user, err := h.userRepo.FindByID(req.UserID)
	if err != nil || user == nil {
		return c.JSON(http.StatusBadRequest, apierr.Error("NO_SUCH_USER", "No such user.", "ccafc7fe-5074-4edd-9dc0-8ef9ef6a701d"))
	}
	// reset-password と同型の administrator 保護 (upstream: 対象が admin かつ
	// 実行者 != 対象なら ACCESS_DENIED)。
	if h.roleService != nil {
		me := middleware.GetUser(c)
		if h.roleService.IsAdministrator(user.ID) && (me == nil || me.ID != user.ID) {
			return c.JSON(http.StatusBadRequest, apierr.Error("ACCESS_DENIED", "Access denied.", "cda8f8ce-89a6-4f92-8055-33bbe0c1464d"))
		}
	}

	// upstream は 1 トランザクションで key 削除 + profile 更新を行う。mk-go は
	// repository 境界を保つため順次実行だが、**パスキー削除を先に行う**。
	// 逆順だと部分失敗時に「2FA 無効 + key 行残存」が残り、被害者が後で TOTP を
	// 再有効化した瞬間に攻撃者のパスキーが復活する (インシデント対応 endpoint
	// としては許容できない)。repo 未配線も同じ理由で成功扱いにせず 500。
	if h.securityKeyRepo == nil {
		slog.Error("admin/unset-mfa: security key repository is not wired")
		return c.JSON(http.StatusInternalServerError, apierr.InternalError())
	}
	if err := h.securityKeyRepo.DeleteByUser(user.ID); err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.InternalError())
	}
	// securityKeysAvailable も false にする。upstream はこの列を書かないが、
	// `securityKeys` を毎回 count クエリで算出する (UserEntityService) ため
	// 列が陳腐化しない。mk-go は列をキャッシュとして読む
	// (entity/user.go の SecurityKeys) ので、全鍵削除に合わせて更新しないと
	// 「鍵 0 件なのに securityKeys=true」になる (被害者が後で TOTP を再有効化
	// した時に露見する)。i/2fa/remove-key の 0 件時挙動とも揃う。
	if err := h.userRepo.UpdateProfile(user.ID, map[string]any{
		"twoFactorSecret":       nil,
		"twoFactorBackupSecret": model.StringArray(nil),
		"twoFactorEnabled":      false,
		"usePasswordLessLogin":  false,
		"securityKeysAvailable": false,
	}); err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.InternalError())
	}

	h.logUserActionWithUser(c, moderationlog.LogUnsetMfa, user)
	return c.NoContent(http.StatusNoContent)
}
