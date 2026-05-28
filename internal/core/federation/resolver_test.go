package federation_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lib/pq"
	"github.com/shiroha-a/mk/internal/activitypub"
	"github.com/shiroha-a/mk/internal/core/federation"
	corenote "github.com/shiroha-a/mk/internal/core/note"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// stubFetcher returns canned bytes/error for FetchObject.
type stubFetcher struct {
	body []byte
	err  error
}

func (s *stubFetcher) FetchObject(_ string) ([]byte, error) {
	return s.body, s.err
}

// blockingFetcher counts FetchObject calls and blocks until gate is closed.
// 並行呼び出しが singleflight に集約される瞬間を観測するためのテスト用 fetcher
// (#300 3-7)。
type blockingFetcher struct {
	body  []byte
	gate  chan struct{}
	calls atomic.Int64
}

func (b *blockingFetcher) FetchObject(_ string) ([]byte, error) {
	b.calls.Add(1)
	<-b.gate
	return b.body, nil
}

const sampleActor = `{
	"id": "https://remote.example/users/alice",
	"type": "Person",
	"preferredUsername": "alice",
	"name": "Alice",
	"inbox": "https://remote.example/users/alice/inbox",
	"endpoints": {"sharedInbox": "https://remote.example/inbox"},
	"publicKey": {
		"id": "https://remote.example/users/alice#main-key",
		"owner": "https://remote.example/users/alice",
		"publicKeyPem": "-----BEGIN PUBLIC KEY-----\nFAKE\n-----END PUBLIC KEY-----"
	}
}`

func newResolver(t *testing.T, body string, err error) (*federation.Resolver, *testutil.MockUserRepository) {
	t.Helper()
	repo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	return federation.NewResolver(repo, noteRepo, urls, &stubFetcher{body: []byte(body), err: err}, idGen), repo
}

func TestResolveActor_NewUser(t *testing.T) {
	r, repo := newResolver(t, sampleActor, nil)
	user, err := r.ResolveActor("https://remote.example/users/alice")
	require.NoError(t, err)
	assert.Equal(t, "alice", user.Username)
	assert.Len(t, repo.Users, 1)

	pem, err := r.PublicKeyForActor(user.ID)
	require.NoError(t, err)
	assert.Contains(t, pem, "FAKE")
}

// #692: 新規 fetch で `_misskey_canChat` を chatScope に翻訳する。
// 欠落 / true は "everyone"、false は "none" にマップ。
func TestResolveActor_NewUserCanChatTranslation(t *testing.T) {
	cases := []struct {
		name     string
		bodyFlag string
		want     string
	}{
		{"missing", "", "everyone"},
		{"true", `, "_misskey_canChat": true`, "everyone"},
		{"false", `, "_misskey_canChat": false`, "none"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := `{
				"id": "https://remote.example/users/alice",
				"type": "Person",
				"preferredUsername": "alice",
				"inbox": "https://remote.example/users/alice/inbox",
				"publicKey": {"publicKeyPem": "FAKE"}` + tc.bodyFlag + `
			}`
			r, _ := newResolver(t, body, nil)
			user, err := r.ResolveActor("https://remote.example/users/alice")
			require.NoError(t, err)
			assert.Equal(t, tc.want, user.ChatScope)
		})
	}
}

func TestResolveActor_NewUserIngestsIconBanner(t *testing.T) {
	// actor.icon / actor.image があれば avatarUrl / bannerUrl に取り込む。
	body := `{
		"id": "https://remote.example/users/alice",
		"type": "Person",
		"preferredUsername": "alice",
		"inbox": "https://remote.example/users/alice/inbox",
		"icon": {"type": "Image", "url": "https://remote.example/avatar.png"},
		"image": {"type": "Image", "url": "https://remote.example/banner.png"},
		"publicKey": {"publicKeyPem": "FAKE"}
	}`
	r, _ := newResolver(t, body, nil)
	user, err := r.ResolveActor("https://remote.example/users/alice")
	require.NoError(t, err)
	require.NotNil(t, user.AvatarURL)
	assert.Equal(t, "https://remote.example/avatar.png", *user.AvatarURL)
	require.NotNil(t, user.BannerURL)
	assert.Equal(t, "https://remote.example/banner.png", *user.BannerURL)
}

func TestResolveActor_NewUserIngestsManuallyApproves(t *testing.T) {
	// actor.manuallyApprovesFollowers が true のリモートアカウントは
	// IsLocked=true で保存する (follow 時に FollowRequest 経路に入るように
	// するため)。
	body := `{
		"id": "https://remote.example/users/locked",
		"type": "Person",
		"preferredUsername": "locked",
		"inbox": "https://remote.example/users/locked/inbox",
		"manuallyApprovesFollowers": true,
		"publicKey": {"publicKeyPem": "FAKE"}
	}`
	r, _ := newResolver(t, body, nil)
	user, err := r.ResolveActor("https://remote.example/users/locked")
	require.NoError(t, err)
	assert.True(t, user.IsLocked, "IsLocked should be true when actor.manuallyApprovesFollowers is true")
}

func TestResolveActor_NewUserWithoutManuallyApproves(t *testing.T) {
	// manuallyApprovesFollowers 省略時は IsLocked=false。
	r, _ := newResolver(t, sampleActor, nil)
	user, err := r.ResolveActor("https://remote.example/users/alice")
	require.NoError(t, err)
	assert.False(t, user.IsLocked)
}

func TestResolveActor_NewUserWithoutIconBanner(t *testing.T) {
	// icon / image を持たない actor は avatarUrl / bannerUrl を nil のまま。
	r, _ := newResolver(t, sampleActor, nil)
	user, err := r.ResolveActor("https://remote.example/users/alice")
	require.NoError(t, err)
	assert.Nil(t, user.AvatarURL)
	assert.Nil(t, user.BannerURL)
}

// --- #1022: remote actor summary → UserProfile.description ---

// AP `summary` (= リモートユーザの bio) を UserProfile.description に取り込む。
// profile 行も同時に作成される (= 旧実装は user 行のみ作成していた regression)。
// Mastodon 系の `<p>...</p>` ラップは mfm.FromHTML で MFM に変換される
// (生 HTML を保存すると frontend MFM render が escape してリテラル表示する
// drop-in regression を起こす、#1140)。
func TestResolveActor_NewUserIngestsDescription(t *testing.T) {
	body := `{
		"id": "https://remote.example/users/alice",
		"type": "Person",
		"preferredUsername": "alice",
		"inbox": "https://remote.example/users/alice/inbox",
		"summary": "<p>Hello from remote!</p>",
		"publicKey": {"publicKeyPem": "FAKE"}
	}`
	r, repo := newResolver(t, body, nil)
	user, err := r.ResolveActor("https://remote.example/users/alice")
	require.NoError(t, err)
	profile, err := repo.FindProfileByUserID(user.ID)
	require.NoError(t, err, "profile 行が作成される (back-fill 経路の前提)")
	require.NotNil(t, profile.Description)
	assert.Equal(t, "Hello from remote!", *profile.Description,
		"<p> wrap は mfm.FromHTML で MFM 段落区切りに変換され TrimSpace で消える")
}

// TestResolveActor_MastodonStyleSummaryConvertedToMFM guards #1140:
// Mastodon / Pleroma 系の actor.summary は典型的に `<p>...</p>` で wrap
// され、`<a href>` mention や `<br>` を含む。これらが mfm.FromHTML 経由で
// MFM に変換され、profile.description に純 MFM として保存されることを
// 確認 (生 HTML が残ると frontend MFM render が escape して `<p>` 等が
// リテラル表示される drop-in regression)。
func TestResolveActor_MastodonStyleSummaryConvertedToMFM(t *testing.T) {
	body := `{
		"id": "https://mstdn.example/users/bob",
		"type": "Person",
		"preferredUsername": "bob",
		"inbox": "https://mstdn.example/users/bob/inbox",
		"summary": "<p>Hello!<br/>I&#39;m bob.</p><p>Follow me at <a href=\"https://example.com\">my site</a></p>",
		"publicKey": {"publicKeyPem": "FAKE"}
	}`
	r, repo := newResolver(t, body, nil)
	user, err := r.ResolveActor("https://mstdn.example/users/bob")
	require.NoError(t, err)
	profile, err := repo.FindProfileByUserID(user.ID)
	require.NoError(t, err)
	require.NotNil(t, profile.Description)
	// `<p>` → 段落区切り (\n\n) / `<br>` → \n / `<a>` → [text](url) MFM。
	desc := *profile.Description
	assert.NotContains(t, desc, "<p>", "<p> tag must be converted out, not stored raw")
	assert.NotContains(t, desc, "</p>", "</p> tag must be converted out")
	assert.NotContains(t, desc, "<a ", "<a> tag must be converted to MFM link syntax")
	assert.Contains(t, desc, "Hello!")
	assert.Contains(t, desc, "I'm bob.")
	assert.Contains(t, desc, "[my site](https://example.com)")
}

// `_misskey_summary` がある場合は AP `summary` より優先される (upstream
// ApPersonService と同じ logic、MFM が壊れずに保存される)。
func TestResolveActor_NewUserPrefersMisskeySummary(t *testing.T) {
	body := `{
		"id": "https://remote.example/users/alice",
		"type": "Person",
		"preferredUsername": "alice",
		"inbox": "https://remote.example/users/alice/inbox",
		"summary": "<p>HTML summary</p>",
		"_misskey_summary": "**MFM summary**",
		"publicKey": {"publicKeyPem": "FAKE"}
	}`
	r, repo := newResolver(t, body, nil)
	user, err := r.ResolveActor("https://remote.example/users/alice")
	require.NoError(t, err)
	profile, err := repo.FindProfileByUserID(user.ID)
	require.NoError(t, err)
	require.NotNil(t, profile.Description)
	assert.Equal(t, "**MFM summary**", *profile.Description)
}

// summary 無し actor も profile 行は作成され、Description は nil。
// 既存 production DB の back-fill 経路 (next refresh で追記) が成立する前提。
func TestResolveActor_NewUserNoSummary(t *testing.T) {
	r, repo := newResolver(t, sampleActor, nil)
	user, err := r.ResolveActor("https://remote.example/users/alice")
	require.NoError(t, err)
	profile, err := repo.FindProfileByUserID(user.ID)
	require.NoError(t, err)
	assert.Nil(t, profile.Description, "summary 無しは Description nil")
}

// 2048 rune を超える summary は varchar(2048) 制約に合わせて truncate。
// rune 単位で切ることで UTF-8 multibyte 文字境界が壊れない。
func TestResolveActor_NewUserDescriptionTruncated(t *testing.T) {
	long := strings.Repeat("あ", 2100) // 2100 rune
	body := `{
		"id": "https://remote.example/users/alice",
		"type": "Person",
		"preferredUsername": "alice",
		"inbox": "https://remote.example/users/alice/inbox",
		"summary": "` + long + `",
		"publicKey": {"publicKeyPem": "FAKE"}
	}`
	r, repo := newResolver(t, body, nil)
	user, err := r.ResolveActor("https://remote.example/users/alice")
	require.NoError(t, err)
	profile, err := repo.FindProfileByUserID(user.ID)
	require.NoError(t, err)
	require.NotNil(t, profile.Description)
	assert.Equal(t, 2048, len([]rune(*profile.Description)))
}

func TestResolveActor_ExistingUser(t *testing.T) {
	r, repo := newResolver(t, sampleActor, nil)
	uri := "https://remote.example/users/alice"
	repo.Users["existing"] = &model.User{
		ID:       "existing",
		Username: "alice",
		URI:      &uri,
	}
	user, err := r.ResolveActor(uri)
	require.NoError(t, err)
	assert.Equal(t, "existing", user.ID)
	// 公開鍵キャッシュは refresh により埋まる
	pem, err := r.PublicKeyForActor("existing")
	require.NoError(t, err)
	assert.Contains(t, pem, "FAKE")
}

func TestResolveActor_FetchError(t *testing.T) {
	r, _ := newResolver(t, "", errors.New("network down"))
	_, err := r.ResolveActor("https://remote.example/users/x")
	assert.Error(t, err)
}

func TestResolveActor_BadJSON(t *testing.T) {
	r, _ := newResolver(t, "{not json", nil)
	_, err := r.ResolveActor("https://remote.example/users/x")
	require.ErrorIs(t, err, federation.ErrInvalidActor)
}

func TestResolveActor_MissingFields(t *testing.T) {
	r, _ := newResolver(t, `{"id":"x"}`, nil)
	_, err := r.ResolveActor("https://remote.example/users/x")
	require.ErrorIs(t, err, federation.ErrInvalidActor)
}

func TestResolveActor_BadHost(t *testing.T) {
	// invalid URL with control char
	body := `{
		"id": "://invalid",
		"preferredUsername": "alice",
		"inbox": "x"
	}`
	r, _ := newResolver(t, body, nil)
	_, err := r.ResolveActor("https://x.example/")
	require.ErrorIs(t, err, federation.ErrInvalidActor)
}

func TestResolveActor_EmptyHost(t *testing.T) {
	// mailto: parses but has no host
	body := `{
		"id": "mailto:alice@example.com",
		"preferredUsername": "alice",
		"inbox": "x"
	}`
	r, _ := newResolver(t, body, nil)
	_, err := r.ResolveActor("https://x.example/")
	require.ErrorIs(t, err, federation.ErrInvalidActor)
}

// failingUserRepo returns Create errors for the resolver.
type failingUserRepo struct {
	*testutil.MockUserRepository
}

func (f *failingUserRepo) Create(_ *model.User) error {
	return errors.New("create failed")
}

func TestResolveActor_RepoCreateError(t *testing.T) {
	mock := testutil.NewMockUserRepository()
	repo := &failingUserRepo{MockUserRepository: mock}
	noteRepo := testutil.NewMockNoteRepository()
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{body: []byte(sampleActor)}, idGen)
	_, err := r.ResolveActor("https://remote.example/users/alice")
	assert.Error(t, err)
}

func TestResolveActorByKeyID(t *testing.T) {
	r, _ := newResolver(t, sampleActor, nil)
	user, err := r.ResolveActorByKeyID("https://remote.example/users/alice#main-key")
	require.NoError(t, err)
	assert.Equal(t, "alice", user.Username)
}

func TestPublicKeyForActor_Missing(t *testing.T) {
	r, _ := newResolver(t, sampleActor, nil)
	_, err := r.PublicKeyForActor("ghost")
	assert.Error(t, err)
}

// --- ResolveNote / IngestNote --------------------------------------------------

const sampleRemoteNote = `{
	"id": "https://remote.example/notes/n1",
	"type": "Note",
	"attributedTo": "https://remote.example/users/alice",
	"content": "hello",
	"to": ["https://www.w3.org/ns/activitystreams#Public"],
	"cc": ["https://remote.example/users/alice/followers"],
	"summary": "cw",
	"sensitive": true
}`

// scriptedFetcher returns different bodies based on a counter so a single
// resolver call can resolve both an actor and a note.
type scriptedFetcher struct {
	bodies [][]byte
	idx    int
}

func (s *scriptedFetcher) FetchObject(_ string) ([]byte, error) {
	if s.idx >= len(s.bodies) {
		return nil, errors.New("no more bodies")
	}
	b := s.bodies[s.idx]
	s.idx++
	return b, nil
}

func TestResolveNote_LocalAlreadyStored(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	noteRepo.Notes["nlocal"] = &model.Note{ID: "nlocal", UserID: "u1"}
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{}, idGen)

	got, err := r.ResolveNote("https://example.com/notes/nlocal")
	require.NoError(t, err)
	assert.Equal(t, "nlocal", got.ID)
}

func TestResolveNote_LocalUnknown(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{}, idGen)

	_, err := r.ResolveNote("https://example.com/notes/missing")
	assert.ErrorIs(t, err, federation.ErrInvalidNote)
}

func TestResolveNote_RemoteCached(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	uri := "https://remote.example/notes/n1"
	noteRepo.Notes["n1"] = &model.Note{ID: "n1", URI: &uri}
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{}, idGen)

	got, err := r.ResolveNote(uri)
	require.NoError(t, err)
	assert.Equal(t, "n1", got.ID)
}

func TestResolveNote_RemoteFetchAndIngest(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	// 最初の呼び出しで Note JSON、続いて attributedTo を解決するための actor JSON を返す
	fetcher := &scriptedFetcher{bodies: [][]byte{[]byte(sampleRemoteNote), []byte(sampleActor)}}
	r := federation.NewResolver(repo, noteRepo, urls, fetcher, idGen)

	got, err := r.ResolveNote("https://remote.example/notes/n1")
	require.NoError(t, err)
	assert.NotNil(t, got)
	require.NotNil(t, got.URI)
	assert.Equal(t, "https://remote.example/notes/n1", *got.URI)
	require.NotNil(t, got.Text)
	assert.Equal(t, "hello", *got.Text)
	require.NotNil(t, got.CW)
	assert.Equal(t, "cw", *got.CW)
	assert.Equal(t, model.NoteVisibilityPublic, got.Visibility)
}

func TestResolveNote_FetchError(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{err: errors.New("net down")}, idGen)
	_, err := r.ResolveNote("https://remote.example/notes/x")
	assert.Error(t, err)
}

func TestResolveNote_NoNoteRepo(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	r := federation.NewResolver(repo, nil, urls, &stubFetcher{}, idGen)
	_, err := r.ResolveNote("https://remote.example/notes/x")
	assert.ErrorIs(t, err, federation.ErrInvalidNote)
}

func TestIngestNote_BadJSON(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{}, idGen)
	_, err := r.IngestNote([]byte(`{not json`))
	assert.ErrorIs(t, err, federation.ErrInvalidNote)
}

func TestIngestNote_MissingFields(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{}, idGen)
	_, err := r.IngestNote([]byte(`{"id":"x"}`))
	assert.ErrorIs(t, err, federation.ErrInvalidNote)
}

func TestIngestNote_NoNoteRepo(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	r := federation.NewResolver(repo, nil, urls, &stubFetcher{}, idGen)
	_, err := r.IngestNote([]byte(sampleRemoteNote))
	assert.ErrorIs(t, err, federation.ErrInvalidNote)
}

func TestIngestNote_DedupOnExisting(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	uri := "https://remote.example/notes/n1"
	noteRepo.Notes["existing"] = &model.Note{ID: "existing", URI: &uri}
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{body: []byte(sampleActor)}, idGen)
	got, err := r.IngestNote([]byte(sampleRemoteNote))
	require.NoError(t, err)
	assert.Equal(t, "existing", got.ID)
}

// IngestNoteWithCreated は新規 INSERT 経路で created=true、dedup hit で
// created=false を返す (#1156)。Processor 側で chart hook を created flag
// で gate するために使う。
func TestIngestNoteWithCreated_FreshIngest(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{body: []byte(sampleActor)}, idGen)
	got, created, err := r.IngestNoteWithCreated([]byte(sampleRemoteNote))
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.True(t, created, "fresh ingest must report created=true so caller can fire chart hooks")
}

func TestIngestNoteWithCreated_DedupReturnsFalse(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	uri := "https://remote.example/notes/n1"
	noteRepo.Notes["existing"] = &model.Note{ID: "existing", URI: &uri}
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{body: []byte(sampleActor)}, idGen)
	got, created, err := r.IngestNoteWithCreated([]byte(sampleRemoteNote))
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "existing", got.ID)
	assert.False(t, created, "dedup hit must report created=false so caller can skip non-idempotent chart hooks")
}

func TestIngestNote_ResolveActorError(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{err: errors.New("actor down")}, idGen)
	_, err := r.IngestNote([]byte(sampleRemoteNote))
	assert.Error(t, err)
}

// failingNoteCreateRepo causes Create on noteRepo to fail.
type failingNoteCreateRepo struct {
	*testutil.MockNoteRepository
}

func (f *failingNoteCreateRepo) Create(_ *model.Note) error {
	return errors.New("create failed")
}

// #679: AP `tag` 配列の Hashtag entry を note.tags に取り込む。
func TestIngestNote_HashtagsFromTagArray(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{body: []byte(sampleActor)}, idGen)

	body := `{
		"id": "https://remote.example/notes/h1",
		"type": "Note",
		"attributedTo": "https://remote.example/users/alice",
		"content": "no inline tag here",
		"to": ["https://www.w3.org/ns/activitystreams#Public"],
		"tag": [
			{"type": "Hashtag", "name": "#golang", "href": "https://remote.example/tags/golang"},
			{"type": "Hashtag", "name": "#federation"},
			{"type": "Mention", "href": "https://remote.example/users/bob"}
		]
	}`
	note, err := r.IngestNote([]byte(body))
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"golang", "federation"}, []string(note.Tags), "Hashtag entries の name から #prefix を剥がして格納")
}

// #679: text 由来の hashtag fallback。Mastodon 等で `tag` 配列が無い実装でも
// 本文 / CW から拾う。
func TestIngestNote_HashtagsFromTextFallback(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{body: []byte(sampleActor)}, idGen)

	body := `{
		"id": "https://remote.example/notes/h2",
		"type": "Note",
		"attributedTo": "https://remote.example/users/alice",
		"content": "hello #golang and #federation world",
		"to": ["https://www.w3.org/ns/activitystreams#Public"]
	}`
	note, err := r.IngestNote([]byte(body))
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"golang", "federation"}, []string(note.Tags))
}

// #679: AP tag と text の両方に hashtag があれば case-insensitive で dedup される。
func TestIngestNote_HashtagsMergeAndDedup(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{body: []byte(sampleActor)}, idGen)

	body := `{
		"id": "https://remote.example/notes/h3",
		"type": "Note",
		"attributedTo": "https://remote.example/users/alice",
		"content": "see #Golang and #news",
		"to": ["https://www.w3.org/ns/activitystreams#Public"],
		"tag": [
			{"type": "Hashtag", "name": "#golang"}
		]
	}`
	note, err := r.IngestNote([]byte(body))
	require.NoError(t, err)
	// "#golang" (tag 配列) と "#Golang" (text) は case-insensitive で同一視。
	// hashtag.Extract は first-seen casing を保つので tag 配列が先で `golang`。
	assert.ElementsMatch(t, []string{"golang", "news"}, []string(note.Tags))
}

// #679: CW (summary) 由来の hashtag も抽出される。本文が無く CW にだけ
// hashtag があるケース (sensitive note 等) で trends 集計に拾われないと
// 取りこぼしになる。
func TestIngestNote_HashtagsFromCW(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{body: []byte(sampleActor)}, idGen)

	body := `{
		"id": "https://remote.example/notes/h-cw",
		"type": "Note",
		"attributedTo": "https://remote.example/users/alice",
		"content": "no inline tag",
		"summary": "important #news here",
		"sensitive": true,
		"to": ["https://www.w3.org/ns/activitystreams#Public"]
	}`
	note, err := r.IngestNote([]byte(body))
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"news"}, []string(note.Tags))
}

// #679 review #2: `name` に `#` prefix が付いていない非標準実装からの
// Hashtag tag も defensive 補完によって正しく拾われる。upstream Misskey の
// renderHashtag では起きないが将来の AP 実装互換性を guard する。
func TestIngestNote_HashtagWithoutPrefixDefensive(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{body: []byte(sampleActor)}, idGen)

	body := `{
		"id": "https://remote.example/notes/h-noprefix",
		"type": "Note",
		"attributedTo": "https://remote.example/users/alice",
		"content": "no inline tag",
		"to": ["https://www.w3.org/ns/activitystreams#Public"],
		"tag": [
			{"type": "Hashtag", "name": "golang"}
		]
	}`
	note, err := r.IngestNote([]byte(body))
	require.NoError(t, err)
	assert.Contains(t, []string(note.Tags), "golang", "name に # が無くても defensive 補完で抽出される")
}

// #679: Hashtag 無しなら note.Tags は nil/empty で残る。
func TestIngestNote_NoHashtags(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{body: []byte(sampleActor)}, idGen)

	body := `{
		"id": "https://remote.example/notes/h4",
		"type": "Note",
		"attributedTo": "https://remote.example/users/alice",
		"content": "no tags here",
		"to": ["https://www.w3.org/ns/activitystreams#Public"]
	}`
	note, err := r.IngestNote([]byte(body))
	require.NoError(t, err)
	assert.Empty(t, []string(note.Tags))
}

func TestIngestNote_NoteCreateError(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	noteRepo := &failingNoteCreateRepo{MockNoteRepository: testutil.NewMockNoteRepository()}
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{body: []byte(sampleActor)}, idGen)
	_, err := r.IngestNote([]byte(sampleRemoteNote))
	assert.Error(t, err)
}

// stubPollVoter records IngestNote → AP poll vote routing.
type stubPollVoter struct {
	calls []struct {
		User   *model.User
		NoteID string
		Choice int
	}
	err error
}

func (s *stubPollVoter) Vote(user *model.User, noteID string, choice int) error {
	s.calls = append(s.calls, struct {
		User   *model.User
		NoteID string
		Choice int
	}{user, noteID, choice})
	return s.err
}

// TestIngestNote_APVoteRoutedToPollService covers the #690 fix where an
// inbound AP Note with `name` + `inReplyTo` to a poll-bearing local note is
// treated as a vote and routed to pollService.Vote, not persisted as a
// reply note.
func TestIngestNote_APVoteRoutedToPollService(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	noteRepo.Notes["pollNote"] = &model.Note{ID: "pollNote", UserID: "author", HasPoll: true, Visibility: model.NoteVisibilityPublic}
	pollRepo := testutil.NewMockPollRepository()
	require.NoError(t, pollRepo.Create(&model.Poll{
		NoteID: "pollNote", Choices: []string{"Apple", "Banana"}, Votes: []int64{0, 0},
	}))
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{body: []byte(sampleActor)}, idGen)
	r.SetPollRepo(pollRepo)
	voter := &stubPollVoter{}
	r.SetPollVoter(voter)

	body := []byte(`{
		"id": "https://remote.example/users/alice#votes/pollNote",
		"type": "Note",
		"attributedTo": "https://remote.example/users/alice",
		"name": "Banana",
		"inReplyTo": "https://example.com/notes/pollNote",
		"to": ["https://example.com/users/author"]
	}`)
	got, err := r.IngestNote(body)
	require.NoError(t, err)
	assert.Nil(t, got, "AP vote must NOT create a note (caller skips fanout/notification)")
	require.Len(t, voter.calls, 1, "pollVoter.Vote must be called exactly once")
	assert.Equal(t, "pollNote", voter.calls[0].NoteID)
	assert.Equal(t, 1, voter.calls[0].Choice, "Banana = choice index 1")
}

// TestIngestNote_APVoteUnknownChoiceFallsThrough verifies that if the
// `name` does NOT match any poll choice, IngestNote falls through to
// regular reply processing instead of swallowing the note silently.
func TestIngestNote_APVoteUnknownChoiceFallsThrough(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	noteRepo.Notes["pollNote"] = &model.Note{ID: "pollNote", UserID: "author", HasPoll: true, Visibility: model.NoteVisibilityPublic}
	pollRepo := testutil.NewMockPollRepository()
	require.NoError(t, pollRepo.Create(&model.Poll{
		NoteID: "pollNote", Choices: []string{"Apple", "Banana"}, Votes: []int64{0, 0},
	}))
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{body: []byte(sampleActor)}, idGen)
	r.SetPollRepo(pollRepo)
	voter := &stubPollVoter{}
	r.SetPollVoter(voter)

	body := []byte(`{
		"id": "https://remote.example/users/alice#votes/pollNote",
		"type": "Note",
		"attributedTo": "https://remote.example/users/alice",
		"name": "Cherry",
		"inReplyTo": "https://example.com/notes/pollNote",
		"to": ["https://example.com/users/author"]
	}`)
	got, err := r.IngestNote(body)
	require.NoError(t, err)
	require.NotNil(t, got, "unmatched choice falls through to reply note creation")
	assert.Empty(t, voter.calls)
	require.NotNil(t, got.ReplyID)
	assert.Equal(t, "pollNote", *got.ReplyID)
}

// TestIngestNote_NormalReplyToPollNotConfusedAsVote verifies that a reply
// note WITHOUT `name` is processed as a normal reply, not a vote.
func TestIngestNote_NormalReplyToPollNotConfusedAsVote(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	noteRepo.Notes["pollNote"] = &model.Note{ID: "pollNote", UserID: "author", HasPoll: true, Visibility: model.NoteVisibilityPublic}
	pollRepo := testutil.NewMockPollRepository()
	require.NoError(t, pollRepo.Create(&model.Poll{
		NoteID: "pollNote", Choices: []string{"Apple"}, Votes: []int64{0},
	}))
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{body: []byte(sampleActor)}, idGen)
	r.SetPollRepo(pollRepo)
	voter := &stubPollVoter{}
	r.SetPollVoter(voter)

	body := []byte(`{
		"id": "https://remote.example/notes/normalreply",
		"type": "Note",
		"attributedTo": "https://remote.example/users/alice",
		"content": "Just commenting on the poll",
		"inReplyTo": "https://example.com/notes/pollNote",
		"to": ["https://example.com/users/author"]
	}`)
	got, err := r.IngestNote(body)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Empty(t, voter.calls, "normal reply must not invoke pollVoter")
	require.NotNil(t, got.ReplyID)
}

func TestIngestNote_ReplyToLocal(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	noteRepo.Notes["parent"] = &model.Note{ID: "parent", UserID: "uparent"}
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{body: []byte(sampleActor)}, idGen)
	body := []byte(`{
		"id": "https://remote.example/notes/n2",
		"type": "Note",
		"attributedTo": "https://remote.example/users/alice",
		"content": "reply",
		"inReplyTo": "https://example.com/notes/parent",
		"to": ["https://www.w3.org/ns/activitystreams#Public"]
	}`)
	got, err := r.IngestNote(body)
	require.NoError(t, err)
	require.NotNil(t, got.ReplyID)
	assert.Equal(t, "parent", *got.ReplyID)
}

func TestIngestNote_ReplyToRemote(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	parentURI := "https://remote.example/notes/parent"
	noteRepo.Notes["parent"] = &model.Note{ID: "parent", UserID: "uparent", URI: &parentURI}
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{body: []byte(sampleActor)}, idGen)
	body := []byte(`{
		"id": "https://remote.example/notes/n3",
		"type": "Note",
		"attributedTo": "https://remote.example/users/alice",
		"content": "reply",
		"inReplyTo": "https://remote.example/notes/parent",
		"to": ["https://www.w3.org/ns/activitystreams#Public"]
	}`)
	got, err := r.IngestNote(body)
	require.NoError(t, err)
	require.NotNil(t, got.ReplyID)
	assert.Equal(t, "parent", *got.ReplyID)
}

func TestIngestNote_SensitiveWithoutSummary(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{body: []byte(sampleActor)}, idGen)
	body := []byte(`{
		"id": "https://remote.example/notes/n4",
		"type": "Note",
		"attributedTo": "https://remote.example/users/alice",
		"content": "nsfw",
		"sensitive": true,
		"to": ["https://www.w3.org/ns/activitystreams#Public"]
	}`)
	got, err := r.IngestNote(body)
	require.NoError(t, err)
	require.NotNil(t, got.CW)
	assert.Equal(t, "", *got.CW)
}

// --- IngestNote visibility (ported from nekonoverse fetch_remote_note) --------

// makeNoteJSON builds a minimal AP Note with the given to/cc audience.
func makeNoteJSON(noteID string, to, cc []string) []byte {
	toJSON, _ := json.Marshal(to)
	ccJSON, _ := json.Marshal(cc)
	return []byte(fmt.Sprintf(`{
		"id": "https://remote.example/notes/%s",
		"type": "Note",
		"attributedTo": "https://remote.example/users/alice",
		"content": "visibility test",
		"to": %s,
		"cc": %s
	}`, noteID, toJSON, ccJSON))
}

func TestIngestNote_PublicVisibility(t *testing.T) {
	// to=[Public], cc=[followers] → public
	repo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{body: []byte(sampleActor)}, idGen)

	body := makeNoteJSON("vis-public", []string{"https://www.w3.org/ns/activitystreams#Public"}, []string{"https://remote.example/users/alice/followers"})
	got, err := r.IngestNote(body)
	require.NoError(t, err)
	assert.Equal(t, model.NoteVisibilityPublic, got.Visibility)
}

func TestIngestNote_HomeVisibility(t *testing.T) {
	// to=[followers], cc=[Public] → home
	repo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{body: []byte(sampleActor)}, idGen)

	body := makeNoteJSON("vis-home", []string{"https://remote.example/users/alice/followers"}, []string{"https://www.w3.org/ns/activitystreams#Public"})
	got, err := r.IngestNote(body)
	require.NoError(t, err)
	assert.Equal(t, model.NoteVisibilityHome, got.Visibility)
}

func TestIngestNote_FollowersVisibility(t *testing.T) {
	// to=[followers], cc=[] → followers
	repo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{body: []byte(sampleActor)}, idGen)

	body := makeNoteJSON("vis-followers", []string{"https://remote.example/users/alice/followers"}, []string{})
	got, err := r.IngestNote(body)
	require.NoError(t, err)
	assert.Equal(t, model.NoteVisibilityFollowers, got.Visibility)
}

func TestIngestNote_DirectVisibility(t *testing.T) {
	// to=[specific user], cc=[] → specified
	repo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{body: []byte(sampleActor)}, idGen)

	body := makeNoteJSON("vis-direct", []string{"https://remote.example/users/bob"}, []string{})
	got, err := r.IngestNote(body)
	require.NoError(t, err)
	assert.Equal(t, model.NoteVisibilitySpecified, got.Visibility)
}

func TestIngestNote_EmptyAudienceVisibility(t *testing.T) {
	// to=[], cc=[] → specified (fallback)
	repo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{body: []byte(sampleActor)}, idGen)

	body := makeNoteJSON("vis-empty", []string{}, []string{})
	got, err := r.IngestNote(body)
	require.NoError(t, err)
	assert.Equal(t, model.NoteVisibilitySpecified, got.Visibility)
}

// --- IngestNote text fallback (source.content → _misskey_content → HTML→MFM) -

func TestIngestNote_TextFallback_SourceContent(t *testing.T) {
	// source.content (MFM mediaType) が最優先で使われる
	repo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{body: []byte(sampleActor)}, idGen)

	body := []byte(`{
		"id": "https://remote.example/notes/source1",
		"type": "Note",
		"attributedTo": "https://remote.example/users/alice",
		"content": "<p>HTML content</p>",
		"source": {"content": "MFM source text", "mediaType": "text/x.misskeymarkdown"},
		"to": ["https://www.w3.org/ns/activitystreams#Public"]
	}`)
	got, err := r.IngestNote(body)
	require.NoError(t, err)
	require.NotNil(t, got.Text)
	assert.Equal(t, "MFM source text", *got.Text)
}

func TestIngestNote_TextFallback_MisskeyContent(t *testing.T) {
	// source が無い場合は _misskey_content が使われる
	repo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{body: []byte(sampleActor)}, idGen)

	body := []byte(`{
		"id": "https://remote.example/notes/mk1",
		"type": "Note",
		"attributedTo": "https://remote.example/users/alice",
		"content": "<p>HTML content</p>",
		"_misskey_content": "MFM via _misskey_content",
		"to": ["https://www.w3.org/ns/activitystreams#Public"]
	}`)
	got, err := r.IngestNote(body)
	require.NoError(t, err)
	require.NotNil(t, got.Text)
	assert.Equal(t, "MFM via _misskey_content", *got.Text)
}

func TestIngestNote_TextFallback_HTMLConversion(t *testing.T) {
	// source も _misskey_content も無い場合は HTML→MFM 変換
	repo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{body: []byte(sampleActor)}, idGen)

	body := []byte(`{
		"id": "https://remote.example/notes/html1",
		"type": "Note",
		"attributedTo": "https://remote.example/users/alice",
		"content": "<p>paragraph1</p><p>paragraph2</p>",
		"to": ["https://www.w3.org/ns/activitystreams#Public"]
	}`)
	got, err := r.IngestNote(body)
	require.NoError(t, err)
	require.NotNil(t, got.Text)
	assert.Equal(t, "paragraph1\n\nparagraph2", *got.Text)
}

func TestIngestNote_TextFallback_Priority(t *testing.T) {
	// 全部揃っている場合は source.content が優先される
	repo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{body: []byte(sampleActor)}, idGen)

	body := []byte(`{
		"id": "https://remote.example/notes/prio1",
		"type": "Note",
		"attributedTo": "https://remote.example/users/alice",
		"content": "<p>HTML</p>",
		"_misskey_content": "misskey content",
		"source": {"content": "source wins", "mediaType": "text/x.misskeymarkdown"},
		"to": ["https://www.w3.org/ns/activitystreams#Public"]
	}`)
	got, err := r.IngestNote(body)
	require.NoError(t, err)
	require.NotNil(t, got.Text)
	assert.Equal(t, "source wins", *got.Text)
}

func TestIngestNote_TextFallback_SourceWrongMediaType(t *testing.T) {
	// source.mediaType が MFM でない場合は _misskey_content にフォールバック
	repo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{body: []byte(sampleActor)}, idGen)

	body := []byte(`{
		"id": "https://remote.example/notes/wrongmt1",
		"type": "Note",
		"attributedTo": "https://remote.example/users/alice",
		"content": "<p>HTML</p>",
		"_misskey_content": "misskey fallback",
		"source": {"content": "plain text source", "mediaType": "text/plain"},
		"to": ["https://www.w3.org/ns/activitystreams#Public"]
	}`)
	got, err := r.IngestNote(body)
	require.NoError(t, err)
	require.NotNil(t, got.Text)
	assert.Equal(t, "misskey fallback", *got.Text)
}

// --- UpdateRemoteNote (Step J) -----------------------------------------------

const remoteNoteUpdateBody = `{
	"id": "https://remote.example/notes/n1",
	"type": "Note",
	"attributedTo": "https://remote.example/users/alice",
	"content": "edited content",
	"summary": "edited cw"
}`

func TestUpdateRemoteNote_HappyPath(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	uri := "https://remote.example/notes/n1"
	host := "remote.example"
	original := "original"
	noteRepo.Notes["n1"] = &model.Note{
		ID: "n1", URI: &uri, UserID: "alice-id", UserHost: &host, Text: &original,
	}
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{}, idGen)

	got, err := r.UpdateRemoteNote([]byte(remoteNoteUpdateBody))
	require.NoError(t, err)
	require.NotNil(t, got)
	require.NotNil(t, got.Text)
	assert.Equal(t, "edited content", *got.Text)
	require.NotNil(t, got.CW)
	assert.Equal(t, "edited cw", *got.CW)
}

// #679: UpdateRemoteNote が note.tags を再計算する。tag 配列が変わった場合に
// DB tags も追従すること。
func TestUpdateRemoteNote_RecalculatesHashtags(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	uri := "https://remote.example/notes/n1"
	host := "remote.example"
	noteRepo.Notes["n1"] = &model.Note{
		ID: "n1", URI: &uri, UserID: "alice-id", UserHost: &host,
		Tags: []string{"old"},
	}
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{}, idGen)

	body := `{
		"id": "https://remote.example/notes/n1",
		"type": "Note",
		"attributedTo": "https://remote.example/users/alice",
		"content": "edited and now tagged #news",
		"tag": [
			{"type": "Hashtag", "name": "#federation"}
		]
	}`
	got, err := r.UpdateRemoteNote([]byte(body))
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.ElementsMatch(t, []string{"federation", "news"}, []string(got.Tags), "古い tags は捨て、tag 配列 + 本文 fallback で再構築")
}

func TestUpdateRemoteNote_NoNoteRepo(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	r := federation.NewResolver(repo, nil, urls, &stubFetcher{}, idGen)
	_, err := r.UpdateRemoteNote([]byte(remoteNoteUpdateBody))
	assert.ErrorIs(t, err, federation.ErrInvalidNote)
}

func TestUpdateRemoteNote_BadJSON(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{}, idGen)
	_, err := r.UpdateRemoteNote([]byte(`{not json`))
	assert.ErrorIs(t, err, federation.ErrInvalidNote)
}

func TestUpdateRemoteNote_MissingID(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{}, idGen)
	_, err := r.UpdateRemoteNote([]byte(`{"type":"Note"}`))
	assert.ErrorIs(t, err, federation.ErrInvalidNote)
}

func TestUpdateRemoteNote_NotFound(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{}, idGen)
	got, err := r.UpdateRemoteNote([]byte(remoteNoteUpdateBody))
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestUpdateRemoteNote_LocalNoteSkipped(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	uri := "https://remote.example/notes/n1"
	original := "original"
	// UserHost == nil → ローカルノート扱い
	noteRepo.Notes["n1"] = &model.Note{
		ID: "n1", URI: &uri, UserID: "alice-id", Text: &original,
	}
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{}, idGen)

	got, err := r.UpdateRemoteNote([]byte(remoteNoteUpdateBody))
	require.NoError(t, err)
	require.NotNil(t, got)
	// Text は変わらない
	require.NotNil(t, got.Text)
	assert.Equal(t, "original", *got.Text)
}

func TestUpdateRemoteNote_EmptyContentNoOp(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	uri := "https://remote.example/notes/n1"
	host := "remote.example"
	original := "original"
	noteRepo.Notes["n1"] = &model.Note{
		ID: "n1", URI: &uri, UserID: "alice-id", UserHost: &host, Text: &original,
	}
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{}, idGen)

	body := []byte(`{
		"id": "https://remote.example/notes/n1",
		"type": "Note",
		"attributedTo": "https://remote.example/users/alice"
	}`)
	got, err := r.UpdateRemoteNote(body)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.NotNil(t, got.Text)
	assert.Equal(t, "original", *got.Text)
}

func TestUpdateRemoteNote_SensitiveWithoutSummary(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	uri := "https://remote.example/notes/n1"
	host := "remote.example"
	noteRepo.Notes["n1"] = &model.Note{
		ID: "n1", URI: &uri, UserID: "alice-id", UserHost: &host,
	}
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{}, idGen)

	body := []byte(`{
		"id": "https://remote.example/notes/n1",
		"type": "Note",
		"attributedTo": "https://remote.example/users/alice",
		"content": "nsfw",
		"sensitive": true
	}`)
	got, err := r.UpdateRemoteNote(body)
	require.NoError(t, err)
	require.NotNil(t, got.CW)
	assert.Equal(t, "", *got.CW)
}

// failingNoteUpdateRepo causes UpdateFields to fail.
type failingNoteUpdateRepo struct {
	*testutil.MockNoteRepository
}

func (f *failingNoteUpdateRepo) UpdateFields(_ string, _ map[string]any) error {
	return errors.New("update failed")
}

func TestUpdateRemoteNote_UpdateFieldsError(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	mock := testutil.NewMockNoteRepository()
	uri := "https://remote.example/notes/n1"
	host := "remote.example"
	mock.Notes["n1"] = &model.Note{
		ID: "n1", URI: &uri, UserID: "alice-id", UserHost: &host,
	}
	noteRepo := &failingNoteUpdateRepo{MockNoteRepository: mock}
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{}, idGen)
	_, err := r.UpdateRemoteNote([]byte(remoteNoteUpdateBody))
	assert.Error(t, err)
}

// --- TTL cache (Step G) -------------------------------------------------------

func TestResolveActor_TTLRefresh(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	uri := "https://remote.example/users/alice"
	// LastFetchedAt が古いため refresh が走るはず
	old := time.Now().Add(-48 * time.Hour)
	repo.Users["existing"] = &model.User{
		ID:            "existing",
		Username:      "alice",
		URI:           &uri,
		LastFetchedAt: &old,
	}
	// 更新後の actor を返す
	updated := `{
		"id": "https://remote.example/users/alice",
		"type": "Person",
		"preferredUsername": "alice",
		"name": "Alice Refreshed",
		"inbox": "https://remote.example/users/alice/inbox-v2",
		"endpoints": {"sharedInbox": "https://remote.example/inbox-v2"},
		"publicKey": {"publicKeyPem": "REFRESHED"}
	}`
	r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{body: []byte(updated)}, idGen)
	user, err := r.ResolveActor(uri)
	require.NoError(t, err)
	require.NotNil(t, user.Name)
	assert.Equal(t, "Alice Refreshed", *user.Name)
	require.NotNil(t, user.Inbox)
	assert.Equal(t, "https://remote.example/users/alice/inbox-v2", *user.Inbox)
	require.NotNil(t, user.SharedInbox)
	assert.Equal(t, "https://remote.example/inbox-v2", *user.SharedInbox)

	pem, err := r.PublicKeyForActor("existing")
	require.NoError(t, err)
	assert.Contains(t, pem, "REFRESHED")
}

// refresh 時に actor.summary を UserProfile.description に同期する (#1022)。
// profile 行が既に存在するケース (= post-fix で新規取り込まれた user) は
// UpdateProfile 経由で description を上書きする。
func TestResolveActor_TTLRefreshUpdatesDescription(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	uri := "https://remote.example/users/alice"
	old := time.Now().Add(-48 * time.Hour)
	repo.Users["existing"] = &model.User{
		ID:            "existing",
		Username:      "alice",
		URI:           &uri,
		LastFetchedAt: &old,
	}
	oldDesc := "old bio"
	repo.Profiles["existing"] = &model.UserProfile{
		UserID:      "existing",
		Description: &oldDesc,
	}
	updated := `{
		"id": "https://remote.example/users/alice",
		"type": "Person",
		"preferredUsername": "alice",
		"inbox": "https://remote.example/users/alice/inbox",
		"summary": "new bio",
		"publicKey": {"publicKeyPem": "REFRESHED"}
	}`
	r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{body: []byte(updated)}, idGen)
	_, err := r.ResolveActor(uri)
	require.NoError(t, err)
	require.NotNil(t, repo.Profiles["existing"].Description)
	assert.Equal(t, "new bio", *repo.Profiles["existing"].Description)
}

// TestResolveActor_TTLRefreshConvertsHTMLDescription guards #1140 on the
// refresh path: 既存 remote actor が 旧 mk-go (生 HTML 保存) で取り込まれて
// いた場合、次の TTL refresh で MFM 化されて natural healing する。本 PR で
// fix した extractRemoteDescription は initial insert + refresh 両方から
// 呼ばれるので、symmetry で動作するが、明示的な regression guard としても
// refresh path を直接 cover する。
func TestResolveActor_TTLRefreshConvertsHTMLDescription(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	uri := "https://mstdn.example/users/bob"
	old := time.Now().Add(-48 * time.Hour)
	repo.Users["existing-bob"] = &model.User{
		ID:            "existing-bob",
		Username:      "bob",
		URI:           &uri,
		LastFetchedAt: &old,
	}
	// 既存 row には旧 mk-go 由来の生 HTML が入っている state を再現。
	staleDesc := "<p>old html bio</p>"
	repo.Profiles["existing-bob"] = &model.UserProfile{
		UserID:      "existing-bob",
		Description: &staleDesc,
	}
	updated := `{
		"id": "https://mstdn.example/users/bob",
		"type": "Person",
		"preferredUsername": "bob",
		"inbox": "https://mstdn.example/users/bob/inbox",
		"summary": "<p>fresh html bio from Mastodon</p>",
		"publicKey": {"publicKeyPem": "REFRESHED"}
	}`
	r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{body: []byte(updated)}, idGen)
	_, err := r.ResolveActor(uri)
	require.NoError(t, err)
	got := repo.Profiles["existing-bob"].Description
	require.NotNil(t, got)
	assert.Equal(t, "fresh html bio from Mastodon", *got,
		"refresh path は extractRemoteDescription 経由なので MFM 変換が効く")
	assert.NotContains(t, *got, "<p>", "natural healing で生 HTML が残らない")
}

// 既存 user で profile 行が無い (= 本 fix 以前に取り込まれた remote user) は
// refresh 経路で back-fill される (#1022)。production の漸進的な救済経路。
func TestResolveActor_TTLRefreshBackfillsProfile(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	uri := "https://remote.example/users/alice"
	old := time.Now().Add(-48 * time.Hour)
	host := "remote.example"
	repo.Users["existing"] = &model.User{
		ID:            "existing",
		Username:      "alice",
		Host:          &host,
		URI:           &uri,
		LastFetchedAt: &old,
	}
	// profile 行は意図的に未作成
	updated := `{
		"id": "https://remote.example/users/alice",
		"type": "Person",
		"preferredUsername": "alice",
		"inbox": "https://remote.example/users/alice/inbox",
		"summary": "back-filled bio",
		"publicKey": {"publicKeyPem": "REFRESHED"}
	}`
	r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{body: []byte(updated)}, idGen)
	_, err := r.ResolveActor(uri)
	require.NoError(t, err)
	profile, err := repo.FindProfileByUserID("existing")
	require.NoError(t, err, "back-fill で profile 行が作成される")
	require.NotNil(t, profile.Description)
	assert.Equal(t, "back-filled bio", *profile.Description)
}

func TestResolveActor_TTLRefreshUpdatesIconBanner(t *testing.T) {
	// refresh 時にリモートの icon / image が更新されていれば反映する。
	repo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	uri := "https://remote.example/users/alice"
	old := time.Now().Add(-48 * time.Hour)
	oldAvatar := "https://remote.example/old-avatar.png"
	oldBanner := "https://remote.example/old-banner.png"
	repo.Users["existing"] = &model.User{
		ID:            "existing",
		Username:      "alice",
		URI:           &uri,
		AvatarURL:     &oldAvatar,
		BannerURL:     &oldBanner,
		LastFetchedAt: &old,
	}
	updated := `{
		"id": "https://remote.example/users/alice",
		"type": "Person",
		"preferredUsername": "alice",
		"inbox": "https://remote.example/users/alice/inbox",
		"icon": {"type": "Image", "url": "https://remote.example/new-avatar.png"},
		"image": {"type": "Image", "url": "https://remote.example/new-banner.png"},
		"publicKey": {"publicKeyPem": "REFRESHED"}
	}`
	r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{body: []byte(updated)}, idGen)
	user, err := r.ResolveActor(uri)
	require.NoError(t, err)
	require.NotNil(t, user.AvatarURL)
	assert.Equal(t, "https://remote.example/new-avatar.png", *user.AvatarURL)
	require.NotNil(t, user.BannerURL)
	assert.Equal(t, "https://remote.example/new-banner.png", *user.BannerURL)
}

func TestResolveActor_TTLRefreshPreservesIconWhenOmitted(t *testing.T) {
	// refresh 先の actor から icon が欠落していたら既存値を保持する (削除は追わない)。
	repo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	uri := "https://remote.example/users/alice"
	old := time.Now().Add(-48 * time.Hour)
	oldAvatar := "https://remote.example/keep-avatar.png"
	repo.Users["existing"] = &model.User{
		ID:            "existing",
		Username:      "alice",
		URI:           &uri,
		AvatarURL:     &oldAvatar,
		LastFetchedAt: &old,
	}
	// icon / image を含まない更新 actor
	updated := `{
		"id": "https://remote.example/users/alice",
		"type": "Person",
		"preferredUsername": "alice",
		"inbox": "https://remote.example/users/alice/inbox",
		"publicKey": {"publicKeyPem": "REFRESHED"}
	}`
	r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{body: []byte(updated)}, idGen)
	user, err := r.ResolveActor(uri)
	require.NoError(t, err)
	require.NotNil(t, user.AvatarURL)
	assert.Equal(t, "https://remote.example/keep-avatar.png", *user.AvatarURL)
}

func TestResolveActor_TTLRefreshUpdatesIsLocked(t *testing.T) {
	// TTL refresh でリモートが manuallyApprovesFollowers を true→false or
	// false→true に切り替えたときにローカルの IsLocked も追従する。
	repo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	uri := "https://remote.example/users/alice"
	old := time.Now().Add(-48 * time.Hour)
	repo.Users["existing"] = &model.User{
		ID:            "existing",
		Username:      "alice",
		URI:           &uri,
		IsLocked:      false,
		LastFetchedAt: &old,
	}
	updated := `{
		"id": "https://remote.example/users/alice",
		"type": "Person",
		"preferredUsername": "alice",
		"inbox": "https://remote.example/users/alice/inbox",
		"manuallyApprovesFollowers": true,
		"publicKey": {"publicKeyPem": "REFRESHED"}
	}`
	r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{body: []byte(updated)}, idGen)
	user, err := r.ResolveActor(uri)
	require.NoError(t, err)
	assert.True(t, user.IsLocked, "IsLocked should flip to true on refresh when remote opts in")
}

func TestResolveActor_NoRefreshWhenFresh(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	uri := "https://remote.example/users/alice"
	// LastFetchedAt が十分新しいので refresh は走らない (refreshActor は呼ばれない)
	now := time.Now()
	originalName := "Original"
	repo.Users["existing"] = &model.User{
		ID:            "existing",
		Username:      "alice",
		URI:           &uri,
		Name:          &originalName,
		LastFetchedAt: &now,
	}
	// fetcher が何を返しても name は更新されないはず
	r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{body: []byte(sampleActor)}, idGen)
	// 公開鍵キャッシュは空 → refreshPublicKey 経路を通る
	user, err := r.ResolveActor(uri)
	require.NoError(t, err)
	require.NotNil(t, user.Name)
	assert.Equal(t, "Original", *user.Name)

	// publicKey は in-memory にキャッシュされたはず
	pem, err := r.PublicKeyForActor("existing")
	require.NoError(t, err)
	assert.Contains(t, pem, "FAKE")
}

func TestResolveActor_TTLRefreshFetchError(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	uri := "https://remote.example/users/alice"
	old := time.Now().Add(-48 * time.Hour)
	originalName := "Original"
	repo.Users["existing"] = &model.User{
		ID:            "existing",
		Username:      "alice",
		URI:           &uri,
		Name:          &originalName,
		LastFetchedAt: &old,
	}
	// fetcher エラーでも refresh はベストエフォートなので既存 user を返す
	r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{err: errors.New("net down")}, idGen)
	user, err := r.ResolveActor(uri)
	require.NoError(t, err)
	assert.Equal(t, "Original", *user.Name)
}

func TestPublicKeyForActor_DBFallback(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{body: []byte(sampleActor)}, idGen)
	pkRepo := testutil.NewMockUserPublickeyRepository()
	r.SetPublickeyRepo(pkRepo)

	// ResolveActorで公開鍵をin-memory + DBにキャッシュ
	user, err := r.ResolveActor("https://remote.example/users/alice")
	require.NoError(t, err)

	// DBに永続化されていることを確認
	require.Len(t, pkRepo.Keys, 1)
	pk := pkRepo.Keys[user.ID]
	require.NotNil(t, pk)
	assert.Equal(t, "https://remote.example/users/alice#main-key", pk.KeyID)
	assert.Contains(t, pk.KeyPEM, "FAKE")

	// in-memoryキャッシュを期限切れにする
	r.SetClock(func() time.Time { return time.Now().Add(48 * time.Hour) })

	// DBフォールバックで復元できることを確認
	pem, err := r.PublicKeyForActor(user.ID)
	require.NoError(t, err)
	assert.Contains(t, pem, "FAKE")
}

func TestPublicKeyForActor_DBFallback_NilRepo(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{body: []byte(sampleActor)}, idGen)
	// publickeyRepo未設定

	user, err := r.ResolveActor("https://remote.example/users/alice")
	require.NoError(t, err)

	// in-memoryキャッシュを期限切れにする
	r.SetClock(func() time.Time { return time.Now().Add(48 * time.Hour) })

	// DB無しなのでmiss
	_, err = r.PublicKeyForActor(user.ID)
	assert.Error(t, err)
}

func TestPublicKeyForActor_TTLExpiry(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{body: []byte(sampleActor)}, idGen)

	// 解決して publicKey をキャッシュする (現在時刻で fetched 扱い)
	user, err := r.ResolveActor("https://remote.example/users/alice")
	require.NoError(t, err)
	pem, err := r.PublicKeyForActor(user.ID)
	require.NoError(t, err)
	assert.Contains(t, pem, "FAKE")

	// 時計を進める → expired として miss するはず
	r.SetClock(func() time.Time { return time.Now().Add(48 * time.Hour) })
	_, err = r.PublicKeyForActor(user.ID)
	assert.Error(t, err)
}

func TestResolver_SetClockNil(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{body: []byte(sampleActor)}, idGen)
	// nil を渡しても panic / 変更なし
	r.SetClock(nil)
	// 直後に呼んでもデフォルト clock のまま動く
	_, err := r.ResolveActor("https://remote.example/users/alice")
	require.NoError(t, err)
}

// stubInstanceTracker collects host registrations for assertions.
type stubInstanceTracker struct {
	hosts []string
}

func (s *stubInstanceTracker) RegisterFromHost(host string) (*model.Instance, error) {
	s.hosts = append(s.hosts, host)
	return nil, nil
}

func TestResolveActor_NotifiesInstanceTrackerOnCreate(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{body: []byte(sampleActor)}, idGen)
	tracker := &stubInstanceTracker{}
	r.SetInstanceTracker(tracker)

	_, err := r.ResolveActor("https://remote.example/users/alice")
	require.NoError(t, err)
	require.Len(t, tracker.hosts, 1)
	assert.Equal(t, "remote.example", tracker.hosts[0])
}

func TestResolveActor_NotifiesInstanceTrackerOnRefresh(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	uri := "https://remote.example/users/alice"
	host := "remote.example"
	old := time.Now().Add(-48 * time.Hour)
	repo.Users["existing"] = &model.User{
		ID:            "existing",
		Username:      "alice",
		URI:           &uri,
		Host:          &host,
		LastFetchedAt: &old,
	}
	r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{body: []byte(sampleActor)}, idGen)
	tracker := &stubInstanceTracker{}
	r.SetInstanceTracker(tracker)

	_, err := r.ResolveActor(uri)
	require.NoError(t, err)
	require.Len(t, tracker.hosts, 1)
	assert.Equal(t, "remote.example", tracker.hosts[0])
}

func TestResolveActor_NoTrackerNoOp(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{body: []byte(sampleActor)}, idGen)
	r.SetInstanceTracker(nil)
	_, err := r.ResolveActor("https://remote.example/users/alice")
	require.NoError(t, err)
}

// stubChartHook captures chart hook fires from the federation resolver.
type stubChartHook struct {
	users []string // user IDs
}

func (s *stubChartHook) OnRemoteUserCreated(u *model.User) {
	s.users = append(s.users, u.ID)
}

func TestResolveActor_ChartHookFiresOnNewUser(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{body: []byte(sampleActor)}, idGen)
	hook := &stubChartHook{}
	r.SetChartHook(hook)

	user, err := r.ResolveActor("https://remote.example/users/alice")
	require.NoError(t, err)
	require.Len(t, hook.users, 1)
	assert.Equal(t, user.ID, hook.users[0])
}

// stubHashtagHook captures hashtag hook fires from the federation resolver.
// #680: IngestNote / UpdateRemoteNote が note.Tags 非空時に呼ぶことを保証する。
type stubHashtagHook struct {
	calls []hashtagHookCall
}

type hashtagHookCall struct {
	noteID   string
	authorID string
	tags     []string
	isLocal  bool
}

func (s *stubHashtagHook) OnNoteCreated(n *model.Note, a *model.User) {
	s.calls = append(s.calls, hashtagHookCall{
		noteID:   n.ID,
		authorID: a.ID,
		tags:     []string(n.Tags),
		isLocal:  a.IsLocal(),
	})
}

func TestIngestNote_HashtagHookFiresOnRemoteIngest(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{body: []byte(sampleActor)}, idGen)
	hook := &stubHashtagHook{}
	r.SetHashtagHook(hook)

	body := `{
		"id": "https://remote.example/notes/hh1",
		"type": "Note",
		"attributedTo": "https://remote.example/users/alice",
		"content": "tagged #golang",
		"to": ["https://www.w3.org/ns/activitystreams#Public"]
	}`
	note, err := r.IngestNote([]byte(body))
	require.NoError(t, err)
	require.Len(t, hook.calls, 1)
	assert.Equal(t, note.ID, hook.calls[0].noteID)
	assert.False(t, hook.calls[0].isLocal, "remote actor → isLocal=false")
	assert.Contains(t, hook.calls[0].tags, "golang")
}

func TestIngestNote_HashtagHookSkipsWhenNoTags(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{body: []byte(sampleActor)}, idGen)
	hook := &stubHashtagHook{}
	r.SetHashtagHook(hook)

	body := `{
		"id": "https://remote.example/notes/hh2",
		"type": "Note",
		"attributedTo": "https://remote.example/users/alice",
		"content": "no tags here",
		"to": ["https://www.w3.org/ns/activitystreams#Public"]
	}`
	_, err := r.IngestNote([]byte(body))
	require.NoError(t, err)
	assert.Empty(t, hook.calls, "hashtag 抽出が空なら hook は呼ばれない")
}

func TestUpdateRemoteNote_HashtagHookFiresOnTagsChange(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	uri := "https://remote.example/notes/hh3"
	host := "remote.example"
	noteRepo.Notes["hh3"] = &model.Note{
		ID: "hh3", URI: &uri, UserID: "alice-id", UserHost: &host,
		Tags: []string{"old"},
	}
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{}, idGen)
	hook := &stubHashtagHook{}
	r.SetHashtagHook(hook)

	body := `{
		"id": "https://remote.example/notes/hh3",
		"type": "Note",
		"attributedTo": "https://remote.example/users/alice",
		"content": "edited and now tagged #news"
	}`
	_, err := r.UpdateRemoteNote([]byte(body))
	require.NoError(t, err)
	require.Len(t, hook.calls, 1)
	assert.Equal(t, "hh3", hook.calls[0].noteID)
	assert.Equal(t, "alice-id", hook.calls[0].authorID)
	assert.False(t, hook.calls[0].isLocal)
	assert.Contains(t, hook.calls[0].tags, "news")
}

func TestUpdateRemoteNote_HashtagHookSkipsWhenTagsUnchanged(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	uri := "https://remote.example/notes/hh4"
	host := "remote.example"
	noteRepo.Notes["hh4"] = &model.Note{
		ID: "hh4", URI: &uri, UserID: "alice-id", UserHost: &host,
		Tags: []string{"news"},
	}
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{}, idGen)
	hook := &stubHashtagHook{}
	r.SetHashtagHook(hook)

	body := `{
		"id": "https://remote.example/notes/hh4",
		"type": "Note",
		"attributedTo": "https://remote.example/users/alice",
		"content": "edit body but tag unchanged #news"
	}`
	_, err := r.UpdateRemoteNote([]byte(body))
	require.NoError(t, err)
	assert.Empty(t, hook.calls, "tags が変化しなければ hook は呼ばれない")
}

func TestResolveActor_FreshButCacheMissFetchError(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	uri := "https://remote.example/users/alice"
	now := time.Now()
	repo.Users["existing"] = &model.User{
		ID:            "existing",
		Username:      "alice",
		URI:           &uri,
		LastFetchedAt: &now,
	}
	// publicKey は cache 空 → refreshPublicKey 経路に入るが fetcher はエラー
	r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{err: errors.New("net down")}, idGen)
	user, err := r.ResolveActor(uri)
	require.NoError(t, err)
	assert.Equal(t, "existing", user.ID)
	// fetch 失敗時もエラーは伝搬しない
	_, perr := r.PublicKeyForActor("existing")
	assert.Error(t, perr)
}

func TestResolver_SetActorTTL(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{body: []byte(sampleActor)}, idGen)
	// 0 / 負値は無視されデフォルト維持
	r.SetActorTTL(0)
	r.SetActorTTL(-1)
	r.SetActorTTL(time.Minute)

	// 1 分 TTL に設定し、LastFetchedAt が 5 分前のユーザーを refresh するか確認
	uri := "https://remote.example/users/alice"
	old := time.Now().Add(-5 * time.Minute)
	originalName := "Original"
	repo.Users["existing"] = &model.User{
		ID:            "existing",
		Username:      "alice",
		URI:           &uri,
		Name:          &originalName,
		LastFetchedAt: &old,
	}
	user, err := r.ResolveActor(uri)
	require.NoError(t, err)
	require.NotNil(t, user.Name)
	assert.Equal(t, "Alice", *user.Name) // sampleActor の name で上書き
}

func TestRefreshPublicKey_OnExistingUser_FetchError(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	uri := "https://remote.example/users/alice"
	repo.Users["existing"] = &model.User{ID: "existing", Username: "alice", URI: &uri}
	noteRepo := testutil.NewMockNoteRepository()
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{err: errors.New("oops")}, idGen)
	user, err := r.ResolveActor(uri)
	require.NoError(t, err)
	assert.Equal(t, "existing", user.ID)
	// fetch失敗してもエラーは返さない
	_, err = r.PublicKeyForActor("existing")
	assert.Error(t, err)
}

// --- multi actor type support (#153) ---

// actorJSON renders a minimal actor JSON document with the given type.
func actorJSON(actorType string) string {
	return fmt.Sprintf(`{
		"id": "https://remote.example/users/x",
		"type": %q,
		"preferredUsername": "x",
		"name": "X",
		"inbox": "https://remote.example/users/x/inbox",
		"endpoints": {"sharedInbox": "https://remote.example/inbox"},
		"publicKey": {
			"id": "https://remote.example/users/x#main-key",
			"owner": "https://remote.example/users/x",
			"publicKeyPem": "-----BEGIN PUBLIC KEY-----\nFAKE\n-----END PUBLIC KEY-----"
		}
	}`, actorType)
}

func TestResolveActor_AllValidActorTypesAccepted(t *testing.T) {
	for _, typ := range activitypub.ValidActorTypes {
		t.Run(typ, func(t *testing.T) {
			r, repo := newResolver(t, actorJSON(typ), nil)
			user, err := r.ResolveActor("https://remote.example/users/x")
			require.NoError(t, err)
			assert.Equal(t, "x", user.Username)
			assert.Len(t, repo.Users, 1)
		})
	}
}

func TestResolveActor_InvalidActorTypeRejected(t *testing.T) {
	for _, typ := range []string{"Note", "Tombstone", "Article", ""} {
		t.Run(typ, func(t *testing.T) {
			r, _ := newResolver(t, actorJSON(typ), nil)
			_, err := r.ResolveActor("https://remote.example/users/x")
			require.ErrorIs(t, err, federation.ErrInvalidActor)
		})
	}
}

func TestResolveActor_ServiceTypeSetsIsBot(t *testing.T) {
	r, repo := newResolver(t, actorJSON("Service"), nil)
	user, err := r.ResolveActor("https://remote.example/users/x")
	require.NoError(t, err)
	assert.True(t, user.IsBot)
	// 永続化された user も isBot=true
	require.Len(t, repo.Users, 1)
	for _, u := range repo.Users {
		assert.True(t, u.IsBot)
	}
}

func TestResolveActor_ApplicationTypeSetsIsBot(t *testing.T) {
	r, _ := newResolver(t, actorJSON("Application"), nil)
	user, err := r.ResolveActor("https://remote.example/users/x")
	require.NoError(t, err)
	assert.True(t, user.IsBot)
}

func TestResolveActor_GroupTypeDoesNotSetIsBot(t *testing.T) {
	r, _ := newResolver(t, actorJSON("Group"), nil)
	user, err := r.ResolveActor("https://remote.example/users/x")
	require.NoError(t, err)
	assert.False(t, user.IsBot)
}

func TestResolveActor_OrganizationTypeDoesNotSetIsBot(t *testing.T) {
	r, _ := newResolver(t, actorJSON("Organization"), nil)
	user, err := r.ResolveActor("https://remote.example/users/x")
	require.NoError(t, err)
	assert.False(t, user.IsBot)
}

// #692: refresh で `_misskey_canChat: false` を受け取ったら chatScope を
// "none" に書き換える (DB / メモリ両方)。flag 欠落時は既存 chatScope を
// 保持する (連合先が一時的に field を消した場合の保護)。
func TestRefreshActor_UpdatesChatScopeFromCanChat(t *testing.T) {
	cases := []struct {
		name         string
		bodyFlag     string
		initialScope string
		want         string
	}{
		{"false_flag_sets_none", `, "_misskey_canChat": false`, "everyone", "none"},
		{"true_flag_sets_everyone", `, "_misskey_canChat": true`, "none", "everyone"},
		{"missing_keeps_existing", "", "everyone", "everyone"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := testutil.NewMockUserRepository()
			uri := "https://remote.example/users/x"
			// LastFetchedAt を TTL より十分過去にして shouldRefreshActor →
			// refreshActor 経路を強制発火させる (resolveActorOnce の cold path
			// 内で refresh が実際に走らないと canChat の取込みが行われない)。
			stale := time.Now().Add(-48 * time.Hour)
			repo.Users["existing"] = &model.User{
				ID:            "existing",
				Username:      "x",
				URI:           &uri,
				ChatScope:     tc.initialScope,
				LastFetchedAt: &stale,
			}
			body := `{
				"id": "https://remote.example/users/x",
				"type": "Person",
				"preferredUsername": "x",
				"inbox": "https://remote.example/users/x/inbox",
				"publicKey": {"publicKeyPem": "FAKE"}` + tc.bodyFlag + `
			}`
			noteRepo := testutil.NewMockNoteRepository()
			urls := activitypub.NewURLBuilder("https://example.com")
			idGen, _ := id.NewGenerator("aidx")
			r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{body: []byte(body)}, idGen)
			user, err := r.ResolveActor(uri)
			require.NoError(t, err)
			assert.Equal(t, tc.want, user.ChatScope)
		})
	}
}

func TestRefreshActor_UpdatesIsBotOnTypeChange(t *testing.T) {
	// 既存 Person ユーザーが Service に切り替わった場合、refresh で IsBot=true
	// に追従する。
	repo := testutil.NewMockUserRepository()
	uri := "https://remote.example/users/x"
	stale := time.Now().Add(-48 * time.Hour) // TTL超過
	repo.Users["existing"] = &model.User{
		ID:            "existing",
		Username:      "x",
		URI:           &uri,
		IsBot:         false,
		LastFetchedAt: &stale,
	}
	noteRepo := testutil.NewMockNoteRepository()
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{body: []byte(actorJSON("Service"))}, idGen)

	user, err := r.ResolveActor(uri)
	require.NoError(t, err)
	assert.True(t, user.IsBot)
}

// --- extractEmojiTags / upsertEmojis (#330) ----------------------------------

func TestExtractEmojiTags(t *testing.T) {
	t.Run("valid emoji tag", func(t *testing.T) {
		tags := []any{
			map[string]any{
				"type": "Emoji",
				"name": ":blobcat:",
				"icon": map[string]any{
					"type": "Image",
					"url":  "https://remote.example/emojis/blobcat.webp",
				},
				"id":      "https://remote.example/emojis/blobcat",
				"updated": "2025-01-01T00:00:00Z",
			},
		}
		got := federation.ExtractEmojiTags(tags)
		require.Len(t, got, 1)
		assert.Equal(t, "Emoji", got[0].Type)
		assert.Equal(t, ":blobcat:", got[0].Name)
		assert.Equal(t, "https://remote.example/emojis/blobcat.webp", got[0].Icon.URL)
		assert.Equal(t, "https://remote.example/emojis/blobcat", got[0].ID)
		assert.Equal(t, "2025-01-01T00:00:00Z", got[0].Updated)
		// #731: AP tag に `_misskey_license` が無いケースは License = nil
		assert.Nil(t, got[0].License, "no license wrapper → nil")
	})

	t.Run("emoji tag with _misskey_license", func(t *testing.T) {
		// #731: upstream renderEmoji は `_misskey_license: {freeText: ...}` で
		// license を federate する。mk-go の extractEmojiTags はこれを拾って
		// EmojiTag.License に non-nil の wrapper を入れる。
		tags := []any{
			map[string]any{
				"type": "Emoji",
				"name": ":licensed:",
				"icon": map[string]any{
					"type": "Image",
					"url":  "https://remote.example/emojis/licensed.webp",
				},
				"_misskey_license": map[string]any{
					"freeText": "CC-BY-4.0",
				},
			},
		}
		got := federation.ExtractEmojiTags(tags)
		require.Len(t, got, 1)
		require.NotNil(t, got[0].License)
		require.NotNil(t, got[0].License.FreeText)
		assert.Equal(t, "CC-BY-4.0", *got[0].License.FreeText)
	})

	t.Run("emoji tag _misskey_license null freeText keeps FreeText nil", func(t *testing.T) {
		// upstream renderEmoji は emoji.license=null でも wrapper を出す。
		// 3 状態を区別する設計 (#731):
		//   - wrapper 欠落 → License = nil ("license 情報が federate されて
		//     いない", 既存値温存)
		//   - wrapper あり + freeText=null → FreeText = nil ("license は明示
		//     的に未設定", NULL 上書き OK)
		//   - wrapper あり + freeText=string → 具体値
		// JSON null は string assertion が失敗するので FreeText は nil のまま。
		tags := []any{
			map[string]any{
				"type": "Emoji",
				"name": ":no_license:",
				"icon": map[string]any{
					"type": "Image",
					"url":  "https://remote.example/emojis/no_license.webp",
				},
				"_misskey_license": map[string]any{
					"freeText": nil,
				},
			},
		}
		got := federation.ExtractEmojiTags(tags)
		require.Len(t, got, 1)
		require.NotNil(t, got[0].License, "wrapper present even when freeText is null")
		assert.Nil(t, got[0].License.FreeText, "JSON null freeText → FreeText nil (explicit no license)")
	})

	t.Run("non-emoji tags filtered out", func(t *testing.T) {
		tags := []any{
			map[string]any{
				"type": "Mention",
				"name": "@alice",
				"href": "https://remote.example/users/alice",
			},
			map[string]any{
				"type": "Hashtag",
				"name": "#golang",
				"href": "https://remote.example/tags/golang",
			},
			map[string]any{
				"type": "Emoji",
				"name": ":valid:",
				"icon": map[string]any{"url": "https://remote.example/emojis/valid.png"},
			},
		}
		got := federation.ExtractEmojiTags(tags)
		require.Len(t, got, 1)
		assert.Equal(t, ":valid:", got[0].Name)
	})

	t.Run("missing icon URL skipped", func(t *testing.T) {
		tags := []any{
			map[string]any{
				"type": "Emoji",
				"name": ":noicon:",
				// icon未設定
			},
			map[string]any{
				"type": "Emoji",
				"name": ":emptyurl:",
				"icon": map[string]any{"url": ""},
			},
		}
		got := federation.ExtractEmojiTags(tags)
		assert.Len(t, got, 0)
	})

	t.Run("empty name skipped", func(t *testing.T) {
		tags := []any{
			map[string]any{
				"type": "Emoji",
				"name": "",
				"icon": map[string]any{"url": "https://remote.example/e.png"},
			},
		}
		got := federation.ExtractEmojiTags(tags)
		assert.Len(t, got, 0)
	})

	t.Run("nil and non-map entries skipped", func(t *testing.T) {
		tags := []any{
			nil,
			"string entry",
			42,
			map[string]any{
				"type": "Emoji",
				"name": ":ok:",
				"icon": map[string]any{"url": "https://remote.example/ok.png"},
			},
		}
		got := federation.ExtractEmojiTags(tags)
		require.Len(t, got, 1)
		assert.Equal(t, ":ok:", got[0].Name)
	})

	t.Run("empty tags returns nil", func(t *testing.T) {
		got := federation.ExtractEmojiTags(nil)
		assert.Nil(t, got)
		got = federation.ExtractEmojiTags([]any{})
		assert.Nil(t, got)
	})

	t.Run("multiple valid emojis", func(t *testing.T) {
		tags := []any{
			map[string]any{
				"type": "Emoji",
				"name": ":cat:",
				"icon": map[string]any{"url": "https://remote.example/cat.png"},
			},
			map[string]any{
				"type": "Emoji",
				"name": ":dog:",
				"icon": map[string]any{"url": "https://remote.example/dog.png"},
				"id":   "https://remote.example/emojis/dog",
			},
		}
		got := federation.ExtractEmojiTags(tags)
		require.Len(t, got, 2)
		assert.Equal(t, ":cat:", got[0].Name)
		assert.Equal(t, ":dog:", got[1].Name)
	})
}

func TestUpsertEmojis(t *testing.T) {
	t.Run("creates new emoji", func(t *testing.T) {
		repo := testutil.NewMockUserRepository()
		noteRepo := testutil.NewMockNoteRepository()
		urls := activitypub.NewURLBuilder("https://example.com")
		idGen, _ := id.NewGenerator("aidx")
		r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{}, idGen)
		emojiRepo := testutil.NewMockEmojiRepository()
		r.SetEmojiRepo(emojiRepo)

		tags := []activitypub.EmojiTag{
			{
				Type: "Emoji",
				Name: ":blobcat:",
				Icon: activitypub.Image{Type: "Image", URL: "https://remote.example/blobcat.webp"},
				ID:   "https://remote.example/emojis/blobcat",
			},
		}
		names := r.UpsertEmojis(tags, "remote.example")
		assert.Equal(t, []string{"blobcat"}, []string(names))

		// emojiRepoにcreateされたことを確認
		e, err := emojiRepo.FindByNameAndHost("blobcat", strPtr("remote.example"))
		require.NoError(t, err)
		assert.Equal(t, "blobcat", e.Name)
		assert.Equal(t, "https://remote.example/blobcat.webp", e.OriginalURL)
		assert.Equal(t, "https://remote.example/blobcat.webp", e.PublicURL)
		require.NotNil(t, e.URI)
		assert.Equal(t, "https://remote.example/emojis/blobcat", *e.URI)
		require.NotNil(t, e.Host)
		assert.Equal(t, "remote.example", *e.Host)
	})

	t.Run("populates license from _misskey_license", func(t *testing.T) {
		// #731: upstream Misskey TS が AP `_misskey_license.freeText` で
		// federate する license を新規 emoji 取り込み時に保存する。
		repo := testutil.NewMockUserRepository()
		noteRepo := testutil.NewMockNoteRepository()
		urls := activitypub.NewURLBuilder("https://example.com")
		idGen, _ := id.NewGenerator("aidx")
		r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{}, idGen)
		emojiRepo := testutil.NewMockEmojiRepository()
		r.SetEmojiRepo(emojiRepo)

		licenseText := "CC-BY-4.0"
		tags := []activitypub.EmojiTag{
			{
				Type:    "Emoji",
				Name:    ":licensed:",
				Icon:    activitypub.Image{Type: "Image", URL: "https://remote.example/licensed.webp"},
				License: &activitypub.MisskeyLicense{FreeText: &licenseText},
			},
		}
		r.UpsertEmojis(tags, "remote.example")

		e, err := emojiRepo.FindByNameAndHost("licensed", strPtr("remote.example"))
		require.NoError(t, err)
		require.NotNil(t, e.License, "license should be populated from AP _misskey_license")
		assert.Equal(t, "CC-BY-4.0", *e.License)
	})

	t.Run("updates existing license when AP tag has different value", func(t *testing.T) {
		// #731: AP tag に新しい license が来たら既存値を上書きする経路。
		// pointerStringsEqual の "1 つ nil / 値変化" 分岐をカバー。
		repo := testutil.NewMockUserRepository()
		noteRepo := testutil.NewMockNoteRepository()
		urls := activitypub.NewURLBuilder("https://example.com")
		idGen, _ := id.NewGenerator("aidx")
		r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{}, idGen)
		emojiRepo := testutil.NewMockEmojiRepository()
		r.SetEmojiRepo(emojiRepo)

		host := "remote.example"
		oldLicense := "old"
		emojiRepo.Emojis["chg@remote.example"] = &model.Emoji{
			ID: "ex", Name: "chg", Host: &host,
			OriginalURL: "https://remote.example/chg.webp",
			PublicURL:   "https://remote.example/chg.webp",
			License:     &oldLicense,
		}

		newLicense := "new-license"
		tags := []activitypub.EmojiTag{
			{
				Type:    "Emoji",
				Name:    ":chg:",
				Icon:    activitypub.Image{Type: "Image", URL: "https://remote.example/chg.webp"},
				License: &activitypub.MisskeyLicense{FreeText: &newLicense},
			},
		}
		r.UpsertEmojis(tags, host)

		e, err := emojiRepo.FindByNameAndHost("chg", &host)
		require.NoError(t, err)
		require.NotNil(t, e.License)
		assert.Equal(t, "new-license", *e.License, "license should be updated to new value")
	})

	t.Run("no-op when license unchanged", func(t *testing.T) {
		// #731: 同じ license が再 federate されても update 不要。
		// pointerStringsEqual が true を返す経路をカバー。
		repo := testutil.NewMockUserRepository()
		noteRepo := testutil.NewMockNoteRepository()
		urls := activitypub.NewURLBuilder("https://example.com")
		idGen, _ := id.NewGenerator("aidx")
		r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{}, idGen)
		emojiRepo := testutil.NewMockEmojiRepository()
		r.SetEmojiRepo(emojiRepo)

		host := "remote.example"
		same := "same-license"
		emojiRepo.Emojis["nop@remote.example"] = &model.Emoji{
			ID: "ex", Name: "nop", Host: &host,
			OriginalURL: "https://remote.example/nop.webp",
			PublicURL:   "https://remote.example/nop.webp",
			License:     &same,
		}

		// 同じ license 値で再 federate
		sameAgain := "same-license"
		tags := []activitypub.EmojiTag{
			{
				Type:    "Emoji",
				Name:    ":nop:",
				Icon:    activitypub.Image{Type: "Image", URL: "https://remote.example/nop.webp"},
				License: &activitypub.MisskeyLicense{FreeText: &sameAgain},
			},
		}
		r.UpsertEmojis(tags, host)

		e, err := emojiRepo.FindByNameAndHost("nop", &host)
		require.NoError(t, err)
		require.NotNil(t, e.License)
		assert.Equal(t, "same-license", *e.License, "value unchanged")
	})

	t.Run("clears existing license when AP tag explicitly federates null freeText", func(t *testing.T) {
		// #731: wrapper あり + freeText=null は "license を明示的に解除" の
		// シグナル。既存 license を NULL で上書きする経路。pointerStringsEqual
		// の "片方 nil" 分岐をカバー。
		repo := testutil.NewMockUserRepository()
		noteRepo := testutil.NewMockNoteRepository()
		urls := activitypub.NewURLBuilder("https://example.com")
		idGen, _ := id.NewGenerator("aidx")
		r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{}, idGen)
		emojiRepo := testutil.NewMockEmojiRepository()
		r.SetEmojiRepo(emojiRepo)

		host := "remote.example"
		oldLicense := "to-clear"
		emojiRepo.Emojis["clr@remote.example"] = &model.Emoji{
			ID: "ex", Name: "clr", Host: &host,
			OriginalURL: "https://remote.example/clr.webp",
			PublicURL:   "https://remote.example/clr.webp",
			License:     &oldLicense,
		}

		// freeText=nil (= JSON null 受信) で wrapper あり
		tags := []activitypub.EmojiTag{
			{
				Type:    "Emoji",
				Name:    ":clr:",
				Icon:    activitypub.Image{Type: "Image", URL: "https://remote.example/clr.webp"},
				License: &activitypub.MisskeyLicense{FreeText: nil},
			},
		}
		r.UpsertEmojis(tags, host)

		e, err := emojiRepo.FindByNameAndHost("clr", &host)
		require.NoError(t, err)
		assert.Nil(t, e.License, "explicit null freeText should clear existing license")
	})

	t.Run("preserves existing license when AP tag has no _misskey_license wrapper", func(t *testing.T) {
		// #731: AP tag の `_misskey_license` が欠落している場合、既存 emoji の
		// license は温存する (連合先が一時的に license export を停止しても
		// 上書きしない)。
		repo := testutil.NewMockUserRepository()
		noteRepo := testutil.NewMockNoteRepository()
		urls := activitypub.NewURLBuilder("https://example.com")
		idGen, _ := id.NewGenerator("aidx")
		r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{}, idGen)
		emojiRepo := testutil.NewMockEmojiRepository()
		r.SetEmojiRepo(emojiRepo)

		host := "remote.example"
		old := "old-license"
		emojiRepo.Emojis["preserved@remote.example"] = &model.Emoji{
			ID: "existing", Name: "preserved", Host: &host,
			OriginalURL: "https://remote.example/preserved.webp",
			PublicURL:   "https://remote.example/preserved.webp",
			License:     &old,
		}

		// License: nil の AP tag (= wrapper 欠落)
		tags := []activitypub.EmojiTag{
			{
				Type: "Emoji",
				Name: ":preserved:",
				Icon: activitypub.Image{Type: "Image", URL: "https://remote.example/preserved.webp"},
			},
		}
		r.UpsertEmojis(tags, host)

		e, err := emojiRepo.FindByNameAndHost("preserved", &host)
		require.NoError(t, err)
		require.NotNil(t, e.License)
		assert.Equal(t, "old-license", *e.License, "existing license must be preserved when wrapper is missing")
	})

	t.Run("updates existing emoji URL", func(t *testing.T) {
		repo := testutil.NewMockUserRepository()
		noteRepo := testutil.NewMockNoteRepository()
		urls := activitypub.NewURLBuilder("https://example.com")
		idGen, _ := id.NewGenerator("aidx")
		r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{}, idGen)
		emojiRepo := testutil.NewMockEmojiRepository()
		r.SetEmojiRepo(emojiRepo)

		host := "remote.example"
		// 既存の絵文字をrepoに配置
		emojiRepo.Emojis["blobcat@remote.example"] = &model.Emoji{
			ID:          "existing-id",
			Name:        "blobcat",
			Host:        &host,
			OriginalURL: "https://remote.example/old-blobcat.webp",
			PublicURL:   "https://remote.example/old-blobcat.webp",
		}

		tags := []activitypub.EmojiTag{
			{
				Type: "Emoji",
				Name: ":blobcat:",
				Icon: activitypub.Image{URL: "https://remote.example/new-blobcat.webp"},
				ID:   "https://remote.example/emojis/blobcat",
			},
		}
		names := r.UpsertEmojis(tags, "remote.example")
		assert.Equal(t, []string{"blobcat"}, []string(names))

		// URLが更新されたことを確認
		e, err := emojiRepo.FindByNameAndHost("blobcat", &host)
		require.NoError(t, err)
		assert.Equal(t, "https://remote.example/new-blobcat.webp", e.OriginalURL)
	})

	t.Run("nil emojiRepo returns empty array", func(t *testing.T) {
		repo := testutil.NewMockUserRepository()
		noteRepo := testutil.NewMockNoteRepository()
		urls := activitypub.NewURLBuilder("https://example.com")
		idGen, _ := id.NewGenerator("aidx")
		r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{}, idGen)
		// emojiRepo未設定

		tags := []activitypub.EmojiTag{
			{Name: ":x:", Icon: activitypub.Image{URL: "https://example.com/x.png"}},
		}
		names := r.UpsertEmojis(tags, "example.com")
		assert.Empty(t, names)
	})

	t.Run("empty tags returns empty array", func(t *testing.T) {
		repo := testutil.NewMockUserRepository()
		noteRepo := testutil.NewMockNoteRepository()
		urls := activitypub.NewURLBuilder("https://example.com")
		idGen, _ := id.NewGenerator("aidx")
		r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{}, idGen)
		emojiRepo := testutil.NewMockEmojiRepository()
		r.SetEmojiRepo(emojiRepo)

		names := r.UpsertEmojis(nil, "example.com")
		assert.Empty(t, names)
	})

	t.Run("colon-only name skipped", func(t *testing.T) {
		repo := testutil.NewMockUserRepository()
		noteRepo := testutil.NewMockNoteRepository()
		urls := activitypub.NewURLBuilder("https://example.com")
		idGen, _ := id.NewGenerator("aidx")
		r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{}, idGen)
		emojiRepo := testutil.NewMockEmojiRepository()
		r.SetEmojiRepo(emojiRepo)

		// "::" → Trim(":") → "" → スキップ
		tags := []activitypub.EmojiTag{
			{Name: "::", Icon: activitypub.Image{URL: "https://example.com/x.png"}},
		}
		names := r.UpsertEmojis(tags, "example.com")
		assert.Empty(t, names)
	})

	t.Run("emoji without URI does not set URI field", func(t *testing.T) {
		repo := testutil.NewMockUserRepository()
		noteRepo := testutil.NewMockNoteRepository()
		urls := activitypub.NewURLBuilder("https://example.com")
		idGen, _ := id.NewGenerator("aidx")
		r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{}, idGen)
		emojiRepo := testutil.NewMockEmojiRepository()
		r.SetEmojiRepo(emojiRepo)

		tags := []activitypub.EmojiTag{
			{Name: ":nouri:", Icon: activitypub.Image{URL: "https://example.com/nouri.png"}},
		}
		names := r.UpsertEmojis(tags, "remote.example")
		assert.Equal(t, []string{"nouri"}, []string(names))

		e, err := emojiRepo.FindByNameAndHost("nouri", strPtr("remote.example"))
		require.NoError(t, err)
		assert.Nil(t, e.URI)
	})

	t.Run("duplicate names deduplicated", func(t *testing.T) {
		repo := testutil.NewMockUserRepository()
		noteRepo := testutil.NewMockNoteRepository()
		urls := activitypub.NewURLBuilder("https://example.com")
		idGen, _ := id.NewGenerator("aidx")
		r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{}, idGen)
		emojiRepo := testutil.NewMockEmojiRepository()
		r.SetEmojiRepo(emojiRepo)

		// 同名タグが複数 (リモート側で重複している場合)
		tags := []activitypub.EmojiTag{
			{Name: ":dup:", Icon: activitypub.Image{URL: "https://remote.example/a.png"}},
			{Name: ":dup:", Icon: activitypub.Image{URL: "https://remote.example/b.png"}},
		}
		names := r.UpsertEmojis(tags, "remote.example")
		assert.Equal(t, []string{"dup"}, []string(names), "重複nameは1件に集約される")
	})

	t.Run("create error excludes name from result", func(t *testing.T) {
		repo := testutil.NewMockUserRepository()
		noteRepo := testutil.NewMockNoteRepository()
		urls := activitypub.NewURLBuilder("https://example.com")
		idGen, _ := id.NewGenerator("aidx")
		r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{}, idGen)
		emojiRepo := testutil.NewMockEmojiRepository()
		emojiRepo.CreateErr = errors.New("simulated db error")
		r.SetEmojiRepo(emojiRepo)

		tags := []activitypub.EmojiTag{
			{Name: ":fail:", Icon: activitypub.Image{URL: "https://remote.example/fail.png"}},
		}
		names := r.UpsertEmojis(tags, "remote.example")
		assert.Empty(t, names, "create失敗時はnameを返さない")
	})

	t.Run("update error keeps name in result", func(t *testing.T) {
		repo := testutil.NewMockUserRepository()
		noteRepo := testutil.NewMockNoteRepository()
		urls := activitypub.NewURLBuilder("https://example.com")
		idGen, _ := id.NewGenerator("aidx")
		r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{}, idGen)
		emojiRepo := testutil.NewMockEmojiRepository()

		host := "remote.example"
		emojiRepo.Emojis["existing@remote.example"] = &model.Emoji{
			ID:          "existing-id",
			Name:        "existing",
			Host:        &host,
			OriginalURL: "https://remote.example/old.png",
			PublicURL:   "https://remote.example/old.png",
		}
		emojiRepo.UpdateErr = errors.New("simulated update error")
		r.SetEmojiRepo(emojiRepo)

		tags := []activitypub.EmojiTag{
			{Name: ":existing:", Icon: activitypub.Image{URL: "https://remote.example/new.png"}},
		}
		names := r.UpsertEmojis(tags, "remote.example")
		assert.Equal(t, []string{"existing"}, []string(names),
			"update失敗でも行は存在するためnameは返す")
	})

	t.Run("batch lookup handles multiple existing and new in one call", func(t *testing.T) {
		repo := testutil.NewMockUserRepository()
		noteRepo := testutil.NewMockNoteRepository()
		urls := activitypub.NewURLBuilder("https://example.com")
		idGen, _ := id.NewGenerator("aidx")
		r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{}, idGen)
		emojiRepo := testutil.NewMockEmojiRepository()
		r.SetEmojiRepo(emojiRepo)

		host := "remote.example"
		emojiRepo.Emojis["existing@remote.example"] = &model.Emoji{
			ID: "ex-id", Name: "existing", Host: &host,
			OriginalURL: "https://remote.example/existing.png",
			PublicURL:   "https://remote.example/existing.png",
		}

		tags := []activitypub.EmojiTag{
			{Name: ":existing:", Icon: activitypub.Image{URL: "https://remote.example/existing.png"}},
			{Name: ":newone:", Icon: activitypub.Image{URL: "https://remote.example/newone.png"}},
			{Name: ":another:", Icon: activitypub.Image{URL: "https://remote.example/another.png"}},
		}
		names := r.UpsertEmojis(tags, "remote.example")
		assert.ElementsMatch(t, []string{"existing", "newone", "another"}, []string(names))
		assert.Equal(t, []string{"existing", "newone", "another"}, []string(names),
			"順序は元のタグ順を維持")
	})
}

// sampleActorWithEmoji is a Person JSON with emoji tags in the tag array.
const sampleActorWithEmoji = `{
	"id": "https://remote.example/users/alice",
	"type": "Person",
	"preferredUsername": "alice",
	"name": "Alice :blobcat:",
	"inbox": "https://remote.example/users/alice/inbox",
	"endpoints": {"sharedInbox": "https://remote.example/inbox"},
	"publicKey": {
		"id": "https://remote.example/users/alice#main-key",
		"owner": "https://remote.example/users/alice",
		"publicKeyPem": "-----BEGIN PUBLIC KEY-----\nFAKE\n-----END PUBLIC KEY-----"
	},
	"tag": [
		{
			"type": "Emoji",
			"name": ":blobcat:",
			"icon": {"type": "Image", "url": "https://remote.example/emojis/blobcat.webp"},
			"id": "https://remote.example/emojis/blobcat"
		},
		{
			"type": "Mention",
			"name": "@bob",
			"href": "https://remote.example/users/bob"
		}
	]
}`

func TestResolveActor_EmojiTagExtraction(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{body: []byte(sampleActorWithEmoji)}, idGen)
	emojiRepo := testutil.NewMockEmojiRepository()
	r.SetEmojiRepo(emojiRepo)

	user, err := r.ResolveActor("https://remote.example/users/alice")
	require.NoError(t, err)

	// user.Emojisに絵文字名 (コロンなし) が含まれることを確認
	require.Len(t, user.Emojis, 1)
	assert.Equal(t, "blobcat", user.Emojis[0])

	// emojiRepoに新規作成されたことを確認
	e, err := emojiRepo.FindByNameAndHost("blobcat", strPtr("remote.example"))
	require.NoError(t, err)
	assert.Equal(t, "blobcat", e.Name)
	assert.Equal(t, "https://remote.example/emojis/blobcat.webp", e.OriginalURL)
	require.NotNil(t, e.Host)
	assert.Equal(t, "remote.example", *e.Host)
}

func TestResolveActor_EmojiTagExtraction_NoEmojiRepo(t *testing.T) {
	// emojiRepoが未設定の場合、Emojisは空になるがエラーにはならない
	repo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{body: []byte(sampleActorWithEmoji)}, idGen)
	// SetEmojiRepoを呼ばない

	user, err := r.ResolveActor("https://remote.example/users/alice")
	require.NoError(t, err)
	assert.Empty(t, user.Emojis)
}

func TestRefreshActor_EmojiTagExtraction(t *testing.T) {
	// TTL超過でrefreshが走る場合、Tag配列から絵文字が抽出されることを確認
	repo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")

	uri := "https://remote.example/users/alice"
	host := "remote.example"
	stale := time.Now().Add(-48 * time.Hour)
	repo.Users["existing"] = &model.User{
		ID:            "existing",
		Username:      "alice",
		URI:           &uri,
		Host:          &host,
		LastFetchedAt: &stale,
	}

	r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{body: []byte(sampleActorWithEmoji)}, idGen)
	emojiRepo := testutil.NewMockEmojiRepository()
	r.SetEmojiRepo(emojiRepo)

	user, err := r.ResolveActor(uri)
	require.NoError(t, err)

	// refreshActorでEmojisが更新されたことを確認
	require.Len(t, user.Emojis, 1)
	assert.Equal(t, "blobcat", user.Emojis[0])

	// emojiRepoに作成されたことを確認
	e, err := emojiRepo.FindByNameAndHost("blobcat", &host)
	require.NoError(t, err)
	assert.Equal(t, "blobcat", e.Name)
}

// sampleNoteWithEmoji is a Note JSON with emoji tags in the tag array.
const sampleNoteWithEmoji = `{
	"id": "https://remote.example/notes/emoji1",
	"type": "Note",
	"attributedTo": "https://remote.example/users/alice",
	"content": "<p>Hello :blobcat: :partyparrot:</p>",
	"to": ["https://www.w3.org/ns/activitystreams#Public"],
	"tag": [
		{
			"type": "Emoji",
			"name": ":blobcat:",
			"icon": {"type": "Image", "url": "https://remote.example/emojis/blobcat.webp"},
			"id": "https://remote.example/emojis/blobcat"
		},
		{
			"type": "Emoji",
			"name": ":partyparrot:",
			"icon": {"type": "Image", "url": "https://remote.example/emojis/partyparrot.gif"},
			"id": "https://remote.example/emojis/partyparrot"
		},
		{
			"type": "Mention",
			"name": "@alice",
			"href": "https://remote.example/users/alice"
		}
	]
}`

func TestIngestNote_EmojiTagExtraction(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	// IngestNoteではまずNote JSONを処理してからactorを解決するためscriptedFetcherを使う
	// ただしIngestNoteは直接body []byteを受け取るので、actorの解決にだけfetcherが使われる
	r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{body: []byte(sampleActorWithEmoji)}, idGen)
	emojiRepo := testutil.NewMockEmojiRepository()
	r.SetEmojiRepo(emojiRepo)

	note, err := r.IngestNote([]byte(sampleNoteWithEmoji))
	require.NoError(t, err)

	// note.Emojisに絵文字名 (コロンなし) が含まれることを確認
	require.Len(t, note.Emojis, 2)
	assert.Contains(t, []string(note.Emojis), "blobcat")
	assert.Contains(t, []string(note.Emojis), "partyparrot")

	// emojiRepoに2つの絵文字が作成されたことを確認
	host := "remote.example"
	e1, err := emojiRepo.FindByNameAndHost("blobcat", &host)
	require.NoError(t, err)
	assert.Equal(t, "https://remote.example/emojis/blobcat.webp", e1.OriginalURL)

	e2, err := emojiRepo.FindByNameAndHost("partyparrot", &host)
	require.NoError(t, err)
	assert.Equal(t, "https://remote.example/emojis/partyparrot.gif", e2.OriginalURL)
}

func TestIngestNote_EmojiTagExtraction_NoEmojiRepo(t *testing.T) {
	// emojiRepoが未設定の場合でもIngestNoteは成功する (Emojisは空)
	repo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{body: []byte(sampleActor)}, idGen)
	// SetEmojiRepoを呼ばない

	note, err := r.IngestNote([]byte(sampleNoteWithEmoji))
	require.NoError(t, err)
	assert.Empty(t, note.Emojis)
}

func TestUpdateRemoteNote_EmojiTagExtraction(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	uri := "https://remote.example/notes/emoji1"
	host := "remote.example"
	original := "original text"
	noteRepo.Notes["emoji1"] = &model.Note{
		ID: "emoji1", URI: &uri, UserID: "alice-id", UserHost: &host, Text: &original,
	}
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{}, idGen)
	emojiRepo := testutil.NewMockEmojiRepository()
	r.SetEmojiRepo(emojiRepo)

	updateBody := []byte(`{
		"id": "https://remote.example/notes/emoji1",
		"type": "Note",
		"attributedTo": "https://remote.example/users/alice",
		"content": "updated :blobcat:",
		"tag": [
			{
				"type": "Emoji",
				"name": ":blobcat:",
				"icon": {"type": "Image", "url": "https://remote.example/emojis/blobcat.webp"},
				"id": "https://remote.example/emojis/blobcat"
			}
		]
	}`)
	got, err := r.UpdateRemoteNote(updateBody)
	require.NoError(t, err)
	require.NotNil(t, got)

	// Emojisが更新されたことを確認
	require.Len(t, got.Emojis, 1)
	assert.Equal(t, "blobcat", got.Emojis[0])

	// emojiRepoに作成されたことを確認
	e, err := emojiRepo.FindByNameAndHost("blobcat", &host)
	require.NoError(t, err)
	assert.Equal(t, "blobcat", e.Name)
	assert.Equal(t, "https://remote.example/emojis/blobcat.webp", e.OriginalURL)
}

func TestUpdateRemoteNote_EmojiTagExtraction_ExistingEmoji(t *testing.T) {
	// 既存の絵文字がある場合、URLが変わっていればupdateされることを確認
	repo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	uri := "https://remote.example/notes/emoji1"
	host := "remote.example"
	original := "original text"
	noteRepo.Notes["emoji1"] = &model.Note{
		ID: "emoji1", URI: &uri, UserID: "alice-id", UserHost: &host, Text: &original,
	}
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{}, idGen)
	emojiRepo := testutil.NewMockEmojiRepository()
	r.SetEmojiRepo(emojiRepo)

	// 既存の絵文字を配置
	emojiRepo.Emojis["blobcat@remote.example"] = &model.Emoji{
		ID:          "old-emoji-id",
		Name:        "blobcat",
		Host:        &host,
		OriginalURL: "https://remote.example/emojis/old-blobcat.webp",
		PublicURL:   "https://remote.example/emojis/old-blobcat.webp",
	}

	updateBody := []byte(`{
		"id": "https://remote.example/notes/emoji1",
		"type": "Note",
		"attributedTo": "https://remote.example/users/alice",
		"content": "updated :blobcat:",
		"tag": [
			{
				"type": "Emoji",
				"name": ":blobcat:",
				"icon": {"type": "Image", "url": "https://remote.example/emojis/new-blobcat.webp"},
				"id": "https://remote.example/emojis/blobcat"
			}
		]
	}`)
	got, err := r.UpdateRemoteNote(updateBody)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Len(t, got.Emojis, 1)
	assert.Equal(t, "blobcat", got.Emojis[0])

	// URLが更新されたことを確認
	e, err := emojiRepo.FindByNameAndHost("blobcat", &host)
	require.NoError(t, err)
	assert.Equal(t, "https://remote.example/emojis/new-blobcat.webp", e.OriginalURL)
}

// strPtr is a helper that returns a pointer to its argument.
func strPtr(s string) *string { return &s }

// --- #378 attachment ingest --------------------------------------------------

func TestExtractAttachments(t *testing.T) {
	tests := []struct {
		name     string
		raw      []any
		expected int
	}{
		{
			name: "Document image is extracted",
			raw: []any{
				map[string]any{
					"type":      "Document",
					"mediaType": "image/png",
					"url":       "https://remote.example/files/cat.png",
					"name":      "cute cat",
					"sensitive": false,
				},
			},
			expected: 1,
		},
		{
			name: "Image / Audio / Video types accepted",
			raw: []any{
				map[string]any{"type": "Image", "url": "https://r/i.jpg"},
				map[string]any{"type": "Audio", "url": "https://r/a.mp3"},
				map[string]any{"type": "Video", "url": "https://r/v.mp4"},
			},
			expected: 3,
		},
		{
			name: "Unknown types skipped",
			raw: []any{
				map[string]any{"type": "Note", "url": "https://r/n"},
				map[string]any{"type": "Document", "url": "https://r/ok.png"},
			},
			expected: 1,
		},
		{
			name: "missing url skipped",
			raw: []any{
				map[string]any{"type": "Document"},
				map[string]any{"type": "Document", "url": ""},
			},
			expected: 0,
		},
		{
			name:     "empty input",
			raw:      nil,
			expected: 0,
		},
		{
			name: "non-map entries skipped",
			raw: []any{
				"https://r/string-not-object.png",
				42,
				map[string]any{"type": "Document", "url": "https://r/ok.png"},
			},
			expected: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := federation.ExtractAttachments(tt.raw)
			assert.Len(t, got, tt.expected)
		})
	}
}

// TestExtractAttachments_MetadataFields covers #460/#461: width / height /
// icon / _misskey_blurhash が remote から届いたときに Document に展開される
// ことを確認する。JSON unmarshal 後 width/height は float64 で来るので
// numberAsInt 経由で int に丸められる必要がある。
func TestExtractAttachments_MetadataFields(t *testing.T) {
	raw := []any{
		map[string]any{
			"type":              "Document",
			"mediaType":         "image/png",
			"url":               "https://r/cat.png",
			"width":             float64(640),
			"height":            float64(480),
			"_misskey_blurhash": "L6PZfSi_.AyE_3t7t7R**0o#DgR4",
			"icon": map[string]any{
				"type": "Image",
				"url":  "https://r/cat-thumb.png",
			},
		},
		map[string]any{
			// width/height/icon/blurhash いずれも欠落しても zero value で
			// 通過する (旧来 ingestion との互換)。
			"type": "Document",
			"url":  "https://r/no-meta.bin",
		},
	}
	got := federation.ExtractAttachments(raw)
	require.Len(t, got, 2)
	assert.Equal(t, 640, got[0].Width)
	assert.Equal(t, 480, got[0].Height)
	assert.Equal(t, "L6PZfSi_.AyE_3t7t7R**0o#DgR4", got[0].Blurhash)
	require.NotNil(t, got[0].Icon)
	assert.Equal(t, "https://r/cat-thumb.png", got[0].Icon.URL)
	// 欠落側
	assert.Equal(t, 0, got[1].Width)
	assert.Equal(t, 0, got[1].Height)
	assert.Empty(t, got[1].Blurhash)
	assert.Nil(t, got[1].Icon)
}

func TestUpsertAttachments(t *testing.T) {
	t.Run("creates new drive_file as link", func(t *testing.T) {
		repo := testutil.NewMockUserRepository()
		noteRepo := testutil.NewMockNoteRepository()
		urls := activitypub.NewURLBuilder("https://example.com")
		idGen, _ := id.NewGenerator("aidx")
		r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{}, idGen)
		drive := testutil.NewMockDriveFileRepository()
		r.SetDriveFileRepo(drive)

		userID := "u1"
		host := "remote.example"
		docs := []activitypub.Document{
			{Type: "Document", MediaType: "image/png", URL: "https://r/cat.png", Name: "cat", Sensitive: false},
			{Type: "Image", MediaType: "image/jpeg", URL: "https://r/dog.jpg", Sensitive: true},
		}
		ids := r.UpsertAttachments(docs, &userID, &host)
		require.Len(t, ids, 2)

		// 各 drive_file を引いて期待値を確認
		f1, err := drive.FindByURI("https://r/cat.png")
		require.NoError(t, err)
		assert.True(t, f1.IsLink, "remote attachment は link 形式")
		assert.Equal(t, "image/png", f1.Type)
		assert.Equal(t, "cat", f1.Name)
		require.NotNil(t, f1.Comment)
		assert.Equal(t, "cat", *f1.Comment)
		require.NotNil(t, f1.UserHost)
		assert.Equal(t, "remote.example", *f1.UserHost)
		assert.Equal(t, "https://r/cat.png", f1.URL)

		f2, err := drive.FindByURI("https://r/dog.jpg")
		require.NoError(t, err)
		assert.True(t, f2.IsSensitive)
	})

	t.Run("dedup by URI: 既存 row を再利用する", func(t *testing.T) {
		repo := testutil.NewMockUserRepository()
		noteRepo := testutil.NewMockNoteRepository()
		urls := activitypub.NewURLBuilder("https://example.com")
		idGen, _ := id.NewGenerator("aidx")
		r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{}, idGen)
		drive := testutil.NewMockDriveFileRepository()
		r.SetDriveFileRepo(drive)

		uri := "https://r/dup.png"
		drive.Files["pre-existing"] = &model.DriveFile{
			ID: "pre-existing", URI: &uri, URL: uri, Type: "image/png", Name: "dup",
		}

		userID := "u1"
		host := "remote.example"
		ids := r.UpsertAttachments(
			[]activitypub.Document{{Type: "Document", URL: uri, Name: "dup"}},
			&userID, &host,
		)
		require.Len(t, ids, 1)
		assert.Equal(t, "pre-existing", ids[0], "既存 row の ID が返る")
		// drive_file が新規追加されていない
		assert.Len(t, drive.Files, 1)
	})

	t.Run("missing mediaType / name fallbacks", func(t *testing.T) {
		repo := testutil.NewMockUserRepository()
		noteRepo := testutil.NewMockNoteRepository()
		urls := activitypub.NewURLBuilder("https://example.com")
		idGen, _ := id.NewGenerator("aidx")
		r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{}, idGen)
		drive := testutil.NewMockDriveFileRepository()
		r.SetDriveFileRepo(drive)

		userID := "u1"
		host := "remote.example"
		ids := r.UpsertAttachments(
			[]activitypub.Document{{Type: "Document", URL: "https://r/no-meta.bin"}},
			&userID, &host,
		)
		require.Len(t, ids, 1)
		f, err := drive.FindByURI("https://r/no-meta.bin")
		require.NoError(t, err)
		assert.Equal(t, "application/octet-stream", f.Type)
		assert.Equal(t, "file", f.Name)
		assert.Nil(t, f.Comment, "AP name 空のとき comment は nil")
	})

	t.Run("nil driveFileRepo returns empty", func(t *testing.T) {
		repo := testutil.NewMockUserRepository()
		noteRepo := testutil.NewMockNoteRepository()
		urls := activitypub.NewURLBuilder("https://example.com")
		idGen, _ := id.NewGenerator("aidx")
		r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{}, idGen)

		userID := "u1"
		host := "remote.example"
		ids := r.UpsertAttachments(
			[]activitypub.Document{{Type: "Document", URL: "https://r/x.png"}},
			&userID, &host,
		)
		assert.Empty(t, ids)
	})

	t.Run("Create error skips attachment but keeps processing", func(t *testing.T) {
		repo := testutil.NewMockUserRepository()
		noteRepo := testutil.NewMockNoteRepository()
		urls := activitypub.NewURLBuilder("https://example.com")
		idGen, _ := id.NewGenerator("aidx")
		r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{}, idGen)
		drive := &createErrDriveRepo{
			MockDriveFileRepository: testutil.NewMockDriveFileRepository(),
			err:                     errors.New("db down"),
		}
		r.SetDriveFileRepo(drive)

		userID := "u1"
		host := "remote.example"
		ids := r.UpsertAttachments(
			[]activitypub.Document{{Type: "Document", URL: "https://r/err.png"}},
			&userID, &host,
		)
		// Create が失敗した attachment は ID リストに含まれない
		assert.Empty(t, ids)
	})

	// SetImageProbeClient setter が無 panic で呼べることを確認 (SSRF
	// 対策 #464)。実際の probe 経路は image_dimensions_test.go で
	// カバー済。
	t.Run("SetImageProbeClient does not panic", func(t *testing.T) {
		repo := testutil.NewMockUserRepository()
		noteRepo := testutil.NewMockNoteRepository()
		urls := activitypub.NewURLBuilder("https://example.com")
		idGen, _ := id.NewGenerator("aidx")
		r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{}, idGen)
		client := &http.Client{Timeout: 1 * time.Second}
		r.SetImageProbeClient(client)
		// nil 渡しでも panic しない (= invalidate 相当)
		r.SetImageProbeClient(nil)
	})

	// #460/#461: width / height / icon.url / _misskey_blurhash が AP
	// Document に乗ってきた場合、drive_file の properties / thumbnailUrl
	// / blurhash に永続化される。
	t.Run("persists width/height/thumbnail/blurhash from AP Document", func(t *testing.T) {
		repo := testutil.NewMockUserRepository()
		noteRepo := testutil.NewMockNoteRepository()
		urls := activitypub.NewURLBuilder("https://example.com")
		idGen, _ := id.NewGenerator("aidx")
		r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{}, idGen)
		drive := testutil.NewMockDriveFileRepository()
		r.SetDriveFileRepo(drive)

		userID := "u1"
		host := "remote.example"
		ids := r.UpsertAttachments(
			[]activitypub.Document{{
				Type:      "Document",
				MediaType: "image/png",
				URL:       "https://r/cat.png",
				Width:     1280,
				Height:    720,
				Icon:      &activitypub.Image{Type: "Image", URL: "https://r/cat-thumb.png"},
				Blurhash:  "L6PZfSi_.AyE_3t7t7R**0o#DgR4",
			}},
			&userID, &host,
		)
		require.Len(t, ids, 1)
		f, err := drive.FindByURI("https://r/cat.png")
		require.NoError(t, err)
		require.NotNil(t, f.ThumbnailURL)
		assert.Equal(t, "https://r/cat-thumb.png", *f.ThumbnailURL)
		require.NotNil(t, f.Blurhash)
		assert.Equal(t, "L6PZfSi_.AyE_3t7t7R**0o#DgR4", *f.Blurhash)
		assert.JSONEq(t, `{"width":1280,"height":720}`, string(f.Properties))
	})

	// metadata が一部しか乗ってこないケース (width のみ等) でも、その
	// 部分だけ properties に入れて、抜けている方は埋めない。
	t.Run("partial metadata: width only persists width", func(t *testing.T) {
		repo := testutil.NewMockUserRepository()
		noteRepo := testutil.NewMockNoteRepository()
		urls := activitypub.NewURLBuilder("https://example.com")
		idGen, _ := id.NewGenerator("aidx")
		r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{}, idGen)
		drive := testutil.NewMockDriveFileRepository()
		r.SetDriveFileRepo(drive)

		userID := "u1"
		host := "remote.example"
		_ = r.UpsertAttachments(
			[]activitypub.Document{{
				Type: "Document", URL: "https://r/half.png", Width: 100,
			}},
			&userID, &host,
		)
		f, err := drive.FindByURI("https://r/half.png")
		require.NoError(t, err)
		assert.JSONEq(t, `{"width":100}`, string(f.Properties))
		assert.Nil(t, f.ThumbnailURL)
		assert.Nil(t, f.Blurhash)
	})

	// 全 metadata 欠落 (旧来 attachment と互換) では properties 空、
	// thumbnailUrl / blurhash 共に nil のまま。
	t.Run("no metadata leaves properties empty and thumbnail/blurhash nil", func(t *testing.T) {
		repo := testutil.NewMockUserRepository()
		noteRepo := testutil.NewMockNoteRepository()
		urls := activitypub.NewURLBuilder("https://example.com")
		idGen, _ := id.NewGenerator("aidx")
		r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{}, idGen)
		drive := testutil.NewMockDriveFileRepository()
		r.SetDriveFileRepo(drive)

		userID := "u1"
		host := "remote.example"
		_ = r.UpsertAttachments(
			[]activitypub.Document{{Type: "Document", URL: "https://r/bare.png"}},
			&userID, &host,
		)
		f, err := drive.FindByURI("https://r/bare.png")
		require.NoError(t, err)
		// Properties は zero value (nil JSON) のまま、DB default の `{}`
		// が DB 側で適用される
		assert.Empty(t, f.Properties)
		assert.Nil(t, f.ThumbnailURL)
		assert.Nil(t, f.Blurhash)
	})
}

// createErrDriveRepo wraps the mock and forces Create to error so the
// upsertAttachments error branch can be exercised.
type createErrDriveRepo struct {
	*testutil.MockDriveFileRepository
	err error
}

func (c *createErrDriveRepo) Create(_ *model.DriveFile) error { return c.err }

func TestCollectAttachedFileTypes(t *testing.T) {
	t.Run("nil driveFileRepo returns empty", func(t *testing.T) {
		repo := testutil.NewMockUserRepository()
		noteRepo := testutil.NewMockNoteRepository()
		urls := activitypub.NewURLBuilder("https://example.com")
		idGen, _ := id.NewGenerator("aidx")
		r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{}, idGen)
		assert.Empty(t, r.CollectAttachedFileTypes([]string{"f1"}))
	})

	t.Run("empty fileIDs returns empty", func(t *testing.T) {
		repo := testutil.NewMockUserRepository()
		noteRepo := testutil.NewMockNoteRepository()
		urls := activitypub.NewURLBuilder("https://example.com")
		idGen, _ := id.NewGenerator("aidx")
		r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{}, idGen)
		drive := testutil.NewMockDriveFileRepository()
		r.SetDriveFileRepo(drive)
		assert.Empty(t, r.CollectAttachedFileTypes(nil))
	})

	t.Run("returns MIME types in input order", func(t *testing.T) {
		repo := testutil.NewMockUserRepository()
		noteRepo := testutil.NewMockNoteRepository()
		urls := activitypub.NewURLBuilder("https://example.com")
		idGen, _ := id.NewGenerator("aidx")
		r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{}, idGen)
		drive := testutil.NewMockDriveFileRepository()
		r.SetDriveFileRepo(drive)
		drive.Files["a"] = &model.DriveFile{ID: "a", Type: "image/png"}
		drive.Files["b"] = &model.DriveFile{ID: "b", Type: "image/jpeg"}
		drive.Files["c"] = &model.DriveFile{ID: "c", Type: "video/mp4"}

		got := r.CollectAttachedFileTypes([]string{"c", "a", "b"})
		assert.Equal(t, []string{"video/mp4", "image/png", "image/jpeg"}, got)
	})

	t.Run("missing IDs are skipped", func(t *testing.T) {
		repo := testutil.NewMockUserRepository()
		noteRepo := testutil.NewMockNoteRepository()
		urls := activitypub.NewURLBuilder("https://example.com")
		idGen, _ := id.NewGenerator("aidx")
		r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{}, idGen)
		drive := testutil.NewMockDriveFileRepository()
		r.SetDriveFileRepo(drive)
		drive.Files["a"] = &model.DriveFile{ID: "a", Type: "image/png"}

		got := r.CollectAttachedFileTypes([]string{"a", "missing"})
		assert.Equal(t, []string{"image/png"}, got)
	})

	t.Run("FindByIDs error returns empty", func(t *testing.T) {
		repo := testutil.NewMockUserRepository()
		noteRepo := testutil.NewMockNoteRepository()
		urls := activitypub.NewURLBuilder("https://example.com")
		idGen, _ := id.NewGenerator("aidx")
		r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{}, idGen)
		r.SetDriveFileRepo(&findIDsErrDriveRepo{
			MockDriveFileRepository: testutil.NewMockDriveFileRepository(),
			err:                     errors.New("db down"),
		})
		assert.Empty(t, r.CollectAttachedFileTypes([]string{"a"}))
	})
}

// findIDsErrDriveRepo forces FindByIDs to error.
type findIDsErrDriveRepo struct {
	*testutil.MockDriveFileRepository
	err error
}

func (f *findIDsErrDriveRepo) FindByIDs(_ []string) ([]*model.DriveFile, error) {
	return nil, f.err
}

// --- #397 mention extraction / resolution / merge ---

func TestExtractMentionTags(t *testing.T) {
	tests := []struct {
		name     string
		raw      []any
		expected []string
	}{
		{
			name: "Mention href is extracted",
			raw: []any{
				map[string]any{"type": "Mention", "href": "https://a/users/alice"},
				map[string]any{"type": "Mention", "href": "https://b/users/bob", "name": "@bob@b"},
			},
			expected: []string{"https://a/users/alice", "https://b/users/bob"},
		},
		{
			name: "non-Mention types (Emoji/Hashtag) are skipped",
			raw: []any{
				map[string]any{"type": "Emoji", "href": "https://x/e"},
				map[string]any{"type": "Hashtag", "href": "https://x/t"},
				map[string]any{"type": "Mention", "href": "https://a/users/alice"},
			},
			expected: []string{"https://a/users/alice"},
		},
		{
			name: "empty href and non-map entries are skipped",
			raw: []any{
				map[string]any{"type": "Mention"},
				map[string]any{"type": "Mention", "href": ""},
				"not-an-object",
				42,
				map[string]any{"type": "Mention", "href": "https://a/users/alice"},
			},
			expected: []string{"https://a/users/alice"},
		},
		{
			name:     "empty input",
			raw:      nil,
			expected: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := federation.ExtractMentionTags(tt.raw)
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestMergeMentionIDs(t *testing.T) {
	t.Run("dedup preserves input order", func(t *testing.T) {
		got := federation.MergeMentionIDs([]string{"a", "b"}, []string{"b", "c"})
		assert.Equal(t, []string{"a", "b", "c"}, []string(got))
	})
	t.Run("empty strings skipped", func(t *testing.T) {
		got := federation.MergeMentionIDs([]string{"", "a", ""}, []string{"", "b"})
		assert.Equal(t, []string{"a", "b"}, []string(got))
	})
	t.Run("both empty still returns non-nil", func(t *testing.T) {
		got := federation.MergeMentionIDs(nil, nil)
		require.NotNil(t, got)
		assert.Empty(t, got)
	})
	t.Run("dedup within single side", func(t *testing.T) {
		// a, b, a, b → a, b
		got := federation.MergeMentionIDs([]string{"a", "b", "a"}, []string{"b", "a"})
		assert.Equal(t, []string{"a", "b"}, []string(got))
	})
}

func TestResolveMentionedUserIDs(t *testing.T) {
	t.Run("local URI resolved via ExtractLocalUserID", func(t *testing.T) {
		repo := testutil.NewMockUserRepository()
		noteRepo := testutil.NewMockNoteRepository()
		urls := activitypub.NewURLBuilder("https://example.com")
		idGen, _ := id.NewGenerator("aidx")
		r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{}, idGen)

		ids := r.ResolveMentionedUserIDs([]string{
			"https://example.com/users/alice",
			"https://example.com/users/bob/inbox", // 末尾サフィックス付き
		})
		assert.Equal(t, []string{"alice", "bob"}, ids)
	})

	t.Run("known remote URI resolved via userRepo.FindByURI", func(t *testing.T) {
		repo := testutil.NewMockUserRepository()
		host := "remote.example"
		uri := "https://remote.example/users/charlie"
		repo.Users["remote-charlie"] = &model.User{
			ID:   "remote-charlie",
			Host: &host,
			URI:  &uri,
		}
		noteRepo := testutil.NewMockNoteRepository()
		urls := activitypub.NewURLBuilder("https://example.com")
		idGen, _ := id.NewGenerator("aidx")
		r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{}, idGen)

		ids := r.ResolveMentionedUserIDs([]string{uri})
		assert.Equal(t, []string{"remote-charlie"}, ids)
	})

	t.Run("unknown remote URI skipped without fetch", func(t *testing.T) {
		repo := testutil.NewMockUserRepository()
		noteRepo := testutil.NewMockNoteRepository()
		urls := activitypub.NewURLBuilder("https://example.com")
		idGen, _ := id.NewGenerator("aidx")
		r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{}, idGen)

		ids := r.ResolveMentionedUserIDs([]string{"https://unknown.example/users/x"})
		assert.Empty(t, ids)
	})

	t.Run("dedup", func(t *testing.T) {
		repo := testutil.NewMockUserRepository()
		noteRepo := testutil.NewMockNoteRepository()
		urls := activitypub.NewURLBuilder("https://example.com")
		idGen, _ := id.NewGenerator("aidx")
		r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{}, idGen)

		ids := r.ResolveMentionedUserIDs([]string{
			"https://example.com/users/alice",
			"https://example.com/users/alice",
		})
		assert.Equal(t, []string{"alice"}, ids)
	})

	t.Run("empty input", func(t *testing.T) {
		repo := testutil.NewMockUserRepository()
		noteRepo := testutil.NewMockNoteRepository()
		urls := activitypub.NewURLBuilder("https://example.com")
		idGen, _ := id.NewGenerator("aidx")
		r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{}, idGen)

		assert.Empty(t, r.ResolveMentionedUserIDs(nil))
	})
}

func TestResolveTextMentionUserIDs(t *testing.T) {
	mkResolver := func() (*federation.Resolver, *testutil.MockUserRepository) {
		repo := testutil.NewMockUserRepository()
		noteRepo := testutil.NewMockNoteRepository()
		urls := activitypub.NewURLBuilder("https://example.com")
		idGen, _ := id.NewGenerator("aidx")
		return federation.NewResolver(repo, noteRepo, urls, &stubFetcher{}, idGen), repo
	}
	t.Run("resolves both local and remote to IDs", func(t *testing.T) {
		r, repo := mkResolver()
		repo.Users["local-id"] = &model.User{ID: "local-id", Username: "alice", UsernameLower: "alice"}
		host := "remote.example"
		repo.Users["remote-id"] = &model.User{ID: "remote-id", Username: "bob", UsernameLower: "bob", Host: &host}

		ids := r.ResolveTextMentionUserIDs([]corenote.Mention{
			{Username: "alice"},
			{Username: "bob", Host: "remote.example"},
		})
		assert.Equal(t, []string{"local-id", "remote-id"}, ids)
	})
	t.Run("unknown user is skipped", func(t *testing.T) {
		r, _ := mkResolver()
		ids := r.ResolveTextMentionUserIDs([]corenote.Mention{{Username: "ghost"}})
		assert.Empty(t, ids)
	})
	t.Run("empty input", func(t *testing.T) {
		r, _ := mkResolver()
		assert.Nil(t, r.ResolveTextMentionUserIDs(nil))
	})
}

// TestIngestNote_SpecifiedDMPopulatesMentionsAndVisibleUserIDs covers the
// #397 fix end-to-end: a specified DM whose body has no @mention but whose
// AP `tag` array has a Mention to alice should still end up with alice in
// note.Mentions and note.VisibleUserIDs so /api/notes/mentions returns it
// and CanView lets alice read it.
func TestIngestNote_SpecifiedDMPopulatesMentionsAndVisibleUserIDs(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	// alice はローカル mk-A のユーザー (URI = https://example.com/users/alice)
	noteRepo := testutil.NewMockNoteRepository()
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{body: []byte(sampleActor)}, idGen)

	body := []byte(`{
		"id": "https://remote.example/notes/dm1",
		"type": "Note",
		"attributedTo": "https://remote.example/users/alice",
		"content": "secret hello",
		"to": ["https://example.com/users/alice"],
		"cc": [],
		"tag": [
			{"type": "Mention", "href": "https://example.com/users/alice", "name": "@alice"}
		]
	}`)
	got, err := r.IngestNote(body)
	require.NoError(t, err)
	assert.Equal(t, model.NoteVisibilitySpecified, got.Visibility)
	assert.Equal(t, []string{"alice"}, []string(got.Mentions),
		"AP tag Mention 由来でも mentions が埋まる")
	assert.Equal(t, []string{"alice"}, []string(got.VisibleUserIDs),
		"specified では VisibleUserIDs が AP to から埋まる")
}

func TestIngestNote_NonSpecifiedSkipsVisibleUserIDs(t *testing.T) {
	// public visibility なら VisibleUserIDs は埋めない (空のまま)。
	repo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{body: []byte(sampleActor)}, idGen)

	body := []byte(`{
		"id": "https://remote.example/notes/pub1",
		"type": "Note",
		"attributedTo": "https://remote.example/users/alice",
		"content": "hello world",
		"to": ["https://www.w3.org/ns/activitystreams#Public"],
		"cc": [],
		"tag": [
			{"type": "Mention", "href": "https://example.com/users/alice", "name": "@alice"}
		]
	}`)
	got, err := r.IngestNote(body)
	require.NoError(t, err)
	assert.Equal(t, model.NoteVisibilityPublic, got.Visibility)
	assert.Equal(t, []string{"alice"}, []string(got.Mentions))
	assert.Empty(t, []string(got.VisibleUserIDs))
}

func TestIngestNote_TagMentionsMergedWithTextMentions(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	// ローカル bob を MockUserRepository に登録 (FindByUsernameLower 解決用)。
	// MockUserRepository は usernameLower で lookup する。
	repo.Users["bob-local-id"] = &model.User{
		ID:            "bob-local-id",
		Username:      "bob",
		UsernameLower: "bob",
	}
	noteRepo := testutil.NewMockNoteRepository()
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{body: []byte(sampleActor)}, idGen)

	// 本文 "@bob" → bob-local-id、tag → alice。両方が mentions に入ること。
	body := []byte(`{
		"id": "https://remote.example/notes/merge1",
		"type": "Note",
		"attributedTo": "https://remote.example/users/alice",
		"content": "hi @bob",
		"to": ["https://example.com/users/alice"],
		"cc": [],
		"tag": [
			{"type": "Mention", "href": "https://example.com/users/alice", "name": "@alice"}
		]
	}`)
	got, err := r.IngestNote(body)
	require.NoError(t, err)
	mentions := []string(got.Mentions)
	assert.Contains(t, mentions, "alice")
	assert.Contains(t, mentions, "bob-local-id", "本文の @bob は user ID へ resolve される")
}

// TestUpdateRemoteNote_MentionsRecomputed confirms tag-derived mentions are
// recomputed on inbound Update even if text is unchanged (#397)。
func TestUpdateRemoteNote_MentionsRecomputed(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{body: []byte(sampleActor)}, idGen)

	// 既存リモートノート (mentions 空)
	host := "remote.example"
	uri := "https://remote.example/notes/edit1"
	existing := &model.Note{
		ID:       "n-edit1",
		UserID:   "remote-alice",
		UserHost: &host,
		URI:      &uri,
		Mentions: pq.StringArray{},
	}
	noteRepo.Notes[existing.ID] = existing

	body := []byte(`{
		"id": "https://remote.example/notes/edit1",
		"type": "Note",
		"attributedTo": "https://remote.example/users/alice",
		"content": "edited content",
		"tag": [
			{"type": "Mention", "href": "https://example.com/users/alice", "name": "@alice"}
		]
	}`)
	got, err := r.UpdateRemoteNote(body)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, []string{"alice"}, []string(got.Mentions))
}

// 同一 URI への並行 ResolveActor 呼び出しは singleflight で 1 回の HTTP
// fetch に collapse される (#300 3-7)。inbox 受信が同じ remote actor の
// activity を連続して取り込むケースを想定。
func TestResolveActor_DedupesConcurrentCalls(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	fetcher := &blockingFetcher{body: []byte(sampleActor), gate: make(chan struct{})}
	r := federation.NewResolver(repo, noteRepo, urls, fetcher, idGen)

	const N = 16
	var wg sync.WaitGroup
	results := make([]*model.User, N)
	errs := make([]error, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = r.ResolveActor("https://remote.example/users/alice")
		}(i)
	}
	close(fetcher.gate)
	wg.Wait()

	for i := 0; i < N; i++ {
		require.NoError(t, errs[i], "call %d", i)
		require.NotNil(t, results[i])
		assert.Equal(t, "alice", results[i].Username)
	}
	// 16 並行 → 1 fetch に集約されることが目標。leader が 1 回完了すると
	// 別の wave の leader が立ち得るので寛容な上限を取る。
	assert.LessOrEqual(t, fetcher.calls.Load(), int64(2),
		"singleflight must collapse concurrent same-URI ResolveActor to ~1 fetch")
	// DB 上は単一 user 行になることも担保する (UNIQUE 衝突が発生しない)。
	assert.Len(t, repo.Users, 1)
}

// blockingNoteFetcher は FetchObject の calls を atomic で数える Note 用 fetcher。
// FetchObject の戻り値はユニコードな Note JSON である必要があるので、actor
// JSON を流用する resolver test のヘルパとは別に持つ。
type blockingNoteFetcher struct {
	body  []byte
	gate  chan struct{}
	calls atomic.Int64
}

func (b *blockingNoteFetcher) FetchObject(_ string) ([]byte, error) {
	b.calls.Add(1)
	<-b.gate
	return b.body, nil
}

// 同一 URI への並行 ResolveNote 呼び出しも同様に collapse される (#300 3-7)。
func TestResolveNote_DedupesConcurrentCalls(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	repo.Users["uA"] = &model.User{
		ID:       "uA",
		Username: "alice",
		Host:     ptrString("remote.example"),
		URI:      ptrString("https://remote.example/users/alice"),
	}
	noteRepo := testutil.NewMockNoteRepository()
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	body := []byte(`{
		"id": "https://remote.example/notes/n1",
		"type": "Note",
		"attributedTo": "https://remote.example/users/alice",
		"content": "hello"
	}`)
	fetcher := &blockingNoteFetcher{body: body, gate: make(chan struct{})}
	r := federation.NewResolver(repo, noteRepo, urls, fetcher, idGen)

	const N = 16
	var wg sync.WaitGroup
	results := make([]*model.Note, N)
	errs := make([]error, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = r.ResolveNote("https://remote.example/notes/n1")
		}(i)
	}
	close(fetcher.gate)
	wg.Wait()

	for i := 0; i < N; i++ {
		require.NoError(t, errs[i], "call %d", i)
		require.NotNil(t, results[i])
	}
	assert.LessOrEqual(t, fetcher.calls.Load(), int64(2),
		"singleflight must collapse concurrent same-URI ResolveNote to ~1 fetch")
}

func ptrString(s string) *string { return &s }

// stubPublickeyExtraRepo は FEP-521a Multikey 永続化用の in-memory mock。
// resolver の cacheAssertionMethods / PublicKeyForKeyID 経路を unit test
// から exercise するために使う (#1067 / #1070)。
type stubPublickeyExtraRepo struct {
	mu      sync.RWMutex
	entries map[string]*model.UserPublickeyExtra // keyed by keyID
	upserts []model.UserPublickeyExtra
	failErr error
}

func (s *stubPublickeyExtraRepo) Upsert(pk *model.UserPublickeyExtra) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failErr != nil {
		return s.failErr
	}
	if s.entries == nil {
		s.entries = make(map[string]*model.UserPublickeyExtra)
	}
	s.entries[pk.KeyID] = pk
	s.upserts = append(s.upserts, *pk)
	return nil
}

func (s *stubPublickeyExtraRepo) FindByKeyID(keyID string) (*model.UserPublickeyExtra, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if pk, ok := s.entries[keyID]; ok {
		return pk, nil
	}
	// production の gorm 経由 repository は ErrRecordNotFound を返すので、
	// stub も同じ semantic を返して PublicKeyForKeyID の DB error と "行なし"
	// の区別 path をテスト経由でも walk できるようにする (#1070 follow-up)。
	return nil, gorm.ErrRecordNotFound
}

func (s *stubPublickeyExtraRepo) ListByUserID(userID string) ([]model.UserPublickeyExtra, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.failErr != nil {
		return nil, s.failErr
	}
	var out []model.UserPublickeyExtra
	for _, pk := range s.entries {
		if pk.UserID == userID {
			out = append(out, *pk)
		}
	}
	return out, nil
}

func (s *stubPublickeyExtraRepo) DeleteByKeyID(userID, keyID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if pk, ok := s.entries[keyID]; ok && pk.UserID == userID {
		delete(s.entries, keyID)
	}
	return nil
}

// 以下は repository.UserPublickeyExtraRepository interface 完全実装のための
// stub。resolver test では使わないが、同 package 内の deliver_service_test
// で stub を流用するために必要 (#1067 / #1071)。
func (s *stubPublickeyExtraRepo) FindByUserAndKeyID(userID, keyID string) (*model.UserPublickeyExtra, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if pk, ok := s.entries[keyID]; ok && pk.UserID == userID {
		return pk, nil
	}
	return nil, errors.New("not found")
}

func (s *stubPublickeyExtraRepo) DeleteByUserID(userID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for keyID, pk := range s.entries {
		if pk.UserID == userID {
			delete(s.entries, keyID)
		}
	}
	return nil
}

// stubPublickeyRepo は keys map race test 用の minimal な PublickeyStore
// 実装。本物の publickey_repo は GORM 経由なのでテストでは差し替える。
type stubPublickeyRepo struct {
	mu      sync.RWMutex
	entries map[string]*model.UserPublickey
}

func (s *stubPublickeyRepo) FindByUserID(userID string) (*model.UserPublickey, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if pk, ok := s.entries[userID]; ok {
		return pk, nil
	}
	return nil, errors.New("not found")
}

func (s *stubPublickeyRepo) Upsert(pk *model.UserPublickey) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.entries == nil {
		s.entries = make(map[string]*model.UserPublickey)
	}
	s.entries[pk.UserID] = pk
	return nil
}

// Ed25519 鍵を持つ remote actor を fetch して assertionMethod を
// user_publickey_extra に upsert する (#1067 / #1070)。
func TestResolveActor_PersistsAssertionMethod(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	mb, err := activitypub.EncodeEd25519Multikey(pub)
	require.NoError(t, err)

	body := `{
		"id": "https://remote.example/users/alice",
		"type": "Person",
		"preferredUsername": "alice",
		"inbox": "https://remote.example/users/alice/inbox",
		"publicKey": {
			"id": "https://remote.example/users/alice#main-key",
			"owner": "https://remote.example/users/alice",
			"publicKeyPem": "-----BEGIN PUBLIC KEY-----\nFAKE\n-----END PUBLIC KEY-----"
		},
		"assertionMethod": [{
			"id": "https://remote.example/users/alice#ed25519-key",
			"type": "Multikey",
			"controller": "https://remote.example/users/alice",
			"publicKeyMultibase": "` + mb + `"
		}]
	}`

	r, _ := newResolver(t, body, nil)
	extra := &stubPublickeyExtraRepo{}
	r.SetPublickeyExtraRepo(extra)

	user, err := r.ResolveActor("https://remote.example/users/alice")
	require.NoError(t, err)

	row, err := extra.FindByKeyID("https://remote.example/users/alice#ed25519-key")
	require.NoError(t, err)
	assert.Equal(t, user.ID, row.UserID)
	assert.Equal(t, model.AlgEd25519, row.Alg)
	assert.Contains(t, row.KeyPEM, "PUBLIC KEY")

	// round-trip: 保存された PEM から元の Ed25519 公開鍵を復元できる
	parsed, err := activitypub.ParseEd25519PublicKeyPEM(row.KeyPEM)
	require.NoError(t, err)
	assert.Equal(t, pub, parsed)
}

// 不正な Multikey (Multibase decode 失敗) が混ざっていても、正常な entry は
// upsert される (silently skip + warn log)。
func TestResolveActor_SkipsMalformedAssertionMethod(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	mb, err := activitypub.EncodeEd25519Multikey(pub)
	require.NoError(t, err)

	body := `{
		"id": "https://remote.example/users/alice",
		"type": "Person",
		"preferredUsername": "alice",
		"inbox": "https://remote.example/users/alice/inbox",
		"publicKey": {
			"id": "https://remote.example/users/alice#main-key",
			"owner": "https://remote.example/users/alice",
			"publicKeyPem": "-----BEGIN PUBLIC KEY-----\nFAKE\n-----END PUBLIC KEY-----"
		},
		"assertionMethod": [
			{"id": "https://remote.example/users/alice#bad", "type": "Multikey", "controller": "x", "publicKeyMultibase": "INVALID"},
			{"id": "https://remote.example/users/alice#ed25519-key", "type": "Multikey", "controller": "x", "publicKeyMultibase": "` + mb + `"},
			{"id": "https://remote.example/users/alice#non-multikey", "type": "JsonWebKey", "controller": "x", "publicKeyMultibase": "z6Mk..."}
		]
	}`

	r, _ := newResolver(t, body, nil)
	extra := &stubPublickeyExtraRepo{}
	r.SetPublickeyExtraRepo(extra)

	_, err = r.ResolveActor("https://remote.example/users/alice")
	require.NoError(t, err)

	// 不正 entry / 非 Multikey type は skip され、正常な Ed25519 のみ upsert される
	rows, err := extra.ListByUserID(extra.upserts[0].UserID)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "https://remote.example/users/alice#ed25519-key", rows[0].KeyID)
}

// PublicKeyForKeyID は user_publickey_extra (Ed25519 / Multikey) を最初に探し、
// miss なら user_publickey (RSA) を fallback で返す dual lookup を行う (#1070)。
func TestPublicKeyForKeyID_DualLookup(t *testing.T) {
	r, _ := newResolver(t, sampleActor, nil)
	pkRepo := &stubPublickeyRepo{entries: map[string]*model.UserPublickey{}}
	r.SetPublickeyRepo(pkRepo)
	extra := &stubPublickeyExtraRepo{}
	r.SetPublickeyExtraRepo(extra)

	// user_publickey に RSA primary key を seed
	require.NoError(t, pkRepo.Upsert(&model.UserPublickey{
		UserID: "alice", KeyID: "https://remote.example/users/alice#main-key",
		KeyPEM: "-----BEGIN PUBLIC KEY-----\nRSA-FAKE\n-----END PUBLIC KEY-----",
	}))
	// user_publickey_extra に Ed25519 を seed
	require.NoError(t, extra.Upsert(&model.UserPublickeyExtra{
		UserID: "alice", KeyID: "https://remote.example/users/alice#ed25519-key",
		KeyPEM: "-----BEGIN PUBLIC KEY-----\nED25519-FAKE\n-----END PUBLIC KEY-----",
		Alg:    model.AlgEd25519,
	}))

	// Ed25519 keyId 一致 → extra から返る
	pem, err := r.PublicKeyForKeyID("alice", "https://remote.example/users/alice#ed25519-key")
	require.NoError(t, err)
	assert.Contains(t, pem, "ED25519-FAKE")

	// RSA keyId (= extra に無し) → PublicKeyForActor fallback で RSA primary が返る
	pem, err = r.PublicKeyForKeyID("alice", "https://remote.example/users/alice#main-key")
	require.NoError(t, err)
	assert.Contains(t, pem, "RSA-FAKE")
}

// 同じ actor を refresh する経路で stale keyId が削除されることを検証する。
// 1 回目 ResolveActor で 2 keys を seed → actorTTL を 0 に強制 → 2 回目で
// 1 key だけになった body を返す fetcher に切り替え → 旧 1 key が purge される。
func TestResolveActor_RefreshRemovesStaleAssertionMethod(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	mb, err := activitypub.EncodeEd25519Multikey(pub)
	require.NoError(t, err)

	bodyTwoKeys := `{
		"id": "https://remote.example/users/alice",
		"type": "Person",
		"preferredUsername": "alice",
		"inbox": "https://remote.example/users/alice/inbox",
		"publicKey": {"id": "https://remote.example/users/alice#main-key", "owner": "x", "publicKeyPem": "-----BEGIN PUBLIC KEY-----\nFAKE\n-----END PUBLIC KEY-----"},
		"assertionMethod": [
			{"id": "https://remote.example/users/alice#old-key", "type": "Multikey", "controller": "x", "publicKeyMultibase": "` + mb + `"},
			{"id": "https://remote.example/users/alice#new-key", "type": "Multikey", "controller": "x", "publicKeyMultibase": "` + mb + `"}
		]
	}`
	bodyOneKey := `{
		"id": "https://remote.example/users/alice",
		"type": "Person",
		"preferredUsername": "alice",
		"inbox": "https://remote.example/users/alice/inbox",
		"publicKey": {"id": "https://remote.example/users/alice#main-key", "owner": "x", "publicKeyPem": "-----BEGIN PUBLIC KEY-----\nFAKE\n-----END PUBLIC KEY-----"},
		"assertionMethod": [
			{"id": "https://remote.example/users/alice#new-key", "type": "Multikey", "controller": "x", "publicKeyMultibase": "` + mb + `"}
		]
	}`

	repo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	swappable := &swappableFetcher{body: bodyTwoKeys}
	r := federation.NewResolver(repo, noteRepo, urls, swappable, idGen)
	extra := &stubPublickeyExtraRepo{}
	r.SetPublickeyExtraRepo(extra)
	r.SetActorTTL(time.Nanosecond) // 即時 refresh をトリガするため極短 TTL

	// 1 回目: 2 keys が seed される
	user, err := r.ResolveActor("https://remote.example/users/alice")
	require.NoError(t, err)
	rows, _ := extra.ListByUserID(user.ID)
	require.Len(t, rows, 2)

	// 2 回目: fetcher を 1 key body に差し替えて refresh をトリガ (TTL 失効済)
	swappable.body = bodyOneKey
	time.Sleep(2 * time.Nanosecond) // TTL を確実に超過させる
	_, err = r.ResolveActor("https://remote.example/users/alice")
	require.NoError(t, err)

	rows, _ = extra.ListByUserID(user.ID)
	require.Len(t, rows, 1, "stale #old-key は purge されて #new-key のみ残る")
	assert.Equal(t, "https://remote.example/users/alice#new-key", rows[0].KeyID)
}

// swappableFetcher は body を test 中に差し替えられる stubFetcher 派生。
type swappableFetcher struct {
	body string
}

func (s *swappableFetcher) FetchObject(_ string) ([]byte, error) {
	return []byte(s.body), nil
}

// publickeyExtraRepo 未配線でも PublicKeyForKeyID は PublicKeyForActor と
// 等価な挙動を保つ (= drop-in 互換)。
func TestPublicKeyForKeyID_WithoutExtraRepoFallsBackToActor(t *testing.T) {
	r, _ := newResolver(t, sampleActor, nil)
	pkRepo := &stubPublickeyRepo{entries: map[string]*model.UserPublickey{}}
	r.SetPublickeyRepo(pkRepo)
	require.NoError(t, pkRepo.Upsert(&model.UserPublickey{
		UserID: "alice", KeyID: "https://remote.example/users/alice#main-key",
		KeyPEM: "-----BEGIN PUBLIC KEY-----\nFALLBACK\n-----END PUBLIC KEY-----",
	}))

	pem, err := r.PublicKeyForKeyID("alice", "https://remote.example/users/alice#ed25519-key")
	require.NoError(t, err)
	assert.Contains(t, pem, "FALLBACK")
}

// 複数 goroutine が異なる actor の PublicKeyForActor を並行に呼ぶと、内部
// keys map が無ロックだと race detector / 実行時 panic が発火する。
// Devin review #555 FLAG-1 への対策として keysMu を入れたので、並行アクセス
// が clean に動くことを race detector で担保する。本テストは r.keys のみを
// 対象にして mock userRepo の race を巻き込まないように、PublickeyForActor
// 経路だけを叩く (DB fallback で r.keys 書き込みを発火させる)。
func TestResolver_KeysMapConcurrentAccessIsRaceFree(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{body: []byte(sampleActor)}, idGen)

	pkRepo := &stubPublickeyRepo{entries: map[string]*model.UserPublickey{}}
	for i := 0; i < 4; i++ {
		uid := fmt.Sprintf("u%d", i)
		_ = pkRepo.Upsert(&model.UserPublickey{UserID: uid, KeyID: "k" + uid, KeyPEM: "pem" + uid})
	}
	r.SetPublickeyRepo(pkRepo)

	// 8 並行で異なる userID の PublicKeyForActor を呼ぶ。in-memory cache
	// miss → DB fallback → r.keys 書き込み が並行で走る。
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		uid := fmt.Sprintf("u%d", i%4)
		wg.Add(1)
		go func(uid string) {
			defer wg.Done()
			_, _ = r.PublicKeyForActor(uid)
		}(uid)
	}
	wg.Wait()
	// 検証ポイントは race detector が黙ること。
}

// upstream Misskey #17167 (= 2026.5.0 fix / triage #1004): mentionLimit を
// 超える inbound Note は ErrContainsTooManyMentions を返して保存せず、caller
// (processor.handleCreate) が non-retry skip 化する。21 件の local user URI を
// tag 配列に詰めて ExtractLocalUserID 経由で resolveMentionedUserIDs が
// 全件 ID を返すよう仕込み、limit 超え (= corenote.DefaultMentionLimit) を発火させる。
func TestIngestNote_MentionLimitExceededReturnsSentinel(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{body: []byte(sampleActor)}, idGen)

	// 21 件 = limit (20) + 1 の local user URI を tag に並べる。Mock urls の
	// UserURI prefix は "https://example.com/users/" なので ExtractLocalUserID
	// が "u1".."u21" を返す。
	var tagJSON string
	for i := 1; i <= corenote.DefaultMentionLimit+1; i++ {
		if i > 1 {
			tagJSON += ","
		}
		tagJSON += `{"type": "Mention", "href": "https://example.com/users/u` + strconv.Itoa(i) + `"}`
	}
	body := []byte(`{
		"id": "https://remote.example/notes/over",
		"type": "Note",
		"attributedTo": "https://remote.example/users/alice",
		"content": "x",
		"to": ["https://www.w3.org/ns/activitystreams#Public"],
		"tag": [` + tagJSON + `]
	}`)
	_, err := r.IngestNote(body)
	require.ErrorIs(t, err, corenote.ErrContainsTooManyMentions)
	// 未保存であることも確認 (= queue retry 経由で再試行されても結果は同じ)。
	_, lookupErr := noteRepo.FindByURI("https://remote.example/notes/over")
	require.Error(t, lookupErr, "limit exceed note should NOT be persisted")
}

// 境界条件 (= limit ぴったりの 20 件) は受理される。off-by-one 検出。
func TestIngestNote_MentionLimitBoundaryAccepted(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{body: []byte(sampleActor)}, idGen)

	var tagJSON string
	for i := 1; i <= corenote.DefaultMentionLimit; i++ {
		if i > 1 {
			tagJSON += ","
		}
		tagJSON += `{"type": "Mention", "href": "https://example.com/users/u` + strconv.Itoa(i) + `"}`
	}
	body := []byte(`{
		"id": "https://remote.example/notes/boundary",
		"type": "Note",
		"attributedTo": "https://remote.example/users/alice",
		"content": "x",
		"to": ["https://www.w3.org/ns/activitystreams#Public"],
		"tag": [` + tagJSON + `]
	}`)
	note, err := r.IngestNote(body)
	require.NoError(t, err)
	require.NotNil(t, note)
	assert.Len(t, note.Mentions, corenote.DefaultMentionLimit)
}

// TestResolveActor_ExistingUserRefreshesHashtags は actor 再取得 (refreshActor)
// で person.tag の変化が user.tags に追従することを確認する (#1360, Part 2)。
// 自己紹介の hashtag を編集した remote user が hashtags/users に反映されるための前提。
func TestResolveActor_ExistingUserRefreshesHashtags(t *testing.T) {
	body := `{
		"id": "https://remote.example/users/alice",
		"type": "Person",
		"preferredUsername": "alice",
		"inbox": "https://remote.example/users/alice/inbox",
		"tag": [{"type": "Hashtag", "name": "#Refreshed"}],
		"publicKey": {"publicKeyPem": "FAKE"}
	}`
	r, repo := newResolver(t, body, nil)
	uri := "https://remote.example/users/alice"
	host := "remote.example"
	repo.Users["existing"] = &model.User{
		ID:       "existing",
		Username: "alice",
		URI:      &uri,
		Host:     &host,
		Tags:     []string{"stale"},
	}
	_, err := r.ResolveActor(uri)
	require.NoError(t, err)
	// refresh で旧 tags ("stale") が person.tag 由来の正規化済み tag に置き換わる。
	assert.Equal(t, []string{"refreshed"}, []string(repo.Users["existing"].Tags))
}

// TestResolveActor_NewUserIngestsHashtags は remote actor の person.tag の
// Hashtag entry が正規化されて user.tags に取り込まれることを確認する
// (#1360, Part 2)。hashtags/users が remote user を引けるための前提。
func TestResolveActor_NewUserIngestsHashtags(t *testing.T) {
	body := `{
		"id": "https://remote.example/users/tagger",
		"type": "Person",
		"preferredUsername": "tagger",
		"inbox": "https://remote.example/users/tagger/inbox",
		"summary": "<p>bio</p>",
		"tag": [
			{"type": "Hashtag", "name": "#Golang", "href": "https://remote.example/tags/golang"},
			{"type": "Hashtag", "name": "#ActivityPub"},
			{"type": "Emoji", "name": ":party:"}
		],
		"publicKey": {"publicKeyPem": "FAKE"}
	}`
	r, _ := newResolver(t, body, nil)
	user, err := r.ResolveActor("https://remote.example/users/tagger")
	require.NoError(t, err)
	// Hashtag のみ正規化して取り込む (Emoji は除外)。
	assert.ElementsMatch(t, []string{"golang", "activitypub"}, []string(user.Tags))
}
