package federation

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestParseAPPublishedTime(t *testing.T) {
	now := time.Now()
	fallback := time.Unix(0, 0).UTC()

	t.Run("empty string falls back", func(t *testing.T) {
		got := parseAPPublishedTime("", fallback)
		assert.Equal(t, fallback, got)
	})

	t.Run("malformed string falls back", func(t *testing.T) {
		got := parseAPPublishedTime("not-a-time", fallback)
		assert.Equal(t, fallback, got)
	})

	t.Run("valid RFC3339 is parsed", func(t *testing.T) {
		want := time.Date(2026, 1, 15, 10, 30, 0, 0, time.UTC)
		got := parseAPPublishedTime(want.Format(time.RFC3339), fallback)
		assert.Equal(t, want, got)
	})

	t.Run("valid RFC3339Nano is parsed", func(t *testing.T) {
		want := time.Date(2026, 1, 15, 10, 30, 0, 123456789, time.UTC)
		got := parseAPPublishedTime(want.Format(time.RFC3339Nano), fallback)
		assert.Equal(t, want, got)
	})

	t.Run("past timestamp is preserved (one hour ago)", func(t *testing.T) {
		past := now.Add(-1 * time.Hour).UTC().Truncate(time.Second)
		got := parseAPPublishedTime(past.Format(time.RFC3339), fallback)
		assert.WithinDuration(t, past, got, time.Second)
	})

	t.Run("near-future within skew is preserved", func(t *testing.T) {
		future := now.Add(1 * time.Minute).UTC().Truncate(time.Second)
		got := parseAPPublishedTime(future.Format(time.RFC3339), fallback)
		assert.WithinDuration(t, future, got, time.Second)
	})

	t.Run("far-future beyond skew falls back (spoof guard)", func(t *testing.T) {
		future := now.Add(1 * time.Hour).UTC().Truncate(time.Second)
		got := parseAPPublishedTime(future.Format(time.RFC3339), fallback)
		assert.Equal(t, fallback, got)
	})

	t.Run("ancient past beyond floor falls back (parse guard)", func(t *testing.T) {
		// Misskey/Mastodon は 2017+ なので 1980 は明らかに parse バグ。
		ancient := time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)
		got := parseAPPublishedTime(ancient.Format(time.RFC3339), fallback)
		assert.Equal(t, fallback, got)
	})
}
