// Package iprelation ranks accounts that shared IP addresses with a target
// account (#3105, 親 #3066).
//
// **出るのは調査の候補であって判定ではない。** 同じ IP を使ったことは同一人物で
// あることを意味しない (家庭・職場・学校・公衆 Wi-Fi・携帯回線の CGNAT・VPN)。
// ここで出す値は**順位を決めるためだけ**に使い、自動判定にも自動処分にも使わない。
package iprelation

import (
	"math"
	"sort"
	"time"
)

// DefaultHalfLife is how long it takes a match to count half as much.
//
// 30 日。**「最近その IP を使ったか」を連続的に効かせるための値**で、これを境に
// 何かが切り替わるわけではない。窓 (`sinceDays`) は「そもそも見るかどうか」で、
// こちらは「見た中での重み」という別の軸。
const DefaultHalfLife = 30 * 24 * time.Hour

// Weight returns 2^(-elapsed/halfLife), clamped to [0, 1].
//
// **未来の観測は 1 にする (経過時間を負にしない)。** 記録は goroutine で走るので
// 観測時刻がわずかに進むことがあり、そのとき 1 を超える重みを付けると「同じ IP を
// 1 つ共有しただけの候補」が「3 つ共有した候補」を追い抜きうる。
//
// halfLife <= 0 は DefaultHalfLife に倒す。0 を割ると NaN になり、
// 並べ替えの比較が全て false になって**並びが入力順のまま**になる (エラーも出ない)。
func Weight(elapsed, halfLife time.Duration) float64 {
	if halfLife <= 0 {
		halfLife = DefaultHalfLife
	}
	if elapsed <= 0 {
		return 1
	}
	return math.Exp2(-float64(elapsed) / float64(halfLife))
}

// SharedIP is one IP both the target and the candidate were seen from.
type SharedIP struct {
	IP string
	// TargetLastSeen / CandidateLastSeen は同じ IP に対する双方の最終観測。
	//
	// **これを「同時に使っていた」とは読まない。** 両方を並べて出すだけで、
	// 一致していなければ画面もそうは書かない (#3105)。
	TargetLastSeen    time.Time
	CandidateLastSeen time.Time
	// IPAccountCount は窓の中でその IP を使ったアカウント数 (対象本人を含む)。
	// 共有回線かどうかを読む側が判断するための材料。
	//
	// **上限まで見えたときは下限でしかない** (正確に数えると走査が有界でなくなる)。
	IPAccountCount             int
	IPAccountCountIsLowerBound bool
	// Weight はこの一致の重み、ElapsedDays は減衰に使った経過日数。`Rank` が埋める。
	Weight      float64
	ElapsedDays float64
}

// Candidate is one account that shared at least one IP with the target.
type Candidate struct {
	UserID string
	// SharedIPs は共有した IP。重みの大きい順。
	SharedIPs []SharedIP
	// Score は重みの単純和。
	//
	// **確率ではない。** 上限は 1 ではなく、共有 IP の数だけ足し上がる
	// (今日使った IP を 3 つ共有していれば 3.0)。パーセントとして出さないこと。
	// 使ってよいのは順位を決めることだけで、値そのものは根拠にならない。
	Score float64
}

// Observation is one (candidate, ip) pair to rank.
type Observation struct {
	UserID                     string
	IP                         string
	TargetLastSeen             time.Time
	CandidateLastSeen          time.Time
	IPAccountCount             int
	IPAccountCountIsLowerBound bool
}

// Rank groups observations per account and orders them by decayed score.
//
// **減衰は「古い側からの経過時間」で決める** (#3105):
//
//	t(p) = max(0, now - min(targetLast(p), candidateLast(p)))
//	w(p) = 2 ^ (-t(p) / halfLife)
//	score = Σ w(p)   (p は重複を除いた共有 IP)
//
// 古い側を採るのは、**一方が最近その IP を使っただけで、もう一方の古い一致まで
// 新しい一致として扱わない**ため。
//
// **同じ IP を 2 回数えない。** 観測回数では加点しないので、同じ IP への
// ログインを繰り返しても順位は上がらない。共有 IP の数は和に既に効いているので、
// さらに掛けて二重に加点しない。
//
// **now は呼び出し側が固定する。** 1 回の検索の中で減衰の基準を揃えないと、
// 同じ検索の中で先に計算した候補ほど不利になる。
func Rank(now time.Time, halfLife time.Duration, observations []Observation) []Candidate {
	byUser := make(map[string]*Candidate, len(observations))
	seen := make(map[string]map[string]struct{}, len(observations))
	for _, o := range observations {
		if o.UserID == "" || o.IP == "" {
			continue
		}
		if ips, ok := seen[o.UserID]; ok {
			if _, dup := ips[o.IP]; dup {
				continue
			}
			ips[o.IP] = struct{}{}
		} else {
			seen[o.UserID] = map[string]struct{}{o.IP: {}}
		}
		c, ok := byUser[o.UserID]
		if !ok {
			c = &Candidate{UserID: o.UserID}
			byUser[o.UserID] = c
		}
		// 古い側からの経過時間で減衰させる。
		older := o.TargetLastSeen
		if o.CandidateLastSeen.Before(older) {
			older = o.CandidateLastSeen
		}
		elapsed := now.Sub(older)
		if elapsed < 0 {
			elapsed = 0
		}
		w := Weight(elapsed, halfLife)
		c.SharedIPs = append(c.SharedIPs, SharedIP{
			IP: o.IP, TargetLastSeen: o.TargetLastSeen, CandidateLastSeen: o.CandidateLastSeen,
			IPAccountCount:             o.IPAccountCount,
			IPAccountCountIsLowerBound: o.IPAccountCountIsLowerBound,
			Weight:                     w,
			ElapsedDays:                elapsed.Hours() / 24,
		})
		c.Score += w
	}

	out := make([]Candidate, 0, len(byUser))
	for _, c := range byUser {
		// 根拠の並びも決定的にする。重みの大きい順 → IP 昇順。
		sort.SliceStable(c.SharedIPs, func(i, j int) bool {
			if c.SharedIPs[i].Weight != c.SharedIPs[j].Weight {
				return c.SharedIPs[i].Weight > c.SharedIPs[j].Weight
			}
			return c.SharedIPs[i].IP < c.SharedIPs[j].IP
		})
		out = append(out, *c)
	}
	// **同点は必ず同じ順に並べる。** 浮動小数の和なので厳密な同点は普通に起きる
	// (同じ時刻の観測など)。決定的でないと、ページを送ったときに同じ候補が
	// 2 回出たり抜けたりする。
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		if len(a.SharedIPs) != len(b.SharedIPs) {
			return len(a.SharedIPs) > len(b.SharedIPs)
		}
		if am, bm := latestShared(a), latestShared(b); !am.Equal(bm) {
			return am.After(bm)
		}
		return a.UserID < b.UserID
	})
	return out
}

// latestShared returns the candidate's most recent observation, zero if none.
//
// **対象側の観測は見ない。** 見ると「対象がその IP を最近使った」という理由だけで
// 候補の順位が上がり、issue が排除している「一方が最近使っただけで古い一致を
// 新しい一致として扱う」形を tiebreak の粒度で再導入してしまう (#3105)。
func latestShared(c Candidate) time.Time {
	var latest time.Time
	for _, p := range c.SharedIPs {
		if p.CandidateLastSeen.After(latest) {
			latest = p.CandidateLastSeen
		}
	}
	return latest
}
