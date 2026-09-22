package processors_test

import (
	"context"
	"errors"
	"testing"

	"github.com/shiroha-a/mk/internal/queue"
	"github.com/shiroha-a/mk/internal/queue/driver"
	"github.com/shiroha-a/mk/internal/queue/processors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeUnfollower struct {
	calls [][2]string
	err   error
}

func (f *fakeUnfollower) Unfollow(followerID, followeeID string) error {
	f.calls = append(f.calls, [2]string{followerID, followeeID})
	return f.err
}

func TestUnfollowProcessor_Success(t *testing.T) {
	uf := &fakeUnfollower{}
	p := processors.NewUnfollowProcessor(uf)
	task := queue.NewUnfollowTask(queue.UnfollowPayload{
		FollowerID: "rA", FolloweeID: "localA",
	})
	require.NoError(t, p.Handle(context.Background(), task))
	require.Len(t, uf.calls, 1)
	assert.Equal(t, [2]string{"rA", "localA"}, uf.calls[0])
}

// 既に解消済の row は ErrNotFollowing を返すが、worker は冪等吸収して
// 成功扱いにする。
func TestUnfollowProcessor_AlreadyNotFollowing_Success(t *testing.T) {
	uf := &fakeUnfollower{err: processors.ErrNotFollowing}
	p := processors.NewUnfollowProcessor(uf)
	task := queue.NewUnfollowTask(queue.UnfollowPayload{
		FollowerID: "rA", FolloweeID: "localA",
	})
	assert.NoError(t, p.Handle(context.Background(), task))
}

// upstream core/following.ErrNotFollowing のような「文字列が "not following"」
// な error も吸収できること。
func TestUnfollowProcessor_NotFollowingByMessage_Success(t *testing.T) {
	uf := &fakeUnfollower{err: errors.New("not following")}
	p := processors.NewUnfollowProcessor(uf)
	task := queue.NewUnfollowTask(queue.UnfollowPayload{
		FollowerID: "rA", FolloweeID: "localA",
	})
	assert.NoError(t, p.Handle(context.Background(), task))
}

func TestUnfollowProcessor_GenericError_Retries(t *testing.T) {
	uf := &fakeUnfollower{err: errors.New("network down")}
	p := processors.NewUnfollowProcessor(uf)
	task := queue.NewUnfollowTask(queue.UnfollowPayload{
		FollowerID: "rA", FolloweeID: "localA",
	})
	err := p.Handle(context.Background(), task)
	assert.Error(t, err)
	assert.False(t, errors.Is(err, driver.ErrSkipRetry), "transient error は retry させる")
}

func TestUnfollowProcessor_MissingFields_SkipsRetry(t *testing.T) {
	uf := &fakeUnfollower{}
	p := processors.NewUnfollowProcessor(uf)
	// FollowerID 未指定
	task := queue.NewUnfollowTask(queue.UnfollowPayload{FolloweeID: "localA"})
	err := p.Handle(context.Background(), task)
	require.Error(t, err)
	assert.True(t, errors.Is(err, driver.ErrSkipRetry))
	assert.Empty(t, uf.calls)
}

func TestUnfollowProcessor_NoUnfollower_SkipsRetry(t *testing.T) {
	p := processors.NewUnfollowProcessor(nil)
	task := queue.NewUnfollowTask(queue.UnfollowPayload{
		FollowerID: "rA", FolloweeID: "localA",
	})
	err := p.Handle(context.Background(), task)
	require.Error(t, err)
	assert.True(t, errors.Is(err, driver.ErrSkipRetry))
}

func TestUnfollowProcessor_BadPayload_SkipsRetry(t *testing.T) {
	p := processors.NewUnfollowProcessor(&fakeUnfollower{})
	task := driver.RawTask{TypeName: queue.TaskTypeUnfollow, Body: []byte("not json")}
	err := p.Handle(context.Background(), task)
	require.Error(t, err)
	assert.True(t, errors.Is(err, driver.ErrSkipRetry))
}
