package admin

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/api/apierr"
	"github.com/shiroha-a/mk/internal/queue"
)

// QueueClear handles POST /api/admin/queue/clear.
func (h *Handler) QueueClear(c echo.Context) error {
	// upstream Misskey TS は paramDef で queue + state を required にしている (#929)。
	// mk-go も同 shape に揃え、permissive な「全 queue 一括 clear」を弾く。
	var req struct {
		Queue string `json:"queue"`
		State string `json:"state"`
	}
	if err := c.Bind(&req); err != nil || req.Queue == "" || req.State == "" {
		return c.JSON(http.StatusBadRequest, apierr.InvalidParam("queue and state are required."))
	}
	if h.queueInspector == nil {
		return c.NoContent(http.StatusNoContent)
	}
	_, _ = h.queueInspector.DeleteAllPendingTasks(req.Queue)
	return c.NoContent(http.StatusNoContent)
}

// QueueDeliverDelayed handles POST /api/admin/queue/deliver-delayed.
// Returns scheduled/retry tasks on the `deliver` queue.
func (h *Handler) QueueDeliverDelayed(c echo.Context) error {
	return h.listDelayedTasks(c, "deliver")
}

// QueueInboxDelayed handles POST /api/admin/queue/inbox-delayed.
// Returns scheduled/retry tasks on the `inbox` queue.
func (h *Handler) QueueInboxDelayed(c echo.Context) error {
	return h.listDelayedTasks(c, "inbox")
}

// delayedTasksMaxFetch is the upper bound on how many of each of scheduled /
// retry we pull from asynq to build the virtual delayed list. asynq pages
// scheduled and retry independently so we cannot naively forward the user's
// page number to both (retry items before the scheduled boundary would
// disappear). We instead fetch the first asynq page (up to 100 items) of each,
// merge scheduled-first, then slice by the user's (page, limit).
//
// 想定: admin/queue/*-delayed は通常 "stuck な配送" を目視で確認する用途で、
// 合計 200 件を超えるケースは運用上ほぼ存在しない。深いページングが必要なら
// /admin/queue/jobs を state=scheduled / state=retry で使う。
const delayedTasksMaxFetch = 100

func (h *Handler) listDelayedTasks(c echo.Context, queueName string) error {
	if h.queueInspector == nil {
		return c.JSON(http.StatusOK, []any{})
	}
	var req struct {
		Limit int `json:"limit"`
		Page  int `json:"page"`
	}
	_ = c.Bind(&req)
	if req.Limit <= 0 || req.Limit > 100 {
		req.Limit = 30
	}
	if req.Page < 1 {
		req.Page = 1
	}

	scheduled, _ := h.queueInspector.ListScheduledTasks(queueName, 1, delayedTasksMaxFetch)
	retry, _ := h.queueInspector.ListRetryTasks(queueName, 1, delayedTasksMaxFetch)
	combined := make([]*QueueTaskSummary, 0, len(scheduled)+len(retry))
	combined = append(combined, scheduled...)
	combined = append(combined, retry...)

	offset := (req.Page - 1) * req.Limit
	if offset >= len(combined) {
		return c.JSON(http.StatusOK, []map[string]any{})
	}
	end := min(offset+req.Limit, len(combined))
	out := make([]map[string]any, 0, end-offset)
	for _, t := range combined[offset:end] {
		out = append(out, packTaskSummary(t))
	}
	return c.JSON(http.StatusOK, out)
}

// QueueJobs handles POST /api/admin/queue/jobs.
//
// frontend admin/job-queue.vue は state を Bull の state 名配列で送る
// (`['completed', 'failed', 'active', 'delayed', 'wait']` など)。
// mk-go は asynq バックなので Bull state 名を asynq の list 呼び出しに
// マッピングする。合計 limit を超えないよう走査中に切り詰める。
func (h *Handler) QueueJobs(c echo.Context) error {
	// state は string でも string[] でも受け取れるようにする (frontend は
	// 配列、既存テストや admin CLI からは単一文字列でくる可能性がある)。
	var req struct {
		Queue  string          `json:"queue"`
		State  json.RawMessage `json:"state"`
		Limit  int             `json:"limit"`
		Page   int             `json:"page"`
		Search string          `json:"search"`
	}
	if err := c.Bind(&req); err != nil || req.Queue == "" || len(req.State) == 0 {
		// upstream Misskey TS は paramDef で queue + state を required にしている (#929)。
		return c.JSON(http.StatusBadRequest, apierr.InvalidParam("queue and state are required."))
	}
	if h.queueInspector == nil {
		return c.JSON(http.StatusOK, []any{})
	}
	if req.Limit <= 0 || req.Limit > 100 {
		req.Limit = 30
	}
	if req.Page < 1 {
		req.Page = 1
	}
	states := parseStateField(req.State)
	seen := make(map[string]struct{}, req.Limit)
	out := make([]map[string]any, 0, req.Limit)
outer:
	for _, state := range states {
		rows, err := h.listTasksForState(req.Queue, state, req.Page, req.Limit)
		if err != nil {
			continue
		}
		for _, t := range rows {
			if len(out) >= req.Limit {
				break outer
			}
			// state指定が複数重なる (例: all タブ) とき同じ task が
			// 重複しないよう ID で de-dup する。
			if _, dup := seen[t.ID]; dup {
				continue
			}
			seen[t.ID] = struct{}{}
			out = append(out, packTaskSummary(t))
		}
	}
	return c.JSON(http.StatusOK, out)
}

// parseStateField normalizes the `state` request field which can be a single
// string or an array of strings (Misskey frontend sends array). Empty input
// defaults to "wait" (Bull wording) = asynq "pending".
func parseStateField(raw json.RawMessage) []string {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return []string{"wait"}
	}
	// try as array first
	var arr []string
	if err := json.Unmarshal(raw, &arr); err == nil && len(arr) > 0 {
		return arr
	}
	var single string
	if err := json.Unmarshal(raw, &single); err == nil && single != "" {
		return []string{single}
	}
	return []string{"wait"}
}

// listTasksForState maps a Bull/asynq state name to the matching asynq list
// call. Returns an empty slice for states asynq does not support (completed /
// failed / paused) so the admin tab renders an empty list instead of 500.
func (h *Handler) listTasksForState(queue, state string, page, limit int) ([]*QueueTaskSummary, error) {
	switch state {
	case "active":
		return h.queueInspector.ListActiveTasks(queue, page, limit)
	case "scheduled":
		return h.queueInspector.ListScheduledTasks(queue, page, limit)
	case "retry":
		return h.queueInspector.ListRetryTasks(queue, page, limit)
	// Bull と asynq の用語対応
	case "wait", "pending":
		return h.queueInspector.ListPendingTasks(queue, page, limit)
	case "delayed":
		// delayed は Bull 用語で asynq の scheduled + retry に対応する。
		sched, _ := h.queueInspector.ListScheduledTasks(queue, page, limit)
		retry, _ := h.queueInspector.ListRetryTasks(queue, page, limit)
		return append(sched, retry...), nil
	case "completed", "failed", "paused":
		// asynq は retention 未設定だと completed/failed 履歴を保持しない。
		// 履歴APIが無いので空配列でフロントを通す (tab 表示自体は出す)。
		return nil, nil
	default:
		return h.queueInspector.ListPendingTasks(queue, page, limit)
	}
}

// QueuePromoteJobs handles POST /api/admin/queue/promote-jobs.
func (h *Handler) QueuePromoteJobs(c echo.Context) error {
	// asynq に bulk promote API が無いため、対象 queue の scheduled/retry を
	// 1 ページずつ拾って RunTask で逐次 promote する。大量投入時は後続の
	// ページを クライアント側で再呼び出しする運用。
	// upstream Misskey TS は paramDef で queue を required にしている (#929)。
	var req struct {
		Queue string `json:"queue"`
	}
	if err := c.Bind(&req); err != nil || req.Queue == "" {
		return c.JSON(http.StatusBadRequest, apierr.InvalidParam("queue is required."))
	}
	if h.queueInspector == nil {
		return c.NoContent(http.StatusNoContent)
	}
	promoted := 0
	for _, state := range []string{"scheduled", "retry"} {
		var rows []*QueueTaskSummary
		if state == "scheduled" {
			rows, _ = h.queueInspector.ListScheduledTasks(req.Queue, 1, 100)
		} else {
			rows, _ = h.queueInspector.ListRetryTasks(req.Queue, 1, 100)
		}
		for _, t := range rows {
			if err := h.queueInspector.RunTask(req.Queue, t.ID); err == nil {
				promoted++
			}
		}
	}
	return c.JSON(http.StatusOK, map[string]any{"promoted": promoted})
}

// shapeQueueForFrontend adapts a QueueInfoResult to the Misskey Bull-shaped
// JSON expected by the admin/job-queue.vue page.
//
// BullMQ (Misskey本家のjob queue) と asynq は設計思想が根本的に異なり、
// 完全互換は不可能：
//   - asynq に存在しない queue 名 (inbox / db / relationship / system 等):
//     frontend は Misskey.queueTypes で hardcode しており mk-go の queue 名
//     (deliver / push / webhook / export / maintenance) と一致しない。
//   - db.{memory,uptime,clients} は BullMQ が per-queue で持つ Redis 統計。
//     asynq には相当機能が無い。
//   - metrics.{completed,failed}.data は BullMQ の per-queue time-series。
//     asynq は retention 設定なしでは履歴を持たない。
//
// BullMQ と完全互換のまま Go ネイティブ性能を活かすのは別レイヤ (独立
// OSS ライブラリ化) で取り組む方針 (#377 参照)。それまでの中継措置として
// 見た目が壊れないよう未対応 field を 0 固定で stub する。
func shapeQueueForFrontend(info *QueueInfoResult, completed, failed *QueueMetricsResult) map[string]any {
	// delayed は Misskey 用語で scheduled + retry の合計に相当する
	// (どちらも「すぐには実行されない」状態)。
	delayed := info.Scheduled + info.Retry

	// metrics は driver から渡された QueueMetricsResult を採用し、
	// nil/欠損時は info.Completed / info.Failed の累積値で count を埋め、
	// data は空配列にフォールバックする。前者は mkq driver で
	// WithJobMetrics が無効、後者は asynq driver の time-series 非対応
	// シナリオを想定。
	completedData, completedCount := metricsToFrontend(completed, int64(info.Completed))
	failedData, failedCount := metricsToFrontend(failed, int64(info.Failed))

	return map[string]any{
		"name":          info.Queue,
		"qualifiedName": info.Queue,
		"isPaused":      false,
		"counts": map[string]any{
			"active":    info.Active,
			"delayed":   delayed,
			"waiting":   info.Pending,
			"completed": info.Completed,
			"failed":    info.Failed,
		},
		"metrics": map[string]any{
			"completed": map[string]any{"data": completedData, "count": completedCount},
			"failed":    map[string]any{"data": failedData, "count": failedCount},
		},
		// asynq にはBull相当のper-queue redis DB statsがないため、frontend
		// が参照するフィールドを0固定でstubする。
		"db": map[string]any{
			"processId": 0,
			"port":      0,
			"runId":     "",
			"clients":   map[string]any{"connected": 0, "blocked": 0},
			"memory":    map[string]any{"peak": 0, "total": 0, "used": 0},
			"uptime":    0,
		},
	}
}

// metricsToFrontend normalises a driver-supplied QueueMetricsResult
// into the (data, count) pair the BullMQ-shaped frontend expects.
// nil m or empty m.Data both map to an empty []int64 (frontend's
// XChart treats nil and []) for stable JSON.
//
// fallbackCount kicks in when the driver returned no Count (e.g.
// metrics writer disabled) — admin UI then shows the cumulative
// Completed/Failed from GetQueueInfo as the headline number even
// though the chart line stays flat.
func metricsToFrontend(m *QueueMetricsResult, fallbackCount int64) ([]int64, int64) {
	if m == nil {
		return []int64{}, fallbackCount
	}
	data := m.Data
	if data == nil {
		data = []int64{}
	}
	count := m.Count
	if count == 0 {
		count = fallbackCount
	}
	return data, count
}

// QueueQueueStats handles POST /api/admin/queue/queue-stats.
// frontend admin/job-queue.vue の `fetchCurrentQueue` は単一 queue 名を
// 受けて 1 queue の shape を返す設計になっているので、req.Queue が来たら
// その queue だけ、来なければ全 queue を返すという両対応にする。
//
// frontend は Misskey Bull の queue 名を hardcode (`Misskey.queueTypes`) で
// 列挙しており、mk-go の asynq queue 名 (deliver/push/maintenance/webhook/
// export) と完全には一致しない。存在しない queue を叩かれたときは 500 で
// はなくゼロ埋めの shape を返して、フロント側の queueInfo が stale に
// ならないようにする。
func (h *Handler) QueueQueueStats(c echo.Context) error {
	// upstream Misskey TS は paramDef で queue を required にしている (#929)。
	var req struct {
		Queue string `json:"queue"`
	}
	if err := c.Bind(&req); err != nil || req.Queue == "" {
		return c.JSON(http.StatusBadRequest, apierr.InvalidParam("queue is required."))
	}
	if h.queueInspector == nil {
		return c.JSON(http.StatusOK, map[string]any{})
	}
	info, err := h.queueInspector.GetQueueInfo(req.Queue)
	if err != nil || info == nil {
		completed, failed := h.fetchQueueMetrics(req.Queue)
		return c.JSON(http.StatusOK, shapeQueueForFrontend(&QueueInfoResult{Queue: req.Queue}, completed, failed))
	}
	completed, failed := h.fetchQueueMetrics(req.Queue)
	return c.JSON(http.StatusOK, shapeQueueForFrontend(info, completed, failed))
}

// fetchQueueMetrics queries the inspector for completed / failed
// per-minute history. Errors are absorbed and returned as nil so the
// admin endpoints stay 200 OK — partial chart data is preferable to
// a hard failure when the metrics writer is opt-in (mkq driver) or
// absent altogether (asynq driver).
func (h *Handler) fetchQueueMetrics(qname string) (completed, failed *QueueMetricsResult) {
	if h.queueInspector == nil {
		return nil, nil
	}
	completed, _ = h.queueInspector.QueueMetrics(qname, queue.MetricsKindCompleted)
	failed, _ = h.queueInspector.QueueMetrics(qname, queue.MetricsKindFailed)
	return
}

// QueueQueues handles POST /api/admin/queue/queues.
func (h *Handler) QueueQueues(c echo.Context) error {
	if h.queueInspector == nil {
		return c.JSON(http.StatusOK, []any{})
	}
	queues, err := h.queueInspector.Queues()
	if err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.InternalError())
	}
	result := make([]map[string]any, 0, len(queues))
	for _, q := range queues {
		info, err := h.queueInspector.GetQueueInfo(q)
		if err != nil {
			continue
		}
		completed, failed := h.fetchQueueMetrics(q)
		result = append(result, shapeQueueForFrontend(info, completed, failed))
	}
	return c.JSON(http.StatusOK, result)
}

// QueueRemoveJob handles POST /api/admin/queue/remove-job.
func (h *Handler) QueueRemoveJob(c echo.Context) error {
	var req struct {
		Queue string `json:"queue"`
		ID    string `json:"id"`
	}
	if err := c.Bind(&req); err != nil || req.Queue == "" || req.ID == "" {
		return c.JSON(http.StatusBadRequest, apierr.InvalidParam("queue and id are required."))
	}
	if h.queueInspector == nil {
		return c.NoContent(http.StatusNoContent)
	}
	// asynq DeleteTask は不明 task id でも nil error を返す (= idempotent)
	// が、upstream Misskey TS は 4xx を返す。drop-in 互換のため事前に
	// GetTaskInfo で存在確認して、無ければ 404 を返す (#929)。
	if _, err := h.queueInspector.GetTaskInfo(req.Queue, req.ID); err != nil {
		return c.JSON(http.StatusNotFound, apierr.NotFound())
	}
	if err := h.queueInspector.DeleteTask(req.Queue, req.ID); err != nil {
		return c.JSON(http.StatusNotFound, apierr.NotFound())
	}
	return c.NoContent(http.StatusNoContent)
}

// QueueRetryJob handles POST /api/admin/queue/retry-job.
func (h *Handler) QueueRetryJob(c echo.Context) error {
	var req struct {
		Queue string `json:"queue"`
		ID    string `json:"id"`
	}
	if err := c.Bind(&req); err != nil || req.Queue == "" || req.ID == "" {
		return c.JSON(http.StatusBadRequest, apierr.InvalidParam("queue and id are required."))
	}
	if h.queueInspector == nil {
		return c.NoContent(http.StatusNoContent)
	}
	// asynq RunTask も不明 id で nil を返す idempotent 挙動。drop-in 互換の
	// ため事前に GetTaskInfo で存在確認 (#929)。
	if _, err := h.queueInspector.GetTaskInfo(req.Queue, req.ID); err != nil {
		return c.JSON(http.StatusNotFound, apierr.NotFound())
	}
	if err := h.queueInspector.RunTask(req.Queue, req.ID); err != nil {
		return c.JSON(http.StatusNotFound, apierr.NotFound())
	}
	return c.NoContent(http.StatusNoContent)
}

// QueueShowJob handles POST /api/admin/queue/show-job.
func (h *Handler) QueueShowJob(c echo.Context) error {
	var req struct {
		Queue string `json:"queue"`
		ID    string `json:"id"`
	}
	if err := c.Bind(&req); err != nil || req.Queue == "" || req.ID == "" {
		return c.JSON(http.StatusBadRequest, apierr.InvalidParam("queue and id are required."))
	}
	if h.queueInspector == nil {
		return c.JSON(http.StatusNotFound, apierr.NotFound())
	}
	t, err := h.queueInspector.GetTaskInfo(req.Queue, req.ID)
	if err != nil {
		return c.JSON(http.StatusNotFound, apierr.NotFound())
	}
	return c.JSON(http.StatusOK, packTaskSummary(t))
}

// QueueShowJobLogs handles POST /api/admin/queue/show-job-logs.
// asynq does not persist per-task log output. Returns an empty array to keep
// the admin UI usable without extra infra.
func (h *Handler) QueueShowJobLogs(c echo.Context) error { return c.JSON(http.StatusOK, []any{}) }

// packTaskSummary normalizes a QueueTaskSummary into the Misskey Bull-shaped
// JSON expected by admin/job-queue.vue and job-queue.job.vue. frontend は
// `job.stacktrace.length` / `job.opts.attempts` / `job.opts.repeat` などを
// 直接参照するので、未設定フィールドは undefined ではなく空配列 / 0 / nil
// で埋めて render crash を防ぐ。
//
// asynqにしか無い field (state / queue / payload raw 等) は残しつつ、
// Bull 互換 field を追加する形で出力する (両方を見るadmin toolへの配慮)。
func packTaskSummary(t *QueueTaskSummary) map[string]any {
	if t == nil {
		return nil
	}
	isFailed := t.LastErr != ""
	// asynqはBullと違いEnqueuedAtを保持しない (TaskInfoに該当field無し)。
	// frontend の MkTl / MkTime は 0 を「たった今」として扱うので害はない。
	pack := map[string]any{
		// Bull 互換 field (frontend 必須)
		"id":           t.ID,
		"name":         t.Type,
		"timestamp":    formatUnixMillisOrZero(t.NextProcessAt),
		"processedAt":  formatUnixMillisOrZero(t.LastFailedAt),
		"processedOn":  formatUnixMillisOrZero(t.LastFailedAt),
		"finishedOn":   formatUnixMillisOrZero(t.CompletedAt),
		"progress":     0,
		"attempts":     t.Retried,
		"attemptsMade": t.Retried,
		"isFailed":     isFailed,
		"delay":        0,
		"returnValue":  nil,
		"stacktrace":   stacktraceFrom(t.LastErr),
		"data":         rawJSONOrString(t.Payload),
		"opts": map[string]any{
			"attempts": t.MaxRetry,
			"delay":    0,
			"repeat":   nil,
		},
		// asynq-native field (既存 admin tool 互換のために残す)
		"queue":    t.Queue,
		"type":     t.Type,
		"state":    t.State,
		"payload":  string(t.Payload),
		"retried":  t.Retried,
		"maxRetry": t.MaxRetry,
	}
	if t.LastErr != "" {
		pack["lastErr"] = t.LastErr
		pack["failedReason"] = t.LastErr
	}
	if !t.LastFailedAt.IsZero() {
		pack["lastFailedAt"] = t.LastFailedAt.UTC().Format(time.RFC3339Nano)
	}
	if !t.NextProcessAt.IsZero() {
		pack["nextProcessAt"] = t.NextProcessAt.UTC().Format(time.RFC3339Nano)
	}
	if !t.CompletedAt.IsZero() {
		pack["completedAt"] = t.CompletedAt.UTC().Format(time.RFC3339Nano)
	}
	return pack
}

// formatUnixMillisOrZero returns the unix-ms representation of t, or 0 when
// t is the zero time. Bull's timestamps are unix milliseconds.
func formatUnixMillisOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

// stacktraceFrom returns a single-element stacktrace array when lastErr is
// non-empty, otherwise an empty array. frontend は `job.stacktrace.length`
// を見るため undefined では TypeError で crash する。
func stacktraceFrom(lastErr string) []string {
	if lastErr == "" {
		return []string{}
	}
	return []string{lastErr}
}

// rawJSONOrString attempts to decode payload as JSON so the admin UI can
// render structured data. Falls back to a string representation for payloads
// that are not valid JSON.
func rawJSONOrString(payload []byte) any {
	if len(payload) == 0 {
		return nil
	}
	var decoded any
	if err := json.Unmarshal(payload, &decoded); err == nil {
		return decoded
	}
	return string(payload)
}

// QueueStats handles POST /api/admin/queue/stats.
func (h *Handler) QueueStats(c echo.Context) error {
	if h.queueInspector == nil {
		return c.JSON(http.StatusOK, map[string]any{
			"deliver": map[string]any{"activeSince": nil, "active": 0, "waiting": 0, "delayed": 0},
			"inbox":   map[string]any{"activeSince": nil, "active": 0, "waiting": 0, "delayed": 0},
		})
	}
	result := map[string]any{}
	for _, qname := range []string{"deliver", "inbox"} {
		info, err := h.queueInspector.GetQueueInfo(qname)
		if err != nil {
			result[qname] = map[string]any{"activeSince": nil, "active": 0, "waiting": 0, "delayed": 0}
			continue
		}
		result[qname] = map[string]any{
			// Bull の delayed は asynq の Scheduled (未来実行予定) と
			// Retry (失敗後再試行待ち) の両方を含む。stream/queue_stats_publisher.go
			// の WebSocket publisher と semantics を揃える (#654)。
			"activeSince": nil, "active": info.Active, "waiting": info.Pending, "delayed": info.Scheduled + info.Retry,
		}
	}
	return c.JSON(http.StatusOK, result)
}
