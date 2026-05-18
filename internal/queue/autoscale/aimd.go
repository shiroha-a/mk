// Package autoscale implements the dynamic worker pool sizing logic for
// mk-go's job queue (see docs/design/auto-scale-job-workers.md). This
// package contains only the controller library — driver wiring lives in
// mkqdriver (#1124) and queue_factory configuration in router setup (#1125).
//
// AIMDController is the initial algorithm (additive-increase /
// multiplicative-decrease, modelled on TCP congestion control). The
// Controller interface allows swapping in a PI / PID variant later
// without touching driver-side call sites.
package autoscale

import (
	"time"
)

// AIMDConfig configures one AIMDController instance. All fields are
// required to be sensible by the caller; NewAIMDController returns an
// error if any constraint is violated so misconfiguration is caught at
// startup rather than at first Observe call.
type AIMDConfig struct {
	// Queue is the queue name this controller manages. Used only for
	// logging / metric labels; the controller does not enforce uniqueness.
	Queue string

	// MinWorkers / MaxWorkers are the inclusive bounds the controller
	// must respect. Action returned by Observe never proposes a target
	// outside [MinWorkers, MaxWorkers].
	MinWorkers int
	MaxWorkers int

	// UpThresholdMultiplier triggers scale-up when QueueDepth >
	// CurrentWorkers × UpThresholdMultiplier. ADR §3.1 fixes this at 4.0.
	UpThresholdMultiplier float64

	// SustainedIdleCycles is the number of consecutive Observe calls
	// with QueueDepth == 0 required before scale-down is triggered.
	// ADR §3.1 fixes this at 5.
	SustainedIdleCycles int

	// CooldownDuration is the minimum time between scale events
	// (scale-up or scale-down). ADR §3.4 fixes this at 1 second.
	CooldownDuration time.Duration

	// Clock is the time source (nil = systemClock). Tests inject a fake
	// clock to make cool-down behaviour deterministic.
	Clock Clock
}

// AIMDController implements Controller via TCP-style AIMD on queue depth.
//
// State held across Observe calls:
//   - lastScaleAt: timestamp of the most recent scale event, for cool-down
//   - idleCycleCount: number of consecutive empty-queue observations, for
//     sustained-idle scale-down trigger
//
// Observe is NOT safe for concurrent invocation. Per ADR §3.2 the design
// assumes one ticker goroutine per AIMDController instance, with one
// instance per queue, so serialised calls are sufficient.
type AIMDController struct {
	cfg AIMDConfig

	lastScaleAt    time.Time
	idleCycleCount int
}

// NewAIMDController constructs and validates an AIMDController. Returns
// a non-nil error if any required AIMDConfig field is invalid
// (operator catches misconfiguration at startup, not at runtime).
func NewAIMDController(cfg AIMDConfig) (*AIMDController, error) {
	if cfg.Queue == "" {
		return nil, errInvalidConfig("Queue must be non-empty")
	}
	if cfg.MinWorkers < 0 {
		return nil, errInvalidConfig("MinWorkers must be >= 0")
	}
	if cfg.MaxWorkers < 1 {
		return nil, errInvalidConfig("MaxWorkers must be >= 1")
	}
	if cfg.MinWorkers > cfg.MaxWorkers {
		return nil, errInvalidConfig("MinWorkers must be <= MaxWorkers")
	}
	if cfg.UpThresholdMultiplier <= 0 {
		return nil, errInvalidConfig("UpThresholdMultiplier must be > 0")
	}
	if cfg.SustainedIdleCycles < 1 {
		return nil, errInvalidConfig("SustainedIdleCycles must be >= 1")
	}
	if cfg.CooldownDuration < 0 {
		return nil, errInvalidConfig("CooldownDuration must be >= 0")
	}
	if cfg.Clock == nil {
		cfg.Clock = systemClock{}
	}
	return &AIMDController{cfg: cfg}, nil
}

// Observe consumes one tick of queue metrics and returns the scale
// decision. The implementation has 3 stages:
//
//  1. cool-down gate: if less than CooldownDuration has elapsed since
//     the last scale event, return NoOp regardless of signal.
//  2. scale-up gate: if QueueDepth > CurrentWorkers × UpThresholdMultiplier,
//     propose additive-increase to min(CurrentWorkers + max(1, ⌈N×0.25⌉),
//     MaxWorkers). Resets the idle counter.
//  3. sustained-idle gate: if QueueDepth == 0, increment the idle
//     counter; once it reaches SustainedIdleCycles, propose
//     multiplicative-decrease to max(⌊CurrentWorkers × 0.5⌋,
//     MinWorkers). Any non-zero observation resets the counter.
//
// At-bound cases (CurrentWorkers == MaxWorkers and we want to scale up,
// or CurrentWorkers == MinWorkers and we want to scale down) return
// NoOp rather than a scale action with delta=0 — callers should not
// call driver.Resize redundantly.
func (c *AIMDController) Observe(metric ObservedMetric) ControlAction {
	now := c.cfg.Clock.Now()

	// cool-down: 直前の scale event から CooldownDuration 経過していない
	// なら一切判定しない。idleCycleCount も更新しないため、cool-down 中の
	// 観測値は idle 判定にも寄与しない (= cool-down 明けで再カウント開始)。
	if !c.lastScaleAt.IsZero() && now.Sub(c.lastScaleAt) < c.cfg.CooldownDuration {
		return ControlAction{Kind: ActionNoOp}
	}

	// scale-up trigger: queue が「現 worker 数 × 4」を超えて backed up。
	upThreshold := float64(metric.CurrentWorkers) * c.cfg.UpThresholdMultiplier
	if metric.CurrentWorkers > 0 && float64(metric.QueueDepth) > upThreshold {
		c.idleCycleCount = 0
		if metric.CurrentWorkers >= c.cfg.MaxWorkers {
			return ControlAction{Kind: ActionNoOp}
		}
		step := max(metric.CurrentWorkers/4, 1)
		target := min(metric.CurrentWorkers+step, c.cfg.MaxWorkers)
		c.lastScaleAt = now
		return ControlAction{Kind: ActionScaleUp, TargetWorkers: target}
	}

	// sustained-idle scale-down: QueueDepth == 0 が SustainedIdleCycles 連続。
	// 非ゼロ観測 1 回でカウンタリセット (transient な処理追いつきで scale-down
	// しないよう、ADR §3.1 で明示的に hysteresis 設計を採用)。
	if metric.QueueDepth == 0 {
		c.idleCycleCount++
		if c.idleCycleCount >= c.cfg.SustainedIdleCycles {
			c.idleCycleCount = 0
			if metric.CurrentWorkers <= c.cfg.MinWorkers {
				return ControlAction{Kind: ActionNoOp}
			}
			target := max(metric.CurrentWorkers/2, c.cfg.MinWorkers)
			c.lastScaleAt = now
			return ControlAction{Kind: ActionScaleDown, TargetWorkers: target}
		}
		return ControlAction{Kind: ActionNoOp}
	}

	// その他 (中間状態): カウンタリセット + NoOp。
	c.idleCycleCount = 0
	return ControlAction{Kind: ActionNoOp}
}

// IdleCycleCount returns the current sustained-idle counter. Exposed for
// tests so we can assert reset semantics without depending on Observe
// return values for indirect signals.
func (c *AIMDController) IdleCycleCount() int {
	return c.idleCycleCount
}

// errInvalidConfig is a tiny package-local error helper.
type configError struct{ msg string }

func (e *configError) Error() string { return "autoscale: " + e.msg }

func errInvalidConfig(msg string) error { return &configError{msg: msg} }
