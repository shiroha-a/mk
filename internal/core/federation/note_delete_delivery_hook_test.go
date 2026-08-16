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

func newDeleteHook(t *testing.T) (
	*federation.NoteDeleteDeliveryHook,
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
	hook := federation.NewNoteDeleteDeliveryHook(deliver, renderer, urls)
	return hook, enq, userRepo, followingRepo, keypairRepo
}

func TestNoteDeleteHook_LocalAuthor(t *testing.T) {
	hook, enq, userRepo, followingRepo, keypairRepo := newDeleteHook(t)
	author := &model.User{ID: "alice", Username: "alice"}
	userRepo.Users["alice"] = author
	keypairRepo.Keypairs["alice"] = &model.UserKeypair{UserID: "alice", PrivateKey: "PEM"}
	followingRepo.RemoteInboxes["alice"] = []string{"https://r.example/inbox"}

	note := &model.Note{ID: "n1", UserID: "alice"}
	hook.OnNoteDeleted(author, note)
	require.Len(t, enq.calls, 1)

	var got map[string]any
	require.NoError(t, json.Unmarshal(enq.calls[0].Body, &got))
	assert.Equal(t, "Delete", got["type"])
	tomb, ok := got["object"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "Tombstone", tomb["type"])
	assert.Equal(t, "https://example.com/notes/n1", tomb["id"])
}

func TestNoteDeleteHook_RemoteNoteURI(t *testing.T) {
	// 取り込まれたリモート note を削除する経路 (実際にはほぼ起きないが、URI
	// フォールバック分岐をカバーする)。 author=local、note.URI=remote の混乱した
	// ケースで URI フォールバックが動くか確認。
	hook, enq, userRepo, followingRepo, keypairRepo := newDeleteHook(t)
	author := &model.User{ID: "alice", Username: "alice"}
	userRepo.Users["alice"] = author
	keypairRepo.Keypairs["alice"] = &model.UserKeypair{UserID: "alice", PrivateKey: "PEM"}
	followingRepo.RemoteInboxes["alice"] = []string{"https://r.example/inbox"}

	uri := "https://other.example/notes/x"
	note := &model.Note{ID: "n1", UserID: "alice", URI: &uri}
	hook.OnNoteDeleted(author, note)
	require.Len(t, enq.calls, 1)

	var got map[string]any
	require.NoError(t, json.Unmarshal(enq.calls[0].Body, &got))
	tomb := got["object"].(map[string]any)
	assert.Equal(t, uri, tomb["id"])
}

func TestNoteDeleteHook_RemoteAuthorSkipped(t *testing.T) {
	hook, enq, _, _, _ := newDeleteHook(t)
	host := "remote.example"
	author := &model.User{ID: "bob", Host: &host}
	hook.OnNoteDeleted(author, &model.Note{ID: "n1", UserID: "bob"})
	assert.Empty(t, enq.calls)
}

func TestNoteDeleteHook_LocalOnlySkipped(t *testing.T) {
	hook, enq, userRepo, _, _ := newDeleteHook(t)
	author := &model.User{ID: "alice"}
	userRepo.Users["alice"] = author
	note := &model.Note{ID: "n1", UserID: "alice", LocalOnly: true}
	hook.OnNoteDeleted(author, note)
	assert.Empty(t, enq.calls)
}

func TestNoteDeleteHook_NilArgs(t *testing.T) {
	hook, enq, _, _, _ := newDeleteHook(t)
	hook.OnNoteDeleted(nil, &model.Note{})
	hook.OnNoteDeleted(&model.User{}, nil)
	assert.Empty(t, enq.calls)
}

func TestNoteDeleteHook_DeliverErrorDoesNotPanic(t *testing.T) {
	hook, _, userRepo, followingRepo, _ := newDeleteHook(t)
	author := &model.User{ID: "alice"}
	userRepo.Users["alice"] = author
	// keypair なしで signerCredentials が失敗する
	followingRepo.RemoteInboxes["alice"] = []string{"https://r.example/inbox"}
	note := &model.Note{ID: "n1", UserID: "alice"}
	hook.OnNoteDeleted(author, note)
}

func TestNoteDeleteHook_BroadcastToRemoteInboxes(t *testing.T) {
	hook, enq, userRepo, _, keypairRepo := newDeleteHook(t)
	hook.SetUserRepo(userRepo)

	author := &model.User{ID: "alice", Username: "alice"}
	userRepo.Users["alice"] = author
	keypairRepo.Keypairs["alice"] = &model.UserKeypair{UserID: "alice", PrivateKey: "PEM"}

	// 既知のリモートインスタンスを 2 つ追加。片方は sharedInbox 優先、もう片方は inbox。
	h := "remote1.example"
	shared := "https://remote1.example/inbox"
	userRepo.Users["r1"] = &model.User{ID: "r1", Host: &h, SharedInbox: &shared}
	h2 := "remote2.example"
	inbox := "https://remote2.example/users/bob/inbox"
	userRepo.Users["r2"] = &model.User{ID: "r2", Host: &h2, Inbox: &inbox}

	note := &model.Note{ID: "n1", UserID: "alice", Visibility: model.NoteVisibilityPublic}
	hook.OnNoteDeleted(author, note)

	// Followers への配信 0 件 + 全リモート 2 件 = 2 件 enqueued.
	require.Len(t, enq.calls, 2)
	inboxes := []string{enq.calls[0].Inbox, enq.calls[1].Inbox}
	assert.Contains(t, inboxes, shared)
	assert.Contains(t, inboxes, inbox)
}

func TestNoteDeleteHook_BroadcastSkippedForFollowersVisibility(t *testing.T) {
	hook, enq, userRepo, _, keypairRepo := newDeleteHook(t)
	hook.SetUserRepo(userRepo)

	author := &model.User{ID: "alice"}
	userRepo.Users["alice"] = author
	keypairRepo.Keypairs["alice"] = &model.UserKeypair{UserID: "alice", PrivateKey: "PEM"}

	h := "remote.example"
	shared := "https://remote.example/inbox"
	userRepo.Users["r1"] = &model.User{ID: "r1", Host: &h, SharedInbox: &shared}

	note := &model.Note{ID: "n1", UserID: "alice", Visibility: model.NoteVisibilityFollowers}
	hook.OnNoteDeleted(author, note)

	// Followers 可視性では broadcast しないため、フォロワー 0 人分の 0 件。
	assert.Empty(t, enq.calls)
}

func TestNoteDeleteHook_BroadcastEmptyInboxes(t *testing.T) {
	hook, enq, userRepo, _, keypairRepo := newDeleteHook(t)
	hook.SetUserRepo(userRepo)

	author := &model.User{ID: "alice"}
	userRepo.Users["alice"] = author
	keypairRepo.Keypairs["alice"] = &model.UserKeypair{UserID: "alice", PrivateKey: "PEM"}

	note := &model.Note{ID: "n1", UserID: "alice", Visibility: model.NoteVisibilityPublic}
	hook.OnNoteDeleted(author, note)
	// 既知リモート 0 件で broadcast はスキップされる。
	assert.Empty(t, enq.calls)
}

// failingListInboxes makes ListRemoteInboxes return an error to exercise the
// error-logging branch in the delete delivery hook.
type failingListInboxes struct{ *testutil.MockUserRepository }

func (f *failingListInboxes) ListRemoteInboxes() ([]model.RemoteInbox, error) {
	return nil, assert.AnError
}

func TestNoteDeleteHook_BroadcastListError(t *testing.T) {
	hook, enq, userRepo, _, keypairRepo := newDeleteHook(t)
	hook.SetUserRepo(&failingListInboxes{MockUserRepository: userRepo})

	author := &model.User{ID: "alice"}
	userRepo.Users["alice"] = author
	keypairRepo.Keypairs["alice"] = &model.UserKeypair{UserID: "alice", PrivateKey: "PEM"}

	note := &model.Note{ID: "n1", UserID: "alice", Visibility: model.NoteVisibilityPublic}
	hook.OnNoteDeleted(author, note)
	// エラー時は broadcast されない。
	assert.Empty(t, enq.calls)
}

func TestNoteDeleteHook_BroadcastDeliverError_DoesNotPanic(t *testing.T) {
	hook, _, userRepo, _, _ := newDeleteHook(t)
	hook.SetUserRepo(userRepo)

	author := &model.User{ID: "alice"}
	userRepo.Users["alice"] = author
	// keypair なしで DeliverActivity 側が署名失敗 → panic しないこと。
	h := "remote.example"
	shared := "https://remote.example/inbox"
	userRepo.Users["r1"] = &model.User{ID: "r1", Host: &h, SharedInbox: &shared}

	note := &model.Note{ID: "n1", UserID: "alice", Visibility: model.NoteVisibilityPublic}
	hook.OnNoteDeleted(author, note)
}

// フォロワーは既知リモートの部分集合 (どちらも sharedInbox 優先で解決する) なので、
// フォロワー配送と broadcast を重ねると全フォロワーが同じ Delete を同じ URL に
// 2 回受け取る (#2575)。
func TestNoteDeleteHook_DoesNotDoubleDeliverToFollowers(t *testing.T) {
	hook, enq, userRepo, followingRepo, keypairRepo := newDeleteHook(t)
	hook.SetUserRepo(userRepo)

	author := &model.User{ID: "alice", Username: "alice"}
	userRepo.Users["alice"] = author
	keypairRepo.Keypairs["alice"] = &model.UserKeypair{UserID: "alice", PrivateKey: "PEM"}

	// フォロワー (shared inbox 持ち) と、フォローしていない既知リモート。
	host := "remote.example"
	sharedInbox := "https://remote.example/inbox"
	follower := &model.User{ID: "bob", Host: &host, SharedInbox: &sharedInbox}
	userRepo.Users["bob"] = follower
	otherHost := "other.example"
	otherInbox := "https://other.example/users/x/inbox"
	userRepo.Users["carol"] = &model.User{ID: "carol", Host: &otherHost, Inbox: &otherInbox}
	followingRepo.RemoteInboxes["alice"] = []string{sharedInbox}
	followingRepo.RemoteSharedInboxes[sharedInbox] = true

	hook.OnNoteDeleted(author, &model.Note{
		ID: "n1", UserID: "alice", Visibility: model.NoteVisibilityPublic,
	})

	byInbox := map[string]int{}
	shared := map[string]bool{}
	for _, c := range enq.calls {
		byInbox[c.Inbox]++
		shared[c.Inbox] = c.IsSharedInbox
	}
	assert.Equal(t, 1, byInbox[sharedInbox], "フォロワーの inbox は 1 回だけ")
	assert.Equal(t, 1, byInbox[otherInbox], "フォローしていない既知リモートにも届く")
	// **shared フラグを落とさない。** 410 Gone でインスタンス全体を suspend する
	// 判定 (#1811) がこれを見る。従来 broadcast は常に false で送っていた。
	assert.True(t, shared[sharedInbox], "shared inbox は IsSharedInbox=true")
	assert.False(t, shared[otherInbox], "個別 inbox は IsSharedInbox=false")
}

// 一覧が引けないときはフォロワーには届ける。**何も送らないのは今より悪い。**
func TestNoteDeleteHook_FallsBackToFollowersWhenListFails(t *testing.T) {
	hook, enq, userRepo, followingRepo, keypairRepo := newDeleteHook(t)
	hook.SetUserRepo(&failingListInboxes{MockUserRepository: userRepo})

	author := &model.User{ID: "alice", Username: "alice"}
	userRepo.Users["alice"] = author
	keypairRepo.Keypairs["alice"] = &model.UserKeypair{UserID: "alice", PrivateKey: "PEM"}
	followingRepo.RemoteInboxes["alice"] = []string{"https://remote.example/inbox"}

	hook.OnNoteDeleted(author, &model.Note{
		ID: "n1", UserID: "alice", Visibility: model.NoteVisibilityPublic,
	})

	require.Len(t, enq.calls, 1)
	assert.Equal(t, "https://remote.example/inbox", enq.calls[0].Inbox)
}

// Public / Home 以外は broadcast しないので、従来どおりフォロワーだけ。
func TestNoteDeleteHook_NonPublicStaysFollowersOnly(t *testing.T) {
	hook, enq, userRepo, followingRepo, keypairRepo := newDeleteHook(t)
	hook.SetUserRepo(userRepo)

	author := &model.User{ID: "alice", Username: "alice"}
	userRepo.Users["alice"] = author
	keypairRepo.Keypairs["alice"] = &model.UserKeypair{UserID: "alice", PrivateKey: "PEM"}
	otherHost := "other.example"
	otherInbox := "https://other.example/users/x/inbox"
	userRepo.Users["carol"] = &model.User{ID: "carol", Host: &otherHost, Inbox: &otherInbox}
	followingRepo.RemoteInboxes["alice"] = []string{"https://remote.example/inbox"}

	hook.OnNoteDeleted(author, &model.Note{
		ID: "n1", UserID: "alice", Visibility: model.NoteVisibilityFollowers,
	})

	require.Len(t, enq.calls, 1)
	assert.Equal(t, "https://remote.example/inbox", enq.calls[0].Inbox)
}

// 既知リモートが 0 件でもフォロワー配送は走る。**0 件を「送るものが無い」と
// 解釈すると、片方のクエリだけが変わったときに黙って配送が消える。**
func TestNoteDeleteHook_EmptyBroadcastListStillDeliversToFollowers(t *testing.T) {
	hook, enq, userRepo, followingRepo, keypairRepo := newDeleteHook(t)
	hook.SetUserRepo(userRepo)

	author := &model.User{ID: "alice", Username: "alice"}
	userRepo.Users["alice"] = author
	keypairRepo.Keypairs["alice"] = &model.UserKeypair{UserID: "alice", PrivateKey: "PEM"}
	// userRepo にリモートユーザーを入れないので ListRemoteInboxes は空。
	followingRepo.RemoteInboxes["alice"] = []string{"https://remote.example/inbox"}

	hook.OnNoteDeleted(author, &model.Note{
		ID: "n1", UserID: "alice", Visibility: model.NoteVisibilityPublic,
	})

	require.Len(t, enq.calls, 1)
	assert.Equal(t, "https://remote.example/inbox", enq.calls[0].Inbox)
}
