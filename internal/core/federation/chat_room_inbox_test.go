package federation_test

import (
	"errors"
	"testing"

	"github.com/shiroha-a/mk/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeChatRoomReceiver records inbound chat room federation calls.
type fakeChatRoomReceiver struct {
	ensureCalls [][4]string // roomID, name, summary, ownerUserID
	inviteCalls [][2]string // roomID, inviteeUserID
	memberCalls [][2]string // roomID, userID
	removeCalls [][2]string // roomID, userID
	ensureErr   error
	inviteErr   error
}

func (f *fakeChatRoomReceiver) EnsureRoomViaAP(roomID, name, summary, ownerUserID string) error {
	f.ensureCalls = append(f.ensureCalls, [4]string{roomID, name, summary, ownerUserID})
	return f.ensureErr
}

func (f *fakeChatRoomReceiver) CreateInvitationViaAP(roomID, inviteeUserID string) error {
	f.inviteCalls = append(f.inviteCalls, [2]string{roomID, inviteeUserID})
	return f.inviteErr
}

func (f *fakeChatRoomReceiver) AddMemberViaAP(roomID, userID string) error {
	f.memberCalls = append(f.memberCalls, [2]string{roomID, userID})
	return nil
}

func (f *fakeChatRoomReceiver) RemoveInvitationViaAP(roomID, userID string) error {
	f.removeCalls = append(f.removeCalls, [2]string{roomID, userID})
	return nil
}

func TestProcess_ChatRoomInvite_CreatesRoomCopyAndInvitation(t *testing.T) {
	p, repo, _, _ := newProcessor(t, aliceActor)
	recv := &fakeChatRoomReceiver{}
	p.SetChatRoomReceiver(recv)
	// invitee は local user bob。
	bobURI := "https://example.com/users/bob"
	repo.Users["bob"] = &model.User{ID: "bob", Username: "bob", URI: &bobURI}

	body := []byte(`{
		"type": "Invite",
		"actor": "https://remote.example/users/alice",
		"target": "https://example.com/users/bob",
		"object": {
			"type": "Group",
			"id": "https://remote.example/chat/rooms/room1",
			"name": "General",
			"summary": "desc",
			"attributedTo": "https://remote.example/users/alice"
		}
	}`)
	require.NoError(t, p.Process(body))

	require.Len(t, recv.ensureCalls, 1)
	assert.Equal(t, "room1", recv.ensureCalls[0][0])
	assert.Equal(t, "General", recv.ensureCalls[0][1])
	assert.Equal(t, "desc", recv.ensureCalls[0][2])
	assert.NotEmpty(t, recv.ensureCalls[0][3], "owner (resolved remote actor) must be set")
	require.Len(t, recv.inviteCalls, 1)
	assert.Equal(t, [2]string{"room1", "bob"}, recv.inviteCalls[0])
}

func TestProcess_ChatRoomAccept_AddsMembership(t *testing.T) {
	p, _, _, _ := newProcessor(t, aliceActor)
	recv := &fakeChatRoomReceiver{}
	p.SetChatRoomReceiver(recv)

	// remote alice が我々の room 招待を accept した。
	body := []byte(`{
		"type": "Accept",
		"actor": "https://remote.example/users/alice",
		"object": {
			"type": "Invite",
			"actor": "https://example.com/users/owner",
			"target": "https://remote.example/users/alice",
			"object": {
				"type": "Group",
				"id": "https://example.com/chat/rooms/room1",
				"name": "General"
			}
		}
	}`)
	require.NoError(t, p.Process(body))

	require.Len(t, recv.memberCalls, 1)
	assert.Equal(t, "room1", recv.memberCalls[0][0])
	assert.NotEmpty(t, recv.memberCalls[0][1])
}

func TestProcess_ChatRoomReject_RemovesInvitation(t *testing.T) {
	p, _, _, _ := newProcessor(t, aliceActor)
	recv := &fakeChatRoomReceiver{}
	p.SetChatRoomReceiver(recv)

	body := []byte(`{
		"type": "Reject",
		"actor": "https://remote.example/users/alice",
		"object": {
			"type": "Invite",
			"actor": "https://example.com/users/owner",
			"object": {
				"type": "Group",
				"id": "https://example.com/chat/rooms/room1",
				"name": "General"
			}
		}
	}`)
	require.NoError(t, p.Process(body))

	require.Len(t, recv.removeCalls, 1)
	assert.Equal(t, "room1", recv.removeCalls[0][0])
}

func TestProcess_ChatRoomInvite_MissingTarget(t *testing.T) {
	p, _, _, _ := newProcessor(t, aliceActor)
	recv := &fakeChatRoomReceiver{}
	p.SetChatRoomReceiver(recv)
	body := []byte(`{
		"type": "Invite",
		"actor": "https://remote.example/users/alice",
		"object": {"type": "Group", "id": "https://remote.example/chat/rooms/room1", "name": "G"}
	}`)
	// target が無い Invite はエラーで、room/invitation は作られない。
	require.Error(t, p.Process(body))
	assert.Empty(t, recv.ensureCalls)
}

func TestProcess_ChatRoomInvite_InviteeNotLocal(t *testing.T) {
	p, _, _, _ := newProcessor(t, aliceActor)
	recv := &fakeChatRoomReceiver{}
	p.SetChatRoomReceiver(recv)
	body := []byte(`{
		"type": "Invite",
		"actor": "https://remote.example/users/alice",
		"target": "https://remote.example/users/charlie",
		"object": {"type": "Group", "id": "https://remote.example/chat/rooms/room1", "name": "G"}
	}`)
	// invitee が local user でなければ招待を受け付けない。
	require.Error(t, p.Process(body))
	assert.Empty(t, recv.inviteCalls)
}

func TestProcess_ChatRoomInvite_NoReceiverWired(t *testing.T) {
	p, _, _, _ := newProcessor(t, aliceActor)
	// chatRoomReceiver 未配線でも panic せず未対応扱いになる。
	body := []byte(`{
		"type": "Invite",
		"actor": "https://remote.example/users/alice",
		"target": "https://example.com/users/bob",
		"object": {"type": "Group", "id": "https://remote.example/chat/rooms/room1", "name": "G"}
	}`)
	assert.Error(t, p.Process(body))
}

func TestProcess_ChatRoomAccept_NonGroupInnerIsNoOp(t *testing.T) {
	p, _, _, _ := newProcessor(t, aliceActor)
	recv := &fakeChatRoomReceiver{}
	p.SetChatRoomReceiver(recv)
	// inner が Invite だが object が Group でない (Game) → membership 化しない。
	body := []byte(`{
		"type": "Accept",
		"actor": "https://remote.example/users/alice",
		"object": {
			"type": "Invite",
			"object": {"type": "Game", "id": "https://remote.example/reversi/g1"}
		}
	}`)
	require.NoError(t, p.Process(body))
	assert.Empty(t, recv.memberCalls)
}

func TestProcess_ChatRoomInvite_BadRoomIDFallsThrough(t *testing.T) {
	p, _, _, _ := newProcessor(t, aliceActor)
	recv := &fakeChatRoomReceiver{}
	p.SetChatRoomReceiver(recv)
	// Group だが id が /chat/rooms/ 形式でない → chat room 経路に入らない。
	body := []byte(`{
		"type": "Invite",
		"actor": "https://remote.example/users/alice",
		"object": {"type": "Group", "id": "https://remote.example/groups/x"}
	}`)
	_ = p.Process(body)
	assert.Empty(t, recv.ensureCalls)
}

func TestProcess_ChatRoomInvite_EnsureRoomError(t *testing.T) {
	p, repo, _, _ := newProcessor(t, aliceActor)
	recv := &fakeChatRoomReceiver{ensureErr: errors.New("boom")}
	p.SetChatRoomReceiver(recv)
	bobURI := "https://example.com/users/bob"
	repo.Users["bob"] = &model.User{ID: "bob", Username: "bob", URI: &bobURI}
	body := []byte(`{
		"type": "Invite",
		"actor": "https://remote.example/users/alice",
		"target": "https://example.com/users/bob",
		"object": {"type": "Group", "id": "https://remote.example/chat/rooms/room1", "name": "G"}
	}`)
	// EnsureRoomViaAP が失敗したら invitation 作成まで進まずエラー。
	require.Error(t, p.Process(body))
	assert.Empty(t, recv.inviteCalls)
}

func TestProcess_ChatRoomInvite_CreateInvitationError(t *testing.T) {
	p, repo, _, _ := newProcessor(t, aliceActor)
	recv := &fakeChatRoomReceiver{inviteErr: errors.New("boom")}
	p.SetChatRoomReceiver(recv)
	bobURI := "https://example.com/users/bob"
	repo.Users["bob"] = &model.User{ID: "bob", Username: "bob", URI: &bobURI}
	body := []byte(`{
		"type": "Invite",
		"actor": "https://remote.example/users/alice",
		"target": "https://example.com/users/bob",
		"object": {"type": "Group", "id": "https://remote.example/chat/rooms/room1", "name": "G"}
	}`)
	require.Error(t, p.Process(body))
	require.Len(t, recv.ensureCalls, 1)
}

func TestProcess_ChatRoomAccept_NoReceiverIsNoOp(t *testing.T) {
	p, _, _, _ := newProcessor(t, aliceActor)
	// receiver 未配線でも accept は panic せず no-op。
	body := []byte(`{
		"type": "Accept",
		"actor": "https://remote.example/users/alice",
		"object": {
			"type": "Invite",
			"object": {"type": "Group", "id": "https://example.com/chat/rooms/room1"}
		}
	}`)
	require.NoError(t, p.Process(body))
}

func TestProcess_ChatRoomReject_NonGroupAndNoReceiver(t *testing.T) {
	// receiver 未配線 + group inner でも reject は no-op。
	p, _, _, _ := newProcessor(t, aliceActor)
	body := []byte(`{
		"type": "Reject",
		"actor": "https://remote.example/users/alice",
		"object": {
			"type": "Invite",
			"object": {"type": "Group", "id": "https://example.com/chat/rooms/room1"}
		}
	}`)
	require.NoError(t, p.Process(body))

	// receiver 配線済 + inner が非 Group → no-op (membership/remove 共に呼ばれない)。
	p2, _, _, _ := newProcessor(t, aliceActor)
	recv := &fakeChatRoomReceiver{}
	p2.SetChatRoomReceiver(recv)
	body2 := []byte(`{
		"type": "Reject",
		"actor": "https://remote.example/users/alice",
		"object": {"type": "Invite", "object": {"type": "Game", "id": "https://remote.example/reversi/g1"}}
	}`)
	require.NoError(t, p2.Process(body2))
	assert.Empty(t, recv.removeCalls)
}

func TestProcess_NonGroupInvite_NotRoutedToChatRoom(t *testing.T) {
	p, _, _, _ := newProcessor(t, aliceActor)
	recv := &fakeChatRoomReceiver{}
	p.SetChatRoomReceiver(recv)

	// object が Game (reversi) の Invite は chat room 経路に入らない。
	// reversi 未配線なので未対応扱いになるが、chatRoomReceiver は呼ばれない。
	body := []byte(`{
		"type": "Invite",
		"actor": "https://remote.example/users/alice",
		"object": {"type": "Game", "id": "https://remote.example/reversi/g1"}
	}`)
	_ = p.Process(body)
	assert.Empty(t, recv.ensureCalls, "Game invite must not hit chat room federation")
}
