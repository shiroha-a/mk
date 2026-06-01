package notes

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	corenote "github.com/shiroha-a/mk/internal/core/note"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/server/middleware"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newExtraHandler(t *testing.T) (*Handler, *testutil.MockNoteRepository, *testutil.MockNoteFavoriteRepository) {
	t.Helper()
	noteRepo := testutil.NewMockNoteRepository()
	favRepo := testutil.NewMockNoteFavoriteRepository()
	pollRepo := testutil.NewMockPollRepository()
	idGen, _ := id.NewGenerator("aidx")
	createSvc := corenote.NewCreateService(noteRepo, pollRepo, idGen, nil)
	deleteSvc := corenote.NewDeleteService(noteRepo)
	h := NewHandler(noteRepo, createSvc, deleteSvc, nil, nil, nil, nil, nil, idGen)
	h.SetFavoriteRepo(favRepo)
	return h, noteRepo, favRepo
}

func postExtra(h func(echo.Context) error, body string, user *model.User) *httptest.ResponseRecorder {
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

// --- Favorites ---

func TestFavoritesCreate_Success(t *testing.T) {
	h, noteRepo, favRepo := newExtraHandler(t)
	noteRepo.Notes["n1"] = &model.Note{ID: "n1", UserID: "u1"}
	rec := postExtra(h.FavoritesCreate, `{"noteId":"n1"}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Len(t, favRepo.Favorites, 1)
}

func TestFavoritesCreate_AlreadyFavorited(t *testing.T) {
	h, noteRepo, favRepo := newExtraHandler(t)
	noteRepo.Notes["n1"] = &model.Note{ID: "n1", UserID: "u1"}
	favRepo.Favorites["u1:n1"] = &model.NoteFavorite{ID: "f1", UserID: "u1", NoteID: "n1"}
	rec := postExtra(h.FavoritesCreate, `{"noteId":"n1"}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusConflict, rec.Code)
}

func TestFavoritesCreate_NoteNotFound(t *testing.T) {
	h, _, _ := newExtraHandler(t)
	rec := postExtra(h.FavoritesCreate, `{"noteId":"ghost"}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestFavoritesCreate_InvalidParam(t *testing.T) {
	h, _, _ := newExtraHandler(t)
	rec := postExtra(h.FavoritesCreate, `{}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestFavoritesCreate_NilRepo(t *testing.T) {
	noteRepo := testutil.NewMockNoteRepository()
	noteRepo.Notes["n1"] = &model.Note{ID: "n1"}
	idGen, _ := id.NewGenerator("aidx")
	h := NewHandler(noteRepo, nil, nil, nil, nil, nil, nil, nil, idGen)
	rec := postExtra(h.FavoritesCreate, `{"noteId":"n1"}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestFavoritesDelete_Success(t *testing.T) {
	h, _, favRepo := newExtraHandler(t)
	favRepo.Favorites["u1:n1"] = &model.NoteFavorite{UserID: "u1", NoteID: "n1"}
	rec := postExtra(h.FavoritesDelete, `{"noteId":"n1"}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Empty(t, favRepo.Favorites)
}

func TestFavoritesDelete_InvalidParam(t *testing.T) {
	h, _, _ := newExtraHandler(t)
	rec := postExtra(h.FavoritesDelete, `{}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestFavoritesDelete_NilRepo(t *testing.T) {
	idGen, _ := id.NewGenerator("aidx")
	h := NewHandler(testutil.NewMockNoteRepository(), nil, nil, nil, nil, nil, nil, nil, idGen)
	rec := postExtra(h.FavoritesDelete, `{"noteId":"n1"}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

// --- Featured ---

func TestFeatured_Success(t *testing.T) {
	h, noteRepo, _ := newExtraHandler(t)
	noteRepo.Notes["n1"] = &model.Note{ID: "n1", UserID: "u1", Visibility: "public", User: &model.User{ID: "u1"}}
	rec := postExtra(h.Featured, `{}`, nil)
	assert.Equal(t, http.StatusOK, rec.Code)
	var resp []any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.NotEmpty(t, resp)
}

func TestFeatured_InvalidJSON(t *testing.T) {
	h, _, _ := newExtraHandler(t)
	rec := postExtra(h.Featured, `invalid`, nil)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// channelId 指定時は当該チャンネルに属するノートだけが返ること (#489)。
// handler が channelId を bind せずに repo に渡し忘れると、ハイライト
// タブで他チャンネル / グローバルのノートが混入する。
func TestFeatured_ChannelFilter(t *testing.T) {
	h, noteRepo, _ := newExtraHandler(t)
	chID := "ch1"
	noteRepo.Notes["n_ch"] = &model.Note{ID: "n_ch", UserID: "u1", Visibility: "public", ChannelID: &chID, User: &model.User{ID: "u1"}}
	noteRepo.Notes["n_glb"] = &model.Note{ID: "n_glb", UserID: "u1", Visibility: "public", User: &model.User{ID: "u1"}}

	rec := postExtra(h.Featured, `{"channelId":"ch1"}`, nil)
	assert.Equal(t, http.StatusOK, rec.Code)
	var resp []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp, 1)
	assert.Equal(t, "n_ch", resp[0]["id"])
}

// --- Unrenote ---

func TestUnrenote_Success(t *testing.T) {
	h, noteRepo, _ := newExtraHandler(t)
	renoteID := "orig"
	noteRepo.Notes["orig"] = &model.Note{ID: "orig", UserID: "u1"}
	noteRepo.Notes["rn1"] = &model.Note{ID: "rn1", UserID: "u1", RenoteID: &renoteID, Text: nil}
	rec := postExtra(h.Unrenote, `{"noteId":"orig"}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

func TestUnrenote_NotFound(t *testing.T) {
	h, _, _ := newExtraHandler(t)
	rec := postExtra(h.Unrenote, `{"noteId":"ghost"}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestUnrenote_InvalidParam(t *testing.T) {
	h, _, _ := newExtraHandler(t)
	rec := postExtra(h.Unrenote, `{}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// --- Mentions ---

func TestMentions_Success(t *testing.T) {
	h, noteRepo, _ := newExtraHandler(t)
	noteRepo.Notes["n1"] = &model.Note{ID: "n1", UserID: "u2", Mentions: []string{"u1"}, User: &model.User{ID: "u2"}}
	rec := postExtra(h.Mentions, `{}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestMentions_InvalidJSON(t *testing.T) {
	h, _, _ := newExtraHandler(t)
	rec := postExtra(h.Mentions, `invalid`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// --- UserListTimeline ---

func TestUserListTimeline_Success(t *testing.T) {
	h, noteRepo, _ := newExtraHandler(t)
	listRepo := testutil.NewMockUserListRepository()
	listRepo.Lists["l1"] = &model.UserList{ID: "l1", UserID: "u1", Name: "my list"}
	h.SetUserListRepo(listRepo)
	// リストメンバーのノートを用意
	noteRepo.Notes["n1"] = &model.Note{ID: "n1", UserID: "member1", Visibility: "public", User: &model.User{ID: "member1"}}
	rec := postExtra(h.UserListTimeline, `{"listId":"l1"}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusOK, rec.Code)
	var resp []any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
}

func TestUserListTimeline_NotOwner(t *testing.T) {
	h, _, _ := newExtraHandler(t)
	listRepo := testutil.NewMockUserListRepository()
	// リストは別ユーザー "other" が所有
	listRepo.Lists["l1"] = &model.UserList{ID: "l1", UserID: "other", Name: "their list"}
	h.SetUserListRepo(listRepo)
	rec := postExtra(h.UserListTimeline, `{"listId":"l1"}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusNotFound, rec.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	errObj := resp["error"].(map[string]any)
	assert.Equal(t, "NO_SUCH_LIST", errObj["code"])
}

func TestUserListTimeline_ListNotFound(t *testing.T) {
	h, _, _ := newExtraHandler(t)
	listRepo := testutil.NewMockUserListRepository()
	h.SetUserListRepo(listRepo)
	// リストが存在しない場合
	rec := postExtra(h.UserListTimeline, `{"listId":"ghost"}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusNotFound, rec.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	errObj := resp["error"].(map[string]any)
	assert.Equal(t, "NO_SUCH_LIST", errObj["code"])
}

func TestUserListTimeline_InvalidParam(t *testing.T) {
	h, _, _ := newExtraHandler(t)
	rec := postExtra(h.UserListTimeline, `{}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestUserListTimeline_WithoutUserListRepo(t *testing.T) {
	// userListRepoがnilの場合は所有権チェックをスキップしてDBクエリに進む
	h, _, _ := newExtraHandler(t)
	rec := postExtra(h.UserListTimeline, `{"listId":"l1"}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusOK, rec.Code)
}

type failingListByUserListRepo struct{ *testutil.MockNoteRepository }

func (f *failingListByUserListRepo) ListByUserList(_ string, _ int, _, _ string) ([]*model.Note, error) {
	return nil, testutil.ErrNotFound
}

func TestUserListTimeline_RepoError(t *testing.T) {
	idGen, _ := id.NewGenerator("aidx")
	h := NewHandler(&failingListByUserListRepo{testutil.NewMockNoteRepository()}, nil, nil, nil, nil, nil, nil, nil, idGen)
	rec := postExtra(h.UserListTimeline, `{"listId":"l1"}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

// --- SearchByTag ---

func TestSearchByTag_Success(t *testing.T) {
	h, noteRepo, _ := newExtraHandler(t)
	noteRepo.Notes["n1"] = &model.Note{ID: "n1", UserID: "u1", Tags: []string{"golang"}, Visibility: "public", User: &model.User{ID: "u1"}}
	rec := postExtra(h.SearchByTag, `{"tag":"golang"}`, nil)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestSearchByTag_InvalidParam(t *testing.T) {
	h, _, _ := newExtraHandler(t)
	rec := postExtra(h.SearchByTag, `{}`, nil)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestSearchByTag_QueryArray(t *testing.T) {
	h, noteRepo, _ := newExtraHandler(t)
	noteRepo.Notes["n1"] = &model.Note{ID: "n1", UserID: "u1", Tags: []string{"go"}, Visibility: "public", User: &model.User{ID: "u1"}}
	// query の最初の要素がタグとして使われる
	rec := postExtra(h.SearchByTag, `{"query":[["go","rust"],["web"]]}`, nil)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestSearchByTag_QueryArrayEmpty(t *testing.T) {
	h, _, _ := newExtraHandler(t)
	// query が空配列 → tag も空 → 400
	assert.Equal(t, http.StatusBadRequest, postExtra(h.SearchByTag, `{"query":[]}`, nil).Code)
	assert.Equal(t, http.StatusBadRequest, postExtra(h.SearchByTag, `{"query":[[]]}`, nil).Code)
}

// search-by-tag の visibility push-down (#1439)。discovery 系なので匿名/非
// follower には followers / specified note を返さず、author 本人 / follower /
// visibleUserIds 対象には返すことを固定する。
func seedSearchVisibilityNotes(noteRepo *testutil.MockNoteRepository) {
	noteRepo.Notes["t_pub"] = &model.Note{ID: "t_pub", UserID: "author", Tags: []string{"tag"}, Visibility: "public", User: &model.User{ID: "author"}}
	noteRepo.Notes["t_fol"] = &model.Note{ID: "t_fol", UserID: "author", Tags: []string{"tag"}, Visibility: "followers", User: &model.User{ID: "author"}}
	noteRepo.Notes["t_spec"] = &model.Note{ID: "t_spec", UserID: "author", Tags: []string{"tag"}, Visibility: "specified", VisibleUserIDs: []string{"allowed"}, User: &model.User{ID: "author"}}
}

func searchTagIDs(t *testing.T, rec *httptest.ResponseRecorder) map[string]bool {
	t.Helper()
	require.Equal(t, http.StatusOK, rec.Code)
	var out []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	ids := map[string]bool{}
	for _, n := range out {
		ids[n["id"].(string)] = true
	}
	return ids
}

func TestSearchByTag_AnonymousExcludesNonPublic(t *testing.T) {
	h, noteRepo, _ := newExtraHandler(t)
	seedSearchVisibilityNotes(noteRepo)
	ids := searchTagIDs(t, postExtra(h.SearchByTag, `{"tag":"tag"}`, nil))
	assert.True(t, ids["t_pub"], "public は匿名に見える")
	assert.False(t, ids["t_fol"], "followers は匿名に漏らさない")
	assert.False(t, ids["t_spec"], "specified は対象外に漏らさない")
}

func TestSearchByTag_NonFollowerExcludesFollowers(t *testing.T) {
	h, noteRepo, _ := newExtraHandler(t)
	seedSearchVisibilityNotes(noteRepo)
	// stranger は follow なし / specified 対象外 -> public のみ。
	ids := searchTagIDs(t, postExtra(h.SearchByTag, `{"tag":"tag"}`, &model.User{ID: "stranger"}))
	assert.True(t, ids["t_pub"])
	assert.False(t, ids["t_fol"])
	assert.False(t, ids["t_spec"])
}

func TestSearchByTag_FollowerSeesFollowers(t *testing.T) {
	h, noteRepo, _ := newExtraHandler(t)
	seedSearchVisibilityNotes(noteRepo)
	noteRepo.Following = map[string][]string{"follower": {"author"}}
	ids := searchTagIDs(t, postExtra(h.SearchByTag, `{"tag":"tag"}`, &model.User{ID: "follower"}))
	assert.True(t, ids["t_pub"])
	assert.True(t, ids["t_fol"], "follower は followers note を見られる")
	assert.False(t, ids["t_spec"])
}

func TestSearchByTag_SpecifiedTargetAndAuthorSee(t *testing.T) {
	h, noteRepo, _ := newExtraHandler(t)
	seedSearchVisibilityNotes(noteRepo)
	// visibleUserIds 対象は public + specified。
	allowedIDs := searchTagIDs(t, postExtra(h.SearchByTag, `{"tag":"tag"}`, &model.User{ID: "allowed"}))
	assert.True(t, allowedIDs["t_pub"])
	assert.True(t, allowedIDs["t_spec"], "visibleUserIds 対象は specified を見られる")
	assert.False(t, allowedIDs["t_fol"])
	// author 本人は全 visibility を見られる。
	authorIDs := searchTagIDs(t, postExtra(h.SearchByTag, `{"tag":"tag"}`, &model.User{ID: "author"}))
	assert.True(t, authorIDs["t_pub"] && authorIDs["t_fol"] && authorIDs["t_spec"], "author 本人は全て見られる")
}

type failingSearchByTagRepo struct{ *testutil.MockNoteRepository }

func (f *failingSearchByTagRepo) SearchByTag(_, _ string, _ int, _, _ string) ([]*model.Note, error) {
	return nil, testutil.ErrNotFound
}

func TestSearchByTag_RepoError(t *testing.T) {
	idGen, _ := id.NewGenerator("aidx")
	h := NewHandler(&failingSearchByTagRepo{testutil.NewMockNoteRepository()}, nil, nil, nil, nil, nil, nil, nil, idGen)
	rec := postExtra(h.SearchByTag, `{"tag":"x"}`, nil)
	assert.Equal(t, http.StatusOK, rec.Code)
}

// --- Clips ---

func TestClips_Success(t *testing.T) {
	h, _, _ := newExtraHandler(t)
	rec := postExtra(h.Clips, `{"noteId":"n1"}`, nil)
	assert.Equal(t, http.StatusOK, rec.Code)
}

// --- Translate ---

func TestTranslate_NoTranslator(t *testing.T) {
	h, _, _ := newExtraHandler(t)
	rec := postExtra(h.Translate, `{"noteId":"n1","targetLang":"en"}`, nil)
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func TestTranslate_InvalidParam(t *testing.T) {
	h, _, _ := newExtraHandler(t)
	rec := postExtra(h.Translate, `{}`, nil)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// --- ShowPartialBulk ---

// queryService が wire されているとき、public note は visibility filter を
// 通って返ること。
func TestShowPartialBulk_Success(t *testing.T) {
	noteRepo := testutil.NewMockNoteRepository()
	pollRepo := testutil.NewMockPollRepository()
	idGen, _ := id.NewGenerator("aidx")
	createSvc := corenote.NewCreateService(noteRepo, pollRepo, idGen, nil)
	deleteSvc := corenote.NewDeleteService(noteRepo)
	querySvc := corenote.NewQueryService(noteRepo, nil)
	h := NewHandler(noteRepo, createSvc, deleteSvc, querySvc, nil, nil, nil, nil, idGen)

	noteRepo.Notes["n1"] = &model.Note{
		ID: "n1", UserID: "u1", Visibility: model.NoteVisibilityPublic,
		User: &model.User{ID: "u1"},
	}
	rec := postExtra(h.ShowPartialBulk, `{"noteIds":["n1"]}`, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"id":"n1"`,
		"public note must be returned when queryService is wired")
}

func TestShowPartialBulk_Empty(t *testing.T) {
	h, _, _ := newExtraHandler(t)
	rec := postExtra(h.ShowPartialBulk, `{"noteIds":[]}`, nil)
	assert.Equal(t, http.StatusOK, rec.Code)
}

type failingFindManyRepo struct{ *testutil.MockNoteRepository }

func (f *failingFindManyRepo) FindManyByIDsWithUser(_ []string) ([]*model.Note, error) {
	return nil, testutil.ErrNotFound
}

func TestShowPartialBulk_Error(t *testing.T) {
	idGen, _ := id.NewGenerator("aidx")
	h := NewHandler(&failingFindManyRepo{testutil.NewMockNoteRepository()}, nil, nil, nil, nil, nil, nil, nil, idGen)
	rec := postExtra(h.ShowPartialBulk, `{"noteIds":["n1"]}`, nil)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestShowPartialBulk_InvalidParam(t *testing.T) {
	h, _, _ := newExtraHandler(t)
	rec := postExtra(h.ShowPartialBulk, `invalid`, nil)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// queryService 未配線時は note 行があっても空配列を返す fail-closed 挙動
// を担保する (Devin #529 FLAG-1、Show / BulkShow と同種の defensive)。
// router.go で RequireAuth() 無しに公開している endpoint なので、
// followers / specified visibility のノートを匿名閲覧者に漏らさないため
// に重要。
func TestShowPartialBulk_NoQueryServiceRejects(t *testing.T) {
	h, noteRepo, _ := newExtraHandler(t) // newExtraHandler は queryService=nil
	noteRepo.Notes["n1"] = &model.Note{
		ID: "n1", UserID: "u1", Visibility: model.NoteVisibilityPublic,
		User: &model.User{ID: "u1"},
	}
	rec := postExtra(h.ShowPartialBulk, `{"noteIds":["n1"]}`, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "[]\n", rec.Body.String(),
		"ShowPartialBulk must return [] when queryService is unwired so notes never bypass visibility filtering")
}

// --- Failing repo tests ---

type failingFavCreateRepo struct {
	*testutil.MockNoteFavoriteRepository
}

func (f *failingFavCreateRepo) Create(_ *model.NoteFavorite) error { return testutil.ErrNotFound }

func TestFavoritesCreate_CreateError(t *testing.T) {
	noteRepo := testutil.NewMockNoteRepository()
	noteRepo.Notes["n1"] = &model.Note{ID: "n1"}
	idGen, _ := id.NewGenerator("aidx")
	h := NewHandler(noteRepo, nil, nil, nil, nil, nil, nil, nil, idGen)
	h.SetFavoriteRepo(&failingFavCreateRepo{testutil.NewMockNoteFavoriteRepository()})
	rec := postExtra(h.FavoritesCreate, `{"noteId":"n1"}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

type failingFavDeleteRepo struct {
	*testutil.MockNoteFavoriteRepository
}

func (f *failingFavDeleteRepo) Delete(_, _ string) error { return testutil.ErrNotFound }

func TestFavoritesDelete_Error(t *testing.T) {
	idGen, _ := id.NewGenerator("aidx")
	h := NewHandler(testutil.NewMockNoteRepository(), nil, nil, nil, nil, nil, nil, nil, idGen)
	h.SetFavoriteRepo(&failingFavDeleteRepo{testutil.NewMockNoteFavoriteRepository()})
	rec := postExtra(h.FavoritesDelete, `{"noteId":"n1"}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

type failingFeaturedRepo struct{ *testutil.MockNoteRepository }

func (f *failingFeaturedRepo) ListFeatured(_, _ string, _, _ int) ([]*model.Note, error) {
	return nil, testutil.ErrNotFound
}

func TestFeatured_Error(t *testing.T) {
	idGen, _ := id.NewGenerator("aidx")
	h := NewHandler(&failingFeaturedRepo{testutil.NewMockNoteRepository()}, nil, nil, nil, nil, nil, nil, nil, idGen)
	rec := postExtra(h.Featured, `{}`, nil)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

type failingDeleteNoteRepo struct{ *testutil.MockNoteRepository }

func (f *failingDeleteNoteRepo) Delete(_ *model.Note) error { return testutil.ErrNotFound }

func TestUnrenote_DeleteError(t *testing.T) {
	noteRepo := testutil.NewMockNoteRepository()
	renoteID := "orig"
	noteRepo.Notes["orig"] = &model.Note{ID: "orig", UserID: "u1"}
	noteRepo.Notes["rn1"] = &model.Note{ID: "rn1", UserID: "u1", RenoteID: &renoteID}
	idGen, _ := id.NewGenerator("aidx")
	deleteSvc := corenote.NewDeleteService(&failingDeleteNoteRepo{noteRepo})
	h := NewHandler(noteRepo, nil, deleteSvc, nil, nil, nil, nil, nil, idGen)
	rec := postExtra(h.Unrenote, `{"noteId":"orig"}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

type failingMentionsRepo struct{ *testutil.MockNoteRepository }

func (f *failingMentionsRepo) ListMentions(_ string, _ int, _, _ string) ([]*model.Note, error) {
	return nil, testutil.ErrNotFound
}

func TestMentions_Error(t *testing.T) {
	idGen, _ := id.NewGenerator("aidx")
	h := NewHandler(&failingMentionsRepo{testutil.NewMockNoteRepository()}, nil, nil, nil, nil, nil, nil, nil, idGen)
	rec := postExtra(h.Mentions, `{}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}
