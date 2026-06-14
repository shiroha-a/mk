package testutil

import (
	"sort"
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

func (m *MockChatRepository) ListRoomsByOwner(ownerID string) ([]*model.ChatRoom, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*model.ChatRoom, 0)
	for _, r := range m.Rooms {
		if r.OwnerID == ownerID {
			out = append(out, r)
		}
	}
	return out, nil
}

func (m *MockChatRepository) ListJoinedRooms(userID string) ([]*model.ChatRoom, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*model.ChatRoom, 0)
	for _, mem := range m.Memberships {
		if mem.UserID == userID {
			if r, ok := m.Rooms[mem.RoomID]; ok {
				out = append(out, r)
			}
		}
	}
	return out, nil
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

func (m *MockChatRepository) ListMessagesByRoom(_ string, _ int) ([]*model.ChatMessage, error) {
	return nil, nil
}

func (m *MockChatRepository) ListMessagesByUser(_, _ string, _ int) ([]*model.ChatMessage, error) {
	return nil, nil
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

func (m *MockChatRepository) SearchMessages(_, _ string, _ int) ([]*model.ChatMessage, error) {
	return nil, nil
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

func (m *MockChatRepository) ListMembershipsByUser(userID string) ([]*model.ChatRoomMembership, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ListMembershipsErr != nil {
		return nil, m.ListMembershipsErr
	}
	out := make([]*model.ChatRoomMembership, 0)
	for _, mem := range m.Memberships {
		if mem.UserID == userID {
			cp := *mem
			if r, ok := m.Rooms[mem.RoomID]; ok {
				cp.Room = r
			}
			out = append(out, &cp)
		}
	}
	// upstream getMyMemberships は membership id 降順で返す。
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out, nil
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

func (m *MockChatRepository) ListInvitationsByUser(_ string, _ bool) ([]*model.ChatRoomInvitation, error) {
	return nil, nil
}

func (m *MockChatRepository) ListInvitationsByRoom(roomID string) ([]*model.ChatRoomInvitation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var rows []*model.ChatRoomInvitation
	for _, inv := range m.Invitations {
		if inv.RoomID == roomID {
			rows = append(rows, inv)
		}
	}
	return rows, nil
}

// --- Read tracking / unread count ---

func (m *MockChatRepository) CountUnread(_ string) (int64, error) { return 0, nil }

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

func (m *MockChatRepository) AddReaction(_, _ string) error    { return nil }
func (m *MockChatRepository) RemoveReaction(_, _ string) error { return nil }
