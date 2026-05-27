package i

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/shiroha-a/mk/internal/entitycompat/shapetest"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubGalleryRepo implements i.GalleryRepository.
type stubGalleryRepo struct {
	posts []*model.GalleryPost
	likes []*model.GalleryLike
	err   error
}

func (s *stubGalleryRepo) ListByUser(_, _, _ string, _, _ int) ([]*model.GalleryPost, error) {
	return s.posts, s.err
}
func (s *stubGalleryRepo) ListLikesByUser(_, _, _ string, _, _ int) ([]*model.GalleryLike, error) {
	return s.likes, s.err
}
func (s *stubGalleryRepo) FindPostsByIDs(_ []string) ([]*model.GalleryPost, error) {
	return s.posts, s.err
}

func TestGalleryLikes(t *testing.T) {
	h, _ := newExtraHandler(t)
	assert.Equal(t, http.StatusOK, postExtra(h.GalleryLikes, `{}`, stubUser).Code)
}

func TestGalleryPostsI(t *testing.T) {
	h, _ := newExtraHandler(t)
	assert.Equal(t, http.StatusOK, postExtra(h.GalleryPosts, `{}`, stubUser).Code)
}

// --- P4-6 (#166): i/gallery/* ---

func TestGalleryPosts_WithRepo(t *testing.T) {
	h, _ := newExtraHandler(t)
	idGen, _ := id.NewGenerator("aidx")
	postID := idGen.Generate(time.Now())
	h.SetGalleryRepo(&stubGalleryRepo{posts: []*model.GalleryPost{
		{ID: postID, Title: "t", UserID: stubUser.ID},
	}})
	rec := postExtra(h.GalleryPosts, `{}`, stubUser)
	assert.Equal(t, http.StatusOK, rec.Code)
	var got []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Len(t, got, 1)
	assert.Equal(t, postID, got[0]["id"])
	shapetest.Assert(t, "GalleryPost", got[0]) // L3 (#1322)
}

func TestGalleryLikes_WithRepo(t *testing.T) {
	h, _ := newExtraHandler(t)
	// upstream の `{id, post: GalleryPost}` shape を返す経路 (#493)。
	// stubGalleryRepo.FindPostsByIDs は posts スライスをそのまま返すので
	// PostID が一致するエントリを posts にも入れておく。
	h.SetGalleryRepo(&stubGalleryRepo{
		likes: []*model.GalleryLike{
			{ID: "l1", PostID: "p1", UserID: stubUser.ID},
		},
		posts: []*model.GalleryPost{
			{ID: "p1", Title: "t", UserID: stubUser.ID},
		},
	})
	rec := postExtra(h.GalleryLikes, `{}`, stubUser)
	assert.Equal(t, http.StatusOK, rec.Code)
	var got []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Len(t, got, 1)
	post, ok := got[0]["post"].(map[string]any)
	require.True(t, ok, "expected post object embedded")
	assert.Equal(t, "p1", post["id"])
}
