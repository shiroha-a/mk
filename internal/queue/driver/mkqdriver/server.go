package mkqdriver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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
// API.
//
// Stop semantics: mkq.Worker.Stop **cancels** the in-flight job's
// context (= ctx.Done fires for the handler) and waits for the worker
// goroutine to exit cleanly. It does NOT wait for the in-flight job to
// complete naturally. Cancelled jobs are not lost: BullMQ re-locks
// them on next pickup so a different Worker (in the same or another
// pool) will retry. This trade-off prevents scale-down from blocking
// for minutes on slow remote inboxes.
//
// Locking model:
//   - s.mu protects s.started, s.handlers, and the s.pools map (not
//     individual pool state).
//   - Each pool owns its own pool.mu for the workers slice. Resize
//     takes pool.mu and HOLDS IT across Worker.Stop calls. Stop is
//     fast (ms-order cancellation, not job completion) so this is
//     acceptable; different queues use independent pool.mu so
//     cross-queue Resize is parallel.
//   - workerPool.shutdown flag (set under pool.mu in shutdownLocked)
//     guards against the Resize-after-Shutdown race: once Shutdown
//     marks a pool, subsequent Resize calls on that captured pool
//     return ErrResizeAfterShutdown instead of spawning leaked
//     Workers.
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
	// idlePollInterval は mkq.WithIdlePollInterval の引数。0 なら
	// defaultIdlePollInterval を使う。
	idlePollInterval time.Duration

	// perQueueConcurrent / perQueueRate are runtime tuning overrides
	// keyed by queue name (#495). Missing / zero entries fall back to
	// the shared per-queue defaults (`concurrency` for the worker pool,
	// no rate limiter respectively).
	perQueueConcurrent map[string]int
	perQueueRate       map[string]int

	// observer is the optional per-job timing hook (#2277). nil のときは
	// dispatch path で clock も触らない。
	observer driver.Observer

	mu       sync.Mutex
	handlers map[string]driver.HandlerFunc
	pools    map[string]*workerPool
	started  bool
}

// workerPool owns a slice of mkq.Worker instances for one queue. Each
// Worker is started with WithConcurrency(1), so |workers| equals the
// effective concurrency for the queue. Resize mutates the slice while
// holding mu and issues Worker.Stop calls under the same mu (Stop is
// ms-order cancellation, not job completion, so holding the lock is
// acceptable).
//
// shutdown is set true under mu by shutdownLocked. Once set, all
// subsequent resizeLocked calls return ErrResizeAfterShutdown so a
// caller that captured the pool pointer before Server.Shutdown ran
// cannot spawn leaked Workers on a pool that is no longer owned by
// any Server.
type workerPool struct {
	queue   *mkq.Queue[framedPayload]
	handler mkq.Handler[framedPayload]
	// optsBase is the WorkerOption slice common to every Worker in this
	// pool (rate limit, job metrics, etc.). WithConcurrency(1) is added
	// per-worker in Resize, not here.
	optsBase []mkq.WorkerOption

	mu       sync.Mutex
	workers  []*mkq.Worker
	shutdown bool
}

// ErrResizeAfterShutdown is returned by Resize when the pool's owning
// Server has already shut down. Callers (auto-scale controller) should
// treat this as "stop trying to scale, the server is gone" — same
// semantics as driver.ErrResizeNotSupported on asynq.
var ErrResizeAfterShutdown = errors.New("mkqdriver: pool already shut down")

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
		var optsBase []mkq.WorkerOption
		if rl, ok := s.perQueueRate[name]; ok && rl > 0 {
			// mkq.WithRateLimit は per-Worker に適用される。Pool 内で N
			// Worker いるなら合計 rate は rl × N に倍化するため、ADR §5.1
			// trade-off の通り pool-of-Workers では rate limit が単一
			// Worker 時と一致しない。#1126 queue-bench で挙動を検証予定。
			optsBase = append(optsBase, mkq.WithRateLimit(rl, time.Second))
			if concurrency > 1 {
				// operator が rate limit を「合計 N jobs/sec」と認識する誤読を
				// 防ぐため、両方を非自明値に設定したケースで startup ログを出す。
				// docs/configuration.md と #1124 PR 説明に同 trade-off を記載。
				slog.Warn("mkqdriver: rate limit applies per-Worker; effective total rate = rl × concurrency",
					"queue", name,
					"rl", rl,
					"concurrency", concurrency,
					"effective_total_per_sec", rl*concurrency)
			}
		}
		if s.maxMetricsPoints > 0 {
			// BullMQ-compatible per-queue metrics opt-in。
			optsBase = append(optsBase, mkq.WithJobMetrics(s.maxMetricsPoints))
		}
		if s.idlePollInterval > 0 {
			// アイドル時の空振りポーリングを間引きたい運用者向けの opt-in。
			//
			// **ジョブ取得は遅くならない。** worker は BZPOPMIN で marker key を
			// 待っており、ジョブが積まれた時点で Lua 側が marker を push するので
			// ミリ秒で起きる (interval 30 秒でも取得 18.9ms を実測)。この値は
			// 「marker を取り逃した場合に気づくまで」の上限でしかない。
			//
			// **既定を延ばさないのは shutdown が延びるため。** 発行済みの
			// BZPOPMIN は ctx キャンセルで中断できないので、停止には最大
			// interval だけかかる (実測: 1 秒で 0.6 秒、5 秒で 4.6 秒)。
			// mkq 既定の 100ms は go-redis が 1 秒へ切り上げるため実効 1 秒で、
			// worker 44 個の構成ではアイドル時に Redis へ毎秒 774 コマンド撃つ
			// (本番実測。Misskey TS は毎秒 21.5)。Redis の CPU は 1 コアの
			// 1.2% なので、再起動の速さと引き換えにする価値は薄いと判断した。
			optsBase = append(optsBase, mkq.WithIdlePollInterval(s.idlePollInterval))
		}
		// custom backoff (`{type:"custom"}`) で enqueue された job の retry delay
		// を算出する strategy を全 Worker に登録する。deliver / inbox は
		// Misskey TS httpRelatedBackoff で enqueue されるので、worker 側で同じ
		// 式を再現して drop-in 一致させる (#1406, mkq#67)。built-in backoff
		// (fixed / exponential) の job には影響しない。
		optsBase = append(optsBase, mkq.WithBackoffStrategy(func(attemptsMade int) time.Duration {
			return httpRelatedBackoff(attemptsMade, nil)
		}))

		pool := &workerPool{
			queue:    s.driver.queues[name],
			handler:  newDispatchHandler(handlersSnapshot, name, s.observer),
			optsBase: optsBase,
		}
		// `*Locked` 名 method の convention で pool.mu を保持。新規構築直後の
		// pool で外部参照は無いため race は無いが、convention 一貫性のため。
		pool.mu.Lock()
		spawnErr := pool.resizeLocked(concurrency)
		if spawnErr != nil {
			pool.shutdownLocked()
		}
		pool.mu.Unlock()
		if spawnErr != nil {
			// partial 既存 pools を cleanup してから error 返却。
			for _, p := range pools {
				p.mu.Lock()
				p.shutdownLocked()
				p.mu.Unlock()
			}
			s.mu.Lock()
			s.started = false
			s.mu.Unlock()
			return fmt.Errorf("mkqdriver: start workers for %q (after %d queues started successfully): %w",
				name, len(pools), spawnErr)
		}
		pools[name] = pool
	}

	s.mu.Lock()
	s.pools = pools
	s.mu.Unlock()
	return nil
}

// Shutdown stops every worker in every pool. Calls after the first are
// no-ops. Pools are shut down in parallel so the wall-clock latency is
// max(per-pool shutdown time) rather than the sum.
//
// Stop is fast (cancel + goroutine join, not job completion) so per
// pool shutdown is ms-order; the parallelism is mostly insurance
// against pathological cases (many queues × many Workers each).
func (s *Server) Shutdown() {
	s.mu.Lock()
	toShutdown := s.pools
	s.pools = nil
	s.started = false
	s.mu.Unlock()

	var wg sync.WaitGroup
	for _, p := range toShutdown {
		wg.Add(1)
		go func(p *workerPool) {
			defer wg.Done()
			p.mu.Lock()
			defer p.mu.Unlock()
			p.shutdownLocked()
		}(p)
	}
	wg.Wait()
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
// from the slice and stopped concurrently (Stop = cancel in-flight,
// not wait for completion — see Server type doc).
//
// Post-Shutdown guard: returns ErrResizeAfterShutdown if shutdownLocked
// has already run on this pool. Prevents the captured-pool race where
// Resize spawns Workers on a pool no longer owned by any Server.
func (p *workerPool) resizeLocked(n int) error {
	if p.shutdown {
		return ErrResizeAfterShutdown
	}
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
				stopWorkers(spawned)
				return fmt.Errorf("mkqdriver: spawn worker: %w", err)
			}
			spawned = append(spawned, w)
		}
		p.workers = append(p.workers, spawned...)
		return nil
	default: // n < current
		// scale-down: 末尾から (current - n) 個を Stop。Worker 単位の
		// cancel 経路 (mkq.Worker.Stop は in-flight job の ctx を即
		// キャンセル、ms オーダーで return)。
		toStop := p.workers[n:]
		p.workers = p.workers[:n]
		stopWorkers(toStop)
		return nil
	}
}

// shutdownLocked stops every Worker in the pool and marks the pool as
// shut down (= subsequent Resize calls return ErrResizeAfterShutdown).
// Caller must hold pool.mu. Used by Server.Shutdown.
func (p *workerPool) shutdownLocked() {
	p.shutdown = true
	stopWorkers(p.workers)
	p.workers = nil
}

// stopWorkerTimeout bounds how long stopWorker waits for one Worker.
//
// **上限が無いと、起こしを取りこぼした 1 本が起動全体を止める。** dispatcher は
// BZPOPMIN で最大 30 秒 park するが、発行済みの読み取りは ctx キャンセルで
// 中断できない。mkq は Stop で専用 key を突いて起こすので通常は ms で返る。
// ここはその起こしが届かなかった場合の保険で、30 秒は park の上限に合わせて
// ある (それを過ぎれば dispatcher は自力で目を覚ます)。
//
// 実際に無制限で踏んだ: Resize が inbox を 16 → 4 に縮める過程で Stop が
// 返らず、Server.Start が完了せず HTTP の listen まで到達しなかった。
const stopWorkerTimeout = 30 * time.Second

// stopWorker wraps mkq.Worker.Stop with structured error logging. Stop
// failures here are non-fatal (the goroutine still exits via runCancel)
// but operators should be alerted via logs because frequent failures
// usually indicate Redis connectivity issues that need investigation.
func stopWorker(w *mkq.Worker) {
	ctx, cancel := context.WithTimeout(context.Background(), stopWorkerTimeout)
	defer cancel()
	if err := w.Stop(ctx); err != nil {
		slog.Warn("mkqdriver: worker stop error", "err", err)
	}
}

// stopWorkers stops every Worker in ws concurrently.
//
// **逐次に止めない。** 1 本あたりの待ちは通常 ms だが、起こしを取りこぼすと
// stopWorkerTimeout まで伸びる。逐次だとそれが本数分積み上がる (16 本なら
// 最悪 8 分)。並列なら 1 回分で済む。
func stopWorkers(ws []*mkq.Worker) {
	var wg sync.WaitGroup
	for _, w := range ws {
		wg.Add(1)
		go func(w *mkq.Worker) {
			defer wg.Done()
			stopWorker(w)
		}(w)
	}
	wg.Wait()
}

// newDispatchHandler builds the mkq handler closure that demuxes
// incoming jobs to the registered driver.HandlerFunc. The handlers
// map is captured by reference; callers must guarantee it is not
// mutated after the closure is created (Server.Start enforces this
// by snapshotting via maps.Clone before the call).
//
// driver.SkipRetry → mkq.ErrUnrecoverable conversion lives here so
// processors can keep their existing %w-wrap idiom unchanged.
func newDispatchHandler(handlers map[string]driver.HandlerFunc, queue string, obs driver.Observer) mkq.Handler[framedPayload] {
	return func(ctx context.Context, job *mkq.Job[framedPayload]) (any, error) {
		taskType := job.Data.Type
		h := handlers[taskType]
		if h == nil {
			// 未登録 task type は SkipRetry 相当 (再 enqueue しても処理者が
			// いないので無限ループになる)。
			return nil, fmt.Errorf("mkqdriver: no handler for %q: %w", taskType, mkq.ErrUnrecoverable)
		}
		// observer 未配線なら clock も触らない (hot path、#2277)。
		if obs == nil {
			err := h(ctx, mkqTask{taskType: taskType, payload: job.Data.Body})
			if err != nil && errors.Is(err, driver.SkipRetry) {
				return nil, fmt.Errorf("%w: %w", err, mkq.ErrUnrecoverable)
			}
			return nil, err
		}
		// dispatch wait は **初回試行のみ** 観測する。job.Timestamp は BullMQ の
		// 作成時刻で attempt ごとに更新されないため、retry 分を含めると backoff
		// 待ち (意図的な遅延) が「詰まり」として混ざり、p95 が数十秒に化ける。
		// 見たいのは「enqueue された job がどれだけ待たされてから最初に拾われたか」
		// = 混雑度なので、AttemptsMade == 0 に限定する。
		if job.AttemptsMade == 0 && !job.Timestamp.IsZero() {
			obs.ObserveDispatchWait(queue, time.Since(job.Timestamp))
		}
		started := time.Now()
		err := h(ctx, mkqTask{taskType: taskType, payload: job.Data.Body})
		obs.ObserveProcessing(queue, time.Since(started), err != nil)
		if err != nil && errors.Is(err, driver.SkipRetry) {
			return nil, fmt.Errorf("%w: %w", err, mkq.ErrUnrecoverable)
		}
		return nil, err
	}
}

// SetObserver wires the per-job timing hook. Must be called before Start;
// Start snapshots the observer into each queue's dispatch closure.
func (s *Server) SetObserver(obs driver.Observer) {
	s.mu.Lock()
	s.observer = obs
	s.mu.Unlock()
}
