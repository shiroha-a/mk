package signupform

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// redisNonceKeyPrefix namespaces the burnt nonces.
const redisNonceKeyPrefix = "mk:signupform:nonce"

// RedisNonceStore implements NonceStore on top of go-redis.
//
// **fail-open にしない。** TOTP の replay guard (core/twofactor) は Redis 障害時に
// 保護を落として通すが、あちらは「operator が自分の環境から締め出される」ほうが
// 重い。こちらは未認証の入口で、素通しにすると captcha 未設定のインスタンスで
// 防波堤が 1 つも無い状態に戻る。error は呼び出し元へ返し、申請を断る。
type RedisNonceStore struct {
	client redis.UniversalClient
}

// NewRedisNonceStore creates a NonceStore, returning nil for a nil client.
//
// **戻り値は具象型。** interface に入れてから nil 判定すると typed-nil で必ず
// 非 nil になるので、呼び出し側は具象のまま nil を見て「未配線」と判断すること
// (router がそうしている)。
func NewRedisNonceStore(client redis.UniversalClient) *RedisNonceStore {
	if client == nil {
		return nil
	}
	return &RedisNonceStore{client: client}
}

// Burn marks nonce as consumed, returning true only for the first caller.
func (s *RedisNonceStore) Burn(ctx context.Context, nonce string, ttl time.Duration) (bool, error) {
	if s == nil || s.client == nil {
		return false, fmt.Errorf("signupform: nonce store is not wired")
	}
	fresh, err := s.client.SetNX(ctx, redisNonceKeyPrefix+":"+nonce, "1", ttl).Result()
	if err != nil {
		return false, fmt.Errorf("signupform: setnx nonce: %w", err)
	}
	return fresh, nil
}
