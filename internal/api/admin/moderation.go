package admin

import (
	"log/slog"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/api/apierr"
	"github.com/shiroha-a/mk/internal/core/moderationlog"
)

// UnsetUserAvatar handles POST /api/admin/unset-user-avatar.
func (h *Handler) UnsetUserAvatar(c echo.Context) error {
	var req struct {
		UserID string `json:"userId"`
	}
	if err := c.Bind(&req); err != nil || req.UserID == "" {
		return c.NoContent(http.StatusNoContent)
	}
	if err := h.userRepo.UpdateUser(req.UserID, map[string]any{"avatarId": nil, "avatarUrl": nil, "avatarBlurhash": nil}); err == nil {
		h.logUserAction(c, moderationlog.LogUnsetUserAvatar, req.UserID)
	}
	return c.NoContent(http.StatusNoContent)
}

// UnsetUserBanner handles POST /api/admin/unset-user-banner.
func (h *Handler) UnsetUserBanner(c echo.Context) error {
	var req struct {
		UserID string `json:"userId"`
	}
	if err := c.Bind(&req); err != nil || req.UserID == "" {
		return c.NoContent(http.StatusNoContent)
	}
	if err := h.userRepo.UpdateUser(req.UserID, map[string]any{"bannerId": nil, "bannerUrl": nil, "bannerBlurhash": nil}); err == nil {
		h.logUserAction(c, moderationlog.LogUnsetUserBanner, req.UserID)
	}
	return c.NoContent(http.StatusNoContent)
}

// UpdateUserNote handles POST /api/admin/update-user-note. user_profile の
// moderationNote 列を更新する。
func (h *Handler) UpdateUserNote(c echo.Context) error {
	var req struct {
		UserID string `json:"userId"`
		Text   string `json:"text"`
	}
	if err := c.Bind(&req); err != nil || req.UserID == "" {
		return c.NoContent(http.StatusNoContent)
	}
	// before を log の info に含めるため UpdateProfile の前に取得する。
	// 取得失敗時は before 不明扱い (空文字) で log を書く方が監査価値が高い。
	var before string
	if profile, err := h.userRepo.FindProfileByUserID(req.UserID); err == nil && profile != nil && profile.ModerationNote != nil {
		before = *profile.ModerationNote
	}
	if err := h.userRepo.UpdateProfile(req.UserID, map[string]any{"moderationNote": req.Text}); err != nil {
		return c.NoContent(http.StatusNoContent)
	}
	target, err := h.userRepo.FindByID(req.UserID)
	if err != nil || target == nil {
		// UpdateProfile が成功した直後に user lookup が失敗するのは
		// 並行削除等のレアケース。debug-level に残して早期 return する。
		slog.DebugContext(c.Request().Context(), "moderation log: target user not found after UpdateUserNote",
			"userId", req.UserID)
		return c.NoContent(http.StatusNoContent)
	}
	info := moderationlog.UserInfo(target)
	info["before"] = before
	info["after"] = req.Text
	h.logModeration(c, moderationlog.LogUpdateUserNote, info)
	return c.NoContent(http.StatusNoContent)
}

// UpdateAbuseUserReport handles POST /api/admin/update-abuse-user-report.
//
// upstream AbuseReportService.update は `moderationNote` 列のみを更新し、
// resolved は触らない (resolve は resolve-abuse-user-report 側の責務)。旧
// mk-go は moderationNote param を無視して resolved=true を立て、log type も
// resolveAbuseReport にしていた (= resolve-abuse-user-report との取り違え)。
// 本 fix で moderationNote を受け取り、変化時のみ updateAbuseReportNote を
// log する (upstream update-abuse-user-report.ts / AbuseReportService.ts:154)。
func (h *Handler) UpdateAbuseUserReport(c echo.Context) error {
	var req struct {
		ReportID       string  `json:"reportId"`
		ModerationNote *string `json:"moderationNote"`
	}
	if err := c.Bind(&req); err != nil || req.ReportID == "" {
		return c.JSON(http.StatusBadRequest, apierr.InvalidParam("reportId is required."))
	}
	if h.abuseRepo == nil {
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_ABUSE_REPORT", "No such abuse report.", "15f51cf5-46d1-4b1d-a618-b35bcbed0662"))
	}
	before, err := h.abuseRepo.FindByID(req.ReportID)
	if err != nil || before == nil {
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_ABUSE_REPORT", "No such abuse report.", "15f51cf5-46d1-4b1d-a618-b35bcbed0662"))
	}
	// upstream は moderationNote が undefined のとき列を触らない (TypeORM が
	// undefined field を skip)。mk-go も moderationNote 未送出時は no-op。
	if req.ModerationNote == nil {
		return c.NoContent(http.StatusNoContent)
	}
	note := *req.ModerationNote
	// 更新前の値を控える (UpdateFields 後に before を参照すると、in-memory repo
	// 実装では同一ポインタが書き換わって比較が壊れる)。
	beforeNote := before.ModerationNote
	if err := h.abuseRepo.UpdateFields(req.ReportID, map[string]any{"moderationNote": note}); err != nil {
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_ABUSE_REPORT", "No such abuse report.", "15f51cf5-46d1-4b1d-a618-b35bcbed0662"))
	}
	// 変化があったときのみ updateAbuseReportNote を記録 (upstream と同じ)。
	if beforeNote != note {
		h.logModeration(c, moderationlog.LogUpdateAbuseReportNote, map[string]any{
			"reportId": req.ReportID,
			"report":   before,
			"before":   beforeNote,
			"after":    note,
		})
	}
	return c.NoContent(http.StatusNoContent)
}

// DeleteAllFilesOfUser handles POST /api/admin/delete-all-files-of-a-user.
// driveFile レコードを user 単位で一括 DELETE する。S3 等 object storage の
// 物理 file 削除は drive_file の deletion hook (別経路) で扱う想定。
func (h *Handler) DeleteAllFilesOfUser(c echo.Context) error {
	var req struct {
		UserID string `json:"userId"`
	}
	if err := c.Bind(&req); err != nil || req.UserID == "" {
		return c.JSON(http.StatusBadRequest, apierr.InvalidParam("userId is required."))
	}
	if h.driveFileRepo == nil {
		return c.NoContent(http.StatusNoContent)
	}
	// 単一の DELETE 文で完結するため同期実行。大量ファイル (数万) の場合も
	// PostgreSQL で 1 秒未満に収まる想定。将来バッチが必要なら queue へ。
	if _, err := h.driveFileRepo.DeleteByUser(req.UserID); err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.InternalError())
	}
	return c.NoContent(http.StatusNoContent)
}
