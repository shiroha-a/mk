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
	testDB.Exec(`DELETE FROM "drive_file" WHERE id LIKE 'galf_%'`)
}

// seedDriveFile inserts a minimal drive file owned by ownerID for gallery tests.
func seedDriveFile(id, ownerID string) {
	uid := ownerID
	testDB.Create(&model.DriveFile{
		ID: id, UserID: &uid, MD5: "x", Name: id + ".png", Type: "image/png",
		Size: 1, StoredInternal: true, URL: "https://example/" + id + ".png",
	})
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

// list (Posts) が複数 post の files を batch 解決し (順序保持)、各 post の
// isLiked を viewer ごとに正しく埋める (liked=true / not-liked=false)。
func TestPosts_BatchFilesAndPerPostIsLiked(t *testing.T) {
	cleanup()
	defer cleanup()
	seedDriveFile("galf_a1", "gal_u1")
	seedDriveFile("galf_a2", "gal_u1")
	seedDriveFile("galf_b1", "gal_u1")
	// postA: files [a1,a2]、liked。postB: [b1]、not-liked。
	testDB.Create(&model.GalleryPost{ID: "gp_a", UpdatedAt: time.Now(), Title: "A", UserID: "gal_u1", FileIDs: []string{"galf_a1", "galf_a2"}, Tags: []string{}})
	testDB.Create(&model.GalleryPost{ID: "gp_b", UpdatedAt: time.Now(), Title: "B", UserID: "gal_u1", FileIDs: []string{"galf_b1"}, Tags: []string{}})
	testDB.Create(&model.GalleryLike{ID: "gl_a", UserID: "gal_u1", PostID: "gp_a"})

	rec := doPost(newHandler().Posts, `{}`, &model.User{ID: "gal_u1"})
	require.Equal(t, http.StatusOK, rec.Code)
	var resp []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	byID := map[string]map[string]any{}
	for _, p := range resp {
		byID[p["id"].(string)] = p
	}
	a, b := byID["gp_a"], byID["gp_b"]
	require.NotNil(t, a)
	require.NotNil(t, b)
	// 各 post は自分の files のみを fileIds 順で持つ。
	assert.Equal(t, []any{"galf_a1", "galf_a2"}, a["fileIds"])
	assert.Len(t, a["files"].([]any), 2)
	assert.Len(t, b["files"].([]any), 1)
	// isLiked は post ごとに正しい (A=true, B=false)。
	assert.Equal(t, true, a["isLiked"])
	assert.Equal(t, false, b["isLiked"])
}

func TestPostsUpdate_DBError(t *testing.T) {
	assert.Equal(t, http.StatusInternalServerError,
		doPost(brokenHandler().PostsUpdate, `{"postId":"x","fileIds":["galf_x"]}`, &model.User{ID: "gal_u1"}).Code)
}

// update で isSensitive 省略時は false で上書きされる (TS default:false)。
func TestPostsUpdate_IsSensitiveResetOnOmit(t *testing.T) {
	cleanup()
	defer cleanup()
	testDB.Create(&model.GalleryPost{ID: "gp_ir", UpdatedAt: time.Now(), Title: "Old", UserID: "gal_u1", FileIDs: []string{}, Tags: []string{}, IsSensitive: true})
	rec := doPost(newHandler().PostsUpdate, `{"postId":"gp_ir","title":"New"}`, &model.User{ID: "gal_u1"})
	require.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, false, resp["isSensitive"], "isSensitive 省略時は false で上書き (TS default)")
}

// create: title 空文字 / fileIds 重複 dedup / maxItems 超過。
func TestPostsCreate_TitleEmptyAndDedup(t *testing.T) {
	cleanup()
	defer cleanup()
	seedDriveFile("galf_d1", "gal_u1")
	// title 空は 400 (update 側 minLength)。create は title=="" を既存どおり弾く。
	assert.Equal(t, http.StatusBadRequest, doPost(newHandler().PostsCreate, `{"title":"","fileIds":["galf_d1"]}`, &model.User{ID: "gal_u1"}).Code)
	// 重複 fileId は dedup されて 1 件で保存される。
	rec := doPost(newHandler().PostsCreate, `{"title":"Dup","fileIds":["galf_d1","galf_d1"]}`, &model.User{ID: "gal_u1"})
	require.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, []any{"galf_d1"}, resp["fileIds"], "重複は dedup")
}

// create: fileIds が 32 を超えると 400 (maxItems)。
func TestPostsCreate_TooManyFiles(t *testing.T) {
	ids := make([]string, 33)
	for i := range ids {
		ids[i] = `"f` + string(rune('a'+i%26)) + string(rune('0'+i/26)) + `"`
	}
	body := `{"title":"x","fileIds":[` + strings.Join(ids, ",") + `]}`
	assert.Equal(t, http.StatusBadRequest, doPost(newHandler().PostsCreate, body, &model.User{ID: "gal_u1"}).Code)
}

// 混在 (owned + foreign) は owned のみを入力順で残す。
func TestPostsCreate_MixedOwnedForeignOrder(t *testing.T) {
	cleanup()
	defer cleanup()
	seedDriveFile("galf_m1", "gal_u1")
	seedDriveFile("galf_m2", "gal_u1")
	seedDriveFile("galf_mforeign", "someone_else")
	rec := doPost(newHandler().PostsCreate, `{"title":"Mix","fileIds":["galf_m2","galf_mforeign","galf_m1"]}`, &model.User{ID: "gal_u1"})
	require.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, []any{"galf_m2", "galf_m1"}, resp["fileIds"], "owned のみ入力順で残す")
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
	defer cleanup()
	seedDriveFile("galf_c1", "gal_u1")
	user := &model.User{ID: "gal_u1", Username: "galuser"}
	rec := doPost(newHandler().PostsCreate, `{"title":"My Art","fileIds":["galf_c1"]}`, user)
	assert.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "My Art", resp["title"])
	// files が DriveFile に解決される (空配列でない)。
	files, ok := resp["files"].([]any)
	require.True(t, ok)
	assert.Len(t, files, 1)
	// 作成者視点なので isLiked=false が出る。
	assert.Equal(t, false, resp["isLiked"])
	shapetest.Assert(t, "GalleryPost", resp) // L3 (#1270)
}

// fileIds 必須: 省略時は 400。
func TestPostsCreate_FileIdsRequired(t *testing.T) {
	assert.Equal(t, http.StatusBadRequest,
		doPost(newHandler().PostsCreate, `{"title":"x"}`, &model.User{ID: "gal_u1"}).Code)
}

// 他人所有の fileId のみだと有効ファイル 0 で 400。
func TestPostsCreate_ForeignFilesRejected(t *testing.T) {
	cleanup()
	defer cleanup()
	seedDriveFile("galf_other", "someone_else")
	rec := doPost(newHandler().PostsCreate, `{"title":"x","fileIds":["galf_other"]}`, &model.User{ID: "gal_u1"})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestPostsCreate_InvalidParam(t *testing.T) {
	assert.Equal(t, http.StatusBadRequest, doPost(newHandler().PostsCreate, `{}`, &model.User{ID: "u1"}).Code)
}

func TestPostsCreate_DBError(t *testing.T) {
	// fileIds 検証 (ownedFileIDs) が壊れた DB で error → 500。
	assert.Equal(t, http.StatusInternalServerError, doPost(brokenHandler().PostsCreate, `{"title":"x","fileIds":["galf_x"]}`, &model.User{ID: "u1"}).Code)
}

// --- Show ---

func TestPostsShow_Found(t *testing.T) {
	cleanup()
	testDB.Create(&model.GalleryPost{ID: "gp_s1", UpdatedAt: time.Now(), Title: "Show", UserID: "gal_u1", FileIDs: []string{}, Tags: []string{}})
	defer cleanup()
	rec := doPost(newHandler().PostsShow, `{"postId":"gp_s1"}`, nil)
	assert.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	shapetest.Assert(t, "GalleryPost", resp) // L3 (#1320)
}

// viewer が like 済みなら show の isLiked=true。未認証 viewer では isLiked 省略。
func TestPostsShow_IsLikedAndFiles(t *testing.T) {
	cleanup()
	defer cleanup()
	seedDriveFile("galf_s2", "gal_u1")
	testDB.Create(&model.GalleryPost{ID: "gp_s2", UpdatedAt: time.Now(), Title: "S", UserID: "gal_u1", FileIDs: []string{"galf_s2"}, Tags: []string{}})
	testDB.Create(&model.GalleryLike{ID: "gl_s2", UserID: "gal_u1", PostID: "gp_s2"})

	// 認証 viewer (like 済み): isLiked=true、files 解決。
	rec := doPost(newHandler().PostsShow, `{"postId":"gp_s2"}`, &model.User{ID: "gal_u1"})
	require.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, true, resp["isLiked"])
	files, _ := resp["files"].([]any)
	assert.Len(t, files, 1)

	// 未認証 viewer: isLiked は省略される (upstream optional)。
	rec = doPost(newHandler().PostsShow, `{"postId":"gp_s2"}`, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	var anon map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &anon))
	_, has := anon["isLiked"]
	assert.False(t, has, "未認証では isLiked を出さない")
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
	// upstream は更新後の GalleryPost を返す (204 ではない)。
	rec := doPost(newHandler().PostsUpdate, `{"postId":"gp_u1","title":"New"}`, &model.User{ID: "gal_u1"})
	require.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "New", resp["title"])
}

func TestPostsUpdate_WithDescription(t *testing.T) {
	cleanup()
	testDB.Create(&model.GalleryPost{ID: "gp_ud1", UpdatedAt: time.Now(), Title: "Old", UserID: "gal_u1", FileIDs: []string{}, Tags: []string{}})
	defer cleanup()
	assert.Equal(t, http.StatusOK, doPost(newHandler().PostsUpdate, `{"postId":"gp_ud1","title":"New","description":"desc"}`, &model.User{ID: "gal_u1"}).Code)
}

// update で他人所有 fileId のみ指定すると有効ファイル 0 で 400 (更新されない)。
func TestPostsUpdate_ForeignFilesRejected(t *testing.T) {
	cleanup()
	defer cleanup()
	seedDriveFile("galf_foreign", "someone_else")
	testDB.Create(&model.GalleryPost{ID: "gp_uf", UpdatedAt: time.Now(), Title: "Old", UserID: "gal_u1", FileIDs: []string{}, Tags: []string{}})
	rec := doPost(newHandler().PostsUpdate, `{"postId":"gp_uf","fileIds":["galf_foreign"]}`, &model.User{ID: "gal_u1"})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// update が fileIds / isSensitive を反映し、files を解決して返す。
func TestPostsUpdate_FileIdsAndSensitive(t *testing.T) {
	cleanup()
	defer cleanup()
	seedDriveFile("galf_u2a", "gal_u1")
	testDB.Create(&model.GalleryPost{ID: "gp_u2", UpdatedAt: time.Now(), Title: "Old", UserID: "gal_u1", FileIDs: []string{}, Tags: []string{}})
	rec := doPost(newHandler().PostsUpdate, `{"postId":"gp_u2","fileIds":["galf_u2a"],"isSensitive":true}`, &model.User{ID: "gal_u1"})
	require.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, true, resp["isSensitive"])
	assert.Equal(t, []any{"galf_u2a"}, resp["fileIds"])
	files, _ := resp["files"].([]any)
	assert.Len(t, files, 1)
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
