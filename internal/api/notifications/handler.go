// Package notifications provides /api/notifications/* endpoints.
package notifications

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/api/apierr"
	"github.com/shiroha-a/mk/internal/api/notehide"
	"github.com/shiroha-a/mk/internal/api/pagination"
	corenote "github.com/shiroha-a/mk/internal/core/note"
	"github.com/shiroha-a/mk/internal/core/notification"
	"github.com/shiroha-a/mk/internal/entity"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
	"github.com/shiroha-a/mk/internal/server/middleware"
)

// Handler handles notifications-related API endpoints.
type Handler struct {
	svc           *notification.Service
	idGen         id.Generator
	userRepo      repository.UserRepository
	noteRepo      repository.NoteRepository
	queryService  *corenote.QueryService
	followReqRepo repository.FollowRequestRepository
	// mutingRepo は read 時の valid-notifier filter で viewer の muting set
	// (ListMuteeIDs) を引くための optional 依存 (#1775)。未配線なら muting filter
	// は素通り (suspended / mutedInstances filter は引き続き効く)。
	mutingRepo           repository.MutingRepository
	instanceRepo         repository.InstanceRepository
	emojiRepo            repository.EmojiRepository
	testNotifier         TestNotifier
	roleLookup           entity.RoleLookup
	chatInvitationLookup entity.ChatInvitationLookup
	// accessTokenRepo は notifications/create の 'app' 通知で、リクエストに使われた
	// access token を raw token から再解決し、header/icon の fallback (token.name /
	// token.iconUrl) + appAccessTokenId を埋めるために使う (#1557)。未配線なら
	// fallback 無し。
	accessTokenRepo repository.AccessTokenRepository
}

// SetAccessTokenRepo wires the access token repo used by notifications/create
// to resolve the request's app token for header/icon fallback (#1557)。
func (h *Handler) SetAccessTokenRepo(r repository.AccessTokenRepository) {
	h.accessTokenRepo = r
}

// SetRoleLookup wires the lookup used to pack roleAssigned notifications'
// embedded role (#1559)。
func (h *Handler) SetRoleLookup(fn entity.RoleLookup) { h.roleLookup = fn }

// SetChatInvitationLookup wires the lookup used to pack
// chatRoomInvitationReceived notifications' embedded invitation (#1559)。
func (h *Handler) SetChatInvitationLookup(fn entity.ChatInvitationLookup) {
	h.chatInvitationLookup = fn
}

// notificationOptions returns the per-call packing options (role / chat
// invitation lookups, viewer) for the given viewer (= notifiee)。
func (h *Handler) notificationOptions(viewerID string) []entity.NotificationOption {
	return []entity.NotificationOption{
		entity.WithRoleLookup(h.roleLookup),
		entity.WithChatInvitationLookup(h.chatInvitationLookup),
		entity.WithViewer(viewerID),
	}
}

// NewHandler creates a new notifications Handler.
func NewHandler(svc *notification.Service, idGen id.Generator) *Handler {
	return &Handler{svc: svc, idGen: idGen}
}

// TestNotifier creates a 'test' notification on the caller's own stream so the
// test-notification endpoint can verify web push / streaming delivery (#1559)。
// 循環依存を避けるため interface で受け取る (実装は core/notification.Hook)。
type TestNotifier interface {
	OnTest(userID string)
}

// SetTestNotifier attaches a TestNotifier used by TestNotification so the test
// notification also fires web push (upstream notifications/test-notification)。
func (h *Handler) SetTestNotifier(n TestNotifier) { h.testNotifier = n }

// SetRepos attaches repositories for resolving user/note objects in notifications.
func (h *Handler) SetRepos(userRepo repository.UserRepository, noteRepo repository.NoteRepository) {
	h.userRepo = userRepo
	h.noteRepo = noteRepo
}

// SetInstanceRepo attaches an InstanceRepository so user.instance (used by
// frontend InstanceTicker for theme color / software name) is populated on
// notification payloads (#415 follow-up).
func (h *Handler) SetInstanceRepo(r repository.InstanceRepository) {
	h.instanceRepo = r
}

// SetEmojiRepo attaches an EmojiRepository so custom emoji shortcodes in
// user.name / note.text resolve to URLs in notification payloads.
func (h *Handler) SetEmojiRepo(r repository.EmojiRepository) {
	h.emojiRepo = r
}

// instanceLookup returns an InstanceLookup adapter (nil-safe when the
// repository hasn't been wired).
func (h *Handler) instanceLookup() entity.InstanceLookup {
	if h.instanceRepo == nil {
		return nil
	}
	return h.instanceRepo
}

// emojiLookup returns an EmojiLookup adapter (nil-safe when the repository
// hasn't been wired).
func (h *Handler) emojiLookup() entity.EmojiLookup {
	if h.emojiRepo == nil {
		return nil
	}
	return h.emojiRepo
}

// SetQueryService attaches a note QueryService used to gate embedded notes by
// viewer visibility. #1444 で「i/notifications の note embed が CanSeeNote
// check 無しで followers / specified note を漏らす」IDOR を塞ぐ。未配線の
// 場合は fail-closed として note embed 自体を丸ごと skip する (production の
// router.go は必ず wire する)。
func (h *Handler) SetQueryService(qs *corenote.QueryService) {
	h.queryService = qs
}

// SetFollowRequestRepo attaches a FollowRequestRepository. 受信済み
// receiveFollowRequest 通知について対応する follow_request 行が無ければ
// (承認/拒否により消えた) 通知一覧から除外する用途 (本家
// NotificationEntityService と同じ挙動)。
func (h *Handler) SetFollowRequestRepo(r repository.FollowRequestRepository) {
	h.followReqRepo = r
}

// ListRequest is the request body for notifications.
type ListRequest struct {
	Limit        int      `json:"limit"`
	IncludeTypes []string `json:"includeTypes"`
	ExcludeTypes []string `json:"excludeTypes"`
	SinceID      string   `json:"sinceId"`
	UntilID      string   `json:"untilId"`
	SinceDate    *int64   `json:"sinceDate"`
	UntilDate    *int64   `json:"untilDate"`
	// MarkAsRead controls whether fetching the notification list implicitly
	// marks all notifications as read. nil / true means "mark as read"
	// (default), explicit false leaves the read state untouched.
	// 本家 i/notifications の暗黙引数。Misskey フロントが
	// `notifications/mark-all-as-read` を明示呼び出しせず、通知一覧 fetch の
	// 副作用で badge が消えることを期待しているため (#420)。
	MarkAsRead *bool `json:"markAsRead"`
}

// Show handles POST /api/i/notifications - returns the authenticated user's
// notification timeline ordered newest first.
func (h *Handler) Show(c echo.Context) error {
	user := middleware.GetUser(c)
	req, ok := h.bindListRequest(c)
	if !ok {
		return apierr.JSONInvalidParam(c)
	}

	filtered, notifierByID, noteByID, err := h.collectNotifications(c, user, req)
	if err != nil {
		return apierr.JSONInternalError(c)
	}

	items := make([]entity.NotificationItem, 0, len(filtered))
	for _, n := range filtered {
		items = append(items, entity.NotificationItem{
			N:    n,
			User: notifierByID[n.NotifierID],
			Note: noteByID[n.NoteID],
		})
	}
	out := entity.PackNotifications(items, h.idGen, h.instanceLookup(), h.emojiLookup(), h.notificationOptions(user.ID)...)
	// depth-2 embed hide (#1570): collectNotifications の #1444 CanSeeNote gate は
	// 見えない note を丸ごと落とすが embed (renote/reply) には再帰しない。通知 note の
	// embed と著者設定ゲートを viewer 可視性で適用する。これを欠くと #1570 で塞いだ
	// i/notifications の depth-2 embed IDOR が再オープンする。
	notehide.HideNotificationNotes(user, out)
	h.maybeMarkAsRead(c, user, req)
	return c.JSON(http.StatusOK, out)
}

// bindListRequest binds the shared i/notifications(-grouped) request body and
// normalizes the cursor / limit. Returns ok=false on a bind error so callers
// can emit INVALID_PARAM.
func (h *Handler) bindListRequest(c echo.Context) (ListRequest, bool) {
	var req ListRequest
	if err := c.Bind(&req); err != nil {
		return req, false
	}
	// sinceDate / untilDate を aidx prefix に正規化 (#1174)。Notification.ID
	// は notification_service.go で `idGen.Generate(now)` で aidx 形式で発番
	// されるため、aidx 比較 (= 後段の post-fetch filter `n.ID <= sinceID` /
	// `n.ID >= untilID`) で sinceDate / untilDate も正しく効く。Redis Stream
	// native ID と aidx ID は別物だが、本 endpoint の cursor は notification.ID
	// (= aidx) で判定する設計なので adapter pattern で完結する。
	req.SinceID, req.UntilID = id.NormalizeCursor(req.SinceID, req.UntilID, req.SinceDate, req.UntilDate)
	req.Limit = pagination.ClampLimit(req.Limit, 10, 100)
	return req, true
}

// collectNotifications runs the shared fetch + filter + batched user/note
// resolution path used by both Show and Grouped. It returns the post-filter
// notification slice (newest first) plus the resolved notifier-user and
// viewer-visible note maps. The note map is gated by CanSeeNote (#1444 IDOR):
// followers / specified notes the viewer cannot see are dropped so noteId
// stays but the embedded detail does not leak.
func (h *Handler) collectNotifications(c echo.Context, user *model.User, req ListRequest) ([]*notification.Notification, map[string]*model.User, map[string]*model.Note, error) {
	// upstream notifications.ts: 明示的な includeTypes:[] は空配列を返す
	// (= 何も含めない)。omit (nil) は全 type を通す。Go の []string は
	// absent→nil / []→non-nil(len 0) で区別できるので、ここで早期 return する
	// (#1546)。excludeTypes 全指定の空返却は下の per-row exclude filter で既に
	// 成立するため特別扱い不要。
	if req.IncludeTypes != nil && len(req.IncludeTypes) == 0 {
		return nil, map[string]*model.User{}, map[string]*model.Note{}, nil
	}
	rows, err := h.svc.List(c.Request().Context(), user.ID, req.Limit)
	if err != nil {
		return nil, nil, nil, err
	}

	// includeTypes / excludeTypes フィルタ
	includeSet := make(map[string]bool, len(req.IncludeTypes))
	for _, t := range req.IncludeTypes {
		includeSet[t] = true
	}
	excludeSet := make(map[string]bool, len(req.ExcludeTypes))
	for _, t := range req.ExcludeTypes {
		excludeSet[t] = true
	}

	// 2-pass で組み立てる:
	//   pass 1: cursor / type / followReq filter を通して残った rows と、
	//           その notifierID / noteID を集約
	//   pass 2: userRepo.FindManyByIDs / noteRepo.FindManyByIDsWithUser で
	//           まとめて引き、map で O(1) 解決して PackNotifications に渡す
	// (旧実装は 1 通知ごとに FindByID + FindByIDWithRelations を呼んで最大
	// 600 round-trip の N+1 を出していた。#515)。
	filtered := make([]*notification.Notification, 0, len(rows))
	notifierIDSet := make(map[string]struct{})
	for _, n := range rows {
		// カーソルベースページネーション
		if req.SinceID != "" && n.ID <= req.SinceID {
			continue
		}
		if req.UntilID != "" && n.ID >= req.UntilID {
			continue
		}
		// タイプフィルタ
		if len(includeSet) > 0 && !includeSet[string(n.Type)] {
			continue
		}
		if excludeSet[string(n.Type)] {
			continue
		}
		// 既に解決された follow request (accept / reject で消えた) に対応する
		// receiveFollowRequest 通知はユーザー視点で既に古く「残り続ける」ので
		// 除外する (#349 コメント対応、本家 NotificationEntityService 互換)。
		if n.Type == notification.TypeReceiveFollowReq && h.followReqRepo != nil && n.NotifierID != "" {
			if exists, err := h.followReqRepo.Exists(n.NotifierID, user.ID); err == nil && !exists {
				continue
			}
		}
		filtered = append(filtered, n)
		if n.NotifierID != "" {
			notifierIDSet[n.NotifierID] = struct{}{}
		}
	}

	notifierByID := make(map[string]*model.User, len(notifierIDSet))
	if h.userRepo != nil && len(notifierIDSet) > 0 {
		ids := make([]string, 0, len(notifierIDSet))
		for id := range notifierIDSet {
			ids = append(ids, id)
		}
		if users, err := h.userRepo.FindManyByIDs(ids); err == nil {
			for _, u := range users {
				notifierByID[u.ID] = u
			}
		}
	}

	// #1775: read-time valid-notifier filter。create 時 mute だけでなく read 時に
	// も notifier が (a) いま viewer に mute されている / (b) viewer の mutedInstances
	// host / (c) suspended のいずれかなら通知を落とす (upstream
	// NotificationEntityService #filterValidNotifier)。noteIDSet は survivor から
	// 組み立てる (落とした通知の note を無駄に引かない)。
	filtered = h.filterValidNotifiers(user.ID, filtered, notifierByID)

	noteIDSet := make(map[string]struct{})
	for _, n := range filtered {
		if n.NoteID != "" {
			noteIDSet[n.NoteID] = struct{}{}
		}
	}
	// noteByID 構築には CanSeeNote ゲートを挟む。followers / specified note は
	// 通知 recipient が author の follower でない / visibleUserIds に含まれない
	// 場合に embed を nil 化する。これが無いと B の followers note への reply
	// 通知経由で「A が B を follow していなくても」B の full note + author が
	// 漏れる (#1444 IDOR)。通知行自体は残し、note field だけ落とすことで本家
	// NotificationEntityService と同じ shape (noteId は残るが detail は無い) を
	// 維持する。queryService 未配線時は fail-closed で note embed を完全 skip
	// する (= production router は必ず wire、test の partial setup を漏れに
	// 繋げない)。
	noteByID := make(map[string]*model.Note, len(noteIDSet))
	if h.noteRepo != nil && h.queryService != nil && len(noteIDSet) > 0 {
		ids := make([]string, 0, len(noteIDSet))
		for id := range noteIDSet {
			ids = append(ids, id)
		}
		if notes, err := h.noteRepo.FindManyByIDsWithUser(ids); err == nil {
			visible := h.queryService.FilterVisible(user, notes)
			for _, nn := range visible {
				noteByID[nn.ID] = nn
			}
		}
	}
	return filtered, notifierByID, noteByID, nil
}

// SetMutingRepo wires the muting repository used by the read-time
// valid-notifier filter to load the viewer's muting set (#1775)。
func (h *Handler) SetMutingRepo(r repository.MutingRepository) {
	h.mutingRepo = r
}

// filterValidNotifiers drops notifications whose notifier is no longer a valid
// source for the viewer at READ time, mirroring upstream
// NotificationEntityService #validateNotifier (#1775):
//   - notifierId in the viewer's muting set
//   - notifier.host in the viewer's mutedInstances
//   - notifier.isSuspended
//
// Notifications without a notifierId (system notifications) always pass.
// Unlike upstream, a notification whose notifier could not be resolved is kept
// (not dropped): mk-go resolves notifiers best-effort and a transient
// FindManyByIDs failure must not mass-drop the whole list; the packer renders a
// missing notifier gracefully.
func (h *Handler) filterValidNotifiers(viewerID string, rows []*notification.Notification, notifierByID map[string]*model.User) []*notification.Notification {
	muted := map[string]bool{}
	if h.mutingRepo != nil {
		if ids, err := h.mutingRepo.ListMuteeIDs(viewerID); err == nil {
			for _, id := range ids {
				muted[id] = true
			}
		}
	}
	mutedInstances := map[string]bool{}
	if h.userRepo != nil {
		if profile, err := h.userRepo.FindProfileByUserID(viewerID); err == nil && profile != nil && len(profile.MutedInstances) > 0 {
			var hosts []string
			if json.Unmarshal(profile.MutedInstances, &hosts) == nil {
				for _, host := range hosts {
					mutedInstances[host] = true
				}
			}
		}
	}
	if len(muted) == 0 && len(mutedInstances) == 0 && !h.anyNotifierSuspended(rows, notifierByID) {
		return rows
	}
	out := make([]*notification.Notification, 0, len(rows))
	for _, n := range rows {
		if n.NotifierID != "" {
			if muted[n.NotifierID] {
				continue
			}
			if notifier, ok := notifierByID[n.NotifierID]; ok {
				if notifier.IsSuspended {
					continue
				}
				if notifier.Host != nil && mutedInstances[*notifier.Host] {
					continue
				}
			}
		}
		out = append(out, n)
	}
	return out
}

// anyNotifierSuspended reports whether at least one resolved notifier is
// suspended, so filterValidNotifiers can skip the rebuild loop entirely when
// there is nothing to drop (no mutes, no muted instances, no suspended).
func (h *Handler) anyNotifierSuspended(rows []*notification.Notification, notifierByID map[string]*model.User) bool {
	for _, n := range rows {
		if n.NotifierID == "" {
			continue
		}
		if u, ok := notifierByID[n.NotifierID]; ok && u.IsSuspended {
			return true
		}
	}
	return false
}

// maybeMarkAsRead honours the implicit markAsRead side effect: 本家
// i/notifications と互換で markAsRead 未指定または true なら通知一覧取得の
// 副作用で全通知を既読化し、main stream に readAllNotifications を publish
// する。これが無いとフロントエンドが /my/notifications を開いてもバッジ
// カウントが残り続ける (#420)。
func (h *Handler) maybeMarkAsRead(c echo.Context, user *model.User, req ListRequest) {
	if req.MarkAsRead != nil && !*req.MarkAsRead {
		return
	}
	if err := h.svc.MarkAllAsRead(c.Request().Context(), user.ID); err != nil {
		// 既読化失敗は通知一覧の取得結果には影響しないので 200 のまま
		// 返し、ログだけ残す。
		slog.Warn("notifications: implicit mark-all-as-read failed",
			"userId", user.ID, "err", err)
	}
}

// MarkAllAsRead handles POST /api/notifications/mark-all-as-read.
func (h *Handler) MarkAllAsRead(c echo.Context) error {
	user := middleware.GetUser(c)
	if err := h.svc.MarkAllAsRead(c.Request().Context(), user.ID); err != nil {
		return apierr.JSONInternalError(c)
	}
	return c.NoContent(http.StatusNoContent)
}

// Create handles POST /api/notifications/create.
//
// upstream Misskey TS notifications/create と同 semantics: 認証中の app
// (access token) が自分自身に type 'app' の任意通知を作る。body は必須、
// header / icon は任意。body / header / icon は Extra に格納し、entity の
// Extra spread で notification 出力に surface される (#1217)。
//
// header / icon は upstream 同様 `ps.header ?? token?.name ?? null` /
// `ps.icon ?? token?.iconUrl ?? null` で、リクエスト値が無ければ access token の
// name / iconUrl にフォールバックする (#1557)。token は raw token から
// 再解決する (native session token は access_token 行が無いので nil = fallback
// 無し)。'app' 通知の header/icon は schema 上 required + nullable なので、
// 値が無くても null で常に present にする。appAccessTokenId も記録する
// (内部メタデータ、レスポンスには出さない)。
func (h *Handler) Create(c echo.Context) error {
	user := middleware.GetUser(c)
	var req struct {
		Body   *string `json:"body"`
		Header *string `json:"header"`
		Icon   *string `json:"icon"`
	}
	// upstream paramDef は body を required にするのみ (ajv の required は
	// presence チェックなので空文字 "" も受理される)。よって不在 (nil) のみを
	// INVALID_PARAM とし、空文字は upstream 同様に通す。
	if err := c.Bind(&req); err != nil || req.Body == nil {
		return apierr.JSONInvalidParam(c)
	}
	if h.svc == nil || user == nil {
		return c.NoContent(http.StatusNoContent)
	}

	// リクエストの access token を再解決する (header/icon fallback + appAccessTokenId)。
	token := h.resolveRequestToken(c)

	// header = ps.header ?? token?.name ?? null。icon も同様。nil のままにすると
	// Extra spread で `null` として出力され、required + nullable な schema に揃う。
	var header any
	if req.Header != nil {
		header = *req.Header
	} else if token != nil && token.Name != nil {
		header = *token.Name
	}
	var icon any
	if req.Icon != nil {
		icon = *req.Icon
	} else if token != nil && token.IconURL != nil {
		icon = *token.IconURL
	}
	extra := map[string]any{"body": *req.Body, "header": header, "icon": icon}

	appAccessTokenID := ""
	if token != nil {
		appAccessTokenID = token.ID
	}

	// app 通知は notifier を持たない。NotifierID を空にすることで
	// service.Create の self-notification ガード (NotifierID == NotifieeID) も
	// 回避する。upstream 同様 fire-and-forget なので結果は 204 固定。
	if _, err := h.svc.Create(c.Request().Context(), notification.CreateInput{
		NotifieeID:       user.ID,
		Type:             notification.TypeApp,
		Extra:            extra,
		AppAccessTokenID: appAccessTokenID,
	}); err != nil {
		slog.Warn("notifications/create failed", "user", user.ID, "err", err)
	}
	return c.NoContent(http.StatusNoContent)
}

// resolveRequestToken re-resolves the request's raw token to its AccessToken
// row (#1557)。native session login token は access_token 行を持たないので nil
// (= header/icon fallback 無し)。repo 未配線 / token 無し / 解決失敗も nil。
func (h *Handler) resolveRequestToken(c echo.Context) *model.AccessToken {
	if h.accessTokenRepo == nil {
		return nil
	}
	raw := middleware.GetToken(c)
	if raw == "" {
		return nil
	}
	sum := sha256.Sum256([]byte(raw))
	at, err := h.accessTokenRepo.FindByHashOrToken(hex.EncodeToString(sum[:]), raw)
	if err != nil {
		return nil
	}
	return at
}

// Flush handles POST /api/notifications/flush.
func (h *Handler) Flush(c echo.Context) error {
	user := middleware.GetUser(c)
	if h.svc != nil {
		if err := h.svc.Flush(c.Request().Context(), user.ID); err != nil {
			return apierr.JSONInternalError(c)
		}
	}
	return c.NoContent(http.StatusNoContent)
}

// TestNotification handles POST /api/notifications/test-notification.
// 本人へ 'test' 通知を送り、web push / streaming の疎通を確認できるようにする
// (upstream notifications/test-notification、#1559)。
func (h *Handler) TestNotification(c echo.Context) error {
	user := middleware.GetUser(c)
	if h.testNotifier != nil && user != nil {
		h.testNotifier.OnTest(user.ID)
	}
	return c.NoContent(http.StatusNoContent)
}
