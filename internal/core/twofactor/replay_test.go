package twofactor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/pquerna/otp/totp"
	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newMiniRedis(t *testing.T) (*miniredis.Miniredis, goredis.UniversalClient) {
	t.Helper()
	mr, err := miniredis.Run()
	require.NoError(t, err)
	t.Cleanup(mr.Close)
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return mr, client
}

func TestRedisReplayGuard_FirstUseAccepted(t *testing.T) {
	_, client := newMiniRedis(t)
	g := NewRedisReplayGuard(client)

	ok, err := g.MarkUsed(context.Background(), "u1", "123456")
	require.NoError(t, err)
	assert.True(t, ok, "first acceptance must succeed")
}

func TestRedisReplayGuard_SecondUseRejected(t *testing.T) {
	_, client := newMiniRedis(t)
	g := NewRedisReplayGuard(client)
	ctx := context.Background()

	ok, err := g.MarkUsed(ctx, "u1", "123456")
	require.NoError(t, err)
	require.True(t, ok)

	ok2, err := g.MarkUsed(ctx, "u1", "123456")
	require.NoError(t, err)
	assert.False(t, ok2, "second acceptance of same (user, code) must be refused")
}

func TestRedisReplayGuard_DifferentUsersIndependent(t *testing.T) {
	_, client := newMiniRedis(t)
	g := NewRedisReplayGuard(client)
	ctx := context.Background()

	ok, _ := g.MarkUsed(ctx, "alice", "123456")
	require.True(t, ok)
	// 別ユーザは同じ 6 桁を独立して使えること
	ok2, err := g.MarkUsed(ctx, "bob", "123456")
	require.NoError(t, err)
	assert.True(t, ok2)
}

func TestRedisReplayGuard_DifferentCodesIndependent(t *testing.T) {
	_, client := newMiniRedis(t)
	g := NewRedisReplayGuard(client)
	ctx := context.Background()

	ok, _ := g.MarkUsed(ctx, "u1", "111111")
	require.True(t, ok)
	ok2, err := g.MarkUsed(ctx, "u1", "222222")
	require.NoError(t, err)
	assert.True(t, ok2)
}

func TestRedisReplayGuard_TTLExpiresEntry(t *testing.T) {
	mr, client := newMiniRedis(t)
	g := &RedisReplayGuard{Client: client, TTL: 1 * time.Second}
	ctx := context.Background()

	ok, _ := g.MarkUsed(ctx, "u1", "123456")
	require.True(t, ok)
	// miniredis の FastForward で TTL を強制経過
	mr.FastForward(2 * time.Second)
	ok2, err := g.MarkUsed(ctx, "u1", "123456")
	require.NoError(t, err)
	assert.True(t, ok2, "entry must expire after TTL so a brand-new code with the same digits is accepted")
}

func TestRedisReplayGuard_CustomKeyPrefix(t *testing.T) {
	mr, client := newMiniRedis(t)
	g := &RedisReplayGuard{Client: client, KeyPrefix: "custom:prefix"}
	ctx := context.Background()

	_, err := g.MarkUsed(ctx, "u1", "777777")
	require.NoError(t, err)
	keys := mr.Keys()
	require.Len(t, keys, 1)
	assert.Equal(t, "custom:prefix:u1:777777", keys[0])
}

func TestNewRedisReplayGuard_NilClient(t *testing.T) {
	g := NewRedisReplayGuard(nil)
	assert.Nil(t, g, "nil client must return nil guard so caller can opt out cleanly")
}

func TestRedisReplayGuard_NilReceiver_FailsOpen(t *testing.T) {
	// nil guard 経由でも panic せず "accept" を返す (= 後方互換 fallback)
	var g *RedisReplayGuard
	ok, err := g.MarkUsed(context.Background(), "u1", "123456")
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestValidateWithReplay_NilGuard_BehavesLikeValidate(t *testing.T) {
	secret, _, err := GenerateSecret("Misskey", "user")
	require.NoError(t, err)
	code, err := totp.GenerateCode(secret, time.Now())
	require.NoError(t, err)

	// nil guard でも valid code は通る
	assert.True(t, ValidateWithReplay(context.Background(), nil, "u1", code, secret))
	// invalid code は通らない
	assert.False(t, ValidateWithReplay(context.Background(), nil, "u1", "000000", secret))
}

func TestValidateWithReplay_RejectsReplayWithRedisGuard(t *testing.T) {
	_, client := newMiniRedis(t)
	g := NewRedisReplayGuard(client)

	secret, _, err := GenerateSecret("Misskey", "user")
	require.NoError(t, err)
	code, err := totp.GenerateCode(secret, time.Now())
	require.NoError(t, err)
	ctx := context.Background()

	assert.True(t, ValidateWithReplay(ctx, g, "u1", code, secret), "first use must succeed")
	assert.False(t, ValidateWithReplay(ctx, g, "u1", code, secret), "second use of same code must be refused as replay")
	assert.False(t, ValidateWithReplay(ctx, g, "u1", code, secret), "subsequent retries are also refused")
}

func TestValidateWithReplay_InvalidCode_DoesNotConsumeGuardSlot(t *testing.T) {
	mr, client := newMiniRedis(t)
	g := NewRedisReplayGuard(client)

	secret, _, err := GenerateSecret("Misskey", "user")
	require.NoError(t, err)
	ctx := context.Background()

	// 構造的に invalid なコードを送っても Redis には何も書かない
	assert.False(t, ValidateWithReplay(ctx, g, "u1", "000000", secret))
	assert.Empty(t, mr.Keys(), "failed validation must not consume the replay slot")
}

func TestValidateWithReplay_AnotherUserSameCode_AllowedIndependently(t *testing.T) {
	_, client := newMiniRedis(t)
	g := NewRedisReplayGuard(client)

	secret, _, err := GenerateSecret("Misskey", "user")
	require.NoError(t, err)
	code, err := totp.GenerateCode(secret, time.Now())
	require.NoError(t, err)
	ctx := context.Background()

	assert.True(t, ValidateWithReplay(ctx, g, "alice", code, secret))
	// 同じ secret を共有することは通常ないが、guard の隔離性は user 単位
	assert.True(t, ValidateWithReplay(ctx, g, "bob", code, secret))
}

// failingGuard returns an error from MarkUsed, simulating Redis outage.
type failingGuard struct{}

func (failingGuard) MarkUsed(_ context.Context, _, _ string) (bool, error) {
	return false, errors.New("redis down")
}

func TestValidateWithReplay_GuardError_FailsOpen(t *testing.T) {
	// Redis 障害時に 2FA を完全閉塞させない fail-open ポリシーを guard する。
	secret, _, err := GenerateSecret("Misskey", "user")
	require.NoError(t, err)
	code, err := totp.GenerateCode(secret, time.Now())
	require.NoError(t, err)
	assert.True(t, ValidateWithReplay(context.Background(), failingGuard{}, "u1", code, secret))
}
