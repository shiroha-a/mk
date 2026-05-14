package federation

import (
	"context"
	"testing"

	"github.com/shiroha-a/mk/internal/activitypub"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/queue"
	"github.com/shiroha-a/mk/internal/queue/driver"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingEnqueuer captures DeliverPayloads enqueued via DeliverActivity so
// hook tests can assert what was scheduled without a real asynq server. The
// non-deliver methods are no-ops to satisfy queue.Enqueuer.
type recordingEnqueuer struct {
	delivers []queue.DeliverPayload
}

func (r *recordingEnqueuer) EnqueueDeliver(p queue.DeliverPayload, _ ...driver.EnqueueOption) error {
	r.delivers = append(r.delivers, p)
	return nil
}
func (r *recordingEnqueuer) EnqueueExport(queue.ExportPayload) error { return nil }
func (r *recordingEnqueuer) EnqueueImport(queue.ImportPayload) error { return nil }
func (r *recordingEnqueuer) EnqueueWebPush(context.Context, queue.WebPushPayload) error {
	return nil
}
func (r *recordingEnqueuer) EnqueueUserWebhook(context.Context, queue.WebhookPayload) error {
	return nil
}
func (r *recordingEnqueuer) EnqueueSystemWebhook(context.Context, queue.WebhookPayload) error {
	return nil
}
func (r *recordingEnqueuer) EnqueueInbox(context.Context, queue.InboxPayload) error { return nil }
func (r *recordingEnqueuer) EnqueuePostScheduledNote(queue.PostScheduledNotePayload, ...driver.EnqueueOption) error {
	return nil
}
func (r *recordingEnqueuer) ClearScheduledNote(string) error { return nil }
func (r *recordingEnqueuer) SupportsScheduledNote() bool     { return true }
func (r *recordingEnqueuer) Close() error                    { return nil }

func newHookSetup(t *testing.T) (*PollDeliveryHook, *recordingEnqueuer, *testutil.MockUserRepository, *testutil.MockUserKeypairRepository, id.Generator) {
	t.Helper()
	idGen, err := id.NewGenerator("aidx")
	require.NoError(t, err)
	urls := activitypub.NewURLBuilder("https://local.example")
	renderer := activitypub.NewRenderer(urls)
	enq := &recordingEnqueuer{}
	userRepo := testutil.NewMockUserRepository()
	keypairRepo := testutil.NewMockUserKeypairRepository()
	followingRepo := testutil.NewMockFollowingRepository()
	deliver := NewDeliverService(enq, userRepo, followingRepo, keypairRepo, urls)
	hook := NewPollDeliveryHook(renderer, deliver, userRepo, urls, idGen)
	return hook, enq, userRepo, keypairRepo, idGen
}

func seedSigner(t *testing.T, userRepo *testutil.MockUserRepository, keypairRepo *testutil.MockUserKeypairRepository, userID string) {
	t.Helper()
	userRepo.Users[userID] = &model.User{ID: userID, Username: userID}
	keypairRepo.Keypairs[userID] = &model.UserKeypair{
		UserID: userID,
		// Test-only ECDSA-style stub key. DeliverActivity signs with the PEM
		// we pass through; the recording enqueuer ignores it.
		PrivateKey: "-----BEGIN PRIVATE KEY-----\nstub\n-----END PRIVATE KEY-----",
	}
}

func TestPollDeliveryHook_OnVote_LocalPollSkipped(t *testing.T) {
	hook, enq, _, _, _ := newHookSetup(t)
	target := &model.Note{ID: "n1", UserID: "author", UserHost: nil}
	hook.OnVote(&model.User{ID: "voter"}, target, 0, "A")
	assert.Empty(t, enq.delivers, "local poll must not enqueue any deliver")
}

func TestPollDeliveryHook_OnVote_NilTargetSkipped(t *testing.T) {
	hook, enq, _, _, _ := newHookSetup(t)
	hook.OnVote(&model.User{ID: "voter"}, nil, 0, "A")
	hook.OnVote(nil, &model.Note{}, 0, "A")
	assert.Empty(t, enq.delivers)
}

func TestPollDeliveryHook_OnVote_MissingMetadataSkipped(t *testing.T) {
	hook, enq, userRepo, _, _ := newHookSetup(t)
	host := "remote.example"
	uri := "https://remote.example/notes/r1"
	target := &model.Note{ID: "n1", UserID: "author_remote", UserHost: &host, URI: &uri}

	// 1. author lookup fails
	hook.OnVote(&model.User{ID: "voter"}, target, 0, "A")
	assert.Empty(t, enq.delivers)

	// 2. author exists but no inbox
	authorURI := "https://remote.example/users/author_remote"
	userRepo.Users["author_remote"] = &model.User{ID: "author_remote", URI: &authorURI}
	hook.OnVote(&model.User{ID: "voter"}, target, 0, "A")
	assert.Empty(t, enq.delivers)

	// 3. author exists with inbox but no URI
	inbox := "https://remote.example/inbox"
	userRepo.Users["author_remote"] = &model.User{ID: "author_remote", Inbox: &inbox}
	hook.OnVote(&model.User{ID: "voter"}, target, 0, "A")
	assert.Empty(t, enq.delivers)

	// 4. target.URI nil -> skip even with everything else set
	target2 := &model.Note{ID: "n1", UserID: "author_remote", UserHost: &host}
	userRepo.Users["author_remote"] = &model.User{ID: "author_remote", Inbox: &inbox, URI: &authorURI}
	hook.OnVote(&model.User{ID: "voter"}, target2, 0, "A")
	assert.Empty(t, enq.delivers)
}

func TestPollDeliveryHook_OnVote_RemoteEnqueues(t *testing.T) {
	hook, enq, userRepo, keypairRepo, _ := newHookSetup(t)
	seedSigner(t, userRepo, keypairRepo, "voter")

	host := "remote.example"
	uri := "https://remote.example/notes/r1"
	target := &model.Note{ID: "n1", UserID: "author_remote", UserHost: &host, URI: &uri}
	authorURI := "https://remote.example/users/author_remote"
	inbox := "https://remote.example/users/author_remote/inbox"
	userRepo.Users["author_remote"] = &model.User{ID: "author_remote", Inbox: &inbox, URI: &authorURI}

	hook.OnVote(&model.User{ID: "voter"}, target, 0, "Apple")
	require.Len(t, enq.delivers, 1, "remote poll must enqueue exactly 1 deliver to author inbox")
	assert.Equal(t, inbox, enq.delivers[0].Inbox)
	assert.Contains(t, string(enq.delivers[0].Body), `"name":"Apple"`)
	assert.Contains(t, string(enq.delivers[0].Body), `"inReplyTo":"https://remote.example/notes/r1"`)
}

func TestPollDeliveryHook_OnLocalPollUpdated_RemoteSkipped(t *testing.T) {
	hook, enq, _, _, _ := newHookSetup(t)
	host := "remote.example"
	hook.OnLocalPollUpdated(&model.Note{ID: "n1", UserHost: &host})
	assert.Empty(t, enq.delivers, "remote poll must not trigger Update broadcast")
}

func TestPollDeliveryHook_OnLocalPollUpdated_LocalOnlySkipped(t *testing.T) {
	hook, enq, _, _, _ := newHookSetup(t)
	hook.OnLocalPollUpdated(&model.Note{ID: "n1", LocalOnly: true})
	assert.Empty(t, enq.delivers, "local-only poll must not trigger Update broadcast")
}

func TestPollDeliveryHook_OnLocalPollUpdated_NoFollowersNoDeliver(t *testing.T) {
	hook, enq, userRepo, keypairRepo, _ := newHookSetup(t)
	seedSigner(t, userRepo, keypairRepo, "author")
	// followingRepo is empty -> ListRemoteFollowerInboxes returns []
	hook.OnLocalPollUpdated(&model.Note{ID: "n1", UserID: "author", HasPoll: true, Visibility: model.NoteVisibilityPublic})
	assert.Empty(t, enq.delivers, "no followers -> no deliver enqueued")
}

func TestPollDeliveryHook_NilSetterNoOp(t *testing.T) {
	var hook *PollDeliveryHook
	hook.OnVote(&model.User{}, &model.Note{}, 0, "x")
	hook.OnLocalPollUpdated(&model.Note{})
}

// TestPollDeliveryHook_OnVote_DeliverErrorLogged exercises the failure path
// where DeliverActivity returns an error (signer keypair missing). The hook
// only logs and returns — DB state is not rolled back.
func TestPollDeliveryHook_OnVote_DeliverErrorLogged(t *testing.T) {
	hook, _, userRepo, _, _ := newHookSetup(t)
	// voter ユーザーは作るが keypair は seed しないので signerCredentials が err を返す
	userRepo.Users["voter"] = &model.User{ID: "voter", Username: "voter"}

	host := "remote.example"
	uri := "https://remote.example/notes/r1"
	target := &model.Note{ID: "n1", UserID: "author_remote", UserHost: &host, URI: &uri}
	authorURI := "https://remote.example/users/author_remote"
	inbox := "https://remote.example/users/author_remote/inbox"
	userRepo.Users["author_remote"] = &model.User{ID: "author_remote", Inbox: &inbox, URI: &authorURI}

	// no panic, no return value — error is silently logged
	hook.OnVote(&model.User{ID: "voter"}, target, 0, "Apple")
}

// TestPollDeliveryHook_OnLocalPollUpdated_DeliverErrorLogged exercises the
// equivalent failure path on the Update(Question) broadcast side.
func TestPollDeliveryHook_OnLocalPollUpdated_DeliverErrorLogged(t *testing.T) {
	hook, _, userRepo, _, _ := newHookSetup(t)
	// author exists but no keypair — signer fetch fails when followers are present.
	userRepo.Users["author"] = &model.User{ID: "author"}
	hook.OnLocalPollUpdated(&model.Note{
		ID: "n1", UserID: "author", HasPoll: true, Visibility: model.NoteVisibilityPublic,
	})
}
