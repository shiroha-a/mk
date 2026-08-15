package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shiroha-a/mk/internal/config"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
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
		ssrMetaDeps{User: userRepo, Note: noteRepo, IDGen: testIDGen(t)},
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

// testIDGen returns the default (aidx) generator. drive file の pack が
// createdAt の復元に使う。
func testIDGen(t *testing.T) id.Generator {
	t.Helper()
	g, err := id.NewGenerator("aidx")
	require.NoError(t, err)
	return g
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
	host := "remote.example"
	head := userHead(&model.User{ID: "u2", Username: "bob", Host: &host}, nil, false)
	assert.Contains(t, head, `<meta name="robots" content="noindex">`)

	local := userHead(&model.User{ID: "u1", Username: "alice"}, nil, false)
	assert.NotContains(t, local, "noindex")
	assert.Empty(t, userHead(nil, nil, false))
}

// upstream の各 view が profile を見て出す crawler 向けディレクティブ (#2528)。
func TestSSRUserHead_ProfileCrawlerDirectives(t *testing.T) {
	local := &model.User{ID: "u1", Username: "alice"}

	t.Run("noCrawle で noindex", func(t *testing.T) {
		head := userHead(local, &model.UserProfile{UserID: "u1", NoCrawle: true}, false)
		assert.Contains(t, head, `<meta name="robots" content="noindex">`)
	})

	t.Run("preventAiLearning で noimageai / noai", func(t *testing.T) {
		head := userHead(local, &model.UserProfile{UserID: "u1", PreventAiLearning: true}, false)
		assert.Contains(t, head, `<meta name="robots" content="noimageai">`)
		assert.Contains(t, head, `<meta name="robots" content="noai">`)
		// 学習拒否そのものは index を妨げない
		assert.NotContains(t, head, `content="noindex"`)
	})

	t.Run("どちらも false なら robots を出さない", func(t *testing.T) {
		head := userHead(local, &model.UserProfile{UserID: "u1"}, false)
		assert.NotContains(t, head, `name="robots"`)
	})

	t.Run("forceNoindex はページ側の条件 (renote) 用", func(t *testing.T) {
		head := userHead(local, &model.UserProfile{UserID: "u1"}, true)
		assert.Contains(t, head, `<meta name="robots" content="noindex">`)
	})
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
	h := newSSRMetaHandler(&config.Config{URL: "https://example.test"}, metaRepo, nil, nil, ssrMetaDeps{IDGen: testIDGen(t)})

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

// upstream user.tsx / note.tsx の metaSlot が出す rel=alternate / rel=me (#2528)。
func TestSSRUserPage_AlternateAndMeLinks(t *testing.T) {
	h, userRepo, _ := newSSRTestHandler(t)
	alice := ssrTestUser("u1", "alice")
	userRepo.Users["u1"] = alice
	userRepo.Profiles["u1"] = &model.UserProfile{
		UserID: "u1",
		Fields: datatypes.JSON([]byte(`[{"name":"web","value":"https://alice.example"},{"name":"pronoun","value":"they/them"}]`)),
	}

	body := ssrGet(t, h.UserPage, "/@alice", map[string]string{"acct": "alice"}).Body.String()

	assert.Contains(t, body, `<link rel="alternate" type="application/activity+json" href="https://example.test/users/u1">`)
	// URL でない field は rel=me にしない (所有証明に使われるので混ぜない)
	assert.Contains(t, body, `<link rel="me" href="https://alice.example">`)
	assert.NotContains(t, body, "they/them")
}

// sub パス (`/@alice/following` 等) では alternate を出さない。AP actor の
// 正規 URL は `/@alice` の方なので、サブページに同じ alternate を並べない。
func TestSSRUserPage_SubPathHasNoAlternate(t *testing.T) {
	h, userRepo, _ := newSSRTestHandler(t)
	userRepo.Users["u1"] = ssrTestUser("u1", "alice")

	body := ssrGet(t, h.UserPage, "/@alice/following",
		map[string]string{"acct": "alice", "sub": "following"}).Body.String()

	assert.NotContains(t, body, `rel="alternate"`)
	assert.Contains(t, body, `<meta name="misskey:user-id" content="u1">`)
}

// remote user は自分の URI を、local user は自インスタンスの AP URL を出す。
func TestSSRUserPage_RemoteUserAlternateUsesURI(t *testing.T) {
	h, userRepo, _ := newSSRTestHandler(t)
	host := "remote.example"
	uri := "https://remote.example/users/9"
	profileURL := "https://remote.example/@bob"
	bob := ssrTestUser("u2", "bob")
	bob.Host = &host
	bob.URI = &uri
	userRepo.Users["u2"] = bob
	userRepo.Profiles["u2"] = &model.UserProfile{UserID: "u2", URL: &profileURL}

	body := ssrGet(t, h.UserPage, "/@bob@remote.example",
		map[string]string{"acct": "bob@remote.example"}).Body.String()

	assert.Contains(t, body, `<link rel="alternate" type="application/activity+json" href="`+uri+`">`)
	assert.Contains(t, body, `<link rel="alternate" type="text/html" href="`+profileURL+`">`)
	// remote user に自インスタンスの AP URL を主張させない
	assert.NotContains(t, body, `href="https://example.test/users/u2"`)
}

func TestSSRNotePage_AlternateAndRenoteNoindex(t *testing.T) {
	h, userRepo, noteRepo := newSSRTestHandler(t)
	alice := ssrTestUser("u1", "alice")
	userRepo.Users["u1"] = alice
	text := "hello"
	noteRepo.Notes["n1"] = &model.Note{
		ID: "n1", UserID: "u1", User: alice, Text: &text,
		Visibility: model.NoteVisibilityPublic,
	}
	renoteID := "n1"
	noteRepo.Notes["n2"] = &model.Note{
		ID: "n2", UserID: "u1", User: alice, RenoteID: &renoteID,
		Visibility: model.NoteVisibilityPublic,
	}

	plain := ssrGet(t, h.NotePage, "/notes/n1", map[string]string{"id": "n1"}).Body.String()
	assert.Contains(t, plain, `<link rel="alternate" type="application/activity+json" href="https://example.test/notes/n1">`)
	assert.NotContains(t, plain, `content="noindex"`)

	// upstream note.tsx は isRenotePacked (renoteId != null) で noindex。
	// 引用も対象で、他人の投稿を写した URL を検索対象にしない。
	renote := ssrGet(t, h.NotePage, "/notes/n2", map[string]string{"id": "n2"}).Body.String()
	assert.Contains(t, renote, `<meta name="robots" content="noindex">`)
}

// federation を切っているインスタンスでは AP の URI を広告しない。
func TestSSRPages_NoAlternateWhenFederationDisabled(t *testing.T) {
	h, userRepo, _ := newSSRTestHandler(t)
	metaRepo := testutil.NewMockMetaRepository()
	metaRepo.Meta = &model.Meta{ID: "x", Federation: "none"}
	h.metaRepo = metaRepo
	userRepo.Users["u1"] = ssrTestUser("u1", "alice")

	body := ssrGet(t, h.UserPage, "/@alice", map[string]string{"acct": "alice"}).Body.String()

	assert.NotContains(t, body, `rel="alternate"`)
}

// note ページの OGP が upstream views/note.tsx の ogBlock と揃うこと (#2528)。
func TestSSRNotePage_MediaOpenGraph(t *testing.T) {
	newHandler := func(t *testing.T) (*ssrMetaHandler, *testutil.MockNoteRepository, *testutil.MockDriveFileRepository) {
		t.Helper()
		h, userRepo, noteRepo := newSSRTestHandler(t)
		driveRepo := testutil.NewMockDriveFileRepository()
		h.driveFileRepo = driveRepo
		userRepo.Users["u1"] = ssrTestUser("u1", "alice")
		return h, noteRepo, driveRepo
	}
	author := func(t *testing.T, h *ssrMetaHandler) *model.User {
		t.Helper()
		u, err := h.userRepo.FindByID("u1")
		require.NoError(t, err)
		return u
	}

	t.Run("画像添付は summary_large_image と og:image", func(t *testing.T) {
		h, noteRepo, driveRepo := newHandler(t)
		driveRepo.Files["f1"] = &model.DriveFile{
			ID: "f1", Type: "image/png", URL: "https://example.test/files/f1.png",
			Properties: datatypes.JSON([]byte(`{"width":800,"height":600}`)),
		}
		text := "写真"
		noteRepo.Notes["n1"] = &model.Note{
			ID: "n1", UserID: "u1", User: author(t, h), Text: &text,
			FileIDs: []string{"f1"}, Visibility: model.NoteVisibilityPublic,
		}

		body := ssrGet(t, h.NotePage, "/notes/n1", map[string]string{"id": "n1"}).Body.String()

		assert.Contains(t, body, `<meta name="twitter:card" content="summary_large_image">`)
		assert.Contains(t, body, `<meta property="og:image" content="https://example.test/files/f1.png">`)
		assert.Contains(t, body, `<meta property="og:image:width" content="800">`)
		assert.Contains(t, body, `<meta property="og:image:height" content="600">`)
		// 添付があると summary に (📎1) が付く
		assert.Contains(t, body, `<meta property="og:description" content="写真 (📎1)">`)
	})

	t.Run("画像が無ければ avatar と summary", func(t *testing.T) {
		h, noteRepo, _ := newHandler(t)
		text := "文字だけ"
		noteRepo.Notes["n1"] = &model.Note{
			ID: "n1", UserID: "u1", User: author(t, h), Text: &text,
			Visibility: model.NoteVisibilityPublic,
		}

		body := ssrGet(t, h.NotePage, "/notes/n1", map[string]string{"id": "n1"}).Body.String()

		assert.Contains(t, body, `<meta name="twitter:card" content="summary">`)
		assert.NotContains(t, body, "summary_large_image")
		// avatar 未設定なら identicon。**絶対 URL** でないとクローラが解決できない
		assert.Contains(t, body, `<meta property="og:image" content="https://example.test/identicon/alice">`)
	})

	t.Run("動画は og:video 一式を出す", func(t *testing.T) {
		h, noteRepo, driveRepo := newHandler(t)
		thumb := "https://example.test/files/thumb.webp"
		driveRepo.Files["v1"] = &model.DriveFile{
			ID: "v1", Type: "video/mp4", URL: "https://example.test/files/v1.mp4",
			ThumbnailURL: &thumb,
			Properties:   datatypes.JSON([]byte(`{"width":1920,"height":1080}`)),
		}
		noteRepo.Notes["n1"] = &model.Note{
			ID: "n1", UserID: "u1", User: author(t, h),
			FileIDs: []string{"v1"}, Visibility: model.NoteVisibilityPublic,
		}

		body := ssrGet(t, h.NotePage, "/notes/n1", map[string]string{"id": "n1"}).Body.String()

		assert.Contains(t, body, `<meta property="og:video:url" content="https://example.test/files/v1.mp4">`)
		assert.Contains(t, body, `<meta property="og:video:secure_url" content="https://example.test/files/v1.mp4">`)
		assert.Contains(t, body, `<meta property="og:video:type" content="video/mp4">`)
		assert.Contains(t, body, `<meta property="og:video:image" content="`+thumb+`">`)
		assert.Contains(t, body, `<meta property="og:video:width" content="1920">`)
		assert.Contains(t, body, `<meta property="og:video:height" content="1080">`)
		// 画像が無いので card は summary + avatar
		assert.Contains(t, body, `<meta name="twitter:card" content="summary">`)
	})

	t.Run("title は <著者> | <インスタンス>", func(t *testing.T) {
		h, noteRepo, _ := newHandler(t)
		metaRepo := testutil.NewMockMetaRepository()
		name := "実験室"
		metaRepo.Meta = &model.Meta{ID: "x", Name: &name}
		h.metaRepo = metaRepo
		text := "hello"
		noteRepo.Notes["n1"] = &model.Note{
			ID: "n1", UserID: "u1", User: author(t, h), Text: &text,
			Visibility: model.NoteVisibilityPublic,
		}

		body := ssrGet(t, h.NotePage, "/notes/n1", map[string]string{"id": "n1"}).Body.String()

		assert.Contains(t, body, `<title>@alice | 実験室</title>`)
		// description もノートの要約になる (インスタンス説明ではない)
		assert.Contains(t, body, `<meta name="description" content="hello">`)
	})
}

// summary に含める reply / renote は、viewer なしで読めるものだけ。
// upstream は pack 済み note を渡すので非公開は (⛔) に落ちる。ここが緩むと
// permalink から非公開投稿の本文が読めてしまう。
func TestSSRNotePage_SummaryHidesNonPublicAncestors(t *testing.T) {
	h, userRepo, noteRepo := newSSRTestHandler(t)
	alice := ssrTestUser("u1", "alice")
	userRepo.Users["u1"] = alice

	secret := "secret followers only"
	noteRepo.Notes["hidden"] = &model.Note{
		ID: "hidden", UserID: "u1", User: alice, Text: &secret,
		Visibility: model.NoteVisibilityFollowers,
	}
	open := "public parent"
	noteRepo.Notes["open"] = &model.Note{
		ID: "open", UserID: "u1", User: alice, Text: &open,
		Visibility: model.NoteVisibilityPublic,
	}
	replyID := "hidden"
	renoteID := "open"
	text := "child"
	noteRepo.Notes["n1"] = &model.Note{
		ID: "n1", UserID: "u1", User: alice, Text: &text,
		ReplyID: &replyID, RenoteID: &renoteID,
		Visibility: model.NoteVisibilityPublic,
	}

	body := ssrGet(t, h.NotePage, "/notes/n1", map[string]string{"id": "n1"}).Body.String()

	assert.NotContains(t, body, secret)
	assert.Contains(t, body, "RE: (⛔)")
	assert.Contains(t, body, "RN: public parent")
}

// user / clip / flash / gallery / page の OGP が upstream の各 view と揃うこと (#2528)。
func TestSSRPages_OpenGraph(t *testing.T) {
	t.Run("user", func(t *testing.T) {
		h, userRepo, _ := newSSRTestHandler(t)
		name := "アリス"
		bio := "紹介文"
		alice := ssrTestUser("u1", "alice")
		alice.Name = &name
		userRepo.Users["u1"] = alice
		userRepo.Profiles["u1"] = &model.UserProfile{UserID: "u1", Description: &bio}

		body := ssrGet(t, h.UserPage, "/@alice", map[string]string{"acct": "alice"}).Body.String()

		assert.Contains(t, body, `<meta property="og:type" content="blog">`)
		assert.Contains(t, body, `<meta property="og:title" content="アリス (@alice)">`)
		assert.Contains(t, body, `<meta property="og:description" content="紹介文">`)
		assert.Contains(t, body, `<meta property="og:image" content="https://example.test/identicon/alice">`)
		assert.Contains(t, body, `<meta name="twitter:card" content="summary">`)
		// Layout の title は og:title と違って host を付けない
		assert.Contains(t, body, `<title>アリス (@alice) | Misskey</title>`)
		assert.Contains(t, body, `<meta name="description" content="紹介文">`)
	})

	t.Run("user は description が無ければ og:description を出さない", func(t *testing.T) {
		h, userRepo, _ := newSSRTestHandler(t)
		userRepo.Users["u1"] = ssrTestUser("u1", "alice")

		body := ssrGet(t, h.UserPage, "/@alice", map[string]string{"acct": "alice"}).Body.String()

		assert.NotContains(t, body, `property="og:description"`)
		// desc は `?? ''` で渡るので description タグ自体は出る (既定文言)
		assert.Contains(t, body, `<meta name="description" content="`+defaultInstanceDescription+`">`)
	})

	t.Run("clip", func(t *testing.T) {
		h, userRepo, _ := newSSRTestHandler(t)
		userRepo.Users["u1"] = ssrTestUser("u1", "alice")
		clipRepo := testutil.NewMockClipRepository()
		desc := "クリップの説明"
		clipRepo.Clips["c1"] = &model.Clip{ID: "c1", UserID: "u1", Name: "まとめ", IsPublic: true, Description: &desc}
		h.clipRepo = clipRepo

		body := ssrGet(t, h.ClipPage, "/clips/c1", map[string]string{"clip": "c1"}).Body.String()

		assert.Contains(t, body, `<meta property="og:title" content="まとめ">`)
		assert.Contains(t, body, `<meta property="og:description" content="クリップの説明">`)
		assert.Contains(t, body, `<meta property="og:url" content="https://example.test/clips/c1">`)
		assert.Contains(t, body, `<meta property="og:image" content="https://example.test/identicon/alice">`)
		assert.Contains(t, body, `<title>まとめ | Misskey</title>`)
	})

	t.Run("flash", func(t *testing.T) {
		h, userRepo, _ := newSSRTestHandler(t)
		userRepo.Users["u1"] = ssrTestUser("u1", "alice")
		flashRepo := testutil.NewMockFlashRepository()
		flashRepo.Flashes["f1"] = &model.Flash{ID: "f1", UserID: "u1", Title: "ゲーム", Summary: "遊べます"}
		h.flashRepo = flashRepo

		body := ssrGet(t, h.FlashPage, "/play/f1", map[string]string{"id": "f1"}).Body.String()

		assert.Contains(t, body, `<meta property="og:title" content="ゲーム">`)
		assert.Contains(t, body, `<meta property="og:description" content="遊べます">`)
		assert.Contains(t, body, `<meta property="og:url" content="https://example.test/play/f1">`)
		assert.Contains(t, body, `<title>ゲーム | Misskey</title>`)
	})
}

// gallery は sensitive かどうかで展開先に出す画像を変える (upstream
// gallery-post.tsx)。作品そのものを不用意に展開しない。
func TestSSRGalleryPage_SensitiveFallsBackToAvatar(t *testing.T) {
	newHandler := func(t *testing.T, sensitive bool) string {
		t.Helper()
		h, userRepo, _ := newSSRTestHandler(t)
		userRepo.Users["u1"] = ssrTestUser("u1", "alice")
		driveRepo := testutil.NewMockDriveFileRepository()
		thumb := "https://example.test/files/thumb.webp"
		driveRepo.Files["g1"] = &model.DriveFile{
			ID: "g1", Type: "image/png", URL: "https://example.test/files/g1.png", ThumbnailURL: &thumb,
		}
		h.driveFileRepo = driveRepo
		galleryRepo := testutil.NewMockGalleryRepository()
		galleryRepo.Posts["p1"] = &model.GalleryPost{
			ID: "p1", UserID: "u1", Title: "作品", FileIDs: []string{"g1"}, IsSensitive: sensitive,
		}
		h.galleryRepo = galleryRepo
		return ssrGet(t, h.GalleryPage, "/gallery/p1", map[string]string{"post": "p1"}).Body.String()
	}

	t.Run("通常は作品のサムネイルと summary_large_image", func(t *testing.T) {
		body := newHandler(t, false)
		assert.Contains(t, body, `<meta property="og:image" content="https://example.test/files/thumb.webp">`)
		assert.Contains(t, body, `<meta name="twitter:card" content="summary_large_image">`)
		assert.Contains(t, body, `<title>作品 | Misskey</title>`)
	})

	t.Run("sensitive なら avatar と summary", func(t *testing.T) {
		body := newHandler(t, true)
		assert.NotContains(t, body, "thumb.webp")
		assert.Contains(t, body, `<meta property="og:image" content="https://example.test/identicon/alice">`)
		assert.Contains(t, body, `<meta name="twitter:card" content="summary">`)
	})
}

// reversiSSRRepo は FindByID だけを差し替える test double。ReversiRepository は
// 11 メソッドあるが SSR が使うのは 1 つだけなので、埋め込みで型を満たす
// (未実装メソッドを呼ぶと nil panic するので、使われていないことも担保される)。
type reversiSSRRepo struct {
	repository.ReversiRepository
	games map[string]*model.ReversiGame
}

func (r *reversiSSRRepo) FindByID(id string) (*model.ReversiGame, error) {
	g, ok := r.games[id]
	if !ok {
		return nil, nil
	}
	return g, nil
}

// upstream views/channel.tsx の OGP (#2531)。
func TestSSRChannelPage(t *testing.T) {
	newHandler := func(t *testing.T, ch *model.Channel) *ssrMetaHandler {
		t.Helper()
		h, _, _ := newSSRTestHandler(t)
		chRepo := testutil.NewMockChannelRepository()
		if ch != nil {
			chRepo.Channels[ch.ID] = ch
		}
		h.channelRepo = chRepo
		return h
	}

	t.Run("banner があれば og:image に使う", func(t *testing.T) {
		desc := "チャンネルの説明"
		bannerID := "b1"
		h := newHandler(t, &model.Channel{ID: "ch1", Name: "雑談", Description: &desc, BannerID: &bannerID})
		driveRepo := testutil.NewMockDriveFileRepository()
		driveRepo.Files["b1"] = &model.DriveFile{ID: "b1", Type: "image/png", URL: "https://example.test/files/b1.png"}
		h.driveFileRepo = driveRepo

		body := ssrGet(t, h.ChannelPage, "/channels/ch1", map[string]string{"channel": "ch1"}).Body.String()

		assert.Contains(t, body, `<meta property="og:type" content="website">`)
		assert.Contains(t, body, `<meta property="og:title" content="雑談">`)
		assert.Contains(t, body, `<meta property="og:description" content="チャンネルの説明">`)
		assert.Contains(t, body, `<meta property="og:url" content="https://example.test/channels/ch1">`)
		assert.Contains(t, body, `<meta property="og:image" content="https://example.test/files/b1.png">`)
		assert.Contains(t, body, `<meta name="twitter:card" content="summary">`)
		assert.Contains(t, body, `<title>雑談 | Misskey</title>`)
	})

	t.Run("banner も description も無ければ出さない", func(t *testing.T) {
		h := newHandler(t, &model.Channel{ID: "ch1", Name: "雑談"})

		body := ssrGet(t, h.ChannelPage, "/channels/ch1", map[string]string{"channel": "ch1"}).Body.String()

		assert.NotContains(t, body, `property="og:description"`)
		assert.NotContains(t, body, `property="og:image"`)
		assert.NotContains(t, body, `name="twitter:card"`)
	})

	t.Run("存在しないチャンネルは素の shell", func(t *testing.T) {
		h := newHandler(t, nil)

		rec := ssrGet(t, h.ChannelPage, "/channels/ghost", map[string]string{"channel": "ghost"})

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, rec.Body.String(), `<meta property="og:type" content="website">`)
		assert.NotContains(t, rec.Body.String(), "/channels/ghost")
	})
}

// upstream views/reversi-game.tsx の OGP (#2531)。
func TestSSRReversiGamePage(t *testing.T) {
	h, userRepo, _ := newSSRTestHandler(t)
	userRepo.Users["u1"] = ssrTestUser("u1", "alice")
	userRepo.Users["u2"] = ssrTestUser("u2", "bob")
	h.reversiRepo = &reversiSSRRepo{games: map[string]*model.ReversiGame{
		"g1": {ID: "g1", User1ID: "u1", User2ID: "u2"},
	}}

	body := ssrGet(t, h.ReversiGamePage, "/reversi/g/g1", map[string]string{"game": "g1"}).Body.String()

	assert.Contains(t, body, `<meta property="og:type" content="article">`)
	assert.Contains(t, body, `<meta property="og:title" content="alice vs bob">`)
	assert.Contains(t, body, `<meta property="og:description" content="⚫⚪Misskey Reversi⚪⚫">`)
	assert.Contains(t, body, `<meta property="og:url" content="https://example.test/reversi/g/g1">`)
	assert.Contains(t, body, `<meta name="twitter:card" content="summary">`)
	assert.Contains(t, body, `<title>alice vs bob | Misskey</title>`)
}

// upstream views/announcement.tsx の OGP (#2531)。
func TestSSRAnnouncementPage(t *testing.T) {
	newHandler := func(t *testing.T, a *model.Announcement) *ssrMetaHandler {
		t.Helper()
		h, _, _ := newSSRTestHandler(t)
		repo := testutil.NewMockAnnouncementRepository()
		if a != nil {
			repo.Items[a.ID] = a
		}
		h.announceRepo = repo
		return h
	}

	t.Run("画像があれば summary_large_image", func(t *testing.T) {
		img := "https://example.test/files/a1.png"
		h := newHandler(t, &model.Announcement{ID: "a1", Title: "メンテナンス", Text: "明日実施します", ImageURL: &img})

		body := ssrGet(t, h.AnnouncementPage, "/announcements/a1", map[string]string{"id": "a1"}).Body.String()

		assert.Contains(t, body, `<meta property="og:title" content="メンテナンス">`)
		assert.Contains(t, body, `<meta property="og:description" content="明日実施します">`)
		assert.Contains(t, body, `<meta property="og:url" content="https://example.test/announcements/a1">`)
		assert.Contains(t, body, `<meta property="og:image" content="`+img+`">`)
		assert.Contains(t, body, `<meta name="twitter:card" content="summary_large_image">`)
		assert.Contains(t, body, `<title>メンテナンス | Misskey</title>`)
	})

	// 個人宛てのお知らせは permalink で配らない。URL を知っているだけで
	// 他人宛ての内容が読めてはいけない (upstream も userId IS NULL で絞る)。
	t.Run("個人宛ては meta を出さない", func(t *testing.T) {
		uid := "u1"
		secret := "あなた宛ての警告です"
		h := newHandler(t, &model.Announcement{ID: "a1", Title: "個人宛て", Text: secret, UserID: &uid})

		rec := ssrGet(t, h.AnnouncementPage, "/announcements/a1", map[string]string{"id": "a1"})

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.NotContains(t, rec.Body.String(), secret)
		assert.NotContains(t, rec.Body.String(), "個人宛て")
	})
}

// 本文の切り詰めは rune 単位。byte で切ると日本語の途中で壊れた UTF-8 が
// meta に載る。
func TestAnnouncementSummary(t *testing.T) {
	assert.Equal(t, "短い本文", announcementSummary("短い本文"))

	exact := strings.Repeat("あ", 100)
	assert.Equal(t, exact, announcementSummary(exact))

	long := strings.Repeat("あ", 101)
	got := announcementSummary(long)
	assert.Equal(t, strings.Repeat("あ", 100)+"…", got)
	assert.True(t, utf8.ValidString(got))
}

// upstream が `{ noindex: true }` で描いているページ (#2531)。
func TestSSRNoIndexPage(t *testing.T) {
	h, _, _ := newSSRTestHandler(t)

	rec := ssrGet(t, h.NoIndexPage, "/tags/misskey", map[string]string{"tag": "misskey"})

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `<meta name="robots" content="noindex">`)
	// 中身は SPA が描くので shell の既定 OGP はそのまま
	assert.Contains(t, rec.Body.String(), `<meta property="og:type" content="website">`)
}
