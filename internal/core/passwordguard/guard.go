// Package passwordguard limits how often a signed-in account's current
// password can be checked and found wrong.
//
// `i/*` の endpoint のうち現在のパスワードを照合するもの (パスワード変更・
// token 再生成・アカウント削除・2FA の設定変更など) は、token を持つ相手に
// とってパスワードの総当たりのオラクルになる。当たればパスワードそのものが
// 手に入り、token を失効させても signin し直せる。
//
// **数えるのは照合に失敗した回数だけで、key はアカウント単位 1 つ。**
// route ごとのレート制限 (limiter) で絞ると、limiter は権限検査より前に走って
// 成否に関係なく消費するので、token を持つだけの第三者が被害者の
// `i/regenerate-token` を使い切れる (= token 漏洩時の唯一の対処を止められる)。
// endpoint ごとの bucket では合計の試行回数も endpoint の数だけ増える。
package passwordguard

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// Default budget: this many wrong passwords per account within Window.
const (
	DefaultMaxFailures = 10
	DefaultWindow      = time.Hour
)

// ErrTooManyFailures is returned (wrapped in *LimitedError) when the account
// has used up its failure budget.
var ErrTooManyFailures = errors.New("passwordguard: too many incorrect passwords")

// LimitedError reports when the next check will be allowed.
type LimitedError struct {
	RetryAfter time.Duration
}

func (e *LimitedError) Error() string { return ErrTooManyFailures.Error() }

// Unwrap lets errors.Is match ErrTooManyFailures.
func (e *LimitedError) Unwrap() error { return ErrTooManyFailures }

// Attempt is one reserved password check.
//
// 予約はそのまま「失敗 1 回」として残る。照合に成功したとき、または照合
// しなかったとき (検証枠が取れなかった等) にだけ Release する。
type Attempt interface {
	// Release drops the reservation so it does not count as a failure.
	Release(ctx context.Context)
}

// Guard reserves password checks against a per-account failure budget.
type Guard interface {
	// Begin reserves one check for userID. It returns an error wrapping
	// ErrTooManyFailures when the budget is exhausted; the caller must not
	// compare the password then.
	Begin(ctx context.Context, userID string) (Attempt, error)
}

// RedisGuard implements Guard with a sliding-window sorted set per account.
type RedisGuard struct {
	rdb         redis.Cmdable
	maxFailures int
	window      time.Duration
	now         func() time.Time
}

// NewRedisGuard returns a Guard allowing DefaultMaxFailures wrong passwords
// per account within DefaultWindow.
func NewRedisGuard(rdb redis.Cmdable) *RedisGuard {
	return &RedisGuard{rdb: rdb, maxFailures: DefaultMaxFailures, window: DefaultWindow, now: time.Now}
}

func failureKey(userID string) string {
	return "passwordguard:failures:" + userID
}

// Begin implements Guard.
//
// **照合の前に予約する。** 「失敗したら記録する」形だと、bcrypt の照合中
// (~100ms) に並行で投げたリクエストがすべて残り枠を見て通り、上限を何倍にも
// 超えられる。先に ZADD してから数えるので、並行でも照合に進めるのは
// 上限までに限られる。上限を超えた予約はその場で取り消す (照合していない
// ので失敗として数えない。数えると 429 を無視して叩き続けるだけで窓が
// 押し戻され続ける)。
func (g *RedisGuard) Begin(ctx context.Context, userID string) (Attempt, error) {
	if g == nil || g.rdb == nil {
		return noopAttempt{}, nil
	}
	now := g.now()
	nowMicro := now.UnixMicro()
	member, err := newMember(nowMicro)
	if err != nil {
		return nil, err
	}
	key := failureKey(userID)
	pipe := g.rdb.TxPipeline()
	pipe.ZRemRangeByScore(ctx, key, "-inf", strconv.FormatInt(nowMicro-g.window.Microseconds(), 10))
	pipe.ZAdd(ctx, key, redis.Z{Score: float64(nowMicro), Member: member})
	card := pipe.ZCard(ctx, key)
	oldest := pipe.ZRangeWithScores(ctx, key, 0, 0)
	pipe.PExpire(ctx, key, g.window)
	if _, err := pipe.Exec(ctx); err != nil {
		return nil, fmt.Errorf("passwordguard: reserve: %w", err)
	}
	a := &redisAttempt{rdb: g.rdb, key: key, member: member}
	if int(card.Val()) <= g.maxFailures {
		return a, nil
	}
	a.Release(ctx)
	retry := g.window
	if o := oldest.Val(); len(o) > 0 {
		retry = time.Duration(int64(o[0].Score)+g.window.Microseconds()-nowMicro) * time.Microsecond
	}
	if retry < 0 {
		retry = 0
	}
	return nil, &LimitedError{RetryAfter: retry}
}

func newMember(nowMicro int64) (string, error) {
	var b [8]byte
	if _, err := cryptorand.Read(b[:]); err != nil {
		return "", fmt.Errorf("passwordguard: member: %w", err)
	}
	return strconv.FormatInt(nowMicro, 10) + "-" + hex.EncodeToString(b[:]), nil
}

type redisAttempt struct {
	rdb    redis.Cmdable
	key    string
	member string
}

// Release implements Attempt.
func (a *redisAttempt) Release(ctx context.Context) {
	// 取り消しに失敗しても失敗 1 回として残るだけで、安全側に倒れる。
	_ = a.rdb.ZRem(ctx, a.key, a.member).Err()
}

type noopAttempt struct{}

// Release implements Attempt.
func (noopAttempt) Release(context.Context) {}

// NoopAttempt returns an Attempt whose Release does nothing. Callers use it
// when the guard is not configured or its store failed.
func NoopAttempt() Attempt { return noopAttempt{} }
