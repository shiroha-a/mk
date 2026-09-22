package queue_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shiroha-a/mk/internal/queue"
	"github.com/shiroha-a/mk/internal/testutil"
)

// newSchedulerForTest constructs a Scheduler against the mkq driver bound
// to the test Redis.
//
// **実 Redis が要る。** mkq の Register は cron 式を Go 側で解析したあと
// BullMQ の repeat ZSET へ upsert するので、driver の構築にも登録にも接続が
// いる (旧 asynq driver は Scheduler を遅延構築していたので接続不要だった)。
func newSchedulerForTest(t *testing.T) *queue.Scheduler {
	t.Helper()
	testutil.SkipIfNoDocker(t)
	flushTestRedis(t)
	s := queue.NewScheduler(newDriver(t))
	t.Cleanup(s.Shutdown)
	return s
}

// TestNewScheduler_RegistersWithoutError is a smoke test that the
// Scheduler wrapper builds and accepts a registration.
func TestNewScheduler_RegistersWithoutError(t *testing.T) {
	s := newSchedulerForTest(t)
	require.NotNil(t, s)
	require.NoError(t, s.RegisterChartJobs())
}

// TestScheduler_RegisterChartJobs_NoErr verifies the cron syntax + queue
// options are accepted.
func TestScheduler_RegisterChartJobs_NoErr(t *testing.T) {
	s := newSchedulerForTest(t)
	require.NoError(t, s.RegisterChartJobs())
}

// TestScheduler_RegisterMaintenanceJobs_NoErr verifies the #1563 cron jobs
// register without error (cron syntax + queue options accepted).
func TestScheduler_RegisterMaintenanceJobs_NoErr(t *testing.T) {
	s := newSchedulerForTest(t)
	require.NoError(t, s.RegisterCheckExpiredMutingsJob())
	require.NoError(t, s.RegisterCleanJob())
	require.NoError(t, s.RegisterCleanRemoteNotesJob())
	require.NoError(t, s.RegisterCheckModeratorsActivityJob())
}

// TestScheduler_RegisterOrphanCleanupJobs_NoErr は孤児掃除 2 本の cron 式と
// queue オプションが driver に受理されることを見る (#2340 / #2722)。
//
// **登録失敗は起動時に warn ログを出すだけ** (`server.go` の jobs ループ) なので、
// cron 式のタイポは静かに「そのジョブだけ走らない」状態になる。ここで弾く。
func TestScheduler_RegisterOrphanCleanupJobs_NoErr(t *testing.T) {
	s := newSchedulerForTest(t)
	require.NoError(t, s.RegisterOrphanUserCleanupJob())
	require.NoError(t, s.RegisterOrphanAttachmentCleanupJob())
}

func TestMaintenanceQueueName_Const(t *testing.T) {
	// 既存定数と被らないこと (ベタ書きの同期ミスを防ぐ smoke)
	assert.Equal(t, "maintenance", queue.MaintenanceQueueName)
}
