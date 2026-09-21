package stream

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubContextSubscriber implements ContextSubscriber for tests.
type stubContextSubscriber struct {
	subTopics     []string
	cancelledTops []string
}

func (s *stubContextSubscriber) Subscribe(_ context.Context, channel string, _ func([]byte)) func() {
	s.subTopics = append(s.subTopics, channel)
	return func() { s.cancelledTops = append(s.cancelledTops, channel) }
}

func TestEventPubSubBus_SubscribeForwardsToInner(t *testing.T) {
	inner := &stubContextSubscriber{}
	bus := NewEventPubSubBus(inner)
	cancel := bus.Subscribe("home:alice", func([]byte) {})
	require.Len(t, inner.subTopics, 1)
	assert.Equal(t, "home:alice", inner.subTopics[0])
	require.NotNil(t, cancel, "解除ハンドルを返すこと (これが無いと他人の購読を閉じるしかなくなる)")
}

// 解除は返り値のハンドルで行われ、inner へそのまま伝わること。
func TestEventPubSubBus_CancelForwardsToInner(t *testing.T) {
	inner := &stubContextSubscriber{}
	bus := NewEventPubSubBus(inner)
	cancel := bus.Subscribe("home:alice", func([]byte) {})
	cancel()
	require.Len(t, inner.cancelledTops, 1)
	assert.Equal(t, "home:alice", inner.cancelledTops[0])
}
