package middleware

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/redis/go-redis/v9"
	"github.com/shiroha-a/mk/internal/api/apierr"
)

// EndpointLimit defines rate limit parameters for an API endpoint.
type EndpointLimit struct {
	Duration    time.Duration // ウィンドウ幅（例: 1時間）
	Max         int           // ウィンドウ内最大リクエスト数
	MinInterval time.Duration // 連続リクエスト最小間隔（0なら無効）
	// RejectResponse overrides the default RATE_LIMIT_EXCEEDED body returned on
	// 429 for this endpoint. nil なら汎用 RateLimitExceeded。signin 系は
	// TOO_MANY_AUTHENTICATION_FAILURES を返すために使う (#1829)。
	RejectResponse func() map[string]any
}

// LimitInfo holds the result of a rate limit check.
type LimitInfo struct {
	Remaining int   // 残りリクエスト数
	ResetMs   int64 // リセット時刻 (Unix milliseconds)
}

// RateLimitStore abstracts the backing store for rate limit counters.
type RateLimitStore interface {
	// Check records a request and returns the current limit status.
	// key is the rate limit bucket identifier, duration is the window
	// size, and max is the maximum number of requests allowed.
	Check(ctx context.Context, key string, duration time.Duration, max int) (LimitInfo, error)
}

// RedisRateLimitStore implements RateLimitStore using Redis sorted sets.
// The algorithm mirrors the TS ratelimiter (visionmedia/node-ratelimiter):
// each request is a ZADD with a microsecond timestamp as both score and
// member, old entries are pruned with ZREMRANGEBYSCORE, and ZCARD gives
// the current count within the sliding window.
type RedisRateLimitStore struct {
	rdb *redis.Client
}

// NewRedisRateLimitStore creates a store backed by the given Redis client.
func NewRedisRateLimitStore(rdb *redis.Client) *RedisRateLimitStore {
	return &RedisRateLimitStore{rdb: rdb}
}

// Check implements RateLimitStore using a sliding-window sorted set.
func (s *RedisRateLimitStore) Check(ctx context.Context, key string, duration time.Duration, max int) (LimitInfo, error) {
	now := time.Now().UnixMicro()
	windowStart := now - duration.Microseconds()
	rkey := "limit:" + key

	pipe := s.rdb.Pipeline()
	pipe.ZRemRangeByScore(ctx, rkey, "0", fmt.Sprintf("%d", windowStart))
	zcardCmd := pipe.ZCard(ctx, rkey)
	pipe.ZAdd(ctx, rkey, redis.Z{Score: float64(now), Member: now})
	zrangeOldestCmd := pipe.ZRange(ctx, rkey, 0, 0)
	zrangeAtMaxCmd := pipe.ZRange(ctx, rkey, int64(-max), int64(-max))
	pipe.ZRemRangeByRank(ctx, rkey, 0, int64(-(max + 1)))
	pipe.PExpire(ctx, rkey, duration)

	_, err := pipe.Exec(ctx)
	if err != nil {
		return LimitInfo{}, fmt.Errorf("rate limit redis pipeline: %w", err)
	}

	count := int(zcardCmd.Val())
	remaining := 0
	if count < max {
		remaining = max - count
	}

	// リセット時刻の計算: TS版と同じロジック
	// -max位置のエントリがあればそのタイムスタンプ、なければ最古のエントリ
	var resetMicro int64
	atMax := zrangeAtMaxCmd.Val()
	oldest := zrangeOldestCmd.Val()
	if len(atMax) > 0 {
		resetMicro = parseIntFromString(atMax[0]) + duration.Microseconds()
	} else if len(oldest) > 0 {
		resetMicro = parseIntFromString(oldest[0]) + duration.Microseconds()
	} else {
		resetMicro = now + duration.Microseconds()
	}

	return LimitInfo{
		Remaining: remaining,
		ResetMs:   resetMicro / 1000,
	}, nil
}

// parseIntFromString は文字列を int64 にパースする。
// Redis の ZRANGE は member を文字列で返すため。
func parseIntFromString(s string) int64 {
	var v int64
	fmt.Sscanf(s, "%d", &v)
	return v
}

// PolicyProvider abstracts user policy lookup so the middleware can
// scale Max by `rateLimitFactor` from the user's role policies (#606 item 4)。
// 循環依存を避けるため interface で受け取る (実装は core/role.Service)。
type PolicyProvider interface {
	GetUserPolicies(userID string) map[string]any
}

// RateLimiter provides per-endpoint rate limiting as Echo middleware.
type RateLimiter struct {
	store             RateLimitStore
	enableIPRateLimit bool
	limits            map[string]*EndpointLimit
	policyProvider    PolicyProvider // optional, nil なら factor=1 固定
	disabled          bool
}

// NewRateLimiter creates a RateLimiter with the given store and config.
func NewRateLimiter(store RateLimitStore, enableIPRateLimit bool, limits map[string]*EndpointLimit) *RateLimiter {
	return &RateLimiter{
		store:             store,
		enableIPRateLimit: enableIPRateLimit,
		limits:            limits,
	}
}

// NewRedisRateLimiter creates a RateLimiter backed by Redis.
func NewRedisRateLimiter(rdb *redis.Client, enableIPRateLimit bool, limits map[string]*EndpointLimit) *RateLimiter {
	return NewRateLimiter(NewRedisRateLimitStore(rdb), enableIPRateLimit, limits)
}

// Disable turns the limiter into a no-op.
//
// 本家 RateLimiterService は constructor で NODE_ENV !== 'production' なら
// disabled を立て、開発・テスト環境ではレート制限を一切かけない。mk-go に
// NODE_ENV に相当する概念は無いので、本番では必ず false になる TestMode を
// 同じ用途で使う (呼び出しは router.go 側)。
func (rl *RateLimiter) Disable() {
	rl.disabled = true
}

// SetPolicyProvider wires a PolicyProvider so authenticated user の
// rateLimitFactor が反映される。trusted user に factor=2.0 を割り当てて
// 実効 Max を 2 倍にする等の運用が可能 (#606 item 4)。
func (rl *RateLimiter) SetPolicyProvider(p PolicyProvider) {
	rl.policyProvider = p
}

// Middleware returns an Echo middleware that enforces rate limiting.
// リミット定義のないエンドポイントはそのまま通過する。
// Redisエラー時はfail-open（ログだけ出してリクエストを通す）。
//
// 認証済みリクエストでは userID と IP の両 bucket を OR で評価する
// (#606 item 2): 攻撃者が認証して別アカウントを brute-force するケースで
// userID bucket に逃がされず、IP bucket でも捕捉する。X-RateLimit-Remaining
// は最も逼迫した bucket (最小値) を返す。
func (rl *RateLimiter) Middleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if rl.disabled {
				return next(c)
			}
			endpoint := strings.TrimPrefix(c.Path(), "/api/")
			limit, ok := rl.limits[endpoint]
			if !ok {
				return next(c)
			}

			actors := rl.resolveActors(c)
			if len(actors) == 0 {
				return next(c)
			}

			ctx := c.Request().Context()
			minRemaining := -1

			for _, a := range actors {
				effectiveMax := scaledMax(limit.Max, a.factor)

				// minInterval チェック。#2106 N28: upstream RateLimiterService は
				// `duration: minInterval * factor` で window に factor を乗算する
				// (旧 mk-go は factor 未適用で乖離していた)。
				if limit.MinInterval > 0 {
					minInterval := scaledMinInterval(limit.MinInterval, a.factor)
					info, err := rl.store.Check(ctx, a.key+":"+endpoint+":min", minInterval, 1)
					if err != nil {
						slog.Warn("rate limit store error (minInterval)", "endpoint", endpoint, "err", err)
						continue
					}
					if info.Remaining == 0 {
						return rl.rejectRequest(c, info, limit)
					}
				}

				// duration/max チェック (factor 適用済みの effectiveMax で評価)
				if limit.Duration > 0 && effectiveMax > 0 {
					info, err := rl.store.Check(ctx, a.key+":"+endpoint, limit.Duration, effectiveMax)
					if err != nil {
						slog.Warn("rate limit store error", "endpoint", endpoint, "err", err)
						continue
					}
					if info.Remaining == 0 {
						return rl.rejectRequest(c, info, limit)
					}
					if minRemaining < 0 || info.Remaining < minRemaining {
						minRemaining = info.Remaining
					}
				}
			}
			if minRemaining >= 0 {
				c.Response().Header().Set("X-RateLimit-Remaining", fmt.Sprintf("%d", minRemaining))
			}

			return next(c)
		}
	}
}

// rateLimitActor は単一 bucket を識別する key + factor のペア。
// 認証済 user では userID 由来 actor (factor=user policy) と IP 由来 actor
// (factor=1.0) が並ぶ。未認証では IP 由来 actor のみ。
type rateLimitActor struct {
	key    string  // bucket 識別子 (userID そのまま / "ip-<hash>" 等)
	factor float64 // Max scaling 係数 (>0、1.0 で base、>1 で緩和、<1 で厳格)
}

// resolveActors returns all bucket actors that should be checked for this
// request. 認証済 user は userID + IP の両方、未認証は IP のみを返す。
// EnableIPRateLimit=false かつ未認証なら空 (= 制限スキップ、既存挙動維持)。
func (rl *RateLimiter) resolveActors(c echo.Context) []rateLimitActor {
	var actors []rateLimitActor
	if user := GetUser(c); user != nil {
		// user.ID は ULID で `ip-` prefix と衝突しないので prefix なしで
		// そのまま bucket key にする (既存 Redis state との互換性維持)。
		actors = append(actors, rateLimitActor{
			key:    user.ID,
			factor: rl.userFactor(user.ID),
		})
		// 認証済でも IP bucket を併用 (#606 item 2)。NAT 配下の正当ユーザーは
		// 同一 IP で複数 user として扱われるが、auth 系 endpoint は 1h/60 程度
		// と緩いので実害は小さい (個別 user の rate-limit は userID bucket で
		// 守られる)。
		if rl.enableIPRateLimit {
			actors = append(actors, rateLimitActor{
				key:    ipHash(c.RealIP()),
				factor: 1.0, // IP 由来 bucket は user 単位の factor を適用しない
			})
		}
		return actors
	}
	if !rl.enableIPRateLimit {
		return nil
	}
	return []rateLimitActor{{key: ipHash(c.RealIP()), factor: 1.0}}
}

// userFactor returns the rateLimitFactor from the user's role policies.
// 未配線 / 値不正 / 0 以下なら 1.0 (= 影響なし) で返す。
func (rl *RateLimiter) userFactor(userID string) float64 {
	if rl.policyProvider == nil {
		return 1.0
	}
	policies := rl.policyProvider.GetUserPolicies(userID)
	raw, ok := policies["rateLimitFactor"]
	if !ok {
		return 1.0
	}
	switch v := raw.(type) {
	case float64:
		if v > 0 {
			return v
		}
	case int:
		if v > 0 {
			return float64(v)
		}
	}
	return 1.0
}

// scaledMax divides the base Max by factor, mirroring upstream
// RateLimiterService.limit() の `max: limitation.max / factor` (#2106 N28)。
// rateLimitFactor は **除数** で、値が大きいほど実効 max が小さくなる
// (= role policy で締める) — 例 factor=3(300%) で 1/3 に厳格化、factor=0.3(30%) で
// 約 3.3x に緩和。factor=1.0 / 不正値 (<=0) は base そのまま。結果は最低 1 に
// クランプ (factor 過大の事故で全 user が瞬時に 429 を食らうのを防ぐ)。
func scaledMax(base int, factor float64) int {
	if factor <= 0 || factor == 1.0 {
		return base
	}
	scaled := int(float64(base) / factor)
	if scaled < 1 {
		return 1
	}
	return scaled
}

// scaledMinInterval multiplies the base minInterval window by factor, mirroring
// upstream RateLimiterService の `duration: limitation.minInterval * factor`
// (#2106 N28)。max と同じく factor が大きいほど window が長くなり (= 連投間隔が
// 厳しくなる)、小さいほど緩む。factor=1.0 / 不正値は base そのまま。
func scaledMinInterval(base time.Duration, factor float64) time.Duration {
	if factor <= 0 || factor == 1.0 {
		return base
	}
	scaled := time.Duration(float64(base) * factor)
	if scaled < 0 {
		return base
	}
	return scaled
}

// rejectRequest returns a 429 response with appropriate headers. limit may
// carry a per-endpoint RejectResponse override (e.g. signin returns
// TOO_MANY_AUTHENTICATION_FAILURES instead of the generic RATE_LIMIT_EXCEEDED、
// #1829)。
func (rl *RateLimiter) rejectRequest(c echo.Context, info LimitInfo, limit *EndpointLimit) error {
	retryAfterSec := (info.ResetMs - time.Now().UnixMilli() + 999) / 1000
	if retryAfterSec < 0 {
		retryAfterSec = 0
	}
	c.Response().Header().Set("Retry-After", fmt.Sprintf("%d", retryAfterSec))
	if limit != nil && limit.RejectResponse != nil {
		return c.JSON(http.StatusTooManyRequests, limit.RejectResponse())
	}
	return c.JSON(http.StatusTooManyRequests, apierr.RateLimitExceeded())
}

// ipHash computes a rate-limit key for an IP address.
// TS版 getIpHash 互換: IPv6は/64マスク、IPv4はフルアドレスをハッシュ。
func ipHash(ipStr string) string {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		// パース不能な場合はSHA256フォールバック
		h := sha256.Sum256([]byte(ipStr))
		n := new(big.Int).SetBytes(h[:8])
		return "ip-" + n.Text(36)
	}

	if ip4 := ip.To4(); ip4 != nil {
		// IPv4: フルアドレスをそのまま使用
		n := new(big.Int).SetBytes(ip4)
		return "ip-" + n.Text(36)
	}

	// IPv6: /64マスク（先頭8バイトのみ）
	ip16 := ip.To16()
	n := new(big.Int).SetBytes(ip16[:8])
	return "ip-" + n.Text(36)
}
