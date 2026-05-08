package federation

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	coreinstance "github.com/shiroha-a/mk/internal/core/instance"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newHandler(t *testing.T) (*Handler, *testutil.MockInstanceRepository) {
	t.Helper()
	repo := testutil.NewMockInstanceRepository()
	metaRepo := testutil.NewMockMetaRepository()
	metaRepo.Meta = &model.Meta{}
	idGen, _ := id.NewGenerator("aidx")
	svc := coreinstance.NewService(repo, metaRepo, idGen)
	return NewHandler(svc), repo
}

func newReq(t *testing.T, body string) (echo.Context, *httptest.ResponseRecorder) {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	return e.NewContext(req, rec), rec
}

// postBody invokes a handler with the given JSON body and returns the recorded
// response. Used by the per-handler test files (host_listings_test.go,
// stats_test.go, update_remote_user_test.go).
func postBody(handler func(echo.Context) error, body string) *httptest.ResponseRecorder {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	_ = handler(c)
	return rec
}

// postStub is a shorthand for postBody with an empty JSON body.
func postStub(handler func(echo.Context) error) *httptest.ResponseRecorder {
	return postBody(handler, `{}`)
}

func seedInstance(t *testing.T, repo *testutil.MockInstanceRepository, host string) *model.Instance {
	t.Helper()
	inst := &model.Instance{
		ID:               "i_" + host,
		Host:             host,
		FirstRetrievedAt: time.Now(),
		SuspensionState:  model.SuspensionStateNone,
	}
	repo.Instances[host] = inst
	return inst
}

func TestInstances_Empty(t *testing.T) {
	h, _ := newHandler(t)
	c, rec := newReq(t, `{}`)
	require.NoError(t, h.Instances(c))
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestInstances_Filtered(t *testing.T) {
	h, repo := newHandler(t)
	seedInstance(t, repo, "alpha.example")
	seedInstance(t, repo, "beta.example")
	c, rec := newReq(t, `{"host":"alpha"}`)
	require.NoError(t, h.Instances(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "alpha.example")
	assert.NotContains(t, rec.Body.String(), "beta.example")
}

func TestInstances_BadJSON(t *testing.T) {
	h, _ := newHandler(t)
	c, rec := newReq(t, `{not json`)
	require.NoError(t, h.Instances(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// failingInstanceRepo causes List to fail.
type failingInstanceRepo struct {
	*testutil.MockInstanceRepository
}

func (f *failingInstanceRepo) List(_ model.InstanceListFilter) ([]*model.Instance, error) {
	return nil, errors.New("boom")
}

func TestInstances_RepoError(t *testing.T) {
	mock := testutil.NewMockInstanceRepository()
	repo := &failingInstanceRepo{MockInstanceRepository: mock}
	metaRepo := testutil.NewMockMetaRepository()
	metaRepo.Meta = &model.Meta{}
	idGen, _ := id.NewGenerator("aidx")
	svc := coreinstance.NewService(repo, metaRepo, idGen)
	h := NewHandler(svc)
	c, rec := newReq(t, `{}`)
	require.NoError(t, h.Instances(c))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestShowInstance_Success(t *testing.T) {
	h, repo := newHandler(t)
	seedInstance(t, repo, "alpha.example")
	c, rec := newReq(t, `{"host":"alpha.example"}`)
	require.NoError(t, h.ShowInstance(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "alpha.example")
}

func TestShowInstance_BadJSON(t *testing.T) {
	h, _ := newHandler(t)
	c, rec := newReq(t, `{not`)
	require.NoError(t, h.ShowInstance(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestShowInstance_EmptyHost(t *testing.T) {
	h, _ := newHandler(t)
	c, rec := newReq(t, `{}`)
	require.NoError(t, h.ShowInstance(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// TestShowInstance_NotFound: upstream Misskey TS と同じく未知 host は
// 204 No Content (= null 相当) を返す。旧実装は 404 + error body だったが、
// drop-in 互換のため 204 に揃えた (#915)。
func TestShowInstance_NotFound(t *testing.T) {
	h, _ := newHandler(t)
	c, rec := newReq(t, `{"host":"missing.example"}`)
	require.NoError(t, h.ShowInstance(c))
	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Empty(t, rec.Body.String(), "204 response should have empty body")
}

// TestShowInstance_FederatingFlags verifies that the response includes the
// computed federating / subscribing / publishing fields expected by misskey-js.
func TestShowInstance_FederatingFlags(t *testing.T) {
	cases := []struct {
		name        string
		following   int
		followers   int
		federating  bool
		subscribing bool
		publishing  bool
	}{
		{name: "no federation", following: 0, followers: 0, federating: false, subscribing: false, publishing: false},
		{name: "only outgoing", following: 3, followers: 0, federating: true, subscribing: false, publishing: true},
		{name: "only incoming", following: 0, followers: 5, federating: true, subscribing: true, publishing: false},
		{name: "bidirectional", following: 4, followers: 2, federating: true, subscribing: true, publishing: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, repo := newHandler(t)
			inst := seedInstance(t, repo, "flags.example")
			inst.FollowingCount = tc.following
			inst.FollowersCount = tc.followers

			c, rec := newReq(t, `{"host":"flags.example"}`)
			require.NoError(t, h.ShowInstance(c))
			require.Equal(t, http.StatusOK, rec.Code)

			var got map[string]any
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
			assert.Equal(t, tc.federating, got["federating"])
			assert.Equal(t, tc.subscribing, got["subscribing"])
			assert.Equal(t, tc.publishing, got["publishing"])
		})
	}
}
