package flash

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	coreflash "github.com/shiroha-a/mk/internal/core/flash"
	"github.com/shiroha-a/mk/internal/core/moderationlog"
	"github.com/shiroha-a/mk/internal/entitycompat/shapetest"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/server/middleware"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newHandler(t *testing.T) (*Handler, *testutil.MockFlashRepository, *testutil.MockFlashLikeRepository) {
	t.Helper()
	repo := testutil.NewMockFlashRepository()
	likeRepo := testutil.NewMockFlashLikeRepository()
	idGen, _ := id.NewGenerator("aidx")
	svc := coreflash.NewService(repo, likeRepo, idGen)
	return NewHandler(svc, nil, nil), repo, likeRepo
}

func newReq(t *testing.T, body string) (echo.Context, *httptest.ResponseRecorder) {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	return e.NewContext(req, rec), rec
}

func setUser(c echo.Context, userID string) {
	c.Set(string(middleware.UserContextKey), &model.User{ID: userID})
}

// stubRoles implements flash.RoleChecker for #1548 tests.
type stubRoles struct{ moderators map[string]bool }

func (s *stubRoles) IsModerator(userID string) bool { return s.moderators[userID] }

// stubModLog records moderation log calls for #1548 tests.
type stubModLog struct{ calls []stubModLogCall }

type stubModLogCall struct {
	moderatorID string
	logType     moderationlog.LogType
	info        map[string]any
}

func (s *stubModLog) Log(_ context.Context, moderatorID string, t moderationlog.LogType, info map[string]any) {
	s.calls = append(s.calls, stubModLogCall{moderatorID, t, info})
}

// --- Create ----------------------------------------------------------------

func TestCreate_Success(t *testing.T) {
	h, _, _ := newHandler(t)
	c, rec := newReq(t, `{"title":"t","summary":"s","script":"x","permissions":[]}`)
	setUser(c, "alice")
	require.NoError(t, h.Create(c))
	assert.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	shapetest.Assert(t, "Flash", resp) // L3 (#1270)
}

// #1548: summary が欠けると 400 (upstream paramDef required)。
func TestCreate_SummaryRequired(t *testing.T) {
	h, _, _ := newHandler(t)
	c, rec := newReq(t, `{"title":"t","script":"x","permissions":[]}`)
	setUser(c, "alice")
	require.NoError(t, h.Create(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// #1548: permissions が欠けると 400 (upstream paramDef required)。
func TestCreate_PermissionsRequired(t *testing.T) {
	h, _, _ := newHandler(t)
	c, rec := newReq(t, `{"title":"t","summary":"s","script":"x"}`)
	setUser(c, "alice")
	require.NoError(t, h.Create(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// #1548: summary="" / permissions=[] は present 扱いで作成される。
func TestCreate_EmptySummaryAndPermissionsAccepted(t *testing.T) {
	h, _, _ := newHandler(t)
	c, rec := newReq(t, `{"title":"t","summary":"","script":"x","permissions":[]}`)
	setUser(c, "alice")
	require.NoError(t, h.Create(c))
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestCreate_BadJSON(t *testing.T) {
	h, _, _ := newHandler(t)
	c, rec := newReq(t, `{not`)
	setUser(c, "alice")
	require.NoError(t, h.Create(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestCreate_TitleRequired(t *testing.T) {
	h, _, _ := newHandler(t)
	c, rec := newReq(t, `{"script":"x"}`)
	setUser(c, "alice")
	require.NoError(t, h.Create(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestCreate_ScriptRequired(t *testing.T) {
	h, _, _ := newHandler(t)
	c, rec := newReq(t, `{"title":"t"}`)
	setUser(c, "alice")
	require.NoError(t, h.Create(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// failingFlashRepo causes Create to fail.
type failingFlashRepo struct {
	*testutil.MockFlashRepository
}

func (r *failingFlashRepo) Create(_ *model.Flash) error { return errors.New("boom") }

func TestCreate_RepoError(t *testing.T) {
	mock := testutil.NewMockFlashRepository()
	repo := &failingFlashRepo{MockFlashRepository: mock}
	idGen, _ := id.NewGenerator("aidx")
	svc := coreflash.NewService(repo, testutil.NewMockFlashLikeRepository(), idGen)
	h := NewHandler(svc, nil, nil)
	c, rec := newReq(t, `{"title":"t","summary":"s","script":"x","permissions":[]}`)
	setUser(c, "alice")
	require.NoError(t, h.Create(c))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

// --- Show ------------------------------------------------------------------

func TestShow_Success(t *testing.T) {
	h, repo, _ := newHandler(t)
	repo.Flashes["f1"] = &model.Flash{ID: "f1", UserID: "alice", Title: "t"}
	c, rec := newReq(t, `{"flashId":"f1"}`)
	require.NoError(t, h.Show(c))
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestShow_BadJSON(t *testing.T) {
	h, _, _ := newHandler(t)
	c, rec := newReq(t, `{not`)
	require.NoError(t, h.Show(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestShow_NotFound(t *testing.T) {
	h, _, _ := newHandler(t)
	c, rec := newReq(t, `{"flashId":"missing"}`)
	require.NoError(t, h.Show(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestShow_WithUser(t *testing.T) {
	h, repo, _ := newHandler(t)
	repo.Flashes["f1"] = &model.Flash{ID: "f1", UserID: "alice", Title: "t"}
	c, rec := newReq(t, `{"flashId":"f1"}`)
	setUser(c, "alice")
	require.NoError(t, h.Show(c))
	assert.Equal(t, http.StatusOK, rec.Code)
}

// --- Update ----------------------------------------------------------------

func TestUpdate_Success(t *testing.T) {
	h, repo, _ := newHandler(t)
	repo.Flashes["f1"] = &model.Flash{ID: "f1", UserID: "alice", Title: "t"}
	c, rec := newReq(t, `{"flashId":"f1","title":"t2","summary":"s","script":"y","permissions":["a"],"visibility":"private"}`)
	setUser(c, "alice")
	require.NoError(t, h.Update(c))
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestUpdate_BadJSON(t *testing.T) {
	h, _, _ := newHandler(t)
	c, rec := newReq(t, `{not`)
	setUser(c, "alice")
	require.NoError(t, h.Update(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestUpdate_NotFound(t *testing.T) {
	h, _, _ := newHandler(t)
	c, rec := newReq(t, `{"flashId":"missing"}`)
	setUser(c, "alice")
	require.NoError(t, h.Update(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestUpdate_AccessDenied(t *testing.T) {
	h, repo, _ := newHandler(t)
	repo.Flashes["f1"] = &model.Flash{ID: "f1", UserID: "owner"}
	c, rec := newReq(t, `{"flashId":"f1"}`)
	setUser(c, "alice")
	require.NoError(t, h.Update(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestUpdate_TitleEmpty(t *testing.T) {
	h, repo, _ := newHandler(t)
	repo.Flashes["f1"] = &model.Flash{ID: "f1", UserID: "alice"}
	c, rec := newReq(t, `{"flashId":"f1","title":""}`)
	setUser(c, "alice")
	require.NoError(t, h.Update(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// failingUpdateRepo causes UpdateFields to fail.
type failingUpdateRepo struct {
	*testutil.MockFlashRepository
}

func (r *failingUpdateRepo) UpdateFields(_ string, _ map[string]any) error {
	return errors.New("boom")
}

func TestUpdate_RepoError(t *testing.T) {
	mock := testutil.NewMockFlashRepository()
	mock.Flashes["f1"] = &model.Flash{ID: "f1", UserID: "alice"}
	repo := &failingUpdateRepo{MockFlashRepository: mock}
	idGen, _ := id.NewGenerator("aidx")
	svc := coreflash.NewService(repo, testutil.NewMockFlashLikeRepository(), idGen)
	h := NewHandler(svc, nil, nil)
	c, rec := newReq(t, `{"flashId":"f1","title":"x"}`)
	setUser(c, "alice")
	require.NoError(t, h.Update(c))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

// --- Delete ----------------------------------------------------------------

func TestDelete_Success(t *testing.T) {
	h, repo, _ := newHandler(t)
	repo.Flashes["f1"] = &model.Flash{ID: "f1", UserID: "alice"}
	c, rec := newReq(t, `{"flashId":"f1"}`)
	setUser(c, "alice")
	require.NoError(t, h.Delete(c))
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

func TestDelete_BadJSON(t *testing.T) {
	h, _, _ := newHandler(t)
	c, rec := newReq(t, `{not`)
	setUser(c, "alice")
	require.NoError(t, h.Delete(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestDelete_NotFound(t *testing.T) {
	h, _, _ := newHandler(t)
	c, rec := newReq(t, `{"flashId":"missing"}`)
	setUser(c, "alice")
	require.NoError(t, h.Delete(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestDelete_AccessDenied(t *testing.T) {
	h, repo, _ := newHandler(t)
	repo.Flashes["f1"] = &model.Flash{ID: "f1", UserID: "owner"}
	c, rec := newReq(t, `{"flashId":"f1"}`)
	setUser(c, "alice")
	require.NoError(t, h.Delete(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// failingDeleteRepo causes Delete to fail.
type failingDeleteRepo struct {
	*testutil.MockFlashRepository
}

func (r *failingDeleteRepo) Delete(_ *model.Flash) error { return errors.New("boom") }

func TestDelete_RepoError(t *testing.T) {
	mock := testutil.NewMockFlashRepository()
	mock.Flashes["f1"] = &model.Flash{ID: "f1", UserID: "alice"}
	repo := &failingDeleteRepo{MockFlashRepository: mock}
	idGen, _ := id.NewGenerator("aidx")
	svc := coreflash.NewService(repo, testutil.NewMockFlashLikeRepository(), idGen)
	h := NewHandler(svc, nil, nil)
	c, rec := newReq(t, `{"flashId":"f1"}`)
	setUser(c, "alice")
	require.NoError(t, h.Delete(c))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

// #1548: モデレータは他人の flash を削除でき moderationLog を残す。
func TestDelete_ModeratorWithModLog(t *testing.T) {
	idGen, _ := id.NewGenerator("aidx")
	repo := testutil.NewMockFlashRepository()
	repo.Flashes["f1"] = &model.Flash{ID: "f1", UserID: "owner", Title: "T"}
	userRepo := testutil.NewMockUserRepository()
	require.NoError(t, userRepo.Create(&model.User{ID: "owner", Username: "ownername"}))
	svc := coreflash.NewService(repo, testutil.NewMockFlashLikeRepository(), idGen)
	h := NewHandler(svc, userRepo, idGen)
	h.SetRoleChecker(&stubRoles{moderators: map[string]bool{"mod": true}})
	ml := &stubModLog{}
	h.SetModLog(ml)

	c, rec := newReq(t, `{"flashId":"f1"}`)
	setUser(c, "mod")
	require.NoError(t, h.Delete(c))
	assert.Equal(t, http.StatusNoContent, rec.Code)
	_, err := repo.FindByID("f1")
	assert.Error(t, err, "flash must be deleted")
	require.Len(t, ml.calls, 1)
	assert.Equal(t, moderationlog.LogDeleteFlash, ml.calls[0].logType)
	assert.Equal(t, "mod", ml.calls[0].moderatorID)
	assert.Equal(t, "f1", ml.calls[0].info["flashId"])
	assert.Equal(t, "owner", ml.calls[0].info["flashUserId"])
	assert.Equal(t, "ownername", ml.calls[0].info["flashUserUsername"])
}

// #1548: 所有者自身の削除では moderationLog を残さない。
func TestDelete_OwnerNoModLog(t *testing.T) {
	h, repo, _ := newHandler(t)
	repo.Flashes["f1"] = &model.Flash{ID: "f1", UserID: "alice"}
	h.SetRoleChecker(&stubRoles{moderators: map[string]bool{"alice": true}})
	ml := &stubModLog{}
	h.SetModLog(ml)
	c, rec := newReq(t, `{"flashId":"f1"}`)
	setUser(c, "alice")
	require.NoError(t, h.Delete(c))
	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Empty(t, ml.calls)
}

// --- My ---------------------------------------------------------------------

func TestMy_Success(t *testing.T) {
	h, repo, _ := newHandler(t)
	repo.Flashes["f1"] = &model.Flash{ID: "f1", UserID: "alice", Title: "alpha"}
	c, rec := newReq(t, `{}`)
	setUser(c, "alice")
	require.NoError(t, h.My(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "alpha")
}

func TestMy_BadJSON(t *testing.T) {
	h, _, _ := newHandler(t)
	c, rec := newReq(t, `{not`)
	setUser(c, "alice")
	require.NoError(t, h.My(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// listFailRepo causes ListByUser to fail.
type listFailRepo struct {
	*testutil.MockFlashRepository
}

func (r *listFailRepo) ListByUser(_, _, _ string, _, _ int) ([]*model.Flash, error) {
	return nil, errors.New("boom")
}

func TestMy_RepoError(t *testing.T) {
	repo := &listFailRepo{MockFlashRepository: testutil.NewMockFlashRepository()}
	idGen, _ := id.NewGenerator("aidx")
	svc := coreflash.NewService(repo, testutil.NewMockFlashLikeRepository(), idGen)
	h := NewHandler(svc, nil, nil)
	c, rec := newReq(t, `{}`)
	setUser(c, "alice")
	require.NoError(t, h.My(c))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

// #1773: flash/my は upstream flash/my.ts (packMany without me) に合わせ isLiked を
// omit する。認証 viewer が自分の flash を like 済みでも my では isLiked を出さない
// (featured/search/my-likes は me 付きで isLiked を出すのと対照的)。
func TestMy_OmitsIsLiked(t *testing.T) {
	h, repo, likeRepo := newHandler(t)
	repo.Flashes["f1"] = &model.Flash{ID: "f1", UserID: "alice", Title: "mine"}
	likeRepo.Likes["l1"] = &model.FlashLike{ID: "l1", UserID: "alice", FlashID: "f1"}
	c, rec := newReq(t, `{}`)
	setUser(c, "alice")
	require.NoError(t, h.My(c))
	require.Equal(t, http.StatusOK, rec.Code)
	var resp []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp, 1)
	_, has := resp[0]["isLiked"]
	assert.False(t, has, "flash/my must omit isLiked (upstream packMany without me)")
}

// #1773: flash の応答は permissions を含めない。upstream FlashEntityService.pack は
// permissions を返さず flash.ts json-schema にも無いため、create が受理・保存しても
// 応答 shape からは除外する。
func TestFlashResponse_OmitsPermissions(t *testing.T) {
	h, repo, _ := newHandler(t)
	repo.Flashes["f1"] = &model.Flash{ID: "f1", UserID: "alice", Title: "x", Permissions: model.StringArray{"a", "b"}}
	c, rec := newReq(t, `{}`)
	setUser(c, "alice")
	require.NoError(t, h.My(c))
	require.Equal(t, http.StatusOK, rec.Code)
	var resp []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp, 1)
	_, has := resp[0]["permissions"]
	assert.False(t, has, "flash response must omit permissions (#1773)")
}

// --- Featured --------------------------------------------------------------

func TestFeatured_Success(t *testing.T) {
	h, repo, _ := newHandler(t)
	repo.Flashes["f1"] = &model.Flash{ID: "f1", UserID: "alice", Title: "alpha"}
	c, rec := newReq(t, `{}`)
	require.NoError(t, h.Featured(c))
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestFeatured_BadJSON(t *testing.T) {
	h, _, _ := newHandler(t)
	c, rec := newReq(t, `{not`)
	require.NoError(t, h.Featured(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// featuredFailRepo causes ListFeatured to fail.
type featuredFailRepo struct {
	*testutil.MockFlashRepository
}

func (r *featuredFailRepo) ListFeatured(_, _ string, _, _ int) ([]*model.Flash, error) {
	return nil, errors.New("boom")
}

func TestFeatured_RepoError(t *testing.T) {
	repo := &featuredFailRepo{MockFlashRepository: testutil.NewMockFlashRepository()}
	idGen, _ := id.NewGenerator("aidx")
	svc := coreflash.NewService(repo, testutil.NewMockFlashLikeRepository(), idGen)
	h := NewHandler(svc, nil, nil)
	c, rec := newReq(t, `{}`)
	require.NoError(t, h.Featured(c))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

// --- Search ----------------------------------------------------------------

func TestSearch_Success(t *testing.T) {
	h, repo, _ := newHandler(t)
	repo.Flashes["f1"] = &model.Flash{ID: "f1", UserID: "alice", Title: "alpha calc"}
	c, rec := newReq(t, `{"query":"calc"}`)
	require.NoError(t, h.Search(c))
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestSearch_BadJSON(t *testing.T) {
	h, _, _ := newHandler(t)
	c, rec := newReq(t, `{not`)
	require.NoError(t, h.Search(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestSearch_QueryRequired(t *testing.T) {
	h, _, _ := newHandler(t)
	c, rec := newReq(t, `{}`)
	require.NoError(t, h.Search(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// searchFailRepo causes Search to fail.
type searchFailRepo struct {
	*testutil.MockFlashRepository
}

func (r *searchFailRepo) Search(_, _, _ string, _, _ int) ([]*model.Flash, error) {
	return nil, errors.New("boom")
}

func TestSearch_RepoError(t *testing.T) {
	repo := &searchFailRepo{MockFlashRepository: testutil.NewMockFlashRepository()}
	idGen, _ := id.NewGenerator("aidx")
	svc := coreflash.NewService(repo, testutil.NewMockFlashLikeRepository(), idGen)
	h := NewHandler(svc, nil, nil)
	c, rec := newReq(t, `{"query":"x"}`)
	require.NoError(t, h.Search(c))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

// --- Like ------------------------------------------------------------------

func TestLike_Success(t *testing.T) {
	h, repo, _ := newHandler(t)
	repo.Flashes["f1"] = &model.Flash{ID: "f1", UserID: "alice"}
	c, rec := newReq(t, `{"flashId":"f1"}`)
	setUser(c, "bob")
	require.NoError(t, h.Like(c))
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

func TestLike_BadJSON(t *testing.T) {
	h, _, _ := newHandler(t)
	c, rec := newReq(t, `{not`)
	setUser(c, "bob")
	require.NoError(t, h.Like(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestLike_NotFound(t *testing.T) {
	h, _, _ := newHandler(t)
	c, rec := newReq(t, `{"flashId":"missing"}`)
	setUser(c, "bob")
	require.NoError(t, h.Like(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestLike_AlreadyLiked(t *testing.T) {
	h, repo, _ := newHandler(t)
	repo.Flashes["f1"] = &model.Flash{ID: "f1", UserID: "alice"}
	c, rec := newReq(t, `{"flashId":"f1"}`)
	setUser(c, "bob")
	require.NoError(t, h.Like(c))
	c2, rec2 := newReq(t, `{"flashId":"f1"}`)
	setUser(c2, "bob")
	require.NoError(t, h.Like(c2))
	assert.Equal(t, http.StatusBadRequest, rec2.Code)
	assert.Contains(t, rec2.Body.String(), "ALREADY_LIKED")
	_ = rec
}

// #1548: 自分の flash を like すると YOUR_FLASH。
func TestLike_YourFlash(t *testing.T) {
	h, repo, _ := newHandler(t)
	repo.Flashes["f1"] = &model.Flash{ID: "f1", UserID: "alice"}
	c, rec := newReq(t, `{"flashId":"f1"}`)
	setUser(c, "alice")
	require.NoError(t, h.Like(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "3fd8a0e7-5955-4ba9-85bb-bf3e0c30e13b")
}

// #1548: list endpoint (featured/my/search/my-likes) は viewer の isLiked を返す。
func TestList_IsLikedPopulated(t *testing.T) {
	h, repo, likeRepo := newHandler(t)
	repo.Flashes["f1"] = &model.Flash{ID: "f1", UserID: "owner", Title: "liked"}
	repo.Flashes["f2"] = &model.Flash{ID: "f2", UserID: "owner", Title: "notliked"}
	likeRepo.Likes["l1"] = &model.FlashLike{ID: "l1", UserID: "bob", FlashID: "f1"}

	c, rec := newReq(t, `{}`)
	setUser(c, "bob")
	require.NoError(t, h.Featured(c))
	require.Equal(t, http.StatusOK, rec.Code)
	var resp []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	byID := map[string]map[string]any{}
	for _, f := range resp {
		byID[f["id"].(string)] = f
	}
	require.NotNil(t, byID["f1"])
	require.NotNil(t, byID["f2"])
	assert.Equal(t, true, byID["f1"]["isLiked"])
	assert.Equal(t, false, byID["f2"]["isLiked"])
}

// 未認証 viewer では isLiked は省略される (upstream optional)。
func TestList_IsLikedOmittedForAnon(t *testing.T) {
	h, repo, _ := newHandler(t)
	repo.Flashes["f1"] = &model.Flash{ID: "f1", UserID: "owner", Title: "x"}
	c, rec := newReq(t, `{}`)
	require.NoError(t, h.Featured(c)) // no user
	require.Equal(t, http.StatusOK, rec.Code)
	var resp []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp, 1)
	_, has := resp[0]["isLiked"]
	assert.False(t, has, "anonymous viewer must not get isLiked")
}

// failingCreateLikeRepo causes Create to fail.
type failingCreateLikeRepo struct {
	*testutil.MockFlashLikeRepository
}

func (r *failingCreateLikeRepo) Create(_ *model.FlashLike) error { return errors.New("boom") }

func TestLike_RepoError(t *testing.T) {
	repo := testutil.NewMockFlashRepository()
	repo.Flashes["f1"] = &model.Flash{ID: "f1", UserID: "alice"}
	likeRepo := &failingCreateLikeRepo{MockFlashLikeRepository: testutil.NewMockFlashLikeRepository()}
	idGen, _ := id.NewGenerator("aidx")
	svc := coreflash.NewService(repo, likeRepo, idGen)
	h := NewHandler(svc, nil, nil)
	c, rec := newReq(t, `{"flashId":"f1"}`)
	setUser(c, "bob")
	require.NoError(t, h.Like(c))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

// --- Unlike ----------------------------------------------------------------

func TestUnlike_Success(t *testing.T) {
	h, repo, _ := newHandler(t)
	repo.Flashes["f1"] = &model.Flash{ID: "f1", UserID: "alice"}
	c, rec := newReq(t, `{"flashId":"f1"}`)
	setUser(c, "bob")
	require.NoError(t, h.Like(c))
	c2, rec2 := newReq(t, `{"flashId":"f1"}`)
	setUser(c2, "bob")
	require.NoError(t, h.Unlike(c2))
	assert.Equal(t, http.StatusNoContent, rec2.Code)
	_ = rec
}

func TestUnlike_BadJSON(t *testing.T) {
	h, _, _ := newHandler(t)
	c, rec := newReq(t, `{not`)
	setUser(c, "bob")
	require.NoError(t, h.Unlike(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestUnlike_NotFound(t *testing.T) {
	h, _, _ := newHandler(t)
	c, rec := newReq(t, `{"flashId":"missing"}`)
	setUser(c, "bob")
	require.NoError(t, h.Unlike(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestUnlike_NotLiked(t *testing.T) {
	h, repo, _ := newHandler(t)
	repo.Flashes["f1"] = &model.Flash{ID: "f1", UserID: "alice"}
	c, rec := newReq(t, `{"flashId":"f1"}`)
	setUser(c, "bob")
	require.NoError(t, h.Unlike(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "NOT_LIKED")
}

// failingDeleteLikeRepo causes Delete to fail.
type failingDeleteLikeRepo struct {
	*testutil.MockFlashLikeRepository
}

func (r *failingDeleteLikeRepo) Delete(_ *model.FlashLike) error { return errors.New("boom") }

func TestUnlike_RepoError(t *testing.T) {
	repo := testutil.NewMockFlashRepository()
	repo.Flashes["f1"] = &model.Flash{ID: "f1", UserID: "alice"}
	mock := testutil.NewMockFlashLikeRepository()
	mock.Likes["fl1"] = &model.FlashLike{ID: "fl1", UserID: "bob", FlashID: "f1"}
	likeRepo := &failingDeleteLikeRepo{MockFlashLikeRepository: mock}
	idGen, _ := id.NewGenerator("aidx")
	svc := coreflash.NewService(repo, likeRepo, idGen)
	h := NewHandler(svc, nil, nil)
	c, rec := newReq(t, `{"flashId":"f1"}`)
	setUser(c, "bob")
	require.NoError(t, h.Unlike(c))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

// --- MyLikes ---------------------------------------------------------------

func TestMyLikes_Success(t *testing.T) {
	h, repo, _ := newHandler(t)
	repo.Flashes["f1"] = &model.Flash{ID: "f1", UserID: "alice", Title: "alpha"}
	c, rec := newReq(t, `{"flashId":"f1"}`)
	setUser(c, "bob")
	require.NoError(t, h.Like(c))
	c2, rec2 := newReq(t, `{}`)
	setUser(c2, "bob")
	require.NoError(t, h.MyLikes(c2))
	assert.Equal(t, http.StatusOK, rec2.Code)
	assert.Contains(t, rec2.Body.String(), "alpha")
	_ = rec
}

func TestMyLikes_BadJSON(t *testing.T) {
	h, _, _ := newHandler(t)
	c, rec := newReq(t, `{not`)
	setUser(c, "bob")
	require.NoError(t, h.MyLikes(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// myLikesFailRepo causes ListByUserSearch (used by MyLikes) on the like repo to fail.
type myLikesFailRepo struct {
	*testutil.MockFlashLikeRepository
}

func (r *myLikesFailRepo) ListByUserSearch(_, _, _, _ string, _, _ int) ([]*model.FlashLike, error) {
	return nil, errors.New("boom")
}

func TestMyLikes_RepoError(t *testing.T) {
	repo := testutil.NewMockFlashRepository()
	likeRepo := &myLikesFailRepo{MockFlashLikeRepository: testutil.NewMockFlashLikeRepository()}
	idGen, _ := id.NewGenerator("aidx")
	svc := coreflash.NewService(repo, likeRepo, idGen)
	h := NewHandler(svc, nil, nil)
	c, rec := newReq(t, `{}`)
	setUser(c, "bob")
	require.NoError(t, h.MyLikes(c))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

// #2027: visibility enum 外は 400。
func TestCreate_InvalidVisibility(t *testing.T) {
	h, _, _ := newHandler(t)
	c, rec := newReq(t, `{"title":"t","script":"s","summary":"","permissions":[],"visibility":"bogus"}`)
	require.NoError(t, h.Create(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}
