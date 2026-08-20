package queue_test

import (
	"context"
	"testing"
	"time"

	"github.com/shiroha-a/mk/internal/queue"
	"github.com/shiroha-a/mk/internal/queue/driver"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingDriverClient is a minimal driver.Client implementation that
// captures the last Enqueue call's options. Used to verify wire-through
// of queue.Policy.KeepFailed into driver opts (#1184).
type recordingDriverClient struct {
	lastTaskType string
	lastOpts     driver.EnqueueOptions
}

func (r *recordingDriverClient) Enqueue(_ context.Context, taskType string, _ []byte, opts ...driver.EnqueueOption) error {
	r.lastTaskType = taskType
	r.lastOpts = driver.ApplyEnqueueOptions(opts)
	return nil
}

func (r *recordingDriverClient) Close() error { return nil }

// stubDriver wraps a Client + a nil Inspector so queue.NewClient can
// construct a queue.Client without spinning up an actual driver.
type stubDriver struct {
	client driver.Client
}

func (s *stubDriver) Client() driver.Client        { return s.client }
func (s *stubDriver) Inspector() driver.Inspector  { return nil }
func (s *stubDriver) Server() driver.Server        { return nil }
func (s *stubDriver) Scheduler() driver.Scheduler  { return nil }
func (s *stubDriver) Close() error                 { return nil }
func (s *stubDriver) WorkerCount(_ string) int     { return 0 }
func (s *stubDriver) Resize(_ string, _ int) error { return driver.ErrResizeNotSupported }

// TestClient_EnqueueDeliver_PolicyKeepFailedApplied verifies that
// Policy.KeepFailed reaches the driver Enqueue call as
// driver.WithKeepFailed(n) (#1184).
func TestClient_EnqueueDeliver_PolicyKeepFailedApplied(t *testing.T) {
	rec := &recordingDriverClient{}
	c := queue.NewClient(&stubDriver{client: rec})
	defer func() { _ = c.Close() }()

	c.SetPolicy(queue.QueueName, queue.Policy{KeepFailed: 250})

	require.NoError(t, c.EnqueueDeliver(queue.DeliverPayload{Inbox: "x", Body: []byte(`{}`)}))

	assert.Equal(t, queue.TaskTypeDeliver, rec.lastTaskType)
	assert.True(t, rec.lastOpts.KeepFailedSet, "KeepFailedSet must be propagated")
	assert.Equal(t, 250, rec.lastOpts.KeepFailed)
}

// TestClient_EnqueueInbox_PolicyKeepFailedApplied: 同様 inbox 経路。
func TestClient_EnqueueInbox_PolicyKeepFailedApplied(t *testing.T) {
	rec := &recordingDriverClient{}
	c := queue.NewClient(&stubDriver{client: rec})
	defer func() { _ = c.Close() }()

	c.SetPolicy(queue.InboxQueueName, queue.Policy{KeepFailed: 500})

	require.NoError(t, c.EnqueueInbox(context.Background(), queue.InboxPayload{Body: []byte(`{}`)}))

	assert.Equal(t, queue.TaskTypeInbox, rec.lastTaskType)
	assert.True(t, rec.lastOpts.KeepFailedSet)
	assert.Equal(t, 500, rec.lastOpts.KeepFailed)
}

// TestClient_EnqueueDeliver_PolicyZeroKeepFailedSkipped: KeepFailed=0
// (= 未指定 or 明示的 unlimited) は driver opts に WithKeepFailed を
// 渡さない (= driver default = retention 無し)。
func TestClient_EnqueueDeliver_PolicyZeroKeepFailedSkipped(t *testing.T) {
	rec := &recordingDriverClient{}
	c := queue.NewClient(&stubDriver{client: rec})
	defer func() { _ = c.Close() }()

	c.SetPolicy(queue.QueueName, queue.Policy{KeepFailed: 0})

	require.NoError(t, c.EnqueueDeliver(queue.DeliverPayload{Inbox: "x", Body: []byte(`{}`)}))

	assert.False(t, rec.lastOpts.KeepFailedSet, "KeepFailedSet must NOT be set when policy KeepFailed=0")
	assert.Equal(t, 0, rec.lastOpts.KeepFailed)
}

// TestClient_EnqueueDeliver_PolicyKeepCompletedApplied verifies that
// Policy.KeepCompleted reaches the driver as driver.WithKeepCompleted(n)
// (#1193, KeepFailed と対称)。
func TestClient_EnqueueDeliver_PolicyKeepCompletedApplied(t *testing.T) {
	rec := &recordingDriverClient{}
	c := queue.NewClient(&stubDriver{client: rec})
	defer func() { _ = c.Close() }()

	c.SetPolicy(queue.QueueName, queue.Policy{
		KeepCompleted:    30,
		KeepCompletedAge: 7 * 24 * time.Hour,
		KeepFailedAge:    7 * 24 * time.Hour,
	})

	require.NoError(t, c.EnqueueDeliver(queue.DeliverPayload{Inbox: "x", Body: []byte(`{}`)}))

	assert.True(t, rec.lastOpts.KeepCompletedSet, "KeepCompletedSet must be propagated")
	assert.Equal(t, 30, rec.lastOpts.KeepCompleted)
	assert.Equal(t, 7*24*time.Hour, rec.lastOpts.KeepCompletedAge)
	assert.Equal(t, 7*24*time.Hour, rec.lastOpts.KeepFailedAge)
}

// TestClient_EnqueueInbox_PolicyKeepCompletedApplied: 同様 inbox 経路。
func TestClient_EnqueueInbox_PolicyKeepCompletedApplied(t *testing.T) {
	rec := &recordingDriverClient{}
	c := queue.NewClient(&stubDriver{client: rec})
	defer func() { _ = c.Close() }()

	c.SetPolicy(queue.InboxQueueName, queue.Policy{KeepCompleted: 30})

	require.NoError(t, c.EnqueueInbox(context.Background(), queue.InboxPayload{Body: []byte(`{}`)}))

	assert.True(t, rec.lastOpts.KeepCompletedSet)
	assert.Equal(t, 30, rec.lastOpts.KeepCompleted)
}

// TestClient_EnqueueDeliver_PolicyZeroKeepCompletedSkipped: KeepCompleted=0
// は emit しない (KeepFailed と同じ semantic、driver 側で unlimited 蓄積)。
func TestClient_EnqueueDeliver_PolicyZeroKeepCompletedSkipped(t *testing.T) {
	rec := &recordingDriverClient{}
	c := queue.NewClient(&stubDriver{client: rec})
	defer func() { _ = c.Close() }()

	c.SetPolicy(queue.QueueName, queue.Policy{KeepCompleted: 0})

	require.NoError(t, c.EnqueueDeliver(queue.DeliverPayload{Inbox: "x", Body: []byte(`{}`)}))

	assert.False(t, rec.lastOpts.KeepCompletedSet, "KeepCompletedSet must NOT be set when policy KeepCompleted=0")
}

// TestClient_EnqueueExport_PolicyApplied: export queue (= TS の dbQueue
// 相当) でも Policy が適用される (#1193 で applyClientPolicies が deliver /
// inbox 以外にも広がった。現在は maintenance を除く 7 queue が対象)。
func TestClient_EnqueueExport_PolicyApplied(t *testing.T) {
	rec := &recordingDriverClient{}
	c := queue.NewClient(&stubDriver{client: rec})
	defer func() { _ = c.Close() }()

	c.SetPolicy(queue.ExportQueueName, queue.Policy{
		KeepCompleted:    30,
		KeepCompletedAge: 7 * 24 * time.Hour,
	})

	require.NoError(t, c.EnqueueExport(queue.ExportPayload{UserID: "u", Type: "notes"}))

	assert.Equal(t, 30, rec.lastOpts.KeepCompleted)
	assert.Equal(t, 7*24*time.Hour, rec.lastOpts.KeepCompletedAge)
}

// TestClient_EnqueueWebPush_PolicyApplied: push queue でも適用される。
func TestClient_EnqueueWebPush_PolicyApplied(t *testing.T) {
	rec := &recordingDriverClient{}
	c := queue.NewClient(&stubDriver{client: rec})
	defer func() { _ = c.Close() }()

	c.SetPolicy(queue.PushQueueName, queue.Policy{KeepCompleted: 30})

	require.NoError(t, c.EnqueueWebPush(context.Background(), queue.WebPushPayload{}))
	assert.Equal(t, 30, rec.lastOpts.KeepCompleted)
}

// TestClient_EnqueueUserWebhook_PolicyApplied: webhook queue でも適用。
func TestClient_EnqueueUserWebhook_PolicyApplied(t *testing.T) {
	rec := &recordingDriverClient{}
	c := queue.NewClient(&stubDriver{client: rec})
	defer func() { _ = c.Close() }()

	c.SetPolicy(queue.WebhookQueueName, queue.Policy{KeepCompleted: 30})

	require.NoError(t, c.EnqueueUserWebhook(context.Background(), queue.WebhookPayload{}))
	assert.Equal(t, 30, rec.lastOpts.KeepCompleted)
}
