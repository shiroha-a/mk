package queue

// Policy captures runtime tuning knobs for a single logical queue. The
// fields are applied lazily by NewClient (default MaxRetry on enqueue) and
// by the driver Server constructors (worker concurrency / rate limit). All
// zero values mean "use the driver default" — silent no-op when unset.
//
// MaxAttempts only affects enqueue paths that pre-pend the default before
// caller opts (currently EnqueueDeliver only — webhook / cleanRemoteNotes
// / reactionFlush keep their hard-coded retry policies because they encode
// task-specific semantics rather than queue-wide tuning).
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
	// `<queue>JobMaxAttempts`. Zero = "fall back to driver default".
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
}

// PolicyMap maps queue name → Policy. Lookups for missing queues return
// the zero Policy, which the driver / client treats as "no override".
type PolicyMap map[string]Policy

// PolicyFor returns the Policy registered for queueName, or the zero value
// when nothing is configured. Safe to call on a nil PolicyMap.
func (m PolicyMap) PolicyFor(queueName string) Policy {
	if m == nil {
		return Policy{}
	}
	return m[queueName]
}
