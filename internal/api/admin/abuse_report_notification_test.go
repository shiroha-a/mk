package admin_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	apiadmin "github.com/shiroha-a/mk/internal/api/admin"
	"github.com/shiroha-a/mk/internal/entitycompat/shapetest"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupAbuseRecipientHandler returns a handler with RecipientRepo wired and
// optional seed rows. RecipientRepo を使う test の boilerplate (handler 構築
// + repo 生成 + seed + SetRecipientRepo) を 1 行に圧縮する (#761)。
// 戻り値の repo を直接 mutate して CreateErr 等の error injection も可能。
func setupAbuseRecipientHandler(t *testing.T, seed ...*model.AbuseReportNotificationRecipient) (*apiadmin.Handler, *testutil.MockAbuseReportNotificationRecipientRepository) {
	t.Helper()
	h, _, _, _ := newTestHandler(t)
	repo := testutil.NewMockAbuseReportNotificationRecipientRepository()
	for _, r := range seed {
		require.NoError(t, repo.Create(r))
	}
	h.SetRecipientRepo(repo)
	return h, repo
}

// --- AbuseReportNotificationRecipient ---

func TestRecipientCreate_Success(t *testing.T) {
	h, _ := setupAbuseRecipientHandler(t)

	// upstream Misskey TS paramDef: name + method + isActive required、
	// method=email では userId 必須 (#929 C)。
	rec := doPost(h.AbuseReportNotificationRecipientCreate,
		`{"name":"ops","method":"email","isActive":true,"userId":"u1"}`, adminUser)
	assert.Equal(t, http.StatusOK, rec.Code)

	var got model.AbuseReportNotificationRecipient
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, "ops", got.Name)
	assert.Equal(t, "email", got.Method)
	assert.True(t, got.IsActive)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &raw))
	shapetest.Assert(t, "AbuseReportNotificationRecipient", raw) // L3 (#1294)
	// method=email では systemWebhookId は省略される (omitempty)。
	_, hasSysWH := raw["systemWebhookId"]
	assert.False(t, hasSysWH, "systemWebhookId must be omitted when method=email")
}

func TestRecipientCreate_MissingRequired(t *testing.T) {
	// upstream paramDef で name + method + isActive は required (#929 C)。
	h, _ := setupAbuseRecipientHandler(t)
	assert.Equal(t, http.StatusBadRequest, doPost(h.AbuseReportNotificationRecipientCreate, `{}`, adminUser).Code)
	// method 欠落
	assert.Equal(t, http.StatusBadRequest, doPost(h.AbuseReportNotificationRecipientCreate, `{"name":"ops","isActive":true}`, adminUser).Code)
	// isActive 欠落
	assert.Equal(t, http.StatusBadRequest, doPost(h.AbuseReportNotificationRecipientCreate, `{"name":"ops","method":"email"}`, adminUser).Code)
}

func TestRecipientCreate_CorrelationCheck(t *testing.T) {
	// method=email なら userId 必須、method=webhook なら systemWebhookId 必須 (#929 C)。
	h, _ := setupAbuseRecipientHandler(t)
	rec := doPost(h.AbuseReportNotificationRecipientCreate,
		`{"name":"ops","method":"email","isActive":true}`, adminUser)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	rec = doPost(h.AbuseReportNotificationRecipientCreate,
		`{"name":"ops","method":"webhook","isActive":true}`, adminUser)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestRecipientCreate_InvalidMethod(t *testing.T) {
	// upstream paramDef は method.enum: ['email', 'webhook'] を強制 (#929 C)。
	h, _ := setupAbuseRecipientHandler(t)
	rec := doPost(h.AbuseReportNotificationRecipientCreate,
		`{"name":"ops","method":"slack","isActive":true,"userId":"u1"}`, adminUser)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestRecipientCreate_RepoError(t *testing.T) {
	h, repo := setupAbuseRecipientHandler(t)
	repo.CreateErr = assertError{}
	rec := doPost(h.AbuseReportNotificationRecipientCreate,
		`{"name":"x","method":"email","isActive":true,"userId":"u1"}`, adminUser)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestRecipientList_ReturnsRows(t *testing.T) {
	h, _ := setupAbuseRecipientHandler(t,
		&model.AbuseReportNotificationRecipient{ID: "r1", Name: "a", Method: "email"},
		&model.AbuseReportNotificationRecipient{ID: "r2", Name: "b", Method: "webhook"},
	)

	rec := doPost(h.AbuseReportNotificationRecipientList, `{}`, adminUser)
	assert.Equal(t, http.StatusOK, rec.Code)
	var rows []model.AbuseReportNotificationRecipient
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &rows))
	assert.Len(t, rows, 2)
}

func TestRecipientShow_FoundAndNotFound(t *testing.T) {
	h, _ := setupAbuseRecipientHandler(t,
		&model.AbuseReportNotificationRecipient{ID: "r1", Name: "ops", Method: "email"},
	)

	rec := doPost(h.AbuseReportNotificationRecipientShow, `{"id":"r1"}`, adminUser)
	assert.Equal(t, http.StatusOK, rec.Code)

	rec2 := doPost(h.AbuseReportNotificationRecipientShow, `{"id":"missing"}`, adminUser)
	assert.Equal(t, http.StatusNotFound, rec2.Code)
}

func TestRecipientDelete_Removes(t *testing.T) {
	h, repo := setupAbuseRecipientHandler(t,
		&model.AbuseReportNotificationRecipient{ID: "r1"},
	)

	rec := doPost(h.AbuseReportNotificationRecipientDelete, `{"id":"r1"}`, adminUser)
	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.NotContains(t, repo.Recipients, "r1")
}

func TestRecipientUpdate_PartialFields(t *testing.T) {
	h, repo := setupAbuseRecipientHandler(t,
		&model.AbuseReportNotificationRecipient{
			ID: "r1", Name: "old", Method: "email", IsActive: true,
		},
	)

	// method='webhook' に切り替えるなら systemWebhookId も同 payload に含める
	// 必要がある (upstream correlation check と同 semantics、PR #1108 で追加)。
	rec := doPost(h.AbuseReportNotificationRecipientUpdate,
		`{"id":"r1","name":"new","method":"webhook","systemWebhookId":"w1","isActive":false}`, adminUser)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "new", repo.Recipients["r1"].Name)
	assert.Equal(t, "webhook", repo.Recipients["r1"].Method)
	assert.False(t, repo.Recipients["r1"].IsActive)
}

// PR #1108: method 変更時の correlation check が動作することの regression guard。
// method='webhook' なのに systemWebhookId 未指定 → 400 (silent drift 防止)。
func TestRecipientUpdate_MethodWithoutCounterpartReturns400(t *testing.T) {
	h, _ := setupAbuseRecipientHandler(t,
		&model.AbuseReportNotificationRecipient{
			ID: "r1", Name: "old", Method: "email", IsActive: true,
			// 既存 row に SystemWebhookID は無い
		},
	)
	rec := doPost(h.AbuseReportNotificationRecipientUpdate,
		`{"id":"r1","method":"webhook"}`, adminUser)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "CORRELATION_CHECK_WEBHOOK")
}

// upstream enum: ['email', 'webhook'] 以外の値は 400 reject。
func TestRecipientUpdate_InvalidMethodReturns400(t *testing.T) {
	h, _ := setupAbuseRecipientHandler(t,
		&model.AbuseReportNotificationRecipient{
			ID: "r1", Name: "old", Method: "email", IsActive: true,
		},
	)
	rec := doPost(h.AbuseReportNotificationRecipientUpdate,
		`{"id":"r1","method":"sms"}`, adminUser)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// PR #1108 sweep follow-up: 不存在 ID + 不完全 method payload は
// **404 NotFound** が返る (correlation_check の 400 が先に発火しない)。
// エラー優先順位を「不存在 → 入力不正」の順に統一する regression guard。
// 旧版は before 取得失敗を捨てて correlation_check に進んでいたため、
// admin UI / CLI が ID typo を 400 として誤って受け取っていた edge case。
func TestRecipientUpdate_NotFoundTakesPrecedenceOverCorrelationCheck(t *testing.T) {
	h, _ := setupAbuseRecipientHandler(t) // recipient 0 件で開始
	// method=webhook + systemWebhookId 未指定 = 普段なら correlation_check で
	// 400 になるが、ID が存在しないので 404 が先に返る。
	rec := doPost(h.AbuseReportNotificationRecipientUpdate,
		`{"id":"ghost","method":"webhook"}`, adminUser)
	assert.Equal(t, http.StatusNotFound, rec.Code)
	// correlation_check のメッセージは含まれていないこと
	assert.NotContains(t, rec.Body.String(), "CORRELATION_CHECK_WEBHOOK")
}

func TestRecipientUpdate_NotFound(t *testing.T) {
	h, _ := setupAbuseRecipientHandler(t)
	rec := doPost(h.AbuseReportNotificationRecipientUpdate, `{"id":"missing","name":"x"}`, adminUser)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestRecipientUpdate_MissingID(t *testing.T) {
	h, _ := setupAbuseRecipientHandler(t)
	rec := doPost(h.AbuseReportNotificationRecipientUpdate, `{}`, adminUser)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// --- thin nil-repo smoke tests ---
//
// nil recipientRepo 経路 (newTestHandler は wire しない) で expected status を
// 返すことを担保。詳細テストは repo を wire して実挙動を検証するので、本群は
// nil 分岐の coverage 補完。

func TestAbuseReportNotificationRecipientCreate(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	// required 欠落は 400 (validate-first ordering、#929 C)。
	assert.Equal(t, http.StatusBadRequest, doPost(h.AbuseReportNotificationRecipientCreate, `{}`, adminUser).Code)
	// required 揃いの正規 bind は nil recipientRepo で 204
	assert.Equal(t, http.StatusNoContent, doPost(h.AbuseReportNotificationRecipientCreate,
		`{"name":"ops","method":"email","isActive":true,"userId":"u1"}`, adminUser).Code)
}

func TestAbuseReportNotificationRecipientDelete(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	assert.Equal(t, http.StatusNoContent, doPost(h.AbuseReportNotificationRecipientDelete, `{}`, adminUser).Code)
}

func TestAbuseReportNotificationRecipientList(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	assert.Equal(t, http.StatusOK, doPost(h.AbuseReportNotificationRecipientList, `{}`, adminUser).Code)
}

func TestAbuseReportNotificationRecipientShow(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	assert.Equal(t, http.StatusNotFound, doPost(h.AbuseReportNotificationRecipientShow, `{}`, adminUser).Code)
}

func TestAbuseReportNotificationRecipientUpdate(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	assert.Equal(t, http.StatusNoContent, doPost(h.AbuseReportNotificationRecipientUpdate, `{}`, adminUser).Code)
}

// --- moderation log assertions (#665) ---

func TestRecipientCreate_WritesModerationLog(t *testing.T) {
	h, _ := setupAbuseRecipientHandler(t)
	repo := attachModLog(t, h)

	rec := doPost(h.AbuseReportNotificationRecipientCreate,
		`{"name":"r","method":"email","isActive":true,"userId":"u1"}`, adminUser)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Eventually(t, func() bool { return len(repo.Snapshot()) == 1 }, 500*time.Millisecond, 5*time.Millisecond)
	assert.Equal(t, "createAbuseReportNotificationRecipient", repo.Snapshot()[0].Type)
}

func TestRecipientUpdate_WritesModerationLog(t *testing.T) {
	h, _ := setupAbuseRecipientHandler(t,
		&model.AbuseReportNotificationRecipient{ID: "r1", Name: "old", Method: "email"},
	)
	repo := attachModLog(t, h)

	rec := doPost(h.AbuseReportNotificationRecipientUpdate, `{"id":"r1","name":"new"}`, adminUser)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Eventually(t, func() bool { return len(repo.Snapshot()) == 1 }, 500*time.Millisecond, 5*time.Millisecond)
	logs := repo.Snapshot()
	assert.Equal(t, "updateAbuseReportNotificationRecipient", logs[0].Type)
	var info map[string]any
	require.NoError(t, json.Unmarshal(logs[0].Info, &info))
	require.NotNil(t, info["before"])
	require.NotNil(t, info["after"])
}

func TestRecipientDelete_WritesModerationLog(t *testing.T) {
	h, _ := setupAbuseRecipientHandler(t,
		&model.AbuseReportNotificationRecipient{ID: "r1", Name: "doomed", Method: "email"},
	)
	repo := attachModLog(t, h)

	rec := doPost(h.AbuseReportNotificationRecipientDelete, `{"id":"r1"}`, adminUser)
	require.Equal(t, http.StatusNoContent, rec.Code)
	require.Eventually(t, func() bool { return len(repo.Snapshot()) == 1 }, 500*time.Millisecond, 5*time.Millisecond)
	assert.Equal(t, "deleteAbuseReportNotificationRecipient", repo.Snapshot()[0].Type)
}
