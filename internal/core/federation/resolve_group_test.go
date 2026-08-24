package federation

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 同じ鍵の並行呼び出しを 1 回に畳むこと (singleflight から置き換えても
// collapse の性質は保つ)。
func TestResolveGroup_CollapsesConcurrentCalls(t *testing.T) {
	r := &Resolver{}
	var g resolveGroup
	var calls atomic.Int32

	release := make(chan struct{})
	entered := make(chan struct{})
	var once sync.Once

	// 先頭を 1 本立ててから追従を 3 本ぶら下げる。**「entry がある」ことを
	// 待つのでは足りない** — それは先頭が作った瞬間に成立するので、追従側が
	// begin に着く前に解放してしまい、retire 済みの鍵を拾った goroutine が
	// 2 回目の先頭になって落ちる。待ちの印が立つまで待つこと。
	var wg sync.WaitGroup
	got := make([]any, 4)
	errs := make([]error, 4)
	start := func(i int) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got[i], errs[i] = r.joinResolve(&g, chainWithID(uint64(i+1)), "wk", "K", func() (any, error) {
				calls.Add(1)
				once.Do(func() { close(entered) })
				<-release
				return "v", nil
			})
		}()
	}
	start(0)
	<-entered
	for i := 1; i < len(got); i++ {
		start(i)
		waitUntilBlocked(t, r, uint64(i+1), "wk")
	}
	close(release)
	wg.Wait()

	assert.Equal(t, int32(1), calls.Load(), "先頭 1 回だけ走ること")
	for i := range got {
		assert.NoError(t, errs[i])
		assert.Equal(t, "v", got[i], "追従側も同じ結果を受け取ること")
	}
}

// 先頭が終わったら鍵を retire し、次の呼び出しが新しく走ること。
func TestResolveGroup_RetiresFinishedCall(t *testing.T) {
	r := &Resolver{}
	var g resolveGroup
	var calls atomic.Int32
	run := func() (any, error) {
		calls.Add(1)
		return nil, nil
	}
	_, err := r.joinResolve(&g, chainWithID(1), "", "K", run)
	require.NoError(t, err)
	_, err = r.joinResolve(&g, chainWithID(1), "", "K", run)
	require.NoError(t, err)
	assert.Equal(t, int32(2), calls.Load())
	g.mu.Lock()
	defer g.mu.Unlock()
	assert.Empty(t, g.m, "終わった呼び出しを残さないこと")
}

// 先頭のエラーは追従側にも伝わること。
func TestResolveGroup_SharesError(t *testing.T) {
	r := &Resolver{}
	var g resolveGroup
	boom := errors.New("boom")
	entered := make(chan struct{})

	var followerErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		<-entered
		_, followerErr = r.joinResolve(&g, chainWithID(2), "wk", "K", func() (any, error) {
			t.Error("follower must not run the work")
			return nil, nil
		})
	}()
	_, leaderErr := r.joinResolve(&g, chainWithID(1), "lk", "K", func() (any, error) {
		close(entered)
		waitUntilBlocked(t, r, 2, "wk")
		return nil, boom
	})
	<-done
	assert.ErrorIs(t, leaderErr, boom)
	assert.ErrorIs(t, followerErr, boom)
}

// **待ちには上限がある。** 循環検出はモデル化した待ちしか見ないので、見逃しても
// 永久には止まらないようにする (#2685 review)。
func TestResolveGroup_FollowerGivesUpAfterTimeout(t *testing.T) {
	prev := resolveJoinTimeout
	resolveJoinTimeout = 30 * time.Millisecond
	defer func() { resolveJoinTimeout = prev }()

	r := &Resolver{}
	var g resolveGroup
	release := make(chan struct{})
	entered := make(chan struct{})
	leaderDone := make(chan struct{})
	go func() {
		defer close(leaderDone)
		_, _ = r.joinResolve(&g, chainWithID(1), "", "K", func() (any, error) {
			close(entered)
			<-release
			return nil, nil
		})
	}()
	<-entered

	// **追従側を goroutine に出して有界に待つ。** 直接呼ぶと、上限が効かなく
	// なる回帰でテストがハングし、package ごと go test の timeout を食う
	// (失敗までに 10 分かかると原因も読み取りにくい)。
	followerErr := make(chan error, 1)
	go func() {
		// **waitKey を "" にしないこと。** 空の鍵はグラフに載らない
		// (resolveWaits が無視する) ので、下の「印を残さない」assertion が
		// 最初から成立して何も守らなくなる (#2685 review round 5 M-1)。
		_, err := r.joinResolve(&g, chainWithID(2), "wk", "K", func() (any, error) {
			t.Error("follower must not run the work")
			return nil, nil
		})
		followerErr <- err
	}()
	select {
	case err := <-followerErr:
		assert.ErrorIs(t, err, ErrResolveJoinTimeout)
	case <-time.After(10 * time.Second):
		t.Fatal("追従側が上限で切り上げていない")
	}

	// **自分から降りた側は自分で片付ける。** finish が起こしてくれる相手では
	// ないので、印と登録が残ると死んだチェーン経由で偽の循環が見える。
	r.waits.mu.Lock()
	_, stillWaiting := r.waits.chains[2]
	r.waits.mu.Unlock()
	assert.False(t, stillWaiting, "諦めた側の待ちの印を残さないこと")
	g.mu.Lock()
	leftover := 0
	if c := g.m["K"]; c != nil {
		leftover = len(c.waiters)
	}
	g.mu.Unlock()
	assert.Zero(t, leftover, "諦めた側を待ち行列に残さないこと")

	close(release)
	<-leaderDone
}

// 先頭が panic で巻き戻っても追従側を起こすこと。起こさないと鍵が残り、以降の
// 追従側が上限まで待たされ続ける。
func TestResolveGroup_PanicWakesFollowers(t *testing.T) {
	// 追従側を起こさない回帰が入ったとき、既定の 5 分を待たずに落ちるように
	// 縮めておく (待ちの上限が最後の受け皿になる経路なので)。
	prev := resolveJoinTimeout
	resolveJoinTimeout = 2 * time.Second
	defer func() { resolveJoinTimeout = prev }()

	r := &Resolver{}
	var g resolveGroup
	entered := make(chan struct{})

	var followerErr error
	followerDone := make(chan struct{})
	go func() {
		defer close(followerDone)
		<-entered
		_, followerErr = r.joinResolve(&g, chainWithID(2), "wk", "K", func() (any, error) {
			t.Error("follower must not run the work")
			return nil, nil
		})
	}()

	func() {
		defer func() {
			assert.NotNil(t, recover(), "panic は先頭の呼び出し元へそのまま伝えること")
		}()
		_, _ = r.joinResolve(&g, chainWithID(1), "lk", "K", func() (any, error) {
			close(entered)
			waitUntilBlocked(t, r, 2, "wk")
			panic("boom")
		})
	}()

	<-followerDone
	assert.ErrorIs(t, followerErr, errResolveAborted)
	g.mu.Lock()
	defer g.mu.Unlock()
	assert.Empty(t, g.m, "panic でも鍵を残さないこと")
}

// 待ちが循環するなら参加しないこと。
func TestResolveGroup_RefusesCyclicJoin(t *testing.T) {
	r := &Resolver{}
	var g resolveGroup
	entered := make(chan struct{})
	release := make(chan struct{})
	defer close(release)

	go func() {
		_, _ = r.joinResolve(&g, chainWithID(1), "wk", "K", func() (any, error) {
			close(entered)
			<-release
			return nil, nil
		})
	}()
	<-entered

	// 1 が握っている鍵を 1 自身が待とうとする形。
	_, err := r.joinResolve(&g, chainWithID(1), "wk", "K", func() (any, error) {
		t.Error("must not run the work")
		return nil, nil
	})
	assert.ErrorIs(t, err, ErrResolveWouldDeadlock)
}

// 追従側が諦めても先頭の結果は壊れないこと (諦めた側は待つのをやめるだけ)。
func TestResolveGroup_LeaderUnaffectedByAbandonedFollower(t *testing.T) {
	prev := resolveJoinTimeout
	resolveJoinTimeout = 20 * time.Millisecond
	defer func() { resolveJoinTimeout = prev }()

	r := &Resolver{}
	var g resolveGroup
	entered := make(chan struct{})
	release := make(chan struct{})
	result := make(chan any, 1)
	go func() {
		v, _ := r.joinResolve(&g, chainWithID(1), "", "K", func() (any, error) {
			close(entered)
			<-release
			return "v", nil
		})
		result <- v
	}()
	<-entered
	_, err := r.joinResolve(&g, chainWithID(2), "", "K", func() (any, error) { return nil, nil })
	require.ErrorIs(t, err, ErrResolveJoinTimeout)
	close(release)
	assert.Equal(t, "v", <-result)
}

// waitUntilBlocked blocks until chain id is registered as waiting on key.
func waitUntilBlocked(t *testing.T, r *Resolver, id uint64, key string) {
	t.Helper()
	require.Eventually(t, func() bool {
		r.waits.mu.Lock()
		defer r.waits.mu.Unlock()
		st := r.waits.chains[id]
		return st != nil && st.waitingOn == key
	}, 5*time.Second, time.Millisecond, "chain %d never blocked on %q", id, key)
}

// **後片付けが漏れると偽陽性になる。** 死んだチェーンが待ちの印を残したまま
// グラフに居座ると、そこを経由して実在しない循環が見えるようになり、以後その
// 鍵に触る解決が諦め続ける。相互に鍵を取り合わせたあと、残骸ゼロを固定する
// (#2685 review MEDIUM-4)。
func TestResolveGroup_LeavesNoResidue(t *testing.T) {
	r := &Resolver{}
	var g resolveGroup
	keys := []string{"A", "B", "C", "D"}

	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			outer := keys[i%len(keys)]
			inner := keys[(i+1)%len(keys)]
			c := chainWithID(uint64(i + 1))
			_, _ = r.joinResolve(&g, c, "w:"+outer, outer, func() (any, error) {
				_, _ = r.joinResolve(&g, c, "w:"+inner, inner, func() (any, error) {
					return nil, nil
				})
				return nil, nil
			})
		}(i)
	}
	wg.Wait()

	r.waits.mu.Lock()
	defer r.waits.mu.Unlock()
	assert.Empty(t, r.waits.chains, "生きているチェーンを残さないこと")
	assert.Empty(t, r.waits.holders, "保持者の索引を残さないこと")
	g.mu.Lock()
	defer g.mu.Unlock()
	assert.Empty(t, g.m, "in-flight を残さないこと")
}

// chainWithID builds a chain that carries a fixed tree identity, for tests that
// drive joinResolve directly.
func chainWithID(id uint64) *resolveChain {
	return &resolveChain{id: id}
}

// 木の待ち予算を使い切っていたら、追従側はその場で諦めること。
//
// **上限は join ごとではなく木ごと**なので、深い解決の途中で予算が尽きた時点で
// それ以上待たなくなる (#2685 review HIGH-2)。
func TestResolveGroup_ExhaustedBudgetDoesNotWait(t *testing.T) {
	// 予算が木ごとでなく join ごとに戻る回帰が入ったとき、既定の 5 分を待たずに
	// 落ちるように縮めておく。
	prev := resolveJoinTimeout
	resolveJoinTimeout = 2 * time.Second
	defer func() { resolveJoinTimeout = prev }()

	r := &Resolver{}
	var g resolveGroup
	entered := make(chan struct{})
	release := make(chan struct{})
	leaderDone := make(chan struct{})
	go func() {
		defer close(leaderDone)
		_, _ = r.joinResolve(&g, chainWithID(1), "", "K", func() (any, error) {
			close(entered)
			<-release
			return nil, nil
		})
	}()
	<-entered

	spent := (*resolveChain)(nil).ensureTree()
	spent.chargeWait(resolveJoinTimeout)
	start := time.Now()
	_, err := r.joinResolve(&g, spent, "", "K", func() (any, error) {
		t.Error("follower must not run the work")
		return nil, nil
	})
	assert.ErrorIs(t, err, ErrResolveJoinTimeout)
	assert.Less(t, time.Since(start), time.Second, "予算切れなら待たずに返ること")

	close(release)
	<-leaderDone
}

// 木の外から来た呼び出し (chain なし) も先頭として走れること。
func TestResolveGroup_NilChainRunsAsLeader(t *testing.T) {
	r := &Resolver{}
	var g resolveGroup
	v, err := r.joinResolve(&g, nil, "", "K", func() (any, error) { return "v", nil })
	require.NoError(t, err)
	assert.Equal(t, "v", v)
	r.waits.mu.Lock()
	defer r.waits.mu.Unlock()
	assert.Empty(t, r.waits.chains, "id が無いチェーンはグラフに載せないこと")
}

// **保持を落としてから鍵を retire すること。** 逆にすると、次の先頭が鍵を握った
// 瞬間に「同じ鍵の保持者が 2 つ」見える。古い方はこの後に別の鍵を待ちに行く
// こともあるので、そこを通る実在しない辺で循環を誤検出しうる。
func TestResolveGroup_ReleasesHoldBeforeRetiring(t *testing.T) {
	var g resolveGroup
	call, leader, ok := g.begin("K", 0, func() {}, func() bool { return true })
	require.True(t, ok)
	require.True(t, leader)

	// 追従側を 1 つぶら下げる。
	r := &Resolver{}
	_, follower, ok2 := g.begin("K", 2, func() {}, func() bool { return r.waits.wait(2, "wk") })
	require.True(t, ok2)
	require.False(t, follower)

	called := false
	g.finish("K", call, nil, nil, func(waiters map[uint64]struct{}) {
		called = true
		// beforeRetire は g.mu を保持したまま呼ばれるので、ここで locking
		// すると自分で自分を待つ。直接読むのが正しい。
		assert.Same(t, call, g.m["K"], "retire より前に呼ぶこと")
		assert.Contains(t, waiters, uint64(2), "待っているチェーンを渡すこと")
		r.waits.unwaitAll(waiters, "wk")
	})
	assert.True(t, called, "beforeRetire を呼ぶこと")
	// **起こすのと同じ critical section で印が落ちていること。** 追従側の defer に
	// 任せると、鍵が次の先頭へ渡ってから印が消えるまでの間、第三者が消え残った
	// 印を辿って実在しない循環と判定する (#2685 review round 4)。
	r.waits.mu.Lock()
	_, stillWaiting := r.waits.chains[2]
	r.waits.mu.Unlock()
	assert.False(t, stillWaiting, "retire までに待ちの印を落とすこと")
	g.mu.Lock()
	defer g.mu.Unlock()
	assert.Empty(t, g.m)
}

// 実際に待った時間が木の予算から引かれること。引かないと上限が効かない。
func TestResolveGroup_WaitingConsumesTreeBudget(t *testing.T) {
	prev := resolveJoinTimeout
	resolveJoinTimeout = 200 * time.Millisecond
	defer func() { resolveJoinTimeout = prev }()

	r := &Resolver{}
	var g resolveGroup
	entered := make(chan struct{})
	release := make(chan struct{})
	leaderDone := make(chan struct{})
	go func() {
		defer close(leaderDone)
		_, _ = r.joinResolve(&g, chainWithID(1), "", "K", func() (any, error) {
			close(entered)
			<-release
			return nil, nil
		})
	}()
	<-entered

	chain := (*resolveChain)(nil).ensureTree()
	require.Equal(t, resolveJoinTimeout, chain.waitBudget())
	_, err := r.joinResolve(&g, chain, "", "K", func() (any, error) {
		t.Error("follower must not run the work")
		return nil, nil
	})
	require.ErrorIs(t, err, ErrResolveJoinTimeout)
	assert.LessOrEqual(t, chain.waitBudget(), time.Duration(0),
		"待った時間を予算から引くこと")

	close(release)
	<-leaderDone
}

// 起こす側が追従側の待ちの印を落とすこと。追従側の defer に任せると、鍵が次の
// 先頭へ渡ってから印が消えるまでの窓で第三者が実在しない循環と判定する。
func TestResolveGroup_LeaderClearsFollowerWaitMarks(t *testing.T) {
	r := &Resolver{}
	var g resolveGroup
	entered := make(chan struct{})
	release := make(chan struct{})
	leaderDone := make(chan struct{})
	go func() {
		defer close(leaderDone)
		_, _ = r.joinResolve(&g, chainWithID(1), "wk", "K", func() (any, error) {
			close(entered)
			<-release
			return nil, nil
		})
	}()
	<-entered

	// 追従側を登録するだけで、起こされた後に自分では片付けない (本物の追従側が
	// 起こされてから defer を走らせるまでの状態に相当する)。
	_, follower, ok := g.begin("K", 2, func() {}, func() bool { return r.waits.wait(2, "wk") })
	require.True(t, ok)
	require.False(t, follower)

	close(release)
	<-leaderDone

	r.waits.mu.Lock()
	defer r.waits.mu.Unlock()
	assert.NotContains(t, r.waits.chains, uint64(2),
		"先頭が終わった時点で追従側の待ちの印も落ちていること")
}

// 上限で降りた側の印が残ると、**第三者が実在しない循環で弾かれる**。
// 弾かれた側が引用なら renoteId を落とすので、この PR が直した欠落がそのまま
// 戻る (#2685 review round 5 M-1)。
func TestResolveWaits_StaleWaitMarkWouldFakeACycle(t *testing.T) {
	var w resolveWaits

	// 1 が K の先頭、2 が K を待って降りた (印が残っている状態を作る)。
	w.hold(1, "K")
	require.True(t, w.wait(2, "K"))
	// 2 は別の鍵 M の先頭になり、3 は P の先頭になる。
	w.hold(2, "M")
	w.hold(3, "P")
	// 1 は P を待つ。
	require.True(t, w.wait(1, "P"))

	// ここで 3 が M を待とうとすると、2 に残った K の印を辿って
	// 3 → M → 2 → K → 1 → P → 3 の循環に見える。
	assert.False(t, w.wait(3, "M"), "残骸があると偽の循環になること (前提の確認)")

	// 降りた時点で印を落としていれば、この待ちは通る。
	w.unwait(2)
	assert.True(t, w.wait(3, "M"), "印が落ちていれば弾かれないこと")
}
