package procstats

import (
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Deps が空でも収集は成功する。最小構成 / テスト構成でもエンドポイントが 500 に
// ならないための保証。
func TestCollect_ZeroDeps(t *testing.T) {
	s := Collect(Deps{}, "2026.7.0", "1.1.2")

	assert.Equal(t, "2026.7.0", s.Version.Misskey)
	assert.Equal(t, "1.1.2", s.Version.MkGo)
	assert.Zero(t, s.UptimeMs, "起動時刻未設定なら 0")
	assert.Positive(t, s.Go.Goroutines)
	assert.Positive(t, s.Go.GOMAXPROCS)
	assert.Positive(t, s.Go.HeapSysBytes)
}

func TestCollect_Uptime(t *testing.T) {
	start := time.Date(2026, 8, 8, 0, 0, 0, 0, time.UTC)
	s := Collect(Deps{
		StartedAt: start,
		Now:       func() time.Time { return start.Add(90 * time.Second) },
	}, "v", "v")

	assert.EqualValues(t, 90_000, s.UptimeMs)
}

// 時計が巻き戻っても負の uptime を出さない (NTP 補正等)。
func TestCollect_UptimeNeverNegative(t *testing.T) {
	start := time.Date(2026, 8, 8, 0, 0, 0, 0, time.UTC)
	s := Collect(Deps{
		StartedAt: start,
		Now:       func() time.Time { return start.Add(-time.Minute) },
	}, "v", "v")

	assert.Zero(t, s.UptimeMs)
}

// GC が走っていれば直近の pause を拾う。リングバッファの添字計算を間違えると
// 常に 0 か無関係な値になる。
func TestCollect_LastGCPause(t *testing.T) {
	runtime.GC()
	s := Collect(Deps{}, "v", "v")

	require.Positive(t, s.Go.GCNum, "GC が走っている前提")
	assert.Positive(t, s.Go.LastGCPauseNs, "直近の GC pause が取れる")
}

// heap は実際に確保すれば増える。ReadMemStats の写し替え先を間違えていないこと。
func TestCollect_HeapReflectsAllocation(t *testing.T) {
	before := Collect(Deps{}, "v", "v")
	ballast := make([]byte, 32<<20)
	after := Collect(Deps{}, "v", "v")
	runtime.KeepAlive(ballast)

	assert.Greater(t, after.Go.HeapAllocBytes, before.Go.HeapAllocBytes)
}
