package mediaproxy

import (
	"context"
	"errors"
	"runtime"
	"time"

	"golang.org/x/sync/semaphore"
)

// ErrOverloaded reports that the image pipeline was saturated for longer than
// cpuAcquireTimeout, so the request was shed instead of queued forever.
//
// **`ErrTooLarge` 等と違い、利用者の入力は正しい。** 再試行すれば通るので
// handler は 503 + Retry-After に倒す。
var ErrOverloaded = errors.New("mediaproxy: image pipeline is saturated")

// cpuAcquireTimeout は画像処理の枠が空くのを待つ上限。
//
// 値の根拠: 最も重い経路 (AVIF 入力) は 8 core・枠 4 の実測で p50 819ms /
// 9.1 rps なので、5 秒あれば 45 枚前後を消化できる。タイムライン 1 画面分の
// アバターが同時に来ても取りこぼさない。これより短いと、枠が守ろうとして
// いるまさにその負荷で正当な要求を落とす — #2849 の argon2 枠の実測が
// そのまま当てはまる (`internal/misc/password/verify.go` に「1 秒だと
// 同時 50 で 14 件、100 で 58 件が失敗」と残っている。あちらも 1 秒は
// 候補として測っただけで、採用値は 3 秒)。
//
// **これは待ち時間の上限であって、待ち行列の長さの上限ではない。**
// acquire は**ダウンロード完了後**に行われる (`fetchRemote` が
// `io.ReadAll` してから `processAndReturn` を呼ぶ) ので、待っている
// リクエストは入力バイト列 (最大 `maxDownload` = 32MiB) を抱えたまま
// 積み上がる。到着率が排出率を大きく超える open-loop の状況では、
// 枠で抑えたピークが行列側に戻る。**行列そのものに上限を置くのは
// このコミットの範囲外**で、現状の上限は「5 秒 × 到着率」にしかならない。
//
// **const ではなく var。** in-package のテストが枯渇を短い待ちで踏めるように
// するため (export しないので公開面は変わらない)。値は
// TestCPUAcquireTimeout が固定する。
var cpuAcquireTimeout = 5 * time.Second

// defaultCPUConcurrency returns the default number of image pipeline runs
// allowed to run at the same time.
//
// **この枠が抑えているのはピークメモリで、レイテンシではない (#3032)。**
// 同時に走るデコードの本数がそのまま「同時に確保される中間バッファの本数」
// になる。8 core で AVIF を c=8 流し込んだときの peak RSS の実測:
//
//	枠 1 → 131MB / 2 → 409MB / 4 → 636MB / 8 → 995MB / 無制限 → 959MB
//
// 同条件の jpg→avatar スループット (c=8) は枠 1 → 30.7 rps / 2 → 45.5 /
// 4 → 63.9 / 8 → 71.2 / 無制限 → 71.6。GOMAXPROCS の半分は
// **peak RSS -34% をスループット -11% で買う**点にあたる。2GB VPS 構成
// (定常 343MiB) で AVIF のバーストに耐えるにはこの上限が要る。
//
// **この実測は closed-loop (c=8 固定) なので、抑えられているのは
// 「同時にデコードしている分」だけ。** 待ち行列側は別で、そちらに上限は
// 無い (cpuAcquireTimeout の説明を参照)。到着率が排出率を大きく超える
// 状況の peak をこの数字で見積もらないこと。
//
// **GOMAXPROCS 全部にしてはいけない** — 上の表のとおり無制限と区別が付かず、
// 枠を持つ意味が無い。逆に 1 まで絞るとスループットが半分以下になる。
//
// **テールレイテンシはこの枠では直らない。** 同居する軽いリクエストの p99 は
// 枠を 1 にしても 155ms のままで、無制限 (168ms) と誤差の範囲だった。原因は
// P の奪い合いではなく **GC assist** で、裾はリクエスト自身の割り当て量に
// 比例する (343KB の pass-through で p99 164ms、163B で 6.3ms)。GOGC を
// 上げると消える (800 で p99 2.5ms) が、peak RSS が 1-3.1GB に膨らむので
// 採れない。根治にはデコードのバッファを Go ヒープの外に出すしかなく、
// それはコーデックの動的リンク化 (docs/mediaproxy-govips-evaluation.md) の
// 話になる。
func defaultCPUConcurrency() int {
	if n := runtime.GOMAXPROCS(0) / 2; n > 0 {
		return n
	}
	return 1
}

// SetCPUConcurrency caps how many image pipeline runs may run at once.
// n <= 0 restores the default (GOMAXPROCS / 2, at least 1).
//
// 設定キーは `mediaProxyConcurrency` (config)。メモリの少ないホストで
// さらに絞りたい / 逆に緩めたいときのために露出している。
//
// **起動時専用。goroutine-safe ではない。** `s.cpuSlots` を差し替えるので、
// リクエストを捌いている最中に呼ぶと (a) `acquireCPU` の読みとの間で
// データ競合になり、(b) 旧枠の保持者 n 本 + 新枠の容量 n 本で一瞬 2n 本が
// 同時にデコードする — つまり**この枠が守っている不変条件そのものが
// 破れる**。production の呼び出しは `NewService` (構築中) と `router.go`
// (listen 前) の 2 箇所で、どちらも Service が共有される前
// (数え方: 非テストの Go で `SetCPUConcurrency(` を grep)。
func (s *Service) SetCPUConcurrency(n int) {
	if n <= 0 {
		n = defaultCPUConcurrency()
	}
	s.cpuConcurrency = n
	s.cpuSlots = semaphore.NewWeighted(int64(n))
}

// acquireCPU takes one image pipeline slot, returning a release func.
//
// **囲むのは decode/resize/encode だけ。** リモート fetch や video thumbnail
// generator への HTTP 呼び出しを枠の内側に入れると、中間バッファを 1 つも
// 抱えていない待ち時間で枠を占有してしまい、同じ枠数でスループットだけが
// 落ちる。
func (s *Service) acquireCPU(parent context.Context) (func(), error) {
	// **取った semaphore をクロージャに捕まえる。** `SetCPUConcurrency` は
	// `s.cpuSlots` を**差し替える**ので、解放時にフィールドを読み直すと
	// acquire したのと別の Weighted へ Release が入り
	// `semaphore: released more than held` で **panic してプロセスが落ちる**。
	// `SetCPUConcurrency` は起動時専用と決めてあるが、破ったときの代償が
	// クラッシュなので、こちら側でも受けておく。
	slots := s.cpuSlots
	// **失敗時も no-op func を返す。** nil を返すと
	// `release, err := s.acquireCPU(ctx); defer release()` と書かれた
	// ときに nil panic になる。
	noop := func() {}
	if slots == nil {
		// 枠が張られていない = 無制限。`NewService` は必ず張るので
		// (TestNewServiceInstallsCPULimit が固定)、ここに来るのは
		// `&Service{}` をパッケージ内で直に組むテストだけ。
		return noop, nil
	}
	ctx, cancel := context.WithTimeout(parent, cpuAcquireTimeout)
	defer cancel()
	if err := slots.Acquire(ctx, 1); err != nil {
		// **呼び出し元の cancel と枠の枯渇を区別する。** 前者は利用者が
		// 接続を切っただけで過負荷ではないので、503 に倒すと監視上
		// 「サーバーが落ちている」と読めてしまう。
		if parent.Err() != nil {
			return noop, parent.Err()
		}
		return noop, ErrOverloaded
	}
	var released bool
	return func() {
		// Release は 1 回だけ。二重に返すと semaphore が panic する。
		if released {
			return
		}
		released = true
		slots.Release(1)
	}, nil
}
