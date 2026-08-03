package asynqdriver

import (
	"context"
	"errors"
	"fmt"

	"github.com/hibiken/asynq"
	"golang.org/x/time/rate"

	"github.com/shiroha-a/mk/internal/queue/driver"
)

// ServerConfig configures the asynq-backed worker.
type ServerConfig struct {
	// Concurrency is the number of worker goroutines. Zero falls
	// back to 16 to match the historical default.
	Concurrency int
	// Queues maps queue name → relative priority weight. asynq
	// uses these as scheduling weights; if empty, the asynq driver
	// fills in the queue list expected by mk-go (deliver / push /
	// export / webhook / maintenance).
	Queues map[string]int
	// RateLimits maps queue name → tasks/sec cap. Entries with
	// value <= 0 are ignored. Implemented as a token-bucket
	// middleware that wraps asynq's dispatch — handlers block on
	// the limiter rather than being rejected. Burst defaults to
	// the rate (1 second of headroom) so short spikes are absorbed.
	RateLimits map[string]int
}

// Server implements driver.Server over *asynq.Server + ServeMux.
type Server struct {
	inner *asynq.Server
	mux   *asynq.ServeMux
}

// NewServer constructs the worker side of the asynq driver bound to
// the given Redis connection.
func NewServer(redisOpt asynq.RedisClientOpt, cfg ServerConfig) *Server {
	concurrency := cfg.Concurrency
	if concurrency <= 0 {
		concurrency = 16
	}
	queues := cfg.Queues
	if len(queues) == 0 {
		// Misskey-go の従来 wiring と同じ queue set を埋め直す。
		// 新しい queue を増やすときはここと queue.QueueName 等の
		// 定数追加を同時に行う。
		queues = map[string]int{
			"deliver":       1,
			"inbox":         1,
			"push":          1,
			"export":        1,
			"webhook":       1,
			"maintenance":   1,
			"objectStorage": 1,
		}
	}
	inner := asynq.NewServer(redisOpt, asynq.Config{
		Concurrency: concurrency,
		Queues:      queues,
	})
	mux := asynq.NewServeMux()
	if mw := buildRateLimitMiddleware(cfg.RateLimits); mw != nil {
		mux.Use(mw)
	}
	return &Server{inner: inner, mux: mux}
}

// buildRateLimitMiddleware returns nil when no queue has a positive
// RatePerSec — keeping the dispatch fast-path allocation-free for the
// common "no rate limit" case. When at least one queue is rate-limited,
// returns a middleware that blocks (Wait) on the per-queue limiter.
//
// 注意: asynq は共有 worker pool で各 queue を捌くため、レート制限対象の
// queue (例: deliver) に多数のタスクが pending している状況では、ワーカー
// goroutine が l.Wait で待機して他 queue (push / export / webhook /
// maintenance) のタスクが starvation する可能性がある (#531 review)。
// これは asynq の設計 (per-queue pull-rate 制御 API が無い) に起因する
// 制約で、`Reserve` ベースに切替えても根本的に解消しない (タスクを
// 即時失敗させて再 enqueue するか、Wait で worker を寝かせるかのトレード
// オフ)。実運用で rate limit を本格運用するなら mkq driver 利用を推奨
// (mkq.WithRateLimit は worker pull レイヤで制御するので他 queue に
// 影響しない)。docs/configuration.md の jobQueueDriver 節に明記。
func buildRateLimitMiddleware(rates map[string]int) asynq.MiddlewareFunc {
	limiters := map[string]*rate.Limiter{}
	for q, r := range rates {
		if r <= 0 {
			continue
		}
		// Burst = rate gives ~1s of headroom for spikes (Wait blocks
		// when exhausted). Smaller burst makes the limiter brittle
		// for high-fanout deliveries; larger weakens the cap.
		limiters[q] = rate.NewLimiter(rate.Limit(r), r)
	}
	if len(limiters) == 0 {
		return nil
	}
	return func(next asynq.Handler) asynq.Handler {
		return asynq.HandlerFunc(func(ctx context.Context, t *asynq.Task) error {
			qname, _ := asynq.GetQueueName(ctx)
			if l, ok := limiters[qname]; ok {
				if err := l.Wait(ctx); err != nil {
					return err
				}
			}
			return next.ProcessTask(ctx, t)
		})
	}
}

// Handle registers a HandlerFunc for the given task type. The
// handler receives a driver.Task adapter wrapping the underlying
// *asynq.Task.
//
// Errors wrapping driver.SkipRetry are translated to
// asynq.SkipRetry so the asynq runtime drops the job from the retry
// queue exactly as it did before the driver indirection.
func (s *Server) Handle(taskType string, h driver.HandlerFunc) {
	s.mux.HandleFunc(taskType, func(ctx context.Context, t *asynq.Task) error {
		err := h(ctx, asynqTask{t: t})
		if err != nil && errors.Is(err, driver.SkipRetry) {
			// 既存挙動: handler が SkipRetry を wrap した error を
			// 返すと、asynq の retry を抑止する。元の error message
			// は %w 連鎖でそのまま保持する。
			return fmt.Errorf("%w: %w", err, asynq.SkipRetry)
		}
		return err
	})
}

// Start launches the asynq worker in the background. Returns
// immediately once the worker goroutines are up.
func (s *Server) Start() error { return s.inner.Start(s.mux) }

// Shutdown gracefully stops the worker, waiting for in-flight jobs
// to finish.
func (s *Server) Shutdown() { s.inner.Shutdown() }
