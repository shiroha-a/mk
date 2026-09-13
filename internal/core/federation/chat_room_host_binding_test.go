package federation_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	corechat "github.com/shiroha-a/mk/internal/core/chat"
	"github.com/shiroha-a/mk/internal/core/federation"
	"github.com/shiroha-a/mk/internal/model"
)

// chatRoomInviteBody renders an Invite carrying a chat room Group object.
func chatRoomInviteBody(actor, groupID string) []byte {
	return []byte(`{
		"type": "Invite",
		"actor": "` + actor + `",
		"target": "https://example.com/users/bob",
		"object": {
			"type": "Group",
			"id": "` + groupID + `",
			"name": "General",
			"summary": "desc",
			"attributedTo": "` + actor + `"
		}
	}`)
}

// newChatRoomInviteProcessor wires a processor with a local invitee (bob) and a
// recording chat room receiver.
func newChatRoomInviteProcessor(t *testing.T) (*federation.Processor, *fakeChatRoomReceiver) {
	t.Helper()
	p, repo, _, _ := newProcessor(t, aliceActor)
	recv := &fakeChatRoomReceiver{}
	p.SetChatRoomReceiver(recv)
	bobURI := "https://example.com/users/bob"
	repo.Users["bob"] = &model.User{ID: "bob", Username: "bob", URI: &bobURI}
	return p, recv
}

// **自分の host でない room URI を名乗る Invite は drop する。** 通すと、署名が
// 通る任意の remote actor が第三者インスタンスの room として行を作れる (room の
// 素性が偽装できる)。id の先取りそのものは host 列が無い以上ここでは塞げない。
func TestProcess_ChatRoomInvite_ForeignHostGroupIDDropped(t *testing.T) {
	p, recv := newChatRoomInviteProcessor(t)

	err := p.Process(chatRoomInviteBody(
		"https://remote.example/users/alice",
		"https://evil.invalid/chat/rooms/room1",
	))

	assert.ErrorIs(t, err, federation.ErrUnsupportedActivity)
	assert.Empty(t, recv.ensureCalls, "別ホストの room は作らない")
	assert.Empty(t, recv.inviteCalls, "別ホストの room への招待は作らない")
}

// 自インスタンスの room URI を名乗る remote actor の Invite も drop する
// (ローカル room の乗っ取り / 先取り)。
func TestProcess_ChatRoomInvite_LocalHostGroupIDDropped(t *testing.T) {
	p, recv := newChatRoomInviteProcessor(t)

	err := p.Process(chatRoomInviteBody(
		"https://remote.example/users/alice",
		"https://example.com/chat/rooms/room1",
	))

	assert.ErrorIs(t, err, federation.ErrUnsupportedActivity)
	assert.Empty(t, recv.ensureCalls)
	assert.Empty(t, recv.inviteCalls)
}

// サブドメイン違い (親ドメインを名乗る形) も別ホスト扱いで drop する。
func TestProcess_ChatRoomInvite_SubdomainMismatchDropped(t *testing.T) {
	p, recv := newChatRoomInviteProcessor(t)

	err := p.Process(chatRoomInviteBody(
		"https://remote.example/users/alice",
		"https://chat.remote.example/chat/rooms/room1",
	))

	assert.ErrorIs(t, err, federation.ErrUnsupportedActivity)
	assert.Empty(t, recv.ensureCalls)
}

// 既定 port の明示は同一ホスト (punyHost 相当の正規化)。厳しすぎる比較に
// なっていないことを固定する。
func TestProcess_ChatRoomInvite_DefaultPortIsSameHost(t *testing.T) {
	p, recv := newChatRoomInviteProcessor(t)

	require.NoError(t, p.Process(chatRoomInviteBody(
		"https://remote.example/users/alice",
		"https://remote.example:443/chat/rooms/room1",
	)))

	require.Len(t, recv.ensureCalls, 1)
	assert.Equal(t, "room1", recv.ensureCalls[0][0])
	require.Len(t, recv.inviteCalls, 1)
}

// local actor 名義の Invite (loopback / なりすまし) は drop する。host 一致
// だけでは通ってしまう経路で、通すと「作った覚えのない room」がローカル
// 利用者を owner にして生える。
func TestProcess_ChatRoomInvite_LocalActorDropped(t *testing.T) {
	p, repo, _, _ := newProcessor(t, aliceActor)
	recv := &fakeChatRoomReceiver{}
	p.SetChatRoomReceiver(recv)
	bobURI := "https://example.com/users/bob"
	repo.Users["bob"] = &model.User{ID: "bob", Username: "bob", URI: &bobURI}
	// local owner (Host == nil => IsLocal)。
	localURI := "https://example.com/users/carol"
	repo.Users["carol"] = &model.User{ID: "carol", Username: "carol", URI: &localURI}

	err := p.Process(chatRoomInviteBody(
		"https://example.com/users/carol",
		"https://example.com/chat/rooms/room1",
	))

	assert.ErrorIs(t, err, federation.ErrUnsupportedActivity)
	assert.Empty(t, recv.ensureCalls, "local actor の Invite で room を作らない")
	assert.Empty(t, recv.inviteCalls)
}

// block されている相手からの招待は永久に解決しないので retry させない
// (ErrUnsupportedActivity にして inbox job を dead にしない)。
func TestProcess_ChatRoomInvite_BlockedIsNotRetried(t *testing.T) {
	p, recv := newChatRoomInviteProcessor(t)
	recv.inviteErr = corechat.ErrChatBlocked

	err := p.Process(chatRoomInviteBody(
		"https://remote.example/users/alice",
		"https://remote.example/chat/rooms/room1",
	))

	assert.ErrorIs(t, err, federation.ErrUnsupportedActivity)
	require.Len(t, recv.inviteCalls, 1, "招待判定自体は core に委ねる")
}

// 満室 (ErrRoomFull) も同様に non-retry。
func TestProcess_ChatRoomInvite_RoomFullIsNotRetried(t *testing.T) {
	p, recv := newChatRoomInviteProcessor(t)
	recv.inviteErr = corechat.ErrRoomFull

	err := p.Process(chatRoomInviteBody(
		"https://remote.example/users/alice",
		"https://remote.example/chat/rooms/room1",
	))

	assert.ErrorIs(t, err, federation.ErrUnsupportedActivity)
}

// room 不在 (ErrNotFound) も non-retry。
func TestProcess_ChatRoomInvite_RoomMissingIsNotRetried(t *testing.T) {
	p, recv := newChatRoomInviteProcessor(t)
	recv.inviteErr = corechat.ErrNotFound

	err := p.Process(chatRoomInviteBody(
		"https://remote.example/users/alice",
		"https://remote.example/chat/rooms/room1",
	))

	assert.ErrorIs(t, err, federation.ErrUnsupportedActivity)
}
