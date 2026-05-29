package mkqdriver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/shiroha-a/mkq"

	"github.com/shiroha-a/mk/internal/queue/driver"
)

// Inspector implements driver.Inspector over the per-queue API
// surface mkq exposes (Counts / ListJobs / Get / RemoveJob /
// PromoteJob / RetryJob).
//
// State strings returned in TaskSummary mirror BullMQ buckets ("wait",
// "active", "delayed", "prioritized", "completed", "failed", "paused")
// rather than the asynq state strings — this keeps the bull-board /
// Misskey admin UI rendering correct without an extra translation.
type Inspector struct {
	driver *Driver
}

// Queues returns the queues mk-go manages (Define'd at driver construction),
// in configured order. We intentionally do NOT return mkq's shared Redis queue
// registry (SMEMBERS {prefix}:queues): on a drop-in over an existing Misskey TS
// instance that registry also carries the upstream BullMQ queue names
// (system / endedPollNotification / postScheduledNote / db / relationship 等)
// which mk-go never processes, so listing them would only fill the admin
// dashboard with permanently-zero dead queues (#1393)。
func (i *Inspector) Queues() ([]string, error) {
	out := make([]string, len(i.driver.queueNames))
	copy(out, i.driver.queueNames)
	return out, nil
}

// GetQueueInfo returns the wait / active / delayed / completed /
// failed counters for the named queue. Pending maps to mkq's wait
// bucket (asynq's pending semantics).
//
// Retry / Scheduled semantics: mkq の delayed bucket は scheduled
// (cron / 初回 delayed enqueue) と retry-backoff 待ち (= 失敗して次の
// 試行待ち) が **混在** している (BullMQ 同等。mkq#64 close 時の
// upstream comment で確認)。両者を区別するため driver 側で delayed
// bucket を `atm` (= attemptsMade、BullMQ HASH field) で filter し、
// `atm == 0` を Scheduled / `atm > 0` を Retry にマッピングする。
// `failed` bucket は permanent failure (= dead letter) のみで Retry には
// 含めない。これにより asynq semantic と完全一致する (#1187):
//
//	asynq.Scheduled = delayed[atm==0] + repeat ZSET
//	asynq.Retry     = delayed[atm>0]
//	asynq.Failed    = failed bucket size
//
// cost: delayed bucket 全件 ListJobs (ZRANGE + N×HGETALL pipeline) が
// publisher の 3 秒間隔で走る。通常 < 100 件で問題なし、federation 障害
// 時の数千件でも admin polling として許容。将来必要なら Lua count-only
// API を mkq に proposal する余地あり。
//
// fallback: delayed split が失敗したら保守的に旧挙動 (delayed 全体を
// Scheduled、Retry=0) に倒す。panel 表示は救う方が優先 (= 全部 error 返し
// にすると graph が真っ白になる)。
func (i *Inspector) GetQueueInfo(qname string) (*driver.InspectorInfo, error) {
	q := i.driver.queueFor(qname)
	if q == nil {
		return nil, fmt.Errorf("mkqdriver: unknown queue %q", qname)
	}
	counts, err := q.Counts(inspectorCtx())
	if err != nil {
		return nil, fmt.Errorf("mkqdriver: counts %q: %w", qname, err)
	}
	// Repeat ZSET の登録数を Scheduled に加算する (mk-go #455)。
	// mkq scheduler は次回発火を `bull:<queue>:repeat` ZSET にだけ
	// 保持し、concrete delayed job を pre-allocate しない。upstream
	// BullMQ と異なる挙動なので、driver 側で repeat ZCARD を Scheduled
	// に足して admin UI の Delayed カラムに「予定数」として可視化する。
	// ZCARD 失敗は致命ではないので 0 として扱い、queue 全体を error
	// 返却にしない (Counts が成功した時点で UI 表示は救う方を優先)。
	repeatCount, _ := i.driver.rdb.ZCard(inspectorCtx(), i.driver.repeatKey(qname)).Result()

	// delayed bucket を atm で split (#1187、mkq#64 close 時の手法)。
	scheduledCount, retryCount, err := i.partitionDelayedByAttempts(q)
	if err != nil {
		slog.Warn("mkqdriver: failed to split delayed bucket by atm, falling back",
			"queue", qname, "err", err)
		scheduledCount = int(counts.Delayed)
		retryCount = 0
	}

	// Size mirrors asynq.QueueInfo.Size:
	//   "sum of Pending, Active, Scheduled, Retry, Aggregating and Archived"
	// — explicitly excluding Completed (asynq treats stored completed
	// tasks as retention storage, not queue residents). 旧実装と同様、
	// delayed 全体 + failed bucket + repeat を入れる。
	//
	// #1187 で内訳が変わったが合計値は不変: scheduledCount + retryCount ==
	// counts.Delayed (= 同じ delayed bucket を partition しただけ) なので、
	// Size 計算では partition 前の counts.Delayed をそのまま使える。
	return &driver.InspectorInfo{
		Queue:     qname,
		Size:      int(counts.Wait+counts.Active+counts.Delayed+counts.Prioritized+counts.Failed) + int(repeatCount),
		Active:    int(counts.Active),
		Pending:   int(counts.Wait),
		Completed: int(counts.Completed),
		Failed:    int(counts.Failed),
		Scheduled: scheduledCount + int(repeatCount),
		Retry:     retryCount,
	}, nil
}

// partitionDelayedByAttempts inspects mkq's delayed bucket and partitions
// entries into two counts by `atm` (= attemptsMade):
//
//   - atm == 0 → scheduled (cron / 初回 delayed enqueue)
//   - atm > 0  → retry-backoff (= 失敗して次の試行待ち)
//
// 戻り値は (scheduled, retry, err) (#1187)。
//
// delayed bucket は ZSET-backed なので 全件取り直しでも score 順に並ぶ。
// fetch cost は ZRANGE 1 + HGETALL N pipeline で、publisher 3 秒間隔の
// admin polling として許容範囲。
func (i *Inspector) partitionDelayedByAttempts(q *mkq.Queue[framedPayload]) (scheduled int, retry int, err error) {
	listed, err := q.ListJobs(inspectorCtx(), mkq.JobBucketDelayed, 0, -1, true)
	if err != nil {
		return 0, 0, err
	}
	for _, lj := range listed {
		if lj.Job != nil && lj.Job.AttemptsMade > 0 {
			retry++
		} else {
			scheduled++
		}
	}
	return scheduled, retry, nil
}

// QueueMetrics returns BullMQ-compatible per-minute completed /
// failed history for the named queue. mkq's Worker writes these on
// finalise when WithJobMetrics is set (see Server.Start) — without
// it the lists do not exist and mkq.GetMetrics returns a zero-valued
// QueueMetrics, which we map to an empty driver.MetricsResult so
// admin handlers can detect the disabled state.
func (i *Inspector) QueueMetrics(qname, kind string) (*driver.MetricsResult, error) {
	q := i.driver.queueFor(qname)
	if q == nil {
		return nil, fmt.Errorf("mkqdriver: unknown queue %q", qname)
	}
	var bucket mkq.JobBucket
	switch kind {
	case driver.MetricsKindCompleted:
		bucket = mkq.JobBucketCompleted
	case driver.MetricsKindFailed:
		bucket = mkq.JobBucketFailed
	default:
		return nil, fmt.Errorf("mkqdriver: invalid metrics kind %q", kind)
	}
	// LRANGE 0 -1 で全 bucket。Misskey TS フロントは無条件に全件
	// LRANGE してチャート全体を再描画するので、page 化は不要。
	m, err := q.GetMetrics(inspectorCtx(), bucket, 0, -1)
	if err != nil {
		return nil, fmt.Errorf("mkqdriver: get metrics %q/%s: %w", qname, kind, err)
	}
	return &driver.MetricsResult{
		Count: m.Meta.Count,
		Data:  m.Data,
	}, nil
}

// DeleteTask removes a job from the named queue regardless of its
// current bucket. Wraps mkq.RemoveJob; missing jobs return an
// ErrJobNotFound which we translate into a generic error to avoid
// leaking mkq internals to admin handlers.
func (i *Inspector) DeleteTask(qname, taskID string) error {
	q := i.driver.queueFor(qname)
	if q == nil {
		return fmt.Errorf("mkqdriver: unknown queue %q", qname)
	}
	return q.RemoveJob(inspectorCtx(), taskID)
}

// DeleteAllPendingTasks drains the wait bucket. Returns the number of
// jobs that were removed; mkq's DrainPending does not currently report
// the count, so this returns 0 even on success — matching the asynq
// driver's "best-effort count" semantics for callers that only need
// success/failure.
func (i *Inspector) DeleteAllPendingTasks(qname string) (int, error) {
	q := i.driver.queueFor(qname)
	if q == nil {
		return 0, fmt.Errorf("mkqdriver: unknown queue %q", qname)
	}
	if err := q.DrainPending(inspectorCtx()); err != nil {
		return 0, err
	}
	return 0, nil
}

// RunTask re-enqueues a task back into wait state, equivalent to asynq's
// `Inspector.RunTask`. asynq の RunTask は delayed (= scheduled) と retry
// 両方を受け入れるが、mkq では bucket 別に API が分かれている:
//
//   - PromoteJob: delayed → wait 専用 (失敗時 `ErrJobNotInDelayed`)
//   - RetryJob:   failed → wait 専用 (asynq RunTask の後半相当)
//
// よって PromoteJob を先に試し、delayed 状態でなければ RetryJob に
// fallback する。これで「Retry all queues now」(admin/queue/promote-jobs)
// が **delayed bucket (= scheduled + retry-backoff 待ち) と failed bucket
// (= permanent failure)** どちらの job も含めて promote できる (#1181)。
//
// 注意: #1187 以前は「retry-backoff は failed bucket に居る」と説明して
// いたが事実誤認。mkq の delayed bucket が両者を混在させている (#1187 で
// 訂正)。fallback ロジック自体は不変で、両 API を試すので両 bucket を
// 覆える。
//
// 両 path で job が見つからなかった場合は最初の (PromoteJob の) error を
// そのまま返す。job が wait や active など別 bucket に既に居る場合も
// 同様に PromoteJob の error が caller に届く。
//
// semantic 差の注記: mkq の `RetryJob` は default で attempt 数 (`atm` /
// `ats` BullMQ HASH counter) を 0 リセットする (`WithResetAttempts(true)`
// 相当)。asynq の `Inspector.RunTask` は attempt 数を保持するので、
// failed bucket path では mkq 側が「fresh start」semantic で、asynq 側が
// 「resume with same attempts」semantic、という違いがある。admin の
// 「Retry all queues now」の運用としては、operator が手動で promote した
// 時点で fresh start が直感的なので、この semantic 差は意図通り。
func (i *Inspector) RunTask(qname, taskID string) error {
	q := i.driver.queueFor(qname)
	if q == nil {
		return fmt.Errorf("mkqdriver: unknown queue %q", qname)
	}
	err := q.PromoteJob(inspectorCtx(), taskID)
	if errors.Is(err, mkq.ErrJobNotInDelayed) {
		return q.RetryJob(inspectorCtx(), taskID)
	}
	return err
}

// Close is a no-op — the underlying client is owned by the parent
// Driver.
func (i *Inspector) Close() error { return nil }

// ListPendingTasks returns up to pageSize entries from the wait
// bucket starting at the given (1-indexed) page.
func (i *Inspector) ListPendingTasks(qname string, page, pageSize int) ([]*driver.TaskSummary, error) {
	return i.list(qname, mkq.JobBucketWait, page, pageSize)
}

// ListActiveTasks returns up to pageSize active tasks.
func (i *Inspector) ListActiveTasks(qname string, page, pageSize int) ([]*driver.TaskSummary, error) {
	return i.list(qname, mkq.JobBucketActive, page, pageSize)
}

// ListCompletedTasks returns up to pageSize jobs retained in the completed
// bucket (mkq keeps finished jobs per WithKeepCompleted retention).
func (i *Inspector) ListCompletedTasks(qname string, page, pageSize int) ([]*driver.TaskSummary, error) {
	return i.list(qname, mkq.JobBucketCompleted, page, pageSize)
}

// ListFailedTasks returns up to pageSize jobs retained in the failed bucket.
func (i *Inspector) ListFailedTasks(qname string, page, pageSize int) ([]*driver.TaskSummary, error) {
	return i.list(qname, mkq.JobBucketFailed, page, pageSize)
}

// ListScheduledTasks returns up to pageSize tasks scheduled for first-time
// processing (= delayed bucket entries with `atm == 0`). cron / 初回
// delayed enqueue 等が含まれる。retry-backoff 待ち (= `atm > 0`) は
// `ListRetryTasks` 側に出る (#1187)。
func (i *Inspector) ListScheduledTasks(qname string, page, pageSize int) ([]*driver.TaskSummary, error) {
	return i.listDelayedFiltered(qname, false /* want atm == 0 */, page, pageSize)
}

// ListRetryTasks returns up to pageSize tasks waiting for a retry attempt
// (= delayed bucket entries with `atm > 0`). mkq の delayed bucket は
// scheduled (cron 等) と retry-backoff 待ちが混在しており、後者だけを
// 抽出して admin UI の Retry tab に出す (#1187、mkq#64 close 時の手法)。
// permanent failure (= dead letter) は failed bucket に居て本 list には
// 含まれない。
func (i *Inspector) ListRetryTasks(qname string, page, pageSize int) ([]*driver.TaskSummary, error) {
	return i.listDelayedFiltered(qname, true /* want atm > 0 */, page, pageSize)
}

// listDelayedFiltered fetches the delayed bucket in full, filters by
// `atm` (= attemptsMade) according to retry, and applies page/pageSize
// in Go. Pagination is post-filter so callers see consistent counts.
func (i *Inspector) listDelayedFiltered(qname string, retry bool, page, pageSize int) ([]*driver.TaskSummary, error) {
	q := i.driver.queueFor(qname)
	if q == nil {
		return nil, fmt.Errorf("mkqdriver: unknown queue %q", qname)
	}
	if page < 1 {
		page = 1
	}
	if pageSize <= 0 || pageSize > 100 {
		pageSize = 30
	}
	listed, err := q.ListJobs(inspectorCtx(), mkq.JobBucketDelayed, 0, -1, true)
	if err != nil {
		return nil, fmt.Errorf("mkqdriver: list delayed %s: %w", qname, err)
	}
	// filter で集めた後 page/pageSize で slice する。post-filter pagination
	// なので「Retry に 5 件出ているはずだが page 1 が空」のような不整合が
	// 起きない。
	filtered := make([]*driver.TaskSummary, 0, len(listed))
	for _, lj := range listed {
		hasAttempts := lj.Job != nil && lj.Job.AttemptsMade > 0
		if hasAttempts != retry {
			continue
		}
		summary := jobToSummary(qname, string(mkq.JobBucketDelayed), lj.Job, lj.State)
		if summary == nil {
			continue
		}
		filtered = append(filtered, summary)
	}
	start := (page - 1) * pageSize
	if start >= len(filtered) {
		return []*driver.TaskSummary{}, nil
	}
	end := start + pageSize
	if end > len(filtered) {
		end = len(filtered)
	}
	return filtered[start:end], nil
}

// GetTaskInfo returns the full snapshot for a single task. The
// state string is derived from the JobState timestamps because mkq's
// q.Get does not return the bucket directly — see deriveJobState.
func (i *Inspector) GetTaskInfo(qname, taskID string) (*driver.TaskSummary, error) {
	q := i.driver.queueFor(qname)
	if q == nil {
		return nil, fmt.Errorf("mkqdriver: unknown queue %q", qname)
	}
	job, state, err := q.Get(inspectorCtx(), taskID)
	if err != nil {
		if errors.Is(err, mkq.ErrJobNotFound) {
			return nil, err
		}
		return nil, fmt.Errorf("mkqdriver: get job %q: %w", taskID, err)
	}
	return jobToSummary(qname, deriveJobState(state), job, state), nil
}

// deriveJobState maps a *mkq.JobState into a BullMQ bucket-name
// string by inspecting timestamp / error fields. mkq's q.Get does
// not include the bucket key (delayed / wait / prioritized are not
// distinguishable from JobState alone), so the heuristic only
// distinguishes terminal states (failed / completed) from in-flight
// (active) and the catch-all (wait). Admin UI rendering benefits
// most from the failed/completed labels — the wait/delayed
// distinction is rarely actionable from a single-job lookup.
func deriveJobState(st *mkq.JobState) string {
	if st == nil {
		return string(mkq.JobBucketWait)
	}
	switch {
	case !st.FinishedOn.IsZero() && st.FailedReason != "":
		return string(mkq.JobBucketFailed)
	case !st.FinishedOn.IsZero():
		return string(mkq.JobBucketCompleted)
	case !st.ProcessedOn.IsZero():
		return string(mkq.JobBucketActive)
	default:
		return string(mkq.JobBucketWait)
	}
}

// list normalises mkq.ListJobs inputs (1-indexed page/pageSize) into
// 0-indexed [start, end] zranges and decodes the resulting jobs into
// driver.TaskSummary slices.
func (i *Inspector) list(qname string, bucket mkq.JobBucket, page, pageSize int) ([]*driver.TaskSummary, error) {
	q := i.driver.queueFor(qname)
	if q == nil {
		return nil, fmt.Errorf("mkqdriver: unknown queue %q", qname)
	}
	if page < 1 {
		page = 1
	}
	if pageSize <= 0 || pageSize > 100 {
		pageSize = 30
	}
	start := int64((page - 1) * pageSize)
	end := start + int64(pageSize) - 1

	jobs, err := q.ListJobs(inspectorCtx(), bucket, start, end, true)
	if err != nil {
		return nil, fmt.Errorf("mkqdriver: list %s/%s: %w", qname, bucket, err)
	}
	out := make([]*driver.TaskSummary, 0, len(jobs))
	for _, lj := range jobs {
		// jobToSummary returns nil when lj.Job is nil. mkq's current
		// ListJobs contract returns non-nil entries, but the contract
		// is not strict — guard here so callers iterating *TaskSummary
		// never see a nil entry that would NPE downstream.
		summary := jobToSummary(qname, string(bucket), lj.Job, lj.State)
		if summary == nil {
			continue
		}
		out = append(out, summary)
	}
	return out, nil
}

// jobToSummary converts a mkq Job + JobState pair into the driver-
// neutral TaskSummary shape. The Type field is taken from the
// framedPayload wrapper rather than mkq's BullMQ Job.name since the
// latter does not survive scheduled-fire dispatch.
func jobToSummary(queue, state string, job *mkq.Job[framedPayload], st *mkq.JobState) *driver.TaskSummary {
	if job == nil {
		return nil
	}
	body := job.Data.Body
	taskType := job.Data.Type
	if taskType == "" {
		// Fallback for foreign jobs lacking the framing — surface the
		// BullMQ Job.name so admin UIs at least show *something*.
		taskType = job.Name
	}
	s := &driver.TaskSummary{
		ID:         job.ID,
		Queue:      queue,
		Type:       taskType,
		State:      state,
		Payload:    body,
		Retried:    job.AttemptsMade,
		EnqueuedAt: job.Timestamp,
	}
	if st != nil {
		// CompletedAt は asynq driver と揃えて「成功完了したジョブ」
		// のみセット。failed (FailedReason != "") の場合は LastFailedAt
		// 側に出すので、ここでは触らない。admin UI が completedAt 列を
		// 失敗ジョブにも表示してしまうと operator が混乱するため。
		if !st.FinishedOn.IsZero() && st.FailedReason == "" {
			s.CompletedAt = st.FinishedOn
		}
		if st.FailedReason != "" {
			s.LastErr = st.FailedReason
			s.LastFailedAt = st.FinishedOn
		}
		// NextProcessAt は本来「次に処理される時刻」(将来) を意味する
		// asynq.TaskInfo.NextProcessAt のミラー。mkq の JobState は
		// ProcessedOn (過去: 直近のディスパッチ時刻) しか持たないため
		// ここに ProcessedOn を入れると意味的に逆。retry までの
		// 待ち時刻も mkq からは取得できないので zero のまま残す方が
		// admin UI の混乱が少ない。delayed bucket の予定時刻が必要
		// になった場合は別途 ZSET score を読みに行く実装を検討。
	}
	return s
}

// inspectorCtx returns a Background context. The driver.Inspector
// interface does not thread context.Context, but mkq's inspector
// paths talk directly to Redis (no Lua loops, no waits) so the
// underlying Redis client's read/write timeouts already bound the
// call. Adding a per-call context.WithTimeout here would leak
// timers because driver.Inspector methods cannot return a cleanup
// closure to the caller.
func inspectorCtx() context.Context {
	return context.Background()
}
