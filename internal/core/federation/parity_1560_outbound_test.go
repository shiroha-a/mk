package federation_test

import (
	"encoding/json"
	"testing"

	"github.com/shiroha-a/mk/internal/activitypub"
	"github.com/shiroha-a/mk/internal/core/federation"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newBlockingHook(t *testing.T) (*federation.BlockingDeliveryHook, *stubEnqueuer, *testutil.MockUserRepository, *testutil.MockUserKeypairRepository) {
	t.Helper()
	enq := &stubEnqueuer{}
	userRepo := testutil.NewMockUserRepository()
	followingRepo := testutil.NewMockFollowingRepository()
	keypairRepo := testutil.NewMockUserKeypairRepository()
	urls := activitypub.NewURLBuilder("https://example.com")
	deliver := federation.NewDeliverService(enq, userRepo, followingRepo, keypairRepo, urls)
	renderer := activitypub.NewRenderer(urls)
	hook := federation.NewBlockingDeliveryHook(deliver, userRepo, renderer)
	return hook, enq, userRepo, keypairRepo
}

// #1560 [MEDIUM] local→remote の Block / Undo(Block) を相手 inbox へ配信。
func TestBlockingHook_DeliversBlockAndUndo(t *testing.T) {
	hook, enq, userRepo, keypairRepo := newBlockingHook(t)
	userRepo.Users["alice"] = &model.User{ID: "alice", Username: "alice"} // local
	keypairRepo.Keypairs["alice"] = &model.UserKeypair{UserID: "alice", PrivateKey: "PEM"}
	bobURI, bobInbox, host := "https://remote.example/users/bob", "https://remote.example/users/bob/inbox", "remote.example"
	userRepo.Users["bob"] = &model.User{ID: "bob", Username: "bob", Host: &host, URI: &bobURI, Inbox: &bobInbox}

	hook.OnBlocked("alice", "bob")
	require.Len(t, enq.calls, 1)
	var blk map[string]any
	require.NoError(t, json.Unmarshal(enq.calls[0].Body, &blk))
	assert.Equal(t, "Block", blk["type"])
	assert.Equal(t, bobURI, blk["object"])
	assert.Contains(t, blk["actor"], "alice")

	hook.OnUnblocked("alice", "bob")
	require.Len(t, enq.calls, 2)
	var undo map[string]any
	require.NoError(t, json.Unmarshal(enq.calls[1].Body, &undo))
	assert.Equal(t, "Undo", undo["type"])
	inner, ok := undo["object"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "Block", inner["type"])
	assert.Equal(t, bobURI, inner["object"])
}

// blockee がローカル or URI 無しなら配信しない。
func TestBlockingHook_NoDeliveryForLocalOrUnknown(t *testing.T) {
	hook, enq, userRepo, _ := newBlockingHook(t)
	userRepo.Users["alice"] = &model.User{ID: "alice", Username: "alice"}
	userRepo.Users["carol"] = &model.User{ID: "carol", Username: "carol"} // local blockee
	hook.OnBlocked("alice", "carol")
	assert.Empty(t, enq.calls, "local blockee → no AP delivery")
}

func newProfileUpdateHook(t *testing.T) (*federation.ProfileUpdateDeliveryHook, *stubEnqueuer, *testutil.MockUserRepository, *testutil.MockFollowingRepository, *testutil.MockUserKeypairRepository) {
	t.Helper()
	enq := &stubEnqueuer{}
	userRepo := testutil.NewMockUserRepository()
	followingRepo := testutil.NewMockFollowingRepository()
	keypairRepo := testutil.NewMockUserKeypairRepository()
	urls := activitypub.NewURLBuilder("https://example.com")
	deliver := federation.NewDeliverService(enq, userRepo, followingRepo, keypairRepo, urls)
	renderer := activitypub.NewRenderer(urls)
	hook := federation.NewProfileUpdateDeliveryHook(deliver, renderer, userRepo, keypairRepo)
	return hook, enq, userRepo, followingRepo, keypairRepo
}

// #1560 [MEDIUM] profile 編集で Update(Person) を remote followers へ配信。
func TestProfileUpdateHook_DeliversToFollowers(t *testing.T) {
	hook, enq, userRepo, followingRepo, keypairRepo := newProfileUpdateHook(t)
	userRepo.Users["alice"] = &model.User{ID: "alice", Username: "alice"} // local
	keypairRepo.Keypairs["alice"] = &model.UserKeypair{UserID: "alice", PrivateKey: "PEM"}
	// remote follower bob の inbox を登録
	followingRepo.RemoteInboxes["alice"] = []string{"https://remote.example/users/bob/inbox"}

	hook.OnLocalProfileUpdated("alice")
	require.Len(t, enq.calls, 1)
	assert.Equal(t, "https://remote.example/users/bob/inbox", enq.calls[0].Inbox)
	var upd map[string]any
	require.NoError(t, json.Unmarshal(enq.calls[0].Body, &upd))
	assert.Equal(t, "Update", upd["type"])
	inner, ok := upd["object"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "Person", inner["type"])
	assert.Contains(t, inner["id"], "alice")
}
