package stream

import (
	"context"
	"encoding/json"
	"log/slog"
)

// BroadcastTopic is the Redis pubsub channel that carries instance-wide stream
// events (emojiAdded / emojiUpdated / emojiDeleted など) delivered to every
// connected client. upstream `GlobalEventService.publishBroadcastStream` の
// `broadcast` channel に相当する (#2046)。
const BroadcastTopic = "broadcast"

// BroadcastEnvelope is the JSON wire format on BroadcastTopic: a `{type, body}`
// pair that the Manager forwards to every connection as a top-level WS message
// (upstream Connection.onBroadcastMessage → sendMessageToWs(type, body))。
type BroadcastEnvelope struct {
	Type string          `json:"type"`
	Body json.RawMessage `json:"body"`
}

// BroadcastPublisher emits `{type, body}` envelopes to BroadcastTopic.
type BroadcastPublisher struct {
	pub PubSubPublisher
}

// NewBroadcastPublisher constructs a BroadcastPublisher backed by the given
// PubSubPublisher.
func NewBroadcastPublisher(pub PubSubPublisher) *BroadcastPublisher {
	return &BroadcastPublisher{pub: pub}
}

// PublishBroadcast emits an instance-wide event to every connected client.
func (p *BroadcastPublisher) PublishBroadcast(eventType string, body any) {
	if p == nil || p.pub == nil || eventType == "" {
		return
	}
	rawBody, err := json.Marshal(body)
	if err != nil {
		slog.Warn("broadcast publisher: body marshal failed", "event", eventType, "err", err)
		return
	}
	raw, err := json.Marshal(BroadcastEnvelope{Type: eventType, Body: rawBody})
	if err != nil {
		slog.Warn("broadcast publisher: envelope marshal failed", "event", eventType, "err", err)
		return
	}
	if err := p.pub.Publish(context.Background(), BroadcastTopic, json.RawMessage(raw)); err != nil {
		slog.Warn("broadcast publisher: publish failed", "event", eventType, "err", err)
	}
}

// SubscribeBroadcast starts listening on BroadcastTopic and forwards each event
// to every registered connection as a top-level `{type, body}` WS message
// (#2046)。
//
// **同一 Manager に対して 1 度だけ呼ぶこと** ([[SubscribeWordMuteReload]] と同様、
// 複数回呼ぶと handler が二重登録される)。bus が nil なら no-op。
func (m *Manager) SubscribeBroadcast() {
	if m.bus == nil {
		return
	}
	m.bus.Subscribe(BroadcastTopic, func(raw []byte) {
		var env BroadcastEnvelope
		if err := json.Unmarshal(raw, &env); err != nil || env.Type == "" {
			slog.Warn("stream: broadcast payload unmarshal failed", "err", err)
			return
		}
		m.broadcastToAll(env.Type, env.Body)
	})
}

// UnsubscribeBroadcast stops listening on BroadcastTopic (Manager.Shutdown 経路)。
func (m *Manager) UnsubscribeBroadcast() {
	if m.bus == nil {
		return
	}
	m.bus.Unsubscribe(BroadcastTopic)
}

// broadcastToAll sends a `{type, body}` message to every registered connection.
// Manager の mu は snapshot を取って早めに release し、Send (Connection 内部 mutex
// で thread-safe) を lock 外で呼ぶ。
func (m *Manager) broadcastToAll(eventType string, body json.RawMessage) {
	m.mu.RLock()
	targets := make([]*Connection, 0, len(m.conns))
	for _, c := range m.conns {
		targets = append(targets, c)
	}
	m.mu.RUnlock()

	msg := map[string]any{"type": eventType, "body": body}
	for _, c := range targets {
		_ = c.Send(msg)
	}
}
