package i

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPageLikes(t *testing.T) {
	h, _ := newExtraHandler(t)
	assert.Equal(t, http.StatusOK, postExtra(h.PageLikes, `{}`, stubUser).Code)
}

// --- P4-6 (#166): i/page-likes ---

// TestPageLikes_WithRepo: pageRepo / userService 未配線時は fail-soft で
// 空配列を返す。配線後の本格 case は TestPageLikes_FullShape を参照 (#1136)。
func TestPageLikes_WithRepo(t *testing.T) {
	h, _ := newExtraHandler(t)
	pageLike := testutil.NewMockPageLikeRepository()
	require.NoError(t, pageLike.Create(&model.PageLike{ID: "pl1", UserID: stubUser.ID, PageID: "pg1"}))
	h.SetPageLikeRepo(pageLike)
	rec := postExtra(h.PageLikes, `{}`, stubUser)
	assert.Equal(t, http.StatusOK, rec.Code)
	var got []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Empty(t, got, "pageRepo / userService 未配線時は fail-soft で空")
}

// TestPageLikes_FullShape guards #1136: like row が `{ id, page: <full Page> }`
// shape で返り、page.user.username が含まれることを確認 (frontend MkPagePreview
// の `like.page.user.username` 参照に対応)。
func TestPageLikes_FullShape(t *testing.T) {
	h, userRepo := newExtraHandler(t)
	// page owner を userRepo に登録 → userService.FindManyByIDs で解決される。
	userRepo.Users["author"] = &model.User{ID: "author", Username: "author", UsernameLower: "author"}
	// page 本体を登録 → pageRepo.FindManyByIDs で解決される。
	pageRepo := testutil.NewMockPageRepository()
	require.NoError(t, pageRepo.Create(&model.Page{
		ID: "pg1", UserID: "author", Title: "T", Name: "n",
		Visibility: model.PageVisibilityPublic,
	}))
	h.SetPageRepo(pageRepo)
	pageLike := testutil.NewMockPageLikeRepository()
	require.NoError(t, pageLike.Create(&model.PageLike{ID: "pl1", UserID: stubUser.ID, PageID: "pg1"}))
	h.SetPageLikeRepo(pageLike)
	rec := postExtra(h.PageLikes, `{}`, stubUser)
	require.Equal(t, http.StatusOK, rec.Code)
	var got []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Len(t, got, 1)
	assert.Equal(t, "pl1", got[0]["id"])
	page, ok := got[0]["page"].(map[string]any)
	require.True(t, ok, "like row must include page field (#1136)")
	assert.Equal(t, "pg1", page["id"])
	user, ok := page["user"].(map[string]any)
	require.True(t, ok, "page must include user field (#1136)")
	assert.Equal(t, "author", user["username"])
}

// TestPageLikes_DropsDanglingLike: page が削除された後の dangling like 行は
// drop される (frontend crash を防ぐ fail-soft)。
func TestPageLikes_DropsDanglingLike(t *testing.T) {
	h, _ := newExtraHandler(t)
	pageRepo := testutil.NewMockPageRepository()
	// 意図的に page を登録しない → FindManyByIDs で見つからず drop されるはず。
	h.SetPageRepo(pageRepo)
	pageLike := testutil.NewMockPageLikeRepository()
	require.NoError(t, pageLike.Create(&model.PageLike{ID: "pl1", UserID: stubUser.ID, PageID: "ghost"}))
	h.SetPageLikeRepo(pageLike)
	rec := postExtra(h.PageLikes, `{}`, stubUser)
	require.Equal(t, http.StatusOK, rec.Code)
	var got []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Empty(t, got, "dangling like (page missing) must be dropped, not emitted with null page")
}
