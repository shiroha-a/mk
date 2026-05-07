package blocking

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	coreblocking "github.com/shiroha-a/mk/internal/core/blocking"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/server/middleware"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newHandler(t *testing.T) (*Handler, *testutil.MockUserRepository) {
	t.Helper()
	userRepo := testutil.NewMockUserRepository()
	blockingRepo := testutil.NewMockBlockingRepository()
	idGen, _ := id.NewGenerator("aidx")
	svc := coreblocking.NewService(userRepo, blockingRepo, nil, idGen)
	return NewHandler(svc, userRepo, idGen), userRepo
}

func newReq(t *testing.T, body string) (echo.Context, *httptest.ResponseRecorder) {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	return e.NewContext(req, rec), rec
}

func setUser(c echo.Context, id string) {
	c.Set(string(middleware.UserContextKey), &model.User{ID: id})
}

func addUser(repo *testutil.MockUserRepository, id string) {
	repo.Users[id] = &model.User{ID: id, Username: id}
}

func TestCreate_Success(t *testing.T) {
	h, repo := newHandler(t)
	addUser(repo, "alice")
	addUser(repo, "bob")

	c, rec := newReq(t, `{"userId":"bob"}`)
	setUser(c, "alice")
	require.NoError(t, h.Create(c))
	// upstream Misskey TS と同じ 200 + UserDetailed (= drop-in 互換、#870)。
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"id":"bob"`)
}

func TestCreate_InvalidParam(t *testing.T) {
	h, _ := newHandler(t)
	c, rec := newReq(t, `{}`)
	setUser(c, "alice")
	require.NoError(t, h.Create(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestCreate_InvalidJSON(t *testing.T) {
	h, _ := newHandler(t)
	c, rec := newReq(t, `{invalid`)
	setUser(c, "alice")
	require.NoError(t, h.Create(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestCreate_Self(t *testing.T) {
	h, repo := newHandler(t)
	addUser(repo, "alice")

	c, rec := newReq(t, `{"userId":"alice"}`)
	setUser(c, "alice")
	require.NoError(t, h.Create(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestCreate_NoSuchUser(t *testing.T) {
	h, _ := newHandler(t)
	c, rec := newReq(t, `{"userId":"ghost"}`)
	setUser(c, "alice")
	require.NoError(t, h.Create(c))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestCreate_AlreadyBlocking(t *testing.T) {
	h, repo := newHandler(t)
	addUser(repo, "alice")
	addUser(repo, "bob")
	c1, _ := newReq(t, `{"userId":"bob"}`)
	setUser(c1, "alice")
	require.NoError(t, h.Create(c1))

	c2, rec := newReq(t, `{"userId":"bob"}`)
	setUser(c2, "alice")
	require.NoError(t, h.Create(c2))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// failingBlockingRepo causes Create to fail to exercise the internalError path.
type failingBlockingRepo struct {
	*testutil.MockBlockingRepository
}

func (f *failingBlockingRepo) Create(_ *model.Blocking) error {
	return testutil.ErrNotFound
}

func TestCreate_RepoError(t *testing.T) {
	userRepo := testutil.NewMockUserRepository()
	addUser(userRepo, "alice")
	addUser(userRepo, "bob")
	idGen, _ := id.NewGenerator("aidx")
	svc := coreblocking.NewService(userRepo, &failingBlockingRepo{MockBlockingRepository: testutil.NewMockBlockingRepository()}, nil, idGen)
	h := NewHandler(svc, nil, nil)

	c, rec := newReq(t, `{"userId":"bob"}`)
	setUser(c, "alice")
	require.NoError(t, h.Create(c))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestDelete_Success(t *testing.T) {
	h, repo := newHandler(t)
	addUser(repo, "alice")
	addUser(repo, "bob")
	c1, _ := newReq(t, `{"userId":"bob"}`)
	setUser(c1, "alice")
	require.NoError(t, h.Create(c1))

	c2, rec := newReq(t, `{"userId":"bob"}`)
	setUser(c2, "alice")
	require.NoError(t, h.Delete(c2))
	// upstream Misskey TS と同じ 200 + UserDetailed (#870)。
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"id":"bob"`)
}

func TestDelete_InvalidParam(t *testing.T) {
	h, _ := newHandler(t)
	c, rec := newReq(t, `{}`)
	setUser(c, "alice")
	require.NoError(t, h.Delete(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestDelete_Self(t *testing.T) {
	h, _ := newHandler(t)
	c, rec := newReq(t, `{"userId":"alice"}`)
	setUser(c, "alice")
	require.NoError(t, h.Delete(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestDelete_NotBlocking(t *testing.T) {
	h, _ := newHandler(t)
	c, rec := newReq(t, `{"userId":"bob"}`)
	setUser(c, "alice")
	require.NoError(t, h.Delete(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// failingDeleteBlockingRepo lets us trigger Delete errors.
type failingDeleteBlockingRepo struct {
	*testutil.MockBlockingRepository
}

func (f *failingDeleteBlockingRepo) Delete(_ *model.Blocking) error {
	return testutil.ErrNotFound
}

func TestDelete_RepoError(t *testing.T) {
	userRepo := testutil.NewMockUserRepository()
	addUser(userRepo, "alice")
	addUser(userRepo, "bob")
	mock := testutil.NewMockBlockingRepository()
	mock.Blockings["b1"] = &model.Blocking{ID: "b1", BlockerID: "alice", BlockeeID: "bob"}
	idGen, _ := id.NewGenerator("aidx")
	svc := coreblocking.NewService(userRepo, &failingDeleteBlockingRepo{MockBlockingRepository: mock}, nil, idGen)
	h := NewHandler(svc, nil, nil)

	c, rec := newReq(t, `{"userId":"bob"}`)
	setUser(c, "alice")
	require.NoError(t, h.Delete(c))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestList_OK(t *testing.T) {
	h, repo := newHandler(t)
	addUser(repo, "alice")
	addUser(repo, "bob")
	c1, _ := newReq(t, `{"userId":"bob"}`)
	setUser(c1, "alice")
	require.NoError(t, h.Create(c1))

	c, rec := newReq(t, `{}`)
	setUser(c, "alice")
	require.NoError(t, h.List(c))
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestList_LimitClamping(t *testing.T) {
	h, _ := newHandler(t)
	c, rec := newReq(t, `{"limit":1000}`)
	setUser(c, "alice")
	require.NoError(t, h.List(c))
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestList_InvalidJSON(t *testing.T) {
	h, _ := newHandler(t)
	c, rec := newReq(t, `{invalid`)
	setUser(c, "alice")
	require.NoError(t, h.List(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// failingListBlockingRepo lets us trigger List errors.
type failingListBlockingRepo struct {
	*testutil.MockBlockingRepository
}

func (f *failingListBlockingRepo) ListByBlocker(_, _, _ string, _, _ int) ([]*model.Blocking, error) {
	return nil, testutil.ErrNotFound
}

func TestList_RepoError(t *testing.T) {
	userRepo := testutil.NewMockUserRepository()
	idGen, _ := id.NewGenerator("aidx")
	svc := coreblocking.NewService(userRepo, &failingListBlockingRepo{MockBlockingRepository: testutil.NewMockBlockingRepository()}, nil, idGen)
	h := NewHandler(svc, nil, nil)

	c, rec := newReq(t, `{}`)
	setUser(c, "alice")
	require.NoError(t, h.List(c))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}
