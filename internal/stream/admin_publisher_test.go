package stream

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAdminStreamPublisher_PublishEnvelope(t *testing.T) {
	bus := &capturingPubSub{}
	p := NewAdminStreamPublisher(bus)
	p.PublishAdminEvent("mod1", "newAbuseUserReport", map[string]any{"id": "r1", "comment": "spam"})

	require.Len(t, bus.calls, 1)
	assert.Equal(t, "adminStream:mod1", bus.calls[0].topic)

	raw, ok := bus.calls[0].payload.(json.RawMessage)
	require.True(t, ok)
	var env struct {
		Type string          `json:"type"`
		Body json.RawMessage `json:"body"`
	}
	require.NoError(t, json.Unmarshal(raw, &env))
	assert.Equal(t, "newAbuseUserReport", env.Type)

	var body map[string]any
	require.NoError(t, json.Unmarshal(env.Body, &body))
	assert.Equal(t, "r1", body["id"])
	assert.Equal(t, "spam", body["comment"])
}

func TestAdminStreamPublisher_NilPub(t *testing.T) {
	p := NewAdminStreamPublisher(nil)
	p.PublishAdminEvent("mod1", "any", nil) // no panic
}

func TestAdminStreamPublisher_EmptyUserID(t *testing.T) {
	bus := &capturingPubSub{}
	NewAdminStreamPublisher(bus).PublishAdminEvent("", "newAbuseUserReport", nil)
	assert.Empty(t, bus.calls)
}

func TestAdminStreamPublisher_EmptyEventType(t *testing.T) {
	bus := &capturingPubSub{}
	NewAdminStreamPublisher(bus).PublishAdminEvent("mod1", "", nil)
	assert.Empty(t, bus.calls)
}

func TestAdminStreamPublisher_PublishError(t *testing.T) {
	bus := &capturingPubSub{err: errors.New("pub boom")}
	NewAdminStreamPublisher(bus).PublishAdminEvent("mod1", "newAbuseUserReport", nil) // logged, not panicked
}
