package mkqdriver

import (
	"context"
	"errors"
	"fmt"

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

// Queues returns the set of queues mkq has Define'd against the
// underlying client.
func (i *Inspector) Queues() ([]string, error) {
	return i.driver.client.Queues(inspectorCtx())
}

// GetQueueInfo returns the wait / active / delayed / completed /
// failed counters for the named queue. Pending maps to mkq's wait
// bucket (asynq's pending semantics).
//
// Retry semantics: mkq には asynq の Retry bucket に直接対応する独立 bucket
// が存在せず、retry-backoff 待ちの job は failed bucket に居続け、次の
// retry 時刻になって delayed bucket に移動する設計 (= `ListRetryTasks` も
// failed bucket を読む)。よって driver level では Retry = failed bucket
// size として asynq semantic に揃える。
//
// 副作用として Retry と Failed が同値になるが、これは mkq library が
// 「retry-pending」と「permanent failure」を区別する bucket を持たない
// limitation 由来。frontend (admin/job-queue.vue) は両方とも current size
// を見れば十分なので影響は無い。BullMQ や asynq の semantic と完全一致
// させるには mkq 自体に retry bucket を追加する必要があり、upstream に
// 提案済 (shiroha-a/mkq#64)。それが land すれば本 driver も両 counter を
// 区別して報告できるようになる。
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
	// Size mirrors asynq.QueueInfo.Size:
	//   "sum of Pending, Active, Scheduled, Retry, Aggregating and Archived"
	// — explicitly excluding Completed (asynq treats stored completed
	// tasks as retention storage, not queue residents).
	//
	// mkq → asynq bucket map:
	//   wait        ↔ pending
	//   active      ↔ active
	//   delayed     ↔ scheduled (+ repeat ZSET, mk-go addition)
	//   prioritized ↔ pending (asynq has no prioritized bucket)
	//   failed      ↔ archived (stored failed jobs)
	//
	// completed / paused は asynq Size に含まれないので除外する。
	// 含めると admin UI の queue size が driver 切替で大きく変動する。
	scheduled := int(counts.Delayed) + int(repeatCount)
	return &driver.InspectorInfo{
		Queue:     qname,
		Size:      int(counts.Wait+counts.Active+counts.Delayed+counts.Prioritized+counts.Failed) + int(repeatCount),
		Active:    int(counts.Active),
		Pending:   int(counts.Wait),
		Completed: int(counts.Completed),
		Failed:    int(counts.Failed),
		Scheduled: scheduled,
		// Retry = failed bucket current size。mkq では retry-backoff 待ち
		// が failed bucket に居る ため、stream/queue_stats_publisher の
		// `Delayed = Scheduled + Retry` 計算で正しく retry 待ち分が graph
		// に乗るようになる。Failed と同値になる semantic limitation は
		// 関数 doc 冒頭で説明済 (#1181)。
		Retry: int(counts.Failed),
	}, nil
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
// が failed bucket に居る retry-backoff 待ち job も含めて promote できる
// ようになる (#1181)。
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

// ListScheduledTasks returns up to pageSize delayed (scheduled) tasks.
func (i *Inspector) ListScheduledTasks(qname string, page, pageSize int) ([]*driver.TaskSummary, error) {
	return i.list(qname, mkq.JobBucketDelayed, page, pageSize)
}

// ListRetryTasks returns up to pageSize tasks waiting to be retried.
// mkq keeps retries inside the failed bucket; expose those here so
// admin UIs can show "retry" as a separate tab rather than empty.
func (i *Inspector) ListRetryTasks(qname string, page, pageSize int) ([]*driver.TaskSummary, error) {
	return i.list(qname, mkq.JobBucketFailed, page, pageSize)
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
