package channels

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubLogProvider は server_stats_test.go で定義済 (同 package)。

// factory 経由で historical log を返す (#571 item 2 main path)
func TestQueueStatsFactory_RequestLogServesHistorical(t *testing.T) {
	logger := &stubLogProvider{
		entries: []json.RawMessage{
			json.RawMessage(`{"deliver":{"active":1}}`),
			json.RawMessage(`{"deliver":{"active":2}}`),
		},
	}
	factory := NewQueueStatsFactory(logger)
	ctx := newCtx(nil)
	ch := factory(ctx)
	require.NoError(t, ch.Init(nil))
	ch.OnClientMessage("requestLog", json.RawMessage(`{"length":10}`))

	require.Len(t, ctx.sentType, 1)
	assert.Equal(t, "statsLog", ctx.sentType[0])
	body, ok := ctx.sentBody[0].([]json.RawMessage)
	require.True(t, ok)
	assert.Len(t, body, 2)
	assert.Equal(t, 10, logger.lastMaxLenSeen)
}
