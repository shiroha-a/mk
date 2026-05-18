package asynqdriver

import (
	"errors"
	"sync"

	"github.com/hibiken/asynq"

	"github.com/shiroha-a/mk/internal/queue/driver"
)

// Driver bundles the four asynq sub-services into a single
// driver.Driver. Each call to a getter returns the lazily-built
// component so a Driver instance can be created without immediately
// touching Redis.
//
// All sub-component getters are safe to call concurrently — the
// underlying lazy construction is serialised via mu, mirroring the
// mkq driver's contract.
type Driver struct {
	redisOpt  asynq.RedisClientOpt
	serverCfg ServerConfig

	mu        sync.Mutex
	client    *Client
	server    *Server
	inspector *Inspector
	scheduler *Scheduler
}

// New constructs an asynq-backed driver.Driver. The redisOpt is
// shared by every sub-service; the serverCfg only affects the
// worker side (concurrency / queue priority weights).
func New(redisOpt asynq.RedisClientOpt, serverCfg ServerConfig) *Driver {
	return &Driver{redisOpt: redisOpt, serverCfg: serverCfg}
}

// Client returns the lazily-constructed driver.Client.
func (d *Driver) Client() driver.Client {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.client == nil {
		d.client = NewClient(d.redisOpt)
	}
	return d.client
}

// Server returns the lazily-constructed driver.Server.
func (d *Driver) Server() driver.Server {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.server == nil {
		d.server = NewServer(d.redisOpt, d.serverCfg)
	}
	return d.server
}

// Inspector returns the lazily-constructed driver.Inspector.
func (d *Driver) Inspector() driver.Inspector {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.inspector == nil {
		d.inspector = NewInspector(d.redisOpt)
	}
	return d.inspector
}

// Scheduler returns the lazily-constructed driver.Scheduler.
func (d *Driver) Scheduler() driver.Scheduler {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.scheduler == nil {
		d.scheduler = NewScheduler(d.redisOpt)
	}
	return d.scheduler
}

// Resize is unimplemented for asynq backend: asynq's Concurrency is
// fixed at Server construction time and the library exposes no API to
// change it at runtime. Per ADR §5.2 the auto-scale controller treats
// asynq as out-of-scope; this stub returns driver.ErrResizeNotSupported
// so callers can detect and degrade gracefully (= keep observing
// metrics, but skip the resize call).
func (d *Driver) Resize(qname string, n int) error {
	return driver.ErrResizeNotSupported
}

// WorkerCount returns the configured Concurrency for the asynq pool.
// asynq shares a single worker pool across all queues, so the same value
// is reported for every qname (the per-queue priority is handled by
// asynq internally via queue priority weights, not by separate pools).
// Before Server() is called this returns 0, matching the driver.Driver
// contract for unstarted drivers.
//
// 注: 本 driver は #1120 tracker の auto-scale 対象外 (ADR §5.2)。値は
// 静的 Config 由来で runtime 変化しない。
func (d *Driver) WorkerCount(qname string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.server == nil {
		return 0
	}
	return d.serverCfg.Concurrency
}

// Close releases every constructed sub-service. Components that
// have not been built yet are skipped. Errors are aggregated so a
// failure in one Close does not mask the others.
func (d *Driver) Close() error {
	d.mu.Lock()
	client := d.client
	inspector := d.inspector
	d.mu.Unlock()

	var errs []error
	if client != nil {
		if err := client.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if inspector != nil {
		if err := inspector.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	// Server / Scheduler は Shutdown() で停止する。Close 失敗の概念は
	// asynq 側に存在しないため、ここでは触らない (呼び出し側が
	// driver.Server.Shutdown / driver.Scheduler.Shutdown を別途呼ぶ)。
	return errors.Join(errs...)
}
