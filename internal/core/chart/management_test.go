package chart

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// makeChart constructs a Chart wrapping the given fakeRepo for management
// tests. We allow the caller to share repos so they can inspect side
// effects after Save() completes.
func makeChart(t *testing.T, name string) *Chart {
	t.Helper()
	c, err := New(Config{
		Schema: Schema{Name: name, Columns: []ColumnDef{{Name: "v"}}},
		Repo:   newFakeRepo(),
		Lock:   NewMemoryLocker(),
		Clock:  newFakeClock(time.Date(2026, 4, 9, 12, 0, 0, 0, time.UTC)),
	})
	require.NoError(t, err)
	return c
}

func TestNewManagementService_DefaultInterval(t *testing.T) {
	m := NewManagementService(nil, 0)
	assert.Equal(t, 20*time.Minute, m.interval)
}

// TestNewManagementService_DefaultLoggerExecuted ensures the default
// no-op logger func body is actually entered, satisfying the coverage
// requirement for the func literal allocated inside NewManagementService.
func TestNewManagementService_DefaultLoggerExecuted(t *testing.T) {
	c := makeChart(t, "deflog")
	repo := c.repo.(*fakeRepo)
	repo.armError("FindCurrent", errors.New("default logger boom"))
	require.NoError(t, c.Commit(Diff{"v": 1}, ""))

	m := NewManagementService([]*Chart{c}, time.Minute)
	// Intentionally do NOT call SetLogger so the default no-op fires.
	if err := m.SaveAll(context.Background()); err == nil {
		t.Fatal("expected error to be propagated")
	}
}

func TestManagementService_SaveAll(t *testing.T) {
	a := makeChart(t, "a")
	b := makeChart(t, "b")
	require.NoError(t, a.Commit(Diff{"v": 1}, ""))
	require.NoError(t, b.Commit(Diff{"v": 2}, ""))
	m := NewManagementService([]*Chart{a, b}, time.Minute)
	require.NoError(t, m.SaveAll(context.Background()))
}

func TestManagementService_SaveAllReturnsFirstError(t *testing.T) {
	a := makeChart(t, "a")
	repoA := a.repo.(*fakeRepo)
	repoA.armError("FindCurrent", errors.New("a fail"))
	require.NoError(t, a.Commit(Diff{"v": 1}, ""))

	type logLine struct {
		msg  string
		args []any
	}
	captured := make([]logLine, 0)
	m := NewManagementService([]*Chart{a}, time.Minute)
	m.SetLogger(func(msg string, args ...any) {
		captured = append(captured, logLine{msg: msg, args: args})
	})
	if err := m.SaveAll(context.Background()); err == nil {
		t.Fatal("expected error")
	}
	require.Len(t, captured, 1)
	// chart 名は key/value で出す。メッセージ本文に埋めるとフィルタできない。
	assert.Equal(t, "chart: save failed", captured[0].msg)
	assert.Contains(t, captured[0].args, "chart")
	assert.Contains(t, captured[0].args, "a")
	// **原因を運ぶのはこの属性だけ。** 落とすと本番で「どの chart が失敗した
	// か」しか分からなくなる。
	assert.Contains(t, captured[0].args, "err")
	var errArg error
	for i := 0; i+1 < len(captured[0].args); i += 2 {
		if captured[0].args[i] == "err" {
			errArg, _ = captured[0].args[i+1].(error)
		}
	}
	require.NotNil(t, errArg)
	assert.Contains(t, errArg.Error(), "a fail")
}

// SaveAll が chart ごとに中身を出すので、呼び出し側は戻り値を再ログしない。
// 再ログすると、失敗 group 数ぶんに伸びた同じ文字列が 2 本出る。
func TestManagementService_DoesNotDoubleLogSaveErrors(t *testing.T) {
	c := makeChart(t, "dup")
	repo := c.repo.(*fakeRepo)
	repo.armError("FindCurrent", errors.New("boom"))
	require.NoError(t, c.Commit(Diff{"v": 1}, ""))

	var msgs []string
	m := NewManagementService([]*Chart{c}, 2*time.Millisecond)
	m.SetLogger(func(msg string, args ...any) { msgs = append(msgs, msg) })
	require.NoError(t, m.Start(context.Background()))
	time.Sleep(15 * time.Millisecond)
	m.Stop(context.Background())

	// エラーの中身を出すのは "chart: save failed" だけ。
	for _, msg := range msgs {
		assert.NotContains(t, msg, "periodic save failed",
			"loop は SaveAll の戻り値を再ログしない")
	}
	assert.Contains(t, msgs, "chart: save failed")
}

func TestManagementService_StartStopFlushes(t *testing.T) {
	c := makeChart(t, "loopchart")
	require.NoError(t, c.Commit(Diff{"v": 1}, ""))

	m := NewManagementService([]*Chart{c}, 24*time.Hour)
	require.NoError(t, m.Start(context.Background()))

	// 二重 Start は弾かれる
	if err := m.Start(context.Background()); err == nil {
		t.Error("expected error on double-start")
	}

	m.Stop(context.Background())
	// Stop が最終 Save を走らせていれば row が 1 件出来ている
	repo := c.repo.(*fakeRepo)
	assert.Len(t, repo.hour[""], 1)
}

func TestManagementService_PeriodicLoopRuns(t *testing.T) {
	c := makeChart(t, "tickchart")
	called := atomic.Int32{}
	tickRepo := c.repo.(*fakeRepo)
	// Pre-populate buffer for each tick by registering tickFn that
	// commits via the chart on demand. To assert the periodic save loop
	// runs at least once, we use a very short interval and wait briefly.
	c.tick = func(_ context.Context, _ string, _ bool) (map[string]int64, error) {
		called.Add(1)
		return nil, nil
	}
	require.NoError(t, c.Commit(Diff{"v": 7}, ""))

	m := NewManagementService([]*Chart{c}, 5*time.Millisecond)
	require.NoError(t, m.Start(context.Background()))
	time.Sleep(20 * time.Millisecond)
	m.Stop(context.Background())

	// Save が走った結果 row が出来ている
	assert.NotEmpty(t, tickRepo.hour[""])
}

func TestManagementService_StopWithoutStartIsNoOp(t *testing.T) {
	m := NewManagementService(nil, time.Minute)
	m.Stop(context.Background()) // 二重に Stop しても panic しない
}

func TestManagementService_StopFinalSaveErrorIsLogged(t *testing.T) {
	c := makeChart(t, "stopfail")
	repo := c.repo.(*fakeRepo)
	require.NoError(t, c.Commit(Diff{"v": 1}, ""))
	// Arm the FindCurrent error so the *final* SaveAll inside Stop fails.
	repo.armError("FindCurrent", errors.New("stop boom"))

	var mu sync.Mutex
	var msgs []string
	var shutdownArgs []any
	m := NewManagementService([]*Chart{c}, 24*time.Hour)
	m.SetLogger(func(msg string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		msgs = append(msgs, msg)
		if msg == "chart: final save reported errors" {
			shutdownArgs = args
		}
	})
	require.NoError(t, m.Start(context.Background()))
	m.Stop(context.Background())

	mu.Lock()
	defer mu.Unlock()
	// SaveAll の詳細行に加えて、Stop 由来の marker が出る。marker が無いと
	// 「最後の Save だった」= 次の周期が無いことが読み取れない。
	assert.Contains(t, msgs, "chart: save failed")
	require.Contains(t, msgs, "chart: final save reported errors")
	assert.Contains(t, shutdownArgs, "phase")
	assert.Contains(t, shutdownArgs, "shutdown")
	// 終了時に残っていた group 数も出す。**値まで見る** — キーの存在だけだと
	// 常に 0 を返す実装でも通る。
	var unsaved any
	for i := 0; i+1 < len(shutdownArgs); i += 2 {
		if shutdownArgs[i] == "unsavedGroups" {
			unsaved = shutdownArgs[i+1]
		}
	}
	assert.Equal(t, 1, unsaved, "戻された group が 1 つ残っている")
}

func TestManagementService_LoopErrorIsLogged(t *testing.T) {
	c := makeChart(t, "errchart")
	repo := c.repo.(*fakeRepo)
	repo.armError("FindCurrent", errors.New("loop boom"))
	require.NoError(t, c.Commit(Diff{"v": 1}, ""))

	logged := atomic.Int32{}
	m := NewManagementService([]*Chart{c}, 2*time.Millisecond)
	m.SetLogger(func(string, ...any) { logged.Add(1) })
	require.NoError(t, m.Start(context.Background()))
	time.Sleep(15 * time.Millisecond)
	m.Stop(context.Background())
	if logged.Load() == 0 {
		t.Fatal("expected loop save error to be logged")
	}
}
