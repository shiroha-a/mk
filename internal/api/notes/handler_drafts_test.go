package notes

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
