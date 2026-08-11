package deliveryhealth

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// DefaultFlushInterval is how often accumulated deltas reach Redis.
//
// 配送 1 件ごとに Redis を叩かないための緩衝。短くすると Redis 操作が増え、
// 長くすると管理画面の反映が遅れる。30 秒は「画面を開いて眺める」用途に対して
// 十分速く、Redis 操作は (ホスト数 / 30 秒) に収まる。
const DefaultFlushInterval = 30 * time.Second

// Service ties the in-memory aggregator to the Redis store.
//
// deliver processor から見た口は Record だけ。flush は自前の goroutine で回す。
type Service struct {
	agg   *Aggregator
	store *Store

	interval time.Duration
	mu       sync.Mutex
	cancel   context.CancelFunc
	wg       sync.WaitGroup
}

// NewService constructs a Service. interval <= 0 uses DefaultFlushInterval.
func NewService(store *Store, maxHosts int, interval time.Duration) *Service {
	if interval <= 0 {
		interval = DefaultFlushInterval
	}
	return &Service{agg: NewAggregator(maxHosts), store: store, interval: interval}
}

// RecordDelivery implements the deliver processor's telemetry hook.
//
// **hot path なので失敗しても何もしない。** 観測のために配送を落とさない。
func (s *Service) RecordDelivery(host string, o Outcome) {
	if s == nil {
		return
	}
	s.agg.Record(host, o)
}

// Start launches the periodic flush loop. 二重呼び出しは no-op。
func (s *Service) Start(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		return
	}
	loopCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.wg.Add(1)
	go s.loop(loopCtx)
}

// Stop drains one final time and waits for the loop to exit.
//
// 最後に flush するのは、停止直前の数十秒が丸ごと消えるのを避けるため。
// graceful shutdown の deadline は ctx で効く。
func (s *Service) Stop(ctx context.Context) {
	s.mu.Lock()
	cancel := s.cancel
	s.cancel = nil
	s.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	s.wg.Wait()
	s.flush(ctx)
}

func (s *Service) loop(ctx context.Context) {
	defer s.wg.Done()
	tick := time.NewTicker(s.interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			s.flush(ctx)
		}
	}
}

// flush drains the aggregator into Redis.
//
// Drain は先に行う。Redis が落ちている間ため込み続けると、復旧時に巨大な
// pipeline を投げることになり、しかもメモリは LRU 上限まで伸びる。**落とす方を
// 選ぶ** (観測データであって、失うと困る記録ではない)。
func (s *Service) flush(ctx context.Context) {
	deltas := s.agg.Drain()
	if len(deltas) == 0 || s.store == nil {
		return
	}
	if err := s.store.Flush(ctx, deltas); err != nil {
		slog.Warn("deliveryhealth: flush failed", "hosts", len(deltas), "err", err)
	}
}

// Query exposes the aggregated view for the admin endpoint.
func (s *Service) Query(ctx context.Context, window time.Duration) ([]HostHealth, error) {
	if s == nil || s.store == nil {
		return nil, nil
	}
	return s.store.Query(ctx, window)
}

// EvictedHosts reports how many hosts the in-memory cap dropped.
func (s *Service) EvictedHosts() int64 {
	if s == nil {
		return 0
	}
	return s.agg.EvictedHosts()
}
