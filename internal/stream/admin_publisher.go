package stream

import (
	"context"
	"encoding/json"
	"log/slog"
)

// AdminStreamPublisher serializes `{type, body}` envelopes to the Redis topic
// `adminStream:{userID}` which the per-user `admin` WebSocket channel
// subscribes to (#1549). Used to emit moderator events such as
// `newAbuseUserReport` to a single moderator/administrator.
type AdminStreamPublisher struct {
	pub PubSubPublisher
}

// NewAdminStreamPublisher constructs an AdminStreamPublisher backed by the given
// PubSubPublisher.
func NewAdminStreamPublisher(pub PubSubPublisher) *AdminStreamPublisher {
	return &AdminStreamPublisher{pub: pub}
}

// PublishAdminEvent emits an event to `adminStream:{userID}` with a
// `{type, body}` envelope, matching TS `admin` stream event shape.
func (p *AdminStreamPublisher) PublishAdminEvent(userID, eventType string, body any) {
	if p.pub == nil || userID == "" || eventType == "" {
		return
	}
	env := map[string]any{"type": eventType, "body": body}
	raw, err := json.Marshal(env)
	if err != nil {
		slog.Warn("admin publisher: marshal failed", "event", eventType, "err", err)
		return
	}
	topic := "adminStream:" + userID
	if err := p.pub.Publish(context.Background(), topic, json.RawMessage(raw)); err != nil {
		slog.Warn("admin publisher: publish failed", "topic", topic, "err", err)
	}
}
