package reversi

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newMatchAnyService(t *testing.T) *Service {
	t.Helper()
	require.NoError(t, reversiTestRedis.Client.Del(context.Background(), matchAnyKey).Err())
	return &Service{redis: reversiTestRedis.Client}
}

// 待機者が居なければ自分が列に入り、相手は返らない。
func TestEnqueueMatchAny_EnqueuesWhenAlone(t *testing.T) {
	s := newMatchAnyService(t)
	ctx := context.Background()

	res, err := s.EnqueueMatchAny(ctx, "u1", false)
	require.NoError(t, err)
	assert.False(t, res.Matched())

	members, err := reversiTestRedis.Client.ZRange(ctx, matchAnyKey, 0, -1).Result()
	require.NoError(t, err)
	assert.Equal(t, []string{"u1"}, members)

	// key に TTL が付く (切断したクライアントの列が永久に残らない)。
	ttl, err := reversiTestRedis.Client.TTL(ctx, matchAnyKey).Result()
	require.NoError(t, err)
	assert.Greater(t, ttl, time.Duration(0))
}

// 先に待っている相手が居ればマッチし、双方の entry が列から消える。
func TestEnqueueMatchAny_PairsWaitingUser(t *testing.T) {
	s := newMatchAnyService(t)
	ctx := context.Background()

	first, err := s.EnqueueMatchAny(ctx, "u1", false)
	require.NoError(t, err)
	require.False(t, first.Matched())

	second, err := s.EnqueueMatchAny(ctx, "u2", false)
	require.NoError(t, err)
	require.True(t, second.Matched())
	assert.Equal(t, "u1", second.OpponentID)

	card, err := reversiTestRedis.Client.ZCard(ctx, matchAnyKey).Result()
	require.NoError(t, err)
	assert.EqualValues(t, 0, card, "マッチしたら双方が列から消える")
}

// 自分自身とはマッチしない。再呼び出し (heartbeat) しても待機のまま。
func TestEnqueueMatchAny_NeverMatchesSelf(t *testing.T) {
	s := newMatchAnyService(t)
	ctx := context.Background()

	for range 3 {
		res, err := s.EnqueueMatchAny(ctx, "u1", false)
		require.NoError(t, err)
		assert.False(t, res.Matched(), "自分自身とマッチしてはいけない")
	}
	card, err := reversiTestRedis.Client.ZCard(ctx, matchAnyKey).Result()
	require.NoError(t, err)
	assert.EqualValues(t, 1, card, "heartbeat で二重登録しない")
}

// ルール設定を変えて待機し直しても、同じ user が 2 形式で二重に並ばない。
func TestEnqueueMatchAny_NoDuplicateAcrossRuleForms(t *testing.T) {
	s := newMatchAnyService(t)
	ctx := context.Background()

	_, err := s.EnqueueMatchAny(ctx, "u1", false)
	require.NoError(t, err)
	_, err = s.EnqueueMatchAny(ctx, "u1", true)
	require.NoError(t, err)

	members, err := reversiTestRedis.Client.ZRange(ctx, matchAnyKey, 0, -1).Result()
	require.NoError(t, err)
	assert.Equal(t, []string{"u1" + noIrregularRulesSuffix}, members)
}

// どちらかが標準ルールを要求していれば標準ルールになる (upstream と同じ OR)。
func TestEnqueueMatchAny_NoIrregularRulesIsOr(t *testing.T) {
	cases := []struct {
		name           string
		waiterStandard bool
		callerStandard bool
		want           bool
	}{
		{"どちらも不問", false, false, false},
		{"待機側が標準を要求", true, false, true},
		{"呼び出し側が標準を要求", false, true, true},
		{"両方", true, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newMatchAnyService(t)
			ctx := context.Background()
			_, err := s.EnqueueMatchAny(ctx, "waiter", tc.waiterStandard)
			require.NoError(t, err)

			res, err := s.EnqueueMatchAny(ctx, "caller", tc.callerStandard)
			require.NoError(t, err)
			require.True(t, res.Matched())
			assert.Equal(t, tc.want, res.NoIrregularRules)
		})
	}
}

// 切断して古くなった待機 entry は拾わず、その場で掃除する。
//
// key 全体の TTL は誰かが待ち続けると延び続けるため、放置すると「応答しない
// 相手とマッチした」状態になる。
func TestEnqueueMatchAny_SkipsStaleEntry(t *testing.T) {
	s := newMatchAnyService(t)
	ctx := context.Background()

	stale := time.Now().Add(-matchAnyStaleAfter - time.Minute)
	score, err := strconv.ParseFloat(matchAnyScore(stale), 64)
	require.NoError(t, err)
	require.NoError(t, reversiTestRedis.Client.ZAdd(ctx, matchAnyKey,
		redis.Z{Score: score, Member: "ghost"}).Err())

	res, err := s.EnqueueMatchAny(ctx, "u1", false)
	require.NoError(t, err)
	assert.False(t, res.Matched(), "古い待機者とはマッチしない")

	members, err := reversiTestRedis.Client.ZRange(ctx, matchAnyKey, 0, -1).Result()
	require.NoError(t, err)
	assert.NotContains(t, members, "ghost", "古い entry はその場で掃除する")
	assert.Contains(t, members, "u1")
}

// **取り合いの排他。** 同時に来ても、同じ待機者が 2 人に確保されることはない。
// upstream は ZREM の戻り値を見ないのでここが競合する。
//
// 「ペアが 1 組だけできる」ではないことに注意。相手を得られなかった側は待機列に
// 入り、後続がそれを拾うので複数のペアが成立するのが正しい。守るべきなのは
// **1 人の待機者が 2 つの対局に割り当てられないこと**。
func TestEnqueueMatchAny_ConcurrentClaimsGiveOneWinner(t *testing.T) {
	s := newMatchAnyService(t)
	ctx := context.Background()

	_, err := s.EnqueueMatchAny(ctx, "prey", false)
	require.NoError(t, err)

	const contenders = 12
	var (
		mu      sync.Mutex
		winners []string
		wg      sync.WaitGroup
	)
	wg.Add(contenders)
	for i := range contenders {
		go func(n int) {
			defer wg.Done()
			res, err := s.EnqueueMatchAny(ctx, "hunter"+strconv.Itoa(n), false)
			if err == nil && res.Matched() {
				mu.Lock()
				winners = append(winners, res.OpponentID)
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()

	seen := map[string]int{}
	for _, w := range winners {
		seen[w]++
	}
	for id, n := range seen {
		assert.Equalf(t, 1, n, "%q が %d 回確保された (2 つの対局に割り当てられている)", id, n)
	}
	assert.Contains(t, winners, "prey", "最初の待機者は誰かに確保されるはず")

	// 確保された人数 + 列に残った人数 = 参加者総数。取りこぼしも重複も無い。
	remaining, err := reversiTestRedis.Client.ZCard(ctx, matchAnyKey).Result()
	require.NoError(t, err)
	// 確保した側は列に入らないので、総数 = 待機者 1 + hunter 12。
	assert.EqualValues(t, contenders+1, len(winners)*2+int(remaining),
		"確保された人 + 確保した人 + 待機中 = 参加者総数")
}

// CancelMatchAny は 2 形式とも消す。片方だけだと、ルール設定を変えて待機した
// 利用者の古い entry が残り続ける。
func TestCancelMatchAny_RemovesBothForms(t *testing.T) {
	s := newMatchAnyService(t)
	ctx := context.Background()

	require.NoError(t, reversiTestRedis.Client.ZAdd(ctx, matchAnyKey,
		redis.Z{Score: 1, Member: "u1"},
		redis.Z{Score: 2, Member: "u1" + noIrregularRulesSuffix},
		redis.Z{Score: 3, Member: "other"},
	).Err())

	require.NoError(t, s.CancelMatchAny(ctx, "u1"))

	members, err := reversiTestRedis.Client.ZRange(ctx, matchAnyKey, 0, -1).Result()
	require.NoError(t, err)
	assert.Equal(t, []string{"other"}, members, "他人の待機は残す")
}

// redis 未配線 / 空 ID では何もしない (panic しない)。
func TestMatchAny_NilSafe(t *testing.T) {
	ctx := context.Background()
	var s Service

	res, err := s.EnqueueMatchAny(ctx, "u1", false)
	require.NoError(t, err)
	assert.False(t, res.Matched())
	require.NoError(t, s.CancelMatchAny(ctx, "u1"))

	withRedis := newMatchAnyService(t)
	res, err = withRedis.EnqueueMatchAny(ctx, "", false)
	require.NoError(t, err)
	assert.False(t, res.Matched())
	require.NoError(t, withRedis.CancelMatchAny(ctx, ""))
}

// 未知の suffix が付いた member は読み飛ばす (将来 upstream が形式を増やしても
// 誤ってマッチさせない)。
func TestParseMatchAnyMember(t *testing.T) {
	cases := []struct {
		member   string
		wantID   string
		wantRule bool
	}{
		{"u1", "u1", false},
		{"u1" + noIrregularRulesSuffix, "u1", true},
		{"u1:somethingElse", "", false},
		{"", "", false},
	}
	for _, tc := range cases {
		id, rule := parseMatchAnyMember(tc.member)
		assert.Equalf(t, tc.wantID, id, "parseMatchAnyMember(%q)", tc.member)
		assert.Equalf(t, tc.wantRule, rule, "parseMatchAnyMember(%q)", tc.member)
	}
}
