// Package metrics exposes Prometheus metrics for the job queue subsystem.
//
// 設計合意は docs/design/auto-scale-job-workers.md §6.1 を参照。本パッケージ
// は #1122 の最小スコープ (controller / Resize 配線前) なので以下の役割分担:
//
//   - mk_job_workers_active{queue} (gauge): scrape 時に driver.WorkerCount を
//     都度 read。auto-scale で動的変化する将来も追従できる。
//   - mk_job_queue_pending{queue} (gauge): scrape 時に Inspector.GetQueueInfo
//     を呼ぶ。中身は Redis ZCARD 相当で軽量。
//   - mk_job_dispatch_wait_seconds{queue} (histogram): declare のみ。
//     enqueue → dispatch hook は #1124 mkqdriver Resize PR で配線予定。
//   - mk_job_processing_seconds{queue,status} (histogram): declare のみ。
//     handler 前後の observation hook は #1124 で配線予定。
//   - mk_job_scale_events_total{queue,direction} (counter): declare のみ。
//     scale event 発火は #1125 controller wiring PR で配線予定。
//
// 5 metric 全部を一度に declare しておくことで、後続 PR が hook を追加するとき
// metric 名 / label 体系の議論を再度しなくて済む (前方互換 contract を本 PR で
// 確定する)。
package metrics

import (
	"sync"
	"sync/atomic"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/shiroha-a/mk/internal/queue/driver"
)

// Standard queue label set covering all 5 queues defined in queue/queue.go.
// Used so metric collectors return zero-valued series for unseen queues at
// startup (Prometheus convention: zero-init avoids "no data" gaps in graphs).
var standardQueues = []string{"deliver", "inbox", "export", "push", "webhook"}

// Metrics bundles every Prometheus collector owned by the queue subsystem.
// Construct with New, optionally call BindDriver to wire the pull-based
// gauges, then Register against a prometheus.Registry.
//
// 設計判断: prometheus.DefaultRegisterer を直接触らず、Server スコープの
// Registry を新規作成して inject する方式 (testing / 多重 instance 起動時に
// global 汚染を避けるため、`testutil` で使う sub-Registry pattern と整合)。
type Metrics struct {
	// pullCollector holds workers_active / queue_pending which are read
	// from the driver at scrape time. nil until BindDriver is called.
	pullCollector *driverCollector

	// Push-mode collectors below. Histograms / counters are observed
	// inline by call sites (driver hooks / controller). HistogramVec /
	// CounterVec keep their accumulated values until next scrape.
	DispatchWaitSeconds *prometheus.HistogramVec
	ProcessingSeconds   *prometheus.HistogramVec
	ScaleEventsTotal    *prometheus.CounterVec

	// scrapeErrs is the (queue, kind) → cumulative count map. Lives on
	// Metrics (not driverCollector) so accumulated counts persist across
	// BindDriver re-bind (= driver swap mid-life, e.g. asynq → mkq config
	// change). driverCollector holds a pointer to this map so Inc + emit
	// both touch the same series.
	scrapeErrs sync.Map // map[scrapeErrKey]*atomic.Uint64
}

// ScrapeErrorCount returns the cumulative count of /metrics scrape failures
// for the (queue, kind) pair, or 0 if no errors have been observed.
// Persisted on Metrics so re-binding the driver via BindDriver does not
// reset the count.
func (m *Metrics) ScrapeErrorCount(queue, kind string) uint64 {
	return loadScrapeError(&m.scrapeErrs, scrapeErrKey{queue: queue, kind: kind})
}

// New constructs the Metrics bundle (no registration, no driver binding).
//
// Histogram buckets are chosen for typical mk-go job latencies:
//   - dispatch_wait: 1ms..16s (covers normal idle dispatch through queue backed-up)
//   - processing: 10ms..160s (covers fast Redis touches through slow federation deliver)
func New() *Metrics {
	m := &Metrics{
		DispatchWaitSeconds: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "mk",
			Subsystem: "job",
			Name:      "dispatch_wait_seconds",
			Help:      "Seconds a job waited between enqueue and worker dispatch.",
			Buckets:   prometheus.ExponentialBuckets(0.001, 4, 8), // 1ms .. ~16s
		}, []string{"queue"}),
		ProcessingSeconds: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "mk",
			Subsystem: "job",
			Name:      "processing_seconds",
			Help:      "Seconds a worker spent processing a job.",
			Buckets:   prometheus.ExponentialBuckets(0.01, 4, 8), // 10ms .. ~160s
		}, []string{"queue", "status"}),
		ScaleEventsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "mk",
			Subsystem: "job",
			Name:      "scale_events_total",
			Help:      "Total auto-scale events triggered, by queue and direction (up/down).",
		}, []string{"queue", "direction"}),
	}
	// Pre-init zero-valued series for push-mode metrics so Prometheus
	// dashboards show a continuous line instead of "no data". Pull-mode
	// metrics (workers_active / queue_pending / scrape_errors_total) don't
	// need pre-init because driverCollector.Collect always emits a value for
	// every standardQueues entry on each scrape.
	for _, q := range standardQueues {
		m.DispatchWaitSeconds.WithLabelValues(q)
		m.ProcessingSeconds.WithLabelValues(q, "success")
		m.ProcessingSeconds.WithLabelValues(q, "failure")
		m.ScaleEventsTotal.WithLabelValues(q, "up")
		m.ScaleEventsTotal.WithLabelValues(q, "down")
	}
	return m
}

// BindDriver wires the pull-based gauges (workers_active / queue_pending) to
// the given driver.Driver. Calling BindDriver multiple times replaces the
// previous binding (= last caller wins). Call order vs Register is free —
// the new driverCollector owns its own scrape error state internally so
// scrape errors are emitted from within the same Collect goroutine as the
// gauges (= no race vs a separately-registered CounterVec; #1136 follow-up).
//
// Gauges are evaluated lazily on each scrape, so a non-running driver
// (Server not yet Start()ed) shows zero without erroring (driver.WorkerCount
// returns 0 in that case).
func (m *Metrics) BindDriver(d driver.Driver) {
	if d == nil {
		m.pullCollector = nil
		return
	}
	m.pullCollector = &driverCollector{driver: d, scrapeErrs: &m.scrapeErrs}
}

// Register attaches all owned collectors to r. Returns the first error to
// occur; callers should treat any error as a startup failure so the operator
// notices duplicate registration (= bug). Idempotent registration is NOT
// supported by prometheus.Registry, so call this once per Registry.
func (m *Metrics) Register(r prometheus.Registerer) error {
	collectors := []prometheus.Collector{
		m.DispatchWaitSeconds,
		m.ProcessingSeconds,
		m.ScaleEventsTotal,
	}
	if m.pullCollector != nil {
		collectors = append(collectors, m.pullCollector)
	}
	for _, c := range collectors {
		if err := r.Register(c); err != nil {
			return err
		}
	}
	return nil
}

// driverCollector implements prometheus.Collector for the pull-based metrics
// (workers_active / queue_pending / scrape_errors_total). All 3 metrics
// share a single Collector so Describe / Collect execute one pass over the
// queue list, AND scrape errors are accumulated + emitted from within the
// same Collect goroutine — emitting via MustNewConstMetric captures the
// counter value synchronously, avoiding the race that existed when
// scrape_errors_total was a separate CounterVec (its Counter.Write was
// called LATER by prometheus.Registry.Gather's main goroutine, after a
// peer Collector's Inc had already mutated the counter — off-by-one
// observation in the same scrape; #1136 follow-up).
//
// At scrape time, Collect calls driver.WorkerCount and Inspector.GetQueueInfo
// for every standardQueues entry. Errors from Inspector are swallowed at the
// gauge layer (reported as 0 to keep /metrics responsive under transient
// Redis failures) BUT the failure is also counted via
// `mk_job_scrape_errors_total{kind="queue_pending"}` so operators can alert
// on the failure rate without having to watch the gauge for "suspicious zeros".
type driverCollector struct {
	driver driver.Driver

	// scrapeErrs is a pointer to the Metrics-owned map so accumulated
	// counts persist across BindDriver re-bind (driver swap). sync.Map +
	// *atomic.Uint64 lets concurrent scrapes (= simultaneous Prometheus
	// scrapers) increment safely without blocking each other.
	scrapeErrs *sync.Map // map[scrapeErrKey]*atomic.Uint64
}

// scrapeErrKey identifies a single scrape-error counter series. kind is the
// upstream label ("queue_pending" for inspector failures; future kinds may
// cover other pull sources).
type scrapeErrKey struct {
	queue, kind string
}

// incScrapeError increments the cumulative count for (queue, kind) on the
// given sync.Map. Safe for concurrent invocation.
func incScrapeError(m *sync.Map, key scrapeErrKey) {
	v, _ := m.LoadOrStore(key, new(atomic.Uint64))
	v.(*atomic.Uint64).Add(1)
}

// loadScrapeError returns the cumulative count for (queue, kind), 0 if the
// counter has never been incremented.
func loadScrapeError(m *sync.Map, key scrapeErrKey) uint64 {
	v, ok := m.Load(key)
	if !ok {
		return 0
	}
	return v.(*atomic.Uint64).Load()
}

var (
	// workersActiveDesc reports per-queue worker pool size on mkq backend.
	// asynq backend semantics (pool-wide value reported per queue label)
	// are documented in docs/configuration.md alongside the enableMetrics
	// flag rather than being shoved into the Help text.
	workersActiveDesc = prometheus.NewDesc(
		"mk_job_workers_active",
		"Number of active worker goroutines per queue (asynq backend reports pool-wide; see docs/configuration.md).",
		[]string{"queue"}, nil,
	)
	queuePendingDesc = prometheus.NewDesc(
		"mk_job_queue_pending",
		"Number of pending jobs per queue (Redis ZCARD).",
		[]string{"queue"}, nil,
	)
	scrapeErrorsDesc = prometheus.NewDesc(
		"mk_job_scrape_errors_total",
		"Total Prometheus scrape errors per queue, by source kind (e.g. queue_pending = Inspector.GetQueueInfo failed). A non-zero rate here means the corresponding gauge is reporting stale/zero values; alert on this rather than only on the gauge values themselves.",
		[]string{"queue", "kind"}, nil,
	)
)

func (c *driverCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- workersActiveDesc
	ch <- queuePendingDesc
	ch <- scrapeErrorsDesc
}

func (c *driverCollector) Collect(ch chan<- prometheus.Metric) {
	inspector := c.driver.Inspector()
	for _, q := range standardQueues {
		ch <- prometheus.MustNewConstMetric(
			workersActiveDesc, prometheus.GaugeValue,
			float64(c.driver.WorkerCount(q)), q,
		)
		var pending int
		info, err := inspector.GetQueueInfo(q)
		switch {
		case err != nil:
			incScrapeError(c.scrapeErrs, scrapeErrKey{queue: q, kind: "queue_pending"})
		case info != nil:
			pending = info.Pending
		}
		ch <- prometheus.MustNewConstMetric(
			queuePendingDesc, prometheus.GaugeValue,
			float64(pending), q,
		)
	}
	// Emit scrape error counters AFTER all Inc calls above so the value
	// reflects this scrape's failures. MustNewConstMetric captures the
	// count synchronously within this goroutine — no race with the main
	// Gather goroutine reading via Counter.Write later. Zero series are
	// emitted for every standard queue so dashboards see a continuous line.
	for _, q := range standardQueues {
		ch <- prometheus.MustNewConstMetric(
			scrapeErrorsDesc, prometheus.CounterValue,
			float64(loadScrapeError(c.scrapeErrs, scrapeErrKey{queue: q, kind: "queue_pending"})),
			q, "queue_pending",
		)
	}
}
