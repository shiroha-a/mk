package autoscale

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeClock implements Clock with a manually advanced now value. Tests
// call Advance to move time forward without sleeping. Not safe for
// concurrent use — AIMDController.Observe is not concurrent anyway
// (one ticker goroutine per controller, per ADR §3.2).
type fakeClock struct{ now time.Time }

func newFakeClock(start time.Time) *fakeClock { return &fakeClock{now: start} }
func (c *fakeClock) Now() time.Time           { return c.now }
func (c *fakeClock) Advance(d time.Duration)  { c.now = c.now.Add(d) }

// validConfig returns an AIMDConfig matching the ADR defaults so each
// test only overrides the fields it cares about. Cooldown is 1s and
// idle cycles is 5 per ADR §3.1 / §3.4.
func validConfig(clock Clock) AIMDConfig {
	return AIMDConfig{
		Queue:                 "deliver",
		MinWorkers:            4,
		MaxWorkers:            128,
		UpThresholdMultiplier: 4.0,
		SustainedIdleCycles:   5,
		CooldownDuration:      time.Second,
		Clock:                 clock,
	}
}

// TestNewAIMDController_Validation table-tests every input validation
// branch so misconfiguration surfaces at NewAIMDController, not on the
// first Observe call.
func TestNewAIMDController_Validation(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*AIMDConfig)
		wantErr bool
	}{
		{"valid", func(c *AIMDConfig) {}, false},
		{"empty Queue", func(c *AIMDConfig) { c.Queue = "" }, true},
		// Note: assert.Error checks err != nil but doesn't invoke Error().
		// TestConfigError_Message below covers the message format path.
		{"negative MinWorkers", func(c *AIMDConfig) { c.MinWorkers = -1 }, true},
		{"zero MaxWorkers", func(c *AIMDConfig) { c.MaxWorkers = 0 }, true},
		{"Min > Max", func(c *AIMDConfig) { c.MinWorkers = 200; c.MaxWorkers = 128 }, true},
		{"zero UpThresholdMultiplier", func(c *AIMDConfig) { c.UpThresholdMultiplier = 0 }, true},
		{"zero SustainedIdleCycles", func(c *AIMDConfig) { c.SustainedIdleCycles = 0 }, true},
		{"negative CooldownDuration", func(c *AIMDConfig) { c.CooldownDuration = -time.Second }, true},
		{"nil Clock falls back to systemClock", func(c *AIMDConfig) { c.Clock = nil }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConfig(newFakeClock(time.Unix(0, 0)))
			tc.mutate(&cfg)
			ctrl, err := NewAIMDController(cfg)
			if tc.wantErr {
				assert.Error(t, err)
				assert.Nil(t, ctrl)
			} else {
				require.NoError(t, err)
				assert.NotNil(t, ctrl)
			}
		})
	}
}

// TestObserve_ScaleUpTrigger verifies that depth > N×4 fires an
// additive-increase by max(1, N/4) within the [MinWorkers, MaxWorkers]
// bound, and resets the idle counter so transient backlog after idle
// scale-down doesn't double-trigger.
func TestObserve_ScaleUpTrigger(t *testing.T) {
	clock := newFakeClock(time.Unix(0, 0))
	cfg := validConfig(clock)
	cfg.MinWorkers = 1
	ctrl, err := NewAIMDController(cfg)
	require.NoError(t, err)

	// Seed the idle counter so we can verify reset on scale-up.
	for i := 0; i < 3; i++ {
		ctrl.Observe(ObservedMetric{QueueDepth: 0, CurrentWorkers: 16})
	}
	require.Equal(t, 3, ctrl.idleCycleCount, "idle counter should accumulate")
	clock.Advance(2 * time.Second) // exit cool-down (none active here)

	// depth = 65, current = 16, threshold = 16×4=64 → trigger
	action := ctrl.Observe(ObservedMetric{QueueDepth: 65, CurrentWorkers: 16})
	assert.Equal(t, ActionScaleUp, action.Kind)
	assert.Equal(t, 20, action.TargetWorkers, "16 + max(1, 16/4=4) = 20")
	assert.Equal(t, 0, ctrl.idleCycleCount, "idle counter should reset on scale-up")
}

// TestObserve_ScaleUpStep_SmallCurrent verifies the max(1, N/4) floor
// triggers correctly for small N where integer division would give 0.
func TestObserve_ScaleUpStep_SmallCurrent(t *testing.T) {
	clock := newFakeClock(time.Unix(0, 0))
	cfg := validConfig(clock)
	cfg.MinWorkers = 1
	ctrl, err := NewAIMDController(cfg)
	require.NoError(t, err)

	// current=2 → threshold=8 → depth=9 → trigger.
	// 2/4 = 0 (integer div), so step floors to 1 → target = 3.
	action := ctrl.Observe(ObservedMetric{QueueDepth: 9, CurrentWorkers: 2})
	assert.Equal(t, ActionScaleUp, action.Kind)
	assert.Equal(t, 3, action.TargetWorkers, "2 + max(1, 2/4=0) = 3")
}

// TestObserve_ScaleUpCappedAtMax verifies that an at-max controller
// returns NoOp (not a scale event with delta=0). Caller should never
// invoke driver.Resize when target == current.
func TestObserve_ScaleUpCappedAtMax(t *testing.T) {
	clock := newFakeClock(time.Unix(0, 0))
	cfg := validConfig(clock)
	cfg.MaxWorkers = 16
	ctrl, err := NewAIMDController(cfg)
	require.NoError(t, err)

	// depth = 1000, current = 16 (= MaxWorkers) → would-be target capped.
	action := ctrl.Observe(ObservedMetric{QueueDepth: 1000, CurrentWorkers: 16})
	assert.Equal(t, ActionNoOp, action.Kind, "no scale event when already at MaxWorkers")
	assert.Equal(t, 0, action.TargetWorkers)
}

// TestObserve_ScaleUpClampedToMax verifies that proposed target > Max
// gets clamped down (not rejected as NoOp) when current < Max.
func TestObserve_ScaleUpClampedToMax(t *testing.T) {
	clock := newFakeClock(time.Unix(0, 0))
	cfg := validConfig(clock)
	cfg.MaxWorkers = 20
	ctrl, err := NewAIMDController(cfg)
	require.NoError(t, err)

	// current=16, step = max(1, 16/4=4) = 4 → target = 20 = MaxWorkers.
	action := ctrl.Observe(ObservedMetric{QueueDepth: 1000, CurrentWorkers: 16})
	assert.Equal(t, ActionScaleUp, action.Kind)
	assert.Equal(t, 20, action.TargetWorkers, "target clamped to MaxWorkers")
}

// TestObserve_SustainedIdle verifies that 5 consecutive empty-queue
// observations trigger scale-down, but any non-zero in between resets
// the counter (= hysteresis against transient catch-up).
func TestObserve_SustainedIdle(t *testing.T) {
	clock := newFakeClock(time.Unix(0, 0))
	cfg := validConfig(clock)
	ctrl, err := NewAIMDController(cfg)
	require.NoError(t, err)

	current := 16
	for i := 0; i < 4; i++ {
		action := ctrl.Observe(ObservedMetric{QueueDepth: 0, CurrentWorkers: current})
		assert.Equal(t, ActionNoOp, action.Kind, "cycle %d should not trigger yet", i+1)
		clock.Advance(time.Second + time.Millisecond) // exit cool-down between observations
	}
	assert.Equal(t, 4, ctrl.idleCycleCount)

	// 5 cycle 目で発火。
	action := ctrl.Observe(ObservedMetric{QueueDepth: 0, CurrentWorkers: current})
	assert.Equal(t, ActionScaleDown, action.Kind)
	assert.Equal(t, 8, action.TargetWorkers, "16/2 = 8")
	assert.Equal(t, 0, ctrl.idleCycleCount, "counter resets after scale-down")
}

// TestObserve_SustainedIdle_ResetByNonZero verifies that a single
// non-zero observation resets the idle counter (hysteresis).
func TestObserve_SustainedIdle_ResetByNonZero(t *testing.T) {
	clock := newFakeClock(time.Unix(0, 0))
	cfg := validConfig(clock)
	ctrl, err := NewAIMDController(cfg)
	require.NoError(t, err)

	current := 16
	// 4 idle observations.
	for i := 0; i < 4; i++ {
		ctrl.Observe(ObservedMetric{QueueDepth: 0, CurrentWorkers: current})
		clock.Advance(time.Second + time.Millisecond)
	}
	require.Equal(t, 4, ctrl.idleCycleCount)

	// 1 non-zero observation (= queue caught up momentarily) resets.
	action := ctrl.Observe(ObservedMetric{QueueDepth: 3, CurrentWorkers: current})
	assert.Equal(t, ActionNoOp, action.Kind, "below up-threshold and non-zero = NoOp")
	assert.Equal(t, 0, ctrl.idleCycleCount, "idle counter resets on non-zero observation")

	// Resuming idle observations: counter starts fresh from 0, so 5 more
	// idle cycles are needed before the next scale-down.
	for i := 0; i < 4; i++ {
		clock.Advance(time.Second + time.Millisecond)
		action := ctrl.Observe(ObservedMetric{QueueDepth: 0, CurrentWorkers: current})
		assert.Equal(t, ActionNoOp, action.Kind, "still in idle accumulation")
	}
	clock.Advance(time.Second + time.Millisecond)
	action = ctrl.Observe(ObservedMetric{QueueDepth: 0, CurrentWorkers: current})
	assert.Equal(t, ActionScaleDown, action.Kind, "5th idle cycle (post-reset) triggers scale-down")
}

// TestObserve_ScaleDownFlooredAtMin verifies that proposed scale-down
// target < MinWorkers gets clamped up to MinWorkers.
func TestObserve_ScaleDownFlooredAtMin(t *testing.T) {
	clock := newFakeClock(time.Unix(0, 0))
	cfg := validConfig(clock)
	cfg.MinWorkers = 4
	cfg.SustainedIdleCycles = 1 // shortcut for the test
	ctrl, err := NewAIMDController(cfg)
	require.NoError(t, err)

	// current = 6, 6/2 = 3 < MinWorkers=4 → target clamped to 4.
	action := ctrl.Observe(ObservedMetric{QueueDepth: 0, CurrentWorkers: 6})
	assert.Equal(t, ActionScaleDown, action.Kind)
	assert.Equal(t, 4, action.TargetWorkers, "6/2 = 3 → clamped up to MinWorkers=4")
}

// TestObserve_ScaleDownNoopAtMin verifies that an at-min controller
// returns NoOp on sustained idle (no redundant Resize call) AND that
// the early-return optimisation keeps the idle counter at 0 (= we
// avoid the wasteful "accumulate to threshold → reset → NoOp" loop).
//
// Additionally exercises the explicit counter reset on at-min entry
// (= mid-cycle transition to at-min from a partially-accumulated state
// clears the counter, matching the scale-up at-max symmetry).
func TestObserve_ScaleDownNoopAtMin(t *testing.T) {
	clock := newFakeClock(time.Unix(0, 0))
	cfg := validConfig(clock)
	cfg.MinWorkers = 4
	cfg.SustainedIdleCycles = 5
	ctrl, err := NewAIMDController(cfg)
	require.NoError(t, err)

	// 10 sustained-idle observations at MinWorkers — counter should
	// never advance because the at-min early return skips increment.
	for i := 0; i < 10; i++ {
		action := ctrl.Observe(ObservedMetric{QueueDepth: 0, CurrentWorkers: 4})
		assert.Equal(t, ActionNoOp, action.Kind, "tick %d should be NoOp", i+1)
		clock.Advance(time.Second + time.Millisecond)
	}
	assert.Equal(t, 0, ctrl.idleCycleCount,
		"at-min idle path must not advance the idle counter (early-return optimisation)")

	// mid-cycle transition: 先に counter を 3 まで貯めてから at-min に
	// 飛び込み、明示リセットされることを確認 (driver 側で外部に worker を
	// 削減される等のレアケース対策)。
	ctrl.idleCycleCount = 3
	action := ctrl.Observe(ObservedMetric{QueueDepth: 0, CurrentWorkers: 4})
	assert.Equal(t, ActionNoOp, action.Kind)
	assert.Equal(t, 0, ctrl.idleCycleCount,
		"at-min early return must reset the idle counter (symmetry with scale-up at-max reset)")
}

// TestObserve_CooldownGate verifies that a scale-up event followed by
// another scale-eligible observation within CooldownDuration returns
// NoOp. After cool-down expiry, the next observation can scale again.
func TestObserve_CooldownGate(t *testing.T) {
	clock := newFakeClock(time.Unix(0, 0))
	cfg := validConfig(clock)
	cfg.MinWorkers = 1
	ctrl, err := NewAIMDController(cfg)
	require.NoError(t, err)

	// First trigger: scale-up.
	action := ctrl.Observe(ObservedMetric{QueueDepth: 100, CurrentWorkers: 16})
	require.Equal(t, ActionScaleUp, action.Kind)

	// Within cool-down: NoOp even if signal still says scale up.
	clock.Advance(500 * time.Millisecond)
	action = ctrl.Observe(ObservedMetric{QueueDepth: 200, CurrentWorkers: 20})
	assert.Equal(t, ActionNoOp, action.Kind, "scale-up blocked during cool-down")

	// After cool-down: signal evaluated again.
	clock.Advance(600 * time.Millisecond) // total 1100ms > 1s cool-down
	action = ctrl.Observe(ObservedMetric{QueueDepth: 200, CurrentWorkers: 20})
	assert.Equal(t, ActionScaleUp, action.Kind, "scale-up unblocked after cool-down")
}

// TestObserve_CooldownDoesNotAdvanceIdleCounter verifies that
// observations during cool-down do not contribute to the idle counter
// (= cool-down ⇒ fully suppressed observation).
func TestObserve_CooldownDoesNotAdvanceIdleCounter(t *testing.T) {
	clock := newFakeClock(time.Unix(0, 0))
	cfg := validConfig(clock)
	ctrl, err := NewAIMDController(cfg)
	require.NoError(t, err)

	// Trigger a scale-up to start cool-down.
	ctrl.Observe(ObservedMetric{QueueDepth: 100, CurrentWorkers: 16})
	require.False(t, ctrl.lastScaleAt.IsZero(), "scale-up must have set lastScaleAt")

	// 10 idle observations during cool-down → no counter advance.
	for i := 0; i < 10; i++ {
		clock.Advance(50 * time.Millisecond) // stays under 1s cool-down
		ctrl.Observe(ObservedMetric{QueueDepth: 0, CurrentWorkers: 20})
	}
	assert.Equal(t, 0, ctrl.idleCycleCount,
		"idle counter should not advance during cool-down")
}

// TestObserve_ZeroCurrentWorkersIsNoOp verifies that a controller with
// no workers running yet does not try to scale up (depth > 0 × N = 0
// would always be true). Real-world: Server not yet Start()ed, driver
// returns 0 from WorkerCount.
func TestObserve_ZeroCurrentWorkersIsNoOp(t *testing.T) {
	clock := newFakeClock(time.Unix(0, 0))
	cfg := validConfig(clock)
	ctrl, err := NewAIMDController(cfg)
	require.NoError(t, err)

	action := ctrl.Observe(ObservedMetric{QueueDepth: 1000, CurrentWorkers: 0})
	assert.Equal(t, ActionNoOp, action.Kind,
		"no scale-up when CurrentWorkers == 0 (= driver not started)")
}

// TestObserve_BelowUpThreshold verifies the canonical NoOp case
// (queue has some load but not enough to trigger scale-up).
func TestObserve_BelowUpThreshold(t *testing.T) {
	clock := newFakeClock(time.Unix(0, 0))
	cfg := validConfig(clock)
	ctrl, err := NewAIMDController(cfg)
	require.NoError(t, err)

	// current=16, threshold = 16×4 = 64, depth=50 (below).
	action := ctrl.Observe(ObservedMetric{QueueDepth: 50, CurrentWorkers: 16})
	assert.Equal(t, ActionNoOp, action.Kind)
	assert.Equal(t, 0, ctrl.idleCycleCount, "non-idle observation keeps counter at 0")
}

// TestObserve_ImplementsControllerInterface guards against accidental
// interface drift — if Controller signature changes, this test stops
// compiling.
func TestObserve_ImplementsControllerInterface(t *testing.T) {
	cfg := validConfig(newFakeClock(time.Unix(0, 0)))
	ctrl, err := NewAIMDController(cfg)
	require.NoError(t, err)
	var _ Controller = ctrl
}

// TestSystemClock_NowAdvances is a smoke check that the default
// systemClock returns monotonically advancing time (= we did not
// accidentally hard-code a constant).
func TestSystemClock_NowAdvances(t *testing.T) {
	c := systemClock{}
	t0 := c.Now()
	time.Sleep(time.Millisecond)
	t1 := c.Now()
	assert.True(t, t1.After(t0), "systemClock.Now should advance")
}

// TestConfigError_Message verifies the error message format includes the
// package prefix so operators can grep for "autoscale:" in startup logs.
func TestConfigError_Message(t *testing.T) {
	err := errInvalidConfig("MinWorkers must be >= 0")
	require.Error(t, err)
	assert.Equal(t, "autoscale: MinWorkers must be >= 0", err.Error())
}

// BenchmarkObserve provides a baseline for AIMDController.Observe latency
// so a future PI / PID controller swap (per ADR §3.1) can be measured
// against this. Observe is called once per controller per cool-down (~1s
// in production), so absolute speed is not a hot path concern — but the
// benchmark catches accidental allocations and serves as a comparison
// anchor.
func BenchmarkObserve(b *testing.B) {
	ctrl, err := NewAIMDController(validConfig(newFakeClock(time.Unix(0, 0))))
	if err != nil {
		b.Fatal(err)
	}
	metric := ObservedMetric{QueueDepth: 50, CurrentWorkers: 16}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ctrl.Observe(metric)
	}
}
