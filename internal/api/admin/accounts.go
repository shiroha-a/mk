package admin

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/api/apierr"
	"github.com/shiroha-a/mk/internal/core/moderationlog"
	"github.com/shiroha-a/mk/internal/entity"
	"github.com/shiroha-a/mk/internal/queue"
)

// AccountsDelete handles POST /api/admin/accounts/delete.
func (h *Handler) AccountsDelete(c echo.Context) error {
	var req struct {
		UserID string `json:"userId"`
	}
	if err := c.Bind(&req); err != nil || req.UserID == "" {
		return c.NoContent(http.StatusNoContent)
	}
	// root / system アカウントの削除は連合を壊すため拒否する (#parity review F1)。
	if h.isProtectedAccount(req.UserID) {
		return c.JSON(http.StatusForbidden, apierr.Error("ACCESS_DENIED", "Cannot delete a root or system account.", "1fb7cb09-d46a-4fff-b8df-057708cce513"))
	}
	if err := h.userRepo.UpdateUser(req.UserID, map[string]any{"isSuspended": true, "isDeleted": true}); err == nil {
		// 論理削除直後の auth bypass 防止 (#965)。target の全 token cache
		// entry を即時 invalidate して 30s stale window を消す。DB 更新が
		// 失敗したケースでは cache を触る理由がないので、err 成功時のみ。
		h.invalidateUserTokenCache(req.UserID)
		h.logUserAction(c, moderationlog.LogDeleteAccount, req.UserID)
	}
	h.scheduleAccountCascade(req.UserID)
	return c.NoContent(http.StatusNoContent)
}

// AccountsFindByEmail handles POST /api/admin/accounts/find-by-email.
// user_profile.email 列を検索して、紐づく user を返す。本家 Misskey の
// admin/accounts/find-by-email と同等。
func (h *Handler) AccountsFindByEmail(c echo.Context) error {
	var req struct {
		Email string `json:"email"`
	}
	if err := c.Bind(&req); err != nil || req.Email == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "email is required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	profile, err := h.userRepo.FindProfileByEmail(req.Email)
	if err != nil {
		return c.JSON(http.StatusNotFound, apierr.Error("USER_NOT_FOUND", "User not found.", "cb865949-8af5-4062-a88c-ef55e8786d1d"))
	}
	user, err := h.userRepo.FindByID(profile.UserID)
	if err != nil {
		return c.JSON(http.StatusNotFound, apierr.Error("USER_NOT_FOUND", "User not found.", "cb865949-8af5-4062-a88c-ef55e8786d1d"))
	}
	// upstream admin/accounts/find-by-email.ts は pack(user, null,
	// {schema:'UserDetailedNotMe'}) (includeSecrets 無し) を返すため email /
	// emailVerified / securityKeysList は含めない。packAdminUser を使うとこれら
	// includeSecrets 限定 field が漏れる (#1847、#1822 と同 class)。UserDetailed に
	// 揃えて過剰露出を防ぐ (ShowUsers と同方針)。生 model.User の内部 field
	// (inbox/sharedInbox/usernameLower) も UserDetailed では出ない。
	return c.JSON(http.StatusOK, entity.PackUserDetailed(user, profile, h.idGen))
}

// DeleteAccount handles POST /api/admin/delete-account. AccountsDelete と
// 機能的には同じだが、本家 Misskey が両 endpoint を持つので互換性のため
// 別 handler として保持する。
func (h *Handler) DeleteAccount(c echo.Context) error {
	var req struct {
		UserID string `json:"userId"`
	}
	if err := c.Bind(&req); err != nil || req.UserID == "" {
		return c.NoContent(http.StatusNoContent)
	}
	// root / system アカウントの削除は連合を壊すため拒否する (#parity review F1)。
	if h.isProtectedAccount(req.UserID) {
		return c.JSON(http.StatusForbidden, apierr.Error("ACCESS_DENIED", "Cannot delete a root or system account.", "1fb7cb09-d46a-4fff-b8df-057708cce513"))
	}
	// AP Delete(actor) 配信のため、更新前に user を控える (#1759)。
	user, _ := h.userRepo.FindByID(req.UserID)
	if err := h.userRepo.UpdateUser(req.UserID, map[string]any{"isSuspended": true, "isDeleted": true}); err == nil {
		// AccountsDelete と同じ。target の全 token cache entry を即時
		// invalidate (#965)。
		h.invalidateUserTokenCache(req.UserID)
		h.logUserAction(c, moderationlog.LogDeleteAccount, req.UserID)
	}
	// upstream DeleteAccountService: local user は物理削除 job の前に全 sharedInbox
	// へ Delete(actor) を配信する (#1759)。mk-go の cascade は soft (user 行/鍵を
	// 残す) ため queue 経由配信でも署名鍵は生存する。
	if user != nil && h.userModerationFed != nil {
		h.userModerationFed.OnUserDeleted(user)
	}
	h.scheduleAccountCascade(req.UserID)
	return c.NoContent(http.StatusNoContent)
}

// isProtectedAccount reports whether the given user ID is an account that must
// never be deleted: the instance root account or a local system account
// (instance.actor / relay.actor / proxy.actor 等)。upstream DeleteAccountService
// の `meta.rootUserId === user.id` (root) と `user.host === null &&
// username.includes('.')` (system account) ガードに対応する (#parity review F1)。
// Returns false when the user cannot be resolved.
func (h *Handler) isProtectedAccount(userID string) bool {
	if h.userRepo == nil || userID == "" {
		return false
	}
	// root user id は meta が権威ソース (role service の isRootUser と揃える)。
	if h.metaRepo != nil {
		if meta, err := h.metaRepo.Fetch(); err == nil && meta != nil && meta.RootUserID != nil && *meta.RootUserID == userID {
			return true
		}
	}
	u, err := h.userRepo.FindByID(userID)
	if err != nil || u == nil {
		return false
	}
	if u.IsRoot {
		return true
	}
	// ローカル system account: host=null かつ username に '.' を含む
	// (systemaccount は `<kind>.actor` 形式で作られる)。
	if u.Host == nil && strings.Contains(u.Username, ".") {
		return true
	}
	return false
}

// scheduleAccountCascade queues the background cascade deletion. Errors
// from the enqueuer are logged but never surfaced — the admin flag flip
// is the user-visible source of truth, so a failed enqueue only delays
// the cleanup until the next manual retry.
func (h *Handler) scheduleAccountCascade(userID string) {
	if h.deleteAccountEnqueuer == nil || userID == "" {
		return
	}
	if err := h.deleteAccountEnqueuer.EnqueueDeleteAccount(queue.DeleteAccountPayload{UserID: userID}); err != nil {
		slog.Warn("admin: enqueue delete-account failed", "userId", userID, "err", err)
	}
}
