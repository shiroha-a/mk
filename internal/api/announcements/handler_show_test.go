package announcements_test

import (
	"net/http"
	"testing"

	"github.com/shiroha-a/mk/internal/model"
	"github.com/stretchr/testify/assert"
)

// Show エンドポイントの 3 経路 (bad param / not found / ok) をカバーする。
// 0% だった handler_show.go を完全に exercise する目的。
func TestShow_InvalidParam(t *testing.T) {
	h, _ := newTestHandler(t)
	rec := doPost(h.Show, `{}`, nil)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestShow_NotFound(t *testing.T) {
	h, _ := newTestHandler(t)
	rec := doPost(h.Show, `{"announcementId":"ghost"}`, nil)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestShow_Success(t *testing.T) {
	h, repo := newTestHandler(t)
	repo.Items["a1"] = &model.Announcement{ID: "a1", Title: "hi", Text: "hello", IsActive: true}
	rec := doPost(h.Show, `{"announcementId":"a1"}`, nil)
	assert.Equal(t, http.StatusOK, rec.Code)
}

// upstream Misskey TS 2026.5.4 fix: userId-targeted announcement に対して
// 非ログイン user (me == nil) は 404 を返す。旧 mk-go では Show 経路に権限
// check が一切なく、admin が特定 user に向けた通知が漏れる drop-in regression
// 状態だった (#1164 Phase C)。
func TestShow_UserTargeted_AnonymousReturns404(t *testing.T) {
	h, repo := newTestHandler(t)
	uid := "alice"
	repo.Items["a1"] = &model.Announcement{ID: "a1", Title: "private", Text: "to alice", IsActive: true, UserID: &uid}

	rec := doPost(h.Show, `{"announcementId":"a1"}`, nil)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// upstream 2026.5.3 以前から閉じている穴 (= 他人宛 announcement へのアクセス)
// が mk-go 単体では未対応だったため同時に塞ぐ。
func TestShow_UserTargeted_OtherUserReturns404(t *testing.T) {
	h, repo := newTestHandler(t)
	uid := "alice"
	repo.Items["a1"] = &model.Announcement{ID: "a1", Title: "private", Text: "to alice", IsActive: true, UserID: &uid}

	bob := &model.User{ID: "bob"}
	rec := doPost(h.Show, `{"announcementId":"a1"}`, bob)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// 当該 user 本人は閲覧できる (= upstream の挙動と一致)。
func TestShow_UserTargeted_OwnerReturnsOK(t *testing.T) {
	h, repo := newTestHandler(t)
	uid := "alice"
	repo.Items["a1"] = &model.Announcement{ID: "a1", Title: "private", Text: "to alice", IsActive: true, UserID: &uid}

	alice := &model.User{ID: "alice"}
	rec := doPost(h.Show, `{"announcementId":"a1"}`, alice)
	assert.Equal(t, http.StatusOK, rec.Code)
}
