package metrics

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shiroha-a/mk/internal/queue/driver"
)

// fakeDriver is a minimal driver.Driver implementation that returns
// canned values for WorkerCount and routes Inspector calls to a backing
// fakeInspector. Other Driver methods panic; tests should not exercise them.
type fakeDriver struct {
	workers   map[string]int
	inspector *fakeInspector
}

func (f *fakeDriver) Client() driver.Client       { panic("not used") }
func (f *fakeDriver) Server() driver.Server       { panic("not used") }
func (f *fakeDriver) Inspector() driver.Inspector { return f.inspector }
func (f *fakeDriver) Scheduler() driver.Scheduler { panic("not used") }
func (f *fakeDriver) Close() error                { return nil }
func (f *fakeDriver) WorkerCount(qname string) int {
	if v, ok := f.workers[qname]; ok {
		return v
	}
	return 0
}

type fakeInspector struct {
	pendingByQueue map[string]int
	errByQueue     map[string]error
}

func (i *fakeInspector) Queues() ([]string, error) { return nil, nil }
func (i *fakeInspector) GetQueueInfo(qname string) (*driver.InspectorInfo, error) {
	if err, ok := i.errByQueue[qname]; ok {
		return nil, err
	}
	pending, ok := i.pendingByQueue[qname]
	if !ok {
		return &driver.InspectorInfo{Queue: qname}, nil
	}
	return &driver.InspectorInfo{Queue: qname, Pending: pending}, nil
}
func (i *fakeInspector) DeleteTask(qname, taskID string) error           { return nil }
func (i *fakeInspector) DeleteAllPendingTasks(qname string) (int, error) { return 0, nil }
func (i *fakeInspector) RunTask(qname, taskID string) error              { return nil }
func (i *fakeInspector) ListPendingTasks(qname string, page, pageSize int) ([]*driver.TaskSummary, error) {
	return nil, nil
}
func (i *fakeInspector) ListActiveTasks(qname string, page, pageSize int) ([]*driver.TaskSummary, error) {
	return nil, nil
}
func (i *fakeInspector) ListScheduledTasks(qname string, page, pageSize int) ([]*driver.TaskSummary, error) {
	return nil, nil
}
func (i *fakeInspector) ListRetryTasks(qname string, page, pageSize int) ([]*driver.TaskSummary, error) {
	return nil, nil
}
func (i *fakeInspector) GetTaskInfo(qname, taskID string) (*driver.TaskSummary, error) {
	return nil, nil
}
func (i *fakeInspector) QueueMetrics(qname, kind string) (*driver.MetricsResult, error) {
	return nil, nil
}
func (i *fakeInspector) Close() error { return nil }

// TestNew_PreInitializesZeroSeries verifies that every standard queue label
// has a zero-valued series after New (for push-mode metrics), so Prometheus
// graphs don't show "no data" gaps before the first observation. The pull-
// mode metrics (workers_active / queue_pending) are emitted by Collect at
// scrape time so they don't need pre-init here.
func TestNew_PreInitializesZeroSeries(t *testing.T) {
	m := New()
	for _, q := range standardQueues {
		assert.Equal(t, 0.0, testutil.ToFloat64(m.ScaleEventsTotal.WithLabelValues(q, "up")))
		assert.Equal(t, 0.0, testutil.ToFloat64(m.ScaleEventsTotal.WithLabelValues(q, "down")))
	}
}

// TestRegister_AttachesAllCollectors verifies that Register succeeds against
// a fresh Registry and registering twice fails (Prometheus contract).
func TestRegister_AttachesAllCollectors(t *testing.T) {
	r := prometheus.NewRegistry()
	m := New()
	require.NoError(t, m.Register(r))

	// 2 度目の登録は AlreadyRegisteredError を返す。
	err := m.Register(r)
	require.Error(t, err)
	var already prometheus.AlreadyRegisteredError
	assert.True(t, errors.As(err, &already), "expected AlreadyRegisteredError, got %T", err)
}

// TestBindDriver_CollectsAtScrapeTime verifies that workers_active and
// queue_pending values are read from the driver each scrape (= reflect
// driver state at /metrics call time, not at BindDriver time).
func TestBindDriver_CollectsAtScrapeTime(t *testing.T) {
	d := &fakeDriver{
		workers: map[string]int{"deliver": 32, "inbox": 16},
		inspector: &fakeInspector{
			pendingByQueue: map[string]int{"deliver": 100, "inbox": 5},
		},
	}

	m := New()
	m.BindDriver(d)
	r := prometheus.NewRegistry()
	require.NoError(t, m.Register(r))

	body := scrape(t, r)
	assert.Contains(t, body, `mk_job_workers_active{queue="deliver"} 32`)
	assert.Contains(t, body, `mk_job_workers_active{queue="inbox"} 16`)
	assert.Contains(t, body, `mk_job_queue_pending{queue="deliver"} 100`)
	assert.Contains(t, body, `mk_job_queue_pending{queue="inbox"} 5`)

	// Driver の WorkerCount を後から変更 → 再 scrape で反映される。
	d.workers["deliver"] = 64
	body = scrape(t, r)
	assert.Contains(t, body, `mk_job_workers_active{queue="deliver"} 64`)
}

// TestBindDriver_NilIsNoOp verifies that BindDriver(nil) does not panic and
// leaves the pull collector unset (= /metrics still works but omits
// workers_active / queue_pending series).
func TestBindDriver_NilIsNoOp(t *testing.T) {
	m := New()
	m.BindDriver(nil)
	assert.Nil(t, m.pullCollector)
}

// TestBindDriver_InspectorErrorReturnsZero verifies that scrape time errors
// from Inspector.GetQueueInfo are swallowed and reported as 0, so a transient
// Redis hiccup does not break the entire /metrics response.
func TestBindDriver_InspectorErrorReturnsZero(t *testing.T) {
	d := &fakeDriver{
		workers: map[string]int{"deliver": 8},
		inspector: &fakeInspector{
			errByQueue: map[string]error{"deliver": errors.New("redis timeout")},
		},
	}
	m := New()
	m.BindDriver(d)
	r := prometheus.NewRegistry()
	require.NoError(t, m.Register(r))

	body := scrape(t, r)
	assert.Contains(t, body, `mk_job_workers_active{queue="deliver"} 8`)
	assert.Contains(t, body, `mk_job_queue_pending{queue="deliver"} 0`)
}

// TestBindDriver_ReplacesPreviousBinding verifies that calling BindDriver
// twice replaces the previous binding (= last caller wins), so re-wiring on
// driver swap behaves predictably.
func TestBindDriver_ReplacesPreviousBinding(t *testing.T) {
	m := New()
	first := &fakeDriver{workers: map[string]int{"deliver": 1}, inspector: &fakeInspector{}}
	second := &fakeDriver{workers: map[string]int{"deliver": 99}, inspector: &fakeInspector{}}
	m.BindDriver(first)
	m.BindDriver(second)

	r := prometheus.NewRegistry()
	require.NoError(t, m.Register(r))
	body := scrape(t, r)
	assert.Contains(t, body, `mk_job_workers_active{queue="deliver"} 99`)
}

// TestObserveHistograms verifies that DispatchWaitSeconds and ProcessingSeconds
// accept observations and record counts. Buckets are not enumerated exhaustively
// here; we only verify the count side moves so future hook PRs (#1124) can
// rely on the histogram being functional.
func TestObserveHistograms(t *testing.T) {
	m := New()
	m.DispatchWaitSeconds.WithLabelValues("deliver").Observe(0.005)
	m.ProcessingSeconds.WithLabelValues("deliver", "success").Observe(0.5)
	m.ProcessingSeconds.WithLabelValues("deliver", "failure").Observe(1.5)

	r := prometheus.NewRegistry()
	require.NoError(t, m.Register(r))
	body := scrape(t, r)
	assert.Contains(t, body, `mk_job_dispatch_wait_seconds_count{queue="deliver"} 1`)
	assert.Contains(t, body, `mk_job_processing_seconds_count{queue="deliver",status="success"} 1`)
	assert.Contains(t, body, `mk_job_processing_seconds_count{queue="deliver",status="failure"} 1`)
}

// TestScaleEventsTotalCounter verifies that the counter can be incremented
// (sub-issue #1125 will wire actual increments from the controller).
func TestScaleEventsTotalCounter(t *testing.T) {
	m := New()
	m.ScaleEventsTotal.WithLabelValues("deliver", "up").Inc()
	m.ScaleEventsTotal.WithLabelValues("deliver", "up").Inc()
	m.ScaleEventsTotal.WithLabelValues("deliver", "down").Inc()
	assert.Equal(t, 2.0, testutil.ToFloat64(m.ScaleEventsTotal.WithLabelValues("deliver", "up")))
	assert.Equal(t, 1.0, testutil.ToFloat64(m.ScaleEventsTotal.WithLabelValues("deliver", "down")))
}

// scrape calls the registry's HTTP handler and returns the response body as
// a string. Used to assert text-format output rather than scanning collectors
// directly (matches how Prometheus actually consumes the endpoint).
func scrape(t *testing.T, r *prometheus.Registry) string {
	t.Helper()
	srv := httptest.NewServer(promhttp.HandlerFor(r, promhttp.HandlerOpts{}))
	t.Cleanup(srv.Close)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return string(body)
}
