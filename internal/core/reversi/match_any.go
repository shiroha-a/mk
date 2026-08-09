package reversi

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// matchAnyKey is the ZSET holding users waiting for a random opponent.
// upstream ReversiService と同じキー名にしてある (drop-in で TS backend に
// 切り替えても待機列がそのまま引き継がれる)。
const matchAnyKey = "reversi:matchAny"

// noIrregularRulesSuffix marks a waiting entry that requested standard rules.
// upstream は `<userId>` か `<userId>:noIrregularRules` の 2 形式を同じ ZSET に
// 混在させる。member 文字列がそのまま識別子なので、両方消さないと待機列に
// 残骸が残る。
const noIrregularRulesSuffix = ":noIrregularRules"

// matchAnyTTL は待機列自体の寿命。upstream は `EXPIRE ... 15 NX` で 15 秒。
// frontend の matchHeatbeat が 10 秒間隔で再呼び出しするため、生きている
// クライアントは毎回 ZADD で score を更新し続ける。切断したクライアントの
// entry は次の EXPIRE 更新までに落ちる。
const matchAnyTTL = 15 * time.Second

// matchAnyStaleAfter は個々の待機 entry を古いとみなす閾値。
//
// key 全体の TTL (15 秒) は誰か 1 人でも待機し続けると延び続けるため、
// **切断したクライアントの entry だけが取り残される**。upstream は
// ZRANGE の上位 3 件しか見ないので古い entry が上位に来ることは稀だが、
// 待機者が少ないと実際に拾ってしまい「応答しない相手とマッチした」状態に
// なる。mk-go では score (= 最終 heartbeat 時刻) を見て弾く。
const matchAnyStaleAfter = 30 * time.Second

// MatchAnyResult reports what EnqueueMatchAny decided.
type MatchAnyResult struct {
	// OpponentID is the user paired with the caller. 空なら待機列に入った。
	OpponentID string
	// NoIrregularRules is the effective rule setting for the new game:
	// どちらかが標準ルールを要求していれば true (upstream と同じ OR)。
	NoIrregularRules bool
}

// Matched reports whether an opponent was found.
func (r MatchAnyResult) Matched() bool { return r.OpponentID != "" }

// EnqueueMatchAny pairs the caller with a waiting user, or enqueues them.
//
// upstream `ReversiService.matchAnyUser` の待機列部分に対応する (#2407)。
// 呼び出し側 (handler) が先に「既存の未開始 game」「自分宛ての招待」を調べ、
// どちらも無いときにここへ来る。
//
// # upstream との違い: 取り合いを防ぐ
//
// upstream は ZRANGE → filter → ZREM → matched の順で、**ZREM の戻り値を見て
// いない**。同じ待機者を 2 人が同時に見つけると両方が対局を作り、待機者は 2 つの
// ゲームに割り当てられる。
//
// mk-go は ZREM の戻り値を**要求の証**として使う。1 が返った呼び出しだけが対局を
// 作り、0 なら次の候補へ進む。Lua を持ち込まずに取り合いを解消できる。
func (s *Service) EnqueueMatchAny(ctx context.Context, meID string, noIrregularRules bool) (MatchAnyResult, error) {
	if s.redis == nil || meID == "" {
		return MatchAnyResult{}, nil
	}
	now := time.Now()

	// 上位から候補を見る。自分の entry は 2 形式ありうるので、上限は
	// upstream の 3 件より広めに取る (自分 2 件 + 候補が埋まる余地)。
	entries, err := s.redis.ZRevRangeWithScores(ctx, matchAnyKey, 0, 9).Result()
	if err != nil && err != redis.Nil {
		return MatchAnyResult{}, err
	}
	for _, e := range entries {
		member, _ := e.Member.(string)
		id, wantsStandard := parseMatchAnyMember(member)
		if id == "" || id == meID {
			continue
		}
		// 切断した待機者を拾わない。key の TTL は誰かが待ち続けると延びるので、
		// entry 単位の鮮度を score で判定する。
		if now.Sub(time.UnixMilli(int64(e.Score))) > matchAnyStaleAfter {
			// 古い entry はこの機会に掃除する。失敗しても次の呼び出しで再試行
			// されるだけなので戻り値は見ない。
			_ = s.redis.ZRem(ctx, matchAnyKey, member).Err()
			continue
		}
		// **ここが取り合いの解決点。** 1 が返った呼び出しだけが相手を確保する。
		removed, rerr := s.redis.ZRem(ctx, matchAnyKey, member).Result()
		if rerr != nil || removed == 0 {
			continue
		}
		// 相手を確保できたので自分の待機 entry も片付ける (2 形式とも)。
		_ = s.redis.ZRem(ctx, matchAnyKey, meID, meID+noIrregularRulesSuffix).Err()
		return MatchAnyResult{
			OpponentID: id,
			// upstream と同じ OR。どちらかが標準ルールを要求していれば標準。
			NoIrregularRules: noIrregularRules || wantsStandard,
		}, nil
	}

	// 相手が居ないので待機列に入る。score は最終 heartbeat 時刻。
	member := meID
	if noIrregularRules {
		member = meID + noIrregularRulesSuffix
	}
	// 同じ user が両形式で二重に並ばないよう、もう片方を消してから入れる。
	other := meID
	if !noIrregularRules {
		other = meID + noIrregularRulesSuffix
	}
	pipe := s.redis.Pipeline()
	pipe.ZRem(ctx, matchAnyKey, other)
	pipe.ZAdd(ctx, matchAnyKey, redis.Z{Score: float64(now.UnixMilli()), Member: member})
	// upstream と同じ `NX` 付き。既に TTL があれば延ばさない。
	pipe.Expire(ctx, matchAnyKey, matchAnyTTL)
	if _, err := pipe.Exec(ctx); err != nil {
		return MatchAnyResult{}, err
	}
	return MatchAnyResult{}, nil
}

// CancelMatchAny removes the caller from the waiting queue.
//
// upstream `matchAnyUserCancel` と同じく両形式を消す。片方だけ消すと、
// ルール設定を変えて再度待機した利用者の古い entry が残り続ける。
func (s *Service) CancelMatchAny(ctx context.Context, meID string) error {
	if s.redis == nil || meID == "" {
		return nil
	}
	return s.redis.ZRem(ctx, matchAnyKey, meID, meID+noIrregularRulesSuffix).Err()
}

// parseMatchAnyMember splits a waiting entry into its user id and rule flag.
// 未知の suffix が付いた member は識別できないので空 id を返して読み飛ばす
// (将来 upstream が形式を増やしても誤ってマッチさせない)。
func parseMatchAnyMember(member string) (userID string, noIrregularRules bool) {
	if member == "" {
		return "", false
	}
	if strings.HasSuffix(member, noIrregularRulesSuffix) {
		return strings.TrimSuffix(member, noIrregularRulesSuffix), true
	}
	if strings.Contains(member, ":") {
		return "", false
	}
	return member, false
}

// matchAnyScore renders a heartbeat timestamp the way upstream stores it
// (milliseconds since epoch). テストから待機 entry を仕込むのに使う。
func matchAnyScore(t time.Time) string {
	return strconv.FormatInt(t.UnixMilli(), 10)
}
