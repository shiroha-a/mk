package admin

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/lib/pq"
	"github.com/shiroha-a/mk/internal/api/apierr"
	"github.com/shiroha-a/mk/internal/api/pagination"
	"github.com/shiroha-a/mk/internal/config"
	"github.com/shiroha-a/mk/internal/core/captcha"
	"github.com/shiroha-a/mk/internal/core/moderationlog"
	"github.com/shiroha-a/mk/internal/core/role"
	"github.com/shiroha-a/mk/internal/core/signup"
	corewebhook "github.com/shiroha-a/mk/internal/core/webhook"
	"github.com/shiroha-a/mk/internal/core/webpush"
	"github.com/shiroha-a/mk/internal/entity"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/queue"
	"github.com/shiroha-a/mk/internal/repository"
	"github.com/shiroha-a/mk/internal/server/middleware"
	"gorm.io/datatypes"
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
	signupService *signup.Service
	roleService   *role.Service
	metaRepo      repository.MetaRepository
	userRepo      repository.UserRepository
	abuseRepo     repository.AbuseReportRepository
	modLogService *moderationlog.Service
	emojiRepo     repository.EmojiRepository
	driveFileRepo repository.DriveFileRepository
	// driveBulkDeleter は federation/delete-all-files で host のファイルを物理
	// ストレージ込みで削除し drive 使用量を減算する (#1772)。未配線時は
	// driveFileRepo.DeleteByHost に degrade (row のみ削除)。
	driveBulkDeleter      DriveBulkDeleter
	adminDB               *gorm.DB
	userIPRepo            repository.UserIPRepository
	queueInspector        QueueInspector
	queueRedis            QueueRedisInfoProvider
	emojiEnqueuer         EmojiImportEnqueuer
	emojiImageFetcher     EmojiImageFetcher
	relayService          RelayService
	abuseForwarder        AbuseForwarder
	deleteAccountEnqueuer DeleteAccountEnqueuer
	systemWebhookRepo     repository.SystemWebhookRepository
	recipientRepo         repository.AbuseReportNotificationRecipientRepository
	// systemWebhookDispatcher は resolve-abuse-user-report 時に
	// abuseReportResolved system webhook を発火するための dispatcher
	// (*core/webhook.Service)。nil なら発火しない (#1723)。
	systemWebhookDispatcher SystemWebhookDispatcher
	adRepo                  repository.AdRepository
	avatarDecoRepo          repository.AvatarDecorationRepository
	inviteRepo              repository.RegistrationTicketRepository
	promoNoteRepo           repository.PromoNoteRepository
	noteFinder              NoteFinder
	smtpProxyURL            string
	serverURL               string
	idGen                   id.Generator
	configSetupPassword     string
	instanceMetadataFetcher InstanceMetadataFetcher
	systemAccountFetcher    SystemAccountFetcher
	followingRepo           repository.FollowingRepository
	unfollowEnqueuer        UnfollowEnqueuer
	// followReqRepo は suspend 時に対象 user の双方向 followRequest を削除する
	// ために使う (#1759, upstream UserSuspendService.postSuspend)。nil なら skip。
	followReqRepo repository.FollowRequestRepository
	// storageDeleter は admin の bulk drive cleanup で物理オブジェクトを消す
	// object storage backend (nil なら DB 行のみ削除)。
	storageDeleter StorageDeleter
	// captchaVerifierFactory builds a one-off captcha.Verifier for
	// admin/captcha/save verification. nil uses the real providers; tests
	// inject a stub to avoid external HTTP calls.
	captchaVerifierFactory CaptchaVerifierFactory
	// instanceRepo は admin/federation/* の instance lookup / update を
	// inject 可能にするための DI 口 (#676)。FederationUpdateInstance の
	// log type 分岐をテストするため、`adminDB` 直叩きから repository 経由
	// に剥がしている。未配線時は `adminDB` フォールバックは持たず no-op。
	instanceRepo repository.InstanceRepository
	// userTokenInvalidator は admin が他 user を suspend / unsuspend /
	// 論理削除した直後に target user の全 tokenCache entry を即時失効する
	// ために使う (#965)。i/regenerate-token (#884) や i/update (#960) と
	// 同じ AuthMiddleware が「token 単独削除」「user 全 token 削除」の 2
	// 種類の API を提供しており、admin 経由は後者を使う。production では
	// router で必ず wire する (未配線時は 30s cache TTL 待ちで stale 旧 user
	// が auth 通過する security regression が残る)。
	userTokenInvalidator UserTokenInvalidator
	// signinRepo は admin/show-user の `signins` field を実データで埋める
	// ために使う (#1198)。未配線時は `[]` fallback で shape compat を保つ。
	signinRepo repository.SigninRepository
	// instanceSuspendInvalidator は admin が remote instance を suspend /
	// unsuspend した直後に deliver hot path の suspend 判定 cache を即時失効
	// するために使う (#1407 review)。未配線時は最大 cacheTTL (5min) 待ちで
	// stale な配送可否判定が残るが security regression ではないため optional。
	instanceSuspendInvalidator InstanceSuspendCacheInvalidator
	// userModerationFed は suspend / delete-account / unsuspend 時に対象 local
	// user の AP Delete / Undo(Delete) を remote instances へ配信する (#1759)。
	// nil なら連合副作用は no-op (security guard / DB 更新には影響しない)。
	userModerationFed UserModerationFederationHook
}

// UserModerationFederationHook delivers AP Delete / Undo(Delete) for a local
// user's actor to remote instances when the account is suspended, deleted, or
// unsuspended (#1759). Implemented by core/federation.UserModerationDeliveryHook.
// nil disables the federation side-effect.
type UserModerationFederationHook interface {
	// OnUserDeleted は suspend / delete-account で Delete(actor) を配信する。
	OnUserDeleted(user *model.User)
	// OnUserRestored は unsuspend で Undo(Delete) を配信する。
	OnUserRestored(user *model.User)
}

// SetUserModerationFederationHook wires the AP delivery hook used on
// suspend / delete-account / unsuspend (#1759). nil disables federation.
func (h *Handler) SetUserModerationFederationHook(hook UserModerationFederationHook) {
	h.userModerationFed = hook
}

// SetFollowRequestRepo wires the follow-request repository so SuspendUser can
// delete the target's pending follow requests (both directions, #1759).
func (h *Handler) SetFollowRequestRepo(r repository.FollowRequestRepository) {
	h.followReqRepo = r
}

// InstanceSuspendCacheInvalidator drops the cached delivery-suspend decision
// for a host so the next ShouldSkipDelivery re-reads it from the DB. Implemented
// by core/instance.Service. Used by admin/federation/update-instance which
// writes suspensionState through instanceRepo directly (bypassing
// instance.Service.Suspend) — see #1407 review.
type InstanceSuspendCacheInvalidator interface {
	InvalidateSuspendCache(host string)
}

// SetInstanceSuspendCacheInvalidator wires the instance Service so
// admin/federation/update-instance flushes the deliver suspend cache for the
// affected host immediately. production では wire 推奨 (未配線時は最大 5min
// stale window が残るが、issue #1407 が許容する範囲)。
func (h *Handler) SetInstanceSuspendCacheInvalidator(inv InstanceSuspendCacheInvalidator) {
	h.instanceSuspendInvalidator = inv
}

// invalidateInstanceSuspendCache は host の deliver suspend 判定 cache を即時
// 失効させる helper。invalidator 未配線 / host 空のときは noop。
func (h *Handler) invalidateInstanceSuspendCache(host string) {
	if h.instanceSuspendInvalidator == nil || host == "" {
		return
	}
	h.instanceSuspendInvalidator.InvalidateSuspendCache(host)
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

// SetSigninRepo wires a SigninRepository so admin/show-user can populate the
// target user's signin history. Without it the `signins` field falls back to
// an empty array (#1198).
func (h *Handler) SetSigninRepo(r repository.SigninRepository) { h.signinRepo = r }

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

// SystemWebhookDispatcher fans a system-level event out to subscribed system
// webhooks, optionally excluding specific webhook IDs. Implemented by
// *core/webhook.Service. Used to fire abuseReportResolved on report resolution
// (#1723).
type SystemWebhookDispatcher interface {
	DispatchSystemExcluding(eventType string, body any, excludes []string)
	// DispatchSystemTest enqueues a single test delivery to a system webhook
	// (admin/system-webhook/test, #1542). overrideURL/Secret が非空ならそちらへ。
	DispatchSystemTest(webhookID, eventType string, body any, overrideURL, overrideSecret string)
}

// SetSystemWebhookDispatcher wires the system webhook dispatcher used to emit
// abuseReportResolved. nil disables firing (DB update still happens).
func (h *Handler) SetSystemWebhookDispatcher(d SystemWebhookDispatcher) {
	h.systemWebhookDispatcher = d
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

// CaptchaVerifierFactory builds a captcha.Verifier for the given provider and
// the request-supplied secret / sitekey / instanceUrl. Used by
// admin/captcha/save to verify the captchaResult before persisting settings.
type CaptchaVerifierFactory func(provider, secret, sitekey, instanceURL string) captcha.Verifier

// SetCaptchaVerifierFactory overrides the captcha verifier construction used by
// admin/captcha/save (tests inject a stub to avoid external HTTP).
func (h *Handler) SetCaptchaVerifierFactory(f CaptchaVerifierFactory) {
	h.captchaVerifierFactory = f
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
	ListCompletedTasks(qname string, page, pageSize int) ([]*QueueTaskSummary, error)
	ListFailedTasks(qname string, page, pageSize int) ([]*QueueTaskSummary, error)
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
	EnqueuedAt    time.Time
	ProcessedAt   time.Time
	CompletedAt   time.Time
	// ProcessedBy is the worker name that last dequeued the job (BullMQ
	// job.processedBy). Output as the upstream optional QueueJob.processedBy.
	ProcessedBy string
}

// SetDriveFileRepo attaches a DriveFileRepository for admin drive operations.
func (h *Handler) SetDriveFileRepo(r repository.DriveFileRepository) {
	h.driveFileRepo = r
}

// StorageDeleter removes a stored object by its access key (drive.Storage の
// Delete に対応)。admin/drive/clean-remote-files と delete-all-files-of-a-user で
// DB 行削除前に object storage の物理オブジェクトを消すために使う。
type StorageDeleter interface {
	Delete(accessKey string) error
}

// SetStorageDeleter wires the object-storage backend so bulk drive cleanups can
// remove physical objects (not just DB rows). nil keeps the DB-only behaviour.
func (h *Handler) SetStorageDeleter(s StorageDeleter) {
	h.storageDeleter = s
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
		"badgeRoles":        badgeRolesForMap(detailed.BadgeRoles),
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

	// 非 administrator (= moderator) は他 administrator の情報を閲覧できない
	// (upstream show-user.ts:223-226 の 'cannot show info of admin' guard)。
	if h.roleService != nil {
		me := middleware.GetUser(c)
		if me != nil && !h.roleService.IsAdministrator(me.ID) && h.roleService.IsAdministrator(user.ID) {
			return c.JSON(http.StatusForbidden, apierr.Error("ACCESS_DENIED", "Cannot show info of admin.", "0d4e3a3e-2c1f-4d8b-9d2a-7a0c1c1b2f3a"))
		}
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
		Username string `json:"username"`
		Sort     string `json:"sort"`
		Limit    int    `json:"limit"`
		Offset   int    `json:"offset"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "Invalid parameters.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}

	req.Limit = pagination.ClampLimit(req.Limit, 10, 100)
	users, err := h.userRepo.ListUsers(model.UserListFilter{
		State:    req.State,
		Origin:   req.Origin,
		Hostname: req.Hostname,
		Username: req.Username,
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

	// upstream show-users.ts:117 は packMany(users, me, {schema:'UserDetailed'})
	// で UserDetailed を返す。admin detail packer (packAdminUser) を使うと
	// email / signins / roleAssigns / notificationRecieveConfig といった admin
	// 専用 field が一覧経由で漏れ、show-user の admin guard を迂回できてしまう
	// (moderator が admin の連絡先や signin IP を取得可能)。一覧は UserDetailed
	// に揃えて過剰露出を防ぐ。
	result := make([]entity.UserDetailed, 0, len(users))
	for _, u := range users {
		result = append(result, entity.PackUserDetailed(u, profileByUser[u.ID], h.idGen))
	}
	return c.JSON(http.StatusOK, result)
}

// badgeRolesForMap returns badgeRoles for admin map-based responses. The packer
// now returns *[]any (nil = absent for remote users with
// showRoleBadgesOfRemoteUsers off, #1653), but admin endpoints serialise via
// map[string]any which ignores omitempty — a nil pointer would emit
// schema-invalid `null` (UserLite.badgeRoles is nullable:false). Coerce nil to
// `[]` so the admin response keeps its previous `[]` shape.
func badgeRolesForMap(br *[]any) []any {
	if br == nil {
		return []any{}
	}
	return *br
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
		"badgeRoles": badgeRolesForMap(detailed.BadgeRoles),
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
		// moderation 関連の実データ (upstream show-user.ts:234-253)。旧実装は
		// map literal の固定値 ("" / nil / {}) のままだった。
		resp["followedMessage"] = profile.FollowedMessage
		if profile.ModerationNote != nil {
			resp["moderationNote"] = *profile.ModerationNote
		} else {
			resp["moderationNote"] = ""
		}
		resp["notificationRecieveConfig"] = metaJSONValue(profile.NotificationRecieveConfig, map[string]any{})
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
		// isSilenced は role policy 由来 (canPublicNote を否定する role を持つか)。
		// upstream admin/show-user も roleService から算出する。map literal の
		// false 固定をここで上書きする。
		resp["isSilenced"] = h.roleService.IsSilenced(u.ID)
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
	// signins / roleAssigns は upstream admin/show-user shape の必須 field
	// (#888)。frontend admin moderation view が user の signin 履歴と role
	// 割当履歴を直接参照するので実データで埋める (#1198)。repo / service
	// 未配線や lookup 失敗時は frontend の `.map(...)` が例外を吐かないよう
	// 空配列に fallback し slog.Warn で観測する (roles の扱いと揃え)。
	resp["signins"] = h.packUserSignins(u.ID)
	resp["roleAssigns"] = h.packUserRoleAssigns(u.ID)
	resp["isHibernated"] = u.IsHibernated
	if u.LastActiveDate != nil {
		resp["lastActiveDate"] = u.LastActiveDate.UTC().Format("2006-01-02T15:04:05.000Z")
	} else {
		resp["lastActiveDate"] = nil
	}
	return resp
}

// packUserSignins returns the user's signin history in the admin/show-user
// `signins` shape. Upstream returns every row (signinsRepository.findBy);
// we pass limit=-1 so GORM omits the LIMIT clause and matches that. Rows come
// back newest-first (ListByUserID's default order); upstream leaves the order
// unspecified so this is a benign deviation. Returns an empty slice (never
// nil) so the JSON field is always `[]` for callers without a wired signin
// repository or on lookup failure.
func (h *Handler) packUserSignins(userID string) []map[string]any {
	out := []map[string]any{}
	if h.signinRepo == nil {
		return out
	}
	signins, err := h.signinRepo.ListByUserID(userID, -1, "", "")
	if err != nil {
		slog.Warn("admin/show-user: failed to load signins", "userId", userID, "err", err)
		return out
	}
	for _, s := range signins {
		if packed := entity.PackSignin(s, h.idGen); packed != nil {
			out = append(out, packed)
		}
	}
	return out
}

// packUserRoleAssigns returns the user's active role assignments in the
// admin/show-user `roleAssigns` shape ({createdAt, expiresAt, roleId}).
// createdAt is derived from the aidx-encoded assignment ID. Mirrors upstream
// which maps roleService.getUserAssigns (expired assignments excluded).
func (h *Handler) packUserRoleAssigns(userID string) []map[string]any {
	out := []map[string]any{}
	if h.roleService == nil {
		return out
	}
	assigns, err := h.roleService.GetUserAssigns(userID)
	if err != nil {
		slog.Warn("admin/show-user: failed to load role assignments", "userId", userID, "err", err)
		return out
	}
	const tsFormat = "2006-01-02T15:04:05.000Z"
	for _, a := range assigns {
		entry := map[string]any{
			"roleId":    a.RoleID,
			"expiresAt": nil,
		}
		if t, perr := h.idGen.ParseTime(a.ID); perr == nil {
			entry["createdAt"] = t.UTC().Format(tsFormat)
		}
		if a.ExpiresAt != nil {
			entry["expiresAt"] = a.ExpiresAt.UTC().Format(tsFormat)
		}
		out = append(out, entry)
	}
	return out
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

	// upstream suspend-user.ts: モデレーター/管理者 (root を含む) アカウントは
	// 凍結できない。moderator が他の moderator/admin を凍結する権限昇格を防ぐ。
	if user.IsRoot || (h.roleService != nil && h.roleService.IsModerator(user.ID)) {
		return c.JSON(http.StatusForbidden, apierr.Error("ACCESS_DENIED", "Cannot suspend a moderator account.", "1fb7cb09-d46a-4fff-b8df-057708cce513"))
	}

	if err := h.userRepo.UpdateUser(req.UserID, map[string]any{"isSuspended": true}); err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	// 凍結直後の auth bypass 防止 (#965)。target の全 token cache entry を
	// 即時削除し、middleware 通過後の P2 gate (#964) に依存せず確実に弾く。
	h.invalidateUserTokenCache(req.UserID)
	// upstream UserSuspendService.suspend: local user なら全 sharedInbox へ
	// Delete(actor) を配信する (#1759)。best-effort (queue 経由)。
	if h.userModerationFed != nil {
		h.userModerationFed.OnUserDeleted(user)
	}
	// upstream postSuspend + unFollowAll: 双方向 followRequest 削除 + outgoing
	// follow の全 unfollow (#1759)。best-effort。
	h.cleanupSuspendedUserRelations(req.UserID)
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
	// upstream UserSuspendService.unsuspend: local user なら全 sharedInbox へ
	// Undo(Delete) を配信する (#1759)。
	if h.userModerationFed != nil {
		h.userModerationFed.OnUserRestored(user)
	}
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

// metaJSONValue decodes a jsonb meta column (clientOptions /
// deliverSuspendedSoftware) into a generic value for the admin/meta response.
// Empty or invalid JSON falls back to the provided default so the frontend
// always sees the expected object/array shape (upstream returns the raw column
// which defaults to '{}' / '[]').
func metaJSONValue(raw []byte, fallback any) any {
	if len(raw) == 0 {
		return fallback
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil || out == nil {
		return fallback
	}
	return out
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
		"version": config.MisskeyVersion, "uri": h.serverURL,
		"name": m.Name, "shortName": m.ShortName, "description": m.Description,
		"langs": m.Langs, "pinnedUsers": m.PinnedUsers,
		"hiddenTags": m.HiddenTags, "blockedHosts": m.BlockedHosts,
		"silencedHosts": m.SilencedHosts, "sensitiveWords": m.SensitiveWords,
		"prohibitedWords": m.ProhibitedWords,
		"themeColor":      m.ThemeColor, "bannerUrl": m.BannerURL,
		"backgroundImageUrl": m.BackgroundImageURL, "logoImageUrl": m.LogoImageURL,
		"iconUrl":       m.IconURL,
		"app192IconUrl": m.App192IconURL, "app512IconUrl": m.App512IconURL,
		"defaultLightTheme": m.DefaultLightTheme, "defaultDarkTheme": m.DefaultDarkTheme,
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
		"privacyPolicyUrl": m.PrivacyPolicyURL, "inquiryUrl": m.InquiryURL,
		// Federation
		"federation": m.Federation, "federationHosts": m.FederationHosts,
		"enableFanoutTimeline":           m.EnableFanoutTimeline,
		"enableFanoutTimelineDbFallback": m.EnableFanoutTimelineDbFallback,
		"proxyRemoteFiles":               m.ProxyRemoteFiles,
		"signToActivityPubGet":           m.SignToActivityPubGet,
		// Policies (upstream は { ...DEFAULT_POLICIES, ...instance.policies })
		"policies": role.MergeMetaPolicies(m.Policies),
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
		"singleUserMode":                    m.SingleUserMode,
		"allowExternalApRedirect":           m.AllowExternalApRedirect,
		// Images
		"serverErrorImageUrl": m.ServerErrorImageURL, "notFoundImageUrl": m.NotFoundImageURL,
		"infoImageUrl": m.InfoImageURL, "mascotImageUrl": m.MascotImageURL,
		// Misc
		"translatorAvailable": m.DeeplAuthKey != nil,
		"notesPerOneAd":       m.NotesPerOneAd,
		"clientOptions":       metaJSONValue(m.ClientOptions, map[string]any{}),
		"deeplAuthKey":        m.DeeplAuthKey, "deeplIsPro": m.DeeplIsPro,
		"googleAnalyticsMeasurementId": m.GoogleAnalyticsMeasurementID,
		"manifestJsonOverride":         m.ManifestJSONOverride,
		"bannedEmailDomains":           m.BannedEmailDomains,
		"mediaSilencedHosts":           m.MediaSilencedHosts,
		"preservedUsernames":           m.PreservedUsernames,
		"prohibitedWordsForNameOfUser": m.ProhibitedWordsForNameOfUser,
		"deliverSuspendedSoftware":     metaJSONValue(m.DeliverSuspendedSoftware, []any{}),
		"verifymailAuthKey":            m.VerifymailAuthKey, "truemailAuthKey": m.TruemailAuthKey, "truemailInstance": m.TruemailInstance,
		// proxyAccountId は frontend admin/settings 画面が読み込み時に
		// users/show でこの ID を引くため、必ず非空でなければ画面が
		// 真っ白になる (#348)。本家 SystemAccountService.fetch('proxy')
		// と同じく lazy 作成して埋める。
		"proxyAccountId": h.fetchProxyAccountID(),
		// URL Preview
		"urlPreviewEnabled":              m.URLPreviewEnabled,
		"urlPreviewTimeout":              m.URLPreviewTimeout,
		"urlPreviewMaximumContentLength": m.URLPreviewMaximumContentLength,
		"urlPreviewRequireContentLength": m.URLPreviewRequireContentLength,
		"urlPreviewUserAgent":            m.URLPreviewUserAgent,
		"urlPreviewSummaryProxyUrl":      m.URLPreviewSummaryProxyURL,
		"urlPreviewAllowRedirect":        m.URLPreviewAllowRedirect,
		// upstream: summalyProxy は urlPreviewSummaryProxyUrl の別名で同値を返す
		"summalyProxy": m.URLPreviewSummaryProxyURL,
		// Timeline cache
		"perLocalUserUserTimelineCacheMax":  m.PerLocalUserUserTimelineCacheMax,
		"perRemoteUserUserTimelineCacheMax": m.PerRemoteUserUserTimelineCacheMax,
		"perUserHomeTimelineCacheMax":       m.PerUserHomeTimelineCacheMax,
		"perUserListTimelineCacheMax":       m.PerUserListTimelineCacheMax,
		// Remote notes cleaning
		"remoteNotesCleaningExpiryDaysForEachNotes":         m.RemoteNotesCleaningExpiryDaysForEachNotes,
		"remoteNotesCleaningMaxProcessingDurationInMinutes": m.RemoteNotesCleaningMaxProcessingDurationInMinutes,
		// Visitor
		"ugcVisibilityForVisitor": m.UgcVisibilityForVisitor,
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
	// upstream update-meta.ts の enum 制約を持つ field を pre-validate (#1108
	// sweep)。silent fallback で不正値が DB に書かれる drift を防ぐ:
	// sensitiveMediaDetection が "purple" のような不正値で保存されると
	// downstream consumer (media detection service) が default に倒れて
	// admin の意図と乖離する。federation / ugcVisibilityForVisitor は
	// drastic な network 遮断や匿名 view 不可化を引き起こすので、こちらは
	// silent corruption 防止としても重要。
	if err := validateUpdateMetaEnums(fields); err != nil {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", err.Error(), "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	// frontend が送る API 名 → DB カラム名の差異を吸収する (#348)。API は
	// 本家互換の camelCase alias (tosUrl 等) を使うが、DB 側は
	// packages/backend/src/models/Meta.ts と同じ正規名で保持している。
	// alias が frontend から来たら DB カラム名に translate して渡す。
	renameUpdateMetaFields(fields)

	// upstream update-meta.ts の値正規化 (host lowercase/sort/dedup、空文字→null、
	// URL 検証、trim) を再現する。coerceMetaArrayFields より前に走らせ、host 系は
	// ここで pq.StringArray 化まで済ませる (coerce 側は pass-through になる)。
	normalizeUpdateMetaFields(fields)

	// JSON Bind 後の []any{...} (中身は string) を pq.StringArray に揃える
	// (#590)。GORM は map[string]any を Updates() に渡すと値の型をそのまま
	// driver に流すため、pq.StringArray (= driver.Valuer 実装) に変換しない
	// と varchar[] 列の UPDATE が `expression is of type record` で失敗する。
	// これは federationHosts / blockedHosts / silencedHosts / langs 等
	// すべての varchar[] 列に影響していたバグで、admin が whitelist 連合や
	// host ブロックを保存しても永続化されない原因だった。
	coerceMetaArrayFields(fields)
	// clientOptions は upstream update-meta.ts と同じく既存値への shallow merge
	// (`{...serverSettings.clientOptions, ...ps.clientOptions}`) にする (#1851)。
	// replace だと partial update で未指定キーが消える。incoming が map なら既存
	// meta.clientOptions に上書きマージ、null (= map でない) なら incoming 無し
	// として既存を維持する (upstream の `{...existing, ...null}` と同じ)。coerce
	// 前に行い、merge 後の map を coerceMetaJSONBFields が datatypes.JSON 化する。
	if raw, ok := fields["clientOptions"]; ok {
		merged := map[string]any{}
		if m, err := h.metaRepo.Fetch(); err == nil && m != nil && len(m.ClientOptions) > 0 {
			_ = json.Unmarshal(m.ClientOptions, &merged)
		}
		if incoming, isMap := raw.(map[string]any); isMap {
			for k, v := range incoming {
				merged[k] = v
			}
		}
		fields["clientOptions"] = merged
	}
	// jsonb 列 (deliverSuspendedSoftware / policies / clientOptions) は decoded
	// []any / map を []byte に marshal しないと UPDATE が型不一致で失敗する
	// (#1732 / #1823 / #1846)。
	coerceMetaJSONBFields(fields)

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
	// API param は mcaptchaSiteKey (capital K) だが DB 列は mcaptchaSitekey
	// (lower k)。alias しないと存在しない列への UPDATE で保存できない (#1174)。
	"mcaptchaSiteKey": "mcaptchaSitekey",
}

func renameUpdateMetaFields(fields map[string]any) {
	for alias, canonical := range updateMetaFieldAliases {
		if v, ok := fields[alias]; ok {
			fields[canonical] = v
			delete(fields, alias)
		}
	}
}

// updateMetaEnumAllowed enumerates upstream update-meta.ts の `enum: [...]`
// 制約付き field とその許容値集合 (#1108 sweep)。silent fallback を
// 防ぐため UpdateMeta の入口で reject する。upstream 同様 nil / 未送出
// は no-op (= field 自体が fields map に無いので validate 対象外)。
//
// clientOptions.entrancePageStyle は nested object なので別途 dict 構造
// で扱う必要がある。本 helper は top-level enum のみ対象。entrancePage
// Style は admin UI 改修が他系列より少ない前提 + nested handler 不在の
// ため本 sweep の scope 外 (= 別 PR で nested validator を追加するなら
// follow-up)。
var updateMetaEnumAllowed = map[string]map[string]struct{}{
	"sensitiveMediaDetection":            {"none": {}, "all": {}, "local": {}, "remote": {}},
	"sensitiveMediaDetectionSensitivity": {"medium": {}, "low": {}, "high": {}, "veryLow": {}, "veryHigh": {}},
	"federation":                         {"all": {}, "none": {}, "specified": {}},
	"ugcVisibilityForVisitor":            {"all": {}, "local": {}, "none": {}},
}

// validateUpdateMetaEnums returns an error when fields map contains an
// enum-constrained key whose value isn't in the allowed set. nil / non-string
// values are reject (= upstream の json schema validator が string + enum で
// 制約する semantics に合わせる)。
func validateUpdateMetaEnums(fields map[string]any) error {
	for key, allowed := range updateMetaEnumAllowed {
		v, ok := fields[key]
		if !ok {
			continue
		}
		s, ok := v.(string)
		if !ok {
			return errors.New(key + " must be a string")
		}
		if _, ok := allowed[s]; !ok {
			// 許容値は alphabetical sort で error 文に並べる (= deterministic
			// error message、test の assert.Contains が安定する)。
			keys := make([]string, 0, len(allowed))
			for k := range allowed {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			return errors.New(key + " must be one of: " + strings.Join(keys, ", "))
		}
	}
	return nil
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

// metaJSONBColumns lists meta jsonb columns whose update-meta payload arrives as
// a decoded []any / map[string]any and must be json.Marshal された []byte に
// 変換しないと jsonb 列の UPDATE が型不一致で失敗する (#1732 deliverSuspendedSoftware)。
// policies は object 形 jsonb で update-default-policies / update-meta の両経路から
// 来る (#1823)。clientOptions も object 形 jsonb で update-meta が whitelist 無しで
// 受理するため、未登録だと同じ型不一致 500 になる (#1846)。
//
// model.Meta に datatypes.JSON 列を追加したらここにも登録すること。漏れは
// TestMetaJSONBColumns_CoversAllJSONColumns (reflection guard) が検出する。
var metaJSONBColumns = map[string]struct{}{
	"deliverSuspendedSoftware": {},
	"policies":                 {},
	"clientOptions":            {},
}

// metaJSONBObjectColumns は metaJSONBColumns のうち object ({}) 形のもの。nil の
// とき array 形は `[]`、object 形は `{}` を default にするための区別 (#1823 / #1846)。
var metaJSONBObjectColumns = map[string]struct{}{
	"policies":      {},
	"clientOptions": {},
}

// coerceMetaJSONBFields marshals decoded JSON values for known jsonb meta
// columns into datatypes.JSON ([]byte) so GORM/pgx can write them. nil は
// 空 default (array→`[]` / object→`{}`)、既に []byte/string のものはそのまま流す。
func coerceMetaJSONBFields(fields map[string]any) {
	for k, v := range fields {
		if _, ok := metaJSONBColumns[k]; !ok {
			continue
		}
		switch v.(type) {
		case nil:
			if _, isObj := metaJSONBObjectColumns[k]; isObj {
				fields[k] = datatypes.JSON([]byte("{}"))
			} else {
				fields[k] = datatypes.JSON([]byte("[]"))
			}
		case []byte, string, datatypes.JSON:
			// 既に raw JSON。そのまま流す。
		default:
			if raw, err := json.Marshal(v); err == nil {
				fields[k] = datatypes.JSON(raw)
			}
		}
	}
}

// normalizeUpdateMetaFields reproduces upstream update-meta.ts の値正規化:
// host リストの filter(Boolean)/lowercase/sort/dedup/blocked 除外、空文字→null、
// repositoryUrl の URL 検証、urlPreviewUserAgent/summalyProxy の trim。fields は
// rename 済 (canonical column name) を前提とする。array 系は pq.StringArray に
// 変換するので、後続の coerceMetaArrayFields は pass-through となる。
func normalizeUpdateMetaFields(fields map[string]any) {
	// filter(Boolean): 空文字列要素を除去する array columns。
	for _, key := range []string{"langs", "pinnedUsers", "hiddenTags", "sensitiveWords", "prohibitedWords", "prohibitedWordsForNameOfUser"} {
		if arr, ok := metaStringSlice(fields[key]); ok {
			fields[key] = pq.StringArray(filterNonEmptyHosts(arr, false))
		}
	}
	// blockedHosts / federationHosts: filter(Boolean) + lowercase。
	var blocked []string
	for _, key := range []string{"blockedHosts", "federationHosts"} {
		if arr, ok := metaStringSlice(fields[key]); ok {
			norm := filterNonEmptyHosts(arr, true)
			fields[key] = pq.StringArray(norm)
			if key == "blockedHosts" {
				blocked = norm
			}
		}
	}
	// silencedHosts / mediaSilencedHosts: sort + dedup + 空除外 + blockedHosts 除外。
	// blocked は同一リクエストで送られた (正規化済) blockedHosts のみを参照する
	// (upstream は set.blockedHosts?.includes、= リクエストに無ければ除外しない)。
	for _, key := range []string{"silencedHosts", "mediaSilencedHosts"} {
		if arr, ok := metaStringSlice(fields[key]); ok {
			fields[key] = pq.StringArray(sortDedupExcludeHosts(arr, blocked))
		}
	}
	// 空文字→null の string columns (deeplAuthKey 等)。
	for _, key := range []string{"deeplAuthKey", "verifymailAuthKey", "truemailInstance", "truemailAuthKey", "googleAnalyticsMeasurementId"} {
		if v, ok := fields[key]; ok {
			if s, ok := v.(string); ok && s == "" {
				fields[key] = nil
			}
		}
	}
	// repositoryUrl: 妥当な絶対 URL でなければ null (upstream URL.canParse)。
	if v, ok := fields["repositoryUrl"]; ok {
		if s, ok := v.(string); ok && !isParsableURL(s) {
			fields["repositoryUrl"] = nil
		}
	}
	// urlPreviewUserAgent: trim して空なら null、非空なら原文 (untrimmed) を保存
	// (upstream は trim 結果で null 判定しつつ ps.urlPreviewUserAgent を格納)。
	if v, ok := fields["urlPreviewUserAgent"]; ok {
		if s, ok := v.(string); ok {
			if strings.TrimSpace(s) == "" {
				fields["urlPreviewUserAgent"] = nil
			}
		}
	}
	// summalyProxy は urlPreviewSummaryProxyUrl の別名。両者のうち
	// urlPreviewSummaryProxyUrl を優先し、trim して空なら null、非空なら trim 済を
	// 保存する (upstream)。
	//
	// upstream は `(urlPreviewSummaryProxyUrl ?? summalyProxy)` の null 合体だが、
	// ここでは key 存在で分岐する。urlPreviewSummaryProxyUrl が明示的 JSON null で
	// 送られた場合は summalyProxy へフォールバックせず null になる差分があるが、
	// frontend が両者を同時送出するケースは無いため許容する。
	if _, hasA := fields["urlPreviewSummaryProxyUrl"]; hasA {
		fields["urlPreviewSummaryProxyUrl"] = trimToNil(fields["urlPreviewSummaryProxyUrl"])
		delete(fields, "summalyProxy")
	} else if v, hasB := fields["summalyProxy"]; hasB {
		fields["urlPreviewSummaryProxyUrl"] = trimToNil(v)
		delete(fields, "summalyProxy")
	}
}

// metaStringSlice extracts a []string from a JSON-bound array value. Returns
// ok=false when v is not an array of all-strings (nil / non-array / mixed types
// are left untouched so coerceMetaArrayFields / the real repo handle them).
func metaStringSlice(v any) ([]string, bool) {
	switch arr := v.(type) {
	case []string:
		return arr, true
	case pq.StringArray:
		return []string(arr), true
	case []any:
		out := make([]string, 0, len(arr))
		for _, e := range arr {
			s, ok := e.(string)
			if !ok {
				return nil, false
			}
			out = append(out, s)
		}
		return out, true
	}
	return nil, false
}

// filterNonEmptyHosts drops empty-string entries and, when lower is true,
// lowercases each entry (upstream blockedHosts/federationHosts handling).
func filterNonEmptyHosts(in []string, lower bool) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" {
			continue
		}
		if lower {
			s = strings.ToLower(s)
		}
		out = append(out, s)
	}
	return out
}

// sortDedupExcludeHosts sorts, removes empties + duplicates, and excludes any
// host present in blocked (upstream silencedHosts/mediaSilencedHosts handling).
func sortDedupExcludeHosts(in, blocked []string) []string {
	sorted := append([]string(nil), in...)
	sort.Strings(sorted)
	blockedSet := make(map[string]struct{}, len(blocked))
	for _, b := range blocked {
		blockedSet[b] = struct{}{}
	}
	out := make([]string, 0, len(sorted))
	last := ""
	first := true
	for _, h := range sorted {
		if h == "" {
			continue
		}
		if !first && h == last {
			continue
		}
		first = false
		last = h
		if _, blockedHost := blockedSet[h]; blockedHost {
			continue
		}
		out = append(out, h)
	}
	return out
}

// isParsableURL reports whether s is a parseable absolute URL (scheme + host),
// approximating upstream `URL.canParse(ps.repositoryUrl)`.
//
// 厳密には URL.canParse より厳格で、host を要求する。これにより
// `mailto:a@b.com` / `javascript:...` のような hostless scheme を弾く
// (canParse は true)。repositoryUrl の現実的入力は http(s) のみで、
// scheme だけの危険 URL を排除できる安全側の差分なので許容する。
func isParsableURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && u.Scheme != "" && u.Host != ""
}

// trimToNil trims a string value and returns nil when the result is empty,
// otherwise the trimmed string. Non-string values pass through unchanged.
func trimToNil(v any) any {
	s, ok := v.(string)
	if !ok {
		return v
	}
	t := strings.TrimSpace(s)
	if t == "" {
		return nil
	}
	return t
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
	return c.JSON(http.StatusOK, h.packRole(r))
}

// packRole renders a role in the upstream-compatible shape (usersCount /
// createdAt / default-filled policies 等)。raw model.Role を返すとこれらが欠落
// する。usersCount は active assignment 数 (per-role count、role は少数なので
// N+1 でも許容、upstream pack も per-role count する)。
func (h *Handler) packRole(r *model.Role) map[string]any {
	return entity.PackRole(r, h.roleService.CountAssignedUsers(r.ID), h.idGen, role.DefaultPolicies())
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
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_ROLE", "No such role.", "07dc7d34-c0d8-49b7-96c6-db3ce64ee0b3"))
	}
	return c.JSON(http.StatusOK, h.packRole(r))
}

// RolesList handles POST /api/admin/roles/list.
func (h *Handler) RolesList(c echo.Context) error {
	roles, err := h.roleService.List()
	if err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	out := make([]map[string]any, 0, len(roles))
	for _, r := range roles {
		out = append(out, h.packRole(r))
	}
	return c.JSON(http.StatusOK, out)
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
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_ROLE", "No such role.", "cd23ef55-09ad-428a-ac61-95a45e124b32"))
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
			return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_ROLE", "No such role.", "cd23ef55-09ad-428a-ac61-95a45e124b32"))
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
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_ROLE", "No such role.", "de0d6ecd-8e0a-4253-88ff-74bc89ae3d45"))
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
		UserID string `json:"userId"`
		RoleID string `json:"roleId"`
		// upstream assign.ts paramDef は expiresAt: { type:'integer', nullable }
		// = epoch ms。frontend も epoch ms (number) を送るため *int64 で受ける。
		// 以前は *string + RFC3339 parse だったが、JSON number を *string に
		// bind すると unmarshal error → 400 となり期限付き role 付与が常に
		// 失敗していた (#1542)。
		ExpiresAt *int64 `json:"expiresAt"`
	}
	if err := c.Bind(&req); err != nil || req.UserID == "" || req.RoleID == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "userId and roleId are required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	// canEditMembersByModerator gate (upstream assign.ts:69-76)。role lookup →
	// gate の順で、moderator (非 administrator) は canEditMembersByModerator が
	// 立つ role しか付け外しできない。NO_SUCH_ROLE / ACCESS_DENIED の UUID は
	// assign 固有のもの。
	if handled, err := h.requireCanEditRoleMembers(c, req.RoleID, "6503c040-6af4-4ed9-bf07-f2dd16678eab", "25b5bc31-dc79-4ebd-9bd2-c84978fd052c"); handled {
		return err
	}
	// upstream assign.ts:77-81: 対象 user 不在なら NO_SUCH_USER (#1542)。
	if h.userRepo != nil {
		if _, err := h.userRepo.FindByID(req.UserID); err != nil {
			return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_USER", "No such user.", "558ea170-f653-4700-94d0-5a818371d0df"))
		}
	}
	var expiresAt *time.Time
	if req.ExpiresAt != nil {
		t := time.UnixMilli(*req.ExpiresAt)
		// upstream assign.ts:83-85: 過去 (現在以下) の expiresAt は no-op で
		// return する (過去日時付与の無効化)。
		if !t.After(time.Now()) {
			return c.NoContent(http.StatusNoContent)
		}
		expiresAt = &t
	}
	if err := h.roleService.Assign(req.UserID, req.RoleID, expiresAt); err != nil {
		if err == role.ErrRoleNotFound {
			return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_ROLE", "No such role.", "6503c040-6af4-4ed9-bf07-f2dd16678eab"))
		}
		if err == role.ErrAlreadyAssigned {
			return c.JSON(http.StatusConflict, apierr.Error("ALREADY_ASSIGNED", "Role already assigned.", "67d8689c-25c6-435f-8eed-6ea68e5e53e9"))
		}
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	h.logRoleAssignment(c, moderationlog.LogAssignRole, req.UserID, req.RoleID)
	return c.NoContent(http.StatusNoContent)
}

// requireCanEditRoleMembers enforces the upstream canEditMembersByModerator
// gate shared by admin/roles/assign and /unassign. It looks up the role first
// (responding NO_SUCH_ROLE with the per-endpoint error id when missing) and then
// denies moderators that may not edit the role's members, while administrators
// (and root) are always allowed.
//
// The returned handled flag reports whether a response was already written: when
// true the caller must return the accompanying error immediately; when false the
// caller may proceed. echo の c.JSON は成功時 nil を返すため bool で「応答を書い
// たか」を伝える (err だけでは deny を short-circuit できない)。
//
// 認可判定 (viewer-based) なので service 層ではなく handler 層に置く。viewer が
// 取得できない場合は administrator とみなさず ACCESS_DENIED に倒す (fail-closed)。
// NO_SUCH_ROLE / ACCESS_DENIED の UUID は assign と unassign で異なるため呼び
// 出し側から渡す。
func (h *Handler) requireCanEditRoleMembers(c echo.Context, roleID, noSuchRoleID, accessDeniedID string) (handled bool, err error) {
	r, err := h.roleService.Show(roleID)
	if err != nil {
		return true, c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_ROLE", "No such role.", noSuchRoleID))
	}
	if r.CanEditMembersByModerator {
		return false, nil
	}
	if me := middleware.GetUser(c); me != nil && h.roleService.IsAdministrator(me.ID) {
		return false, nil
	}
	return true, c.JSON(http.StatusForbidden, apierr.Error("ACCESS_DENIED", "Only administrators can edit members of the role.", accessDeniedID))
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
	// canEditMembersByModerator gate (upstream unassign.ts:71-78)。role 不在は
	// assign 同様 NO_SUCH_ROLE を先に返す (TS と同順)。NO_SUCH_ROLE /
	// ACCESS_DENIED の UUID は unassign 固有のもの。
	if handled, err := h.requireCanEditRoleMembers(c, req.RoleID, "6e519036-a70d-4c76-b679-bc8fb18194e2", "24636eee-e8c1-493e-94b2-e16ad401e262"); handled {
		return err
	}
	// upstream unassign.ts:80-84: 対象 user 不在なら NO_SUCH_USER (#1542)。
	if h.userRepo != nil {
		if _, err := h.userRepo.FindByID(req.UserID); err != nil {
			return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_USER", "No such user.", "2b730f78-1179-461b-88ad-d24c9af1a5ce"))
		}
	}
	if err := h.roleService.Unassign(req.UserID, req.RoleID); err != nil {
		if err == role.ErrNotAssigned {
			return c.JSON(http.StatusNotFound, apierr.Error("NOT_ASSIGNED", "Role not assigned.", "b9060ac7-5c94-4da4-9f55-2047c953df44"))
		}
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	h.logRoleAssignment(c, moderationlog.LogUnassignRole, req.UserID, req.RoleID)
	return c.NoContent(http.StatusNoContent)
}

// RolesUsers handles POST /api/admin/roles/users.
func (h *Handler) RolesUsers(c echo.Context) error {
	var req struct {
		RoleID    string `json:"roleId"`
		Limit     int    `json:"limit"`
		SinceID   string `json:"sinceId"`
		UntilID   string `json:"untilId"`
		SinceDate *int64 `json:"sinceDate"`
		UntilDate *int64 `json:"untilDate"`
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
	// sinceDate / untilDate を aidx prefix に正規化 (#1173)。
	sinceID, untilID := id.NormalizeCursor(req.SinceID, req.UntilID, req.SinceDate, req.UntilDate)

	assignments, err := h.roleService.ListByRole(req.RoleID, untilID, sinceID, limit)
	if err != nil {
		if err == role.ErrRoleNotFound {
			return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_ROLE", "No such role.", "224eff5e-2488-4b18-b3e7-f50d94421648"))
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
		// upstream users.ts:101 は expiresAt: assign.expiresAt?.toISOString() ?? null
		// を含める (#1542)。nil は JSON null。
		var expiresAt any
		if a.ExpiresAt != nil {
			expiresAt = a.ExpiresAt.UTC().Format("2006-01-02T15:04:05.000Z")
		}
		result = append(result, map[string]any{
			"id":        a.ID,
			"createdAt": createdAt,
			// upstream users.ts:95 は packMany(users, me, {schema:'UserDetailed'})。
			// packAdminUser を使うと email / signins / roleAssigns 等の admin 専用
			// field が read:admin:roles scope に漏れる (show-user の admin guard 迂回)。
			// ShowUsers と同様 UserDetailed に揃えて過剰露出を防ぐ (#1822)。
			"user":      entity.PackUserDetailed(a.User, profileByUser[a.User.ID], h.idGen),
			"expiresAt": expiresAt,
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
	// upstream update-default-policies.ts は更新前後の policies を取得して
	// moderationLogService.log('updateServerSettings', {before, after}) を記録する
	// (#1542)。before snapshot を Update 前に取得する。
	beforeMeta, _ := h.metaRepo.Fetch()
	// Meta の policies フィールドを更新。policies は jsonb 列なので decoded map を
	// そのまま渡すと UPDATE が型不一致で失敗する (#1823)。coerceMetaJSONBFields で
	// datatypes.JSON ([]byte) に変換してから渡す (RolesCreate/Update の Marshal と
	// 同じ動機、UpdateMeta 経路と共通化)。
	policyFields := map[string]any{"policies": req.Policies}
	coerceMetaJSONBFields(policyFields)
	if err := h.metaRepo.Update(policyFields); err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	// moderation log (before/after policies)。TS は policies のみを記録する。
	if afterMeta, err := h.metaRepo.Fetch(); err == nil {
		var beforePolicies any
		if beforeMeta != nil {
			beforePolicies = beforeMeta.Policies
		}
		h.logModeration(c, moderationlog.LogUpdateServerSettings, map[string]any{
			"before": beforePolicies,
			"after":  afterMeta.Policies,
		})
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
		Name        string   `json:"name"`
		URL         string   `json:"url"`
		FileID      string   `json:"fileId"`
		Category    *string  `json:"category"`
		Aliases     []string `json:"aliases"`
		License     *string  `json:"license"`
		IsSensitive bool     `json:"isSensitive"`
		LocalOnly   bool     `json:"localOnly"`
		RoleIDs     []string `json:"roleIdsThatCanBeUsedThisEmojiAsReaction"`
	}
	if err := c.Bind(&req); err != nil || req.Name == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "name is required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	// upstream paramDef は name に `^[a-zA-Z0-9_]+$` を強制する。
	if !emojiNamePattern.MatchString(req.Name) {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "Invalid emoji name.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	if h.emojiRepo == nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	// fileId 経路は drive_file を resolve する (NO_SUCH_FILE)。filetype 検証は
	// upstream の error 順序 (noSuchFile → duplicate → unsupportedFileType) に
	// 合わせ、duplicate チェックの後で行う。
	var driveFile *model.DriveFile
	url := req.URL
	if url == "" && req.FileID != "" && h.driveFileRepo != nil {
		f, err := h.driveFileRepo.FindByID(req.FileID)
		if err != nil {
			return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_FILE", "No such file.", "fc46b5a4-6b92-4c33-ac66-b806659bb5cf"))
		}
		driveFile = f
	}
	if url == "" && driveFile == nil {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "url or fileId is required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	// 同名のローカル emoji が既に存在する場合は DUPLICATE_NAME (upstream 互換)。
	if existing, err := h.emojiRepo.FindByNameAndHost(req.Name, nil); err == nil && existing != nil {
		return c.JSON(http.StatusBadRequest, apierr.Error("DUPLICATE_NAME", "Duplicate name.", "f7a3462c-4e6e-4069-8421-b9bd4f4c3975"))
	}
	// drive 画像なら MIME を allowlist で検証し、webpublic variant を優先して
	// originalUrl/publicUrl/type を導出する (upstream CustomEmojiService.add)。
	publicURL := url
	var fileType *string
	if driveFile != nil {
		if !isAllowedEmojiImageType(driveFile.Type) {
			return c.JSON(http.StatusBadRequest, apierr.Error("UNSUPPORTED_FILE_TYPE", "Unsupported file type.", "f7599d96-8750-af68-1633-9575d625c1a7"))
		}
		url = driveFile.URL
		publicURL = preferWebpublicURL(driveFile)
		fileType = preferWebpublicType(driveFile)
	}
	now := time.Now()
	e := &model.Emoji{
		ID:                                      h.idGen.Generate(now),
		UpdatedAt:                               &now,
		Name:                                    req.Name,
		OriginalURL:                             url,
		PublicURL:                               publicURL,
		Type:                                    fileType,
		Category:                                req.Category,
		Aliases:                                 pq.StringArray(req.Aliases),
		License:                                 req.License,
		IsSensitive:                             req.IsSensitive,
		LocalOnly:                               req.LocalOnly,
		RoleIDsThatCanBeUsedThisEmojiAsReaction: pq.StringArray(req.RoleIDs),
	}
	if err := h.emojiRepo.Create(e); err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	h.logModeration(c, moderationlog.LogAddCustomEmoji, map[string]any{
		"emojiId": e.ID,
		"emoji":   e,
	})
	// upstream は EmojiDetailed (packDetailed) を返す。raw model.Emoji を返すと
	// `url` が欠落し originalUrl/publicUrl/uri/type 等の内部 field が漏れる。
	return c.JSON(http.StatusOK, entity.PackEmojiDetailed(e))
}

// emojiNamePattern mirrors upstream admin/emoji/add の paramDef name pattern。
var emojiNamePattern = regexp.MustCompile(`^[a-zA-Z0-9_]+$`)

// allowedEmojiImageTypes mirrors upstream FILE_TYPE_IMAGE (const.ts)。
// image/svg+xml は XSS 理由で意図的に除外する。prefix 判定 ("image/") では
// svg や任意 subtype を通してしまうため、明示 allowlist で完全一致判定する。
var allowedEmojiImageTypes = map[string]bool{
	"image/png":    true,
	"image/gif":    true,
	"image/jpeg":   true,
	"image/webp":   true,
	"image/avif":   true,
	"image/apng":   true,
	"image/bmp":    true,
	"image/tiff":   true,
	"image/x-icon": true,
}

// isAllowedEmojiImageType reports whether a drive file MIME may back a custom
// emoji. Empty type is rejected (upstream FILE_TYPE_IMAGE.includes("")===false)。
func isAllowedEmojiImageType(mime string) bool {
	return allowedEmojiImageTypes[mime]
}

// preferWebpublicURL returns the drive file's webpublic URL when present,
// else its canonical URL (upstream `webpublicUrl ?? url`).
func preferWebpublicURL(f *model.DriveFile) string {
	if f.WebpublicURL != nil && *f.WebpublicURL != "" {
		return *f.WebpublicURL
	}
	return f.URL
}

// preferWebpublicType returns the drive file's webpublic MIME when present,
// else its canonical type (upstream `webpublicType ?? type`). Returned as a
// pointer so a non-empty value lands in emoji.type (NULL when both empty).
func preferWebpublicType(f *model.DriveFile) *string {
	if f.WebpublicType != nil && *f.WebpublicType != "" {
		t := *f.WebpublicType
		return &t
	}
	if f.Type != "" {
		t := f.Type
		return &t
	}
	return nil
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
		FileID      *string  `json:"fileId"`
		Category    *string  `json:"category"`
		Aliases     []string `json:"aliases"`
		License     *string  `json:"license"`
		IsSensitive *bool    `json:"isSensitive"`
		LocalOnly   *bool    `json:"localOnly"`
		RoleIDs     []string `json:"roleIdsThatCanBeUsedThisEmojiAsReaction"`
	}
	// upstream update.ts paramDef は allOf+anyOf で {id} または {name} の
	// どちらか必須。id 無し name 指定時は local emoji を name で解決する (#1543)。
	if err := c.Bind(&req); err != nil || (req.ID == "" && (req.Name == nil || *req.Name == "")) {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "id or name is required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	if h.emojiRepo == nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	// upstream update.ts:91-95 は emoji 解決より先に fileId の driveFile を検証し、
	// null なら NO_SUCH_FILE を返す (#1772。emoji 不在 AND fileId 不正が同時のとき
	// upstream は NO_SUCH_FILE を優先する)。ここで解決した file を後段の URL 差し替え
	// でも再利用する。
	var emojiFile *model.DriveFile
	if req.FileID != nil && *req.FileID != "" {
		if h.driveFileRepo == nil {
			return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
		}
		f, ferr := h.driveFileRepo.FindByID(*req.FileID)
		if ferr != nil {
			return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_FILE", "No such file.", "14fb9fd9-0731-4e2f-aeb9-f09e4740333d"))
		}
		emojiFile = f
	}
	// before snapshot for moderation log (also used to gate NO_SUCH_EMOJI when
	// the row is missing — UpdateFields の RowsAffected==0 経路は repo 側で
	// ErrRecordNotFound に昇格済み (#650 問題 2)).
	// id 指定があれば id 解決、無ければ name で local emoji を解決する。upstream
	// CustomEmojiService.update は `data.id ? getEmojiById : getEmojiByName` (#1543)。
	var before *model.Emoji
	var err error
	if req.ID != "" {
		before, err = h.emojiRepo.FindByID(req.ID)
	} else {
		before, err = h.emojiRepo.FindByNameAndHost(*req.Name, nil)
	}
	if err != nil || before == nil {
		// #729: 再現報告のため request 側 id と原因 (find vs update path) を
		// log に残す。frontend 側で stale id を握っているのか、repo 側で
		// 何か起きているのか切り分けに使う。
		slog.WarnContext(c.Request().Context(), "EmojiUpdate: NO_SUCH_EMOJI on lookup",
			"id", req.ID, "err", err)
		return c.JSON(http.StatusNotFound, apierr.NoSuchEmoji())
	}
	// 以降の UpdateFields / moderation log は解決済み id を使う。
	req.ID = before.ID
	fields := map[string]any{}
	if req.Name != nil {
		// リネーム時は同名 local emoji の重複を弾く (SAME_NAME_EMOJI_EXISTS)。
		if *req.Name != before.Name {
			if dup, derr := h.emojiRepo.FindByNameAndHost(*req.Name, nil); derr == nil && dup != nil && dup.ID != req.ID {
				return c.JSON(http.StatusBadRequest, apierr.Error("SAME_NAME_EMOJI_EXISTS", "Emoji with the same name already exists.", "7180fe9d-1ee3-bff9-647d-fe9896d2ffb8"))
			}
		}
		fields["name"] = *req.Name
	}
	// fileId 指定時は drive の画像で URL を差し替える (upstream 互換)。file は
	// emoji 解決前に検証済み (emojiFile)、NO_SUCH_FILE はそこで返している (#1772)。
	if emojiFile != nil {
		f := emojiFile
		if !isAllowedEmojiImageType(f.Type) {
			return c.JSON(http.StatusBadRequest, apierr.Error("UNSUPPORTED_FILE_TYPE", "Unsupported file type.", "f7599d96-8750-af68-1633-9575d625c1a7"))
		}
		// upstream update.ts: originalUrl=url, publicUrl=webpublicUrl??url,
		// fileType=webpublicType??type。EmojiAdd / EmojiCopy と同ロジック。
		fields["originalUrl"] = f.URL
		fields["publicUrl"] = preferWebpublicURL(f)
		if t := preferWebpublicType(f); t != nil {
			fields["type"] = *t
		}
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
	// リアクション利用可能ロールの更新 (upstream の roleIdsThatCanBeUsedThisEmojiAsReaction)。
	if req.RoleIDs != nil {
		fields["roleIdsThatCanBeUsedThisEmojiAsReaction"] = pq.StringArray(req.RoleIDs)
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
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_EMOJI", "No such emoji.", "be83669b-773a-44b7-b1f8-e5e5170ac3c2"))
	}
	if err := h.emojiRepo.Delete(req.ID); err != nil {
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_EMOJI", "No such emoji.", "be83669b-773a-44b7-b1f8-e5e5170ac3c2"))
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
		Query     string `json:"query"`
		Category  string `json:"category"`
		SinceID   string `json:"sinceId"`
		UntilID   string `json:"untilId"`
		SinceDate *int64 `json:"sinceDate"`
		UntilDate *int64 `json:"untilDate"`
		Limit     int    `json:"limit"`
		Offset    int    `json:"offset"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "Invalid parameters.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	if h.emojiRepo == nil {
		return c.JSON(http.StatusOK, []any{})
	}
	// sinceDate / untilDate を aidx prefix に正規化 (#1173)。
	sinceID, untilID := id.NormalizeCursor(req.SinceID, req.UntilID, req.SinceDate, req.UntilDate)
	req.Limit = pagination.ClampLimit(req.Limit, 10, 100)
	emojis, err := h.emojiRepo.ListWithFilter(req.Query, req.Category, true, sinceID, untilID, req.Limit, req.Offset)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	return c.JSON(http.StatusOK, entity.PackEmojiDetailedList(emojis))
}

// EmojiListV2 handles POST /api/v2/admin/emoji/list.
// Returns an object with pagination info (allCount, allPages) instead of a plain array.
func (h *Handler) EmojiListV2(c echo.Context) error {
	var req struct {
		Query     *emojiV2QueryReq `json:"query"`
		SinceID   string           `json:"sinceId"`
		UntilID   string           `json:"untilId"`
		SinceDate *int64           `json:"sinceDate"`
		UntilDate *int64           `json:"untilDate"`
		Limit     int              `json:"limit"`
		Page      int              `json:"page"`
		SortKeys  []string         `json:"sortKeys"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "Invalid parameters.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	if h.emojiRepo == nil {
		return c.JSON(http.StatusOK, emojiListV2Response{
			Emojis:   []map[string]any{},
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

	// sinceDate / untilDate を aidx prefix に正規化 (#1173)。
	sinceID, untilID := id.NormalizeCursor(req.SinceID, req.UntilID, req.SinceDate, req.UntilDate)
	filter := model.EmojiV2Filter{
		SinceID:  sinceID,
		UntilID:  untilID,
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

	roleByID := h.emojiRoleByID()
	packed := make([]map[string]any, 0, len(emojis))
	for _, e := range emojis {
		packed = append(packed, packEmojiDetailedAdmin(e, roleByID))
	}

	return c.JSON(http.StatusOK, emojiListV2Response{
		Emojis:   packed,
		Count:    len(packed),
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
	Emojis   []map[string]any `json:"emojis"`
	Count    int              `json:"count"`
	AllCount int64            `json:"allCount"`
	AllPages int              `json:"allPages"`
}

// emojiRoleByID resolves all roles by id once, so the v2 emoji packer can embed
// {id, name} for roleIdsThatCanBeUsedThisEmojiAsReaction without an N+1 lookup
// per emoji (#1300), and sort them by displayOrder/id like upstream (#1948-13)。
// roleService 未配線 / 取得失敗時は空 map を返し、未知 role 同様 roleIds から省かれる。
func (h *Handler) emojiRoleByID() map[string]*model.Role {
	out := map[string]*model.Role{}
	if h.roleService == nil {
		return out
	}
	roles, err := h.roleService.List()
	if err != nil {
		return out
	}
	for _, r := range roles {
		out[r.ID] = r
	}
	return out
}

// packEmojiDetailedAdmin builds the golden EmojiDetailedAdmin shape for the v2
// admin emoji list. upstream packDetailedAdmin と同じく roleIds を {id, name}
// に解決する (golden は object array、生の string[] ではない)。未知 role は
// 省く (#1300)。scalar field は model.Emoji の json tag と一致する。
func packEmojiDetailedAdmin(e *model.Emoji, roleByID map[string]*model.Role) map[string]any {
	// upstream packDetailedAdmin は解決した role を displayOrder DESC, id ASC で
	// sort してから {id, name} に map する (EmojiEntityService、#1948-13)。
	resolved := make([]*model.Role, 0, len(e.RoleIDsThatCanBeUsedThisEmojiAsReaction))
	for _, rid := range e.RoleIDsThatCanBeUsedThisEmojiAsReaction {
		if r, ok := roleByID[rid]; ok {
			resolved = append(resolved, r)
		}
	}
	sort.SliceStable(resolved, func(i, j int) bool {
		if resolved[i].DisplayOrder != resolved[j].DisplayOrder {
			return resolved[i].DisplayOrder > resolved[j].DisplayOrder // displayOrder DESC
		}
		return resolved[i].ID < resolved[j].ID // id ASC
	})
	roles := make([]map[string]any, 0, len(resolved))
	for _, r := range resolved {
		roles = append(roles, map[string]any{"id": r.ID, "name": r.Name})
	}
	// golden aliases は string[] 必須 (non-null)。nil pq.StringArray は null に
	// なるため [] へ coalesce する。
	aliases := []string(e.Aliases)
	if aliases == nil {
		aliases = []string{}
	}
	return map[string]any{
		"id": e.ID,
		// upstream EmojiEntityService.ts:112 は updatedAt?.toISOString() ?? null。
		// raw *time.Time だと RFC3339Nano になるため .000Z/null に揃える (#1948-10)。
		"updatedAt":   entity.ISOMillisPtr(e.UpdatedAt),
		"name":        e.Name,
		"host":        e.Host,
		"publicUrl":   e.PublicURL,
		"originalUrl": e.OriginalURL,
		"uri":         e.URI,
		"type":        e.Type,
		"aliases":     aliases,
		"category":    e.Category,
		"license":     e.License,
		"localOnly":   e.LocalOnly,
		"isSensitive": e.IsSensitive,
		"roleIdsThatCanBeUsedThisEmojiAsReaction": roles,
	}
}

// --- Abuse Report endpoints ---

// AbuseReports handles POST /api/admin/abuse-user-reports.
//
// upstream paramDef (= packages/backend/src/server/api/endpoints/admin/
// abuse-user-reports.ts) で受ける主要 field を frontend (= MkPagination
// + pages/admin/abuses.vue) と互換にする:
//
//   - sinceId / untilId — cursor pagination (#1114 root cause)
//   - state             — 'all' | 'unresolved' | 'resolved'
//   - reporterOrigin    — 'combined' | 'local' | 'remote'
//   - targetUserOrigin  — 'combined' | 'local' | 'remote'
//   - limit             — default 10 / max 100
//
// 旧 mk-go は `resolved` boolean / `limit` / `offset` しか受けず、frontend が
// 送る untilId を無視して同じ first page を返し続けて MkPagination が末尾
// 検知できず無限ロードになっていた (= 過去 #487 / #488 / #491 / #493 / #520
// / #712 と完全に同 pattern)。
//
// 後方互換: 旧 `resolved` boolean field は引き続き受け付ける (= 旧 client /
// 既存 e2e 互換)。`state` と `resolved` の両方が来た場合は `state` を優先
// する (= upstream paramDef での enum 優先 pattern と同じ)。
func (h *Handler) AbuseReports(c echo.Context) error {
	var req struct {
		Resolved         *bool  `json:"resolved"`
		State            string `json:"state"`
		ReporterOrigin   string `json:"reporterOrigin"`
		TargetUserOrigin string `json:"targetUserOrigin"`
		SinceID          string `json:"sinceId"`
		UntilID          string `json:"untilId"`
		SinceDate        *int64 `json:"sinceDate"`
		UntilDate        *int64 `json:"untilDate"`
		Limit            int    `json:"limit"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "Invalid parameters.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	if h.abuseRepo == nil {
		return c.JSON(http.StatusOK, []any{})
	}
	// state enum 優先、無ければ legacy boolean fallback。'all' / 空文字列は
	// 「フィルタなし (= resolved 列で絞らない)」を意味する。
	resolved := req.Resolved
	switch req.State {
	case "unresolved":
		v := false
		resolved = &v
	case "resolved":
		v := true
		resolved = &v
	case "all", "":
		// 何もしない (= legacy boolean fallback または「全件」)
	default:
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "state must be 'all', 'unresolved', or 'resolved'.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	// origin enum 検証: 空文字列 / 'combined' / 'local' / 'remote' 以外は reject
	// (= silent fallback で全件返すと「local だけ見たかった」が裏切られて
	// 静かに drop-in regression になるため)。
	if !isValidOrigin(req.ReporterOrigin) {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "reporterOrigin must be 'combined', 'local', or 'remote'.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	if !isValidOrigin(req.TargetUserOrigin) {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "targetUserOrigin must be 'combined', 'local', or 'remote'.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	// sinceDate / untilDate を aidx prefix に正規化 (#1173)。
	sinceID, untilID := id.NormalizeCursor(req.SinceID, req.UntilID, req.SinceDate, req.UntilDate)
	req.Limit = pagination.ClampLimit(req.Limit, 10, 100)
	reports, err := h.abuseRepo.List(resolved, req.ReporterOrigin, req.TargetUserOrigin, sinceID, untilID, req.Limit)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	// model.AbuseUserReport は createdAt 列を持たず aidx ID 先頭 8 文字に
	// timestamp を埋め込んでいる。frontend (MkAbuseReport.vue) は
	// `<MkTime :time="report.createdAt"/>` を直接読むため、aidx から派生
	// した createdAt 文字列を response に注入する。ShowModerationLogs と
	// 同パターン (#1116)。embedded struct で既存 field を JSON inline、
	// CreatedAt だけ上乗せする。
	// reporter / targetUser / assignee に紐づく profile を 1 batch で解決して
	// UserDetailed を pack する (N+1 回避)。
	profByID := h.abuseUserProfiles(reports)
	out := make([]packedAbuseReport, 0, len(reports))
	for _, r := range reports {
		out = append(out, h.packAbuseReport(c.Request().Context(), r, profByID))
	}
	return c.JSON(http.StatusOK, out)
}

// packAbuseReport converts an abuse report into the wire shape shared by
// admin/abuse-user-reports (list) and the abuseReportResolved system webhook
// body (#1723). profByID は abuseUserProfiles で batch 解決した profile map。
func (h *Handler) packAbuseReport(ctx context.Context, r *model.AbuseUserReport, profByID map[string]*model.UserProfile) packedAbuseReport {
	p := packedAbuseReport{
		ID:             r.ID,
		Comment:        r.Comment,
		Resolved:       r.Resolved,
		ReporterID:     r.ReporterID,
		TargetUserID:   r.TargetUserID,
		AssigneeID:     r.AssigneeID,
		Forwarded:      r.Forwarded,
		ResolvedAs:     r.ResolvedAs,
		ModerationNote: r.ModerationNote,
		// 生 model.User を inline すると usernameLower / inbox 等の内部
		// field が漏れ、UserDetailedNotMe 固有 field も欠ける。UserDetailed
		// で pack する。
		Reporter:   h.packAbuseUser(r.Reporter, profByID),
		TargetUser: h.packAbuseUser(r.TargetUser, profByID),
		Assignee:   h.packAbuseUser(r.Assignee, profByID),
	}
	if s, err := aidxCreatedAtString(h.idGen, r.ID); err == nil {
		p.CreatedAt = s
	} else if !errors.Is(err, ErrIDGenMissing) {
		// idGen は wired されているのに parse 失敗した場合のみログに残す。
		// 非 aidx 形式の legacy ID 等で createdAt が出せない時に frontend
		// 側で「日時の解析が失敗しました。」が出る原因を後追いできるように
		// する (ShowModerationLogs と同 pattern)。
		slog.DebugContext(ctx, "abuseReport: createdAt derive failed",
			"reportId", r.ID, "err", err)
	}
	return p
}

// abuseUserProfiles batch-fetches the profiles of every reporter / targetUser /
// assignee referenced by the reports, keyed by user ID (N+1 回避)。
func (h *Handler) abuseUserProfiles(reports []*model.AbuseUserReport) map[string]*model.UserProfile {
	if h.userRepo == nil {
		return nil
	}
	seen := map[string]struct{}{}
	ids := make([]string, 0, len(reports)*3)
	add := func(u *model.User) {
		if u == nil {
			return
		}
		if _, ok := seen[u.ID]; ok {
			return
		}
		seen[u.ID] = struct{}{}
		ids = append(ids, u.ID)
	}
	for _, r := range reports {
		add(r.Reporter)
		add(r.TargetUser)
		add(r.Assignee)
	}
	if len(ids) == 0 {
		return nil
	}
	profiles, err := h.userRepo.FindProfilesByUserIDs(ids)
	if err != nil {
		return nil
	}
	byID := make(map[string]*model.UserProfile, len(profiles))
	for _, p := range profiles {
		byID[p.UserID] = p
	}
	return byID
}

// packAbuseUser packs a related user as UserDetailed (= UserDetailedNotMe shape).
// Returns nil (-> JSON null) when u is nil so the assignee key stays present.
func (h *Handler) packAbuseUser(u *model.User, profByID map[string]*model.UserProfile) *entity.UserDetailed {
	if u == nil {
		return nil
	}
	d := entity.PackUserDetailed(u, profByID[u.ID], h.idGen)
	return &d
}

// packedAbuseReport mirrors the upstream abuse-user-reports res schema
// (abuse-user-reports.ts:26-89) field-for-field. 旧実装は model.AbuseUserReport
// を embed して生 model.User の relation や targetUserHost/reporterHost 等の
// 内部 field を漏らしていた。明示 struct にして TS res と 1:1 に揃える。
// assignee/assigneeId/resolvedAs は nullable+optional:false なので key 常在
// (null when unset)。createdAt は aidx 由来で legacy 非 aidx ID では omit。
type packedAbuseReport struct {
	ID             string               `json:"id"`
	CreatedAt      string               `json:"createdAt,omitempty"`
	Comment        string               `json:"comment"`
	Resolved       bool                 `json:"resolved"`
	ReporterID     string               `json:"reporterId"`
	TargetUserID   string               `json:"targetUserId"`
	AssigneeID     *string              `json:"assigneeId"`
	Reporter       *entity.UserDetailed `json:"reporter"`
	TargetUser     *entity.UserDetailed `json:"targetUser"`
	Assignee       *entity.UserDetailed `json:"assignee"`
	Forwarded      bool                 `json:"forwarded"`
	ResolvedAs     *string              `json:"resolvedAs"`
	ModerationNote string               `json:"moderationNote"`
}

// isValidOrigin returns true if s is one of the valid origin filter values
// accepted by admin endpoints ('combined' / 'local' / 'remote') or empty.
// 'combined' と 空文字列は「フィルタなし」として等価扱い。
func isValidOrigin(s string) bool {
	switch s {
	case "", "combined", "local", "remote":
		return true
	default:
		return false
	}
}

// ResolveAbuseReport handles POST /api/admin/resolve-abuse-user-report.
//
// upstream paramDef:
//
//	reportId:    string (required)
//	resolvedAs:  enum ['accept', 'reject', null], nullable
//
// 旧 mk-go は `resolvedAs` を `"accept"` 固定にしていたため、admin が
// reject (= 「報告は不適切」判定) を選んでも accept として記録され、
// abuse handling の判定結果が反映されない drop-in regression があった
// (PR #1108 sweep)。本 fix で upstream paramDef 通り `resolvedAs` を
// accept し enum 検証する。
func (h *Handler) ResolveAbuseReport(c echo.Context) error {
	var req struct {
		ReportID   string  `json:"reportId"`
		ResolvedAs *string `json:"resolvedAs"`
	}
	if err := c.Bind(&req); err != nil || req.ReportID == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "reportId is required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	if h.abuseRepo == nil {
		return c.JSON(http.StatusNotFound, apierr.ErrorWithKind("NO_SUCH_ABUSE_REPORT", "No such abuse report.", "ac3794dd-2ce4-d878-e546-73c60c06b398", apierr.KindServer))
	}
	// upstream enum: ['accept', 'reject', null]。null は「未送出」+ JSON null
	// 両方を「resolve したが判定保留」として扱う (= column も null)。
	// 不正値は silent fallback せず 400 で reject (PR #1102 の RolesUpdate.target
	// fix と同方針: 既存 row の resolvedAs が意図せず書き換わる silent
	// corruption を防ぐ)。
	// upstream は resolve 時に assigneeId=moderator.id を記録する。
	fields := map[string]any{"resolved": true}
	if me := middleware.GetUser(c); me != nil {
		fields["assigneeId"] = me.ID
	}
	switch {
	case req.ResolvedAs == nil:
		fields["resolvedAs"] = nil
	case *req.ResolvedAs == "accept":
		fields["resolvedAs"] = "accept"
	case *req.ResolvedAs == "reject":
		fields["resolvedAs"] = "reject"
	case *req.ResolvedAs == "":
		// 明示的空文字も null 扱い (frontend が `null` の代わりに `""` を送る
		// 互換ケース、cf. PR #1102 color clear 経路と同じ workaround)。
		fields["resolvedAs"] = nil
	default:
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "resolvedAs must be 'accept', 'reject', or null.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	if err := h.abuseRepo.UpdateFields(req.ReportID, fields); err != nil {
		return c.JSON(http.StatusNotFound, apierr.ErrorWithKind("NO_SUCH_ABUSE_REPORT", "No such abuse report.", "ac3794dd-2ce4-d878-e546-73c60c06b398", apierr.KindServer))
	}
	// upstream AbuseReportService.resolve は notifySystemWebhook('abuseReportResolved')
	// を呼ぶ。best-effort: DB 更新は済んでいるので発火失敗は握り潰す (#1723)。
	h.notifyAbuseReportResolved(c.Request().Context(), req.ReportID)
	return c.NoContent(http.StatusNoContent)
}

// notifyAbuseReportResolved fires the abuseReportResolved system webhook for a
// resolved report. 本家 AbuseReportNotificationService.notifySystemWebhook 相当:
// inactive な notification recipient (method=webhook) が指す systemWebhookId を
// excludes に渡し、残りの active system webhook へ packed report を配送する。
// dispatcher / abuseRepo 未配線時は no-op (#1723)。
func (h *Handler) notifyAbuseReportResolved(ctx context.Context, reportID string) {
	if h.systemWebhookDispatcher == nil || h.abuseRepo == nil {
		return
	}
	report, err := h.abuseRepo.FindByID(reportID)
	if err != nil {
		slog.WarnContext(ctx, "abuseReportResolved: load report failed", "reportId", reportID, "err", err)
		return
	}
	profByID := h.abuseUserProfiles([]*model.AbuseUserReport{report})
	body := h.packAbuseReport(ctx, report, profByID)
	h.systemWebhookDispatcher.DispatchSystemExcluding(
		corewebhook.SystemEventAbuseReportResolved, body, h.inactiveAbuseWebhookIDs())
}

// inactiveAbuseWebhookIDs returns the systemWebhookId values of inactive
// abuse-report-notification recipients (method=webhook). 本家 notifySystemWebhook
// の withoutWebhookIds 相当 (= 無効化された recipient 宛は配送しない)。recipientRepo
// 未配線 / 取得失敗時は nil (= excludes 無し)。
func (h *Handler) inactiveAbuseWebhookIDs() []string {
	if h.recipientRepo == nil {
		return nil
	}
	recipients, err := h.recipientRepo.List()
	if err != nil {
		return nil
	}
	var excludes []string
	for _, r := range recipients {
		if r.Method == "webhook" && !r.IsActive && r.SystemWebhookID != nil {
			excludes = append(excludes, *r.SystemWebhookID)
		}
	}
	return excludes
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
		Limit     int    `json:"limit"`
		SinceID   string `json:"sinceId"`
		UntilID   string `json:"untilId"`
		SinceDate *int64 `json:"sinceDate"`
		UntilDate *int64 `json:"untilDate"`
		Type      string `json:"type"`
		UserID    string `json:"userId"`
		Search    string `json:"search"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "Invalid parameters.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	// sinceDate/untilDate を aidx prefix に正規化し、type/userId/search で絞る
	// (upstream show-moderation-logs.ts, #1539)。
	sinceID, untilID := id.NormalizeCursor(req.SinceID, req.UntilID, req.SinceDate, req.UntilDate)
	logs, err := h.modLogService.List(model.ModerationLogFilter{
		Limit:   pagination.ClampLimit(req.Limit, 10, 100),
		SinceID: sinceID,
		UntilID: untilID,
		Type:    req.Type,
		UserID:  req.UserID,
		Search:  req.Search,
	})
	if err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	// upstream は user を UserDetailedNotMe (optional:false) で pack する。生 model.User
	// を埋めると usernameLower/inbox 等の内部列が漏れ avatarUrl 等 packed field も欠ける。
	// actor (moderator) の profile は重複が多いので distinct user で 1 度だけ fetch して
	// pack する (#1539)。
	profileByUser := map[string]*model.UserProfile{}
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
			prof, ok := profileByUser[l.UserID]
			if !ok {
				if h.userRepo != nil {
					prof, _ = h.userRepo.FindProfileByUserID(l.UserID)
				}
				profileByUser[l.UserID] = prof
			}
			m["user"] = entity.PackUserDetailed(l.User, prof, h.idGen)
		}
		out = append(out, m)
	}
	return c.JSON(http.StatusOK, out)
}
