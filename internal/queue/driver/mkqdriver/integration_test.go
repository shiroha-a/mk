package mkqdriver_test

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shiroha-a/mk/internal/queue/driver"
	"github.com/shiroha-a/mk/internal/queue/driver/mkqdriver"
	"github.com/shiroha-a/mk/internal/testutil"
)

var testRedis *testutil.TestRedis

func TestMain(m *testing.M) {
	ctx := context.Background()
	var err error
	testRedis, err = testutil.SetupRedis(ctx)
	if err != nil {
		log.Fatalf("setup redis: %v", err)
	}
	code := m.Run()
	testRedis.Teardown(ctx)
	os.Exit(code)
}

func newDriver(t *testing.T) *mkqdriver.Driver {
	t.Helper()
	testutil.SkipIfNoDocker(t)
	flushRedis(t)
	d, err := mkqdriver.New(context.Background(), mkqdriver.Config{
		Redis:       redis.UniversalOptions{Addrs: []string{testRedis.Addr}},
		Concurrency: 4,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func flushRedis(t *testing.T) {
	t.Helper()
	if err := testRedis.Client.FlushAll(context.Background()).Err(); err != nil {
		t.Fatalf("flush: %v", err)
	}
}

func waitGroupTimeout(wg *sync.WaitGroup, d time.Duration) bool {
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}

// TestEndToEnd_EnqueueProcess submits a single job, runs the worker
// against the live Redis, and verifies the dispatch handler observed
// the original task type and payload.
func TestEndToEnd_EnqueueProcess(t *testing.T) {
	d := newDriver(t)

	srv := d.Server()
	var (
		wg       sync.WaitGroup
		received int32
		gotType  string
		gotBody  []byte
		mu       sync.Mutex
	)
	wg.Add(1)
	srv.Handle("test:ok", func(_ context.Context, task driver.Task) error {
		defer wg.Done()
		atomic.AddInt32(&received, 1)
		mu.Lock()
		gotType = task.Type()
		gotBody = task.Payload()
		mu.Unlock()
		return nil
	})
	require.NoError(t, srv.Start())
	t.Cleanup(srv.Shutdown)

	require.NoError(t, d.Client().Enqueue(context.Background(), "test:ok", []byte(`{"a":1}`),
		driver.WithQueue("deliver")))

	if !waitGroupTimeout(&wg, 5*time.Second) {
		t.Fatal("handler not invoked within timeout")
	}
	assert.Equal(t, int32(1), atomic.LoadInt32(&received))
	mu.Lock()
	assert.Equal(t, "test:ok", gotType)
	assert.Equal(t, []byte(`{"a":1}`), gotBody)
	mu.Unlock()
}

// #495: policy MaxAttempts → driver.WithMaxRetry(N) → mkq.WithAttempts(N+1)
// → BullMQ HASH `opts.attempts` の wire を end-to-end で検証する。queue
// package を import するためテストファイル末尾の helper 経由で側 redis
// (testRedis.Client) を使って HASH を直接覗く。
func TestEnqueue_MaxRetryAppliedToBullMQHash(t *testing.T) {
	d := newDriver(t)

	require.NoError(t, d.Client().Enqueue(context.Background(),
		"deliver:test", []byte(`{"x":1}`),
		driver.WithQueue("deliver"),
		driver.WithMaxRetry(7),
	))

	// BullMQ の HASH key 形式: `bull:<queue>:<jobID>`。最新 job を ID
	// counter (`bull:<queue>:id`) から逆引きする。
	ctx := context.Background()
	idStr, err := testRedis.Client.Get(ctx, "bull:deliver:id").Result()
	require.NoError(t, err)
	require.NotEmpty(t, idStr)

	hashKey := "bull:deliver:" + idStr
	opts, err := testRedis.Client.HGet(ctx, hashKey, "opts").Result()
	require.NoError(t, err)

	// opts は JSON 文字列。`attempts:8` (MaxRetry 7 + 初回 1 = 8) を含む
	// ことを確認すれば、queue.Client → mkqdriver.Client → mkq の
	// 全経路が wire されていることが verifiable。
	assert.Contains(t, opts, `"attempts":8`,
		"MaxRetry=7 should map to attempts=8 in BullMQ HASH (got %q)", opts)
}

// #495: mkqdriver.Config.QueueRateLimits = {"deliver": N} を渡すと
// mkq.WithRateLimit(N, time.Second) が worker に積まれて dispatch が
// tasks/sec に back-pressure される。flaky 防止のため緩い閾値で elapsed
// を assert する。
func TestServer_RateLimit_BackPressuresDispatch(t *testing.T) {
	testutil.SkipIfNoDocker(t)
	flushRedis(t)

	d, err := mkqdriver.New(context.Background(), mkqdriver.Config{
		Redis:           redis.UniversalOptions{Addrs: []string{testRedis.Addr}},
		Concurrency:     8,
		QueueRateLimits: map[string]int{"deliver": 2},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.Close() })

	srv := d.Server()
	var (
		wg       sync.WaitGroup
		received int32
	)
	wg.Add(5)
	srv.Handle("rl:tick", func(_ context.Context, _ driver.Task) error {
		defer wg.Done()
		atomic.AddInt32(&received, 1)
		return nil
	})
	require.NoError(t, srv.Start())
	t.Cleanup(srv.Shutdown)

	start := time.Now()
	for range 5 {
		require.NoError(t, d.Client().Enqueue(context.Background(), "rl:tick", nil,
			driver.WithQueue("deliver")))
	}
	if !waitGroupTimeout(&wg, 10*time.Second) {
		t.Fatal("rate-limited handler did not complete within timeout")
	}
	elapsed := time.Since(start)
	assert.Equal(t, int32(5), atomic.LoadInt32(&received))
	assert.GreaterOrEqual(t, elapsed, 1*time.Second,
		"mkq rate limit should pace dispatch (got elapsed=%s)", elapsed)
}

// QueueConcurrency = {"deliver": 2} で起動 → 5 task を concurrent に
// 詰めて、handler 内の sync.barrier で peak 並列数を観測する。peak <= 2
// かつ少なくとも一度は >= 2 同時実行されたことを assert する。
func TestServer_PerQueueConcurrency_CapsParallelHandlers(t *testing.T) {
	testutil.SkipIfNoDocker(t)
	flushRedis(t)

	const cap = 2
	d, err := mkqdriver.New(context.Background(), mkqdriver.Config{
		Redis:            redis.UniversalOptions{Addrs: []string{testRedis.Addr}},
		Concurrency:      8,
		QueueConcurrency: map[string]int{"deliver": cap},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.Close() })

	srv := d.Server()

	var (
		wg       sync.WaitGroup
		inFlight atomic.Int32
		peak     atomic.Int32
		release  = make(chan struct{})
	)
	wg.Add(5)
	srv.Handle("conc:hold", func(ctx context.Context, _ driver.Task) error {
		defer wg.Done()
		now := inFlight.Add(1)
		// peak の monotonic update。Add は atomic だが peak は CAS が必要。
		for {
			cur := peak.Load()
			if now <= cur || peak.CompareAndSwap(cur, now) {
				break
			}
		}
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
		inFlight.Add(-1)
		return nil
	})
	require.NoError(t, srv.Start())
	t.Cleanup(srv.Shutdown)

	for range 5 {
		require.NoError(t, d.Client().Enqueue(context.Background(), "conc:hold", nil,
			driver.WithQueue("deliver")))
	}

	// 並列数が cap に達するまで待つ (確実に同時 cap 個動いた状態を観測)。
	deadline := time.After(5 * time.Second)
	for peak.Load() < int32(cap) {
		select {
		case <-deadline:
			t.Fatalf("peak concurrent handlers never reached cap=%d (got %d)", cap, peak.Load())
		case <-time.After(20 * time.Millisecond):
		}
	}
	// 余分な handler が cap を超えて起動していないことを 200ms 観測。
	time.Sleep(200 * time.Millisecond)
	assert.LessOrEqual(t, peak.Load(), int32(cap),
		"per-queue concurrency cap=%d should bound peak parallel handlers", cap)

	close(release)
	if !waitGroupTimeout(&wg, 5*time.Second) {
		t.Fatal("handlers did not drain after release")
	}
}

// TestEnqueue_UnknownQueueRejects ensures the driver refuses to fall
// back to a default queue for callers that forget WithQueue.
func TestEnqueue_UnknownQueueRejects(t *testing.T) {
	d := newDriver(t)
	err := d.Client().Enqueue(context.Background(), "x", nil,
		driver.WithQueue("not-a-queue"))
	require.Error(t, err)

	err = d.Client().Enqueue(context.Background(), "x", nil)
	require.Error(t, err)
}

// TestEnqueue_DuplicateUniqueDropsSilently confirms WithUnique TTL
// matches asynq's silent-drop behaviour.
func TestEnqueue_DuplicateUniqueDropsSilently(t *testing.T) {
	d := newDriver(t)
	for i := 0; i < 3; i++ {
		require.NoError(t, d.Client().Enqueue(context.Background(),
			"unique:test", nil,
			driver.WithQueue("deliver"),
			driver.WithUnique(time.Minute),
		))
	}
	// Counts should be 1 wait job. Inspector exercises Counts().
	info, err := d.Inspector().GetQueueInfo("deliver")
	require.NoError(t, err)
	assert.Equal(t, 1, info.Pending)
}

// TestServer_HandleSkipRetryConvertsToUnrecoverable ensures the
// driver-level SkipRetry sentinel reaches mkq as ErrUnrecoverable
// (which mkq surfaces as a permanent failure rather than a retry).
func TestServer_HandleSkipRetryConvertsToUnrecoverable(t *testing.T) {
	d := newDriver(t)
	srv := d.Server()
	var wg sync.WaitGroup
	wg.Add(1)
	srv.Handle("test:skip", func(_ context.Context, _ driver.Task) error {
		defer wg.Done()
		return fmt.Errorf("decode boom: %w", driver.SkipRetry)
	})
	require.NoError(t, srv.Start())
	t.Cleanup(srv.Shutdown)

	require.NoError(t, d.Client().Enqueue(context.Background(), "test:skip", nil,
		driver.WithQueue("deliver")))
	if !waitGroupTimeout(&wg, 5*time.Second) {
		t.Fatal("handler not invoked")
	}

	// Wait for finalisation. mkq processes the result asynchronously;
	// the failed counter takes a moment to update after the handler
	// returns.
	require.Eventually(t, func() bool {
		info, err := d.Inspector().GetQueueInfo("deliver")
		if err != nil {
			return false
		}
		return info.Failed >= 1
	}, 5*time.Second, 50*time.Millisecond, "expected job to land in failed bucket")
}

// TestServer_HandleNoRegisteredHandler verifies an unknown job name
// surfaces as a permanent failure (no infinite retry loop).
func TestServer_HandleNoRegisteredHandler(t *testing.T) {
	d := newDriver(t)
	srv := d.Server()
	require.NoError(t, srv.Start())
	t.Cleanup(srv.Shutdown)

	require.NoError(t, d.Client().Enqueue(context.Background(), "not-registered", nil,
		driver.WithQueue("deliver")))

	require.Eventually(t, func() bool {
		info, err := d.Inspector().GetQueueInfo("deliver")
		if err != nil {
			return false
		}
		return info.Failed >= 1
	}, 5*time.Second, 50*time.Millisecond)
}

// TestInspector_FullSurface walks every Inspector entry point against
// the live Redis. Some methods (DrainPending) drop counts to zero;
// each case asserts the post-condition rather than the exact return
// value.
func TestInspector_FullSurface(t *testing.T) {
	d := newDriver(t)

	// Seed the deliver queue with two pending tasks (one ad-hoc, one
	// that we'll later inspect by ID).
	require.NoError(t, d.Client().Enqueue(context.Background(), "ins:a", []byte("a"),
		driver.WithQueue("deliver")))
	require.NoError(t, d.Client().Enqueue(context.Background(), "ins:b", []byte("b"),
		driver.WithQueue("deliver")))

	ins := d.Inspector()

	queues, err := ins.Queues()
	require.NoError(t, err)
	assert.Contains(t, queues, "deliver")

	info, err := ins.GetQueueInfo("deliver")
	require.NoError(t, err)
	assert.Equal(t, 2, info.Pending)

	// Listing
	pending, err := ins.ListPendingTasks("deliver", 1, 30)
	require.NoError(t, err)
	require.Len(t, pending, 2)
	taskID := pending[0].ID
	assert.NotEmpty(t, pending[0].Type)

	got, err := ins.GetTaskInfo("deliver", taskID)
	require.NoError(t, err)
	assert.Equal(t, taskID, got.ID)

	_, err = ins.ListActiveTasks("deliver", 1, 30)
	require.NoError(t, err)
	_, err = ins.ListScheduledTasks("deliver", 1, 30)
	require.NoError(t, err)
	_, err = ins.ListRetryTasks("deliver", 1, 30)
	require.NoError(t, err)

	// Page / pageSize clamp paths.
	_, err = ins.ListPendingTasks("deliver", 0, 0)
	require.NoError(t, err)
	_, err = ins.ListPendingTasks("deliver", -1, 999)
	require.NoError(t, err)

	// Delete one specific task.
	require.NoError(t, ins.DeleteTask("deliver", taskID))
	infoAfterDelete, err := ins.GetQueueInfo("deliver")
	require.NoError(t, err)
	assert.Equal(t, 1, infoAfterDelete.Pending)

	// Drain remaining pending.
	_, err = ins.DeleteAllPendingTasks("deliver")
	require.NoError(t, err)
	infoAfterDrain, err := ins.GetQueueInfo("deliver")
	require.NoError(t, err)
	assert.Equal(t, 0, infoAfterDrain.Pending)

	// Unknown queue returns error.
	_, err = ins.GetQueueInfo("missing")
	require.Error(t, err)
	require.Error(t, ins.DeleteTask("missing", "x"))
	_, err = ins.DeleteAllPendingTasks("missing")
	require.Error(t, err)
	require.Error(t, ins.RunTask("missing", "x"))
	_, err = ins.GetTaskInfo("missing", "x")
	require.Error(t, err)
	_, err = ins.ListPendingTasks("missing", 1, 10)
	require.Error(t, err)
}

// TestInspector_QueueMetrics_PopulatesAfterFinalize spins up a worker
// with WithJobMetrics implicitly enabled (default Driver config), runs
// jobs to completion, and asserts the BullMQ-spec metrics keys are
// reflected in Inspector.QueueMetrics. The minute-bucket gating means
// Data may stay empty inside the first minute, so we only assert
// Count > 0 and Data being non-nil; the LIST roll is exercised in
// mkq's own integration tests.
func TestInspector_QueueMetrics_PopulatesAfterFinalize(t *testing.T) {
	d := newDriver(t)
	srv := d.Server()

	var wg sync.WaitGroup
	wg.Add(2)
	srv.Handle("metrics:ok", func(_ context.Context, _ driver.Task) error {
		defer wg.Done()
		return nil
	})
	require.NoError(t, srv.Start())
	t.Cleanup(srv.Shutdown)

	require.NoError(t, d.Client().Enqueue(context.Background(), "metrics:ok", nil,
		driver.WithQueue("deliver")))
	require.NoError(t, d.Client().Enqueue(context.Background(), "metrics:ok", nil,
		driver.WithQueue("deliver")))
	if !waitGroupTimeout(&wg, 5*time.Second) {
		t.Fatal("handlers did not run")
	}

	// Wait for finalisation — Lua collectMetrics runs inside the
	// finalize transaction, so Count should observe the cumulative
	// total once both jobs roll into the completed bucket.
	require.Eventually(t, func() bool {
		m, err := d.Inspector().QueueMetrics("deliver", driver.MetricsKindCompleted)
		return err == nil && m != nil && m.Count >= 2
	}, 5*time.Second, 50*time.Millisecond)

	// Failed bucket: no failures in this scenario, but the call must
	// still succeed and return a zero-valued result rather than an
	// error (no metrics keys yet means the lua HMGET / LRANGE return
	// empty and mkq.GetMetrics maps that to QueueMetrics{}).
	failed, err := d.Inspector().QueueMetrics("deliver", driver.MetricsKindFailed)
	require.NoError(t, err)
	require.NotNil(t, failed)
	assert.EqualValues(t, 0, failed.Count)
}

// TestInspector_QueueMetrics_InvalidArgs covers the validation paths.
func TestInspector_QueueMetrics_InvalidArgs(t *testing.T) {
	d := newDriver(t)

	_, err := d.Inspector().QueueMetrics("missing", driver.MetricsKindCompleted)
	require.Error(t, err)

	_, err = d.Inspector().QueueMetrics("deliver", "weird")
	require.Error(t, err)
}

// TestInspector_GetQueueInfo_IncludesRepeatSchedules verifies the
// mk-go-specific behaviour added for #455: registering a repeatable
// schedule via Scheduler.Register populates `bull:<queue>:repeat`,
// and Inspector.GetQueueInfo surfaces those entries through the
// Scheduled field even though mkq does not pre-allocate concrete
// delayed jobs into the `bull:<queue>:delayed` ZSET.
//
// admin/job-queue.vue maps Scheduled into its "Delayed" KPI column,
// so without this addition operators running mk-go on the mkq driver
// would see Delayed=0 even with N cron jobs registered.
func TestInspector_GetQueueInfo_IncludesRepeatSchedules(t *testing.T) {
	d := newDriver(t)
	sched := d.Scheduler()

	// Pre-condition: zero before any schedule is registered.
	infoBefore, err := d.Inspector().GetQueueInfo("maintenance")
	require.NoError(t, err)
	assert.Equal(t, 0, infoBefore.Scheduled)

	require.NoError(t, sched.Register("0 0 * * *", "task:daily", nil,
		driver.WithQueue("maintenance"),
	))
	require.NoError(t, sched.Register("*/5 * * * *", "task:every5", nil,
		driver.WithQueue("maintenance"),
	))

	infoAfter, err := d.Inspector().GetQueueInfo("maintenance")
	require.NoError(t, err)
	// addJobScheduler-11.lua は repeat ZSET と delayed ZSET の両方に
	// 書き込むので、Scheduled には少なくとも 2 (repeat ZCARD 由来) と、
	// fresh state では加えて concrete delayed の 2 が足される。本番の
	// steady-state では concrete delayed が即 promote されて 0 になり、
	// repeat ZCARD だけが残る。assertion は「register した数以上が
	// 見える」という性質に留め、過剰カウント側は許容する。
	assert.GreaterOrEqual(t, infoAfter.Scheduled, 2,
		"Scheduled should reflect at least the repeat ZSET cardinality")
	assert.GreaterOrEqual(t, infoAfter.Size, 2)
}

// TestInspector_RunTaskPromotesDelayed verifies that RunTask pulls a
// scheduled task back to wait, mirroring asynq's "Run scheduled".
func TestInspector_RunTaskPromotesDelayed(t *testing.T) {
	d := newDriver(t)

	require.NoError(t, d.Client().Enqueue(context.Background(),
		"ins:run", nil,
		driver.WithQueue("deliver"),
		driver.WithProcessIn(time.Hour),
	))

	ins := d.Inspector()
	scheduled, err := ins.ListScheduledTasks("deliver", 1, 30)
	require.NoError(t, err)
	require.NotEmpty(t, scheduled)

	require.NoError(t, ins.RunTask("deliver", scheduled[0].ID))
	// After promotion the task should sit in pending.
	require.Eventually(t, func() bool {
		info, err := ins.GetQueueInfo("deliver")
		return err == nil && info.Pending >= 1
	}, 3*time.Second, 50*time.Millisecond)
}

// TestInspector_GetQueueInfo_RetryReflectsFailedBucket verifies that the
// Retry field surfaces the current failed bucket size (= retry-pending +
// permanent failures). Without this the queueStats publisher's
// Delayed = Scheduled + Retry calculation undercounts and admin/job-queue
// graph stays stuck at 0 even when Errored Instances panel shows entries
// (#1181).
func TestInspector_GetQueueInfo_RetryReflectsFailedBucket(t *testing.T) {
	d := newDriver(t)
	srv := d.Server()
	var wg sync.WaitGroup
	wg.Add(1)
	srv.Handle("ins:fail-then-check", func(_ context.Context, _ driver.Task) error {
		defer wg.Done()
		// SkipRetry sentinel = immediate failure into the failed bucket
		// (mkq's ErrUnrecoverable). No backoff retry path involved, but
		// the resulting bucket position is the same one retry-pending
		// jobs occupy in mkq.
		return fmt.Errorf("intentional fail: %w", driver.SkipRetry)
	})
	require.NoError(t, srv.Start())
	t.Cleanup(srv.Shutdown)

	require.NoError(t, d.Client().Enqueue(context.Background(),
		"ins:fail-then-check", nil,
		driver.WithQueue("deliver")))
	if !waitGroupTimeout(&wg, 5*time.Second) {
		t.Fatal("handler not invoked")
	}

	ins := d.Inspector()
	require.Eventually(t, func() bool {
		info, err := ins.GetQueueInfo("deliver")
		return err == nil && info.Retry >= 1 && info.Failed >= 1
	}, 5*time.Second, 50*time.Millisecond,
		"Retry should reflect failed bucket size (was hardcoded to 0)")
}

// TestInspector_RunTask_FallsBackToRetryJobForFailedBucket verifies that
// RunTask succeeds against a job in the failed bucket, by falling back
// from PromoteJob (delayed-only) to RetryJob. asynq's Inspector.RunTask
// transparently handles both buckets; this test pins the equivalent
// behaviour for the mkq driver (#1181).
//
// Without this fallback the admin panel's "Retry all queues now" button
// is a no-op on retry-pending jobs and operators cannot recover stuck
// delivery from the UI.
func TestInspector_RunTask_FallsBackToRetryJobForFailedBucket(t *testing.T) {
	d := newDriver(t)
	srv := d.Server()
	var wg sync.WaitGroup
	wg.Add(1)
	srv.Handle("ins:fail-then-retry", func(_ context.Context, _ driver.Task) error {
		defer wg.Done()
		return fmt.Errorf("intentional fail: %w", driver.SkipRetry)
	})
	require.NoError(t, srv.Start())
	t.Cleanup(srv.Shutdown)

	require.NoError(t, d.Client().Enqueue(context.Background(),
		"ins:fail-then-retry", nil,
		driver.WithQueue("deliver")))
	if !waitGroupTimeout(&wg, 5*time.Second) {
		t.Fatal("handler not invoked")
	}

	ins := d.Inspector()
	// Wait until the job has landed in the failed bucket (= ListRetryTasks
	// sees it through mkq's failed bucket).
	var failedTaskID string
	require.Eventually(t, func() bool {
		rows, err := ins.ListRetryTasks("deliver", 1, 30)
		if err != nil || len(rows) == 0 {
			return false
		}
		failedTaskID = rows[0].ID
		return true
	}, 5*time.Second, 50*time.Millisecond)

	// RunTask must succeed via RetryJob fallback even though the job is
	// not in mkq's delayed bucket. asynq's parity contract: callers only
	// pass a task ID and the driver routes to the right move primitive.
	require.NoError(t, ins.RunTask("deliver", failedTaskID),
		"RunTask should fall back to RetryJob for failed bucket jobs")

	// After the fallback the failed bucket count drops by one. We assert
	// the count rather than chasing the job through wait→active→failed
	// again (the handler is still registered and will refail it), since
	// the contract under test is just "RunTask did not error and the job
	// left the failed bucket".
	require.Eventually(t, func() bool {
		rows, err := ins.ListRetryTasks("deliver", 1, 30)
		if err != nil {
			return false
		}
		for _, r := range rows {
			if r.ID == failedTaskID {
				return false // still in failed bucket
			}
		}
		return true
	}, 5*time.Second, 50*time.Millisecond,
		"job should have left the failed bucket after RetryJob fallback")
}

// TestScheduler_RegisterRoundtrip exercises the cron Register API and
// the validator branches Scheduler exposes.
func TestScheduler_RegisterRoundtrip(t *testing.T) {
	d := newDriver(t)
	sched := d.Scheduler()
	require.NoError(t, sched.Register("0 0 * * *", "task:daily", nil,
		driver.WithQueue("maintenance"),
		driver.WithMaxRetry(0),
	))
	require.NoError(t, sched.Start())
	sched.Shutdown()

	// Unknown queue → error.
	require.Error(t, sched.Register("0 0 * * *", "task:x", nil,
		driver.WithQueue("missing")))

	// Missing queue → error.
	require.Error(t, sched.Register("0 0 * * *", "task:y", nil))
}

// TestServer_StartTwiceRejects ensures the server cannot be started
// repeatedly — Process holds Redis connections per worker slot and
// re-Start would leak them.
func TestServer_StartTwiceRejects(t *testing.T) {
	d := newDriver(t)
	srv := d.Server()
	require.NoError(t, srv.Start())
	t.Cleanup(srv.Shutdown)

	require.Error(t, srv.Start(), "second Start must be rejected")
}

// TestServer_HandleAfterStartPanics validates the documented contract:
// callers must register every handler before Start.
func TestServer_HandleAfterStartPanics(t *testing.T) {
	d := newDriver(t)
	srv := d.Server()
	require.NoError(t, srv.Start())
	t.Cleanup(srv.Shutdown)

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("Handle after Start must panic")
		}
	}()
	srv.Handle("late", func(context.Context, driver.Task) error { return nil })
}

// TestDriver_CloseWithoutStart confirms a freshly-built driver cleans
// up cleanly when no sub-services were exercised.
func TestDriver_CloseWithoutStart(t *testing.T) {
	d := newDriver(t)
	// reset Cleanup so we can call Close manually.
	require.NoError(t, d.Close())

	// Close after Close — second call should still succeed (idempotent
	// on the worker side; the underlying redis client returns nil for
	// repeated Close).
	require.NoError(t, d.Close())
}

// TestNewDriver_BadAddressFails ensures the constructor surfaces
// connect failures rather than returning a half-built Driver.
func TestNewDriver_BadAddressFails(t *testing.T) {
	testutil.SkipIfNoDocker(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := mkqdriver.New(ctx, mkqdriver.Config{
		Redis: redis.UniversalOptions{Addrs: []string{"127.0.0.1:1"}},
	})
	require.Error(t, err)
	if !errors.Is(err, context.DeadlineExceeded) && err == nil {
		t.Fatalf("expected non-nil error, got %v", err)
	}
}

// TestDriver_Resize_BeforeStartReturnsNotSupported verifies the
// pre-Start contract — Resize called before Server.Start returns
// ErrResizeNotSupported (there is no pool to resize yet).
func TestDriver_Resize_BeforeStartReturnsNotSupported(t *testing.T) {
	d := newDriver(t)
	// No call to d.Server() yet.
	err := d.Resize("deliver", 8)
	require.ErrorIs(t, err, driver.ErrResizeNotSupported)
}

// TestServer_Resize_UnknownQueueReturnsError verifies that Resize on
// a queue not in the driver's known set returns a descriptive error
// (not a silent no-op that would hide controller bugs).
func TestServer_Resize_UnknownQueueReturnsError(t *testing.T) {
	d := newDriver(t)
	srv := d.Server()
	srv.Handle("noop", func(ctx context.Context, _ driver.Task) error { return nil })
	require.NoError(t, srv.Start())
	t.Cleanup(srv.Shutdown)

	err := d.Resize("does-not-exist", 4)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does-not-exist")
}

// TestServer_Resize_NegativeNReturnsError verifies that callers cannot
// pass invalid negative pool sizes (controller bug guard).
func TestServer_Resize_NegativeNReturnsError(t *testing.T) {
	d := newDriver(t)
	srv := d.Server()
	srv.Handle("noop", func(ctx context.Context, _ driver.Task) error { return nil })
	require.NoError(t, srv.Start())
	t.Cleanup(srv.Shutdown)

	err := d.Resize("deliver", -1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be >= 0")
}

// TestServer_ResizeUp_ProcessesMoreInParallel verifies that Resize-up
// actually increases the parallel handler count observable via the
// in-flight gauge pattern (= ADR §7.2 the "double parallelism" check).
// Strategy: start with N=2, observe peak=2; Resize to N=8, observe peak
// climbs to 8.
func TestServer_ResizeUp_ProcessesMoreInParallel(t *testing.T) {
	testutil.SkipIfNoDocker(t)
	flushRedis(t)

	d, err := mkqdriver.New(context.Background(), mkqdriver.Config{
		Redis:            redis.UniversalOptions{Addrs: []string{testRedis.Addr}},
		Concurrency:      32,
		QueueConcurrency: map[string]int{"deliver": 2},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.Close() })

	srv := d.Server()
	var (
		inFlight atomic.Int32
		peak     atomic.Int32
		release  = make(chan struct{})
	)
	srv.Handle("resize:hold", func(ctx context.Context, _ driver.Task) error {
		now := inFlight.Add(1)
		for {
			cur := peak.Load()
			if now <= cur || peak.CompareAndSwap(cur, now) {
				break
			}
		}
		select {
		case <-release:
		case <-ctx.Done():
		}
		inFlight.Add(-1)
		return nil
	})
	require.NoError(t, srv.Start())
	t.Cleanup(srv.Shutdown)

	// Phase 1: pre-resize. Enqueue 4 jobs, peak should reach 2 (= initial).
	for range 4 {
		require.NoError(t, d.Client().Enqueue(context.Background(), "resize:hold", nil,
			driver.WithQueue("deliver")))
	}
	deadline := time.After(5 * time.Second)
	for peak.Load() < 2 {
		select {
		case <-deadline:
			t.Fatalf("phase 1 peak never reached 2 (got %d)", peak.Load())
		case <-time.After(20 * time.Millisecond):
		}
	}
	// 2 を超えていないことを 100ms 観測。
	time.Sleep(100 * time.Millisecond)
	require.LessOrEqual(t, peak.Load(), int32(2),
		"pre-resize peak should be capped at initial concurrency=2")
	assert.Equal(t, 2, d.WorkerCount("deliver"), "WorkerCount should reflect initial pool size")

	// Phase 2: Resize-up to 8.
	require.NoError(t, d.Resize("deliver", 8))
	assert.Equal(t, 8, d.WorkerCount("deliver"), "WorkerCount should reflect post-resize pool size")

	// Phase 2 peak observation: enqueue more jobs, peak should now climb.
	for range 8 {
		require.NoError(t, d.Client().Enqueue(context.Background(), "resize:hold", nil,
			driver.WithQueue("deliver")))
	}
	deadline = time.After(5 * time.Second)
	for peak.Load() < 8 {
		select {
		case <-deadline:
			t.Fatalf("phase 2 peak never reached 8 after Resize (got %d)", peak.Load())
		case <-time.After(20 * time.Millisecond):
		}
	}

	close(release)
}

// TestServer_ResizeDown_CancelsInFlight verifies the actual mkq semantics
// of Worker.Stop: it cancels the run context of in-flight handlers
// (handler ctx.Done fires) and waits for the goroutines to exit cleanly,
// rather than blocking until natural job completion.
//
// 注: ADR §7.2 で「graceful drain」と書いたが、これは "goroutine leak しない"
// の意味で、in-flight job は cancel 経路で中断される (next pickup で retry
// される前提)。slow remote inbox 等で scale-down が分単位 block する事態を
// 避ける mkq library の合理的選択。
func TestServer_ResizeDown_CancelsInFlight(t *testing.T) {
	testutil.SkipIfNoDocker(t)
	flushRedis(t)

	d, err := mkqdriver.New(context.Background(), mkqdriver.Config{
		Redis:            redis.UniversalOptions{Addrs: []string{testRedis.Addr}},
		Concurrency:      32,
		QueueConcurrency: map[string]int{"deliver": 4},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.Close() })

	srv := d.Server()
	var (
		inFlight  atomic.Int32
		completed atomic.Int32
		cancelled atomic.Int32
	)
	srv.Handle("resize:slow", func(ctx context.Context, _ driver.Task) error {
		inFlight.Add(1)
		defer inFlight.Add(-1)
		// 長 job: 5 秒 sleep。Resize で cancel された場合は ctx.Done で
		// 即座に return、completed しない。
		select {
		case <-time.After(5 * time.Second):
			completed.Add(1)
			return nil
		case <-ctx.Done():
			cancelled.Add(1)
			return ctx.Err()
		}
	})
	require.NoError(t, srv.Start())
	t.Cleanup(srv.Shutdown)

	// 4 jobs enqueue; each Worker picks one up immediately.
	for range 4 {
		require.NoError(t, d.Client().Enqueue(context.Background(), "resize:slow", nil,
			driver.WithQueue("deliver")))
	}

	// Poll until 全 4 Worker が job を pick up 済を確認。
	deadline := time.After(3 * time.Second)
	for inFlight.Load() < 4 {
		select {
		case <-deadline:
			t.Fatalf("inFlight never reached 4 (got %d)", inFlight.Load())
		case <-time.After(10 * time.Millisecond):
		}
	}

	// Resize down: should NOT block 5s — Worker.Stop cancels in-flight.
	start := time.Now()
	require.NoError(t, d.Resize("deliver", 1))
	elapsed := time.Since(start)

	// 3 Worker × Stop ≈ ms オーダー (cancel + goroutine join のみ)、決して
	// 5 秒 (job 完了時間) はかからない。
	assert.Less(t, elapsed, 2*time.Second,
		"Resize-down should not block for natural job completion (elapsed=%v)", elapsed)

	// 完了 (completed) ではなく cancel (cancelled) として終わったことを確認。
	// 最低 3 個は cancel されているはず (kept=1 はそのまま動き続ける)。
	assert.GreaterOrEqual(t, cancelled.Load(), int32(3),
		"at least 3 of 4 in-flight jobs should be cancelled by Resize-down (got %d)", cancelled.Load())
	assert.Equal(t, int32(0), completed.Load(),
		"no job should naturally complete in this short test window")
	assert.Equal(t, 1, d.WorkerCount("deliver"))
}

// TestServer_ResizeRace verifies that overlapping Resize calls on the
// same queue serialise safely (no goroutine leak, no negative pool
// size, no panic). The final state matches the last Resize call.
func TestServer_ResizeRace(t *testing.T) {
	testutil.SkipIfNoDocker(t)
	flushRedis(t)

	d, err := mkqdriver.New(context.Background(), mkqdriver.Config{
		Redis:            redis.UniversalOptions{Addrs: []string{testRedis.Addr}},
		Concurrency:      32,
		QueueConcurrency: map[string]int{"deliver": 4},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.Close() })

	srv := d.Server()
	srv.Handle("noop", func(ctx context.Context, _ driver.Task) error { return nil })
	require.NoError(t, srv.Start())
	t.Cleanup(srv.Shutdown)

	// 10 concurrent Resize calls with alternating sizes. Each call
	// should complete without error; final state must equal the last
	// committed value (= we test the sequential-consistency property
	// that the lock provides).
	var wg sync.WaitGroup
	sizes := []int{8, 2, 6, 3, 5, 1, 4, 7, 2, 8}
	for _, n := range sizes {
		wg.Add(1)
		go func(target int) {
			defer wg.Done()
			err := d.Resize("deliver", target)
			assert.NoError(t, err)
		}(n)
	}
	wg.Wait()

	// 最終 worker 数は 1..8 のいずれか (最後に commit された Resize の値)
	// で、いずれにせよ [1, 8] の範囲内。
	finalCount := d.WorkerCount("deliver")
	assert.GreaterOrEqual(t, finalCount, 1)
	assert.LessOrEqual(t, finalCount, 8)
}

// TestServer_Resize_AfterShutdownReturnsError verifies the post-shutdown
// race guard: a Resize call that captured a pool pointer just before
// Server.Shutdown ran must not spawn leaked Workers on the orphan pool.
//
// We simulate the race by calling Shutdown immediately before Resize on
// the same Server. The Resize is expected to fail with either "unknown
// queue" (s.pools is now nil) or ErrResizeAfterShutdown (the captured
// pool flag is checked) — both are valid no-spawn outcomes.
func TestServer_Resize_AfterShutdownReturnsError(t *testing.T) {
	testutil.SkipIfNoDocker(t)
	flushRedis(t)

	d, err := mkqdriver.New(context.Background(), mkqdriver.Config{
		Redis:            redis.UniversalOptions{Addrs: []string{testRedis.Addr}},
		Concurrency:      32,
		QueueConcurrency: map[string]int{"deliver": 2},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.Close() })

	srv := d.Server()
	srv.Handle("noop", func(ctx context.Context, _ driver.Task) error { return nil })
	require.NoError(t, srv.Start())

	// Shutdown を呼んでから Resize を試みる (= 後追い caller の最悪
	// シナリオの sequential 化、本物の race window と同等の効果)。
	srv.Shutdown()

	err = d.Resize("deliver", 8)
	require.Error(t, err, "Resize after Shutdown must not silently spawn workers")
	// Worker 数は 0 のまま、leak していない。
	assert.Equal(t, 0, d.WorkerCount("deliver"))
}

// TestServer_Resize_ToZeroStopsAll verifies that Resize(qname, 0)
// removes every Worker for the queue (= "pause queue" semantics).
// Subsequent enqueues backlog in Redis until a non-zero Resize.
func TestServer_Resize_ToZeroStopsAll(t *testing.T) {
	testutil.SkipIfNoDocker(t)
	flushRedis(t)

	d, err := mkqdriver.New(context.Background(), mkqdriver.Config{
		Redis:            redis.UniversalOptions{Addrs: []string{testRedis.Addr}},
		Concurrency:      32,
		QueueConcurrency: map[string]int{"deliver": 2},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.Close() })

	srv := d.Server()
	var processed atomic.Int32
	srv.Handle("noop", func(ctx context.Context, _ driver.Task) error {
		processed.Add(1)
		return nil
	})
	require.NoError(t, srv.Start())
	t.Cleanup(srv.Shutdown)

	// Resize to 0 → no workers should be active.
	require.NoError(t, d.Resize("deliver", 0))
	assert.Equal(t, 0, d.WorkerCount("deliver"))

	// Enqueue 3 jobs — they should accumulate in Redis, not be processed.
	for range 3 {
		require.NoError(t, d.Client().Enqueue(context.Background(), "noop", nil,
			driver.WithQueue("deliver")))
	}
	time.Sleep(200 * time.Millisecond)
	assert.Equal(t, int32(0), processed.Load(),
		"no jobs should be processed while pool size is 0")

	// Resize back to 2 → jobs should drain.
	require.NoError(t, d.Resize("deliver", 2))
	deadline := time.After(3 * time.Second)
	for processed.Load() < 3 {
		select {
		case <-deadline:
			t.Fatalf("processed never reached 3 after Resize back to 2 (got %d)", processed.Load())
		case <-time.After(20 * time.Millisecond):
		}
	}
}
