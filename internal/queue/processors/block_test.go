package processors_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shiroha-a/mk/internal/core/blocking"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/queue"
	"github.com/shiroha-a/mk/internal/queue/driver"
	"github.com/shiroha-a/mk/internal/queue/processors"
)

type fakeBlocker struct {
	calls [][2]string
	err   error
}

func (f *fakeBlocker) Block(blockerID, blockeeID string) (*model.Blocking, error) {
	f.calls = append(f.calls, [2]string{blockerID, blockeeID})
	if f.err != nil {
		return nil, f.err
	}
	return &model.Blocking{}, nil
}

func TestBlockProcessor_Success(t *testing.T) {
	fb := &fakeBlocker{}
	p := processors.NewBlockProcessor(fb)
	task := queue.NewBlockTask(queue.BlockPayload{BlockerID: "localA", BlockeeID: "rA"})
	require.NoError(t, p.Handle(context.Background(), task))
	require.Len(t, fb.calls, 1)
	assert.Equal(t, [2]string{"localA", "rA"}, fb.calls[0])
}

// 既に block 済は望む終状態なので成功扱い。
func TestBlockProcessor_AlreadyBlocking_Success(t *testing.T) {
	fb := &fakeBlocker{err: blocking.ErrAlreadyBlocking}
	p := processors.NewBlockProcessor(fb)
	task := queue.NewBlockTask(queue.BlockPayload{BlockerID: "a", BlockeeID: "b"})
	assert.NoError(t, p.Handle(context.Background(), task))
}

// 自己 block / 対象不在は retry 不能の恒久エラー。
func TestBlockProcessor_PermanentErrors_SkipRetry(t *testing.T) {
	for _, sentinel := range []error{blocking.ErrSelfBlock, blocking.ErrBlockeeNotFound} {
		fb := &fakeBlocker{err: sentinel}
		p := processors.NewBlockProcessor(fb)
		task := queue.NewBlockTask(queue.BlockPayload{BlockerID: "a", BlockeeID: "b"})
		err := p.Handle(context.Background(), task)
		require.Error(t, err, sentinel.Error())
		assert.True(t, errors.Is(err, driver.SkipRetry), sentinel.Error())
	}
}

func TestBlockProcessor_GenericError_Retries(t *testing.T) {
	fb := &fakeBlocker{err: errors.New("network down")}
	p := processors.NewBlockProcessor(fb)
	task := queue.NewBlockTask(queue.BlockPayload{BlockerID: "a", BlockeeID: "b"})
	err := p.Handle(context.Background(), task)
	assert.Error(t, err)
	assert.False(t, errors.Is(err, driver.SkipRetry), "transient error は retry させる")
}

func TestBlockProcessor_MissingFields_SkipsRetry(t *testing.T) {
	fb := &fakeBlocker{}
	p := processors.NewBlockProcessor(fb)
	task := queue.NewBlockTask(queue.BlockPayload{BlockeeID: "rA"})
	err := p.Handle(context.Background(), task)
	require.Error(t, err)
	assert.True(t, errors.Is(err, driver.SkipRetry))
	assert.Empty(t, fb.calls)
}

func TestBlockProcessor_NoBlocker_SkipsRetry(t *testing.T) {
	p := processors.NewBlockProcessor(nil)
	task := queue.NewBlockTask(queue.BlockPayload{BlockerID: "a", BlockeeID: "b"})
	err := p.Handle(context.Background(), task)
	require.Error(t, err)
	assert.True(t, errors.Is(err, driver.SkipRetry))
}

func TestBlockProcessor_BadPayload_SkipsRetry(t *testing.T) {
	p := processors.NewBlockProcessor(&fakeBlocker{})
	task := driver.RawTask{TypeName: queue.TaskTypeBlock, Body: []byte("not json")}
	err := p.Handle(context.Background(), task)
	require.Error(t, err)
	assert.True(t, errors.Is(err, driver.SkipRetry))
}
