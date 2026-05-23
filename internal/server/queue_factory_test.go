package server

import (
	"context"
	"testing"
	"time"

	"github.com/shiroha-a/mk/internal/config"
	"github.com/shiroha-a/mk/internal/queue"
	"github.com/shiroha-a/mk/internal/queue/driver"
	"github.com/shiroha-a/mk/internal/queue/driver/asynqdriver"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func intp(v int) *int { return &v }

// #495: cfg.DeliverJobConcurrency がそのまま deliver queue の concurrency
// として map に積まれる。inbox/relationship は mk-go に該当 queue が無い
// ため出力されない (config だけ受けて driver は no-op で扱う)。
func TestPerQueueConcurrencyFromConfig(t *testing.T) {
	cases := []struct {
		name string
		cfg  *config.Config
		want map[string]int
	}{
		{"empty", &config.Config{}, nil},
		{"deliverOnly", &config.Config{DeliverJobConcurrency: intp(8)}, map[string]int{"deliver": 8}},
		{"zeroIgnored", &config.Config{DeliverJobConcurrency: intp(0)}, nil},
		{"negativeIgnored", &config.Config{DeliverJobConcurrency: intp(-1)}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, perQueueConcurrencyFromConfig(tc.cfg))
		})
	}
}

func TestPerQueueRatesFromConfig(t *testing.T) {
	cases := []struct {
		name string
		cfg  *config.Config
		want map[string]int
	}{
		{"empty", &config.Config{}, nil},
		{"deliverOnly", &config.Config{DeliverJobPerSec: intp(50)}, map[string]int{"deliver": 50}},
		{"zeroIgnored", &config.Config{DeliverJobPerSec: intp(0)}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, perQueueRatesFromConfig(tc.cfg))
		})
	}
}

// applyClientPolicies はその後の EnqueueDeliver で WithMaxRetry default が
// 効くよう Client に Policy を登録する。Policy が無効 (0) ならば SetPolicy
// は呼ばれない (PolicyMap が nil のままで PolicyFor がゼロ Policy を返す
// fast-path が維持される)。
func TestApplyClientPolicies_DeliverMaxAttempts(t *testing.T) {
	c := queue.NewClient(asynqdriver.New(asynqdriver.BuildRedisOpt(config.RedisOptions{Host: "localhost", Port: 6379}), asynqdriver.ServerConfig{}))
	defer func() { _ = c.Close() }()

	cfg := &config.Config{DeliverJobMaxAttempts: intp(5)}
	applyClientPolicies(c, cfg)

	// reflectionで取れないので indirect に: SetPolicy がもう 1 度
	// 呼ばれて上書きできることだけ確認する。
	c.SetPolicy(queue.QueueName, queue.Policy{MaxAttempts: 99})
}

// 0 や nil なら applyClientPolicies は SetPolicy を呼ばないが、
// Client は引き続き正常に動く (panic しない / EnqueueDeliver する経路は
// 別 testで verify 済み)。
func TestApplyClientPolicies_NoOpForZero(t *testing.T) {
	c := queue.NewClient(asynqdriver.New(asynqdriver.BuildRedisOpt(config.RedisOptions{Host: "localhost", Port: 6379}), asynqdriver.ServerConfig{}))
	defer func() { _ = c.Close() }()

	require.NotPanics(t, func() {
		applyClientPolicies(c, &config.Config{})
		applyClientPolicies(c, &config.Config{DeliverJobMaxAttempts: intp(0)})
	})
}

// recordingDriverClient is a minimal driver.Client implementation that
// captures the last Enqueue call's options. server _test 内に持つ用途は
// applyClientPolicies が 5 queue 全てに Policy を伝搬するのを enqueue
// 経由で観測すること (queue.Client.policyFor が unexported のため)。
type recordingDriverClient struct {
	lastTaskType string
	lastOpts     driver.EnqueueOptions
}

func (r *recordingDriverClient) Enqueue(_ context.Context, taskType string, _ []byte, opts ...driver.EnqueueOption) error {
	r.lastTaskType = taskType
	r.lastOpts = driver.ApplyEnqueueOptions(opts)
	return nil
}

func (r *recordingDriverClient) Close() error { return nil }

// stubDriver wraps just enough of driver.Driver to satisfy queue.NewClient.
type stubDriver struct {
	client driver.Client
}

func (s *stubDriver) Client() driver.Client        { return s.client }
func (s *stubDriver) Inspector() driver.Inspector  { return nil }
func (s *stubDriver) Server() driver.Server        { return nil }
func (s *stubDriver) Scheduler() driver.Scheduler  { return nil }
func (s *stubDriver) Close() error                 { return nil }
func (s *stubDriver) WorkerCount(_ string) int     { return 0 }
func (s *stubDriver) Resize(_ string, _ int) error { return driver.ErrResizeNotSupported }

// TestApplyClientPolicies_AllFiveQueues verifies that applyClientPolicies
// propagates retention defaults onto all five application queues (deliver /
// inbox / export / push / webhook) — not just deliver / inbox like before
// #1193。各 queue で enqueue 後に recordingDriverClient が見る driver
// options が default の `{age: 7d, count: 30}` / `{age: 7d, count: 1000}`
// を含むことを assert する。
func TestApplyClientPolicies_AllFiveQueues(t *testing.T) {
	rec := &recordingDriverClient{}
	c := queue.NewClient(&stubDriver{client: rec})
	defer func() { _ = c.Close() }()

	applyClientPolicies(c, &config.Config{})

	cases := []struct {
		name string
		enq  func() error
	}{
		{
			name: "deliver",
			enq: func() error {
				return c.EnqueueDeliver(queue.DeliverPayload{Inbox: "x", Body: []byte(`{}`)})
			},
		},
		{
			name: "inbox",
			enq: func() error {
				return c.EnqueueInbox(context.Background(), queue.InboxPayload{Body: []byte(`{}`)})
			},
		},
		{
			name: "export",
			enq: func() error {
				return c.EnqueueExport(queue.ExportPayload{UserID: "u", Type: "notes"})
			},
		},
		{
			name: "push",
			enq: func() error {
				return c.EnqueueWebPush(context.Background(), queue.WebPushPayload{})
			},
		},
		{
			name: "webhook",
			enq: func() error {
				return c.EnqueueUserWebhook(context.Background(), queue.WebhookPayload{})
			},
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			require.NoError(t, tt.enq())
			assert.Equal(t, defaultKeepCompleted, rec.lastOpts.KeepCompleted, "KeepCompleted default propagated")
			assert.True(t, rec.lastOpts.KeepCompletedSet, "KeepCompletedSet must be propagated")
			assert.Equal(t, defaultKeepFailed, rec.lastOpts.KeepFailed)
			assert.True(t, rec.lastOpts.KeepFailedSet)
			assert.Equal(t, defaultKeepCompletedAge, rec.lastOpts.KeepCompletedAge)
			assert.Equal(t, defaultKeepFailedAge, rec.lastOpts.KeepFailedAge)
		})
	}
}

// TestApplyClientPolicies_OperatorOptOut: deliverJobKeepCompleted=0 を
// operator が明示すると completed retention が 0 (= unlimited 蓄積に
// opt-out) として伝わり、driver opt 側で WithKeepCompleted が emit
// されなくなることを pin (#1193 review feedback)。
func TestApplyClientPolicies_OperatorOptOut(t *testing.T) {
	rec := &recordingDriverClient{}
	c := queue.NewClient(&stubDriver{client: rec})
	defer func() { _ = c.Close() }()

	applyClientPolicies(c, &config.Config{DeliverJobKeepCompleted: intp(0)})

	require.NoError(t, c.EnqueueDeliver(queue.DeliverPayload{Inbox: "x", Body: []byte(`{}`)}))

	assert.False(t, rec.lastOpts.KeepCompletedSet, "operator opt-out must NOT set KeepCompletedSet")
	assert.Equal(t, 0, rec.lastOpts.KeepCompleted)
	// failed 側は default が残るはず。
	assert.Equal(t, defaultKeepFailed, rec.lastOpts.KeepFailed)
}

// TestDefaultPolicy: defaultPolicy() helper が buildPolicy(nil, nil, nil)
// と同値であることを assert (refactoring 後の equivalence pin)。
func TestDefaultPolicy(t *testing.T) {
	assert.Equal(t, buildPolicy(nil, nil, nil), defaultPolicy())
}

// TestBuildPolicy validates the (maxAttempts, keepFailed, keepCompleted)
// → Policy transform end-to-end: unset → defaults (= 1000 / 30 / 7d / 7d)、
// 明示 0 → 0 (= operator opt-out, unlimited 蓄積)、明示 N → N。Age 値は
// YAML key を持たないので常に固定 7d (#1184 / #1193)。
func TestBuildPolicy(t *testing.T) {
	defaultAges := struct {
		completed time.Duration
		failed    time.Duration
	}{
		completed: defaultKeepCompletedAge,
		failed:    defaultKeepFailedAge,
	}
	tests := []struct {
		name          string
		maxAttempts   *int
		keepFailed    *int
		keepCompleted *int
		want          queue.Policy
	}{
		{
			"all unset",
			nil, nil, nil,
			queue.Policy{
				KeepFailed:       defaultKeepFailed,
				KeepCompleted:    defaultKeepCompleted,
				KeepCompletedAge: defaultAges.completed,
				KeepFailedAge:    defaultAges.failed,
			},
		},
		{
			"only maxAttempts",
			intp(8), nil, nil,
			queue.Policy{
				MaxAttempts:      8,
				KeepFailed:       defaultKeepFailed,
				KeepCompleted:    defaultKeepCompleted,
				KeepCompletedAge: defaultAges.completed,
				KeepFailedAge:    defaultAges.failed,
			},
		},
		{
			"only keepFailed explicit 500",
			nil, intp(500), nil,
			queue.Policy{
				KeepFailed:       500,
				KeepCompleted:    defaultKeepCompleted,
				KeepCompletedAge: defaultAges.completed,
				KeepFailedAge:    defaultAges.failed,
			},
		},
		{
			"only keepFailed explicit 0 (operator opt-out)",
			nil, intp(0), nil,
			queue.Policy{
				KeepFailed:       0,
				KeepCompleted:    defaultKeepCompleted,
				KeepCompletedAge: defaultAges.completed,
				KeepFailedAge:    defaultAges.failed,
			},
		},
		{
			"only keepCompleted explicit 100",
			nil, nil, intp(100),
			queue.Policy{
				KeepFailed:       defaultKeepFailed,
				KeepCompleted:    100,
				KeepCompletedAge: defaultAges.completed,
				KeepFailedAge:    defaultAges.failed,
			},
		},
		{
			"only keepCompleted explicit 0 (operator opt-out)",
			nil, nil, intp(0),
			queue.Policy{
				KeepFailed:       defaultKeepFailed,
				KeepCompleted:    0,
				KeepCompletedAge: defaultAges.completed,
				KeepFailedAge:    defaultAges.failed,
			},
		},
		{
			"all explicit",
			intp(4), intp(2000), intp(50),
			queue.Policy{
				MaxAttempts:      4,
				KeepFailed:       2000,
				KeepCompleted:    50,
				KeepCompletedAge: defaultAges.completed,
				KeepFailedAge:    defaultAges.failed,
			},
		},
		// maxAttempts<=0 は driver default に倒す既存挙動を維持。
		// `MaxAttempts: 0` は「Policy で上書きしない (= driver の default
		// retry を使う)」を意味する zero-value。明示 0 を受け取っても
		// Policy には流さないので 0 のまま残る。
		{
			"maxAttempts zero leaves driver default",
			intp(0), intp(500), intp(15),
			queue.Policy{
				MaxAttempts:      0,
				KeepFailed:       500,
				KeepCompleted:    15,
				KeepCompletedAge: defaultAges.completed,
				KeepFailedAge:    defaultAges.failed,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildPolicy(tt.maxAttempts, tt.keepFailed, tt.keepCompleted)
			assert.Equal(t, tt.want, got)
		})
	}
}
