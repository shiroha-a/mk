package driver

import (
	"context"
	"time"
)

// HandlerFunc processes a single task. Returning an error wrapping
// SkipRetry tells the driver not to retry even if attempts remain.
type HandlerFunc func(ctx context.Context, t Task) error

// Client enqueues tasks. Drivers translate the (taskType, payload,
// opts) tuple into their native enqueue call.
type Client interface {
	Enqueue(ctx context.Context, taskType string, payload []byte, opts ...EnqueueOption) error
	Close() error
}

// Server is the worker-side runtime: register handlers, then Start
// to begin consuming jobs. Shutdown stops accepting new jobs and
// waits for in-flight ones.
type Server interface {
	Handle(taskType string, h HandlerFunc)
	Start() error
	Shutdown()
}

// InspectorInfo is the per-queue summary returned by Inspector.GetQueueInfo.
// Field names mirror asynq.QueueInfo so the admin UI mapping stays
// straightforward.
type InspectorInfo struct {
	Queue     string
	Size      int
	Active    int
	Pending   int
	Completed int
	Failed    int
	Scheduled int
	Retry     int
}

// MetricsResult is the BullMQ-compatible per-minute history of
// finalised jobs (completed or failed) for a queue. Drivers that do
// not natively track time-series data return Data nil and only
// populate Count from the cumulative bucket size — admin UIs that
// only display the chart will simply render empty in that case.
//
// Data ordering follows BullMQ's `Queue.getMetrics` contract: newest
// minute first. Entries are int64 to keep the field representable on
// 32-bit platforms despite the underlying minute counts staying small.
type MetricsResult struct {
	Count int64
	Data  []int64
}

// MetricsKindCompleted / MetricsKindFailed name the only kinds
// Inspector.QueueMetrics accepts. Other strings return an error.
const (
	MetricsKindCompleted = "completed"
	MetricsKindFailed    = "failed"
)

// TaskSummary is the driver-neutral projection of a task used by the
// admin/queue list APIs.
type TaskSummary struct {
	ID            string
	Queue         string
	Type          string
	State         string
	Payload       []byte
	Retried       int
	MaxRetry      int
	LastErr       string
	LastFailedAt  time.Time
	NextProcessAt time.Time
	EnqueuedAt    time.Time
	ScheduledAt   time.Time
	CompletedAt   time.Time
}

// Inspector exposes admin-level queue introspection used by the
// admin/queue endpoints.
type Inspector interface {
	Queues() ([]string, error)
	GetQueueInfo(qname string) (*InspectorInfo, error)

	DeleteTask(qname, taskID string) error
	DeleteAllPendingTasks(qname string) (int, error)
	RunTask(qname, taskID string) error

	ListPendingTasks(qname string, page, pageSize int) ([]*TaskSummary, error)
	ListActiveTasks(qname string, page, pageSize int) ([]*TaskSummary, error)
	ListScheduledTasks(qname string, page, pageSize int) ([]*TaskSummary, error)
	ListRetryTasks(qname string, page, pageSize int) ([]*TaskSummary, error)

	GetTaskInfo(qname, taskID string) (*TaskSummary, error)

	// QueueMetrics returns the BullMQ-spec per-minute history for the
	// given queue. kind must be MetricsKindCompleted or
	// MetricsKindFailed; other values return an error. Drivers without
	// native time-series support return MetricsResult.Data == nil but
	// still populate Count from the cumulative bucket cardinality.
	QueueMetrics(qname, kind string) (*MetricsResult, error)

	Close() error
}

// Scheduler registers cron-driven recurring tasks. cronspec follows
// the underlying driver's cron syntax (asynq accepts standard 5-field
// cron expressions).
type Scheduler interface {
	Register(cronspec, taskType string, payload []byte, opts ...EnqueueOption) error
	Start() error
	Shutdown()
}

// Driver bundles the per-deployment queue driver factory. A single
// Driver instance owns the underlying connection / runtime; callers
// obtain Client / Server / Inspector / Scheduler from it.
//
// Close releases the resources owned by the driver. Sub-components
// (Client, Inspector etc.) do not need to be Close'd individually
// when the parent Driver is Closed.
type Driver interface {
	Client() Client
	Server() Server
	Inspector() Inspector
	Scheduler() Scheduler
	Close() error

	// WorkerCount returns the number of worker goroutines currently
	// dedicated to qname. Drivers that share a single worker pool across
	// queues (e.g. asynq) return the pool-wide Concurrency for every
	// qname. Drivers that have not started their Server yet return 0.
	//
	// Used by the Prometheus metrics layer (`mk_job_workers_active`) and
	// later by the auto-scale controller (#1120 tracker) to read the
	// current pool size when computing scale decisions.
	WorkerCount(qname string) int

	// Resize changes the worker pool size for qname to n at runtime.
	// Returns ErrResizeNotSupported on backends without dynamic resize
	// support (= asynq today). On supported backends:
	//
	//   - n > current: spawn (n - current) new worker goroutines.
	//   - n < current: gracefully stop (current - n) workers, waiting
	//     for any in-flight jobs they own to finish before returning.
	//   - n == current: no-op, returns nil.
	//
	// Resize is intended for the auto-scale controller (#1120 tracker
	// PR #1125 wiring). Concurrent Resize calls on the same qname
	// serialise internally; callers do not need to gate themselves.
	//
	// Negative n returns an error. n is clamped to [0, server-defined-max]
	// per the driver's implementation (mkqdriver has no hard ceiling
	// because the auto-scale controller enforces it upstream).
	Resize(qname string, n int) error
}
