package stream

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUserListStreamPublisher_PublishEnvelope(t *testing.T) {
	bus := &capturingPubSub{}
	p := NewUserListStreamPublisher(bus)
	p.PublishUserListEvent("list1", "userAdded", map[string]any{"id": "u1", "username": "alice"})

	require.Len(t, bus.calls, 1)
	assert.Equal(t, "userListStream:list1", bus.calls[0].topic)

	raw, ok := bus.calls[0].payload.(json.RawMessage)
	require.True(t, ok)
	var env struct {
		Type string          `json:"type"`
		Body json.RawMessage `json:"body"`
	}
	require.NoError(t, json.Unmarshal(raw, &env))
	assert.Equal(t, "userAdded", env.Type)

	var body map[string]any
	require.NoError(t, json.Unmarshal(env.Body, &body))
	assert.Equal(t, "u1", body["id"])
	assert.Equal(t, "alice", body["username"])
}

func TestUserListStreamPublisher_NilPub(t *testing.T) {
	p := NewUserListStreamPublisher(nil)
	p.PublishUserListEvent("list1", "userAdded", nil) // no panic
}

func TestUserListStreamPublisher_EmptyListID(t *testing.T) {
	bus := &capturingPubSub{}
	NewUserListStreamPublisher(bus).PublishUserListEvent("", "userAdded", nil)
	assert.Empty(t, bus.calls)
}

func TestUserListStreamPublisher_EmptyEventType(t *testing.T) {
	bus := &capturingPubSub{}
	NewUserListStreamPublisher(bus).PublishUserListEvent("list1", "", nil)
	assert.Empty(t, bus.calls)
}

func TestUserListStreamPublisher_PublishError(t *testing.T) {
	bus := &capturingPubSub{err: errors.New("pub boom")}
	NewUserListStreamPublisher(bus).PublishUserListEvent("list1", "userRemoved", nil) // logged, not panicked
}

func TestUserListStreamPublisher_MarshalError(t *testing.T) {
	bus := &capturingPubSub{}
	// json.Marshal は chan を encode できず error を返す。publish には到達しない。
	NewUserListStreamPublisher(bus).PublishUserListEvent("list1", "userAdded", make(chan int))
	assert.Empty(t, bus.calls)
}
