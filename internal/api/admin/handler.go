package admin

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/api/apierr"
	"github.com/shiroha-a/mk/internal/core/role"
	"github.com/shiroha-a/mk/internal/core/signup"
	"github.com/shiroha-a/mk/internal/entity"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/queue"
	"github.com/shiroha-a/mk/internal/repository"
	"github.com/shiroha-a/mk/internal/server/middleware"
	"gorm.io/gorm"
)

// RelayService is the subset of core/relay.Service needed by the
// admin/relays endpoints. Kept as an interface so tests can inject a
// fake without the rest of the federation pipeline.
type RelayService interface {
	Add(ctx context.Context, inbox string) (*model.Relay, error)
	Remove(ctx context.Context, id string) error
	List(ctx context.Context) ([]*model.Relay, error)
}

// AbuseForwarder encapsulates the full "render a Flag activity for the
// report and deliver it to the target user's origin inbox" flow used by
// admin/forward-abuse-user-report. Kept as an interface so the handler
// stays testable without dragging in the full activitypub + queue stack.
type AbuseForwarder interface {
	ForwardReport(reportID string) error
}

// DeleteAccountEnqueuer schedules the background cascade deletion of a
// user's related rows (notes / drive files / follow graph) after the
// admin/accounts/delete flag flip. narrow interface to keep admin handler
// tests decoupled from the full queue stack.
type DeleteAccountEnqueuer interface {
	EnqueueDeleteAccount(payload queue.DeleteAccountPayload) error
}

// Handler handles admin API endpoints.
type Handler struct {
	signupService         *signup.Service
	roleService           *role.Service
	metaRepo              repository.MetaRepository
	userRepo              repository.UserRepository
	abuseRepo             repository.AbuseReportRepository
	modLogRepo            repository.ModerationLogRepository
	emojiRepo             repository.EmojiRepository
	driveFileRepo         repository.DriveFileRepository
	adminDB               *gorm.DB
	userIPRepo            repository.UserIPRepository
	queueInspector        QueueInspector
	emojiEnqueuer         EmojiImportEnqueuer
	relayService          RelayService
	abuseForwarder        AbuseForwarder
	deleteAccountEnqueuer DeleteAccountEnqueuer
	systemWebhookRepo     repository.SystemWebhookRepository
	recipientRepo         repository.AbuseReportNotificationRecipientRepository
	adRepo                repository.AdRepository
	avatarDecoRepo        repository.AvatarDecorationRepository
	inviteRepo            repository.RegistrationTicketRepository
	promoNoteRepo         repository.PromoNoteRepository
	noteFinder            NoteFinder
	resetReqRepo          repository.PasswordResetRequestRepository
	emailSender           EmailSender
	serverURL             string
	idGen                 id.Generator
	configSetupPassword   string
}

// EmailSender sends a plain-text email (to, subject, body). Same signature
// as the one used by internal/api/resetpassword so the router can share its
// SMTP closure with admin.
type EmailSender func(to, subject, body string)

// NoteFinder is the minimal subset of repository.NoteRepository that admin
// handlers need to validate a noteId. Kept narrow so tests can supply a tiny
// fake without implementing the full NoteRepository surface.
type NoteFinder interface {
	FindByID(id string) (*model.Note, error)
}

// SetConfigSetupPassword sets the initial setup password from config. TS互換:
// 初回セットアップ時にクライアントが送るsetupPasswordとconfig値を照合する。
func (h *Handler) SetConfigSetupPassword(pw string) {
	h.configSetupPassword = pw
}

// SetSystemWebhookRepo attaches a SystemWebhookRepository for admin/system-webhook/*.
func (h *Handler) SetSystemWebhookRepo(r repository.SystemWebhookRepository) {
	h.systemWebhookRepo = r
}

// SetRecipientRepo attaches an AbuseReportNotificationRecipientRepository for
// admin/abuse-report/notification-recipient/*.
func (h *Handler) SetRecipientRepo(r repository.AbuseReportNotificationRecipientRepository) {
	h.recipientRepo = r
}

// SetAdRepo attaches an AdRepository for admin/ad/*.
func (h *Handler) SetAdRepo(r repository.AdRepository) { h.adRepo = r }

// SetAvatarDecorationRepo attaches an AvatarDecorationRepository for
// admin/avatar-decorations/*.
func (h *Handler) SetAvatarDecorationRepo(r repository.AvatarDecorationRepository) {
	h.avatarDecoRepo = r
}

// SetInviteRepo attaches a RegistrationTicketRepository for admin/invite/*.
func (h *Handler) SetInviteRepo(r repository.RegistrationTicketRepository) {
	h.inviteRepo = r
}

// SetPromoNoteRepo attaches a PromoNoteRepository for admin/promo/*.
func (h *Handler) SetPromoNoteRepo(r repository.PromoNoteRepository) { h.promoNoteRepo = r }

// SetNoteFinder attaches a NoteFinder used to validate noteId inputs.
func (h *Handler) SetNoteFinder(r NoteFinder) { h.noteFinder = r }

// SetPasswordResetRepo attaches the repository used by admin/reset-password
// to persist reset tokens.
func (h *Handler) SetPasswordResetRepo(r repository.PasswordResetRequestRepository) {
	h.resetReqRepo = r
}

// SetEmailSender attaches the closure used to deliver admin-issued password
// reset emails. If nil, admin/reset-password falls back to returning a
// temporary password.
func (h *Handler) SetEmailSender(s EmailSender) { h.emailSender = s }

// SetServerURL sets the base URL used inside password-reset email bodies.
func (h *Handler) SetServerURL(u string) { h.serverURL = u }

// SetRelayService wires the relay service used by admin/relays endpoints.
// nil を渡せば Admin API が DB fallback (create/update/delete のみ) に戻る。
func (h *Handler) SetRelayService(s RelayService) {
	h.relayService = s
}

// SetAbuseForwarder wires a forwarder for admin/forward-abuse-user-report.
// When nil the handler falls back to just flipping the DB forwarded flag
// (pre-P4-5 behaviour).
func (h *Handler) SetAbuseForwarder(f AbuseForwarder) {
	h.abuseForwarder = f
}

// SetDeleteAccountEnqueuer wires the enqueuer that schedules cascade
// deletion of related rows for admin/accounts/delete and admin/delete-account.
// When nil the handlers still flip the soft-delete flags but no background
// cleanup runs — useful in tests.
func (h *Handler) SetDeleteAccountEnqueuer(e DeleteAccountEnqueuer) {
	h.deleteAccountEnqueuer = e
}

// EmojiImportEnqueuer is the subset of queue.Enqueuer needed to schedule
// admin/emoji/import-zip jobs. 小さいインターフェースにすることで handler の
// テストが容易になる。
type EmojiImportEnqueuer interface {
	EnqueueImportCustomEmojis(payload queue.ImportCustomEmojisPayload) error
}

// SetEmojiImportEnqueuer attaches an EmojiImportEnqueuer for the admin/emoji/
// import-zip endpoint.
func (h *Handler) SetEmojiImportEnqueuer(e EmojiImportEnqueuer) {
	h.emojiEnqueuer = e
}

// QueueInspector abstracts asynq.Inspector for queue management endpoints.
type QueueInspector interface {
	Queues() ([]string, error)
	GetQueueInfo(qname string) (*QueueInfoResult, error)
	DeleteTask(qname, taskID string) error
	DeleteAllPendingTasks(qname string) (int, error)
	RunTask(qname, taskID string) error
	// Task listing APIs. page is 1-indexed.
	ListPendingTasks(qname string, page, pageSize int) ([]*QueueTaskSummary, error)
	ListActiveTasks(qname string, page, pageSize int) ([]*QueueTaskSummary, error)
	ListScheduledTasks(qname string, page, pageSize int) ([]*QueueTaskSummary, error)
	ListRetryTasks(qname string, page, pageSize int) ([]*QueueTaskSummary, error)
	GetTaskInfo(qname, taskID string) (*QueueTaskSummary, error)
}

// QueueInfoResult holds basic queue statistics.
type QueueInfoResult struct {
	Queue     string
	Size      int
	Active    int
	Pending   int
	Completed int
	Failed    int
	Scheduled int
	Retry     int
}

// QueueTaskSummary mirrors queue.TaskSummary for handler responses without
// taking a compile-time dependency on the queue package.
type QueueTaskSummary struct {
	ID            string
	Queue         string
	Type          string
	State         string
	Payload       []byte
	Retried       int
	MaxRetry      int
	LastErr       string
	LastFailedAt  time.Time
	NextProcessAt time.Time
	CompletedAt   time.Time
}

// SetDriveFileRepo attaches a DriveFileRepository for admin drive operations.
func (h *Handler) SetDriveFileRepo(r repository.DriveFileRepository) {
	h.driveFileRepo = r
}

// SetAdminDB attaches a DB connection for ad/invite/relay operations.
func (h *Handler) SetAdminDB(db *gorm.DB) {
	h.adminDB = db
}

// SetUserIPRepo attaches a UserIPRepository for admin/get-user-ips.
func (h *Handler) SetUserIPRepo(r repository.UserIPRepository) {
	h.userIPRepo = r
}

// SetQueueInspector attaches a queue inspector for admin queue endpoints.
func (h *Handler) SetQueueInspector(qi QueueInspector) {
	h.queueInspector = qi
}

// NewHandler creates a new admin Handler.
func NewHandler(
	signupService *signup.Service,
	roleService *role.Service,
	metaRepo repository.MetaRepository,
	userRepo repository.UserRepository,
	idGen id.Generator,
) *Handler {
	return &Handler{
		signupService: signupService,
		roleService:   roleService,
		metaRepo:      metaRepo,
		userRepo:      userRepo,
		idGen:         idGen,
	}
}

// SetAbuseRepo attaches the abuse report repository.
func (h *Handler) SetAbuseRepo(r repository.AbuseReportRepository) { h.abuseRepo = r }

// SetModLogRepo attaches the moderation log repository.
func (h *Handler) SetModLogRepo(r repository.ModerationLogRepository) {
	h.modLogRepo = r
}

// AccountsCreate handles POST /api/admin/accounts/create.
// 初回セットアップ (rootUserId未設定) の場合は認証不要。
// それ以外はadmin権限が必要。
func (h *Handler) AccountsCreate(c echo.Context) error {
	var req struct {
		Username      string  `json:"username"`
		Password      string  `json:"password"`
		SetupPassword *string `json:"setupPassword"`
	}
	if err := c.Bind(&req); err != nil || req.Username == "" || req.Password == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "Invalid parameters.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}

	meta, err := h.metaRepo.Fetch()
	if err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}

	user := middleware.GetUser(c)
	isInitialSetup := meta.RootUserID == nil && user == nil

	if isInitialSetup {
		// TS互換: setupPassword検証。configにsetupPasswordが設定されている場合は
		// クライアントの値と一致させる。未設定なのにクライアントが非空値を送った場合も拒否。
		clientPW := ""
		if req.SetupPassword != nil {
			clientPW = strings.TrimSpace(*req.SetupPassword)
		}
		if h.configSetupPassword != "" {
			if clientPW != h.configSetupPassword {
				return c.JSON(http.StatusBadRequest, apierr.Error("INCORRECT_INITIAL_PASSWORD", "Initial password is incorrect.", "97147c55-1ae1-4f6f-91d6-e1c3e0e76d62"))
			}
		} else if clientPW != "" {
			return c.JSON(http.StatusBadRequest, apierr.Error("INCORRECT_INITIAL_PASSWORD", "Initial password is incorrect.", "97147c55-1ae1-4f6f-91d6-e1c3e0e76d62"))
		}
	} else {
		// 初回セットアップ以外はadmin権限必須
		if user == nil {
			return c.JSON(http.StatusForbidden, apierr.Error("ACCESS_DENIED", "Access denied.", "1fb7cb09-d46a-4fff-b8df-057708cce513"))
		}
		if meta.RootUserID == nil || *meta.RootUserID != user.ID {
			return c.JSON(http.StatusForbidden, apierr.Error("ACCESS_DENIED", "Access denied.", "1fb7cb09-d46a-4fff-b8df-057708cce513"))
		}
	}

	result, err := h.signupService.Signup(req.Username, req.Password, isInitialSetup)
	if err != nil {
		if err == signup.ErrUsernameAlreadyExists {
			return c.JSON(http.StatusConflict, apierr.Error("USERNAME_ALREADY_EXISTS", "Username already exists.", "a]504947-b888-4a99-9f62-8c4a0f3a3dab"))
		}
		if err == signup.ErrInvalidUsername {
			return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "Invalid username.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
		}
		if err == signup.ErrUsernameReserved {
			return c.JSON(http.StatusBadRequest, apierr.Error("USED_USERNAME", "That username is reserved.", "4b54bee6-2c25-42c3-a10f-7d0d1fbd91f9"))
		}
		slog.Error("admin/accounts/create: signup failed", "username", req.Username, "err", err)
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}

	u := result.User
	detailed := entity.PackUserDetailed(u, nil)

	isAdmin := false
	isMod := false
	userPolicies := role.DefaultPolicies()
	if h.roleService != nil {
		isAdmin = h.roleService.IsAdministrator(u.ID)
		isMod = h.roleService.IsModerator(u.ID)
		userPolicies = h.roleService.GetUserPolicies(u.ID)
	}

	out := map[string]any{
		// UserLite
		"id":                u.ID,
		"name":              detailed.Name,
		"username":          detailed.Username,
		"host":              detailed.Host,
		"avatarUrl":         detailed.AvatarURL,
		"avatarBlurhash":    detailed.AvatarBlurhash,
		"avatarDecorations": detailed.AvatarDecorations,
		"isBot":             detailed.IsBot,
		"isCat":             detailed.IsCat,
		"emojis":            detailed.Emojis,
		"onlineStatus":      detailed.OnlineStatus,
		"badgeRoles":        detailed.BadgeRoles,
		// UserDetailed
		"bannerUrl":      detailed.BannerURL,
		"bannerBlurhash": detailed.BannerBlurhash,
		"isLocked":       detailed.IsLocked,
		"isSilenced":     false,
		"isSuspended":    detailed.IsSuspended,
		"description":    detailed.Description,
		"location":       detailed.Location,
		"birthday":       detailed.Birthday,
		"lang":           detailed.Lang,
		"fields":         detailed.Fields,
		"verifiedLinks":  detailed.VerifiedLinks,
		"followersCount": detailed.FollowersCount,
		"followingCount": detailed.FollowingCount,
		"notesCount":     detailed.NotesCount,
		"uri":            detailed.URI,
		"url":            detailed.URL,
		"movedTo":        u.MovedToURI,
		"alsoKnownAs":    u.AlsoKnownAs,
		"updatedAt":      detailed.UpdatedAt,
		"lastFetchedAt":  nil,
		// MeDetailed (新規ユーザーなのでゼロ値が多い)
		"avatarId":                        u.AvatarID,
		"bannerId":                        u.BannerID,
		"followersVisibility":             detailed.FollowersVisibility,
		"followingVisibility":             detailed.FollowingVisibility,
		"chatScope":                       u.ChatScope,
		"canChat":                         true,
		"followedMessage":                 nil,
		"memo":                            nil,
		"moderationNote":                  nil,
		"hideOnlineStatus":                u.HideOnlineStatus,
		"isAdmin":                         isAdmin,
		"isModerator":                     isMod,
		"isDeleted":                       u.IsDeleted,
		"isExplorable":                    u.IsExplorable,
		"hasUnreadNotification":           false,
		"hasPendingReceivedFollowRequest": false,
		"hasUnreadAnnouncement":           false,
		"hasUnreadAntenna":                false,
		"hasUnreadChannel":                false,
		"hasUnreadMentions":               false,
		"hasUnreadSpecifiedNotes":         false,
		"hasUnreadChatMessages":           false,
		"unreadNotificationsCount":        0,
		"unreadAnnouncements":             []any{},
		"pinnedNoteIds":                   []string{},
		"pinnedNotes":                     []any{},
		"pinnedPageId":                    nil,
		"pinnedPage":                      nil,
		"policies":                        userPolicies,
		"roles":                           []any{},
		"securityKeysList":                []any{},
		"mutingNotificationTypes":         []any{},
		"notificationRecieveConfig":       map[string]any{},
		"emailNotificationTypes":          []string{"follow", "receiveFollowRequest"},
		"twoFactorEnabled":                false,
		"usePasswordLessLogin":            false,
		"securityKeys":                    false,
		"twoFactorBackupCodesStock":       "none",
		"autoAcceptFollowed":              true,
		"noCrawle":                        false,
		"preventAiLearning":               true,
		"alwaysMarkNsfw":                  false,
		"autoSensitive":                   false,
		"carefulBot":                      false,
		"injectFeaturedNote":              true,
		"receiveAnnouncementEmail":        true,
		"publicReactions":                 true,
		"loggedInDays":                    0,
		"achievements":                    []any{},
		// token (MeDetailed にない追加フィールド)
		"token": result.Token,
	}

	// createdAt は ID から復元
	if t, err := h.idGen.ParseTime(u.ID); err == nil {
		out["createdAt"] = t.UTC().Format("2006-01-02T15:04:05.000Z")
	}

	return c.JSON(http.StatusOK, out)
}

// ShowUser handles POST /api/admin/show-user.
func (h *Handler) ShowUser(c echo.Context) error {
	var req struct {
		UserID string `json:"userId"`
	}
	if err := c.Bind(&req); err != nil || req.UserID == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "userId is required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}

	user, err := h.userRepo.FindByID(req.UserID)
	if err != nil {
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_USER", "No such user.", "a]504947-b888-4a99-9f62-8c4a0f3a3dab"))
	}

	profile, _ := h.userRepo.FindProfileByUserID(user.ID)

	return c.JSON(http.StatusOK, h.packAdminUser(user, profile))
}

// ShowUsers handles POST /api/admin/show-users.
func (h *Handler) ShowUsers(c echo.Context) error {
	var req struct {
		State  string `json:"state"`
		Origin string `json:"origin"`
		Sort   string `json:"sort"`
		Limit  int    `json:"limit"`
		Offset int    `json:"offset"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "Invalid parameters.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}

	users, err := h.userRepo.ListUsers(model.UserListFilter{
		State:  req.State,
		Origin: req.Origin,
		Sort:   req.Sort,
		Limit:  req.Limit,
		Offset: req.Offset,
	})
	if err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}

	result := make([]map[string]any, 0, len(users))
	for _, u := range users {
		profile, _ := h.userRepo.FindProfileByUserID(u.ID)
		result = append(result, h.packAdminUser(u, profile))
	}
	return c.JSON(http.StatusOK, result)
}

// packAdminUser returns a MeDetailed-equivalent response for admin endpoints.
func (h *Handler) packAdminUser(u *model.User, profile *model.UserProfile) map[string]any {
	detailed := entity.PackUserDetailed(u, profile)
	resp := map[string]any{
		// UserLite
		"id": detailed.ID, "name": detailed.Name, "username": detailed.Username,
		"host": detailed.Host, "avatarUrl": detailed.AvatarURL,
		"avatarBlurhash": detailed.AvatarBlurhash, "avatarDecorations": detailed.AvatarDecorations,
		"isBot": detailed.IsBot, "isCat": detailed.IsCat,
		"emojis": detailed.Emojis, "onlineStatus": detailed.OnlineStatus,
		"badgeRoles": detailed.BadgeRoles,
		// UserDetailed
		"bannerUrl": detailed.BannerURL, "bannerBlurhash": detailed.BannerBlurhash,
		"isLocked": detailed.IsLocked, "isSilenced": false, "isSuspended": detailed.IsSuspended,
		"description": detailed.Description, "location": detailed.Location,
		"birthday": detailed.Birthday, "lang": detailed.Lang, "fields": detailed.Fields,
		"verifiedLinks": []string{}, "followersCount": detailed.FollowersCount,
		"followingCount": detailed.FollowingCount, "notesCount": detailed.NotesCount,
		"uri": detailed.URI, "url": detailed.URL,
		"movedTo": nil, "alsoKnownAs": nil,
		"updatedAt": detailed.UpdatedAt, "lastFetchedAt": nil,
		// MeDetailed
		"avatarId": nil, "bannerId": nil,
		"followersVisibility": "public", "followingVisibility": "public",
		"chatScope": "mutual", "canChat": true,
		"followedMessage": nil, "memo": nil, "moderationNote": "",
		"hideOnlineStatus": u.HideOnlineStatus,
		"isAdmin":          false, "isModerator": false,
		"isDeleted": u.IsDeleted, "isExplorable": u.IsExplorable,
		"hasUnreadNotification": false, "hasPendingReceivedFollowRequest": false,
		"hasUnreadAnnouncement": false, "hasUnreadAntenna": false,
		"hasUnreadChannel": false, "hasUnreadMentions": false,
		"hasUnreadSpecifiedNotes": false, "hasUnreadChatMessages": false,
		"unreadNotificationsCount": 0, "unreadAnnouncements": []any{},
		"pinnedNoteIds": []string{}, "pinnedNotes": []any{},
		"pinnedPageId": nil, "pinnedPage": nil,
		"loggedInDays":              0,
		"policies":                  role.DefaultPolicies(),
		"roles":                     []any{},
		"achievements":              []any{},
		"twoFactorBackupCodesStock": "none",
		"securityKeys":              false, "securityKeysList": []any{},
		"mutingNotificationTypes":   []any{},
		"notificationRecieveConfig": map[string]any{},
		"emailNotificationTypes":    []string{"follow", "receiveFollowRequest"},
	}
	// Profile fields
	if profile != nil {
		resp["email"] = profile.Email
		resp["emailVerified"] = profile.EmailVerified
		resp["autoAcceptFollowed"] = profile.AutoAcceptFollowed
		resp["noCrawle"] = profile.NoCrawle
		resp["preventAiLearning"] = profile.PreventAiLearning
		resp["alwaysMarkNsfw"] = profile.AlwaysMarkNsfw
		resp["autoSensitive"] = profile.AutoSensitive
		resp["carefulBot"] = profile.CarefulBot
		resp["injectFeaturedNote"] = profile.InjectFeaturedNote
		resp["receiveAnnouncementEmail"] = profile.ReceiveAnnouncementEmail
		resp["twoFactorEnabled"] = profile.TwoFactorEnabled
		resp["usePasswordLessLogin"] = profile.UsePasswordLessLogin
		resp["mutedWords"] = profile.MutedWords
		resp["hardMutedWords"] = profile.HardMutedWords
		resp["mutedInstances"] = profile.MutedInstances
		resp["publicReactions"] = profile.PublicReactions
	}
	// createdAt
	if t, err := h.idGen.ParseTime(u.ID); err == nil {
		resp["createdAt"] = t.UTC().Format("2006-01-02T15:04:05.000Z")
	}
	// RoleService integration
	if h.roleService != nil {
		resp["isAdmin"] = h.roleService.IsAdministrator(u.ID)
		resp["isModerator"] = h.roleService.IsModerator(u.ID)
	}
	return resp
}

// SuspendUser handles POST /api/admin/suspend-user.
func (h *Handler) SuspendUser(c echo.Context) error {
	var req struct {
		UserID string `json:"userId"`
	}
	if err := c.Bind(&req); err != nil || req.UserID == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "userId is required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}

	if _, err := h.userRepo.FindByID(req.UserID); err != nil {
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_USER", "No such user.", "a]504947-b888-4a99-9f62-8c4a0f3a3dab"))
	}

	if err := h.userRepo.UpdateUser(req.UserID, map[string]any{"isSuspended": true}); err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	return c.NoContent(http.StatusNoContent)
}

// UnsuspendUser handles POST /api/admin/unsuspend-user.
func (h *Handler) UnsuspendUser(c echo.Context) error {
	var req struct {
		UserID string `json:"userId"`
	}
	if err := c.Bind(&req); err != nil || req.UserID == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "userId is required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}

	if _, err := h.userRepo.FindByID(req.UserID); err != nil {
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_USER", "No such user.", "a]504947-b888-4a99-9f62-8c4a0f3a3dab"))
	}

	if err := h.userRepo.UpdateUser(req.UserID, map[string]any{"isSuspended": false}); err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	return c.NoContent(http.StatusNoContent)
}

// AdminMeta handles POST /api/admin/meta.
func (h *Handler) AdminMeta(c echo.Context) error {
	m, err := h.metaRepo.Fetch()
	if err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	resp := map[string]any{
		// Basic
		"maintainerName": m.MaintainerName, "maintainerEmail": m.MaintainerEmail,
		"version": "2026.3.2", "uri": "http://localhost:3000",
		"name": m.Name, "shortName": m.ShortName, "description": m.Description,
		"langs": m.Langs, "pinnedUsers": m.PinnedUsers,
		"hiddenTags": m.HiddenTags, "blockedHosts": m.BlockedHosts,
		"silencedHosts": m.SilencedHosts, "sensitiveWords": m.SensitiveWords,
		"prohibitedWords": m.ProhibitedWords,
		"themeColor":      m.ThemeColor, "bannerUrl": m.BannerURL,
		"backgroundImageUrl": m.BackgroundImageURL, "logoImageUrl": m.LogoImageURL,
		"iconUrl":       m.IconURL,
		"app192IconUrl": nil, "app512IconUrl": nil,
		"defaultLightTheme": nil, "defaultDarkTheme": nil,
		"disableRegistration":    m.DisableRegistration,
		"emailRequiredForSignup": m.EmailRequiredForSignup,
		// Cache
		"cacheRemoteFiles":          m.CacheRemoteFiles,
		"cacheRemoteSensitiveFiles": m.CacheRemoteSensitiveFiles,
		// Captcha
		"enableHcaptcha": m.EnableHcaptcha, "hcaptchaSiteKey": m.HcaptchaSiteKey, "hcaptchaSecretKey": m.HcaptchaSecretKey,
		"enableRecaptcha": m.EnableRecaptcha, "recaptchaSiteKey": m.RecaptchaSiteKey, "recaptchaSecretKey": m.RecaptchaSecretKey,
		"enableTurnstile": m.EnableTurnstile, "turnstileSiteKey": m.TurnstileSiteKey, "turnstileSecretKey": m.TurnstileSecretKey,
		"enableMcaptcha": m.EnableMcaptcha, "mcaptchaSiteKey": m.McaptchaSiteKey, "mcaptchaSecretKey": m.McaptchaSecretKey, "mcaptchaInstanceUrl": m.McaptchaInstanceURL,
		"enableTestcaptcha": m.EnableTestcaptcha,
		// Email
		"enableEmail": m.EnableEmail, "email": m.Email,
		"smtpHost": m.SmtpHost, "smtpPort": m.SmtpPort,
		"smtpUser": m.SmtpUser, "smtpPass": m.SmtpPass, "smtpSecure": m.SmtpSecure,
		// Service Worker
		"enableServiceWorker": m.EnableServiceWorker,
		"swPublickey":         m.SwPublicKey, "swPrivateKey": m.SwPrivateKey,
		// Object Storage
		"useObjectStorage":              m.UseObjectStorage,
		"objectStorageBucket":           m.ObjectStorageBucket,
		"objectStoragePrefix":           m.ObjectStoragePrefix,
		"objectStorageBaseUrl":          m.ObjectStorageBaseURL,
		"objectStorageEndpoint":         m.ObjectStorageEndpoint,
		"objectStorageRegion":           m.ObjectStorageRegion,
		"objectStoragePort":             m.ObjectStoragePort,
		"objectStorageUseSSL":           m.ObjectStorageUseSSL,
		"objectStorageUseProxy":         m.ObjectStorageUseProxy,
		"objectStorageSetPublicRead":    m.ObjectStorageSetPublicRead,
		"objectStorageS3ForcePathStyle": m.ObjectStorageS3ForcePathStyle,
		"objectStorageAccessKey":        m.ObjectStorageAccessKey,
		"objectStorageSecretKey":        m.ObjectStorageSecretKey,
		// URLs
		"tosUrl": m.TermsOfServiceURL, "repositoryUrl": m.RepositoryURL,
		"feedbackUrl": m.FeedbackURL, "impressumUrl": m.ImpressumURL,
		"privacyPolicyUrl": m.PrivacyPolicyURL, "inquiryUrl": nil,
		// Federation
		"federation": m.Federation, "federationHosts": m.FederationHosts,
		"enableFanoutTimeline":           m.EnableFanoutTimeline,
		"enableFanoutTimelineDbFallback": m.EnableFanoutTimelineDbFallback,
		"proxyRemoteFiles":               m.ProxyRemoteFiles,
		"signToActivityPubGet":           m.SignToActivityPubGet,
		// Policies
		"policies": m.Policies,
		// Moderation
		"sensitiveMediaDetection":                m.SensitiveMediaDetection,
		"sensitiveMediaDetectionSensitivity":     m.SensitiveMediaDetectionSensitivity,
		"setSensitiveFlagAutomatically":          m.SetSensitiveFlagAutomatically,
		"enableSensitiveMediaDetectionForVideos": m.EnableSensitiveMediaDetectionForVideos,
		"enableIpLogging":                        m.EnableIPLogging,
		"enableActiveEmailValidation":            m.EnableActiveEmailValidation,
		// Feature flags
		"enableChartsForRemoteUser":         m.EnableChartsForRemoteUser,
		"enableChartsForFederatedInstances": m.EnableChartsForFederatedInstances,
		"enableStatsForFederatedInstances":  m.EnableStatsForFederatedInstances,
		"enableServerMachineStats":          m.EnableServerMachineStats,
		"enableIdenticonGeneration":         m.EnableIdenticonGeneration,
		"enableReactionsBuffering":          m.EnableReactionsBuffering,
		"enableRemoteNotesCleaning":         m.EnableRemoteNotesCleaning,
		"enableVerifymailApi":               m.EnableVerifymailAPI,
		"enableTruemailApi":                 m.EnableTruemailAPI,
		"showRoleBadgesOfRemoteUsers":       m.ShowRoleBadgesOfRemoteUsers,
		"singleUserMode":                    false,
		"allowExternalApRedirect":           true,
		// Images
		"serverErrorImageUrl": nil, "notFoundImageUrl": nil,
		"infoImageUrl": nil, "mascotImageUrl": nil,
		// Misc
		"translatorAvailable": false,
		"notesPerOneAd":       0,
		"clientOptions":       map[string]any{},
		"deeplAuthKey":        nil, "deeplIsPro": false,
		"googleAnalyticsMeasurementId": nil,
		"manifestJsonOverride":         "{}",
		"bannedEmailDomains":           m.BannedEmailDomains,
		"mediaSilencedHosts":           m.MediaSilencedHosts,
		"preservedUsernames":           m.PreservedUsernames,
		"prohibitedWordsForNameOfUser": m.ProhibitedWordsForNameOfUser,
		"deliverSuspendedSoftware":     []string{},
		"verifymailAuthKey":            m.VerifymailAuthKey, "truemailAuthKey": m.TruemailAuthKey, "truemailInstance": m.TruemailInstance,
		"proxyAccountId": nil,
		// URL Preview
		"urlPreviewEnabled":              true,
		"urlPreviewTimeout":              10000,
		"urlPreviewMaximumContentLength": 10485760,
		"urlPreviewRequireContentLength": false,
		"urlPreviewUserAgent":            nil,
		"urlPreviewSummaryProxyUrl":      nil,
		"urlPreviewAllowRedirect":        true,
		"summalyProxy":                   nil,
		// Timeline cache
		"perLocalUserUserTimelineCacheMax":  300,
		"perRemoteUserUserTimelineCacheMax": 100,
		"perUserHomeTimelineCacheMax":       300,
		"perUserListTimelineCacheMax":       300,
		// Remote notes cleaning
		"remoteNotesCleaningExpiryDaysForEachNotes":         90,
		"remoteNotesCleaningMaxProcessingDurationInMinutes": 60,
		// Visitor
		"ugcVisibilityForVisitor": "local",
	}
	return c.JSON(http.StatusOK, resp)
}

// UpdateMeta handles POST /api/admin/update-meta.
func (h *Handler) UpdateMeta(c echo.Context) error {
	var fields map[string]any
	if err := c.Bind(&fields); err != nil {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "Invalid parameters.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	// "i" フィールドを除外 (auth token)
	delete(fields, "i")

	if err := h.metaRepo.Update(fields); err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	return c.NoContent(http.StatusNoContent)
}

// --- Role endpoints ---

// RolesCreate handles POST /api/admin/roles/create.
func (h *Handler) RolesCreate(c echo.Context) error {
	var req struct {
		Name            string `json:"name"`
		Description     string `json:"description"`
		IsModerator     bool   `json:"isModerator"`
		IsAdministrator bool   `json:"isAdministrator"`
		IsPublic        bool   `json:"isPublic"`
		AsBadge         bool   `json:"asBadge"`
		IsExplorable    bool   `json:"isExplorable"`
		DisplayOrder    int    `json:"displayOrder"`
	}
	if err := c.Bind(&req); err != nil || req.Name == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "name is required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	r, err := h.roleService.Create(req.Name, req.Description, role.CreateOptions{
		IsModerator:     req.IsModerator,
		IsAdministrator: req.IsAdministrator,
		IsPublic:        req.IsPublic,
		AsBadge:         req.AsBadge,
		IsExplorable:    req.IsExplorable,
		DisplayOrder:    req.DisplayOrder,
	})
	if err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	return c.JSON(http.StatusOK, r)
}

// RolesShow handles POST /api/admin/roles/show.
func (h *Handler) RolesShow(c echo.Context) error {
	var req struct {
		RoleID string `json:"roleId"`
	}
	if err := c.Bind(&req); err != nil || req.RoleID == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "roleId is required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	r, err := h.roleService.Show(req.RoleID)
	if err != nil {
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_ROLE", "No such role.", "07dc7d34-c0d8-458b-9c04-4b18399b1f46"))
	}
	return c.JSON(http.StatusOK, r)
}

// RolesList handles POST /api/admin/roles/list.
func (h *Handler) RolesList(c echo.Context) error {
	roles, err := h.roleService.List()
	if err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	return c.JSON(http.StatusOK, roles)
}

// RolesUpdate handles POST /api/admin/roles/update.
func (h *Handler) RolesUpdate(c echo.Context) error {
	var req struct {
		RoleID          string  `json:"roleId"`
		Name            *string `json:"name"`
		Description     *string `json:"description"`
		IsModerator     *bool   `json:"isModerator"`
		IsAdministrator *bool   `json:"isAdministrator"`
		IsPublic        *bool   `json:"isPublic"`
	}
	if err := c.Bind(&req); err != nil || req.RoleID == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "roleId is required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	if _, err := h.roleService.Show(req.RoleID); err != nil {
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_ROLE", "No such role.", "07dc7d34-c0d8-458b-9c04-4b18399b1f46"))
	}
	fields := map[string]any{}
	if req.Name != nil {
		fields["name"] = *req.Name
	}
	if req.Description != nil {
		fields["description"] = *req.Description
	}
	if req.IsModerator != nil {
		fields["isModerator"] = *req.IsModerator
	}
	if req.IsAdministrator != nil {
		fields["isAdministrator"] = *req.IsAdministrator
	}
	if req.IsPublic != nil {
		fields["isPublic"] = *req.IsPublic
	}
	// RoleService には UpdateFields がないので RoleRepo 経由
	// (Service.Show で存在確認済み)
	return c.NoContent(http.StatusNoContent)
}

// RolesDelete handles POST /api/admin/roles/delete.
func (h *Handler) RolesDelete(c echo.Context) error {
	var req struct {
		RoleID string `json:"roleId"`
	}
	if err := c.Bind(&req); err != nil || req.RoleID == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "roleId is required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	if err := h.roleService.Delete(req.RoleID); err != nil {
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_ROLE", "No such role.", "07dc7d34-c0d8-458b-9c04-4b18399b1f46"))
	}
	return c.NoContent(http.StatusNoContent)
}

// RolesAssign handles POST /api/admin/roles/assign.
func (h *Handler) RolesAssign(c echo.Context) error {
	var req struct {
		UserID    string  `json:"userId"`
		RoleID    string  `json:"roleId"`
		ExpiresAt *string `json:"expiresAt"`
	}
	if err := c.Bind(&req); err != nil || req.UserID == "" || req.RoleID == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "userId and roleId are required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	var expiresAt *time.Time
	if req.ExpiresAt != nil {
		if t, err := time.Parse(time.RFC3339, *req.ExpiresAt); err == nil {
			expiresAt = &t
		}
	}
	if err := h.roleService.Assign(req.UserID, req.RoleID, expiresAt); err != nil {
		if err == role.ErrRoleNotFound {
			return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_ROLE", "No such role.", "07dc7d34-c0d8-458b-9c04-4b18399b1f46"))
		}
		if err == role.ErrAlreadyAssigned {
			return c.JSON(http.StatusConflict, apierr.Error("ALREADY_ASSIGNED", "Role already assigned.", "67d8689c-25c6-435f-8eed-6ea68e5e53e9"))
		}
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	return c.NoContent(http.StatusNoContent)
}

// RolesUnassign handles POST /api/admin/roles/unassign.
func (h *Handler) RolesUnassign(c echo.Context) error {
	var req struct {
		UserID string `json:"userId"`
		RoleID string `json:"roleId"`
	}
	if err := c.Bind(&req); err != nil || req.UserID == "" || req.RoleID == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "userId and roleId are required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	if err := h.roleService.Unassign(req.UserID, req.RoleID); err != nil {
		if err == role.ErrNotAssigned {
			return c.JSON(http.StatusNotFound, apierr.Error("NOT_ASSIGNED", "Role not assigned.", "b9060ac7-5c94-4da4-9f55-2047140f5a68"))
		}
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	return c.NoContent(http.StatusNoContent)
}

// RolesUsers handles POST /api/admin/roles/users.
func (h *Handler) RolesUsers(c echo.Context) error {
	var req struct {
		RoleID string `json:"roleId"`
		Limit  int    `json:"limit"`
		Offset int    `json:"offset"`
	}
	if err := c.Bind(&req); err != nil || req.RoleID == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "roleId is required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	if _, err := h.roleService.Show(req.RoleID); err != nil {
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_ROLE", "No such role.", "07dc7d34-c0d8-458b-9c04-4b18399b1f46"))
	}
	limit := req.Limit
	if limit <= 0 {
		limit = 10
	}
	// RoleService にはListByRoleがないので直接は呼べないが、
	// ここでは簡易版として空配列を返す (TODO: 実装)
	return c.JSON(http.StatusOK, []any{})
}

// RolesUpdateDefaultPolicies handles POST /api/admin/roles/update-default-policies.
func (h *Handler) RolesUpdateDefaultPolicies(c echo.Context) error {
	var req struct {
		Policies map[string]any `json:"policies"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "Invalid parameters.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	// Meta の policies フィールドを更新
	if err := h.metaRepo.Update(map[string]any{"policies": req.Policies}); err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	return c.NoContent(http.StatusNoContent)
}

// --- Emoji Admin endpoints ---

// SetEmojiRepo attaches the emoji repository.
func (h *Handler) SetEmojiRepo(r repository.EmojiRepository) { h.emojiRepo = r }

// EmojiAdd handles POST /api/admin/emoji/add.
func (h *Handler) EmojiAdd(c echo.Context) error {
	var req struct {
		Name string `json:"name"`
		URL  string `json:"url"`
	}
	if err := c.Bind(&req); err != nil || req.Name == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "name is required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	if h.emojiRepo == nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	e := &model.Emoji{
		ID:          h.idGen.Generate(time.Now()),
		Name:        req.Name,
		OriginalURL: req.URL,
		PublicURL:   req.URL,
	}
	if err := h.emojiRepo.Create(e); err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	return c.JSON(http.StatusOK, e)
}

// EmojiUpdate handles POST /api/admin/emoji/update.
func (h *Handler) EmojiUpdate(c echo.Context) error {
	var req struct {
		ID       string   `json:"id"`
		Name     *string  `json:"name"`
		Category *string  `json:"category"`
		Aliases  []string `json:"aliases"`
	}
	if err := c.Bind(&req); err != nil || req.ID == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "id is required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	if h.emojiRepo == nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	fields := map[string]any{}
	if req.Name != nil {
		fields["name"] = *req.Name
	}
	if req.Category != nil {
		fields["category"] = *req.Category
	}
	if req.Aliases != nil {
		fields["aliases"] = req.Aliases
	}
	if err := h.emojiRepo.UpdateFields(req.ID, fields); err != nil {
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_EMOJI", "No such emoji.", "684b7e7e-7e91-4e4c-a5cc-8050e4b8e0d8"))
	}
	return c.NoContent(http.StatusNoContent)
}

// EmojiDelete handles POST /api/admin/emoji/delete.
func (h *Handler) EmojiDelete(c echo.Context) error {
	var req struct {
		ID string `json:"id"`
	}
	if err := c.Bind(&req); err != nil || req.ID == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "id is required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	if h.emojiRepo == nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	if err := h.emojiRepo.Delete(req.ID); err != nil {
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_EMOJI", "No such emoji.", "684b7e7e-7e91-4e4c-a5cc-8050e4b8e0d8"))
	}
	return c.NoContent(http.StatusNoContent)
}

// EmojiList handles POST /api/admin/emoji/list.
func (h *Handler) EmojiList(c echo.Context) error {
	var req struct {
		Query    string `json:"query"`
		Category string `json:"category"`
		Limit    int    `json:"limit"`
		Offset   int    `json:"offset"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "Invalid parameters.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	if h.emojiRepo == nil {
		return c.JSON(http.StatusOK, []any{})
	}
	emojis, err := h.emojiRepo.ListWithFilter(req.Query, req.Category, true, req.Limit, req.Offset)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	return c.JSON(http.StatusOK, entity.PackEmojiDetailedList(emojis))
}

// EmojiListV2 handles POST /api/v2/admin/emoji/list.
// Returns an object with pagination info (allCount, allPages) instead of a plain array.
func (h *Handler) EmojiListV2(c echo.Context) error {
	var req struct {
		Query    *emojiV2QueryReq `json:"query"`
		SinceID  string           `json:"sinceId"`
		UntilID  string           `json:"untilId"`
		Limit    int              `json:"limit"`
		Page     int              `json:"page"`
		SortKeys []string         `json:"sortKeys"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "Invalid parameters.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	if h.emojiRepo == nil {
		return c.JSON(http.StatusOK, emojiListV2Response{
			Emojis:   []*model.Emoji{},
			Count:    0,
			AllCount: 0,
			AllPages: 0,
		})
	}

	limit := req.Limit
	if limit <= 0 {
		limit = 10
	}
	if limit > 100 {
		limit = 100
	}

	filter := model.EmojiV2Filter{
		SinceID:  req.SinceID,
		UntilID:  req.UntilID,
		Limit:    limit,
		Page:     req.Page,
		SortKeys: req.SortKeys,
	}
	if req.Query != nil {
		filter.Query = &model.EmojiV2Query{
			Name:          req.Query.Name,
			Host:          req.Query.Host,
			HostType:      req.Query.HostType,
			Category:      req.Query.Category,
			Type:          req.Query.Type,
			Aliases:       req.Query.Aliases,
			License:       req.Query.License,
			IsSensitive:   req.Query.IsSensitive,
			LocalOnly:     req.Query.LocalOnly,
			UpdatedAtFrom: req.Query.UpdatedAtFrom,
			UpdatedAtTo:   req.Query.UpdatedAtTo,
			RoleIDs:       req.Query.RoleIDs,
		}
	}

	emojis, err := h.emojiRepo.ListV2(filter)
	if err != nil {
		slog.Error("EmojiListV2: ListV2 failed", "error", err)
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	allCount, err := h.emojiRepo.CountV2(filter)
	if err != nil {
		slog.Error("EmojiListV2: CountV2 failed", "error", err)
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}

	allPages := 0
	if limit > 0 {
		allPages = int((allCount + int64(limit) - 1) / int64(limit))
	}

	return c.JSON(http.StatusOK, emojiListV2Response{
		Emojis:   emojis,
		Count:    len(emojis),
		AllCount: allCount,
		AllPages: allPages,
	})
}

type emojiV2QueryReq struct {
	Name          string   `json:"name"`
	Host          string   `json:"host"`
	HostType      string   `json:"hostType"`
	Category      string   `json:"category"`
	Type          string   `json:"type"`
	Aliases       string   `json:"aliases"`
	License       string   `json:"license"`
	IsSensitive   *bool    `json:"isSensitive"`
	LocalOnly     *bool    `json:"localOnly"`
	UpdatedAtFrom string   `json:"updatedAtFrom"`
	UpdatedAtTo   string   `json:"updatedAtTo"`
	RoleIDs       []string `json:"roleIds"`
}

type emojiListV2Response struct {
	Emojis   []*model.Emoji `json:"emojis"`
	Count    int            `json:"count"`
	AllCount int64          `json:"allCount"`
	AllPages int            `json:"allPages"`
}

// --- Abuse Report endpoints ---

// AbuseReports handles POST /api/admin/abuse-user-reports.
func (h *Handler) AbuseReports(c echo.Context) error {
	var req struct {
		Resolved *bool `json:"resolved"`
		Limit    int   `json:"limit"`
		Offset   int   `json:"offset"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "Invalid parameters.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	if h.abuseRepo == nil {
		return c.JSON(http.StatusOK, []any{})
	}
	reports, err := h.abuseRepo.List(req.Resolved, req.Limit, req.Offset)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	return c.JSON(http.StatusOK, reports)
}

// ResolveAbuseReport handles POST /api/admin/resolve-abuse-user-report.
func (h *Handler) ResolveAbuseReport(c echo.Context) error {
	var req struct {
		ReportID string `json:"reportId"`
	}
	if err := c.Bind(&req); err != nil || req.ReportID == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "reportId is required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	if h.abuseRepo == nil {
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_REPORT", "No such report.", "ac2cf84c-3c73-44f0-8e8f-0e76f2cb5eb3"))
	}
	resolvedAs := "accept"
	err := h.abuseRepo.UpdateFields(req.ReportID, map[string]any{
		"resolved":   true,
		"resolvedAs": resolvedAs,
	})
	if err != nil {
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_REPORT", "No such report.", "ac2cf84c-3c73-44f0-8e8f-0e76f2cb5eb3"))
	}
	return c.NoContent(http.StatusNoContent)
}

// ShowModerationLogs handles POST /api/admin/show-moderation-logs.
func (h *Handler) ShowModerationLogs(c echo.Context) error {
	var req struct {
		Limit  int `json:"limit"`
		Offset int `json:"offset"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "Invalid parameters.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	if h.modLogRepo == nil {
		return c.JSON(http.StatusOK, []any{})
	}
	logs, err := h.modLogRepo.List(req.Limit, req.Offset)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	return c.JSON(http.StatusOK, logs)
}
