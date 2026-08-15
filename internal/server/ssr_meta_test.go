package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shiroha-a/mk/internal/config"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/testutil"
	"gorm.io/datatypes"
)

func newSSRTestHandler(t *testing.T) (*ssrMetaHandler, *testutil.MockUserRepository, *testutil.MockNoteRepository) {
	t.Helper()
	metaRepo := testutil.NewMockMetaRepository()
	metaRepo.Meta = &model.Meta{ID: "x"}
	userRepo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	h := newSSRMetaHandler(
		&config.Config{URL: "https://example.test", Host: "example.test"},
		metaRepo, nil, nil,
		userRepo, noteRepo, nil, nil, nil, nil,
	)
	return h, userRepo, noteRepo
}

func ssrGet(t *testing.T, handler echo.HandlerFunc, path string, params map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	e := echo.New()
	rec := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest(http.MethodGet, path, nil), rec)
	names := make([]string, 0, len(params))
	values := make([]string, 0, len(params))
	for k, v := range params {
		names = append(names, k)
		values = append(values, v)
	}
	c.SetParamNames(names...)
	c.SetParamValues(values...)
	require.NoError(t, handler(c))
	return rec
}

func ssrTestUser(id, username string) *model.User {
	return &model.User{ID: id, Username: username, UsernameLower: username, AvatarDecorations: datatypes.JSON([]byte("[]"))}
}

func TestSSRUserPage_EmitsUserMeta(t *testing.T) {
	h, userRepo, _ := newSSRTestHandler(t)
	userRepo.Users["u1"] = ssrTestUser("u1", "alice")

	rec := ssrGet(t, h.UserPage, "/@alice", map[string]string{"acct": "alice"})

	assert.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, `<meta name="misskey:user-username" content="alice">`)
	assert.Contains(t, body, `<meta name="misskey:user-id" content="u1">`)
	assert.Contains(t, body, `<meta property="og:url" content="https://example.test/@alice">`)
}

// 存在しない acct でも 200 で shell を返す (404 にするのは frontend の役目)。
func TestSSRUserPage_UnknownUserStillServesShell(t *testing.T) {
	h, _, _ := newSSRTestHandler(t)

	rec := ssrGet(t, h.UserPage, "/@ghost", map[string]string{"acct": "ghost"})

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.NotContains(t, rec.Body.String(), "misskey:user-id")
}

// 自ホスト付きの acct は local user として解決する。
func TestSSRUserPage_SelfHostAcctResolvesLocal(t *testing.T) {
	h, userRepo, _ := newSSRTestHandler(t)
	userRepo.Users["u1"] = ssrTestUser("u1", "alice")

	rec := ssrGet(t, h.UserPage, "/@alice@example.test", map[string]string{"acct": "alice@example.test"})

	assert.Contains(t, rec.Body.String(), `<meta name="misskey:user-id" content="u1">`)
}

func TestSSRNotePage_EmitsNoteMeta(t *testing.T) {
	h, userRepo, noteRepo := newSSRTestHandler(t)
	alice := ssrTestUser("u1", "alice")
	userRepo.Users["u1"] = alice
	text := "hello"
	noteRepo.Notes["n1"] = &model.Note{
		ID: "n1", UserID: "u1", User: alice, Text: &text,
		Visibility: model.NoteVisibilityPublic,
	}

	rec := ssrGet(t, h.NotePage, "/notes/n1", map[string]string{"id": "n1"})

	body := rec.Body.String()
	assert.Contains(t, body, `<meta name="misskey:note-id" content="n1">`)
	assert.Contains(t, body, `<meta name="misskey:user-username" content="alice">`)
	assert.Contains(t, body, `<meta property="og:description" content="hello">`)
}

// public でない note は meta を出さない (クローラ / リンク展開に本文を渡さない)。
func TestSSRNotePage_NonPublicNoteHasNoMeta(t *testing.T) {
	h, userRepo, noteRepo := newSSRTestHandler(t)
	alice := ssrTestUser("u1", "alice")
	userRepo.Users["u1"] = alice
	text := "secret"
	noteRepo.Notes["n1"] = &model.Note{
		ID: "n1", UserID: "u1", User: alice, Text: &text,
		Visibility: model.NoteVisibilityFollowers,
	}

	body := ssrGet(t, h.NotePage, "/notes/n1", map[string]string{"id": "n1"}).Body.String()

	assert.NotContains(t, body, "misskey:note-id")
	assert.NotContains(t, body, "secret")
}

func TestSSRNotePage_UnknownNoteStillServesShell(t *testing.T) {
	h, _, _ := newSSRTestHandler(t)
	rec := ssrGet(t, h.NotePage, "/notes/ghost", map[string]string{"id": "ghost"})
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.NotContains(t, rec.Body.String(), "misskey:note-id")
}

// meta の content は必ず escape する (username は攻撃者が持ち込める)。
func TestSSRMetaTagEscapesContent(t *testing.T) {
	got := metaTag("misskey:user-username", `a"><script>alert(1)</script>`)
	assert.NotContains(t, got, "<script>")
	assert.Contains(t, got, "&lt;script&gt;")
	assert.Contains(t, propertyTag("og:title", `x"y`), "&#34;")
}

func TestSSRDisplayName(t *testing.T) {
	host := "remote.example"
	name := "Alice"
	assert.Equal(t, "@alice", displayName(&model.User{Username: "alice"}))
	assert.Equal(t, "Alice (@alice)", displayName(&model.User{Username: "alice", Name: &name}))
	assert.Equal(t, "@alice@remote.example", displayName(&model.User{Username: "alice", Host: &host}))
}

// remote user のページは noindex を出す (自インスタンスの URL を正規と
// みなされないようにする)。
func TestSSRUserHead_RemoteUserIsNoindex(t *testing.T) {
	h, _, _ := newSSRTestHandler(t)
	host := "remote.example"
	head := h.userHead(&model.User{ID: "u2", Username: "bob", Host: &host})
	assert.Contains(t, head, `<meta name="robots" content="noindex">`)

	local := h.userHead(&model.User{ID: "u1", Username: "alice"})
	assert.NotContains(t, local, "noindex")
	assert.Empty(t, h.userHead(nil))
}

func TestPrefersHTML(t *testing.T) {
	e := echo.New()
	newCtx := func(accept string) echo.Context {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		if accept != "" {
			req.Header.Set(echo.HeaderAccept, accept)
		}
		return e.NewContext(req, httptest.NewRecorder())
	}
	assert.True(t, prefersHTML(newCtx("")), "Accept 無しは HTML 扱い")
	assert.True(t, prefersHTML(newCtx("text/html, */*")))
	assert.True(t, prefersHTML(newCtx("*/*")))
	assert.False(t, prefersHTML(newCtx("application/activity+json")))
	assert.False(t, prefersHTML(newCtx(`application/ld+json; profile="https://www.w3.org/ns/activitystreams"`)))
}

// repo が未配線でも panic せず shell を返す。
func TestSSRHandlers_NilReposServeShell(t *testing.T) {
	metaRepo := testutil.NewMockMetaRepository()
	metaRepo.Meta = &model.Meta{ID: "x"}
	h := newSSRMetaHandler(&config.Config{URL: "https://example.test"}, metaRepo, nil, nil, nil, nil, nil, nil, nil, nil)

	for name, handler := range map[string]echo.HandlerFunc{
		"user":    h.UserPage,
		"page":    h.UserPagePage,
		"note":    h.NotePage,
		"clip":    h.ClipPage,
		"flash":   h.FlashPage,
		"gallery": h.GalleryPage,
	} {
		rec := ssrGet(t, handler, "/", map[string]string{"acct": "alice", "id": "x", "clip": "x", "post": "x", "page": "p"})
		assert.Equal(t, http.StatusOK, rec.Code, name)
	}
}

// permalink が自前の OGP を出すとき、shell 側の既定 OGP は出さないこと (#2527)。
//
// 両方出すと 1 ページに og:title / og:url / og:type が 2 つ並ぶ。OGP のパーサは
// 先頭を採用するのが一般的なので、ノートを共有しても著者名でなくインスタンス名が
// 出てしまう (本番で実際にそうなっていた)。upstream は base.tsx の `ogSlot` ごと
// 差し替えるので重複しない。
func TestSSRNotePage_DoesNotDuplicateShellOG(t *testing.T) {
	h, userRepo, noteRepo := newSSRTestHandler(t)
	alice := ssrTestUser("u1", "alice")
	userRepo.Users["u1"] = alice
	text := "hello"
	noteRepo.Notes["n1"] = &model.Note{
		ID: "n1", UserID: "u1", User: alice, Text: &text,
		Visibility: model.NoteVisibilityPublic,
	}

	body := ssrGet(t, h.NotePage, "/notes/n1", map[string]string{"id": "n1"}).Body.String()

	assert.Equal(t, 1, strings.Count(body, `property="og:title"`))
	assert.Equal(t, 1, strings.Count(body, `property="og:url"`))
	assert.Equal(t, 1, strings.Count(body, `property="og:type"`))
	assert.Equal(t, 1, strings.Count(body, `property="og:description"`))
	// note 側の値が残っていること (shell 側を消した結果 note の分まで消えていない)
	assert.Contains(t, body, `<meta property="og:type" content="article">`)
	assert.Contains(t, body, `<meta property="og:url" content="https://example.test/notes/n1">`)
	// site_name / instance_url は upstream でも ogSlot の外なので残る
	assert.Contains(t, body, `<meta property="og:site_name"`)
	assert.Contains(t, body, `<meta property="instance_url"`)
}

// 対象が見つからないページは素の shell を返すので、shell 側の既定 OGP は残る。
func TestSSRNotePage_UnknownNoteKeepsShellOG(t *testing.T) {
	h, _, _ := newSSRTestHandler(t)

	body := ssrGet(t, h.NotePage, "/notes/ghost", map[string]string{"id": "ghost"}).Body.String()

	assert.Contains(t, body, `<meta property="og:type" content="website">`)
	assert.Contains(t, body, `<meta name="twitter:card" content="summary">`)
}

// permalink では shell 側の「インスタンスの」description も出さない (#2527)。
//
// upstream は note ページの `desc` をノート要約に差し替えるので、インスタンス
// 説明が note の description として出ることはない。mk-go はページ固有の
// description をまだ持たないため、誤った値を出すよりは出さない方に倒す。
func TestSSRNotePage_DoesNotEmitInstanceDescription(t *testing.T) {
	h, userRepo, noteRepo := newSSRTestHandler(t)
	metaRepo := testutil.NewMockMetaRepository()
	instanceDesc := "インスタンスの説明"
	metaRepo.Meta = &model.Meta{ID: "x", Description: &instanceDesc}
	h.metaRepo = metaRepo

	alice := ssrTestUser("u1", "alice")
	userRepo.Users["u1"] = alice
	text := "hello"
	noteRepo.Notes["n1"] = &model.Note{
		ID: "n1", UserID: "u1", User: alice, Text: &text,
		Visibility: model.NoteVisibilityPublic,
	}

	body := ssrGet(t, h.NotePage, "/notes/n1", map[string]string{"id": "n1"}).Body.String()

	assert.NotContains(t, body, `<meta name="description" content="`+instanceDesc+`">`)
	assert.Contains(t, body, `<meta property="og:description" content="hello">`)

	// 素の shell (対象なし) では従来どおり出る。
	shell := ssrGet(t, h.NotePage, "/notes/ghost", map[string]string{"id": "ghost"}).Body.String()
	assert.Contains(t, shell, `<meta name="description" content="`+instanceDesc+`">`)
}
