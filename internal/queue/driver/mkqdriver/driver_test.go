package mkqdriver

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// 診断用に公開したラッパー (#2469) のテスト。
//
// **公開しただけで test が無いと、パッケージのカバレッジが閾値ぎりぎりに張り付く。**
// 実際 #2470 マージ後に mkqdriver が 90.0% ちょうどになり、CI の微差で
// `test-shards` が落ちた。薄いラッパーでも、公開する以上は呼んで確かめる。
func TestResolveQueueConcurrency_ExportedWrapper(t *testing.T) {
	queues := []string{"deliver", "inbox", "custom"}

	got := ResolveQueueConcurrency(queues, nil)
	// 既定は per-queue の hot-tuned 値。未知の queue は fallback。
	assert.Equal(t, defaultQueueConcurrency["deliver"], got["deliver"])
	assert.Equal(t, defaultQueueConcurrency["inbox"], got["inbox"])
	assert.Equal(t, unknownQueueConcurrency, got["custom"])

	// 明示指定が既定に勝つ。
	got = ResolveQueueConcurrency(queues, map[string]int{"deliver": 3})
	assert.Equal(t, 3, got["deliver"])
	assert.Equal(t, defaultQueueConcurrency["inbox"], got["inbox"], "指定していない queue は既定のまま")

	// 0 以下は無視される (未設定と同じ扱い)。
	got = ResolveQueueConcurrency(queues, map[string]int{"deliver": 0})
	assert.Equal(t, defaultQueueConcurrency["deliver"], got["deliver"])
}

func TestWorkerPoolSize_ExportedWrapper(t *testing.T) {
	queues := []string{"deliver", "inbox"}

	base := WorkerPoolSize(queues, nil)
	assert.Positive(t, base)

	// worker を増やすと pool も追従する (worker 1 つにつき BZPopMin の接続を
	// 1 つ保持するため)。
	bigger := WorkerPoolSize(queues, map[string]int{"deliver": defaultQueueConcurrency["deliver"] + 64})
	assert.Greater(t, bigger, base)
}
