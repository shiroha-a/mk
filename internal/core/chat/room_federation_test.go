package chat_test

import (
	"context"
	"testing"

	corechat "github.com/shiroha-a/mk/internal/core/chat"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newRoomFedService(t *testing.T) (*corechat.Service, *testutil.MockChatRepository) {
	t.Helper()
	repo := newFakeRepo()
	idGen, _ := id.NewGenerator("aidx")
	return corechat.NewService(repo, idGen), repo
}

func TestEnsureRoomViaAP_CreatesCopy(t *testing.T) {
	svc, repo := newRoomFedService(t)
	require.NoError(t, svc.EnsureRoomViaAP("room1", "General", "desc", "remoteOwner"))
	room, err := repo.FindRoomByID("room1")
	require.NoError(t, err)
	assert.Equal(t, "General", room.Name)
	assert.Equal(t, "desc", room.Description)
	assert.Equal(t, "remoteOwner", room.OwnerID)
}

func TestEnsureRoomViaAP_IdempotentSameOwner(t *testing.T) {
	svc, repo := newRoomFedService(t)
	require.NoError(t, repo.CreateRoom(&model.ChatRoom{ID: "room1", Name: "Existing", OwnerID: "remoteOwner"}))
	// 同一 owner の既存 room には何も書き込まない (idempotent)。
	require.NoError(t, svc.EnsureRoomViaAP("room1", "Different", "x", "remoteOwner"))
	room, _ := repo.FindRoomByID("room1")
	assert.Equal(t, "Existing", room.Name)
}

func TestEnsureRoomViaAP_OwnerMismatchRejected(t *testing.T) {
	svc, repo := newRoomFedService(t)
	// 同 ID で owner が異なる room が既にある場合は ID 衝突 / hijack とみなして拒否。
	require.NoError(t, repo.CreateRoom(&model.ChatRoom{ID: "room1", Name: "Local", OwnerID: "localOwner"}))
	err := svc.EnsureRoomViaAP("room1", "Remote", "x", "remoteOwner")
	require.Error(t, err)
}

func TestCreateInvitationViaAP_CreatesAndIdempotent(t *testing.T) {
	svc, repo := newRoomFedService(t)
	require.NoError(t, svc.CreateInvitationViaAP("room1", "localUser"))
	inv, err := repo.FindInvitation("localUser", "room1")
	require.NoError(t, err)
	assert.Equal(t, "room1", inv.RoomID)
	firstID := inv.ID
	// 2 回目は no-op (重複作成しない)。
	require.NoError(t, svc.CreateInvitationViaAP("room1", "localUser"))
	inv2, _ := repo.FindInvitation("localUser", "room1")
	assert.Equal(t, firstID, inv2.ID)
}

func TestAddMemberViaAP_RequiresPendingInvitation(t *testing.T) {
	svc, repo := newRoomFedService(t)
	// 招待が無い相手の Accept は membership 化しない (なりすまし防止)。
	require.NoError(t, svc.AddMemberViaAP("room1", "stranger"))
	_, err := repo.FindMembership("stranger", "room1")
	assert.Error(t, err, "membership should not be created without an invitation")
}

func TestAddMemberViaAP_CreatesMembershipAndConsumesInvitation(t *testing.T) {
	svc, repo := newRoomFedService(t)
	require.NoError(t, svc.CreateInvitationViaAP("room1", "remoteUser"))
	require.NoError(t, svc.AddMemberViaAP("room1", "remoteUser"))
	_, err := repo.FindMembership("remoteUser", "room1")
	require.NoError(t, err, "membership should be created")
	// 招待は consume される。
	_, err = repo.FindInvitation("remoteUser", "room1")
	assert.Error(t, err, "invitation should be consumed")
}

func TestRemoveInvitationViaAP_DeletesPending(t *testing.T) {
	svc, repo := newRoomFedService(t)
	require.NoError(t, svc.CreateInvitationViaAP("room1", "remoteUser"))
	require.NoError(t, svc.RemoveInvitationViaAP("room1", "remoteUser"))
	_, err := repo.FindInvitation("remoteUser", "room1")
	assert.Error(t, err)
	// 存在しない invitation の削除は no-op。
	require.NoError(t, svc.RemoveInvitationViaAP("room1", "nobody"))
}

func TestFederateInvitationResponse_DeliversAcceptToRemoteOwner(t *testing.T) {
	svc, chatRepo, userRepo, deliverer := newInvitationService(t)
	remoteHost := "remote.example"
	ownerURI := "https://remote.example/users/owner"
	userRepo.Users["remoteOwner"] = &model.User{ID: "remoteOwner", Username: "owner", Host: &remoteHost, URI: &ownerURI}
	userRepo.Users["bob"] = &model.User{ID: "bob", Username: "bob"}
	// remote owner の room copy。
	require.NoError(t, chatRepo.CreateRoom(&model.ChatRoom{ID: "room1", Name: "General", OwnerID: "remoteOwner"}))

	svc.FederateInvitationResponse("room1", "bob", true)
	assert.Equal(t, 1, deliverer.called)
	body := string(deliverer.lastBody)
	assert.Contains(t, body, `"type":"Accept"`)
	assert.Contains(t, body, `"type":"Invite"`)
	// inner Group id は owner host から復元した remote room URI。
	assert.Contains(t, body, "https://remote.example/chat/rooms/room1")
	// Accept の actor は応答した local invitee。
	assert.Contains(t, body, "https://local.example/users/bob")
}

func TestFederateInvitationResponse_DeliversRejectToRemoteOwner(t *testing.T) {
	svc, chatRepo, userRepo, deliverer := newInvitationService(t)
	remoteHost := "remote.example"
	ownerURI := "https://remote.example/users/owner"
	userRepo.Users["remoteOwner"] = &model.User{ID: "remoteOwner", Username: "owner", Host: &remoteHost, URI: &ownerURI}
	userRepo.Users["bob"] = &model.User{ID: "bob", Username: "bob"}
	require.NoError(t, chatRepo.CreateRoom(&model.ChatRoom{ID: "room1", Name: "General", OwnerID: "remoteOwner"}))

	svc.FederateInvitationResponse("room1", "bob", false)
	assert.Equal(t, 1, deliverer.called)
	assert.Contains(t, string(deliverer.lastBody), `"type":"Reject"`)
}

func TestFederateInvitationResponse_LocalRoomIsNoOp(t *testing.T) {
	svc, chatRepo, userRepo, deliverer := newInvitationService(t)
	userRepo.Users["localOwner"] = &model.User{ID: "localOwner", Username: "owner"}
	userRepo.Users["bob"] = &model.User{ID: "bob", Username: "bob"}
	// 自前 (local owner) の room は federation しない。
	require.NoError(t, chatRepo.CreateRoom(&model.ChatRoom{ID: "room1", Name: "General", OwnerID: "localOwner"}))

	svc.FederateInvitationResponse("room1", "bob", true)
	assert.Equal(t, 0, deliverer.called)
}

func TestFederateInvitationResponse_NoOpWhenRoomMissing(t *testing.T) {
	svc, _, _, deliverer := newInvitationService(t)
	svc.FederateInvitationResponse("ghost", "bob", true)
	assert.Equal(t, 0, deliverer.called)
}

func TestFederateInvitationResponse_OwnerWithoutURIIsNoOp(t *testing.T) {
	svc, chatRepo, userRepo, deliverer := newInvitationService(t)
	remoteHost := "remote.example"
	// remote owner だが URI 未設定 → 配送できないので no-op。
	userRepo.Users["remoteOwner"] = &model.User{ID: "remoteOwner", Username: "owner", Host: &remoteHost}
	require.NoError(t, chatRepo.CreateRoom(&model.ChatRoom{ID: "room1", Name: "General", OwnerID: "remoteOwner"}))

	svc.FederateInvitationResponse("room1", "bob", true)
	assert.Equal(t, 0, deliverer.called)
}

func TestFederateInvitationResponse_BadOwnerURIIsNoOp(t *testing.T) {
	svc, chatRepo, userRepo, deliverer := newInvitationService(t)
	remoteHost := "remote.example"
	badURI := "not-a-url"
	// owner URI から host を取れない場合は room URI を復元できず no-op。
	userRepo.Users["remoteOwner"] = &model.User{ID: "remoteOwner", Username: "owner", Host: &remoteHost, URI: &badURI}
	require.NoError(t, chatRepo.CreateRoom(&model.ChatRoom{ID: "room1", Name: "General", OwnerID: "remoteOwner"}))

	svc.FederateInvitationResponse("room1", "bob", true)
	assert.Equal(t, 0, deliverer.called)
}

func TestFederateInvitationResponse_DeliveryFailureSwallowed(t *testing.T) {
	svc, chatRepo, userRepo, deliverer := newInvitationService(t)
	deliverer.returnErr = assert.AnError
	remoteHost := "remote.example"
	ownerURI := "https://remote.example/users/owner"
	userRepo.Users["remoteOwner"] = &model.User{ID: "remoteOwner", Username: "owner", Host: &remoteHost, URI: &ownerURI}
	require.NoError(t, chatRepo.CreateRoom(&model.ChatRoom{ID: "room1", Name: "General", OwnerID: "remoteOwner"}))

	// 配送失敗しても panic/propagate しない。
	svc.FederateInvitationResponse("room1", "bob", false)
	assert.Equal(t, 1, deliverer.called)
}

func TestFederateInvitationResponse_UnwiredIsNoOp(t *testing.T) {
	// AP delivery 未配線の service では何もしない (panic しない)。
	svc, repo := newRoomFedService(t)
	require.NoError(t, repo.CreateRoom(&model.ChatRoom{ID: "room1", Name: "General", OwnerID: "remoteOwner"}))
	svc.FederateInvitationResponse("room1", "bob", true)
}

func TestCreateMessageToRoom_DeliversToRemoteMembers(t *testing.T) {
	svc, chatRepo, userRepo, deliverer := newInvitationService(t)
	remoteHost := "remote.example"
	bobURI := "https://remote.example/users/bob"
	userRepo.Users["alice"] = &model.User{ID: "alice", Username: "alice"}
	userRepo.Users["bob"] = &model.User{ID: "bob", Username: "bob", Host: &remoteHost, URI: &bobURI}
	// alice (local owner) の room に remote member bob。
	require.NoError(t, chatRepo.CreateRoom(&model.ChatRoom{ID: "room1", Name: "General", OwnerID: "alice"}))
	require.NoError(t, chatRepo.CreateMembership(&model.ChatRoomMembership{ID: "mem1", UserID: "bob", RoomID: "room1"}))

	_, err := svc.CreateMessageToRoom(context.Background(), "alice", "room1", "hi room", "")
	require.NoError(t, err)
	assert.Equal(t, 1, deliverer.called)
	body := string(deliverer.lastBody)
	assert.Contains(t, body, `"_misskey_talk":true`)
	assert.Contains(t, body, "https://local.example/chat/rooms/room1")
	assert.Contains(t, body, "https://remote.example/users/bob")
	assert.Contains(t, body, "hi room")
}

func TestCreateMessageToRoom_LocalOnlyRoomNoFederation(t *testing.T) {
	svc, chatRepo, userRepo, deliverer := newInvitationService(t)
	userRepo.Users["alice"] = &model.User{ID: "alice", Username: "alice"}
	userRepo.Users["carol"] = &model.User{ID: "carol", Username: "carol"}
	require.NoError(t, chatRepo.CreateRoom(&model.ChatRoom{ID: "room1", Name: "General", OwnerID: "alice"}))
	require.NoError(t, chatRepo.CreateMembership(&model.ChatRoomMembership{ID: "mem1", UserID: "carol", RoomID: "room1"}))

	_, err := svc.CreateMessageToRoom(context.Background(), "alice", "room1", "hi", "")
	require.NoError(t, err)
	// remote member が居ないので配送しない。
	assert.Equal(t, 0, deliverer.called)
}

func TestCreateMessageToRoom_RemoteRoomUsesOwnerHostURI(t *testing.T) {
	svc, chatRepo, userRepo, deliverer := newInvitationService(t)
	remoteHost := "remote.example"
	ownerURI := "https://remote.example/users/owner"
	userRepo.Users["owner"] = &model.User{ID: "owner", Username: "owner", Host: &remoteHost, URI: &ownerURI}
	userRepo.Users["alice"] = &model.User{ID: "alice", Username: "alice"}
	// remote owner の room copy に local member alice が投稿する。
	require.NoError(t, chatRepo.CreateRoom(&model.ChatRoom{ID: "room1", Name: "General", OwnerID: "owner"}))
	require.NoError(t, chatRepo.CreateMembership(&model.ChatRoomMembership{ID: "mem1", UserID: "alice", RoomID: "room1"}))

	_, err := svc.CreateMessageToRoom(context.Background(), "alice", "room1", "hi", "")
	require.NoError(t, err)
	assert.Equal(t, 1, deliverer.called)
	// room URI は owner の host から復元した remote URI になる。
	assert.Contains(t, string(deliverer.lastBody), "https://remote.example/chat/rooms/room1")
}

func TestCreateMessageToRoom_DeliveryFailureSwallowed(t *testing.T) {
	svc, chatRepo, userRepo, deliverer := newInvitationService(t)
	deliverer.returnErr = assert.AnError
	remoteHost := "remote.example"
	bobURI := "https://remote.example/users/bob"
	userRepo.Users["alice"] = &model.User{ID: "alice", Username: "alice"}
	userRepo.Users["bob"] = &model.User{ID: "bob", Username: "bob", Host: &remoteHost, URI: &bobURI}
	require.NoError(t, chatRepo.CreateRoom(&model.ChatRoom{ID: "room1", Name: "General", OwnerID: "alice"}))
	require.NoError(t, chatRepo.CreateMembership(&model.ChatRoomMembership{ID: "mem1", UserID: "bob", RoomID: "room1"}))

	// 配送失敗してもメッセージ作成は成功する。
	msg, err := svc.CreateMessageToRoom(context.Background(), "alice", "room1", "hi", "")
	require.NoError(t, err)
	require.NotNil(t, msg)
	assert.Equal(t, 1, deliverer.called)
}

func TestCreateRoomMessageViaAP_PersistsForMember(t *testing.T) {
	svc, repo := newRoomFedService(t)
	require.NoError(t, repo.CreateRoom(&model.ChatRoom{ID: "room1", Name: "General", OwnerID: "localOwner"}))
	require.NoError(t, repo.CreateMembership(&model.ChatRoomMembership{ID: "mem1", UserID: "rmt", RoomID: "room1"}))
	sender := &model.User{ID: "rmt", Username: "rmt"}

	uri := "https://remote.example/chat/messages/m1"
	err := svc.CreateRoomMessageViaAP(uri, sender, "room1", "hi room")
	require.NoError(t, err)
	stored, ferr := repo.FindMessageByURI(uri)
	require.NoError(t, ferr)
	assert.Equal(t, "rmt", stored.FromUserID)
	require.NotNil(t, stored.ToRoomID)
	assert.Equal(t, "room1", *stored.ToRoomID)
}

func TestCreateRoomMessageViaAP_DedupByURI(t *testing.T) {
	svc, repo := newRoomFedService(t)
	require.NoError(t, repo.CreateRoom(&model.ChatRoom{ID: "room1", Name: "General", OwnerID: "rmt"}))
	sender := &model.User{ID: "rmt", Username: "rmt"}
	uri := "https://remote.example/chat/messages/m1"
	require.NoError(t, svc.CreateRoomMessageViaAP(uri, sender, "room1", "hi"))
	// 同一 URI の再送は重複作成しない (AP retry 対策)。
	require.NoError(t, svc.CreateRoomMessageViaAP(uri, sender, "room1", "hi"))
	assert.Len(t, repo.Messages, 1)
}

func TestCreateRoomMessageViaAP_UnknownRoom(t *testing.T) {
	svc, _ := newRoomFedService(t)
	sender := &model.User{ID: "rmt", Username: "rmt"}
	err := svc.CreateRoomMessageViaAP("https://remote.example/chat/messages/m1", sender, "ghost", "hi")
	assert.ErrorIs(t, err, corechat.ErrNotFound)
}

func TestCreateRoomMessageViaAP_NonMemberForbidden(t *testing.T) {
	svc, repo := newRoomFedService(t)
	// owner = localOwner、sender rmt は member でない。
	require.NoError(t, repo.CreateRoom(&model.ChatRoom{ID: "room1", Name: "General", OwnerID: "localOwner"}))
	sender := &model.User{ID: "rmt", Username: "rmt"}
	err := svc.CreateRoomMessageViaAP("https://remote.example/chat/messages/m1", sender, "room1", "hi")
	assert.ErrorIs(t, err, corechat.ErrForbidden)
}

func TestCreateMessageToRoom_RemoteSenderDoesNotFederate(t *testing.T) {
	svc, chatRepo, userRepo, deliverer := newInvitationService(t)
	remoteHost := "remote.example"
	senderURI := "https://remote.example/users/rmt"
	bobURI := "https://remote.example/users/bob"
	userRepo.Users["rmt"] = &model.User{ID: "rmt", Username: "rmt", Host: &remoteHost, URI: &senderURI}
	userRepo.Users["bob"] = &model.User{ID: "bob", Username: "bob", Host: &remoteHost, URI: &bobURI}
	// remote owner の room copy。sender 自身も remote member。
	require.NoError(t, chatRepo.CreateRoom(&model.ChatRoom{ID: "room1", Name: "General", OwnerID: "rmt"}))
	require.NoError(t, chatRepo.CreateMembership(&model.ChatRoomMembership{ID: "mem1", UserID: "bob", RoomID: "room1"}))

	// remote sender が起点のメッセージは本インスタンスからは federation しない。
	_, err := svc.CreateMessageToRoom(context.Background(), "rmt", "room1", "hi", "")
	require.NoError(t, err)
	assert.Equal(t, 0, deliverer.called)
}
