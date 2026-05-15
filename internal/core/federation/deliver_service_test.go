package federation_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

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
func (s *stubEnqueuer) ClearScheduledNote(_ string) error { return nil }
func (s *stubEnqueuer) SupportsScheduledNote() bool       { return true }

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

// --- Ed25519 capability gate (#1067 / #1071) ---

// stubKeypairExtraRepo implements repository.UserKeypairExtraRepository in
// memory for capability tests.
type stubKeypairExtraRepo struct {
	rows map[string]*model.UserKeypairExtra
}

func (s *stubKeypairExtraRepo) Upsert(k *model.UserKeypairExtra) error {
	if s.rows == nil {
		s.rows = map[string]*model.UserKeypairExtra{}
	}
	s.rows[k.UserID] = k
	return nil
}

func (s *stubKeypairExtraRepo) InsertIfAbsent(k *model.UserKeypairExtra) (bool, error) {
	if s.rows == nil {
		s.rows = map[string]*model.UserKeypairExtra{}
	}
	if _, exists := s.rows[k.UserID]; exists {
		return false, nil
	}
	s.rows[k.UserID] = k
	return true, nil
}

func (s *stubKeypairExtraRepo) FindByUserID(userID string) (*model.UserKeypairExtra, error) {
	if k, ok := s.rows[userID]; ok {
		return k, nil
	}
	return nil, errors.New("not found")
}

func (s *stubKeypairExtraRepo) Delete(_ string) error { return nil }

// recipient (= remote user) が user_publickey_extra に Ed25519 行を持ち、
// sender も user_keypair_extra に Ed25519 鍵を持つときだけ payload に
// Ed25519 鍵情報が詰められる。
func TestDeliverToUser_WithEd25519CapableRecipient_AddsEd25519Payload(t *testing.T) {
	svc, enq, userRepo, _, keypairRepo := newDeliverService(t)
	installLocalSigner(t, userRepo, keypairRepo)

	// sender に Ed25519 鍵 wire
	kpExtra := &stubKeypairExtraRepo{}
	require.NoError(t, kpExtra.Upsert(&model.UserKeypairExtra{
		UserID: "alice", Ed25519PublicKey: "PUB-ED", Ed25519PrivateKey: "PRIV-ED",
	}))
	svc.SetKeypairExtraRepo(kpExtra)

	// recipient (= bob) を Ed25519 capable に設定
	host := "remote.example"
	bob := &model.User{ID: "bob", Username: "bob", Host: &host, Inbox: strPtr("https://remote.example/users/bob/inbox")}
	pkExtra := &stubPublickeyExtraRepo{}
	require.NoError(t, pkExtra.Upsert(&model.UserPublickeyExtra{
		UserID: "bob", KeyID: "https://remote.example/users/bob#ed25519-key",
		KeyPEM: "-----BEGIN PUBLIC KEY-----\nB\n-----END PUBLIC KEY-----",
		Alg:    model.AlgEd25519,
	}))
	svc.SetPublickeyExtraRepo(pkExtra)

	require.NoError(t, svc.DeliverToUser("alice", bob, []byte(`{"type":"Create"}`)))
	require.Len(t, enq.calls, 1)
	got := enq.calls[0]
	assert.Equal(t, "https://example.com/users/alice#ed25519-key", got.Ed25519KeyID)
	assert.Equal(t, "PRIV-ED", got.Ed25519PrivPEM)
	assert.Equal(t, "PEM-DATA", got.KeyPEM, "RSA も並行で詰められる (Processor 側 fallback 用)")
}

// recipient が Ed25519 capable でない → payload に Ed25519 鍵情報なし
// (= 従来通り RSA only)。
func TestDeliverToUser_WithRSAOnlyRecipient_NoEd25519Payload(t *testing.T) {
	svc, enq, userRepo, _, keypairRepo := newDeliverService(t)
	installLocalSigner(t, userRepo, keypairRepo)

	kpExtra := &stubKeypairExtraRepo{}
	require.NoError(t, kpExtra.Upsert(&model.UserKeypairExtra{
		UserID: "alice", Ed25519PublicKey: "PUB-ED", Ed25519PrivateKey: "PRIV-ED",
	}))
	svc.SetKeypairExtraRepo(kpExtra)

	// recipient (= bob) は Ed25519 capable でない (= ListByUserID が空 slice)
	host := "remote.example"
	bob := &model.User{ID: "bob", Username: "bob", Host: &host, Inbox: strPtr("https://remote.example/users/bob/inbox")}
	svc.SetPublickeyExtraRepo(&stubPublickeyExtraRepo{})

	require.NoError(t, svc.DeliverToUser("alice", bob, []byte(`{}`)))
	require.Len(t, enq.calls, 1)
	assert.Empty(t, enq.calls[0].Ed25519KeyID)
	assert.Empty(t, enq.calls[0].Ed25519PrivPEM)
}

// sender が Ed25519 鍵を持たない (= P5 backfill 前の旧 user) なら payload に
// Ed25519 情報なし。
func TestDeliverToUser_SenderWithoutEd25519Key_NoEd25519Payload(t *testing.T) {
	svc, enq, userRepo, _, keypairRepo := newDeliverService(t)
	installLocalSigner(t, userRepo, keypairRepo)

	// sender の Ed25519 鍵を wire しない (keypairExtraRepo 未配線 = nil)
	host := "remote.example"
	bob := &model.User{ID: "bob", Username: "bob", Host: &host, Inbox: strPtr("https://remote.example/users/bob/inbox")}
	pkExtra := &stubPublickeyExtraRepo{}
	require.NoError(t, pkExtra.Upsert(&model.UserPublickeyExtra{
		UserID: "bob", KeyID: "k", KeyPEM: "P", Alg: model.AlgEd25519,
	}))
	svc.SetPublickeyExtraRepo(pkExtra)

	require.NoError(t, svc.DeliverToUser("alice", bob, []byte(`{}`)))
	require.Len(t, enq.calls, 1)
	assert.Empty(t, enq.calls[0].Ed25519KeyID)
}

// recipient capability の TTL cache が hit すると 2 回目以降の DeliverToUser
// で publickeyExtraRepo を叩かない (#1080 review #2: N+1 抑制)。
func TestDeliverToUser_RecipientCapabilityCachedAcrossCalls(t *testing.T) {
	svc, _, userRepo, _, keypairRepo := newDeliverService(t)
	installLocalSigner(t, userRepo, keypairRepo)
	svc.SetKeypairExtraRepo(&stubKeypairExtraRepo{rows: map[string]*model.UserKeypairExtra{
		"alice": {UserID: "alice", Ed25519PublicKey: "P", Ed25519PrivateKey: "K"},
	}})
	countingRepo := &countingPublickeyExtraRepo{
		stubPublickeyExtraRepo: stubPublickeyExtraRepo{},
	}
	require.NoError(t, countingRepo.Upsert(&model.UserPublickeyExtra{
		UserID: "bob", KeyID: "k", KeyPEM: "P", Alg: model.AlgEd25519,
	}))
	svc.SetPublickeyExtraRepo(countingRepo)

	host := "remote.example"
	bob := &model.User{ID: "bob", Username: "bob", Host: &host, Inbox: strPtr("https://remote.example/users/bob/inbox")}

	// 2 回 deliver しても ListByUserID は 1 回しか呼ばれない (cache hit)
	for i := 0; i < 2; i++ {
		require.NoError(t, svc.DeliverToUser("alice", bob, []byte(`{}`)))
	}
	assert.Equal(t, int32(1), countingRepo.listCalls.Load(),
		"recipient capability は cache 経由で 1 回だけ DB 経由")
}

// capability cache の TTL (5min) が経過したら次の deliver で再 fetch される。
// SetClock 経由で時間進行を deterministic に制御する。
func TestDeliverToUser_CapabilityCacheExpiresAfterTTL(t *testing.T) {
	svc, _, userRepo, _, keypairRepo := newDeliverService(t)
	installLocalSigner(t, userRepo, keypairRepo)
	svc.SetKeypairExtraRepo(&stubKeypairExtraRepo{rows: map[string]*model.UserKeypairExtra{
		"alice": {UserID: "alice", Ed25519PublicKey: "P", Ed25519PrivateKey: "K"},
	}})
	countingRepo := &countingPublickeyExtraRepo{stubPublickeyExtraRepo: stubPublickeyExtraRepo{}}
	require.NoError(t, countingRepo.Upsert(&model.UserPublickeyExtra{
		UserID: "bob", KeyID: "k", KeyPEM: "P", Alg: model.AlgEd25519,
	}))
	svc.SetPublickeyExtraRepo(countingRepo)

	now := time.Unix(1_700_000_000, 0).UTC()
	svc.SetClock(func() time.Time { return now })

	host := "remote.example"
	bob := &model.User{ID: "bob", Username: "bob", Host: &host, Inbox: strPtr("https://remote.example/users/bob/inbox")}

	// 1 回目: cache miss → DB query
	require.NoError(t, svc.DeliverToUser("alice", bob, []byte(`{}`)))
	assert.Equal(t, int32(1), countingRepo.listCalls.Load())

	// TTL 内: cache hit → DB query なし
	now = now.Add(4 * time.Minute)
	require.NoError(t, svc.DeliverToUser("alice", bob, []byte(`{}`)))
	assert.Equal(t, int32(1), countingRepo.listCalls.Load(), "TTL 内は cache hit")

	// TTL 超過: cache miss → 再 DB query
	now = now.Add(2 * time.Minute) // 合計 6 min 経過
	require.NoError(t, svc.DeliverToUser("alice", bob, []byte(`{}`)))
	assert.Equal(t, int32(2), countingRepo.listCalls.Load(), "TTL 経過で refetch")
}

// capability lookup が DB error を返した場合、cache には書き込まれず次回も
// retry される (= 一時的 DB 障害が "Ed25519 capable 永続化" として cache されない)。
func TestDeliverToUser_CapabilityLookupDBErrorNotCached(t *testing.T) {
	svc, _, userRepo, _, keypairRepo := newDeliverService(t)
	installLocalSigner(t, userRepo, keypairRepo)
	svc.SetKeypairExtraRepo(&stubKeypairExtraRepo{rows: map[string]*model.UserKeypairExtra{
		"alice": {UserID: "alice", Ed25519PublicKey: "P", Ed25519PrivateKey: "K"},
	}})
	// publickeyExtraRepo は常に error を返す
	failingRepo := &countingPublickeyExtraRepo{stubPublickeyExtraRepo: stubPublickeyExtraRepo{failErr: errors.New("db down")}}
	svc.SetPublickeyExtraRepo(failingRepo)

	host := "remote.example"
	bob := &model.User{ID: "bob", Username: "bob", Host: &host, Inbox: strPtr("https://remote.example/users/bob/inbox")}

	// 2 回 deliver: DB error なので cache に入らず、両方とも DB を叩く
	for i := 0; i < 2; i++ {
		require.NoError(t, svc.DeliverToUser("alice", bob, []byte(`{}`)))
	}
	assert.Equal(t, int32(2), failingRepo.listCalls.Load(), "DB error は cache せず retry")
}

// sender Ed25519 鍵の cache も同様。
func TestDeliverToUser_SenderEd25519KeyCachedAcrossCalls(t *testing.T) {
	svc, _, userRepo, _, keypairRepo := newDeliverService(t)
	installLocalSigner(t, userRepo, keypairRepo)
	countingKpRepo := &countingKeypairExtraRepo{
		stubKeypairExtraRepo: stubKeypairExtraRepo{rows: map[string]*model.UserKeypairExtra{
			"alice": {UserID: "alice", Ed25519PublicKey: "P", Ed25519PrivateKey: "K"},
		}},
	}
	svc.SetKeypairExtraRepo(countingKpRepo)
	pkExtra := &stubPublickeyExtraRepo{}
	require.NoError(t, pkExtra.Upsert(&model.UserPublickeyExtra{
		UserID: "bob", KeyID: "k", KeyPEM: "P", Alg: model.AlgEd25519,
	}))
	svc.SetPublickeyExtraRepo(pkExtra)

	host := "remote.example"
	bob := &model.User{ID: "bob", Username: "bob", Host: &host, Inbox: strPtr("https://remote.example/users/bob/inbox")}

	for i := 0; i < 3; i++ {
		require.NoError(t, svc.DeliverToUser("alice", bob, []byte(`{}`)))
	}
	assert.Equal(t, int32(1), countingKpRepo.findCalls.Load(),
		"sender Ed25519 鍵は cache 経由で 1 回だけ DB 経由")
}

// countingPublickeyExtraRepo wraps stubPublickeyExtraRepo to record how many
// times ListByUserID is called, for the capability cache tests.
type countingPublickeyExtraRepo struct {
	stubPublickeyExtraRepo
	listCalls atomic.Int32
}

func (c *countingPublickeyExtraRepo) ListByUserID(userID string) ([]model.UserPublickeyExtra, error) {
	c.listCalls.Add(1)
	return c.stubPublickeyExtraRepo.ListByUserID(userID)
}

type countingKeypairExtraRepo struct {
	stubKeypairExtraRepo
	findCalls atomic.Int32
}

func (c *countingKeypairExtraRepo) FindByUserID(userID string) (*model.UserKeypairExtra, error) {
	c.findCalls.Add(1)
	return c.stubKeypairExtraRepo.FindByUserID(userID)
}

// DeliverActivity (= recipient 不明な経路) は常に Ed25519 鍵情報を詰めない。
func TestDeliverActivity_NoRecipientNoEd25519Payload(t *testing.T) {
	svc, enq, userRepo, _, keypairRepo := newDeliverService(t)
	installLocalSigner(t, userRepo, keypairRepo)

	// extra repo を wire しても、recipient が渡らない経路では Ed25519 詰めない
	kpExtra := &stubKeypairExtraRepo{}
	require.NoError(t, kpExtra.Upsert(&model.UserKeypairExtra{
		UserID: "alice", Ed25519PublicKey: "P", Ed25519PrivateKey: "K",
	}))
	svc.SetKeypairExtraRepo(kpExtra)
	pkExtra := &stubPublickeyExtraRepo{}
	require.NoError(t, pkExtra.Upsert(&model.UserPublickeyExtra{
		UserID: "bob", KeyID: "k", KeyPEM: "P", Alg: model.AlgEd25519,
	}))
	svc.SetPublickeyExtraRepo(pkExtra)

	require.NoError(t, svc.DeliverActivity("alice", []byte(`{}`), []string{"https://remote.example/inbox"}))
	require.Len(t, enq.calls, 1)
	assert.Empty(t, enq.calls[0].Ed25519KeyID, "recipient 不明経路は RSA only")
}

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
