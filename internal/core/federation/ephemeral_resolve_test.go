package federation_test

import (
	"context"
	"testing"

	"github.com/shiroha-a/mk/internal/activitypub"
	"github.com/shiroha-a/mk/internal/core/federation"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeSink captures what the ephemeral path would write to Redis.
type fakeSink struct {
	notes   map[string]*model.Note
	authors map[string]*model.User
	byURI   map[string]string
	putErr  error
}

func newFakeSink() *fakeSink {
	return &fakeSink{
		notes:   map[string]*model.Note{},
		authors: map[string]*model.User{},
		byURI:   map[string]string{},
	}
}

func (f *fakeSink) PutNote(_ context.Context, n *model.Note, author *model.User) error {
	if f.putErr != nil {
		return f.putErr
	}
	f.notes[n.ID] = n
	f.authors[author.ID] = author
	if author.URI != nil {
		f.byURI[*author.URI] = author.ID
	}
	return nil
}

func (f *fakeSink) UserIDByURI(_ context.Context, uri string) (string, error) {
	return f.byURI[uri], nil
}

func (f *fakeSink) GetUser(_ context.Context, id string) (*model.User, error) {
	return f.authors[id], nil
}

// docFetcher serves a different document per URI so that a note fetch and the
// subsequent actor fetch can both be satisfied.
type docFetcher struct{ docs map[string]string }

func (d *docFetcher) FetchObject(uri string) ([]byte, error) {
	if body, ok := d.docs[uri]; ok {
		return []byte(body), nil
	}
	return nil, assertNoDoc{uri}
}

type assertNoDoc struct{ uri string }

func (e assertNoDoc) Error() string { return "no fixture for " + e.uri }

// ephResolverDocs builds a Resolver serving documents per URI.
func ephResolverDocs(t *testing.T, docs map[string]string) (*federation.Resolver, *fakeSink, *testutil.MockNoteRepository, *testutil.MockUserRepository) {
	t.Helper()
	userRepo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, err := id.NewGenerator("aidx")
	require.NoError(t, err)
	r := federation.NewResolver(userRepo, noteRepo, urls, &docFetcher{docs: docs}, idGen)
	sink := newFakeSink()
	r.SetEphemeralSink(sink)
	return r, sink, noteRepo, userRepo
}

// ephResolver builds a Resolver whose fetcher returns the given document for
// every request, with the ephemeral sink wired.
func ephResolver(t *testing.T, body string) (*federation.Resolver, *fakeSink, *testutil.MockNoteRepository, *testutil.MockUserRepository) {
	t.Helper()
	userRepo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, err := id.NewGenerator("aidx")
	require.NoError(t, err)
	r := federation.NewResolver(userRepo, noteRepo, urls, &stubFetcher{body: []byte(body)}, idGen)
	sink := newFakeSink()
	r.SetEphemeralSink(sink)
	return r, sink, noteRepo, userRepo
}

const ephActorDoc = `{
	"@context": "https://www.w3.org/ns/activitystreams",
	"id": "https://remote.example/users/alice",
	"type": "Person",
	"preferredUsername": "alice",
	"inbox": "https://remote.example/users/alice/inbox",
	"publicKey": {"publicKeyPem": "PEM"}
}`

// TestResolveNoteEphemeral_DoesNotTouchNoteRepo は本機能の核心。
// リレー由来の投稿が DB に 1 行も入らないこと。
func TestResolveNoteEphemeral_DoesNotTouchNoteRepo(t *testing.T) {
	doc := `{
		"@context": "https://www.w3.org/ns/activitystreams",
		"id": "https://remote.example/notes/1",
		"type": "Note",
		"attributedTo": "https://remote.example/users/alice",
		"content": "relay hello",
		"to": ["https://www.w3.org/ns/activitystreams#Public"]
	}`
	r, sink, noteRepo, userRepo := ephResolverDocs(t, map[string]string{
		"https://remote.example/notes/1":     doc,
		"https://remote.example/users/alice": ephActorDoc,
	})

	note, err := r.ResolveNoteEphemeral("https://remote.example/notes/1")
	require.NoError(t, err)
	require.NotNil(t, note)

	assert.Empty(t, noteRepo.Notes, "ephemeral 経路では note 行を作らない")
	require.Len(t, sink.notes, 1, "ephemeral store に入っていること")
	assert.Equal(t, note.ID, sink.notes[note.ID].ID)

	// 著者は Phase 1 では通常経路で解決するため DB に入る。ノートが入らない
	// ことが本 Phase の目的で、著者の ephemeral 化は Phase 2 で扱う。
	assert.NotEmpty(t, userRepo.Users, "著者は Phase 1 では DB に入る")
}

// 既に DB に在る URI は DB 行をそのまま返す (直接配送で来ていた場合)。
func TestResolveNoteEphemeral_ExistingDBRowWins(t *testing.T) {
	r, sink, noteRepo, _ := ephResolver(t, ephActorDoc)
	uri := "https://remote.example/notes/1"
	noteRepo.Notes["existing"] = &model.Note{ID: "existing", UserID: "u1", URI: &uri}

	got, err := r.ResolveNoteEphemeral(uri)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "existing", got.ID)
	assert.Empty(t, sink.notes, "DB に在るなら ephemeral には入れない")
}

// sink 未配線なら従来どおり DB 経路にフォールバックする。
func TestResolveNoteEphemeral_NoSinkFallsBackToDB(t *testing.T) {
	userRepo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	r := federation.NewResolver(userRepo, noteRepo, urls, &stubFetcher{body: []byte(ephActorDoc)}, idGen)

	uri := "https://remote.example/notes/1"
	noteRepo.Notes["existing"] = &model.Note{ID: "existing", UserID: "u1", URI: &uri}

	got, err := r.ResolveNoteEphemeral(uri)
	require.NoError(t, err)
	assert.Equal(t, "existing", got.ID)
}

// --- 検証が ephemeral モードでも働くこと ---
//
// ここが抜けると、リレー経由が連合の安全性検証を迂回する経路になる。
// 検証は通常経路と共通のコードを通しているが、モード追加で分岐が入って
// いないことを実際に確かめる。

func TestResolveNoteEphemeral_RejectsHostMismatch(t *testing.T) {
	// note.id の host と attributedTo の host が食い違う cross-host forge。
	doc := `{
		"@context": "https://www.w3.org/ns/activitystreams",
		"id": "https://remote.example/notes/1",
		"type": "Note",
		"attributedTo": "https://evil.example/users/mallory",
		"content": "forged",
		"to": ["https://www.w3.org/ns/activitystreams#Public"]
	}`
	r, sink, noteRepo, _ := ephResolver(t, doc)

	_, err := r.ResolveNoteEphemeral("https://remote.example/notes/1")
	assert.Error(t, err, "host 不一致は ephemeral でも拒否されること")
	assert.Empty(t, sink.notes)
	assert.Empty(t, noteRepo.Notes)
}

func TestResolveNoteEphemeral_RejectsMissingContext(t *testing.T) {
	// fetch した standalone document は AS @context 必須 (#1828)。
	doc := `{
		"id": "https://remote.example/notes/1",
		"type": "Note",
		"attributedTo": "https://remote.example/users/alice",
		"content": "no context"
	}`
	r, sink, _, _ := ephResolver(t, doc)

	_, err := r.ResolveNoteEphemeral("https://remote.example/notes/1")
	assert.Error(t, err, "@context 欠落は ephemeral でも拒否されること")
	assert.Empty(t, sink.notes)
}

func TestResolveNoteEphemeral_RejectsFragmentURI(t *testing.T) {
	r, sink, _, _ := ephResolver(t, ephActorDoc)

	_, err := r.ResolveNoteEphemeral("https://remote.example/notes/1#frag")
	assert.Error(t, err, "fragment 付き URI は ephemeral でも拒否されること")
	assert.Empty(t, sink.notes)
}

func TestResolveNoteEphemeral_RejectsRequestHostMismatch(t *testing.T) {
	// 要求した URI の host と返ってきた id の host が違う (redirect 誘導)。
	doc := `{
		"@context": "https://www.w3.org/ns/activitystreams",
		"id": "https://other.example/notes/1",
		"type": "Note",
		"attributedTo": "https://other.example/users/alice",
		"content": "redirected",
		"to": ["https://www.w3.org/ns/activitystreams#Public"]
	}`
	r, sink, _, _ := ephResolver(t, doc)

	_, err := r.ResolveNoteEphemeral("https://remote.example/notes/1")
	assert.Error(t, err, "request host 不一致は ephemeral でも拒否されること")
	assert.Empty(t, sink.notes)
}

// 著者解決の順序: DB に居ればその実 ID を使う。
// これを飛ばすと、ミュート済みの著者が ephemeral 側の別 ID で並び続ける。
func TestResolveNoteAuthor_PrefersExistingDBUser(t *testing.T) {
	r, sink, noteRepo, userRepo := ephResolver(t, ephActorDoc)
	actorURI := "https://remote.example/users/alice"
	host := "remote.example"
	userRepo.Users["real"] = &model.User{ID: "real", Username: "alice", Host: &host, URI: &actorURI}

	noteURI := "https://remote.example/notes/1"
	noteRepo.Notes["n"] = &model.Note{ID: "n", UserID: "real", URI: &noteURI}

	got, err := r.ResolveNoteEphemeral(noteURI)
	require.NoError(t, err)
	assert.Equal(t, "real", got.UserID)
	assert.Empty(t, sink.notes)
}
