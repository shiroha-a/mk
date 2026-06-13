// Package notifications provides /api/notifications/* endpoints.
package notifications

import (
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
	svc                  *notification.Service
	idGen                id.Generator
	userRepo             repository.UserRepository
	noteRepo             repository.NoteRepository
	queryService         *corenote.QueryService
	followReqRepo        repository.FollowRequestRepository
	instanceRepo         repository.InstanceRepository
	emojiRepo            repository.EmojiRepository
	testNotifier         TestNotifier
	roleLookup           entity.RoleLookup
	chatInvitationLookup entity.ChatInvitationLookup
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
	noteIDSet := make(map[string]struct{})
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
		if n.NoteID != "" {
			noteIDSet[n.NoteID] = struct{}{}
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
// 注: upstream は header/icon が無い場合 token.name / token.iconUrl に
// フォールバックし appAccessTokenId も記録するが、mk-go の request context は
// raw token 文字列のみで AccessToken オブジェクトを持たないため、ここでは
// 渡された body / header / icon のみを使う (token 由来の fallback は省略)。
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

	extra := map[string]any{"body": *req.Body}
	if req.Header != nil {
		extra["header"] = *req.Header
	}
	if req.Icon != nil {
		extra["icon"] = *req.Icon
	}
	// app 通知は notifier を持たない。NotifierID を空にすることで
	// service.Create の self-notification ガード (NotifierID == NotifieeID) も
	// 回避する。upstream 同様 fire-and-forget なので結果は 204 固定。
	if _, err := h.svc.Create(c.Request().Context(), notification.CreateInput{
		NotifieeID: user.ID,
		Type:       notification.TypeApp,
		Extra:      extra,
	}); err != nil {
		slog.Warn("notifications/create failed", "user", user.ID, "err", err)
	}
	return c.NoContent(http.StatusNoContent)
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
