package admin

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// 保持期間から既定の窓を導くところ。**現在の定数 (90 日) では両端とも効かない**
// ので、導出そのものを表で固定する。片端だけ守ると、逆側で「サーバーが返した
// sinceDays をそのまま送り返すと 400」が起きる。
func TestRetentionAndSinceDayClamps(t *testing.T) {
	// **下端は `clampSinceDays` を直接叩いて踏む。** 呼び出し元の
	// `retentionDays` が既に 1 以上を返すので、組で回すと下端の枝が一度も
	// 実行されない (保険として残してあるが、空虚にはしない)。
	assert.Equal(t, 1, clampSinceDays(0))
	assert.Equal(t, 1, clampSinceDays(-5))

	for _, tc := range []struct {
		name          string
		in            time.Duration
		wantRetention int
		wantDefault   int
	}{
		{"1 日未満は 1 日に切り上げる", time.Hour, 1, 1},
		{"0 も 1 日", 0, 1, 1},
		{"端数は切り捨て", 90*24*time.Hour + 23*time.Hour, 90, 90},
		{"既定", 90 * 24 * time.Hour, 90, 90},
		{"上限ちょうど", ipSearchMaxSinceDays * 24 * time.Hour, ipSearchMaxSinceDays, ipSearchMaxSinceDays},
		// **保持期間そのものは長いまま返す** (事実なので丸めない)。窓だけ上限へ寄せる。
		{"上限を超える保持期間", 4000 * 24 * time.Hour, 4000, ipSearchMaxSinceDays},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := retentionDays(tc.in)
			assert.Equal(t, tc.wantRetention, got)
			assert.Equal(t, tc.wantDefault, clampSinceDays(got))
		})
	}
}

// 導出した既定値は、この endpoint 自身が受け付ける範囲に収まっていること。
// **サーバーが返した値を送り返して 400 になる状態を作らない。**
func TestDefaultSinceDaysIsAcceptable(t *testing.T) {
	assert.GreaterOrEqual(t, ipSearchDefaultSinceDays, 1)
	assert.LessOrEqual(t, ipSearchDefaultSinceDays, ipSearchMaxSinceDays)
}
