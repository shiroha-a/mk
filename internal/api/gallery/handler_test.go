package gallery_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/api/gallery"
	"github.com/shiroha-a/mk/internal/entitycompat/shapetest"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/server/middleware"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

var testDB *gorm.DB
var testIDGen id.Generator

func init() {
	testDB = testutil.MustOpenTestDB()
	testIDGen, _ = id.NewGenerator("aidx")
	testutil.ApplyMigrations(testDB)
	testDB.Exec(`INSERT INTO "user" (id, username, "usernameLower", "avatarDecorations") VALUES ('gal_u1', 'galuser', 'galuser', '[]') ON CONFLICT DO NOTHING`)
}

func newHandler() *gallery.Handler {
	return gallery.NewHandler(testDB, testIDGen)
}

func brokenHandler() *gallery.Handler {
	db := testutil.MustOpenTestDB()
	sqlDB, _ := db.DB()
	_ = sqlDB.Close()
	return gallery.NewHandler(db, testIDGen)
}

func cleanup() {
	testDB.Exec(`DELETE FROM "gallery_like"`)
	testDB.Exec(`DELETE FROM "gallery_post"`)
}

func doPost(h func(echo.Context) error, body string, user *model.User) *httptest.ResponseRecorder {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	if user != nil {
		c.Set(string(middleware.UserContextKey), user)
	}
	_ = h(c)
	return rec
}

// --- Featured / Popular / Posts ---

func TestFeatured_Empty(t *testing.T) {
	cleanup()
	assert.Equal(t, http.StatusOK, doPost(newHandler().Featured, `{}`, nil).Code)
}

func TestFeatured_WithData(t *testing.T) {
	cleanup()
	testDB.Create(&model.GalleryPost{ID: "gp_f1", UpdatedAt: time.Now(), Title: "Art", UserID: "gal_u1", FileIDs: []string{}, Tags: []string{}})
	defer cleanup()
	rec := doPost(newHandler().Featured, `{}`, nil)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "Art")
}

func TestPopular(t *testing.T) {
	cleanup()
	assert.Equal(t, http.StatusOK, doPost(newHandler().Popular, `{}`, nil).Code)
}

func TestPosts_Success(t *testing.T) {
	cleanup()
	assert.Equal(t, http.StatusOK, doPost(newHandler().Posts, `{}`, nil).Code)
}

func TestPosts_WithOffset(t *testing.T) {
	assert.Equal(t, http.StatusOK, doPost(newHandler().Posts, `{"limit":5,"offset":1}`, nil).Code)
}

func TestPosts_InvalidJSON(t *testing.T) {
	assert.Equal(t, http.StatusBadRequest, doPost(newHandler().Posts, `invalid`, nil).Code)
}

func TestFeatured_DBError(t *testing.T) {
	assert.Equal(t, http.StatusInternalServerError, doPost(brokenHandler().Featured, `{}`, nil).Code)
}

func TestPosts_DBError(t *testing.T) {
	assert.Equal(t, http.StatusInternalServerError, doPost(brokenHandler().Posts, `{}`, nil).Code)
}

// --- Create ---

func TestPostsCreate_Success(t *testing.T) {
	cleanup()
	user := &model.User{ID: "gal_u1", Username: "galuser"}
	rec := doPost(newHandler().PostsCreate, `{"title":"My Art"}`, user)
	assert.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "My Art", resp["title"])
	shapetest.Assert(t, "GalleryPost", resp) // L3 (#1270)
	cleanup()
}

func TestPostsCreate_InvalidParam(t *testing.T) {
	assert.Equal(t, http.StatusBadRequest, doPost(newHandler().PostsCreate, `{}`, &model.User{ID: "u1"}).Code)
}

func TestPostsCreate_DBError(t *testing.T) {
	assert.Equal(t, http.StatusInternalServerError, doPost(brokenHandler().PostsCreate, `{"title":"x"}`, &model.User{ID: "u1"}).Code)
}

// --- Show ---

func TestPostsShow_Found(t *testing.T) {
	cleanup()
	testDB.Create(&model.GalleryPost{ID: "gp_s1", UpdatedAt: time.Now(), Title: "Show", UserID: "gal_u1", FileIDs: []string{}, Tags: []string{}})
	defer cleanup()
	assert.Equal(t, http.StatusOK, doPost(newHandler().PostsShow, `{"postId":"gp_s1"}`, nil).Code)
}

func TestPostsShow_NotFound(t *testing.T) {
	assert.Equal(t, http.StatusNotFound, doPost(newHandler().PostsShow, `{"postId":"ghost"}`, nil).Code)
}

func TestPostsShow_InvalidParam(t *testing.T) {
	assert.Equal(t, http.StatusBadRequest, doPost(newHandler().PostsShow, `{}`, nil).Code)
}

// --- Delete ---

func TestPostsDelete_Success(t *testing.T) {
	cleanup()
	testDB.Create(&model.GalleryPost{ID: "gp_d1", UpdatedAt: time.Now(), Title: "Del", UserID: "gal_u1", FileIDs: []string{}, Tags: []string{}})
	assert.Equal(t, http.StatusNoContent, doPost(newHandler().PostsDelete, `{"postId":"gp_d1"}`, &model.User{ID: "gal_u1"}).Code)
}

func TestPostsDelete_NotFound(t *testing.T) {
	assert.Equal(t, http.StatusNotFound, doPost(newHandler().PostsDelete, `{"postId":"ghost"}`, &model.User{ID: "u1"}).Code)
}

func TestPostsDelete_InvalidParam(t *testing.T) {
	assert.Equal(t, http.StatusBadRequest, doPost(newHandler().PostsDelete, `{}`, &model.User{ID: "u1"}).Code)
}

// --- Update ---

func TestPostsUpdate_Success(t *testing.T) {
	cleanup()
	testDB.Create(&model.GalleryPost{ID: "gp_u1", UpdatedAt: time.Now(), Title: "Old", UserID: "gal_u1", FileIDs: []string{}, Tags: []string{}})
	defer cleanup()
	assert.Equal(t, http.StatusNoContent, doPost(newHandler().PostsUpdate, `{"postId":"gp_u1","title":"New"}`, &model.User{ID: "gal_u1"}).Code)
}

func TestPostsUpdate_WithDescription(t *testing.T) {
	cleanup()
	testDB.Create(&model.GalleryPost{ID: "gp_ud1", UpdatedAt: time.Now(), Title: "Old", UserID: "gal_u1", FileIDs: []string{}, Tags: []string{}})
	defer cleanup()
	assert.Equal(t, http.StatusNoContent, doPost(newHandler().PostsUpdate, `{"postId":"gp_ud1","title":"New","description":"desc"}`, &model.User{ID: "gal_u1"}).Code)
}

func TestPostsUpdate_NotFound(t *testing.T) {
	assert.Equal(t, http.StatusNotFound, doPost(newHandler().PostsUpdate, `{"postId":"ghost","title":"x"}`, &model.User{ID: "u1"}).Code)
}

func TestPostsUpdate_InvalidParam(t *testing.T) {
	assert.Equal(t, http.StatusBadRequest, doPost(newHandler().PostsUpdate, `{}`, &model.User{ID: "u1"}).Code)
}

// --- Like / Unlike ---

func TestPostsLike_Success(t *testing.T) {
	cleanup()
	pid := testIDGen.Generate(time.Now())
	testDB.Create(&model.GalleryPost{ID: pid, UpdatedAt: time.Now(), Title: "Likeable", UserID: "gal_u1", FileIDs: []string{}, Tags: []string{}})
	defer cleanup()
	assert.Equal(t, http.StatusNoContent, doPost(newHandler().PostsLike, `{"postId":"`+pid+`"}`, &model.User{ID: "gal_u1"}).Code)
}

func TestPostsLike_AlreadyLiked(t *testing.T) {
	cleanup()
	pid := testIDGen.Generate(time.Now())
	testDB.Create(&model.GalleryPost{ID: pid, UpdatedAt: time.Now(), Title: "Liked", UserID: "gal_u1", FileIDs: []string{}, Tags: []string{}})
	defer cleanup()
	h := newHandler()
	doPost(h.PostsLike, `{"postId":"`+pid+`"}`, &model.User{ID: "gal_u1"})
	assert.Equal(t, http.StatusConflict, doPost(h.PostsLike, `{"postId":"`+pid+`"}`, &model.User{ID: "gal_u1"}).Code)
}

func TestPostsLike_InvalidParam(t *testing.T) {
	assert.Equal(t, http.StatusBadRequest, doPost(newHandler().PostsLike, `{}`, &model.User{ID: "u1"}).Code)
}

func TestPostsUnlike_Success(t *testing.T) {
	assert.Equal(t, http.StatusNoContent, doPost(newHandler().PostsUnlike, `{"postId":"p1"}`, &model.User{ID: "u1"}).Code)
}

func TestPostsUnlike_InvalidParam(t *testing.T) {
	assert.Equal(t, http.StatusBadRequest, doPost(newHandler().PostsUnlike, `{}`, &model.User{ID: "u1"}).Code)
}
