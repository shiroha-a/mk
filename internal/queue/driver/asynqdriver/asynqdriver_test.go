package asynqdriver

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/hibiken/asynq"

	"github.com/shiroha-a/mk/internal/config"
	"github.com/shiroha-a/mk/internal/queue/driver"
)

func TestBuildRedisOpt_TCP(t *testing.T) {
	got := BuildRedisOpt(config.RedisOptions{
		Host: "redis.example",
		Port: 16379,
		Pass: "p",
		DB:   3,
	})
	want := asynq.RedisClientOpt{
		Addr:     "redis.example:16379",
		Password: "p",
		DB:       3,
	}
	if got != want {
		t.Fatalf("got %#v want %#v", got, want)
	}
}

func TestBuildRedisOpt_UnixSocket(t *testing.T) {
	got := BuildRedisOpt(config.RedisOptions{
		Host: "/tmp/redis.sock",
		Pass: "p",
	})
	want := asynq.RedisClientOpt{
		Network:  "unix",
		Addr:     "/tmp/redis.sock",
		Password: "p",
	}
	if got != want {
		t.Fatalf("got %#v want %#v", got, want)
	}
}

func TestBuildRedisOpt_PoolSize(t *testing.T) {
	pool := 64
	got := BuildRedisOpt(config.RedisOptions{
		Host:     "redis.example",
		Port:     6379,
		PoolSize: &pool,
	})
	if got.PoolSize != 64 {
		t.Fatalf("PoolSize: want 64, got %d", got.PoolSize)
	}
}

func TestBuildRedisOpt_PoolSizeNilKeepsDefault(t *testing.T) {
	got := BuildRedisOpt(config.RedisOptions{
		Host: "redis.example",
		Port: 6379,
	})
	if got.PoolSize != 0 {
		t.Fatalf("PoolSize: want 0 (default), got %d", got.PoolSize)
	}
}

func TestToAsynqOptions(t *testing.T) {
	o := driver.ApplyEnqueueOptions([]driver.EnqueueOption{
		driver.WithQueue("deliver"),
		driver.WithMaxRetry(0),
		driver.WithUnique(time.Hour),
		driver.WithProcessIn(2 * time.Second),
	})
	got := toAsynqOptions(o)
	if len(got) != 4 {
		t.Fatalf("expected 4 options, got %d (%+v)", len(got), got)
	}
}

func TestToAsynqOptions_DefaultsSkipped(t *testing.T) {
	got := toAsynqOptions(driver.EnqueueOptions{})
	if len(got) != 0 {
		t.Fatalf("expected no options for zero EnqueueOptions, got %d", len(got))
	}
}

func TestToAsynqOptions_MaxRetryRequiresExplicit(t *testing.T) {
	// MaxRetrySet=false means "use driver default" so the option
	// must NOT be added even though MaxRetry==0.
	got := toAsynqOptions(driver.EnqueueOptions{MaxRetry: 5})
	if len(got) != 0 {
		t.Fatalf("MaxRetry without MaxRetrySet must be skipped, got %d", len(got))
	}
}

// TestToAsynqOptions_KeepFailedIsSilentNoOp: asynq には per-job 相当
// API が無いので driver-neutral `WithKeepFailed` は silent no-op で
// なければならない (#1184 の driver semantics)。
func TestToAsynqOptions_KeepFailedIsSilentNoOp(t *testing.T) {
	got := toAsynqOptions(driver.EnqueueOptions{KeepFailed: 100, KeepFailedSet: true})
	if len(got) != 0 {
		t.Fatalf("KeepFailed must be silently ignored by asynqdriver, got %d options", len(got))
	}
}

// fakeRedis returns a non-routable RedisClientOpt; we never Start the
// server in these tests so the address is fine. asynq client/inspector
// constructors do not Dial until the first request.
func fakeRedis() asynq.RedisClientOpt {
	return asynq.RedisClientOpt{Addr: "127.0.0.1:0"}
}

func TestDriver_LazyConstruction(t *testing.T) {
	d := New(fakeRedis(), ServerConfig{Concurrency: 4})

	if d.client != nil || d.server != nil || d.inspector != nil || d.scheduler != nil {
		t.Fatalf("Driver fields must be nil before first access")
	}

	if d.Client() == nil {
		t.Fatal("Client must be non-nil")
	}
	if d.client == nil {
		t.Fatal("Client field must be cached after access")
	}

	if d.Server() == nil {
		t.Fatal("Server must be non-nil")
	}
	if d.Inspector() == nil {
		t.Fatal("Inspector must be non-nil")
	}
	if d.Scheduler() == nil {
		t.Fatal("Scheduler must be non-nil")
	}

	// 2nd call must return the cached instance.
	first := d.Client()
	second := d.Client()
	if first != second {
		t.Fatal("Client must be cached")
	}
}

func TestDriver_Close_NoComponents(t *testing.T) {
	d := New(fakeRedis(), ServerConfig{})
	if err := d.Close(); err != nil {
		t.Fatalf("Close on unused Driver: %v", err)
	}
}

func TestDriver_Close_ClosesConstructedComponents(t *testing.T) {
	d := New(fakeRedis(), ServerConfig{})
	_ = d.Client()
	_ = d.Inspector()
	if err := d.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestServer_HandleSkipRetryConversion(t *testing.T) {
	s := NewServer(fakeRedis(), ServerConfig{})
	wrapped := fmt.Errorf("decode: %w", driver.SkipRetry)

	var captured error
	s.Handle("dummy", func(_ context.Context, _ driver.Task) error {
		return wrapped
	})

	// Pull the registered handler out of the mux and invoke it
	// directly to verify the SkipRetry conversion. asynq's ServeMux
	// exposes the handler via Handler(*asynq.Task) but only when a
	// task type matches; we synthesize the task here.
	t.Helper()
	h, _ := s.mux.Handler(asynq.NewTask("dummy", nil))
	if h == nil {
		t.Fatal("handler not registered")
	}
	captured = h.ProcessTask(context.Background(), asynq.NewTask("dummy", nil))
	if !errors.Is(captured, asynq.SkipRetry) {
		t.Fatalf("captured error must wrap asynq.SkipRetry, got %v", captured)
	}
	if !errors.Is(captured, driver.SkipRetry) {
		t.Fatalf("captured error must still wrap driver.SkipRetry, got %v", captured)
	}
}

func TestServer_HandlePassesNonSkipErrorThrough(t *testing.T) {
	s := NewServer(fakeRedis(), ServerConfig{})
	want := errors.New("transient")
	s.Handle("plain", func(_ context.Context, _ driver.Task) error {
		return want
	})

	h, _ := s.mux.Handler(asynq.NewTask("plain", nil))
	got := h.ProcessTask(context.Background(), asynq.NewTask("plain", nil))
	if !errors.Is(got, want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
	if errors.Is(got, asynq.SkipRetry) {
		t.Fatal("non-SkipRetry handler error must not be tagged with asynq.SkipRetry")
	}
}

func TestServer_DefaultQueueWeights(t *testing.T) {
	// Queues=nil → default fillup applies.
	s := NewServer(fakeRedis(), ServerConfig{Queues: nil})
	if s.inner == nil {
		t.Fatal("inner asynq.Server not constructed")
	}
}

func TestNewServer_ConcurrencyDefault(t *testing.T) {
	// Concurrency<=0 falls back to 16 (covered indirectly — verify
	// constructor does not panic and produces an inner server).
	s := NewServer(fakeRedis(), ServerConfig{})
	if s.inner == nil || s.mux == nil {
		t.Fatal("Server must be fully constructed")
	}
}

// #495: rate limit middleware を入れずに builder を呼ぶと nil が返り
// dispatch fast-path が触らない (allocation-free)。
func TestBuildRateLimitMiddleware_EmptyMapReturnsNil(t *testing.T) {
	if mw := buildRateLimitMiddleware(nil); mw != nil {
		t.Fatal("nil rates map should yield nil middleware")
	}
	if mw := buildRateLimitMiddleware(map[string]int{"deliver": 0, "push": -1}); mw != nil {
		t.Fatal("non-positive rates should yield nil middleware (no limiter active)")
	}
}

// rate>0 が 1 つでもあれば middleware を返し、対象 queue の handler は
// limiter.Wait を経由する。Wait は token があれば即座に返るため、
// 1 token 取れることだけ確認する。
func TestBuildRateLimitMiddleware_RateAppliedPerQueue(t *testing.T) {
	mw := buildRateLimitMiddleware(map[string]int{"deliver": 100})
	if mw == nil {
		t.Fatal("middleware should be non-nil when at least one queue has rate>0")
	}
	called := false
	wrapped := mw(asynq.HandlerFunc(func(_ context.Context, _ *asynq.Task) error {
		called = true
		return nil
	}))
	// queue 名を引かないのは context に未設定でも middleware が落ちず
	// 通過する確認も兼ねる (qname="" は limiter map で未ヒット → 素通り)。
	if err := wrapped.ProcessTask(context.Background(), asynq.NewTask("t", nil)); err != nil {
		t.Fatalf("ProcessTask: %v", err)
	}
	if !called {
		t.Fatal("inner handler should have been invoked")
	}
}

func TestAsynqTask_Wrapper(t *testing.T) {
	t1 := asynq.NewTask("foo", []byte("bar"))
	w := asynqTask{t: t1}
	if w.Type() != "foo" {
		t.Fatalf("Type: got %q", w.Type())
	}
	if string(w.Payload()) != "bar" {
		t.Fatalf("Payload: got %q", string(w.Payload()))
	}
}
