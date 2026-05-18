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
	// Pre-init zero-valued series for every (queue, [status|direction]) pair
	// so Prometheus dashboards show a continuous line instead of "no data".
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
// previous binding (= last caller wins).
//
// Gauges are evaluated lazily on each scrape, so a non-running driver
// (Server not yet Start()ed) shows zero without erroring (driver.WorkerCount
// returns 0 in that case).
func (m *Metrics) BindDriver(d driver.Driver) {
	if d == nil {
		m.pullCollector = nil
		return
	}
	m.pullCollector = &driverCollector{driver: d}
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

// driverCollector implements prometheus.Collector for the pull-based gauges
// (workers_active / queue_pending). Both metrics share a single Collector
// so the Describe / Collect calls execute one pass over the queue list,
// rather than registering 2 × N GaugeFunc collectors.
//
// At scrape time, Collect calls driver.WorkerCount and Inspector.GetQueueInfo
// for every standardQueues entry. Errors from Inspector are swallowed and
// reported as 0 to keep the /metrics response robust against transient Redis
// failures (operator should monitor Redis separately).
type driverCollector struct {
	driver driver.Driver
}

var (
	workersActiveDesc = prometheus.NewDesc(
		"mk_job_workers_active",
		"Number of currently active worker goroutines per queue.",
		[]string{"queue"}, nil,
	)
	queuePendingDesc = prometheus.NewDesc(
		"mk_job_queue_pending",
		"Number of pending jobs per queue (Redis ZCARD).",
		[]string{"queue"}, nil,
	)
)

func (c *driverCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- workersActiveDesc
	ch <- queuePendingDesc
}

func (c *driverCollector) Collect(ch chan<- prometheus.Metric) {
	inspector := c.driver.Inspector()
	for _, q := range standardQueues {
		ch <- prometheus.MustNewConstMetric(
			workersActiveDesc, prometheus.GaugeValue,
			float64(c.driver.WorkerCount(q)), q,
		)
		var pending int
		if info, err := inspector.GetQueueInfo(q); err == nil && info != nil {
			pending = info.Pending
		}
		ch <- prometheus.MustNewConstMetric(
			queuePendingDesc, prometheus.GaugeValue,
			float64(pending), q,
		)
	}
}
