package autoscale

// ObservedMetric is the per-tick input the Controller consumes. Currently
// just (queue depth, current worker count); a future PI controller may
// extend this with dispatch_wait p95 or processing_seconds p95 — both
// can be added without breaking existing AIMD callers because Controller
// implementations are free to ignore fields they do not consume.
//
// Per-queue context (which queue, what min/max bounds) is held by the
// Controller instance itself, not in this struct, so the same struct can
// be reused across queues without allocation overhead.
type ObservedMetric struct {
	// QueueDepth is the current number of pending jobs in the queue
	// (Redis ZCARD相当). Must be >= 0.
	QueueDepth int

	// CurrentWorkers is the number of worker goroutines currently
	// running for the queue. Must be >= 0. The Controller uses this to
	// compute the proposed delta (= TargetWorkers - CurrentWorkers).
	CurrentWorkers int
}

// ControlActionKind enumerates the three possible decisions a Controller
// can return per Observe call.
type ControlActionKind int

const (
	// ActionNoOp means the Controller chose not to change worker count
	// this tick (cool-down active, signal below threshold, bound
	// already reached, etc.). Caller should not invoke driver.Resize.
	ActionNoOp ControlActionKind = iota

	// ActionScaleUp means the Controller wants to increase worker count
	// to ControlAction.TargetWorkers. TargetWorkers > CurrentWorkers
	// is guaranteed.
	ActionScaleUp

	// ActionScaleDown means the Controller wants to decrease worker
	// count to ControlAction.TargetWorkers. TargetWorkers < CurrentWorkers
	// is guaranteed (and TargetWorkers >= MinWorkers from the
	// Controller's configuration).
	ActionScaleDown
)

// ControlAction is the decision a Controller returns per Observe call.
// Callers should switch on Kind and (for ScaleUp / ScaleDown) call
// driver.Resize(queue, TargetWorkers).
type ControlAction struct {
	Kind          ControlActionKind
	TargetWorkers int
}

// Controller is the algorithm-agnostic interface for scale decisions.
// AIMDController implements this; future PI / PID controllers can plug
// in via the same shape. queue_factory wires the Controller into a
// ticker that calls Observe at the configured interval.
//
// Implementations must be safe for concurrent Observe calls from a
// single ticker goroutine; cross-queue concurrency is handled by
// constructing one Controller instance per queue (per ADR §3.2).
type Controller interface {
	Observe(metric ObservedMetric) ControlAction
}
