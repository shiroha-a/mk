package queue

import (
	"testing"

	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shiroha-a/mk/internal/queue/driver/asynqdriver"
)

// newSchedulerForTest constructs a Scheduler against a driver that
// does not require a live Redis (asynq.Scheduler is constructed lazily
// in NewScheduler — Register validates cron syntax client-side).
func newSchedulerForTest(t *testing.T) *Scheduler {
	t.Helper()
	d := asynqdriver.New(asynq.RedisClientOpt{Addr: "127.0.0.1:6379"}, asynqdriver.ServerConfig{})
	s := NewScheduler(d)
	t.Cleanup(s.Shutdown)
	return s
}

// TestNewScheduler_NotNil is a simple smoke test that the Scheduler
// wrapper builds without touching Redis.
func TestNewScheduler_NotNil(t *testing.T) {
	s := newSchedulerForTest(t)
	require.NotNil(t, s)
	require.NotNil(t, s.inner)
}

// TestScheduler_RegisterChartJobs_NoErr verifies the cron syntax + queue
// options are accepted. Register does not enqueue — it simply records
// the schedule — so no Redis is required.
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

func TestMaintenanceQueueName_Const(t *testing.T) {
	// 既存定数と被らないこと (ベタ書きの同期ミスを防ぐ smoke)
	assert.Equal(t, "maintenance", MaintenanceQueueName)
}
