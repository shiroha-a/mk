package notes

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shiroha-a/mk/internal/queue"
	"github.com/shiroha-a/mk/internal/queue/driver"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/api/apierr"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/server/middleware"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func postDraft(handler func(echo.Context) error, body string, user *model.User) *httptest.ResponseRecorder {
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

func newDraftHandler() *Handler {
	idGen, _ := id.NewGenerator("aidx")
	return &Handler{idGen: idGen}
}

// --- Mock DraftRepo ---

var errDraftMock = assert.AnError

type mockDraftRepo struct {
	drafts    map[string]*model.NoteDraft
	createErr error
}

func newMockDraftRepo() *mockDraftRepo {
	return &mockDraftRepo{drafts: make(map[string]*model.NoteDraft)}
}

func (m *mockDraftRepo) Create(d *model.NoteDraft) error {
	if m.createErr != nil {
		return m.createErr
	}
	m.drafts[d.ID] = d
	return nil
}
func (m *mockDraftRepo) FindByIDAndUser(id, userID string) (*model.NoteDraft, error) {
	if d, ok := m.drafts[id]; ok && d.UserID == userID {
		return d, nil
	}
	return nil, errDraftMock
}
func (m *mockDraftRepo) ListByUser(userID string, _ int) ([]*model.NoteDraft, error) {
	var result []*model.NoteDraft
	for _, d := range m.drafts {
		if d.UserID == userID {
			result = append(result, d)
		}
	}
	return result, nil
}
func (m *mockDraftRepo) Update(d *model.NoteDraft) error {
	m.drafts[d.ID] = d
	return nil
}
func (m *mockDraftRepo) Delete(id, _ string) (int64, error) {
	if _, ok := m.drafts[id]; !ok {
		return 0, nil
	}
	delete(m.drafts, id)
	return 1, nil
}
func (m *mockDraftRepo) CountByUser(userID string) (int64, error) {
	var count int64
	for _, d := range m.drafts {
		if d.UserID == userID {
			count++
		}
	}
	return count, nil
}

func (m *mockDraftRepo) FindByID(id string) (*model.NoteDraft, error) {
	if d, ok := m.drafts[id]; ok {
		return d, nil
	}
	return nil, errors.New("note draft not found")
}

func (m *mockDraftRepo) CountScheduledByUser(userID string) (int64, error) {
	var count int64
	for _, d := range m.drafts {
		if d.UserID == userID && d.IsActuallyScheduled {
			count++
		}
	}
	return count, nil
}

func newDraftHandlerWithRepo() (*Handler, *mockDraftRepo) {
	h := newDraftHandler()
	repo := newMockDraftRepo()
	h.draftRepo = repo
	return h, repo
}

// --- DraftsList ---

func TestDraftsList_NilRepo(t *testing.T) {
	h := newDraftHandler()
	rec := postDraft(h.DraftsList, `{}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "[]\n", rec.Body.String())
}

func TestDraftsList_WithData(t *testing.T) {
	h, repo := newDraftHandlerWithRepo()
	text := "hello"
	repo.drafts["d1"] = &model.NoteDraft{ID: "d1", UserID: "u1", Text: &text, Visibility: "public"}
	rec := postDraft(h.DraftsList, `{}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusOK, rec.Code)
	var resp []any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Len(t, resp, 1)
}

// --- DraftsCreate ---

func TestDraftsCreate_NilRepo(t *testing.T) {
	h := newDraftHandler()
	rec := postDraft(h.DraftsCreate, `{"text":"hello"}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

func TestDraftsCreate_Success(t *testing.T) {
	h, repo := newDraftHandlerWithRepo()
	rec := postDraft(h.DraftsCreate, `{"text":"draft text"}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Len(t, repo.drafts, 1)
}

func TestDraftsCreate_DefaultVisibility(t *testing.T) {
	h, _ := newDraftHandlerWithRepo()
	rec := postDraft(h.DraftsCreate, `{"text":"test"}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestDraftsCreate_InvalidJSON(t *testing.T) {
	h, _ := newDraftHandlerWithRepo()
	rec := postDraft(h.DraftsCreate, `invalid`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestDraftsCreate_Error(t *testing.T) {
	h, repo := newDraftHandlerWithRepo()
	repo.createErr = errDraftMock
	rec := postDraft(h.DraftsCreate, `{"text":"x"}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

// #1029: noteDraftLimit role policy gate test。
type draftStubPolicyProvider struct {
	policies map[string]any
}

func (s *draftStubPolicyProvider) GetUserPolicies(_ string) map[string]any {
	return s.policies
}

func TestDraftsCreate_NoteDraftLimitExceeded(t *testing.T) {
	h, repo := newDraftHandlerWithRepo()
	repo.drafts["d1"] = &model.NoteDraft{ID: "d1", UserID: "u1"}
	repo.drafts["d2"] = &model.NoteDraft{ID: "d2", UserID: "u1"}
	h.SetPolicyProvider(&draftStubPolicyProvider{policies: map[string]any{
		"noteDraftLimit": 2,
	}})
	rec := postDraft(h.DraftsCreate, `{"text":"third"}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "TOO_MANY_NOTE_DRAFTS")
}

func TestDraftsCreate_NoteDraftLimit_PassesUnderLimit(t *testing.T) {
	h, _ := newDraftHandlerWithRepo()
	h.SetPolicyProvider(&draftStubPolicyProvider{policies: map[string]any{
		"noteDraftLimit": 10,
	}})
	rec := postDraft(h.DraftsCreate, `{"text":"draft"}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusOK, rec.Code)
}

// --- DraftsUpdate ---

func TestDraftsUpdate_NilRepo(t *testing.T) {
	h := newDraftHandler()
	rec := postDraft(h.DraftsUpdate, `{"draftId":"d1"}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

func TestDraftsUpdate_Success(t *testing.T) {
	h, repo := newDraftHandlerWithRepo()
	text := "old"
	repo.drafts["d1"] = &model.NoteDraft{ID: "d1", UserID: "u1", Text: &text, Visibility: "public"}
	rec := postDraft(h.DraftsUpdate, `{"draftId":"d1","text":"new","visibility":"home"}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "new", *repo.drafts["d1"].Text)
}

func TestDraftsUpdate_NotFound(t *testing.T) {
	// #688: 存在しない draftId 指定時に upstream notes/drafts/update 互換の
	// NO_SUCH_NOTE_DRAFT (UUID 49cd6b9d-...) を返すこと。
	h, _ := newDraftHandlerWithRepo()
	rec := postDraft(h.DraftsUpdate, `{"draftId":"ghost"}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusNotFound, rec.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	errObj := resp["error"].(map[string]any)
	assert.Equal(t, "NO_SUCH_NOTE_DRAFT", errObj["code"])
	assert.Equal(t, apierr.UUIDNoSuchNoteDraft, errObj["id"])
}

func TestDraftsUpdate_InvalidParam(t *testing.T) {
	h, _ := newDraftHandlerWithRepo()
	rec := postDraft(h.DraftsUpdate, `{}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// --- DraftsDelete ---

func TestDraftsDelete_NilRepo(t *testing.T) {
	h := newDraftHandler()
	rec := postDraft(h.DraftsDelete, `{"draftId":"d1"}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

func TestDraftsDelete_Success(t *testing.T) {
	h, repo := newDraftHandlerWithRepo()
	repo.drafts["d1"] = &model.NoteDraft{ID: "d1", UserID: "u1"}
	rec := postDraft(h.DraftsDelete, `{"draftId":"d1"}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Empty(t, repo.drafts)
}

func TestDraftsDelete_InvalidParam(t *testing.T) {
	h, _ := newDraftHandlerWithRepo()
	rec := postDraft(h.DraftsDelete, `{}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestDraftsDelete_NotFound(t *testing.T) {
	// #688 review follow-up: 存在しない draftId で削除しても upstream
	// notes/drafts/delete.ts と同じく `NO_SUCH_NOTE_DRAFT` を返す。silent
	// 204 だと frontend が「該当 draft が無い」を観測できないので 404 が
	// 期待挙動。
	h, _ := newDraftHandlerWithRepo()
	rec := postDraft(h.DraftsDelete, `{"draftId":"ghost"}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusNotFound, rec.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	errObj := resp["error"].(map[string]any)
	assert.Equal(t, "NO_SUCH_NOTE_DRAFT", errObj["code"])
	assert.Equal(t, apierr.UUIDNoSuchNoteDraft, errObj["id"])
}

// --- DraftsCount ---

func TestDraftsCount_NilRepo(t *testing.T) {
	h := newDraftHandler()
	rec := postDraft(h.DraftsCount, `{}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"count":0`)
}

func TestDraftsCount_WithData(t *testing.T) {
	h, repo := newDraftHandlerWithRepo()
	repo.drafts["d1"] = &model.NoteDraft{ID: "d1", UserID: "u1"}
	rec := postDraft(h.DraftsCount, `{}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"count":1`)
}

// --- ThreadMuting ---

func TestThreadMutingCreate_Success(t *testing.T) {
	h := newDraftHandler()
	rec := postDraft(h.ThreadMutingCreate, `{"noteId":"n1"}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

func TestThreadMutingCreate_InvalidParam(t *testing.T) {
	h := newDraftHandler()
	rec := postDraft(h.ThreadMutingCreate, `{}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestThreadMutingDelete_Success(t *testing.T) {
	h := newDraftHandler()
	rec := postDraft(h.ThreadMutingDelete, `{"noteId":"n1"}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

func TestThreadMutingDelete_InvalidParam(t *testing.T) {
	h := newDraftHandler()
	rec := postDraft(h.ThreadMutingDelete, `{}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// --- PollsRecommendation ---

func TestPollsRecommendation(t *testing.T) {
	h := newDraftHandler()
	rec := postDraft(h.PollsRecommendation, `{}`, nil)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "[]\n", rec.Body.String())
}

// --- #1040: scheduled note (drafts/create with scheduledAt) ---

// scheduledEnqueueStub captures EnqueuePostScheduledNote calls for assertion.
type scheduledEnqueueStub struct {
	calls []queue.PostScheduledNotePayload
	err   error
}

func (s *scheduledEnqueueStub) EnqueuePostScheduledNote(p queue.PostScheduledNotePayload, _ ...driver.EnqueueOption) error {
	if s.err != nil {
		return s.err
	}
	s.calls = append(s.calls, p)
	return nil
}

func futureMs(d time.Duration) int64 {
	return time.Now().Add(d).UnixMilli()
}

func TestDraftsCreate_ScheduledHappy(t *testing.T) {
	h, repo := newDraftHandlerWithRepo()
	enq := &scheduledEnqueueStub{}
	h.SetScheduledNoteEnqueuer(enq)
	body := fmt.Sprintf(`{"text":"hi","scheduledAt":%d,"isActuallyScheduled":true}`, futureMs(time.Hour))
	rec := postDraft(h.DraftsCreate, body, &model.User{ID: "u1"})
	require.Equal(t, http.StatusOK, rec.Code)
	require.Len(t, repo.drafts, 1)
	require.Len(t, enq.calls, 1, "scheduled draft 1 件で enqueue が 1 度走る")
	// draft.IsActuallyScheduled=true で保存される
	for _, d := range repo.drafts {
		assert.True(t, d.IsActuallyScheduled)
		require.NotNil(t, d.ScheduledAt)
	}
}

func TestDraftsCreate_ScheduledAtRequired(t *testing.T) {
	h, _ := newDraftHandlerWithRepo()
	rec := postDraft(h.DraftsCreate, `{"text":"hi","isActuallyScheduled":true}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "SCHEDULED_AT_REQUIRED")
	assert.Contains(t, rec.Body.String(), "94a89a43-3591-400a-9c17-dd166e71fdfa")
}

func TestDraftsCreate_ScheduledAtMustBeInFuture(t *testing.T) {
	h, _ := newDraftHandlerWithRepo()
	past := time.Now().Add(-time.Hour).UnixMilli()
	body := fmt.Sprintf(`{"text":"hi","scheduledAt":%d,"isActuallyScheduled":true}`, past)
	rec := postDraft(h.DraftsCreate, body, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "SCHEDULED_AT_MUST_BE_IN_FUTURE")
	assert.Contains(t, rec.Body.String(), "b34d0c1b-996f-4e34-a428-c636d98df457")
}

func TestDraftsCreate_ScheduledNoteLimitExceeded(t *testing.T) {
	h, repo := newDraftHandlerWithRepo()
	repo.drafts["d1"] = &model.NoteDraft{ID: "d1", UserID: "u1", IsActuallyScheduled: true}
	repo.drafts["d2"] = &model.NoteDraft{ID: "d2", UserID: "u1", IsActuallyScheduled: true}
	h.SetPolicyProvider(&draftStubPolicyProvider{policies: map[string]any{
		"scheduledNoteLimit": 2,
	}})
	body := fmt.Sprintf(`{"text":"third","scheduledAt":%d,"isActuallyScheduled":true}`, futureMs(time.Hour))
	rec := postDraft(h.DraftsCreate, body, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "TOO_MANY_SCHEDULED_NOTES")
	assert.Contains(t, rec.Body.String(), "c3275f19-4558-4c59-83e1-4f684b5fab66")
}

// isActuallyScheduled=false で scheduledAt が指定されても enqueue は走らない
// (= 単なる「下書きとしての希望時刻」)。
func TestDraftsCreate_ScheduledAtWithoutActuallyScheduled(t *testing.T) {
	h, _ := newDraftHandlerWithRepo()
	enq := &scheduledEnqueueStub{}
	h.SetScheduledNoteEnqueuer(enq)
	body := fmt.Sprintf(`{"text":"hi","scheduledAt":%d}`, futureMs(time.Hour))
	rec := postDraft(h.DraftsCreate, body, &model.User{ID: "u1"})
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Empty(t, enq.calls, "isActuallyScheduled=false なら enqueue されない")
}
