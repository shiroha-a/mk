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
	"github.com/lib/pq"
	coreinstance "github.com/shiroha-a/mk/internal/core/instance"
	"github.com/shiroha-a/mk/internal/entitycompat/shapetest"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/server/middleware"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newHandler(t *testing.T) (*Handler, *testutil.MockInstanceRepository) {
	h, repo, _ := newHandlerWithMeta(t)
	return h, repo
}

// newHandlerWithMeta is like newHandler but also exposes the MockMetaRepository
// so tests can configure blockedHosts / silencedHosts / mediaSilencedHosts.
func newHandlerWithMeta(t *testing.T) (*Handler, *testutil.MockInstanceRepository, *testutil.MockMetaRepository) {
	t.Helper()
	repo := testutil.NewMockInstanceRepository()
	metaRepo := testutil.NewMockMetaRepository()
	metaRepo.Meta = &model.Meta{}
	idGen, _ := id.NewGenerator("aidx")
	svc := coreinstance.NewService(repo, metaRepo, idGen)
	return NewHandler(svc), repo, metaRepo
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

// fakeModerator implements ModeratorChecker for the moderationNote gate tests.
type fakeModerator struct{ ids map[string]bool }

func (f fakeModerator) IsModerator(userID string) bool { return f.ids[userID] }

// moderationNote は公開エンドポイントなので未認証 (non-moderator) には null。
func TestShowInstance_ModerationNoteHiddenFromPublic(t *testing.T) {
	h, repo := newHandler(t)
	inst := seedInstance(t, repo, "alpha.example")
	inst.ModerationNote = "secret moderator memo"

	c, rec := newReq(t, `{"host":"alpha.example"}`)
	require.NoError(t, h.ShowInstance(c))
	assert.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Nil(t, resp["moderationNote"], "moderationNote must be null for non-moderator")
	assert.NotContains(t, rec.Body.String(), "secret moderator memo")
}

// moderator として認証されていれば moderationNote の実値が返る。
func TestShowInstance_ModerationNoteVisibleToModerator(t *testing.T) {
	h, repo := newHandler(t)
	h.SetModeratorChecker(fakeModerator{ids: map[string]bool{"mod1": true}})
	inst := seedInstance(t, repo, "alpha.example")
	inst.ModerationNote = "secret moderator memo"

	c, rec := newReq(t, `{"host":"alpha.example"}`)
	c.Set(string(middleware.UserContextKey), &model.User{ID: "mod1"})
	require.NoError(t, h.ShowInstance(c))
	assert.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "secret moderator memo", resp["moderationNote"])
}

// 認証済みでも moderator でなければ moderationNote は隠す。
func TestInstances_ModerationNoteHiddenFromNonModerator(t *testing.T) {
	h, repo := newHandler(t)
	h.SetModeratorChecker(fakeModerator{ids: map[string]bool{"mod1": true}})
	inst := seedInstance(t, repo, "alpha.example")
	inst.ModerationNote = "secret memo"

	c, rec := newReq(t, `{"host":"alpha"}`)
	c.Set(string(middleware.UserContextKey), &model.User{ID: "regular"})
	require.NoError(t, h.Instances(c))
	assert.NotContains(t, rec.Body.String(), "secret memo")
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

// TestInstances_BlockedFilter は federation 画面の「ブロック」タブのバグ
// (blocked/silenced フィルタが無視され全件返っていた) の regression guard。
// blocked:true は meta.blockedHosts に exact 一致する host だけ、blocked:false
// はそれ以外を返すこと。
func TestInstances_BlockedFilter(t *testing.T) {
	h, repo, metaRepo := newHandlerWithMeta(t)
	seedInstance(t, repo, "blocked.example")
	seedInstance(t, repo, "normal.example")
	metaRepo.Meta = &model.Meta{BlockedHosts: pq.StringArray{"blocked.example"}}

	c, rec := newReq(t, `{"blocked":true}`)
	require.NoError(t, h.Instances(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "blocked.example")
	assert.NotContains(t, rec.Body.String(), "normal.example")

	c, rec = newReq(t, `{"blocked":false}`)
	require.NoError(t, h.Instances(c))
	assert.NotContains(t, rec.Body.String(), "blocked.example")
	assert.Contains(t, rec.Body.String(), "normal.example")
}

// TestInstances_BlockedEmptyList: blockedHosts が空のとき blocked:true は 0 件
// (本家の 1=0 相当)、blocked:false は全件返ること。
func TestInstances_BlockedEmptyList(t *testing.T) {
	h, repo, _ := newHandlerWithMeta(t)
	seedInstance(t, repo, "normal.example")

	c, rec := newReq(t, `{"blocked":true}`)
	require.NoError(t, h.Instances(c))
	assert.NotContains(t, rec.Body.String(), "normal.example")

	c, rec = newReq(t, `{"blocked":false}`)
	require.NoError(t, h.Instances(c))
	assert.Contains(t, rec.Body.String(), "normal.example")
}

// TestInstances_SilencedFilter は silenced タブの同等 regression guard。
func TestInstances_SilencedFilter(t *testing.T) {
	h, repo, metaRepo := newHandlerWithMeta(t)
	seedInstance(t, repo, "silenced.example")
	seedInstance(t, repo, "normal.example")
	metaRepo.Meta = &model.Meta{SilencedHosts: pq.StringArray{"silenced.example"}}

	c, rec := newReq(t, `{"silenced":true}`)
	require.NoError(t, h.Instances(c))
	assert.Contains(t, rec.Body.String(), "silenced.example")
	assert.NotContains(t, rec.Body.String(), "normal.example")

	c, rec = newReq(t, `{"silenced":false}`)
	require.NoError(t, h.Instances(c))
	assert.NotContains(t, rec.Body.String(), "silenced.example")
	assert.Contains(t, rec.Body.String(), "normal.example")
}

// TestInstances_StatusFields verifies isBlocked / isSilenced / isMediaSilenced
// が meta との suffix-match で算出されレスポンスに載ること。サブドメインも
// suffix-match で拾う (本家 InstanceEntityService と同じ)。
func TestInstances_StatusFields(t *testing.T) {
	h, repo, metaRepo := newHandlerWithMeta(t)
	seedInstance(t, repo, "sub.blocked.example")
	metaRepo.Meta = &model.Meta{
		BlockedHosts:       pq.StringArray{"blocked.example"},
		SilencedHosts:      pq.StringArray{"sub.blocked.example"},
		MediaSilencedHosts: pq.StringArray{"other.example"},
	}

	c, rec := newReq(t, `{"host":"sub.blocked.example"}`)
	require.NoError(t, h.Instances(c))
	require.Equal(t, http.StatusOK, rec.Code)

	var got []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Len(t, got, 1)
	assert.Equal(t, true, got[0]["isBlocked"], "subdomain of blockedHosts entry is blocked")
	assert.Equal(t, true, got[0]["isSilenced"])
	assert.Equal(t, false, got[0]["isMediaSilenced"])
}

// TestInstances_MetaError: meta が読めないとき blocked/silenced 判定が
// できないので 500 を返す (本家 TS も metaService.fetch throw で 500)。
func TestInstances_MetaError(t *testing.T) {
	h, repo, metaRepo := newHandlerWithMeta(t)
	seedInstance(t, repo, "normal.example")
	metaRepo.Meta = nil
	c, rec := newReq(t, `{}`)
	require.NoError(t, h.Instances(c))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
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

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	shapetest.Assert(t, "FederationInstance", resp) // L3 (#1284)
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

// TestShowInstance_DBError は #915 review fix の regression guard。
// Service が raw DB error を返したら handler は 500 を返し、未知 host
// (= 204) と同じ扱いにしないこと。観測性確保。
func TestShowInstance_DBError(t *testing.T) {
	h, repo := newHandler(t)
	repo.FindByHostErr = errors.New("connection reset by peer")
	c, rec := newReq(t, `{"host":"alpha.example"}`)
	require.NoError(t, h.ShowInstance(c))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

// TestShowInstance_MetaError: instance 行は見つかっても meta が読めなければ
// isBlocked 等を埋められないので 500。
func TestShowInstance_MetaError(t *testing.T) {
	h, repo, metaRepo := newHandlerWithMeta(t)
	seedInstance(t, repo, "alpha.example")
	metaRepo.Meta = nil
	c, rec := newReq(t, `{"host":"alpha.example"}`)
	require.NoError(t, h.ShowInstance(c))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
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
