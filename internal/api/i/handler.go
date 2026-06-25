package i

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/labstack/echo/v4"
	"github.com/lib/pq"
	"github.com/shiroha-a/mk/internal/api/apierr"
	"github.com/shiroha-a/mk/internal/api/notehide"
	"github.com/shiroha-a/mk/internal/core/notification"
	"github.com/shiroha-a/mk/internal/core/role"
	"github.com/shiroha-a/mk/internal/core/twofactor"
	"github.com/shiroha-a/mk/internal/core/user"
	"github.com/shiroha-a/mk/internal/core/wordmute"
	"github.com/shiroha-a/mk/internal/entity"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/misc/langmap"
	miscsmtp "github.com/shiroha-a/mk/internal/misc/smtp"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
	"github.com/shiroha-a/mk/internal/server/middleware"
)

// RoleProvider abstracts role queries for /api/i responses.
// 循環参照を避けるため interface で受け取る。
type RoleProvider interface {
	IsAdministrator(userID string) bool
	IsModerator(userID string) bool
	IsSilenced(userID string) bool
	GetUserRoles(userID string) ([]*model.Role, error)
	GetUserPolicies(userID string) map[string]any
	// HasRolePolicy gates per-policy field updates (canUpdateBioMedia 等)
	// 単発 lookup なので caller は GetUserPolicies を介さずに済む (#1024)。
	HasRolePolicy(userID, policyKey string) bool
}

// EmailSender sends an email message (subject + text + optional HTML).
// SMTP 設定は実装側が Meta から読み取る。テストではスタブを注入する。
// HTML 同送が必要なら Message.HTML を設定する (#600 item 4)。
type EmailSender func(to string, msg miscsmtp.Message)

// AccountMover performs the i/move workflow (AP delivery + user row updates).
// 具体実装は core/move.Service。循環依存を避けるため handler 側には narrow
// interface として置く。
type AccountMover interface {
	Move(src *model.User, dstURI string) error
}

// Handler handles account-related API endpoints.
type Handler struct {
	userService      *user.Service
	idGen            id.Generator
	roleProvider     RoleProvider
	registryRepo     repository.RegistryRepository
	favoriteRepo     repository.NoteFavoriteRepository
	transferEnqueuer TransferEnqueuer
	// #2230: i/delete-account の cascade 削除と連合 Delete 配信 (admin/delete-account と同経路)。
	deleteAccountEnqueuer DeleteAccountEnqueuer
	accountDeletionFed    AccountDeletionFederationHook
	webauthnSvc           *twofactor.WebAuthnService
	securityKeyRepo       repository.UserSecurityKeyRepository
	metaRepo              repository.MetaRepository
	emailSender           EmailSender
	serverURL             string
	signinRepo            repository.SigninRepository
	accessTokenRepo       repository.AccessTokenRepository
	galleryRepo           GalleryRepository
	pageLikeRepo          repository.PageLikeRepository
	mover                 AccountMover
	notificationSvc       UnreadNotificationSource
	achievementNotifier   AchievementNotifier
	followRequestRepo     repository.FollowRequestRepository
	announcementRepo      AnnouncementUnreadSource
	chatRepo              ChatUnreadSource
	antennaUnreadRepo     AntennaUnreadSource
	channelUnreadRepo     ChannelUnreadSource
	piningRepo            repository.UserNotePiningRepository
	noteRepo              repository.NoteRepository
	pageRepo              repository.PageRepository
	instanceRepo          repository.InstanceRepository
	emojiRepo             repository.EmojiRepository
	bufReader             entity.BufferedReactionsReader
	avatarDecorationRepo  repository.AvatarDecorationRepository
	mainStreamPublisher   MainStreamPublisher
	fieldRes              *entity.NoteFieldResolver
	// emailValidationClient は verifymail / truemail SaaS への outbound に
	// 使う SSRF-safe HTTP client (#638)。nil ならデフォルトクライアント。
	emailValidationClient *http.Client
	// verifyLinkClient は i/update の rel=me link 検証で profile field の URL を
	// fetch するための SSRF-safe HTTP client (#1786)。nil なら検証を skip。
	verifyLinkClient *http.Client
	// hardMutePublisher は i/update で hardMutedWords を変更したときに
	// streaming connection に reload signal を流す (#791)。未配線時は
	// publish skip = 旧挙動 (= reconnect で反映)。
	hardMutePublisher HardMutePublisher
	// pinDeliveryHook は note pin/unpin 時に Add/Remove(featured) を followers へ
	// 配信する (#2024)。未配線なら連合配信 skip。
	pinDeliveryHook PinDeliveryHook
	// profileUpdateHook は profile 編集時に Update(Person) を followers へ
	// 配信する (#1560)。nil なら配信しない。
	profileUpdateHook ProfileUpdateHook
	// authInvalidator は i/regenerate-token で旧 token を auth middleware
	// の cache から即時削除するために使う (#884)。未配線時は cache TTL
	// (30 秒) 待ちで stale 旧 token が auth 通過する security regression が
	// 残るので production では必ず wire する。
	authInvalidator TokenInvalidator
	// totpReplayGuard は i/2fa/done / 2fa-gated 操作で同一 TOTP コードの
	// 二度使いを refuse する (RFC 6238 §5.2 / mk-go 独自 hardening、upstream
	// Misskey TS は持たない)。nil なら無保護 (= unit test / dev fallback)。
	// production では必ず wire する。
	totpReplayGuard twofactor.ReplayGuard
	// userRepo は movedTo / alsoKnownAs の URI→ローカルID 解決 (#1255) に使う。
	// FindByURI による local lookup のみで remote fetch しない。
	userRepo repository.UserRepository
	// driveFileRepo は i/import-* の fileId 所有権検証 (#1555) に使う。
	// upstream Misskey の各 import endpoint は driveFilesRepository.findOneBy
	// ({id, userId: me.id}) で「リクエスト元 user 所有の file」のみ許可し、
	// 他人 / 不在 / system file (userId NULL) は NO_SUCH_FILE で弾く。未配線時は
	// fail-closed (= 常に NO_SUCH_FILE) にして cross-user file read を防ぐ。
	driveFileRepo repository.DriveFileRepository
	// importDriveReader は i/import-antennas で file content を同期 download して
	// antenna 件数を数え TOO_MANY_ANTENNAS を enforce するために使う (#1667)。
	// 未配線時は limit check を skip して worker に委ねる (degrade)。
	importDriveReader ImportFileReader
	// antennaCounter は i/import-antennas の TOO_MANY_ANTENNAS 判定で現 antenna
	// 件数を数えるのに使う (#1667)。未配線時は limit check を skip する。
	antennaCounter AntennaCounter
}

// TokenInvalidator は i/regenerate-token / i/change-password 等の sensitive
// 操作後に auth cache の旧 token entry を即時削除するための interface
// (#884)。実装は internal/server/middleware/auth.go の AuthMiddleware が
// 担当する。circular import を避けるため interface 経由で受け取る。
//
// InvalidateTokensForUser は user 自身が複数 device から log-in している
// ケースで「自分の全 session を即時失効したい」操作 (i/delete-account 等)
// で使う (#965)。token 単独削除では他端末の cache が最大 30s stale 続け、
// 削除済 user 名義で操作可能な攻撃面が残るため (PR #966 で admin 経路
// から要求された same-shape gap)。
type TokenInvalidator interface {
	InvalidateToken(token string)
	InvalidateTokensForUser(userID string)
}

// HardMutePublisher publishes a wordmute reload event for the given user.
// Implementation lives in router.go (Redis pubsub adapter), the handler
// only depends on this minimal interface.
type HardMutePublisher interface {
	PublishHardMuteReload(userID string)
}

// ProfileUpdateHook delivers Update(Person) to remote followers on local
// profile edit (#1560)。実装は core/federation。
type ProfileUpdateHook interface {
	OnLocalProfileUpdated(userID string)
}

// PinDeliveryHook delivers Add/Remove(featured collection) to remote followers
// when a local user pins/unpins a note (#2024)。実装は core/federation。
type PinDeliveryHook interface {
	OnLocalPinChanged(userID, noteID string, isAddition bool)
}

// SetPinDeliveryHook wires the AP Add/Remove featured-collection delivery hook (#2024)。
func (h *Handler) SetPinDeliveryHook(hook PinDeliveryHook) {
	h.pinDeliveryHook = hook
}

// SetHardMutePublisher wires a publisher that emits wordmute reload events
// when i/update changes hardMutedWords (#791). Optional; nil disables the
// realtime reload path (the streaming connection still picks up the new
// rules at the next reconnect).
func (h *Handler) SetHardMutePublisher(p HardMutePublisher) {
	h.hardMutePublisher = p
}

// SetProfileUpdateHook wires the AP Update(Person) delivery hook (#1560)。
func (h *Handler) SetProfileUpdateHook(hook ProfileUpdateHook) {
	h.profileUpdateHook = hook
}

// SetEmailValidationClient wires the outbound HTTP client used by
// verifymail / truemail siteverify calls (#638). production では SSRF-safe
// + forward proxy 経由の client を渡すこと。
func (h *Handler) SetEmailValidationClient(c *http.Client) {
	h.emailValidationClient = c
}

// SetVerifyLinkClient wires the SSRF-safe outbound HTTP client used by i/update
// to verify rel=me profile-field backlinks (#1786). nil disables verification.
func (h *Handler) SetVerifyLinkClient(c *http.Client) {
	h.verifyLinkClient = c
}

// SetNoteFieldResolver wires the shared resolver that fills Files /
// MyReaction / Channel on packed pinned notes (#426)。
func (h *Handler) SetNoteFieldResolver(r *entity.NoteFieldResolver) {
	h.fieldRes = r
}

// SetUserRepo wires a UserRepository used to resolve movedTo / alsoKnownAs
// actor URIs to local user IDs (#1255). Unwired (test) leaves those null.
func (h *Handler) SetUserRepo(r repository.UserRepository) {
	h.userRepo = r
}

// resolveUserIDByURI resolves an ActivityPub actor URI to a local user ID via
// a local DB lookup only (no remote fetch). Returns ("", false) when unwired
// or the URI is not known locally.
func (h *Handler) resolveUserIDByURI(uri string) (string, bool) {
	if h.userRepo == nil {
		return "", false
	}
	u, err := h.userRepo.FindByURI(uri)
	if err != nil || u == nil {
		return "", false
	}
	return u.ID, true
}

// resolveAlsoKnownAs converts the i/update alsoKnownAs lines (each an acct
// `@user@host` / `@user` or a canonical AP URI) into a deduplicated list of
// canonical actor URIs, mirroring upstream Misskey TS i/update.ts:445-475.
//
// 各 line につき:
//   - 空文字なら NO_SUCH_USER (upstream の `if (!line) throw noSuchUser`)
//   - acct/URI をローカル DB から解決 (i/move 同様 remote fetch はしない。
//     frontend は移行先 actor 解決後の acct/URI を渡す運用)
//   - 解決した user が自分自身なら FORBIDDEN_TO_SET_YOURSELF
//   - 解決した user の canonical URI が空なら URI_NULL
//
// 戻り値の URI は入力順を保ちつつ重複を除く (upstream の Set 相当)。空 line を
// 1 つも含まない要求が空配列ならそのまま空 slice を返す (= core 側で NULL
// クリア)。
func (h *Handler) resolveAlsoKnownAs(meID string, lines []string) ([]string, *avatarDecorationAPIError) {
	noSuchUser := &avatarDecorationAPIError{
		status: http.StatusBadRequest,
		body:   apierr.Error("NO_SUCH_USER", "No such user.", "fcd2eef9-a9b2-4c4f-8624-038099e90aa5"),
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			return nil, noSuchUser
		}
		u := h.lookupAlsoKnownAsTarget(line)
		if u == nil {
			return nil, noSuchUser
		}
		if u.ID == meID {
			return nil, &avatarDecorationAPIError{
				status: http.StatusBadRequest,
				body:   apierr.Error("FORBIDDEN_TO_SET_YOURSELF", "You can't set yourself as your own alias.", "25c90186-4ab0-49c8-9bba-a1fa6c202ba4"),
			}
		}
		uri := h.canonicalUserURI(u)
		if uri == "" {
			return nil, &avatarDecorationAPIError{
				status: http.StatusBadRequest,
				body:   apierr.Error("URI_NULL", "User ActivityPup URI is null.", "bf326f31-d430-4f97-9933-5d61e4d48a23"),
			}
		}
		if _, dup := seen[uri]; dup {
			continue
		}
		seen[uri] = struct{}{}
		out = append(out, uri)
	}
	return out, nil
}

// lookupAlsoKnownAsTarget resolves a single alsoKnownAs line to a local user
// row. http(s) URIs go through FindByURI; acct forms (`@user@host` / `@user`)
// go through FindByUsernameLower. Returns nil on any miss or when userRepo is
// unwired (fail-closed = NO_SUCH_USER at the caller).
func (h *Handler) lookupAlsoKnownAsTarget(line string) *model.User {
	if h.userRepo == nil {
		return nil
	}
	if strings.HasPrefix(line, "http://") || strings.HasPrefix(line, "https://") {
		u, err := h.userRepo.FindByURI(line)
		if err != nil || u == nil {
			return nil
		}
		return u
	}
	username, host := splitAcct(line)
	if username == "" {
		return nil
	}
	u, err := h.userRepo.FindByUsernameLower(username, host)
	if err != nil || u == nil {
		return nil
	}
	return u
}

// canonicalUserURI returns the actor URI used as the alsoKnownAs alias for u:
// local users get the server-rooted /users/<id> form, remote users surface
// their stored uri. Returns "" when a remote user has no uri (= URI_NULL).
func (h *Handler) canonicalUserURI(u *model.User) string {
	if u.IsLocal() {
		if h.serverURL == "" {
			return ""
		}
		return strings.TrimRight(h.serverURL, "/") + "/users/" + u.ID
	}
	if u.URI != nil {
		return *u.URI
	}
	return ""
}

// splitAcct parses an acct form (`@user@host` / `user@host` / `@user`) into a
// lowercased username and optional host pointer (nil for local). Empty / `@`
// only input yields an empty username so the caller can reject it.
func splitAcct(acct string) (string, *string) {
	trimmed := strings.TrimPrefix(strings.TrimSpace(acct), "@")
	if trimmed == "" {
		return "", nil
	}
	username := trimmed
	var host string
	if at := strings.IndexByte(trimmed, '@'); at >= 0 {
		username = trimmed[:at]
		host = strings.ToLower(strings.TrimSpace(trimmed[at+1:]))
	}
	username = strings.ToLower(username)
	if host == "" {
		return username, nil
	}
	return username, &host
}

// MainStreamPublisher emits events to a single user's `main` WebSocket
// channel. Used here to publish `myTokenRegenerated` so other sessions of
// the same user learn that their API token was invalidated.
// 循環依存を避けるためinterfaceで受け取る(実装はinternal/stream)。
type MainStreamPublisher interface {
	PublishMainEvent(userID, eventType string, body any)
}

// SetAuthInvalidator attaches a token invalidator so RegenerateToken and
// other sensitive endpoints can drop the old token from the auth cache
// immediately (#884、security regression fix)。
func (h *Handler) SetAuthInvalidator(inv TokenInvalidator) {
	h.authInvalidator = inv
}

// invalidateRequestTokenCache removes the current request's token entry
// from the auth middleware tokenCache so that subsequent requests with the
// same token are forced to re-resolve user state from DB. Self-mutation
// handlers call this after committing changes that would otherwise be
// invisible to subsequent middleware.GetUser(c) reads within the cache
// TTL window (#960 / #962 P0).
//
// noop when invalidator is not wired (= unit test or router-config drift)
// or when no token is in the request context (= request was authenticated
// via a non-token mechanism, or middleware wiring drift). Defensive nil-
// guard so handler success paths do not regress on minor wiring changes.
func (h *Handler) invalidateRequestTokenCache(c echo.Context) {
	if h.authInvalidator == nil {
		return
	}
	tok := middleware.GetToken(c)
	if tok == "" {
		return
	}
	h.authInvalidator.InvalidateToken(tok)
}

// invalidateUserTokenCache removes every cached entry that resolves to the
// given userID, across all of the user's tokens (native login + access
// tokens / multi-device sessions). Self-mutation that affects the entire
// session set (e.g. i/delete-account 論理削除) calls this so other devices
// of the same user are also locked out within the cache TTL window
// (#967, sibling of admin-side #966 user-token invalidate).
//
// noop when invalidator is not wired or userID is empty. invalidateRequest
// TokenCache (token 単独削除) との関係: 本 helper は user 全 token を対象
// にするので、自分以外の device session も即時 cache から消える上位互換。
// 自分 1 device しか想定しない update 系では request-token helper の方が
// 軽い (O(1) vs O(N))。
func (h *Handler) invalidateUserTokenCache(userID string) {
	if h.authInvalidator == nil || userID == "" {
		return
	}
	h.authInvalidator.InvalidateTokensForUser(userID)
}

// SetMainStreamPublisher attaches a publisher used to emit events on
// /api/i/* endpoints (currently `myTokenRegenerated`). Optional — nil
// disables emit.
func (h *Handler) SetMainStreamPublisher(p MainStreamPublisher) {
	h.mainStreamPublisher = p
}

// SetTOTPReplayGuard wires the per-user replay guard used to refuse a
// TOTP code that was already consumed within its acceptance window
// (RFC 6238 §5.2). nil disables the protection. Affects i/2fa/done and
// any sensitive endpoint that re-checks the TOTP via verify2FAToken.
func (h *Handler) SetTOTPReplayGuard(g twofactor.ReplayGuard) {
	h.totpReplayGuard = g
}

// SetInstanceRepo attaches an InstanceRepository so favorites/notifications
// note embeds populate UserLite.Instance for remote users (#277).
func (h *Handler) SetInstanceRepo(r repository.InstanceRepository) {
	h.instanceRepo = r
}

func (h *Handler) instanceLookup() entity.InstanceLookup {
	if h.instanceRepo == nil {
		return nil
	}
	return h.instanceRepo
}

// SetEmojiRepo attaches an EmojiRepository so custom emoji shortcodes in
// note text and user displayNames get resolved to URLs.
func (h *Handler) SetEmojiRepo(r repository.EmojiRepository) {
	h.emojiRepo = r
}

// SetReactionReader wires a BufferedReactionsReader so PackNote / PackNotes
// can merge in-flight buffered reaction deltas (#647)。
func (h *Handler) SetReactionReader(r entity.BufferedReactionsReader) {
	h.bufReader = r
}

func (h *Handler) reactionReader() entity.BufferedReactionsReader {
	return h.bufReader
}

func (h *Handler) emojiLookup() entity.EmojiLookup {
	if h.emojiRepo == nil {
		return nil
	}
	return h.emojiRepo
}

// UnreadNotificationSource is the subset of notification.Service used by /api/i
// to compute hasUnreadNotification / unreadNotificationsCount /
// hasUnreadMentions / hasUnreadSpecifiedNotes. Keeping this as a local
// interface avoids pulling core/notification into the handler test harness
// and lets the tests inject a stub.
//
// 3 フラグを 1 度の Redis scan で集約する UnreadSummary を採用済み (#321)。
type UnreadNotificationSource interface {
	UnreadSummary(ctx context.Context, userID string, mentionTypes []notification.Type) (notification.UnreadSummary, error)
}

// AchievementNotifier creates the achievementEarned notification when a user
// unlocks a new achievement (claim-achievement)。未配線なら通知は作らず実績の
// 記録だけ行う (degrade)。実装は *notification.Service。
type AchievementNotifier interface {
	Create(ctx context.Context, in notification.CreateInput) (*notification.Notification, error)
}

// AntennaUnreadSource reports whether a user has any unread antenna notes.
// Satisfied by repository.AntennaNoteUnreadRepository.
type AntennaUnreadSource interface {
	HasAnyByUser(userID string) (bool, error)
}

// ChannelUnreadSource reports whether a user has any unread channel notes.
// Satisfied by repository.ChannelNoteUnreadRepository.
type ChannelUnreadSource interface {
	HasAnyByUser(userID string) (bool, error)
}

// AnnouncementUnreadSource lists active announcements the user has not read.
// Mirrors the subset of AnnouncementRepository needed here.
type AnnouncementUnreadSource interface {
	UnreadForUser(userID string) ([]*model.Announcement, error)
}

// ChatUnreadSource returns the number of unread chat messages for a user.
type ChatUnreadSource interface {
	CountUnread(userID string) (int64, error)
}

// GalleryRepository is the subset of repository.GalleryRepository used by
// the i/gallery/* endpoints. Kept as a handler-local alias so we can
// swap implementations in tests without pulling the full repository.
type GalleryRepository interface {
	ListByUser(userID, sinceID, untilID string, limit, offset int) ([]*model.GalleryPost, error)
	ListLikesByUser(userID, sinceID, untilID string, limit, offset int) ([]*model.GalleryLike, error)
	FindPostsByIDs(ids []string) ([]*model.GalleryPost, error)
	// ExistsLike は GalleryPost.isLiked 解決用 (#1555)。
	ExistsLike(userID, postID string) (bool, error)
}

// SetAccessTokenRepo wires the access_token repo for i/authorized-apps and
// i/revoke-token.
func (h *Handler) SetAccessTokenRepo(r repository.AccessTokenRepository) {
	h.accessTokenRepo = r
}

// SetGalleryRepo wires the gallery repo for i/gallery/*.
func (h *Handler) SetGalleryRepo(r GalleryRepository) { h.galleryRepo = r }

// SetPageLikeRepo wires the page_like repo for i/page-likes.
func (h *Handler) SetPageLikeRepo(r repository.PageLikeRepository) {
	h.pageLikeRepo = r
}

// isSilenced returns whether the given user has any role whose merged
// policies deny canPublicNote. Wraps roleProvider so a nil provider
// (early boot / tests that skip role wiring) yields false.
func (h *Handler) isSilenced(userID string) bool {
	if h.roleProvider == nil {
		return false
	}
	return h.roleProvider.IsSilenced(userID)
}

// SetServerURL sets the base URL used for email verification links.
func (h *Handler) SetServerURL(u string) {
	h.serverURL = u
}

// SetEmailSender attaches an EmailSender for update-email verification.
func (h *Handler) SetEmailSender(s EmailSender) {
	h.emailSender = s
}

// SetMetaRepo attaches a MetaRepository. When set, i/update enforces
// meta.prohibitedWordsForNameOfUser against the display name. Tests that
// don't need this validation can leave it unset.
func (h *Handler) SetMetaRepo(r repository.MetaRepository) {
	h.metaRepo = r
}

// SetWebAuthn attaches the WebAuthn service + security key repository.
// Both are required to enable WebAuthn endpoints; if either is nil the
// register/done/remove/update/passwordless handlers return a no-op 204 (so
// existing test fixtures that don't wire the dependency keep passing).
func (h *Handler) SetWebAuthn(svc *twofactor.WebAuthnService, repo repository.UserSecurityKeyRepository) {
	h.webauthnSvc = svc
	h.securityKeyRepo = repo
}

// SetSigninRepo attaches a SigninRepository for i/signin-history.
func (h *Handler) SetSigninRepo(r repository.SigninRepository) {
	h.signinRepo = r
}

// SetFavoriteRepo attaches a NoteFavoriteRepository for i/favorites.
func (h *Handler) SetFavoriteRepo(r repository.NoteFavoriteRepository) {
	h.favoriteRepo = r
}

// SetAccountMover attaches an AccountMover used by i/move. If unset, i/move
// returns 501 so the endpoint fails loudly instead of silently no-oping.
func (h *Handler) SetAccountMover(m AccountMover) {
	h.mover = m
}

// SetNotificationService wires the notification service used to compute
// hasUnreadNotification / unreadNotificationsCount / hasUnreadMentions on
// /api/i. When unset those fields fall back to default (false/0).
func (h *Handler) SetNotificationService(s UnreadNotificationSource) {
	h.notificationSvc = s
}

// SetAchievementNotifier wires the notification service used to emit the
// achievementEarned notification on claim-achievement.
func (h *Handler) SetAchievementNotifier(s AchievementNotifier) {
	h.achievementNotifier = s
}

// SetFollowRequestRepo wires the follow_request repository used to compute
// hasPendingReceivedFollowRequest on /api/i.
func (h *Handler) SetFollowRequestRepo(r repository.FollowRequestRepository) {
	h.followRequestRepo = r
}

// SetAnnouncementRepo wires the announcement repository used to compute
// hasUnreadAnnouncement / unreadAnnouncements on /api/i.
func (h *Handler) SetAnnouncementRepo(r AnnouncementUnreadSource) {
	h.announcementRepo = r
}

// SetChatRepo wires the chat repository used to compute hasUnreadChatMessages
// on /api/i.
func (h *Handler) SetChatRepo(r ChatUnreadSource) {
	h.chatRepo = r
}

// SetAntennaUnreadRepo wires the antenna_note_unread repository used to
// populate hasUnreadAntenna on /api/i. Optional — nil leaves the field
// false (current behaviour).
func (h *Handler) SetAntennaUnreadRepo(r AntennaUnreadSource) {
	h.antennaUnreadRepo = r
}

// SetChannelUnreadRepo wires the channel_note_unread repository used to
// populate hasUnreadChannel on /api/i. Optional — nil leaves the field
// false (current behaviour).
func (h *Handler) SetChannelUnreadRepo(r ChannelUnreadSource) {
	h.channelUnreadRepo = r
}

// SetPiningRepo wires the user_note_pining repository used to fill
// pinnedNoteIds / pinnedNotes on /api/i.
func (h *Handler) SetPiningRepo(r repository.UserNotePiningRepository) {
	h.piningRepo = r
}

// SetNoteRepo wires the note repository used to pack pinnedNotes entities.
func (h *Handler) SetNoteRepo(r repository.NoteRepository) {
	h.noteRepo = r
}

// SetPageRepo wires the page repository used to fill pinnedPage on /api/i.
func (h *Handler) SetPageRepo(r repository.PageRepository) {
	h.pageRepo = r
}

// SetAvatarDecorationRepo wires the avatar_decoration repository used to
// validate i/update avatarDecorations entries (existence + role gating).
// Optional — nil disables validation and lets i/update accept arbitrary
// decoration ids without checking the catalog.
func (h *Handler) SetAvatarDecorationRepo(r repository.AvatarDecorationRepository) {
	h.avatarDecorationRepo = r
}

// NewHandler creates a new account Handler.
func NewHandler(userService *user.Service, idGen id.Generator) *Handler {
	return &Handler{userService: userService, idGen: idGen}
}

// SetRoleProvider attaches a RoleProvider for dynamic role/policy resolution.
func (h *Handler) SetRoleProvider(rp RoleProvider) {
	h.roleProvider = rp
}

// SetRegistryRepo attaches a RegistryRepository for i/registry/* endpoints.
func (h *Handler) SetRegistryRepo(r repository.RegistryRepository) {
	h.registryRepo = r
}

// RegistryGet handles POST /api/i/registry/get.
func (h *Handler) RegistryGet(c echo.Context) error {
	u := middleware.GetUser(c)
	var req struct {
		Key    string   `json:"key"`
		Scope  []string `json:"scope"`
		Domain *string  `json:"domain"`
	}
	if err := c.Bind(&req); err != nil || req.Key == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "key is required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	if req.Scope == nil {
		req.Scope = []string{}
	}
	if !validRegistryScope(req.Scope) {
		return apierr.JSONInvalidParam(c)
	}
	item, err := h.registryRepo.Get(u.ID, req.Key, req.Scope, registryEffectiveDomain(c, req.Domain))
	if err != nil {
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_KEY", "No such key.", "ac3ed68a-62f0-422b-a7bc-d5e09e8f6a6a"))
	}
	// value をそのまま返す (JSONBの中身)
	return c.JSONBlob(http.StatusOK, item.Value)
}

// RegistrySet handles POST /api/i/registry/set.
func (h *Handler) RegistrySet(c echo.Context) error {
	u := middleware.GetUser(c)
	var req struct {
		Key    string          `json:"key"`
		Value  json.RawMessage `json:"value"`
		Scope  []string        `json:"scope"`
		Domain *string         `json:"domain"`
	}
	if err := c.Bind(&req); err != nil || req.Key == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "key is required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	if req.Scope == nil {
		req.Scope = []string{}
	}
	if !validRegistryScope(req.Scope) {
		return apierr.JSONInvalidParam(c)
	}
	// app token は自分の token id を domain に強制する (#1717)。
	domain := registryEffectiveDomain(c, req.Domain)
	item := &model.RegistryItem{
		ID:        h.idGen.Generate(time.Now()),
		UpdatedAt: time.Now(),
		UserID:    u.ID,
		Key:       req.Key,
		Value:     []byte(req.Value),
		Scope:     req.Scope,
		Domain:    domain,
	}
	if err := h.registryRepo.Set(item); err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	// TS本家 RegistryApiService.set (lines 59-66): domain == null のとき
	// のみmainにpublishする。domain指定はサードパーティアプリ固有の領域
	// なので他クライアントには broadcast しない仕様。app token は domain が
	// token id に強制されるため publish されない (#1717)。
	if h.mainStreamPublisher != nil && domain == nil {
		h.mainStreamPublisher.PublishMainEvent(u.ID, "registryUpdated", map[string]any{
			"scope": req.Scope,
			"key":   req.Key,
			"value": json.RawMessage(req.Value),
		})
	}
	return c.NoContent(http.StatusNoContent)
}

// RegistryGetAll handles POST /api/i/registry/get-all.
func (h *Handler) RegistryGetAll(c echo.Context) error {
	u := middleware.GetUser(c)
	var req struct {
		Scope  []string `json:"scope"`
		Domain *string  `json:"domain"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "Invalid parameters.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	if req.Scope == nil {
		req.Scope = []string{}
	}
	if !validRegistryScope(req.Scope) {
		return apierr.JSONInvalidParam(c)
	}
	items, err := h.registryRepo.GetAll(u.ID, req.Scope, registryEffectiveDomain(c, req.Domain))
	if err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	result := make(map[string]json.RawMessage, len(items))
	for _, item := range items {
		result[item.Key] = json.RawMessage(item.Value)
	}
	return c.JSON(http.StatusOK, result)
}

// RegistryKeysWithType handles POST /api/i/registry/keys-with-type.
func (h *Handler) RegistryKeysWithType(c echo.Context) error {
	u := middleware.GetUser(c)
	var req struct {
		Scope  []string `json:"scope"`
		Domain *string  `json:"domain"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "Invalid parameters.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	if req.Scope == nil {
		req.Scope = []string{}
	}
	if !validRegistryScope(req.Scope) {
		return apierr.JSONInvalidParam(c)
	}
	keys, err := h.registryRepo.KeysWithType(u.ID, req.Scope, registryEffectiveDomain(c, req.Domain))
	if err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	return c.JSON(http.StatusOK, keys)
}

// RegistryRemove handles POST /api/i/registry/remove.
func (h *Handler) RegistryRemove(c echo.Context) error {
	u := middleware.GetUser(c)
	var req struct {
		Key    string   `json:"key"`
		Scope  []string `json:"scope"`
		Domain *string  `json:"domain"`
	}
	if err := c.Bind(&req); err != nil || req.Key == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "key is required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	if req.Scope == nil {
		req.Scope = []string{}
	}
	if !validRegistryScope(req.Scope) {
		return apierr.JSONInvalidParam(c)
	}
	if err := h.registryRepo.Remove(u.ID, req.Key, req.Scope, registryEffectiveDomain(c, req.Domain)); err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	return c.NoContent(http.StatusNoContent)
}

// Me handles POST /api/i - returns the authenticated user's info.
//
// MeDetailed (UserLite + UserDetailed + 11 self-view + notification 3) は
// entity.PackMeDetailed 経由で一括展開し、`/api/i` 固有の private field
// (email / mutedWords / twoFactor* / clientData / role / unread 等) を
// resp map に merge する design (#971)。MeDetailed packer に新 field が
// 追加されたとき自動追従する。
// recordLoginDay appends today's date to the profile's loggedInDates when not
// already present and persists it, mirroring upstream i.ts. The date format is
// server-local `YYYY/M/D` (no zero-padding, 1-indexed month) to match
// `${getFullYear()}/${getMonth()+1}/${getDate()}`. Updates profile in place so
// the response's loggedInDays reflects the new count. Best-effort.
func (h *Handler) recordLoginDay(userID string, profile *model.UserProfile) {
	now := time.Now()
	today := fmt.Sprintf("%d/%d/%d", now.Year(), int(now.Month()), now.Day())
	for _, d := range profile.LoggedInDates {
		if d == today {
			return
		}
	}
	newDates := append(append(pq.StringArray{}, profile.LoggedInDates...), today)
	if err := h.userService.UpdateProfileFields(userID, map[string]any{"loggedInDates": newDates}); err != nil {
		slog.Warn("i: record login day failed", "user", userID, "err", err)
		return
	}
	profile.LoggedInDates = newDates
}

func (h *Handler) Me(c echo.Context) error {
	u := middleware.GetUser(c)

	profile := h.userService.GetProfile(u.ID)

	// upstream i.ts:67-72: /api/i アクセスごとに当日を loggedInDates に追記する
	// (loggedInDays と login-streak achievement の唯一の source、#1767)。
	// best-effort: 永続化失敗でも応答は返す (upstream も update を await しない)。
	if profile != nil {
		h.recordLoginDay(u.ID, profile)
	}

	// MeDetailed を JSON round-trip で map に展開して base にする。
	// 1 request あたり Marshal + Unmarshal が 1 回ずつ走るが、/api/i は
	// per-session 数回の頻度なので影響は無視できる。順序保証は不要
	// (JSON object は順序不定)。
	//
	// errors are infeasible: MeDetailed struct は JSON-safe な primitive と
	// slice / map のみで構成され、`any` 型 field を含まない。万一将来 non-
	// marshalable な field が追加されても、(*json.UnsupportedTypeError) は
	// CI の TestMe_PreservesMeDetailedFields で即検出される (= empty resp
	// で field 不在 assertion が落ちる)。silent leak には繋がらない。
	me := entity.PackMeDetailed(u, profile, h.idGen)
	// movedTo / alsoKnownAs を URI→ローカルID 解決して埋める (#1255)。self
	// view も他 path と同じ shape にするため marshal 前に enrich する。
	me.ResolveMoveTargets(u, h.resolveUserIDByURI)
	b, _ := json.Marshal(me)
	resp := map[string]any{}
	_ = json.Unmarshal(b, &resp)

	// MeDetailed packer に乗っていない /api/i 固有 field を上書き / 追加。
	// avatarId / bannerId / chatScope は PackUserDetailed 経由で同 value が
	// 既に乗っているので override 不要 (#987 review で重複削除)。
	// movedTo / alsoKnownAs / lastFetchedAt は PackUserDetailed +
	// ResolveMoveTargets (上で実施) が golden 互換 shape (movedTo: string|null /
	// alsoKnownAs: string[]|null / lastFetchedAt: string|null) で乗せるので
	// override しない。旧 override は alsoKnownAs を comma-joined string のまま
	// 出していて golden 非互換だった。
	// canChat は PackMeDetailed → UserLite 経由で role policy
	// chatAvailability === 'available' を見るようになった (#988)。
	// 旧 self-view 用 hardcode `resp["canChat"] = true` を撤去し、
	// upstream と同じ role policy ベースの判定に揃える。
	// memo / moderationNote は UserDetailed.Memo (omitempty) / MeDetailed
	// memo は PackMeDetailed (UserDetailed.Memo, #1259) が null で乗せるので
	// override 不要。moderationNote は golden で `moderationNote?: string`
	// (optional・null 不可、moderator が他者を見るとき専用) なので self-view
	// (/api/i) では出さない。旧実装は `resp["moderationNote"]=nil` で null を出し
	// golden 非互換だった (#1270 L3 で検出)。
	// isSilenced は role policy 由来 (MeDetailed/UserDetailedNotMe 共通 field)。
	// PackUserDetailed も entity.SetSilencedLookup 経由で同値を埋めるが、/api/i は
	// handler 層の roleProvider を権威ソースとして明示的に上書きする (両者とも
	// 同一 roleService なので値は一致)。
	resp["isSilenced"] = h.isSilenced(u.ID)

	// isSecure: native login token (cookie / i param) のときだけ true。OAuth /
	// MiAuth の app access token では false (RequireSecure と同一判定)。upstream
	// i.ts の `isSecure = token == null` 相当で、email / emailVerified /
	// securityKeysList を includeSecrets ブロックとして gate する (#1824)。これが
	// 無いと read:account scope の app token だけで email・WebAuthn キー一覧が読めた。
	isSecure := u.Token != nil && *u.Token == middleware.GetToken(c)

	// Private fields from profile (MeDetailed scope 外)
	if profile != nil {
		if isSecure {
			// email / emailVerified は includeSecrets 限定 (upstream
			// UserEntityService.ts:624-626)。app token には出さない。
			resp["email"] = profile.Email
			resp["emailVerified"] = profile.EmailVerified
		}
		resp["twoFactorEnabled"] = profile.TwoFactorEnabled
		resp["usePasswordLessLogin"] = profile.UsePasswordLessLogin
		// mutedWords / mutedInstances は PackMeDetailed (AsMeDetailed) が profile
		// から非null array で乗せる。raw profile 値で override すると空カラム時に
		// null になり golden (string[][] / string[]、非null) 非互換 (#1270 L3 検出)。
		resp["publicReactions"] = profile.PublicReactions
		resp["loggedInDays"] = len(profile.LoggedInDates)
		resp["achievements"] = jsonbArray(profile.Achievements)
		// upstream UserEntityService.ts:580 と同じく securityKeys は 2FA 有効時のみ
		// 真。PackMeDetailed も同 gate だが、ここで profile 値を override するため
		// 揃える (#1921)。
		resp["securityKeys"] = profile.TwoFactorEnabled && profile.SecurityKeysAvailable
		// hardMutedWords / twoFactorBackupCodesStock は PackMeDetailed (AsMeDetailed)
		// が profile から正しい shape で乗せるので override 不要 (#1258 follow-up)。
		// clientData / room は jsonb を生のまま返すと frontend (本家) が
		// オブジェクトとして parse するため、空/不正時は空オブジェクトに
		// 正規化する。user が手動でキーを書き換えるだけなので scheme は持たない。
		resp["clientData"] = jsonbObject(profile.ClientData)
		resp["room"] = jsonbObject(profile.Room)
	}

	// フロントエンド互換性フィールド (Phase 4.5c / Phase 5)
	isAdmin := false
	isMod := false
	userPolicies := role.DefaultPolicies()
	var userRoles []any
	if h.roleProvider != nil {
		isAdmin = h.roleProvider.IsAdministrator(u.ID)
		isMod = h.roleProvider.IsModerator(u.ID)
		userPolicies = h.roleProvider.GetUserPolicies(u.ID)
		if roles, err := h.roleProvider.GetUserRoles(u.ID); err == nil {
			for _, r := range roles {
				userRoles = append(userRoles, map[string]any{
					"id":              r.ID,
					"name":            r.Name,
					"color":           r.Color,
					"iconUrl":         r.IconURL,
					"description":     r.Description,
					"isModerator":     r.IsModerator,
					"isAdministrator": r.IsAdministrator,
					"displayOrder":    r.DisplayOrder,
				})
			}
		}
	}
	if userRoles == nil {
		userRoles = []any{}
	}
	resp["isAdmin"] = isAdmin
	resp["isModerator"] = isMod
	// upstream UserEntityService.ts:574: moderationNote は moderator viewer のみに
	// emit する (空なら '')。/api/i は self-view なので viewer==self、self が moderator
	// のとき自身の profile.moderationNote を返す。非 moderator では field 自体を出さない
	// (upstream の undefined 相当、#2094)。
	if isMod && profile != nil {
		mn := ""
		if profile.ModerationNote != nil {
			mn = *profile.ModerationNote
		}
		resp["moderationNote"] = mn
	}
	// isDeleted / isExplorable は PackMeDetailed 由来で base 展開済 (#971)。
	// 未読系フィールドを依存する repo / service から実際に引く
	// (hasUnreadAntenna / hasUnreadChannel は antennaUnreadRepo /
	// channelUnreadRepo、hasUnreadSpecifiedNotes は notificationSvc.UnreadSummary
	// 経由で fillUnreadFields が populate する)。未wireのものは false/0/[] に
	// フォールバックする (テスト互換)。
	h.fillUnreadFields(c.Request().Context(), u, resp)
	h.fillPinnedFields(c.Request().Context(), u, profile, resp)
	resp["policies"] = userPolicies
	resp["roles"] = userRoles
	// securityKeysList: WebAuthnキーの一覧。includeSecrets 限定 (upstream
	// UserEntityService.ts:627-639) なので native session のときだけ出す。app token
	// では key 自体を省く (#1824、misskey-js golden で optional)。さらに upstream は
	// `twoFactorEnabled ? find() : []` で 2FA 有効時のみ実リストを返すため、無効時は
	// repo を引かず [] にする (#1921)。
	if isSecure {
		if profile != nil && profile.TwoFactorEnabled {
			resp["securityKeysList"] = h.securityKeysList(u.ID)
		} else {
			resp["securityKeysList"] = []any{}
		}
	}
	// MeDetailed の notification-related 3 field (#985) は PackMeDetailed 経由で
	// base 展開済 (#971 で集約)。ここで追加 override は不要。

	// createdAt は ID から復元
	if t, err := h.idGen.ParseTime(u.ID); err == nil {
		resp["createdAt"] = t.UTC().Format("2006-01-02T15:04:05.000Z")
	}

	return c.JSON(http.StatusOK, resp)
}

// UpdateRequest is the request body for i/update.
// 各フィールドはポインタで「未指定なら変更しない」セマンティクスを持つ。
// 文字列のnullable化はrawMessageでなくJSONで対応するため省略している。
type UpdateRequest struct {
	Name              *string `json:"name"`
	Description       *string `json:"description"`
	Location          *string `json:"location"`
	Birthday          *string `json:"birthday"`
	Lang              *string `json:"lang"`
	FollowedMessage   *string `json:"followedMessage"`
	PublicReactions   *bool   `json:"publicReactions"`
	IsLocked          *bool   `json:"isLocked"`
	IsBot             *bool   `json:"isBot"`
	IsCat             *bool   `json:"isCat"`
	IsExplorable      *bool   `json:"isExplorable"`
	HideOnlineStatus  *bool   `json:"hideOnlineStatus"`
	AlwaysMarkNsfw    *bool   `json:"alwaysMarkNsfw"`
	AutoSensitive     *bool   `json:"autoSensitive"`
	NoCrawle          *bool   `json:"noCrawle"`
	PreventAiLearning *bool   `json:"preventAiLearning"`
	// 以下 4 field は upstream Misskey TS i/update.ts:178-188 の paramDef に
	// あり、profile の bool 設定として persist される。response 側は
	// MeDetailed packer (#969) 経由で含まれていたが、UpdateRequest struct
	// から抜けていて silent drop される asymmetric drift があった (#972)。
	AutoAcceptFollowed       *bool `json:"autoAcceptFollowed"`
	CarefulBot               *bool `json:"carefulBot"`
	InjectFeaturedNote       *bool `json:"injectFeaturedNote"`
	ReceiveAnnouncementEmail *bool `json:"receiveAnnouncementEmail"`
	// ChatScope は 1-on-1 DM の受信許可レベル (#692)。
	// upstream paramDef enum: everyone / followers / following / mutual / none
	ChatScope *string `json:"chatScope"`
	// FollowingVisibility / FollowersVisibility はフォロー/フォロワー一覧の
	// 公開範囲 (#1546)。upstream Misskey TS i/update.ts:191-192 paramDef enum:
	// public / followers / private。model 列 (user_profile) と entity packer
	// (read 側) は既にあったが write 経路が抜けていて silent drop していた。
	// users/show 等に followersVisibility gate (#1461) があるのに設定できず
	// privacy 上の drift だったため write を追加する。
	FollowingVisibility *string `json:"followingVisibility"`
	FollowersVisibility *string `json:"followersVisibility"`
	// Room は frontend の「部屋」機能用の任意スキーマ jsonb。
	// 本家も object をそのまま受け取って上書き保存する (部分マージはしない)。
	Room json.RawMessage `json:"room"`
	// AvatarID / BannerID — drive_file の ID。`null` / 省略は不変、空文字列
	// は CLEAR、文字列値は SET。詳細は user.UpdateInput の docコメント参照。
	AvatarID *string `json:"avatarId"`
	BannerID *string `json:"bannerId"`
	// AvatarDecorations は装着するデコレーション配列。nil (省略) なら不変、
	// `[]` (空配列) なら全外し、`[{id,...}, ...]` で上書き。各要素は
	// avatar_decoration テーブルに登録された id を参照する。
	AvatarDecorations *[]AvatarDecorationInput `json:"avatarDecorations"`
	// MutedWords / HardMutedWords は ワードミュート設定 (#787)。
	// frontend は upstream Misskey TS と同じ `[["foo"], ["bar","baz"]]` 形式
	// (内側 array が AND、外側が OR) を JSON で送ってくるので変換せず jsonb
	// 列にそのまま保存する。Bind が json.RawMessage に詰めた段階で内部構造
	// (string / regex) は触らない。soft mute は frontend が `/api/i` のレス
	// ポンスから読んで client-side filter、hard mute は backend が TL fetch
	// 時に CheckWordMute で除外する。
	MutedWords     json.RawMessage `json:"mutedWords"`
	HardMutedWords json.RawMessage `json:"hardMutedWords"`
	// Fields はプロフィール上の name/value ペア (#956)。upstream paramDef は
	// `array of {name, value}` で maxItems 16。nil (= 省略) は不変、`[]` で
	// クリア、要素ありで上書き。core/user 側で trim + 空 entry 排除を行う。
	Fields *[]FieldInput `json:"fields"`
	// AlsoKnownAs は引越し元エイリアス (#1546)。upstream Misskey TS
	// i/update.ts:226-231 paramDef: array maxItems 10 uniqueItems。各要素は
	// acct (@user@host) または canonical URI。handler が resolveAlsoKnownAs で
	// canonical URI 列へ解決し core に渡す。nil (省略) は不変、`[]` は NULL
	// クリア。i/move の前提となる alsoKnownAs を設定する経路 (これが無いと
	// 移行 flow が成立しない)。
	AlsoKnownAs *[]string `json:"alsoKnownAs"`
	// 以下は #1546 で追加した i/update 残りの paramDef field。いずれも model 列 /
	// entity packer (read 側) は既にあり write 経路だけ抜けていた asymmetric drift。
	//
	// NotificationRecieveConfig は通知種別ごとの受信設定 object (jsonb)。
	// MutedInstances / EmailNotificationTypes は string 配列 (jsonb)。
	// いずれも json.RawMessage で受けて jsonb 列へそのまま書く (len 0 = 省略=不変)。
	NotificationRecieveConfig json.RawMessage `json:"notificationRecieveConfig"`
	MutedInstances            json.RawMessage `json:"mutedInstances"`
	EmailNotificationTypes    json.RawMessage `json:"emailNotificationTypes"`
	// PinnedPageID はプロフィールにピン留めする page id (nullable)。upstream は
	// truthy-string で所有権検証後に SET、null で CLEAR、undefined で不変。
	// json.RawMessage で 3 状態 (省略 / null / 値) を区別する。
	PinnedPageID json.RawMessage `json:"pinnedPageId"`
	// RequireSigninToViewContents は未ログインへのコンテンツ非表示設定 (bool)。
	RequireSigninToViewContents *bool `json:"requireSigninToViewContents"`
	// MakeNotesFollowersOnlyBefore / MakeNotesHiddenBefore は過去ノートの自動
	// followers-only 化 / 非表示化の基準時刻 (integer nullable)。null で解除、
	// 値で設定、省略で不変。json.RawMessage で 3 状態を区別する。
	MakeNotesFollowersOnlyBefore json.RawMessage `json:"makeNotesFollowersOnlyBefore"`
	MakeNotesHiddenBefore        json.RawMessage `json:"makeNotesHiddenBefore"`
}

// FieldInput is the {name, value} shape accepted by i/update for profile
// fields (#956). upstream paramDef のように name / value を string で
// 受け取り、core/user 側で trim + 空 entry 除外を行う。
type FieldInput struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// AvatarDecorationInput is one entry of the avatarDecorations array on
// i/update. Only id is required; angle / flipH / offsetX / offsetY default
// to 0 / false / 0 / 0 when omitted (matching upstream).
type AvatarDecorationInput struct {
	ID      string   `json:"id"`
	Angle   *float64 `json:"angle,omitempty"`
	FlipH   *bool    `json:"flipH,omitempty"`
	OffsetX *float64 `json:"offsetX,omitempty"`
	OffsetY *float64 `json:"offsetY,omitempty"`
}

// jsonbObject normalizes a raw jsonb byte slice into map[string]any for the
// Me response. Empty or malformed payloads become an empty object so the
// frontend always sees a stable shape.
func jsonbObject(raw []byte) any {
	if len(raw) == 0 {
		return map[string]any{}
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil || out == nil {
		return map[string]any{}
	}
	return out
}

// jsonbArray normalizes a raw jsonb byte slice into []any for the Me response.
// Empty or malformed payloads become an empty array.
func jsonbArray(raw []byte) any {
	if len(raw) == 0 {
		return []any{}
	}
	var out []any
	if err := json.Unmarshal(raw, &out); err != nil || out == nil {
		return []any{}
	}
	return out
}

// meDetailedWithUnread packs a MeDetailed for self-view stream/return paths
// (i/update, meUpdated) and enriches it with authoritative unread values via
// fillUnreadFields. Raw PackMeDetailed would emit the struct defaults
// (unreadNotificationsCount:0 / unreadAnnouncements:[] / hasUnread*:false),
// which the frontend's updateCurrentAccountPartial would merge into `$i`,
// clobbering the real badge/unread state. Going through fillUnreadFields keeps
// those fields correct on every path (the same shape /api/i returns).
func (h *Handler) meDetailedWithUnread(ctx context.Context, u *model.User, profile *model.UserProfile) map[string]any {
	me := entity.PackMeDetailed(u, profile, h.idGen)
	b, _ := json.Marshal(me)
	resp := map[string]any{}
	_ = json.Unmarshal(b, &resp)
	h.fillUnreadFields(ctx, u, resp)
	return resp
}

// appendIncludeSecrets adds the includeSecrets-gated MeDetailed fields (email /
// emailVerified / securityKeysList) to resp when the request is a native login
// session, mirroring upstream pack(..., {includeSecrets: isSecure}). isSecure =
// the request's raw token equals users.token (= RequireSecure と同一判定);
// OAuth/MiAuth の app token (token != users.token) では出さない。
//
// /api/i (Me) は同等の判定を inline で持つ (#1824)。/api/i/update もこの helper で
// native session に email 系を返す (#1919)。両者が drift しないよう、変更時は
// Me handler の isSecure ブロックと揃えること。
func (h *Handler) appendIncludeSecrets(c echo.Context, u *model.User, profile *model.UserProfile, resp map[string]any) {
	if u == nil || u.Token == nil || *u.Token != middleware.GetToken(c) {
		return
	}
	if profile != nil {
		resp["email"] = profile.Email
		resp["emailVerified"] = profile.EmailVerified
	}
	// upstream UserEntityService.ts:627 は `twoFactorEnabled ? find() : []` なので
	// 2FA 有効時のみ実リスト、無効時は [] (#1921)。Me handler の isSecure ブロックと
	// 同 gate を保つこと。
	if profile != nil && profile.TwoFactorEnabled {
		resp["securityKeysList"] = h.securityKeysList(u.ID)
	} else {
		resp["securityKeysList"] = []map[string]any{}
	}
}

// securityKeysList returns the user's WebAuthn key list for the includeSecrets
// block, returning [] when no repo is wired or no keys exist.
func (h *Handler) securityKeysList(userID string) []map[string]any {
	list := []map[string]any{}
	if h.securityKeyRepo == nil {
		return list
	}
	keys, err := h.securityKeyRepo.ListByUser(userID)
	if err != nil {
		return list
	}
	for _, k := range keys {
		list = append(list, map[string]any{
			"id":       k.ID,
			"name":     k.Name,
			"lastUsed": k.LastUsed.UTC().Format("2006-01-02T15:04:05.000Z"),
		})
	}
	return list
}

// countMuteWords returns the total number of entries in a muted-words
// payload, flattening AND-groups. mirrors upstream Misskey TS i/update.ts
// `checkMuteWordCount`: 各 entry が string なら 1、array なら array.length
// を加算する (#1029)。raw が nil / 空なら 0。
func countMuteWords(raw json.RawMessage) int {
	if len(raw) == 0 {
		return 0
	}
	var arr []any
	if err := json.Unmarshal(raw, &arr); err != nil {
		return 0
	}
	length := 0
	for _, item := range arr {
		switch v := item.(type) {
		case string:
			length++
		case []any:
			length += len(v)
		}
	}
	return length
}

// normalizeMutedWords validates and normalizes the mutedWords / hardMutedWords
// payload from i/update. Returns (raw, ok, err): ok=false means "field not
// present in request" (caller should leave the column unchanged); err != nil
// means the payload is invalid (caller should 400 the request).
//
// Accepts only top-level JSON arrays (including the empty array which clears
// the column). Internal structure (string | array of string for AND-grouping,
// regex literals like `/foo/i`) is forwarded verbatim — frontend matches the
// same wire format and the backend CheckWordMute helper interprets it later.
func normalizeMutedWords(raw json.RawMessage) (json.RawMessage, bool, error) {
	if len(raw) == 0 {
		return nil, false, nil
	}
	// `null` は明示的に omit と同義 (frontend が clear 意図で送ることは無い、
	// upstream paramDef も nullable: false)。
	trimmed := bytes.TrimSpace(raw)
	if bytes.Equal(trimmed, []byte("null")) {
		return nil, false, nil
	}
	// top-level が配列であることだけ確認する (内側は CheckWordMute が解釈)。
	var v any
	if err := json.Unmarshal(trimmed, &v); err != nil {
		return nil, false, err
	}
	if _, ok := v.([]any); !ok {
		return nil, false, errors.New("mutedWords must be an array")
	}
	out := append(json.RawMessage(nil), trimmed...)
	return out, true, nil
}

// containsProhibitedWord reports whether name contains any entry from words
// case-insensitively (substring match). Empty or whitespace-only entries are
// skipped so a misconfigured empty element cannot ban every display name.
func containsProhibitedWord(name string, words []string) bool {
	lname := strings.ToLower(name)
	for _, w := range words {
		w = strings.TrimSpace(w)
		if w == "" {
			continue
		}
		if strings.Contains(lname, strings.ToLower(w)) {
			return true
		}
	}
	return false
}

// isValidFollowVisibility reports whether v is one of the
// followingVisibility / followersVisibility enum values accepted by upstream
// Misskey i/update (public / followers / private). Used to fail-close on any
// other value instead of silently persisting a bogus privacy setting.
// parseNullableInt decodes a request json.RawMessage for an integer-nullable
// i/update field into the **int 3-state convention used by user.UpdateInput
// (#1546): absent (len 0) → (nil,false) no change; explicit null →
// (&(*int)(nil),true) clear; integer value → (&&v,true) set. A non-integer,
// non-null body is a bind error.
func parseNullableInt(raw json.RawMessage) (**int, bool, error) {
	if len(raw) == 0 {
		return nil, false, nil
	}
	if string(bytes.TrimSpace(raw)) == "null" {
		var clear *int
		return &clear, true, nil
	}
	var n int
	if err := json.Unmarshal(raw, &n); err != nil {
		return nil, false, err
	}
	p := &n
	return &p, true, nil
}

func isValidFollowVisibility(v string) bool {
	switch model.FollowingVisibility(v) {
	case model.FollowingVisibilityPublic,
		model.FollowingVisibilityFollowers,
		model.FollowingVisibilityPrivate:
		return true
	default:
		return false
	}
}

// Update handles POST /api/i/update.
// birthdayPattern mirrors upstream birthdaySchema (User.ts:325)。
var birthdayPattern = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}$`)

// profileTextWithinLimit reports whether s (when present and non-empty) is
// within max code points, mirroring upstream nameSchema/descriptionSchema/
// locationSchema/followedMessageSchema maxLength (#1918)。nil / "" (= mk-go の
// クリア要求) は常に許容する。長さは他の mk-go handler と同じく rune 数で数える。
func profileTextWithinLimit(s *string, max int) bool {
	if s == nil || *s == "" {
		return true
	}
	return utf8.RuneCountInString(*s) <= max
}

func (h *Handler) Update(c echo.Context) error {
	me := middleware.GetUser(c)

	var req UpdateRequest
	if err := c.Bind(&req); err != nil {
		return apierr.JSONInvalidParam(c)
	}

	// upstream paramDef の maxLength / pattern / enum を検証する (#1918)。
	// name≤50 / description≤1500 / location≤50 / followedMessage≤256、
	// birthday は ^YYYY-MM-DD$、lang は langmap enum。空文字 ("") は mk-go の
	// クリア要求として通すため検証は非空値のみに適用し、違反は INVALID_PARAM(400)。
	if !profileTextWithinLimit(req.Name, 50) ||
		!profileTextWithinLimit(req.Description, 1500) ||
		!profileTextWithinLimit(req.Location, 50) ||
		!profileTextWithinLimit(req.FollowedMessage, 256) {
		return apierr.JSONInvalidParam(c)
	}
	if req.Birthday != nil && *req.Birthday != "" && !birthdayPattern.MatchString(*req.Birthday) {
		return apierr.JSONInvalidParam(c)
	}
	if req.Lang != nil && *req.Lang != "" && !langmap.IsValid(*req.Lang) {
		return apierr.JSONInvalidParam(c)
	}

	// upstream Misskey TS i/update.ts: role policy alwaysMarkNsfw=true の user は
	// profile flag を変更できない (admin 強制 NSFW marker の保護、#1028)。
	//
	// 「許可型」policy (canUpdateBioMedia 等) と挙動が逆で、true = 制限 ON な
	// 「禁止型」policy なので、HasRolePolicy の admin bypass (admin に対して
	// 常に true を返す) を使うと admin が逆に制限されてしまう。GetUserPolicies
	// で生値を直接見て、admin / moderator は明示的に bypass する。
	if req.AlwaysMarkNsfw != nil && h.roleProvider != nil &&
		!h.roleProvider.IsAdministrator(me.ID) && !h.roleProvider.IsModerator(me.ID) {
		policies := h.roleProvider.GetUserPolicies(me.ID)
		if v, ok := policies[role.PolicyAlwaysMarkNsfw].(bool); ok && v {
			return apierr.JSONRestrictedByRole(c)
		}
	}

	// 全 *bool field は req と in の field type が一致するので struct literal
	// で直接代入する。req.X が nil なら in.X も nil (= service 側で no-op
	// 扱い) になり、`if != nil` block 経由と意味的に等価。
	in := user.UpdateInput{
		IsLocked:                 req.IsLocked,
		IsBot:                    req.IsBot,
		IsCat:                    req.IsCat,
		IsExplorable:             req.IsExplorable,
		HideOnlineStatus:         req.HideOnlineStatus,
		AlwaysMarkNsfw:           req.AlwaysMarkNsfw,
		AutoSensitive:            req.AutoSensitive,
		NoCrawle:                 req.NoCrawle,
		PreventAiLearning:        req.PreventAiLearning,
		PublicReactions:          req.PublicReactions,
		AutoAcceptFollowed:       req.AutoAcceptFollowed,
		CarefulBot:               req.CarefulBot,
		InjectFeaturedNote:       req.InjectFeaturedNote,
		ReceiveAnnouncementEmail: req.ReceiveAnnouncementEmail,
	}
	if req.Name != nil {
		// 表示名の禁止ワードチェック。meta 未注入 / 未設定時は素通りする。
		// 本家 Misskey と同様に部分一致 case-insensitive で、空文字 ("") の
		// クリアリクエストは検査対象外 (ユーザー体験上必要)。
		if h.metaRepo != nil && *req.Name != "" {
			if m, err := h.metaRepo.Fetch(); err == nil && containsProhibitedWord(*req.Name, m.ProhibitedWordsForNameOfUser) {
				// upstream update.ts: code=YOUR_NAME_CONTAINS_PROHIBITED_WORDS /
				// id=0b3f9f6a-2f4d-4b1f-9fb4-49d3a2fd7191 / httpStatusCode=422 (#1546)。
				return c.JSON(http.StatusUnprocessableEntity, apierr.Error("YOUR_NAME_CONTAINS_PROHIBITED_WORDS", "Your new name contains prohibited words.", "0b3f9f6a-2f4d-4b1f-9fb4-49d3a2fd7191"))
			}
		}
		in.Name = &req.Name
	}
	if req.Description != nil {
		in.Description = &req.Description
	}
	if req.Location != nil {
		in.Location = &req.Location
	}
	if req.Birthday != nil {
		in.Birthday = &req.Birthday
	}
	if req.Lang != nil {
		in.Lang = &req.Lang
	}
	if req.FollowedMessage != nil {
		in.FollowedMessage = &req.FollowedMessage
	}
	if len(req.Room) > 0 {
		// json.RawMessage は親の Unmarshal が構文チェック済みの
		// バイト列を格納する。改変されないよう独自のスライスにコピーする。
		room := append(json.RawMessage(nil), req.Room...)
		in.Room = &room
	}
	// ワードミュート (#787)。空 byte = field 未指定 (omit) なので不変。
	// `[]` (= 2 byte の空配列) はクリア要求として通す。validation は配列形式
	// であることだけ。upstream paramDef も内側構造は `array of (string |
	// string[])` で、要素 0 個も許容する。
	// wordMuteLimit role policy gate (#1029)。upstream
	// `checkMuteWordCount(mutedWords, policies.wordMuteLimit)` と同 logic。
	// mutedWords / hardMutedWords 両方が指定された request で同 policy を 2 度
	// lookup しないよう、helper closure で memoize する (canUpdateBioMedia と
	// 同 pattern, upstream の `policies ??= await ...` と等価)。
	var (
		wordMuteLimitChecked bool
		wordMuteLimitValue   int
		wordMuteLimitOk      bool
	)
	wordMuteLimit := func() (int, bool) {
		if !wordMuteLimitChecked {
			wordMuteLimitChecked = true
			if h.roleProvider != nil {
				if v, ok := h.roleProvider.GetUserPolicies(me.ID)["wordMuteLimit"].(int); ok && v >= 0 {
					wordMuteLimitValue = v
					wordMuteLimitOk = true
				}
			}
		}
		return wordMuteLimitValue, wordMuteLimitOk
	}
	// #2106 L3: upstream i/update.ts は validateMuteWordRegex で /pattern/flags リテラルが
	// compile 不能なら INVALID_REGEXP(400) を投げる。mk-go も書き込み時に検証する (読み取り側
	// wordmute は不正 regex を nil rule で握り潰すため、ここで弾かないとユーザーへの
	// フィードバックが無く黙殺される)。
	if wordmute.ValidateMutedWords(req.MutedWords) != nil || wordmute.ValidateMutedWords(req.HardMutedWords) != nil {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_REGEXP", "Invalid Regular Expression.", "0d786918-10df-41cd-8f33-8dec7d9a89a5"))
	}
	if mw, ok, err := normalizeMutedWords(req.MutedWords); err != nil {
		return apierr.JSONInvalidParam(c)
	} else if ok {
		if limit, lok := wordMuteLimit(); lok && countMuteWords(mw) > limit {
			return apierr.JSONTooManyMutedWords(c)
		}
		in.MutedWords = &mw
	}
	if mw, ok, err := normalizeMutedWords(req.HardMutedWords); err != nil {
		return apierr.JSONInvalidParam(c)
	} else if ok {
		if limit, lok := wordMuteLimit(); lok && countMuteWords(mw) > limit {
			return apierr.JSONTooManyMutedWords(c)
		}
		in.HardMutedWords = &mw
	}
	// upstream Misskey TS i/update.ts: avatarId / bannerId が non-null 文字列で
	// 指定された場合に canUpdateBioMedia policy gate。null (= 解除) は対象外
	// (= 自分の avatar/banner 解除は常に可、設定だけ制限される #1024)。
	// avatarId と bannerId 両方が指定された request で同 policy を 2 度 lookup
	// しないよう、helper closure で memoize する (upstream の
	// `policies ??= await ...` と同 pattern)。
	var bioMediaChecked, bioMediaAllowed bool
	canUpdateBioMedia := func() bool {
		if !bioMediaChecked {
			bioMediaAllowed = h.roleProvider == nil ||
				h.roleProvider.HasRolePolicy(me.ID, role.PolicyCanUpdateBioMedia)
			bioMediaChecked = true
		}
		return bioMediaAllowed
	}
	if req.AvatarID != nil {
		if v := req.AvatarID; v != nil && *v != "" && !canUpdateBioMedia() {
			return apierr.JSONRestrictedByRole(c)
		}
		in.AvatarID = req.AvatarID
	}
	if req.BannerID != nil {
		if v := req.BannerID; v != nil && *v != "" && !canUpdateBioMedia() {
			return apierr.JSONRestrictedByRole(c)
		}
		in.BannerID = req.BannerID
	}
	if req.AvatarDecorations != nil {
		normalized, apiErr := h.normalizeAvatarDecorations(me.ID, *req.AvatarDecorations)
		if apiErr != nil {
			return c.JSON(apiErr.status, apiErr.body)
		}
		in.AvatarDecorations = &normalized
	}
	if req.ChatScope != nil {
		// upstream paramDef と同じ enum のみ受け付ける (#692)。
		// 不正値を弾かないと cherrypick の chatScope 判定 (none/followers/...)
		// から漏れて "誰からも受信不能" のような壊れた状態を作りうる。
		switch *req.ChatScope {
		case "everyone", "followers", "following", "mutual", "none":
			in.ChatScope = req.ChatScope
		default:
			return apierr.JSONInvalidParam(c)
		}
	}
	// followingVisibility / followersVisibility は upstream paramDef と同じ
	// enum (public / followers / private) のみ受け付ける (#1546)。enum 外は
	// INVALID_PARAM で fail-close し、不正値が privacy gate を素通りしない
	// ようにする。null / 省略は no-op。
	if req.FollowingVisibility != nil {
		if !isValidFollowVisibility(*req.FollowingVisibility) {
			return apierr.JSONInvalidParam(c)
		}
		in.FollowingVisibility = req.FollowingVisibility
	}
	if req.FollowersVisibility != nil {
		if !isValidFollowVisibility(*req.FollowersVisibility) {
			return apierr.JSONInvalidParam(c)
		}
		in.FollowersVisibility = req.FollowersVisibility
	}
	if req.Fields != nil {
		// upstream paramDef は maxItems 16。超過は INVALID_PARAM で弾く
		// (= cherrypick / Misskey TS と同じ挙動)。各要素の trim / 空除外は
		// core/user 側で行う。
		if len(*req.Fields) > 16 {
			return apierr.JSONInvalidParam(c)
		}
		items := make([]user.FieldItem, len(*req.Fields))
		for i, f := range *req.Fields {
			items[i] = user.FieldItem{Name: f.Name, Value: f.Value}
		}
		in.Fields = &items
	}
	if req.AlsoKnownAs != nil {
		// 引越し元エイリアス (#1546)。upstream update.ts:226-231,445-475 と同順:
		//  1. 既に movedToUri 済なら YOUR_ACCOUNT_MOVED (403)
		//  2. 各 line を acct/URI → canonical URI へ解決
		//     - 解決失敗で NO_SUCH_USER、自分指定で FORBIDDEN_TO_SET_YOURSELF、
		//       resolved user の URI が空なら URI_NULL
		//  3. 重複排除した URI 群を core に渡す (空なら NULL クリア)
		// upstream paramDef maxItems 10 / uniqueItems も合わせて enforce する。
		if me.MovedToURI != nil && *me.MovedToURI != "" {
			return c.JSON(http.StatusForbidden, apierr.Error(
				"YOUR_ACCOUNT_MOVED", "You have moved your account.",
				"56f20ec9-fd06-4fa5-841b-edd6d7d4fa31",
			))
		}
		if len(*req.AlsoKnownAs) > 10 {
			return apierr.JSONInvalidParam(c)
		}
		resolved, apiErr := h.resolveAlsoKnownAs(me.ID, *req.AlsoKnownAs)
		if apiErr != nil {
			return c.JSON(apiErr.status, apiErr.body)
		}
		in.AlsoKnownAs = &resolved
	}
	// #1546 残り field の request → input 変換。
	// jsonb passthrough (notificationRecieveConfig / mutedInstances /
	// emailNotificationTypes)。len 0 = 省略=不変。親 Unmarshal 済バイト列を
	// コピーして所有権を切り離す。
	if len(req.NotificationRecieveConfig) > 0 {
		v := append(json.RawMessage(nil), req.NotificationRecieveConfig...)
		in.NotificationRecieveConfig = &v
	}
	if len(req.MutedInstances) > 0 {
		v := append(json.RawMessage(nil), req.MutedInstances...)
		in.MutedInstances = &v
	}
	if len(req.EmailNotificationTypes) > 0 {
		v := append(json.RawMessage(nil), req.EmailNotificationTypes...)
		in.EmailNotificationTypes = &v
	}
	in.RequireSigninToViewContents = req.RequireSigninToViewContents
	if v, ok, err := parseNullableInt(req.MakeNotesFollowersOnlyBefore); err != nil {
		return apierr.JSONInvalidParam(c)
	} else if ok {
		in.MakeNotesFollowersOnlyBefore = v
	}
	if v, ok, err := parseNullableInt(req.MakeNotesHiddenBefore); err != nil {
		return apierr.JSONInvalidParam(c)
	} else if ok {
		in.MakeNotesHiddenBefore = v
	}
	// pinnedPageId は upstream と同じ 3 分岐: null → CLEAR、truthy id →
	// 所有権検証して SET、それ以外 (省略 / "") → 不変 (upstream の
	// `if (ps.pinnedPageId)` は "" が falsy で no-op)。
	if len(req.PinnedPageID) > 0 {
		if string(bytes.TrimSpace(req.PinnedPageID)) == "null" {
			var clear *string
			in.PinnedPageID = &clear
		} else {
			var pid string
			if err := json.Unmarshal(req.PinnedPageID, &pid); err != nil {
				return apierr.JSONInvalidParam(c)
			}
			if pid != "" {
				// 所有権検証: 自分の page でなければ NO_SUCH_PAGE。
				if h.pageRepo != nil {
					p, perr := h.pageRepo.FindByID(pid)
					if perr != nil || p == nil || p.UserID != me.ID {
						return c.JSON(http.StatusBadRequest, apierr.Error("NO_SUCH_PAGE", "No such page.", "8e01b590-7eb9-431b-a239-860e086c408e"))
					}
				}
				ptr := &pid
				in.PinnedPageID = &ptr
			}
		}
	}

	bundle, err := h.userService.UpdateProfile(me.ID, in)
	if err != nil {
		switch {
		case errors.Is(err, user.ErrUserNotFound):
			return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_USER", "No such user.", "fcd2eef9-a9b2-4c4f-8624-038099e90aa5"))
		case errors.Is(err, user.ErrAvatarNotFound):
			// upstream Misskey の NO_SUCH_AVATAR error UUID を流用 (frontend
			// がコード固有の locale 表示をしているため一致が望ましい)。
			return c.JSON(http.StatusBadRequest, apierr.Error("NO_SUCH_AVATAR", "No such avatar file.", "539f3a45-f215-4f81-a9a8-31293640207f"))
		case errors.Is(err, user.ErrAvatarNotImage):
			return c.JSON(http.StatusBadRequest, apierr.Error("AVATAR_NOT_AN_IMAGE", "The file specified as an avatar is not an image.", "f419f9f8-2f4d-46b1-9fb4-49d3a2fd7191"))
		case errors.Is(err, user.ErrBannerNotFound):
			return c.JSON(http.StatusBadRequest, apierr.Error("NO_SUCH_BANNER", "No such banner file.", "0d8f5629-f210-41c2-9433-735831a58595"))
		case errors.Is(err, user.ErrBannerNotImage):
			return c.JSON(http.StatusBadRequest, apierr.Error("BANNER_NOT_AN_IMAGE", "The file specified as a banner is not an image.", "75aedb19-2afd-4e6d-87fc-67941256fa60"))
		}
		return apierr.JSONInternalError(c)
	}

	// upstream i/update.ts:532-593: verifiedLinks を一旦 [] にリセットし、profile
	// fields の https URL それぞれについて rel=me backlink を非同期に検証して
	// verifiedLinks に積み直す (#1786)。同期で [] に reset (応答も空で返す)、
	// goroutine で再検証して書き込む。fire-and-forget は upstream と同様。
	if h.verifyLinkClient != nil && me.Username != "" && h.serverURL != "" {
		if err := h.userService.UpdateProfileFields(me.ID, map[string]any{"verifiedLinks": pq.StringArray{}}); err == nil && bundle.Profile != nil {
			bundle.Profile.VerifiedLinks = nil
		}
		myLink := strings.TrimRight(h.serverURL, "/") + "/@" + me.Username
		if urls := httpsFieldValues(bundle.Profile); len(urls) > 0 {
			uid := me.ID
			go func() {
				if verified := verifyLinks(h.verifyLinkClient, myLink, urls); len(verified) > 0 {
					_ = h.userService.UpdateProfileFields(uid, map[string]any{"verifiedLinks": pq.StringArray(verified)})
				}
			}()
		}
	}

	// hardMutedWords が変更された場合は streaming connection に reload signal
	// を流して即時反映する (#791)。soft (mutedWords) は frontend が /api/i から
	// 取り直して client-side で filter するので publish 不要。
	if in.HardMutedWords != nil && h.hardMutePublisher != nil {
		h.hardMutePublisher.PublishHardMuteReload(me.ID)
	}

	// profile 変更を remote followers に Update(Person) で配信する (#1560)。
	if h.profileUpdateHook != nil {
		h.profileUpdateHook.OnLocalProfileUpdated(me.ID)
	}

	// auth middleware の tokenCache (30 秒 TTL) は name / description などの
	// mutable field を持つ user object を保持するため、更新後も同じ token で
	// stale な値が返り続けてしまう。helper 経由で本 request の token を即時
	// invalidate して、次回 lookup で DB から fresh fetch させる (#960)。
	h.invalidateRequestTokenCache(c)

	// upstream Misskey TS UserEntityService.ts:590-630 の MeDetailed shape を
	// drop-in 互換で返す。i/update のレスポンスはフロントの
	// updateCurrentAccountPartial にそのまま流し込まれるため、isExplorable /
	// noCrawle / preventAiLearning など self-view-only field が UserDetailed
	// 側に無いと session の `$i` が更新されず stale なまま残る (#968)。
	// unread 系は meDetailedWithUnread で実値を埋める。生 PackMeDetailed だと
	// unreadNotificationsCount:0 等の default が `$i` を clobber する (#1258 fu)。
	resp := h.meDetailedWithUnread(c.Request().Context(), bundle.User, bundle.Profile)
	// #2106 N1: isAdmin/isModerator は role 依存で PackMeDetailed が埋めない (MeDetailed の
	// 両 field は omitempty 無し bool なので default false が乗る)。Me handler 同様
	// roleProvider から override し upstream の MeDetailed isMe ブロック (実値を返す) に揃える。
	if h.roleProvider != nil {
		resp["isAdmin"] = h.roleProvider.IsAdministrator(bundle.User.ID)
		resp["isModerator"] = h.roleProvider.IsModerator(bundle.User.ID)
	}
	// upstream i/update は pack(..., {includeSecrets: isSecure}) を返すため native
	// session では email / emailVerified / securityKeysList を含める (#1919)。Token
	// 判定は middleware が確実に load する me を使い、email は更新後の最新 profile を
	// 使う。app token (token != users.token) では出さない。
	h.appendIncludeSecrets(c, me, bundle.Profile, resp)
	return c.JSON(http.StatusOK, resp)
}

// avatarDecorationAPIError carries a (status, body) pair from
// normalizeAvatarDecorations back to the i/update handler so callers can emit
// the precise Misskey-compatible error payload.
type avatarDecorationAPIError struct {
	status int
	body   map[string]any
}

// normalizeAvatarDecorations validates the requested decoration array and
// returns the JSON bytes ready for `user.avatarDecorations` jsonb storage.
//
// 検証順:
//  1. policies.avatarDecorationLimit を超える長さは TOO_MANY_AVATAR_DECORATIONS で 400
//  2. 各 id が avatar_decoration テーブルに存在しなければ NO_SUCH_AVATAR_DECORATION で 400
//  3. decoration.roleIdsThatCanBeUsedThisDecoration が空でない場合、
//     ユーザーが許可ロールを持っていなければ RESTRICTED_BY_ROLE で 400
//
// avatarDecorationRepo / roleProvider が未配線でも処理は継続する (length
// チェックは default 1、catalog / role 検証は repo / provider が無い場合
// skip する)。これは Update ハンドラを最小依存で test できるようにする
// ための fallback。
//
// 戻り値の JSON は `[{id,angle,flipH,offsetX,offsetY}, ...]` 形式。空配列
// なら `[]` を返してデコレーション全外しを表現する。
func (h *Handler) normalizeAvatarDecorations(userID string, in []AvatarDecorationInput) ([]byte, *avatarDecorationAPIError) {
	limit := 1
	if h.roleProvider != nil {
		policies := h.roleProvider.GetUserPolicies(userID)
		// role override 経路は role.Policies の jsonb を json.Unmarshal で
		// any に展開するので数値は float64 で入ってくる。一方 default 経路
		// (DefaultPolicies()) は Go の int リテラル。両方を受けないと role
		// 上書きが silent ignore される (#524 review)。
		switch v := policies["avatarDecorationLimit"].(type) {
		case int:
			limit = v
		case float64:
			limit = int(v)
		}
	}
	if len(in) > limit {
		return nil, &avatarDecorationAPIError{
			status: http.StatusBadRequest,
			body: apierr.Error(
				"TOO_MANY_AVATAR_DECORATIONS",
				"You cannot apply more avatar decorations than the limit allows.",
				"b449d1cf-d840-4a0a-8e25-f0b0c1782132",
			),
		}
	}
	// 許可ロール検証用の lookup set。provider 未配線時は空 set でフォールスルーし、
	// roleIds 制限のあるデコレーションは catalog 側で 1 件目で弾かれる。
	allowedRoleIDs := map[string]struct{}{}
	if h.roleProvider != nil {
		if roles, err := h.roleProvider.GetUserRoles(userID); err == nil {
			for _, r := range roles {
				allowedRoleIDs[r.ID] = struct{}{}
			}
		}
	}
	out := make([]map[string]any, 0, len(in))
	for _, d := range in {
		if d.ID == "" {
			return nil, &avatarDecorationAPIError{
				status: http.StatusBadRequest,
				body:   apierr.InvalidParam(),
			}
		}
		if h.avatarDecorationRepo != nil {
			deco, err := h.avatarDecorationRepo.FindByID(d.ID)
			if err != nil || deco == nil {
				return nil, &avatarDecorationAPIError{
					status: http.StatusBadRequest,
					body: apierr.Error(
						"NO_SUCH_AVATAR_DECORATION",
						"No such avatar decoration.",
						"c0fa7a0d-a32a-4cb1-b657-ae8a90eaa3a8",
					),
				}
			}
			if len(deco.RoleIDs) > 0 {
				ok := false
				for _, rid := range deco.RoleIDs {
					if _, has := allowedRoleIDs[rid]; has {
						ok = true
						break
					}
				}
				if !ok {
					return nil, &avatarDecorationAPIError{
						status: http.StatusBadRequest,
						body: apierr.Error(
							"RESTRICTED_BY_ROLE",
							"This feature is restricted by your role.",
							"8feff0ba-5ab5-585b-31f4-4df816663fad",
						),
					}
				}
			}
		}
		row := map[string]any{
			"id":      d.ID,
			"angle":   0.0,
			"flipH":   false,
			"offsetX": 0.0,
			"offsetY": 0.0,
		}
		if d.Angle != nil {
			row["angle"] = *d.Angle
		}
		if d.FlipH != nil {
			row["flipH"] = *d.FlipH
		}
		if d.OffsetX != nil {
			row["offsetX"] = *d.OffsetX
		}
		if d.OffsetY != nil {
			row["offsetY"] = *d.OffsetY
		}
		out = append(out, row)
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return nil, &avatarDecorationAPIError{
			status: http.StatusInternalServerError,
			body:   apierr.InternalError(),
		}
	}
	return raw, nil
}

// PinRequest is the request body for i/pin and i/unpin.
type PinRequest struct {
	NoteID string `json:"noteId"`
}

// Pin handles POST /api/i/pin.
func (h *Handler) Pin(c echo.Context) error {
	me := middleware.GetUser(c)

	var req PinRequest
	if err := c.Bind(&req); err != nil || req.NoteID == "" {
		return apierr.JSONInvalidParam(c)
	}

	if err := h.userService.PinNote(me.ID, req.NoteID); err != nil {
		switch {
		case errors.Is(err, user.ErrNoteNotFound):
			return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_NOTE", "No such note.", "56734f8b-3928-431e-bf80-6ff87df40cb3"))
		case errors.Is(err, user.ErrAlreadyPinned):
			return c.JSON(http.StatusBadRequest, apierr.Error("ALREADY_PINNED", "That note has already been pinned.", "8b18c2b7-68fe-4edb-9892-c0cbaeb6c913"))
		case errors.Is(err, user.ErrPinLimitExceeded):
			return c.JSON(http.StatusBadRequest, apierr.Error("PIN_LIMIT_EXCEEDED", "You can not pin notes any more.", "72dab508-c64d-498f-8740-a8eec1ba385a"))
		default:
			return apierr.JSONInternalError(c)
		}
	}

	// featured collection への pin を followers/relay へ連合配信 (#2024)。
	if h.pinDeliveryHook != nil {
		h.pinDeliveryHook.OnLocalPinChanged(me.ID, req.NoteID, true)
	}

	return h.Me(c)
}

// Unpin handles POST /api/i/unpin.
func (h *Handler) Unpin(c echo.Context) error {
	me := middleware.GetUser(c)

	var req PinRequest
	if err := c.Bind(&req); err != nil || req.NoteID == "" {
		return apierr.JSONInvalidParam(c)
	}

	if err := h.userService.UnpinNote(me.ID, req.NoteID); err != nil {
		if errors.Is(err, user.ErrPinNotFound) {
			return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_NOTE", "No such note.", "454170ce-9d63-4a43-9da1-ea10afe81e21"))
		}
		return apierr.JSONInternalError(c)
	}

	// featured collection からの unpin を followers/relay へ連合配信 (#2024)。
	if h.pinDeliveryHook != nil {
		h.pinDeliveryHook.OnLocalPinChanged(me.ID, req.NoteID, false)
	}

	return h.Me(c)
}

// fillUnreadFields writes the unread-related flags/counts onto resp.
// 依存が未wireのフィールドはdefault (false/0/[]) にフォールバックする。
func (h *Handler) fillUnreadFields(ctx context.Context, u *model.User, resp map[string]any) {
	// Defaults
	resp["hasUnreadNotification"] = false
	resp["unreadNotificationsCount"] = 0
	resp["hasUnreadMentions"] = false
	resp["hasPendingReceivedFollowRequest"] = false
	resp["hasUnreadAnnouncement"] = false
	resp["unreadAnnouncements"] = []any{}
	resp["hasUnreadChatMessages"] = false
	resp["hasUnreadAntenna"] = false
	resp["hasUnreadChannel"] = false
	resp["hasUnreadSpecifiedNotes"] = false

	// Notification (Redis Streams): 1 回の UnreadSummary で 3 値を集約する
	// (#321)。従来は UnreadCount / HasUnreadOfTypes / HasUnreadSpecifiedNotes
	// の 3 XRevRange scan に分かれていたが、hot path 最適化のため統合した。
	// err 時も summary に格納済みの部分結果は採用する (Redis 成功 + DB 失敗
	// で mentions 判定までは保持する partial resilience)。
	if h.notificationSvc != nil {
		summary, err := h.notificationSvc.UnreadSummary(ctx, u.ID, []notification.Type{
			notification.TypeMention, notification.TypeReply,
		})
		if err != nil {
			slog.Warn("UnreadSummary partial failure", "userID", u.ID, "err", err)
		}
		resp["unreadNotificationsCount"] = summary.TotalCount
		resp["hasUnreadNotification"] = summary.TotalCount > 0
		resp["hasUnreadMentions"] = summary.HasMentions
		resp["hasUnreadSpecifiedNotes"] = summary.HasSpecifiedNote
	}

	// FollowRequest (DB)
	if h.followRequestRepo != nil {
		if n, err := h.followRequestRepo.CountReceived(u.ID); err == nil {
			resp["hasPendingReceivedFollowRequest"] = n > 0
		}
	}

	// Announcement (DB)
	if h.announcementRepo != nil {
		if ann, err := h.announcementRepo.UnreadForUser(u.ID); err == nil {
			resp["hasUnreadAnnouncement"] = len(ann) > 0
			out := make([]map[string]any, 0, len(ann))
			for _, a := range ann {
				// PackAnnouncement で createdAt / updatedAt / icon / display 等を
				// 含む upstream 互換の shape に揃える。独自 map だと createdAt が
				// 欠落し、misskey_dart の AnnouncementsResponse.fromJson が
				// createdAt を非null String として cast して落ちる (#1224)。
				// #2106 L52: UnreadForUser は viewer u 宛て/global の announcement。viewerID=u.ID。
				packed := entity.PackAnnouncement(a, h.idGen, false, u.ID)
				if packed == nil {
					// nil entry を配列に入れると [null] になり、misskey_dart が
					// 各要素を fromJson する際 null で落ちるため除外する。
					continue
				}
				out = append(out, packed)
			}
			resp["unreadAnnouncements"] = out
		}
	}

	// Chat (Redis set, wrapped in ChatRepository)
	if h.chatRepo != nil {
		if n, err := h.chatRepo.CountUnread(u.ID); err == nil {
			resp["hasUnreadChatMessages"] = n > 0
		}
	}

	// Antenna / Channel (DB)
	if h.antennaUnreadRepo != nil {
		if ok, err := h.antennaUnreadRepo.HasAnyByUser(u.ID); err == nil {
			resp["hasUnreadAntenna"] = ok
		}
	}
	if h.channelUnreadRepo != nil {
		if ok, err := h.channelUnreadRepo.HasAnyByUser(u.ID); err == nil {
			resp["hasUnreadChannel"] = ok
		}
	}
}

// fillPinnedFields populates pinnedNoteIds / pinnedNotes / pinnedPageId /
// pinnedPage onto resp using the wired repos. Missing repos fall back to
// default empty / nil (tests that skip wiring keep passing).
func (h *Handler) fillPinnedFields(ctx context.Context, u *model.User, profile *model.UserProfile, resp map[string]any) {
	// Defaults
	resp["pinnedNoteIds"] = []string{}
	resp["pinnedNotes"] = []any{}
	resp["pinnedPageId"] = nil
	resp["pinnedPage"] = nil

	// Pinned notes: user_note_pining の行から noteId 一覧を取り、note 本体を
	// NoteRepository で fetch して pack する。
	if h.piningRepo != nil {
		pinings, err := h.piningRepo.ListByUser(u.ID)
		if err == nil && len(pinings) > 0 {
			ids := make([]string, 0, len(pinings))
			for _, p := range pinings {
				ids = append(ids, p.NoteID)
			}
			resp["pinnedNoteIds"] = ids

			if h.noteRepo != nil {
				if notes, err := h.noteRepo.FindManyByIDsWithUser(ids); err == nil {
					entities := entity.PackNotes(ctx, notes, h.idGen, h.instanceLookup(), h.emojiLookup(), h.reactionReader())
					// /api/i は認証された自身を返す path なので u 自身を viewer
					// として渡し、pinned note の myReaction も含めて埋める (#426)。
					h.fieldRes.Apply(entities, u)
					notehide.HideEmbeds(u, entities)
					packed := make([]any, 0, len(entities))
					for _, pn := range entities {
						packed = append(packed, pn)
					}
					resp["pinnedNotes"] = packed
				}
			}
		}
	}

	// Pinned page: user_profile.pinnedPageId からの展開。
	if profile != nil && profile.PinnedPageID != nil && *profile.PinnedPageID != "" {
		resp["pinnedPageId"] = profile.PinnedPageID
		if h.pageRepo != nil {
			if p, err := h.pageRepo.FindByID(*profile.PinnedPageID); err == nil {
				// golden Page は user 必須。pinnedPage は自分の page なので
				// owner=u を渡して user (UserLite) を埋める (#1266 follow-up)。
				resp["pinnedPage"] = entity.PackPageWithContext(p, entity.PackPageContext{IDGen: h.idGen, Owner: u})
			}
		}
	}
}
