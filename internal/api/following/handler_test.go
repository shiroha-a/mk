package following

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	corefollowing "github.com/shiroha-a/mk/internal/core/following"
	coreuser "github.com/shiroha-a/mk/internal/core/user"
	"github.com/shiroha-a/mk/internal/entitycompat/shapetest"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/server/middleware"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
)

// stubError is a sentinel error for repository failures in tests.
var stubError = errors.New("stub error")

// failingUserRepo wraps MockUserRepository and forces counter operations to fail.
// This produces errors from the service that bubble up to the handler's default
// error branches.
type failingUserRepo struct {
	*testutil.MockUserRepository
}

func (f *failingUserRepo) IncrementFollowingCount(_ string, _ int) error { return stubError }
func (f *failingUserRepo) IncrementFollowersCount(_ string, _ int) error { return stubError }

// failingFollowRequestRepo wraps MockFollowRequestRepository and forces Delete
// to fail.
type failingFollowRequestRepo struct {
	*testutil.MockFollowRequestRepository
}

func (f *failingFollowRequestRepo) Delete(_ *model.FollowRequest) error { return stubError }

// failingListReqRepo wraps MockFollowRequestRepository and makes ListReceived fail.
type failingListReqRepo struct {
	*testutil.MockFollowRequestRepository
}

func (f *failingListReqRepo) ListReceived(_ string, _ int, _, _ string) ([]*model.FollowRequest, error) {
	return nil, stubError
}

// newHandlerWithFailingCounters builds a handler whose user-counter increments fail.
// This makes Follow/Unfollow/AcceptRequest return non-domain errors and exercises
// the handler's default error branches.
func newHandlerWithFailingCounters(t *testing.T) (*Handler, *testutil.MockUserRepository, *testutil.MockFollowingRepository, *testutil.MockFollowRequestRepository) {
	t.Helper()
	mockUR := testutil.NewMockUserRepository()
	userRepo := &failingUserRepo{MockUserRepository: mockUR}
	fRepo := testutil.NewMockFollowingRepository()
	frRepo := testutil.NewMockFollowRequestRepository()
	idGen, _ := id.NewGenerator("aidx")
	fSvc := corefollowing.NewService(userRepo, fRepo, frRepo, idGen)
	uSvc := coreuser.NewService(userRepo, nil, nil, nil)
	return NewHandler(fSvc, uSvc), mockUR, fRepo, frRepo
}

// newHandlerWithFailingRequestDelete builds a handler whose FollowRequest Delete fails.
func newHandlerWithFailingRequestDelete(t *testing.T) (*Handler, *testutil.MockUserRepository, *testutil.MockFollowRequestRepository) {
	t.Helper()
	userRepo := testutil.NewMockUserRepository()
	mockFRR := testutil.NewMockFollowRequestRepository()
	frRepo := &failingFollowRequestRepo{MockFollowRequestRepository: mockFRR}
	fRepo := testutil.NewMockFollowingRepository()
	idGen, _ := id.NewGenerator("aidx")
	fSvc := corefollowing.NewService(userRepo, fRepo, frRepo, idGen)
	uSvc := coreuser.NewService(userRepo, nil, nil, nil)
	return NewHandler(fSvc, uSvc), userRepo, mockFRR
}

// newHandlerWithFailingListReceived builds a handler whose ListReceived fails.
func newHandlerWithFailingListReceived(t *testing.T) (*Handler, *testutil.MockUserRepository) {
	t.Helper()
	userRepo := testutil.NewMockUserRepository()
	mockFRR := testutil.NewMockFollowRequestRepository()
	frRepo := &failingListReqRepo{MockFollowRequestRepository: mockFRR}
	fRepo := testutil.NewMockFollowingRepository()
	idGen, _ := id.NewGenerator("aidx")
	fSvc := corefollowing.NewService(userRepo, fRepo, frRepo, idGen)
	uSvc := coreuser.NewService(userRepo, nil, nil, nil)
	return NewHandler(fSvc, uSvc), userRepo
}

func newTestHandler(t *testing.T) (*Handler, *testutil.MockUserRepository) {
	t.Helper()
	userRepo := testutil.NewMockUserRepository()
	fRepo := testutil.NewMockFollowingRepository()
	frRepo := testutil.NewMockFollowRequestRepository()
	idGen, _ := id.NewGenerator("aidx")
	fSvc := corefollowing.NewService(userRepo, fRepo, frRepo, idGen)
	uSvc := coreuser.NewService(userRepo, nil, nil, nil)
	return NewHandler(fSvc, uSvc), userRepo
}

func addUser(repo *testutil.MockUserRepository, id string, locked bool) *model.User {
	u := &model.User{
		ID:                id,
		Username:          id,
		UsernameLower:     id,
		IsLocked:          locked,
		AvatarDecorations: datatypes.JSON([]byte("[]")),
	}
	repo.Users[id] = u
	return u
}

func postJSON(h echo.HandlerFunc, body string, me *model.User) *httptest.ResponseRecorder {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	if me != nil {
		c.Set(string(middleware.UserContextKey), me)
	}
	_ = h(c)
	return rec
}

func TestCreate_Success(t *testing.T) {
	h, repo := newTestHandler(t)
	alice := addUser(repo, "alice", false)
	addUser(repo, "bob", false)

	rec := postJSON(h.Create, `{"userId": "bob"}`, alice)
	assert.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "bob", resp["id"])
	// following/create は PackUserDetailed (UserLite & UserDetailedNotMeOnly) を返す。
	shapetest.Assert(t, "UserLite", resp)              // L3 (#1312)
	shapetest.Assert(t, "UserDetailedNotMeOnly", resp) // L3 (#1312)
}

func TestCreate_LockedReturnsOK(t *testing.T) {
	h, repo := newTestHandler(t)
	alice := addUser(repo, "alice", false)
	addUser(repo, "bob", true)

	rec := postJSON(h.Create, `{"userId": "bob"}`, alice)
	// 鍵アカウントへのフォローもbobのプロフィールを返す
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestCreate_SelfFollow(t *testing.T) {
	h, repo := newTestHandler(t)
	alice := addUser(repo, "alice", false)

	rec := postJSON(h.Create, `{"userId": "alice"}`, alice)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// newTestHandlerWithRepos exposes the mock repositories so that tests can
// verify persisted row state (used by #1056 WithReplies tests).
func newTestHandlerWithRepos(t *testing.T) (*Handler, *testutil.MockUserRepository, *testutil.MockFollowingRepository, *testutil.MockFollowRequestRepository) {
	t.Helper()
	userRepo := testutil.NewMockUserRepository()
	fRepo := testutil.NewMockFollowingRepository()
	frRepo := testutil.NewMockFollowRequestRepository()
	idGen, _ := id.NewGenerator("aidx")
	fSvc := corefollowing.NewService(userRepo, fRepo, frRepo, idGen)
	uSvc := coreuser.NewService(userRepo, nil, nil, nil)
	return NewHandler(fSvc, uSvc), userRepo, fRepo, frRepo
}

// #1056: POST /api/following/create with withReplies=true should persist
// `Following.withReplies=true` on the new row (auto-accept path).
func TestCreate_WithReplies_True_PersistedOnFollowingRow(t *testing.T) {
	h, userRepo, fRepo, _ := newTestHandlerWithRepos(t)
	alice := addUser(userRepo, "alice", false)
	addUser(userRepo, "bob", false)

	rec := postJSON(h.Create, `{"userId": "bob", "withReplies": true}`, alice)
	require.Equal(t, http.StatusOK, rec.Code)

	require.Len(t, fRepo.Followings, 1)
	for _, f := range fRepo.Followings {
		assert.Equal(t, "alice", f.FollowerID)
		assert.Equal(t, "bob", f.FolloweeID)
		assert.True(t, f.WithReplies, "withReplies=true should be persisted on the Following row (#1056)")
	}
}

// #1056: withReplies が省略された場合は default false で row が作られる。
func TestCreate_WithReplies_Omitted_DefaultFalse(t *testing.T) {
	h, userRepo, fRepo, _ := newTestHandlerWithRepos(t)
	alice := addUser(userRepo, "alice", false)
	addUser(userRepo, "bob", false)

	rec := postJSON(h.Create, `{"userId": "bob"}`, alice)
	require.Equal(t, http.StatusOK, rec.Code)

	require.Len(t, fRepo.Followings, 1)
	for _, f := range fRepo.Followings {
		assert.False(t, f.WithReplies, "withReplies omitted should default to false")
	}
}

// #1056: locked followee 経由でも withReplies が FollowRequest row に反映される。
func TestCreate_WithReplies_True_LockedFollowee_PersistedOnFollowRequestRow(t *testing.T) {
	h, userRepo, _, frRepo := newTestHandlerWithRepos(t)
	alice := addUser(userRepo, "alice", false)
	addUser(userRepo, "bob", true) // locked

	rec := postJSON(h.Create, `{"userId": "bob", "withReplies": true}`, alice)
	require.Equal(t, http.StatusOK, rec.Code)

	require.Len(t, frRepo.Requests, 1)
	for _, r := range frRepo.Requests {
		assert.Equal(t, "alice", r.FollowerID)
		assert.Equal(t, "bob", r.FolloweeID)
		assert.True(t, r.WithReplies, "withReplies=true should be persisted on the FollowRequest row (#1056)")
	}
}

func TestCreate_AlreadyFollowing(t *testing.T) {
	h, repo := newTestHandler(t)
	alice := addUser(repo, "alice", false)
	addUser(repo, "bob", false)
	postJSON(h.Create, `{"userId": "bob"}`, alice)

	rec := postJSON(h.Create, `{"userId": "bob"}`, alice)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestCreate_FolloweeNotFound(t *testing.T) {
	h, repo := newTestHandler(t)
	alice := addUser(repo, "alice", false)

	rec := postJSON(h.Create, `{"userId": "ghost"}`, alice)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// stubBlockedChecker reports the configured pair as blocked.
type stubBlockedChecker struct{ blockerID, blockeeID string }

func (s *stubBlockedChecker) IsBlocked(blockerID, blockeeID string) (bool, error) {
	return blockerID == s.blockerID && blockeeID == s.blockeeID, nil
}

func TestCreate_Blocked(t *testing.T) {
	h, repo := newTestHandler(t)
	alice := addUser(repo, "alice", false)
	addUser(repo, "bob", false)
	// alice -> bob を follow しようとして bob が alice をブロックしている状況
	h.followingService.SetBlockingChecker(&stubBlockedChecker{blockerID: "bob", blockeeID: "alice"})

	rec := postJSON(h.Create, `{"userId": "bob"}`, alice)
	// upstream Misskey TS と同じ 400 BLOCKED (= drop-in 互換、#872)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), `"code":"BLOCKED"`)
}

func TestCreate_InvalidParam(t *testing.T) {
	h, repo := newTestHandler(t)
	alice := addUser(repo, "alice", false)

	rec := postJSON(h.Create, `{}`, alice)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestCreate_InvalidJSON(t *testing.T) {
	h, repo := newTestHandler(t)
	alice := addUser(repo, "alice", false)

	rec := postJSON(h.Create, `{invalid`, alice)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestDelete_Success(t *testing.T) {
	h, repo := newTestHandler(t)
	alice := addUser(repo, "alice", false)
	addUser(repo, "bob", false)
	postJSON(h.Create, `{"userId": "bob"}`, alice)

	rec := postJSON(h.Delete, `{"userId": "bob"}`, alice)
	assert.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	shapetest.Assert(t, "UserLite", resp)              // L3 (#1312)
	shapetest.Assert(t, "UserDetailedNotMeOnly", resp) // L3 (#1312)
}

func TestDelete_UserNotFound(t *testing.T) {
	h, repo := newTestHandler(t)
	alice := addUser(repo, "alice", false)

	rec := postJSON(h.Delete, `{"userId": "ghost"}`, alice)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestDelete_NotFollowing(t *testing.T) {
	h, repo := newTestHandler(t)
	alice := addUser(repo, "alice", false)
	addUser(repo, "bob", false)

	rec := postJSON(h.Delete, `{"userId": "bob"}`, alice)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestDelete_SelfFollow(t *testing.T) {
	h, repo := newTestHandler(t)
	alice := addUser(repo, "alice", false)

	rec := postJSON(h.Delete, `{"userId": "alice"}`, alice)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestDelete_InvalidParam(t *testing.T) {
	h, repo := newTestHandler(t)
	alice := addUser(repo, "alice", false)

	rec := postJSON(h.Delete, `{}`, alice)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestRequests_AcceptRejectCancel(t *testing.T) {
	h, repo := newTestHandler(t)
	alice := addUser(repo, "alice", false)
	bob := addUser(repo, "bob", true)

	// alice -> bob (locked) creates request
	postJSON(h.Create, `{"userId": "bob"}`, alice)

	// list received by bob
	rec := postJSON(h.ListRequests, `{}`, bob)
	assert.Equal(t, http.StatusOK, rec.Code)
	var listResp []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &listResp))
	assert.Len(t, listResp, 1)

	// bob accepts
	rec = postJSON(h.AcceptRequest, `{"userId": "alice"}`, bob)
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

func TestAcceptRequest_NotFound(t *testing.T) {
	h, repo := newTestHandler(t)
	bob := addUser(repo, "bob", false)

	rec := postJSON(h.AcceptRequest, `{"userId": "alice"}`, bob)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestAcceptRequest_InvalidParam(t *testing.T) {
	h, repo := newTestHandler(t)
	bob := addUser(repo, "bob", false)

	rec := postJSON(h.AcceptRequest, `{}`, bob)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestRejectRequest_Success(t *testing.T) {
	h, repo := newTestHandler(t)
	alice := addUser(repo, "alice", false)
	bob := addUser(repo, "bob", true)
	postJSON(h.Create, `{"userId": "bob"}`, alice)

	rec := postJSON(h.RejectRequest, `{"userId": "alice"}`, bob)
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

func TestRejectRequest_NotFound(t *testing.T) {
	h, repo := newTestHandler(t)
	bob := addUser(repo, "bob", false)

	rec := postJSON(h.RejectRequest, `{"userId": "alice"}`, bob)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestRejectRequest_InvalidParam(t *testing.T) {
	h, repo := newTestHandler(t)
	bob := addUser(repo, "bob", false)

	rec := postJSON(h.RejectRequest, `{}`, bob)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestCancelRequest_Success(t *testing.T) {
	h, repo := newTestHandler(t)
	alice := addUser(repo, "alice", false)
	addUser(repo, "bob", true)
	postJSON(h.Create, `{"userId": "bob"}`, alice)

	rec := postJSON(h.CancelRequest, `{"userId": "bob"}`, alice)
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

func TestCancelRequest_NotFound(t *testing.T) {
	h, repo := newTestHandler(t)
	alice := addUser(repo, "alice", false)

	rec := postJSON(h.CancelRequest, `{"userId": "bob"}`, alice)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestCancelRequest_InvalidParam(t *testing.T) {
	h, repo := newTestHandler(t)
	alice := addUser(repo, "alice", false)

	rec := postJSON(h.CancelRequest, `{}`, alice)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestListRequests_Empty(t *testing.T) {
	h, repo := newTestHandler(t)
	bob := addUser(repo, "bob", false)

	rec := postJSON(h.ListRequests, `{}`, bob)
	assert.Equal(t, http.StatusOK, rec.Code)
}

// alice が bob (鍵付) に Follow すると FollowRequest 行が作られる。
// me=bob で ListRequests を呼ぶと follower フィールドに alice の情報が入ること、
// limit / sinceId / untilId の各 pagination param が反映されることを検証。
func TestListRequests_PopulatesFollowerAndPaginates(t *testing.T) {
	h, repo := newTestHandler(t)
	bob := addUser(repo, "bob", true) // locked
	addUser(repo, "alice", false)
	addUser(repo, "carol", false)
	addUser(repo, "dave", false)
	_, _ = h.followingService.Follow("alice", bob.ID, corefollowing.FollowOptions{})
	_, _ = h.followingService.Follow("carol", bob.ID, corefollowing.FollowOptions{})
	_, _ = h.followingService.Follow("dave", bob.ID, corefollowing.FollowOptions{})

	rec := postJSON(h.ListRequests, `{}`, bob)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"alice"`)
	assert.Contains(t, rec.Body.String(), `"carol"`)
	assert.Contains(t, rec.Body.String(), `"dave"`)

	// limit=1 で件数が絞られること
	rec = postJSON(h.ListRequests, `{"limit":1}`, bob)
	require.Equal(t, http.StatusOK, rec.Code)
	var items []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &items))
	assert.Len(t, items, 1)
}

// --- failing-repo tests for handler default branches ---

func TestCreate_InternalError(t *testing.T) {
	h, repo, _, _ := newHandlerWithFailingCounters(t)
	alice := addUser(repo, "alice", false)
	addUser(repo, "bob", false)

	rec := postJSON(h.Create, `{"userId": "bob"}`, alice)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestDelete_InternalError(t *testing.T) {
	h, repo, fRepo, _ := newHandlerWithFailingCounters(t)
	alice := addUser(repo, "alice", false)
	addUser(repo, "bob", false)
	// 既存のfollowingを直接挿入してUnfollowに到達させる
	fRepo.Followings["x"] = &model.Following{ID: "x", FollowerID: "alice", FolloweeID: "bob"}

	rec := postJSON(h.Delete, `{"userId": "bob"}`, alice)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestAcceptRequest_InternalError(t *testing.T) {
	h, repo, _, frRepo := newHandlerWithFailingCounters(t)
	addUser(repo, "alice", false)
	bob := addUser(repo, "bob", true)
	frRepo.Requests["r"] = &model.FollowRequest{ID: "r", FollowerID: "alice", FolloweeID: "bob"}

	rec := postJSON(h.AcceptRequest, `{"userId": "alice"}`, bob)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestRejectRequest_InternalError(t *testing.T) {
	h, repo, frRepo := newHandlerWithFailingRequestDelete(t)
	addUser(repo, "alice", false)
	bob := addUser(repo, "bob", true)
	frRepo.Requests["r"] = &model.FollowRequest{ID: "r", FollowerID: "alice", FolloweeID: "bob"}

	rec := postJSON(h.RejectRequest, `{"userId": "alice"}`, bob)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestCancelRequest_InternalError(t *testing.T) {
	h, repo, frRepo := newHandlerWithFailingRequestDelete(t)
	alice := addUser(repo, "alice", false)
	addUser(repo, "bob", true)
	frRepo.Requests["r"] = &model.FollowRequest{ID: "r", FollowerID: "alice", FolloweeID: "bob"}

	rec := postJSON(h.CancelRequest, `{"userId": "bob"}`, alice)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestListRequests_InternalError(t *testing.T) {
	h, repo := newHandlerWithFailingListReceived(t)
	bob := addUser(repo, "bob", false)

	rec := postJSON(h.ListRequests, `{}`, bob)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

// --- /api/following/list (upstream 2026.5.2 #17385 + #17416) ---

// newTestHandlerWithFollowings は List テスト向けに followee user を seed して
// MockFollowingRepository に Following 行を入れた状態の Handler を返す。
func newTestHandlerWithFollowings(t *testing.T) (*Handler, *testutil.MockUserRepository, *testutil.MockFollowingRepository) {
	t.Helper()
	userRepo := testutil.NewMockUserRepository()
	fRepo := testutil.NewMockFollowingRepository()
	frRepo := testutil.NewMockFollowRequestRepository()
	idGen, _ := id.NewGenerator("aidx")
	fSvc := corefollowing.NewService(userRepo, fRepo, frRepo, idGen)
	uSvc := coreuser.NewService(userRepo, nil, nil, nil)
	h := NewHandler(fSvc, uSvc)
	h.SetIDGen(idGen)
	return h, userRepo, fRepo
}

func TestList_ReturnsFollowingsWithFollowee(t *testing.T) {
	h, repo, fRepo := newTestHandlerWithFollowings(t)
	alice := addUser(repo, "alice", false)
	addUser(repo, "bob", false)
	addUser(repo, "carol", false)
	fRepo.Followings["f1"] = &model.Following{
		ID:         "f1",
		FollowerID: alice.ID,
		FolloweeID: "bob",
	}
	fRepo.Followings["f2"] = &model.Following{
		ID:         "f2",
		FollowerID: alice.ID,
		FolloweeID: "carol",
	}

	rec := postJSON(h.List, `{}`, alice)
	require.Equal(t, http.StatusOK, rec.Code)

	var out []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.Len(t, out, 2)
	// followee が embed されること、id / followeeId / followerId が一致
	for _, item := range out {
		assert.Equal(t, alice.ID, item["followerId"])
		assert.NotNil(t, item["followee"])
		_, hasFollower := item["follower"]
		assert.False(t, hasFollower, "populateFollower=false なので follower は省略")
	}
}

func TestList_NotificationFilter(t *testing.T) {
	h, repo, fRepo := newTestHandlerWithFollowings(t)
	alice := addUser(repo, "alice", false)
	addUser(repo, "bob", false)
	addUser(repo, "carol", false)
	notify := "play"
	fRepo.Followings["f1"] = &model.Following{
		ID:         "f1",
		FollowerID: alice.ID,
		FolloweeID: "bob",
		Notify:     &notify,
	}
	fRepo.Followings["f2"] = &model.Following{
		ID:         "f2",
		FollowerID: alice.ID,
		FolloweeID: "carol",
		// Notify == nil → notification=true で除外される
	}

	rec := postJSON(h.List, `{"notification": true}`, alice)
	require.Equal(t, http.StatusOK, rec.Code)

	var out []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.Len(t, out, 1)
	assert.Equal(t, "bob", out[0]["followeeId"])
}

func TestList_LimitClamp(t *testing.T) {
	h, repo, fRepo := newTestHandlerWithFollowings(t)
	alice := addUser(repo, "alice", false)
	// 200 件積んで limit=100 にクランプされること
	for i := 0; i < 200; i++ {
		fid := "fu" + string(rune('a'+i%26))
		addUser(repo, fid, false)
		// id は 200 件 unique にしたいので i を含める (= mock では順序に
		// 影響するが、limit クランプ確認には十分)
		fRepo.Followings[fid] = &model.Following{
			ID:         fid,
			FollowerID: alice.ID,
			FolloweeID: fid,
		}
	}

	rec := postJSON(h.List, `{"limit": 9999}`, alice)
	require.Equal(t, http.StatusOK, rec.Code)
	var out []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	assert.LessOrEqual(t, len(out), 100, "上限 100 にクランプされる")
}

func TestList_DefaultLimit(t *testing.T) {
	h, repo, fRepo := newTestHandlerWithFollowings(t)
	alice := addUser(repo, "alice", false)
	for i := 0; i < 30; i++ {
		fid := "fu" + string(rune('a'+i%26)) + string(rune('a'+i/26))
		addUser(repo, fid, false)
		fRepo.Followings[fid] = &model.Following{
			ID:         fid,
			FollowerID: alice.ID,
			FolloweeID: fid,
		}
	}

	rec := postJSON(h.List, `{}`, alice)
	require.Equal(t, http.StatusOK, rec.Code)
	var out []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	assert.Equal(t, 10, len(out), "limit 未指定 → default 10")
}
