package admin

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/activitypub"
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
//
// Response shape: `[[host, count], ...]` の tuple 配列 (count desc で sort)。
// host は recipient inbox URL (`DeliverPayload.Inbox`) から抽出する。
// upstream Misskey TS spec (`admin/queue/deliver-delayed.ts`) と互換。
//
// admin/control panel の "Errored Instances" panel は b[1] で count に
// アクセスするので、object 配列 (旧 mk-go shape) では NaN 表示になる (#1179)。
func (h *Handler) QueueDeliverDelayed(c echo.Context) error {
	tasks := h.fetchAllDelayedTasks("deliver")
	return c.JSON(http.StatusOK, aggregateByHost(tasks, deliverHostFromPayload))
}

// QueueInboxDelayed handles POST /api/admin/queue/inbox-delayed.
//
// Response shape: `[[host, count], ...]` の tuple 配列 (count desc で sort)。
// host は HTTP Signature header の `keyId` (絶対 URL) から抽出する。
// upstream Misskey TS は `new URL(job.data.signature.keyId).host` と
// 同等の処理。mk-go の `InboxPayload.Host` は enqueue 時に未設定なので
// signature header 経由でしか引けない (#1179)。
func (h *Handler) QueueInboxDelayed(c echo.Context) error {
	tasks := h.fetchAllDelayedTasks("inbox")
	return c.JSON(http.StatusOK, aggregateByHost(tasks, inboxHostFromPayload))
}

// delayedTasksFetchPageSize は scheduled / retry を page 走査する際の 1 page
// 当たり件数。100 は asynq inspector の page size 制限 (1〜) 内で妥当な大きさ。
const delayedTasksFetchPageSize = 100

// delayedTasksMaxPages は scheduled / retry それぞれの page 走査の上限。
// = 100K tasks per state, 200K total。これは defense-in-depth の二重目的:
//
//   - inspector が常に full page を返すような misbehavior 時の無限ループ防御
//   - 連合先長期 down 等の pathological case で、admin panel が backend
//     memory を食い潰さない soft cap
//
// 通常の運用では数千件で頭打ちになるはずなので、この上限を踏むのは異常状態。
// 到達時は slog.Warn で diag を残し、収集できた分だけで集計を進める
// (= panel に top hosts は出るので、admin はその情報を元に対処できる)。
//
// var にしているのは test seam: cap 防御の挙動を確認する test が小さい
// cap を一時設定するため。production code から書き換えてはならない。
var delayedTasksMaxPages = 1000

// fetchAllDelayedTasks fetches every scheduled+retry task for the queue by
// iterating pages of size delayedTasksFetchPageSize until each list is
// exhausted or delayedTasksMaxPages の上限に達するまで。結果は downstream の
// host aggregation の union として返す。
//
// Misskey TS の BullMQ getJobs(['delayed']) は全件取得が前提なので、通常
// page が空になるまで走査するが、defense-in-depth として上限を設けている
// (`delayedTasksMaxPages` のコメント参照)。
func (h *Handler) fetchAllDelayedTasks(queueName string) []*QueueTaskSummary {
	if h.queueInspector == nil {
		return nil
	}
	var all []*QueueTaskSummary
	for _, spec := range []struct {
		state string
		fetch func(string, int, int) ([]*QueueTaskSummary, error)
	}{
		{"scheduled", h.queueInspector.ListScheduledTasks},
		{"retry", h.queueInspector.ListRetryTasks},
	} {
		for page := 1; page <= delayedTasksMaxPages; page++ {
			rows, err := spec.fetch(queueName, page, delayedTasksFetchPageSize)
			if err != nil || len(rows) == 0 {
				break
			}
			all = append(all, rows...)
			// partial page を取った時点で正常にデータ尽きと判断、warn せず
			// 抜ける。full page が続いたまま cap に到達した時だけ warn する
			// (= データがまだ残っている可能性が高い異常状態)。
			if len(rows) < delayedTasksFetchPageSize {
				break
			}
			if page == delayedTasksMaxPages {
				slog.Warn("admin/queue/delayed: page cap reached, truncating aggregation",
					"queue", queueName, "state", spec.state,
					"maxPages", delayedTasksMaxPages,
					"pageSize", delayedTasksFetchPageSize,
					"collected", len(all))
			}
		}
	}
	return all
}

// aggregateByHost reduces tasks to (host, count) tuples sorted by count
// descending, then host ascending for stable output. hostFn は task 1 件から
// host 文字列を抽出する関数で、空文字列を返した task は skip される
// (= 不正 payload / 集計対象外を弾く)。
//
// 戻り値は []any でなく [][]any なので、frontend が `b[1]` でアクセスする
// JSON tuple 表現 (`[["a", 1], ...]`) になる。
func aggregateByHost(tasks []*QueueTaskSummary, hostFn func(*QueueTaskSummary) string) [][]any {
	counts := make(map[string]int)
	for _, t := range tasks {
		host := hostFn(t)
		if host == "" {
			continue
		}
		counts[host]++
	}
	hosts := make([]string, 0, len(counts))
	for h := range counts {
		hosts = append(hosts, h)
	}
	sort.Slice(hosts, func(i, j int) bool {
		if counts[hosts[i]] != counts[hosts[j]] {
			return counts[hosts[i]] > counts[hosts[j]]
		}
		return hosts[i] < hosts[j]
	})
	out := make([][]any, 0, len(hosts))
	for _, h := range hosts {
		out = append(out, []any{h, counts[h]})
	}
	return out
}

// deliverHostFromPayload extracts the recipient host from a deliver queue
// task. DeliverPayload.Inbox は recipient の AP inbox URL なので、その host
// が即 federation 先 host になる。
//
// 注意: Go の `url.Parse` は JS の `new URL()` より permissive で、ほとんどの
// malformed input に対して err=nil + Host="" の `*url.URL` を返す (err は
// `::scheme::` 形式の壊れた scheme prefix 等、限定的な case でのみ返る)。
// caller (`aggregateByHost`) は host == "" を skip するので、err 分岐と
// 空 Host のどちらも同じ「集計から外す」挙動になり、両方 cover している。
func deliverHostFromPayload(t *QueueTaskSummary) string {
	if t == nil || len(t.Payload) == 0 {
		return ""
	}
	var p queue.DeliverPayload
	if err := json.Unmarshal(t.Payload, &p); err != nil {
		return ""
	}
	u, err := url.Parse(p.Inbox)
	if err != nil {
		return ""
	}
	return u.Host
}

// inboxHostFromPayload extracts the sender host from an inbox queue task.
// InboxPayload.Host は enqueue 時に未設定 (api/inbox/handler.go) なので、
// `Headers["Signature"]` から HTTP Signature の keyId URL を取って host を
// 抽出する。これは upstream Misskey TS の
// `new URL(job.data.signature.keyId).host` と一致する。
func inboxHostFromPayload(t *QueueTaskSummary) string {
	if t == nil || len(t.Payload) == 0 {
		return ""
	}
	var p queue.InboxPayload
	if err := json.Unmarshal(t.Payload, &p); err != nil {
		return ""
	}
	sig := p.Headers["Signature"]
	if sig == "" {
		return ""
	}
	parsed, err := activitypub.ParseSignatureHeader(sig)
	if err != nil || parsed == nil {
		return ""
	}
	u, err := url.Parse(parsed.KeyID)
	if err != nil {
		return ""
	}
	return u.Host
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

// listTasksForState maps a Bull state name to the matching inspector list call.
// completed / failed は mkq の finished-job 保持から引く。"paused" は queue 状態
// であってジョブ一覧ではないため空で返す (admin tab は空表示)。
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
	case "completed":
		// mkq は WithKeepCompleted retention で完了ジョブを保持する。frontend
		// の All / Latest / Completed タブはこれを引く (#1396)。
		return h.queueInspector.ListCompletedTasks(queue, page, limit)
	case "failed":
		return h.queueInspector.ListFailedTasks(queue, page, limit)
	case "paused":
		// "paused" は queue 状態であってジョブ一覧ではない。mk-go は queue を
		// pause しないため空で返す。
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
// counts / db は mkq driver から取得した実値で埋まる (db は job-queue Redis の
// INFO 由来。queueDBStats 参照)。metrics.{completed,failed}.data は mkq の
// per-queue time-series で、job-metrics retention が有効な queue でのみ履歴を
// 持つ (無効なら data 空 + cumulative count にフォールバック)。
//
// 残る非互換: frontend は Misskey の queue 名 (system / endedPollNotification /
// postScheduledNote 等) を列挙しうるが、mk-go が実行しない queue は GetQueueInfo
// が空を返すため 0 表示になる (#1393 で別途整理予定)。
func shapeQueueForFrontend(info *QueueInfoResult, completed, failed *QueueMetricsResult, db map[string]any) map[string]any {
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
		// db は job-queue Redis の INFO 由来 (memory / clients / uptime 等)。
		// caller が queueDBStats() で一度引いて全 queue に渡す (INFO は接続単位
		// で全 queue 同値)。provider 未配線時は defaultQueueDB() の 0 埋め。
		"db": db,
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
	db := h.queueDBStats(c.Request().Context())
	info, err := h.queueInspector.GetQueueInfo(req.Queue)
	if err != nil || info == nil {
		completed, failed := h.fetchQueueMetrics(req.Queue)
		return c.JSON(http.StatusOK, shapeQueueForFrontend(&QueueInfoResult{Queue: req.Queue}, completed, failed, db))
	}
	completed, failed := h.fetchQueueMetrics(req.Queue)
	return c.JSON(http.StatusOK, shapeQueueForFrontend(info, completed, failed, db))
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
	// db (Redis INFO) は全 queue で同値なので一度だけ引いて使い回す。
	db := h.queueDBStats(c.Request().Context())
	result := make([]map[string]any, 0, len(queues))
	for _, q := range queues {
		info, err := h.queueInspector.GetQueueInfo(q)
		if err != nil {
			continue
		}
		completed, failed := h.fetchQueueMetrics(q)
		result = append(result, shapeQueueForFrontend(info, completed, failed, db))
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
	// golden QueueJob.data は Record (object) 必須。空 payload を null で返すと
	// frontend の job detail が型エラーになるため {} に coalesce する (#1304)。
	data := rawJSONOrString(t.Payload)
	if data == nil {
		data = map[string]any{}
	}
	// asynqはBullと違いEnqueuedAtを保持しない (TaskInfoに該当field無し)。
	// frontend の MkTl / MkTime は 0 を「たった今」として扱うので害はない。
	pack := map[string]any{
		// Bull 互換 field (frontend 必須)
		"id":   t.ID,
		"name": t.Type,
		// timestamp = job 作成時刻 (Bull job.timestamp)。常に present。
		"timestamp": formatUnixMillisOrZero(t.EnqueuedAt),
		// golden QueueJob は progress / returnValue を Record (object) 必須、
		// failedReason を string 必須とする。Bull の job は failure 時のみ理由を
		// 持つが、schema 上は常に present (無 failure 時は空文字) なので常に出す。
		// progress / returnValue は asynq に相当概念が無いため空 object で埋める
		// (旧実装は number 0 / null で golden と乖離していた、#1304)。
		"progress":     map[string]any{},
		"attempts":     t.Retried,
		"attemptsMade": t.Retried,
		"isFailed":     isFailed,
		"delay":        0,
		"returnValue":  map[string]any{},
		"failedReason": t.LastErr,
		"stacktrace":   stacktraceFrom(t.LastErr),
		"data":         data,
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
	// processedOn / finishedOn は golden で optional:true (number)。値があるときだけ
	// number で出す。未処理 / 未完了は key 省略 → frontend の `job.processedOn !=
	// null` で "Processed at"/"Finished at" 行が非表示になる (Bull 互換、#1398)。
	if !t.ProcessedAt.IsZero() {
		ms := t.ProcessedAt.UnixMilli()
		pack["processedOn"] = ms
		pack["processedAt"] = ms
	}
	// 完了時刻は成功 (CompletedAt) / 失敗 (LastFailedAt) いずれかの finish 時刻。
	if finished := t.CompletedAt; !finished.IsZero() {
		pack["finishedOn"] = finished.UnixMilli()
	} else if !t.LastFailedAt.IsZero() {
		pack["finishedOn"] = t.LastFailedAt.UnixMilli()
	}
	if t.LastErr != "" {
		pack["lastErr"] = t.LastErr
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
