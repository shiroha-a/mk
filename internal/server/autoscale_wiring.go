package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"sync"
	"time"

	"github.com/shiroha-a/mk/internal/config"
	"github.com/shiroha-a/mk/internal/queue"
	"github.com/shiroha-a/mk/internal/queue/autoscale"
	"github.com/shiroha-a/mk/internal/queue/driver"
	queuemetrics "github.com/shiroha-a/mk/internal/queue/metrics"
	"github.com/shiroha-a/mk/internal/queue/runtimestats"
)

// defaultMinWorkers is the per-queue worker lower bound when MinWorkers
// is unset. Matches ADR §2 default.
const defaultMinWorkers = 4

// autoscaleRunner owns the per-queue AIMD controllers + ticker
// goroutines wired by startAutoScale. Shutdown signals every ticker
// to exit (the run goroutines drain via sync.WaitGroup before
// startAutoScale's returned shutdown func returns).
//
// Per ADR §3.2 the design is "one controller per queue + one ticker
// goroutine per queue". maxWorkersGlobal (optional) is enforced
// across goroutines by the shared `r.workerCounts` snapshot updated
// after each Resize.
type autoscaleRunner struct {
	cancel context.CancelFunc
	wg     *sync.WaitGroup

	// workerCounts tracks the **last observed** per-queue worker count
	// so the maxWorkersGlobal cap can be evaluated across queues
	// without an extra Driver.WorkerCount round-trip per check. Updated
	// after each Resize commit.
	mu           sync.Mutex
	workerCounts map[string]int

	// stats records committed scale events so the admin UI can show what
	// the controller actually did (#2277). nil のときは記録しない。
	stats *runtimestats.Recorder
}

// startAutoScale constructs an AIMDController per managed queue, starts
// a 1Hz ticker goroutine for each, and returns a runner whose Stop
// cancels every ticker and waits for them to exit.
//
// A queue is "managed" iff jobQueueAutoScale=true AND that queue has
// no explicit individual concurrency knob set (= deliverJobConcurrency
// / inboxJobConcurrency etc are nil/0). Per ADR §3.6:
//
//	個別 knob > maxWorkers > controller の優先順
//
// Returns nil runner and nil error when no queues are managed (= the
// caller skips ticker startup entirely; opt-in default-off).
//
// Driver compatibility: callers should check driver.Resize against
// driver.ErrResizeNotSupported before invoking this — asynq backend
// cannot honour Resize so wiring auto-scale against it is a config
// error (returned as an error from this function).
func startAutoScale(
	ctx context.Context,
	cfg *config.Config,
	drv driver.Driver,
	metrics *queuemetrics.Metrics,
	stats *runtimestats.Recorder,
) (*autoscaleRunner, error) {
	if !cfg.JobQueueAutoScale {
		return nil, nil
	}

	queues, skipped := autoScaledQueues(cfg)
	if len(queues) == 0 {
		slog.Info("server: autoscale enabled but every queue has an explicit concurrency knob, no controllers started",
			"skipped", skipped)
		return nil, nil
	}

	// asynq などの Resize 非対応 driver で auto-scale を有効化されたら
	// startup で reject (= silent degrade だと operator が 「何故 scale
	// しないのか」で困る)。test 用に noop drive を許可する経路は無い。
	if err := drv.Resize(queues[0], drv.WorkerCount(queues[0])); err != nil &&
		errors.Is(err, driver.ErrResizeNotSupported) {
		return nil, fmt.Errorf(
			"server: jobQueueAutoScale=true requires a driver that supports Resize; current driver returned ErrResizeNotSupported (%w)", err)
	}

	// 変数名は Go 1.21+ builtin min / max を shadow しないよう
	// minWorkers / maxWorkers と書く。
	minWorkers, maxWorkers := resolveAutoScaleBounds(cfg)
	cooldown := time.Second
	if cfg.AutoScaleCooldownSeconds != nil && *cfg.AutoScaleCooldownSeconds > 0 {
		cooldown = time.Duration(*cfg.AutoScaleCooldownSeconds) * time.Second
	}

	// startup validation: minWorkers floor × queue 数 が maxWorkersGlobal を
	// 超えると scaling headroom 無し (ADR §3.6)。
	if cfg.MaxWorkersGlobal != nil && *cfg.MaxWorkersGlobal > 0 {
		if floor := minWorkers * len(queues); floor > *cfg.MaxWorkersGlobal {
			return nil, fmt.Errorf(
				"server: minWorkers floor (min=%d × %d auto-scaled queues = %d) exceeds maxWorkersGlobal=%d",
				minWorkers, len(queues), floor, *cfg.MaxWorkersGlobal)
		}
	}

	runCtx, cancel := context.WithCancel(ctx)
	runner := &autoscaleRunner{
		cancel:       cancel,
		wg:           &sync.WaitGroup{},
		workerCounts: make(map[string]int, len(queues)),
		stats:        stats,
	}

	for _, qname := range queues {
		ctrl, err := autoscale.NewAIMDController(autoscale.AIMDConfig{
			Queue:                 qname,
			MinWorkers:            minWorkers,
			MaxWorkers:            maxWorkers,
			UpThresholdMultiplier: 4.0,
			SustainedIdleCycles:   5,
			CooldownDuration:      cooldown,
		})
		if err != nil {
			cancel()
			return nil, fmt.Errorf("server: build controller for %q: %w", qname, err)
		}

		// 初期 worker 数を最低 minWorkers にする。Resize 失敗時は warn
		// log のみで起動継続 (= driver 未 ready 等で transient、次 tick で
		// retry)。setWorkerCount は Resize 成功時のみ commit して、
		// globalSumWouldExceed に stale data が混入するのを防ぐ (失敗時の
		// initial worker count は次 tick の Resize 成功で正常化する)。
		if err := drv.Resize(qname, minWorkers); err != nil {
			slog.Warn("server: initial Resize failed; controller will retry", "queue", qname, "err", err)
		} else {
			runner.setWorkerCount(qname, drv.WorkerCount(qname))
		}

		runner.wg.Add(1)
		go runner.tick(runCtx, qname, ctrl, drv, metrics, cfg.MaxWorkersGlobal)
	}

	slog.Info("server: autoscale started",
		"managed", queues, "skipped", skipped,
		"min", minWorkers, "max", maxWorkers,
		"cooldownSec", int(cooldown/time.Second),
		"globalCap", maxWorkersGlobalDescription(cfg.MaxWorkersGlobal))
	return runner, nil
}

// Stop cancels all ticker goroutines and waits for them to finish.
// Safe to call multiple times. After Stop returns (or ctx expires), no
// further Resize calls originate from this runner.
//
// ctx serves as the graceful shutdown deadline propagated from
// Server.Shutdown (#764). Ticker goroutines exit at most 1 ticker
// interval after cancel + their current Resize completes (cancel +
// goroutine join). Typical drain < 1 second; ctx allows the caller to
// cap pathological cases without blocking Server.Shutdown indefinitely.
//
// When ctx expires before goroutines exit, leaked goroutines log Warn
// but the function returns; the runtime cleanup is best-effort.
func (r *autoscaleRunner) Stop(ctx context.Context) {
	if r == nil {
		return
	}
	r.cancel()
	done := make(chan struct{})
	go func() {
		r.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		slog.Warn("server: autoscale shutdown deadline exceeded; goroutines may still be running",
			"err", ctx.Err())
	}
}

// tick is the per-queue ticker loop. Each cycle:
//
//  1. observe Queue depth + current worker count
//  2. ask the controller for a decision
//  3. enforce maxWorkersGlobal at the wiring level
//  4. apply via driver.Resize and bump the scale-event metric
//
// Errors from Resize are logged and the cycle continues — transient
// Redis failures should not kill the ticker (next cycle retries).
func (r *autoscaleRunner) tick(
	ctx context.Context,
	qname string,
	ctrl autoscale.Controller,
	drv driver.Driver,
	metrics *queuemetrics.Metrics,
	globalCap *int,
) {
	defer r.wg.Done()
	// panic recovery: ctrl.Observe / drv.Resize / inspector 経由の panic で
	// goroutine が死ぬと当該 queue が controller 管理外になる (server
	// restart まで復旧不能)。stack trace を log に残して goroutine を
	// clean shutdown する (= wg.Done が defer で実行される)。production
	// resilience のため。
	defer func() {
		if v := recover(); v != nil {
			slog.Error("server: autoscale tick panic; controller for queue is now stopped until server restart",
				"queue", qname, "panic", v)
		}
	}()

	// 観測周期 = 1Hz (ADR §3.4 で cool-down と統一)。Ticker は cancel で
	// 即停止できるよう ctx.Done と select する。
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		current := drv.WorkerCount(qname)
		depth := readQueueDepth(drv, qname)

		action := ctrl.Observe(autoscale.ObservedMetric{
			QueueDepth:     depth,
			CurrentWorkers: current,
		})

		if action.Kind == autoscale.ActionNoOp {
			continue
		}

		// maxWorkersGlobal の enforcement: scale-up は「check + 予約 (workerCounts
		// への反映)」を同一ロック区間で atomic に行い、Resize 前に slot を確保する。
		// これで並行 ticker 間の TOCTOU race (check 通過後・Resize 前に他 queue が
		// commit して合計が cap 超過) を防ぎ、in-process では cap を厳格に守る
		// (#1290)。scale-down / cap 無しは超過し得ないので従来どおり反映のみ。
		if action.Kind == autoscale.ActionScaleUp && globalCap != nil && *globalCap > 0 {
			if !r.reserveIfWithinCap(qname, action.TargetWorkers, *globalCap) {
				continue
			}
		} else {
			r.setWorkerCount(qname, action.TargetWorkers)
		}

		if err := drv.Resize(qname, action.TargetWorkers); err != nil {
			slog.Warn("server: autoscale Resize failed", "queue", qname, "target", action.TargetWorkers, "err", err)
			// 予約を rollback。以後の cap 計算が過大予約で scale-up を詰まらせ
			// ないよう、Resize 前の値 (= driver の実 worker 数) に戻す。
			r.setWorkerCount(qname, current)
			continue
		}

		direction := "down"
		if action.Kind == autoscale.ActionScaleUp {
			direction = "up"
		}
		metrics.ScaleEventsTotal.WithLabelValues(qname, direction).Inc()
		// admin UI 用の短期履歴 (#2277)。Prometheus counter は「何回起きたか」
		// しか持たないので、いつ何本から何本へ動いたかはここで保持する。
		if r.stats != nil {
			r.stats.RecordScale(qname, direction, current, action.TargetWorkers)
		}
		slog.Info("server: autoscale resized",
			"queue", qname, "direction", direction,
			"from", current, "to", action.TargetWorkers, "depth", depth)
	}
}

// setWorkerCount snapshots the latest known worker count for qname.
// Called after every successful Resize so globalSumWouldExceed has a
// fresh view across queues.
func (r *autoscaleRunner) setWorkerCount(qname string, n int) {
	r.mu.Lock()
	r.workerCounts[qname] = n
	r.mu.Unlock()
}

// reserveIfWithinCap atomically checks whether committing `target` workers
// for `qname` keeps the sum across all auto-scaled queues within `globalCap`,
// and — when it does — records the reservation in `workerCounts` before
// returning true. The check and the reservation happen under a single lock
// acquisition so two concurrent tickers cannot both pass the cap check on a
// stale snapshot and then each commit a Resize (the prior TOCTOU race, #1290).
// Returns false (caller skips the scale-up) when the cap would be exceeded.
//
// 注 (multi-pod): この reservation は単一プロセス内の ledger なので、複数 pod が
// 同一 Redis に対して独立に autoscale する構成では合計が cap を超え得る。
// strict な cluster-wide cap が必要なら別途 coordinator が要る (将来 issue)。
//
// 引数名は Go builtin `cap` を shadow しないよう globalCap を使う。
func (r *autoscaleRunner) reserveIfWithinCap(qname string, target, globalCap int) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	sum := target
	for q, c := range r.workerCounts {
		if q == qname {
			continue
		}
		sum += c
	}
	if sum > globalCap {
		return false
	}
	r.workerCounts[qname] = target
	return true
}

// autoScaledQueues returns the queue names that should be controller-
// managed (= autoScale enabled AND no explicit per-queue knob set).
// Per ADR §3.6 individual knobs take priority and exclude the queue
// from controller management.
//
// Queue names are sourced from internal/queue constants so any rename /
// addition in queue.go propagates here without silent drift. Skipped
// queues (currently: any queue with an explicit per-queue knob) are
// returned via the second slice for log visibility.
func autoScaledQueues(cfg *config.Config) (managed, skipped []string) {
	if cfg.DeliverJobConcurrency == nil || *cfg.DeliverJobConcurrency == 0 {
		managed = append(managed, queue.QueueName)
	} else {
		skipped = append(skipped, queue.QueueName)
	}
	if cfg.InboxJobConcurrency == nil || *cfg.InboxJobConcurrency == 0 {
		managed = append(managed, queue.InboxQueueName)
	} else {
		skipped = append(skipped, queue.InboxQueueName)
	}
	// export / push / webhook / objectStorage には個別 knob が無いため常に
	// 管理対象。将来 per-queue knob を生やすときはここに分岐を追加する。
	managed = append(managed, queue.ExportQueueName, queue.PushQueueName, queue.WebhookQueueName, queue.ObjectStorageQueueName)
	return managed, skipped
}

// readQueueDepth returns the Inspector-reported Pending count for the
// queue, or 0 on error (= treat unobservable depth as idle so the
// controller does not aggressively scale up on a Redis hiccup).
func readQueueDepth(drv driver.Driver, qname string) int {
	info, err := drv.Inspector().GetQueueInfo(qname)
	if err != nil || info == nil {
		return 0
	}
	return info.Pending
}

// maxWorkersGlobalDescription renders the optional global cap for
// startup log readability.
func maxWorkersGlobalDescription(v *int) string {
	if v == nil || *v <= 0 {
		return "none"
	}
	return fmt.Sprintf("%d", *v)
}

// resolveAutoScaleBounds resolves the effective [min, max] worker bounds from
// config, applying the same defaults the controller uses. Shared with the
// admin runtime view so the UI never disagrees with the controller (#2277).
func resolveAutoScaleBounds(cfg *config.Config) (minWorkers, maxWorkers int) {
	// 変数名は Go 1.21+ builtin min / max を shadow しないよう
	// minWorkers / maxWorkers と書く。
	minWorkers = defaultMinWorkers
	if cfg.MinWorkers != nil && *cfg.MinWorkers > 0 {
		minWorkers = *cfg.MinWorkers
	}
	maxWorkers = runtime.NumCPU() * 16
	if cfg.MaxWorkers != nil && *cfg.MaxWorkers > 0 {
		maxWorkers = *cfg.MaxWorkers
	}
	return minWorkers, maxWorkers
}
