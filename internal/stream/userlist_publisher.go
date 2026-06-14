package stream

import (
	"context"
	"encoding/json"
	"log/slog"
)

// UserListStreamPublisher serializes `{type, body}` envelopes to the Redis topic
// `userListStream:{listID}` which the `userList` WebSocket channel subscribes to
// for membership events (#1549). Used to emit `userAdded` / `userRemoved` so
// clients update the member list live. Note delivery uses a separate
// `userListTimeline:{listID}` topic.
type UserListStreamPublisher struct {
	pub PubSubPublisher
}

// NewUserListStreamPublisher constructs a UserListStreamPublisher backed by the
// given PubSubPublisher.
func NewUserListStreamPublisher(pub PubSubPublisher) *UserListStreamPublisher {
	return &UserListStreamPublisher{pub: pub}
}

// PublishUserListEvent emits an event to `userListStream:{listID}` with a
// `{type, body}` envelope, matching TS `userList` membership event shape.
func (p *UserListStreamPublisher) PublishUserListEvent(listID, eventType string, body any) {
	if p.pub == nil || listID == "" || eventType == "" {
		return
	}
	env := map[string]any{"type": eventType, "body": body}
	raw, err := json.Marshal(env)
	if err != nil {
		slog.Warn("userlist publisher: marshal failed", "event", eventType, "err", err)
		return
	}
	topic := "userListStream:" + listID
	if err := p.pub.Publish(context.Background(), topic, json.RawMessage(raw)); err != nil {
		slog.Warn("userlist publisher: publish failed", "topic", topic, "err", err)
	}
}
