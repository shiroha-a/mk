package federation_test

import (
	"context"
	"errors"
	"testing"

	"github.com/shiroha-a/mk/internal/activitypub"
	"github.com/shiroha-a/mk/internal/core/federation"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/queue"
	"github.com/shiroha-a/mk/internal/queue/driver"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubEnqueuer captures EnqueueDeliver calls.
type stubEnqueuer struct {
	calls []queue.DeliverPayload
	err   error
}

func (s *stubEnqueuer) EnqueueDeliver(payload queue.DeliverPayload, _ ...driver.EnqueueOption) error {
	if s.err != nil {
		return s.err
	}
	s.calls = append(s.calls, payload)
	return nil
}

func (s *stubEnqueuer) EnqueueExport(_ queue.ExportPayload) error { return nil }
func (s *stubEnqueuer) EnqueueImport(_ queue.ImportPayload) error { return nil }
func (s *stubEnqueuer) EnqueueWebPush(_ context.Context, _ queue.WebPushPayload) error {
	return nil
}
func (s *stubEnqueuer) EnqueueUserWebhook(_ context.Context, _ queue.WebhookPayload) error {
	return nil
}
func (s *stubEnqueuer) EnqueueSystemWebhook(_ context.Context, _ queue.WebhookPayload) error {
	return nil
}
func (s *stubEnqueuer) EnqueueInbox(_ context.Context, _ queue.InboxPayload) error {
	return nil
}
func (s *stubEnqueuer) Close() error { return nil }
func (s *stubEnqueuer) EnqueuePostScheduledNote(_ queue.PostScheduledNotePayload, _ ...driver.EnqueueOption) error {
	return nil
}

func newDeliverService(t *testing.T) (*federation.DeliverService, *stubEnqueuer, *testutil.MockUserRepository, *testutil.MockFollowingRepository, *testutil.MockUserKeypairRepository) {
	t.Helper()
	enq := &stubEnqueuer{}
	userRepo := testutil.NewMockUserRepository()
	followingRepo := testutil.NewMockFollowingRepository()
	keypairRepo := testutil.NewMockUserKeypairRepository()
	urls := activitypub.NewURLBuilder("https://example.com")
	svc := federation.NewDeliverService(enq, userRepo, followingRepo, keypairRepo, urls)
	return svc, enq, userRepo, followingRepo, keypairRepo
}

// installLocalSigner registers a local user "alice" with a keypair so that the
// service can resolve a signer.
func installLocalSigner(t *testing.T, userRepo *testutil.MockUserRepository, keypairRepo *testutil.MockUserKeypairRepository) {
	t.Helper()
	userRepo.Users["alice"] = &model.User{ID: "alice", Username: "alice"}
	keypairRepo.Keypairs["alice"] = &model.UserKeypair{
		UserID:     "alice",
		PublicKey:  "PUB",
		PrivateKey: "PEM-DATA",
	}
}

// stubBlocker is a HostBlockChecker that returns true for the listed hosts.
// disallowed map allows simulating federation mode "specified" / "none".
// 既定 (zero-value) では allow-all 相当。
type stubBlocker struct {
	blocked    map[string]bool
	disallowed map[string]bool
}

func (s *stubBlocker) IsBlocked(host string) bool { return s.blocked[host] }
func (s *stubBlocker) IsAllowed(host string) bool { return !s.disallowed[host] }

func TestDeliverActivity_SkipsBlockedHosts(t *testing.T) {
	svc, enq, userRepo, _, keypairRepo := newDeliverService(t)
	installLocalSigner(t, userRepo, keypairRepo)
	svc.SetHostBlockChecker(&stubBlocker{blocked: map[string]bool{"bad.example": true}})

	require.NoError(t, svc.DeliverActivity("alice", []byte(`{}`), []string{
		"https://bad.example/inbox",
		"https://good.example/inbox",
	}))
	require.Len(t, enq.calls, 1)
	assert.Equal(t, "https://good.example/inbox", enq.calls[0].Inbox)
}

// federation: specified で allowlist 外 host への deliver は skip される
// (#536)。blocked と独立した経路。
func TestDeliverActivity_SkipsDisallowedHosts(t *testing.T) {
	svc, enq, userRepo, _, keypairRepo := newDeliverService(t)
	installLocalSigner(t, userRepo, keypairRepo)
	svc.SetHostBlockChecker(&stubBlocker{disallowed: map[string]bool{"other.example": true}})

	require.NoError(t, svc.DeliverActivity("alice", []byte(`{}`), []string{
		"https://other.example/inbox",
		"https://allowed.example/inbox",
	}))
	require.Len(t, enq.calls, 1, "only the allowed host should be enqueued")
	assert.Equal(t, "https://allowed.example/inbox", enq.calls[0].Inbox)
}

func TestDeliverActivity_BlockedInboxBadURL(t *testing.T) {
	svc, enq, userRepo, _, keypairRepo := newDeliverService(t)
	installLocalSigner(t, userRepo, keypairRepo)
	svc.SetHostBlockChecker(&stubBlocker{blocked: map[string]bool{}})

	// 不正な URL は parse 失敗で blocked 判定対象外 → そのまま enqueue される
	require.NoError(t, svc.DeliverActivity("alice", []byte(`{}`), []string{"://bad-url"}))
	require.Len(t, enq.calls, 1)
}

func TestDeliverActivity_EnqueuesUniqueInboxes(t *testing.T) {
	svc, enq, userRepo, _, keypairRepo := newDeliverService(t)
	installLocalSigner(t, userRepo, keypairRepo)

	body := []byte(`{"type":"Create"}`)
	inboxes := []string{
		"https://a.example/inbox",
		"https://b.example/inbox",
		"https://a.example/inbox", // 重複
		"",                        // 空文字列はスキップ
	}
	require.NoError(t, svc.DeliverActivity("alice", body, inboxes))

	require.Len(t, enq.calls, 2)
	got := []string{enq.calls[0].Inbox, enq.calls[1].Inbox}
	assert.ElementsMatch(t, []string{"https://a.example/inbox", "https://b.example/inbox"}, got)
	assert.Equal(t, body, enq.calls[0].Body)
	assert.Equal(t, "https://example.com/users/alice#main-key", enq.calls[0].KeyID)
	assert.Equal(t, "PEM-DATA", enq.calls[0].KeyPEM)
}

func TestDeliverActivity_EmptyInboxes_NoEnqueue(t *testing.T) {
	svc, enq, _, _, _ := newDeliverService(t)
	require.NoError(t, svc.DeliverActivity("alice", []byte(`{}`), nil))
	assert.Empty(t, enq.calls)
}

func TestDeliverActivity_SignerNotFound(t *testing.T) {
	svc, _, _, _, _ := newDeliverService(t)
	err := svc.DeliverActivity("ghost", []byte(`{}`), []string{"https://x/inbox"})
	require.Error(t, err)
}

func TestDeliverActivity_SignerIsRemote(t *testing.T) {
	svc, _, userRepo, _, _ := newDeliverService(t)
	host := "remote.example"
	userRepo.Users["bob"] = &model.User{ID: "bob", Username: "bob", Host: &host}
	err := svc.DeliverActivity("bob", []byte(`{}`), []string{"https://x/inbox"})
	assert.ErrorIs(t, err, federation.ErrNoLocalSigner)
}

func TestDeliverActivity_KeypairMissing(t *testing.T) {
	svc, _, userRepo, _, _ := newDeliverService(t)
	userRepo.Users["alice"] = &model.User{ID: "alice", Username: "alice"}
	err := svc.DeliverActivity("alice", []byte(`{}`), []string{"https://x/inbox"})
	assert.ErrorIs(t, err, federation.ErrSignerKeyMissing)
}

func TestDeliverActivity_EnqueueError(t *testing.T) {
	svc, enq, userRepo, _, keypairRepo := newDeliverService(t)
	installLocalSigner(t, userRepo, keypairRepo)
	enq.err = errors.New("queue down")
	err := svc.DeliverActivity("alice", []byte(`{}`), []string{"https://x/inbox"})
	assert.Error(t, err)
}

func TestDeliverToFollowers(t *testing.T) {
	svc, enq, userRepo, followingRepo, keypairRepo := newDeliverService(t)
	installLocalSigner(t, userRepo, keypairRepo)
	followingRepo.RemoteInboxes["alice"] = []string{
		"https://remote.example/inbox",
		"https://other.example/users/x/inbox",
	}
	require.NoError(t, svc.DeliverToFollowers("alice", []byte(`{}`)))
	assert.Len(t, enq.calls, 2)
}

func TestDeliverToFollowers_RepoError(t *testing.T) {
	svc, _, userRepo, _, keypairRepo := newDeliverService(t)
	installLocalSigner(t, userRepo, keypairRepo)
	// failingFollowingRepo を使うため、独自構造を組み立てる
	failing := &failingListInboxesRepo{MockFollowingRepository: testutil.NewMockFollowingRepository()}
	urls := activitypub.NewURLBuilder("https://example.com")
	svc = federation.NewDeliverService(&stubEnqueuer{}, userRepo, failing, keypairRepo, urls)
	err := svc.DeliverToFollowers("alice", []byte(`{}`))
	assert.Error(t, err)
}

type failingListInboxesRepo struct {
	*testutil.MockFollowingRepository
}

func (f *failingListInboxesRepo) ListRemoteFollowerInboxes(_ string) ([]string, error) {
	return nil, errors.New("db down")
}

func TestDeliverToUser_LocalRecipientSkipped(t *testing.T) {
	svc, enq, userRepo, _, keypairRepo := newDeliverService(t)
	installLocalSigner(t, userRepo, keypairRepo)
	local := &model.User{ID: "bob", Username: "bob"} // host==nil → local
	require.NoError(t, svc.DeliverToUser("alice", local, []byte(`{}`)))
	assert.Empty(t, enq.calls)
}

func TestDeliverToUser_NilRecipient(t *testing.T) {
	svc, _, _, _, _ := newDeliverService(t)
	// signer が居なくても early-return する
	require.NoError(t, svc.DeliverToUser("alice", nil, []byte(`{}`)))
}

func TestDeliverToUser_RemoteWithSharedInbox(t *testing.T) {
	svc, enq, userRepo, _, keypairRepo := newDeliverService(t)
	installLocalSigner(t, userRepo, keypairRepo)
	host := "remote.example"
	shared := "https://remote.example/inbox"
	inbox := "https://remote.example/users/r/inbox"
	rem := &model.User{ID: "r1", Username: "r", Host: &host, SharedInbox: &shared, Inbox: &inbox}
	require.NoError(t, svc.DeliverToUser("alice", rem, []byte(`{}`)))
	require.Len(t, enq.calls, 1)
	assert.Equal(t, shared, enq.calls[0].Inbox)
}

func TestDeliverToUser_RemoteFallbackToInbox(t *testing.T) {
	svc, enq, userRepo, _, keypairRepo := newDeliverService(t)
	installLocalSigner(t, userRepo, keypairRepo)
	host := "remote.example"
	inbox := "https://remote.example/users/r/inbox"
	rem := &model.User{ID: "r1", Username: "r", Host: &host, Inbox: &inbox}
	require.NoError(t, svc.DeliverToUser("alice", rem, []byte(`{}`)))
	require.Len(t, enq.calls, 1)
	assert.Equal(t, inbox, enq.calls[0].Inbox)
}

func TestDeliverToUser_RemoteEmptyInbox(t *testing.T) {
	svc, enq, userRepo, _, keypairRepo := newDeliverService(t)
	installLocalSigner(t, userRepo, keypairRepo)
	host := "remote.example"
	rem := &model.User{ID: "r1", Username: "r", Host: &host}
	require.NoError(t, svc.DeliverToUser("alice", rem, []byte(`{}`)))
	assert.Empty(t, enq.calls)
}
