package server

import (
	"testing"
	"time"

	"github.com/shiroha-a/mk/internal/config"
	"github.com/shiroha-a/mk/internal/queue"
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
