package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubFeedUsers struct{ users map[string]*model.User }

func (s stubFeedUsers) FindLocalByUsername(username string) (*model.User, error) {
	u, ok := s.users[username]
	if !ok {
		return nil, echo.ErrNotFound
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

func doFeedReq(t *testing.T, h func(echo.Context) error, user string) *httptest.ResponseRecorder {
	t.Helper()
	e := echo.New()
	rec := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest(http.MethodGet, "/", nil), rec)
	c.SetParamNames("user")
	c.SetParamValues(user)
	if err := h(c); err != nil {
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
	for _, fn := range []func(echo.Context) error{h.RSS, h.Atom, h.JSON} {
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
