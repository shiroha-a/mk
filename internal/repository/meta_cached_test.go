package repository_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingPublisher captures MetaReloadTopic broadcasts for assertions and can
// be configured to fail to verify best-effort publish handling.
type recordingPublisher struct {
	mu       sync.Mutex
	channels []string
	err      error
}

func (p *recordingPublisher) Publish(_ context.Context, channel string, _ any) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.channels = append(p.channels, channel)
	return p.err
}

func (p *recordingPublisher) recorded() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.channels...)
}

// countingMetaRepo counts calls to each method for cache verification.
type countingMetaRepo struct {
	fetchCount       atomic.Int32
	updateCount      atomic.Int32
	ensureCount      atomic.Int32
	meta             *model.Meta
	fetchErr         error
	updateErr        error
	ensureInitialErr error
}

func (r *countingMetaRepo) Fetch() (*model.Meta, error) {
	r.fetchCount.Add(1)
	if r.fetchErr != nil {
		return nil, r.fetchErr
	}
	return r.meta, nil
}

func (r *countingMetaRepo) Update(fields map[string]any) error {
	r.updateCount.Add(1)
	return r.updateErr
}

func (r *countingMetaRepo) EnsureInitial(_ string) error {
	r.ensureCount.Add(1)
	return r.ensureInitialErr
}

func TestCachedMetaRepository_CacheHit(t *testing.T) {
	inner := &countingMetaRepo{meta: &model.Meta{ID: "m1"}}
	cached := repository.NewCachedMetaRepositoryWithTTL(inner, 1*time.Minute)

	m1, err := cached.Fetch()
	require.NoError(t, err)
	assert.Equal(t, "m1", m1.ID)

	m2, err := cached.Fetch()
	require.NoError(t, err)
	assert.Equal(t, "m1", m2.ID)

	// inner.Fetchは1回だけ呼ばれる
	assert.Equal(t, int32(1), inner.fetchCount.Load())
}

func TestCachedMetaRepository_TTLExpiry(t *testing.T) {
	inner := &countingMetaRepo{meta: &model.Meta{ID: "m1"}}
	cached := repository.NewCachedMetaRepositoryWithTTL(inner, 10*time.Millisecond)

	_, err := cached.Fetch()
	require.NoError(t, err)
	assert.Equal(t, int32(1), inner.fetchCount.Load())

	// TTL超過を待つ
	time.Sleep(20 * time.Millisecond)

	_, err = cached.Fetch()
	require.NoError(t, err)
	assert.Equal(t, int32(2), inner.fetchCount.Load())
}

func TestCachedMetaRepository_UpdateInvalidates(t *testing.T) {
	inner := &countingMetaRepo{meta: &model.Meta{ID: "m1"}}
	cached := repository.NewCachedMetaRepositoryWithTTL(inner, 1*time.Minute)

	_, err := cached.Fetch()
	require.NoError(t, err)
	assert.Equal(t, int32(1), inner.fetchCount.Load())

	err = cached.Update(map[string]any{"name": "new"})
	require.NoError(t, err)

	// Update後のFetchは再取得する
	_, err = cached.Fetch()
	require.NoError(t, err)
	assert.Equal(t, int32(2), inner.fetchCount.Load())
}

func TestCachedMetaRepository_EnsureInitialInvalidates(t *testing.T) {
	inner := &countingMetaRepo{meta: &model.Meta{ID: "m1"}}
	cached := repository.NewCachedMetaRepositoryWithTTL(inner, 1*time.Minute)

	_, err := cached.Fetch()
	require.NoError(t, err)

	err = cached.EnsureInitial("m1")
	require.NoError(t, err)

	_, err = cached.Fetch()
	require.NoError(t, err)
	assert.Equal(t, int32(2), inner.fetchCount.Load())
}

func TestCachedMetaRepository_ErrorNotCached(t *testing.T) {
	fetchErr := errors.New("db error")
	inner := &countingMetaRepo{fetchErr: fetchErr}
	cached := repository.NewCachedMetaRepositoryWithTTL(inner, 1*time.Minute)

	_, err := cached.Fetch()
	require.ErrorIs(t, err, fetchErr)

	// エラー時はキャッシュされないので次のFetchも再取得する
	_, err = cached.Fetch()
	require.ErrorIs(t, err, fetchErr)
	assert.Equal(t, int32(2), inner.fetchCount.Load())
}

func TestCachedMetaRepository_UpdateErrorDoesNotInvalidate(t *testing.T) {
	inner := &countingMetaRepo{
		meta:      &model.Meta{ID: "m1"},
		updateErr: errors.New("update failed"),
	}
	cached := repository.NewCachedMetaRepositoryWithTTL(inner, 1*time.Minute)

	_, err := cached.Fetch()
	require.NoError(t, err)

	err = cached.Update(map[string]any{"name": "new"})
	require.Error(t, err)

	// Update失敗時はキャッシュを保持する
	_, err = cached.Fetch()
	require.NoError(t, err)
	assert.Equal(t, int32(1), inner.fetchCount.Load())
}

func TestCachedMetaRepository_ConcurrentFetch(t *testing.T) {
	inner := &countingMetaRepo{meta: &model.Meta{ID: "m1"}}
	cached := repository.NewCachedMetaRepositoryWithTTL(inner, 1*time.Minute)

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m, err := cached.Fetch()
			assert.NoError(t, err)
			assert.Equal(t, "m1", m.ID)
		}()
	}
	wg.Wait()

	// ダブルチェックロックにより1〜数回のFetchで済む
	assert.LessOrEqual(t, inner.fetchCount.Load(), int32(3))
}

// #1533: Update は他プロセスの cache を捨てさせるため MetaReloadTopic を
// broadcast する。
func TestCachedMetaRepository_UpdateBroadcastsReload(t *testing.T) {
	inner := &countingMetaRepo{meta: &model.Meta{ID: "m1"}}
	cached := repository.NewCachedMetaRepositoryWithTTL(inner, 1*time.Minute)
	pub := &recordingPublisher{}
	cached.SetReloadPublisher(pub)

	require.NoError(t, cached.Update(map[string]any{"name": "new"}))

	assert.Equal(t, []string{repository.MetaReloadTopic}, pub.recorded())
}

// #1533: publish 失敗は admin update を失敗させず、local cache invalidate も
// そのまま行われる (best-effort broadcast)。
func TestCachedMetaRepository_UpdatePublishErrorIsNonFatal(t *testing.T) {
	inner := &countingMetaRepo{meta: &model.Meta{ID: "m1"}}
	cached := repository.NewCachedMetaRepositoryWithTTL(inner, 1*time.Minute)
	cached.SetReloadPublisher(&recordingPublisher{err: errors.New("redis down")})

	_, err := cached.Fetch()
	require.NoError(t, err)
	require.Equal(t, int32(1), inner.fetchCount.Load())

	require.NoError(t, cached.Update(map[string]any{"name": "new"}))

	// publish が失敗しても cache は invalidate され、次の Fetch は再取得する。
	_, err = cached.Fetch()
	require.NoError(t, err)
	assert.Equal(t, int32(2), inner.fetchCount.Load())
}

// #1533: EnsureInitial は startup seed なので broadcast しない。
func TestCachedMetaRepository_EnsureInitialDoesNotBroadcast(t *testing.T) {
	inner := &countingMetaRepo{meta: &model.Meta{ID: "m1"}}
	cached := repository.NewCachedMetaRepositoryWithTTL(inner, 1*time.Minute)
	pub := &recordingPublisher{}
	cached.SetReloadPublisher(pub)

	require.NoError(t, cached.EnsureInitial("m1"))

	assert.Empty(t, pub.recorded())
}

// #1533: cross-process subscriber が呼ぶ Invalidate() は次の Fetch を強制再取得
// させる。
func TestCachedMetaRepository_InvalidateBustsCache(t *testing.T) {
	inner := &countingMetaRepo{meta: &model.Meta{ID: "m1"}}
	cached := repository.NewCachedMetaRepositoryWithTTL(inner, 1*time.Minute)

	_, err := cached.Fetch()
	require.NoError(t, err)
	require.Equal(t, int32(1), inner.fetchCount.Load())

	cached.Invalidate()

	_, err = cached.Fetch()
	require.NoError(t, err)
	assert.Equal(t, int32(2), inner.fetchCount.Load())
}

// #739: NewCachedMetaRepository (default 5-min TTL constructor) を coverage 化。
// 中身は WithTTL と同じだが default TTL 経路が踏まれていなかった。
func TestNewCachedMetaRepository_DefaultTTL(t *testing.T) {
	inner := &countingMetaRepo{
		meta: &model.Meta{ID: "m1"},
	}
	cached := repository.NewCachedMetaRepository(inner)
	m, err := cached.Fetch()
	require.NoError(t, err)
	assert.Equal(t, "m1", m.ID)
}
