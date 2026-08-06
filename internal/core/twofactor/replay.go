package twofactor

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
)

// testMode mirrors config.testMode. Router が起動時に一度だけ設定する。
var testMode atomic.Bool

// SetTestMode enables the upstream-compatible TOTP bypass for test runs.
// 本番 (testMode=false) では何の効果も無い。
func SetTestMode(v bool) {
	testMode.Store(v)
}

// bypassForTest reports whether TOTP validation should be skipped entirely.
//
// upstream UserAuthService.validateOtp は
// `NODE_ENV === 'test' && MISSKEY_TEST_CHECK_DUPLICATED_TOTP !== '1'` のとき
// 検証ごと素通しにする。本家 e2e は 2FA 登録に使ったコードを直後の signin でも
// 使い回すので、素通しにしないと replay 保護に引っかかって成立しない。
// replay 保護そのものを検証するテストだけが同 env に '1' を立てる。
//
// 環境変数は毎回読む。e2e ハーネスがテストの途中で値を差し替えるため。
func bypassForTest() bool {
	return testMode.Load() && os.Getenv("MISSKEY_TEST_CHECK_DUPLICATED_TOTP") != "1"
}

// ReplayGuard records previously accepted TOTP codes so the same code
// cannot be replayed within its acceptance window.
//
// RFC 6238 §5.2 requires that "the verifier MUST NOT accept the second
// attempt of the OTP after the successful validation has been issued
// for the first OTP". Upstream Misskey TS (UserAuthService) does not
// implement this protection, so an attacker who observes a 6-digit code
// (phishing, shoulder surfing, MITM proxy) can replay it within the code's
// acceptance window — long enough to open multiple sessions or to chain into
// other 2FA-gated endpoints (i/2fa/done, key registration, etc.).
//
// mk-go ships this as an independent hardening on top of the drop-in
// compatible schema: no new tables, no new error codes, just a Redis
// keyspace that mirrors the validity window of a TOTP code.
type ReplayGuard interface {
	// MarkUsed records (userID, code) as consumed. It returns true if
	// the entry was newly inserted (caller may treat the code as valid),
	// or false if the same code was already accepted within the window
	// (caller must reject the request as a replay).
	MarkUsed(ctx context.Context, userID, code string) (bool, error)
}

// defaultReplayTTL covers the full acceptance window of a TOTP code under
// our validation settings (Period=30s, Skew=totpSkew). A code generated for
// step T stays structurally valid across [T-skew, T+skew] = (2*skew+1) steps,
// so the guard entry must survive at least that long after the first
// acceptance — otherwise the code could be replayed during the residual
// validity once the entry expires. We pad by one extra step for clock jitter.
// totpSkew から導出して lockstep を保つ。#2069 で totpSkew が 5→1 になった
// (upstream #17580 validationWindow:1 追従) ため Skew=1 → 4 steps = 120s
// (validity 90s + 30s jitter pad)。upstream ttl=timeStep*(window*2+1)=90s より 1 step
// 保守的。
const defaultReplayTTL = time.Duration(2*totpSkew+2) * 30 * time.Second

// RedisReplayGuard implements ReplayGuard on top of go-redis. Keys are
// per-user and per-code; collisions across users are impossible because
// the userID is part of the key.
type RedisReplayGuard struct {
	Client redis.UniversalClient
	// KeyPrefix is prepended to every Redis key. When empty, a sensible
	// default ("mk:2fa:totp:used") is used so callers can opt into the
	// guard without configuring anything else.
	KeyPrefix string
	// TTL is the lifetime of each replay record. When zero, defaults to
	// defaultReplayTTL (= TOTP acceptance window for the configured Skew,
	// with a one-step safety margin).
	TTL time.Duration
}

// NewRedisReplayGuard creates a ReplayGuard backed by the given Redis
// client. Passing nil returns nil — callers should treat a nil guard
// as "protection disabled" (used in unit tests that do not need a Redis
// fixture; production paths should always wire a non-nil guard).
func NewRedisReplayGuard(client redis.UniversalClient) *RedisReplayGuard {
	if client == nil {
		return nil
	}
	return &RedisReplayGuard{Client: client}
}

// MarkUsed atomically inserts the (userID, code) entry. Returns true if
// inserted (= code is fresh), false if it was already present (= replay).
func (g *RedisReplayGuard) MarkUsed(ctx context.Context, userID, code string) (bool, error) {
	if g == nil || g.Client == nil {
		// fail-open: keep historical Misskey behaviour when no guard is
		// wired (= unit tests, dev without Redis)
		return true, nil
	}
	prefix := g.KeyPrefix
	if prefix == "" {
		prefix = "mk:2fa:totp:used"
	}
	ttl := g.TTL
	if ttl <= 0 {
		// 未設定時は TOTP acceptance window をカバーする default を使う。
		ttl = defaultReplayTTL
	}
	key := fmt.Sprintf("%s:%s:%s", prefix, userID, code)
	return g.Client.SetNX(ctx, key, "1", ttl).Result()
}

// ValidateWithReplay validates a TOTP code AND records it as consumed
// via the supplied guard. Returns true only when the code is structurally
// valid for the secret AND has not been accepted within the replay
// window. A nil guard disables replay protection (returns Validate-only
// semantics) — production callers should always pass a non-nil guard.
//
// The caller is expected to translate a false return into the same
// "invalid token" response shape it already returns for ordinary TOTP
// failures, so the frontend / client behaviour is unchanged.
func ValidateWithReplay(ctx context.Context, guard ReplayGuard, userID, code, secret string) bool {
	if bypassForTest() {
		return true
	}
	if !Validate(code, secret) {
		return false
	}
	if guard == nil {
		return true
	}
	ok, err := guard.MarkUsed(ctx, userID, code)
	if err != nil {
		// Redis 障害時に 2FA を完全閉塞させてしまうと operator が自分の
		// 環境から締め出される事故が起きるため、guard 障害時は historical
		// 挙動 (= replay 保護無し) に fail-open する。「2FA だけ degraded」
		// に気付けるよう warn ログを残す (他経路 queue / cache の alarm が
		// 出ていなくても本ログだけで判別できる)。
		slog.Warn("twofactor: replay guard degraded, falling back to no replay protection", "userID", userID, "err", err)
		return true
	}
	return ok
}
