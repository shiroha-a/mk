package ap

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/activitypub"
	corenote "github.com/shiroha-a/mk/internal/core/note"
	coreuser "github.com/shiroha-a/mk/internal/core/user"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// memoryKeypairRepo is an in-memory repository for tests.
type memoryKeypairRepo struct {
	items map[string]*model.UserKeypair
	err   error
}

func (m *memoryKeypairRepo) Create(k *model.UserKeypair) error {
	if m.err != nil {
		return m.err
	}
	m.items[k.UserID] = k
	return nil
}

func (m *memoryKeypairRepo) FindByUserID(userID string) (*model.UserKeypair, error) {
	if m.err != nil {
		return nil, m.err
	}
	k, ok := m.items[userID]
	if !ok {
		return nil, errors.New("not found")
	}
	return k, nil
}

var _ repository.UserKeypairRepository = (*memoryKeypairRepo)(nil)

func newHandler(t *testing.T) (*Handler, *testutil.MockUserRepository, *testutil.MockNoteRepository, *memoryKeypairRepo) {
	t.Helper()
	userRepo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	piningRepo := testutil.NewMockUserNotePiningRepository()
	idGen, _ := id.NewGenerator("aidx")
	userSvc := coreuser.NewService(userRepo, noteRepo, piningRepo, idGen)
	querySvc := corenote.NewQueryService(noteRepo, nil)
	keypairRepo := &memoryKeypairRepo{items: map[string]*model.UserKeypair{}}
	urls := activitypub.NewURLBuilder("https://example.com")
	renderer := activitypub.NewRenderer(urls)
	return NewHandler(renderer, userSvc, querySvc, keypairRepo, idGen), userRepo, noteRepo, keypairRepo
}

func newReq(t *testing.T, paramName, paramValue string) (echo.Context, *httptest.ResponseRecorder) {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	// User/Note ハンドラは Accept ベースでAPか否かを切り替えるので、既存の
	// AP レスポンスを検証するテストでは AP Accept を明示する。
	req.Header.Set(echo.HeaderAccept, "application/activity+json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames(paramName)
	c.SetParamValues(paramValue)
	return c, rec
}

func TestUser_Success(t *testing.T) {
	h, userRepo, _, keypairRepo := newHandler(t)
	userRepo.Users["u1"] = &model.User{ID: "u1", Username: "alice"}
	keypairRepo.items["u1"] = &model.UserKeypair{UserID: "u1", PublicKey: "PUBKEY"}

	c, rec := newReq(t, "id", "u1")
	require.NoError(t, h.User(c))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "Person")
	assert.Contains(t, rec.Body.String(), "alice")
}

func TestUser_NotFound(t *testing.T) {
	h, _, _, _ := newHandler(t)
	c, rec := newReq(t, "id", "ghost")
	require.NoError(t, h.User(c))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestUser_Remote(t *testing.T) {
	h, userRepo, _, _ := newHandler(t)
	host := "remote.example"
	userRepo.Users["u1"] = &model.User{ID: "u1", Username: "alice", Host: &host}
	c, rec := newReq(t, "id", "u1")
	require.NoError(t, h.User(c))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestUser_KeypairFetchError(t *testing.T) {
	h, userRepo, _, keypairRepo := newHandler(t)
	userRepo.Users["u1"] = &model.User{ID: "u1", Username: "alice"}
	keypairRepo.err = errors.New("db down")
	c, rec := newReq(t, "id", "u1")
	require.NoError(t, h.User(c))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

// FEP-521a Multikey expose 経路 (#1067 / #1069):
// keypairExtraRepo を wire し、Ed25519 鍵を持つ user の actor JSON に
// assertionMethod[] が出力されることを確認する。
func TestUser_WithEd25519Keypair_ExposesAssertionMethod(t *testing.T) {
	h, userRepo, _, keypairRepo := newHandler(t)
	userRepo.Users["u1"] = &model.User{ID: "u1", Username: "alice"}
	keypairRepo.items["u1"] = &model.UserKeypair{UserID: "u1", PublicKey: "PUBKEY"}

	// Ed25519 鍵対を生成して PEM で extra repo に格納
	_, edPubPEM, err := activitypub.GenerateEd25519Keypair()
	require.NoError(t, err)
	extra := testutil.NewMockUserKeypairExtraRepository()
	require.NoError(t, extra.Upsert(&model.UserKeypairExtra{
		UserID:            "u1",
		Ed25519PublicKey:  edPubPEM,
		Ed25519PrivateKey: "STUB",
	}))
	h.SetKeypairExtraRepo(extra)

	c, rec := newReq(t, "id", "u1")
	require.NoError(t, h.User(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, `"assertionMethod"`)
	assert.Contains(t, body, `"Multikey"`)
	assert.Contains(t, body, `#ed25519-key`)
}

// keypairExtraRepo が wire 済 + 該当 user に Ed25519 行が無い場合は P5 lazy
// backfill が走り、新規鍵が生成・保存されて actor JSON に assertionMethod
// が expose される (#1072)。
func TestUser_WithEd25519Repo_NoRowForUser_TriggersLazyBackfill(t *testing.T) {
	h, userRepo, _, keypairRepo := newHandler(t)
	userRepo.Users["u1"] = &model.User{ID: "u1", Username: "alice"}
	keypairRepo.items["u1"] = &model.UserKeypair{UserID: "u1", PublicKey: "PUBKEY"}
	extra := testutil.NewMockUserKeypairExtraRepository()
	h.SetKeypairExtraRepo(extra)

	c, rec := newReq(t, "id", "u1")
	require.NoError(t, h.User(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	// backfill 後は assertionMethod が出力される
	assert.Contains(t, rec.Body.String(), `"assertionMethod"`)
	assert.Contains(t, rec.Body.String(), `"Multikey"`)
	assert.Contains(t, rec.Body.String(), `#ed25519-key`)
	// mock 上に新規 row が保存されている
	row, ok := extra.Keypairs["u1"]
	require.True(t, ok)
	assert.Contains(t, row.Ed25519PublicKey, "PUBLIC KEY")
	assert.Contains(t, row.Ed25519PrivateKey, "PRIVATE KEY")
}

// 並列 actor JSON 生成で同 user に対する複数 lookup が同時に走っても、
// InsertIfAbsent (ON CONFLICT DO NOTHING) で最初に書かれた行が残り、後続は
// 再 lookup で既存行を取得する race-safe 性質を確認する (#1072)。
func TestUser_LazyBackfill_RaceSafe(t *testing.T) {
	h, userRepo, _, keypairRepo := newHandler(t)
	userRepo.Users["u1"] = &model.User{ID: "u1", Username: "alice"}
	keypairRepo.items["u1"] = &model.UserKeypair{UserID: "u1", PublicKey: "PUBKEY"}
	extra := testutil.NewMockUserKeypairExtraRepository()
	h.SetKeypairExtraRepo(extra)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, rec := newReq(t, "id", "u1")
			require.NoError(t, h.User(c))
			assert.Equal(t, http.StatusOK, rec.Code)
		}()
	}
	wg.Wait()

	// 8 並列で backfill しても DB 上は 1 行だけ
	row, ok := extra.Keypairs["u1"]
	require.True(t, ok)
	assert.NotEmpty(t, row.Ed25519PublicKey)
}

// keypairExtraRepo の FindByUserID が gorm.ErrRecordNotFound 以外の DB error
// を返した場合も AssertionMethod を omit して RSA only で 200 を返す
// (fail-soft)。silent degradation の診断は slog.Warn 経由で記録される。
type failingKeypairExtraRepo struct {
	err error
}

func (f *failingKeypairExtraRepo) Upsert(_ *model.UserKeypairExtra) error         { return f.err }
func (f *failingKeypairExtraRepo) InsertIfAbsent(_ *model.UserKeypairExtra) error { return f.err }
func (f *failingKeypairExtraRepo) FindByUserID(_ string) (*model.UserKeypairExtra, error) {
	return nil, f.err
}
func (f *failingKeypairExtraRepo) Delete(_ string) error { return f.err }

func TestUser_WithEd25519Repo_DBError_FailsSoft(t *testing.T) {
	h, userRepo, _, keypairRepo := newHandler(t)
	userRepo.Users["u1"] = &model.User{ID: "u1", Username: "alice"}
	keypairRepo.items["u1"] = &model.UserKeypair{UserID: "u1", PublicKey: "PUBKEY"}
	h.SetKeypairExtraRepo(&failingKeypairExtraRepo{err: errors.New("connection refused")})

	c, rec := newReq(t, "id", "u1")
	require.NoError(t, h.User(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.NotContains(t, rec.Body.String(), `"assertionMethod"`)
}

// backfill 経路で InsertIfAbsent が DB error を返した場合 → assertionMethod
// は出ず RSA only に fail-soft。
type insertFailingExtraRepo struct {
	*testutil.MockUserKeypairExtraRepository
	insertErr error
}

func (i *insertFailingExtraRepo) InsertIfAbsent(_ *model.UserKeypairExtra) error {
	return i.insertErr
}

func TestUser_LazyBackfill_InsertFailureFallsBackToRSAOnly(t *testing.T) {
	h, userRepo, _, keypairRepo := newHandler(t)
	userRepo.Users["u1"] = &model.User{ID: "u1", Username: "alice"}
	keypairRepo.items["u1"] = &model.UserKeypair{UserID: "u1", PublicKey: "PUBKEY"}
	h.SetKeypairExtraRepo(&insertFailingExtraRepo{
		MockUserKeypairExtraRepository: testutil.NewMockUserKeypairExtraRepository(),
		insertErr:                      errors.New("constraint violation"),
	})

	c, rec := newReq(t, "id", "u1")
	require.NoError(t, h.User(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.NotContains(t, rec.Body.String(), `"assertionMethod"`)
}

// keypairExtraRepo が wire 済 + Ed25519 row はあるが PEM が壊れている場合 →
// silently skip して AssertionMethod を omit する (= RSA only に fail-soft)。
func TestUser_WithEd25519Repo_MalformedPEM_FailsSoft(t *testing.T) {
	h, userRepo, _, keypairRepo := newHandler(t)
	userRepo.Users["u1"] = &model.User{ID: "u1", Username: "alice"}
	keypairRepo.items["u1"] = &model.UserKeypair{UserID: "u1", PublicKey: "PUBKEY"}
	extra := testutil.NewMockUserKeypairExtraRepository()
	require.NoError(t, extra.Upsert(&model.UserKeypairExtra{
		UserID:            "u1",
		Ed25519PublicKey:  "NOT-A-VALID-PEM",
		Ed25519PrivateKey: "STUB",
	}))
	h.SetKeypairExtraRepo(extra)

	c, rec := newReq(t, "id", "u1")
	require.NoError(t, h.User(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.NotContains(t, rec.Body.String(), `"assertionMethod"`)
}

// newBrowserReq builds a non-AP request (no application/activity+json in
// Accept). Used to exercise the SPA / redirect fallback branch.
func newBrowserReq(t *testing.T, paramName, paramValue string) (echo.Context, *httptest.ResponseRecorder) {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(echo.HeaderAccept, "text/html,application/xhtml+xml")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames(paramName)
	c.SetParamValues(paramValue)
	return c, rec
}

// TestUser_BrowserRedirectsToAcct guards #691 — frontend が QR code 等で
// `/users/<id>` を生成するため、browser 経路では `/@<username>` permalink
// にリダイレクトして SPA の userPage route に解決させる必要がある。
func TestUser_BrowserRedirectsToAcct(t *testing.T) {
	h, userRepo, _, _ := newHandler(t)
	userRepo.Users["u1"] = &model.User{ID: "u1", Username: "alice"}

	c, rec := newBrowserReq(t, "id", "u1")
	require.NoError(t, h.User(c))
	assert.Equal(t, http.StatusFound, rec.Code)
	assert.Equal(t, "/@alice", rec.Header().Get("Location"))
	assert.Equal(t, "Accept", rec.Header().Get("Vary"),
		"Vary: Accept must be set so browser/AP responses do not collide in caches")
}

// TestUser_BrowserRedirectsRemoteToAcctWithHost ensures remote users get
// their canonical `/@username@host` permalink so the SPA can fetch from
// the right host when opened via `/users/<id>` (e.g. shared QR/code link).
func TestUser_BrowserRedirectsRemoteToAcctWithHost(t *testing.T) {
	h, userRepo, _, _ := newHandler(t)
	host := "remote.example"
	userRepo.Users["u_remote"] = &model.User{ID: "u_remote", Username: "bob", Host: &host}

	c, rec := newBrowserReq(t, "id", "u_remote")
	require.NoError(t, h.User(c))
	assert.Equal(t, http.StatusFound, rec.Code)
	assert.Equal(t, "/@bob@remote.example", rec.Header().Get("Location"))
}

// TestUser_BrowserNotFound preserves the 404-on-missing-user behaviour for
// the browser path so an invalid id doesn't redirect to a confusing /@.
func TestUser_BrowserNotFound(t *testing.T) {
	h, _, _, _ := newHandler(t)
	c, rec := newBrowserReq(t, "id", "ghost")
	require.NoError(t, h.User(c))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// TestUser_BrowserSuspendedLocal404 matches upstream Misskey TS strict filter
// (isSuspended: false on local user) — suspended local users return 404
// instead of redirecting to a "user suspended" SPA page (#691 review).
func TestUser_BrowserSuspendedLocal404(t *testing.T) {
	h, userRepo, _, _ := newHandler(t)
	userRepo.Users["u_susp"] = &model.User{ID: "u_susp", Username: "alice", IsSuspended: true}

	c, rec := newBrowserReq(t, "id", "u_susp")
	require.NoError(t, h.User(c))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// TestUser_BrowserVaryAcceptOnAllPaths guards that Vary: Accept is set on
// every response (redirect / 404 / AP) so intermediate caches do not return
// browser HTML to AP clients or vice versa (#691 review).
func TestUser_BrowserVaryAcceptOnAllPaths(t *testing.T) {
	h, userRepo, _, keypairRepo := newHandler(t)
	userRepo.Users["u1"] = &model.User{ID: "u1", Username: "alice"}
	keypairRepo.items["u1"] = &model.UserKeypair{UserID: "u1", PublicKey: "PUBKEY"}

	// AP path
	c, rec := newReq(t, "id", "u1")
	require.NoError(t, h.User(c))
	assert.Equal(t, "Accept", rec.Header().Get("Vary"))

	// Browser redirect
	c, rec = newBrowserReq(t, "id", "u1")
	require.NoError(t, h.User(c))
	assert.Equal(t, "Accept", rec.Header().Get("Vary"))

	// 404 path (AP)
	c, rec = newReq(t, "id", "ghost")
	require.NoError(t, h.User(c))
	assert.Equal(t, "Accept", rec.Header().Get("Vary"))
}

func TestNote_Success(t *testing.T) {
	h, _, noteRepo, _ := newHandler(t)
	noteRepo.Notes["n1"] = &model.Note{
		ID: "n1", UserID: "u1", Visibility: model.NoteVisibilityPublic,
	}
	c, rec := newReq(t, "id", "n1")
	require.NoError(t, h.Note(c))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "Note")
}

func TestNote_NotFound(t *testing.T) {
	h, _, _, _ := newHandler(t)
	c, rec := newReq(t, "id", "ghost")
	require.NoError(t, h.Note(c))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestNote_Remote(t *testing.T) {
	h, _, noteRepo, _ := newHandler(t)
	host := "remote.example"
	noteRepo.Notes["n1"] = &model.Note{
		ID: "n1", UserID: "u1", Visibility: model.NoteVisibilityPublic, UserHost: &host,
	}
	c, rec := newReq(t, "id", "n1")
	require.NoError(t, h.Note(c))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// --- API endpoint tests ---

func postJSON(handler func(echo.Context) error, body string) *httptest.ResponseRecorder {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	_ = handler(c)
	return rec
}

func TestAPIGet_Success_Note(t *testing.T) {
	h, _, noteRepo, _ := newHandler(t)
	noteRepo.Notes["n1"] = &model.Note{ID: "n1", UserID: "u1", Visibility: model.NoteVisibilityPublic}
	rec := postJSON(h.APIGet, `{"uri":"https://example.com/notes/n1"}`)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "Note")
}

func TestAPIGet_Success_User(t *testing.T) {
	h, userRepo, _, keypairRepo := newHandler(t)
	userRepo.Users["u1"] = &model.User{ID: "u1", Username: "alice"}
	keypairRepo.items["u1"] = &model.UserKeypair{UserID: "u1", PublicKey: "PUB"}
	rec := postJSON(h.APIGet, `{"uri":"https://example.com/users/u1"}`)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "Person")
}

func TestAPIGet_NotFound(t *testing.T) {
	h, _, _, _ := newHandler(t)
	rec := postJSON(h.APIGet, `{"uri":"https://example.com/notes/ghost"}`)
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Contains(t, rec.Body.String(), "NO_SUCH_OBJECT")
}

func TestAPIGet_InvalidParam(t *testing.T) {
	h, _, _, _ := newHandler(t)
	rec := postJSON(h.APIGet, `{}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestAPIGet_UserNotFound(t *testing.T) {
	h, _, _, _ := newHandler(t)
	rec := postJSON(h.APIGet, `{"uri":"https://example.com/users/ghost"}`)
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Contains(t, rec.Body.String(), "NO_SUCH_OBJECT")
}

func TestAPIGet_UserKeypairError(t *testing.T) {
	h, userRepo, _, keypairRepo := newHandler(t)
	userRepo.Users["u1"] = &model.User{ID: "u1", Username: "alice"}
	keypairRepo.err = errors.New("db down")
	rec := postJSON(h.APIGet, `{"uri":"https://example.com/users/u1"}`)
	// keypairエラー → 404
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Contains(t, rec.Body.String(), "NO_SUCH_OBJECT")
}

func TestAPIGet_NoMatch(t *testing.T) {
	h, _, _, _ := newHandler(t)
	rec := postJSON(h.APIGet, `{"uri":"https://other.example/something"}`)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestAPIShow_Note(t *testing.T) {
	h, _, noteRepo, _ := newHandler(t)
	noteRepo.Notes["n1"] = &model.Note{ID: "n1", UserID: "u1", Visibility: model.NoteVisibilityPublic, User: &model.User{ID: "u1", Username: "alice"}}
	rec := postJSON(h.APIShow, `{"uri":"https://example.com/notes/n1"}`)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"type":"Note"`)
}

func TestAPIShow_User(t *testing.T) {
	h, userRepo, _, _ := newHandler(t)
	userRepo.Users["u1"] = &model.User{ID: "u1", Username: "bob"}
	rec := postJSON(h.APIShow, `{"uri":"https://example.com/users/u1"}`)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"type":"User"`)
}

func TestAPIShow_NotFound(t *testing.T) {
	h, _, _, _ := newHandler(t)
	rec := postJSON(h.APIShow, `{"uri":"https://example.com/notes/ghost"}`)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestAPIShow_InvalidParam(t *testing.T) {
	h, _, _, _ := newHandler(t)
	rec := postJSON(h.APIShow, `{}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestAPIShow_RemoteNote(t *testing.T) {
	h, _, noteRepo, _ := newHandler(t)
	host := "remote.example"
	noteRepo.Notes["n1"] = &model.Note{ID: "n1", UserID: "u1", Visibility: model.NoteVisibilityPublic, UserHost: &host}
	rec := postJSON(h.APIShow, `{"uri":"https://example.com/notes/n1"}`)
	// リモートノートはスキップされるので NotFound になる
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestAPIShow_RemoteUser(t *testing.T) {
	h, userRepo, _, _ := newHandler(t)
	host := "remote.example"
	userRepo.Users["u1"] = &model.User{ID: "u1", Username: "bob", Host: &host}
	rec := postJSON(h.APIShow, `{"uri":"https://example.com/users/u1"}`)
	// リモートユーザーはスキップされるので NotFound
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestExtractLocalID(t *testing.T) {
	assert.Equal(t, "abc123", extractLocalID("https://example.com/notes/abc123", "/notes/"))
	assert.Equal(t, "xyz", extractLocalID("https://example.com/users/xyz", "/users/"))
	assert.Equal(t, "", extractLocalID("https://example.com/other", "/notes/"))
	assert.Equal(t, "", extractLocalID("", "/notes/"))
}

func TestExtractLocalID_Fragment(t *testing.T) {
	// #main-key 等のフラグメントが除去されること
	assert.Equal(t, "u1", extractLocalID("https://example.com/users/u1#main-key", "/users/"))
	assert.Equal(t, "n1", extractLocalID("https://example.com/notes/n1#fragment", "/notes/"))
}

func TestPackNoteForAPI_NilUser(t *testing.T) {
	n := &model.Note{ID: "n1", UserID: "u1", Visibility: model.NoteVisibilityPublic}
	result := packNoteForAPI(n)
	assert.Equal(t, "n1", result["id"])
	_, hasUser := result["user"]
	assert.False(t, hasUser)
}

func TestPackUserForAPI_Nil(t *testing.T) {
	result := packUserForAPI(nil)
	assert.Empty(t, result)
}

func TestMustMarshal_Panics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic")
		}
	}()
	_ = mustMarshal(make(chan int))
}

// --- Remote fetch tests ---

type mockFetcher struct {
	data []byte
	err  error
}

func (m *mockFetcher) FetchObject(_ string) ([]byte, error) {
	return m.data, m.err
}

type mockResolver struct {
	user    *model.User
	err     error
	note    *model.Note
	noteErr error
}

func (m *mockResolver) ResolveActor(_ string) (*model.User, error) {
	return m.user, m.err
}

func (m *mockResolver) ResolveNote(_ string) (*model.Note, error) {
	return m.note, m.noteErr
}

func TestSetRemote(t *testing.T) {
	h, _, _, _ := newHandler(t)
	h.SetRemote(&mockFetcher{}, &mockResolver{})
	assert.NotNil(t, h.remoteFetcher)
}

func TestAPIGet_RemoteFetch(t *testing.T) {
	h, _, _, _ := newHandler(t)
	h.SetRemote(&mockFetcher{data: []byte(`{"type":"Note","id":"https://remote.example/notes/1"}`)}, nil)
	rec := postJSON(h.APIGet, `{"uri":"https://remote.example/notes/1"}`)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "remote.example")
}

func TestAPIGet_RemoteFetchError(t *testing.T) {
	h, _, _, _ := newHandler(t)
	h.SetRemote(&mockFetcher{err: assert.AnError}, nil)
	rec := postJSON(h.APIGet, `{"uri":"https://remote.example/notes/1"}`)
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Contains(t, rec.Body.String(), "NO_SUCH_OBJECT")
}

func TestAPIShow_RemoteResolveActor(t *testing.T) {
	h, _, _, _ := newHandler(t)
	h.SetRemote(nil, &mockResolver{user: &model.User{ID: "ru1", Username: "remote_alice"}})
	rec := postJSON(h.APIShow, `{"uri":"https://remote.example/users/alice"}`)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "remote_alice")
}

func TestAPIShow_RemoteFetchNote(t *testing.T) {
	h, _, _, _ := newHandler(t)
	// ResolveNote fails -> fall through to raw AP JSON echo.
	h.SetRemote(
		&mockFetcher{data: []byte(`{"type":"Note","content":"hello"}`)},
		&mockResolver{noteErr: assert.AnError},
	)
	rec := postJSON(h.APIShow, `{"uri":"https://remote.example/notes/1"}`)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"type":"Note"`)
}

func TestAPIShow_RemoteFetchNote_ResolveNoteSuccess(t *testing.T) {
	h, _, _, _ := newHandler(t)
	// ResolveNote succeeds -> return the ingested local note object.
	h.SetRemote(
		&mockFetcher{data: []byte(`{"type":"Note","content":"hello"}`)},
		&mockResolver{note: &model.Note{ID: "ln1", UserID: "u1", Visibility: model.NoteVisibilityPublic, User: &model.User{ID: "u1", Username: "alice"}}},
	)
	rec := postJSON(h.APIShow, `{"uri":"https://remote.example/notes/1"}`)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"type":"Note"`)
	assert.Contains(t, rec.Body.String(), `"id":"ln1"`)
}

func TestAPIShow_RemoteFetchArticle_NoResolver(t *testing.T) {
	h, _, _, _ := newHandler(t)
	// Fetcher returns an Article; no resolver wired -> raw AP JSON.
	h.SetRemote(&mockFetcher{data: []byte(`{"type":"Article","content":"long form"}`)}, nil)
	rec := postJSON(h.APIShow, `{"uri":"https://remote.example/articles/1"}`)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"type":"Note"`)
}

func TestAPIShow_RemoteFetchPerson_ResolveActor(t *testing.T) {
	h, _, _, _ := newHandler(t)
	// Fetcher returns a Person; resolver succeeds -> return user object.
	h.SetRemote(
		&mockFetcher{data: []byte(`{"type":"Person","id":"https://remote.example/users/alice"}`)},
		&mockResolver{user: &model.User{ID: "ru1", Username: "alice"}},
	)
	rec := postJSON(h.APIShow, `{"uri":"https://remote.example/users/alice"}`)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"type":"User"`)
	assert.Contains(t, rec.Body.String(), `"ru1"`)
}

func TestAPIShow_RemoteFetchPerson_ResolveActorFails(t *testing.T) {
	h, _, _, _ := newHandler(t)
	// Fetcher returns a Person; resolver fails AND fallback also fails -> 404.
	h.SetRemote(
		&mockFetcher{data: []byte(`{"type":"Person","id":"https://remote.example/users/alice"}`)},
		&mockResolver{err: assert.AnError},
	)
	rec := postJSON(h.APIShow, `{"uri":"https://remote.example/users/alice"}`)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestAPIShow_RemoteFetchServiceType(t *testing.T) {
	h, _, _, _ := newHandler(t)
	// Coverage for actor subtypes: Service/Application/Organization/Group
	// all take the same branch.
	h.SetRemote(
		&mockFetcher{data: []byte(`{"type":"Service"}`)},
		&mockResolver{user: &model.User{ID: "svc1", Username: "svc"}},
	)
	rec := postJSON(h.APIShow, `{"uri":"https://remote.example/users/svc"}`)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"type":"User"`)
}

func TestAPIShow_RemoteNothing(t *testing.T) {
	h, _, _, _ := newHandler(t)
	h.SetRemote(
		&mockFetcher{err: assert.AnError},
		&mockResolver{err: assert.AnError},
	)
	rec := postJSON(h.APIShow, `{"uri":"https://remote.example/unknown"}`)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// --- UserByAcct + Accept header negotiation ---

func newAcctReq(t *testing.T, accept, acct string) (echo.Context, *httptest.ResponseRecorder) {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if accept != "" {
		req.Header.Set(echo.HeaderAccept, accept)
	}
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("acct")
	c.SetParamValues(acct)
	return c, rec
}

func TestUserByAcct_Success(t *testing.T) {
	h, userRepo, _, keypairRepo := newHandler(t)
	userRepo.Users["u1"] = &model.User{ID: "u1", Username: "alice", UsernameLower: "alice"}
	keypairRepo.items["u1"] = &model.UserKeypair{UserID: "u1", PublicKey: "PUB"}

	c, rec := newAcctReq(t, "application/activity+json", "alice")
	require.NoError(t, h.UserByAcct(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "alice")
}

func TestUserByAcct_HTMLFallsThrough(t *testing.T) {
	h, _, _, _ := newHandler(t)
	c, _ := newAcctReq(t, "text/html", "alice")
	err := h.UserByAcct(c)
	// フォールバック未設定なら serveNonAP は ErrNotFound を返すので、
	// ハンドラ単体のテストで確実に 404 相当を観測できる。
	assert.Equal(t, echo.ErrNotFound, err)
}

func TestUserByAcct_HTMLUsesFallback(t *testing.T) {
	h, _, _, _ := newHandler(t)
	called := false
	h.SetNonAPFallback(func(c echo.Context) error {
		called = true
		return c.String(http.StatusOK, "FRONTEND")
	})
	c, rec := newAcctReq(t, "text/html", "alice")
	require.NoError(t, h.UserByAcct(c))
	assert.True(t, called, "fallback should be invoked for non-AP Accept")
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "FRONTEND", rec.Body.String())
}

// Browser path で `/users/<id>` を踏んだ場合は SPA fallback ではなく
// `/@<username>` permalink への 302 redirect で SPA route に解決させる
// (#691)。frontend は `/users/:id` SPA ルートを持たないので、ここで
// SPA HTML を返しても not-found ページが描画されてしまう。
func TestUser_HTMLRedirectsToAcct(t *testing.T) {
	h, userRepo, _, _ := newHandler(t)
	userRepo.Users["u1"] = &model.User{ID: "u1", Username: "alice"}
	h.SetNonAPFallback(func(c echo.Context) error {
		return c.String(http.StatusOK, "FRONTEND")
	})
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(echo.HeaderAccept, "text/html")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues("u1")
	require.NoError(t, h.User(c))
	assert.Equal(t, http.StatusFound, rec.Code, "browser must be redirected, not served SPA HTML")
	assert.Equal(t, "/@alice", rec.Header().Get("Location"))
}

func TestNote_HTMLUsesFallback(t *testing.T) {
	h, _, noteRepo, _ := newHandler(t)
	noteRepo.Notes["n1"] = &model.Note{ID: "n1", UserID: "u1", Visibility: model.NoteVisibilityPublic}
	h.SetNonAPFallback(func(c echo.Context) error {
		return c.String(http.StatusOK, "FRONTEND")
	})
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(echo.HeaderAccept, "text/html")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues("n1")
	require.NoError(t, h.Note(c))
	assert.Equal(t, "FRONTEND", rec.Body.String())
}

func TestUserByAcct_LDJSON(t *testing.T) {
	h, userRepo, _, keypairRepo := newHandler(t)
	userRepo.Users["u1"] = &model.User{ID: "u1", Username: "alice", UsernameLower: "alice"}
	keypairRepo.items["u1"] = &model.UserKeypair{UserID: "u1", PublicKey: "PUB"}

	c, rec := newAcctReq(t, "application/ld+json", "alice")
	require.NoError(t, h.UserByAcct(c))
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestUserByAcct_WithHostSuffix(t *testing.T) {
	h, userRepo, _, keypairRepo := newHandler(t)
	userRepo.Users["u1"] = &model.User{ID: "u1", Username: "alice", UsernameLower: "alice"}
	keypairRepo.items["u1"] = &model.UserKeypair{UserID: "u1", PublicKey: "PUB"}

	// /@alice@example.com — local lookup should still work (host suffix is ignored).
	c, rec := newAcctReq(t, "application/activity+json", "alice@example.com")
	require.NoError(t, h.UserByAcct(c))
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestUserByAcct_UserNotFound(t *testing.T) {
	h, _, _, _ := newHandler(t)
	c, rec := newAcctReq(t, "application/activity+json", "ghost")
	require.NoError(t, h.UserByAcct(c))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestUserByAcct_RemoteUser(t *testing.T) {
	h, userRepo, _, _ := newHandler(t)
	host := "remote.example"
	// The local FindByUsernameLower filter treats host=nil as "local only" so a
	// remote user would normally be unreachable. We override the lookup here
	// to force-return a user with Host != nil and exercise the defensive
	// Host != nil guard in UserByAcct.
	userRepo.FindByUsernameLowerFn = func(_ string, _ *string) (*model.User, error) {
		return &model.User{ID: "u1", Username: "alice", UsernameLower: "alice", Host: &host}, nil
	}
	c, rec := newAcctReq(t, "application/activity+json", "alice")
	require.NoError(t, h.UserByAcct(c))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestUserByAcct_KeypairError(t *testing.T) {
	h, userRepo, _, keypairRepo := newHandler(t)
	userRepo.Users["u1"] = &model.User{ID: "u1", Username: "alice", UsernameLower: "alice"}
	keypairRepo.err = errors.New("db down")
	c, rec := newAcctReq(t, "application/activity+json", "alice")
	require.NoError(t, h.UserByAcct(c))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestAPIGet_RemoteInvalidJSON(t *testing.T) {
	h, _, _, _ := newHandler(t)
	h.SetRemote(&mockFetcher{data: []byte(`not json`)}, nil)
	rec := postJSON(h.APIGet, `{"uri":"https://remote.example/x"}`)
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Contains(t, rec.Body.String(), "NO_SUCH_OBJECT")
}
