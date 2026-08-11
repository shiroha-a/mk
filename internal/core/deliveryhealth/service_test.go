package deliveryhealth

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestService(t *testing.T, interval time.Duration) *Service {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return NewService(NewStore(rdb), 0, interval)
}

// flush 周期が来るまで Redis を叩かない。配送 1 件ごとに往復すると、高頻度な
// hot path で無視できない負荷になる。
func TestService_DoesNotHitRedisBetweenFlushes(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	s := NewService(NewStore(rdb), 0, time.Hour)

	for i := 0; i < 100; i++ {
		s.RecordDelivery("a.example", Outcome{Class: ClassSuccess})
	}
	assert.Empty(t, mr.Keys(), "flush 前は Redis に何も書かない")

	got, err := s.Query(context.Background(), time.Hour)
	require.NoError(t, err)
	assert.Empty(t, got)
}

// Stop は最後に 1 回 flush する。停止直前の数十秒が丸ごと消えるのを避ける。
func TestService_StopFlushesRemaining(t *testing.T) {
	s := newTestService(t, time.Hour)
	s.Start(context.Background())
	s.RecordDelivery("a.example", Outcome{Class: ClassSuccess, Latency: 10 * time.Millisecond})
	s.Stop(context.Background())

	got, err := s.Query(context.Background(), time.Hour)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, int64(1), got[0].Success)
}

// 周期が来れば自動で flush される。
func TestService_PeriodicFlush(t *testing.T) {
	s := newTestService(t, 20*time.Millisecond)
	s.Start(context.Background())
	t.Cleanup(func() { s.Stop(context.Background()) })

	s.RecordDelivery("a.example", Outcome{Class: ClassServerError, Status: 500, Err: "boom"})

	require.Eventually(t, func() bool {
		got, err := s.Query(context.Background(), time.Hour)
		return err == nil && len(got) == 1 && got[0].Failure == 1
	}, 3*time.Second, 20*time.Millisecond)
}

// Start の二重呼び出しで goroutine が二重に回らないこと。
func TestService_StartTwiceIsNoOp(t *testing.T) {
	s := newTestService(t, time.Hour)
	s.Start(context.Background())
	s.Start(context.Background())
	s.Stop(context.Background())
	// Stop 後にもう一度止めても panic しない。
	s.Stop(context.Background())
}

// telemetry 未配線 (nil Service) でも呼び出し側が落ちないこと。deliver processor
// は hot path でこれを呼ぶ。
func TestService_NilReceiverIsSafe(t *testing.T) {
	var s *Service
	s.RecordDelivery("a.example", Outcome{Class: ClassSuccess})
	got, err := s.Query(context.Background(), time.Hour)
	require.NoError(t, err)
	assert.Nil(t, got)
	assert.Zero(t, s.EvictedHosts())
}

// store が nil でも記録側は動く (Redis 未配線構成)。
func TestService_NilStore(t *testing.T) {
	s := NewService(nil, 0, time.Hour)
	s.RecordDelivery("a.example", Outcome{Class: ClassSuccess})
	s.Start(context.Background())
	s.Stop(context.Background())

	got, err := s.Query(context.Background(), time.Hour)
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestService_EvictedHostsSurfaced(t *testing.T) {
	s := NewService(nil, 1, time.Hour)
	s.RecordDelivery("a", Outcome{Class: ClassSuccess})
	s.RecordDelivery("b", Outcome{Class: ClassSuccess})
	assert.Equal(t, int64(1), s.EvictedHosts())
}
