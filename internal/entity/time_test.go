package entity

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestISOMillis(t *testing.T) {
	// 非 UTC location + ナノ秒を含む時刻でも、固定 3 桁ミリ秒 + Z (UTC) に揃う。
	jst := time.FixedZone("JST", 9*60*60)
	in := time.Date(2026, 6, 21, 12, 34, 56, 789_000_000, jst)
	assert.Equal(t, "2026-06-21T03:34:56.789Z", ISOMillis(in))

	// 秒ちょうど (ナノ秒 0) でも .000 を保持する (RFC3339Nano は落とす)。
	exact := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	assert.Equal(t, "2026-01-02T03:04:05.000Z", ISOMillis(exact))
}

func TestISOMillisPtr(t *testing.T) {
	assert.Nil(t, ISOMillisPtr(nil), "nil ポインタは null")

	v := time.Date(2026, 1, 2, 3, 4, 5, 123_000_000, time.UTC)
	assert.Equal(t, "2026-01-02T03:04:05.123Z", ISOMillisPtr(&v))

	// ゼロ値ポインタは null ではなく epoch を整形する (upstream の truthy Date と同じ)。
	var zero time.Time
	assert.Equal(t, "0001-01-01T00:00:00.000Z", ISOMillisPtr(&zero))
}
