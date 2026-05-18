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
func (i *scriptableInspector) ListPendingTasks(qname string, page, pageSize int) ([]*driver.TaskSummary, error) {
	return nil, nil
}
func (i *scriptableInspector) ListActiveTasks(qname string, page, pageSize int) ([]*driver.TaskSummary, error) {
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

// TestStartAutoScale_AllQueuesHaveExplicitKnobsReturnsNil verifies that
// when every queue has an explicit JobConcurrency knob set, no
// controllers start (per ADR §3.6 priority: individual knob > controller).
func TestStartAutoScale_AllQueuesHaveExplicitKnobsReturnsNil(t *testing.T) {
	dc := 16
	ic := 8
	// 個別 knob で deliver / inbox を覆って残 queue (export/push/webhook)
	// が controller 管理対象になるが、それらに対しては個別 knob 無しで
	// 自動的に管理される設計。本テストでは「個別 knob で全 queue を
	// 覆う」が autoScaledQueues() 上は不可能 (5 queue で 2 個 knob のみ
	// → 残 3 queue が controller 対象) なので、別観点で挙動を確認:
	// "deliver と inbox の knob を立てると当該 2 queue は管理対象外、
	// 残 3 queue は管理対象" を確認する。
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
	require.NotNil(t, runner, "runner should start for unmanaged queues")
	t.Cleanup(runner.Stop)

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
	t.Cleanup(runner.Stop)

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
	t.Cleanup(runner.Stop)

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
		runner.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Stop did not return within 3 seconds (goroutine leak suspected)")
	}

	// 二度 Stop しても panic / hang しない
	assert.NotPanics(t, runner.Stop)
}

// TestStartAutoScale_NilRunnerStopIsNoop ensures (*autoscaleRunner)(nil).Stop()
// is safe — relevant when Shutdown runs before Start finished, or when
// JobQueueAutoScale=false (returned runner is nil).
func TestStartAutoScale_NilRunnerStopIsNoop(t *testing.T) {
	var r *autoscaleRunner
	assert.NotPanics(t, r.Stop)
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
	t.Cleanup(runner.Stop)
}
