package following

import (
	"net/http"
	"testing"

	corefollowing "github.com/shiroha-a/mk/internal/core/following"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/stretchr/testify/assert"
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
