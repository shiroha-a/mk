package admin_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/shiroha-a/mk/internal/entitycompat/shapetest"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- SystemWebhook -----------------------------------------------------------

func TestSystemWebhookCreate_Success(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	repo := testutil.NewMockSystemWebhookRepository()
	h.SetSystemWebhookRepo(repo)

	rec := doPost(h.SystemWebhookCreate,
		`{"name":"hook1","url":"https://example.com/hook","secret":"s","on":["abuseReport"],"isActive":true}`,
		adminUser)
	assert.Equal(t, http.StatusOK, rec.Code)

	var got model.SystemWebhook
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, "hook1", got.Name)
	assert.Equal(t, "https://example.com/hook", got.URL)
	assert.True(t, got.IsActive)
	assert.Contains(t, repo.Webhooks, got.ID)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &raw))
	shapetest.Assert(t, "SystemWebhook", raw) // L3 (#1294)
}

func TestSystemWebhookCreate_MissingFields(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	h.SetSystemWebhookRepo(testutil.NewMockSystemWebhookRepository())
	rec := doPost(h.SystemWebhookCreate, `{"name":""}`, adminUser)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// upstream create.ts required:[isActive,name,on,url]。on / isActive 欠落は 400 (#1542)。
func TestSystemWebhookCreate_RequiredOnIsActive(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	h.SetSystemWebhookRepo(testutil.NewMockSystemWebhookRepository())
	// on 欠落
	assert.Equal(t, http.StatusBadRequest,
		doPost(h.SystemWebhookCreate, `{"name":"x","url":"https://x","isActive":true}`, adminUser).Code)
	// isActive 欠落
	assert.Equal(t, http.StatusBadRequest,
		doPost(h.SystemWebhookCreate, `{"name":"x","url":"https://x","on":["abuseReport"]}`, adminUser).Code)
}

// on は systemWebhookEventTypes enum のみ受理。未知値は 400 (#1542)。
func TestSystemWebhookCreate_InvalidEventType(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	h.SetSystemWebhookRepo(testutil.NewMockSystemWebhookRepository())
	rec := doPost(h.SystemWebhookCreate,
		`{"name":"x","url":"https://x","on":["bogusEvent"],"isActive":true}`, adminUser)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// update も on の enum を検証する (#1542)。
func TestSystemWebhookUpdate_InvalidEventType(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	repo := testutil.NewMockSystemWebhookRepository()
	require.NoError(t, repo.Create(&model.SystemWebhook{ID: "w1", Name: "x", URL: "https://x"}))
	h.SetSystemWebhookRepo(repo)
	rec := doPost(h.SystemWebhookUpdate, `{"id":"w1","on":["bogusEvent"]}`, adminUser)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestSystemWebhookCreate_RepoError(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	repo := testutil.NewMockSystemWebhookRepository()
	repo.CreateErr = assertError{}
	h.SetSystemWebhookRepo(repo)
	rec := doPost(h.SystemWebhookCreate, `{"name":"x","url":"https://x","on":["abuseReport"],"isActive":true}`, adminUser)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestSystemWebhookList_ReturnsRows(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	repo := testutil.NewMockSystemWebhookRepository()
	require.NoError(t, repo.Create(&model.SystemWebhook{ID: "w1", Name: "a", URL: "u", IsActive: true}))
	require.NoError(t, repo.Create(&model.SystemWebhook{ID: "w2", Name: "b", URL: "u", IsActive: true}))
	h.SetSystemWebhookRepo(repo)

	rec := doPost(h.SystemWebhookList, `{}`, adminUser)
	assert.Equal(t, http.StatusOK, rec.Code)
	var rows []model.SystemWebhook
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &rows))
	assert.Len(t, rows, 2)
	// 並び順は id DESC
	assert.Equal(t, "w2", rows[0].ID)
	assert.Equal(t, "w1", rows[1].ID)
}

// #1948-10: SystemWebhook の updatedAt / latestSentAt は upstream toISOString()
// (.000Z / null) で返す。raw time.Time の RFC3339Nano だと wire-byte が乖離する。
func TestSystemWebhookShow_DateFormat(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	repo := testutil.NewMockSystemWebhookRepository()
	sent := time.Date(2026, 6, 21, 1, 2, 3, 456_000_000, time.UTC)
	// w1: 秒ちょうど (RFC3339Nano なら .000 を落とす) updatedAt + latestSentAt set。
	require.NoError(t, repo.Create(&model.SystemWebhook{
		ID: "w1", Name: "hook", URL: "u", IsActive: true,
		UpdatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), LatestSentAt: &sent,
	}))
	// w2: latestSentAt nil。
	require.NoError(t, repo.Create(&model.SystemWebhook{
		ID: "w2", Name: "hook2", URL: "u", IsActive: true,
		UpdatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
	}))
	h.SetSystemWebhookRepo(repo)

	rec := doPost(h.SystemWebhookShow, `{"id":"w1"}`, adminUser)
	require.Equal(t, http.StatusOK, rec.Code)
	var got map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, "2026-01-02T03:04:05.000Z", got["updatedAt"], "updatedAt は .000Z 形式 (#1948-10)")
	assert.Equal(t, "2026-06-21T01:02:03.456Z", got["latestSentAt"], "latestSentAt は .000Z 形式 (#1948-10)")
	// createdAt は upstream SystemWebhook pack に無い (model にも無い) → 露出しない。
	_, hasCreatedAt := got["createdAt"]
	assert.False(t, hasCreatedAt, "createdAt は SystemWebhook shape に含まれない")

	rec = doPost(h.SystemWebhookShow, `{"id":"w2"}`, adminUser)
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Nil(t, got["latestSentAt"], "latestSentAt nil は null (#1948-10)")
}

func TestSystemWebhookShow_FoundAndNotFound(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	repo := testutil.NewMockSystemWebhookRepository()
	require.NoError(t, repo.Create(&model.SystemWebhook{ID: "w1", Name: "hook"}))
	h.SetSystemWebhookRepo(repo)

	rec := doPost(h.SystemWebhookShow, `{"id":"w1"}`, adminUser)
	assert.Equal(t, http.StatusOK, rec.Code)

	rec2 := doPost(h.SystemWebhookShow, `{"id":"missing"}`, adminUser)
	assert.Equal(t, http.StatusNotFound, rec2.Code)
}

func TestSystemWebhookDelete_Removes(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	repo := testutil.NewMockSystemWebhookRepository()
	require.NoError(t, repo.Create(&model.SystemWebhook{ID: "w1"}))
	h.SetSystemWebhookRepo(repo)

	rec := doPost(h.SystemWebhookDelete, `{"id":"w1"}`, adminUser)
	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.NotContains(t, repo.Webhooks, "w1")
}

func TestSystemWebhookUpdate_PartialFields(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	repo := testutil.NewMockSystemWebhookRepository()
	require.NoError(t, repo.Create(&model.SystemWebhook{
		ID: "w1", Name: "old", URL: "https://old", IsActive: true,
	}))
	h.SetSystemWebhookRepo(repo)

	rec := doPost(h.SystemWebhookUpdate,
		`{"id":"w1","name":"new","isActive":false}`, adminUser)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "new", repo.Webhooks["w1"].Name)
	assert.Equal(t, "https://old", repo.Webhooks["w1"].URL)
	assert.False(t, repo.Webhooks["w1"].IsActive)
}

func TestSystemWebhookUpdate_PreservesDeliveryStatus(t *testing.T) {
	// 配送 processor が latestSentAt/latestStatus を書き込んだ後でも
	// admin の Update がそれらを踏まないことを確認する。
	h, _, _, _ := newTestHandler(t)
	repo := testutil.NewMockSystemWebhookRepository()
	require.NoError(t, repo.Create(&model.SystemWebhook{
		ID: "w1", Name: "orig", URL: "https://o", IsActive: true,
	}))
	sentAt := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, repo.UpdateLatestStatus("w1", sentAt, 202))
	h.SetSystemWebhookRepo(repo)

	rec := doPost(h.SystemWebhookUpdate,
		`{"id":"w1","name":"renamed"}`, adminUser)
	assert.Equal(t, http.StatusOK, rec.Code)
	w := repo.Webhooks["w1"]
	assert.Equal(t, "renamed", w.Name)
	require.NotNil(t, w.LatestStatus)
	assert.Equal(t, 202, *w.LatestStatus)
}

func TestSystemWebhookUpdate_NotFound(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	h.SetSystemWebhookRepo(testutil.NewMockSystemWebhookRepository())
	rec := doPost(h.SystemWebhookUpdate, `{"id":"missing","name":"x"}`, adminUser)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestSystemWebhookUpdate_MissingID(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	h.SetSystemWebhookRepo(testutil.NewMockSystemWebhookRepository())
	rec := doPost(h.SystemWebhookUpdate, `{}`, adminUser)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestSystemWebhookTest_WithRepo(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	repo := testutil.NewMockSystemWebhookRepository()
	require.NoError(t, repo.Create(&model.SystemWebhook{ID: "w1", URL: "https://127.0.0.1:1"}))
	h.SetSystemWebhookRepo(repo)
	disp := &stubSystemWebhookDispatcher{}
	h.SetSystemWebhookDispatcher(disp)

	// webhookId 空は 400 (required, #1542)。
	assert.Equal(t, http.StatusBadRequest, doPost(h.SystemWebhookTest, `{}`, adminUser).Code)
	// 存在しない webhookId は NO_SUCH_WEBHOOK (400)。
	rec := doPost(h.SystemWebhookTest, `{"webhookId":"missing"}`, adminUser)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "NO_SUCH_WEBHOOK")
	// 正常系は 204 + dispatcher 呼び出し。
	assert.Equal(t, http.StatusNoContent,
		doPost(h.SystemWebhookTest, `{"webhookId":"w1","type":"abuseReport"}`, adminUser).Code)
	require.Len(t, disp.testCalls, 1)
	assert.Equal(t, "w1", disp.testCalls[0].webhookID)
	assert.Equal(t, "abuseReport", disp.testCalls[0].eventType)
}

// override.url / override.secret を受け取り dispatcher にそのまま渡す (#1542)。
func TestSystemWebhookTest_Override(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	repo := testutil.NewMockSystemWebhookRepository()
	require.NoError(t, repo.Create(&model.SystemWebhook{ID: "w1", URL: "https://saved.example", Secret: "saved"}))
	h.SetSystemWebhookRepo(repo)
	disp := &stubSystemWebhookDispatcher{}
	h.SetSystemWebhookDispatcher(disp)

	rec := doPost(h.SystemWebhookTest,
		`{"webhookId":"w1","type":"userCreated","override":{"url":"https://override.example","secret":"ovsecret"}}`, adminUser)
	require.Equal(t, http.StatusNoContent, rec.Code)
	require.Len(t, disp.testCalls, 1)
	assert.Equal(t, "https://override.example", disp.testCalls[0].overrideURL)
	assert.Equal(t, "ovsecret", disp.testCalls[0].overrideSecret)
}

// --- thin nil-repo smoke tests ---
//
// nil systemWebhookRepo 経路 (newTestHandler は wire しない) で expected
// status を返すことを担保。詳細テストは repo を wire して実挙動を検証する
// ので、本群は nil 分岐の coverage 補完。

func TestSystemWebhookCreate(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	assert.Equal(t, http.StatusNoContent, doPost(h.SystemWebhookCreate, `{}`, adminUser).Code)
}

func TestSystemWebhookDelete(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	assert.Equal(t, http.StatusNoContent, doPost(h.SystemWebhookDelete, `{}`, adminUser).Code)
}

func TestSystemWebhookList(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	assert.Equal(t, http.StatusOK, doPost(h.SystemWebhookList, `{}`, adminUser).Code)
}

func TestSystemWebhookShow(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	assert.Equal(t, http.StatusNotFound, doPost(h.SystemWebhookShow, `{}`, adminUser).Code)
}

func TestSystemWebhookTest(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	// 空 webhookId は 400 (required, #1542)。
	assert.Equal(t, http.StatusBadRequest, doPost(h.SystemWebhookTest, `{}`, adminUser).Code)
	// nil repo + webhookId 指定は 500 (lookup 不能)。
	assert.Equal(t, http.StatusInternalServerError, doPost(h.SystemWebhookTest, `{"webhookId":"x"}`, adminUser).Code)
}

func TestSystemWebhookUpdate(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	assert.Equal(t, http.StatusNoContent, doPost(h.SystemWebhookUpdate, `{}`, adminUser).Code)
}

// --- moderation log assertions (#665) ---

func TestSystemWebhookCreate_WritesModerationLog(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	h.SetSystemWebhookRepo(testutil.NewMockSystemWebhookRepository())
	repo := attachModLog(t, h)

	rec := doPost(h.SystemWebhookCreate, `{"name":"hook","url":"https://x","on":["abuseReport"],"isActive":true}`, adminUser)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Eventually(t, func() bool { return len(repo.Snapshot()) == 1 }, 500*time.Millisecond, 5*time.Millisecond)
	assert.Equal(t, "createSystemWebhook", repo.Snapshot()[0].Type)
}

func TestSystemWebhookUpdate_WritesModerationLog(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	whRepo := testutil.NewMockSystemWebhookRepository()
	require.NoError(t, whRepo.Create(&model.SystemWebhook{ID: "w1", Name: "old", URL: "https://x"}))
	h.SetSystemWebhookRepo(whRepo)
	repo := attachModLog(t, h)

	rec := doPost(h.SystemWebhookUpdate, `{"id":"w1","name":"new"}`, adminUser)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Eventually(t, func() bool { return len(repo.Snapshot()) == 1 }, 500*time.Millisecond, 5*time.Millisecond)
	logs := repo.Snapshot()
	assert.Equal(t, "updateSystemWebhook", logs[0].Type)
	var info map[string]any
	require.NoError(t, json.Unmarshal(logs[0].Info, &info))
	require.NotNil(t, info["before"])
	require.NotNil(t, info["after"])
}

func TestSystemWebhookDelete_WritesModerationLog(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	whRepo := testutil.NewMockSystemWebhookRepository()
	require.NoError(t, whRepo.Create(&model.SystemWebhook{ID: "w1", Name: "doomed", URL: "https://x"}))
	h.SetSystemWebhookRepo(whRepo)
	repo := attachModLog(t, h)

	rec := doPost(h.SystemWebhookDelete, `{"id":"w1"}`, adminUser)
	require.Equal(t, http.StatusNoContent, rec.Code)
	require.Eventually(t, func() bool { return len(repo.Snapshot()) == 1 }, 500*time.Millisecond, 5*time.Millisecond)
	assert.Equal(t, "deleteSystemWebhook", repo.Snapshot()[0].Type)
}
