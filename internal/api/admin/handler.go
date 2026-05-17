package admin

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/lib/pq"
	"github.com/shiroha-a/mk/internal/api/apierr"
	"github.com/shiroha-a/mk/internal/config"
	"github.com/shiroha-a/mk/internal/core/moderationlog"
	"github.com/shiroha-a/mk/internal/core/role"
	"github.com/shiroha-a/mk/internal/core/signup"
	"github.com/shiroha-a/mk/internal/core/webpush"
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

// InstanceMetadataFetcher refreshes remote instance metadata (nodeinfo +
// iconUrl/faviconUrl) for the given host. Narrow interface that matches
// coreinstance.FetchMetadataService.Fetch so admin handler tests don't
// have to pull in the full federation stack.
type InstanceMetadataFetcher interface {
	Fetch(host string) error
}

// SystemAccountFetcher returns (and lazily creates) the built-in proxy /
// relay / instance actor users. Narrow interface matching
// coresystemaccount.Service.Fetch so admin handler tests stay decoupled.
type SystemAccountFetcher interface {
	Fetch(kind string) (*model.User, error)
}

// UnfollowEnqueuer schedules per-pair Unfollow background jobs, used by
// admin/federation/remove-all-following to detach all incoming follows from
// a host without blocking the HTTP request. The job is consumed by
// processors.UnfollowProcessor which calls core/following.Service.Unfollow
// (= Following row 削除 + Reject(Follow) 配送)。Misskey TS の
// queueService.createUnfollowJob 相当。
type UnfollowEnqueuer interface {
	EnqueueUnfollow(payload queue.UnfollowPayload) error
}

// Handler handles admin API endpoints.
type Handler struct {
	signupService           *signup.Service
	roleService             *role.Service
	metaRepo                repository.MetaRepository
	userRepo                repository.UserRepository
	abuseRepo               repository.AbuseReportRepository
	modLogService           *moderationlog.Service
	emojiRepo               repository.EmojiRepository
	driveFileRepo           repository.DriveFileRepository
	adminDB                 *gorm.DB
	userIPRepo              repository.UserIPRepository
	queueInspector          QueueInspector
	emojiEnqueuer           EmojiImportEnqueuer
	emojiImageFetcher       EmojiImageFetcher
	relayService            RelayService
	abuseForwarder          AbuseForwarder
	deleteAccountEnqueuer   DeleteAccountEnqueuer
	systemWebhookRepo       repository.SystemWebhookRepository
	recipientRepo           repository.AbuseReportNotificationRecipientRepository
	adRepo                  repository.AdRepository
	avatarDecoRepo          repository.AvatarDecorationRepository
	inviteRepo              repository.RegistrationTicketRepository
	promoNoteRepo           repository.PromoNoteRepository
	noteFinder              NoteFinder
	resetReqRepo            repository.PasswordResetRequestRepository
	emailSender             EmailSender
	smtpProxyURL            string
	serverURL               string
	idGen                   id.Generator
	configSetupPassword     string
	instanceMetadataFetcher InstanceMetadataFetcher
	systemAccountFetcher    SystemAccountFetcher
	followingRepo           repository.FollowingRepository
	unfollowEnqueuer        UnfollowEnqueuer
	// instanceRepo は admin/federation/* の instance lookup / update を
	// inject 可能にするための DI 口 (#676)。FederationUpdateInstance の
	// log type 分岐をテストするため、`adminDB` 直叩きから repository 経由
	// に剥がしている。未配線時は `adminDB` フォールバックは持たず no-op。
	instanceRepo repository.InstanceRepository
	// webhookTestClient は admin/system-webhook/test の fire-and-forget POST
	// に使う SSRF-safe HTTP client。router.go で safehttp.WithProxy など共通
	// outbound 設定を適用したものを差し込む (#638)。nil のときは default の
	// 10s timeout client にフォールバックする (テスト容易性のため)。
	webhookTestClient *http.Client
	// userTokenInvalidator は admin が他 user を suspend / unsuspend /
	// 論理削除した直後に target user の全 tokenCache entry を即時失効する
	// ために使う (#965)。i/regenerate-token (#884) や i/update (#960) と
	// 同じ AuthMiddleware が「token 単独削除」「user 全 token 削除」の 2
	// 種類の API を提供しており、admin 経由は後者を使う。production では
	// router で必ず wire する (未配線時は 30s cache TTL 待ちで stale 旧 user
	// が auth 通過する security regression が残る)。
	userTokenInvalidator UserTokenInvalidator
}

// UserTokenInvalidator drops every cached auth entry for the given userID
// (= all sessions / API tokens belonging to that user) so the next
// authenticated request from any of those tokens re-resolves through the
// DB. Implemented by middleware.AuthMiddleware. Used by admin actions
// that change a user's middleware-relevant fields (isSuspended /
// isDeleted) — see #965.
type UserTokenInvalidator interface {
	InvalidateTokensForUser(userID string)
}

// SetUserTokenInvalidator wires the AuthMiddleware so admin/suspend-user
// / admin/unsuspend-user / admin/accounts/delete drop the target user's
// cached auth entries immediately. production では必ず wire すること
// (未配線時は最大 30 秒 stale window が残る)。
func (h *Handler) SetUserTokenInvalidator(inv UserTokenInvalidator) {
	h.userTokenInvalidator = inv
}

// invalidateUserTokenCache は target user の全 token cache entry を即時
// 失効させる helper。invalidator 未配線 / userID 空のときは noop。本
// helper を経由することで suspend / unsuspend / delete 等の admin 操作
// から共通の defensive guard を保てる (#965)。
func (h *Handler) invalidateUserTokenCache(userID string) {
	if h.userTokenInvalidator == nil || userID == "" {
		return
	}
	h.userTokenInvalidator.InvalidateTokensForUser(userID)
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

// SetWebhookTestClient attaches an SSRF-safe HTTP client used by
// admin/system-webhook/test to POST a fire-and-forget probe request.
// Typically wired with the server-wide outbound transport (forward proxy,
// outgoing address, allowedPrivateNetworks 等を反映、#638)。
func (h *Handler) SetWebhookTestClient(c *http.Client) {
	h.webhookTestClient = c
}

// SetInstanceMetadataFetcher attaches the fetcher used by
// admin/federation/refresh-remote-instance-metadata to re-fetch nodeinfo +
// icon for a specific host on demand.
func (h *Handler) SetInstanceMetadataFetcher(f InstanceMetadataFetcher) {
	h.instanceMetadataFetcher = f
}

// SetSystemAccountFetcher attaches the system account service used by
// admin/meta and admin/update-proxy-account to materialize the built-in
// proxy account.
func (h *Handler) SetSystemAccountFetcher(f SystemAccountFetcher) {
	h.systemAccountFetcher = f
}

// SetInstanceRepo wires an InstanceRepository for admin/federation handlers
// to use when reading/updating instance rows. Without it,
// FederationUpdateInstance early-returns 204 (#676)。
func (h *Handler) SetInstanceRepo(r repository.InstanceRepository) {
	h.instanceRepo = r
}

// SetFollowingRepo attaches a FollowingRepository for admin endpoints that
// need to enumerate Following rows by host (e.g.
// admin/federation/remove-all-following).
func (h *Handler) SetFollowingRepo(r repository.FollowingRepository) {
	h.followingRepo = r
}

// SetUnfollowEnqueuer attaches the queue client used by
// admin/federation/remove-all-following to schedule per-pair Unfollow
// background jobs. Typically wired with the project-wide queue client.
func (h *Handler) SetUnfollowEnqueuer(e UnfollowEnqueuer) {
	h.unfollowEnqueuer = e
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

// SetSMTPProxyURL forwards admin/send-email TCP connections through the
// configured proxy (cfg.ProxySmtp). Empty string disables the proxy and
// falls back to direct dial. See internal/misc/smtp.SendWithOptions.
func (h *Handler) SetSMTPProxyURL(u string) { h.smtpProxyURL = u }

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

// EmojiImageFetcher downloads a remote image and stores it in the local
// drive, returning the resulting drive file. Used by admin/emoji/copy to
// detach a copied emoji from its source server (#670). 小さい interface に
// 切り出すことで http client / drive.Service への直接依存を持たずに済み、
// handler 単体テストで fake を差し込める。
type EmojiImageFetcher interface {
	FetchAndStore(ctx context.Context, url string, user *model.User, name string) (*model.DriveFile, error)
}

// SetEmojiImageFetcher attaches an EmojiImageFetcher used by admin/emoji/copy.
// nil のままだと EmojiCopy は src の URL をそのまま継承する legacy 挙動を
// 維持する (テスト容易性 + 未配線環境での graceful degradation 用)。
func (h *Handler) SetEmojiImageFetcher(f EmojiImageFetcher) {
	h.emojiImageFetcher = f
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
	QueueMetrics(qname, kind string) (*QueueMetricsResult, error)
}

// QueueMetricsResult is the per-queue completed / failed time-series
// snapshot returned by QueueInspector.QueueMetrics. Mirrors
// driver.MetricsResult — duplicated here to keep the admin package
// free of internal/queue/driver imports.
type QueueMetricsResult struct {
	Count int64
	Data  []int64
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

// SetModLogService attaches the moderation log service. Both the read
// endpoint (admin/show-moderation-logs → Service.List) and the write
// path (admin handlers → Service.Log) go through this single object so
// wiring cannot drift out of sync.
func (h *Handler) SetModLogService(s *moderationlog.Service) {
	h.modLogService = s
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
			return c.JSON(http.StatusConflict, apierr.Error("USERNAME_ALREADY_EXISTS", "Username already exists.", "0a504947-b888-4a99-9f62-8c4a0f3a3dab"))
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
	if s, err := aidxCreatedAtString(h.idGen, u.ID); err == nil {
		out["createdAt"] = s
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
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_USER", "No such user.", "2b730f78-1179-461b-88ad-d24c9af1a5ce"))
	}

	profile, _ := h.userRepo.FindProfileByUserID(user.ID)

	return c.JSON(http.StatusOK, h.packAdminUser(user, profile))
}

// ShowUsers handles POST /api/admin/show-users.
func (h *Handler) ShowUsers(c echo.Context) error {
	var req struct {
		State    string `json:"state"`
		Origin   string `json:"origin"`
		Hostname string `json:"hostname"`
		Sort     string `json:"sort"`
		Limit    int    `json:"limit"`
		Offset   int    `json:"offset"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "Invalid parameters.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}

	users, err := h.userRepo.ListUsers(model.UserListFilter{
		State:    req.State,
		Origin:   req.Origin,
		Hostname: req.Hostname,
		Sort:     req.Sort,
		Limit:    req.Limit,
		Offset:   req.Offset,
	})
	if err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}

	// Profile を per-row FindProfileByUserID で引いていた N+1 (#300 1-4) を
	// FindProfilesByUserIDs (#503 で追加済) の 1 batch query に置換。
	ids := make([]string, 0, len(users))
	for _, u := range users {
		ids = append(ids, u.ID)
	}
	profiles, err := h.userRepo.FindProfilesByUserIDs(ids)
	if err != nil {
		// Profile fetch 失敗は ShowUsers 全体の致命扱いではないので、空 map で
		// fallback する (各 user は profile=nil で pack される)。
		profiles = nil
	}
	profileByUser := make(map[string]*model.UserProfile, len(profiles))
	for _, p := range profiles {
		profileByUser[p.UserID] = p
	}

	result := make([]map[string]any, 0, len(users))
	for _, u := range users {
		result = append(result, h.packAdminUser(u, profileByUser[u.ID]))
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
	// RoleService integration (#888): isAdmin / isModerator に加え、
	// upstream Misskey TS の admin/show-user response shape では
	// `roles` / `policies` も top-level で expected。frontend admin
	// moderation view は user.roles / user.policies を直接参照するので
	// 埋めないと UI が正しく動かない。
	if h.roleService != nil {
		resp["isAdmin"] = h.roleService.IsAdministrator(u.ID)
		resp["isModerator"] = h.roleService.IsModerator(u.ID)
		// roles: GetUserRoles は assigned + expired 除外済 list を返す
		// (= 即時 active な role の minimal shape)。err 時は frontend が
		// `user.roles.map(...)` で例外を吐かないよう空配列に fallback し
		// slog.Warn で観測する (= signins / roleAssigns の空配列扱いと揃え)。
		if userRoles, rerr := h.roleService.GetUserRoles(u.ID); rerr == nil {
			resp["roles"] = userRoles
		} else {
			slog.Warn("admin/show-user: failed to load roles", "userId", u.ID, "err", rerr)
			resp["roles"] = []any{}
		}
		// policies: assigned roles を merge した user-specific policy
		// (= upstream の RolePolicies 互換)。default override だけでなく
		// user の役割に基づいて差し替わる。
		resp["policies"] = h.roleService.GetUserPolicies(u.ID)
	}
	// signins / roleAssigns / isHibernated / lastActiveDate は upstream
	// admin/show-user shape の必須 field (#888)。mk-go では signin 履歴と
	// role assignment 詳細を別 endpoint で取得する設計なので空配列 / null
	// で填めて shape compat を保つ (full integration は別 issue scope)。
	resp["signins"] = []any{}
	resp["roleAssigns"] = []any{}
	resp["isHibernated"] = false
	if u.LastActiveDate != nil {
		resp["lastActiveDate"] = u.LastActiveDate.UTC().Format("2006-01-02T15:04:05.000Z")
	} else {
		resp["lastActiveDate"] = nil
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

	user, err := h.userRepo.FindByID(req.UserID)
	if err != nil {
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_USER", "No such user.", "2b730f78-1179-461b-88ad-d24c9af1a5ce"))
	}

	if err := h.userRepo.UpdateUser(req.UserID, map[string]any{"isSuspended": true}); err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	// 凍結直後の auth bypass 防止 (#965)。target の全 token cache entry を
	// 即時削除し、middleware 通過後の P2 gate (#964) に依存せず確実に弾く。
	h.invalidateUserTokenCache(req.UserID)
	h.logUserActionWithUser(c, moderationlog.LogSuspend, user)
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

	user, err := h.userRepo.FindByID(req.UserID)
	if err != nil {
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_USER", "No such user.", "2b730f78-1179-461b-88ad-d24c9af1a5ce"))
	}

	if err := h.userRepo.UpdateUser(req.UserID, map[string]any{"isSuspended": false}); err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	// 凍結解除直後に target の全 token cache entry を invalidate する (#965)。
	// cache 内に isSuspended=true な stale user が残っていると middleware の
	// P2 gate (#964) が cache hit 経路でも fire してしまい、解除済 user が
	// 認証通らない逆方向の bug になる。
	h.invalidateUserTokenCache(req.UserID)
	h.logUserActionWithUser(c, moderationlog.LogUnsuspend, user)
	return c.NoContent(http.StatusNoContent)
}

// fetchProxyAccountID returns the proxy system user ID for inclusion in the
// admin/meta response. fetcher 未配線 / 取得失敗時は nil を返すが、その場合
// frontend settings.vue の `users/show` 呼び出しが400で落ちて画面が真っ白
// になるため production では必ず wire しておくこと。
func (h *Handler) fetchProxyAccountID() any {
	if h.systemAccountFetcher == nil {
		return nil
	}
	user, err := h.systemAccountFetcher.Fetch("proxy")
	if err != nil || user == nil {
		return nil
	}
	return user.ID
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
		"version": config.MisskeyVersion, "uri": "http://localhost:3000",
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
		// proxyAccountId は frontend admin/settings 画面が読み込み時に
		// users/show でこの ID を引くため、必ず非空でなければ画面が
		// 真っ白になる (#348)。本家 SystemAccountService.fetch('proxy')
		// と同じく lazy 作成して埋める。
		"proxyAccountId": h.fetchProxyAccountID(),
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
	// frontend が送る API 名 → DB カラム名の差異を吸収する (#348)。API は
	// 本家互換の camelCase alias (tosUrl 等) を使うが、DB 側は
	// packages/backend/src/models/Meta.ts と同じ正規名で保持している。
	// alias が frontend から来たら DB カラム名に translate して渡す。
	renameUpdateMetaFields(fields)

	// JSON Bind 後の []any{...} (中身は string) を pq.StringArray に揃える
	// (#590)。GORM は map[string]any を Updates() に渡すと値の型をそのまま
	// driver に流すため、pq.StringArray (= driver.Valuer 実装) に変換しない
	// と varchar[] 列の UPDATE が `expression is of type record` で失敗する。
	// これは federationHosts / blockedHosts / silencedHosts / langs 等
	// すべての varchar[] 列に影響していたバグで、admin が whitelist 連合や
	// host ブロックを保存しても永続化されない原因だった。
	coerceMetaArrayFields(fields)

	// VAPID 鍵 auto-generate (#492): Service Worker 有効化時に
	// swPublicKey / swPrivateKey が両方空のとき backend で生成して詰める。
	// 既存鍵 (片方だけ欠けの中途状態 含む) は触らないことで、運用者が
	// 外部生成した鍵を保持できるようにする。
	if err := h.maybeAutoGenerateVAPID(fields); err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}

	// before snapshot for moderation log。Misskey TS の updateServerSettings
	// info は full meta の before/after で SMTP secret 等もそのまま記録する
	// 仕様 (上流 update-meta.ts でも mask 無し)。互換性最優先で同じ挙動。
	//
	// 順序メモ: maybeAutoGenerateVAPID は fields map に鍵を inject するが
	// metaRepo の DB 状態は触らないので、ここで Fetch() しても VAPID 生成
	// 前後で値は変わらない。よって before lookup の位置は VAPID 生成の前
	// でも後でも結果は同じ。可読性のため Update 直前に置いている。
	beforeMeta, _ := h.metaRepo.Fetch()
	if err := h.metaRepo.Update(fields); err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	if afterMeta, err := h.metaRepo.Fetch(); err == nil {
		h.logModeration(c, moderationlog.LogUpdateServerSettings, map[string]any{
			"before": beforeMeta,
			"after":  afterMeta,
		})
	}
	return c.NoContent(http.StatusNoContent)
}

// maybeAutoGenerateVAPID inspects the update-meta field map and, when
// Service Worker is being enabled (or is already enabled) and both the
// public and private VAPID keys would end up empty, generates a fresh
// key pair and injects it into fields. Skips key generation when:
//   - SW is being disabled or is already disabled (no-op)
//   - at least one key is already set (respects operator-supplied keys)
func (h *Handler) maybeAutoGenerateVAPID(fields map[string]any) error {
	current, err := h.metaRepo.Fetch()
	if err != nil {
		return err
	}
	enable := metaBoolAfterUpdate(fields, "enableServiceWorker", current.EnableServiceWorker)
	if !enable {
		return nil
	}
	pub := metaStringAfterUpdate(fields, "swPublicKey", strDeref(current.SwPublicKey))
	priv := metaStringAfterUpdate(fields, "swPrivateKey", strDeref(current.SwPrivateKey))
	if pub != "" || priv != "" {
		return nil
	}
	newPub, newPriv, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		return err
	}
	fields["swPublicKey"] = newPub
	fields["swPrivateKey"] = newPriv
	return nil
}

// metaBoolAfterUpdate returns the effective bool value of key after the
// update would be applied — incoming value if present and bool-typed,
// otherwise the current DB value.
func metaBoolAfterUpdate(fields map[string]any, key string, current bool) bool {
	if v, ok := fields[key]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return current
}

// metaStringAfterUpdate returns the effective string value of key after the
// update would be applied. Empty string, JSON null, and absence are all
// treated as "empty" so that an admin clearing a key with null still
// triggers the auto-generate path.
func metaStringAfterUpdate(fields map[string]any, key, current string) string {
	if v, ok := fields[key]; ok {
		switch s := v.(type) {
		case string:
			return s
		case nil:
			// JSON null は明示的に空にしたい意図と解釈する
			return ""
		}
	}
	return current
}

func strDeref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// updateMetaFieldAliases maps frontend API names to DB column names for
// update-meta. 本家 Misskey の admin/meta.ts / update-meta.ts は一部
// field で alias を使っており、mk-go の AdminMeta も同じ alias で公開
// しているため、save path にも逆向きの translate を入れる必要がある。
var updateMetaFieldAliases = map[string]string{
	"tosUrl":      "termsOfServiceUrl", // 本家: update-meta.ts の termsOfServiceUrl が admin/meta では tosUrl で出る
	"swPublickey": "swPublicKey",
}

func renameUpdateMetaFields(fields map[string]any) {
	for alias, canonical := range updateMetaFieldAliases {
		if v, ok := fields[alias]; ok {
			fields[canonical] = v
			delete(fields, alias)
		}
	}
}

// metaArrayColumns enumerates every varchar[] column on `meta` that admin
// can touch through /api/admin/update-meta. Each column is declared
// `NOT NULL DEFAULT '{}'` (see migration 000001_initial.up.sql), so any
// JSON null or untyped slice the frontend sends has to be coerced before
// it reaches lib/pq — otherwise the entire UPDATE rolls back. The set is
// kept narrow to avoid touching unrelated fields. Add new entries here
// when a varchar[] meta column is introduced.
var metaArrayColumns = map[string]struct{}{
	"langs":                        {},
	"pinnedUsers":                  {},
	"hiddenTags":                   {},
	"blockedHosts":                 {},
	"silencedHosts":                {},
	"mediaSilencedHosts":           {},
	"sensitiveWords":               {},
	"prohibitedWords":              {},
	"prohibitedWordsForNameOfUser": {},
	"serverRules":                  {},
	"federationHosts":              {},
	"bannedEmailDomains":           {},
	"preservedUsernames":           {},
}

// coerceMetaArrayFields normalises array-shaped values bound from JSON
// into pq.StringArray for known varchar[] columns on the meta row. It is
// the admin/update-meta only helper — every meta varchar[] column listed
// in metaArrayColumns is NOT NULL with DEFAULT '{}'.
//
// 必要な理由 (#590): JSON decoder は array を []any に decode するが、
// lib/pq の Value driver は []any を varchar[] に変換できず
// "expression is of type record" で PostgreSQL 側エラーになり、UPDATE 全体
// が rollback する。さらに JSON null を送られると nil が pq.StringArray
// 列に流れて NOT NULL 制約違反でも rollback する。両方を空配列もしくは
// pq.StringArray に揃えることで、admin の whitelist / blocklist 設定が
// 期待どおり永続化される。
//
// 関連列以外 (例: rootUserId のような nullable string) は触らない。
// metaArrayColumns に列挙された key のみが coerce 対象。
func coerceMetaArrayFields(fields map[string]any) {
	for k, v := range fields {
		if _, ok := metaArrayColumns[k]; !ok {
			continue
		}
		switch arr := v.(type) {
		case nil:
			// JSON null は admin の「リスト解除」意図と解釈し、空配列に
			// 揃えて NOT NULL 制約違反を避ける (Misskey TS は配列型で
			// declared、null 自体を弾くので mk-go 側で寄せる)。
			fields[k] = pq.StringArray{}
		case []any:
			// 全要素 string なら pq.StringArray、不正型混入 (string 以外)
			// は実 repo に流して error にさせる (型エラーを silently 飲み
			// 込まないため)。
			strs := make([]string, 0, len(arr))
			allStrings := true
			for _, e := range arr {
				s, isStr := e.(string)
				if !isStr {
					allStrings = false
					break
				}
				strs = append(strs, s)
			}
			if allStrings {
				fields[k] = pq.StringArray(strs)
			}
		}
		// pq.StringArray / []string が来ているケースは driver.Valuer 互換
		// なのでそのまま real repo に流す。
	}
}

// --- Role endpoints ---

// RolesCreate handles POST /api/admin/roles/create.
//
// upstream Misskey TS の paramDef は 14 field を required (一部 nullable) で
// 受け取る (name / description / color / iconUrl / target / condFormula /
// isPublic / isModerator / isAdministrator / isExplorable / asBadge /
// canEditMembersByModerator / displayOrder / policies)。mk-go も drop-in
// 互換のため同 field を accept する (#889)。
//
// model.Role の全 column を実際に persist する (PR #1102 で配線完了)。
// 旧版は policies / condFormula / color / iconUrl / target / canEdit
// MembersByModerator を request で受け取りつつ /dev/null に流していた
// ため、admin UI から policy / 色 / アイコン等を設定しても DB に書かれず
// 「ロール設定が反映されない」現象になっていた。
func (h *Handler) RolesCreate(c echo.Context) error {
	var req struct {
		Name                            *string         `json:"name"`
		Description                     *string         `json:"description"`
		Color                           *string         `json:"color"`
		IconURL                         *string         `json:"iconUrl"`
		Target                          *string         `json:"target"`
		CondFormula                     *map[string]any `json:"condFormula"`
		IsPublic                        *bool           `json:"isPublic"`
		IsModerator                     *bool           `json:"isModerator"`
		IsAdministrator                 *bool           `json:"isAdministrator"`
		IsExplorable                    *bool           `json:"isExplorable"`
		AsBadge                         *bool           `json:"asBadge"`
		CanEditMembersByModerator       *bool           `json:"canEditMembersByModerator"`
		PreserveAssignmentOnMoveAccount *bool           `json:"preserveAssignmentOnMoveAccount"`
		DisplayOrder                    *int            `json:"displayOrder"`
		Policies                        *map[string]any `json:"policies"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "Invalid parameters.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	// upstream paramDef の required field を非 nil で要求する (= partial
	// payload を 400 で reject、TS 互換、#889)。
	// color / iconUrl は upstream paramDef で nullable: true。JSON で null
	// 送出時に Go の `*string` が nil になり「field 不在」と区別できない
	// ため required check から除外する (= TS は `null` 可で accept、mk-go
	// では実 require は省略するが TS frontend の payload は通る)。
	if req.Name == nil || *req.Name == "" ||
		req.Description == nil ||
		req.Target == nil ||
		req.CondFormula == nil ||
		req.IsPublic == nil ||
		req.IsModerator == nil ||
		req.IsAdministrator == nil ||
		req.AsBadge == nil ||
		req.CanEditMembersByModerator == nil ||
		req.DisplayOrder == nil ||
		req.Policies == nil {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "Required parameters missing.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	opts := role.CreateOptions{
		Color:                     req.Color,
		IconURL:                   req.IconURL,
		IsPublic:                  *req.IsPublic,
		IsModerator:               *req.IsModerator,
		IsAdministrator:           *req.IsAdministrator,
		AsBadge:                   *req.AsBadge,
		IsExplorable:              req.IsExplorable != nil && *req.IsExplorable,
		DisplayOrder:              *req.DisplayOrder,
		CanEditMembersByModerator: *req.CanEditMembersByModerator,
	}
	if req.PreserveAssignmentOnMoveAccount != nil {
		opts.PreserveAssignmentOnMoveAccount = *req.PreserveAssignmentOnMoveAccount
	}
	// Target は upstream paramDef で `enum: ['manual', 'conditional']`。
	// nest.js framework が unknown を 400 で reject するので、mk-go も
	// silent fallback せず 400 を返す (新規 role なので Update 経路ほど
	// 破壊的ではないが、shape を upstream に揃える)。
	switch *req.Target {
	case string(model.RoleTargetManual):
		opts.Target = model.RoleTargetManual
	case string(model.RoleTargetConditional):
		opts.Target = model.RoleTargetConditional
	default:
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "target must be 'manual' or 'conditional'.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	// CondFormula / Policies は JSON object → datatypes.JSON (= []byte) に Marshal。
	// upstream は object 全体をそのまま column に書くだけで内部構造は consumer 任せ。
	if cf, err := json.Marshal(*req.CondFormula); err == nil {
		opts.CondFormula = cf
	} else {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "condFormula must be a JSON object.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	if pol, err := json.Marshal(*req.Policies); err == nil {
		opts.Policies = pol
	} else {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "policies must be a JSON object.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	r, err := h.roleService.Create(*req.Name, *req.Description, opts)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	h.logModeration(c, moderationlog.LogCreateRole, map[string]any{
		"roleId": r.ID,
		"role":   r,
	})
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
//
// upstream Misskey TS の paramDef は 15 field を accept (roleId 以外はすべて
// optional partial update)。旧 mk-go は 5 field しか accept しておらず、
// admin UI で **policies / color / iconUrl / target / condFormula / asBadge /
// isExplorable / displayOrder / canEditMembersByModerator / preserveAssignment
// OnMoveAccount** を変えても DB に書かれず「ロール設定が反映されない」現象
// になっていた (PR #1102)。特に policies field の欠落は「canPublicNote 等の
// ポリシーが UI 上で trueに見えても実際は default 値のまま」という drop-in
// regression 重大度高めの bug。本 fix で upstream 15 field 全部を pipe する。
func (h *Handler) RolesUpdate(c echo.Context) error {
	var req struct {
		RoleID                          string          `json:"roleId"`
		Name                            *string         `json:"name"`
		Description                     *string         `json:"description"`
		Color                           *string         `json:"color"`
		IconURL                         *string         `json:"iconUrl"`
		Target                          *string         `json:"target"`
		CondFormula                     *map[string]any `json:"condFormula"`
		IsModerator                     *bool           `json:"isModerator"`
		IsAdministrator                 *bool           `json:"isAdministrator"`
		IsPublic                        *bool           `json:"isPublic"`
		IsExplorable                    *bool           `json:"isExplorable"`
		AsBadge                         *bool           `json:"asBadge"`
		CanEditMembersByModerator       *bool           `json:"canEditMembersByModerator"`
		PreserveAssignmentOnMoveAccount *bool           `json:"preserveAssignmentOnMoveAccount"`
		DisplayOrder                    *int            `json:"displayOrder"`
		Policies                        *map[string]any `json:"policies"`
	}
	if err := c.Bind(&req); err != nil || req.RoleID == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "roleId is required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	before, err := h.roleService.Show(req.RoleID)
	if err != nil {
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_ROLE", "No such role.", "07dc7d34-c0d8-458b-9c04-4b18399b1f46"))
	}
	fields := map[string]any{}
	if req.Name != nil {
		fields["name"] = *req.Name
	}
	if req.Description != nil {
		fields["description"] = *req.Description
	}
	if req.Color != nil {
		// mk-go specific drift: upstream は `null` 送出時に column を null
		// クリアするが、Go の `*string` JSON binding では「field 未送出」と
		// 「null 明示」を区別できず両者とも nil pointer になる。frontend が
		// クリア意図で `null` を送ったケースを救済するため、空文字 ("") も
		// null クリアとして扱う運用にする。upstream は "" を空文字として
		// 保存するので、theoretically 「色を文字 0 個に設定」したい場合の
		// 挙動は乖離するが、実用上「色 = empty string」は存在しない (#1102)。
		if *req.Color == "" {
			fields["color"] = nil
		} else {
			fields["color"] = *req.Color
		}
	}
	if req.IconURL != nil {
		// 上記 Color と同じ理由で空文字を null クリア扱いする。
		if *req.IconURL == "" {
			fields["iconUrl"] = nil
		} else {
			fields["iconUrl"] = *req.IconURL
		}
	}
	if req.Target != nil {
		// upstream paramDef は `enum: ['manual', 'conditional']` で nest.js
		// framework が unknown 値を 400 で reject する。mk-go も silent
		// fallback (旧版は unknown → manual) せずに 400 を返す方が、frontend
		// の typo 等で **既存 conditional role が意図せず manual に書き換わる
		// silent corruption** を防げる。
		switch *req.Target {
		case string(model.RoleTargetManual):
			fields["target"] = model.RoleTargetManual
		case string(model.RoleTargetConditional):
			fields["target"] = model.RoleTargetConditional
		default:
			return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "target must be 'manual' or 'conditional'.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
		}
	}
	if req.CondFormula != nil {
		cf, err := json.Marshal(*req.CondFormula)
		if err != nil {
			return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "condFormula must be a JSON object.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
		}
		fields["condFormula"] = cf
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
	if req.IsExplorable != nil {
		fields["isExplorable"] = *req.IsExplorable
	}
	if req.AsBadge != nil {
		fields["asBadge"] = *req.AsBadge
	}
	if req.CanEditMembersByModerator != nil {
		fields["canEditMembersByModerator"] = *req.CanEditMembersByModerator
	}
	if req.PreserveAssignmentOnMoveAccount != nil {
		fields["preserveAssignmentOnMoveAccount"] = *req.PreserveAssignmentOnMoveAccount
	}
	if req.DisplayOrder != nil {
		fields["displayOrder"] = *req.DisplayOrder
	}
	if req.Policies != nil {
		pol, err := json.Marshal(*req.Policies)
		if err != nil {
			return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "policies must be a JSON object.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
		}
		fields["policies"] = pol
	}
	if len(fields) == 0 {
		// 全フィールドが nil = 何も変更しないリクエスト。before == after の
		// 無意味な log を書かずに早期 return する。
		return c.NoContent(http.StatusNoContent)
	}
	after, err := h.roleService.UpdateFields(req.RoleID, fields)
	if err != nil {
		if err == role.ErrRoleNotFound {
			return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_ROLE", "No such role.", "07dc7d34-c0d8-458b-9c04-4b18399b1f46"))
		}
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	h.logModeration(c, moderationlog.LogUpdateRole, map[string]any{
		"roleId": req.RoleID,
		"before": before,
		"after":  after,
	})
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
	// 削除直後だと role 情報を取れないので事前に snapshot を取って log info に含める
	snapshot, _ := h.roleService.Show(req.RoleID)
	if err := h.roleService.Delete(req.RoleID); err != nil {
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_ROLE", "No such role.", "07dc7d34-c0d8-458b-9c04-4b18399b1f46"))
	}
	h.logModeration(c, moderationlog.LogDeleteRole, map[string]any{
		"roleId": req.RoleID,
		"role":   snapshot,
	})
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
	h.logRoleAssignment(c, moderationlog.LogAssignRole, req.UserID, req.RoleID)
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
	h.logRoleAssignment(c, moderationlog.LogUnassignRole, req.UserID, req.RoleID)
	return c.NoContent(http.StatusNoContent)
}

// RolesUsers handles POST /api/admin/roles/users.
func (h *Handler) RolesUsers(c echo.Context) error {
	var req struct {
		RoleID  string `json:"roleId"`
		Limit   int    `json:"limit"`
		SinceID string `json:"sinceId"`
		UntilID string `json:"untilId"`
	}
	if err := c.Bind(&req); err != nil || req.RoleID == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "roleId is required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	limit := req.Limit
	if limit <= 0 {
		limit = 10
	}
	if limit > 100 {
		limit = 100
	}

	assignments, err := h.roleService.ListByRole(req.RoleID, req.UntilID, req.SinceID, limit)
	if err != nil {
		if err == role.ErrRoleNotFound {
			return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_ROLE", "No such role.", "07dc7d34-c0d8-458b-9c04-4b18399b1f46"))
		}
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}

	// Profile を per-row 引いていた N+1 を 1 batch query に集約 (#503 と同じ動機)。
	ids := make([]string, 0, len(assignments))
	for _, a := range assignments {
		if a.User != nil {
			ids = append(ids, a.User.ID)
		}
	}
	// Profile fetch 失敗は handler 全体の致命扱いではないので、空 map で
	// fallback する (各 user は profile=nil で pack される)。ShowUsers と同方針。
	profiles, _ := h.userRepo.FindProfilesByUserIDs(ids)
	profileByUser := make(map[string]*model.UserProfile, len(profiles))
	for _, p := range profiles {
		profileByUser[p.UserID] = p
	}

	// Misskey TS は admin/roles/users の返却を { id, createdAt, user } の配列で
	// 包むため互換のため同じ envelope に揃える (UI の Roles 詳細ページが
	// assignment.user を直接参照している)。createdAt は assignment.id (ULID) から
	// 復元 (User と同じく ID 由来 timestamp)。
	result := make([]map[string]any, 0, len(assignments))
	for _, a := range assignments {
		if a.User == nil {
			// role_assignment は残っているのに user 行が消えているデータ不整合。
			// 結果件数だけ silent に減らすと debug が困難なので警告ログを出して
			// admin tooling 側で清掃の signal にする (#598 review item 1)。
			slog.Warn("admin/roles/users: dangling role assignment",
				"assignmentId", a.ID, "userId", a.UserID, "roleId", a.RoleID)
			continue
		}
		createdAt := ""
		if t, err := h.idGen.ParseTime(a.ID); err == nil {
			createdAt = t.UTC().Format("2006-01-02T15:04:05.000Z")
		}
		result = append(result, map[string]any{
			"id":        a.ID,
			"createdAt": createdAt,
			"user":      h.packAdminUser(a.User, profileByUser[a.User.ID]),
		})
	}
	return c.JSON(http.StatusOK, result)
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
//
// upstream Misskey TS は `fileId` を必須として drive 経由でしか emoji を
// 登録できない設計だが、mk-go は legacy で `url` 直接受けも維持する。両 path
// をサポートして drop-in 互換を保つ:
//   - `fileId` 指定時: drive_file から URL を resolve して保存 (= upstream 互換)
//   - `url` 直接指定時: そのまま保存 (legacy)
//   - 両方なし: 400 INVALID_PARAM
func (h *Handler) EmojiAdd(c echo.Context) error {
	var req struct {
		Name   string `json:"name"`
		URL    string `json:"url"`
		FileID string `json:"fileId"`
	}
	if err := c.Bind(&req); err != nil || req.Name == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "name is required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	if h.emojiRepo == nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	url := req.URL
	if url == "" && req.FileID != "" && h.driveFileRepo != nil {
		f, err := h.driveFileRepo.FindByID(req.FileID)
		if err != nil {
			return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_FILE", "No such file.", "fc46b5a4-6b92-4c33-ac66-b806659bb5cf"))
		}
		url = f.URL
	}
	if url == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "url or fileId is required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	e := &model.Emoji{
		ID:          h.idGen.Generate(time.Now()),
		Name:        req.Name,
		OriginalURL: url,
		PublicURL:   url,
	}
	if err := h.emojiRepo.Create(e); err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	h.logModeration(c, moderationlog.LogAddCustomEmoji, map[string]any{
		"emojiId": e.ID,
		"emoji":   e,
	})
	return c.JSON(http.StatusOK, e)
}

// EmojiUpdate handles POST /api/admin/emoji/update.
//
// Misskey TS の admin/emoji/update は name/category/aliases に加え license/
// isSensitive/localOnly も受け付ける。フロントの編集ダイアログがこれら全部
// 送信するため Request struct を Misskey 互換に拡張する (#650 問題 2)。
//
// Aliases は []string なので nil (省略) と [] (空配列) が型で区別できない。
// Misskey TS 側も optional `?` で undefined と空配列を区別しないため、
// nil != 空配列でない場合に "aliases を全削除" として扱う。実装上は
// `req.Aliases != nil` で「フロントが aliases フィールドを明示送信した」
// と判定しており、空配列送信なら全削除、フィールド欠落なら現状維持。
func (h *Handler) EmojiUpdate(c echo.Context) error {
	var req struct {
		ID          string   `json:"id"`
		Name        *string  `json:"name"`
		Category    *string  `json:"category"`
		Aliases     []string `json:"aliases"`
		License     *string  `json:"license"`
		IsSensitive *bool    `json:"isSensitive"`
		LocalOnly   *bool    `json:"localOnly"`
	}
	if err := c.Bind(&req); err != nil || req.ID == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "id is required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	if h.emojiRepo == nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	// before snapshot for moderation log (also used to gate NO_SUCH_EMOJI when
	// the row is missing — UpdateFields の RowsAffected==0 経路は repo 側で
	// ErrRecordNotFound に昇格済み (#650 問題 2)).
	before, err := h.emojiRepo.FindByID(req.ID)
	if err != nil {
		// #729: 再現報告のため request 側 id と原因 (find vs update path) を
		// log に残す。frontend 側で stale id を握っているのか、repo 側で
		// 何か起きているのか切り分けに使う。
		slog.WarnContext(c.Request().Context(), "EmojiUpdate: NO_SUCH_EMOJI on FindByID",
			"id", req.ID, "err", err)
		return c.JSON(http.StatusNotFound, apierr.NoSuchEmoji())
	}
	fields := map[string]any{}
	if req.Name != nil {
		fields["name"] = *req.Name
	}
	if req.Category != nil {
		fields["category"] = *req.Category
	}
	if req.Aliases != nil {
		// `[]string` を直接 GORM に渡すと PostgreSQL の varchar[] 列に対して
		// NULL として serialize される (#729)。`pq.StringArray` で wrap する
		// と空 slice 含めて `'{}'` PostgreSQL array リテラルに正しく変換さ
		// れる。Aliases 列は NOT NULL DEFAULT '{}' なので NULL 書き込みは
		// 即制約違反でエラーになっていた。
		fields["aliases"] = pq.StringArray(req.Aliases)
	}
	if req.License != nil {
		fields["license"] = *req.License
	}
	if req.IsSensitive != nil {
		fields["isSensitive"] = *req.IsSensitive
	}
	if req.LocalOnly != nil {
		fields["localOnly"] = *req.LocalOnly
	}
	if len(fields) == 0 {
		// 何も変更しないリクエストは log を書かずに 204 で返す。
		return c.NoContent(http.StatusNoContent)
	}
	if err := h.emojiRepo.UpdateFields(req.ID, fields); err != nil {
		// #729: FindByID 直後の RowsAffected==0 はほぼ起こらない (concurrent
		// delete 等のレース) ので診断 log で気付けるようにする。
		slog.WarnContext(c.Request().Context(), "EmojiUpdate: NO_SUCH_EMOJI on UpdateFields",
			"id", req.ID, "fields", fieldKeys(fields), "err", err)
		return c.JSON(http.StatusNotFound, apierr.NoSuchEmoji())
	}
	after, err := h.emojiRepo.FindByID(req.ID)
	if err != nil {
		// ここに到達するのは UpdateFields 直後の row 消失 (race) のみ。
		// 操作自体は成功しているので 204 を返す。
		return c.NoContent(http.StatusNoContent)
	}
	h.logModeration(c, moderationlog.LogUpdateCustomEmoji, map[string]any{
		"emojiId": req.ID,
		"before":  before,
		"after":   after,
	})
	return c.NoContent(http.StatusNoContent)
}

// fieldKeys は map のキー一覧を sort 済み slice で返す診断 log 用 helper。
// log 行に value を直接含めると long string が混じって読みにくいので key
// のみ列挙する。
func fieldKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
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
	// log info に snapshot を含めるため削除前に取得。取得失敗は NO_SUCH_EMOJI。
	snapshot, err := h.emojiRepo.FindByID(req.ID)
	if err != nil {
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_EMOJI", "No such emoji.", "684b7e7e-7e91-4e4c-a5cc-8050e4b8e0d8"))
	}
	if err := h.emojiRepo.Delete(req.ID); err != nil {
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_EMOJI", "No such emoji.", "684b7e7e-7e91-4e4c-a5cc-8050e4b8e0d8"))
	}
	h.logModeration(c, moderationlog.LogDeleteCustomEmoji, map[string]any{
		"emojiId": req.ID,
		"emoji":   snapshot,
	})
	return c.NoContent(http.StatusNoContent)
}

// EmojiList handles POST /api/admin/emoji/list.
func (h *Handler) EmojiList(c echo.Context) error {
	var req struct {
		Query    string `json:"query"`
		Category string `json:"category"`
		SinceID  string `json:"sinceId"`
		UntilID  string `json:"untilId"`
		Limit    int    `json:"limit"`
		Offset   int    `json:"offset"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "Invalid parameters.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	if h.emojiRepo == nil {
		return c.JSON(http.StatusOK, []any{})
	}
	emojis, err := h.emojiRepo.ListWithFilter(req.Query, req.Category, true, req.SinceID, req.UntilID, req.Limit, req.Offset)
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
			URI:           req.Query.URI,
			PublicURL:     req.Query.PublicURL,
			OriginalURL:   req.Query.OriginalURL,
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
	// URI / PublicURL / OriginalURL は upstream Misskey の v2 検索 schema
	// で受け付けている filter (#466)。frontend (custom-emojis-manager.remote
	// .vue) の検索ボックスから送られてくるため、handler 側で受けないと
	// silently 無視される。受信したら repo の WHERE 句に展開する。
	URI         string `json:"uri"`
	PublicURL   string `json:"publicUrl"`
	OriginalURL string `json:"originalUrl"`
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
//
// model.ModerationLog は createdAt 列を持たず aidx ID 先頭 8 文字に
// timestamp を埋め込んでいる。frontend (modlog.ModLog.vue) は
// `<MkTime :time="log.createdAt"/>` を直接読むため、handler 側で aidx
// から派生した createdAt 文字列を response に注入する。invite/users 系
// と同じく "2006-01-02T15:04:05.000Z" 形式 (Misskey の標準)。
func (h *Handler) ShowModerationLogs(c echo.Context) error {
	var req struct {
		Limit  int `json:"limit"`
		Offset int `json:"offset"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "Invalid parameters.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	logs, err := h.modLogService.List(req.Limit, req.Offset)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	out := make([]map[string]any, 0, len(logs))
	for _, l := range logs {
		m := map[string]any{
			"id":     l.ID,
			"userId": l.UserID,
			"type":   l.Type,
			"info":   l.Info,
		}
		if s, err := aidxCreatedAtString(h.idGen, l.ID); err == nil {
			m["createdAt"] = s
		} else if !errors.Is(err, ErrIDGenMissing) {
			// idGen は wired されているのに parse 失敗した場合のみログに残す。
			// 非 aidx 形式の legacy ID 等で createdAt が出せない時に frontend
			// 側で「Invalid Date」が出る原因を後追いできるようにする。
			slog.DebugContext(c.Request().Context(), "modlog: createdAt derive failed",
				"logId", l.ID, "err", err)
		}
		if l.User != nil {
			m["user"] = l.User
		}
		out = append(out, m)
	}
	return c.JSON(http.StatusOK, out)
}
