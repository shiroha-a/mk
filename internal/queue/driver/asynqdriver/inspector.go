package asynqdriver

import (
	"fmt"

	"github.com/hibiken/asynq"

	"github.com/shiroha-a/mk/internal/queue/driver"
)

// Inspector implements driver.Inspector over *asynq.Inspector.
type Inspector struct {
	inner *asynq.Inspector
}

// NewInspector constructs an Inspector backed by the supplied asynq
// connection options.
func NewInspector(redisOpt asynq.RedisClientOpt) *Inspector {
	return &Inspector{inner: asynq.NewInspector(redisOpt)}
}

// Queues returns the list of queue names known to the inspector.
func (i *Inspector) Queues() ([]string, error) { return i.inner.Queues() }

// GetQueueInfo returns aggregated counters for the named queue.
func (i *Inspector) GetQueueInfo(qname string) (*driver.InspectorInfo, error) {
	info, err := i.inner.GetQueueInfo(qname)
	if err != nil {
		return nil, err
	}
	return &driver.InspectorInfo{
		Queue:     info.Queue,
		Size:      info.Size,
		Active:    info.Active,
		Pending:   info.Pending,
		Completed: info.Completed,
		Failed:    info.Failed,
		Scheduled: info.Scheduled,
		Retry:     info.Retry,
	}, nil
}

// DeleteTask deletes the named task from the named queue.
func (i *Inspector) DeleteTask(qname, taskID string) error {
	return i.inner.DeleteTask(qname, taskID)
}

// DeleteAllPendingTasks deletes every pending task in the named queue.
func (i *Inspector) DeleteAllPendingTasks(qname string) (int, error) {
	return i.inner.DeleteAllPendingTasks(qname)
}

// RunTask promotes a scheduled / retry task to run immediately.
func (i *Inspector) RunTask(qname, taskID string) error {
	return i.inner.RunTask(qname, taskID)
}

// QueueMetrics returns a stub MetricsResult: asynq does not expose
// BullMQ-style per-minute time-series, so Data is left nil and Count
// is filled from the cumulative bucket cardinality (Completed /
// Failed). The admin chart degrades gracefully (empty plot) but the
// numeric labels stay accurate.
func (i *Inspector) QueueMetrics(qname, kind string) (*driver.MetricsResult, error) {
	info, err := i.inner.GetQueueInfo(qname)
	if err != nil {
		return nil, err
	}
	switch kind {
	case driver.MetricsKindCompleted:
		return &driver.MetricsResult{Count: int64(info.Completed)}, nil
	case driver.MetricsKindFailed:
		return &driver.MetricsResult{Count: int64(info.Failed)}, nil
	default:
		return nil, fmt.Errorf("asynqdriver: invalid metrics kind %q", kind)
	}
}

// Close releases the underlying inspector.
func (i *Inspector) Close() error { return i.inner.Close() }

// ListPendingTasks returns up to pageSize pending tasks in qname,
// starting at the given page (1-indexed).
func (i *Inspector) ListPendingTasks(qname string, page, pageSize int) ([]*driver.TaskSummary, error) {
	return i.listTasks(qname, "pending", page, pageSize)
}

// ListActiveTasks returns up to pageSize active tasks.
func (i *Inspector) ListActiveTasks(qname string, page, pageSize int) ([]*driver.TaskSummary, error) {
	return i.listTasks(qname, "active", page, pageSize)
}

// ListCompletedTasks / ListFailedTasks return nil: asynq does not retain
// finished-job history without per-task retention, and asynq support is being
// dropped in favour of mkq. Provided to satisfy driver.Inspector.
func (i *Inspector) ListCompletedTasks(_ string, _, _ int) ([]*driver.TaskSummary, error) {
	return nil, nil
}

func (i *Inspector) ListFailedTasks(_ string, _, _ int) ([]*driver.TaskSummary, error) {
	return nil, nil
}

// ListScheduledTasks returns up to pageSize scheduled tasks.
func (i *Inspector) ListScheduledTasks(qname string, page, pageSize int) ([]*driver.TaskSummary, error) {
	return i.listTasks(qname, "scheduled", page, pageSize)
}

// ListRetryTasks returns up to pageSize retry tasks.
func (i *Inspector) ListRetryTasks(qname string, page, pageSize int) ([]*driver.TaskSummary, error) {
	return i.listTasks(qname, "retry", page, pageSize)
}

// GetTaskInfo returns full info for a single task.
func (i *Inspector) GetTaskInfo(qname, taskID string) (*driver.TaskSummary, error) {
	info, err := i.inner.GetTaskInfo(qname, taskID)
	if err != nil {
		return nil, err
	}
	return taskInfoToSummary(info), nil
}

func (i *Inspector) listTasks(qname, state string, page, pageSize int) ([]*driver.TaskSummary, error) {
	if page < 1 {
		page = 1
	}
	if pageSize <= 0 || pageSize > 100 {
		pageSize = 30
	}
	opts := []asynq.ListOption{asynq.Page(page), asynq.PageSize(pageSize)}
	var tasks []*asynq.TaskInfo
	var err error
	switch state {
	case "pending":
		tasks, err = i.inner.ListPendingTasks(qname, opts...)
	case "active":
		tasks, err = i.inner.ListActiveTasks(qname, opts...)
	case "scheduled":
		tasks, err = i.inner.ListScheduledTasks(qname, opts...)
	case "retry":
		tasks, err = i.inner.ListRetryTasks(qname, opts...)
	}
	if err != nil {
		return nil, err
	}
	out := make([]*driver.TaskSummary, 0, len(tasks))
	for _, t := range tasks {
		out = append(out, taskInfoToSummary(t))
	}
	return out, nil
}

func taskInfoToSummary(t *asynq.TaskInfo) *driver.TaskSummary {
	return &driver.TaskSummary{
		ID:            t.ID,
		Queue:         t.Queue,
		Type:          t.Type,
		State:         t.State.String(),
		Payload:       t.Payload,
		Retried:       t.Retried,
		MaxRetry:      t.MaxRetry,
		LastErr:       t.LastErr,
		LastFailedAt:  t.LastFailedAt,
		NextProcessAt: t.NextProcessAt,
		CompletedAt:   t.CompletedAt,
	}
}
