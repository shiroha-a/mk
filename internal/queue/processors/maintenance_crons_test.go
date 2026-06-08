package processors

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/shiroha-a/mk/internal/queue/driver"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- CheckExpiredMutings -----------------------------------------------------

type fakeMutePruner struct {
	called bool
	err    error
}

func (f *fakeMutePruner) DeleteExpired(_ time.Time) (int64, error) {
	f.called = true
	return 3, f.err
}

func TestCheckExpiredMutings_Prunes(t *testing.T) {
	p := &fakeMutePruner{}
	proc := NewCheckExpiredMutingsProcessor(p)
	require.NoError(t, proc.Handle(context.Background(), driver.RawTask{TypeName: "test"}))
	assert.True(t, p.called)
}

func TestCheckExpiredMutings_NilRepoNoOp(t *testing.T) {
	require.NoError(t, NewCheckExpiredMutingsProcessor(nil).Handle(context.Background(), driver.RawTask{TypeName: "test"}))
}

func TestCheckExpiredMutings_ErrorSwallowed(t *testing.T) {
	proc := NewCheckExpiredMutingsProcessor(&fakeMutePruner{err: errors.New("boom")})
	require.NoError(t, proc.Handle(context.Background(), driver.RawTask{TypeName: "test"}), "失敗しても success 扱い")
}

// --- Clean -------------------------------------------------------------------

type fakeUserIPPruner struct {
	gotBefore time.Time
	err       error
}

func (f *fakeUserIPPruner) DeleteOlderThan(t time.Time) (int64, error) {
	f.gotBefore = t
	return 1, f.err
}

type fakeRolePruner struct {
	called bool
	err    error
}

func (f *fakeRolePruner) DeleteExpired(_ time.Time) (int64, error) {
	f.called = true
	return 2, f.err
}

type fakeGamePruner struct {
	gotThreshold string
	err          error
}

func (f *fakeGamePruner) DeleteOutdatedGames(threshold string) (int64, error) {
	f.gotThreshold = threshold
	return 4, f.err
}

type fakeCleanIDGen struct{ got time.Time }

func (f *fakeCleanIDGen) Generate(t time.Time) string { f.got = t; return "threshold-id" }

func TestClean_RunsAllSubtasks(t *testing.T) {
	ip := &fakeUserIPPruner{}
	role := &fakeRolePruner{}
	game := &fakeGamePruner{}
	idGen := &fakeCleanIDGen{}
	proc := NewCleanProcessor(ip, role, game, idGen)
	require.NoError(t, proc.Handle(context.Background(), driver.RawTask{TypeName: "test"}))

	// user_ip は 90 日より前を prune する。
	assert.WithinDuration(t, time.Now().Add(-userIPRetention), ip.gotBefore, time.Minute)
	assert.True(t, role.called)
	// reversi の閾値 id は now-10min から生成される。
	assert.Equal(t, "threshold-id", game.gotThreshold)
	assert.WithinDuration(t, time.Now().Add(-reversiOutdatedAfter), idGen.got, time.Minute)
}

func TestClean_NilDepsNoOp(t *testing.T) {
	require.NoError(t, NewCleanProcessor(nil, nil, nil, nil).Handle(context.Background(), driver.RawTask{TypeName: "test"}))
}

func TestClean_SubtaskErrorsSwallowed(t *testing.T) {
	proc := NewCleanProcessor(
		&fakeUserIPPruner{err: errors.New("a")},
		&fakeRolePruner{err: errors.New("b")},
		&fakeGamePruner{err: errors.New("c")},
		&fakeCleanIDGen{},
	)
	require.NoError(t, proc.Handle(context.Background(), driver.RawTask{TypeName: "test"}), "個々の失敗は swallow して success")
}

// --- CheckModeratorsActivity -------------------------------------------------

type fakeChecker struct {
	called bool
	err    error
}

func (f *fakeChecker) Check() error { f.called = true; return f.err }

func TestCheckModeratorsActivity_Delegates(t *testing.T) {
	c := &fakeChecker{}
	proc := NewCheckModeratorsActivityProcessor(c)
	require.NoError(t, proc.Handle(context.Background(), driver.RawTask{TypeName: "test"}))
	assert.True(t, c.called)
}

func TestCheckModeratorsActivity_NilSvcNoOp(t *testing.T) {
	require.NoError(t, NewCheckModeratorsActivityProcessor(nil).Handle(context.Background(), driver.RawTask{TypeName: "test"}))
}

func TestCheckModeratorsActivity_ErrorSwallowed(t *testing.T) {
	proc := NewCheckModeratorsActivityProcessor(&fakeChecker{err: errors.New("boom")})
	require.NoError(t, proc.Handle(context.Background(), driver.RawTask{TypeName: "test"}))
}
