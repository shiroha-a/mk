package webpush_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/shiroha-a/mk/internal/core/webpush"
	"github.com/shiroha-a/mk/internal/queue"
	"github.com/shiroha-a/mk/internal/queue/driver"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeEnqueuer captures EnqueueWebPush calls for assertion.
type fakeEnqueuer struct {
	calls []queue.WebPushPayload
	err   error
}

func (f *fakeEnqueuer) EnqueueDeliver(_ queue.DeliverPayload, _ ...driver.EnqueueOption) error {
	return nil
}
func (f *fakeEnqueuer) EnqueueExport(_ queue.ExportPayload) error { return nil }
func (f *fakeEnqueuer) EnqueueImport(_ queue.ImportPayload) error { return nil }
func (f *fakeEnqueuer) EnqueueWebPush(_ context.Context, payload queue.WebPushPayload) error {
	if f.err != nil {
		return f.err
	}
	f.calls = append(f.calls, payload)
	return nil
}
func (f *fakeEnqueuer) EnqueueUserWebhook(_ context.Context, _ queue.WebhookPayload) error {
	return nil
}
func (f *fakeEnqueuer) EnqueueSystemWebhook(_ context.Context, _ queue.WebhookPayload) error {
	return nil
}
func (f *fakeEnqueuer) EnqueueInbox(_ context.Context, _ queue.InboxPayload) error { return nil }
func (f *fakeEnqueuer) EnqueuePostScheduledNote(_ queue.PostScheduledNotePayload, _ ...driver.EnqueueOption) error {
	return nil
}
func (f *fakeEnqueuer) ClearScheduledNote(_ string) error { return nil }
func (f *fakeEnqueuer) SupportsScheduledNote() bool       { return true }
func (f *fakeEnqueuer) Close() error                      { return nil }

func TestService_PushNotification_Enqueues(t *testing.T) {
	enq := &fakeEnqueuer{}
	svc := webpush.NewService(enq)
	svc.PushNotification("u1", map[string]any{
		"type": "mention",
		"note": map[string]any{"text": "hi", "cw": "spoil"},
	})
	require.Len(t, enq.calls, 1)
	assert.Equal(t, "u1", enq.calls[0].UserID)
	assert.Equal(t, webpush.TypeNotification, enq.calls[0].Type)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(enq.calls[0].Body, &decoded))
	note := decoded["note"].(map[string]any)
	// cw が優先されて summary になる
	assert.Equal(t, "spoil", note["text"])
}

func TestService_PushUnreadAntennaNote(t *testing.T) {
	enq := &fakeEnqueuer{}
	svc := webpush.NewService(enq)
	svc.PushUnreadAntennaNote("u1", map[string]any{
		"antenna": map[string]any{"id": "a1", "name": "ant"},
		"note":    map[string]any{"text": "t"},
	})
	require.Len(t, enq.calls, 1)
	assert.Equal(t, webpush.TypeUnreadAntennaNote, enq.calls[0].Type)
}

func TestService_PushReadAllNotifications(t *testing.T) {
	enq := &fakeEnqueuer{}
	svc := webpush.NewService(enq)
	svc.PushReadAllNotifications("u1")
	require.Len(t, enq.calls, 1)
	assert.Equal(t, webpush.TypeReadAllNotifications, enq.calls[0].Type)
	assert.Empty(t, enq.calls[0].Body)
}

func TestService_PushNewChatMessage(t *testing.T) {
	enq := &fakeEnqueuer{}
	svc := webpush.NewService(enq)
	svc.PushNewChatMessage("u1", map[string]any{"id": "m1", "text": "hi"})
	require.Len(t, enq.calls, 1)
	assert.Equal(t, webpush.TypeNewChatMessage, enq.calls[0].Type)
}

func TestService_NilReceiverNoPanic(t *testing.T) {
	var svc *webpush.Service
	svc.PushNotification("u1", nil)
	svc.PushReadAllNotifications("u1")
}

func TestService_EmptyUserIDSkipped(t *testing.T) {
	enq := &fakeEnqueuer{}
	svc := webpush.NewService(enq)
	svc.PushNotification("", map[string]any{"type": "x"})
	assert.Empty(t, enq.calls)
}

func TestService_EnqueueErrorSilentlySwallowed(t *testing.T) {
	enq := &fakeEnqueuer{err: errors.New("boom")}
	svc := webpush.NewService(enq)
	// エラーは silent に log へ。panic しないことを確認。
	svc.PushNotification("u1", map[string]any{"type": "t"})
}
