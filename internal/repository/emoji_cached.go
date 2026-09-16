package repository

import (
	"sync"
	"time"

	"github.com/shiroha-a/mk/internal/model"
)

// CachedEmojiRepository wraps an EmojiRepository with a time-based in-memory
// cache for the ListLocal hot path. The /api/emojis endpoint is hit by every
// timeline render (frontend caches client-side, but cold loads still flood
// it), and ListLocal performs a full table scan with ORDER BY each time.
// Local emoji set rarely changes (admin-only writes), so a short TTL plus
// invalidation on mutation gives near-perfect cache hit rate while keeping
// staleness bounded (#300 3-6).
type CachedEmojiRepository struct {
	inner EmojiRepository
	ttl   time.Duration

	mu    sync.RWMutex
	local []*model.Emoji
	at    time.Time
}

// NewCachedEmojiRepository wraps inner with a 5-minute TTL. Mutations through
// this wrapper invalidate the cache immediately, so the TTL only matters for
// out-of-band writes (e.g. another mk instance sharing the same DB).
func NewCachedEmojiRepository(inner EmojiRepository) EmojiRepository {
	return &CachedEmojiRepository{inner: inner, ttl: 5 * time.Minute}
}

// NewCachedEmojiRepositoryWithTTL is the test-friendly constructor with an
// explicit TTL.
func NewCachedEmojiRepositoryWithTTL(inner EmojiRepository, ttl time.Duration) *CachedEmojiRepository {
	return &CachedEmojiRepository{inner: inner, ttl: ttl}
}

// ListLocal returns the cached local emoji slice if still valid, otherwise
// fetches from the inner repo. The returned slice is the cache-internal
// pointer; callers must treat it as read-only (never mutate elements or
// the slice itself).
func (c *CachedEmojiRepository) ListLocal() ([]*model.Emoji, error) {
	c.mu.RLock()
	// "キャッシュ済みかどうか" は c.at が non-zero かどうかで判定する。
	// inner.ListLocal() は emoji table が空のとき GORM 由来で (nil, nil)
	// を返す可能性があり、`c.local != nil` で判定すると空テーブルの
	// instance で cache が永久に効かなくなる (Devin #541 BUG-1)。
	if !c.at.IsZero() && time.Since(c.at) < c.ttl {
		v := c.local
		c.mu.RUnlock()
		return v, nil
	}
	c.mu.RUnlock()

	c.mu.Lock()
	defer c.mu.Unlock()
	// double-check: RUnlock〜Lock 間に別 goroutine がフェッチ済みの場合
	if !c.at.IsZero() && time.Since(c.at) < c.ttl {
		return c.local, nil
	}
	list, err := c.inner.ListLocal()
	if err != nil {
		return nil, err
	}
	c.local = list
	c.at = time.Now()
	return list, nil
}

// Invalidate drops the cached ListLocal result. Public so out-of-band paths
// (e.g. emoji import) can force a refresh without going through the wrapper.
func (c *CachedEmojiRepository) Invalidate() {
	c.mu.Lock()
	c.local = nil
	c.at = time.Time{}
	c.mu.Unlock()
}

func (c *CachedEmojiRepository) invalidate() {
	c.Invalidate()
}

// --- mutating methods: delegate then invalidate ----------------------------

func (c *CachedEmojiRepository) Create(e *model.Emoji) error {
	if err := c.inner.Create(e); err != nil {
		return err
	}
	c.invalidate()
	return nil
}

func (c *CachedEmojiRepository) UpdateFields(id string, fields map[string]any) error {
	if err := c.inner.UpdateFields(id, fields); err != nil {
		return err
	}
	c.invalidate()
	return nil
}

func (c *CachedEmojiRepository) UpdateFieldsMany(ids []string, fields map[string]any) error {
	if err := c.inner.UpdateFieldsMany(ids, fields); err != nil {
		return err
	}
	c.invalidate()
	return nil
}

func (c *CachedEmojiRepository) Delete(id string) error {
	if err := c.inner.Delete(id); err != nil {
		return err
	}
	c.invalidate()
	return nil
}

func (c *CachedEmojiRepository) DeleteMany(ids []string) error {
	if err := c.inner.DeleteMany(ids); err != nil {
		return err
	}
	c.invalidate()
	return nil
}

// --- read-only methods: direct delegate ------------------------------------

func (c *CachedEmojiRepository) FindByNameAndHost(name string, host *string) (*model.Emoji, error) {
	if !storable(name) || (host != nil && !storable(*host)) {
		return nil, ErrNotFound
	}
	return c.inner.FindByNameAndHost(name, host)
}

func (c *CachedEmojiRepository) FindByID(id string) (*model.Emoji, error) {
	if !storable(id) {
		return nil, ErrNotFound
	}
	return c.inner.FindByID(id)
}

func (c *CachedEmojiRepository) FindManyByIDs(ids []string) ([]*model.Emoji, error) {
	ids = storableIDs(ids)
	return c.inner.FindManyByIDs(ids)
}

func (c *CachedEmojiRepository) FindManyByNamesAndHost(names []string, host *string) ([]*model.Emoji, error) {
	return c.inner.FindManyByNamesAndHost(names, host)
}

func (c *CachedEmojiRepository) ListWithFilter(query, category string, local bool, sinceID, untilID string, limit, offset int) ([]*model.Emoji, error) {
	return c.inner.ListWithFilter(query, category, local, sinceID, untilID, limit, offset)
}

func (c *CachedEmojiRepository) ListRemoteWithFilter(query, host, sinceID, untilID string, limit, offset int) ([]*model.Emoji, error) {
	return c.inner.ListRemoteWithFilter(query, host, sinceID, untilID, limit, offset)
}

func (c *CachedEmojiRepository) ListV2(filter model.EmojiV2Filter) ([]*model.Emoji, error) {
	return c.inner.ListV2(filter)
}

func (c *CachedEmojiRepository) CountV2(filter model.EmojiV2Filter) (int64, error) {
	return c.inner.CountV2(filter)
}
