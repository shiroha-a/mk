package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/entity"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubFeedUsers struct{ users map[string]*model.User }

func (s stubFeedUsers) FindLocalByUsername(username string) (*model.User, error) {
	u, ok := s.users[username]
	if !ok {
		// **repository の sentinel を返す。** `echo.ErrNotFound` は HTTP の
		// error で、`repository.IsNotFound` では判定できない (#2792)。
		return nil, repository.ErrNotFound
	}
	return u, nil
}

type stubFeedNotes struct{ notes []*model.Note }

func (s stubFeedNotes) ListPublicNotesForFeed(_ string, limit int) ([]*model.Note, error) {
	if len(s.notes) > limit {
		return s.notes[:limit], nil
	}
	return s.notes, nil
}

func strp(s string) *string { return &s }

func newFeedTestHandler(notes []*model.Note) *feedHandler {
	name := "Alice"
	return &feedHandler{
		baseURL: "https://example.test",
		host:    "example.test",
		users: stubFeedUsers{users: map[string]*model.User{
			"alice": {ID: "u1", Username: "alice", Name: &name, NotesCount: 5, FollowingCount: 2, FollowersCount: 3},
		}},
		notes:     stubFeedNotes{notes: notes},
		parseTime: func(string) (time.Time, error) { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), nil },
		profiles: func(string) *model.UserProfile {
			return &model.UserProfile{FollowingVisibility: "public", FollowersVisibility: "public"}
		},
		avatarURL: func(*model.User) string { return "https://example.test/avatar.png" },
		toHTML:    func(text string) string { return "<p>" + text + "</p>" },
	}
}

func doFeedReq(t *testing.T, h func(echo.Context, string) error, user string) *httptest.ResponseRecorder {
	t.Helper()
	e := echo.New()
	rec := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest(http.MethodGet, "/", nil), rec)
	if err := h(c, user); err != nil {
		var he *echo.HTTPError
		if ok := asHTTPError(err, &he); ok {
			rec.Code = he.Code
			return rec
		}
		t.Fatal(err)
	}
	return rec
}

func asHTTPError(err error, out **echo.HTTPError) bool {
	he, ok := err.(*echo.HTTPError)
	if ok {
		*out = he
	}
	return ok
}

func sampleFeedNotes() []*model.Note {
	return []*model.Note{
		{ID: "n1", UserID: "u1", Text: strp("hello"), Visibility: model.NoteVisibilityPublic},
	}
}

func TestFeed_RSS(t *testing.T) {
	h := newFeedTestHandler(sampleFeedNotes())
	rec := doFeedReq(t, h.RSS, "alice")

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/rss+xml; charset=utf-8", rec.Header().Get(echo.HeaderContentType))
	body := rec.Body.String()
	assert.Contains(t, body, "<rss version=\"2.0\">")
	assert.Contains(t, body, "<title>Alice (@alice@example.test)</title>")
	assert.Contains(t, body, "https://example.test/notes/n1")
	assert.Contains(t, body, "5 Notes, 2 Following, 3 Followers")
}

func TestFeed_Atom(t *testing.T) {
	h := newFeedTestHandler(sampleFeedNotes())
	rec := doFeedReq(t, h.Atom, "alice")

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/atom+xml; charset=utf-8", rec.Header().Get(echo.HeaderContentType))
	body := rec.Body.String()
	assert.Contains(t, body, "http://www.w3.org/2005/Atom")
	assert.Contains(t, body, "<id>https://example.test/@alice</id>")
	assert.Contains(t, body, "2026-01-02T03:04:05Z")
}

func TestFeed_JSON(t *testing.T) {
	h := newFeedTestHandler(sampleFeedNotes())
	rec := doFeedReq(t, h.JSON, "alice")

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/json; charset=utf-8", rec.Header().Get(echo.HeaderContentType))

	var doc map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &doc))
	assert.Equal(t, "https://jsonfeed.org/version/1.1", doc["version"])
	assert.Equal(t, "https://example.test/@alice", doc["home_page_url"])
	items, _ := doc["items"].([]any)
	require.Len(t, items, 1)
	item := items[0].(map[string]any)
	assert.Equal(t, "https://example.test/notes/n1", item["url"])
	assert.Equal(t, "<p>hello</p>", item["content_html"])
}

// 存在しないユーザーは 404。upstream も同じ。
func TestFeed_UnknownUserIs404(t *testing.T) {
	h := newFeedTestHandler(nil)
	for _, fn := range []func(echo.Context, string) error{h.RSS, h.Atom, h.JSON} {
		rec := doFeedReq(t, fn, "nobody")
		assert.Equal(t, http.StatusNotFound, rec.Code)
	}
}

// ノートが 1 件も無くても 200 で空のフィードを返す (upstream 同様)。
func TestFeed_EmptyNotesStillServes(t *testing.T) {
	h := newFeedTestHandler(nil)
	rec := doFeedReq(t, h.RSS, "alice")
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "<rss version=\"2.0\">")
}

// フォロー数の可視性が public でなければ数を伏せる。
func TestFeed_HidesCountsWhenNotPublic(t *testing.T) {
	h := newFeedTestHandler(sampleFeedNotes())
	h.profiles = func(string) *model.UserProfile {
		return &model.UserProfile{FollowingVisibility: "private", FollowersVisibility: "followers"}
	}
	rec := doFeedReq(t, h.RSS, "alice")
	assert.Contains(t, rec.Body.String(), "5 Notes, ? Following, ? Followers")
}

// CW 付きノートは summary に出す。
func TestFeed_CWBecomesSummary(t *testing.T) {
	notes := sampleFeedNotes()
	notes[0].CW = strp("注意")
	h := newFeedTestHandler(notes)
	rec := doFeedReq(t, h.Atom, "alice")
	assert.Contains(t, rec.Body.String(), "<summary>注意</summary>")
}

// Echo のルータはセグメント内リテラルを解釈できないので、/@<acct> で受けて
// 拡張子で振り分ける。拡張子が無ければフィードとして扱わない。
func TestFeed_TryServe(t *testing.T) {
	h := newFeedTestHandler(sampleFeedNotes())
	e := echo.New()

	for _, tc := range []struct {
		acct    string
		handled bool
		ctype   string
	}{
		{"alice.rss", true, "application/rss+xml; charset=utf-8"},
		{"alice.atom", true, "application/atom+xml; charset=utf-8"},
		{"alice.json", true, "application/json; charset=utf-8"},
		{"alice", false, ""},
		{"alice.png", false, ""},
	} {
		rec := httptest.NewRecorder()
		c := e.NewContext(httptest.NewRequest(http.MethodGet, "/", nil), rec)
		handled, err := h.TryServe(c, tc.acct)
		require.NoError(t, err, tc.acct)
		assert.Equal(t, tc.handled, handled, tc.acct)
		if tc.handled {
			assert.Equal(t, tc.ctype, rec.Header().Get(echo.HeaderContentType), tc.acct)
		}
	}
}

// 凍結済みの利用者はフィードを配らない。upstream getFeed は
// `isSuspended: false` を lookup 条件に入れており、外れると 404 になる。
func TestFeed_SuspendedUserIs404(t *testing.T) {
	h := newFeedTestHandler(sampleFeedNotes())
	h.users.(stubFeedUsers).users["alice"].IsSuspended = true
	for _, fn := range []func(echo.Context, string) error{h.RSS, h.Atom, h.JSON} {
		rec := doFeedReq(t, fn, "alice")
		assert.Equal(t, http.StatusNotFound, rec.Code)
		assert.NotContains(t, rec.Body.String(), "hello")
	}
}

// 「ログインしていないユーザーにコンテンツを見せない」設定の利用者も同様に
// 404。フィードは常に未認証で読めるので、ここが唯一のゲートになる。
func TestFeed_RequireSigninToViewContentsIs404(t *testing.T) {
	h := newFeedTestHandler(sampleFeedNotes())
	h.users.(stubFeedUsers).users["alice"].RequireSigninToViewContents = true
	for _, fn := range []func(echo.Context, string) error{h.RSS, h.Atom, h.JSON} {
		rec := doFeedReq(t, fn, "alice")
		assert.Equal(t, http.StatusNotFound, rec.Code)
		// 本文 (MFM→HTML 変換済み) も著者名も 1 文字も出さない。
		body := rec.Body.String()
		assert.NotContains(t, body, "hello")
		assert.NotContains(t, body, "Alice")
	}
}

// TryServe 経由 (= 実際のルーティング経路) でも同じゲートが効く。
// serve() を直接叩くテストだけだと、振り分け側にバイパスがあっても気付けない。
func TestFeed_TryServeAppliesVisibilityGate(t *testing.T) {
	e := echo.New()
	for _, tc := range []struct {
		name  string
		apply func(*model.User)
	}{
		{"suspended", func(u *model.User) { u.IsSuspended = true }},
		{"requireSignin", func(u *model.User) { u.RequireSigninToViewContents = true }},
	} {
		for _, acct := range []string{"alice.rss", "alice.atom", "alice.json"} {
			h := newFeedTestHandler(sampleFeedNotes())
			tc.apply(h.users.(stubFeedUsers).users["alice"])
			rec := httptest.NewRecorder()
			c := e.NewContext(httptest.NewRequest(http.MethodGet, "/", nil), rec)
			handled, err := h.TryServe(c, acct)
			assert.True(t, handled, "%s/%s", tc.name, acct)
			var he *echo.HTTPError
			require.True(t, asHTTPError(err, &he), "%s/%s: expected an echo.HTTPError", tc.name, acct)
			assert.Equal(t, http.StatusNotFound, he.Code, "%s/%s", tc.name, acct)
			assert.NotContains(t, rec.Body.String(), "hello", "%s/%s", tc.name, acct)
		}
	}
}

// ゲートを足しても通常の利用者は 200 のまま (= ゲートが広すぎないこと)。
func TestFeed_NormalUserStillServed(t *testing.T) {
	h := newFeedTestHandler(sampleFeedNotes())
	for _, fn := range []func(echo.Context, string) error{h.RSS, h.Atom, h.JSON} {
		rec := doFeedReq(t, fn, "alice")
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, rec.Body.String(), "hello")
	}
}

// avatar 設定済みのユーザーでも media proxy 経由にする (#1529)。フィードは
// 未認証で取得でき、購読者 (第三者クライアント) が remote origin を直接
// 取得してしまう。identicon fallback (= avatar 未設定) は entity.IdenticonURL が
// 相対 URL を返すので、こちらは proxy を通さない。
//
// **`u.AvatarURL` の分岐そのものは本番で到達しない。** feedHandler は
// `FindLocalByUsername` (host=nil) でしか user を引かないので、`u.AvatarURL`
// は常に drive 経由の local URL であり、この分岐が生の remote URL を受け取る
// ことは無い (#3130 review — 最初のケースの `Host: strp("remote.example")` は
// local では作れない値で、ヘルパー自体の検証にはなるが「フィード経由で
// 漏れていた」の裏付けにはならない)。**実際に到達するのはローカル system
// account の `meta.iconUrl` 経由** — `entity.IdenticonURL` が
// username に `.` を含む local user を system account とみなし、
// `meta.iconUrl` (operator が remote URL を設定できる) を解決するため、
// AvatarURL 未設定の system account の feed を購読すると本番でもこの経路を
// 通る (下の subtest で確認)。
func TestFeedAvatarURLProxiesRemoteAvatar(t *testing.T) {
	withMediaProxy(t)

	remote := "https://remote.example/a.png"
	u := &model.User{ID: "u1", Username: "alice", Host: strp("remote.example"), AvatarURL: &remote}
	got := feedAvatarURL(u)
	assert.True(t, strings.HasPrefix(got, "https://local.example/proxy/avatar.webp?"), "got %q", got)
	parsed, err := url.Parse(got)
	require.NoError(t, err)
	assert.Equal(t, "local.example", parsed.Host, "proxy 自身のホスト以外を向いている")

	t.Run("identicon fallback は相対 URL のまま", func(t *testing.T) {
		local := &model.User{ID: "u2", Username: "bob"}
		assert.Equal(t, "/identicon/bob", feedAvatarURL(local))
	})

	t.Run("自オリジンは no-op", func(t *testing.T) {
		same := &model.User{ID: "u3", Username: "carol", AvatarURL: strp("https://local.example/files/a.png")}
		assert.Equal(t, "https://local.example/files/a.png", feedAvatarURL(same))
	})

	// **本番で実際に到達する remote 経路。** local system account
	// (username に '.' を含む、AvatarURL 未設定) は identicon fallback
	// (entity.IdenticonURL) が meta.iconUrl を解決する。ここが remote を
	// 指していれば、feed 経由で購読者に生 URL が渡っていた (#1529)。
	t.Run("local system account の meta.iconUrl (remote) も proxy 経由", func(t *testing.T) {
		entity.SetInstanceIconURLLookup(func() string { return "https://remote.example/icon.png" })
		t.Cleanup(func() { entity.SetInstanceIconURLLookup(nil) })

		sysAccount := &model.User{ID: "u4", Username: "relay.actor"} // local, no avatar
		got := feedAvatarURL(sysAccount)
		assert.True(t, strings.HasPrefix(got, "https://local.example/proxy/avatar.webp?"), "got %q", got)
	})

	// build → 各 format の render まで通ること (配線の実体)。
	t.Run("rendered feed uses the proxied url", func(t *testing.T) {
		n := 0
		h := &feedHandler{
			baseURL: "https://local.example",
			host:    "local.example",
			users:   stubFeedUsers{users: map[string]*model.User{"alice": u}},
			notes:   stubFeedNotes{},
			parseTime: func(string) (time.Time, error) {
				return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), nil
			},
			profiles:  func(string) *model.UserProfile { return nil },
			avatarURL: feedAvatarURL,
			toHTML:    func(text string) string { return text },
		}
		for _, fn := range []func(echo.Context, string) error{h.RSS, h.Atom, h.JSON} {
			rec := doFeedReq(t, fn, "alice")
			require.Equal(t, http.StatusOK, rec.Code)
			assert.Contains(t, rec.Body.String(), "https://local.example/proxy/avatar.webp?",
				"フィード本文に raw avatar が載っている")
			// ssr_meta_test.go の対応する subtest (`TestSSRAnnouncementPage` の
			// 「リモート画像は proxy 経由」) と同じく、生の remote URL が本文の
			// どこにも残っていないことも見る (#3130 review)。
			assert.NotContains(t, rec.Body.String(), remote,
				"フィード本文に生のリモート avatar URL が残っている")
			n++
		}
		require.Equal(t, 3, n)
	})
}
