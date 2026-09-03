package server

import (
	"sync"
	"time"
)

/*
 * peer の受け口に掛けるレート制限 (#2537 の硬化)。
 *
 * **本体の per-endpoint テーブルは効かない。** RateLimiter は
 * `rl.limits[endpoint]` に無いパスを素通しするうえ、仮に載せても
 * `resolveActors` は未認証かつ `enableIPRateLimit=false` (既定) では nil を
 * 返すので、peer のような「Misskey の token を持たない相手」には掛からない。
 *
 * したがって専用に持つ。**プロセス内にしか無い**ので、mk-go を複数プロセスで
 * 動かすと実効的な上限はプロセス数倍になる。それでも「無制限」との差は大きい
 * ので初版はここまでにする。
 */

// peerRateLimit is the sustained rate (requests per second) one key gets.
const peerRateLimit = 6.0

// peerRateBurst is how much one key may spend at once.
//
// リモート利用者のプロフィールを大勢が同時に開くと、相手の 1 インスタンスから
// まとまって届く。**通常の山を落とさない値**にしておかないと、正当な相手の
// 表示が欠ける形で出る。
const peerRateBurst = 60.0

// peerRateMaxKeys bounds the bucket table.
//
// **上限が無いと制限側が資源を食う。** key は相手のホストと IP なので、
// 外から無限に増やせる。
const peerRateMaxKeys = 4096

// peerRateLimiter is a token bucket keyed by remote host or client IP.
type peerRateLimiter struct {
	rate  float64
	burst float64
	max   int
	// now はテストから固定するための時計。nil なら time.Now。
	now func() time.Time

	mu      sync.Mutex
	buckets map[string]*peerRateBucket
}

type peerRateBucket struct {
	tokens float64
	last   time.Time
}

// newPeerRateLimiter returns a limiter with the package defaults.
func newPeerRateLimiter() *peerRateLimiter {
	return &peerRateLimiter{
		rate:    peerRateLimit,
		burst:   peerRateBurst,
		max:     peerRateMaxKeys,
		buckets: map[string]*peerRateBucket{},
	}
}

func (l *peerRateLimiter) clock() time.Time {
	if l.now != nil {
		return l.now()
	}
	return time.Now()
}

// allow spends one token for key, reporting whether it was available.
//
// **nil レシーバは許可する。** テストが組み立てる deps は limiter を持たない
// ので、nil で落ちる形にすると呼び出し側に nil 検査が散る。本番で nil に
// ならないことは配線の gate で担保する。
func (l *peerRateLimiter) allow(key string) bool {
	if l == nil || key == "" {
		return true
	}
	now := l.clock()

	l.mu.Lock()
	defer l.mu.Unlock()

	b, ok := l.buckets[key]
	if !ok {
		l.evictLocked(now)
		b = &peerRateBucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	} else if elapsed := now.Sub(b.last).Seconds(); elapsed > 0 {
		b.tokens += elapsed * l.rate
		if b.tokens > l.burst {
			b.tokens = l.burst
		}
		b.last = now
	}
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// evictLocked makes room for a new key.
//
// **満タンの bucket は捨ててよい。** 次に作り直したものと区別が付かないので、
// 消しても制限は緩まない。それでも溢れるときだけ、最後に触れたのが古い順に
// 落とす。
func (l *peerRateLimiter) evictLocked(now time.Time) {
	if len(l.buckets) < l.max {
		return
	}
	for k, b := range l.buckets {
		if b.tokens+now.Sub(b.last).Seconds()*l.rate >= l.burst {
			delete(l.buckets, k)
		}
	}
	for len(l.buckets) >= l.max {
		var oldestKey string
		var oldest time.Time
		for k, b := range l.buckets {
			if oldestKey == "" || b.last.Before(oldest) {
				oldestKey, oldest = k, b.last
			}
		}
		if oldestKey == "" {
			return
		}
		delete(l.buckets, oldestKey)
	}
}
