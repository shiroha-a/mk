package federation

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shiroha-a/mk/internal/activitypub"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
	"github.com/shiroha-a/mk/internal/testutil"
)

// AP の `type` は文字列とその配列のどちらでも来る。配列で来たときに読めないと、
// Note が Note と判定されず取り込みが丸ごと空振りする。
func TestSingleAPType(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "string", raw: `"Note"`, want: "Note"},
		{name: "array takes the first entry", raw: `["Note", "Article"]`, want: "Note"},
		{name: "empty array", raw: `[]`, want: ""},
		{name: "absent", raw: ``, want: ""},
		{name: "unexpected shape", raw: `{"foo": 1}`, want: ""},
		{name: "array of non-strings", raw: `[1, 2]`, want: ""},
		// **先頭だけを見る (upstream `getApType` と同じ)。** 走査方式にすると
		// `[42, "Note"]` が "Note" を返すので、head 方式との差はこの 2 件目で
		// 必ず表面化する (1 件目だけだと走査方式でも同じ結果になり判別
		// できない)。
		{name: "array with trailing non-string", raw: `["Note", 42]`, want: "Note"},
		{name: "array with leading non-string", raw: `[42, "Note"]`, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, singleAPType(json.RawMessage(tt.raw)))
		})
	}
}

// コレクションの要素は URI 文字列か、埋め込まれたオブジェクトのどちらか。
func TestFeaturedItemRef(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		wantURI  string
		wantType string
	}{
		{
			name:    "bare URI has no type",
			raw:     `"https://remote.example/notes/1"`,
			wantURI: "https://remote.example/notes/1",
		},
		{
			name:     "inlined object",
			raw:      `{"id": "https://remote.example/notes/1", "type": "Note"}`,
			wantURI:  "https://remote.example/notes/1",
			wantType: "Note",
		},
		{
			name:    "object without type",
			raw:     `{"id": "https://remote.example/notes/1"}`,
			wantURI: "https://remote.example/notes/1",
		},
		{name: "number", raw: `42`},
		{name: "malformed", raw: `{`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			uri, apType := featuredItemRef(json.RawMessage(tt.raw))
			assert.Equal(t, tt.wantURI, uri)
			assert.Equal(t, tt.wantType, apType)
		})
	}
}

// in-flight guard に当たっても、既に取り込み済みの投稿は既存行で拾うこと
// (#2684 review HIGH-2 / MED-1)。
//
// **ReplaceByUser は delete-then-insert なので、ここで落とすと集合ごと
// 書き直して生きているピンが消える。** 台帳の区間は ingestNoteWithCreated
// 全体 (著者の取得・featured コレクションの取得・入れ子の note 取得を含む)
// なので**秒のオーダーで開いている**。singleflight 本体全体を覆っていた頃と
// 比べて外れたのは、この note 自身の HTTP fetch の分。
//
// **取得 URI と document id の両方で引くこと。** 行は document id で保存
// されるので、別名 URL では取得 URI だけでは必ず空振りする。窓が重なるのは
// まさにその別名のときなので、片方だけだと fallback が意味を持たない。
//
// 台帳を直接触るのは、この競合を fetch や DB の遅延で再現しようとすると
// 本質的に flaky になるため。見たいのは「鍵が載っている状態で既存行を
// 拾うか」だけなので、その状態を直接作る。
func TestResolveFeaturedNotes_UsesExistingRowWhenTheKeyIsInFlight(t *testing.T) {
	const actorURI = "https://remote.example/users/iris"
	cases := []struct {
		name     string
		listed   string // featured collection に載る URI (= 取得 URI)
		storedAs string // note 行の URI (= document id)
	}{
		{"same uri", "https://remote.example/notes/i1", "https://remote.example/notes/i1"},
		{"alias uri", "https://remote.example/@iris/i1", "https://remote.example/notes/i1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			userRepo := testutil.NewMockUserRepository()
			noteRepo := testutil.NewMockNoteRepository()
			urls := activitypub.NewURLBuilder("https://example.com")
			idGen, err := id.NewGenerator("aidx")
			require.NoError(t, err)
			// fetch してはいけない経路なので、fetcher は必ず失敗する。
			r := NewResolver(userRepo, noteRepo, urls, emptyDocFetcher{}, idGen)

			host := "remote.example"
			uri := actorURI
			user := &model.User{ID: "9irisuser00000000000", Username: "iris", Host: &host, URI: &uri}
			require.NoError(t, userRepo.Create(user))
			nURI := tc.storedAs
			require.NoError(t, noteRepo.Create(&model.Note{ID: "9irisnote00000000000", UserID: user.ID, URI: &nURI}))

			items := []json.RawMessage{json.RawMessage(`"` + tc.listed + `"`)}

			// 台帳に載っている状態でも既存行を拾うこと。**ここで skip すると
			// ReplaceByUser が集合ごと書き直して生きたピンを消す。**
			r.resolvingNotes.Store(noteGroupKey(tc.listed, false, false), tc.storedAs)
			assert.Equal(t, []string{"9irisnote00000000000"}, r.resolveFeaturedNotes(user, items),
				"in-flight でも既存行があれば拾うこと (skip すると生きたピンが消える)")
		})
	}
}

// emptyDocFetcher never serves a document, so any code path that tries to
// fetch fails visibly instead of silently succeeding.
type emptyDocFetcher struct{}

func (emptyDocFetcher) FetchObject(string) ([]byte, error) {
	return nil, errors.New("fetch must not happen in this test")
}

// ledgerProbeNoteRepo records the in-flight ledger contents on every FindByURI
// so a test can observe what resolveNoteOnce stored while ingesting.
type ledgerProbeNoteRepo struct {
	repository.NoteRepository
	r    *Resolver
	seen map[string]any
}

func (p *ledgerProbeNoteRepo) FindByURI(uri string) (*model.Note, error) {
	p.r.resolvingNotes.Range(func(k, v any) bool {
		p.seen[k.(string)] = v
		return true
	})
	return p.NoteRepository.FindByURI(uri)
}

// 台帳には document id を載せること (#2684 review MED-1)。
//
// 値が空だと、別名 URL のとき featured / quote の fallback が既存行を
// 引き当てられない (行は document id で保存されるため)。呼び出し側の
// fallback だけをテストしても、**値を載せる側の配線が消えたことは
// 検出できない**ので、本番経路を通して中身を見る。
func TestResolveNoteOnce_LedgerCarriesTheDocumentID(t *testing.T) {
	const (
		actorURI = "https://remote.example/users/jun"
		aliasURI = "https://remote.example/@jun/j1"
		canonURI = "https://remote.example/notes/j1"
	)
	userRepo := testutil.NewMockUserRepository()
	base := testutil.NewMockNoteRepository()
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, err := id.NewGenerator("aidx")
	require.NoError(t, err)
	probe := &ledgerProbeNoteRepo{NoteRepository: base, seen: map[string]any{}}
	r := NewResolver(userRepo, probe, urls, fixtureDocFetcher{docs: map[string]string{
		actorURI: featuredActorDoc("remote.example", "jun", ""),
		aliasURI: plainNoteDoc(canonURI, actorURI),
	}}, idGen)
	probe.r = r

	_, err = r.ResolveNote(aliasURI)
	require.NoError(t, err)

	// 鍵は取得 URI、値は document id。
	got, ok := probe.seen[noteGroupKey(aliasURI, false, false)]
	require.True(t, ok, "ingest 中に台帳へ載っていること")
	assert.Equal(t, canonURI, got, "台帳の値は document id であること")
}

// fixtureDocFetcher serves canned documents by URI.
type fixtureDocFetcher struct{ docs map[string]string }

func (d fixtureDocFetcher) FetchObject(uri string) ([]byte, error) {
	if body, ok := d.docs[uri]; ok {
		return []byte(body), nil
	}
	return nil, errors.New("no fixture for " + uri)
}

func featuredActorDoc(host, name, featured string) string {
	base := "https://" + host + "/users/" + name
	line := ""
	if featured != "" {
		line = `"featured": "` + featured + `",`
	}
	return `{"@context":"https://www.w3.org/ns/activitystreams","id":"` + base +
		`","type":"Person","preferredUsername":"` + name + `","inbox":"` + base + `/inbox",` + line +
		`"publicKey":{"id":"` + base + `#main-key","owner":"` + base +
		`","publicKeyPem":"-----BEGIN PUBLIC KEY-----\nFAKE\n-----END PUBLIC KEY-----"}}`
}

func plainNoteDoc(noteID, authorURI string) string {
	return `{"@context":"https://www.w3.org/ns/activitystreams","id":"` + noteID +
		`","type":"Note","attributedTo":"` + authorURI +
		`","content":"<p>hi</p>","to":["https://www.w3.org/ns/activitystreams#Public"]}`
}
