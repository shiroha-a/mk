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
	"github.com/shiroha-a/mk/internal/queue/autoscale"
	"github.com/shiroha-a/mk/internal/queue/driver"
	queuemetrics "github.com/shiroha-a/mk/internal/queue/metrics"
)

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
) (*autoscaleRunner, error) {
	if !cfg.JobQueueAutoScale {
		return nil, nil
	}

	queues := autoScaledQueues(cfg)
	if len(queues) == 0 {
		slog.Info("server: autoscale enabled but every queue has an explicit concurrency knob, no controllers started")
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

	min := defaultMinWorkers
	if cfg.MinWorkers != nil && *cfg.MinWorkers > 0 {
		min = *cfg.MinWorkers
	}
	maxW := runtime.NumCPU() * 16
	if cfg.MaxWorkers != nil && *cfg.MaxWorkers > 0 {
		maxW = *cfg.MaxWorkers
	}
	cooldown := time.Second
	if cfg.AutoScaleCooldownSeconds != nil && *cfg.AutoScaleCooldownSeconds > 0 {
		cooldown = time.Duration(*cfg.AutoScaleCooldownSeconds) * time.Second
	}

	// startup validation: minWorkers floor × queue 数 が maxWorkersGlobal を
	// 超えると scaling headroom 無し (ADR §3.6)。
	if cfg.MaxWorkersGlobal != nil && *cfg.MaxWorkersGlobal > 0 {
		if floor := min * len(queues); floor > *cfg.MaxWorkersGlobal {
			return nil, fmt.Errorf(
				"server: minWorkers floor (min=%d × %d auto-scaled queues = %d) exceeds maxWorkersGlobal=%d",
				min, len(queues), floor, *cfg.MaxWorkersGlobal)
		}
	}

	runCtx, cancel := context.WithCancel(ctx)
	runner := &autoscaleRunner{
		cancel:       cancel,
		wg:           &sync.WaitGroup{},
		workerCounts: make(map[string]int, len(queues)),
	}

	for _, qname := range queues {
		ctrl, err := autoscale.NewAIMDController(autoscale.AIMDConfig{
			Queue:                 qname,
			MinWorkers:            min,
			MaxWorkers:            maxW,
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
		// retry)。
		if err := drv.Resize(qname, min); err != nil {
			slog.Warn("server: initial Resize failed; controller will retry", "queue", qname, "err", err)
		}
		runner.setWorkerCount(qname, drv.WorkerCount(qname))

		runner.wg.Add(1)
		go runner.tick(runCtx, qname, ctrl, drv, metrics, cfg.MaxWorkersGlobal)
	}

	slog.Info("server: autoscale started",
		"queues", queues, "min", min, "max", maxW,
		"cooldownSec", int(cooldown/time.Second),
		"globalCap", maxWorkersGlobalDescription(cfg.MaxWorkersGlobal))
	return runner, nil
}

// Stop cancels all ticker goroutines and waits for them to finish.
// Safe to call multiple times. After Stop returns, no further Resize
// calls originate from this runner.
func (r *autoscaleRunner) Stop() {
	if r == nil {
		return
	}
	r.cancel()
	r.wg.Wait()
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

		// maxWorkersGlobal の enforcement: scale-up なら現他 queue の合計
		// と合わせて cap を超えないか check。super 過ぎるなら NoOp に降格。
		if action.Kind == autoscale.ActionScaleUp && globalCap != nil && *globalCap > 0 {
			if r.globalSumWouldExceed(qname, action.TargetWorkers, *globalCap) {
				continue
			}
		}

		if err := drv.Resize(qname, action.TargetWorkers); err != nil {
			slog.Warn("server: autoscale Resize failed", "queue", qname, "target", action.TargetWorkers, "err", err)
			continue
		}
		r.setWorkerCount(qname, action.TargetWorkers)

		direction := "down"
		if action.Kind == autoscale.ActionScaleUp {
			direction = "up"
		}
		metrics.ScaleEventsTotal.WithLabelValues(qname, direction).Inc()
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

// globalSumWouldExceed returns true when committing `target` workers for
// `qname` would push the sum across all auto-scaled queues over cap.
func (r *autoscaleRunner) globalSumWouldExceed(qname string, target, cap int) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	sum := target
	for q, c := range r.workerCounts {
		if q == qname {
			continue
		}
		sum += c
	}
	return sum > cap
}

// autoScaledQueues returns the queue names that should be controller-
// managed (= autoScale enabled AND no explicit per-queue knob set).
// Per ADR §3.6 individual knobs take priority and exclude the queue
// from controller management.
//
// 5 queue names match internal/queue/queue.go constants. Hard-coding
// avoids an import cycle (server depends on queue/metrics already; we
// could import queue too, but the constant set is small and ADR fixes
// it).
func autoScaledQueues(cfg *config.Config) []string {
	const (
		deliver = "deliver"
		inbox   = "inbox"
		export  = "export"
		push    = "push"
		webhook = "webhook"
	)
	out := []string{}
	if cfg.DeliverJobConcurrency == nil || *cfg.DeliverJobConcurrency == 0 {
		out = append(out, deliver)
	}
	if cfg.InboxJobConcurrency == nil || *cfg.InboxJobConcurrency == 0 {
		out = append(out, inbox)
	}
	// export / push / webhook には個別 knob が無いため常に管理対象。
	out = append(out, export, push, webhook)
	return out
}

const defaultMinWorkers = 4

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
