package server

import (
	"context"
	"fmt"
	"time"

	"github.com/shiroha-a/mk/internal/config"
	"github.com/shiroha-a/mk/internal/queue"
	"github.com/shiroha-a/mk/internal/queue/driver"
	"github.com/shiroha-a/mk/internal/queue/driver/asynqdriver"
	"github.com/shiroha-a/mk/internal/queue/driver/mkqdriver"
)

// buildQueueDriver constructs the queue driver selected by config.
//
// driverName=="mkq" connects to Redis via mkq.NewClient (PING + SCRIPT
// LOAD); failures bubble up so the server fails to start rather than
// silently falling back to asynq. asynq is the historical default and
// does not Dial on construction, so its branch is infallible at this
// layer.
//
// `<queue>JobConcurrency` / `<queue>JobPerSec` / `<queue>JobMaxAttempts`
// 系の config は queue_factory が driver Config に流して runtime に反映
// する。deliver / inbox の両 queue に対して有効 (#495 / #534)。
// relationshipJob* は mk-go に該当 queue が無いため TS-compat 用に
// 受け付けのみで no-op (docs/configuration.md 参照)。
func buildQueueDriver(ctx context.Context, cfg *config.Config) (driver.Driver, error) {
	totalConcurrency := 16
	if cfg.DeliverJobConcurrency != nil && *cfg.DeliverJobConcurrency > 0 {
		totalConcurrency = *cfg.DeliverJobConcurrency
	}

	queueConcurrency := perQueueConcurrencyFromConfig(cfg)
	queueRateLimits := perQueueRatesFromConfig(cfg)

	// 空 string は config.resolveJobQueueDriver で "mkq" に正規化されている
	// 想定だが、queue_factory が直接 *config.Config を受けるテスト経由など
	// 正規化されない経路もあるので、ここでも "" → "mkq" に倒す。
	// asynq driver は legacy / future-deprecation candidate (#571 audit)。
	switch cfg.JobQueueDriver {
	case "mkq", "":
		dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		return mkqdriver.New(dialCtx, mkqdriver.Config{
			Redis:            mkqdriver.BuildRedisOptions(cfg.RedisForJobQueue),
			Concurrency:      totalConcurrency,
			QueueConcurrency: queueConcurrency,
			QueueRateLimits:  queueRateLimits,
		})
	case "asynq":
		return asynqdriver.New(
			asynqdriver.BuildRedisOpt(cfg.RedisForJobQueue),
			asynqdriver.ServerConfig{
				Concurrency: totalConcurrency,
				RateLimits:  queueRateLimits,
			},
		), nil
	default:
		return nil, fmt.Errorf("server: unknown jobQueueDriver %q", cfg.JobQueueDriver)
	}
}

// perQueueConcurrencyFromConfig flattens the deliver/inbox/relationship
// concurrency knobs into a queue-name → worker-count map. relationship は
// 該当 queue が無いため forward されない (docs 参照)。
func perQueueConcurrencyFromConfig(cfg *config.Config) map[string]int {
	out := map[string]int{}
	if cfg.DeliverJobConcurrency != nil && *cfg.DeliverJobConcurrency > 0 {
		out["deliver"] = *cfg.DeliverJobConcurrency
	}
	if cfg.InboxJobConcurrency != nil && *cfg.InboxJobConcurrency > 0 {
		out["inbox"] = *cfg.InboxJobConcurrency
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// perQueueRatesFromConfig builds the queue-name → tasks/sec map applied to
// the driver Server's rate-limiter middleware (asynq) / mkq.WithRateLimit
// (mkq).
func perQueueRatesFromConfig(cfg *config.Config) map[string]int {
	out := map[string]int{}
	if cfg.DeliverJobPerSec != nil && *cfg.DeliverJobPerSec > 0 {
		out["deliver"] = *cfg.DeliverJobPerSec
	}
	if cfg.InboxJobPerSec != nil && *cfg.InboxJobPerSec > 0 {
		out["inbox"] = *cfg.InboxJobPerSec
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// defaultKeepFailed bounds the failed bucket retention applied to inbox /
// deliver enqueue when operator が config で明示しない場合の安全側 default。
// 1000 件あれば admin UI の Errored Instances panel (host aggregation で
// top-N 表示) は確実に賄え、redis memory pressure (1 job ≈ 1KB × 2 queue
// = 2MB) も問題にならない。`<queue>JobKeepFailed: 0` で operator が明示
// 解除可能 (= 蓄積無制限の従来挙動)。
const defaultKeepFailed = 1000

// applyClientPolicies copies enqueue-side defaults (deliverJobMaxAttempts /
// inboxJobMaxAttempts / deliverJobKeepFailed / inboxJobKeepFailed) onto the
// queue.Client. Called once at server construction so EnqueueDeliver /
// EnqueueInbox can pre-pend driver options when callers don't override.
func applyClientPolicies(c *queue.Client, cfg *config.Config) {
	c.SetPolicy(queue.QueueName, buildPolicy(cfg.DeliverJobMaxAttempts, cfg.DeliverJobKeepFailed))
	c.SetPolicy(queue.InboxQueueName, buildPolicy(cfg.InboxJobMaxAttempts, cfg.InboxJobKeepFailed))
}

// buildPolicy assembles a queue.Policy from optional config pointers,
// applying defaultKeepFailed when the operator hasn't specified a value.
// MaxAttempts は未指定なら 0 = "driver default" のまま (= 既存挙動)。
func buildPolicy(maxAttempts *int, keepFailed *int) queue.Policy {
	p := queue.Policy{}
	if maxAttempts != nil && *maxAttempts > 0 {
		p.MaxAttempts = *maxAttempts
	}
	if keepFailed != nil {
		p.KeepFailed = *keepFailed
	} else {
		p.KeepFailed = defaultKeepFailed
	}
	return p
}
