package mkqdriver

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"sort"
	"sync"
	"time"

	"github.com/shiroha-a/mkq"

	"github.com/shiroha-a/mk/internal/queue/driver"
)

// Server runs a per-queue worker pool, dispatching jobs to handlers
// registered via Handle(taskType, fn). The dispatch keys off the
// framedPayload.Type field rather than mkq's BullMQ Job.name — that
// field is not overridable by mkq's schedule API, so framing the
// payload is the only consistent way to recover the task type for
// both ad-hoc and scheduled jobs.
//
// Pool architecture (#1124, per ADR §5.1): for each queue we hold a
// `workerPool` containing N `mkq.Worker` instances, each constructed
// with `WithConcurrency(1)`. Adding workers = scale-up; stopping
// workers = scale-down. This sidesteps mkq's lack of a runtime Resize
// API while keeping individual Worker shutdown graceful (each Worker
// drains its single in-flight job on Stop).
//
// Locking model: s.mu protects s.started and s.handlers. Each pool
// owns its own pool.mu for the workers slice; Resize takes pool.mu
// but releases it before calling Worker.Stop so concurrent Resize
// calls on **different** queues do not serialise on each other.
// Resize calls on the **same** queue serialise via pool.mu.
//
// After Start() returns, the dispatch fast-path runs **without** taking
// s.mu — the handler snapshot is captured at Start time and frozen for
// the lifetime of the server. Handle calls after Start panic, so the
// freeze is maintained.
type Server struct {
	driver      *Driver
	concurrency int
	// maxMetricsPoints は mkq.WithJobMetrics の引数。0 だと metrics
	// 書き込みを無効化、正値で BullMQ-spec の metrics LIST に書き込み
	// が走る。
	maxMetricsPoints int

	// perQueueConcurrent / perQueueRate are runtime tuning overrides
	// keyed by queue name (#495). Missing / zero entries fall back to
	// the shared per-queue defaults (`concurrency` for the worker pool,
	// no rate limiter respectively).
	perQueueConcurrent map[string]int
	perQueueRate       map[string]int

	mu       sync.Mutex
	handlers map[string]driver.HandlerFunc
	pools    map[string]*workerPool
	started  bool
}

// workerPool owns a slice of mkq.Worker instances for one queue. Each
// Worker is started with WithConcurrency(1), so |workers| equals the
// effective concurrency for the queue. Resize mutates the slice while
// holding mu; Stop calls are issued **without** mu so concurrent Resize
// calls on the same pool serialise but do not block other pools.
type workerPool struct {
	queue   *mkq.Queue[framedPayload]
	handler mkq.Handler[framedPayload]
	// optsBase is the WorkerOption slice common to every Worker in this
	// pool (rate limit, job metrics, etc.). WithConcurrency(1) is added
	// per-worker in Resize, not here.
	optsBase []mkq.WorkerOption

	mu      sync.Mutex
	workers []*mkq.Worker
}

// Handle registers a handler for the given task type. Must be called
// before Start; calls after Start panic. The dispatch fast-path
// captures a snapshot of the handlers map at Start, so the registry
// is frozen for the worker's lifetime.
func (s *Server) Handle(taskType string, h driver.HandlerFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		panic("mkqdriver: Handle called after Start")
	}
	s.handlers[taskType] = h
}

// Start initialises one workerPool per pre-defined queue and fills it
// with the configured initial concurrency. Returns once all per-queue
// pools are populated.
//
// Failure mode: if any per-queue spawn fails partway, every Worker
// started so far is stopped before bubbling the error up. Stop is
// invoked **without** holding s.mu so the in-flight job draining is not
// blocked.
func (s *Server) Start() error {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return errors.New("mkqdriver: Start called twice")
	}
	handlersSnapshot := maps.Clone(s.handlers)
	if handlersSnapshot == nil {
		handlersSnapshot = map[string]driver.HandlerFunc{}
	}
	s.started = true
	s.mu.Unlock()

	dispatch := newDispatchHandler(handlersSnapshot)

	// Iterate queue names in deterministic order so partial-startup
	// error messages are reproducible across runs (Go map iteration
	// is randomized).
	names := make([]string, 0, len(s.driver.queues))
	for n := range s.driver.queues {
		names = append(names, n)
	}
	sort.Strings(names)

	pools := make(map[string]*workerPool, len(names))
	for _, name := range names {
		concurrency := s.concurrency
		if v, ok := s.perQueueConcurrent[name]; ok && v > 0 {
			concurrency = v
		}
		optsBase := []mkq.WorkerOption{}
		if rl, ok := s.perQueueRate[name]; ok && rl > 0 {
			// mkq.WithRateLimit は per-Worker に適用される。Pool 内で N
			// Worker いるなら合計 rate は rl × N に倍化するため、ADR §5.1
			// trade-off の通り pool-of-Workers では rate limit が単一
			// Worker 時と一致しない。#1126 queue-bench で挙動を検証予定。
			optsBase = append(optsBase, mkq.WithRateLimit(rl, time.Second))
		}
		if s.maxMetricsPoints > 0 {
			// BullMQ-compatible per-queue metrics opt-in。
			optsBase = append(optsBase, mkq.WithJobMetrics(s.maxMetricsPoints))
		}

		pool := &workerPool{
			queue:    s.driver.queues[name],
			handler:  dispatch,
			optsBase: optsBase,
		}
		if err := pool.resizeLocked(concurrency); err != nil {
			pool.shutdownLocked()
			for _, p := range pools {
				p.mu.Lock()
				p.shutdownLocked()
				p.mu.Unlock()
			}
			s.mu.Lock()
			s.started = false
			s.mu.Unlock()
			return fmt.Errorf("mkqdriver: start workers for %q: %w", name, err)
		}
		pools[name] = pool
	}

	s.mu.Lock()
	s.pools = pools
	s.mu.Unlock()
	return nil
}

// Shutdown stops every worker in every pool, waiting for in-flight jobs
// to finish. Calls after the first are no-ops.
func (s *Server) Shutdown() {
	s.mu.Lock()
	toShutdown := s.pools
	s.pools = nil
	s.started = false
	s.mu.Unlock()
	for _, p := range toShutdown {
		p.mu.Lock()
		p.shutdownLocked()
		p.mu.Unlock()
	}
}

// Resize changes the worker count for qname at runtime. Implements the
// Driver.Resize contract (see driver.Driver interface). Concurrent
// Resize calls on the same qname serialise via the pool's internal
// mutex; calls on different qnames proceed in parallel.
//
// n == 0 is allowed and means "stop all workers for this queue" (= the
// queue is paused). n < 0 returns an error. There is no hard upper
// bound — the auto-scale controller (#1125) enforces maxWorkers
// upstream.
func (s *Server) Resize(qname string, n int) error {
	if n < 0 {
		return fmt.Errorf("mkqdriver: Resize: n must be >= 0, got %d", n)
	}
	s.mu.Lock()
	pool, ok := s.pools[qname]
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("mkqdriver: Resize: unknown queue %q", qname)
	}
	pool.mu.Lock()
	defer pool.mu.Unlock()
	return pool.resizeLocked(n)
}

// workerCount reports the current number of Worker instances for qname,
// or 0 if no pool exists (Server not started, queue unknown). Called by
// Driver.WorkerCount.
func (s *Server) workerCount(qname string) int {
	s.mu.Lock()
	pool, ok := s.pools[qname]
	s.mu.Unlock()
	if !ok {
		return 0
	}
	pool.mu.Lock()
	defer pool.mu.Unlock()
	return len(pool.workers)
}

// resizeLocked is the workerPool's internal resize implementation.
// Caller must hold pool.mu. On scale-up, new Workers are spawned via
// mkq.Process. On scale-down, the trailing surplus Workers are removed
// from the slice and stopped sequentially.
//
// 注: Stop は in-flight job 完了まで blocking。pool.mu を保持したまま
// Stop を呼ぶため、scale-down 中の Resize 呼び出しは pool.mu で待たさ
// れる。1Hz controller 想定 (ADR §3.4) では実用上問題なし。長 job 想定
// なら別 pool 単位 (= 別 queue) の Resize は並行できるため、queue 横断
// の影響は無い。
func (p *workerPool) resizeLocked(n int) error {
	current := len(p.workers)
	switch {
	case n == current:
		return nil
	case n > current:
		// scale-up: 不足分の Worker を新規 spawn
		needed := n - current
		spawned := make([]*mkq.Worker, 0, needed)
		for i := 0; i < needed; i++ {
			opts := append([]mkq.WorkerOption{mkq.WithConcurrency(1)}, p.optsBase...)
			w, err := mkq.Process(p.queue, p.handler, opts...)
			if err != nil {
				// 既に spawn した Worker は停止して回復
				for _, sw := range spawned {
					_ = sw.Stop(context.Background())
				}
				return fmt.Errorf("mkqdriver: spawn worker: %w", err)
			}
			spawned = append(spawned, w)
		}
		p.workers = append(p.workers, spawned...)
		return nil
	default: // n < current
		// scale-down: 末尾から (current - n) 個を Stop。Worker 単位の
		// in-flight job が終わるまで blocking。
		toStop := p.workers[n:]
		p.workers = p.workers[:n]
		for _, w := range toStop {
			_ = w.Stop(context.Background())
		}
		return nil
	}
}

// shutdownLocked stops every Worker in the pool. Caller must hold
// pool.mu. Used by Server.Shutdown.
func (p *workerPool) shutdownLocked() {
	for _, w := range p.workers {
		_ = w.Stop(context.Background())
	}
	p.workers = nil
}

// newDispatchHandler builds the mkq handler closure that demuxes
// incoming jobs to the registered driver.HandlerFunc. The handlers
// map is captured by reference; callers must guarantee it is not
// mutated after the closure is created (Server.Start enforces this
// by snapshotting via maps.Clone before the call).
//
// driver.SkipRetry → mkq.ErrUnrecoverable conversion lives here so
// processors can keep their existing %w-wrap idiom unchanged.
func newDispatchHandler(handlers map[string]driver.HandlerFunc) mkq.Handler[framedPayload] {
	return func(ctx context.Context, job *mkq.Job[framedPayload]) (any, error) {
		taskType := job.Data.Type
		h := handlers[taskType]
		if h == nil {
			// 未登録 task type は SkipRetry 相当 (再 enqueue しても処理者が
			// いないので無限ループになる)。
			return nil, fmt.Errorf("mkqdriver: no handler for %q: %w", taskType, mkq.ErrUnrecoverable)
		}
		err := h(ctx, mkqTask{taskType: taskType, payload: job.Data.Body})
		if err != nil && errors.Is(err, driver.SkipRetry) {
			return nil, fmt.Errorf("%w: %w", err, mkq.ErrUnrecoverable)
		}
		return nil, err
	}
}
