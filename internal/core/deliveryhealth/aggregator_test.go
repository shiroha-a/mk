package deliveryhealth

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAggregator_CountsByClass(t *testing.T) {
	a := NewAggregator(0)
	a.Record("a.example", Outcome{Class: ClassSuccess, Status: 202, Latency: 30 * time.Millisecond})
	a.Record("a.example", Outcome{Class: ClassSuccess, Status: 200, Latency: 40 * time.Millisecond})
	a.Record("a.example", Outcome{Class: ClassServerError, Status: 503, Latency: 2 * time.Second})
	a.Record("b.example", Outcome{Class: ClassTransport, Latency: 9 * time.Second, Err: "dial timeout"})

	deltas := a.Drain()
	require.Len(t, deltas, 2)

	byHost := map[string]Delta{}
	for _, d := range deltas {
		byHost[d.Host] = d
	}
	assert.Equal(t, int64(2), byHost["a.example"].ByClass[ClassSuccess])
	assert.Equal(t, int64(1), byHost["a.example"].ByClass[ClassServerError])
	assert.Equal(t, int64(1), byHost["b.example"].ByClass[ClassTransport])
}

// 成功時に lastError を上書きしないこと。上書きすると「最後に見たエラー」が
// 成功のたびに消え、障害の痕跡が残らない。
func TestAggregator_LastErrorOnlyOnFailure(t *testing.T) {
	a := NewAggregator(0)
	a.Record("a.example", Outcome{Class: ClassServerError, Status: 500, Latency: time.Second, Err: "boom"})
	a.Record("a.example", Outcome{Class: ClassSuccess, Status: 202, Latency: time.Millisecond})

	d := a.Drain()[0]
	require.NotNil(t, d.LastError)
	assert.Equal(t, ClassServerError, d.LastError.Class)
	assert.Equal(t, 500, d.LastError.Status)
}

// gone / clientError は「相手が応答したが受理しなかった」もの。成功率に混ぜると
// 死んでいるホストが健全に見えるので、失敗として数える。
func TestOutcomeClass_SucceededOnlyForSuccess(t *testing.T) {
	assert.True(t, ClassSuccess.Succeeded())
	for _, c := range []OutcomeClass{ClassGone, ClassRateLimited, ClassClientError, ClassServerError, ClassTransport} {
		assert.Falsef(t, c.Succeeded(), "%s should not count as success", c)
	}
}

func TestAggregator_LatencyBuckets(t *testing.T) {
	a := NewAggregator(0)
	// 境界は「以下」なので 50ms はバケット 0。
	a.Record("h", Outcome{Class: ClassSuccess, Latency: 50 * time.Millisecond})
	a.Record("h", Outcome{Class: ClassSuccess, Latency: 51 * time.Millisecond})
	a.Record("h", Outcome{Class: ClassSuccess, Latency: time.Minute}) // +Inf

	d := a.Drain()[0]
	assert.Equal(t, int64(1), d.Latency[0])
	assert.Equal(t, int64(1), d.Latency[1])
	assert.Equal(t, int64(1), d.Latency[latencyBucketCount-1], "計測上限超えは +Inf バケット")
}

// Drain は毎回リセットする。しないと同じ計数を何度も Redis へ足し込む。
func TestAggregator_DrainResets(t *testing.T) {
	a := NewAggregator(0)
	a.Record("h", Outcome{Class: ClassSuccess})
	require.Len(t, a.Drain(), 1)
	assert.Empty(t, a.Drain(), "2 回目は空")
}

// Drain の戻り値は呼び出し側の所有物。内部 map を貸すと、flush 中の Record と
// 競合する。
func TestAggregator_DrainReturnsOwnedCopy(t *testing.T) {
	a := NewAggregator(0)
	a.Record("h", Outcome{Class: ClassSuccess})
	d := a.Drain()[0]
	d.ByClass[ClassSuccess] = 999

	a.Record("h", Outcome{Class: ClassSuccess})
	assert.Equal(t, int64(1), a.Drain()[0].ByClass[ClassSuccess], "呼び出し側の変更が内部に漏れない")
}

// host 上限を超えても壊れない。捨てた数は運用者が上限の妥当性を判断できるよう
// 数えておく。
func TestAggregator_EvictsBeyondCap(t *testing.T) {
	a := NewAggregator(2)
	a.Record("a", Outcome{Class: ClassSuccess})
	a.Record("b", Outcome{Class: ClassSuccess})
	a.Record("c", Outcome{Class: ClassSuccess})

	assert.Equal(t, int64(1), a.EvictedHosts())
	assert.Len(t, a.Drain(), 2, "上限ぶんだけ残る")
}

// inbox URL の parse に失敗すると空 host が来る。集計に混ぜない。
func TestAggregator_IgnoresEmptyHost(t *testing.T) {
	a := NewAggregator(0)
	a.Record("", Outcome{Class: ClassSuccess})
	assert.Empty(t, a.Drain())
}

// エラー文字列には相手の応答由来の内容が混ざりうる。保存前に必ず切る。
func TestAggregator_TruncatesErrorMessage(t *testing.T) {
	long := ""
	for i := 0; i < 500; i++ {
		long += "あ"
	}
	a := NewAggregator(0)
	a.Record("h", Outcome{Class: ClassTransport, Err: long})

	d := a.Drain()[0]
	require.NotNil(t, d.LastError)
	assert.Equal(t, maxErrMessageLen, len([]rune(d.LastError.Message)))
}
