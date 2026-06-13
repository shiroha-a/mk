package users

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/server/middleware"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
)

// modByID treats a single user ID as moderator (test stub for the
// ModeratorChecker interface, scoped to package users).
type modByID struct{ id string }

func (m modByID) IsModerator(userID string) bool     { return userID == m.id }
func (m modByID) IsAdministrator(userID string) bool { return userID == m.id }

// showWithViewer invokes users/show for target with an optional viewer.
func showWithViewer(t *testing.T, h *Handler, targetID string, viewer *model.User) map[string]any {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/users/show", strings.NewReader(`{"userId":"`+targetID+`"}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	if viewer != nil {
		c.Set(string(middleware.UserContextKey), viewer)
	}
	require.NoError(t, h.Show(c))
	require.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	return resp
}

func newTargetWithProfile(repo *testutil.MockUserRepository, id string, profile *model.UserProfile) *model.User {
	u := &model.User{ID: id, Username: id, UsernameLower: id, AvatarDecorations: datatypes.JSON([]byte("[]"))}
	repo.Users[id] = u
	profile.UserID = id
	if profile.Fields == nil {
		profile.Fields = datatypes.JSON([]byte("[]"))
	}
	repo.Profiles[id] = profile
	return u
}

// #1558 [MEDIUM] followedMessage は follower / self だけに見せる。
func TestShow_FollowedMessage_FollowerOnly(t *testing.T) {
	msg := "Thanks for following!"

	t.Run("non-follower omits", func(t *testing.T) {
		h, repo := newTestHandler(t)
		newTargetWithProfile(repo, "target1", &model.UserProfile{FollowedMessage: &msg})
		resp := showWithViewer(t, h, "target1", &model.User{ID: "viewer1", Username: "viewer"})
		_, has := resp["followedMessage"]
		assert.False(t, has, "非 follower には followedMessage を出さない")
	})

	t.Run("anonymous omits", func(t *testing.T) {
		h, repo := newTestHandler(t)
		newTargetWithProfile(repo, "target1", &model.UserProfile{FollowedMessage: &msg})
		resp := showWithViewer(t, h, "target1", nil)
		_, has := resp["followedMessage"]
		assert.False(t, has, "anonymous には followedMessage を出さない")
	})

	t.Run("follower sees it", func(t *testing.T) {
		h, repo := newTestHandler(t)
		newTargetWithProfile(repo, "target1", &model.UserProfile{FollowedMessage: &msg})
		fRepo := testutil.NewMockFollowingRepository()
		fRepo.Followings["f1"] = &model.Following{ID: "f1", FollowerID: "viewer1", FolloweeID: "target1"}
		h.SetFollowingRepo(fRepo)
		resp := showWithViewer(t, h, "target1", &model.User{ID: "viewer1", Username: "viewer"})
		assert.Equal(t, msg, resp["followedMessage"], "follower には followedMessage を出す")
	})

	t.Run("self sees it (MeDetailed always present)", func(t *testing.T) {
		h, repo := newTestHandler(t)
		newTargetWithProfile(repo, "self1", &model.UserProfile{FollowedMessage: &msg})
		resp := showWithViewer(t, h, "self1", &model.User{ID: "self1", Username: "self1"})
		assert.Equal(t, msg, resp["followedMessage"])
	})
}

// #1558 [MEDIUM] followersCount / followingCount は restricted visibility で 0 化。
func TestShow_CountVisibility(t *testing.T) {
	mkProfile := func(fv, gv model.FollowingVisibility) *model.UserProfile {
		return &model.UserProfile{FollowersVisibility: fv, FollowingVisibility: gv}
	}
	withCounts := func(repo *testutil.MockUserRepository, id string) {
		repo.Users[id].FollowersCount = 7
		repo.Users[id].FollowingCount = 9
	}

	t.Run("followers visibility hidden from non-follower", func(t *testing.T) {
		h, repo := newTestHandler(t)
		newTargetWithProfile(repo, "target1", mkProfile(model.FollowingVisibilityFollowers, model.FollowingVisibilityFollowers))
		withCounts(repo, "target1")
		resp := showWithViewer(t, h, "target1", &model.User{ID: "viewer1", Username: "viewer"})
		assert.EqualValues(t, 0, resp["followersCount"])
		assert.EqualValues(t, 0, resp["followingCount"])
	})

	t.Run("followers visibility shown to follower", func(t *testing.T) {
		h, repo := newTestHandler(t)
		newTargetWithProfile(repo, "target1", mkProfile(model.FollowingVisibilityFollowers, model.FollowingVisibilityFollowers))
		withCounts(repo, "target1")
		fRepo := testutil.NewMockFollowingRepository()
		fRepo.Followings["f1"] = &model.Following{ID: "f1", FollowerID: "viewer1", FolloweeID: "target1"}
		h.SetFollowingRepo(fRepo)
		resp := showWithViewer(t, h, "target1", &model.User{ID: "viewer1", Username: "viewer"})
		assert.EqualValues(t, 7, resp["followersCount"])
		assert.EqualValues(t, 9, resp["followingCount"])
	})

	t.Run("private hidden from anonymous", func(t *testing.T) {
		h, repo := newTestHandler(t)
		newTargetWithProfile(repo, "target1", mkProfile(model.FollowingVisibilityPrivate, model.FollowingVisibilityPrivate))
		withCounts(repo, "target1")
		resp := showWithViewer(t, h, "target1", nil)
		assert.EqualValues(t, 0, resp["followersCount"])
		assert.EqualValues(t, 0, resp["followingCount"])
	})

	t.Run("private shown to moderator", func(t *testing.T) {
		h, repo := newTestHandler(t)
		newTargetWithProfile(repo, "target1", mkProfile(model.FollowingVisibilityPrivate, model.FollowingVisibilityPrivate))
		withCounts(repo, "target1")
		h.SetModeratorChecker(modByID{id: "mod1"})
		resp := showWithViewer(t, h, "target1", &model.User{ID: "mod1", Username: "mod"})
		assert.EqualValues(t, 7, resp["followersCount"])
		assert.EqualValues(t, 9, resp["followingCount"])
	})

	t.Run("public always shown", func(t *testing.T) {
		h, repo := newTestHandler(t)
		newTargetWithProfile(repo, "target1", mkProfile(model.FollowingVisibilityPublic, model.FollowingVisibilityPublic))
		withCounts(repo, "target1")
		resp := showWithViewer(t, h, "target1", nil)
		assert.EqualValues(t, 7, resp["followersCount"])
		assert.EqualValues(t, 9, resp["followingCount"])
	})

	t.Run("self always shown", func(t *testing.T) {
		h, repo := newTestHandler(t)
		newTargetWithProfile(repo, "self1", mkProfile(model.FollowingVisibilityPrivate, model.FollowingVisibilityPrivate))
		withCounts(repo, "self1")
		resp := showWithViewer(t, h, "self1", &model.User{ID: "self1", Username: "self1"})
		assert.EqualValues(t, 7, resp["followersCount"])
		assert.EqualValues(t, 9, resp["followingCount"])
	})
}

// #1558 [MEDIUM 2-gap] users/search でも moderator viewer に moderationNote を出す。
func TestSearch_ModerationNoteForModerator(t *testing.T) {
	h, repo := newTestHandler(t)
	note := "watch this user"
	newTargetWithProfile(repo, "suspect", &model.UserProfile{
		ModerationNote:      &note,
		FollowersVisibility: model.FollowingVisibilityPublic,
		FollowingVisibility: model.FollowingVisibilityPublic,
	})
	h.SetModeratorChecker(modByID{id: "mod1"})

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/users/search", strings.NewReader(`{"query":"suspect"}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set(string(middleware.UserContextKey), &model.User{ID: "mod1", Username: "mod"})
	require.NoError(t, h.Search(c))
	require.Equal(t, http.StatusOK, rec.Code)

	var resp []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.NotEmpty(t, resp)
	assert.Equal(t, note, resp[0]["moderationNote"], "moderator の search 結果に moderationNote を出す")
}

// 非 moderator の search では moderationNote を出さない。
func TestSearch_NoModerationNoteForNonModerator(t *testing.T) {
	h, repo := newTestHandler(t)
	note := "watch this user"
	newTargetWithProfile(repo, "suspect", &model.UserProfile{
		ModerationNote:      &note,
		FollowersVisibility: model.FollowingVisibilityPublic,
		FollowingVisibility: model.FollowingVisibilityPublic,
	})

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/users/search", strings.NewReader(`{"query":"suspect"}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set(string(middleware.UserContextKey), &model.User{ID: "viewer1", Username: "viewer"})
	require.NoError(t, h.Search(c))

	var resp []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.NotEmpty(t, resp)
	_, has := resp[0]["moderationNote"]
	assert.False(t, has, "非 moderator には moderationNote を出さない")
}
