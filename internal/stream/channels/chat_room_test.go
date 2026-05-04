package channels

import (
	"encoding/json"
	"errors"
	"sync"
	"testing"

	corechat "github.com/shiroha-a/mk/internal/core/chat"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/stream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- minimal in-memory chat repo for channel wiring ---

type chatFakeRepo struct {
	mu          sync.Mutex
	rooms       map[string]*model.ChatRoom
	messages    map[string]*model.ChatMessage
	memberships map[string]*model.ChatRoomMembership
}

func newChatFakeRepo() *chatFakeRepo {
	return &chatFakeRepo{
		rooms:       make(map[string]*model.ChatRoom),
		messages:    make(map[string]*model.ChatMessage),
		memberships: make(map[string]*model.ChatRoomMembership),
	}
}

func (r *chatFakeRepo) CreateRoom(rm *model.ChatRoom) error { r.rooms[rm.ID] = rm; return nil }
func (r *chatFakeRepo) FindRoomByID(id string) (*model.ChatRoom, error) {
	if rm, ok := r.rooms[id]; ok {
		return rm, nil
	}
	return nil, errors.New("not found")
}
func (r *chatFakeRepo) UpdateRoom(_ *model.ChatRoom) error                   { return nil }
func (r *chatFakeRepo) DeleteRoom(_ string) error                            { return nil }
func (r *chatFakeRepo) ListRoomsByOwner(_ string) ([]*model.ChatRoom, error) { return nil, nil }
func (r *chatFakeRepo) ListJoinedRooms(_ string) ([]*model.ChatRoom, error)  { return nil, nil }
func (r *chatFakeRepo) CreateMessage(msg *model.ChatMessage) error {
	r.messages[msg.ID] = msg
	return nil
}
func (r *chatFakeRepo) FindMessageByID(id string) (*model.ChatMessage, error) {
	if m, ok := r.messages[id]; ok {
		return m, nil
	}
	return nil, errors.New("not found")
}
func (r *chatFakeRepo) UpdateMessage(_ *model.ChatMessage) error { return nil }
func (r *chatFakeRepo) DeleteMessage(_ string) error             { return nil }
func (r *chatFakeRepo) ListMessagesByRoom(_ string, _ int) ([]*model.ChatMessage, error) {
	return nil, nil
}
func (r *chatFakeRepo) ListMessagesByUser(_, _ string, _ int) ([]*model.ChatMessage, error) {
	return nil, nil
}
func (r *chatFakeRepo) SearchMessages(_, _ string, _ int) ([]*model.ChatMessage, error) {
	return nil, nil
}
func (r *chatFakeRepo) CreateMembership(m *model.ChatRoomMembership) error {
	r.memberships[m.UserID+":"+m.RoomID] = m
	return nil
}
func (r *chatFakeRepo) FindMembership(userID, roomID string) (*model.ChatRoomMembership, error) {
	if m, ok := r.memberships[userID+":"+roomID]; ok {
		return m, nil
	}
	return nil, errors.New("not found")
}
func (r *chatFakeRepo) UpdateMembership(_ *model.ChatRoomMembership) error { return nil }
func (r *chatFakeRepo) DeleteMembership(_, _ string) error                 { return nil }
func (r *chatFakeRepo) ListMembersByRoom(_ string) ([]*model.ChatRoomMembership, error) {
	return nil, nil
}
func (r *chatFakeRepo) CreateInvitation(_ *model.ChatRoomInvitation) error { return nil }
func (r *chatFakeRepo) DeleteInvitation(_ string) error                    { return nil }
func (r *chatFakeRepo) FindInvitation(_, _ string) (*model.ChatRoomInvitation, error) {
	return nil, errors.New("not found")
}
func (r *chatFakeRepo) CountUnread(_ string) (int64, error) { return 0, nil }
func (r *chatFakeRepo) MarkRead(_, _ string) error          { return nil }
func (r *chatFakeRepo) ListInvitationsByUser(_ string, _ bool) ([]*model.ChatRoomInvitation, error) {
	return nil, nil
}
func (r *chatFakeRepo) ListInvitationsByRoom(_ string) ([]*model.ChatRoomInvitation, error) {
	return nil, nil
}
func (r *chatFakeRepo) UpdateDeliveryStatus(_ string, _, _ bool) error { return nil }
func (r *chatFakeRepo) ListHistory(_ string, _ int) ([]*model.ChatMessage, error) {
	return nil, nil
}
func (r *chatFakeRepo) ListUserHistory(_ string, _ int) ([]*model.ChatMessage, error) {
	return nil, nil
}
func (r *chatFakeRepo) ListRoomHistory(_ string, _ int) ([]*model.ChatMessage, error) {
	return nil, nil
}
func (r *chatFakeRepo) MarkAllRead(_ string) error                         { return nil }
func (r *chatFakeRepo) MarkAllReadFromUser(_, _ string) error              { return nil }
func (r *chatFakeRepo) MarkAllReadInRoom(_, _ string) error                { return nil }
func (r *chatFakeRepo) AddReaction(_, _ string) error                      { return nil }
func (r *chatFakeRepo) RemoveReaction(_, _ string) error                   { return nil }
func (r *chatFakeRepo) UpdateInvitation(_ *model.ChatRoomInvitation) error { return nil }
func (r *chatFakeRepo) FindMessageByURI(_ string) (*model.ChatMessage, error) {
	return nil, errors.New("not found")
}

// --- test helpers ---

func newChatSvc(t *testing.T) (*corechat.Service, *chatFakeRepo) {
	t.Helper()
	repo := newChatFakeRepo()
	idGen, _ := id.NewGenerator("aidx")
	return corechat.NewService(repo, idGen), repo
}

// --- conformance ---

var _ stream.Channel = (*ChatRoomChannel)(nil)
var _ stream.Channel = (*ChatUserChannel)(nil)

// --- ChatRoom channel tests ---

func TestChatRoomChannel_Init_Member(t *testing.T) {
	svc, repo := newChatSvc(t)
	repo.rooms["r1"] = &model.ChatRoom{ID: "r1", OwnerID: "alice"}
	repo.memberships["bob:r1"] = &model.ChatRoomMembership{UserID: "bob", RoomID: "r1"}

	ctx := newCtx(&model.User{ID: "bob"})
	ch := NewChatRoomFactory(svc).New(ctx)
	ch.Init(json.RawMessage(`{"roomId":"r1"}`))

	assert.Equal(t, []string{"chatRoomStream:r1"}, ctx.subs)
}

func TestChatRoomChannel_Init_OwnerImplicitMember(t *testing.T) {
	svc, repo := newChatSvc(t)
	repo.rooms["r1"] = &model.ChatRoom{ID: "r1", OwnerID: "alice"}

	ctx := newCtx(&model.User{ID: "alice"})
	ch := NewChatRoomFactory(svc).New(ctx)
	ch.Init(json.RawMessage(`{"roomId":"r1"}`))

	assert.Equal(t, []string{"chatRoomStream:r1"}, ctx.subs)
}

func TestChatRoomChannel_Init_NotMember(t *testing.T) {
	svc, repo := newChatSvc(t)
	repo.rooms["r1"] = &model.ChatRoom{ID: "r1", OwnerID: "alice"}

	ctx := newCtx(&model.User{ID: "carol"})
	ch := NewChatRoomFactory(svc).New(ctx)
	err := ch.Init(json.RawMessage(`{"roomId":"r1"}`))

	assert.ErrorIs(t, err, stream.ErrInvalidParams)
	assert.Empty(t, ctx.subs, "non-member should not subscribe")
}

func TestChatRoomChannel_Init_Unauthenticated(t *testing.T) {
	svc, _ := newChatSvc(t)
	ctx := newCtx(nil)
	ch := NewChatRoomFactory(svc).New(ctx)
	err := ch.Init(json.RawMessage(`{"roomId":"r1"}`))
	assert.ErrorIs(t, err, stream.ErrInvalidParams)
	assert.Empty(t, ctx.subs)
}

func TestChatRoomChannel_Init_MissingRoomID(t *testing.T) {
	svc, _ := newChatSvc(t)
	ctx := newCtx(&model.User{ID: "alice"})
	ch := NewChatRoomFactory(svc).New(ctx)
	err := ch.Init(json.RawMessage(`{}`))
	assert.ErrorIs(t, err, stream.ErrInvalidParams)
	assert.Empty(t, ctx.subs)
}

func TestChatRoomChannel_Init_BadJSON(t *testing.T) {
	svc, _ := newChatSvc(t)
	ctx := newCtx(&model.User{ID: "alice"})
	ch := NewChatRoomFactory(svc).New(ctx)
	err := ch.Init(json.RawMessage(`not-json`))
	assert.ErrorIs(t, err, stream.ErrInvalidParams)
	assert.Empty(t, ctx.subs)
}

func TestChatRoomChannel_OnRedisEvent_Forwards(t *testing.T) {
	svc, repo := newChatSvc(t)
	repo.rooms["r1"] = &model.ChatRoom{ID: "r1", OwnerID: "alice"}
	ctx := newCtx(&model.User{ID: "alice"})
	ch := NewChatRoomFactory(svc).New(ctx)
	ch.Init(json.RawMessage(`{"roomId":"r1"}`))
	ch.OnRedisEvent([]byte(`{"type":"message","body":{"id":"m1"}}`))
	require.Len(t, ctx.sentType, 1)
	assert.Equal(t, "message", ctx.sentType[0])
}

func TestChatRoomChannel_OnRedisEvent_Invalid(t *testing.T) {
	svc, repo := newChatSvc(t)
	repo.rooms["r1"] = &model.ChatRoom{ID: "r1", OwnerID: "alice"}
	ctx := newCtx(&model.User{ID: "alice"})
	ch := NewChatRoomFactory(svc).New(ctx)
	ch.Init(json.RawMessage(`{"roomId":"r1"}`))
	ch.OnRedisEvent([]byte(`not-json`))
	assert.Empty(t, ctx.sentType)
}

func TestChatRoomChannel_OnRedisEvent_NoType(t *testing.T) {
	svc, repo := newChatSvc(t)
	repo.rooms["r1"] = &model.ChatRoom{ID: "r1", OwnerID: "alice"}
	ctx := newCtx(&model.User{ID: "alice"})
	ch := NewChatRoomFactory(svc).New(ctx)
	ch.Init(json.RawMessage(`{"roomId":"r1"}`))
	ch.OnRedisEvent([]byte(`{"body":{}}`))
	assert.Empty(t, ctx.sentType)
}

func TestChatRoomChannel_OnClientMessage_Read(t *testing.T) {
	svc, repo := newChatSvc(t)
	repo.rooms["r1"] = &model.ChatRoom{ID: "r1", OwnerID: "alice"}
	textStr := "hi"
	repo.messages["m1"] = &model.ChatMessage{
		ID: "m1", FromUserID: "alice", ToRoomID: strPtr("r1"), Text: &textStr,
	}
	ctx := newCtx(&model.User{ID: "alice"})
	ch := NewChatRoomFactory(svc).New(ctx)
	ch.Init(json.RawMessage(`{"roomId":"r1"}`))
	ch.OnClientMessage("read", json.RawMessage(`{"id":"m1"}`))
	// should not panic; we don't verify the side effect here (covered in service tests)
}

func TestChatRoomChannel_OnClientMessage_UnknownType(t *testing.T) {
	svc, repo := newChatSvc(t)
	repo.rooms["r1"] = &model.ChatRoom{ID: "r1", OwnerID: "alice"}
	ctx := newCtx(&model.User{ID: "alice"})
	ch := NewChatRoomFactory(svc).New(ctx)
	ch.Init(json.RawMessage(`{"roomId":"r1"}`))
	ch.OnClientMessage("bogus", json.RawMessage(`{}`))
	// silently ignored
}

func TestChatRoomChannel_OnClientMessage_ReadEmptyID(t *testing.T) {
	svc, repo := newChatSvc(t)
	repo.rooms["r1"] = &model.ChatRoom{ID: "r1", OwnerID: "alice"}
	ctx := newCtx(&model.User{ID: "alice"})
	ch := NewChatRoomFactory(svc).New(ctx)
	ch.Init(json.RawMessage(`{"roomId":"r1"}`))
	ch.OnClientMessage("read", json.RawMessage(`{"id":""}`))
}

func TestChatRoomChannel_OnClientMessage_ReadBadJSON(t *testing.T) {
	svc, repo := newChatSvc(t)
	repo.rooms["r1"] = &model.ChatRoom{ID: "r1", OwnerID: "alice"}
	ctx := newCtx(&model.User{ID: "alice"})
	ch := NewChatRoomFactory(svc).New(ctx)
	ch.Init(json.RawMessage(`{"roomId":"r1"}`))
	ch.OnClientMessage("read", json.RawMessage(`not-json`))
}

func TestChatRoomChannel_OnClientMessage_Unauthenticated(t *testing.T) {
	svc, _ := newChatSvc(t)
	ctx := newCtx(nil)
	ch := &ChatRoomChannel{ctx: ctx, svc: svc, roomID: "r1"}
	ch.OnClientMessage("read", json.RawMessage(`{"id":"m1"}`))
}

func TestChatRoomChannel_OnClientMessage_NilService(t *testing.T) {
	ctx := newCtx(&model.User{ID: "alice"})
	ch := &ChatRoomChannel{ctx: ctx, svc: nil, roomID: "r1"}
	ch.OnClientMessage("read", json.RawMessage(`{"id":"m1"}`))
}

func TestChatRoomChannel_OnClientMessage_ReadServiceError(t *testing.T) {
	svc, _ := newChatSvc(t)
	// message doesn't exist in repo → MarkReadByMessageID returns ErrNotFound
	ctx := newCtx(&model.User{ID: "alice"})
	ch := &ChatRoomChannel{ctx: ctx, svc: svc, roomID: "r1"}
	ch.OnClientMessage("read", json.RawMessage(`{"id":"ghost"}`))
}

func TestChatRoomChannel_Dispose(t *testing.T) {
	svc, repo := newChatSvc(t)
	repo.rooms["r1"] = &model.ChatRoom{ID: "r1", OwnerID: "alice"}
	ctx := newCtx(&model.User{ID: "alice"})
	ch := NewChatRoomFactory(svc).New(ctx)
	ch.Init(json.RawMessage(`{"roomId":"r1"}`))
	ch.Dispose()
	assert.Equal(t, []string{"chatRoomStream:r1"}, ctx.unsubs)
}

func TestChatRoomChannel_Dispose_WithoutInit(t *testing.T) {
	svc, _ := newChatSvc(t)
	ctx := newCtx(nil)
	ch := NewChatRoomFactory(svc).New(ctx)
	ch.Dispose() // no panic
	assert.Empty(t, ctx.unsubs)
}

// helper
func strPtr(s string) *string { return &s }
