package ephemeral_test

import (
	"context"
	"log"
	"os"
	"testing"
	"time"

	"github.com/shiroha-a/mk/internal/core/ephemeral"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var testRedis *testutil.TestRedis

func TestMain(m *testing.M) {
	ctx := context.Background()
	var err error
	testRedis, err = testutil.SetupRedis(ctx)
	if err != nil {
		log.Fatalf("failed to setup redis: %v", err)
	}
	code := m.Run()
	testRedis.Teardown(ctx)
	os.Exit(code)
}

func newStore(t *testing.T, ttl time.Duration) *ephemeral.Store {
	t.Helper()
	testutil.SkipIfNoDocker(t)
	testRedis.FlushAll(context.Background())
	return ephemeral.NewStore(testRedis.Client, "example.com:", func() time.Duration { return ttl })
}

func strptr(s string) *string { return &s }

func sampleNote(id, uri, userID string) *model.Note {
	host := "remote.example"
	return &model.Note{
		ID: id, UserID: userID, UserHost: &host, URI: strptr(uri),
		Text: strptr("hello"), Visibility: model.NoteVisibilityPublic,
	}
}

func sampleUser(id, uri string) *model.User {
	host := "remote.example"
	return &model.User{
		ID: id, Username: "alice", UsernameLower: "alice",
		Host: &host, URI: strptr(uri),
	}
}

func TestStore_PutAndGetNote(t *testing.T) {
	s := newStore(t, time.Minute)
	ctx := context.Background()

	n := sampleNote("n1", "https://remote.example/notes/1", "u1")
	u := sampleUser("u1", "https://remote.example/users/alice")
	require.NoError(t, s.PutNote(ctx, n, u))

	got, err := s.GetNote(ctx, "n1")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "n1", got.ID)
	assert.Equal(t, "hello", *got.Text)
	// 下流の ApplyFilter / PackNotes は note.User を見るので詰まっていること。
	require.NotNil(t, got.User)
	assert.Equal(t, "u1", got.User.ID)
	assert.Equal(t, "alice", got.User.Username)
}

func TestStore_GetNote_Missing(t *testing.T) {
	s := newStore(t, time.Minute)
	got, err := s.GetNote(context.Background(), "nope")
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestStore_GetNotes_SkipsMissingAndKeepsOrder(t *testing.T) {
	s := newStore(t, time.Minute)
	ctx := context.Background()
	u := sampleUser("u1", "https://remote.example/users/alice")
	for _, id := range []string{"n1", "n3"} {
		require.NoError(t, s.PutNote(ctx, sampleNote(id, "https://remote.example/notes/"+id, "u1"), u))
	}

	got, err := s.GetNotes(ctx, []string{"n1", "n2", "n3"})
	require.NoError(t, err)
	require.Len(t, got, 2, "存在しない ID は詰めて返す")
	assert.Equal(t, "n1", got[0].ID)
	assert.Equal(t, "n3", got[1].ID)
}

// 同じ著者の連投で著者 JSON が重複保存されないこと (note 側から User を外す)。
func TestStore_SharesAuthorAcrossNotes(t *testing.T) {
	s := newStore(t, time.Minute)
	ctx := context.Background()
	u := sampleUser("u1", "https://remote.example/users/alice")
	require.NoError(t, s.PutNote(ctx, sampleNote("n1", "https://remote.example/notes/1", "u1"), u))
	require.NoError(t, s.PutNote(ctx, sampleNote("n2", "https://remote.example/notes/2", "u1"), u))

	got, err := s.GetNotes(ctx, []string{"n1", "n2"})
	require.NoError(t, err)
	require.Len(t, got, 2)
	for _, n := range got {
		require.NotNil(t, n.User)
		assert.Equal(t, "u1", n.User.ID)
	}
}

// URI から ID を引けること。同じ著者の 2 件目で ID を再利用するために要る。
func TestStore_LookupIDByURI(t *testing.T) {
	s := newStore(t, time.Minute)
	ctx := context.Background()
	noteURI := "https://remote.example/notes/1"
	userURI := "https://remote.example/users/alice"
	require.NoError(t, s.PutNote(ctx, sampleNote("n1", noteURI, "u1"), sampleUser("u1", userURI)))

	nid, err := s.NoteIDByURI(ctx, noteURI)
	require.NoError(t, err)
	assert.Equal(t, "n1", nid)

	uid, err := s.UserIDByURI(ctx, userURI)
	require.NoError(t, err)
	assert.Equal(t, "u1", uid)
}

func TestStore_LookupIDByURI_Missing(t *testing.T) {
	s := newStore(t, time.Minute)
	ctx := context.Background()
	nid, err := s.NoteIDByURI(ctx, "https://remote.example/notes/ghost")
	require.NoError(t, err)
	assert.Empty(t, nid)
	uid, err := s.UserIDByURI(ctx, "https://remote.example/users/ghost")
	require.NoError(t, err)
	assert.Empty(t, uid)
}

func TestStore_GetUser(t *testing.T) {
	s := newStore(t, time.Minute)
	ctx := context.Background()
	require.NoError(t, s.PutNote(ctx, sampleNote("n1", "https://remote.example/notes/1", "u1"),
		sampleUser("u1", "https://remote.example/users/alice")))

	got, err := s.GetUser(ctx, "u1")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "alice", got.Username)

	missing, err := s.GetUser(ctx, "nope")
	require.NoError(t, err)
	assert.Nil(t, missing)
}

// materialize / 直接配送で DB に入ったときに ephemeral を落とす経路。
func TestStore_DropNote(t *testing.T) {
	s := newStore(t, time.Minute)
	ctx := context.Background()
	uri := "https://remote.example/notes/1"
	require.NoError(t, s.PutNote(ctx, sampleNote("n1", uri, "u1"),
		sampleUser("u1", "https://remote.example/users/alice")))

	require.NoError(t, s.DropNote(ctx, "n1", uri))

	got, err := s.GetNote(ctx, "n1")
	require.NoError(t, err)
	assert.Nil(t, got)
	nid, err := s.NoteIDByURI(ctx, uri)
	require.NoError(t, err)
	assert.Empty(t, nid, "URI 逆引きも消えること (二重取り込みの防止に使うため)")

	// 著者は他のノートが参照しうるので消さない (TTL に任せる)。
	u, err := s.GetUser(ctx, "u1")
	require.NoError(t, err)
	assert.NotNil(t, u)
}

func TestStore_Expires(t *testing.T) {
	s := newStore(t, 300*time.Millisecond)
	ctx := context.Background()
	require.NoError(t, s.PutNote(ctx, sampleNote("n1", "https://remote.example/notes/1", "u1"),
		sampleUser("u1", "https://remote.example/users/alice")))

	require.Eventually(t, func() bool {
		got, err := s.GetNote(ctx, "n1")
		return err == nil && got == nil
	}, 3*time.Second, 100*time.Millisecond, "TTL 切れで消えること")
}

// 閲覧では materialize しないので、読み取り時に TTL を延ばして
// 「開いた直後に期限切れ」を防ぐ。
func TestStore_TouchExtendsTTL(t *testing.T) {
	s := newStore(t, 700*time.Millisecond)
	ctx := context.Background()
	n := sampleNote("n1", "https://remote.example/notes/1", "u1")
	require.NoError(t, s.PutNote(ctx, n, sampleUser("u1", "https://remote.example/users/alice")))

	// TTL の半分ほど待ってから打ち直す。延長が効かなければ元の期限で消える。
	time.Sleep(400 * time.Millisecond)
	require.NoError(t, s.Touch(ctx, n))
	time.Sleep(500 * time.Millisecond)

	got, err := s.GetNote(ctx, "n1")
	require.NoError(t, err)
	assert.NotNil(t, got, "Touch 後は元の TTL を過ぎても生きていること")
}

func TestStore_PutNote_RequiresIDAndURI(t *testing.T) {
	s := newStore(t, time.Minute)
	ctx := context.Background()
	u := sampleUser("u1", "https://remote.example/users/alice")

	assert.Error(t, s.PutNote(ctx, &model.Note{UserID: "u1", URI: strptr("x")}, u), "id 必須")
	assert.Error(t, s.PutNote(ctx, &model.Note{ID: "n1", UserID: "u1"}, u), "uri 必須")
}

// nil レシーバ / nil 引数で落ちないこと (機能無効時は nil を配線するため)。
func TestStore_NilSafe(t *testing.T) {
	var s *ephemeral.Store
	ctx := context.Background()
	assert.NoError(t, s.PutNote(ctx, nil, nil))
	n, err := s.GetNote(ctx, "n1")
	assert.NoError(t, err)
	assert.Nil(t, n)
	assert.NoError(t, s.DropNote(ctx, "n1", "uri"))
	assert.NoError(t, s.Touch(ctx, nil))
}
