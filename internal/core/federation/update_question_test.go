package federation_test

import (
	"testing"

	"github.com/shiroha-a/mk/internal/activitypub"
	"github.com/shiroha-a/mk/internal/core/federation"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newResolverWithPoll wires a resolver with user/note/poll repos for the
// inbound Update(Question) tests (#1779)。
func newResolverWithPoll(t *testing.T) (*federation.Resolver, *testutil.MockUserRepository, *testutil.MockNoteRepository, *testutil.MockPollRepository) {
	t.Helper()
	userRepo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	pollRepo := testutil.NewMockPollRepository()
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	r := federation.NewResolver(userRepo, noteRepo, urls, &stubFetcher{}, idGen)
	r.SetPollRepo(pollRepo)
	return r, userRepo, noteRepo, pollRepo
}

func seedRemotePoll(t *testing.T, userRepo *testutil.MockUserRepository, noteRepo *testutil.MockNoteRepository, pollRepo *testutil.MockPollRepository) (authorURI, noteURI string) {
	t.Helper()
	host := "remote.example"
	authorURI = "https://remote.example/users/alice"
	noteURI = "https://remote.example/notes/q1"
	userRepo.Users["alice"] = &model.User{ID: "alice", URI: &authorURI, Host: &host}
	noteRepo.Notes["n1"] = &model.Note{ID: "n1", UserID: "alice", UserHost: &host, URI: &noteURI, HasPoll: true}
	require.NoError(t, pollRepo.Create(&model.Poll{NoteID: "n1", Choices: []string{"A", "B"}, Votes: []int64{1, 2}}))
	return authorURI, noteURI
}

// #1779: inbound Update(Question) は choice 名で照合して vote 数を refresh する。
func TestUpdateRemoteQuestion_RefreshesVotes(t *testing.T) {
	r, userRepo, noteRepo, pollRepo := newResolverWithPoll(t)
	authorURI, _ := seedRemotePoll(t, userRepo, noteRepo, pollRepo)

	// 順序を入れ替えても name 一致で更新される。
	obj := []byte(`{"id":"https://remote.example/notes/q1","type":"Question","oneOf":[{"name":"B","replies":{"totalItems":5}},{"name":"A","replies":{"totalItems":10}}]}`)
	require.NoError(t, r.UpdateRemoteQuestion(obj, authorURI))

	got, err := pollRepo.FindByNoteID("n1")
	require.NoError(t, err)
	assert.Equal(t, []int64{10, 5}, []int64(got.Votes))
}

// anyOf (複数選択) でも refresh される。
func TestUpdateRemoteQuestion_AnyOf(t *testing.T) {
	r, userRepo, noteRepo, pollRepo := newResolverWithPoll(t)
	authorURI, _ := seedRemotePoll(t, userRepo, noteRepo, pollRepo)

	obj := []byte(`{"id":"https://remote.example/notes/q1","type":"Question","anyOf":[{"name":"A","replies":{"totalItems":7}},{"name":"B","replies":{"totalItems":3}}]}`)
	require.NoError(t, r.UpdateRemoteQuestion(obj, authorURI))
	got, _ := pollRepo.FindByNoteID("n1")
	assert.Equal(t, []int64{7, 3}, []int64(got.Votes))
}

// 別 actor からの Update は拒否され votes は不変 (attribution check)。
func TestUpdateRemoteQuestion_RefusesDifferentActor(t *testing.T) {
	r, userRepo, noteRepo, pollRepo := newResolverWithPoll(t)
	seedRemotePoll(t, userRepo, noteRepo, pollRepo)

	obj := []byte(`{"id":"https://remote.example/notes/q1","type":"Question","oneOf":[{"name":"A","replies":{"totalItems":99}}]}`)
	require.NoError(t, r.UpdateRemoteQuestion(obj, "https://evil.example/users/mallory"))
	got, _ := pollRepo.FindByNoteID("n1")
	assert.Equal(t, []int64{1, 2}, []int64(got.Votes), "他 user からの Update は無視する")
}

// ローカル著者の poll は remote Update で変更しない。
func TestUpdateRemoteQuestion_SkipsLocalNote(t *testing.T) {
	r, _, noteRepo, pollRepo := newResolverWithPoll(t)
	noteURI := "https://example.com/notes/q1"
	noteRepo.Notes["n1"] = &model.Note{ID: "n1", UserID: "localuser", URI: &noteURI, HasPoll: true} // UserHost nil = local
	require.NoError(t, pollRepo.Create(&model.Poll{NoteID: "n1", Choices: []string{"A"}, Votes: []int64{3}}))

	obj := []byte(`{"id":"https://example.com/notes/q1","type":"Question","oneOf":[{"name":"A","replies":{"totalItems":99}}]}`)
	require.NoError(t, r.UpdateRemoteQuestion(obj, "https://example.com/users/localuser"))
	got, _ := pollRepo.FindByNoteID("n1")
	assert.Equal(t, []int64{3}, []int64(got.Votes), "ローカル poll は remote Update で変更されない")
}

// 未取得 note / 不正 object / choice 無し / 変化無しは no-op (error にしない)。
func TestUpdateRemoteQuestion_NoOpCases(t *testing.T) {
	r, userRepo, noteRepo, pollRepo := newResolverWithPoll(t)
	// 未知 note
	assert.NoError(t, r.UpdateRemoteQuestion([]byte(`{"id":"https://remote.example/notes/ghost","type":"Question","oneOf":[{"name":"A","replies":{"totalItems":1}}]}`), "https://remote.example/users/alice"))
	// id 欠落
	assert.NoError(t, r.UpdateRemoteQuestion([]byte(`{"type":"Question"}`), "https://remote.example/users/alice"))
	// 不正 JSON
	assert.NoError(t, r.UpdateRemoteQuestion([]byte(`not json`), "x"))

	authorURI, _ := seedRemotePoll(t, userRepo, noteRepo, pollRepo)
	// choice 無し → no-op
	require.NoError(t, r.UpdateRemoteQuestion([]byte(`{"id":"https://remote.example/notes/q1","type":"Question"}`), authorURI))
	// 既存 votes と同値 → 変化無しで no-op (votes 不変)
	require.NoError(t, r.UpdateRemoteQuestion([]byte(`{"id":"https://remote.example/notes/q1","type":"Question","oneOf":[{"name":"A","replies":{"totalItems":1}},{"name":"B","replies":{"totalItems":2}}]}`), authorURI))
	got, _ := pollRepo.FindByNoteID("n1")
	assert.Equal(t, []int64{1, 2}, []int64(got.Votes))
}

// ExtractActivityID は top-level id を unwrap+Normalize 後に返す。
func TestExtractActivityID(t *testing.T) {
	assert.Equal(t, "https://remote.example/activities/1",
		federation.ExtractActivityID([]byte(`{"id":"https://remote.example/activities/1","type":"Follow","actor":"https://remote.example/users/a"}`)))
	assert.Empty(t, federation.ExtractActivityID([]byte(`{"type":"Follow","actor":"https://remote.example/users/a"}`)))
	assert.Empty(t, federation.ExtractActivityID([]byte(`not json`)))
}

// #1779: inbound Update(Question) を Process 経由 (handleUpdate dispatch) で処理し
// poll の vote 数を refresh する。
func TestProcess_UpdateQuestion_RefreshesVotes(t *testing.T) {
	userRepo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	pollRepo := testutil.NewMockPollRepository()
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	r := federation.NewResolver(userRepo, noteRepo, urls, &stubFetcher{}, idGen)
	r.SetPollRepo(pollRepo)
	authorURI, _ := seedRemotePoll(t, userRepo, noteRepo, pollRepo)

	p := federation.NewProcessor(r, nil, nil, nil, userRepo, noteRepo)
	body := []byte(`{"id":"https://remote.example/updates/1","type":"Update","actor":"` + authorURI + `","object":{"id":"https://remote.example/notes/q1","type":"Question","oneOf":[{"name":"A","replies":{"totalItems":8}},{"name":"B","replies":{"totalItems":4}}]}}`)
	require.NoError(t, p.Process(body))

	got, _ := pollRepo.FindByNoteID("n1")
	assert.Equal(t, []int64{8, 4}, []int64(got.Votes))
}
