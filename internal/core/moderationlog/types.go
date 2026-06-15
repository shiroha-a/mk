// Package moderationlog provides a fire-and-forget audit log writer for
// administrator actions. Each LogType corresponds 1:1 to the values defined
// in upstream Misskey TS (packages/backend/src/types.ts:82-135), and the
// frontend's modlog UI (packages/frontend/src/pages/admin/modlog.ModLog.vue)
// branches on these strings to render type-specific detail panels.
//
// Only the subset that mk-go can actually write today is exposed here:
//   - the corresponding admin handler is implemented, AND
//   - the frontend has a UI branch for it (or the type is part of the TS
//     spec even if the UI falls back to the generic display).
//
// Types defined in upstream but skipped for now:
//   - clearQueue / promoteQueue: tooling-only, no UI branch
//   - markSensitiveDriveFile / unmarkSensitiveDriveFile / deleteDriveFile:
//     mk-go admin/drive write handlers are not implemented
//   - deleteNote / deletePage / deleteFlash / deleteGalleryPost / deleteChatRoom:
//     out of scope for the current modlog rollout
package moderationlog

import "github.com/shiroha-a/mk/internal/model"

// LogType is the discriminant string persisted as moderation_log.type.
// Values must match Misskey TS verbatim so the shared frontend and any
// federation tooling can interoperate.
type LogType string

const (
	LogUpdateServerSettings LogType = "updateServerSettings"

	// User moderation
	LogSuspend         LogType = "suspend"
	LogUnsuspend       LogType = "unsuspend"
	LogUpdateUserNote  LogType = "updateUserNote"
	LogResetPassword   LogType = "resetPassword"
	LogDeleteAccount   LogType = "deleteAccount"
	LogUnsetUserAvatar LogType = "unsetUserAvatar"
	LogUnsetUserBanner LogType = "unsetUserBanner"

	// Notes
	LogDeleteNote LogType = "deleteNote"

	// Roles
	LogCreateRole   LogType = "createRole"
	LogUpdateRole   LogType = "updateRole"
	LogDeleteRole   LogType = "deleteRole"
	LogAssignRole   LogType = "assignRole"
	LogUnassignRole LogType = "unassignRole"

	// Custom emoji
	LogAddCustomEmoji    LogType = "addCustomEmoji"
	LogUpdateCustomEmoji LogType = "updateCustomEmoji"
	LogDeleteCustomEmoji LogType = "deleteCustomEmoji"

	// Announcements
	LogCreateGlobalAnnouncement LogType = "createGlobalAnnouncement"
	LogCreateUserAnnouncement   LogType = "createUserAnnouncement"
	LogUpdateGlobalAnnouncement LogType = "updateGlobalAnnouncement"
	LogUpdateUserAnnouncement   LogType = "updateUserAnnouncement"
	LogDeleteGlobalAnnouncement LogType = "deleteGlobalAnnouncement"
	LogDeleteUserAnnouncement   LogType = "deleteUserAnnouncement"

	// Ads / invitations
	LogCreateAd         LogType = "createAd"
	LogUpdateAd         LogType = "updateAd"
	LogDeleteAd         LogType = "deleteAd"
	LogCreateInvitation LogType = "createInvitation"

	// Remote instance moderation
	LogSuspendRemoteInstance    LogType = "suspendRemoteInstance"
	LogUnsuspendRemoteInstance  LogType = "unsuspendRemoteInstance"
	LogUpdateRemoteInstanceNote LogType = "updateRemoteInstanceNote"

	// Abuse reports
	LogResolveAbuseReport    LogType = "resolveAbuseReport"
	LogForwardAbuseReport    LogType = "forwardAbuseReport"
	LogUpdateAbuseReportNote LogType = "updateAbuseReportNote"

	// Avatar decorations
	LogCreateAvatarDecoration LogType = "createAvatarDecoration"
	LogUpdateAvatarDecoration LogType = "updateAvatarDecoration"
	LogDeleteAvatarDecoration LogType = "deleteAvatarDecoration"

	// System webhooks
	LogCreateSystemWebhook LogType = "createSystemWebhook"
	LogUpdateSystemWebhook LogType = "updateSystemWebhook"
	LogDeleteSystemWebhook LogType = "deleteSystemWebhook"

	// Abuse report notification recipients
	LogCreateAbuseReportNotificationRecipient LogType = "createAbuseReportNotificationRecipient"
	LogUpdateAbuseReportNotificationRecipient LogType = "updateAbuseReportNotificationRecipient"
	LogDeleteAbuseReportNotificationRecipient LogType = "deleteAbuseReportNotificationRecipient"

	// Content deletion by moderators
	LogDeleteGalleryPost LogType = "deleteGalleryPost"
	LogDeleteFlash       LogType = "deleteFlash"
	LogDeleteChatRoom    LogType = "deleteChatRoom"

	// Misc
	LogUpdateProxyAccountDescription LogType = "updateProxyAccountDescription"
)

// UserInfo builds the standard {userId, userUsername, userHost} info
// payload used by user-targeted moderation log entries (suspend,
// updateUserNote, resetPassword, deleteAccount, ...). The Misskey TS
// frontend reads these keys verbatim — do not rename. nil-safe so
// callers can hand it the result of a possibly-failing lookup without
// pre-checking.
func UserInfo(u *model.User) map[string]any {
	if u == nil {
		return map[string]any{}
	}
	return map[string]any{
		"userId":       u.ID,
		"userUsername": u.Username,
		"userHost":     u.Host,
	}
}
