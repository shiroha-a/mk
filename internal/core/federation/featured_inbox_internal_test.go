package federation

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shiroha-a/mk/internal/activitypub"
	"github.com/shiroha-a/mk/internal/misc/id"
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
