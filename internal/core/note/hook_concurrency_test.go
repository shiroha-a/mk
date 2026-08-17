package note

import (
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// withHookConcurrency sets the limit for one test and restores it afterwards.
func withHookConcurrency(t *testing.T, n int) {
	t.Helper()
	SetHookConcurrency(n)
	t.Cleanup(func() { SetHookConcurrency(0) })
}

// 既定は GOMAXPROCS 連動。**固定値にすると、コア数の違うホストで意味が変わる。**
func TestSetHookConcurrency_Default(t *testing.T) {
	withHookConcurrency(t, 0)
	want := runtime.GOMAXPROCS(0) * defaultHookConcurrencyFactor
	if got := cap(hookSem(hookFanout)); got != want {
		t.Fatalf("fanout の枠 %d, want %d", got, want)
	}
	if got := cap(hookSem(hookOther)); got != want {
		t.Fatalf("other の枠 %d, want %d", got, want)
	}
}

// **0 枠を作らせない。** 容量 0 の channel だと送信が永久にブロックし、
// フックが 1 本も走らなくなる。
func TestSetHookConcurrency_NeverZero(t *testing.T) {
	for _, n := range []int{-1, 0} {
		SetHookConcurrency(n)
		if cap(hookSem(hookFanout)) < 1 {
			t.Fatalf("n=%d で枠が %d になった", n, cap(hookSem(hookFanout)))
		}
	}
	SetHookConcurrency(0)
}

// 同時に走る本数が上限を超えないこと。**これが効いていないと、絞りを入れた
// 意味が無い。**
func TestSafeGoKind_BoundsConcurrency(t *testing.T) {
	const limit = 3
	withHookConcurrency(t, limit)

	var running, peak int64
	var wg sync.WaitGroup
	release := make(chan struct{})

	for range 30 {
		wg.Add(1)
		safeGoKind(hookFanout, func() {
			defer wg.Done()
			cur := atomic.AddInt64(&running, 1)
			for {
				old := atomic.LoadInt64(&peak)
				if cur <= old || atomic.CompareAndSwapInt64(&peak, old, cur) {
					break
				}
			}
			<-release
			atomic.AddInt64(&running, -1)
		})
	}
	// 上限まで詰まるのを待ってから解放する。
	deadline := time.After(5 * time.Second)
	for atomic.LoadInt64(&running) < limit {
		select {
		case <-deadline:
			t.Fatalf("上限まで走らない (running=%d)", atomic.LoadInt64(&running))
		default:
			runtime.Gosched()
		}
	}
	close(release)
	wg.Wait()

	if got := atomic.LoadInt64(&peak); got > limit {
		t.Fatalf("同時実行が %d まで伸びた, 上限 %d", got, limit)
	}
}

// **fanout の枠が他のフックに奪われないこと。** 1 つの semaphore を共有すると、
// 他のフックが枠を握っている間 fanout が動けず、投稿がタイムラインに出るまでの
// 時間が壊れる (実測で中央値 5.7 秒 / 25 回中 20 回が 15 秒以内に届かず)。
func TestSafeGoKind_FanoutNotStarvedByOtherHooks(t *testing.T) {
	withHookConcurrency(t, 2)

	blocked := make(chan struct{})
	occupied := make(chan struct{}, 2)
	// other の枠を上限まで占有する。
	for range 2 {
		safeGoKind(hookOther, func() {
			occupied <- struct{}{}
			<-blocked
		})
	}
	for range 2 {
		select {
		case <-occupied:
		case <-time.After(5 * time.Second):
			t.Fatal("other のフックが起動しない")
		}
	}

	// other が全枠を握っていても fanout は走れること。
	ran := make(chan struct{})
	safeGoKind(hookFanout, func() { close(ran) })
	select {
	case <-ran:
	case <-time.After(5 * time.Second):
		close(blocked)
		t.Fatal("other に枠を奪われて fanout が走らない")
	}
	close(blocked)
}

// panic を拾っても枠を返すこと。**返さないと 1 回の panic で枠が 1 つ減り、
// 積み重なると fanout が止まる。**
func TestSafeGoKind_ReleasesSlotOnPanic(t *testing.T) {
	withHookConcurrency(t, 1)

	done := make(chan struct{})
	safeGoKind(hookFanout, func() { defer close(done); panic("boom") })
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("最初のフックが走らない")
	}

	// 枠が返っていれば次が走る。
	ran := make(chan struct{})
	safeGoKind(hookFanout, func() { close(ran) })
	select {
	case <-ran:
	case <-time.After(5 * time.Second):
		t.Fatal("panic で枠が返っていない")
	}
}

// safeGo は other 扱い (既存の呼び出し側の互換)。
func TestSafeGo_UsesOtherKind(t *testing.T) {
	withHookConcurrency(t, 1)

	blocked := make(chan struct{})
	started := make(chan struct{})
	safeGo(func() { close(started); <-blocked })
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("safeGo が走らない")
	}

	// other が埋まっていても fanout は独立して走れる。
	ran := make(chan struct{})
	safeGoKind(hookFanout, func() { close(ran) })
	select {
	case <-ran:
	case <-time.After(5 * time.Second):
		close(blocked)
		t.Fatal("safeGo が fanout の枠を使っている")
	}
	close(blocked)
}
