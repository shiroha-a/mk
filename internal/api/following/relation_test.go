package following

import (
	"net/http"
	"testing"

	corefollowing "github.com/shiroha-a/mk/internal/core/following"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInvalidate_MissingUserIDRejected(t *testing.T) {
	h, _ := newTestHandler(t)
	assert.Equal(t, http.StatusBadRequest, postJSON(h.Invalidate, `{}`, &model.User{ID: "admin"}).Code)
}

func TestUpdateFollow_MissingUserIDRejected(t *testing.T) {
	h, _ := newTestHandler(t)
	assert.Equal(t, http.StatusBadRequest, postJSON(h.UpdateFollow, `{}`, &model.User{ID: "u1"}).Code)
}

func TestUpdateFollowAll_NoFieldsNoOp(t *testing.T) {
	h, _ := newTestHandler(t)
	assert.Equal(t, http.StatusNoContent, postJSON(h.UpdateFollowAll, `{}`, &model.User{ID: "u1"}).Code)
}

func TestInvalidate_Success(t *testing.T) {
	h, userRepo := newTestHandler(t)
	// admin に follower を follow させた状態を作る
	admin := &model.User{ID: "admin", Username: "admin", UsernameLower: "admin"}
	follower := &model.User{ID: "follower", Username: "f", UsernameLower: "f"}
	userRepo.Users[admin.ID] = admin
	userRepo.Users[follower.ID] = follower
	_, _ = h.followingService.Follow(follower.ID, admin.ID, corefollowing.FollowOptions{})

	rec := postJSON(h.Invalidate, `{"userId":"follower"}`, admin)
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

func TestInvalidate_NotFollowing(t *testing.T) {
	h, userRepo := newTestHandler(t)
	admin := &model.User{ID: "admin", Username: "admin", UsernameLower: "admin"}
	other := &model.User{ID: "other", Username: "o", UsernameLower: "o"}
	userRepo.Users[admin.ID] = admin
	userRepo.Users[other.ID] = other
	// other は admin をフォローしていない → NOT_FOLLOWING
	rec := postJSON(h.Invalidate, `{"userId":"other"}`, admin)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestUpdateFollow_Success(t *testing.T) {
	h, _ := newTestHandler(t)
	assert.Equal(t, http.StatusNoContent,
		postJSON(h.UpdateFollow, `{"userId":"u2","notify":"normal","withReplies":true}`, &model.User{ID: "u1"}).Code)
}

func TestUpdateFollowAll_WithFields(t *testing.T) {
	h, _ := newTestHandler(t)
	assert.Equal(t, http.StatusNoContent,
		postJSON(h.UpdateFollowAll, `{"notify":"none"}`, &model.User{ID: "u1"}).Code)
}

// upstream Misskey TS 2026.5.2 #17385 互換: notify="none" は SQL NULL に
// 変換されて DB に書かれる。これがないと /api/following/list の
// notification=true filter (= notify IS NOT NULL) が機能しない。
func TestUpdateFollow_NotifyNone_NormalizedToNull(t *testing.T) {
	h, repo, fRepo, _ := newTestHandlerWithRepos(t)
	alice := addUser(repo, "alice", false)
	bob := addUser(repo, "bob", false)
	// 既存 follow + notify=normal 状態を pre-seed
	notify := "normal"
	fRepo.Followings["f1"] = &model.Following{
		ID:         "f1",
		FollowerID: alice.ID,
		FolloweeID: bob.ID,
		Notify:     &notify,
	}

	rec := postJSON(h.UpdateFollow, `{"userId":"bob","notify":"none"}`, alice)
	assert.Equal(t, http.StatusNoContent, rec.Code)
	// notify="none" → DB の Notify 列が nil (= SQL NULL) になる
	assert.Nil(t, fRepo.Followings["f1"].Notify, "notify=none must normalize to nil for following/list notification=true filter")
}

// notify="normal" は文字列のまま保存される (= notify ON 状態を表す)。
func TestUpdateFollow_NotifyNormal_StoredAsString(t *testing.T) {
	h, repo, fRepo, _ := newTestHandlerWithRepos(t)
	alice := addUser(repo, "alice", false)
	bob := addUser(repo, "bob", false)
	fRepo.Followings["f1"] = &model.Following{
		ID:         "f1",
		FollowerID: alice.ID,
		FolloweeID: bob.ID,
	}

	rec := postJSON(h.UpdateFollow, `{"userId":"bob","notify":"normal"}`, alice)
	assert.Equal(t, http.StatusNoContent, rec.Code)
	require.NotNil(t, fRepo.Followings["f1"].Notify)
	assert.Equal(t, "normal", *fRepo.Followings["f1"].Notify)
}
