package asynqdriver_test

import (
	"context"
	"log"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shiroha-a/mk/internal/queue/driver"
	"github.com/shiroha-a/mk/internal/queue/driver/asynqdriver"
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

func redisOpt() asynq.RedisClientOpt {
	return asynq.RedisClientOpt{Addr: testRedis.Addr}
}

func newDriver() *asynqdriver.Driver {
	return asynqdriver.New(redisOpt(), asynqdriver.ServerConfig{Concurrency: 2})
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

// #495: ServerConfig.RateLimits = {"deliver": N} で middleware が dispatch
// を tasks/sec に back-pressure することを wall-clock で検証する。
// burst=rate なので最初の N 個は即座に通り、それ以降は token 補充待ちで
// 1 秒あたり N 個ずつ進む。flaky 回避のため緩めの閾値で assert する。
func TestServer_RateLimit_BackPressuresHandlerDispatch(t *testing.T) {
	testutil.SkipIfNoDocker(t)
	flushRedis(t)

	// rate=2/sec, 5 task → 期待最低 elapsed: (5-2)/2 = 1.5s 程度。
	// flaky ガードに 1.0s 以上で OK にする (CI スケジューラ揺らぎ吸収)。
	d := asynqdriver.New(redisOpt(), asynqdriver.ServerConfig{
		Concurrency: 8,
		RateLimits:  map[string]int{"deliver": 2},
	})
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
	// 5 task @ 2/sec (burst 2) = 最低 ~1.5s。 1.0s で flaky 防止に余裕。
	// 上限なしの場合は数十 ms で完了するので 1s 超えれば limit が効いている。
	assert.GreaterOrEqual(t, elapsed, 1*time.Second,
		"rate limiter should pace dispatch (got elapsed=%s)", elapsed)
}

// TestEndToEnd_EnqueueProcess confirms the asynq driver wires
// Client.Enqueue, Server.Handle and the SkipRetry conversion through
// to the real asynq runtime against a live Redis.
func TestEndToEnd_EnqueueProcess(t *testing.T) {
	testutil.SkipIfNoDocker(t)
	flushRedis(t)

	d := newDriver()
	t.Cleanup(func() { _ = d.Close() })

	srv := d.Server()
	var (
		wg       sync.WaitGroup
		received int32
	)
	wg.Add(1)
	srv.Handle("e2e:ok", func(_ context.Context, task driver.Task) error {
		defer wg.Done()
		atomic.AddInt32(&received, 1)
		assert.Equal(t, "e2e:ok", task.Type())
		assert.Equal(t, []byte("body"), task.Payload())
		return nil
	})
	require.NoError(t, srv.Start())
	t.Cleanup(srv.Shutdown)

	require.NoError(t, d.Client().Enqueue(context.Background(), "e2e:ok", []byte("body"),
		driver.WithQueue("deliver")))

	if !waitGroupTimeout(&wg, 5*time.Second) {
		t.Fatal("handler not invoked")
	}
	assert.Equal(t, int32(1), atomic.LoadInt32(&received))
}

// TestInspector_LiveQueue exercises every Inspector method that the
// driver delegates to asynq. We enqueue a single task, then ensure the
// listing/inspection methods all return non-error results.
func TestInspector_LiveQueue(t *testing.T) {
	testutil.SkipIfNoDocker(t)
	flushRedis(t)

	d := newDriver()
	t.Cleanup(func() { _ = d.Close() })

	require.NoError(t, d.Client().Enqueue(context.Background(),
		"inspector:list", []byte("a"),
		driver.WithQueue("deliver"),
	))

	ins := d.Inspector()
	queues, err := ins.Queues()
	require.NoError(t, err)
	assert.Contains(t, queues, "deliver")

	info, err := ins.GetQueueInfo("deliver")
	require.NoError(t, err)
	assert.Equal(t, "deliver", info.Queue)

	pending, err := ins.ListPendingTasks("deliver", 1, 30)
	require.NoError(t, err)
	require.NotEmpty(t, pending)
	taskID := pending[0].ID

	got, err := ins.GetTaskInfo("deliver", taskID)
	require.NoError(t, err)
	assert.Equal(t, taskID, got.ID)

	// Ranges that must succeed even when empty.
	_, err = ins.ListActiveTasks("deliver", 1, 30)
	require.NoError(t, err)
	_, err = ins.ListScheduledTasks("deliver", 1, 30)
	require.NoError(t, err)
	_, err = ins.ListRetryTasks("deliver", 1, 30)
	require.NoError(t, err)

	// page/pageSize の clamp 経路: 0 と 過大値の双方を default に
	// 戻すロジックを一度通す。
	_, err = ins.ListPendingTasks("deliver", 0, 0)
	require.NoError(t, err)
	_, err = ins.ListPendingTasks("deliver", -3, 999)
	require.NoError(t, err)

	// DeleteTask: 1 件削除 → 残り 0
	require.NoError(t, ins.DeleteTask("deliver", taskID))

	// 再 enqueue して DeleteAllPendingTasks をテスト
	require.NoError(t, d.Client().Enqueue(context.Background(),
		"inspector:list", []byte("b"),
		driver.WithQueue("deliver"),
	))
	count, err := ins.DeleteAllPendingTasks("deliver")
	require.NoError(t, err)
	assert.Equal(t, 1, count)
}

// TestInspector_QueueMetrics_AsynqStub verifies the driver-neutral
// QueueMetrics surface for the asynq driver: no native time-series,
// so Data is always nil and Count is filled from the cumulative
// Completed / Failed bucket size. Invalid kinds and unknown queues
// surface as errors so the admin handler's defensive swallowing path
// (fetchQueueMetrics) gets exercised.
func TestInspector_QueueMetrics_AsynqStub(t *testing.T) {
	testutil.SkipIfNoDocker(t)
	flushRedis(t)

	d := newDriver()
	t.Cleanup(func() { _ = d.Close() })

	// asynq は最初の Enqueue まで queue を作らないので、まず 1 件
	// 投入して queue を materialize させる。
	require.NoError(t, d.Client().Enqueue(context.Background(),
		"metrics:probe", nil,
		driver.WithQueue("deliver"),
	))

	completed, err := d.Inspector().QueueMetrics("deliver", driver.MetricsKindCompleted)
	require.NoError(t, err)
	require.NotNil(t, completed)
	// asynq には time-series が無いので Data は常に nil。
	assert.Nil(t, completed.Data)
	// Newly-flushed Redis なので累積 Completed = 0 が返る。
	assert.EqualValues(t, 0, completed.Count)

	failed, err := d.Inspector().QueueMetrics("deliver", driver.MetricsKindFailed)
	require.NoError(t, err)
	require.NotNil(t, failed)
	assert.EqualValues(t, 0, failed.Count)

	_, err = d.Inspector().QueueMetrics("deliver", "unknown")
	require.Error(t, err)

	// 存在しない queue は asynq が NOT_FOUND を返すので error 経路が
	// 通る。fetchQueueMetrics 側で握り潰されるので handler 動作は問題
	// ないが、ここでは driver レイヤの契約として error を保証。
	_, err = d.Inspector().QueueMetrics("nonexistent", driver.MetricsKindCompleted)
	require.Error(t, err)
}

// TestInspector_RunTask promotes a scheduled task using the live
// Redis. Scheduled enqueueing uses driver.WithProcessIn so the task
// lands in the scheduled bucket; RunTask then pulls it back to
// pending.
func TestInspector_RunTask(t *testing.T) {
	testutil.SkipIfNoDocker(t)
	flushRedis(t)

	d := newDriver()
	t.Cleanup(func() { _ = d.Close() })

	require.NoError(t, d.Client().Enqueue(context.Background(),
		"inspector:run", []byte{},
		driver.WithQueue("deliver"),
		driver.WithProcessIn(time.Hour),
	))

	ins := d.Inspector()
	scheduled, err := ins.ListScheduledTasks("deliver", 1, 30)
	require.NoError(t, err)
	require.NotEmpty(t, scheduled)
	taskID := scheduled[0].ID

	require.NoError(t, ins.RunTask("deliver", taskID))
}

// TestScheduler_RegisterAndStart exercises the cron scheduler. The
// schedule pattern fires every minute, but we only verify Start /
// Shutdown succeed against a live Redis (Register validates cron
// syntax client-side).
func TestScheduler_RegisterAndStart(t *testing.T) {
	testutil.SkipIfNoDocker(t)
	flushRedis(t)

	d := newDriver()
	t.Cleanup(func() { _ = d.Close() })

	sched := d.Scheduler()
	require.NoError(t, sched.Register("* * * * *", "scheduled:every-min", []byte{},
		driver.WithQueue("maintenance"),
		driver.WithMaxRetry(0),
		driver.WithUnique(time.Minute),
	))
	require.NoError(t, sched.Start())
	sched.Shutdown()
}

// #2069 (upstream #17436): Inspector.PauseQueue/UnpauseQueue + GetQueueInfo.IsPaused が
// asynq native pause に wire されていることを検証。
func TestInspector_PauseResume(t *testing.T) {
	testutil.SkipIfNoDocker(t)
	flushRedis(t)

	d := newDriver()
	t.Cleanup(func() { _ = d.Close() })

	// queue を実体化するため 1 件 enqueue。
	require.NoError(t, d.Client().Enqueue(context.Background(),
		"pause:probe", []byte("x"), driver.WithQueue("deliver")))

	ins := d.Inspector()
	require.NoError(t, ins.PauseQueue("deliver"))
	info, err := ins.GetQueueInfo("deliver")
	require.NoError(t, err)
	assert.True(t, info.IsPaused, "pause 後は IsPaused=true")

	require.NoError(t, ins.UnpauseQueue("deliver"))
	info, err = ins.GetQueueInfo("deliver")
	require.NoError(t, err)
	assert.False(t, info.IsPaused, "resume 後は IsPaused=false")
}
