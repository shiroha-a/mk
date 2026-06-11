package queue_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shiroha-a/mk/internal/queue"
	"github.com/shiroha-a/mk/internal/queue/driver"
)

// recordedTask is one Enqueue call captured by recordingQueueClient.
type recordedTask struct {
	taskType string
	payload  []byte
}

// recordingQueueClient is a driver.Client fake that records Enqueue calls
// in-memory. relationship 系 client API は redis を要しないため、driver
// 非依存 (= asynq にも mkq にも縛られない) の fake で検証する。
type recordingQueueClient struct {
	enqueued []recordedTask
	// failWhen returns an error for payloads it wants to reject.
	failWhen func(taskType string, payload []byte) error
}

func (c *recordingQueueClient) Enqueue(_ context.Context, taskType string, payload []byte, _ ...driver.EnqueueOption) error {
	if c.failWhen != nil {
		if err := c.failWhen(taskType, payload); err != nil {
			return err
		}
	}
	c.enqueued = append(c.enqueued, recordedTask{taskType: taskType, payload: payload})
	return nil
}

func (c *recordingQueueClient) Close() error { return nil }

// recordingDriver satisfies driver.Driver with only Client populated; the
// queue.Client under test touches Client and Inspector only.
type recordingDriver struct{ client *recordingQueueClient }

func (d *recordingDriver) Client() driver.Client       { return d.client }
func (d *recordingDriver) Server() driver.Server       { return nil }
func (d *recordingDriver) Inspector() driver.Inspector { return nil }
func (d *recordingDriver) Scheduler() driver.Scheduler { return nil }
func (d *recordingDriver) Close() error                { return nil }
func (d *recordingDriver) WorkerCount(string) int      { return 0 }
func (d *recordingDriver) Resize(string, int) error    { return driver.ErrResizeNotSupported }

func newRecordingClient(failWhen func(string, []byte) error) (*queue.Client, *recordingQueueClient) {
	rc := &recordingQueueClient{failWhen: failWhen}
	return queue.NewClient(&recordingDriver{client: rc}), rc
}

func TestNewFollowTask_RoundTrip(t *testing.T) {
	payload := queue.FollowPayload{FollowerID: "localA", FolloweeID: "rA", WithReplies: true}
	task := queue.NewFollowTask(payload)
	require.Equal(t, queue.TaskTypeFollow, task.Type())
	got, err := queue.DecodeFollowPayload(task.Payload())
	require.NoError(t, err)
	require.Equal(t, payload, got)
}

func TestDecodeFollowPayload_MalformedReturnsError(t *testing.T) {
	_, err := queue.DecodeFollowPayload([]byte(`not-json`))
	require.Error(t, err)
}

func TestNewBlockTask_RoundTrip(t *testing.T) {
	payload := queue.BlockPayload{BlockerID: "localA", BlockeeID: "rA"}
	task := queue.NewBlockTask(payload)
	require.Equal(t, queue.TaskTypeBlock, task.Type())
	got, err := queue.DecodeBlockPayload(task.Payload())
	require.NoError(t, err)
	require.Equal(t, payload, got)
}

func TestDecodeBlockPayload_MalformedReturnsError(t *testing.T) {
	_, err := queue.DecodeBlockPayload([]byte(`not-json`))
	require.Error(t, err)
}

func TestNewUnblockTask_RoundTrip(t *testing.T) {
	payload := queue.UnblockPayload{BlockerID: "localA", BlockeeID: "rA"}
	task := queue.NewUnblockTask(payload)
	require.Equal(t, queue.TaskTypeUnblock, task.Type())
	got, err := queue.DecodeUnblockPayload(task.Payload())
	require.NoError(t, err)
	require.Equal(t, payload, got)
}

func TestDecodeUnblockPayload_MalformedReturnsError(t *testing.T) {
	_, err := queue.DecodeUnblockPayload([]byte(`not-json`))
	require.Error(t, err)
}

func TestClient_EnqueueFollow_TaskTypeAndPayload(t *testing.T) {
	c, rc := newRecordingClient(nil)
	require.NoError(t, c.EnqueueFollow(queue.FollowPayload{
		FollowerID: "localA", FolloweeID: "rA", WithReplies: true,
	}))
	require.Len(t, rc.enqueued, 1)
	assert.Equal(t, queue.TaskTypeFollow, rc.enqueued[0].taskType)
	got, err := queue.DecodeFollowPayload(rc.enqueued[0].payload)
	require.NoError(t, err)
	assert.Equal(t, "localA", got.FollowerID)
	assert.Equal(t, "rA", got.FolloweeID)
	assert.True(t, got.WithReplies)
}

func TestClient_EnqueueBlockAndUnblock_TaskTypes(t *testing.T) {
	c, rc := newRecordingClient(nil)
	require.NoError(t, c.EnqueueBlock(queue.BlockPayload{BlockerID: "a", BlockeeID: "b"}))
	require.NoError(t, c.EnqueueUnblock(queue.UnblockPayload{BlockerID: "a", BlockeeID: "b"}))
	require.Len(t, rc.enqueued, 2)
	assert.Equal(t, queue.TaskTypeBlock, rc.enqueued[0].taskType)
	assert.Equal(t, queue.TaskTypeUnblock, rc.enqueued[1].taskType)
}

func TestClient_EnqueueFollowBulk_EnqueuesAll(t *testing.T) {
	c, rc := newRecordingClient(nil)
	require.NoError(t, c.EnqueueFollowBulk([]queue.FollowPayload{
		{FollowerID: "a", FolloweeID: "x"},
		{FollowerID: "b", FolloweeID: "y"},
		{FollowerID: "c", FolloweeID: "z"},
	}))
	require.Len(t, rc.enqueued, 3)
	for _, e := range rc.enqueued {
		assert.Equal(t, queue.TaskTypeFollow, e.taskType)
	}
}

// 途中の payload が enqueue 失敗しても残りは enqueue され、失敗分が
// errors.Join で集約されて返ること (本家 addBulk 相当の best-effort)。
func TestClient_EnqueueFollowBulk_PartialFailureJoinsErrors(t *testing.T) {
	c, rc := newRecordingClient(func(_ string, payload []byte) error {
		p, err := queue.DecodeFollowPayload(payload)
		require.NoError(t, err)
		if p.FollowerID == "b" {
			return assert.AnError
		}
		return nil
	})
	err := c.EnqueueFollowBulk([]queue.FollowPayload{
		{FollowerID: "a", FolloweeID: "x"},
		{FollowerID: "b", FolloweeID: "y"},
		{FollowerID: "c", FolloweeID: "z"},
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, assert.AnError)
	assert.Contains(t, err.Error(), "b->y", "失敗ペアを特定できるエラーメッセージ")
	require.Len(t, rc.enqueued, 2, "失敗以外の payload は enqueue 済")
}

func TestClient_EnqueueUnfollowBulk_EnqueuesAll(t *testing.T) {
	c, rc := newRecordingClient(nil)
	require.NoError(t, c.EnqueueUnfollowBulk([]queue.UnfollowPayload{
		{FollowerID: "a", FolloweeID: "x"},
		{FollowerID: "b", FolloweeID: "y"},
	}))
	require.Len(t, rc.enqueued, 2)
	for _, e := range rc.enqueued {
		assert.Equal(t, queue.TaskTypeUnfollow, e.taskType)
	}
}

func TestClient_EnqueueBlockBulk_EnqueuesAll(t *testing.T) {
	c, rc := newRecordingClient(nil)
	require.NoError(t, c.EnqueueBlockBulk([]queue.BlockPayload{
		{BlockerID: "a", BlockeeID: "x"},
		{BlockerID: "b", BlockeeID: "y"},
	}))
	require.Len(t, rc.enqueued, 2)
	for _, e := range rc.enqueued {
		assert.Equal(t, queue.TaskTypeBlock, e.taskType)
	}
}

func TestClient_EnqueueUnblockBulk_EnqueuesAll(t *testing.T) {
	c, rc := newRecordingClient(nil)
	require.NoError(t, c.EnqueueUnblockBulk([]queue.UnblockPayload{
		{BlockerID: "a", BlockeeID: "x"},
		{BlockerID: "b", BlockeeID: "y"},
	}))
	require.Len(t, rc.enqueued, 2)
	for _, e := range rc.enqueued {
		assert.Equal(t, queue.TaskTypeUnblock, e.taskType)
	}
}

func TestClient_EnqueueBlockBulk_PartialFailureJoinsErrors(t *testing.T) {
	c, rc := newRecordingClient(func(_ string, payload []byte) error {
		p, err := queue.DecodeBlockPayload(payload)
		require.NoError(t, err)
		if p.BlockerID == "a" {
			return assert.AnError
		}
		return nil
	})
	err := c.EnqueueBlockBulk([]queue.BlockPayload{
		{BlockerID: "a", BlockeeID: "x"},
		{BlockerID: "b", BlockeeID: "y"},
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, assert.AnError)
	require.Len(t, rc.enqueued, 1)
}

func TestClient_EnqueueUnfollowBulk_PartialFailureJoinsErrors(t *testing.T) {
	c, rc := newRecordingClient(func(_ string, payload []byte) error {
		p, err := queue.DecodeUnfollowPayload(payload)
		require.NoError(t, err)
		if p.FollowerID == "a" {
			return assert.AnError
		}
		return nil
	})
	err := c.EnqueueUnfollowBulk([]queue.UnfollowPayload{
		{FollowerID: "a", FolloweeID: "x"},
		{FollowerID: "b", FolloweeID: "y"},
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, assert.AnError)
	require.Len(t, rc.enqueued, 1)
}

func TestClient_EnqueueUnblockBulk_PartialFailureJoinsErrors(t *testing.T) {
	c, rc := newRecordingClient(func(_ string, payload []byte) error {
		p, err := queue.DecodeUnblockPayload(payload)
		require.NoError(t, err)
		if p.BlockerID == "a" {
			return assert.AnError
		}
		return nil
	})
	err := c.EnqueueUnblockBulk([]queue.UnblockPayload{
		{BlockerID: "a", BlockeeID: "x"},
		{BlockerID: "b", BlockeeID: "y"},
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, assert.AnError)
	require.Len(t, rc.enqueued, 1)
}
