package channels

import (
	"encoding/json"
	"testing"

	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/stream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNotifications_AuthenticatedSubscribesPerUser(t *testing.T) {
	ctx := newCtx(&model.User{ID: "alice"})
	ch := NewNotifications(ctx)
	ch.Init(nil)
	assert.Equal(t, []string{"notifications:alice"}, ctx.subs)

	ch.OnRedisEvent([]byte(`{"id":"n1","type":"follow"}`))
	require.Len(t, ctx.sentType, 1)
	assert.Equal(t, "notification", ctx.sentType[0])

	ch.OnClientMessage("ignored", json.RawMessage(`{}`))
	ch.Dispose()
	assert.Equal(t, []string{"notifications:alice"}, ctx.unsubs)
}

func TestNotifications_AnonymousIsNoOp(t *testing.T) {
	ctx := newCtx(nil)
	ch := NewNotifications(ctx)
	err := ch.Init(nil)
	assert.ErrorIs(t, err, stream.ErrInvalidParams)
	assert.Empty(t, ctx.subs)
	ch.Dispose()
	assert.Empty(t, ctx.unsubs)
}

func TestNotifications_NilUserPointer(t *testing.T) {
	ctx := newCtx((*model.User)(nil))
	ch := NewNotifications(ctx)
	err := ch.Init(nil)
	assert.ErrorIs(t, err, stream.ErrInvalidParams)
	assert.Empty(t, ctx.subs)
}

func TestMain_AuthenticatedSubscribesBoth(t *testing.T) {
	ctx := newCtx(&model.User{ID: "alice"})
	ch := NewMain(ctx)
	ch.Init(nil)
	assert.ElementsMatch(t, []string{"notifications:alice", "main:alice"}, ctx.subs)

	// envelope なし → "notification" として転送される
	ch.OnRedisEvent([]byte(`{"id":"n1"}`))
	require.Len(t, ctx.sentType, 1)
	assert.Equal(t, "notification", ctx.sentType[0])

	// envelope あり → そのタイプとして転送される
	ch.OnRedisEvent([]byte(`{"type":"follow","body":{"userId":"u2"}}`))
	require.Len(t, ctx.sentType, 2)
	assert.Equal(t, "follow", ctx.sentType[1])

	// 通知 timeline の bare body は `type` に "mention" 等を含むが、`body`
	// が無いので envelope ではなく "notification" として転送する (#420
	// follow-up: bare 通知 body の routing 修正)。これが無いと frontend
	// `main.on('notification', ...)` が発火せず timeline がライブ更新しない。
	ctx2 := newCtx(&model.User{ID: "alice"})
	ch2 := NewMain(ctx2)
	ch2.Init(nil)
	ch2.OnRedisEvent([]byte(`{"id":"n1","type":"mention","createdAt":"x","notifierId":"bob"}`))
	require.Len(t, ctx2.sentType, 1)
	assert.Equal(t, "notification", ctx2.sentType[0],
		"bare notification body must forward as `notification`, not its embedded type")

	ch.OnClientMessage("ignored", json.RawMessage(`{}`))
	ch.Dispose()
	assert.ElementsMatch(t, []string{"notifications:alice", "main:alice"}, ctx.unsubs)
}

func TestMain_AnonymousIsNoOp(t *testing.T) {
	ctx := newCtx(nil)
	ch := NewMain(ctx)
	err := ch.Init(nil)
	assert.ErrorIs(t, err, stream.ErrInvalidParams)
	assert.Empty(t, ctx.subs)
	ch.Dispose()
	assert.Empty(t, ctx.unsubs)
}

func TestMain_BadJSONEnvelopeFallsBack(t *testing.T) {
	ctx := newCtx(&model.User{ID: "alice"})
	ch := NewMain(ctx)
	ch.Init(nil)
	// invalid JSON は notification にフォールバックして送信される
	ch.OnRedisEvent([]byte(`{not json`))
	require.Len(t, ctx.sentType, 1)
	assert.Equal(t, "notification", ctx.sentType[0])
}

func TestMain_FollowEventRefreshesSnapshot(t *testing.T) {
	ctx := newCtx(&model.User{ID: "alice"})
	ch := NewMain(ctx)
	ch.Init(nil)
	ch.OnRedisEvent([]byte(`{"type":"follow","body":{"id":"followee1"}}`))
	// snapshot が follow で更新される (followee1 を following=true で追加)。
	require.Len(t, ctx.followingUpd, 1)
	assert.Equal(t, followingUpdate{"followee1", true}, ctx.followingUpd[0])
	// client へも従来どおり転送される。
	assert.Equal(t, []string{"follow"}, ctx.sentType)
}

func TestMain_UnfollowEventRefreshesSnapshot(t *testing.T) {
	ctx := newCtx(&model.User{ID: "alice"})
	ch := NewMain(ctx)
	ch.Init(nil)
	ch.OnRedisEvent([]byte(`{"type":"unfollow","body":{"id":"followee1"}}`))
	require.Len(t, ctx.followingUpd, 1)
	assert.Equal(t, followingUpdate{"followee1", false}, ctx.followingUpd[0])
}

func TestMain_FollowedEventDoesNotRefreshSnapshot(t *testing.T) {
	ctx := newCtx(&model.User{ID: "alice"})
	ch := NewMain(ctx)
	ch.Init(nil)
	// `followed` (誰かが自分を follow) は自分の follow 一覧を変えないので
	// snapshot を触らない。client へは転送される。
	ch.OnRedisEvent([]byte(`{"type":"followed","body":{"id":"follower1"}}`))
	assert.Empty(t, ctx.followingUpd)
	assert.Equal(t, []string{"followed"}, ctx.sentType)
}

func TestMain_FollowEventMissingIDIgnored(t *testing.T) {
	ctx := newCtx(&model.User{ID: "alice"})
	ch := NewMain(ctx)
	ch.Init(nil)
	// id が無い follow body は snapshot 更新できないので no-op (転送は継続)。
	ch.OnRedisEvent([]byte(`{"type":"follow","body":{"username":"x"}}`))
	assert.Empty(t, ctx.followingUpd)
	assert.Equal(t, []string{"follow"}, ctx.sentType)
}
