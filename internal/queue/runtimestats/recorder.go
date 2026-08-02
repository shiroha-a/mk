// Package runtimestats keeps a short in-memory history of job queue runtime
// behaviour so the admin UI can show what the auto-scale controller and the
// workers are actually doing.
//
// Prometheus (`internal/queue/metrics`) is the durable, scrape-oriented view
// and stays the source of truth for alerting. It is intentionally exposed
// without auth (LB/nginx ACL required), so admins cannot read it from the
// control panel. This package fills that gap with a small bounded snapshot
// that `admin/queue/*` can serve to moderators.
//
// 設計判断:
//   - **プロセスローカル / 揮発**。再起動で消えるし、複数プロセス構成では
//     自プロセス分しか見えない。恒久的な観測は Prometheus 側で行う前提で、
//     ここは「今この worker で何が起きているか」を見るためのもの。
//   - reservoir は固定長のリングバッファ。p50/p95 は sort して求める
//     (最大 512 サンプルなので admin API の応答で十分軽い)。
package runtimestats

import (
	"sort"
	"sync"
	"time"
)

// latencySamples is the per-queue ring buffer size for latency observations.
// 512 サンプルあれば p95 が 25 サンプル分に効くので、バースト直後でも
// 「詰まっているか」の判断に足りる。1 サンプル 8 byte なので queue あたり
// 4KB 程度。
const latencySamples = 512

// scaleEvents is the per-queue ring buffer size for auto-scale events.
// AIMD の cooldown が既定 30s なので 64 件で直近 30 分前後を保持できる。
const scaleEvents = 64

// LatencySnapshot summarises the observations currently held for one queue.
// Count is the number of samples in the window, not a lifetime total.
type LatencySnapshot struct {
	Count int           `json:"count"`
	P50   time.Duration `json:"-"`
	P95   time.Duration `json:"-"`
	Max   time.Duration `json:"-"`
}

// ScaleEvent is one auto-scale decision that was actually committed.
type ScaleEvent struct {
	At        time.Time `json:"at"`
	Queue     string    `json:"queue"`
	Direction string    `json:"direction"` // "up" / "down"
	From      int       `json:"from"`
	To        int       `json:"to"`
}

// Snapshot is the per-queue view handed to the admin API.
type Snapshot struct {
	DispatchWait LatencySnapshot
	Processing   LatencySnapshot
	Failures     int
	ScaleEvents  []ScaleEvent
}

type ring struct {
	buf  []time.Duration
	next int
	full bool
}

func newRing(n int) *ring { return &ring{buf: make([]time.Duration, n)} }

func (r *ring) add(v time.Duration) {
	r.buf[r.next] = v
	r.next = (r.next + 1) % len(r.buf)
	if r.next == 0 {
		r.full = true
	}
}

// values returns a copy of the live samples (oldest ordering is irrelevant
// because callers only compute quantiles).
func (r *ring) values() []time.Duration {
	n := r.next
	if r.full {
		n = len(r.buf)
	}
	out := make([]time.Duration, n)
	copy(out, r.buf[:n])
	return out
}

func summarise(vals []time.Duration) LatencySnapshot {
	if len(vals) == 0 {
		return LatencySnapshot{}
	}
	sort.Slice(vals, func(i, j int) bool { return vals[i] < vals[j] })
	return LatencySnapshot{
		Count: len(vals),
		P50:   quantile(vals, 0.50),
		P95:   quantile(vals, 0.95),
		Max:   vals[len(vals)-1],
	}
}

// quantile picks the nearest-rank sample. sorted must be non-empty.
func quantile(sorted []time.Duration, q float64) time.Duration {
	idx := int(float64(len(sorted))*q + 0.5)
	if idx < 1 {
		idx = 1
	}
	if idx > len(sorted) {
		idx = len(sorted)
	}
	return sorted[idx-1]
}

type queueState struct {
	dispatch *ring
	process  *ring
	failures int
	scales   []ScaleEvent
}

// Recorder accumulates observations. The zero value is not usable; construct
// with New. Safe for concurrent use — every worker goroutine writes here.
type Recorder struct {
	mu     sync.Mutex
	queues map[string]*queueState
	now    func() time.Time
}

// New returns an empty Recorder.
func New() *Recorder {
	return &Recorder{queues: map[string]*queueState{}, now: time.Now}
}

// SetClockForTest overrides the clock used for scale event timestamps.
func (r *Recorder) SetClockForTest(now func() time.Time) {
	r.mu.Lock()
	r.now = now
	r.mu.Unlock()
}

func (r *Recorder) stateLocked(queue string) *queueState {
	st := r.queues[queue]
	if st == nil {
		st = &queueState{dispatch: newRing(latencySamples), process: newRing(latencySamples)}
		r.queues[queue] = st
	}
	return st
}

// ObserveDispatchWait records how long a job waited between enqueue and the
// worker picking it up. Negative values (clock skew between the enqueuing and
// consuming process) are clamped to zero rather than dropped, so the sample
// count still reflects throughput.
func (r *Recorder) ObserveDispatchWait(queue string, d time.Duration) {
	if d < 0 {
		d = 0
	}
	r.mu.Lock()
	r.stateLocked(queue).dispatch.add(d)
	r.mu.Unlock()
}

// ObserveProcessing records how long the handler ran, and whether it failed.
func (r *Recorder) ObserveProcessing(queue string, d time.Duration, failed bool) {
	if d < 0 {
		d = 0
	}
	r.mu.Lock()
	st := r.stateLocked(queue)
	st.process.add(d)
	if failed {
		st.failures++
	}
	r.mu.Unlock()
}

// RecordScale appends a committed auto-scale event. from/to are worker counts.
func (r *Recorder) RecordScale(queue, direction string, from, to int) {
	r.mu.Lock()
	st := r.stateLocked(queue)
	st.scales = append(st.scales, ScaleEvent{
		At: r.now().UTC(), Queue: queue, Direction: direction, From: from, To: to,
	})
	if len(st.scales) > scaleEvents {
		st.scales = st.scales[len(st.scales)-scaleEvents:]
	}
	r.mu.Unlock()
}

// Snapshot returns the current view for queue. Unknown queues yield a zero
// snapshot (not an error) so callers can render "no data yet" uniformly.
func (r *Recorder) Snapshot(queue string) Snapshot {
	r.mu.Lock()
	st := r.queues[queue]
	if st == nil {
		r.mu.Unlock()
		return Snapshot{ScaleEvents: []ScaleEvent{}}
	}
	dispatch := st.dispatch.values()
	process := st.process.values()
	failures := st.failures
	scales := make([]ScaleEvent, len(st.scales))
	copy(scales, st.scales)
	r.mu.Unlock()

	// newest first: admin UI は直近から読む
	for i, j := 0, len(scales)-1; i < j; i, j = i+1, j-1 {
		scales[i], scales[j] = scales[j], scales[i]
	}
	return Snapshot{
		DispatchWait: summarise(dispatch),
		Processing:   summarise(process),
		Failures:     failures,
		ScaleEvents:  scales,
	}
}
