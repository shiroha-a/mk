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
	"github.com/shiroha-a/mk/internal/testutil"
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

// create が replyId/renoteId/channelId/visibleUserIds/reactionAcceptance/
// localOnly/hashtag/poll を永続化し、{createdDraft} で full shape を返す。
func TestDraftsCreate_PersistsAllFieldsAndWraps(t *testing.T) {
	h, repo := newDraftHandlerWithRepo()
	body := `{"text":"t","visibility":"specified","visibleUserIds":["v1"],"localOnly":true,` +
		`"reactionAcceptance":"likeOnly","replyId":"r1","renoteId":"rn1","channelId":"ch1",` +
		`"hashtag":"tag","poll":{"choices":["a","b"],"multiple":true,"expiredAfter":3600000}}`
	rec := postDraft(h.DraftsCreate, body, &model.User{ID: "u1"})
	require.Equal(t, http.StatusOK, rec.Code)

	// 永続化検証。
	require.Len(t, repo.drafts, 1)
	var d *model.NoteDraft
	for _, v := range repo.drafts {
		d = v
	}
	require.NotNil(t, d.ReplyID)
	assert.Equal(t, "r1", *d.ReplyID)
	require.NotNil(t, d.RenoteID)
	assert.Equal(t, "rn1", *d.RenoteID)
	require.NotNil(t, d.ChannelID)
	assert.Equal(t, "ch1", *d.ChannelID)
	assert.Equal(t, []string{"v1"}, []string(d.VisibleUserIDs))
	assert.True(t, d.LocalOnly)
	require.NotNil(t, d.ReactionAcceptance)
	assert.Equal(t, "likeOnly", *d.ReactionAcceptance)
	require.NotNil(t, d.Hashtag)
	assert.Equal(t, "tag", *d.Hashtag)
	assert.True(t, d.HasPoll)
	assert.Equal(t, []string{"a", "b"}, []string(d.PollChoices))
	assert.True(t, d.PollMultiple)

	// レスポンスは {createdDraft: NoteDraft} で full shape。
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	cd, ok := resp["createdDraft"].(map[string]any)
	require.True(t, ok, "createdDraft で包む")
	assert.Equal(t, "r1", cd["replyId"])
	assert.Equal(t, "ch1", cd["channelId"])
	assert.Equal(t, []any{"v1"}, cd["visibleUserIds"])
	assert.Equal(t, "likeOnly", cd["reactionAcceptance"])
	poll, ok := cd["poll"].(map[string]any)
	require.True(t, ok, "poll object が出る")
	assert.Equal(t, []any{"a", "b"}, poll["choices"])
	assert.Equal(t, true, poll["multiple"])
	// user (UserLite) が埋まる。
	_, hasUser := cd["user"]
	assert.True(t, hasUser)
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

// update が新 param を反映し {updatedDraft} で包んで返す。
func TestDraftsUpdate_PersistsParamsAndWraps(t *testing.T) {
	h, repo := newDraftHandlerWithRepo()
	text := "old"
	repo.drafts["d1"] = &model.NoteDraft{ID: "d1", UserID: "u1", Text: &text, Visibility: "public"}
	body := `{"draftId":"d1","visibleUserIds":["v2"],"reactionAcceptance":"likeOnly","replyId":"rp","poll":{"choices":["x"],"multiple":false}}`
	rec := postDraft(h.DraftsUpdate, body, &model.User{ID: "u1"})
	require.Equal(t, http.StatusOK, rec.Code)

	d := repo.drafts["d1"]
	assert.Equal(t, []string{"v2"}, []string(d.VisibleUserIDs))
	require.NotNil(t, d.ReplyID)
	assert.Equal(t, "rp", *d.ReplyID)
	assert.True(t, d.HasPoll)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	ud, ok := resp["updatedDraft"].(map[string]any)
	require.True(t, ok, "updatedDraft で包む")
	assert.Equal(t, "rp", ud["replyId"])
	assert.Equal(t, []any{"v2"}, ud["visibleUserIds"])
}

// seedDraftFile registers a drive file owned by ownerID in the mock repo.
func seedDraftFile(repo *testutil.MockDriveFileRepository, fid, ownerID string) {
	uid := ownerID
	repo.Files[fid] = &model.DriveFile{ID: fid, UserID: &uid, Name: fid + ".png", Type: "image/png"}
}

// scheduledAt はレスポンス上 number (ms epoch) でなければならない (ISO 文字列で
// 返すと misskey-js の Date 変換が壊れる)。F1 回帰固定。
func TestDraftsCreate_ScheduledAtIsNumber(t *testing.T) {
	h, _ := newDraftHandlerWithRepo()
	enq := &scheduledEnqueueStub{}
	h.SetScheduledNoteEnqueuer(enq)
	at := futureMs(time.Hour)
	body := fmt.Sprintf(`{"text":"hi","scheduledAt":%d,"isActuallyScheduled":true}`, at)
	rec := postDraft(h.DraftsCreate, body, &model.User{ID: "u1"})
	require.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	cd := resp["createdDraft"].(map[string]any)
	// JSON number は encoding/json で float64 に decode される。
	v, ok := cd["scheduledAt"].(float64)
	require.True(t, ok, "scheduledAt は number で返る")
	assert.Equal(t, at, int64(v))
}

// poll.expiresAt が過去の場合は create を CANNOT_CREATE_ALREADY_EXPIRED_POLL で
// 弾く (upstream NoteDraftService と同 validation)。F4。
func TestDraftsCreate_ExpiredPollRejected(t *testing.T) {
	h, repo := newDraftHandlerWithRepo()
	past := time.Now().Add(-time.Hour).UnixMilli()
	body := fmt.Sprintf(`{"text":"t","poll":{"choices":["a","b"],"expiresAt":%d}}`, past)
	rec := postDraft(h.DraftsCreate, body, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "CANNOT_CREATE_ALREADY_EXPIRED_POLL")
	assert.Contains(t, rec.Body.String(), "04da457d-b083-4055-9082-955525eda5a5")
	assert.Empty(t, repo.drafts, "期限切れ poll では永続化しない")
}

// update でも過去 expiresAt の poll は弾く。F4。
func TestDraftsUpdate_ExpiredPollRejected(t *testing.T) {
	h, repo := newDraftHandlerWithRepo()
	repo.drafts["d1"] = &model.NoteDraft{ID: "d1", UserID: "u1", Visibility: "public"}
	past := time.Now().Add(-time.Hour).UnixMilli()
	body := fmt.Sprintf(`{"draftId":"d1","poll":{"choices":["a"],"expiresAt":%d}}`, past)
	rec := postDraft(h.DraftsUpdate, body, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "CANNOT_CREATE_ALREADY_EXPIRED_POLL")
	assert.False(t, repo.drafts["d1"].HasPoll, "期限切れ poll は適用しない")
}

// 所有していない fileId を含む create は NO_SUCH_FILE で弾く。F5。
func TestDraftsCreate_UnownedFileRejected(t *testing.T) {
	h, repo := newDraftHandlerWithRepo()
	fileRepo := testutil.NewMockDriveFileRepository()
	seedDraftFile(fileRepo, "f1", "u1")
	h.SetDriveFileRepo(fileRepo)
	// f2 は存在しない (= 所有検証で落ちる)。
	rec := postDraft(h.DraftsCreate, `{"text":"t","fileIds":["f1","f2"]}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "NO_SUCH_FILE")
	assert.Contains(t, rec.Body.String(), "b6992544-63e7-67f0-fa7f-32444b1b5306")
	assert.Empty(t, repo.drafts)
}

// 他人所有の fileId も NO_SUCH_FILE。F5 (所有権)。
func TestDraftsCreate_OtherUserFileRejected(t *testing.T) {
	h, _ := newDraftHandlerWithRepo()
	fileRepo := testutil.NewMockDriveFileRepository()
	seedDraftFile(fileRepo, "f1", "someone-else")
	h.SetDriveFileRepo(fileRepo)
	rec := postDraft(h.DraftsCreate, `{"text":"t","fileIds":["f1"]}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "NO_SUCH_FILE")
}

// update で所有していない fileId を渡すと NO_SUCH_FILE。F5。
func TestDraftsUpdate_UnownedFileRejected(t *testing.T) {
	h, repo := newDraftHandlerWithRepo()
	repo.drafts["d1"] = &model.NoteDraft{ID: "d1", UserID: "u1", Visibility: "public"}
	fileRepo := testutil.NewMockDriveFileRepository()
	h.SetDriveFileRepo(fileRepo)
	rec := postDraft(h.DraftsUpdate, `{"draftId":"d1","fileIds":["ghost"]}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "NO_SUCH_FILE")
}

// 所有 file は packDraft で fileIds の順序どおり files に解決される
// (resolveDraftFileMap が map でも順序は fileIds に従う)。
func TestDraftsCreate_OwnedFilesResolvedInOrder(t *testing.T) {
	h, _ := newDraftHandlerWithRepo()
	fileRepo := testutil.NewMockDriveFileRepository()
	seedDraftFile(fileRepo, "f1", "u1")
	seedDraftFile(fileRepo, "f2", "u1")
	h.SetDriveFileRepo(fileRepo)
	// fileIds を逆順 [f2,f1] で渡し、その順で files が並ぶことを確認。
	rec := postDraft(h.DraftsCreate, `{"text":"t","fileIds":["f2","f1"]}`, &model.User{ID: "u1"})
	require.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	cd := resp["createdDraft"].(map[string]any)
	files := cd["files"].([]any)
	require.Len(t, files, 2)
	assert.Equal(t, "f2", files[0].(map[string]any)["id"])
	assert.Equal(t, "f1", files[1].(map[string]any)["id"])
	assert.Equal(t, []any{"f2", "f1"}, cd["fileIds"])
}

// 省略した field は update で維持される (partial update)。
func TestDraftsUpdate_PartialUpdateMaintained(t *testing.T) {
	h, repo := newDraftHandlerWithRepo()
	oldText := "old"
	cw := "warn"
	repo.drafts["d1"] = &model.NoteDraft{
		ID: "d1", UserID: "u1", Text: &oldText, CW: &cw, Visibility: "home",
		LocalOnly: true, VisibleUserIDs: []string{"v1"},
	}
	// text だけ更新。他 field は維持される。
	rec := postDraft(h.DraftsUpdate, `{"draftId":"d1","text":"new"}`, &model.User{ID: "u1"})
	require.Equal(t, http.StatusOK, rec.Code)
	d := repo.drafts["d1"]
	assert.Equal(t, "new", *d.Text)
	require.NotNil(t, d.CW)
	assert.Equal(t, "warn", *d.CW, "cw は維持")
	assert.Equal(t, "home", d.Visibility, "visibility は維持")
	assert.True(t, d.LocalOnly, "localOnly は維持")
	assert.Equal(t, []string{"v1"}, []string(d.VisibleUserIDs), "visibleUserIds は維持")
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

// #1421: 他人 draftID を投げると NO_SUCH_NOTE_DRAFT 404 で reject される
// (IDOR regression guard)。現行 handler は repo.FindByIDAndUser に owner filter
// を SQL push-down しているので構造的に他人 draft を書き換えられないが、
// refactor で FindByID に入れ替わると owner check が抜ける。その回帰を
// handler 層 negative test で固定する。
func TestDraftsUpdate_OtherUserDraft(t *testing.T) {
	h, repo := newDraftHandlerWithRepo()
	text := "owner-only"
	repo.drafts["d1"] = &model.NoteDraft{ID: "d1", UserID: "u1", Text: &text, Visibility: "public"}
	rec := postDraft(h.DraftsUpdate, `{"draftId":"d1","text":"pwned"}`, &model.User{ID: "u2"})
	assert.Equal(t, http.StatusNotFound, rec.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	errObj := resp["error"].(map[string]any)
	assert.Equal(t, "NO_SUCH_NOTE_DRAFT", errObj["code"])
	assert.Equal(t, apierr.UUIDNoSuchNoteDraft, errObj["id"])
	// 他人 draft の text は変更されない
	assert.Equal(t, "owner-only", *repo.drafts["d1"].Text)
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
	calls    []queue.PostScheduledNotePayload
	cleared  []string
	err      error
	disabled bool // true で SupportsScheduledNote が false を返す (= asynq driver の simulate 用)
}

func (s *scheduledEnqueueStub) EnqueuePostScheduledNote(p queue.PostScheduledNotePayload, _ ...driver.EnqueueOption) error {
	if s.err != nil {
		return s.err
	}
	s.calls = append(s.calls, p)
	return nil
}

func (s *scheduledEnqueueStub) ClearScheduledNote(draftID string) error {
	s.cleared = append(s.cleared, draftID)
	return nil
}

// SupportsScheduledNote: default true (= mkq driver と同 capability)。
// `disabled=true` を set すると false を返し asynq driver の挙動を simulate
// する (= handler 側 capability gate test 用)。
func (s *scheduledEnqueueStub) SupportsScheduledNote() bool {
	return !s.disabled
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
	assert.Contains(t, rec.Body.String(), "15e28a55-e74c-4d65-89b7-8880cdaaa87d")
}

func TestDraftsCreate_ScheduledAtMustBeInFuture(t *testing.T) {
	h, _ := newDraftHandlerWithRepo()
	past := time.Now().Add(-time.Hour).UnixMilli()
	body := fmt.Sprintf(`{"text":"hi","scheduledAt":%d,"isActuallyScheduled":true}`, past)
	rec := postDraft(h.DraftsCreate, body, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "SCHEDULED_AT_MUST_BE_IN_FUTURE")
	assert.Contains(t, rec.Body.String(), "e4bed6c9-017e-4934-aed0-01c22cc60ec1")
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
	assert.Contains(t, rec.Body.String(), "22ae69eb-09e3-4541-a850-773cfa45e693")
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

// driver capability が false (= asynq driver) のときは scheduled note 作成を
// TOO_MANY_SCHEDULED_NOTES で reject する (#1045 Phase 2-C)。
func TestDraftsCreate_AsynqDriverDisablesScheduled(t *testing.T) {
	h, _ := newDraftHandlerWithRepo()
	enq := &scheduledEnqueueStub{disabled: true}
	h.SetScheduledNoteEnqueuer(enq)
	body := fmt.Sprintf(`{"text":"hi","scheduledAt":%d,"isActuallyScheduled":true}`, futureMs(time.Hour))
	rec := postDraft(h.DraftsCreate, body, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "TOO_MANY_SCHEDULED_NOTES")
	assert.Empty(t, enq.calls, "capability false なら enqueue されない")
}

// --- #1045 Phase 2-C: DraftsUpdate / DraftsDelete scheduled note 経路 ---

// scheduledAt を変更すると旧 delayed task を clear して新時刻で re-enqueue。
func TestDraftsUpdate_RescheduleClearsAndReenqueues(t *testing.T) {
	h, repo := newDraftHandlerWithRepo()
	enq := &scheduledEnqueueStub{}
	h.SetScheduledNoteEnqueuer(enq)
	oldT := time.Now().Add(time.Hour)
	repo.drafts["d1"] = &model.NoteDraft{
		ID: "d1", UserID: "u1", IsActuallyScheduled: true, ScheduledAt: &oldT,
	}
	body := fmt.Sprintf(`{"draftId":"d1","scheduledAt":%d,"isActuallyScheduled":true}`, futureMs(2*time.Hour))
	rec := postDraft(h.DraftsUpdate, body, &model.User{ID: "u1"})
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, []string{"d1"}, enq.cleared, "旧 task を clear")
	require.Len(t, enq.calls, 1, "新 task を 1 度 enqueue")
	assert.Equal(t, "d1", enq.calls[0].NoteDraftID)
}

// isActuallyScheduled=false への切り替えは clear のみ (= 再 enqueue しない)。
func TestDraftsUpdate_UnscheduleClearsOnly(t *testing.T) {
	h, repo := newDraftHandlerWithRepo()
	enq := &scheduledEnqueueStub{}
	h.SetScheduledNoteEnqueuer(enq)
	t1 := time.Now().Add(time.Hour)
	repo.drafts["d1"] = &model.NoteDraft{
		ID: "d1", UserID: "u1", IsActuallyScheduled: true, ScheduledAt: &t1,
	}
	body := `{"draftId":"d1","isActuallyScheduled":false}`
	rec := postDraft(h.DraftsUpdate, body, &model.User{ID: "u1"})
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, []string{"d1"}, enq.cleared, "clear は呼ばれる")
	assert.Empty(t, enq.calls, "isActuallyScheduled=false で再 enqueue は走らない")
}

// scheduled 関連の field が無い update では clear / enqueue 経路は走らない
// (= 旧挙動互換、scheduledAt 既存 draft への text 編集等を阻害しない)。
func TestDraftsUpdate_NonScheduledChangeDoesNotTouchQueue(t *testing.T) {
	h, repo := newDraftHandlerWithRepo()
	enq := &scheduledEnqueueStub{}
	h.SetScheduledNoteEnqueuer(enq)
	t1 := time.Now().Add(time.Hour)
	repo.drafts["d1"] = &model.NoteDraft{
		ID: "d1", UserID: "u1", IsActuallyScheduled: true, ScheduledAt: &t1,
	}
	body := `{"draftId":"d1","text":"updated"}`
	rec := postDraft(h.DraftsUpdate, body, &model.User{ID: "u1"})
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Empty(t, enq.cleared)
	assert.Empty(t, enq.calls)
}

// asynq driver では update での scheduled 切替も reject (= capability gate)。
func TestDraftsUpdate_AsynqDriverDisablesScheduled(t *testing.T) {
	h, repo := newDraftHandlerWithRepo()
	enq := &scheduledEnqueueStub{disabled: true}
	h.SetScheduledNoteEnqueuer(enq)
	repo.drafts["d1"] = &model.NoteDraft{ID: "d1", UserID: "u1"}
	body := fmt.Sprintf(`{"draftId":"d1","scheduledAt":%d,"isActuallyScheduled":true}`, futureMs(time.Hour))
	rec := postDraft(h.DraftsUpdate, body, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "TOO_MANY_SCHEDULED_NOTES")
}

// Delete 経由でも旧 delayed task を clear する (= unschedule された draft の
// processor 早期 fire を防ぐ best-effort)。
func TestDraftsDelete_ClearsScheduledNote(t *testing.T) {
	h, repo := newDraftHandlerWithRepo()
	enq := &scheduledEnqueueStub{}
	h.SetScheduledNoteEnqueuer(enq)
	repo.drafts["d1"] = &model.NoteDraft{ID: "d1", UserID: "u1"}
	rec := postDraft(h.DraftsDelete, `{"draftId":"d1"}`, &model.User{ID: "u1"})
	require.Equal(t, http.StatusNoContent, rec.Code)
	assert.Equal(t, []string{"d1"}, enq.cleared)
}
