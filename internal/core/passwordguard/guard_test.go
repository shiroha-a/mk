package passwordguard

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestGuard(t *testing.T) (*RedisGuard, *miniredis.Miniredis, *time.Time) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	g := NewRedisGuard(rdb)
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	g.now = func() time.Time { return now }
	return g, mr, &now
}

func failN(t *testing.T, g *RedisGuard, userID string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		_, err := g.Begin(context.Background(), userID)
		require.NoError(t, err, "attempt %d", i+1)
	}
}

func TestRedisGuard_BlocksAfterMaxFailures(t *testing.T) {
	g, _, _ := newTestGuard(t)
	failN(t, g, "u1", DefaultMaxFailures)

	_, err := g.Begin(context.Background(), "u1")
	require.ErrorIs(t, err, ErrTooManyFailures)
	var le *LimitedError
	require.ErrorAs(t, err, &le)
	assert.Equal(t, DefaultWindow, le.RetryAfter)
	assert.Equal(t, ErrTooManyFailures.Error(), le.Error())

	// 別アカウントの枠は独立している。
	_, err = g.Begin(context.Background(), "u2")
	require.NoError(t, err)
}

// 照合に成功した (または照合しなかった) 試行は失敗として数えない。
func TestRedisGuard_ReleasedAttemptsDoNotCount(t *testing.T) {
	g, _, _ := newTestGuard(t)
	for i := 0; i < DefaultMaxFailures*3; i++ {
		a, err := g.Begin(context.Background(), "u1")
		require.NoError(t, err)
		a.Release(context.Background())
	}
	failN(t, g, "u1", DefaultMaxFailures)
}

// 拒否した試行は照合していないので数えない。数えると 429 を無視して叩き
// 続けるだけで窓が押し戻され続ける。
func TestRedisGuard_RejectedAttemptsDoNotExtendTheWindow(t *testing.T) {
	g, _, now := newTestGuard(t)
	failN(t, g, "u1", DefaultMaxFailures)
	for i := 0; i < 5; i++ {
		*now = now.Add(10 * time.Minute)
		_, err := g.Begin(context.Background(), "u1")
		require.ErrorIs(t, err, ErrTooManyFailures)
	}
	*now = now.Add(10*time.Minute + time.Second) // 最初の失敗から 1 時間を過ぎた
	_, err := g.Begin(context.Background(), "u1")
	require.NoError(t, err)
}

func TestRedisGuard_RetryAfterFollowsOldestFailure(t *testing.T) {
	g, _, now := newTestGuard(t)
	failN(t, g, "u1", 1)
	*now = now.Add(20 * time.Minute)
	failN(t, g, "u1", DefaultMaxFailures-1)
	_, err := g.Begin(context.Background(), "u1")
	var le *LimitedError
	require.ErrorAs(t, err, &le)
	assert.Equal(t, 40*time.Minute, le.RetryAfter)
}

// 並行で投げても照合に進めるのは上限まで (bcrypt の照合中に残り枠を
// 見て全部通る形を塞ぐ)。
func TestRedisGuard_ConcurrentReservationsStopAtMax(t *testing.T) {
	g, _, _ := newTestGuard(t)
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		allowed int
	)
	for i := 0; i < DefaultMaxFailures*4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := g.Begin(context.Background(), "u1"); err == nil {
				mu.Lock()
				allowed++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	assert.Equal(t, DefaultMaxFailures, allowed)
}

func TestRedisGuard_StoreErrorIsReturned(t *testing.T) {
	g, mr, _ := newTestGuard(t)
	mr.Close()
	_, err := g.Begin(context.Background(), "u1")
	require.Error(t, err)
	assert.False(t, errors.Is(err, ErrTooManyFailures))
}

func TestRedisGuard_NilIsNoop(t *testing.T) {
	var g *RedisGuard
	a, err := g.Begin(context.Background(), "u1")
	require.NoError(t, err)
	a.Release(context.Background())
	NoopAttempt().Release(context.Background())
}

func TestFailureKey(t *testing.T) {
	assert.Equal(t, "passwordguard:failures:u1", failureKey("u1"))
}
