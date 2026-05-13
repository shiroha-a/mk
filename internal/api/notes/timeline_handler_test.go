package notes

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	corenote "github.com/shiroha-a/mk/internal/core/note"
	coretimeline "github.com/shiroha-a/mk/internal/core/timeline"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
)

var testRedis *testutil.TestRedis

// TestMain provisions a shared Redis container for the timeline handler tests.
// 既存の non-timeline テスト群はRedisを必要としないので、コンテナ起動失敗時は
// テストを単純にスキップして他テストの実行を妨げないようにしている。
func TestMain(m *testing.M) {
	ctx := context.Background()
	tr, err := testutil.SetupRedis(ctx)
	if err != nil {
		log.Printf("redis setup failed: %v (skipping timeline handler tests)", err)
	} else {
		testRedis = tr
	}
	code := m.Run()
	if testRedis != nil {
		testRedis.Teardown(ctx)
	}
	os.Exit(code)
}

// requireRedis skips the test if no Redis container is available.
func requireRedis(t *testing.T) {
	t.Helper()
	if testRedis == nil {
		t.Skip("redis container not available")
	}
}

// newRealTimelineService creates a coretimeline.Service backed by the shared
// Redis container.
func newRealTimelineService(t *testing.T, noteRepo *testutil.MockNoteRepository) (*coretimeline.Service, *coretimeline.FanoutTimelineService) {
	t.Helper()
	requireRedis(t)
	testRedis.FlushAll(context.Background())
	idGen, _ := id.NewGenerator("aidx")
	fanout := coretimeline.NewFanoutTimelineService(testRedis.Client, idGen, "")
	svc := coretimeline.NewService(fanout, noteRepo, testutil.NewMockFollowingRepository())
	return svc, fanout
}

// closedRedisClient returns a redis.Client whose connection has been closed,
// so any command on it returns an error.
func closedRedisClient(t *testing.T) *redis.Client {
	t.Helper()
	c := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
	_ = c.Close()
	return c
}

func newFailingTimelineService(t *testing.T) *coretimeline.Service {
	t.Helper()
	idGen, _ := id.NewGenerator("aidx")
	fanout := coretimeline.NewFanoutTimelineService(closedRedisClient(t), idGen, "")
	return coretimeline.NewService(fanout, testutil.NewMockNoteRepository(), nil)
}

func newTimelineHandler(t *testing.T, noteRepo *testutil.MockNoteRepository, tl *coretimeline.Service) *Handler {
	t.Helper()
	pollRepo := testutil.NewMockPollRepository()
	idGen, _ := id.NewGenerator("aidx")
	createSvc := corenote.NewCreateService(noteRepo, pollRepo, idGen, nil)
	deleteSvc := corenote.NewDeleteService(noteRepo)
	querySvc := corenote.NewQueryService(noteRepo, nil)
	return NewHandler(noteRepo, createSvc, deleteSvc, querySvc, tl, nil, nil, nil, idGen)
}

// seedTimelineNote creates a note in the repo and returns its id.
func seedTimelineNote(repo *testutil.MockNoteRepository) string {
	idGen, _ := id.NewGenerator("aidx")
	noteID := idGen.Generate(time.Now())
	repo.Notes[noteID] = &model.Note{
		ID:         noteID,
		UserID:     "author",
		Visibility: model.NoteVisibilityPublic,
		Reactions:  datatypes.JSON([]byte("{}")),
		User: &model.User{
			ID:                "author",
			Username:          "author",
			AvatarDecorations: datatypes.JSON([]byte("[]")),
		},
	}
	return noteID
}

func TestTimeline_AuthRequired(t *testing.T) {
	noteRepo := testutil.NewMockNoteRepository()
	h := newTimelineHandler(t, noteRepo, newFailingTimelineService(t))

	c, rec := newJSONRequest(t, "/api/notes/timeline", `{}`)
	require.NoError(t, h.Timeline(c))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestTimeline_InvalidJSON(t *testing.T) {
	noteRepo := testutil.NewMockNoteRepository()
	h := newTimelineHandler(t, noteRepo, newFailingTimelineService(t))

	c, rec := newJSONRequest(t, "/api/notes/timeline", `{invalid`)
	setAuthUser(c, &model.User{ID: "u"})
	require.NoError(t, h.Timeline(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestTimeline_FanoutError(t *testing.T) {
	noteRepo := testutil.NewMockNoteRepository()
	h := newTimelineHandler(t, noteRepo, newFailingTimelineService(t))

	c, rec := newJSONRequest(t, "/api/notes/timeline", `{}`)
	setAuthUser(c, &model.User{ID: "u"})
	require.NoError(t, h.Timeline(c))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestLocalTimeline_FanoutError(t *testing.T) {
	noteRepo := testutil.NewMockNoteRepository()
	h := newTimelineHandler(t, noteRepo, newFailingTimelineService(t))

	c, rec := newJSONRequest(t, "/api/notes/local-timeline", `{}`)
	require.NoError(t, h.LocalTimeline(c))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestGlobalTimeline_FanoutError(t *testing.T) {
	noteRepo := testutil.NewMockNoteRepository()
	h := newTimelineHandler(t, noteRepo, newFailingTimelineService(t))

	c, rec := newJSONRequest(t, "/api/notes/global-timeline", `{}`)
	require.NoError(t, h.GlobalTimeline(c))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestHybridTimeline_AuthRequired(t *testing.T) {
	noteRepo := testutil.NewMockNoteRepository()
	h := newTimelineHandler(t, noteRepo, newFailingTimelineService(t))

	c, rec := newJSONRequest(t, "/api/notes/hybrid-timeline", `{}`)
	require.NoError(t, h.HybridTimeline(c))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestHybridTimeline_FanoutError(t *testing.T) {
	noteRepo := testutil.NewMockNoteRepository()
	h := newTimelineHandler(t, noteRepo, newFailingTimelineService(t))

	c, rec := newJSONRequest(t, "/api/notes/hybrid-timeline", `{}`)
	setAuthUser(c, &model.User{ID: "u"})
	require.NoError(t, h.HybridTimeline(c))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

// #1026: ltlAvailable / gtlAvailable role policy gate。匿名でも認証済みでも
// 同じ pattern で gate される (upstream Misskey TS の getUserPolicies(me ?
// me.id : null) 経路と同 semantics、handler 内 gate)。
type stubTimelinePolicyProvider struct {
	policiesByUser map[string]map[string]any // userID -> policies (""=anonymous)
}

func (s *stubTimelinePolicyProvider) GetUserPolicies(userID string) map[string]any {
	if p, ok := s.policiesByUser[userID]; ok {
		return p
	}
	return map[string]any{}
}

func TestLocalTimeline_LtlDisabled_Anonymous(t *testing.T) {
	noteRepo := testutil.NewMockNoteRepository()
	h := newTimelineHandler(t, noteRepo, newFailingTimelineService(t))
	h.SetPolicyProvider(&stubTimelinePolicyProvider{
		policiesByUser: map[string]map[string]any{
			"": {"ltlAvailable": false},
		},
	})

	c, rec := newJSONRequest(t, "/api/notes/local-timeline", `{}`)
	require.NoError(t, h.LocalTimeline(c))
	assert.Equal(t, http.StatusForbidden, rec.Code, "policy=false で 403")
	assert.Contains(t, rec.Body.String(), "LTL_DISABLED")
	assert.Contains(t, rec.Body.String(), "45a6eb02-7695-4393-b023-dd3be9aaaefd")
}

func TestLocalTimeline_LtlDisabled_AuthenticatedUser(t *testing.T) {
	noteRepo := testutil.NewMockNoteRepository()
	h := newTimelineHandler(t, noteRepo, newFailingTimelineService(t))
	h.SetPolicyProvider(&stubTimelinePolicyProvider{
		policiesByUser: map[string]map[string]any{
			"alice": {"ltlAvailable": false},
		},
	})

	c, rec := newJSONRequest(t, "/api/notes/local-timeline", `{}`)
	setAuthUser(c, &model.User{ID: "alice"})
	require.NoError(t, h.LocalTimeline(c))
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "LTL_DISABLED")
}

func TestLocalTimeline_LtlEnabled_PassesThrough(t *testing.T) {
	noteRepo := testutil.NewMockNoteRepository()
	h := newTimelineHandler(t, noteRepo, newFailingTimelineService(t))
	h.SetPolicyProvider(&stubTimelinePolicyProvider{
		policiesByUser: map[string]map[string]any{
			"": {"ltlAvailable": true},
		},
	})

	c, rec := newJSONRequest(t, "/api/notes/local-timeline", `{}`)
	require.NoError(t, h.LocalTimeline(c))
	// policy=true なら gate 通過、後段の failing service で 500 になる
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestGlobalTimeline_GtlDisabled(t *testing.T) {
	noteRepo := testutil.NewMockNoteRepository()
	h := newTimelineHandler(t, noteRepo, newFailingTimelineService(t))
	h.SetPolicyProvider(&stubTimelinePolicyProvider{
		policiesByUser: map[string]map[string]any{
			"": {"gtlAvailable": false},
		},
	})

	c, rec := newJSONRequest(t, "/api/notes/global-timeline", `{}`)
	require.NoError(t, h.GlobalTimeline(c))
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "GTL_DISABLED")
	assert.Contains(t, rec.Body.String(), "0332fc13-6ab2-4427-ae80-a9fadffd1a6b")
}

// hybrid-timeline は ltlAvailable で gate する (= upstream と同 logic)。
// gtlAvailable=false でも hybrid は許可される (= ltl が effective なので)。
func TestHybridTimeline_LtlDisabledRejects(t *testing.T) {
	noteRepo := testutil.NewMockNoteRepository()
	h := newTimelineHandler(t, noteRepo, newFailingTimelineService(t))
	h.SetPolicyProvider(&stubTimelinePolicyProvider{
		policiesByUser: map[string]map[string]any{
			"alice": {"ltlAvailable": false, "gtlAvailable": true},
		},
	})

	c, rec := newJSONRequest(t, "/api/notes/hybrid-timeline", `{}`)
	setAuthUser(c, &model.User{ID: "alice"})
	require.NoError(t, h.HybridTimeline(c))
	assert.Equal(t, http.StatusForbidden, rec.Code, "hybrid は ltlAvailable で gate")
	assert.Contains(t, rec.Body.String(), "LTL_DISABLED")
}

// policyProvider 未配線時は gate を skip する (= 旧挙動互換 / test fixture)。
func TestLocalTimeline_NoPolicyProviderSkipsGate(t *testing.T) {
	noteRepo := testutil.NewMockNoteRepository()
	h := newTimelineHandler(t, noteRepo, newFailingTimelineService(t))
	// SetPolicyProvider を呼ばない

	c, rec := newJSONRequest(t, "/api/notes/local-timeline", `{}`)
	require.NoError(t, h.LocalTimeline(c))
	// gate を skip して後段の failing service で 500
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

// policies map に該当 key が無い場合は fail-soft で許可 (= upstream は明示的
// に key を返すが、mk-go の type assert 失敗時は安全側で gate skip)。
func TestLocalTimeline_MissingPolicyKeyIsFailSoft(t *testing.T) {
	noteRepo := testutil.NewMockNoteRepository()
	h := newTimelineHandler(t, noteRepo, newFailingTimelineService(t))
	h.SetPolicyProvider(&stubTimelinePolicyProvider{
		policiesByUser: map[string]map[string]any{
			"": {}, // empty policies map (= key 不在)
		},
	})

	c, rec := newJSONRequest(t, "/api/notes/local-timeline", `{}`)
	require.NoError(t, h.LocalTimeline(c))
	assert.Equal(t, http.StatusInternalServerError, rec.Code, "key 不在は fail-soft 許可")
}

func TestTimeline_HappyPathHome(t *testing.T) {
	noteRepo := testutil.NewMockNoteRepository()
	noteID := seedTimelineNote(noteRepo)
	tl, fanout := newRealTimelineService(t, noteRepo)
	require.NoError(t, fanout.Push(context.Background(), coretimeline.HomeTimelineName("viewer"), noteID, 100))

	h := newTimelineHandler(t, noteRepo, tl)
	c, rec := newJSONRequest(t, "/api/notes/timeline", `{}`)
	setAuthUser(c, &model.User{ID: "viewer"})
	require.NoError(t, h.Timeline(c))
	require.Equal(t, http.StatusOK, rec.Code)

	var resp []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Len(t, resp, 1)
}

func TestTimeline_HappyPathLocal(t *testing.T) {
	noteRepo := testutil.NewMockNoteRepository()
	noteID := seedTimelineNote(noteRepo)
	tl, fanout := newRealTimelineService(t, noteRepo)
	require.NoError(t, fanout.Push(context.Background(), coretimeline.LocalTimeline, noteID, 100))

	h := newTimelineHandler(t, noteRepo, tl)
	// limit clamping もここで踏んでおく
	c, rec := newJSONRequest(t, "/api/notes/local-timeline", `{"limit":1000}`)
	require.NoError(t, h.LocalTimeline(c))
	require.Equal(t, http.StatusOK, rec.Code)
	var resp []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Len(t, resp, 1)
}

func TestTimeline_HappyPathGlobal(t *testing.T) {
	noteRepo := testutil.NewMockNoteRepository()
	noteID := seedTimelineNote(noteRepo)
	tl, fanout := newRealTimelineService(t, noteRepo)
	require.NoError(t, fanout.Push(context.Background(), coretimeline.GlobalTimeline, noteID, 100))

	h := newTimelineHandler(t, noteRepo, tl)
	c, rec := newJSONRequest(t, "/api/notes/global-timeline", `{}`)
	require.NoError(t, h.GlobalTimeline(c))
	require.Equal(t, http.StatusOK, rec.Code)
}

func TestTimeline_HappyPathHybrid(t *testing.T) {
	noteRepo := testutil.NewMockNoteRepository()
	noteID := seedTimelineNote(noteRepo)
	tl, fanout := newRealTimelineService(t, noteRepo)
	ctx := context.Background()
	require.NoError(t, fanout.Push(ctx, coretimeline.HomeTimelineName("viewer"), noteID, 100))

	h := newTimelineHandler(t, noteRepo, tl)
	c, rec := newJSONRequest(t, "/api/notes/hybrid-timeline", `{}`)
	setAuthUser(c, &model.User{ID: "viewer"})
	require.NoError(t, h.HybridTimeline(c))
	require.Equal(t, http.StatusOK, rec.Code)
}
