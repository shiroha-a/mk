package driver

import "time"

// EnqueueOptions captures driver-neutral parameters for enqueueing a
// single task. Concrete drivers translate these into native options.
//
// Zero-valued fields mean "use the driver default":
//   - Queue ""           : driver decides (often the default queue name)
//   - MaxRetry 0         : driver default retry count
//   - UniqueTTL 0        : no unique-key dedup
//   - ProcessIn 0        : process immediately
//   - KeepFailed 0       : driver default (= no automatic retention)
//   - KeepCompleted 0    : driver default (= no automatic retention)
//   - KeepCompletedAge 0 : driver default (= no age-based prune)
//   - KeepFailedAge 0    : driver default (= no age-based prune)
type EnqueueOptions struct {
	Queue     string
	MaxRetry  int
	UniqueTTL time.Duration
	ProcessIn time.Duration

	// KeepFailed bounds the size of the failed ZSET for this task's
	// queue. mkq translates this to `mkq.WithKeepFailed(n)`. asynq has
	// no per-job equivalent (= silent no-op).
	//
	// KeepFailedSet は MaxRetry / MaxRetrySet と同じ「明示 0 vs default」
	// 区別 pattern を踏襲して提供するが、driver translation 上は両者とも
	// 「unlimited 蓄積 (= driver default)」になり挙動は同じ
	// (mkqdriver/option.go の `> 0` guard 参照)。operator が `<queue>JobKeepFailed:
	// 0` と明示的に opt-out したこと自体は Set=true として記録される
	// (= documented intent としての semantic を保つ)。
	KeepFailed    int
	KeepFailedSet bool

	// KeepCompleted bounds the size of the completed ZSET, mirroring
	// KeepFailed's semantics for the completed bucket. mkq translates
	// this to `mkq.WithKeepCompleted(n)`. asynq has no per-job
	// equivalent (= silent no-op). BullMQ の `removeOnComplete: N`
	// 互換 (#1193)。
	//
	// KeepCompletedSet も KeepFailedSet と同じ「明示 0 vs default」
	// 区別 pattern。
	KeepCompleted    int
	KeepCompletedSet bool

	// KeepCompletedAge drops completed jobs older than this duration
	// (BullMQ `removeOnComplete: {age: <seconds>}` 互換)。mkq
	// translates to `mkq.WithKeepCompletedAge(age)`。0 = age-based
	// prune 無し (driver default を踏ませる)。asynq では silent no-op。
	//
	// Combine with KeepCompleted to cap by both count and age in
	// the same way BullMQ TS's `{age, count}` object form works
	// (BullMQ TS の `removeOnComplete: {age: 7d, count: 30}` 互換)。
	KeepCompletedAge time.Duration

	// KeepFailedAge is the failed-side analogue of KeepCompletedAge
	// (BullMQ `removeOnFail: {age: <seconds>}` 互換)。0 = age-based
	// prune 無し。
	KeepFailedAge time.Duration

	// MaxRetrySet distinguishes "MaxRetry left at default" from
	// "MaxRetry explicitly set to 0". asynq treats MaxRetry=0 as
	// no-retries which differs from "use default", so callers like
	// the cleanRemoteNotes job that want zero retries must opt in
	// explicitly.
	MaxRetrySet bool
}

// EnqueueOption mutates EnqueueOptions; pass via Client.Enqueue.
type EnqueueOption func(*EnqueueOptions)

// WithQueue routes the task to the named queue.
func WithQueue(name string) EnqueueOption {
	return func(o *EnqueueOptions) { o.Queue = name }
}

// WithMaxRetry sets the maximum number of retries for the task. Use
// zero to disable retries entirely (callers should be aware that
// driver defaults differ — asynq defaults to 25).
func WithMaxRetry(n int) EnqueueOption {
	return func(o *EnqueueOptions) {
		o.MaxRetry = n
		o.MaxRetrySet = true
	}
}

// WithUnique sets a uniqueness TTL: the driver suppresses duplicate
// enqueues with the same task type + payload within the window.
func WithUnique(ttl time.Duration) EnqueueOption {
	return func(o *EnqueueOptions) { o.UniqueTTL = ttl }
}

// WithProcessIn delays processing of the task by the given duration.
func WithProcessIn(d time.Duration) EnqueueOption {
	return func(o *EnqueueOptions) { o.ProcessIn = d }
}

// WithKeepFailed bounds the size of the failed bucket / ZSET for this
// task's queue: when the failed bucket exceeds n entries, the oldest
// ones are pruned automatically. n==0 explicitly disables retention
// (= unlimited accumulation, matches the historical behaviour).
//
// 主な使い道: inbox / deliver 等の連合 queue で transient failure が
// 永続蓄積するのを防ぐ。BullMQ の `removeOnFail: N` と意味同等。
//
// driver 別:
//   - mkqdriver: `n > 0` のときだけ `mkq.WithKeepFailed(n)` を AddJob に
//     渡す。注意: mkq native の `WithKeepFailed(0)` は **「即時削除」**
//     (BullMQ `removeOnFail: true` 相当) で driver-neutral semantic と
//     真逆なので、driver translation 側で 0 は skip する。
//   - asynqdriver: silent no-op (asynq は per-job 相当 API を持たない、
//     archived bucket の age-based prune に依拠)
func WithKeepFailed(n int) EnqueueOption {
	return func(o *EnqueueOptions) {
		o.KeepFailed = n
		o.KeepFailedSet = true
	}
}

// WithKeepCompleted bounds the size of the completed bucket / ZSET for
// this task's queue, mirroring WithKeepFailed's semantics for completed
// jobs. n==0 explicitly disables retention (= unlimited accumulation).
//
// 主な使い道: BullMQ default の「completed job 無制限保持」を防ぐ。
// inbox / deliver / db / push / webhook queue の completed bookkeeping
// が時間と共に redis memory を蝕む実害があるため (#1193 で UDS 観測)。
// BullMQ の `removeOnComplete: N` と意味同等。
//
// driver 別:
//   - mkqdriver: `n > 0` のときだけ `mkq.WithKeepCompleted(n)` を AddJob
//     に渡す。注意: mkq native の `WithKeepCompleted(0)` は **「即時削除」**
//     (BullMQ `removeOnComplete: true` 相当) で driver-neutral semantic と
//     真逆なので、driver translation 側で 0 は skip する。
//   - asynqdriver: silent no-op (asynq は完了履歴の per-job retention 制御
//     を持たず、archived bucket とは別系統で管理される)
func WithKeepCompleted(n int) EnqueueOption {
	return func(o *EnqueueOptions) {
		o.KeepCompleted = n
		o.KeepCompletedSet = true
	}
}

// WithKeepCompletedAge drops completed jobs older than age, mirroring
// mkq's `WithKeepCompletedAge` (BullMQ `removeOnComplete: {age: <sec>}`
// 互換)。WithKeepCompleted と併用すると count / age の両条件で prune
// される (BullMQ TS の `removeOnComplete: {age: 7d, count: 30}` 互換)。
//
// driver 別:
//   - mkqdriver: `age > 0` のときだけ `mkq.WithKeepCompletedAge(age)` を
//     AddJob に渡す。
//   - asynqdriver: silent no-op (asynq に対応 API なし)
func WithKeepCompletedAge(age time.Duration) EnqueueOption {
	return func(o *EnqueueOptions) { o.KeepCompletedAge = age }
}

// WithKeepFailedAge is the failed-side analogue of WithKeepCompletedAge
// (BullMQ `removeOnFail: {age: <sec>}` 互換)。WithKeepFailed と併用
// すると count / age の両条件で prune される。
func WithKeepFailedAge(age time.Duration) EnqueueOption {
	return func(o *EnqueueOptions) { o.KeepFailedAge = age }
}

// ApplyEnqueueOptions folds the variadic options into a single
// EnqueueOptions. Drivers call this to obtain a populated struct
// before mapping to their native API.
func ApplyEnqueueOptions(opts []EnqueueOption) EnqueueOptions {
	var o EnqueueOptions
	for _, fn := range opts {
		fn(&o)
	}
	return o
}
