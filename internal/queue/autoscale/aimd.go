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
	"fmt"
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
	//
	// 注: 本フィールドは operator-facing YAML config には expose しない
	// 想定 (queue_factory #1125 で固定値 4.0 を inject する)。test と
	// 将来の experimental tuning のためだけに configurable にしてある。
	UpThresholdMultiplier float64

	// SustainedIdleCycles is the number of consecutive Observe calls
	// with QueueDepth == 0 required before scale-down is triggered.
	// ADR §3.1 fixes this at 5.
	//
	// 注: UpThresholdMultiplier と同じく operator-facing config 化しない。
	SustainedIdleCycles int

	// CooldownDuration is the minimum time between scale events
	// (scale-up or scale-down). ADR §3.4 fixes this at 1 second.
	// Setting 0 disables cool-down entirely (every Observe tick may
	// scale), which is useful for tests but not recommended in
	// production (oscillation risk).
	//
	// 注: 上記 2 field と同じく operator-facing config 化しない。
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
// decision. The implementation has 4 stages:
//
//  1. cool-down gate: if less than CooldownDuration has elapsed since
//     the last scale event, return NoOp regardless of signal.
//  2. floor gate: if CurrentWorkers < MinWorkers, propose scale-up back to
//     MinWorkers regardless of depth. Without this, CurrentWorkers == 0 is
//     an absorbing state — the depth gate below is guarded on
//     CurrentWorkers > 0 and its threshold is CurrentWorkers × 4, so a
//     pool reported as empty could never grow again.
//  3. scale-up gate: if QueueDepth > CurrentWorkers × UpThresholdMultiplier,
//     propose additive-increase to min(CurrentWorkers + max(1, ⌈N×0.25⌉),
//     MaxWorkers). Resets the idle counter unconditionally (the load
//     surge is the canonical "definitely not idle" signal, regardless of
//     whether the controller is at-max).
//  4. sustained-idle gate: if QueueDepth == 0 AND CurrentWorkers >
//     MinWorkers, increment the idle counter; once it reaches
//     SustainedIdleCycles, propose multiplicative-decrease to
//     max(⌊CurrentWorkers × 0.5⌋, MinWorkers). Any non-zero observation
//     resets the counter. At-min skips this stage entirely (early NoOp
//     + counter reset for clean state) so the wasteful "accumulate to
//     threshold → reset → NoOp" loop never runs.
//
// At-bound cases (CurrentWorkers == MaxWorkers and we want to scale up,
// or CurrentWorkers == MinWorkers and we want to scale down) return
// NoOp rather than a scale action with delta=0 — callers should not
// call driver.Resize redundantly. Both at-bound paths also clear the
// idle counter, so state remains consistent across mode transitions.
func (c *AIMDController) Observe(metric ObservedMetric) ControlAction {
	now := c.cfg.Clock.Now()

	// cool-down: 直前の scale event から CooldownDuration 経過していない
	// なら一切判定しない。idleCycleCount も更新しないため、cool-down 中の
	// 観測値は idle 判定にも寄与しない (= cool-down 明けで再カウント開始)。
	if !c.lastScaleAt.IsZero() && now.Sub(c.lastScaleAt) < c.cfg.CooldownDuration {
		return ControlAction{Kind: ActionNoOp}
	}

	// floor 復帰: 何らかの理由で worker 数が MinWorkers を下回ったら、
	// depth を見る前に戻す。
	//
	// **これが無いと 0 が吸収状態になる。** 下の scale-up 判定は
	// `CurrentWorkers > 0` が前提で、閾値も `CurrentWorkers × 4` なので
	// 0 だと閾値が 0 になり分岐に入れない。QueueDepth != 0 なので
	// sustained-idle 側も素通りし、永久に NoOp を返し続ける。mkq driver は
	// handler が戻らなくなった worker を生存数から外すので (#2657)、
	// CurrentWorkers == 0 は実際に起こりうる。
	//
	// MinWorkers == 0 (= 明示的に「worker を置かない」) の構成では発火
	// しない。意図した停止を勝手に覆さない。
	if metric.CurrentWorkers < c.cfg.MinWorkers {
		c.idleCycleCount = 0
		c.lastScaleAt = now
		return ControlAction{Kind: ActionScaleUp, TargetWorkers: c.cfg.MinWorkers}
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
	//
	// at-min での早期 return: 既に MinWorkers なら counter 自体を進めない。
	// counter を進めた末に「threshold 到達 → reset → NoOp」の意味のないサイクル
	// を回さない最適化。counter は念のため明示リセット (操作中の MinWorkers
	// config 変更や driver 側外部リセット等で at-min に飛び込んだ際の state
	// leak を防ぐ、scale-up at-max 経路と対称)。
	if metric.QueueDepth == 0 {
		if metric.CurrentWorkers <= c.cfg.MinWorkers {
			c.idleCycleCount = 0
			return ControlAction{Kind: ActionNoOp}
		}
		c.idleCycleCount++
		if c.idleCycleCount >= c.cfg.SustainedIdleCycles {
			c.idleCycleCount = 0
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

// errInvalidConfig wraps a startup config error with the package prefix
// so operators can grep "autoscale:" in startup logs.
func errInvalidConfig(msg string) error { return fmt.Errorf("autoscale: %s", msg) }
