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

// defaultKeepCompleted は completed bucket retention の最大件数 default。
// Misskey TS upstream の QueueService.ts が全 enqueue で `removeOnComplete:
// {age: 7d, count: 30}` を指定しているのに合わせて 30 件に揃える。BullMQ
// default の「無制限保持」が UDS 観測で実害 (inbox completed 15K+ 蓄積) を
// 出していたためここで切る (#1193)。`<queue>JobKeepCompleted: 0` で
// operator が明示解除可能 (= TS と挙動を意図的にずらしたい場合)。
const defaultKeepCompleted = 30

// defaultKeepCompletedAge / defaultKeepFailedAge は age-based retention の
// default。BullMQ TS upstream と同じ 7 日。count 上限と組み合わせて
// 「7 日 以内 かつ 最新 N 件」を保持する (BullMQ TS の object form 互換)。
const (
	defaultKeepCompletedAge = 7 * 24 * time.Hour
	defaultKeepFailedAge    = 7 * 24 * time.Hour
)

// applyClientPolicies copies enqueue-side defaults onto the queue.Client.
// Called once at server construction so EnqueueDeliver / EnqueueInbox can
// pre-pend driver options when callers don't override.
//
// Policy 設定は deliver / inbox / export / push / webhook の application
// queue 全てに対して適用する。Misskey TS upstream の QueueService.ts が
// 全 enqueue helper で `removeOnComplete: {age: 7d, count: 30}` を指定して
// いる規約に合わせるため (#1193)。maintenance queue は Scheduler.Register
// 経由で mkq native の per-fire option を上流仕様で drop するため本 PR では
// 対象外 (mkq upstream 拡張後に follow-up)。
func applyClientPolicies(c *queue.Client, cfg *config.Config) {
	c.SetPolicy(queue.QueueName, buildPolicy(cfg.DeliverJobMaxAttempts, cfg.DeliverJobKeepFailed, cfg.DeliverJobKeepCompleted))
	c.SetPolicy(queue.InboxQueueName, buildPolicy(cfg.InboxJobMaxAttempts, cfg.InboxJobKeepFailed, cfg.InboxJobKeepCompleted))
	// export / push / webhook 用の YAML key は意図的に増やさない (TS と
	// 同じく一律の default を踏ませれば十分)。tuning 用 hook が必要に
	// なったら deliver/inbox と同じ pattern で *KeepCompleted / *KeepFailed
	// を追加すれば対応可能。
	c.SetPolicy(queue.ExportQueueName, defaultPolicy())
	c.SetPolicy(queue.PushQueueName, defaultPolicy())
	c.SetPolicy(queue.WebhookQueueName, defaultPolicy())
}

// defaultPolicy returns the queue.Policy applied when no operator config
// override exists for the queue. すべての retention default (KeepFailed /
// KeepCompleted / KeepCompletedAge / KeepFailedAge) を含む。tuning hook
// が無い queue (export / push / webhook) で applyClientPolicies が一律
// 適用するために存在する。
func defaultPolicy() queue.Policy {
	return buildPolicy(nil, nil, nil)
}

// buildPolicy assembles a queue.Policy from optional config pointers,
// applying default retention values when the operator hasn't specified
// them. MaxAttempts は未指定なら 0 = "driver default" のまま (= 既存挙動)。
//
// KeepFailed / KeepCompleted は nil で「default 適用」、明示 0 で「retention
// 無し (operator opt-out)」、明示 N で「N 件まで保持」の 3 状態を表す。
// Age 値 (KeepCompletedAge / KeepFailedAge) は operator が tuning する用途が
// 薄いため YAML key を増やさず TS と同じ 7d 固定で適用する。
func buildPolicy(maxAttempts *int, keepFailed *int, keepCompleted *int) queue.Policy {
	p := queue.Policy{}
	if maxAttempts != nil && *maxAttempts > 0 {
		p.MaxAttempts = *maxAttempts
	}
	if keepFailed != nil {
		p.KeepFailed = *keepFailed
	} else {
		p.KeepFailed = defaultKeepFailed
	}
	if keepCompleted != nil {
		p.KeepCompleted = *keepCompleted
	} else {
		p.KeepCompleted = defaultKeepCompleted
	}
	p.KeepCompletedAge = defaultKeepCompletedAge
	p.KeepFailedAge = defaultKeepFailedAge
	return p
}
