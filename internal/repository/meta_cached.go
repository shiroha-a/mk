package repository

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/shiroha-a/mk/internal/model"
)

// MetaReloadTopic is the Redis pubsub topic on which meta cache invalidation
// is broadcast across processes (#1533). It mirrors the upstream Misskey
// MetaService "metaUpdated" internal event: when admin/update-meta changes the
// singleton meta row in one process, every other process must drop its local
// cache so it stops serving stale values (e.g. the SSR boot meta's
// googleAnalyticsMeasurementId). Without this, a non-updating process keeps the
// old meta for up to the cache TTL and clients keep loading the removed GA tag.
const MetaReloadTopic = "meta:reload"

// MetaReloadPayload is the JSON wire format for MetaReloadTopic. The body
// carries no data today (the signal alone triggers a local invalidate); the
// struct exists so future reload variants can extend it without a wire break.
type MetaReloadPayload struct{}

// MetaReloadPublisher broadcasts meta cache invalidation to other processes.
// core/event.PubSubService satisfies this structurally, so the repository layer
// does not import the pubsub package directly (dependency inversion keeps the
// repository -> core import direction from being reversed).
type MetaReloadPublisher interface {
	Publish(ctx context.Context, channel string, payload any) error
}

// CachedMetaRepository wraps a MetaRepository with a time-based in-memory
// cache. The meta table is a singleton row that rarely changes (admin-only
// updates), so a short TTL eliminates repeated DB round-trips on hot paths
// such as federation host blocking checks.
type CachedMetaRepository struct {
	inner MetaRepository
	ttl   time.Duration

	mu  sync.RWMutex
	val *model.Meta
	at  time.Time

	// publisher broadcasts MetaReloadTopic on Update so sibling processes drop
	// their cache (#1533). nil = single-process / no broadcast (Update still
	// invalidates the local cache).
	publisher MetaReloadPublisher
}

// SetReloadPublisher wires the cross-process invalidation broadcaster. It is
// called once at startup (server router) after the pubsub service exists.
func (c *CachedMetaRepository) SetReloadPublisher(p MetaReloadPublisher) {
	c.publisher = p
}

// NewCachedMetaRepository creates a cached wrapper with a 5-minute TTL.
func NewCachedMetaRepository(inner MetaRepository) MetaRepository {
	return &CachedMetaRepository{inner: inner, ttl: 5 * time.Minute}
}

// NewCachedMetaRepositoryWithTTL creates a cached wrapper with a custom TTL.
func NewCachedMetaRepositoryWithTTL(inner MetaRepository, ttl time.Duration) *CachedMetaRepository {
	return &CachedMetaRepository{inner: inner, ttl: ttl}
}

// Fetch returns the cached meta if still valid, otherwise fetches from DB.
// 返却される *model.Meta はキャッシュ内部と同一ポインタのため、呼び出し側で
// フィールドを変更してはならない（read-only）。
func (c *CachedMetaRepository) Fetch() (*model.Meta, error) {
	c.mu.RLock()
	if c.val != nil && time.Since(c.at) < c.ttl {
		v := c.val
		c.mu.RUnlock()
		return v, nil
	}
	c.mu.RUnlock()

	c.mu.Lock()
	defer c.mu.Unlock()
	// ダブルチェック: RUnlock〜Lock間に別goroutineがフェッチ済みの場合
	if c.val != nil && time.Since(c.at) < c.ttl {
		return c.val, nil
	}
	m, err := c.inner.Fetch()
	if err != nil {
		return nil, err
	}
	c.val = m
	c.at = time.Now()
	return m, nil
}

// Update delegates to the inner repository and invalidates the cache. On
// success it also broadcasts MetaReloadTopic so other processes invalidate
// their caches (#1533). The broadcast is best-effort: a publish failure must
// not fail the admin update (the local cache is already correct), so the error
// is only logged.
func (c *CachedMetaRepository) Update(fields map[string]any) error {
	err := c.inner.Update(fields)
	if err != nil {
		return err
	}
	c.Invalidate()
	if c.publisher != nil {
		if perr := c.publisher.Publish(context.Background(), MetaReloadTopic, MetaReloadPayload{}); perr != nil {
			slog.Warn("meta: reload broadcast failed", "err", perr)
		}
	}
	return nil
}

// EnsureInitial delegates to the inner repository and invalidates the cache.
// It does not broadcast: this runs at startup to seed the singleton row, and
// every process performs it independently, so there is no stale cache to bust
// elsewhere.
func (c *CachedMetaRepository) EnsureInitial(id string) error {
	err := c.inner.EnsureInitial(id)
	if err == nil {
		c.Invalidate()
	}
	return err
}

// Invalidate drops the cached meta so the next Fetch reloads from the inner
// repository. It is exported so the cross-process reload subscriber can call it
// when a MetaReloadTopic message arrives from another process.
func (c *CachedMetaRepository) Invalidate() {
	c.mu.Lock()
	c.val = nil
	c.mu.Unlock()
}
