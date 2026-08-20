package chart

import (
	"context"
	"errors"
	"sync"
	"time"
)

// ManagementService runs the periodic Save() loop that flushes all
// registered charts to the database. The upstream implementation calls
// chart.save() every 20 minutes; we replicate that with a single
// goroutine driven by a time.Ticker.
type ManagementService struct {
	charts   []*Chart
	interval time.Duration
	logger   func(format string, args ...any)

	mu     sync.Mutex
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewManagementService constructs a ManagementService. interval defaults
// to 20 minutes when zero or negative.
func NewManagementService(charts []*Chart, interval time.Duration) *ManagementService {
	if interval <= 0 {
		interval = 20 * time.Minute
	}
	return &ManagementService{
		charts:   charts,
		interval: interval,
		logger:   func(string, ...any) {},
	}
}

// SetLogger installs a structured logger for save errors. Defaults to a no-op
// so library users can opt out of logging.
//
// 引数は slog.Logger.Warn と同じ (message + key/value)。printf 形式にすると
// chart 名がメッセージ本文に埋もれてフィルタできず、呼び出し側で
// fmt.Sprintf する shim が要る。
func (m *ManagementService) SetLogger(fn func(msg string, args ...any)) {
	if fn != nil {
		m.logger = fn
	}
}

// Start launches the background save loop. Calling Start more than
// once without an intervening Stop returns an error so wiring bugs are
// caught loudly.
func (m *ManagementService) Start(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cancel != nil {
		return errors.New("chart: management service already started")
	}
	loopCtx, cancel := context.WithCancel(ctx)
	m.cancel = cancel
	m.wg.Add(1)
	go m.loop(loopCtx)
	return nil
}

// Stop signals the background loop to exit and blocks until it has
// flushed any in-flight Save(). The provided context is honoured for
// the final SaveAll, mirroring the upstream "dispose" hook.
func (m *ManagementService) Stop(ctx context.Context) {
	m.mu.Lock()
	cancel := m.cancel
	m.cancel = nil
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	m.wg.Wait()
	// 終了時にも一度 Save する (本家の dispose と同じ)。
	// エラーの中身は SaveAll が chart ごとに出しているので、ここでは
	// 「最後の Save だった」ことだけ足す (同じ文字列を 2 度出さない)。
	if err := m.SaveAll(ctx); err != nil {
		// 終了時の失敗は次の周期が無いぶん恒久的なので、バッファに残ったまま
		// 失われる規模を添える。warn が出るのは retry limit / not retryable に
		// 該当した group だけで、残っているものは黙って消えるため。
		m.logger("chart: final save reported errors",
			"phase", "shutdown", "unsavedGroups", m.bufferedGroups())
	}
}

// SaveAll iterates the registered charts and calls Save() on each.
// Errors are logged here and the first one is returned as a signal that
// something failed. **呼び出し側は retry しない** (Chart.Save が失敗した group を
// 自分でバッファへ戻し、次の周期で再試行する)。戻り値は「失敗したか」の判定
// だけに使う。
//
// **ここが唯一エラーの中身を出す場所。** 呼び出し側は戻り値を再ログしない
// (同じ文字列が 2 本になる)。Chart.Save が返すエラーは失敗 group 数ぶんに
// 伸びうるので、Save 側で件数を打ち切っている (maxReportedGroupErrors)。
func (m *ManagementService) SaveAll(ctx context.Context) error {
	var firstErr error
	for _, c := range m.charts {
		if err := c.Save(ctx); err != nil {
			m.logger("chart: save failed", "chart", c.Name(), "err", err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// bufferedGroups sums the still-unsaved groups across the registered charts.
func (m *ManagementService) bufferedGroups() int {
	total := 0
	for _, c := range m.charts {
		total += c.BufferedGroups()
	}
	return total
}

func (m *ManagementService) loop(ctx context.Context) {
	defer m.wg.Done()
	tick := time.NewTicker(m.interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			// 中身は SaveAll が chart ごとに出しているので再ログしない。
			_ = m.SaveAll(ctx)
		}
	}
}
