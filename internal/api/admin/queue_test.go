package admin_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	apiadmin "github.com/shiroha-a/mk/internal/api/admin"
	"github.com/shiroha-a/mk/internal/entitycompat/shapetest"
	"github.com/shiroha-a/mk/internal/queue"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// deliverPayloadJSON returns a marshaled queue.DeliverPayload with the given
// recipient inbox URL. Helper for *-delayed aggregation tests.
func deliverPayloadJSON(t *testing.T, inbox string) []byte {
	t.Helper()
	b, err := json.Marshal(queue.DeliverPayload{Inbox: inbox})
	require.NoError(t, err)
	return b
}

// inboxPayloadJSON returns a marshaled queue.InboxPayload whose Headers
// contain a minimal HTTP Signature header pointing at the given keyId URL.
// 他の field (algorithm / headers / signature) は dummy で埋める ―
// activitypub.ParseSignatureHeader はキー順序非依存で keyId だけ拾えれば成立。
func inboxPayloadJSON(t *testing.T, keyID string) []byte {
	t.Helper()
	sig := `keyId="` + keyID + `",algorithm="rsa-sha256",headers="(request-target) date host",signature="abc"`
	b, err := json.Marshal(queue.InboxPayload{
		Body:    []byte(`{}`),
		Headers: map[string]string{"Signature": sig},
	})
	require.NoError(t, err)
	return b
}

// inboxPayloadNoSigJSON returns a marshaled InboxPayload with no signature
// header — used to verify the aggregator silently skips it.
func inboxPayloadNoSigJSON(t *testing.T) []byte {
	t.Helper()
	b, err := json.Marshal(queue.InboxPayload{Body: []byte(`{}`)})
	require.NoError(t, err)
	return b
}

// assertHostCount asserts that got[i] == [host, count]. JSON decode yields
// numbers as float64, so this hides the conversion noise at call sites.
func assertHostCount(t *testing.T, got [][]any, i int, host string, count int) {
	t.Helper()
	require.Greater(t, len(got), i, "got len=%d, want index %d", len(got), i)
	assert.Equal(t, []any{host, float64(count)}, got[i],
		"got[%d] should be [%q, %d]", i, host, count)
}

// stubQueueInspector is a test double for admin.QueueInspector that returns
// configurable canned responses without touching Redis.
type stubQueueInspector struct {
	queues        []string
	info          map[string]*apiadmin.QueueInfoResult
	pending       map[string][]*apiadmin.QueueTaskSummary
	active        map[string][]*apiadmin.QueueTaskSummary
	scheduled     map[string][]*apiadmin.QueueTaskSummary
	retry         map[string][]*apiadmin.QueueTaskSummary
	completed     map[string][]*apiadmin.QueueTaskSummary
	failed        map[string][]*apiadmin.QueueTaskSummary
	task          map[string]*apiadmin.QueueTaskSummary
	metrics       map[string]map[string]*apiadmin.QueueMetricsResult // [queue][kind]
	deleted       []string
	runCalls      []string
	deleteAllHits []string
	queuesErr     error
	runErr        error
	deleteErr     error
}

func (s *stubQueueInspector) Queues() ([]string, error) { return s.queues, s.queuesErr }
func (s *stubQueueInspector) GetQueueInfo(q string) (*apiadmin.QueueInfoResult, error) {
	if info, ok := s.info[q]; ok {
		return info, nil
	}
	return nil, errors.New("not found")
}
func (s *stubQueueInspector) DeleteTask(_, id string) error {
	if s.deleteErr != nil {
		return s.deleteErr
	}
	s.deleted = append(s.deleted, id)
	return nil
}
func (s *stubQueueInspector) DeleteAllPendingTasks(q string) (int, error) {
	s.deleteAllHits = append(s.deleteAllHits, q)
	return 0, nil
}
func (s *stubQueueInspector) RunTask(_, id string) error {
	if s.runErr != nil {
		return s.runErr
	}
	s.runCalls = append(s.runCalls, id)
	return nil
}
func (s *stubQueueInspector) ListPendingTasks(q string, _, _ int) ([]*apiadmin.QueueTaskSummary, error) {
	return s.pending[q], nil
}
func (s *stubQueueInspector) ListActiveTasks(q string, _, _ int) ([]*apiadmin.QueueTaskSummary, error) {
	return s.active[q], nil
}
func (s *stubQueueInspector) ListScheduledTasks(q string, _, _ int) ([]*apiadmin.QueueTaskSummary, error) {
	return s.scheduled[q], nil
}
func (s *stubQueueInspector) ListRetryTasks(q string, _, _ int) ([]*apiadmin.QueueTaskSummary, error) {
	return s.retry[q], nil
}
func (s *stubQueueInspector) ListCompletedTasks(q string, _, _ int) ([]*apiadmin.QueueTaskSummary, error) {
	return s.completed[q], nil
}
func (s *stubQueueInspector) ListFailedTasks(q string, _, _ int) ([]*apiadmin.QueueTaskSummary, error) {
	return s.failed[q], nil
}
func (s *stubQueueInspector) GetTaskInfo(_, id string) (*apiadmin.QueueTaskSummary, error) {
	if t, ok := s.task[id]; ok {
		return t, nil
	}
	return nil, errors.New("not found")
}
func (s *stubQueueInspector) QueueMetrics(q, kind string) (*apiadmin.QueueMetricsResult, error) {
	if perKind, ok := s.metrics[q]; ok {
		if m, ok := perKind[kind]; ok {
			return m, nil
		}
	}
	return &apiadmin.QueueMetricsResult{}, nil
}

// --- Queue --------------------------------------------------------------------

func TestQueueClear_WithInspector(t *testing.T) {
	// queue + state 指定で対象 queue 1 つだけが clear される (#929 A の strict 化後)。
	h, _, _, _ := newTestHandler(t)
	insp := &stubQueueInspector{}
	h.SetQueueInspector(insp)
	rec := doPost(h.QueueClear, `{"queue":"deliver","state":"wait"}`, adminUser)
	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Equal(t, []string{"deliver"}, insp.deleteAllHits)
}

func TestQueueClear_MissingParams(t *testing.T) {
	// upstream Misskey TS paramDef で queue + state は required (#929 A)。
	h, _, _, _ := newTestHandler(t)
	insp := &stubQueueInspector{}
	h.SetQueueInspector(insp)
	assert.Equal(t, http.StatusBadRequest, doPost(h.QueueClear, `{}`, adminUser).Code)
	assert.Equal(t, http.StatusBadRequest, doPost(h.QueueClear, `{"queue":"deliver"}`, adminUser).Code)
	assert.Equal(t, http.StatusBadRequest, doPost(h.QueueClear, `{"state":"wait"}`, adminUser).Code)
}

func TestQueueJobs_FilterByQueueAndState(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	insp := &stubQueueInspector{
		queues: []string{"deliver"},
		pending: map[string][]*apiadmin.QueueTaskSummary{
			"deliver": {{ID: "p1", Queue: "deliver", Type: "x", State: "pending"}},
		},
		active: map[string][]*apiadmin.QueueTaskSummary{
			"deliver": {{ID: "a1", Queue: "deliver", Type: "x", State: "active"}},
		},
	}
	h.SetQueueInspector(insp)

	rec := doPost(h.QueueJobs, `{"queue":"deliver","state":"active"}`, adminUser)
	assert.Equal(t, http.StatusOK, rec.Code)
	var rows []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &rows))
	require.Len(t, rows, 1)
	assert.Equal(t, "a1", rows[0]["id"])
}

func TestQueueJobs_MissingParams(t *testing.T) {
	// upstream Misskey TS paramDef で queue + state は required (#929 A)。
	h, _, _, _ := newTestHandler(t)
	insp := &stubQueueInspector{}
	h.SetQueueInspector(insp)
	assert.Equal(t, http.StatusBadRequest, doPost(h.QueueJobs, `{}`, adminUser).Code)
	assert.Equal(t, http.StatusBadRequest, doPost(h.QueueJobs, `{"queue":"deliver"}`, adminUser).Code)
	assert.Equal(t, http.StatusBadRequest, doPost(h.QueueJobs, `{"state":"wait"}`, adminUser).Code)
}

func TestQueueShowJob_Found(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	insp := &stubQueueInspector{
		task: map[string]*apiadmin.QueueTaskSummary{
			"tid1": {ID: "tid1", Queue: "deliver", Type: "x", State: "pending"},
		},
	}
	h.SetQueueInspector(insp)

	rec := doPost(h.QueueShowJob, `{"queue":"deliver","id":"tid1"}`, adminUser)
	assert.Equal(t, http.StatusOK, rec.Code)
	var got map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, "tid1", got["id"])
	// golden QueueJob: progress/returnValue は object、failedReason は string で
	// 常に present (#1304)。
	shapetest.Assert(t, "QueueJob", got) // L3 (#1304)
	assert.IsType(t, map[string]any{}, got["progress"])
	assert.IsType(t, map[string]any{}, got["returnValue"])
	_, hasFailedReason := got["failedReason"]
	assert.True(t, hasFailedReason, "failedReason must always be present (golden required)")
}

func TestQueueShowJob_NotFoundWithInspector(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	insp := &stubQueueInspector{}
	h.SetQueueInspector(insp)
	rec := doPost(h.QueueShowJob, `{"queue":"deliver","id":"ghost"}`, adminUser)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestQueueRemoveJob_Success(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	// asynq DeleteTask は不明 id で nil を返す (idempotent) ため、existence
	// 確認用に GetTaskInfo を事前 hit する (#929 B)。stub の task map に
	// 入れて GetTaskInfo を pass させる。
	insp := &stubQueueInspector{
		task: map[string]*apiadmin.QueueTaskSummary{
			"tid1": {ID: "tid1", Queue: "deliver"},
		},
	}
	h.SetQueueInspector(insp)
	rec := doPost(h.QueueRemoveJob, `{"queue":"deliver","id":"tid1"}`, adminUser)
	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Equal(t, []string{"tid1"}, insp.deleted)
}

func TestQueueRemoveJob_NotFound(t *testing.T) {
	// task map が空 = GetTaskInfo が "not found" を返すケース。idempotent な
	// asynq DeleteTask に到達せず precheck で 404 (#929 B)。
	h, _, _, _ := newTestHandler(t)
	insp := &stubQueueInspector{}
	h.SetQueueInspector(insp)
	rec := doPost(h.QueueRemoveJob, `{"queue":"deliver","id":"x"}`, adminUser)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestQueueRemoveJob_DeleteTaskError(t *testing.T) {
	// precheck は pass するが DeleteTask 自体が失敗する race condition を guard。
	// (例: precheck と DeleteTask の間に他 admin が削除済) 引き続き 404 を返す。
	h, _, _, _ := newTestHandler(t)
	insp := &stubQueueInspector{
		task: map[string]*apiadmin.QueueTaskSummary{
			"tid1": {ID: "tid1", Queue: "deliver"},
		},
		deleteErr: errors.New("not found"),
	}
	h.SetQueueInspector(insp)
	rec := doPost(h.QueueRemoveJob, `{"queue":"deliver","id":"tid1"}`, adminUser)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestQueueRetryJob_Success(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	insp := &stubQueueInspector{
		task: map[string]*apiadmin.QueueTaskSummary{
			"tid1": {ID: "tid1", Queue: "deliver"},
		},
	}
	h.SetQueueInspector(insp)
	rec := doPost(h.QueueRetryJob, `{"queue":"deliver","id":"tid1"}`, adminUser)
	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Equal(t, []string{"tid1"}, insp.runCalls)
}

func TestQueueRetryJob_NotFound(t *testing.T) {
	// 不明 id は GetTaskInfo precheck で 404。RunTask は idempotent (#929 B)。
	h, _, _, _ := newTestHandler(t)
	insp := &stubQueueInspector{}
	h.SetQueueInspector(insp)
	rec := doPost(h.QueueRetryJob, `{"queue":"deliver","id":"x"}`, adminUser)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestQueueRetryJob_RunTaskError(t *testing.T) {
	// precheck pass 後に RunTask 自体が失敗する race condition (precheck と
	// RunTask の間に別 admin が削除した等)。RemoveJob と symmetric。
	h, _, _, _ := newTestHandler(t)
	insp := &stubQueueInspector{
		task: map[string]*apiadmin.QueueTaskSummary{
			"tid1": {ID: "tid1", Queue: "deliver"},
		},
		runErr: errors.New("not found"),
	}
	h.SetQueueInspector(insp)
	rec := doPost(h.QueueRetryJob, `{"queue":"deliver","id":"tid1"}`, adminUser)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestQueuePromoteJobs_RunsScheduledAndRetry(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	insp := &stubQueueInspector{
		scheduled: map[string][]*apiadmin.QueueTaskSummary{
			"deliver": {{ID: "s1"}, {ID: "s2"}},
		},
		retry: map[string][]*apiadmin.QueueTaskSummary{
			"deliver": {{ID: "r1"}},
		},
	}
	h.SetQueueInspector(insp)

	// queue は paramDef required (#929 A)。
	rec := doPost(h.QueuePromoteJobs, `{"queue":"deliver"}`, adminUser)
	assert.Equal(t, http.StatusOK, rec.Code)
	var got map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.EqualValues(t, 3, got["promoted"])
	assert.ElementsMatch(t, []string{"s1", "s2", "r1"}, insp.runCalls)
}

func TestQueueQueueStats_WithInspector(t *testing.T) {
	// frontend admin/job-queue.vue が期待する Misskey Bull shape
	// ({name, counts:{active,delayed,waiting,...}, metrics:..., db:...})
	// を返すことを検証する (#929 A の strict 化後は単一 queue のみ)。
	h, _, _, _ := newTestHandler(t)
	insp := &stubQueueInspector{
		info: map[string]*apiadmin.QueueInfoResult{
			"deliver": {Queue: "deliver", Size: 5, Pending: 3, Active: 1, Completed: 10, Failed: 2, Scheduled: 0, Retry: 1},
		},
		metrics: map[string]map[string]*apiadmin.QueueMetricsResult{
			"deliver": {
				"completed": {Count: 10, Data: []int64{4, 3, 2, 1}},
				"failed":    {Count: 2, Data: []int64{0, 1, 0, 1}},
			},
		},
	}
	h.SetQueueInspector(insp)
	rec := doPost(h.QueueQueueStats, `{"queue":"deliver"}`, adminUser)
	assert.Equal(t, http.StatusOK, rec.Code)
	var got map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, "deliver", got["name"])
	counts, ok := got["counts"].(map[string]any)
	require.True(t, ok)
	assert.EqualValues(t, 1, counts["active"])
	assert.EqualValues(t, 3, counts["waiting"])
	// scheduled(0) + retry(1) がdelayedに集約される。
	assert.EqualValues(t, 1, counts["delayed"])
	shapetest.Assert(t, "QueueCount", counts) // L3 (#1302)

	// metrics shape: data 配列と count が driver から伝播。
	metrics, ok := got["metrics"].(map[string]any)
	require.True(t, ok)
	completed, ok := metrics["completed"].(map[string]any)
	require.True(t, ok)
	assert.EqualValues(t, 10, completed["count"])
	completedData, ok := completed["data"].([]any)
	require.True(t, ok)
	assert.Len(t, completedData, 4)
	assert.EqualValues(t, 4, completedData[0]) // newest-first
}

func TestQueueQueueStats_NoMetrics_FallsBackToCumulative(t *testing.T) {
	// driver が QueueMetrics を実装していても Data 空 / Count 0 を
	// 返すケース (mkq で WithJobMetrics 無効, asynq の time-series 無し)
	// では info.Completed / info.Failed の累積値を count にフォール
	// バックさせ、data は空配列で安定 shape を維持する。
	h, _, _, _ := newTestHandler(t)
	insp := &stubQueueInspector{
		queues: []string{"deliver"},
		info: map[string]*apiadmin.QueueInfoResult{
			"deliver": {Queue: "deliver", Completed: 7, Failed: 3},
		},
		// metrics map を空にすると stub は zero-valued QueueMetricsResult を返す
	}
	h.SetQueueInspector(insp)
	rec := doPost(h.QueueQueueStats, `{"queue":"deliver"}`, adminUser)
	assert.Equal(t, http.StatusOK, rec.Code)
	var got map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	metrics, ok := got["metrics"].(map[string]any)
	require.True(t, ok)
	completed, ok := metrics["completed"].(map[string]any)
	require.True(t, ok)
	assert.EqualValues(t, 7, completed["count"])
	completedData, ok := completed["data"].([]any)
	require.True(t, ok)
	assert.Empty(t, completedData)
	failed, ok := metrics["failed"].(map[string]any)
	require.True(t, ok)
	assert.EqualValues(t, 3, failed["count"])
}

func TestQueueQueueStats_SingleQueueQuery(t *testing.T) {
	// frontend の fetchCurrentQueue は {queue: "deliver"} を投げて
	// 単一 queue の shape を受ける。req.Queue が与えられた場合はその
	// queue 1 つ分を返す挙動を検証する。
	h, _, _, _ := newTestHandler(t)
	insp := &stubQueueInspector{
		queues: []string{"deliver", "push"},
		info: map[string]*apiadmin.QueueInfoResult{
			"deliver": {Queue: "deliver", Size: 5, Pending: 3, Active: 1},
			"push":    {Queue: "push"},
		},
	}
	h.SetQueueInspector(insp)
	rec := doPost(h.QueueQueueStats, `{"queue":"deliver"}`, adminUser)
	assert.Equal(t, http.StatusOK, rec.Code)
	var got map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, "deliver", got["name"])
	counts, ok := got["counts"].(map[string]any)
	require.True(t, ok)
	assert.EqualValues(t, 1, counts["active"])
}

// TestQueueDeliverDelayed_AggregatesByHost verifies the upstream Misskey TS
// contract: [[host, count], ...] tuples sorted by count desc. Mixed
// scheduled+retry tasks targeting the same host must be merged into one
// bucket. Tasks whose payload cannot yield a host (invalid JSON / unparsable
// URL / empty Inbox) must be skipped silently. (#1179)
func TestQueueDeliverDelayed_AggregatesByHost(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	insp := &stubQueueInspector{
		scheduled: map[string][]*apiadmin.QueueTaskSummary{
			"deliver": {
				{ID: "s1", Payload: deliverPayloadJSON(t, "https://a.example/inbox")},
				{ID: "s2", Payload: deliverPayloadJSON(t, "https://b.example/users/foo/inbox")},
				{ID: "s3", Payload: deliverPayloadJSON(t, "https://a.example/users/x/inbox")},
				// 不正 payload は skip
				{ID: "bad1", Payload: []byte("not json")},
				{ID: "bad2", Payload: deliverPayloadJSON(t, "::not a url::")},
				{ID: "bad3", Payload: deliverPayloadJSON(t, "")},
			},
		},
		retry: map[string][]*apiadmin.QueueTaskSummary{
			"deliver": {
				{ID: "r1", Payload: deliverPayloadJSON(t, "https://b.example/inbox")},
				{ID: "r2", Payload: deliverPayloadJSON(t, "https://c.example/inbox")},
				{ID: "r3", Payload: deliverPayloadJSON(t, "https://a.example/u2/inbox")},
			},
		},
	}
	h.SetQueueInspector(insp)
	rec := doPost(h.QueueDeliverDelayed, `{}`, adminUser)
	assert.Equal(t, http.StatusOK, rec.Code)

	var got [][]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	// a.example: 3 (s1+s3+r3), b.example: 2 (s2+r1), c.example: 1 (r2)
	// count desc, tie 無し
	require.Len(t, got, 3)
	assertHostCount(t, got, 0, "a.example", 3)
	assertHostCount(t, got, 1, "b.example", 2)
	assertHostCount(t, got, 2, "c.example", 1)
}

// TestQueueDeliverDelayed_EmptyReturnsEmptyArray ensures the response is
// `[]` (not `null`) when no tasks exist, so frontend.reduce() yields 0.
func TestQueueDeliverDelayed_EmptyReturnsEmptyArray(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	h.SetQueueInspector(&stubQueueInspector{})
	rec := doPost(h.QueueDeliverDelayed, `{}`, adminUser)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "[]\n", rec.Body.String())
}

// TestQueueDeliverDelayed_NoInspectorReturnsEmpty: queueInspector が無い
// (queue 機能無効構成) でも 200 + `[]` を返す。frontend が render crash しない
// よう defensive に動く。
func TestQueueDeliverDelayed_NoInspectorReturnsEmpty(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	rec := doPost(h.QueueDeliverDelayed, `{}`, adminUser)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "[]\n", rec.Body.String())
}

// TestQueueInboxDelayed_AggregatesByHostFromSignature verifies inbox tasks
// resolve host via HTTP Signature header (keyId URL), matching upstream
// Misskey TS の `new URL(job.data.signature.keyId).host`. (#1179)
func TestQueueInboxDelayed_AggregatesByHostFromSignature(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	insp := &stubQueueInspector{
		scheduled: map[string][]*apiadmin.QueueTaskSummary{
			"inbox": {
				{ID: "s1", Payload: inboxPayloadJSON(t, "https://a.example/users/alice#main-key")},
				{ID: "s2", Payload: inboxPayloadJSON(t, "https://a.example/users/bob#main-key")},
				// signature 無 → skip
				{ID: "noSig", Payload: inboxPayloadNoSigJSON(t)},
				// 不正 JSON → skip
				{ID: "bad", Payload: []byte("not json")},
			},
		},
		retry: map[string][]*apiadmin.QueueTaskSummary{
			"inbox": {
				{ID: "r1", Payload: inboxPayloadJSON(t, "https://b.example/users/eve#main-key")},
			},
		},
	}
	h.SetQueueInspector(insp)
	rec := doPost(h.QueueInboxDelayed, `{}`, adminUser)
	assert.Equal(t, http.StatusOK, rec.Code)

	var got [][]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	// a.example: 2, b.example: 1
	require.Len(t, got, 2)
	assertHostCount(t, got, 0, "a.example", 2)
	assertHostCount(t, got, 1, "b.example", 1)
}

// pagedInspector is a QueueInspector stub that always returns full pages,
// emulating an asynq inspector that has so many tasks the cursor never
// reaches the end (or, worse, a misbehaving inspector that ignores empty
// state). Used to verify the page cap defense in fetchAllDelayedTasks.
//
// 他の QueueInspector method は embedded stubQueueInspector の zero-value に
// fall through する (空 map / 空 slice 返却)。cap test では fetchAllDelayedTasks
// 経由の Scheduled/Retry list call しか走らないので問題なし。
type pagedInspector struct {
	stubQueueInspector
	payload []byte
	calls   int
}

func (p *pagedInspector) ListScheduledTasks(_ string, _, pageSize int) ([]*apiadmin.QueueTaskSummary, error) {
	p.calls++
	rows := make([]*apiadmin.QueueTaskSummary, 0, pageSize)
	for i := 0; i < pageSize; i++ {
		rows = append(rows, &apiadmin.QueueTaskSummary{Payload: p.payload})
	}
	return rows, nil
}

func (p *pagedInspector) ListRetryTasks(_ string, _, _ int) ([]*apiadmin.QueueTaskSummary, error) {
	// retry list は常に空 = scheduled の cap だけを test 対象にする。
	return nil, nil
}

// TestQueueDeliverDelayed_PageCapBoundsRunaway: inspector が常に full page を
// 返してくる misbehavior / pathological case で、handler が無限ループせず
// `delayedTasksMaxPages` で打ち切ることを担保する。defense-in-depth の
// 動作 test (#1179 review #1 + #2)。
func TestQueueDeliverDelayed_PageCapBoundsRunaway(t *testing.T) {
	// 本物の cap (1000 pages) で test すると 100K item 走査で 1 unit test に
	// しては重い。test 中だけ cap を小さくして cap の挙動だけを検証する。
	// `pageCap` という名前で builtin `cap()` の shadow を避ける。
	const pageCap = 3
	prev := apiadmin.SetDelayedTasksMaxPages(pageCap)
	t.Cleanup(func() { apiadmin.SetDelayedTasksMaxPages(prev) })

	h, _, _, _ := newTestHandler(t)
	insp := &pagedInspector{payload: deliverPayloadJSON(t, "https://always.example/inbox")}
	h.SetQueueInspector(insp)

	rec := doPost(h.QueueDeliverDelayed, `{}`, adminUser)
	require.Equal(t, http.StatusOK, rec.Code)

	var got [][]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	// 1 host × pageCap (3) × pageSize = 3 × DelayedTasksFetchPageSize 件で
	// 打ち切られる。
	require.Len(t, got, 1)
	assertHostCount(t, got, 0, "always.example", pageCap*apiadmin.DelayedTasksFetchPageSize)
	// inspector への ListScheduledTasks 呼び出しは cap 回数で打ち切られる。
	assert.Equal(t, pageCap, insp.calls,
		"page iteration must stop at delayedTasksMaxPages")
}

// TestQueueDeliverDelayed_StableSortOnTie: 同 count の host は host 名 asc で
// 安定 sort される (frontend で順序が flicker しないように)。
func TestQueueDeliverDelayed_StableSortOnTie(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	insp := &stubQueueInspector{
		scheduled: map[string][]*apiadmin.QueueTaskSummary{
			"deliver": {
				{ID: "1", Payload: deliverPayloadJSON(t, "https://c.example/inbox")},
				{ID: "2", Payload: deliverPayloadJSON(t, "https://a.example/inbox")},
				{ID: "3", Payload: deliverPayloadJSON(t, "https://b.example/inbox")},
			},
		},
	}
	h.SetQueueInspector(insp)
	rec := doPost(h.QueueDeliverDelayed, `{}`, adminUser)
	var got [][]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Len(t, got, 3)
	// all count = 1, host 名 asc で並ぶ
	assert.Equal(t, "a.example", got[0][0])
	assert.Equal(t, "b.example", got[1][0])
	assert.Equal(t, "c.example", got[2][0])
}

// QueueJobs は単一 queue でも limit を超えない件数しか返さないことを確認する。
func TestQueueJobs_SingleQueueCappedAtLimit(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	insp := &stubQueueInspector{
		pending: map[string][]*apiadmin.QueueTaskSummary{
			"deliver": {{ID: "d1"}, {ID: "d2"}, {ID: "d3"}},
		},
	}
	h.SetQueueInspector(insp)

	rec := doPost(h.QueueJobs, `{"queue":"deliver","state":"wait","limit":2}`, adminUser)
	assert.Equal(t, http.StatusOK, rec.Code)
	var rows []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &rows))
	assert.Len(t, rows, 2, "single-queue output must respect limit")
}

// --- thin nil-inspector smoke tests ---
//
// nil queueInspector 経路 (newTestHandler は wire しない) で expected status
// を返すことを担保。詳細テストは inspector を wire して実挙動を検証するので、
// 本群は nil 分岐の coverage 補完。

func TestQueueClear(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	// queue/state 欠落は 400、required 揃いの正規 bind は nil inspector で 204
	assert.Equal(t, http.StatusBadRequest, doPost(h.QueueClear, `{}`, adminUser).Code)
	assert.Equal(t, http.StatusNoContent, doPost(h.QueueClear, `{"queue":"deliver","state":"wait"}`, adminUser).Code)
}

func TestQueueDeliverDelayed(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	assert.Equal(t, http.StatusOK, doPost(h.QueueDeliverDelayed, `{}`, adminUser).Code)
}

func TestQueueInboxDelayed(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	assert.Equal(t, http.StatusOK, doPost(h.QueueInboxDelayed, `{}`, adminUser).Code)
}

func TestQueueJobs(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	// queue/state 欠落は 400、required 揃いの正規 bind は nil inspector で 200 (空配列)
	assert.Equal(t, http.StatusBadRequest, doPost(h.QueueJobs, `{}`, adminUser).Code)
	assert.Equal(t, http.StatusOK, doPost(h.QueueJobs, `{"queue":"deliver","state":"wait"}`, adminUser).Code)
}

func TestQueuePromoteJobs(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	assert.Equal(t, http.StatusBadRequest, doPost(h.QueuePromoteJobs, `{}`, adminUser).Code)
	assert.Equal(t, http.StatusNoContent, doPost(h.QueuePromoteJobs, `{"queue":"deliver"}`, adminUser).Code)
}

func TestQueueQueueStats(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	assert.Equal(t, http.StatusBadRequest, doPost(h.QueueQueueStats, `{}`, adminUser).Code)
	assert.Equal(t, http.StatusOK, doPost(h.QueueQueueStats, `{"queue":"deliver"}`, adminUser).Code)
}

func TestQueueQueues(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	assert.Equal(t, http.StatusOK, doPost(h.QueueQueues, `{}`, adminUser).Code)
}

func TestQueueRemoveJob(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	// queue/id 欠落は 400
	assert.Equal(t, http.StatusBadRequest, doPost(h.QueueRemoveJob, `{}`, adminUser).Code)
	// inspector 未注入は 204
	assert.Equal(t, http.StatusNoContent, doPost(h.QueueRemoveJob, `{"queue":"deliver","id":"x"}`, adminUser).Code)
}

func TestQueueRetryJob(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	assert.Equal(t, http.StatusBadRequest, doPost(h.QueueRetryJob, `{}`, adminUser).Code)
	assert.Equal(t, http.StatusNoContent, doPost(h.QueueRetryJob, `{"queue":"deliver","id":"x"}`, adminUser).Code)
}

func TestQueueShowJob(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	// queue/id 欠落は 400、inspector 未注入 + 正規 bind は 404
	assert.Equal(t, http.StatusBadRequest, doPost(h.QueueShowJob, `{}`, adminUser).Code)
	assert.Equal(t, http.StatusNotFound, doPost(h.QueueShowJob, `{"queue":"deliver","id":"x"}`, adminUser).Code)
}

func TestQueueShowJobLogs(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	assert.Equal(t, http.StatusOK, doPost(h.QueueShowJobLogs, `{}`, adminUser).Code)
}

func TestQueueStatsAdmin(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	rec := doPost(h.QueueStats, `{}`, adminUser)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "deliver")
}

// TestQueueStats_DelayedIncludesScheduledAndRetry guards #654: Misskey
// frontend の WidgetJobQueue は Bull 用語の delayed をグラフ化するが、
// asynq では Scheduled (未来実行予定) と Retry (失敗後再試行待ち) の
// 2 つに分かれる。delayed = Scheduled + Retry を返さないと再試行待ちが
// dashboard に出ないため、この合算が REST API でも維持されることを guard。
func TestQueueStats_DelayedIncludesScheduledAndRetry(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	insp := &stubQueueInspector{
		info: map[string]*apiadmin.QueueInfoResult{
			"deliver": {Queue: "deliver", Active: 1, Pending: 2, Scheduled: 3, Retry: 4},
			"inbox":   {Queue: "inbox", Active: 0, Pending: 0, Scheduled: 5, Retry: 6},
		},
	}
	h.SetQueueInspector(insp)

	rec := doPost(h.QueueStats, `{}`, adminUser)
	require.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))

	// deliver: Scheduled 3 + Retry 4 = 7
	assert.EqualValues(t, 1, resp["deliver"]["active"])
	assert.EqualValues(t, 2, resp["deliver"]["waiting"])
	assert.EqualValues(t, 7, resp["deliver"]["delayed"], "delayed must be Scheduled+Retry")

	// inbox: Scheduled 5 + Retry 6 = 11
	assert.EqualValues(t, 11, resp["inbox"]["delayed"], "delayed must be Scheduled+Retry")
}

// QueueJobs は mkq の finished-job 保持から completed / failed を一覧する (#1396)。
// 旧実装は asynq 前提で completed/failed を nil 固定にしており、busy queue でも
// All / Completed タブが空になっていた。
func TestQueueJobs_ListsCompletedAndFailed(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	insp := &stubQueueInspector{
		completed: map[string][]*apiadmin.QueueTaskSummary{
			"inbox": {{ID: "c1", Queue: "inbox", Type: "inbox:process", State: "completed"}},
		},
		failed: map[string][]*apiadmin.QueueTaskSummary{
			"inbox": {{ID: "f1", Queue: "inbox", Type: "inbox:process", State: "failed"}},
		},
	}
	h.SetQueueInspector(insp)

	// state=completed → 完了ジョブが返る。
	rec := doPost(h.QueueJobs, `{"queue":"inbox","state":["completed"]}`, adminUser)
	require.Equal(t, http.StatusOK, rec.Code)
	var got []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Len(t, got, 1)
	assert.Equal(t, "c1", got[0]["id"])

	// state=all (frontend の All タブ) → completed + failed が両方含まれる。
	rec2 := doPost(h.QueueJobs, `{"queue":"inbox","state":["completed","failed","active","delayed","wait"]}`, adminUser)
	require.Equal(t, http.StatusOK, rec2.Code)
	var got2 []map[string]any
	require.NoError(t, json.Unmarshal(rec2.Body.Bytes(), &got2))
	ids := map[string]bool{}
	for _, j := range got2 {
		ids[j["id"].(string)] = true
	}
	assert.True(t, ids["c1"], "completed job present in all-tab")
	assert.True(t, ids["f1"], "failed job present in all-tab")
}

// ジョブ詳細の timestamp は job 作成/処理/完了の実時刻から出す (#1398)。
// 旧実装は timestamp に NextProcessAt、processedOn に LastFailedAt を使い、
// completed/failed ジョブで 0 (= 1970/1/1) になっていた。
func TestQueueJobs_TimestampsFromJobState(t *testing.T) {
	created := time.UnixMilli(1_700_000_000_000)
	processed := time.UnixMilli(1_700_000_001_000)
	finished := time.UnixMilli(1_700_000_002_000)
	h, _, _, _ := newTestHandler(t)
	insp := &stubQueueInspector{
		completed: map[string][]*apiadmin.QueueTaskSummary{
			"deliver": {{
				ID: "c1", Queue: "deliver", Type: "x", State: "completed",
				EnqueuedAt: created, ProcessedAt: processed, CompletedAt: finished,
			}},
		},
		pending: map[string][]*apiadmin.QueueTaskSummary{
			"deliver": {{ID: "p1", Queue: "deliver", Type: "x", State: "pending", EnqueuedAt: created}},
		},
	}
	h.SetQueueInspector(insp)

	// completed: 3 つの時刻が実 ms で出る。
	rec := doPost(h.QueueJobs, `{"queue":"deliver","state":["completed"]}`, adminUser)
	require.Equal(t, http.StatusOK, rec.Code)
	var got []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Len(t, got, 1)
	assert.EqualValues(t, created.UnixMilli(), got[0]["timestamp"])
	assert.EqualValues(t, processed.UnixMilli(), got[0]["processedOn"])
	assert.EqualValues(t, finished.UnixMilli(), got[0]["finishedOn"])

	// pending: processedOn / finishedOn は省略され 1970 にならない。timestamp は present。
	rec2 := doPost(h.QueueJobs, `{"queue":"deliver","state":["wait"]}`, adminUser)
	require.Equal(t, http.StatusOK, rec2.Code)
	var got2 []map[string]any
	require.NoError(t, json.Unmarshal(rec2.Body.Bytes(), &got2))
	require.Len(t, got2, 1)
	assert.EqualValues(t, created.UnixMilli(), got2[0]["timestamp"])
	_, hasProcessed := got2[0]["processedOn"]
	assert.False(t, hasProcessed, "未処理ジョブは processedOn を省略する")
	_, hasFinished := got2[0]["finishedOn"]
	assert.False(t, hasFinished, "未完了ジョブは finishedOn を省略する")
}
