package repository

import (
	"sync"
	"time"

	"github.com/shiroha-a/mk/internal/model"
)

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

// Update delegates to the inner repository and invalidates the cache.
func (c *CachedMetaRepository) Update(fields map[string]any) error {
	err := c.inner.Update(fields)
	if err == nil {
		c.invalidate()
	}
	return err
}

// EnsureInitial delegates to the inner repository and invalidates the cache.
func (c *CachedMetaRepository) EnsureInitial(id string) error {
	err := c.inner.EnsureInitial(id)
	if err == nil {
		c.invalidate()
	}
	return err
}

func (c *CachedMetaRepository) invalidate() {
	c.mu.Lock()
	c.val = nil
	c.mu.Unlock()
}
