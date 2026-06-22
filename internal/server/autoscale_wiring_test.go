package server

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shiroha-a/mk/internal/config"
	"github.com/shiroha-a/mk/internal/queue/driver"
	queuemetrics "github.com/shiroha-a/mk/internal/queue/metrics"
)

// scriptableDriver is a driver.Driver fake whose WorkerCount /
// Inspector.GetQueueInfo return canned values; Resize records calls
// and updates the worker map atomically so the controller's view
// stays consistent. Other Driver methods panic; tests should not
// exercise them.
type scriptableDriver struct {
	mu        sync.Mutex
	workers   map[string]int
	pending   map[string]int
	resizeErr error // if non-nil, Resize returns this for every call
	resizes   atomic.Int32

	insp *scriptableInspector
}

func newScriptableDriver(initial map[string]int) *scriptableDriver {
	workers := map[string]int{}
	for k, v := range initial {
		workers[k] = v
	}
	d := &scriptableDriver{
		workers: workers,
		pending: map[string]int{},
	}
	d.insp = &scriptableInspector{parent: d}
	return d
}

func (d *scriptableDriver) Client() driver.Client {
	panic("scriptableDriver: Client() not implemented")
}
func (d *scriptableDriver) Server() driver.Server {
	panic("scriptableDriver: Server() not implemented")
}
func (d *scriptableDriver) Inspector() driver.Inspector { return d.insp }
func (d *scriptableDriver) Scheduler() driver.Scheduler {
	panic("scriptableDriver: Scheduler() not implemented")
}
func (d *scriptableDriver) Close() error { return nil }
func (d *scriptableDriver) WorkerCount(qname string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.workers[qname]
}
func (d *scriptableDriver) Resize(qname string, n int) error {
	d.resizes.Add(1)
	if d.resizeErr != nil {
		return d.resizeErr
	}
	d.mu.Lock()
	d.workers[qname] = n
	d.mu.Unlock()
	return nil
}
func (d *scriptableDriver) setPending(qname string, n int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.pending[qname] = n
}

type scriptableInspector struct {
	parent *scriptableDriver
}

func (i *scriptableInspector) Queues() ([]string, error) { return nil, nil }
func (i *scriptableInspector) GetQueueInfo(qname string) (*driver.InspectorInfo, error) {
	i.parent.mu.Lock()
	defer i.parent.mu.Unlock()
	return &driver.InspectorInfo{Queue: qname, Pending: i.parent.pending[qname]}, nil
}
func (i *scriptableInspector) DeleteTask(qname, taskID string) error { return nil }
func (i *scriptableInspector) DeleteAllPendingTasks(qname string) (int, error) {
	return 0, nil
}
func (i *scriptableInspector) RunTask(qname, taskID string) error { return nil }
func (i *scriptableInspector) PauseQueue(qname string) error      { return nil }
func (i *scriptableInspector) UnpauseQueue(qname string) error    { return nil }
func (i *scriptableInspector) ListPendingTasks(qname string, page, pageSize int) ([]*driver.TaskSummary, error) {
	return nil, nil
}
func (i *scriptableInspector) ListActiveTasks(qname string, page, pageSize int) ([]*driver.TaskSummary, error) {
	return nil, nil
}
func (i *scriptableInspector) ListCompletedTasks(qname string, page, pageSize int) ([]*driver.TaskSummary, error) {
	return nil, nil
}
func (i *scriptableInspector) ListFailedTasks(qname string, page, pageSize int) ([]*driver.TaskSummary, error) {
	return nil, nil
}
func (i *scriptableInspector) ListScheduledTasks(qname string, page, pageSize int) ([]*driver.TaskSummary, error) {
	return nil, nil
}
func (i *scriptableInspector) ListRetryTasks(qname string, page, pageSize int) ([]*driver.TaskSummary, error) {
	return nil, nil
}
func (i *scriptableInspector) GetTaskInfo(qname, taskID string) (*driver.TaskSummary, error) {
	return nil, nil
}
func (i *scriptableInspector) QueueMetrics(qname, kind string) (*driver.MetricsResult, error) {
	return nil, nil
}
func (i *scriptableInspector) Close() error { return nil }

// TestStartAutoScale_DisabledReturnsNil covers the no-op path: auto-scale
// flag false → runner nil, no goroutine, no Resize call.
func TestStartAutoScale_DisabledReturnsNil(t *testing.T) {
	cfg := &config.Config{JobQueueAutoScale: false}
	d := newScriptableDriver(nil)

	runner, err := startAutoScale(context.Background(), cfg, d, queuemetrics.New())
	require.NoError(t, err)
	assert.Nil(t, runner, "runner should be nil when auto-scale is off")
	assert.Equal(t, int32(0), d.resizes.Load(), "no Resize call when auto-scale is off")
}

// TestStartAutoScale_DeliverInboxKnobsOnly_OthersStillManaged verifies
// that when deliver / inbox have explicit JobConcurrency knobs set,
// they are excluded from controller management (per ADR §3.6 priority)
// while export / push / webhook continue to be managed (those queues
// have no per-queue knob in the current config schema, so they are
// always managed when jobQueueAutoScale=true).
//
// 注: 現状の config schema では「全 queue を knob で覆う」は不可能なため、
// "all knobs → nil runner" の経路は存在しない。将来 export/push/webhook
// に per-queue knob を生やしたら本テストを延長 / 別 test 化する。
func TestStartAutoScale_DeliverInboxKnobsOnly_OthersStillManaged(t *testing.T) {
	dc := 16
	ic := 8
	cfg := &config.Config{
		JobQueueAutoScale:     true,
		DeliverJobConcurrency: &dc,
		InboxJobConcurrency:   &ic,
	}
	d := newScriptableDriver(map[string]int{
		"deliver": dc, "inbox": ic, "export": 0, "push": 0, "webhook": 0,
	})

	runner, err := startAutoScale(context.Background(), cfg, d, queuemetrics.New())
	require.NoError(t, err)
	require.NotNil(t, runner, "runner should start for unmanaged queues (export/push/webhook)")
	t.Cleanup(func() { runner.Stop(context.Background()) })

	// export / push / webhook の 3 queue が controller 管理対象 → 初期
	// Resize で minWorkers=4 にされる。deliver / inbox は touch されない。
	deadline := time.After(2 * time.Second)
	for d.WorkerCount("export") != 4 {
		select {
		case <-deadline:
			t.Fatalf("export never reached min=4 (got %d)", d.WorkerCount("export"))
		case <-time.After(10 * time.Millisecond):
		}
	}
	assert.Equal(t, 4, d.WorkerCount("export"))
	assert.Equal(t, 4, d.WorkerCount("push"))
	assert.Equal(t, 4, d.WorkerCount("webhook"))
	assert.Equal(t, dc, d.WorkerCount("deliver"), "deliver should retain explicit knob value")
	assert.Equal(t, ic, d.WorkerCount("inbox"), "inbox should retain explicit knob value")
}

// TestStartAutoScale_RejectsAsynqDriver verifies that auto-scale + a
// driver that returns ErrResizeNotSupported → startup error (= asynq).
func TestStartAutoScale_RejectsAsynqDriver(t *testing.T) {
	cfg := &config.Config{JobQueueAutoScale: true}
	d := newScriptableDriver(nil)
	d.resizeErr = driver.ErrResizeNotSupported

	_, err := startAutoScale(context.Background(), cfg, d, queuemetrics.New())
	require.Error(t, err)
	assert.ErrorIs(t, err, driver.ErrResizeNotSupported)
}

// TestStartAutoScale_MinWorkersFloorExceedsGlobalCapRejected verifies the
// ADR §3.6 validation: min × len(queues) must fit inside maxWorkersGlobal.
func TestStartAutoScale_MinWorkersFloorExceedsGlobalCapRejected(t *testing.T) {
	min := 10
	cap := 30 // 5 queue × min=10 = 50 > 30
	cfg := &config.Config{
		JobQueueAutoScale: true,
		MinWorkers:        &min,
		MaxWorkersGlobal:  &cap,
	}
	d := newScriptableDriver(nil)

	_, err := startAutoScale(context.Background(), cfg, d, queuemetrics.New())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds maxWorkersGlobal")
}

// TestStartAutoScale_TickerScalesUpOnBacklog verifies the end-to-end
// ticker loop: high queue depth → controller decides scale-up → driver
// resized → metric incremented.
func TestStartAutoScale_TickerScalesUpOnBacklog(t *testing.T) {
	// minWorkers=2 / maxWorkers=64 / cooldown=1s (default) で起動。
	min := 2
	maxW := 64
	cooldown := 1
	cfg := &config.Config{
		JobQueueAutoScale:        true,
		MinWorkers:               &min,
		MaxWorkers:               &maxW,
		AutoScaleCooldownSeconds: &cooldown,
	}
	d := newScriptableDriver(nil)
	m := queuemetrics.New()

	runner, err := startAutoScale(context.Background(), cfg, d, m)
	require.NoError(t, err)
	require.NotNil(t, runner)
	t.Cleanup(func() { runner.Stop(context.Background()) })

	// Initial Resize で全 queue が min=2 になっている。
	deadline := time.After(2 * time.Second)
	for d.WorkerCount("deliver") != 2 {
		select {
		case <-deadline:
			t.Fatalf("deliver never reached min=2")
		case <-time.After(10 * time.Millisecond):
		}
	}

	// Queue depth を高くする (2 × 4 = 8 越えで scale-up trigger)。
	d.setPending("deliver", 100)

	// Ticker は 1Hz、最低 1 cycle 待って scale-up を観測。
	deadline = time.After(5 * time.Second)
	for d.WorkerCount("deliver") <= 2 {
		select {
		case <-deadline:
			t.Fatalf("deliver never scaled up (got %d)", d.WorkerCount("deliver"))
		case <-time.After(50 * time.Millisecond):
		}
	}
	// scale-up event counter が立った
	assert.Greater(t, testutil.ToFloat64(m.ScaleEventsTotal.WithLabelValues("deliver", "up")), 0.0,
		"scale-up metric should be incremented")
}

// TestStartAutoScale_GlobalCapBlocksScaleUp verifies that
// maxWorkersGlobal limits aggregate worker count even when individual
// queues' upThreshold is exceeded.
func TestStartAutoScale_GlobalCapBlocksScaleUp(t *testing.T) {
	min := 2
	maxW := 64
	// 5 queue × min=2 = 10、cap = 12 → 残 headroom = 2 だけ。
	cap := 12
	cooldown := 1
	cfg := &config.Config{
		JobQueueAutoScale:        true,
		MinWorkers:               &min,
		MaxWorkers:               &maxW,
		MaxWorkersGlobal:         &cap,
		AutoScaleCooldownSeconds: &cooldown,
	}
	d := newScriptableDriver(nil)
	m := queuemetrics.New()

	runner, err := startAutoScale(context.Background(), cfg, d, m)
	require.NoError(t, err)
	require.NotNil(t, runner)
	t.Cleanup(func() { runner.Stop(context.Background()) })

	// すべての queue を高 depth に → ticker が全 queue でスケールしたがる
	for _, q := range []string{"deliver", "inbox", "export", "push", "webhook"} {
		d.setPending(q, 100)
	}

	// しばらく走らせて、合計 worker が cap=12 を超えないことを assert。
	time.Sleep(3 * time.Second)
	total := 0
	for _, q := range []string{"deliver", "inbox", "export", "push", "webhook"} {
		total += d.WorkerCount(q)
	}
	assert.LessOrEqual(t, total, cap, "total workers must stay within maxWorkersGlobal=%d (got %d)", cap, total)
}

// TestAutoscaleRunner_Stop verifies that Stop blocks until every
// ticker goroutine has exited (= no goroutine leak).
func TestAutoscaleRunner_Stop(t *testing.T) {
	cfg := &config.Config{JobQueueAutoScale: true}
	d := newScriptableDriver(nil)

	runner, err := startAutoScale(context.Background(), cfg, d, queuemetrics.New())
	require.NoError(t, err)
	require.NotNil(t, runner)

	done := make(chan struct{})
	go func() {
		runner.Stop(context.Background())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Stop did not return within 3 seconds (goroutine leak suspected)")
	}

	// 二度 Stop しても panic / hang しない
	assert.NotPanics(t, func() { runner.Stop(context.Background()) })
}

// TestAutoscaleRunner_Stop_RespectsDeadline verifies that Stop returns
// when the supplied ctx expires, even if internal goroutines are slow
// to exit. Uses a 1ns-deadline ctx to force the timeout path.
func TestAutoscaleRunner_Stop_RespectsDeadline(t *testing.T) {
	cfg := &config.Config{JobQueueAutoScale: true}
	d := newScriptableDriver(nil)

	runner, err := startAutoScale(context.Background(), cfg, d, queuemetrics.New())
	require.NoError(t, err)
	require.NotNil(t, runner)

	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	start := time.Now()
	runner.Stop(ctx)
	elapsed := time.Since(start)
	// 即座に return (= ctx.Done 経路) すること。goroutine join 待ち
	// (= 1Hz ticker) を待たないので 100ms 以下。
	assert.Less(t, elapsed, 100*time.Millisecond,
		"Stop should return immediately on ctx deadline (elapsed=%v)", elapsed)

	// Clean up the leaked goroutines so the test process exits cleanly.
	runner.Stop(context.Background())
}

// TestStartAutoScale_NilRunnerStopIsNoop ensures (*autoscaleRunner)(nil).Stop()
// is safe — relevant when Shutdown runs before Start finished, or when
// JobQueueAutoScale=false (returned runner is nil).
func TestStartAutoScale_NilRunnerStopIsNoop(t *testing.T) {
	var r *autoscaleRunner
	assert.NotPanics(t, func() { r.Stop(context.Background()) })
}

// panickingDriver wraps scriptableDriver and panics from Resize after
// `panicAfter` calls so we can test the tick goroutine's recover().
type panickingDriver struct {
	*scriptableDriver
	panicAfter atomic.Int32
}

func (p *panickingDriver) Resize(qname string, n int) error {
	if p.panicAfter.Add(-1) < 0 {
		panic("test-induced resize panic")
	}
	return p.scriptableDriver.Resize(qname, n)
}

// TestStartAutoScale_TickRecoversFromPanic verifies that a panic from
// drv.Resize inside the per-queue tick goroutine is recovered (logged
// at Error level) rather than crashing the process. wg.Done still runs
// via defer so Stop() does not hang waiting for the leaked goroutine.
func TestStartAutoScale_TickRecoversFromPanic(t *testing.T) {
	cfg := &config.Config{JobQueueAutoScale: true}
	inner := newScriptableDriver(nil)
	inner.setPending("deliver", 1000) // 高 depth で scale-up trigger
	d := &panickingDriver{scriptableDriver: inner}
	// 初回 + 1 tick で発火する Resize までは success、その次の Resize で panic。
	// 初期 Resize × 5 queue + 1 tick の Resize で 6 = panic 発火タイミング。
	d.panicAfter.Store(6)

	runner, err := startAutoScale(context.Background(), cfg, d, queuemetrics.New())
	require.NoError(t, err)
	require.NotNil(t, runner)

	// panic で deliver の tick goroutine が死ぬまで待ち、Stop が hang
	// しない (= deferred wg.Done が recover 経由でも走る) ことを確認。
	time.Sleep(2 * time.Second)

	done := make(chan struct{})
	go func() {
		runner.Stop(context.Background())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Stop hung after panic; wg.Done likely missed in recover path")
	}
}

// TestStartAutoScale_InitialResizeFailureDoesNotPreventStart verifies
// that a transient Resize failure during initial setup is logged but
// allowed (controller retries on next tick), so a temporary Redis
// hiccup at startup does not block server boot.
func TestStartAutoScale_InitialResizeFailureDoesNotPreventStart(t *testing.T) {
	cfg := &config.Config{JobQueueAutoScale: true}
	d := newScriptableDriver(nil)
	// First few Resize calls fail; later ones succeed (= transient failure).
	// scriptableDriver.resizeErr is sticky, so we test the simpler case:
	// permanent failure — wiring must still return without panicking.
	d.resizeErr = errors.New("temporary redis hiccup")
	defer func() { d.resizeErr = nil }()

	runner, err := startAutoScale(context.Background(), cfg, d, queuemetrics.New())
	// startup-time validation passes (Resize is called but failure is logged
	// and tolerated); only ErrResizeNotSupported triggers startup rejection.
	require.NoError(t, err)
	require.NotNil(t, runner)
	t.Cleanup(func() { runner.Stop(context.Background()) })
}
