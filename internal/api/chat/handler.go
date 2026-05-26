package chat

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/api/apierr"
	corechat "github.com/shiroha-a/mk/internal/core/chat"
	"github.com/shiroha-a/mk/internal/entity"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
	"github.com/shiroha-a/mk/internal/server/middleware"
)

// legacyNow is a tiny indirection so tests can override time in the handler's
// legacy (non-service) path if needed. 現状は time.Now を直接返す。
func legacyNow() time.Time { return time.Now() }

// Handler handles chat/* endpoints.
type Handler struct {
	repo  repository.ChatRepository
	idGen id.Generator
	svc   *corechat.Service
	// fileRepo / moderatorChecker は drive/files/attached-chat-messages 用
	// (#1218)。fileId の owner 検証 (moderator は任意 file 可) に使う。未配線
	// 時は当該 endpoint が空配列を返す。
	fileRepo         repository.DriveFileRepository
	moderatorChecker ModeratorChecker
}

// ModeratorChecker reports whether a user has moderator privileges. Implemented
// by core/role.Service. Used to widen attached-chat-messages from owner-only to
// any file for moderators (upstream `userId: isModerator ? undefined : me.id`).
type ModeratorChecker interface {
	IsModerator(userID string) bool
}

// SetDriveFileRepo wires the drive file repository for attached-chat-messages.
func (h *Handler) SetDriveFileRepo(r repository.DriveFileRepository) { h.fileRepo = r }

// SetModeratorChecker wires a moderator checker for attached-chat-messages.
func (h *Handler) SetModeratorChecker(m ModeratorChecker) { h.moderatorChecker = m }

// NewHandler creates a new chat handler.
func NewHandler(repo repository.ChatRepository, idGen id.Generator) *Handler {
	return &Handler{repo: repo, idGen: idGen}
}

// SetService wires a chat Service so that state-changing message endpoints
// (create/update/delete/read) route through it and publish streaming events.
// 未設定の場合は従来どおり repo 直操作にフォールバックする (既存テスト互換)。
func (h *Handler) SetService(svc *corechat.Service) {
	h.svc = svc
}

// mapChatErr maps a core/chat service error to an Echo response.
func (h *Handler) mapChatErr(c echo.Context, err error) error {
	switch {
	case errors.Is(err, corechat.ErrNotFound):
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_MESSAGE", "No such message.", "006d73c9-dada-5b5d-a0f5-00b01f70bc3c"))
	case errors.Is(err, corechat.ErrForbidden):
		return c.JSON(http.StatusForbidden, apierr.Error("ACCESS_DENIED", "Access denied.", "1fb7cb09-d46a-4fff-b8df-057708cce513"))
	case errors.Is(err, corechat.ErrInvalidTarget):
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "toUserId or toRoomId is required.", "ed1d7571-a3ac-4370-899c-0dbe5e230cc8"))
	case errors.Is(err, corechat.ErrChatScopeViolation):
		// CherryPick の `recipient is cannot chat (...)` 相当 (#692)。
		// upstream は ApiError を持たず単なる Error を投げるので mk-go 固有
		// code (RECIPIENT_CANNOT_CHAT) で返す。frontend の対応は別 issue。
		return c.JSON(http.StatusForbidden, apierr.Error("RECIPIENT_CANNOT_CHAT", "Recipient does not allow chat from you.", "5e1fa6e8-1d2f-49a3-9c20-7a09be7b4c43"))
	case errors.Is(err, corechat.ErrChatScopeUnconfigured):
		// followingRepo 未配線。production では起きてはいけないので 500 と
		// log で運用者に気付かせる (silent allow するより安全側)。
		slog.Error("chat: followingRepo missing; chatScope granular check failed closed", "err", err)
		return apierr.JSONInternalError(c)
	}
	return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
}

// packRoom returns the bare ChatRoom shape used by packRoomWithCreatedAt.
// createdAt は wrapper 側で ID 派生で付与するため、本関数は packRoom*
// helper の内部実装として packRoomWithCreatedAt 経由でのみ呼ぶこと。
// 直接 caller として使うと createdAt が抜けて upstream 互換 contract を
// 破る (= packedChatRoomSchema は createdAt 必須)。
//
// upstream Misskey TS の packedChatRoomSchema は `isArchived` を返さない。
// mk-go も互換のため field を出力しない (#851)。archived state は別経路で
// 取得する設計を踏襲する。
func packRoom(r *model.ChatRoom) map[string]any {
	result := map[string]any{
		"id": r.ID, "name": r.Name, "ownerId": r.OwnerID,
		"description": r.Description,
	}
	if r.Owner != nil {
		result["owner"] = packUser(r.Owner)
	}
	return result
}

// packMessage builds the ChatMessage response shape used across chat
// endpoints. nil pointer field (toUserId / toRoomId / text / fileId) は
// upstream の TypeORM 由来 undefined 挙動に揃え、JSON response から omit する
// (= map に key を入れない、#851)。
//
// 注: m.URI は remote chat message の AP canonical URI (federation 内部用途)
// だが、upstream の packedChatMessageSchema には `uri` field が無いため
// 外部 response には含めない。
//
// 注: m.Reads (chat_message.reads) は mk-go 独自の DB column で、upstream の
// packedChatMessageSchema には対応 field が無い。response から omit して
// upstream 互換に揃える (#855)。既読判定 (isRead) は別途 hint 経由で
// computed する設計に移行予定。
func packMessage(m *model.ChatMessage) map[string]any {
	result := map[string]any{
		"id":         m.ID,
		"fromUserId": m.FromUserID,
		"reactions":  packChatReactions(m.Reactions),
	}
	if m.ToUserID != nil {
		result["toUserId"] = *m.ToUserID
	}
	if m.ToRoomID != nil {
		result["toRoomId"] = *m.ToRoomID
	}
	if m.Text != nil {
		result["text"] = *m.Text
	}
	if m.FileID != nil {
		result["fileId"] = *m.FileID
	}
	if m.FromUser != nil {
		result["fromUser"] = packUser(m.FromUser)
	}
	return result
}

// packChatReactions converts the stored `"<userId>/<reaction>"` reaction
// strings (CherryPick chat reaction 形式) into the object array shape that
// misskey_dart の ChatMessage.fromJson expects (`reactions: [{reaction}]`)。
// raw 文字列配列のまま返すと `String is not Map<String, dynamic>` で落ちる
// (#1246)。user は upstream の packMessageLite と同じく省略 (schema 上 nullable)。
func packChatReactions(reactions []string) []map[string]any {
	out := make([]map[string]any, 0, len(reactions))
	for _, r := range reactions {
		reaction := r
		// 保存形式は "<userId>/<reaction>"。最初の "/" 以降を reaction とする。
		if i := strings.Index(r, "/"); i >= 0 {
			reaction = r[i+1:]
		}
		out = append(out, map[string]any{"reaction": reaction})
	}
	return out
}

// packMessageWithCreatedAt augments packMessage with createdAt parsed from the
// message ID and the eager-loaded file (if any). createdAt 欠落だと FE 側の
// MkTime が `Invalid Date` を表示し、一覧では NaN/NaN になる (#692)。
//
// file field は upstream packedChatMessageSchema の `file: optional/nullable
// (DriveFile)` に対応し、m.File が Preload で eager load されている場合のみ
// pack する (#855 PR-C)。chat repository の主要 list/find query で
// Preload("File") を行う前提。create-to-user 直後の response は CreateMessage
// が file を eager load しないため、file field 不在となる現状制約あり (= TS
// upstream とは差分、follow-up issue で create response も file を含めるよう
// 修正予定)。
func (h *Handler) packMessageWithCreatedAt(m *model.ChatMessage) map[string]any {
	result := packMessage(m)
	if h.idGen != nil {
		if t, err := h.idGen.ParseTime(m.ID); err == nil {
			result["createdAt"] = t.UTC().Format("2006-01-02T15:04:05.000Z")
		}
		if m.File != nil {
			result["file"] = entity.PackDriveFile(m.File, h.idGen)
		}
	}
	return result
}

// packRoomWithCreatedAt augments packRoom with createdAt parsed from the
// room ID. upstream の packedChatRoomSchema は createdAt を必須 field と
// するため、room を返す全 endpoint は本 method 経由で createdAt を含める
// (packMessageWithCreatedAt と同 pattern, #855)。
func (h *Handler) packRoomWithCreatedAt(r *model.ChatRoom) map[string]any {
	result := packRoom(r)
	if h.idGen != nil {
		if t, err := h.idGen.ParseTime(r.ID); err == nil {
			result["createdAt"] = t.UTC().Format("2006-01-02T15:04:05.000Z")
		}
	}
	return result
}

// packRoomDetailed augments packRoomWithCreatedAt with viewer (= meID) -
// specific optional field `isMuted` / `invitationExists`。upstream Misskey
// TS の ChatEntityService.packRoom と同じ semantics:
//   - meID が空、または me === room.OwnerID の場合は lookup を skip
//     (= owner は自身の room なので mute も invitation も無関係、両 false)
//   - me !== owner の場合のみ membership / invitation を lookup する
//
// upstream packedChatRoomSchema は両 field を `optional: true` で定義する
// が、mk-go は handler 経由 response で常に boolean を返す (= 利用側が
// undefined / boolean のどちらか分岐を考えずに済むよう一貫させる、#855)。
//
// caller が meID を渡せない場面 (例: federation 経由の eager pack や
// 内部用途) では packRoomWithCreatedAt をそのまま使う。
func (h *Handler) packRoomDetailed(r *model.ChatRoom, meID string) map[string]any {
	result := h.packRoomWithCreatedAt(r)
	isMuted := false
	invitationExists := false
	if meID != "" && meID != r.OwnerID {
		if mem, err := h.repo.FindMembership(meID, r.ID); err == nil && mem != nil {
			isMuted = mem.IsMuted
		}
		if _, err := h.repo.FindInvitation(meID, r.ID); err == nil {
			invitationExists = true
		}
	}
	result["isMuted"] = isMuted
	result["invitationExists"] = invitationExists
	return result
}

// packMessageDetailed builds the upstream-compatible ChatMessage payload
// expected by MkChatHistories.vue: createdAt (ID から派生)、fromUser、
// toUser (DM の時)、toRoom (room の時)。upstream の
// ChatEntityService.packMessageDetailed 相当 (#692)。
//
// FE は createdAt を `new Date(m.createdAt).getTime()` で sort key に使う
// ので、欠損していると `NaN` になりリストが空になる。room メッセージは
// `'room' in m` で判定して "other" のかわりに room を表示する。
//
// meID は viewer (request 主体) の user ID。toRoom の `isMuted` /
// `invitationExists` を viewer 視点で計算する (#860 PR-D)。空文字列を渡した
// 場合は packRoomDetailed が lookup を skip して両 false 固定。
//
// 注: upstream の packMessageDetailed は `isRead` field を返さない (json-schema
// 上 `optional: true` だが実装側で emit していない、PR #861 で再調査済)。
// mk-go 側で先に追加すると upstream と逆方向の shape drift を生むため
// **意図的に未対応** とする。upstream が isRead を実装した場合に追従する。
func (h *Handler) packMessageDetailed(m *model.ChatMessage, meID string) map[string]any {
	result := h.packMessageWithCreatedAt(m)
	if m.ToUser != nil {
		result["toUser"] = packUser(m.ToUser)
	}
	if m.ToRoom != nil {
		result["toRoom"] = h.packRoomDetailed(m.ToRoom, meID)
		// upstream の `'room' in m` 判定に合わせて `room` alias も入れる。
		// 旧 chat 実装ではこの key で判定していた残存 client がある。
		result["room"] = h.packRoomDetailed(m.ToRoom, meID)
	}
	return result
}

// packUser produces the UserLite shape consumed by FE chat components.
// FE の MkAvatar / MkUserName が `avatarUrl` / `avatarBlurhash` を読むため、
// 含めないとアイコンが空のまま表示される (#692)。avatarUrl 未設定時は
// `entity.IdenticonURL` 経由で identicon URL に fallback する (TS upstream
// `getIdenticonUrl` 相当)。
func packUser(u *model.User) map[string]any {
	return map[string]any{
		"id":             u.ID,
		"username":       u.Username,
		"name":           u.Name,
		"host":           u.Host,
		"avatarUrl":      entity.IdenticonURL(u),
		"avatarBlurhash": u.AvatarBlurhash,
		"isBot":          u.IsBot,
		"isCat":          u.IsCat,
	}
}

// AttachedChatMessages handles POST /api/drive/files/attached-chat-messages.
// 指定 drive file を添付した chat message を newest-first で返す。upstream
// (read:drive) と同じく file は owner-scope で引く (moderator は任意 file 可)。
// 連合互換: res は ChatMessage の配列 (packMessageDetailed)。
func (h *Handler) AttachedChatMessages(c echo.Context) error {
	user := middleware.GetUser(c)
	var req struct {
		FileID    string `json:"fileId"`
		Limit     int    `json:"limit"`
		SinceID   string `json:"sinceId"`
		UntilID   string `json:"untilId"`
		SinceDate *int64 `json:"sinceDate"`
		UntilDate *int64 `json:"untilDate"`
	}
	if err := c.Bind(&req); err != nil || req.FileID == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "fileId is required.", "6b7f9d2c-1e4a-4b8c-9d3e-7a5f0c2b1e88"))
	}
	// upstream paramDef は limit を minimum:1 / maximum:100 / default:10 で
	// clamp する。> 100 は default に落とさず max へ clamp して挙動を揃える。
	if req.Limit <= 0 {
		req.Limit = 10
	} else if req.Limit > 100 {
		req.Limit = 100
	}
	if h.fileRepo == nil {
		return c.JSON(http.StatusOK, []any{})
	}
	file, err := h.fileRepo.FindByID(req.FileID)
	if err != nil || file == nil {
		return apierr.JSONNoSuchFile(c)
	}
	// owner-scope: moderator 以外は自分の file のみ参照可 (他人の private chat
	// の添付を覗けないようにする)。
	isMod := h.moderatorChecker != nil && h.moderatorChecker.IsModerator(user.ID)
	if !isMod && (file.UserID == nil || *file.UserID != user.ID) {
		return apierr.JSONNoSuchFile(c)
	}
	sinceID, untilID := id.NormalizeCursor(req.SinceID, req.UntilID, req.SinceDate, req.UntilDate)
	msgs, err := h.repo.ListMessagesByFileID(req.FileID, untilID, sinceID, req.Limit)
	if err != nil {
		return apierr.JSONInternalError(c)
	}
	out := make([]map[string]any, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, h.packMessageDetailed(m, user.ID))
	}
	return c.JSON(http.StatusOK, out)
}

// --- Rooms ---

// RoomsCreate handles POST /api/chat/rooms/create.
func (h *Handler) RoomsCreate(c echo.Context) error {
	user := middleware.GetUser(c)
	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := c.Bind(&req); err != nil || req.Name == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "name is required.", "ed1d7571-a3ac-4370-899c-0dbe5e230cc8"))
	}
	room := &model.ChatRoom{
		ID: h.idGen.Generate(time.Now()), Name: req.Name,
		OwnerID: user.ID, Description: req.Description,
	}
	if err := h.repo.CreateRoom(room); err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	// golden ChatRoom は owner (UserLite) を必須とする。upstream は ownerId から
	// owner を解決して必ず含めるため、作成者 (= 認証 user) を attach してから
	// pack する (#1284)。未 attach だと packRoom が owner を omit していた。
	room.Owner = user
	return c.JSON(http.StatusOK, h.packRoomDetailed(room, user.ID))
}

// RoomsShow handles POST /api/chat/rooms/show.
//
// upstream Misskey TS 2026.5.4 (`hasPermissionToViewRoomInfo`) と互換するため、
// owner / member / 招待受信者 / moderator 以外は noSuchRoom (404) を返す
// (#1164 Phase C)。旧 mk-go は room id を知っていれば誰でも room メタ情報を
// 取得できる drop-in regression 状態だったため本 gate で塞ぐ。
func (h *Handler) RoomsShow(c echo.Context) error {
	user := middleware.GetUser(c)
	var req struct {
		RoomID string `json:"roomId"`
	}
	if err := c.Bind(&req); err != nil || req.RoomID == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "roomId is required.", "ed1d7571-a3ac-4370-899c-0dbe5e230cc8"))
	}
	room, err := h.repo.FindRoomByID(req.RoomID)
	if err != nil {
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_ROOM", "No such room.", "6ab4d7df-5043-57b9-bd5d-ff9908288473"))
	}
	if h.svc != nil {
		ok, err := h.svc.HasPermissionToViewRoomInfo(user.ID, room)
		if err != nil || !ok {
			return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_ROOM", "No such room.", "6ab4d7df-5043-57b9-bd5d-ff9908288473"))
		}
	}
	return c.JSON(http.StatusOK, h.packRoomDetailed(room, user.ID))
}

// RoomsUpdate handles POST /api/chat/rooms/update.
func (h *Handler) RoomsUpdate(c echo.Context) error {
	user := middleware.GetUser(c)
	var req struct {
		RoomID      string `json:"roomId"`
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := c.Bind(&req); err != nil || req.RoomID == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "roomId is required.", "ed1d7571-a3ac-4370-899c-0dbe5e230cc8"))
	}
	room, err := h.repo.FindRoomByID(req.RoomID)
	if err != nil || room.OwnerID != user.ID {
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_ROOM", "No such room.", "6ab4d7df-5043-57b9-bd5d-ff9908288473"))
	}
	if req.Name != "" {
		room.Name = req.Name
	}
	if req.Description != "" {
		room.Description = req.Description
	}
	if err := h.repo.UpdateRoom(room); err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	return c.JSON(http.StatusOK, h.packRoomDetailed(room, user.ID))
}

// RoomsDelete handles POST /api/chat/rooms/delete.
func (h *Handler) RoomsDelete(c echo.Context) error {
	user := middleware.GetUser(c)
	var req struct {
		RoomID string `json:"roomId"`
	}
	if err := c.Bind(&req); err != nil || req.RoomID == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "roomId is required.", "ed1d7571-a3ac-4370-899c-0dbe5e230cc8"))
	}
	room, err := h.repo.FindRoomByID(req.RoomID)
	if err != nil || room.OwnerID != user.ID {
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_ROOM", "No such room.", "6ab4d7df-5043-57b9-bd5d-ff9908288473"))
	}
	_ = h.repo.DeleteRoom(req.RoomID)
	return c.NoContent(http.StatusNoContent)
}

// RoomsOwned handles POST /api/chat/rooms/owned.
func (h *Handler) RoomsOwned(c echo.Context) error {
	user := middleware.GetUser(c)
	rooms, _ := h.repo.ListRoomsByOwner(user.ID)
	result := make([]map[string]any, len(rooms))
	for i, r := range rooms {
		// 全 room の owner = user.ID (= 一覧の filter 条件)。packRoomDetailed は
		// 内部で me === owner check で lookup を skip し isMuted/invitationExists
		// を false に固定する。
		result[i] = h.packRoomDetailed(r, user.ID)
	}
	return c.JSON(http.StatusOK, result)
}

// RoomsJoined handles POST /api/chat/rooms/joined.
func (h *Handler) RoomsJoined(c echo.Context) error {
	user := middleware.GetUser(c)
	rooms, _ := h.repo.ListJoinedRooms(user.ID)
	result := make([]map[string]any, len(rooms))
	for i, r := range rooms {
		// joined 一覧は本人が member の room なので room ごとに membership
		// lookup が走る (= isMuted を返すため)。upstream は packRooms hint で
		// bulk fetch するが、本実装は per-room lookup (= 件数 N の clean fetch)。
		// hint cache 化は別 issue で扱う。
		result[i] = h.packRoomDetailed(r, user.ID)
	}
	return c.JSON(http.StatusOK, result)
}

// RoomsLeave handles POST /api/chat/rooms/leave.
func (h *Handler) RoomsLeave(c echo.Context) error {
	user := middleware.GetUser(c)
	var req struct {
		RoomID string `json:"roomId"`
	}
	if err := c.Bind(&req); err != nil || req.RoomID == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "roomId is required.", "ed1d7571-a3ac-4370-899c-0dbe5e230cc8"))
	}
	_ = h.repo.DeleteMembership(user.ID, req.RoomID)
	return c.NoContent(http.StatusNoContent)
}

// RoomsMute handles POST /api/chat/rooms/mute.
func (h *Handler) RoomsMute(c echo.Context) error {
	user := middleware.GetUser(c)
	var req struct {
		RoomID string `json:"roomId"`
	}
	if err := c.Bind(&req); err != nil || req.RoomID == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "roomId is required.", "ed1d7571-a3ac-4370-899c-0dbe5e230cc8"))
	}
	m, err := h.repo.FindMembership(user.ID, req.RoomID)
	if err != nil {
		return c.NoContent(http.StatusNoContent)
	}
	m.IsMuted = true
	_ = h.repo.UpdateMembership(m)
	return c.NoContent(http.StatusNoContent)
}

// RoomsUnmute handles POST /api/chat/rooms/unmute.
func (h *Handler) RoomsUnmute(c echo.Context) error {
	user := middleware.GetUser(c)
	var req struct {
		RoomID string `json:"roomId"`
	}
	if err := c.Bind(&req); err != nil || req.RoomID == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "roomId is required.", "ed1d7571-a3ac-4370-899c-0dbe5e230cc8"))
	}
	m, err := h.repo.FindMembership(user.ID, req.RoomID)
	if err != nil {
		return c.NoContent(http.StatusNoContent)
	}
	m.IsMuted = false
	_ = h.repo.UpdateMembership(m)
	return c.NoContent(http.StatusNoContent)
}

// RoomsTransferOwnership handles POST /api/chat/rooms/transfer-ownership.
func (h *Handler) RoomsTransferOwnership(c echo.Context) error {
	user := middleware.GetUser(c)
	var req struct {
		RoomID string `json:"roomId"`
		UserID string `json:"userId"`
	}
	if err := c.Bind(&req); err != nil || req.RoomID == "" || req.UserID == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "roomId and userId are required.", "ed1d7571-a3ac-4370-899c-0dbe5e230cc8"))
	}
	room, err := h.repo.FindRoomByID(req.RoomID)
	if err != nil || room.OwnerID != user.ID {
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_ROOM", "No such room.", "6ab4d7df-5043-57b9-bd5d-ff9908288473"))
	}
	room.OwnerID = req.UserID
	_ = h.repo.UpdateRoom(room)
	return c.NoContent(http.StatusNoContent)
}

// --- Messages ---

// MessagesCreate handles POST /api/chat/messages/create.
// service が wire されていれば streaming 配信も同時に行う。
func (h *Handler) MessagesCreate(c echo.Context) error {
	user := middleware.GetUser(c)
	var req struct {
		Text     *string `json:"text"`
		ToUserID *string `json:"toUserId"`
		ToRoomID *string `json:"toRoomId"`
		FileID   *string `json:"fileId"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "Invalid parameters.", "ed1d7571-a3ac-4370-899c-0dbe5e230cc8"))
	}
	if h.svc == nil {
		// 未wire時のフォールバック (テスト互換)
		return h.messagesCreateLegacy(c, user, req.Text, req.ToUserID, req.ToRoomID, req.FileID)
	}
	text := ""
	if req.Text != nil {
		text = *req.Text
	}
	fileID := ""
	if req.FileID != nil {
		fileID = *req.FileID
	}
	var (
		msg *model.ChatMessage
		err error
	)
	switch {
	case req.ToRoomID != nil && *req.ToRoomID != "":
		msg, err = h.svc.CreateMessageToRoom(c.Request().Context(), user.ID, *req.ToRoomID, text, fileID)
	case req.ToUserID != nil && *req.ToUserID != "":
		msg, err = h.svc.CreateMessageToUser(c.Request().Context(), user.ID, *req.ToUserID, text, fileID)
	default:
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "toUserId or toRoomId is required.", "ed1d7571-a3ac-4370-899c-0dbe5e230cc8"))
	}
	if err != nil {
		return h.mapChatErr(c, err)
	}
	return c.JSON(http.StatusOK, h.packMessageDetailed(withSender(h.reloadIfFilePending(msg), user), user.ID))
}

// messagesCreateLegacy preserves pre-Phase-9.8 behaviour for callers that
// construct a Handler without wiring the service. 既存の chat handler テスト
// はこちらを通る。
func (h *Handler) messagesCreateLegacy(c echo.Context, user *model.User, text, toUserID, toRoomID, fileID *string) error {
	msg := &model.ChatMessage{
		ID: h.idGen.Generate(legacyNow()), FromUserID: user.ID,
		ToUserID: toUserID, ToRoomID: toRoomID,
		Text: text, FileID: fileID,
	}
	if err := h.repo.CreateMessage(msg); err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	return c.JSON(http.StatusOK, h.packMessageDetailed(withSender(h.reloadIfFilePending(msg), user), user.ID))
}

// withSender attaches the authenticated sender as FromUser when the row has
// no eager-loaded relation, so create responses include the golden-required
// `fromUser` (UserLite). 作成直後の message は CreateMessage が FromUser を
// Preload しないため fromUser が欠落していた (#1288。ChatRoom owner #1285 と
// 同種の embed 漏れ)。reload で DB から FromUser が載った場合は上書きしない。
func withSender(m *model.ChatMessage, sender *model.User) *model.ChatMessage {
	if m != nil && m.FromUser == nil {
		m.FromUser = sender
	}
	return m
}

// reloadIfFilePending re-fetches the message via FindMessageByID (which
// preloads File) when the message has a fileId but no eager-loaded File
// yet. CreateMessage 経路は File を Preload しないので、create response の
// `file` field を upstream packMessageDetailed と同 shape にするための
// post-fetch helper (#860 PR-D)。reload に失敗した場合は元の msg をその
// まま返す (= file 不在で fall-through)。
func (h *Handler) reloadIfFilePending(msg *model.ChatMessage) *model.ChatMessage {
	if msg == nil || msg.FileID == nil || msg.File != nil {
		return msg
	}
	if reloaded, err := h.repo.FindMessageByID(msg.ID); err == nil {
		return reloaded
	}
	return msg
}

// MessagesShow handles POST /api/chat/messages/show.
func (h *Handler) MessagesShow(c echo.Context) error {
	user := middleware.GetUser(c)
	var req struct {
		MessageID string `json:"messageId"`
	}
	if err := c.Bind(&req); err != nil || req.MessageID == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "messageId is required.", "ed1d7571-a3ac-4370-899c-0dbe5e230cc8"))
	}
	msg, err := h.repo.FindMessageByID(req.MessageID)
	if err != nil {
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_MESSAGE", "No such message.", "006d73c9-dada-5b5d-a0f5-00b01f70bc3c"))
	}
	return c.JSON(http.StatusOK, h.packMessageDetailed(msg, user.ID))
}

// MessagesUpdate handles POST /api/chat/messages/update.
func (h *Handler) MessagesUpdate(c echo.Context) error {
	user := middleware.GetUser(c)
	var req struct {
		MessageID string  `json:"messageId"`
		Text      *string `json:"text"`
	}
	if err := c.Bind(&req); err != nil || req.MessageID == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "messageId is required.", "ed1d7571-a3ac-4370-899c-0dbe5e230cc8"))
	}
	if h.svc == nil {
		// legacy fallback
		msg, err := h.repo.FindMessageByID(req.MessageID)
		if err != nil || msg.FromUserID != user.ID {
			return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_MESSAGE", "No such message.", "006d73c9-dada-5b5d-a0f5-00b01f70bc3c"))
		}
		if req.Text != nil {
			msg.Text = req.Text
			_ = h.repo.UpdateMessage(msg)
		}
		return c.NoContent(http.StatusNoContent)
	}
	text := ""
	if req.Text != nil {
		text = *req.Text
	}
	if _, err := h.svc.UpdateMessage(c.Request().Context(), user.ID, req.MessageID, text); err != nil {
		return h.mapChatErr(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}

// MessagesDelete handles POST /api/chat/messages/delete.
func (h *Handler) MessagesDelete(c echo.Context) error {
	user := middleware.GetUser(c)
	var req struct {
		MessageID string `json:"messageId"`
	}
	if err := c.Bind(&req); err != nil || req.MessageID == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "messageId is required.", "ed1d7571-a3ac-4370-899c-0dbe5e230cc8"))
	}
	if h.svc == nil {
		msg, err := h.repo.FindMessageByID(req.MessageID)
		if err != nil || msg.FromUserID != user.ID {
			return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_MESSAGE", "No such message.", "006d73c9-dada-5b5d-a0f5-00b01f70bc3c"))
		}
		_ = h.repo.DeleteMessage(req.MessageID)
		return c.NoContent(http.StatusNoContent)
	}
	if err := h.svc.DeleteMessage(c.Request().Context(), user.ID, req.MessageID); err != nil {
		return h.mapChatErr(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}

// MessagesRead handles POST /api/chat/messages/read.
func (h *Handler) MessagesRead(c echo.Context) error {
	user := middleware.GetUser(c)
	var req struct {
		MessageID string `json:"messageId"`
	}
	if err := c.Bind(&req); err != nil || req.MessageID == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "messageId is required.", "ed1d7571-a3ac-4370-899c-0dbe5e230cc8"))
	}
	if h.svc != nil {
		if err := h.svc.MarkReadByMessageID(c.Request().Context(), user.ID, req.MessageID); err != nil {
			return h.mapChatErr(c, err)
		}
		return c.NoContent(http.StatusNoContent)
	}
	_ = h.repo.MarkRead(user.ID, req.MessageID)
	return c.NoContent(http.StatusNoContent)
}

// Messages handles POST /api/chat/messages (list).
func (h *Handler) Messages(c echo.Context) error {
	user := middleware.GetUser(c)
	var req struct {
		RoomID string `json:"roomId"`
		UserID string `json:"userId"`
		Limit  int    `json:"limit"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "Invalid parameters.", "ed1d7571-a3ac-4370-899c-0dbe5e230cc8"))
	}
	var msgs []*model.ChatMessage
	if req.RoomID != "" {
		msgs, _ = h.repo.ListMessagesByRoom(req.RoomID, req.Limit)
	} else if req.UserID != "" {
		msgs, _ = h.repo.ListMessagesByUser(user.ID, req.UserID, req.Limit)
	}
	result := make([]map[string]any, len(msgs))
	for i, m := range msgs {
		result[i] = h.packMessageDetailed(m, user.ID)
	}
	return c.JSON(http.StatusOK, result)
}

// MessagesSearch handles POST /api/chat/messages/search.
func (h *Handler) MessagesSearch(c echo.Context) error {
	user := middleware.GetUser(c)
	var req struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "Invalid parameters.", "ed1d7571-a3ac-4370-899c-0dbe5e230cc8"))
	}
	msgs, _ := h.repo.SearchMessages(user.ID, req.Query, req.Limit)
	result := make([]map[string]any, len(msgs))
	for i, m := range msgs {
		result[i] = h.packMessageDetailed(m, user.ID)
	}
	return c.JSON(http.StatusOK, result)
}

// ReactionsCreate handles POST /api/chat/messages/reactions/create and
// POST /api/chat/messages/react. Reaction format: "userId/emoji".
func (h *Handler) ReactionsCreate(c echo.Context) error {
	user := middleware.GetUser(c)
	var req struct {
		MessageID string `json:"messageId"`
		Reaction  string `json:"reaction"`
	}
	if err := c.Bind(&req); err != nil || req.MessageID == "" || req.Reaction == "" {
		return apierr.JSONInvalidParam(c)
	}
	reaction := user.ID + "/" + req.Reaction
	if err := h.repo.AddReaction(req.MessageID, reaction); err != nil {
		return apierr.JSONInternalError(c)
	}
	return c.NoContent(http.StatusNoContent)
}

// ReactionsDelete handles POST /api/chat/messages/reactions/delete and
// POST /api/chat/messages/unreact.
func (h *Handler) ReactionsDelete(c echo.Context) error {
	user := middleware.GetUser(c)
	var req struct {
		MessageID string `json:"messageId"`
		Reaction  string `json:"reaction"`
	}
	if err := c.Bind(&req); err != nil || req.MessageID == "" || req.Reaction == "" {
		return apierr.JSONInvalidParam(c)
	}
	reaction := user.ID + "/" + req.Reaction
	if err := h.repo.RemoveReaction(req.MessageID, reaction); err != nil {
		return apierr.JSONInternalError(c)
	}
	return c.NoContent(http.StatusNoContent)
}

// --- Invitations ---

// InvitationsCreate handles POST /api/chat/rooms/invitations/create.
func (h *Handler) InvitationsCreate(c echo.Context) error {
	user := middleware.GetUser(c)
	var req struct {
		RoomID string `json:"roomId"`
		UserID string `json:"userId"`
	}
	if err := c.Bind(&req); err != nil || req.RoomID == "" || req.UserID == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "roomId and userId are required.", "ed1d7571-a3ac-4370-899c-0dbe5e230cc8"))
	}
	// owner だけが招待を作成・連合できる。これが無いと任意の認証ユーザーが
	// owner の鍵で署名された Invite を remote へなりすまし送信できてしまう
	// (#1201 review)。room owner 以外は NO_SUCH_ROOM で弾く (RoomsUpdate と同方針)。
	room, err := h.repo.FindRoomByID(req.RoomID)
	if err != nil || room.OwnerID != user.ID {
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_ROOM", "No such room.", "6ab4d7df-5043-57b9-bd5d-ff9908288473"))
	}
	inv := &model.ChatRoomInvitation{
		ID: h.idGen.Generate(time.Now()), UserID: req.UserID, RoomID: req.RoomID,
	}
	if err := h.repo.CreateInvitation(inv); err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	// invitee が remote user なら Invite activity を配送する (CherryPick group
	// chat federation, #1201)。local invitee なら service 側で no-op。
	if h.svc != nil {
		h.svc.FederateInvitation(req.RoomID, req.UserID)
	}
	return c.NoContent(http.StatusNoContent)
}

// InvitationsDelete handles POST /api/chat/rooms/invitations/delete.
func (h *Handler) InvitationsDelete(c echo.Context) error {
	var req struct {
		InvitationID string `json:"invitationId"`
	}
	if err := c.Bind(&req); err != nil || req.InvitationID == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "invitationId is required.", "ed1d7571-a3ac-4370-899c-0dbe5e230cc8"))
	}
	_ = h.repo.DeleteInvitation(req.InvitationID)
	return c.NoContent(http.StatusNoContent)
}

// InvitationsAccept handles POST /api/chat/rooms/invitations/accept.
func (h *Handler) InvitationsAccept(c echo.Context) error {
	user := middleware.GetUser(c)
	var req struct {
		InvitationID string `json:"invitationId"`
		RoomID       string `json:"roomId"`
	}
	if err := c.Bind(&req); err != nil || req.RoomID == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "roomId is required.", "ed1d7571-a3ac-4370-899c-0dbe5e230cc8"))
	}
	// pending invitation がある場合のみ accept できる。これが無いと招待無しに
	// membership が作られ、任意 room owner へ未承諾 Accept が飛ぶ (#1206 review)。
	// 招待なしの参加は別途 rooms/join を使う。
	inv, err := h.repo.FindInvitation(user.ID, req.RoomID)
	if err != nil {
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_INVITATION", "No such invitation.", "8c8f38f0-3b7e-4f5e-9c6d-2e3f4a5b6c7d"))
	}
	// メンバーシップを作成して招待を消費する。
	m := &model.ChatRoomMembership{
		ID: h.idGen.Generate(time.Now()), UserID: user.ID, RoomID: req.RoomID,
	}
	_ = h.repo.CreateMembership(m)
	_ = h.repo.DeleteInvitation(inv.ID)
	// remote room の招待なら Accept を owner へ配送する (#1205)。local room は
	// service 側で no-op。
	if h.svc != nil {
		h.svc.FederateInvitationResponse(req.RoomID, user.ID, true)
	}
	return c.NoContent(http.StatusNoContent)
}

// InvitationsReject handles POST /api/chat/rooms/invitations/reject.
func (h *Handler) InvitationsReject(c echo.Context) error {
	user := middleware.GetUser(c)
	var req struct {
		RoomID string `json:"roomId"`
	}
	if err := c.Bind(&req); err != nil || req.RoomID == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "roomId is required.", "ed1d7571-a3ac-4370-899c-0dbe5e230cc8"))
	}
	// pending invitation がある場合のみ reject できる (任意 room owner への
	// 未承諾 Reject 送信を防ぐ、#1206 review)。
	inv, err := h.repo.FindInvitation(user.ID, req.RoomID)
	if err != nil {
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_INVITATION", "No such invitation.", "8c8f38f0-3b7e-4f5e-9c6d-2e3f4a5b6c7d"))
	}
	_ = h.repo.DeleteInvitation(inv.ID)
	// remote room の招待なら Reject を owner へ配送する (#1205)。
	if h.svc != nil {
		h.svc.FederateInvitationResponse(req.RoomID, user.ID, false)
	}
	return c.NoContent(http.StatusNoContent)
}

// MembersBan handles POST /api/chat/rooms/members/ban.
func (h *Handler) MembersBan(c echo.Context) error {
	var req struct {
		RoomID string `json:"roomId"`
		UserID string `json:"userId"`
	}
	if err := c.Bind(&req); err != nil || req.RoomID == "" || req.UserID == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "roomId and userId are required.", "ed1d7571-a3ac-4370-899c-0dbe5e230cc8"))
	}
	_ = h.repo.DeleteMembership(req.UserID, req.RoomID)
	return c.NoContent(http.StatusNoContent)
}

// MembersUpdateMembership handles POST /api/chat/rooms/members/update-membership.
// ルームオーナーのみが他メンバーの設定を変更できる。
func (h *Handler) MembersUpdateMembership(c echo.Context) error {
	user := middleware.GetUser(c)
	var req struct {
		RoomID  string `json:"roomId"`
		UserID  string `json:"userId"`
		IsMuted *bool  `json:"isMuted"`
	}
	if err := c.Bind(&req); err != nil || req.RoomID == "" || req.UserID == "" {
		return apierr.JSONInvalidParam(c)
	}
	room, err := h.repo.FindRoomByID(req.RoomID)
	if err != nil || room.OwnerID != user.ID {
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_ROOM", "No such room.", "b3926861-29ef-4df6-98b5-a7c640ad2b5a"))
	}
	mem, err := h.repo.FindMembership(req.UserID, req.RoomID)
	if err != nil {
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_MEMBER", "Not a member.", "e3285385-56ee-4909-9ef4-4a0e6e2e614a"))
	}
	if req.IsMuted != nil {
		mem.IsMuted = *req.IsMuted
	}
	if err := h.repo.UpdateMembership(mem); err != nil {
		return apierr.JSONInternalError(c)
	}
	return c.NoContent(http.StatusNoContent)
}

// --- TS-compatible aliases ---

// MessagesCreateToUser handles POST /api/chat/messages/create-to-user.
func (h *Handler) MessagesCreateToUser(c echo.Context) error {
	return h.MessagesCreate(c)
}

// MessagesCreateToRoom handles POST /api/chat/messages/create-to-room.
func (h *Handler) MessagesCreateToRoom(c echo.Context) error {
	return h.MessagesCreate(c)
}

// UserTimeline handles POST /api/chat/messages/user-timeline.
//
// upstream の readUserChatMessage 相当: チャット画面を開いた瞬間に「相手 →
// 自分」の DM をすべて既読としてマークする。FE は新着メッセージにしか
// 'read' イベントを送らないので、initial load 時点の既読化はサーバ側で
// やらないと unread badge がリロードのたびに復活する (#692)。
func (h *Handler) UserTimeline(c echo.Context) error {
	user := middleware.GetUser(c)
	var req struct {
		UserID string `json:"userId"`
		Limit  int    `json:"limit"`
	}
	if err := c.Bind(&req); err != nil || req.UserID == "" {
		return apierr.JSONInvalidParam(c)
	}
	if req.Limit <= 0 {
		req.Limit = 10
	}
	if req.Limit > 100 {
		req.Limit = 100
	}
	msgs, err := h.repo.ListMessagesByUser(user.ID, req.UserID, req.Limit)
	if err != nil {
		return apierr.JSONInternalError(c)
	}
	// 既読マーク (best effort; 失敗しても timeline 表示は継続)。
	if err := h.repo.MarkAllReadFromUser(user.ID, req.UserID); err != nil {
		slog.Warn("chat: MarkAllReadFromUser failed", "readerId", user.ID, "fromUserId", req.UserID, "err", err)
	}
	out := make([]map[string]any, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, h.packMessageDetailed(m, user.ID))
	}
	return c.JSON(http.StatusOK, out)
}

// RoomTimeline handles POST /api/chat/messages/room-timeline.
//
// upstream の readRoomChatMessage 相当 (#692)。
func (h *Handler) RoomTimeline(c echo.Context) error {
	user := middleware.GetUser(c)
	var req struct {
		RoomID string `json:"roomId"`
		Limit  int    `json:"limit"`
	}
	if err := c.Bind(&req); err != nil || req.RoomID == "" {
		return apierr.JSONInvalidParam(c)
	}
	if req.Limit <= 0 {
		req.Limit = 10
	}
	if req.Limit > 100 {
		req.Limit = 100
	}
	msgs, err := h.repo.ListMessagesByRoom(req.RoomID, req.Limit)
	if err != nil {
		return apierr.JSONInternalError(c)
	}
	if err := h.repo.MarkAllReadInRoom(user.ID, req.RoomID); err != nil {
		slog.Warn("chat: MarkAllReadInRoom failed", "readerId", user.ID, "roomId", req.RoomID, "err", err)
	}
	out := make([]map[string]any, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, h.packMessageDetailed(m, user.ID))
	}
	return c.JSON(http.StatusOK, out)
}

// ReadAll handles POST /api/chat/read-all.
func (h *Handler) ReadAll(c echo.Context) error {
	user := middleware.GetUser(c)
	if err := h.repo.MarkAllRead(user.ID); err != nil {
		return apierr.JSONInternalError(c)
	}
	return c.NoContent(http.StatusNoContent)
}

// InvitationsIgnore handles POST /api/chat/rooms/invitations/ignore.
func (h *Handler) InvitationsIgnore(c echo.Context) error {
	user := middleware.GetUser(c)
	var req struct {
		RoomID string `json:"roomId"`
	}
	if err := c.Bind(&req); err != nil || req.RoomID == "" {
		return apierr.JSONInvalidParam(c)
	}
	if inv, err := h.repo.FindInvitation(user.ID, req.RoomID); err == nil {
		inv.Ignored = true
		_ = h.repo.UpdateInvitation(inv)
	}
	return c.NoContent(http.StatusNoContent)
}

// InvitationsInbox handles POST /api/chat/rooms/invitations/inbox.
func (h *Handler) InvitationsInbox(c echo.Context) error {
	user := middleware.GetUser(c)
	rows, err := h.repo.ListInvitationsByUser(user.ID, false)
	if err != nil {
		return apierr.JSONInternalError(c)
	}
	out := make([]map[string]any, 0, len(rows))
	for _, inv := range rows {
		entry := map[string]any{"id": inv.ID, "roomId": inv.RoomID, "userId": inv.UserID}
		if inv.Room != nil {
			// inbox は本人が invitee (= owner ではない)。lookup で
			// invitationExists = true、isMuted = false (まだ非 member) になる。
			entry["room"] = h.packRoomDetailed(inv.Room, user.ID)
		}
		out = append(out, entry)
	}
	return c.JSON(http.StatusOK, out)
}

// InvitationsOutbox handles POST /api/chat/rooms/invitations/outbox.
func (h *Handler) InvitationsOutbox(c echo.Context) error {
	user := middleware.GetUser(c)
	var req struct {
		RoomID string `json:"roomId"`
	}
	if err := c.Bind(&req); err != nil || req.RoomID == "" {
		return apierr.JSONInvalidParam(c)
	}
	// 自分が所有するルームの招待一覧を返す
	room, err := h.repo.FindRoomByID(req.RoomID)
	if err != nil || room.OwnerID != user.ID {
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_ROOM", "No such room.", "b3926861-29ef-4df6-98b5-a7c640ad2b5a"))
	}
	rows, err := h.repo.ListInvitationsByRoom(req.RoomID)
	if err != nil {
		return apierr.JSONInternalError(c)
	}
	out := make([]map[string]any, 0, len(rows))
	for _, inv := range rows {
		entry := map[string]any{"id": inv.ID, "roomId": inv.RoomID, "userId": inv.UserID}
		if inv.User != nil {
			entry["user"] = packUser(inv.User)
		}
		out = append(out, entry)
	}
	return c.JSON(http.StatusOK, out)
}

// RoomsJoin handles POST /api/chat/rooms/join.
func (h *Handler) RoomsJoin(c echo.Context) error {
	user := middleware.GetUser(c)
	var req struct {
		RoomID string `json:"roomId"`
	}
	if err := c.Bind(&req); err != nil || req.RoomID == "" {
		return apierr.JSONInvalidParam(c)
	}
	if _, err := h.repo.FindRoomByID(req.RoomID); err != nil {
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_ROOM", "No such room.", "b3926861-29ef-4df6-98b5-a7c640ad2b5a"))
	}
	// UNIQUE制約違反を避けるため既存メンバーは冪等に204を返す
	if _, err := h.repo.FindMembership(user.ID, req.RoomID); err == nil {
		return c.NoContent(http.StatusNoContent)
	}
	mem := &model.ChatRoomMembership{
		ID: h.idGen.Generate(time.Now()), UserID: user.ID, RoomID: req.RoomID,
	}
	if err := h.repo.CreateMembership(mem); err != nil {
		return apierr.JSONInternalError(c)
	}
	return c.NoContent(http.StatusNoContent)
}

// RoomsJoining handles POST /api/chat/rooms/joining (TS-compatible alias for /joined).
func (h *Handler) RoomsJoining(c echo.Context) error {
	return h.RoomsJoined(c)
}

// RoomsMembers handles POST /api/chat/rooms/members.
func (h *Handler) RoomsMembers(c echo.Context) error {
	var req struct {
		RoomID string `json:"roomId"`
		Limit  int    `json:"limit"`
	}
	if err := c.Bind(&req); err != nil || req.RoomID == "" {
		return apierr.JSONInvalidParam(c)
	}
	members, err := h.repo.ListMembersByRoom(req.RoomID)
	if err != nil {
		return apierr.JSONInternalError(c)
	}
	out := make([]map[string]any, 0, len(members))
	for _, m := range members {
		entry := map[string]any{"id": m.ID, "userId": m.UserID, "roomId": m.RoomID, "isMuted": m.IsMuted}
		if m.User != nil {
			entry["user"] = packUser(m.User)
		}
		out = append(out, entry)
	}
	return c.JSON(http.StatusOK, out)
}

// --- Other ---

// History handles POST /api/chat/history.
//
// upstream `chat/history` は `room: bool` parameter を取り、true の時は
// chat ルーム内の最新メッセージ、false (デフォルト) の時は 1-on-1 DM の
// 最新メッセージを返す。FE の MkChatHistories.vue は両方を並行に呼んで
// マージするため、room パラメータを正しくハンドリングしないと chat home
// で何も描画されない (#692)。
func (h *Handler) History(c echo.Context) error {
	user := middleware.GetUser(c)
	var req struct {
		Limit int  `json:"limit"`
		Room  bool `json:"room"`
	}
	_ = c.Bind(&req)
	if req.Limit <= 0 {
		req.Limit = 10
	}
	if req.Limit > 100 {
		req.Limit = 100
	}
	var (
		msgs []*model.ChatMessage
		err  error
	)
	if req.Room {
		msgs, err = h.repo.ListRoomHistory(user.ID, req.Limit)
	} else {
		msgs, err = h.repo.ListUserHistory(user.ID, req.Limit)
	}
	if err != nil {
		return apierr.JSONInternalError(c)
	}
	out := make([]map[string]any, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, h.packMessageDetailed(m, user.ID))
	}
	return c.JSON(http.StatusOK, out)
}

// UnreadCount handles POST /api/chat/unread-count.
func (h *Handler) UnreadCount(c echo.Context) error {
	user := middleware.GetUser(c)
	count, _ := h.repo.CountUnread(user.ID)
	return c.JSON(http.StatusOK, map[string]any{"count": count})
}
