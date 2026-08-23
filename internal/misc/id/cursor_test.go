package id

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestAidxCutoffPrefix_ClampsUpperTimestamp(t *testing.T) {
	max := time.UnixMilli(time2000 + testAIDMaxOffsetMillis)
	assert.Equal(t, "zzzzzzzz00000000", AidxCutoffPrefix(max))
	assert.Equal(t, AidxCutoffPrefix(max), AidxCutoffPrefix(max.Add(time.Millisecond)))
}

func TestNormalizeCursor_BothEmptyReturnsEmpty(t *testing.T) {
	s, u := NormalizeCursor("", "", nil, nil)
	assert.Empty(t, s)
	assert.Empty(t, u)
}

func TestNormalizeCursor_SinceIDPassesThrough(t *testing.T) {
	s, u := NormalizeCursor("abc123def456", "", nil, nil)
	assert.Equal(t, "abc123def456", s)
	assert.Empty(t, u)
}

func TestNormalizeCursor_UntilIDPassesThrough(t *testing.T) {
	s, u := NormalizeCursor("", "xyz789", nil, nil)
	assert.Empty(t, s)
	assert.Equal(t, "xyz789", u)
}

func TestNormalizeCursor_SinceDateConvertedToAidxPrefix(t *testing.T) {
	// 2026-05-22T00:00:00Z (= 既知の Unix ms)。AidxCutoffPrefix と同じ結果を期待。
	dateMs := int64(1779744000000)
	s, u := NormalizeCursor("", "", &dateMs, nil)
	assert.NotEmpty(t, s, "sinceDate を渡したら sinceID が aidx prefix に変換される")
	assert.Len(t, s, 16, "aidx prefix は 16 文字")
	assert.Empty(t, u)
}

func TestNormalizeCursor_UntilDateConvertedToAidxPrefix(t *testing.T) {
	dateMs := int64(1779744000000)
	s, u := NormalizeCursor("", "", nil, &dateMs)
	assert.Empty(t, s)
	assert.NotEmpty(t, u)
	assert.Len(t, u, 16)
}

func TestNormalizeCursor_SinceIDOverridesSinceDate(t *testing.T) {
	// upstream 仕様: sinceID が指定されていれば sinceDate は無視。
	dateMs := int64(1779744000000)
	s, _ := NormalizeCursor("explicit-id", "", &dateMs, nil)
	assert.Equal(t, "explicit-id", s, "sinceID 優先、sinceDate は ignore")
}

func TestNormalizeCursor_BothDatesProduceDifferentPrefixes(t *testing.T) {
	since := int64(1779744000000) // 2026-05-22T00:00:00Z
	until := int64(1779830400000) // 2026-05-23T00:00:00Z (+24h)
	s, u := NormalizeCursor("", "", &since, &until)
	assert.NotEmpty(t, s)
	assert.NotEmpty(t, u)
	assert.NotEqual(t, s, u, "異なる timestamp は異なる prefix を生成")
}

func TestNormalizeCursor_AidxPrefixSuffixIsZero(t *testing.T) {
	// AidxCutoffPrefix の仕様: 後半 8 文字は "00000000" で揃う (= 最小 ID
	// prefix で `id > prefix` の SQL で同 msec の全 ID を含む)。
	dateMs := int64(1779744000000)
	s, _ := NormalizeCursor("", "", &dateMs, nil)
	assert.Equal(t, "00000000", s[8:], "aidx prefix 後半 8 文字は counter 最小値")
}
