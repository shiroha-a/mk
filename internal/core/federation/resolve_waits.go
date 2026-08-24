package federation

import "sync"

// 鍵の名前空間。**note と actor の 2 つの group を 1 つのグラフに載せる**ので、
// 同じ URI が両方に現れても別物として扱う必要がある。
//
// **actor 側も載せる理由** (#2685 review HIGH-1)。actor の解決は他の actor を
// 待つ — `processRemoteMove` が移行先を解決するので、**互いを movedTo に指す
// 2 つの actor** を 2 worker が同時に取得すると actor どうしで循環する。note の
// 解決も著者解決で actor を待つので、待ちの辺は 2 つの group をまたぐ。片方だけ
// モデル化すると、もう片方で待っているチェーンが「走っている」と見えて循環を
// 見逃す。
//
// note → actor → note の循環は、featured の取り込みが待たなくなった
// (`resolveNoteBestEffort`) ので現状は作れない。**それに依存して actor 側を
// 外さないこと** — 待つ経路が 1 つ増えるたびに成立するようになる。
const (
	waitKeyNote  = "n\x00"
	waitKeyActor = "a\x00"
)

// noteWaitKey / actorWaitKey namespace a group key for the wait graph. 空の鍵は
// 空のまま返す — 同一性を持たないものをグラフに載せると、無関係な呼び出しが
// 1 つの節点に集まってしまう (resolveWaits 側が "" を無視する)。
func noteWaitKey(key string) string {
	if key == "" {
		return ""
	}
	return waitKeyNote + key
}

func actorWaitKey(key string) string {
	if key == "" {
		return ""
	}
	return waitKeyActor + key
}

// resolveWaits is the wait-for graph over live resolve chains: which keys each
// chain is currently running, and which key it is blocked on. A chain consults
// it before waiting on another chain's in-flight resolve, and refuses to wait
// when doing so would close a cycle.
//
// **チェーンローカルの判定だけでは cross-goroutine のデッドロックを防げない。**
// プロセス全体の台帳だった頃は「他の goroutine が握っていたら諦める」ことで
// **意図せず**それも防いでいた。チェーンに閉じて「待つ」ようにすると、相互に
// 引用し合う 2 つの投稿を 2 worker が同時に解決したときに待ちが循環する。
//
// ここでは待つ直前にグラフを辿り、**循環になる場合だけ**諦める。循環でない
// 待ちは従来どおり待って引けるので、#2685 が直した renoteId 欠落は戻らない。
// 見落としに備えて待ち自体にも上限がある (resolveJoinTimeout)。
//
// **前提: 1 つのチェーン id を同時に触る goroutine は 1 つ。** 先頭は
// resolveGroup が呼び出し元の goroutine でそのまま走らせ、追従側は結果が出るか
// 上限に達するまでブロックするので、チェーン id は実質その goroutine の識別子に
// なる。だから「あるチェーンが待っている鍵」を 1 つの field で持てる。
type resolveWaits struct {
	mu sync.Mutex
	// chains は生きているチェーンの状態。
	chains map[uint64]*chainWait
	// holders は鍵 → それを走らせているチェーン。グラフを辿るときに毎回
	// 全チェーンを走査しないための索引 (#2685 review MEDIUM-3)。
	holders map[string]map[uint64]struct{}
}

// chainWait is one live chain: the keys it is running, and the key it is
// blocked on ("" when it is not blocked).
//
// **「走らせている鍵」と「待っている鍵」を分ける。** 待ち側を保持者に混ぜると
// 「同じ鍵を待っている者どうしが互いに依存している」という実在しない辺ができ、
// 循環を誤検出する。追従側の完了を決めるのは先頭だけ。
type chainWait struct {
	running   map[string]struct{}
	waitingOn string
}

// hold registers that chain id is running key (it is the group leader).
func (w *resolveWaits) hold(id uint64, key string) {
	if id == 0 || key == "" {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	st := w.chainLocked(id)
	st.running[key] = struct{}{}
	if w.holders == nil {
		w.holders = make(map[string]map[uint64]struct{})
	}
	hs := w.holders[key]
	if hs == nil {
		hs = make(map[uint64]struct{}, 1)
		w.holders[key] = hs
	}
	hs[id] = struct{}{}
}

// release undoes hold.
func (w *resolveWaits) release(id uint64, key string) {
	if id == 0 || key == "" {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if st := w.chains[id]; st != nil {
		delete(st.running, key)
		w.gcLocked(id, st)
	}
	if hs := w.holders[key]; hs != nil {
		delete(hs, id)
		if len(hs) == 0 {
			delete(w.holders, key)
		}
	}
}

// wait registers that chain id is about to block on key, and reports whether
// it may. false のときは**待ってはいけない** — 待つと循環する。
//
// **判定と登録を同じロックの中で行う。** 分けると、相互に待つ 2 つのチェーンが
// 互いの登録前に判定を通してしまい、両方が待ちに入る。
func (w *resolveWaits) wait(id uint64, key string) bool {
	if id == 0 || key == "" {
		return true
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.reachesLocked(key, id) {
		return false
	}
	w.chainLocked(id).waitingOn = key
	return true
}

// unwait clears the wait mark. チェーンが同時に待てる鍵は 1 つなので鍵は要らない。
func (w *resolveWaits) unwait(id uint64) {
	if id == 0 {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if st := w.chains[id]; st != nil {
		st.waitingOn = ""
		w.gcLocked(id, st)
	}
}

// unwaitAll clears the wait marks of chains that were blocked on key.
//
// **起こす側がまとめて落とす。** 追従側の defer に任せると、起こされてから
// defer が走るまでの間だけ「もう待っていないチェーンが待っている」ように見え、
// そこを経由した第三者が実在しない循環で弾かれる (#2685 review round 4)。
// 弾かれた側は既存行に落ちるので、引用なら renoteId を落とす。
//
// key を照合するのは、上限で自分から降りて**別の鍵を待ち直した**チェーンの
// 新しい印を消さないため。
func (w *resolveWaits) unwaitAll(ids map[uint64]struct{}, key string) {
	if len(ids) == 0 {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	for id := range ids {
		st := w.chains[id]
		if st == nil || st.waitingOn != key {
			continue
		}
		st.waitingOn = ""
		w.gcLocked(id, st)
	}
}

// chainLocked returns (creating if needed) the state for id. Caller holds w.mu.
func (w *resolveWaits) chainLocked(id uint64) *chainWait {
	if w.chains == nil {
		w.chains = make(map[uint64]*chainWait)
	}
	st := w.chains[id]
	if st == nil {
		st = &chainWait{running: make(map[string]struct{}, 2)}
		w.chains[id] = st
	}
	return st
}

// gcLocked drops a chain that is neither running nor waiting. Caller holds w.mu.
func (w *resolveWaits) gcLocked(id uint64, st *chainWait) {
	if len(st.running) == 0 && st.waitingOn == "" {
		delete(w.chains, id)
	}
}

// reachesLocked reports whether the chain running key is, transitively, blocked
// on a key that chain `me` is running — i.e. whether waiting on key would close
// a cycle. Caller must hold w.mu.
//
// 辿るのは「その鍵を走らせているチェーン」→「そのチェーンが待っている鍵」→ …。
// me 自身が走らせている鍵を待とうとした場合 (同一チェーンの自己再入) も、
// 最初の 1 歩で me に当たるのでここで捕まる。生きているチェーンは同時実行の
// worker 数程度なので、素朴な BFS で足りる。
func (w *resolveWaits) reachesLocked(key string, me uint64) bool {
	seen := make(map[uint64]bool, 4)
	queue := w.holdersLocked(key, nil)
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if id == me {
			return true
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		st := w.chains[id]
		if st == nil || st.waitingOn == "" {
			continue
		}
		queue = w.holdersLocked(st.waitingOn, queue)
	}
	return false
}

// holdersLocked appends the chains running key to out.
func (w *resolveWaits) holdersLocked(key string, out []uint64) []uint64 {
	for id := range w.holders[key] {
		out = append(out, id)
	}
	return out
}
