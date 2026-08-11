package queue

import (
	"time"

	"github.com/shiroha-a/mk/internal/queue/driver"
)

// MaintenanceQueueName is the queue used for low-priority maintenance
// jobs (chart tick / resync / clean etc). Kept separate from the
// latency-sensitive deliver queue so a long aggregation does not stall
// federation traffic.
const MaintenanceQueueName = "maintenance"

// Scheduler is the driver-neutral facade over driver.Scheduler. It
// registers cron-style entries once at startup and continuously
// enqueues tasks per their schedule onto the standard worker queues.
type Scheduler struct {
	inner driver.Scheduler
}

// NewScheduler wraps the driver's scheduler.
func NewScheduler(d driver.Driver) *Scheduler {
	return &Scheduler{inner: d.Scheduler()}
}

// RegisterChartJobs registers the 3 chart-related cron jobs.
//
//   - tickCharts : 毎時 55 分 (TS Misskey と同一 cron pattern)
//   - resyncCharts: 毎日 00:00 UTC
//   - cleanCharts : 毎日 00:00 UTC
//
// 重複 enqueue 防止のため Unique TTL を cron 周期と合わせる。前回の
// 同種ジョブが処理中のまま次回 cron が発火しても、TTL 内であれば
// driver が重複 enqueue を弾く。
func (s *Scheduler) RegisterChartJobs() error {
	jobs := []struct {
		cron      string
		taskType  string
		uniqueTTL time.Duration
	}{
		{"55 * * * *", TaskTypeChartTick, 1 * time.Hour},
		{"0 0 * * *", TaskTypeChartResync, 24 * time.Hour},
		{"0 0 * * *", TaskTypeChartClean, 24 * time.Hour},
	}
	for _, j := range jobs {
		if err := s.inner.Register(j.cron, j.taskType, nil,
			driver.WithQueue(MaintenanceQueueName),
			driver.WithMaxRetry(0),
			driver.WithUnique(j.uniqueTTL),
		); err != nil {
			return err
		}
	}
	return nil
}

// RegisterInstanceRefreshJob registers the daily remote-instance metadata
// refresh cron (#393) at 03:00 UTC. The actual walk + fetch is implemented
// by processors.InstanceRefreshProcessor.
func (s *Scheduler) RegisterInstanceRefreshJob() error {
	return s.inner.Register("0 3 * * *", TaskTypeInstanceRefresh, nil,
		driver.WithQueue(MaintenanceQueueName),
		driver.WithMaxRetry(0),
		driver.WithUnique(24*time.Hour),
	)
}

// RegisterRetentionJob registers the daily retention aggregation cron
// (#421) at 00:00 UTC. The actual computation is implemented by
// processors.RetentionAggregateProcessor.
func (s *Scheduler) RegisterRetentionJob() error {
	return s.inner.Register("0 0 * * *", TaskTypeRetentionAggregate, nil,
		driver.WithQueue(MaintenanceQueueName),
		driver.WithMaxRetry(0),
		driver.WithUnique(24*time.Hour),
	)
}

// RegisterCheckExpiredMutingsJob registers the expired-mute prune cron
// (#1563) every 5 minutes, mirroring upstream `checkExpiredMutings`. The
// prune is implemented by processors.CheckExpiredMutingsProcessor.
func (s *Scheduler) RegisterCheckExpiredMutingsJob() error {
	return s.inner.Register("*/5 * * * *", TaskTypeCheckExpiredMutings, nil,
		driver.WithQueue(MaintenanceQueueName),
		driver.WithMaxRetry(0),
		driver.WithUnique(5*time.Minute),
	)
}

// RegisterCleanJob registers the daily generic clean cron (#1563) at 00:00
// UTC, mirroring upstream `clean`. user_ip 90 日 prune / 期限切れ
// role_assignment 削除 / reversi outdated game 削除を processors.CleanProcessor
// が行う。
func (s *Scheduler) RegisterCleanJob() error {
	return s.inner.Register("0 0 * * *", TaskTypeClean, nil,
		driver.WithQueue(MaintenanceQueueName),
		driver.WithMaxRetry(0),
		driver.WithUnique(24*time.Hour),
	)
}

// RegisterChunkedUploadGCJob registers the chunked upload session GC cron
// (#2313) every 15 minutes. 期限切れセッションの multipart upload を abort して
// 行を消す。頻度を上げているのは、オブジェクトストレージが未完了の
// マルチパートアップロードにも課金するため (詳細は TaskTypeChunkedUploadGC)。
func (s *Scheduler) RegisterChunkedUploadGCJob() error {
	return s.inner.Register("*/15 * * * *", TaskTypeChunkedUploadGC, nil,
		driver.WithQueue(MaintenanceQueueName),
		driver.WithMaxRetry(0),
		driver.WithUnique(15*time.Minute),
	)
}

// RegisterPluginJob registers a plugin's cron entry (#2478).
//
// maintenance キューに載せるのは、プラグインの定期処理が配送 (deliver) の
// レイテンシに影響しないようにするため。プラグインの処理時間は mk-go 側から
// 見積もれないので、遅い処理が来ても連合が詰まらない側に置く。
//
// Unique TTL を設けないのは cron 周期が任意だから。周期より長い TTL を勝手に
// 決めると発火を落とすことになる。重複が困る処理はプラグイン側で冪等に書く
// (トランザクションを跨げない制約と同じ理由で、いずれにせよ冪等性は要る)。
func (s *Scheduler) RegisterPluginJob(cron string, taskType string, payload []byte) error {
	return s.inner.Register(cron, taskType, payload,
		driver.WithQueue(MaintenanceQueueName),
		driver.WithMaxRetry(0),
	)
}

// RegisterOrphanUserCleanupJob registers the daily cleanup of relay-derived
// orphan remote users (#2340) at 05:00 UTC.
//
// cleanRemoteNotes (04:00) の後に置くのは、ノートが先に消えた方が「ノート 0 件」
// の条件に合致する行が増えるため。enable gate は processor 側で meta を見るので
// cron は無条件登録する。
func (s *Scheduler) RegisterOrphanUserCleanupJob() error {
	return s.inner.Register("0 5 * * *", TaskTypeOrphanUserCleanup, nil,
		driver.WithQueue(MaintenanceQueueName),
		driver.WithMaxRetry(0),
		driver.WithUnique(24*time.Hour),
	)
}

// RegisterCleanRemoteNotesJob registers the daily remote-note cleanup cron
// (#1563) at 04:00 UTC, mirroring upstream `cleanRemoteNotes`. 旧実装は
// router.go の 6h time.Ticker だったが、TS の systemQueue cron に揃える。
// enable gate は processor 側で meta を見るので cron は無条件登録する。
func (s *Scheduler) RegisterCleanRemoteNotesJob() error {
	return s.inner.Register("0 4 * * *", TaskTypeCleanRemoteNotes, nil,
		driver.WithQueue(MaintenanceQueueName),
		driver.WithMaxRetry(0),
		driver.WithUnique(24*time.Hour),
	)
}

// RegisterCheckModeratorsActivityJob registers the hourly moderator-activity
// cron (#1563) at minute 30, mirroring upstream `checkModeratorsActivity`.
// processors.CheckModeratorsActivityProcessor が inactivity 判定と通知を行う。
// remaining.hours%6 の de-dup が崩れないよう 1h cadence を保つ (ticker 不可)。
func (s *Scheduler) RegisterCheckModeratorsActivityJob() error {
	return s.inner.Register("30 * * * *", TaskTypeCheckModeratorsActivity, nil,
		driver.WithQueue(MaintenanceQueueName),
		driver.WithMaxRetry(0),
		driver.WithUnique(1*time.Hour),
	)
}

// Start launches the scheduler in the background. Returns immediately.
func (s *Scheduler) Start() error { return s.inner.Start() }

// Shutdown stops the scheduler and releases its connection.
func (s *Scheduler) Shutdown() { s.inner.Shutdown() }
