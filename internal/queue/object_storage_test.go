package queue_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/shiroha-a/mk/internal/queue"
	"github.com/shiroha-a/mk/internal/queue/driver"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// payloadRecordingClient captures the payload as well as the options, which
// recordingDriverClient (queue_keepfailed_test.go) deliberately drops.
type payloadRecordingClient struct {
	lastTaskType string
	lastPayload  []byte
	lastOpts     driver.EnqueueOptions
	calls        int
	err          error
}

func (r *payloadRecordingClient) Enqueue(_ context.Context, taskType string, payload []byte, opts ...driver.EnqueueOption) error {
	r.calls++
	if r.err != nil {
		return r.err
	}
	r.lastTaskType = taskType
	r.lastPayload = payload
	r.lastOpts = driver.ApplyEnqueueOptions(opts)
	return nil
}

func (r *payloadRecordingClient) Close() error { return nil }

func TestClient_EnqueueDeleteObjectStorageFile(t *testing.T) {
	rec := &payloadRecordingClient{}
	c := queue.NewClient(&stubDriver{client: rec})
	defer func() { _ = c.Close() }()

	require.NoError(t, c.EnqueueDeleteObjectStorageFile("abc123.webp"))

	assert.Equal(t, queue.TaskTypeObjectStorageDeleteFile, rec.lastTaskType)
	assert.Equal(t, queue.ObjectStorageQueueName, rec.lastOpts.Queue)

	var got queue.ObjectStorageDeleteFilePayload
	require.NoError(t, json.Unmarshal(rec.lastPayload, &got))
	assert.Equal(t, "abc123.webp", got.Key)

	// storage の一時障害を待てるよう、指数バックオフ付きで再試行させる。
	assert.Positive(t, rec.lastOpts.MaxRetry)
	assert.Equal(t, driver.BackoffExponential, rec.lastOpts.BackoffType)
	assert.Equal(t, time.Minute, rec.lastOpts.BackoffDelay)
}

func TestClient_EnqueueDeleteObjectStorageFile_RetentionPolicyApplied(t *testing.T) {
	rec := &payloadRecordingClient{}
	c := queue.NewClient(&stubDriver{client: rec})
	defer func() { _ = c.Close() }()

	c.SetPolicy(queue.ObjectStorageQueueName, queue.Policy{KeepFailed: 42, KeepCompleted: 7})
	require.NoError(t, c.EnqueueDeleteObjectStorageFile("k"))

	assert.True(t, rec.lastOpts.KeepFailedSet)
	assert.Equal(t, 42, rec.lastOpts.KeepFailed)
	assert.True(t, rec.lastOpts.KeepCompletedSet)
	assert.Equal(t, 7, rec.lastOpts.KeepCompleted)
}

func TestClient_EnqueueDeleteObjectStorageFile_DriverError(t *testing.T) {
	rec := &payloadRecordingClient{err: errors.New("redis down")}
	c := queue.NewClient(&stubDriver{client: rec})
	defer func() { _ = c.Close() }()

	assert.Error(t, c.EnqueueDeleteObjectStorageFile("k"))
}

func TestClient_EnqueueCleanRemoteFiles(t *testing.T) {
	rec := &payloadRecordingClient{}
	c := queue.NewClient(&stubDriver{client: rec})
	defer func() { _ = c.Close() }()

	require.NoError(t, c.EnqueueCleanRemoteFiles())

	assert.Equal(t, queue.TaskTypeCleanRemoteFiles, rec.lastTaskType)
	assert.Equal(t, queue.ObjectStorageQueueName, rec.lastOpts.Queue)
	assert.Nil(t, rec.lastPayload)
	// 一括削除は job 1 本で回しきる。失敗しても再実行すればよいので retry せず、
	// 連打は unique window で吸収する。
	assert.Equal(t, 0, rec.lastOpts.MaxRetry)
	assert.Positive(t, rec.lastOpts.UniqueTTL)
}

func TestClient_EnqueueCleanRemoteFiles_DriverError(t *testing.T) {
	rec := &payloadRecordingClient{err: errors.New("redis down")}
	c := queue.NewClient(&stubDriver{client: rec})
	defer func() { _ = c.Close() }()

	assert.Error(t, c.EnqueueCleanRemoteFiles())
}
