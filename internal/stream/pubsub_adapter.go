package stream

import (
	"context"
)

// ContextSubscriber は core/event.PubSubService が満たす最小サブセット。
// テスト時にスタブ化できるよう interface で定義する。
type ContextSubscriber interface {
	// Subscribe returns the function that removes this handler again.
	// **トピック名で解除する API は持たない** — どの購読者を外すのかを
	// 表現できず、他人の購読を閉じてしまうため (#H-4)。
	Subscribe(ctx context.Context, channel string, handler func([]byte)) func()
}

// EventPubSubBus adapts a ContextSubscriber (in production: core/event.
// PubSubService) to the stream.PubSubBus interface used by Dispatcher.
// PubSubBus.Subscribe drops the context, so we wrap a background context
// internally.
type EventPubSubBus struct {
	inner ContextSubscriber
}

// NewEventPubSubBus constructs an EventPubSubBus around the given subscriber.
func NewEventPubSubBus(inner ContextSubscriber) *EventPubSubBus {
	return &EventPubSubBus{inner: inner}
}

// Subscribe implements PubSubBus.
func (b *EventPubSubBus) Subscribe(topic string, handler func([]byte)) func() {
	return b.inner.Subscribe(context.Background(), topic, handler)
}
