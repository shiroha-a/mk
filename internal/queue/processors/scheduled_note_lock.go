package processors

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisScheduledNoteLock implements ScheduledNoteLock using a Redis `SETNX`
// + TTL key. This satisfies asynq's at-least-once delivery: a job that is
// retried while the previous attempt is still running will find the key set
// and return (false, nil) so the duplicate caller skips publishing. The TTL
// guarantees that a crashed worker eventually releases the lock without
// requiring manual cleanup, and avoids any database schema changes (= the
// upstream Misskey TS `note_draft` table is left untouched, preserving
// drop-in compatibility, #1045 Phase 2-A)。
type RedisScheduledNoteLock struct {
	redis     *redis.Client
	keyPrefix string
	ttl       time.Duration
}

// defaultScheduledNoteLockTTL is large enough to survive a single publish
// attempt (= note insert + fanout + delete) while small enough to release the
// lock promptly after a crash. 5 minutes mirrors the upper bound of upstream
// note creation pipelines (delivery fanout + chart hooks).
const defaultScheduledNoteLockTTL = 5 * time.Minute

// NewRedisScheduledNoteLock wires the lock with the given Redis client. The
// keyPrefix should include the deployment's global Redis prefix so locks are
// namespaced per instance. ttl<=0 falls back to the package default.
func NewRedisScheduledNoteLock(client *redis.Client, keyPrefix string, ttl time.Duration) *RedisScheduledNoteLock {
	if ttl <= 0 {
		ttl = defaultScheduledNoteLockTTL
	}
	return &RedisScheduledNoteLock{
		redis:     client,
		keyPrefix: keyPrefix + "scheduled-note-lock:",
		ttl:       ttl,
	}
}

// TryAcquire returns (true, nil) when SET NX establishes a fresh key (= this
// caller is the first publisher), (false, nil) when the key already exists
// (= another retry holds the lock), or (_, err) when Redis itself is
// unreachable so the asynq retry path can back off. SetArgs を使う理由は
// go-redis 公式の `SetNX(key, val, ttl)` が deprecated 化されたため。
func (l *RedisScheduledNoteLock) TryAcquire(ctx context.Context, draftID string) (bool, error) {
	out, err := l.redis.SetArgs(ctx, l.keyPrefix+draftID, "1", redis.SetArgs{Mode: "NX", TTL: l.ttl}).Result()
	if err != nil {
		// redis.Nil = key already exists (= 競合) → 占有失敗、retry でも
		// 結果は同じ。
		if err == redis.Nil {
			return false, nil
		}
		return false, err
	}
	// SET NX が成功すると "OK" を返す。それ以外は失敗扱い。
	return out == "OK", nil
}
