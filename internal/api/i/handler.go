package i

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/api/apierr"
	"github.com/shiroha-a/mk/internal/core/notification"
	"github.com/shiroha-a/mk/internal/core/role"
	"github.com/shiroha-a/mk/internal/core/twofactor"
	"github.com/shiroha-a/mk/internal/core/user"
	"github.com/shiroha-a/mk/internal/entity"
	"github.com/shiroha-a/mk/internal/misc/id"
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
	userService          *user.Service
	idGen                id.Generator
	roleProvider         RoleProvider
	registryRepo         repository.RegistryRepository
	favoriteRepo         repository.NoteFavoriteRepository
	transferEnqueuer     TransferEnqueuer
	webauthnSvc          *twofactor.WebAuthnService
	securityKeyRepo      repository.UserSecurityKeyRepository
	metaRepo             repository.MetaRepository
	emailSender          EmailSender
	serverURL            string
	signinRepo           repository.SigninRepository
	accessTokenRepo      repository.AccessTokenRepository
	galleryRepo          GalleryRepository
	pageLikeRepo         repository.PageLikeRepository
	mover                AccountMover
	notificationSvc      UnreadNotificationSource
	followRequestRepo    repository.FollowRequestRepository
	announcementRepo     AnnouncementUnreadSource
	chatRepo             ChatUnreadSource
	antennaUnreadRepo    AntennaUnreadSource
	channelUnreadRepo    ChannelUnreadSource
	piningRepo           repository.UserNotePiningRepository
	noteRepo             repository.NoteRepository
	pageRepo             repository.PageRepository
	instanceRepo         repository.InstanceRepository
	emojiRepo            repository.EmojiRepository
	bufReader            entity.BufferedReactionsReader
	avatarDecorationRepo repository.AvatarDecorationRepository
	mainStreamPublisher  MainStreamPublisher
	fieldRes             *entity.NoteFieldResolver
	// emailValidationClient は verifymail / truemail SaaS への outbound に
	// 使う SSRF-safe HTTP client (#638)。nil ならデフォルトクライアント。
	emailValidationClient *http.Client
}

// SetEmailValidationClient wires the outbound HTTP client used by
// verifymail / truemail siteverify calls (#638). production では SSRF-safe
// + forward proxy 経由の client を渡すこと。
func (h *Handler) SetEmailValidationClient(c *http.Client) {
	h.emailValidationClient = c
}

// SetNoteFieldResolver wires the shared resolver that fills Files /
// MyReaction / Channel on packed pinned notes (#426)。
func (h *Handler) SetNoteFieldResolver(r *entity.NoteFieldResolver) {
	h.fieldRes = r
}

// MainStreamPublisher emits events to a single user's `main` WebSocket
// channel. Used here to publish `myTokenRegenerated` so other sessions of
// the same user learn that their API token was invalidated.
// 循環依存を避けるためinterfaceで受け取る(実装はinternal/stream)。
type MainStreamPublisher interface {
	PublishMainEvent(userID, eventType string, body any)
}

// SetMainStreamPublisher attaches a publisher used to emit events on
// /api/i/* endpoints (currently `myTokenRegenerated`). Optional — nil
// disables emit.
func (h *Handler) SetMainStreamPublisher(p MainStreamPublisher) {
	h.mainStreamPublisher = p
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
	item, err := h.registryRepo.Get(u.ID, req.Key, req.Scope, req.Domain)
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
	item := &model.RegistryItem{
		ID:        h.idGen.Generate(time.Now()),
		UpdatedAt: time.Now(),
		UserID:    u.ID,
		Key:       req.Key,
		Value:     []byte(req.Value),
		Scope:     req.Scope,
		Domain:    req.Domain,
	}
	if err := h.registryRepo.Set(item); err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	// TS本家 RegistryApiService.set (lines 59-66): domain == null のとき
	// のみmainにpublishする。domain指定はサードパーティアプリ固有の領域
	// なので他クライアントには broadcast しない仕様。
	if h.mainStreamPublisher != nil && req.Domain == nil {
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
	items, err := h.registryRepo.GetAll(u.ID, req.Scope, req.Domain)
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
	keys, err := h.registryRepo.KeysWithType(u.ID, req.Scope, req.Domain)
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
	if err := h.registryRepo.Remove(u.ID, req.Key, req.Scope, req.Domain); err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	return c.NoContent(http.StatusNoContent)
}

// Me handles POST /api/i - returns the authenticated user's info.
func (h *Handler) Me(c echo.Context) error {
	u := middleware.GetUser(c)

	profile := h.userService.GetProfile(u.ID)

	detailed := entity.PackUserDetailed(u, profile, h.idGen)

	// /api/i returns additional private fields
	resp := map[string]any{
		// UserLite fields
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
		// UserDetailed fields
		"bannerUrl":      detailed.BannerURL,
		"bannerBlurhash": detailed.BannerBlurhash,
		"isLocked":       detailed.IsLocked,
		"isSilenced":     h.isSilenced(u.ID),
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
		// MeDetailed fields
		"avatarId":            u.AvatarID,
		"bannerId":            u.BannerID,
		"followersVisibility": detailed.FollowersVisibility,
		"followingVisibility": detailed.FollowingVisibility,
		"chatScope":           u.ChatScope,
		"canChat":             true,
		"followedMessage":     nil,
		"memo":                nil,
		"moderationNote":      nil,
		"hideOnlineStatus":    u.HideOnlineStatus,
	}

	// Private fields from profile
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
		resp["followedMessage"] = profile.FollowedMessage
		resp["loggedInDays"] = len(profile.LoggedInDates)
		resp["achievements"] = jsonbArray(profile.Achievements)
		resp["securityKeys"] = profile.SecurityKeysAvailable
		// twoFactorBackupCodesStock: full/partial/none
		resp["twoFactorBackupCodesStock"] = backupCodesStock(profile)
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
	resp["isDeleted"] = u.IsDeleted
	resp["isExplorable"] = u.IsExplorable
	// 未読系フィールドを依存する repo / service から実際に引く。
	// 未wireのものは false/0/[] にフォールバックする (テスト互換)。
	// antenna / channel / specifiedNotes は別issueで追跡中のためここでは false 固定。
	h.fillUnreadFields(c.Request().Context(), u, resp)
	h.fillPinnedFields(c.Request().Context(), u, profile, resp)
	resp["policies"] = userPolicies
	resp["roles"] = userRoles
	// securityKeysList: WebAuthnキーの一覧
	if h.securityKeyRepo != nil {
		if keys, err := h.securityKeyRepo.ListByUser(u.ID); err == nil && len(keys) > 0 {
			list := make([]map[string]any, len(keys))
			for i, k := range keys {
				list[i] = map[string]any{
					"id":       k.ID,
					"name":     k.Name,
					"lastUsed": k.LastUsed.UTC().Format("2006-01-02T15:04:05.000Z"),
				}
			}
			resp["securityKeysList"] = list
		} else {
			resp["securityKeysList"] = []any{}
		}
	} else {
		resp["securityKeysList"] = []any{}
	}
	resp["mutingNotificationTypes"] = []any{}
	resp["notificationRecieveConfig"] = map[string]any{}
	resp["emailNotificationTypes"] = []string{"follow", "receiveFollowRequest"}

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
	// ChatScope は 1-on-1 DM の受信許可レベル (#692)。
	// upstream paramDef enum: everyone / followers / following / mutual / none
	ChatScope *string `json:"chatScope"`
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

// backupCodesStock returns "full", "partial", or "none" based on the number of
// remaining 2FA backup codes. Misskey uses 5 codes as the full set.
func backupCodesStock(profile *model.UserProfile) string {
	if !profile.TwoFactorEnabled || len(profile.TwoFactorBackupSecret) == 0 {
		return "none"
	}
	if len(profile.TwoFactorBackupSecret) >= 5 {
		return "full"
	}
	return "partial"
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

// Update handles POST /api/i/update.
func (h *Handler) Update(c echo.Context) error {
	me := middleware.GetUser(c)

	var req UpdateRequest
	if err := c.Bind(&req); err != nil {
		return apierr.JSONInvalidParam(c)
	}

	in := user.UpdateInput{
		IsLocked:          req.IsLocked,
		IsBot:             req.IsBot,
		IsCat:             req.IsCat,
		IsExplorable:      req.IsExplorable,
		HideOnlineStatus:  req.HideOnlineStatus,
		AlwaysMarkNsfw:    req.AlwaysMarkNsfw,
		AutoSensitive:     req.AutoSensitive,
		NoCrawle:          req.NoCrawle,
		PreventAiLearning: req.PreventAiLearning,
	}
	if req.Name != nil {
		// 表示名の禁止ワードチェック。meta 未注入 / 未設定時は素通りする。
		// 本家 Misskey と同様に部分一致 case-insensitive で、空文字 ("") の
		// クリアリクエストは検査対象外 (ユーザー体験上必要)。
		if h.metaRepo != nil && *req.Name != "" {
			if m, err := h.metaRepo.Fetch(); err == nil && containsProhibitedWord(*req.Name, m.ProhibitedWordsForNameOfUser) {
				return c.JSON(http.StatusBadRequest, apierr.Error("NAME_CONTAINS_PROHIBITED_WORDS", "Your new name contains prohibited words.", "0b3f9f6a-2e7d-4c2c-9d7a-8c6f9b2e1a44"))
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
	if req.PublicReactions != nil {
		in.PublicReactions = req.PublicReactions
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
	if mw, ok, err := normalizeMutedWords(req.MutedWords); err != nil {
		return apierr.JSONInvalidParam(c)
	} else if ok {
		in.MutedWords = &mw
	}
	if mw, ok, err := normalizeMutedWords(req.HardMutedWords); err != nil {
		return apierr.JSONInvalidParam(c)
	} else if ok {
		in.HardMutedWords = &mw
	}
	if req.AvatarID != nil {
		in.AvatarID = req.AvatarID
	}
	if req.BannerID != nil {
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

	bundle, err := h.userService.UpdateProfile(me.ID, in)
	if err != nil {
		switch {
		case errors.Is(err, user.ErrUserNotFound):
			return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_USER", "No such user.", "4362f8dc-731f-4ad8-a694-be5a88922a24"))
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

	return c.JSON(http.StatusOK, entity.PackUserDetailed(bundle.User, bundle.Profile, h.idGen))
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
			return apierr.JSONNoSuchNote(c)
		case errors.Is(err, user.ErrAlreadyPinned):
			return c.JSON(http.StatusBadRequest, apierr.Error("ALREADY_PINNED", "That note has already been pinned.", "8b18c2b7-68fe-4edb-9892-c0cbaeb6c913"))
		case errors.Is(err, user.ErrPinLimitExceeded):
			return c.JSON(http.StatusBadRequest, apierr.Error("PIN_LIMIT_EXCEEDED", "You can not pin notes any more.", "72dab508-c64d-498f-8740-a8eec1ba385a"))
		default:
			return apierr.JSONInternalError(c)
		}
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
			return apierr.JSONNoSuchNote(c)
		}
		return apierr.JSONInternalError(c)
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
				out = append(out, map[string]any{
					"id":       a.ID,
					"title":    a.Title,
					"text":     a.Text,
					"imageUrl": a.ImageURL,
				})
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
				resp["pinnedPage"] = entity.PackPage(p, h.idGen)
			}
		}
	}
}
