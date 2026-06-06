package repository

import (
	"github.com/shiroha-a/mk/internal/model"
	"gorm.io/gorm"
)

// ChatRepository handles chat-related persistence.
type ChatRepository interface {
	// Room operations
	CreateRoom(room *model.ChatRoom) error
	FindRoomByID(id string) (*model.ChatRoom, error)
	UpdateRoom(room *model.ChatRoom) error
	DeleteRoom(id string) error
	ListRoomsByOwner(ownerID string) ([]*model.ChatRoom, error)
	ListJoinedRooms(userID string) ([]*model.ChatRoom, error)

	// Message operations
	CreateMessage(msg *model.ChatMessage) error
	FindMessageByID(id string) (*model.ChatMessage, error)
	FindMessageByURI(uri string) (*model.ChatMessage, error)
	UpdateMessage(msg *model.ChatMessage) error
	DeleteMessage(id string) error
	ListMessagesByRoom(roomID string, limit int) ([]*model.ChatMessage, error)
	ListMessagesByUser(userID, otherUserID string, limit int) ([]*model.ChatMessage, error)
	// ListMessagesByFileID returns chat messages that attached the given drive
	// file, newest-first, with id-cursor pagination. Used by
	// drive/files/attached-chat-messages.
	ListMessagesByFileID(fileID, untilID, sinceID string, limit int) ([]*model.ChatMessage, error)
	SearchMessages(userID, query string, limit int) ([]*model.ChatMessage, error)

	// Membership operations
	CreateMembership(m *model.ChatRoomMembership) error
	FindMembership(userID, roomID string) (*model.ChatRoomMembership, error)
	UpdateMembership(m *model.ChatRoomMembership) error
	DeleteMembership(userID, roomID string) error
	ListMembersByRoom(roomID string) ([]*model.ChatRoomMembership, error)
	ListMembershipsByUser(userID string) ([]*model.ChatRoomMembership, error)

	// Invitation operations
	CreateInvitation(inv *model.ChatRoomInvitation) error
	DeleteInvitation(id string) error
	FindInvitation(userID, roomID string) (*model.ChatRoomInvitation, error)
	UpdateInvitation(inv *model.ChatRoomInvitation) error

	// Unread count
	CountUnread(userID string) (int64, error)

	// Mark read
	MarkRead(userID, messageID string) error

	// Invitation listing
	ListInvitationsByUser(userID string, ignored bool) ([]*model.ChatRoomInvitation, error)
	ListInvitationsByRoom(roomID string) ([]*model.ChatRoomInvitation, error)

	// UpdateDeliveryStatus sets the AP delivery flags on a chat message.
	UpdateDeliveryStatus(messageID string, delivering, failed bool) error

	// ListHistory returns the most recent message per conversation (DM + room)
	// involving userID, ordered by recency.
	ListHistory(userID string, limit int) ([]*model.ChatMessage, error)

	// ListUserHistory is the DM-only variant: 1-on-1 conversations only,
	// rooms excluded. Mirrors upstream ChatService.userHistory (#692)。
	ListUserHistory(userID string, limit int) ([]*model.ChatMessage, error)

	// ListRoomHistory is the room-only variant: per-room latest message,
	// for rooms the user owns or has membership in. Mirrors upstream
	// ChatService.roomHistory (#692)。
	ListRoomHistory(userID string, limit int) ([]*model.ChatMessage, error)

	// Mark all as read
	MarkAllRead(userID string) error

	// MarkAllReadFromUser bulk-marks all DM messages from `fromUserID` to
	// `readerID` as read (appends readerID to `reads`). Used by user-timeline
	// to clear the unread badge on chat open (#692, upstream
	// ChatService.readUserChatMessage 相当)。
	MarkAllReadFromUser(readerID, fromUserID string) error

	// MarkAllReadInRoom bulk-marks all room messages in `roomID` (excluding
	// reader's own) as read. Used by room-timeline (#692, upstream
	// ChatService.readRoomChatMessage 相当)。
	MarkAllReadInRoom(readerID, roomID string) error

	// Reactions
	AddReaction(messageID, reaction string) error
	RemoveReaction(messageID, reaction string) error
}

type chatRepository struct {
	db *gorm.DB
}

// NewChatRepository creates a new ChatRepository.
func NewChatRepository(db *gorm.DB) ChatRepository {
	return &chatRepository{db: db}
}

func (r *chatRepository) CreateRoom(room *model.ChatRoom) error {
	return r.db.Create(room).Error
}

func (r *chatRepository) FindRoomByID(id string) (*model.ChatRoom, error) {
	var room model.ChatRoom
	if err := r.db.Preload("Owner").Where(`"id" = ?`, id).First(&room).Error; err != nil {
		return nil, err
	}
	return &room, nil
}

func (r *chatRepository) UpdateRoom(room *model.ChatRoom) error {
	return r.db.Save(room).Error
}

func (r *chatRepository) DeleteRoom(id string) error {
	return r.db.Where(`"id" = ?`, id).Delete(&model.ChatRoom{}).Error
}

func (r *chatRepository) ListRoomsByOwner(ownerID string) ([]*model.ChatRoom, error) {
	var rooms []*model.ChatRoom
	if err := r.db.Where(`"ownerId" = ?`, ownerID).Order(`"id" DESC`).Find(&rooms).Error; err != nil {
		return nil, err
	}
	return rooms, nil
}

func (r *chatRepository) ListJoinedRooms(userID string) ([]*model.ChatRoom, error) {
	var rooms []*model.ChatRoom
	if err := r.db.Joins(`JOIN "chat_room_membership" ON "chat_room_membership"."roomId" = "chat_room"."id"`).
		Where(`"chat_room_membership"."userId" = ?`, userID).
		Order(`"chat_room"."id" DESC`).Find(&rooms).Error; err != nil {
		return nil, err
	}
	return rooms, nil
}

func (r *chatRepository) CreateMessage(msg *model.ChatMessage) error {
	return r.db.Create(msg).Error
}

func (r *chatRepository) FindMessageByID(id string) (*model.ChatMessage, error) {
	var msg model.ChatMessage
	if err := r.db.Preload("FromUser").Preload("File").Where(`"id" = ?`, id).First(&msg).Error; err != nil {
		return nil, err
	}
	return &msg, nil
}

func (r *chatRepository) FindMessageByURI(uri string) (*model.ChatMessage, error) {
	var msg model.ChatMessage
	if err := r.db.Preload("FromUser").Preload("File").Where(`"uri" = ?`, uri).First(&msg).Error; err != nil {
		return nil, err
	}
	return &msg, nil
}

func (r *chatRepository) UpdateMessage(msg *model.ChatMessage) error {
	return r.db.Save(msg).Error
}

func (r *chatRepository) DeleteMessage(id string) error {
	return r.db.Where(`"id" = ?`, id).Delete(&model.ChatMessage{}).Error
}

func (r *chatRepository) ListMessagesByRoom(roomID string, limit int) ([]*model.ChatMessage, error) {
	if limit <= 0 {
		limit = 20
	}
	var msgs []*model.ChatMessage
	if err := r.db.Preload("FromUser").Preload("File").Where(`"toRoomId" = ?`, roomID).
		Order(`"id" DESC`).Limit(limit).Find(&msgs).Error; err != nil {
		return nil, err
	}
	return msgs, nil
}

func (r *chatRepository) ListMessagesByFileID(fileID, untilID, sinceID string, limit int) ([]*model.ChatMessage, error) {
	if limit <= 0 {
		limit = 10
	}
	q := r.db.Preload("FromUser").Preload("ToUser").Preload("ToRoom").Preload("File").
		Where(`"fileId" = ?`, fileID)
	if untilID != "" {
		q = q.Where(`"id" < ?`, untilID)
	}
	if sinceID != "" {
		q = q.Where(`"id" > ?`, sinceID)
	}
	var msgs []*model.ChatMessage
	if err := q.Order(`"id" DESC`).Limit(limit).Find(&msgs).Error; err != nil {
		return nil, err
	}
	return msgs, nil
}

func (r *chatRepository) ListMessagesByUser(userID, otherUserID string, limit int) ([]*model.ChatMessage, error) {
	if limit <= 0 {
		limit = 20
	}
	var msgs []*model.ChatMessage
	if err := r.db.Preload("FromUser").Preload("File").
		Where(`("fromUserId" = ? AND "toUserId" = ?) OR ("fromUserId" = ? AND "toUserId" = ?)`,
			userID, otherUserID, otherUserID, userID).
		Order(`"id" DESC`).Limit(limit).Find(&msgs).Error; err != nil {
		return nil, err
	}
	return msgs, nil
}

func (r *chatRepository) SearchMessages(userID, query string, limit int) ([]*model.ChatMessage, error) {
	if limit <= 0 {
		limit = 20
	}
	var msgs []*model.ChatMessage
	if err := r.db.Preload("FromUser").Preload("File").
		Where(`("fromUserId" = ? OR "toUserId" = ?) AND "text" ILIKE ?`, userID, userID, "%"+query+"%").
		Order(`"id" DESC`).Limit(limit).Find(&msgs).Error; err != nil {
		return nil, err
	}
	return msgs, nil
}

func (r *chatRepository) CreateMembership(m *model.ChatRoomMembership) error {
	return r.db.Create(m).Error
}

func (r *chatRepository) FindMembership(userID, roomID string) (*model.ChatRoomMembership, error) {
	var m model.ChatRoomMembership
	if err := r.db.Where(`"userId" = ? AND "roomId" = ?`, userID, roomID).First(&m).Error; err != nil {
		return nil, err
	}
	return &m, nil
}

func (r *chatRepository) UpdateMembership(m *model.ChatRoomMembership) error {
	return r.db.Save(m).Error
}

func (r *chatRepository) DeleteMembership(userID, roomID string) error {
	return r.db.Where(`"userId" = ? AND "roomId" = ?`, userID, roomID).Delete(&model.ChatRoomMembership{}).Error
}

func (r *chatRepository) ListMembersByRoom(roomID string) ([]*model.ChatRoomMembership, error) {
	var members []*model.ChatRoomMembership
	if err := r.db.Preload("User").Where(`"roomId" = ?`, roomID).Find(&members).Error; err != nil {
		return nil, err
	}
	return members, nil
}

func (r *chatRepository) ListMembershipsByUser(userID string) ([]*model.ChatRoomMembership, error) {
	var rows []*model.ChatRoomMembership
	if err := r.db.Preload("Room").Preload("Room.Owner").Where(`"userId" = ?`, userID).
		Order(`"id" DESC`).Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *chatRepository) CreateInvitation(inv *model.ChatRoomInvitation) error {
	return r.db.Create(inv).Error
}

func (r *chatRepository) DeleteInvitation(id string) error {
	return r.db.Where(`"id" = ?`, id).Delete(&model.ChatRoomInvitation{}).Error
}

func (r *chatRepository) UpdateInvitation(inv *model.ChatRoomInvitation) error {
	return r.db.Save(inv).Error
}

func (r *chatRepository) FindInvitation(userID, roomID string) (*model.ChatRoomInvitation, error) {
	var inv model.ChatRoomInvitation
	if err := r.db.Where(`"userId" = ? AND "roomId" = ?`, userID, roomID).First(&inv).Error; err != nil {
		return nil, err
	}
	return &inv, nil
}

func (r *chatRepository) CountUnread(userID string) (int64, error) {
	var count int64
	if err := r.db.Model(&model.ChatMessage{}).
		Where(`"toUserId" = ? AND NOT ("reads" @> ARRAY[?]::varchar[])`, userID, userID).
		Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

func (r *chatRepository) MarkRead(userID, messageID string) error {
	return r.db.Exec(`UPDATE "chat_message" SET "reads" = array_append("reads", ?) WHERE "id" = ? AND NOT ("reads" @> ARRAY[?]::varchar[])`,
		userID, messageID, userID).Error
}

func (r *chatRepository) ListInvitationsByUser(userID string, ignored bool) ([]*model.ChatRoomInvitation, error) {
	var rows []*model.ChatRoomInvitation
	if err := r.db.Preload("Room").Where(`"userId" = ? AND "ignored" = ?`, userID, ignored).
		Order("id DESC").Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *chatRepository) ListInvitationsByRoom(roomID string) ([]*model.ChatRoomInvitation, error) {
	var rows []*model.ChatRoomInvitation
	if err := r.db.Preload("User").Where(`"roomId" = ?`, roomID).
		Order("id DESC").Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *chatRepository) UpdateDeliveryStatus(messageID string, delivering, failed bool) error {
	return r.db.Model(&model.ChatMessage{}).Where("id = ?", messageID).
		Updates(map[string]any{"isDelivering": delivering, "isDeliverFailed": failed}).Error
}

// ListHistory returns the most recent message per conversation (1-on-1 or
// room) involving userID, using DISTINCT ON to pick one row per conversation.
func (r *chatRepository) ListHistory(userID string, limit int) ([]*model.ChatMessage, error) {
	if limit <= 0 {
		limit = 10
	}
	if limit > 100 {
		limit = 100
	}
	// 1対1 (toUserId/fromUserId) とルーム (toRoomId経由のmembership) をUNION
	// し、会話keyごとの最新メッセージを取る。
	// GORMの.Limit()は.Raw()の後では無視されるため、SQL内にLIMITを埋め込む。
	// DISTINCT ONはconversation_keyの昇順を強制するため、そのままLIMITを
	// 掛けるとアルファベット順で先頭N件が返ってしまう。外側のサブクエリで
	// id DESCに並べ直してからLIMITすることで最新のN会話を取得する。
	// Raw SQLではPreloadが使えないため、2段階で取得する:
	// 1) Raw SQLで対象メッセージIDを取得
	// 2) 通常のGORMクエリでPreload("FromUser")付きで再取得
	// ルーム所有者はmembershipレコードなしでも暗黙メンバーとして扱う
	// (IsRoomMemberと同じセマンティクス)。EXISTS句でmembership OR owner を判定。
	var ids []string
	err := r.db.Raw(`
		SELECT id FROM (
			SELECT DISTINCT ON (conversation_key) *
			FROM (
				SELECT *, LEAST("fromUserId","toUserId") || '-' || GREATEST("fromUserId","toUserId") AS conversation_key
				FROM "chat_message"
				WHERE ("fromUserId" = ? OR "toUserId" = ?) AND "toRoomId" IS NULL
				UNION ALL
				SELECT cm.*, 'room:' || cm."toRoomId" AS conversation_key
				FROM "chat_message" cm
				WHERE cm."toRoomId" IS NOT NULL
				AND (
					EXISTS (SELECT 1 FROM "chat_room_membership" m WHERE m."roomId" = cm."toRoomId" AND m."userId" = ?)
					OR EXISTS (SELECT 1 FROM "chat_room" cr WHERE cr."id" = cm."toRoomId" AND cr."ownerId" = ?)
				)
			) sub
			ORDER BY conversation_key, id DESC
		) conversations
		ORDER BY id DESC
		LIMIT ?
	`, userID, userID, userID, userID, limit).Pluck("id", &ids).Error
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}
	var msgs []*model.ChatMessage
	if err := r.db.Preload("FromUser").Preload("ToUser").Preload("ToRoom").Preload("File").Where("id IN ?", ids).Order("id DESC").Find(&msgs).Error; err != nil {
		return nil, err
	}
	return msgs, nil
}

// ListUserHistory returns the latest DM (1-on-1) message per peer, ordered
// by recency. Mirrors upstream ChatService.userHistory (#692)。room messages
// (toRoomId IS NOT NULL) は対象外。
func (r *chatRepository) ListUserHistory(userID string, limit int) ([]*model.ChatMessage, error) {
	if limit <= 0 {
		limit = 10
	}
	if limit > 100 {
		limit = 100
	}
	var ids []string
	err := r.db.Raw(`
		SELECT id FROM (
			SELECT DISTINCT ON (conversation_key) *
			FROM (
				SELECT *, LEAST("fromUserId","toUserId") || '-' || GREATEST("fromUserId","toUserId") AS conversation_key
				FROM "chat_message"
				WHERE ("fromUserId" = ? OR "toUserId" = ?) AND "toRoomId" IS NULL
			) sub
			ORDER BY conversation_key, id DESC
		) conversations
		ORDER BY id DESC
		LIMIT ?
	`, userID, userID, limit).Pluck("id", &ids).Error
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}
	var msgs []*model.ChatMessage
	if err := r.db.Preload("FromUser").Preload("ToUser").Preload("File").Where("id IN ?", ids).Order("id DESC").Find(&msgs).Error; err != nil {
		return nil, err
	}
	return msgs, nil
}

// ListRoomHistory returns the latest message per room that userID owns or
// is a member of. Mirrors upstream ChatService.roomHistory (#692)。
func (r *chatRepository) ListRoomHistory(userID string, limit int) ([]*model.ChatMessage, error) {
	if limit <= 0 {
		limit = 10
	}
	if limit > 100 {
		limit = 100
	}
	var ids []string
	err := r.db.Raw(`
		SELECT id FROM (
			SELECT DISTINCT ON ("toRoomId") cm.*
			FROM "chat_message" cm
			WHERE cm."toRoomId" IS NOT NULL
			AND (
				EXISTS (SELECT 1 FROM "chat_room_membership" m WHERE m."roomId" = cm."toRoomId" AND m."userId" = ?)
				OR EXISTS (SELECT 1 FROM "chat_room" cr WHERE cr."id" = cm."toRoomId" AND cr."ownerId" = ?)
			)
			ORDER BY "toRoomId", cm.id DESC
		) conversations
		ORDER BY id DESC
		LIMIT ?
	`, userID, userID, limit).Pluck("id", &ids).Error
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}
	var msgs []*model.ChatMessage
	if err := r.db.Preload("FromUser").Preload("ToRoom").Preload("File").Where("id IN ?", ids).Order("id DESC").Find(&msgs).Error; err != nil {
		return nil, err
	}
	return msgs, nil
}

func (r *chatRepository) MarkAllRead(userID string) error {
	return r.db.Exec(`UPDATE "chat_message" SET "reads" = array_append("reads", ?) WHERE "toUserId" = ? AND NOT ("reads" @> ARRAY[?]::varchar[])`,
		userID, userID, userID).Error
}

// MarkAllReadFromUser appends readerID to `reads` for every unread DM
// where fromUserID -> readerID. Idempotent via NOT (reads @> ...) guard.
func (r *chatRepository) MarkAllReadFromUser(readerID, fromUserID string) error {
	return r.db.Exec(
		`UPDATE "chat_message" SET "reads" = array_append("reads", ?) WHERE "fromUserId" = ? AND "toUserId" = ? AND NOT ("reads" @> ARRAY[?]::varchar[])`,
		readerID, fromUserID, readerID, readerID,
	).Error
}

// MarkAllReadInRoom appends readerID to `reads` for every unread room
// message in roomID, except messages authored by readerID itself.
func (r *chatRepository) MarkAllReadInRoom(readerID, roomID string) error {
	return r.db.Exec(
		`UPDATE "chat_message" SET "reads" = array_append("reads", ?) WHERE "toRoomId" = ? AND "fromUserId" <> ? AND NOT ("reads" @> ARRAY[?]::varchar[])`,
		readerID, roomID, readerID, readerID,
	).Error
}

func (r *chatRepository) AddReaction(messageID, reaction string) error {
	// MarkReadと同じく重複ガード付き。同一reactionの二重追加を防ぐ。
	return r.db.Exec(`UPDATE "chat_message" SET "reactions" = array_append("reactions", ?) WHERE "id" = ? AND NOT ("reactions" @> ARRAY[?]::varchar[])`,
		reaction, messageID, reaction).Error
}

func (r *chatRepository) RemoveReaction(messageID, reaction string) error {
	return r.db.Exec(`UPDATE "chat_message" SET "reactions" = array_remove("reactions", ?) WHERE "id" = ?`,
		reaction, messageID).Error
}
