package testutil

import (
	"sort"
	"strings"
	"sync"

	"github.com/shiroha-a/mk/internal/model"
)

// MockChatRepository is an in-memory implementation of repository.ChatRepository
// shared across api/chat, core/chat and stream/channels test packages (#709).
//
// 各 test ファイルで重複していた mockChatRepo / fakeChatRepo / chatFakeRepo
// を集約。ChatRepository に新メソッドを追加するたびに複数 stub に実装を生やす
// 苦痛を解消する。
//
// 設計の意図:
//   - 全 map 操作は内部 mutex 配下で行うため goroutine 並列 (例: ap_delivery
//     のジョブ送出) でも race にならない。
//   - メッセージ書き込み / 読み込みは clone して保持することで、呼び出し側が
//     返り値を mutate しても store 内が壊れないようにする。
//   - CreateErr / UpdateErr / DeleteErr で error path をテストできる。
//   - 各 List* / FindInvitation 等 filter 操作はテストで実際に必要だった
//     ものだけ実装している。残り (ListRoomsByOwner, ListMembersByRoom 等)
//     は記録された state を元に予測可能な順序で返す。
type MockChatRepository struct {
	mu sync.Mutex

	// Rooms keyed by room ID.
	Rooms map[string]*model.ChatRoom
	// Messages keyed by message ID. Stored as cloned copies.
	Messages map[string]*model.ChatMessage
	// Memberships keyed by "<userID>:<roomID>".
	Memberships map[string]*model.ChatRoomMembership
	// Invitations keyed by invitation ID.
	Invitations map[string]*model.ChatRoomInvitation
	// AddedReactions / RemovedReactions record the keys passed to
	// AddReaction / RemoveReaction so tests can assert canonicalisation (#2106 N4).
	AddedReactions   []string
	RemovedReactions []string

	// CreateErr forces Create* to return this error without persisting.
	CreateErr error
	// UpdateErr forces UpdateMessage to return this error without persisting.
	UpdateErr error
	// DeleteErr forces DeleteMessage to return this error without removing.
	DeleteErr error
	// ListMembershipsErr forces ListMembershipsByUser to return this error.
	ListMembershipsErr error
}

// NewMockChatRepository constructs an empty MockChatRepository ready for use.
func NewMockChatRepository() *MockChatRepository {
	return &MockChatRepository{
		Rooms:       make(map[string]*model.ChatRoom),
		Messages:    make(map[string]*model.ChatMessage),
		Memberships: make(map[string]*model.ChatRoomMembership),
		Invitations: make(map[string]*model.ChatRoomInvitation),
	}
}

func membershipKey(userID, roomID string) string { return userID + ":" + roomID }

// cursorKeep reports whether id passes the (sinceID, untilID) id-cursor used by
// the paginated chat list mocks (#1747)。
func cursorKeep(id, sinceID, untilID string) bool {
	if untilID != "" && id >= untilID {
		return false
	}
	if sinceID != "" && id <= sinceID {
		return false
	}
	return true
}

// clampMockLimit resolves limit<=0 to def and caps at 100 (upstream maximum)。
func clampMockLimit(limit, def int) int {
	if limit <= 0 {
		return def
	}
	if limit > 100 {
		return 100
	}
	return limit
}

// --- Rooms ---

func (m *MockChatRepository) CreateRoom(room *model.ChatRoom) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.CreateErr != nil {
		return m.CreateErr
	}
	m.Rooms[room.ID] = room
	return nil
}

func (m *MockChatRepository) FindRoomByID(id string) (*model.ChatRoom, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r, ok := m.Rooms[id]; ok {
		return r, nil
	}
	return nil, ErrNotFound
}

func (m *MockChatRepository) UpdateRoom(room *model.ChatRoom) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Rooms[room.ID] = room
	return nil
}

func (m *MockChatRepository) DeleteRoom(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.Rooms, id)
	return nil
}

func (m *MockChatRepository) ListRoomsByOwner(ownerID, sinceID, untilID string, limit int) ([]*model.ChatRoom, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*model.ChatRoom, 0)
	for _, r := range m.Rooms {
		if r.OwnerID == ownerID && cursorKeep(r.ID, sinceID, untilID) {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return capRooms(out, clampMockLimit(limit, 30)), nil
}

func (m *MockChatRepository) ListJoinedRooms(userID, sinceID, untilID string, limit int) ([]*model.ChatRoom, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*model.ChatRoom, 0)
	for _, mem := range m.Memberships {
		if mem.UserID == userID {
			if r, ok := m.Rooms[mem.RoomID]; ok && cursorKeep(r.ID, sinceID, untilID) {
				out = append(out, r)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return capRooms(out, clampMockLimit(limit, 30)), nil
}

func capRooms(rooms []*model.ChatRoom, limit int) []*model.ChatRoom {
	if len(rooms) > limit {
		return rooms[:limit]
	}
	return rooms
}

// --- Messages ---

func (m *MockChatRepository) CreateMessage(msg *model.ChatMessage) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.CreateErr != nil {
		return m.CreateErr
	}
	clone := *msg
	m.Messages[msg.ID] = &clone
	return nil
}

func (m *MockChatRepository) FindMessageByID(id string) (*model.ChatMessage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if msg, ok := m.Messages[id]; ok {
		clone := *msg
		return &clone, nil
	}
	return nil, ErrNotFound
}

func (m *MockChatRepository) FindMessageByURI(uri string) (*model.ChatMessage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, msg := range m.Messages {
		if msg.URI != nil && *msg.URI == uri {
			clone := *msg
			return &clone, nil
		}
	}
	return nil, ErrNotFound
}

func (m *MockChatRepository) UpdateMessage(msg *model.ChatMessage) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.UpdateErr != nil {
		return m.UpdateErr
	}
	clone := *msg
	m.Messages[msg.ID] = &clone
	return nil
}

func (m *MockChatRepository) DeleteMessage(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.DeleteErr != nil {
		return m.DeleteErr
	}
	delete(m.Messages, id)
	return nil
}

func (m *MockChatRepository) ListMessagesByRoom(roomID, sinceID, untilID string, limit int) ([]*model.ChatMessage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*model.ChatMessage
	for _, msg := range m.Messages {
		if msg.ToRoomID != nil && *msg.ToRoomID == roomID && cursorKeep(msg.ID, sinceID, untilID) {
			cp := *msg
			out = append(out, &cp)
		}
	}
	return capMessages(out, clampMockLimit(limit, 20)), nil
}

func (m *MockChatRepository) ListMessagesByUser(userID, otherUserID, sinceID, untilID string, limit int) ([]*model.ChatMessage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*model.ChatMessage
	for _, msg := range m.Messages {
		dm := (msg.FromUserID == userID && msg.ToUserID != nil && *msg.ToUserID == otherUserID) ||
			(msg.FromUserID == otherUserID && msg.ToUserID != nil && *msg.ToUserID == userID)
		if dm && cursorKeep(msg.ID, sinceID, untilID) {
			cp := *msg
			out = append(out, &cp)
		}
	}
	return capMessages(out, clampMockLimit(limit, 20)), nil
}

func capMessages(msgs []*model.ChatMessage, limit int) []*model.ChatMessage {
	sort.Slice(msgs, func(i, j int) bool { return msgs[i].ID > msgs[j].ID })
	if len(msgs) > limit {
		return msgs[:limit]
	}
	return msgs
}

func (m *MockChatRepository) ListMessagesByFileID(fileID, untilID, sinceID string, limit int) ([]*model.ChatMessage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if limit <= 0 {
		limit = 10
	}
	var out []*model.ChatMessage
	for _, msg := range m.Messages {
		if msg.FileID == nil || *msg.FileID != fileID {
			continue
		}
		if untilID != "" && msg.ID >= untilID {
			continue
		}
		if sinceID != "" && msg.ID <= sinceID {
			continue
		}
		out = append(out, msg)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *MockChatRepository) SearchMessages(meID, query string, limit int, userID, roomID string) ([]*model.ChatMessage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if limit <= 0 {
		limit = 10
	}
	q := strings.ToLower(query)
	var out []*model.ChatMessage
	for _, msg := range m.Messages {
		if msg.Text == nil || !strings.Contains(strings.ToLower(*msg.Text), q) {
			continue
		}
		match := false
		switch {
		case userID != "":
			match = (msg.FromUserID == meID && msg.ToUserID != nil && *msg.ToUserID == userID) ||
				(msg.FromUserID == userID && msg.ToUserID != nil && *msg.ToUserID == meID)
		case roomID != "":
			match = msg.ToRoomID != nil && *msg.ToRoomID == roomID
		default:
			switch {
			case msg.FromUserID == meID:
				match = true
			case msg.ToUserID != nil && *msg.ToUserID == meID:
				match = true
			case msg.ToRoomID != nil:
				if _, ok := m.Memberships[membershipKey(meID, *msg.ToRoomID)]; ok {
					match = true
				} else if r, ok := m.Rooms[*msg.ToRoomID]; ok && r.OwnerID == meID {
					match = true
				}
			}
		}
		if match {
			cp := *msg
			out = append(out, &cp)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// --- Memberships ---

func (m *MockChatRepository) CreateMembership(mem *model.ChatRoomMembership) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Memberships[membershipKey(mem.UserID, mem.RoomID)] = mem
	return nil
}

func (m *MockChatRepository) FindMembership(userID, roomID string) (*model.ChatRoomMembership, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if mem, ok := m.Memberships[membershipKey(userID, roomID)]; ok {
		return mem, nil
	}
	return nil, ErrNotFound
}

func (m *MockChatRepository) UpdateMembership(mem *model.ChatRoomMembership) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Memberships[membershipKey(mem.UserID, mem.RoomID)] = mem
	return nil
}

func (m *MockChatRepository) DeleteMembership(userID, roomID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.Memberships, membershipKey(userID, roomID))
	return nil
}

func (m *MockChatRepository) ListMembersByRoom(roomID string) ([]*model.ChatRoomMembership, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*model.ChatRoomMembership, 0)
	for _, mem := range m.Memberships {
		if mem.RoomID == roomID {
			out = append(out, mem)
		}
	}
	return out, nil
}

func (m *MockChatRepository) ListMembersByRoomPaged(roomID, sinceID, untilID string, limit int) ([]*model.ChatRoomMembership, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*model.ChatRoomMembership, 0)
	for _, mem := range m.Memberships {
		if mem.RoomID == roomID && cursorKeep(mem.ID, sinceID, untilID) {
			out = append(out, mem)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return capMemberships(out, clampMockLimit(limit, 30)), nil
}

func (m *MockChatRepository) ListMembershipsByUser(userID, sinceID, untilID string, limit int) ([]*model.ChatRoomMembership, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ListMembershipsErr != nil {
		return nil, m.ListMembershipsErr
	}
	out := make([]*model.ChatRoomMembership, 0)
	for _, mem := range m.Memberships {
		if mem.UserID == userID && cursorKeep(mem.ID, sinceID, untilID) {
			cp := *mem
			if r, ok := m.Rooms[mem.RoomID]; ok {
				cp.Room = r
			}
			out = append(out, &cp)
		}
	}
	// upstream getMyMemberships は membership id 降順で返す。
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return capMemberships(out, clampMockLimit(limit, 30)), nil
}

func capMemberships(rows []*model.ChatRoomMembership, limit int) []*model.ChatRoomMembership {
	if len(rows) > limit {
		return rows[:limit]
	}
	return rows
}

// --- Invitations ---

func (m *MockChatRepository) CreateInvitation(inv *model.ChatRoomInvitation) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.CreateErr != nil {
		return m.CreateErr
	}
	m.Invitations[inv.ID] = inv
	return nil
}

func (m *MockChatRepository) DeleteInvitation(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.Invitations, id)
	return nil
}

func (m *MockChatRepository) UpdateInvitation(inv *model.ChatRoomInvitation) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Invitations[inv.ID] = inv
	return nil
}

func (m *MockChatRepository) FindInvitationByID(id string) (*model.ChatRoomInvitation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if inv, ok := m.Invitations[id]; ok {
		return inv, nil
	}
	return nil, ErrNotFound
}

func (m *MockChatRepository) FindInvitation(userID, roomID string) (*model.ChatRoomInvitation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, inv := range m.Invitations {
		if inv.UserID == userID && inv.RoomID == roomID {
			return inv, nil
		}
	}
	return nil, ErrNotFound
}

func (m *MockChatRepository) ListInvitationsByUser(_ string, _ bool, _, _ string, _ int) ([]*model.ChatRoomInvitation, error) {
	return nil, nil
}

func (m *MockChatRepository) ListInvitationsByRoom(roomID, sinceID, untilID string, limit int) ([]*model.ChatRoomInvitation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var rows []*model.ChatRoomInvitation
	for _, inv := range m.Invitations {
		if inv.RoomID == roomID && cursorKeep(inv.ID, sinceID, untilID) {
			rows = append(rows, inv)
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID > rows[j].ID })
	if l := clampMockLimit(limit, 30); len(rows) > l {
		rows = rows[:l]
	}
	return rows, nil
}

// --- Read tracking / unread count ---

func (m *MockChatRepository) CountUnread(_ string) (int64, error) { return 0, nil }

// HasUnreadFromUser reports whether readerID has an unread 1-on-1 message from
// otherID (readerID not in reads). Mirrors the production WHERE clause.
func (m *MockChatRepository) HasUnreadFromUser(readerID, otherID string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, msg := range m.Messages {
		if msg.FromUserID == otherID && msg.ToUserID != nil && *msg.ToUserID == readerID && !readsContains(msg.Reads, readerID) {
			return true, nil
		}
	}
	return false, nil
}

// HasUnreadInRoom reports whether readerID has an unread room message (authored
// by someone else) in roomID.
func (m *MockChatRepository) HasUnreadInRoom(readerID, roomID string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, msg := range m.Messages {
		if msg.ToRoomID != nil && *msg.ToRoomID == roomID && msg.FromUserID != readerID && !readsContains(msg.Reads, readerID) {
			return true, nil
		}
	}
	return false, nil
}

func readsContains(reads []string, id string) bool {
	for _, r := range reads {
		if r == id {
			return true
		}
	}
	return false
}

// MarkRead appends userID to the message's reads slice (idempotency は real
// impl 側のクエリで担保される。テスト用なので重複防止はしない)。
func (m *MockChatRepository) MarkRead(userID, messageID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if msg, ok := m.Messages[messageID]; ok {
		msg.Reads = append(msg.Reads, userID)
	}
	return nil
}

func (m *MockChatRepository) MarkAllRead(_ string) error            { return nil }
func (m *MockChatRepository) MarkAllReadFromUser(_, _ string) error { return nil }
func (m *MockChatRepository) MarkAllReadInRoom(_, _ string) error   { return nil }

// --- Delivery status / history ---

func (m *MockChatRepository) UpdateDeliveryStatus(_ string, _, _ bool) error { return nil }

func (m *MockChatRepository) ListHistory(_ string, _ int) ([]*model.ChatMessage, error) {
	return nil, nil
}

func (m *MockChatRepository) ListUserHistory(_ string, _ int) ([]*model.ChatMessage, error) {
	return nil, nil
}

func (m *MockChatRepository) ListRoomHistory(_ string, _ int) ([]*model.ChatMessage, error) {
	return nil, nil
}

// --- Reactions ---

func (m *MockChatRepository) AddReaction(_, key string) error {
	m.AddedReactions = append(m.AddedReactions, key)
	return nil
}

func (m *MockChatRepository) RemoveReaction(_, key string) error {
	m.RemovedReactions = append(m.RemovedReactions, key)
	return nil
}
