package queue_test

import (
	"encoding/json"
	"testing"

	"github.com/shiroha-a/mk/internal/queue"
	"github.com/shiroha-a/mk/internal/queue/driver"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// bucketInspector は ClearScheduledNote が走査する bucket だけを差し替える。
// 未実装のメソッドを呼べば nil の埋め込みで panic するので、走査対象が増えた
// ことに気付ける。
type bucketInspector struct {
	driver.Inspector
	scheduled []*driver.TaskSummary
	retry     []*driver.TaskSummary
	deleted   []string
}

func (i *bucketInspector) ListScheduledTasks(_ string, page, _ int) ([]*driver.TaskSummary, error) {
	if page > 1 {
		return nil, nil
	}
	return i.scheduled, nil
}

func (i *bucketInspector) ListRetryTasks(_ string, page, _ int) ([]*driver.TaskSummary, error) {
	if page > 1 {
		return nil, nil
	}
	return i.retry, nil
}

func (i *bucketInspector) DeleteTask(_ string, taskID string) error {
	i.deleted = append(i.deleted, taskID)
	return nil
}

type bucketDriver struct {
	insp driver.Inspector
}

func (d *bucketDriver) Client() driver.Client       { return nil }
func (d *bucketDriver) Inspector() driver.Inspector { return d.insp }
func (d *bucketDriver) Server() driver.Server       { return nil }
func (d *bucketDriver) Scheduler() driver.Scheduler { return nil }
func (d *bucketDriver) Close() error                { return nil }
func (d *bucketDriver) WorkerCount(_ string) int    { return 0 }
func (d *bucketDriver) Resize(_ string, _ int) error {
	return driver.ErrResizeNotSupported
}

func scheduledNoteSummary(t *testing.T, taskID, draftID string) *driver.TaskSummary {
	t.Helper()
	body, err := json.Marshal(queue.PostScheduledNotePayload{NoteDraftID: draftID})
	require.NoError(t, err)
	return &driver.TaskSummary{ID: taskID, Type: queue.TaskTypePostScheduledNote, Payload: body}
}

// #3121: attempts を積んだので、予約投稿の job は「backoff 待ち」の状態を
// 取りうる。そちらは retry bucket にしか出ないので、delayed だけ見ていると
// **再スケジュールが古い job に届かず、backoff 明けに予定と違う時刻で公開
// される**。
func TestClearScheduledNote_ScansRetryBucket(t *testing.T) {
	insp := &bucketInspector{
		scheduled: []*driver.TaskSummary{scheduledNoteSummary(t, "sched-1", "target")},
		retry: []*driver.TaskSummary{
			scheduledNoteSummary(t, "retry-1", "target"),
			scheduledNoteSummary(t, "retry-2", "other"),
		},
	}
	c := queue.NewClient(&bucketDriver{insp: insp})

	require.NoError(t, c.ClearScheduledNote("target"))
	assert.Equal(t, []string{"sched-1", "retry-1"}, insp.deleted,
		"retry 待ちの job が消し残っている")
}
