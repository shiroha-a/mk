package stream

import (
	"context"
	"log/slog"
)

// NoteEventPublisher emits arbitrary `{type, body}` envelopes on a note's
// pubsub topic (`noteStream:<noteID>`). The Dispatcher subscribes to this
// topic when a frontend client opens the corresponding noteStream channel
// and forwards events to the WebSocket as `{type: "noteUpdated", body:
// {id, type, body}}` — matching the Misskey TS upstream wire format.
//
// Used by core/poll.Service for the `pollVoted` event so frontend can
// update poll counts in real-time without a full reload (#690). Easy to
// extend to other note-scoped event types (reacted, deleted, etc.) by
// adding new call sites that invoke PublishToNoteStream with the correct
// type name.
type NoteEventPublisher struct {
	pub PubSubPublisher
}

// NewNoteEventPublisher constructs a NoteEventPublisher around the given
// PubSubPublisher.
func NewNoteEventPublisher(pub PubSubPublisher) *NoteEventPublisher {
	return &NoteEventPublisher{pub: pub}
}

// PublishToNoteStream wraps body into a {type, body} envelope and publishes
// it to `noteStream:<noteID>`. Failures are best-effort and only logged at
// warn level (the underlying action — vote / reaction — has already
// succeeded; losing the streaming nudge degrades to "user must reload").
func (p *NoteEventPublisher) PublishToNoteStream(noteID string, eventType string, body any) {
	if p.pub == nil || noteID == "" || eventType == "" {
		return
	}
	envelope := map[string]any{
		"type": eventType,
		"body": body,
	}
	if err := p.pub.Publish(context.Background(), "noteStream:"+noteID, envelope); err != nil {
		slog.Warn("note event publisher: publish failed",
			"noteId", noteID, "type", eventType, "err", err)
	}
}
