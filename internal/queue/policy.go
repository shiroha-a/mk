package queue

import (
	"strings"
	"time"
)

// Policy captures runtime tuning knobs for a single logical queue. The
// fields are applied lazily by NewClient (default MaxRetry on enqueue) and
// by the driver Server constructors (worker concurrency / rate limit). All
// zero values mean "use the driver default" — silent no-op when unset.
//
// 各 field の適用範囲:
//
//   - MaxAttempts: EnqueueDeliver / EnqueueInbox のみ pre-pend する
//     (webhook / cleanRemoteNotes / reactionFlush 等は task-specific semantics
//     を encode する hard-coded retry を保持)。
//   - KeepFailed / KeepCompleted / KeepCompletedAge / KeepFailedAge:
//     **全 enqueue helper** で pre-pend される (#1184 / #1193)。retention 系は
//     queue-wide hygiene として一律に効かせる方針 (Misskey TS が QueueService.ts
//     全 helper で `removeOnComplete`/`removeOnFail` を一律 set している規範に
//     合わせる)。
type Policy struct {
	// Concurrency overrides the worker pool size for this queue. 0 means
	// "fall back to driver default" — for asynq this is the global pool
	// gated by priority weights, for mkq it is total/len(queues).
	Concurrency int

	// RatePerSec caps task processing throughput at N tasks per second.
	// 0 means no limit. Implemented as a token-bucket inside the worker
	// dispatch path, so it back-pressures handler invocations rather
	// than rejecting enqueues.
	RatePerSec int

	// MaxAttempts is the **total** number of tries (initial + retries)
	// allowed for tasks on this queue, matching BullMQ's `attempts`
	// option semantics — same shape as Misskey TS YAML
	// `<queue>JobMaxAttempts`.
	//
	// Zero は EnqueueDeliver / EnqueueInbox では「WithMaxRetry を付けない」
	// = mkq の attempts=0 (= リトライ無し) を意味する。落ちた配送先への
	// retry-backoff を発火させるため、deliver/inbox は server 側の
	// buildPolicy が未指定時に TS 互換の default (12 / 8) を当てる (#1411)。
	//
	// 内部では asynq の MaxRetry (= retries on top of initial) に
	// 変換するため EnqueueDeliver で N-1 を渡す。drop-in 互換維持の
	// ために TS の YAML 値そのままで一致させる必要がある (#531 review)。
	MaxAttempts int

	// KeepFailed bounds the size of the failed bucket / ZSET for this
	// queue. mkq では per-job `WithKeepFailed(n)` に翻訳されて failed
	// ZSET の超過分が古い順に prune される (BullMQ `removeOnFail: N`
	// 互換)。0 = retention 無し (= 蓄積し続ける、従来挙動)。
	//
	// `MaxAttempts` と同じく EnqueueDeliver / EnqueueInbox が caller
	// opts の前に prepend する形で渡る (#1184)。
	KeepFailed int

	// KeepCompleted bounds the size of the completed bucket / ZSET for
	// this queue, mirroring KeepFailed's semantics for the completed
	// side (BullMQ `removeOnComplete: N` 互換、#1193)。0 = retention
	// 無し (= BullMQ default の無制限蓄積、UDS 観測で実害があった
	// 旧挙動)。
	KeepCompleted int

	// KeepCompletedAge bounds completed job retention by age, mirroring
	// BullMQ TS の `removeOnComplete: {age: <seconds>}`。KeepCompleted
	// と併用すると count / age の両条件で prune される (TS upstream
	// は両方指定する規約)。
	KeepCompletedAge time.Duration

	// KeepFailedAge は failed bucket の age-based retention。
	// KeepFailed と併用して BullMQ TS の `removeOnFail: {age: <sec>,
	// count: N}` を再現する。
	KeepFailedAge time.Duration

	// BackoffType / BackoffDelay override the retry backoff strategy for this
	// queue. BackoffType is "" (use the queue's built-in default), "fixed",
	// "exponential", or "custom"; BackoffDelay is the base delay (ignored for
	// "custom"). deliver / inbox は未指定なら "custom" (= Misskey TS
	// httpRelatedBackoff: (2^n-1)*60s, cap 8h, +0..20% jitter) が既定
	// (#1405 / #1406, mkq#67)。
	BackoffType  string
	BackoffDelay time.Duration
}

// PolicyMap maps queue name → Policy. Lookups for missing queues return
// the zero Policy, which the driver / client treats as "no override".
type PolicyMap map[string]Policy

// PolicyFor returns the Policy registered for queueName, or the zero value
// when nothing is configured. Safe to call on a nil PolicyMap.
//
// **プラグインのキューは接頭辞で引く。** `plugin:<名前>` は運営者が入れた
// プラグインの数だけ増えるので、名前ごとに登録させると「登録し忘れた
// プラグインだけ retention が効かない」形になる (実際に 1 つも登録されて
// おらず、プラグインの completed ジョブが無期限に積まれていた)。
// `PluginQueuePrefix` をキーにした entry があればそれを既定として使う。
// 個別の名前で登録されていればそちらが優先される。
func (m PolicyMap) PolicyFor(queueName string) Policy {
	if m == nil {
		return Policy{}
	}
	if p, ok := m[queueName]; ok {
		return p
	}
	if strings.HasPrefix(queueName, PluginQueuePrefix) {
		return m[PluginQueuePrefix]
	}
	return Policy{}
}
