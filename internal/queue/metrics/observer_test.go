package metrics

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// histogram の (count, sum) を取り出す。
func histValues(t *testing.T, h prometheus.Observer) (uint64, float64) {
	t.Helper()
	m := &dto.Metric{}
	require.NoError(t, h.(prometheus.Metric).Write(m))
	return m.GetHistogram().GetSampleCount(), m.GetHistogram().GetSampleSum()
}

// Observer が driver の観測を Prometheus histogram へ流すこと。
// これらの histogram は #1122 で declare だけされて hook が無く、scrape しても
// 常に 0 だった (#2277 で初めて書き込み経路が入る)。
func TestObserver_WritesHistograms(t *testing.T) {
	m := New()
	o := NewObserver(m)

	o.ObserveDispatchWait("deliver", 250*time.Millisecond)
	o.ObserveProcessing("deliver", 2*time.Second, false)
	o.ObserveProcessing("deliver", time.Second, true)

	count, sum := histValues(t, m.DispatchWaitSeconds.WithLabelValues("deliver"))
	assert.EqualValues(t, 1, count)
	assert.InDelta(t, 0.25, sum, 0.001)

	okCount, okSum := histValues(t, m.ProcessingSeconds.WithLabelValues("deliver", "success"))
	assert.EqualValues(t, 1, okCount)
	assert.InDelta(t, 2.0, okSum, 0.001)

	failCount, _ := histValues(t, m.ProcessingSeconds.WithLabelValues("deliver", "failure"))
	assert.EqualValues(t, 1, failCount)
}

// nil Metrics / nil receiver でも panic しない (配線が任意のため)。
func TestObserver_NilSafe(t *testing.T) {
	assert.Nil(t, NewObserver(nil))

	var o *Observer
	assert.NotPanics(t, func() {
		o.ObserveDispatchWait("deliver", time.Second)
		o.ObserveProcessing("deliver", time.Second, true)
	})
}

// MultiObserver は全ての observer へ配り、nil entry は読み飛ばす。
func TestMultiObserver_FansOutAndSkipsNil(t *testing.T) {
	m := New()
	spy := &countingObserver{}
	multi := MultiObserver{NewObserver(m), nil, spy}

	multi.ObserveDispatchWait("inbox", time.Second)
	multi.ObserveProcessing("inbox", time.Second, false)

	assert.Equal(t, 1, spy.waits)
	assert.Equal(t, 1, spy.procs)
	count, _ := histValues(t, m.DispatchWaitSeconds.WithLabelValues("inbox"))
	assert.EqualValues(t, 1, count)
}

type countingObserver struct {
	waits int
	procs int
}

func (c *countingObserver) ObserveDispatchWait(string, time.Duration)     { c.waits++ }
func (c *countingObserver) ObserveProcessing(string, time.Duration, bool) { c.procs++ }
