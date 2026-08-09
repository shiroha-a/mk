package mkqdriver

import (
	"context"
	"fmt"

	"github.com/shiroha-a/mk/internal/queue/driver"
)

// Scheduler implements driver.Scheduler over mkq's
// UpsertSchedulePattern API. mkq stores schedule registrations as a
// per-queue ZSET; subsequent Register calls with the same scheduleID
// idempotently replace the existing entry (matches asynq.Scheduler's
// "register-once" semantics).
//
// Limitations:
//
//   - mkq's scheduled-fire job inherits its BullMQ Job.name from the
//     queue name, not the scheduled task type. Server.dispatchHandler
//     reads framedPayload.Type instead of Job.name to work around
//     that — admin UI listings still show the queue name in the
//     Job.name column for scheduled fires.
//
//   - mkq's ScheduleOption set (Limit / StartDate / EndDate / TZ /
//     Immediately) does NOT cover per-fire job options like
//     attempts / unique, so driver.WithMaxRetry / driver.WithUnique /
//     driver.WithProcessIn passed to Register are dropped. **これは
//     現状の呼び出し方では実害が無い** (#2405):
//
//     WithUnique が防ぎたい「同じ cron tick の二重実行」は、mkq が
//     発火 job に決定的な ID (`repeat:<scheduleID>:<nextMillis>`) を
//     振ることで構造的に防がれている。updateJobScheduler-12.lua が
//     `EXISTS` で弾き、重複時は `duplicated` イベントを記録する。
//     さらに `producerId == currentDelayedJobId` の判定により、直前の
//     発火を処理した worker だけが次を積める。
//
//     WithMaxRetry は mk-go の cron が全て 0 (リトライ無し) を渡して
//     おり、mkq の既定 MaxAttempts も 0 なので結果が同じ。
//     WithProcessIn は現在どの cron も渡していない。
//
//     したがって「この driver では cron が壊れる」ことは無い。将来
//     リトライさせたい cron や、mkq の dedup が効かない粒度で dedup
//     したい cron が出てきたら、その時点で bridge を検討する。
type Scheduler struct {
	driver *Driver
}

// Register schedules taskType to fire on the given cron pattern. The
// scheduleID is taken from taskType so re-registering the same task
// at a different cron replaces (rather than duplicates) the entry.
//
// driver.WithMaxRetry, driver.WithUnique, and driver.WithProcessIn
// are accepted (so the mkqdriver and asynqdriver share a call site)
// but are NOT honoured — see the Scheduler doc-comment for why that is
// currently harmless.
//
// 以前はこれらが渡されるたびに起動時 warning を出していたが、実害が無い
// のに毎起動 11 件出て本物の警告を埋めるため落とした (#2405)。
func (s *Scheduler) Register(cronspec, taskType string, payload []byte, opts ...driver.EnqueueOption) error {
	o := driver.ApplyEnqueueOptions(opts)
	if o.Queue == "" {
		return fmt.Errorf("mkqdriver: Scheduler.Register requires WithQueue (taskType=%q)", taskType)
	}
	q := s.driver.queueFor(o.Queue)
	if q == nil {
		return fmt.Errorf("mkqdriver: unknown queue %q (taskType=%q)", o.Queue, taskType)
	}
	framed := framedPayload{Type: taskType, Body: payload}
	return q.UpsertSchedulePattern(context.Background(), taskType, cronspec, framed)
}

// Start is a no-op for mkq — schedules are evaluated lazily by the
// Worker dispatch loop on every prefetch. The method exists to
// satisfy the driver.Scheduler interface.
func (s *Scheduler) Start() error { return nil }

// Shutdown is a no-op for mkq — scheduled entries are persisted in
// Redis and survive driver restarts; nothing to release on the
// scheduler side.
func (s *Scheduler) Shutdown() {}
