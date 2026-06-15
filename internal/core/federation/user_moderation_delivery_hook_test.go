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

func newUserModHook(t *testing.T) (
	*federation.UserModerationDeliveryHook,
	*stubEnqueuer,
	*testutil.MockUserRepository,
	*testutil.MockFollowingRepository,
	*testutil.MockUserKeypairRepository,
) {
	t.Helper()
	enq := &stubEnqueuer{}
	userRepo := testutil.NewMockUserRepository()
	followingRepo := testutil.NewMockFollowingRepository()
	keypairRepo := testutil.NewMockUserKeypairRepository()
	urls := activitypub.NewURLBuilder("https://example.com")
	deliver := federation.NewDeliverService(enq, userRepo, followingRepo, keypairRepo, urls)
	renderer := activitypub.NewRenderer(urls)
	hook := federation.NewUserModerationDeliveryHook(deliver, renderer, userRepo)
	return hook, enq, userRepo, followingRepo, keypairRepo
}

// #1759: suspend / delete は local user の actor へ Delete を配信する。
func TestUserModHook_OnUserDeleted_LocalUser(t *testing.T) {
	hook, enq, userRepo, followingRepo, keypairRepo := newUserModHook(t)
	u := &model.User{ID: "alice", Username: "alice"}
	userRepo.Users["alice"] = u
	keypairRepo.Keypairs["alice"] = &model.UserKeypair{UserID: "alice", PrivateKey: "PEM"}
	followingRepo.RemoteInboxes["alice"] = []string{"https://r.example/inbox"}

	hook.OnUserDeleted(u)
	require.NotEmpty(t, enq.calls)

	var got map[string]any
	require.NoError(t, json.Unmarshal(enq.calls[0].Body, &got))
	assert.Equal(t, "Delete", got["type"])
	// actor delete は object も actor も当該 user の URI 文字列。
	assert.Equal(t, got["actor"], got["object"], "actor delete: object == actor URI")
	assert.Contains(t, got["object"], "/users/alice")
}

// #1759: unsuspend は Undo(Delete) を配信する。
func TestUserModHook_OnUserRestored_LocalUser(t *testing.T) {
	hook, enq, userRepo, followingRepo, keypairRepo := newUserModHook(t)
	u := &model.User{ID: "alice", Username: "alice"}
	userRepo.Users["alice"] = u
	keypairRepo.Keypairs["alice"] = &model.UserKeypair{UserID: "alice", PrivateKey: "PEM"}
	followingRepo.RemoteInboxes["alice"] = []string{"https://r.example/inbox"}

	hook.OnUserRestored(u)
	require.NotEmpty(t, enq.calls)

	var got map[string]any
	require.NoError(t, json.Unmarshal(enq.calls[0].Body, &got))
	assert.Equal(t, "Undo", got["type"])
	inner, ok := got["object"].(map[string]any)
	require.True(t, ok, "Undo wraps the Delete object")
	assert.Equal(t, "Delete", inner["type"])
}

// #1759: remote user は配信しない (当該インスタンスが発火元ではない)。
func TestUserModHook_RemoteUserSkipped(t *testing.T) {
	hook, enq, _, _, _ := newUserModHook(t)
	host := "remote.example"
	hook.OnUserDeleted(&model.User{ID: "bob", Username: "bob", Host: &host})
	assert.Empty(t, enq.calls, "remote user must not trigger delivery")
}
