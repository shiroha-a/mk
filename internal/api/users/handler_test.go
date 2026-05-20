package users

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	corefollowing "github.com/shiroha-a/mk/internal/core/following"
	coreuser "github.com/shiroha-a/mk/internal/core/user"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/server/middleware"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
)

func newTestHandler(t *testing.T) (*Handler, *testutil.MockUserRepository) {
	t.Helper()
	userRepo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	piningRepo := testutil.NewMockUserNotePiningRepository()
	fRepo := testutil.NewMockFollowingRepository()
	frRepo := testutil.NewMockFollowRequestRepository()
	idGen, _ := id.NewGenerator("aidx")
	svc := coreuser.NewService(userRepo, noteRepo, piningRepo, idGen)
	fSvc := corefollowing.NewService(userRepo, fRepo, frRepo, idGen)
	h := NewHandler(svc, fSvc, noteRepo, idGen)
	// followingRepo + followRequestRepo を service と共有する形で wire
	// しておく (#1144 で users/{followers,following} の relation flag +
	// pending request flag の batch lookup が handler 直配線の repo 経由に
	// なったため)。explicit setup が要る test では Set*Repo で上書きする。
	h.SetFollowingRepo(fRepo)
	h.SetFollowRequestRepo(frRepo)
	return h, userRepo
}

func addTestUser(repo *testutil.MockUserRepository) *model.User {
	name := "Test User"
	user := &model.User{
		ID:                "user1",
		Username:          "testuser",
		UsernameLower:     "testuser",
		Name:              &name,
		AvatarDecorations: datatypes.JSON([]byte("[]")),
	}
	repo.Users["user1"] = user
	return user
}

// postStub invokes a handler with the given JSON body, optionally setting an
// authenticated user on the context, and returns the recorded response. Shared
// by per-handler test files (achievements_test.go, content_lists_test.go,
// recommendation_test.go, lists_test.go).
func postStub(handler func(echo.Context) error, body string, user *model.User) *httptest.ResponseRecorder {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	if user != nil {
		c.Set(string(middleware.UserContextKey), user)
	}
	_ = handler(c)
	return rec
}

func TestShow_ByUserID(t *testing.T) {
	h, userRepo := newTestHandler(t)
	addTestUser(userRepo)

	body := `{"userId": "user1"}`
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/users/show", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := h.Show(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "user1", resp["id"])
	assert.Equal(t, "testuser", resp["username"])
	assert.Equal(t, "Test User", resp["name"])
}

// stubChartHook captures users handler chart fires.
type stubChartHook struct {
	calls []struct {
		ownerID, viewerID, visitor string
	}
}

func (s *stubChartHook) OnUserShow(ownerID, viewerID, visitor string) {
	s.calls = append(s.calls, struct {
		ownerID, viewerID, visitor string
	}{ownerID, viewerID, visitor})
}

func TestShow_FiresChartHookAuthenticated(t *testing.T) {
	h, userRepo := newTestHandler(t)
	addTestUser(userRepo)
	hook := &stubChartHook{}
	h.SetChartHook(hook)

	body := `{"userId": "user1"}`
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/users/show", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set("misskeyUser", &model.User{ID: "viewer1"})

	require.NoError(t, h.Show(c))
	require.Len(t, hook.calls, 1)
	assert.Equal(t, "user1", hook.calls[0].ownerID)
	assert.Equal(t, "viewer1", hook.calls[0].viewerID)
	assert.Empty(t, hook.calls[0].visitor)
}

func TestShow_FiresChartHookAnonymous(t *testing.T) {
	h, userRepo := newTestHandler(t)
	addTestUser(userRepo)
	hook := &stubChartHook{}
	h.SetChartHook(hook)

	body := `{"userId": "user1"}`
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/users/show", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	require.NoError(t, h.Show(c))
	require.Len(t, hook.calls, 1)
	assert.Equal(t, "user1", hook.calls[0].ownerID)
	assert.Equal(t, "", hook.calls[0].viewerID)
	assert.NotEmpty(t, hook.calls[0].visitor) // RemoteAddr is set by httptest
}

func TestShow_ByUsername(t *testing.T) {
	h, userRepo := newTestHandler(t)
	addTestUser(userRepo)

	body := `{"username": "testuser"}`
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/users/show", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := h.Show(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "user1", resp["id"])
}

func TestShow_ByUsernameWithHost(t *testing.T) {
	h, userRepo := newTestHandler(t)

	host := "remote.example.com"
	user := &model.User{
		ID:                "user2",
		Username:          "remoteuser",
		UsernameLower:     "remoteuser",
		Host:              &host,
		AvatarDecorations: datatypes.JSON([]byte("[]")),
	}
	userRepo.Users["user2"] = user

	body := `{"username": "remoteuser", "host": "remote.example.com"}`
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/users/show", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := h.Show(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "user2", resp["id"])
}

func TestShow_NotFound(t *testing.T) {
	h, _ := newTestHandler(t)

	body := `{"userId": "nonexistent"}`
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/users/show", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := h.Show(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// stubRemoteResolver is a test double for coreuser.RemoteUserResolver.
type stubRemoteResolver struct {
	user *model.User
	err  error
}

func (s *stubRemoteResolver) ResolveByUsernameHost(_, _ string) (*model.User, error) {
	return s.user, s.err
}

func newTestHandlerWithRemoteResolver(t *testing.T, resolver coreuser.RemoteUserResolver) (*Handler, *testutil.MockUserRepository) {
	t.Helper()
	userRepo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	piningRepo := testutil.NewMockUserNotePiningRepository()
	fRepo := testutil.NewMockFollowingRepository()
	frRepo := testutil.NewMockFollowRequestRepository()
	idGen, _ := id.NewGenerator("aidx")
	svc := coreuser.NewService(userRepo, noteRepo, piningRepo, idGen)
	svc.SetRemoteUserResolver(resolver)
	fSvc := corefollowing.NewService(userRepo, fRepo, frRepo, idGen)
	return NewHandler(svc, fSvc, noteRepo, idGen), userRepo
}

func TestShow_ByUsernameWithHost_RemoteResolveSucceeds(t *testing.T) {
	// webfinger + ResolveActor が成功し、返された remote user がそのまま
	// UserDetailed として返されることを確認する。
	host := "remote.example"
	remoteUser := &model.User{
		ID:                "uR",
		Username:          "remote",
		UsernameLower:     "remote",
		Host:              &host,
		AvatarDecorations: datatypes.JSON([]byte("[]")),
	}
	h, _ := newTestHandlerWithRemoteResolver(t, &stubRemoteResolver{user: remoteUser})

	body := `{"username":"remote","host":"remote.example"}`
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/users/show", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	require.NoError(t, h.Show(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "uR", resp["id"])
}

func TestShow_ByUsernameWithHost_RemoteResolveFails(t *testing.T) {
	// resolver が error を返す場合、FAILED_TO_RESOLVE_REMOTE_USER がレスポンス
	// として返ることを確認する。HTTP ステータス / JSON の code & id を検証。
	h, _ := newTestHandlerWithRemoteResolver(t, &stubRemoteResolver{err: assert.AnError})

	body := `{"username":"ghost","host":"remote.example"}`
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/users/show", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	require.NoError(t, h.Show(c))
	assert.Equal(t, http.StatusNotFound, rec.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	errObj, ok := resp["error"].(map[string]any)
	require.True(t, ok, "response must have error field")
	assert.Equal(t, "FAILED_TO_RESOLVE_REMOTE_USER", errObj["code"])
	// apierr.UUIDFailedToResolveRemoteUser と一致すること。
	assert.Equal(t, "ef7b9be4-9cba-4e6f-ab41-90ed171c7d3c", errObj["id"])
}

func TestShow_MissingParams(t *testing.T) {
	h, _ := newTestHandler(t)

	body := `{}`
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/users/show", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := h.Show(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestShow_WithProfile(t *testing.T) {
	h, userRepo := newTestHandler(t)
	addTestUser(userRepo)

	desc := "Hello, I'm a test user"
	location := "Tokyo"
	userRepo.Profiles["user1"] = &model.UserProfile{
		UserID:      "user1",
		Description: &desc,
		Location:    &location,
		Fields:      datatypes.JSON([]byte("[]")),
	}

	body := `{"userId": "user1"}`
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/users/show", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := h.Show(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "Hello, I'm a test user", resp["description"])
	assert.Equal(t, "Tokyo", resp["location"])
}

func TestShow_InvalidJSON(t *testing.T) {
	h, _ := newTestHandler(t)

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/users/show", strings.NewReader("{invalid"))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := h.Show(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestShow_UsernameNotFound(t *testing.T) {
	h, _ := newTestHandler(t)

	body := `{"username": "nonexistent"}`
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/users/show", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := h.Show(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// TestShow_SelfViewReturnsMeDetailed: #970 — `/api/users/show` で viewer===target
// のとき MeDetailed 拡張 field (isExplorable / noCrawle / emailNotificationTypes
// 等) を merge して返すこと。
func TestShow_SelfViewReturnsMeDetailed(t *testing.T) {
	h, userRepo := newTestHandler(t)

	self := &model.User{
		ID:                "self1",
		Username:          "selfuser",
		IsExplorable:      true,
		IsDeleted:         false,
		HideOnlineStatus:  true,
		AvatarDecorations: datatypes.JSON([]byte("[]")),
	}
	userRepo.Users["self1"] = self
	userRepo.Profiles["self1"] = &model.UserProfile{
		UserID:                    "self1",
		NoCrawle:                  true,
		PreventAiLearning:         false,
		AlwaysMarkNsfw:            true,
		EmailNotificationTypes:    datatypes.JSON([]byte(`["mention"]`)),
		NotificationRecieveConfig: datatypes.JSON([]byte(`{"mention":{"type":"following"}}`)),
		Fields:                    datatypes.JSON([]byte("[]")),
	}

	body := `{"userId":"self1"}`
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/users/show", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set(string(middleware.UserContextKey), self)

	require.NoError(t, h.Show(c))
	assert.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	// MeDetailed-only field が出ること
	assert.Equal(t, true, resp["isExplorable"])
	assert.Equal(t, false, resp["isDeleted"])
	assert.Equal(t, true, resp["hideOnlineStatus"])
	assert.Equal(t, true, resp["noCrawle"])
	assert.Equal(t, false, resp["preventAiLearning"])
	assert.Equal(t, true, resp["alwaysMarkNsfw"])
	// #985 notification 3 field (profile JSON column 由来)
	emailTypes, ok := resp["emailNotificationTypes"].([]any)
	require.True(t, ok)
	require.Len(t, emailTypes, 1)
	assert.Equal(t, "mention", emailTypes[0])
	notifConf, ok := resp["notificationRecieveConfig"].(map[string]any)
	require.True(t, ok)
	require.Contains(t, notifConf, "mention")
	// 通常 UserDetailed field も残る
	assert.Equal(t, "self1", resp["id"])
	assert.Equal(t, "selfuser", resp["username"])
}

// TestShow_NonSelfViewExcludesMeDetailed: viewer != target なら MeDetailed
// 拡張 field は出さない (privacy boundary)。
func TestShow_NonSelfViewExcludesMeDetailed(t *testing.T) {
	h, userRepo := newTestHandler(t)

	target := &model.User{
		ID:                "target1",
		Username:          "target",
		IsExplorable:      true,
		AvatarDecorations: datatypes.JSON([]byte("[]")),
	}
	userRepo.Users["target1"] = target
	userRepo.Profiles["target1"] = &model.UserProfile{
		UserID:                 "target1",
		NoCrawle:               true,
		EmailNotificationTypes: datatypes.JSON([]byte(`["mention"]`)),
		Fields:                 datatypes.JSON([]byte("[]")),
	}
	viewer := &model.User{ID: "viewer1", Username: "viewer"}

	body := `{"userId":"target1"}`
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/users/show", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set(string(middleware.UserContextKey), viewer)

	require.NoError(t, h.Show(c))
	assert.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	// MeDetailed-only field が漏れていないこと
	_, hasIsExplorable := resp["isExplorable"]
	assert.False(t, hasIsExplorable, "isExplorable must not leak for non-self view")
	_, hasNoCrawle := resp["noCrawle"]
	assert.False(t, hasNoCrawle, "noCrawle must not leak for non-self view")
	_, hasEmailNotif := resp["emailNotificationTypes"]
	assert.False(t, hasEmailNotif, "emailNotificationTypes must not leak for non-self view")
}

// TestShow_AnonymousViewExcludesMeDetailed: viewer == nil でも UserDetailed
// shape (= 公開 field のみ) を返す。
func TestShow_AnonymousViewExcludesMeDetailed(t *testing.T) {
	h, userRepo := newTestHandler(t)
	target := &model.User{
		ID:                "target1",
		Username:          "target",
		IsExplorable:      true,
		AvatarDecorations: datatypes.JSON([]byte("[]")),
	}
	userRepo.Users["target1"] = target
	userRepo.Profiles["target1"] = &model.UserProfile{
		UserID: "target1",
		Fields: datatypes.JSON([]byte("[]")),
	}

	body := `{"userId":"target1"}`
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/users/show", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	// viewer は context に set しない (= anonymous)

	require.NoError(t, h.Show(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	_, hasIsExplorable := resp["isExplorable"]
	assert.False(t, hasIsExplorable)
}

func TestShow_ViewerDependentFields(t *testing.T) {
	h, userRepo := newTestHandler(t)

	// ターゲットユーザー
	target := &model.User{
		ID:                "target1",
		Username:          "target",
		AvatarDecorations: datatypes.JSON([]byte("[]")),
	}
	userRepo.Users["target1"] = target

	// viewer
	viewer := &model.User{ID: "viewer1", Username: "viewer"}

	// blockingリポジトリをセット (viewerがtargetをブロック)
	blockingRepo := testutil.NewMockBlockingRepository()
	blockingRepo.Blockings["b1"] = &model.Blocking{ID: "b1", BlockerID: "viewer1", BlockeeID: "target1"}
	h.SetBlockingRepo(blockingRepo)

	// mutingリポジトリをセット
	mutingRepo := testutil.NewMockMutingRepository()
	h.SetMutingRepo(mutingRepo)

	// followRequestリポジトリをセット
	followRequestRepo := testutil.NewMockFollowRequestRepository()
	h.SetFollowRequestRepo(followRequestRepo)

	body := `{"userId": "target1"}`
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/users/show", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set(string(middleware.UserContextKey), viewer)

	err := h.Show(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, true, resp["isBlocking"])
	assert.Equal(t, false, resp["isBlocked"])
	assert.Equal(t, false, resp["isMuted"])
}

func TestShow_ViewerMemo(t *testing.T) {
	h, userRepo := newTestHandler(t)

	target := &model.User{
		ID:                "target1",
		Username:          "target",
		AvatarDecorations: datatypes.JSON([]byte("[]")),
	}
	userRepo.Users["target1"] = target

	viewer := &model.User{ID: "viewer1", Username: "viewer"}

	// memoリポジトリをセット
	memoRepo := testutil.NewMockUserMemoRepository()
	memoRepo.Memos["viewer1:target1"] = &model.UserMemo{
		UserID:       "viewer1",
		TargetUserID: "target1",
		Memo:         "important person",
	}
	h.SetMemoRepo(memoRepo)

	// followingリポジトリをセット (notify/withRepliesのテスト用)
	followingRepo := testutil.NewMockFollowingRepository()
	notify := "normal"
	followingRepo.Followings["f1"] = &model.Following{
		ID:          "f1",
		FollowerID:  "viewer1",
		FolloweeID:  "target1",
		Notify:      &notify,
		WithReplies: true,
	}
	h.SetFollowingRepo(followingRepo)

	// renoteMutingリポジトリをセット
	renoteMutingRepo := testutil.NewMockRenoteMutingRepository()
	h.SetRenoteMutingRepo(renoteMutingRepo)

	body := `{"userId": "target1"}`
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/users/show", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set(string(middleware.UserContextKey), viewer)

	err := h.Show(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "important person", resp["memo"])
	assert.Equal(t, "normal", resp["notify"])
	assert.Equal(t, true, resp["withReplies"])
	assert.Equal(t, true, resp["isFollowing"])
	assert.Equal(t, false, resp["isRenoteMuted"])
}

func TestShow_RemoteUserInstance(t *testing.T) {
	h, userRepo := newTestHandler(t)

	host := "remote.example.com"
	target := &model.User{
		ID:                "remote1",
		Username:          "remoteuser",
		Host:              &host,
		AvatarDecorations: datatypes.JSON([]byte("[]")),
	}
	userRepo.Users["remote1"] = target

	// instanceリポジトリをセット
	instanceRepo := testutil.NewMockInstanceRepository()
	instName := "Remote Instance"
	softwareName := "misskey"
	instanceRepo.Instances["remote.example.com"] = &model.Instance{
		ID:               "inst1",
		Host:             "remote.example.com",
		Name:             &instName,
		SoftwareName:     &softwareName,
		FirstRetrievedAt: time.Now(),
	}
	h.SetInstanceRepo(instanceRepo)

	body := `{"userId": "remote1"}`
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/users/show", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := h.Show(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	inst := resp["instance"].(map[string]any)
	assert.Equal(t, "Remote Instance", inst["name"])
	assert.Equal(t, "misskey", inst["softwareName"])
}

// --- post is a small helper that exercises a handler with an optional body ---

func post(h echo.HandlerFunc, body string) *httptest.ResponseRecorder {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	_ = h(c)
	return rec
}

// --- Search ---

func TestSearch_Success(t *testing.T) {
	h, repo := newTestHandler(t)
	addTestUser(repo)

	rec := post(h.Search, `{"query": "test"}`)
	assert.Equal(t, http.StatusOK, rec.Code)
	var out []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	assert.Len(t, out, 1)
}

// users/search bulk path is now batch (#517). Per-row FindProfileByUserID
// must not be called and the new batch FindProfilesByUserIDs is invoked once
// with all matched user IDs.
type countingSearchUserRepo struct {
	*testutil.MockUserRepository
	findProfileByUserIDCalls   int
	findProfilesByUserIDsCalls int
	findProfilesByUserIDsSize  int
}

func (c *countingSearchUserRepo) FindProfileByUserID(id string) (*model.UserProfile, error) {
	c.findProfileByUserIDCalls++
	return c.MockUserRepository.FindProfileByUserID(id)
}

func (c *countingSearchUserRepo) FindProfilesByUserIDs(ids []string) ([]*model.UserProfile, error) {
	c.findProfilesByUserIDsCalls++
	c.findProfilesByUserIDsSize += len(ids)
	return c.MockUserRepository.FindProfilesByUserIDs(ids)
}

func TestSearch_BatchFetchesProfiles(t *testing.T) {
	repo := &countingSearchUserRepo{MockUserRepository: testutil.NewMockUserRepository()}
	noteRepo := testutil.NewMockNoteRepository()
	piningRepo := testutil.NewMockUserNotePiningRepository()
	fRepo := testutil.NewMockFollowingRepository()
	frRepo := testutil.NewMockFollowRequestRepository()
	idGen, _ := id.NewGenerator("aidx")
	svc := coreuser.NewService(repo, noteRepo, piningRepo, idGen)
	fSvc := corefollowing.NewService(repo, fRepo, frRepo, idGen)
	h := NewHandler(svc, fSvc, noteRepo, idGen)

	for i := 0; i < 5; i++ {
		uid := fmt.Sprintf("searchuser-%d", i)
		repo.Users[uid] = &model.User{
			ID: uid, Username: "matchu" + fmt.Sprintf("%d", i),
			UsernameLower:     "matchu" + fmt.Sprintf("%d", i),
			AvatarDecorations: datatypes.JSON([]byte("[]")),
		}
		desc := "d" + fmt.Sprintf("%d", i)
		repo.Profiles[uid] = &model.UserProfile{UserID: uid, Description: &desc}
	}

	rec := post(h.Search, `{"query":"matchu","limit":10}`)
	assert.Equal(t, http.StatusOK, rec.Code)
	var out []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.Len(t, out, 5)

	assert.Equal(t, 0, repo.findProfileByUserIDCalls,
		"per-row FindProfileByUserID must not be called (N+1 must be eliminated)")
	assert.Equal(t, 1, repo.findProfilesByUserIDsCalls,
		"FindProfilesByUserIDs should be called exactly once per request")
	assert.Equal(t, 5, repo.findProfilesByUserIDsSize,
		"all 5 user IDs should be coalesced into a single batch")
}

func TestSearch_DefaultLimit(t *testing.T) {
	h, repo := newTestHandler(t)
	addTestUser(repo)

	rec := post(h.Search, `{"query": "test", "limit": 0}`)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestSearch_InvalidJSON(t *testing.T) {
	h, _ := newTestHandler(t)
	rec := post(h.Search, `{invalid`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// origin enum validation (#763): upstream paramDef enum と一致しない値は
// 400 で reject。空 / local / remote / combined は OK。
func TestSearch_OriginEnumValidation(t *testing.T) {
	h, _ := newTestHandler(t)
	tt := []struct {
		origin string
		want   int
	}{
		{"", http.StatusOK},              // default → combined
		{"local", http.StatusOK},         // upstream enum
		{"remote", http.StatusOK},        // upstream enum
		{"combined", http.StatusOK},      // upstream enum
		{"all", http.StatusBadRequest},   // typo
		{"LOCAL", http.StatusBadRequest}, // case mismatch (upstream は lower-case 厳格)
		{"none", http.StatusBadRequest},  // 別 enum 由来
		{"foo", http.StatusBadRequest},   // garbage
	}
	for _, tc := range tt {
		t.Run(tc.origin, func(t *testing.T) {
			body := `{"query":"test","origin":"` + tc.origin + `"}`
			rec := post(h.Search, body)
			assert.Equal(t, tc.want, rec.Code, "origin=%q expected %d", tc.origin, tc.want)
		})
	}
}

// --- Notes ---

func TestNotes_Success(t *testing.T) {
	h, repo := newTestHandler(t)
	addTestUser(repo)
	// Insert a note via the noteRepo embedded in handler
	idGen, _ := id.NewGenerator("aidx")
	noteRepo := h.noteRepo.(*testutil.MockNoteRepository)
	noteID := idGen.Generate(time.Now())
	text := "hello"
	noteRepo.Notes[noteID] = &model.Note{
		ID:         noteID,
		UserID:     "user1",
		Text:       &text,
		Visibility: model.NoteVisibilityPublic,
		Reactions:  datatypes.JSON([]byte("{}")),
	}

	rec := post(h.Notes, `{"userId": "user1"}`)
	assert.Equal(t, http.StatusOK, rec.Code)
	var out []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	assert.Len(t, out, 1)
}

// --- #1021: withFiles / withReplies / withRenotes / withChannelNotes filter ---

// 4 種類のノート (text のみ / file 添付あり / reply / pure renote / channel) を
// 同 user に seed し、各 filter で期待数が返ることを確認する。upstream
// `users/notes` paramDef のデフォルトは withFiles=false / withReplies=true /
// withRenotes=true / withChannelNotes=false。
func seedNotesForFilter(t *testing.T, repo *testutil.MockNoteRepository) {
	t.Helper()
	text := "plain text"
	repo.Notes["nf_plain"] = &model.Note{
		ID: "nf_plain", UserID: "user1", Text: &text,
		Visibility: model.NoteVisibilityPublic, Reactions: datatypes.JSON([]byte("{}")),
	}
	repo.Notes["nf_file"] = &model.Note{
		ID: "nf_file", UserID: "user1", Text: &text, FileIDs: []string{"f1"},
		Visibility: model.NoteVisibilityPublic, Reactions: datatypes.JSON([]byte("{}")),
	}
	parentID := "nf_plain"
	repo.Notes["nf_reply"] = &model.Note{
		ID: "nf_reply", UserID: "user1", Text: &text, ReplyID: &parentID,
		Visibility: model.NoteVisibilityPublic, Reactions: datatypes.JSON([]byte("{}")),
	}
	renoteID := "external_target"
	repo.Notes["nf_renote"] = &model.Note{
		ID: "nf_renote", UserID: "user1", RenoteID: &renoteID,
		Visibility: model.NoteVisibilityPublic, Reactions: datatypes.JSON([]byte("{}")),
	}
	channelID := "ch1"
	repo.Notes["nf_channel"] = &model.Note{
		ID: "nf_channel", UserID: "user1", Text: &text, ChannelID: &channelID,
		Visibility: model.NoteVisibilityPublic, Reactions: datatypes.JSON([]byte("{}")),
	}
}

func TestNotes_DefaultFilters(t *testing.T) {
	h, repo := newTestHandler(t)
	addTestUser(repo)
	noteRepo := h.noteRepo.(*testutil.MockNoteRepository)
	seedNotesForFilter(t, noteRepo)
	rec := post(h.Notes, `{"userId": "user1"}`)
	assert.Equal(t, http.StatusOK, rec.Code)
	var out []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	// channel は除外、その他 (plain / file / reply / renote) は含む
	assert.Len(t, out, 4, "default: withChannelNotes=false で channel のみ除外")
}

func TestNotes_WithFiles(t *testing.T) {
	h, repo := newTestHandler(t)
	addTestUser(repo)
	noteRepo := h.noteRepo.(*testutil.MockNoteRepository)
	seedNotesForFilter(t, noteRepo)
	rec := post(h.Notes, `{"userId": "user1", "withFiles": true}`)
	assert.Equal(t, http.StatusOK, rec.Code)
	var out []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.Len(t, out, 1, "file 添付ノートのみ")
	assert.Equal(t, "nf_file", out[0]["id"])
}

func TestNotes_WithoutReplies(t *testing.T) {
	h, repo := newTestHandler(t)
	addTestUser(repo)
	noteRepo := h.noteRepo.(*testutil.MockNoteRepository)
	seedNotesForFilter(t, noteRepo)
	rec := post(h.Notes, `{"userId": "user1", "withReplies": false}`)
	assert.Equal(t, http.StatusOK, rec.Code)
	var out []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	for _, n := range out {
		assert.NotEqual(t, "nf_reply", n["id"], "reply が除外される")
	}
}

func TestNotes_WithoutRenotes(t *testing.T) {
	h, repo := newTestHandler(t)
	addTestUser(repo)
	noteRepo := h.noteRepo.(*testutil.MockNoteRepository)
	seedNotesForFilter(t, noteRepo)
	rec := post(h.Notes, `{"userId": "user1", "withRenotes": false}`)
	assert.Equal(t, http.StatusOK, rec.Code)
	var out []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	for _, n := range out {
		assert.NotEqual(t, "nf_renote", n["id"], "pure renote が除外される")
	}
}

func TestNotes_WithChannelNotes(t *testing.T) {
	h, repo := newTestHandler(t)
	addTestUser(repo)
	noteRepo := h.noteRepo.(*testutil.MockNoteRepository)
	seedNotesForFilter(t, noteRepo)
	rec := post(h.Notes, `{"userId": "user1", "withChannelNotes": true}`)
	assert.Equal(t, http.StatusOK, rec.Code)
	var out []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	var hasChannel bool
	for _, n := range out {
		if n["id"] == "nf_channel" {
			hasChannel = true
		}
	}
	assert.True(t, hasChannel, "withChannelNotes=true で channel 投稿が含まれる")
}

func TestNotes_LimitClamp(t *testing.T) {
	h, repo := newTestHandler(t)
	addTestUser(repo)
	rec := post(h.Notes, `{"userId": "user1", "limit": 9999}`)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestNotes_UserNotFound(t *testing.T) {
	h, _ := newTestHandler(t)
	rec := post(h.Notes, `{"userId": "ghost"}`)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestNotes_InvalidParam(t *testing.T) {
	h, _ := newTestHandler(t)
	rec := post(h.Notes, `{}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// --- Followers / Following ---

func TestFollowers_Success(t *testing.T) {
	h, repo := newTestHandler(t)
	addTestUser(repo)
	// Add a follower relationship
	repo.Users["follower1"] = &model.User{
		ID:                "follower1",
		Username:          "follower1",
		UsernameLower:     "follower1",
		AvatarDecorations: datatypes.JSON([]byte("[]")),
	}
	fSvc := h.followingService
	_, err := fSvc.Follow("follower1", "user1", corefollowing.FollowOptions{})
	require.NoError(t, err)

	rec := post(h.Followers, `{"userId": "user1"}`)
	assert.Equal(t, http.StatusOK, rec.Code)
	var out []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	assert.Len(t, out, 1)
}

// TestFollowers_PopulatesIsFollowedFromViewer guards #1144: when a viewer
// (auth user) requests users/followers of a profile, each follower row's
// embedded `follower` UserDetailed must carry `isFollowed` from the
// viewer's perspective (= "does this follower follow me?"). frontend
// MkUserInfo renders the "follows you" label based on this flag.
func TestFollowers_PopulatesIsFollowedFromViewer(t *testing.T) {
	h, repo := newTestHandler(t)
	addTestUser(repo) // user1 = target profile
	// viewer = bob, follower = alice
	repo.Users["bob"] = &model.User{ID: "bob", Username: "bob", UsernameLower: "bob",
		AvatarDecorations: datatypes.JSON([]byte("[]"))}
	repo.Users["alice"] = &model.User{ID: "alice", Username: "alice", UsernameLower: "alice",
		AvatarDecorations: datatypes.JSON([]byte("[]"))}
	// alice follows user1 (= alice 表示される follower)、
	// 加えて alice follows bob (viewer) → isFollowed = true 期待。
	fSvc := h.followingService
	_, err := fSvc.Follow("alice", "user1", corefollowing.FollowOptions{})
	require.NoError(t, err)
	_, err = fSvc.Follow("alice", "bob", corefollowing.FollowOptions{})
	require.NoError(t, err)

	rec := postStub(h.Followers, `{"userId":"user1"}`, repo.Users["bob"])
	require.Equal(t, http.StatusOK, rec.Code)
	var out []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.Len(t, out, 1)
	follower, ok := out[0]["follower"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, true, follower["isFollowed"], "alice follows viewer(bob) → isFollowed=true")
	// bob は alice を follow していない → isFollowing=false。
	assert.Equal(t, false, follower["isFollowing"])
}

// TestFollowers_PopulatesPendingFollowRequest guards #1144 #2: locked
// account からの pending follow request 状態でも MkFollowButton が正しく
// "follow request pending" 表示にできるよう、
// `hasPendingFollowRequestFromYou` / `hasPendingFollowRequestToYou` を
// embed UserDetailed に埋める。
func TestFollowers_PopulatesPendingFollowRequest(t *testing.T) {
	h, repo := newTestHandler(t)
	addTestUser(repo) // user1 = target profile
	locked := true
	repo.Users["bob"] = &model.User{ID: "bob", Username: "bob", UsernameLower: "bob",
		IsLocked: locked, AvatarDecorations: datatypes.JSON([]byte("[]"))}
	repo.Users["alice"] = &model.User{ID: "alice", Username: "alice", UsernameLower: "alice",
		IsLocked: locked, AvatarDecorations: datatypes.JSON([]byte("[]"))}
	fSvc := h.followingService
	// alice follows user1 → alice が表示される follower。
	_, err := fSvc.Follow("alice", "user1", corefollowing.FollowOptions{})
	require.NoError(t, err)
	// viewer(bob) が alice (locked) に follow request 送信中 →
	// alice.hasPendingFollowRequestFromYou=true 期待。
	_, err = fSvc.Follow("bob", "alice", corefollowing.FollowOptions{})
	require.NoError(t, err) // locked なので pending request になる

	rec := postStub(h.Followers, `{"userId":"user1"}`, repo.Users["bob"])
	require.Equal(t, http.StatusOK, rec.Code)
	var out []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.Len(t, out, 1)
	follower, ok := out[0]["follower"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, true, follower["hasPendingFollowRequestFromYou"],
		"viewer(bob) → alice (locked) に pending request → fromYou=true")
	assert.Equal(t, false, follower["hasPendingFollowRequestToYou"])
}

// fakeRemoteStatsFetcher is a stub for users.RemoteStatsFetcher returning
// canned stats per (host, username) pair. Used by remote stats override
// regression tests (#1146).
type fakeRemoteStatsFetcher struct {
	stats map[string]*RemoteUserStatsView // key = host + "|" + username
	calls int
}

func (f *fakeRemoteStatsFetcher) Fetch(_ context.Context, host, username string) *RemoteUserStatsView {
	f.calls++
	return f.stats[host+"|"+username]
}

// TestFollowers_AppliesRemoteStatsOverride guards #1146: remote user の
// notesCount / followersCount / followingCount は origin instance の
// /api/users/show から取得した値で上書きされる (RemoteStatsFetcher 経由)。
// 旧来は Show 経路だけで適用されていて、list 経路 (followers/following) では
// ローカル観測値のままだった。
func TestFollowers_AppliesRemoteStatsOverride(t *testing.T) {
	h, repo := newTestHandler(t)
	addTestUser(repo) // user1 = local target profile
	remoteHost := "remote.example"
	repo.Users["remote-alice"] = &model.User{
		ID:                "remote-alice",
		Username:          "alice",
		UsernameLower:     "alice",
		Host:              &remoteHost,
		NotesCount:        5,  // ローカル観測値 (= 古い)
		FollowersCount:    10, // ローカル観測値
		FollowingCount:    3,  // ローカル観測値
		AvatarDecorations: datatypes.JSON([]byte("[]")),
	}
	// remote-alice が user1 を follow → list に出る。
	fSvc := h.followingService
	_, err := fSvc.Follow("remote-alice", "user1", corefollowing.FollowOptions{})
	require.NoError(t, err)
	// fetcher は remote-alice に対して大きい本物の値を返す。
	h.SetRemoteStatsFetcher(&fakeRemoteStatsFetcher{
		stats: map[string]*RemoteUserStatsView{
			"remote.example|alice": {NotesCount: 500, FollowersCount: 1000, FollowingCount: 250},
		},
	})

	rec := post(h.Followers, `{"userId":"user1"}`)
	require.Equal(t, http.StatusOK, rec.Code)
	var out []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.Len(t, out, 1)
	follower, ok := out[0]["follower"].(map[string]any)
	require.True(t, ok)
	// 上書き後の remote 実値が反映されていることを assert (= 旧 5/10/3 では
	// なく 500/1000/250)。
	assert.Equal(t, float64(500), follower["notesCount"])
	assert.Equal(t, float64(1000), follower["followersCount"])
	assert.Equal(t, float64(250), follower["followingCount"])
}

// TestFollowers_RemoteStatsOverride_SkipsLocalUser: local user (Host==nil) は
// fetcher 経路を skip し、ローカル観測値をそのまま使う。HTTP request 削減。
func TestFollowers_RemoteStatsOverride_SkipsLocalUser(t *testing.T) {
	h, repo := newTestHandler(t)
	addTestUser(repo) // user1 = local target
	repo.Users["local-bob"] = &model.User{
		ID:                "local-bob",
		Username:          "bob",
		UsernameLower:     "bob",
		NotesCount:        42,
		AvatarDecorations: datatypes.JSON([]byte("[]")),
	}
	fSvc := h.followingService
	_, err := fSvc.Follow("local-bob", "user1", corefollowing.FollowOptions{})
	require.NoError(t, err)
	fetcher := &fakeRemoteStatsFetcher{stats: map[string]*RemoteUserStatsView{}}
	h.SetRemoteStatsFetcher(fetcher)

	rec := post(h.Followers, `{"userId":"user1"}`)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, 0, fetcher.calls, "local user (Host==nil) は fetcher を呼ばない")
}

// TestFollowing_PopulatesIsFollowedFromViewer covers the symmetric case
// for /api/users/following — each `followee` UserDetailed gets viewer's
// relation flags.
func TestFollowing_PopulatesIsFollowedFromViewer(t *testing.T) {
	h, repo := newTestHandler(t)
	addTestUser(repo) // user1 = target profile
	repo.Users["bob"] = &model.User{ID: "bob", Username: "bob", UsernameLower: "bob",
		AvatarDecorations: datatypes.JSON([]byte("[]"))}
	repo.Users["charlie"] = &model.User{ID: "charlie", Username: "charlie", UsernameLower: "charlie",
		AvatarDecorations: datatypes.JSON([]byte("[]"))}
	fSvc := h.followingService
	// user1 follows charlie → charlie が followee として list される。
	_, err := fSvc.Follow("user1", "charlie", corefollowing.FollowOptions{})
	require.NoError(t, err)
	// viewer(bob) follows charlie → isFollowing=true、charlie は bob を
	// follow していない → isFollowed=false。
	_, err = fSvc.Follow("bob", "charlie", corefollowing.FollowOptions{})
	require.NoError(t, err)

	rec := postStub(h.Following, `{"userId":"user1"}`, repo.Users["bob"])
	require.Equal(t, http.StatusOK, rec.Code)
	var out []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.Len(t, out, 1)
	followee, ok := out[0]["followee"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, true, followee["isFollowing"], "viewer(bob) follows charlie → isFollowing=true")
	assert.Equal(t, false, followee["isFollowed"])
}

// users/followers が follower user の取得を per-row ShowByID で叩いて
// いた N+1 を ShowManyByIDs 1 batch に置換した (#300 2-3)。
// 5 follower 検証で per-row FindByID + FindProfileByUserID が 0 回、
// FindManyByIDs / FindProfilesByUserIDs が 1 回ずつだけ呼ばれることを担保。
type countingFollowerUserRepo struct {
	*testutil.MockUserRepository
	findByIDCalls            int
	findProfileByUserIDCalls int
	findManyByIDsCalls       int
	findProfilesByUserIDs    int
	findManyByIDsCallSize    int
}

func (c *countingFollowerUserRepo) FindByID(id string) (*model.User, error) {
	c.findByIDCalls++
	return c.MockUserRepository.FindByID(id)
}

func (c *countingFollowerUserRepo) FindProfileByUserID(id string) (*model.UserProfile, error) {
	c.findProfileByUserIDCalls++
	return c.MockUserRepository.FindProfileByUserID(id)
}

func (c *countingFollowerUserRepo) FindManyByIDs(ids []string) ([]*model.User, error) {
	c.findManyByIDsCalls++
	c.findManyByIDsCallSize += len(ids)
	return c.MockUserRepository.FindManyByIDs(ids)
}

func (c *countingFollowerUserRepo) FindProfilesByUserIDs(ids []string) ([]*model.UserProfile, error) {
	c.findProfilesByUserIDs++
	return c.MockUserRepository.FindProfilesByUserIDs(ids)
}

func TestFollowers_BatchFetchesUsers(t *testing.T) {
	repo := &countingFollowerUserRepo{MockUserRepository: testutil.NewMockUserRepository()}
	noteRepo := testutil.NewMockNoteRepository()
	piningRepo := testutil.NewMockUserNotePiningRepository()
	fRepo := testutil.NewMockFollowingRepository()
	frRepo := testutil.NewMockFollowRequestRepository()
	idGen, _ := id.NewGenerator("aidx")
	svc := coreuser.NewService(repo, noteRepo, piningRepo, idGen)
	fSvc := corefollowing.NewService(repo, fRepo, frRepo, idGen)
	h := NewHandler(svc, fSvc, noteRepo, idGen)

	// target user (followee)
	repo.Users["target"] = &model.User{
		ID: "target", Username: "target", UsernameLower: "target",
		AvatarDecorations: datatypes.JSON([]byte("[]")),
	}
	for i := 0; i < 5; i++ {
		uid := fmt.Sprintf("rel-f-%d", i)
		repo.Users[uid] = &model.User{
			ID: uid, Username: uid, UsernameLower: uid,
			AvatarDecorations: datatypes.JSON([]byte("[]")),
		}
		_, err := fSvc.Follow(uid, "target", corefollowing.FollowOptions{})
		require.NoError(t, err)
	}

	// `followingService.Follow` が内部で repo.FindByID 経由でユーザー存在
	// 確認するため、handler 直前に call counter をリセットして listRelations
	// 経由の query 数だけを観測する。
	repo.findByIDCalls = 0
	repo.findProfileByUserIDCalls = 0
	repo.findManyByIDsCalls = 0
	repo.findManyByIDsCallSize = 0
	repo.findProfilesByUserIDs = 0

	rec := post(h.Followers, `{"userId":"target","limit":10}`)
	require.Equal(t, http.StatusOK, rec.Code)
	var resp []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp, 5)

	// listRelations の冒頭で ShowByID(req.UserID) を呼んで target 存在確認
	// するため、FindByID と FindProfileByUserID が 1 回ずつ走るのは
	// 想定どおり。重要なのは follower 5 件の解決で per-row 経路に落ちて
	// いないこと = listRelations の N+1 が解消されていること。
	assert.Equal(t, 1, repo.findByIDCalls,
		"only target existence check should call FindByID; per-row must use batch")
	assert.Equal(t, 1, repo.findProfileByUserIDCalls,
		"only target existence check should call FindProfileByUserID; per-row must use batch")
	assert.Equal(t, 1, repo.findManyByIDsCalls,
		"FindManyByIDs should be called exactly once per request for the 5 followers")
	assert.Equal(t, 5, repo.findManyByIDsCallSize,
		"all 5 follower IDs should be coalesced into a single batch")
	assert.Equal(t, 1, repo.findProfilesByUserIDs,
		"FindProfilesByUserIDs should be called exactly once per request")
}

func TestFollowers_LimitClamp(t *testing.T) {
	h, repo := newTestHandler(t)
	addTestUser(repo)
	rec := post(h.Followers, `{"userId": "user1", "limit": 9999}`)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestFollowers_UserNotFound(t *testing.T) {
	h, _ := newTestHandler(t)
	rec := post(h.Followers, `{"userId": "ghost"}`)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestFollowers_InvalidParam(t *testing.T) {
	h, _ := newTestHandler(t)
	rec := post(h.Followers, `{}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestFollowing_Success(t *testing.T) {
	h, repo := newTestHandler(t)
	addTestUser(repo)
	repo.Users["followee1"] = &model.User{
		ID:                "followee1",
		Username:          "followee1",
		UsernameLower:     "followee1",
		AvatarDecorations: datatypes.JSON([]byte("[]")),
	}
	_, err := h.followingService.Follow("user1", "followee1", corefollowing.FollowOptions{})
	require.NoError(t, err)

	rec := post(h.Following, `{"userId": "user1"}`)
	assert.Equal(t, http.StatusOK, rec.Code)
	var out []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	assert.Len(t, out, 1)
}

func TestFollowing_InvalidParam(t *testing.T) {
	h, _ := newTestHandler(t)
	rec := post(h.Following, `{}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestShow_BulkUserIDs(t *testing.T) {
	h, repo := newTestHandler(t)
	repo.Users["u1"] = &model.User{ID: "u1", Username: "alice", AvatarDecorations: datatypes.JSON([]byte("[]"))}
	repo.Users["u2"] = &model.User{ID: "u2", Username: "bob", AvatarDecorations: datatypes.JSON([]byte("[]"))}
	rec := post(h.Show, `{"userIds":["u1","u2","ghost"]}`)
	assert.Equal(t, http.StatusOK, rec.Code)
	var out []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	assert.Len(t, out, 2)
}

func TestShow_BulkUserIDs_Empty(t *testing.T) {
	h, _ := newTestHandler(t)
	rec := post(h.Show, `{"userIds":[]}`)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "[]\n", rec.Body.String())
}

// users/show bulk path は ShowManyByIDs に切り替えた (#503) ので、入力順が
// レスポンスにも保持されることを担保する。
func TestShow_BulkUserIDs_PreservesOrder(t *testing.T) {
	h, repo := newTestHandler(t)
	repo.Users["u1"] = &model.User{ID: "u1", Username: "alice", AvatarDecorations: datatypes.JSON([]byte("[]"))}
	repo.Users["u2"] = &model.User{ID: "u2", Username: "bob", AvatarDecorations: datatypes.JSON([]byte("[]"))}
	repo.Users["u3"] = &model.User{ID: "u3", Username: "carol", AvatarDecorations: datatypes.JSON([]byte("[]"))}

	rec := post(h.Show, `{"userIds":["u3","ghost","u1","u2"]}`)
	assert.Equal(t, http.StatusOK, rec.Code)
	var out []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.Len(t, out, 3)
	assert.Equal(t, "u3", out[0]["id"])
	assert.Equal(t, "u1", out[1]["id"])
	assert.Equal(t, "u2", out[2]["id"])
}

// 100 件を超えた userIds は先頭 100 件に切り捨てる動作を担保する (TS 互換)。
func TestShow_BulkUserIDs_Truncates100(t *testing.T) {
	h, repo := newTestHandler(t)
	ids := make([]string, 0, 105)
	for i := 0; i < 105; i++ {
		uid := fmt.Sprintf("ub%03d", i)
		repo.Users[uid] = &model.User{ID: uid, Username: uid, AvatarDecorations: datatypes.JSON([]byte("[]"))}
		ids = append(ids, fmt.Sprintf("%q", uid))
	}
	body := `{"userIds":[` + strings.Join(ids, ",") + `]}`
	rec := post(h.Show, body)
	assert.Equal(t, http.StatusOK, rec.Code)
	var out []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	assert.Len(t, out, 100, "userIds は 100 件で切り捨てられるはず")
}

// --- Internal error paths via failing repos ---

type failingNoteRepo struct {
	*testutil.MockNoteRepository
}

func (f *failingNoteRepo) ListByUserID(_ string, _, _ string, _ int) ([]*model.Note, error) {
	return nil, assertErr
}

func (f *failingNoteRepo) ListByUserIDFiltered(_, _, _ string, _ int, _, _, _, _ bool) ([]*model.Note, error) {
	return nil, assertErr
}

type failingUserRepo struct {
	*testutil.MockUserRepository
}

func (f *failingUserRepo) SearchByUsername(_ string, _, _ int, _ string) ([]*model.User, error) {
	return nil, assertErr
}

type failingFollowingRepo struct {
	*testutil.MockFollowingRepository
}

func (f *failingFollowingRepo) ListFollowers(_ string, _, _ int) ([]*model.Following, error) {
	return nil, assertErr
}

func (f *failingFollowingRepo) ListFollowing(_ string, _, _ int) ([]*model.Following, error) {
	return nil, assertErr
}

var assertErr = &simpleErr{"stub"}

type simpleErr struct{ msg string }

func (e *simpleErr) Error() string { return e.msg }

func newHandlerWithFailingNoteRepo(t *testing.T) *Handler {
	t.Helper()
	userRepo := testutil.NewMockUserRepository()
	noteRepo := &failingNoteRepo{MockNoteRepository: testutil.NewMockNoteRepository()}
	piningRepo := testutil.NewMockUserNotePiningRepository()
	fRepo := testutil.NewMockFollowingRepository()
	frRepo := testutil.NewMockFollowRequestRepository()
	idGen, _ := id.NewGenerator("aidx")
	svc := coreuser.NewService(userRepo, noteRepo, piningRepo, idGen)
	fSvc := corefollowing.NewService(userRepo, fRepo, frRepo, idGen)
	addTestUser(userRepo)
	return NewHandler(svc, fSvc, noteRepo, idGen)
}

func newHandlerWithFailingSearch(t *testing.T) *Handler {
	t.Helper()
	mockUR := testutil.NewMockUserRepository()
	addTestUser(mockUR)
	userRepo := &failingUserRepo{MockUserRepository: mockUR}
	noteRepo := testutil.NewMockNoteRepository()
	piningRepo := testutil.NewMockUserNotePiningRepository()
	fRepo := testutil.NewMockFollowingRepository()
	frRepo := testutil.NewMockFollowRequestRepository()
	idGen, _ := id.NewGenerator("aidx")
	svc := coreuser.NewService(userRepo, noteRepo, piningRepo, idGen)
	fSvc := corefollowing.NewService(userRepo, fRepo, frRepo, idGen)
	return NewHandler(svc, fSvc, noteRepo, idGen)
}

func newHandlerWithFailingFollowing(t *testing.T) *Handler {
	t.Helper()
	userRepo := testutil.NewMockUserRepository()
	addTestUser(userRepo)
	noteRepo := testutil.NewMockNoteRepository()
	piningRepo := testutil.NewMockUserNotePiningRepository()
	fRepo := &failingFollowingRepo{MockFollowingRepository: testutil.NewMockFollowingRepository()}
	frRepo := testutil.NewMockFollowRequestRepository()
	idGen, _ := id.NewGenerator("aidx")
	svc := coreuser.NewService(userRepo, noteRepo, piningRepo, idGen)
	fSvc := corefollowing.NewService(userRepo, fRepo, frRepo, idGen)
	return NewHandler(svc, fSvc, noteRepo, idGen)
}

func TestSearch_InternalError(t *testing.T) {
	h := newHandlerWithFailingSearch(t)
	rec := post(h.Search, `{"query": "test"}`)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestNotes_InternalError(t *testing.T) {
	h := newHandlerWithFailingNoteRepo(t)
	rec := post(h.Notes, `{"userId": "user1"}`)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestFollowers_InternalError(t *testing.T) {
	h := newHandlerWithFailingFollowing(t)
	rec := post(h.Followers, `{"userId": "user1"}`)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestFollowing_InternalError(t *testing.T) {
	h := newHandlerWithFailingFollowing(t)
	rec := post(h.Following, `{"userId": "user1"}`)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

// --- Phase 7-3 (#245): pinnedNotes / pinnedPage on users/show ---

type stubPageRepoForPin struct {
	page *model.Page
}

func (s *stubPageRepoForPin) Create(*model.Page) error                              { return nil }
func (s *stubPageRepoForPin) FindByID(string) (*model.Page, error)                  { return s.page, nil }
func (s *stubPageRepoForPin) FindManyByIDs([]string) ([]*model.Page, error)         { return nil, nil }
func (s *stubPageRepoForPin) FindByUserAndName(string, string) (*model.Page, error) { return nil, nil }
func (s *stubPageRepoForPin) UpdateFields(string, map[string]any) error             { return nil }
func (s *stubPageRepoForPin) Delete(*model.Page) error                              { return nil }
func (s *stubPageRepoForPin) ListByUser(string, string, string, int, int) ([]*model.Page, error) {
	return nil, nil
}
func (s *stubPageRepoForPin) ListPublicByUser(string, string, string, int, int) ([]*model.Page, error) {
	return nil, nil
}
func (s *stubPageRepoForPin) ListFeatured(string, string, int, int) ([]*model.Page, error) {
	return nil, nil
}
func (s *stubPageRepoForPin) IncrementCount(string, string, int) error { return nil }

func TestShow_PinnedNotes_Populated(t *testing.T) {
	h, userRepo := newTestHandler(t)
	addTestUser(userRepo)

	piningRepo := testutil.NewMockUserNotePiningRepository()
	require.NoError(t, piningRepo.Create(&model.UserNotePining{ID: "p1", UserID: "user1", NoteID: "note_a"}))
	h.SetPiningRepo(piningRepo)

	// note 本体は h.noteRepo (newTestHandler 内で MockNoteRepo) に入れる
	nr, _ := h.noteRepo.(*testutil.MockNoteRepository)
	require.NotNil(t, nr)
	txt := "pinned!"
	nr.Notes["note_a"] = &model.Note{ID: "note_a", UserID: "user1", Text: &txt, Visibility: model.NoteVisibilityPublic, Reactions: datatypes.JSON([]byte("{}"))}

	body := `{"userId": "user1"}`
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/users/show", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	require.NoError(t, h.Show(c))

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	ids, ok := resp["pinnedNoteIds"].([]any)
	require.True(t, ok)
	assert.Len(t, ids, 1)
	assert.Equal(t, "note_a", ids[0])
	notes, ok := resp["pinnedNotes"].([]any)
	require.True(t, ok)
	assert.Len(t, notes, 1)
}

func TestShow_PinnedPage_Populated(t *testing.T) {
	h, userRepo := newTestHandler(t)
	addTestUser(userRepo)

	pageID := "pg_1"
	userRepo.Profiles["user1"] = &model.UserProfile{
		UserID:       "user1",
		Fields:       datatypes.JSON([]byte("[]")),
		PinnedPageID: &pageID,
	}
	h.SetPageRepo(&stubPageRepoForPin{page: &model.Page{ID: pageID, Title: "my page", UserID: "user1"}})

	body := `{"userId": "user1"}`
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/users/show", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	require.NoError(t, h.Show(c))

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, pageID, resp["pinnedPageId"])
	page, ok := resp["pinnedPage"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "my page", page["title"])
}

func TestShow_PinnedFields_Defaults(t *testing.T) {
	h, userRepo := newTestHandler(t)
	addTestUser(userRepo)

	body := `{"userId": "user1"}`
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/users/show", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	require.NoError(t, h.Show(c))

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	// pining未wireでもリストは存在 (空)
	ids, _ := resp["pinnedNoteIds"].([]any)
	assert.Empty(t, ids)
	assert.Nil(t, resp["pinnedPageId"])
	assert.Nil(t, resp["pinnedPage"])
}

// --- Show with isFollowing ---

func TestShow_IsFollowing(t *testing.T) {
	h, repo := newTestHandler(t)
	repo.Users["u1"] = &model.User{ID: "u1", Username: "alice", AvatarDecorations: datatypes.JSON("[]")}
	repo.Users["u2"] = &model.User{ID: "u2", Username: "bob", AvatarDecorations: datatypes.JSON("[]")}
	fRepo := h.followingRepo
	_ = fRepo // followingRepoがセットされていない場合のテスト

	rec := postStub(h.Show, `{"userId":"u2"}`, &model.User{ID: "u1"})
	require.Equal(t, http.StatusOK, rec.Code)
}

// #739: SetEmojiRepo / SetReactionReader / SetNoteFieldResolver の setter を
// 配線して非 nil 経路の lookup を踏む。populateUserEmojis も Show 経由で
// 実行されるよう emoji 行を仕込む。
func TestSetters_WireOptionalDeps(t *testing.T) {
	h, repo := newTestHandler(t)
	repo.Users["u1"] = &model.User{
		ID: "u1", Username: "alice", UsernameLower: "alice",
		Emojis:            []string{"smile"},
		AvatarDecorations: datatypes.JSON("[]"),
	}

	emojiRepo := testutil.NewMockEmojiRepository()
	require.NoError(t, emojiRepo.Create(&model.Emoji{
		ID: "e1", Name: "smile", PublicURL: "https://x/smile.png",
	}))
	h.SetEmojiRepo(emojiRepo)
	h.SetInstanceRepo(testutil.NewMockInstanceRepository())
	h.SetReactionReader(stubBufferedReactions{})
	h.SetNoteFieldResolver(nil) // Apply は r==nil で no-op (#739)

	rec := postStub(h.Show, `{"userId":"u1"}`, &model.User{ID: "u1"})
	require.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	// populateUserEmojis が emoji を URL に解決して emojis map に出すこと
	emojis, _ := resp["emojis"].(map[string]any)
	require.NotNil(t, emojis, "emojis should be populated when emojiRepo is wired")
	assert.Equal(t, "https://x/smile.png", emojis["smile"])
}

// stubBufferedReactions implements entity.BufferedReactionsReader as a no-op
// for setter wiring tests (#739)。
type stubBufferedReactions struct{}

func (stubBufferedReactions) GetBufferedMany(_ context.Context, _ []string) (map[string]map[string]int64, error) {
	return map[string]map[string]int64{}, nil
}
