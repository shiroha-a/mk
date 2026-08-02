package runtimestats

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecorder_LatencyQuantiles(t *testing.T) {
	r := New()
	// 1..100ms を投入すると p50=50ms / p95=95ms / max=100ms (nearest-rank)。
	for i := 1; i <= 100; i++ {
		r.ObserveDispatchWait("deliver", time.Duration(i)*time.Millisecond)
	}
	snap := r.Snapshot("deliver")
	assert.Equal(t, 100, snap.DispatchWait.Count)
	assert.Equal(t, 50*time.Millisecond, snap.DispatchWait.P50)
	assert.Equal(t, 95*time.Millisecond, snap.DispatchWait.P95)
	assert.Equal(t, 100*time.Millisecond, snap.DispatchWait.Max)
}

// ring buffer 上限を超えたら古いサンプルが落ちること。
func TestRecorder_RingBufferBounded(t *testing.T) {
	r := New()
	for i := 0; i < latencySamples*3; i++ {
		r.ObserveProcessing("inbox", time.Millisecond, false)
	}
	snap := r.Snapshot("inbox")
	assert.Equal(t, latencySamples, snap.Processing.Count)
}

// 失敗数はサンプル窓と別に累積する (直近の失敗傾向を見るため)。
func TestRecorder_CountsFailures(t *testing.T) {
	r := New()
	r.ObserveProcessing("deliver", time.Millisecond, false)
	r.ObserveProcessing("deliver", time.Millisecond, true)
	r.ObserveProcessing("deliver", time.Millisecond, true)
	assert.Equal(t, 2, r.Snapshot("deliver").Failures)
}

// 負の待ち時間 (enqueue 側と consume 側の clock skew) は 0 に丸めて
// **サンプルとしては数える** (throughput が見えなくならないように)。
func TestRecorder_ClampsNegativeDurations(t *testing.T) {
	r := New()
	r.ObserveDispatchWait("deliver", -5*time.Second)
	snap := r.Snapshot("deliver")
	assert.Equal(t, 1, snap.DispatchWait.Count)
	assert.Equal(t, time.Duration(0), snap.DispatchWait.Max)
}

// scale event は新しい順、上限超過分は古いものから捨てる。
func TestRecorder_ScaleEventsNewestFirstAndBounded(t *testing.T) {
	r := New()
	base := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)
	n := 0
	r.SetClockForTest(func() time.Time {
		n++
		return base.Add(time.Duration(n) * time.Minute)
	})
	for i := 0; i < scaleEvents+10; i++ {
		r.RecordScale("deliver", "up", i, i+1)
	}
	snap := r.Snapshot("deliver")
	require.Len(t, snap.ScaleEvents, scaleEvents)
	// 先頭が最新
	assert.True(t, snap.ScaleEvents[0].At.After(snap.ScaleEvents[1].At))
	assert.Equal(t, scaleEvents+9, snap.ScaleEvents[0].From)
}

// 未知 queue は zero snapshot を返し、ScaleEvents は nil でなく空配列。
func TestRecorder_UnknownQueue(t *testing.T) {
	snap := New().Snapshot("nope")
	assert.Equal(t, 0, snap.DispatchWait.Count)
	assert.NotNil(t, snap.ScaleEvents)
	assert.Empty(t, snap.ScaleEvents)
}

// worker goroutine から並行に書かれるので data race が無いこと。
func TestRecorder_ConcurrentUse(t *testing.T) {
	r := New()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				r.ObserveDispatchWait("deliver", time.Millisecond)
				r.ObserveProcessing("deliver", time.Millisecond, j%3 == 0)
				r.RecordScale("deliver", "up", j, j+1)
				_ = r.Snapshot("deliver")
			}
		}()
	}
	wg.Wait()
	assert.Positive(t, r.Snapshot("deliver").DispatchWait.Count)
}
