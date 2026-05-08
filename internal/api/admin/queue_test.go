package admin_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	apiadmin "github.com/shiroha-a/mk/internal/api/admin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubQueueInspector is a test double for admin.QueueInspector that returns
// configurable canned responses without touching Redis.
type stubQueueInspector struct {
	queues        []string
	info          map[string]*apiadmin.QueueInfoResult
	pending       map[string][]*apiadmin.QueueTaskSummary
	active        map[string][]*apiadmin.QueueTaskSummary
	scheduled     map[string][]*apiadmin.QueueTaskSummary
	retry         map[string][]*apiadmin.QueueTaskSummary
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

func TestQueueDeliverDelayed_CombinesScheduledAndRetry(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	insp := &stubQueueInspector{
		scheduled: map[string][]*apiadmin.QueueTaskSummary{
			"deliver": {{ID: "s1"}},
		},
		retry: map[string][]*apiadmin.QueueTaskSummary{
			"deliver": {{ID: "r1"}},
		},
	}
	h.SetQueueInspector(insp)
	rec := doPost(h.QueueDeliverDelayed, `{}`, adminUser)
	assert.Equal(t, http.StatusOK, rec.Code)
	var rows []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &rows))
	assert.Len(t, rows, 2)
}

// listDelayedTasks が scheduled と retry を結合する際に合計が limit を超え
// ないことを確認する (regression guard)。
func TestQueueDeliverDelayed_CappedAtLimit(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	scheduled := make([]*apiadmin.QueueTaskSummary, 0, 3)
	for _, id := range []string{"s1", "s2", "s3"} {
		scheduled = append(scheduled, &apiadmin.QueueTaskSummary{ID: id})
	}
	retry := make([]*apiadmin.QueueTaskSummary, 0, 3)
	for _, id := range []string{"r1", "r2", "r3"} {
		retry = append(retry, &apiadmin.QueueTaskSummary{ID: id})
	}
	insp := &stubQueueInspector{
		scheduled: map[string][]*apiadmin.QueueTaskSummary{"deliver": scheduled},
		retry:     map[string][]*apiadmin.QueueTaskSummary{"deliver": retry},
	}
	h.SetQueueInspector(insp)

	rec := doPost(h.QueueDeliverDelayed, `{"limit":4}`, adminUser)
	assert.Equal(t, http.StatusOK, rec.Code)
	var rows []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &rows))
	assert.Len(t, rows, 4, "combined output must not exceed requested limit")
	// scheduled を先に詰めるので s1..s3 が最初の 3 件、続いて r1 が 4 件目
	assert.Equal(t, "s1", rows[0]["id"])
	assert.Equal(t, "r1", rows[3]["id"])
}

// listDelayedTasks が scheduled/retry 境界を越えて正しくページングできること
// を確認する。旧実装では同じ req.Page を両リストに forward していたため、
// scheduled でページが埋まると retry 側の早い項目が永久に見えなくなる bug が
// あった (Devin #183 review)。
func TestQueueDeliverDelayed_PagingCrossesBoundary(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	scheduled := make([]*apiadmin.QueueTaskSummary, 0, 5)
	for _, id := range []string{"s1", "s2", "s3", "s4", "s5"} {
		scheduled = append(scheduled, &apiadmin.QueueTaskSummary{ID: id})
	}
	retry := make([]*apiadmin.QueueTaskSummary, 0, 3)
	for _, id := range []string{"r1", "r2", "r3"} {
		retry = append(retry, &apiadmin.QueueTaskSummary{ID: id})
	}
	insp := &stubQueueInspector{
		scheduled: map[string][]*apiadmin.QueueTaskSummary{"deliver": scheduled},
		retry:     map[string][]*apiadmin.QueueTaskSummary{"deliver": retry},
	}
	h.SetQueueInspector(insp)

	// page 1 limit 3 → s1, s2, s3
	rec := doPost(h.QueueDeliverDelayed, `{"limit":3,"page":1}`, adminUser)
	var rows []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &rows))
	require.Len(t, rows, 3)
	assert.Equal(t, []any{"s1", "s2", "s3"}, []any{rows[0]["id"], rows[1]["id"], rows[2]["id"]})

	// page 2 limit 3 → s4, s5, r1 (境界をまたぐ)
	rec = doPost(h.QueueDeliverDelayed, `{"limit":3,"page":2}`, adminUser)
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &rows))
	require.Len(t, rows, 3)
	assert.Equal(t, []any{"s4", "s5", "r1"}, []any{rows[0]["id"], rows[1]["id"], rows[2]["id"]})

	// page 3 limit 3 → r2, r3
	rec = doPost(h.QueueDeliverDelayed, `{"limit":3,"page":3}`, adminUser)
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &rows))
	require.Len(t, rows, 2)
	assert.Equal(t, []any{"r2", "r3"}, []any{rows[0]["id"], rows[1]["id"]})

	// page 4 → 空
	rec = doPost(h.QueueDeliverDelayed, `{"limit":3,"page":4}`, adminUser)
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &rows))
	assert.Empty(t, rows)
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
