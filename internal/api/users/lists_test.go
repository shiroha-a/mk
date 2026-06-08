package users

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListsGetMemberships(t *testing.T) {
	h, _ := newTestHandler(t)
	// 認証は router.RequireAuth が 401 を返す責務。ここでは userId 欠落のみ検証。
	assert.Equal(t, http.StatusBadRequest, postStub(h.ListsGetMemberships, `{}`, &model.User{ID: "u1"}).Code)
}

func TestListsGetMemberships_ReturnsOwnedLists(t *testing.T) {
	h, _ := newTestHandler(t)
	repo := testutil.NewMockUserListRepository()
	require.NoError(t, repo.Create(&model.UserList{ID: "l1", UserID: "owner", Name: "favs"}))
	require.NoError(t, repo.Create(&model.UserList{ID: "l2", UserID: "other", Name: "other-favs"}))
	require.NoError(t, repo.AddMember(&model.UserListMembership{UserListID: "l1", UserID: "u2"}))
	require.NoError(t, repo.AddMember(&model.UserListMembership{UserListID: "l2", UserID: "u2"}))
	h.SetUserListRepo(repo)

	rec := postStub(h.ListsGetMemberships, `{"userId":"u2"}`, &model.User{ID: "owner"})
	assert.Equal(t, http.StatusOK, rec.Code)
	var rows []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &rows))
	require.Len(t, rows, 1, "only caller-owned lists should be returned")
	assert.Equal(t, "l1", rows[0]["id"])
}

// --- ListsCreateFromPublic ---

func TestListsCreateFromPublic_InvalidParam(t *testing.T) {
	h, _ := newTestHandler(t)
	rec := postStub(h.ListsCreateFromPublic, `{}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// --- ListsFavorite ---

func TestListsFavorite(t *testing.T) {
	h, _ := newTestHandler(t)
	// listId未指定 → 400
	rec := postStub(h.ListsFavorite, `{}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestListsFavorite_WithRepo(t *testing.T) {
	h, _ := newTestHandler(t)
	favRepo := testutil.NewMockUserListFavoriteRepository()
	h.SetUserListFavoriteRepo(favRepo)
	rec := postStub(h.ListsFavorite, `{"listId":"l1"}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusNoContent, rec.Code)
	exists, _ := favRepo.Exists("u1", "l1")
	assert.True(t, exists)
}

// 既 fav は upstream `users/lists/favorite` 互換で ALREADY_FAVORITED
// (HTTP 400) を返す (#1550)。旧 mk-go は 204 を返していて shape が乖離して
// いた。UUID は favorite.ts の alreadyFavorited と一致。
func TestListsFavorite_AlreadyFavorited(t *testing.T) {
	h, _ := newTestHandler(t)
	favRepo := testutil.NewMockUserListFavoriteRepository()
	favRepo.Favorites["u1:l1"] = &model.UserListFavorite{ID: "f1", UserID: "u1", UserListID: "l1"}
	listRepo := testutil.NewMockUserListRepository()
	listRepo.Lists["l1"] = &model.UserList{ID: "l1", UserID: "u1", Name: "pub", IsPublic: true}
	h.SetUserListFavoriteRepo(favRepo)
	h.SetUserListRepo(listRepo)
	rec := postStub(h.ListsFavorite, `{"listId":"l1"}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "ALREADY_FAVORITED")
	assert.Contains(t, rec.Body.String(), "6425bba0-985b-461e-af1b-518070e72081")
}

func TestListsFavorite_MissingListID(t *testing.T) {
	h, _ := newTestHandler(t)
	favRepo := testutil.NewMockUserListFavoriteRepository()
	h.SetUserListFavoriteRepo(favRepo)
	rec := postStub(h.ListsFavorite, `{}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// 非公開 list を非所有者が favorite しようとしたら 404 NO_SUCH_USER_LIST で
// 弾き、fav row も作らない (#1423)。これが無いと `i/favorites` 経由で
// 他人の private list の existence fingerprint が成立する。
func TestListsFavorite_PrivateNonOwner(t *testing.T) {
	h, _ := newTestHandler(t)
	favRepo := testutil.NewMockUserListFavoriteRepository()
	listRepo := testutil.NewMockUserListRepository()
	listRepo.Lists["l1"] = &model.UserList{ID: "l1", UserID: "other", Name: "secret", IsPublic: false}
	h.SetUserListFavoriteRepo(favRepo)
	h.SetUserListRepo(listRepo)

	rec := postStub(h.ListsFavorite, `{"listId":"l1"}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Contains(t, rec.Body.String(), "NO_SUCH_USER_LIST")
	// Misskey TS `users/lists/favorite` 固有の UUID (show の NO_SUCH_LIST とは別)。
	assert.Contains(t, rec.Body.String(), "7dbaf3cf-7b42-4b8f-b431-b3919e580dbe")
	exists, _ := favRepo.Exists("u1", "l1")
	assert.False(t, exists, "fav row should not be created for private non-owner list")
}

// TS favorite は `exists({ id, isPublic: true })` ゲートのため、所有者
// 本人でも private list は favorite 不可 (#1423)。owner 分岐を入れると
// TS と挙動が乖離するためここで 404 を期待値として固定する。
func TestListsFavorite_PrivateOwner(t *testing.T) {
	h, _ := newTestHandler(t)
	favRepo := testutil.NewMockUserListFavoriteRepository()
	listRepo := testutil.NewMockUserListRepository()
	listRepo.Lists["l1"] = &model.UserList{ID: "l1", UserID: "u1", Name: "my-secret", IsPublic: false}
	h.SetUserListFavoriteRepo(favRepo)
	h.SetUserListRepo(listRepo)

	rec := postStub(h.ListsFavorite, `{"listId":"l1"}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Contains(t, rec.Body.String(), "NO_SUCH_USER_LIST")
	assert.Contains(t, rec.Body.String(), "7dbaf3cf-7b42-4b8f-b431-b3919e580dbe")
	exists, _ := favRepo.Exists("u1", "l1")
	assert.False(t, exists, "fav row should not be created for private list even when caller is owner")
}

// 公開 list は所有者でなくても favorite 可 (= 既存の正常系を IsPublic
// gate 導入後も維持する)。
func TestListsFavorite_PublicNonOwner(t *testing.T) {
	h, _ := newTestHandler(t)
	favRepo := testutil.NewMockUserListFavoriteRepository()
	listRepo := testutil.NewMockUserListRepository()
	listRepo.Lists["l1"] = &model.UserList{ID: "l1", UserID: "other", Name: "public-list", IsPublic: true}
	h.SetUserListFavoriteRepo(favRepo)
	h.SetUserListRepo(listRepo)

	rec := postStub(h.ListsFavorite, `{"listId":"l1"}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusNoContent, rec.Code)
	exists, _ := favRepo.Exists("u1", "l1")
	assert.True(t, exists)
}

// list が存在しない場合は 404 NO_SUCH_USER_LIST (#1423)。存在しない listId
// に対する favorite 試行を probing 経路にしない。private/不存在を同じ
// UUID で返すことで両者の区別ができないようにする。
func TestListsFavorite_NoSuchList(t *testing.T) {
	h, _ := newTestHandler(t)
	favRepo := testutil.NewMockUserListFavoriteRepository()
	listRepo := testutil.NewMockUserListRepository()
	h.SetUserListFavoriteRepo(favRepo)
	h.SetUserListRepo(listRepo)

	rec := postStub(h.ListsFavorite, `{"listId":"ghost"}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Contains(t, rec.Body.String(), "NO_SUCH_USER_LIST")
	assert.Contains(t, rec.Body.String(), "7dbaf3cf-7b42-4b8f-b431-b3919e580dbe")
	exists, _ := favRepo.Exists("u1", "ghost")
	assert.False(t, exists)
}

// --- ListsUnfavorite ---

func TestListsUnfavorite(t *testing.T) {
	h, _ := newTestHandler(t)
	// listId未指定 → 400
	rec := postStub(h.ListsUnfavorite, `{}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestListsUnfavorite_WithRepo(t *testing.T) {
	h, _ := newTestHandler(t)
	favRepo := testutil.NewMockUserListFavoriteRepository()
	favRepo.Favorites["u1:l1"] = &model.UserListFavorite{ID: "f1", UserID: "u1", UserListID: "l1"}
	h.SetUserListFavoriteRepo(favRepo)
	rec := postStub(h.ListsUnfavorite, `{"listId":"l1"}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusNoContent, rec.Code)
	exists, _ := favRepo.Exists("u1", "l1")
	assert.False(t, exists)
}

// public list + fav row が揃った正常系。NO_SUCH_USER_LIST / ALREADY_FAVORITED
// gate を導入後も unfavorite が成功して fav row を消すことを固定する (#1550)。
func TestListsUnfavorite_PublicWithFav(t *testing.T) {
	h, _ := newTestHandler(t)
	favRepo := testutil.NewMockUserListFavoriteRepository()
	favRepo.Favorites["u1:l1"] = &model.UserListFavorite{ID: "f1", UserID: "u1", UserListID: "l1"}
	listRepo := testutil.NewMockUserListRepository()
	listRepo.Lists["l1"] = &model.UserList{ID: "l1", UserID: "other", Name: "pub", IsPublic: true}
	h.SetUserListFavoriteRepo(favRepo)
	h.SetUserListRepo(listRepo)
	rec := postStub(h.ListsUnfavorite, `{"listId":"l1"}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusNoContent, rec.Code)
	exists, _ := favRepo.Exists("u1", "l1")
	assert.False(t, exists)
}

// upstream `users/lists/unfavorite` は `exists({ id, isPublic: true })` を
// 満たさない list を NO_SUCH_USER_LIST で弾く。private list は所有者本人でも
// unfavorite 経路では存在を隠す (UUID は favorite のそれとは別、#1550)。
func TestListsUnfavorite_PrivateNoSuchList(t *testing.T) {
	h, _ := newTestHandler(t)
	favRepo := testutil.NewMockUserListFavoriteRepository()
	favRepo.Favorites["u1:l1"] = &model.UserListFavorite{ID: "f1", UserID: "u1", UserListID: "l1"}
	listRepo := testutil.NewMockUserListRepository()
	listRepo.Lists["l1"] = &model.UserList{ID: "l1", UserID: "u1", Name: "secret", IsPublic: false}
	h.SetUserListFavoriteRepo(favRepo)
	h.SetUserListRepo(listRepo)
	rec := postStub(h.ListsUnfavorite, `{"listId":"l1"}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Contains(t, rec.Body.String(), "NO_SUCH_USER_LIST")
	assert.Contains(t, rec.Body.String(), "baedb33e-76b8-4b0c-86a8-9375c0a7b94b")
	// list gate で弾かれるので fav row は残ったまま。
	exists, _ := favRepo.Exists("u1", "l1")
	assert.True(t, exists)
}

// 存在しない list への unfavorite も NO_SUCH_USER_LIST (private と同 UUID で
// 区別不能にする、#1550)。
func TestListsUnfavorite_NoSuchList(t *testing.T) {
	h, _ := newTestHandler(t)
	favRepo := testutil.NewMockUserListFavoriteRepository()
	listRepo := testutil.NewMockUserListRepository()
	h.SetUserListFavoriteRepo(favRepo)
	h.SetUserListRepo(listRepo)
	rec := postStub(h.ListsUnfavorite, `{"listId":"ghost"}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Contains(t, rec.Body.String(), "NO_SUCH_USER_LIST")
	assert.Contains(t, rec.Body.String(), "baedb33e-76b8-4b0c-86a8-9375c0a7b94b")
}

// public list だが fav row が無い場合は upstream notFavorited (code
// ALREADY_FAVORITED, UUID は favorite のそれとは別) を HTTP 400 で返す (#1550)。
func TestListsUnfavorite_NotFavorited(t *testing.T) {
	h, _ := newTestHandler(t)
	favRepo := testutil.NewMockUserListFavoriteRepository()
	listRepo := testutil.NewMockUserListRepository()
	listRepo.Lists["l1"] = &model.UserList{ID: "l1", UserID: "other", Name: "pub", IsPublic: true}
	h.SetUserListFavoriteRepo(favRepo)
	h.SetUserListRepo(listRepo)
	rec := postStub(h.ListsUnfavorite, `{"listId":"l1"}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "ALREADY_FAVORITED")
	assert.Contains(t, rec.Body.String(), "835c4b27-463d-4cfa-969b-a9058678d465")
}

// --- ListsUpdate ---

func TestListsUpdate_MissingListID(t *testing.T) {
	h, _ := newTestHandler(t)
	rec := postStub(h.ListsUpdate, `{}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestListsUpdate_Success(t *testing.T) {
	h, _ := newTestHandler(t)
	listRepo := testutil.NewMockUserListRepository()
	listRepo.Lists["l1"] = &model.UserList{ID: "l1", UserID: "u1", Name: "old"}
	h.SetUserListRepo(listRepo)

	rec := postStub(h.ListsUpdate, `{"listId":"l1","name":"new"}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "new", listRepo.Lists["l1"].Name)
}

func TestListsUpdate_NotFound(t *testing.T) {
	h, _ := newTestHandler(t)
	listRepo := testutil.NewMockUserListRepository()
	h.SetUserListRepo(listRepo)

	rec := postStub(h.ListsUpdate, `{"listId":"ghost"}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestListsUpdate_IsPublic(t *testing.T) {
	h, _ := newTestHandler(t)
	listRepo := testutil.NewMockUserListRepository()
	listRepo.Lists["l1"] = &model.UserList{ID: "l1", UserID: "u1", Name: "test"}
	h.SetUserListRepo(listRepo)

	rec := postStub(h.ListsUpdate, `{"listId":"l1","isPublic":true}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.True(t, listRepo.Lists["l1"].IsPublic)
}

func TestListsUpdate_NotOwner(t *testing.T) {
	h, _ := newTestHandler(t)
	listRepo := testutil.NewMockUserListRepository()
	listRepo.Lists["l1"] = &model.UserList{ID: "l1", UserID: "other", Name: "test"}
	h.SetUserListRepo(listRepo)

	// u1は所有者ではないので404
	rec := postStub(h.ListsUpdate, `{"listId":"l1","name":"hacked"}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestListsUpdate_NilRepo(t *testing.T) {
	h, _ := newTestHandler(t)
	// userListRepo未注入時はgraceful NoContent
	rec := postStub(h.ListsUpdate, `{"listId":"l1"}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

// --- ListsUpdateMembership ---

func TestListsUpdateMembership_MissingParams(t *testing.T) {
	h, _ := newTestHandler(t)
	rec := postStub(h.ListsUpdateMembership, `{}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestListsUpdateMembership_Success(t *testing.T) {
	h, _ := newTestHandler(t)
	listRepo := testutil.NewMockUserListRepository()
	listRepo.Lists["l1"] = &model.UserList{ID: "l1", UserID: "u1", Name: "test"}
	listRepo.Members = append(listRepo.Members, &model.UserListMembership{ID: "m1", UserListID: "l1", UserID: "u2"})
	h.SetUserListRepo(listRepo)

	rec := postStub(h.ListsUpdateMembership, `{"listId":"l1","userId":"u2","withReplies":true}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

func TestListsUpdateMembership_NotFound(t *testing.T) {
	h, _ := newTestHandler(t)
	listRepo := testutil.NewMockUserListRepository()
	h.SetUserListRepo(listRepo)

	rec := postStub(h.ListsUpdateMembership, `{"listId":"ghost","userId":"u2","withReplies":false}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestListsUpdateMembership_NotOwner(t *testing.T) {
	h, _ := newTestHandler(t)
	listRepo := testutil.NewMockUserListRepository()
	listRepo.Lists["l1"] = &model.UserList{ID: "l1", UserID: "other", Name: "test"}
	h.SetUserListRepo(listRepo)

	rec := postStub(h.ListsUpdateMembership, `{"listId":"l1","userId":"u2","withReplies":true}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestListsUpdateMembership_NilRepo(t *testing.T) {
	h, _ := newTestHandler(t)
	rec := postStub(h.ListsUpdateMembership, `{"listId":"l1","userId":"u2","withReplies":true}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

func TestListsUpdateMembership_MemberNotFound(t *testing.T) {
	h, _ := newTestHandler(t)
	listRepo := testutil.NewMockUserListRepository()
	listRepo.Lists["l1"] = &model.UserList{ID: "l1", UserID: "u1", Name: "test"}
	// メンバーが存在しないためUpdateMembershipがErrNotFoundを返す→404
	h.SetUserListRepo(listRepo)

	rec := postStub(h.ListsUpdateMembership, `{"listId":"l1","userId":"ghost","withReplies":true}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusNotFound, rec.Code)
}
