package federation

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shiroha-a/mk/internal/activitypub"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/testutil"
)

// 著者がピン留めしている投稿が inbox に直接 Create で届くと、取り込みが
// created=false を返して通知とチャートのフックが飛ぶ (#2686)。
//
// featured 経路の in-flight guard は #2684 で入れた resolvingNotes
// (singleflight の鍵) しか見ておらず、これは resolveNoteOnce でしか書かれない。
// **inbox 直送は resolveNoteOnce を通らない**ので guard が空振りし、同じ note を
// もう一度 fetch して内側の ingest が先に行を作る。外側の Create が UNIQUE に
// 当たり、dedup 経路で created=false になる。
func TestIngestNote_PinnedNoteViaInboxKeepsCreated(t *testing.T) {
	const (
		actorURI = "https://remote.example/users/kate"
		featured = "https://remote.example/users/kate/collections/featured"
		noteURI  = "https://remote.example/notes/k1"
	)
	userRepo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, err := id.NewGenerator("aidx")
	require.NoError(t, err)

	fetcher := &countingFetcher{docs: map[string]string{
		actorURI: featuredActorDoc("remote.example", "kate", featured),
		featured: `{"@context":"https://www.w3.org/ns/activitystreams","id":"` + featured +
			`","type":"OrderedCollection","orderedItems":["` + noteURI + `"]}`,
		noteURI: plainNoteDoc(noteURI, actorURI),
	}}
	r := NewResolver(userRepo, noteRepo, urls, fetcher, idGen)
	// **pinningRepo を配線しないと updateFeatured が即 return する。**
	// 配線を忘れると featured 経路そのものが走らず、テストが素通りする。
	r.SetPinningRepo(testutil.NewMockUserNotePiningRepository(), idGen)

	// inbox 直送 (handleCreate 相当)。resolveNoteOnce を通らない経路。
	note, created, err := r.IngestNoteWithCreated([]byte(plainNoteDoc(noteURI, actorURI)), actorURI)
	require.NoError(t, err)
	require.NotNil(t, note)

	// **created=false になると通知とチャートのフックが飛ぶ。**
	assert.True(t, created, "inbox 直送の取り込みが created=false になっている (通知が落ちる)")
	assert.Equal(t, 0, fetcher.count(noteURI), "既に手元にある note を再取得しないこと")
	assert.Len(t, noteRepo.Notes, 1, "note を二重に作らないこと")
}

// countingFetcher serves fixtures and counts fetches per URI.
type countingFetcher struct {
	docs  map[string]string
	calls []string
}

func (f *countingFetcher) FetchObject(uri string) ([]byte, error) {
	f.calls = append(f.calls, uri)
	if b, ok := f.docs[uri]; ok {
		return []byte(b), nil
	}
	return nil, assert.AnError
}

func (f *countingFetcher) count(uri string) int {
	n := 0
	for _, c := range f.calls {
		if c == uri {
			n++
		}
	}
	return n
}

// ピン留めが**別名 URL** で載っていると、featured 側の in-flight 判定が空振りして
// 同じ note をもう一度取り込み、inbox 直送の created が false に落ちる (#2695)。
//
// #2686 で潰したのは取得 URI と document の id が一致する形だけ。featured が
// `/@user/x` を載せていて id が `/notes/x` のとき、チェーンに載っているのは id 側
// なので取得 URI では引けない。id は fetch しないと分からないので、判定は
// resolveNoteOnce の**取得後**にもう一度要る。
func TestIngestNote_PinnedNoteViaInboxKeepsCreated_AliasURI(t *testing.T) {
	const (
		actorURI = "https://remote.example/users/lena"
		featured = "https://remote.example/users/lena/collections/featured"
		noteURI  = "https://remote.example/notes/l1"
		aliasURI = "https://remote.example/@lena/l1"
	)
	userRepo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, err := id.NewGenerator("aidx")
	require.NoError(t, err)

	fetcher := &countingFetcher{docs: map[string]string{
		actorURI: featuredActorDoc("remote.example", "lena", featured),
		featured: `{"@context":"https://www.w3.org/ns/activitystreams","id":"` + featured +
			`","type":"OrderedCollection","orderedItems":["` + aliasURI + `"]}`,
		// **別名で引いても document の id は canonical。** ここが両者を食い違わせる。
		aliasURI: plainNoteDoc(noteURI, actorURI),
	}}
	r := NewResolver(userRepo, noteRepo, urls, fetcher, idGen)
	// pinningRepo が無いと updateFeatured が即 return してテストが素通りする。
	r.SetPinningRepo(testutil.NewMockUserNotePiningRepository(), idGen)

	note, created, err := r.IngestNoteWithCreated([]byte(plainNoteDoc(noteURI, actorURI)), actorURI)
	require.NoError(t, err)
	require.NotNil(t, note)

	assert.True(t, created,
		"別名 URL のピンで inbox 直送の取り込みが created=false になっている (通知が落ちる、#2695)")
	assert.Len(t, noteRepo.Notes, 1, "note を二重に作らないこと")
	// 別名を 1 度取得するのは避けられない (id はそれでしか分からない)。
	// 2 度目が出るなら判定が効いていない。
	assert.Equal(t, 1, fetcher.count(aliasURI), "別名の取得は 1 度だけ")
	assert.Equal(t, 0, fetcher.count(noteURI), "既に手元にある note を再取得しないこと")
}

// newAliasEnv builds a resolver whose alias URI serves a document with a
// different (canonical) id.
func newAliasEnv(t *testing.T, actorURI, name, aliasURI, noteURI string) (*Resolver, *testutil.MockNoteRepository) {
	t.Helper()
	noteRepo := testutil.NewMockNoteRepository()
	idGen, err := id.NewGenerator("aidx")
	require.NoError(t, err)
	fetcher := &countingFetcher{docs: map[string]string{
		actorURI: featuredActorDoc("remote.example", name, ""),
		aliasURI: plainNoteDoc(noteURI, actorURI),
	}}
	r := NewResolver(testutil.NewMockUserRepository(), noteRepo,
		activitypub.NewURLBuilder("https://example.com"), fetcher, idGen)
	return r, noteRepo
}

// 取得後の判定は **best-effort な枝だけ**に効くこと (#2695)。
//
// featured のピンをこの回だけ落とすのは、次の actor 更新で拾い直せるから許せる。
// 同じことを引用経路でやると `renoteId` が nil のまま保存され、再取り込みは
// resolveNoteOnce も ingestNoteWithCreated も FindByURI で早期 return するので
// **恒久的に失われる** (#2684 review R2 HIGH-1 と同じ型)。直そうとしたバグより
// 実害が大きくなるため、`!chain.mayWait()` の限定を外してはいけない。
func TestResolveNoteOnce_AncestorIngestingGateIsBestEffortOnly(t *testing.T) {
	const (
		actorURI = "https://remote.example/users/mira"
		aliasURI = "https://remote.example/@mira/m1"
		noteURI  = "https://remote.example/notes/m1"
	)
	// 祖先が noteURI を取り込み中のチェーン。値が入っているのが「祖先が載せた」印。
	ancestor := func() *resolveChain {
		return (&resolveChain{}).with(noteURI, noteURI).ensureTree()
	}

	t.Run("quote path ingests anyway", func(t *testing.T) {
		r, noteRepo := newAliasEnv(t, actorURI, "mira", aliasURI, noteURI)
		note, err := r.resolveNoteOnce(aliasURI, 0, false, false, ancestor())
		require.NoError(t, err, "引用経路で諦めている (renoteId を恒久的に落とす)")
		require.NotNil(t, note)
		assert.Len(t, noteRepo.Notes, 1)
	})

	t.Run("featured path gives up", func(t *testing.T) {
		r, noteRepo := newAliasEnv(t, actorURI, "mira", aliasURI, noteURI)
		note, err := r.resolveNoteOnce(aliasURI, 0, false, false, ancestor().asBestEffort())
		require.ErrorIs(t, err, ErrNoteAncestorIngesting)
		assert.Nil(t, note)
		assert.Empty(t, noteRepo.Notes, "祖先の取り込みを横取りしないこと")
	})

	// 既に行があるなら諦めずにそれを返す。ReplaceByUser は delete-then-insert
	// なので、ここで落とすと**生きているピンが消える** (#2685 review MEDIUM-1)。
	t.Run("featured path returns an existing row", func(t *testing.T) {
		r, noteRepo := newAliasEnv(t, actorURI, "mira", aliasURI, noteURI)
		user, err := r.ResolveActor(actorURI)
		require.NoError(t, err)
		uri := noteURI
		require.NoError(t, noteRepo.Create(&model.Note{ID: "9mira0000000000000000", UserID: user.ID, URI: &uri}))

		note, err := r.resolveNoteOnce(aliasURI, 0, false, false, ancestor().asBestEffort())
		require.NoError(t, err)
		require.NotNil(t, note)
		assert.Equal(t, "9mira0000000000000000", note.ID)
	})
}

// チェーン固有の判定を**相乗りしただけの追従側へ配らない**こと (#2695)。
//
// group は先頭の返り値を追従側にも渡す。best-effort な先頭が
// ErrNoteAncestorIngesting で降りたとき、それをそのまま配ると身に覚えのない
// 理由で別チェーンの解決が落ちる (引用経路なら renoteId を恒久的に失う)。
func TestJoinResolveOpt_JoinerRedoesAChainLocalGiveUp(t *testing.T) {
	r := &Resolver{}
	var g resolveGroup

	leaderIn := make(chan struct{})
	leaderGo := make(chan struct{})
	leaderChain := (&resolveChain{}).ensureTree().asBestEffort()
	go func() {
		_, _ = r.joinResolveOpt(&g, leaderChain, noteWaitKey("k"), "k", false, func() (any, error) {
			close(leaderIn)
			<-leaderGo
			return nil, ErrNoteAncestorIngesting
		})
	}()
	<-leaderIn

	joinerRan := 0
	joinerDone := make(chan struct{})
	var got any
	var gotErr error
	go func() {
		defer close(joinerDone)
		got, gotErr = r.joinResolveOpt(&g, (&resolveChain{}).ensureTree(), noteWaitKey("k"), "k", true,
			func() (any, error) {
				joinerRan++
				return "resolved", nil
			})
	}()
	// 追従側が先頭にぶら下がってから先頭を返す。
	time.Sleep(50 * time.Millisecond)
	close(leaderGo)

	select {
	case <-joinerDone:
	case <-time.After(15 * time.Second):
		t.Fatal("追従側が返らない")
	}
	require.NoError(t, gotErr, "先頭のチェーン固有の判定を追従側が受け取っている (#2695)")
	assert.Equal(t, "resolved", got)
	assert.Equal(t, 1, joinerRan, "追従側は 1 度だけやり直すこと")
}
