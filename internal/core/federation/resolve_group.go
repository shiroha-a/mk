package federation

import (
	"log/slog"
	"sync"
	"time"
)

// resolveGroup collapses concurrent resolves of the same key, in place of
// singleflight.Group, with two differences that this package needs.
//
// このパッケージのコメントで「singleflight の鍵」と書いてあるのは、この group
// の鍵のこと (元は `golang.org/x/sync/singleflight` を使っていた)。
//
// 差分:
//
//   - **追従側の待ちに上限がある。** `singleflight.Do` は待ちを打ち切れないので、
//     先頭が返らない限り追従側は永久に止まる。解決の深さと形は相手方サーバーが
//     渡してくるデータで決まるため、待ちの循環を 1 つ見落とすだけで worker を
//     食い潰される (#2685 review)。上限は解決木ごとの合計で、使い切った追従側は
//     諦めてエラーを返す。
//   - **先頭か追従かを、参加を決めたのと同じロックの中で呼び出し側へ渡す。**
//     wait-for グラフへの登録をこの内側でやらないと「登録する前に相手が判定を
//     通す」窓が空き、そこを通る循環を見逃す (#2685 review HIGH-2)。
//
// 先頭は `singleflight.Do` と同じく**呼び出し元の goroutine で**走らせる。
// `DoChan` 相当にすると fn の panic が singleflight の内部 goroutine へ飛び、
// **意図的に recover 不能な形で**再 panic されるので、ジョブハンドラの recover
// を素通りしてプロセスごと落ちる。
type resolveGroup struct {
	mu sync.Mutex
	m  map[string]*resolveCall
}

// resolveCall is one in-flight resolve. val/err are written by the leader
// before done is closed, so readers must not touch them before it is.
type resolveCall struct {
	done chan struct{}
	val  any
	err  error
	// waiters はこの call にぶら下がっているチェーン。g.mu で守る。
	//
	// **起こすときにまとめて待ちの印を落とすために要る。** 追従側が自分で
	// 落とすと、起こされてから defer が走るまでの間「もう待っていないチェーンが
	// 待っている」ようにグラフが見え、そこを経由した第三者が実在しない循環で
	// 弾かれる (#2685 review round 4)。
	waiters map[uint64]struct{}
}

// begin joins the in-flight call for key, or starts one.
//
// onLead / onJoin are invoked **while the group lock is held**, so the caller
// can publish its role to the wait-for graph without a window in which it is
// neither running nor waiting. onJoin returns false to refuse the wait; then
// ok is false and the caller must neither run nor wait.
func (g *resolveGroup) begin(key string, waiter uint64, onLead func(), onJoin func() bool) (call *resolveCall, leader, ok bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if c, hit := g.m[key]; hit {
		if !onJoin() {
			return nil, false, false
		}
		if c.waiters == nil {
			c.waiters = make(map[uint64]struct{}, 1)
		}
		c.waiters[waiter] = struct{}{}
		return c, false, true
	}
	onLead()
	c := &resolveCall{done: make(chan struct{})}
	if g.m == nil {
		g.m = make(map[string]*resolveCall)
	}
	g.m[key] = c
	return c, true, true
}

// finish publishes the result and retires the call. Leader only, exactly once.
//
// beforeRetire runs **while the group lock is held**, immediately before the
// key becomes claimable again. It receives the chains still waiting on this
// call so their wait marks can be dropped in the same critical section.
//
// **保持の解除をここに閉じ込めてある。** retire の後に解除すると、次の先頭が
// 鍵を握った瞬間に「同じ鍵の保持者が 2 つ」見える。古い方は待ちに入る途中の
// こともあるので、そこを通る実在しない辺で循環を誤検出しうる。ロックの中で
// 順序を固定しておけば、呼び出し側が文の順序を間違えても起きない。
func (g *resolveGroup) finish(key string, call *resolveCall, val any, err error, beforeRetire func(waiters map[uint64]struct{})) {
	g.mu.Lock()
	beforeRetire(call.waiters)
	call.waiters = nil
	// 自分が入れた call だけを外す。**現状この判定が false になる経路は無い**
	// (finish は先頭が 1 度だけ呼び、その間 鍵は自分のもの) ので外しても
	// テストは通る。取り違えると他人の in-flight を消して追従側を宙吊りに
	// するので、届かない防御として残してある。
	if g.m[key] == call {
		delete(g.m, key)
	}
	g.mu.Unlock()
	call.val, call.err = val, err
	close(call.done)
}

// await blocks for the call's result. ok is false when the wait timed out; the
// call itself keeps running.
func (call *resolveCall) await(timeout time.Duration) (val any, err error, ok bool) {
	if timeout <= 0 {
		select {
		case <-call.done:
			return call.val, call.err, true
		default:
			return nil, nil, false
		}
	}
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case <-call.done:
		return call.val, call.err, true
	case <-t.C:
		return nil, nil, false
	}
}

// joinResolve runs fn under g, collapsed by key, with the wait-for graph
// consulted before this chain blocks on someone else's call.
//
// chainID は解決木の識別子 (resolveChain.id)。0 のときは追跡しない — 木の根は
// 何も走らせていないので、誰かがそれを待つことがなく循環にも入らない。
func (r *Resolver) joinResolve(g *resolveGroup, chain *resolveChain, waitKey, key string, fn func() (any, error)) (any, error) {
	return r.joinResolveOpt(g, chain, waitKey, key, true, fn)
}

// joinResolveOpt is joinResolve with the option to refuse to wait at all.
//
// mayWait=false は best-effort な呼び出し側 (featured の取り込み) 用。待ちの辺を
// 張らないので、その呼び出しのせいで**本命の解決**が循環の犠牲に選ばれることも、
// 上限が件数分積み上がることも無い (#2685 review HIGH-2 / MEDIUM-2)。
func (r *Resolver) joinResolveOpt(g *resolveGroup, chain *resolveChain, waitKey, key string, mayWait bool, fn func() (any, error)) (any, error) {
	chainID := chain.treeID()
	refused := ErrResolveWouldDeadlock
	// **best-effort の枝は木ごと待たない。** 相乗りする瞬間だけ待たない形に
	// すると、自分が先頭になったときに内側で待ってしまう (#2685 review round 4)。
	mayWait = mayWait && chain.mayWait()
	call, leader, ok := g.begin(key, chainID,
		func() { r.waits.hold(chainID, waitKey) },
		func() bool {
			if !mayWait {
				refused = ErrResolveWouldBlock
				return false
			}
			return r.waits.wait(chainID, waitKey)
		})
	if !ok {
		// Debug なのは、この経路が**相手方のデータで駆動される**ため。
		// 相互に引用し合う投稿を並べるだけで発生させられるので、Warn だと
		// ログを埋められる。実際に止まったときに出るのは下の Warn の方。
		slog.Debug("federation: not joining an in-flight resolve", "key", key, "reason", refused)
		return nil, refused
	}
	if leader {
		release := func(waiters map[uint64]struct{}) {
			r.waits.release(chainID, waitKey)
			// 起こす相手の印もここで落とす。鍵が次の先頭に渡るのと同じ
			// critical section なので、残骸を見た第三者が誤判定しない。
			r.waits.unwaitAll(waiters, waitKey)
		}
		done := false
		defer func() {
			// panic で巻き戻った場合も追従側を起こす。起こさないと鍵が
			// 残り、以降の追従側が上限まで待ってから諦めるのを繰り返す。
			if !done {
				g.finish(key, call, nil, errResolveAborted, release)
			}
		}()
		val, err := fn()
		done = true
		g.finish(key, call, val, err, release)
		return val, err
	}
	// 上限に達して自分から降りた場合は finish が起こしてくれないので、印と
	// 登録を自分で落とす。
	defer func() {
		r.waits.unwait(chainID)
		g.dropWaiter(call, chainID)
	}()
	// **上限は木ごとの合計。** join ごとに掛けると、1 回の解決が待つ回数だけ
	// 積み上がる (#2685 review HIGH-2)。
	start := time.Now()
	val, err, finished := call.await(chain.waitBudget())
	chain.chargeWait(time.Since(start))
	if !finished {
		// **待った時間を出す。** 上限 (resolveJoinTimeout) を出すと、木の予算が
		// 既に尽きていて 0ms でここへ来た場合でも「5 分待った」と読めてしまう。
		slog.Warn("federation: gave up waiting for an in-flight resolve",
			"key", key, "waited", time.Since(start), "budget", resolveJoinTimeout)
		return nil, ErrResolveJoinTimeout
	}
	return val, err
}

// dropWaiter deregisters a chain that stopped waiting on its own.
func (g *resolveGroup) dropWaiter(call *resolveCall, id uint64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(call.waiters, id)
}
