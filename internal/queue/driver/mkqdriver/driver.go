package mkqdriver

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/redis/go-redis/v9"
	"github.com/shiroha-a/mkq"

	"github.com/shiroha-a/mk/internal/queue/driver"
)

// QueueNames is the set of logical queues mkqdriver pre-defines at
// startup. New queue names introduced in mk-go must be added here so
// the corresponding mkq.Queue handle is created and a worker is
// spawned for it. The list mirrors the Queues map asynqdriver Server
// configures.
var QueueNames = []string{
	"deliver",
	"inbox",
	"push",
	"export",
	"webhook",
	"maintenance",
}

// Config is the per-driver configuration the constructor consumes.
type Config struct {
	// Redis is forwarded to mkq.Config.Redis. Construct it via
	// BuildRedisOptions.
	Redis redis.UniversalOptions

	// KeyPrefix overrides BullMQ's "bull" default. Empty string keeps
	// the BullMQ default — recommended unless the same Redis hosts
	// multiple BullMQ deployments.
	KeyPrefix string

	// Concurrency is the **total** worker budget across all queues,
	// matching asynq.Config.Concurrency semantics. Driver.Server
	// divides this by len(QueueNames) (clamped to a minimum of 1)
	// before passing to mkq.WithConcurrency on a per-queue basis.
	// Zero falls back to 16, matching the historical asynq default.
	Concurrency int

	// QueueNames overrides the set of queues to pre-define. Nil/empty
	// keeps the package default (QueueNames).
	QueueNames []string

	// MaxMetricsDataPoints caps the number of one-minute buckets the
	// per-queue completed / failed metrics list keeps. The zero value
	// applies `defaultMaxMetricsDataPoints` (= MetricsTime.ONE_WEEK in
	// BullMQ TS, what Misskey TS upstream uses), so admin UIs see
	// drop-in compatible chart data when sharing a Redis with a TS
	// Misskey instance. Set to a negative value to opt out entirely;
	// no BullMQ metrics keys are written in that case (mkq's default
	// behaviour pre-v1.0.1).
	MaxMetricsDataPoints int

	// QueueConcurrency overrides the per-queue worker pool size for
	// the named queue. Zero / missing entries fall back to the default
	// `total / len(queues)` distribution computed from Concurrency.
	// Used by config keys like `deliverJobConcurrency` to tune the
	// hot deliver queue independently from the rest (#495).
	QueueConcurrency map[string]int

	// QueueRateLimits caps each named queue at N tasks/sec via
	// mkq.WithRateLimit. Zero / missing entries disable the limiter
	// for that queue.
	QueueRateLimits map[string]int
}

// defaultMaxMetricsDataPoints mirrors BullMQ TS's
// `MetricsTime.ONE_WEEK = 10080` and is the value Misskey TS upstream
// applies via baseWorkerOptions. Keeping the same retention here
// preserves chart history across drop-in TS↔mk-go swaps that share
// the Redis instance.
const defaultMaxMetricsDataPoints = 10080

// Driver bundles the Client / Server / Inspector / Scheduler that
// share one *mkq.Client instance. New connects + script-loads against
// Redis; close the Driver to release the connection pool.
type Driver struct {
	client *mkq.Client
	cfg    Config
	queues map[string]*mkq.Queue[framedPayload]

	// rdb is a side-channel redis client used by the Inspector for
	// ad-hoc reads that mkq's public API does not expose (currently
	// the per-queue `repeat` ZSET — see Inspector.GetQueueInfo). mkq's
	// embedded *redis.UniversalClient is unexported, so duplicating
	// the connection here keeps mkqdriver decoupled from mkq internals.
	// keyPrefix mirrors mkq.Config.KeyPrefix with the BullMQ default
	// ("bull") substituted for empty strings, so callers can build keys
	// without re-checking config.
	rdb       redis.UniversalClient
	keyPrefix string

	// Sub-components are constructed lazily on first access.
	mu      sync.Mutex
	dClient *Client
	dServer *Server
	dIns    *Inspector
	dSched  *Scheduler
	closed  bool
}

// New connects to Redis, preloads mkq's vendored Lua scripts, and
// pre-defines the configured queues. The returned Driver owns the
// underlying *mkq.Client and must be Close'd to release resources.
//
// The supplied context bounds the connection setup phase only; once
// New returns, the connection is shared by the driver's sub-services
// for the rest of their lifetime.
func New(ctx context.Context, cfg Config) (*Driver, error) {
	mkqCfg := mkq.Config{Redis: cfg.Redis, KeyPrefix: cfg.KeyPrefix}
	client, err := mkq.NewClient(ctx, mkqCfg)
	if err != nil {
		return nil, fmt.Errorf("mkqdriver: connect: %w", err)
	}

	// side-channel redis client (see Driver.rdb doc). PING is verified
	// implicitly via mkq.NewClient above against the same Addrs, so a
	// second probe here is redundant — let the first ZCARD surface
	// connectivity errors instead.
	rdb := redis.NewUniversalClient(&cfg.Redis)
	keyPrefix := cfg.KeyPrefix
	if keyPrefix == "" {
		keyPrefix = defaultKeyPrefix
	}

	names := cfg.QueueNames
	if len(names) == 0 {
		names = QueueNames
	}
	queues := make(map[string]*mkq.Queue[framedPayload], len(names))
	for _, n := range names {
		queues[n] = mkq.Define[framedPayload](client, n)
	}

	return &Driver{
		client:    client,
		cfg:       cfg,
		queues:    queues,
		rdb:       rdb,
		keyPrefix: keyPrefix,
	}, nil
}

// defaultKeyPrefix mirrors mkq's BullMQ-compatible default. Duplicated
// here (rather than imported from mkq) because mkq exposes the constant
// only via Config field semantics, not as a public symbol.
const defaultKeyPrefix = "bull"

// repeatKey returns the BullMQ ZSET key that holds the "next-fire"
// schedule for repeat jobs on the named queue. Layout: `{prefix}:{queue}:repeat`.
func (d *Driver) repeatKey(queue string) string {
	return d.keyPrefix + ":" + queue + ":repeat"
}

// queueFor returns the pre-defined queue for the given name, or nil.
func (d *Driver) queueFor(name string) *mkq.Queue[framedPayload] {
	return d.queues[name]
}

// Client returns the lazily-constructed driver.Client.
func (d *Driver) Client() driver.Client {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.dClient == nil {
		d.dClient = &Client{driver: d}
	}
	return d.dClient
}

// Server returns the lazily-constructed driver.Server.
//
// cfg.Concurrency is interpreted as a **total** worker budget across
// all queues (matching asynq.Config.Concurrency semantics). mkq
// applies WithConcurrency per queue, so the per-queue value is
// `total / len(queues)` clamped to a minimum of 1 — without this
// scaling an operator setting `deliverJobConcurrency: 16` would get
// 80 goroutines (5 queues × 16) on the mkq driver, vs 16 on the
// asynq driver, surprising operators migrating between the two.
func (d *Driver) Server() driver.Server {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.dServer == nil {
		total := d.cfg.Concurrency
		if total <= 0 {
			total = 16
		}
		queues := len(d.queues)
		if queues < 1 {
			queues = 1
		}
		perQueue := total / queues
		if perQueue < 1 {
			perQueue = 1
		}
		metricsPoints := d.cfg.MaxMetricsDataPoints
		if metricsPoints == 0 {
			metricsPoints = defaultMaxMetricsDataPoints
		}
		d.dServer = &Server{
			driver:             d,
			concurrency:        perQueue,
			maxMetricsPoints:   metricsPoints,
			perQueueConcurrent: d.cfg.QueueConcurrency,
			perQueueRate:       d.cfg.QueueRateLimits,
			handlers:           make(map[string]driver.HandlerFunc),
		}
	}
	return d.dServer
}

// Inspector returns the lazily-constructed driver.Inspector.
func (d *Driver) Inspector() driver.Inspector {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.dIns == nil {
		d.dIns = &Inspector{driver: d}
	}
	return d.dIns
}

// Scheduler returns the lazily-constructed driver.Scheduler.
func (d *Driver) Scheduler() driver.Scheduler {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.dSched == nil {
		d.dSched = &Scheduler{driver: d}
	}
	return d.dSched
}

// WorkerCount returns the per-queue worker pool size. Before Server() is
// called the Server has not been constructed and we return 0 (matches the
// driver.Driver contract for unstarted drivers). After construction,
// per-queue overrides (`QueueConcurrency`) take precedence over the
// default `concurrency` derived from `cfg.Concurrency / len(queues)`.
//
// Auto-scale (#1120 tracker) will mutate the underlying pool size at
// runtime via a future Resize API (#1124); until then this returns the
// static config value.
func (d *Driver) WorkerCount(qname string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.dServer == nil {
		return 0
	}
	if v, ok := d.dServer.perQueueConcurrent[qname]; ok && v > 0 {
		return v
	}
	return d.dServer.concurrency
}

// Close stops the worker (if started) and releases the underlying
// *mkq.Client. Idempotent: subsequent calls are no-ops, matching the
// asynq driver's contract and tolerating double-close from layered
// shutdown hooks.
func (d *Driver) Close() error {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return nil
	}
	d.closed = true
	srv := d.dServer
	d.mu.Unlock()

	if srv != nil {
		srv.Shutdown()
	}
	// closed フラグを true にしてから cleanup を始めているため (上の
	// idempotent ガード)、途中で early return すると残りのリソースが
	// 永久リークする。client / rdb の両方を必ず Close し、エラーは
	// errors.Join で集約して返す。
	var clientErr, rdbErr error
	if err := d.client.Close(); err != nil {
		clientErr = fmt.Errorf("mkqdriver: close client: %w", err)
	}
	if d.rdb != nil {
		// 副 Redis client は GetQueueInfo の ZCARD 専用。失敗しても
		// 主要シャットダウン経路を blocking させる必要はないが、リーク
		// 防止のため errors.Join 経由で呼び出し側に返しておく。
		if err := d.rdb.Close(); err != nil {
			rdbErr = fmt.Errorf("mkqdriver: close rdb: %w", err)
		}
	}
	return errors.Join(clientErr, rdbErr)
}
