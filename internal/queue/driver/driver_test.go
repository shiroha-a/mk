package driver

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestRawTask(t *testing.T) {
	body := []byte("hello")
	rt := RawTask{TypeName: "x", Body: body}
	if rt.Type() != "x" {
		t.Fatalf("Type: got %q", rt.Type())
	}
	if string(rt.Payload()) != "hello" {
		t.Fatalf("Payload: got %q", string(rt.Payload()))
	}
}

func TestSkipRetryIdentity(t *testing.T) {
	wrapped := fmt.Errorf("decode: %w: %w", errors.New("boom"), SkipRetry)
	if !errors.Is(wrapped, SkipRetry) {
		t.Fatalf("errors.Is should match SkipRetry")
	}
}

func TestApplyEnqueueOptions(t *testing.T) {
	got := ApplyEnqueueOptions([]EnqueueOption{
		WithQueue("deliver"),
		WithMaxRetry(3),
		WithUnique(time.Hour),
		WithProcessIn(5 * time.Second),
		WithKeepFailed(500),
	})
	want := EnqueueOptions{
		Queue:         "deliver",
		MaxRetry:      3,
		MaxRetrySet:   true,
		UniqueTTL:     time.Hour,
		ProcessIn:     5 * time.Second,
		KeepFailed:    500,
		KeepFailedSet: true,
	}
	if got != want {
		t.Fatalf("got %#v want %#v", got, want)
	}
}

func TestApplyEnqueueOptionsZeroSentinel(t *testing.T) {
	// MaxRetrySet must distinguish "explicit 0" from "default" so
	// drivers can decide between "use default" vs "no retries".
	got := ApplyEnqueueOptions([]EnqueueOption{WithMaxRetry(0)})
	if got.MaxRetry != 0 || !got.MaxRetrySet {
		t.Fatalf("explicit zero not recorded: %#v", got)
	}

	def := ApplyEnqueueOptions(nil)
	if def.MaxRetrySet {
		t.Fatalf("default options must not record MaxRetrySet")
	}
}

// TestApplyEnqueueOptions_KeepFailedZeroSentinel: WithKeepFailed(0) は
// 「operator が明示的に retention 解除 (= unlimited 蓄積)」を表す。
// 未指定 (= default) と区別するため KeepFailedSet を使う (#1184)。
func TestApplyEnqueueOptions_KeepFailedZeroSentinel(t *testing.T) {
	got := ApplyEnqueueOptions([]EnqueueOption{WithKeepFailed(0)})
	if got.KeepFailed != 0 || !got.KeepFailedSet {
		t.Fatalf("explicit zero not recorded: %#v", got)
	}

	def := ApplyEnqueueOptions(nil)
	if def.KeepFailedSet {
		t.Fatalf("default options must not record KeepFailedSet")
	}
}
