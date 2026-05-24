package chat_test

import (
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
