package chat

import (
	"errors"
	"log/slog"
	"net/http"
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
}

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

func packRoom(r *model.ChatRoom) map[string]any {
	// upstream Misskey TS の packedChatRoomSchema は `isArchived` を返さない。
	// mk-go も互換のため field を出力しない (#851)。archived state は別経路で
	// 取得する設計を踏襲する。
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
func packMessage(m *model.ChatMessage) map[string]any {
	result := map[string]any{
		"id":         m.ID,
		"fromUserId": m.FromUserID,
		"reads":      m.Reads,
		"reactions":  m.Reactions,
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

// packMessageWithCreatedAt augments packMessage with createdAt parsed from the
// message ID. createdAt 欠落だと FE 側の MkTime が `Invalid Date` を表示し、
// 一覧では NaN/NaN になる (#692)。
func (h *Handler) packMessageWithCreatedAt(m *model.ChatMessage) map[string]any {
	result := packMessage(m)
	if h.idGen != nil {
		if t, err := h.idGen.ParseTime(m.ID); err == nil {
			result["createdAt"] = t.UTC().Format("2006-01-02T15:04:05.000Z")
		}
	}
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
func (h *Handler) packMessageDetailed(m *model.ChatMessage) map[string]any {
	result := h.packMessageWithCreatedAt(m)
	if m.ToUser != nil {
		result["toUser"] = packUser(m.ToUser)
	}
	if m.ToRoom != nil {
		result["toRoom"] = packRoom(m.ToRoom)
		// upstream の `'room' in m` 判定に合わせて `room` alias も入れる。
		// 旧 chat 実装ではこの key で判定していた残存 client がある。
		result["room"] = packRoom(m.ToRoom)
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
	return c.JSON(http.StatusOK, packRoom(room))
}

// RoomsShow handles POST /api/chat/rooms/show.
func (h *Handler) RoomsShow(c echo.Context) error {
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
	return c.JSON(http.StatusOK, packRoom(room))
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
	return c.JSON(http.StatusOK, packRoom(room))
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
		result[i] = packRoom(r)
	}
	return c.JSON(http.StatusOK, result)
}

// RoomsJoined handles POST /api/chat/rooms/joined.
func (h *Handler) RoomsJoined(c echo.Context) error {
	user := middleware.GetUser(c)
	rooms, _ := h.repo.ListJoinedRooms(user.ID)
	result := make([]map[string]any, len(rooms))
	for i, r := range rooms {
		result[i] = packRoom(r)
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
	return c.JSON(http.StatusOK, h.packMessageWithCreatedAt(msg))
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
	return c.JSON(http.StatusOK, h.packMessageWithCreatedAt(msg))
}

// MessagesShow handles POST /api/chat/messages/show.
func (h *Handler) MessagesShow(c echo.Context) error {
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
	return c.JSON(http.StatusOK, h.packMessageWithCreatedAt(msg))
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
		result[i] = h.packMessageWithCreatedAt(m)
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
		result[i] = h.packMessageWithCreatedAt(m)
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
	var req struct {
		RoomID string `json:"roomId"`
		UserID string `json:"userId"`
	}
	if err := c.Bind(&req); err != nil || req.RoomID == "" || req.UserID == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "roomId and userId are required.", "ed1d7571-a3ac-4370-899c-0dbe5e230cc8"))
	}
	inv := &model.ChatRoomInvitation{
		ID: h.idGen.Generate(time.Now()), UserID: req.UserID, RoomID: req.RoomID,
	}
	if err := h.repo.CreateInvitation(inv); err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
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
	// メンバーシップを作成
	m := &model.ChatRoomMembership{
		ID: h.idGen.Generate(time.Now()), UserID: user.ID, RoomID: req.RoomID,
	}
	_ = h.repo.CreateMembership(m)
	// 招待を削除
	if inv, err := h.repo.FindInvitation(user.ID, req.RoomID); err == nil {
		_ = h.repo.DeleteInvitation(inv.ID)
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
	if inv, err := h.repo.FindInvitation(user.ID, req.RoomID); err == nil {
		_ = h.repo.DeleteInvitation(inv.ID)
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
		out = append(out, h.packMessageWithCreatedAt(m))
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
		out = append(out, h.packMessageWithCreatedAt(m))
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
			entry["room"] = packRoom(inv.Room)
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
		out = append(out, h.packMessageDetailed(m))
	}
	return c.JSON(http.StatusOK, out)
}

// UnreadCount handles POST /api/chat/unread-count.
func (h *Handler) UnreadCount(c echo.Context) error {
	user := middleware.GetUser(c)
	count, _ := h.repo.CountUnread(user.ID)
	return c.JSON(http.StatusOK, map[string]any{"count": count})
}
