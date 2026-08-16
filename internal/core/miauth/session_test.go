package miauth

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestStore(t *testing.T) (*SessionStore, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	// 接続断のテストで再試行のバックオフを待たないよう、リトライを切る。
	client := redis.NewClient(&redis.Options{Addr: mr.Addr(), MaxRetries: -1})
	t.Cleanup(func() { _ = client.Close() })
	return NewSessionStore(client), mr
}

func TestSessionStore_PendingRoundTrip(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()

	token, err := s.StartPending(ctx, "remote.example", "sess-1")
	require.NoError(t, err)
	assert.Len(t, token, 64, "256bit を hex にした長さ")

	got, err := s.TakePending(ctx, token)
	require.NoError(t, err)
	assert.Equal(t, "remote.example", got.Host)
	assert.Equal(t, "sess-1", got.Session)
}

// **取り出しと削除を分けない。** 残しておくと、同じトークンで check を何度も
// 叩かせる口になる。
func TestSessionStore_TakePending_IsSingleUse(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()

	token, err := s.StartPending(ctx, "remote.example", "sess-1")
	require.NoError(t, err)
	_, err = s.TakePending(ctx, token)
	require.NoError(t, err)

	_, err = s.TakePending(ctx, token)
	assert.ErrorIs(t, err, ErrSessionNotFound)
}

func TestSessionStore_TakePending_Expires(t *testing.T) {
	s, mr := newTestStore(t)
	ctx := context.Background()

	token, err := s.StartPending(ctx, "remote.example", "sess-1")
	require.NoError(t, err)

	mr.FastForward(PendingTTL + time.Second)

	_, err = s.TakePending(ctx, token)
	assert.ErrorIs(t, err, ErrSessionNotFound)
}

func TestSessionStore_TakePending_UnknownToken(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()

	_, err := s.TakePending(ctx, "")
	assert.ErrorIs(t, err, ErrSessionNotFound)

	_, err = s.TakePending(ctx, "deadbeef")
	assert.ErrorIs(t, err, ErrSessionNotFound)
}

func TestSessionStore_TakePending_CorruptPayload(t *testing.T) {
	s, mr := newTestStore(t)
	ctx := context.Background()

	require.NoError(t, mr.Set(pendingPrefix+"tok", "not json"))
	_, err := s.TakePending(ctx, "tok")
	assert.ErrorIs(t, err, ErrSessionNotFound)
}

func TestSessionStore_VerifiedRoundTrip(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()

	token, err := s.SaveVerified(ctx, &Contact{
		Host: "remote.example", RemoteID: "9abc", Username: "alice", Name: "Alice",
	})
	require.NoError(t, err)

	// 申請 → 状態確認 → 登録と複数回使うので、読んでも消えないこと。
	for range 3 {
		got, err := s.Verified(ctx, token)
		require.NoError(t, err)
		assert.Equal(t, "remote.example", got.Host)
		assert.Equal(t, "9abc", got.RemoteID)
		assert.Equal(t, "alice", got.Username)
	}
}

func TestSessionStore_DropVerified(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()

	token, err := s.SaveVerified(ctx, &Contact{Host: "remote.example", RemoteID: "9abc", Username: "alice"})
	require.NoError(t, err)
	require.NoError(t, s.DropVerified(ctx, token))

	_, err = s.Verified(ctx, token)
	assert.ErrorIs(t, err, ErrSessionNotFound)

	// 空トークンは no-op。
	assert.NoError(t, s.DropVerified(ctx, ""))
}

func TestSessionStore_Verified_Expires(t *testing.T) {
	s, mr := newTestStore(t)
	ctx := context.Background()

	token, err := s.SaveVerified(ctx, &Contact{Host: "remote.example", RemoteID: "9abc", Username: "alice"})
	require.NoError(t, err)

	mr.FastForward(VerifiedTTL + time.Second)

	_, err = s.Verified(ctx, token)
	assert.ErrorIs(t, err, ErrSessionNotFound)
}

func TestSessionStore_Verified_UnknownOrCorrupt(t *testing.T) {
	s, mr := newTestStore(t)
	ctx := context.Background()

	_, err := s.Verified(ctx, "")
	assert.ErrorIs(t, err, ErrSessionNotFound)

	_, err = s.Verified(ctx, "deadbeef")
	assert.ErrorIs(t, err, ErrSessionNotFound)

	require.NoError(t, mr.Set(verifiedPrefix+"tok", "not json"))
	_, err = s.Verified(ctx, "tok")
	assert.ErrorIs(t, err, ErrSessionNotFound)
}

// pending と verified で名前空間が分かれていること。**混ざると、承認前の
// トークンが検証済みとして通りうる。**
func TestSessionStore_NamespacesAreSeparate(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()

	pendingToken, err := s.StartPending(ctx, "remote.example", "sess-1")
	require.NoError(t, err)

	_, err = s.Verified(ctx, pendingToken)
	assert.ErrorIs(t, err, ErrSessionNotFound)

	verifiedToken, err := s.SaveVerified(ctx, &Contact{Host: "remote.example", RemoteID: "9abc", Username: "alice"})
	require.NoError(t, err)

	_, err = s.TakePending(ctx, verifiedToken)
	assert.ErrorIs(t, err, ErrSessionNotFound)
}

func TestSessionStore_RedisFailures(t *testing.T) {
	s, mr := newTestStore(t)
	ctx := context.Background()

	token, err := s.SaveVerified(ctx, &Contact{Host: "remote.example", RemoteID: "9abc", Username: "alice"})
	require.NoError(t, err)
	pendingToken, err := s.StartPending(ctx, "remote.example", "sess-1")
	require.NoError(t, err)

	// 接続断は「見つからない」ではなくエラーとして上げる。**取り違えると、
	// Redis が落ちている間ずっと「そんなセッションは無い」と案内してしまう。**
	mr.Close()

	_, err = s.Verified(ctx, token)
	assert.Error(t, err)
	assert.NotErrorIs(t, err, ErrSessionNotFound)

	_, err = s.TakePending(ctx, pendingToken)
	assert.Error(t, err)
	assert.NotErrorIs(t, err, ErrSessionNotFound)

	_, err = s.StartPending(ctx, "remote.example", "sess-2")
	assert.Error(t, err)

	_, err = s.SaveVerified(ctx, &Contact{Host: "remote.example", RemoteID: "x", Username: "y"})
	assert.Error(t, err)

	assert.Error(t, s.DropVerified(ctx, token))
}

// トークンは推測されると他人の検証済みセッションを乗っ取れる。毎回別の値が
// 出ること。
func TestNewSessionID_IsUnique(t *testing.T) {
	seen := make(map[string]bool, 32)
	for range 32 {
		id, err := NewSessionID()
		require.NoError(t, err)
		assert.Len(t, id, 64)
		assert.False(t, seen[id], "duplicate session id: %s", id)
		seen[id] = true
	}
}
