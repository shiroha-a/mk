package processors

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/shiroha-a/mk/internal/core/iplog"
	"github.com/shiroha-a/mk/internal/core/signup"
	"github.com/shiroha-a/mk/internal/queue/driver"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- CheckExpiredMutings -----------------------------------------------------

type fakeMutePruner struct {
	called bool
	owners []string
	err    error
}

func (f *fakeMutePruner) DeleteExpired(_ time.Time) ([]string, error) {
	f.called = true
	if f.err != nil {
		return nil, f.err
	}
	if f.owners != nil {
		return f.owners, nil
	}
	return []string{"u1", "u2", "u3"}, nil
}

type fakeRelationReloadPublisher struct {
	published []string
}

func (f *fakeRelationReloadPublisher) PublishMuteBlockReload(userID string) {
	f.published = append(f.published, userID)
}

func TestCheckExpiredMutings_Prunes(t *testing.T) {
	user := &fakeMutePruner{}
	channel := &fakeMutePruner{}
	proc := NewCheckExpiredMutingsProcessor(user, channel)
	require.NoError(t, proc.Handle(context.Background(), driver.RawTask{TypeName: "test"}))
	assert.True(t, user.called, "user mute を prune")
	assert.True(t, channel.called, "channel mute を prune")
}

func TestCheckExpiredMutings_NilReposNoOp(t *testing.T) {
	require.NoError(t, NewCheckExpiredMutingsProcessor(nil, nil).Handle(context.Background(), driver.RawTask{TypeName: "test"}))
}

func TestCheckExpiredMutings_ErrorSwallowed(t *testing.T) {
	proc := NewCheckExpiredMutingsProcessor(&fakeMutePruner{err: errors.New("boom")}, &fakeMutePruner{err: errors.New("boom2")})
	require.NoError(t, proc.Handle(context.Background(), driver.RawTask{TypeName: "test"}), "失敗しても success 扱い")
}

func TestCheckExpiredMutings_PublishesReloadPerOwner(t *testing.T) {
	// user mute で u1 が 2 行、channel mute でも u1 が 1 行失効する状況。u1 は
	// 3 行ぶん返るが reload は 1 回でなければならない (reload は snapshot 全体を
	// 取り直すので、人数分飛ばすのは無駄な DB 往復になる)。
	user := &fakeMutePruner{owners: []string{"u1", "u1", "u2"}}
	channel := &fakeMutePruner{owners: []string{"u1"}}
	pub := &fakeRelationReloadPublisher{}
	proc := NewCheckExpiredMutingsProcessor(user, channel)
	proc.SetRelationReloadPublisher(pub)

	require.NoError(t, proc.Handle(context.Background(), driver.RawTask{TypeName: "test"}))

	assert.ElementsMatch(t, []string{"u1", "u2"}, pub.published)
}

func TestCheckExpiredMutings_NoExpiryNoPublish(t *testing.T) {
	// 失効が 1 件も無い回で reload を飛ばすと、5 分ごとに全 connection が
	// 無駄に snapshot を取り直すことになる。
	pub := &fakeRelationReloadPublisher{}
	proc := NewCheckExpiredMutingsProcessor(&fakeMutePruner{owners: []string{}}, &fakeMutePruner{owners: []string{}})
	proc.SetRelationReloadPublisher(pub)

	require.NoError(t, proc.Handle(context.Background(), driver.RawTask{TypeName: "test"}))

	assert.Empty(t, pub.published)
}

func TestCheckExpiredMutings_PruneErrorStillPublishesOtherHalf(t *testing.T) {
	// 片方が落ちても、もう片方で実際に消えた行の通知は出す。落ちた側だけ次回の
	// cron に持ち越せばよく、成功した側まで stale にする理由が無い。
	pub := &fakeRelationReloadPublisher{}
	proc := NewCheckExpiredMutingsProcessor(
		&fakeMutePruner{err: errors.New("boom")},
		&fakeMutePruner{owners: []string{"u9"}},
	)
	proc.SetRelationReloadPublisher(pub)

	require.NoError(t, proc.Handle(context.Background(), driver.RawTask{TypeName: "test"}))

	assert.Equal(t, []string{"u9"}, pub.published)
}

func TestCheckExpiredMutings_NoPublisherStillPrunes(t *testing.T) {
	// publisher 未配線でも prune 自体は従来どおり動く (= 既存構成を壊さない)。
	user := &fakeMutePruner{}
	proc := NewCheckExpiredMutingsProcessor(user, nil)
	require.NoError(t, proc.Handle(context.Background(), driver.RawTask{TypeName: "test"}))
	assert.True(t, user.called)
}

func TestCheckExpiredMutings_SkipsEmptyOwnerID(t *testing.T) {
	// 空文字を publish すると RefreshRelations が userId 欠落として warn を出す。
	// prune 側の想定外データで無意味なログを増やさない。
	pub := &fakeRelationReloadPublisher{}
	proc := NewCheckExpiredMutingsProcessor(&fakeMutePruner{owners: []string{"", "u1"}}, nil)
	proc.SetRelationReloadPublisher(pub)

	require.NoError(t, proc.Handle(context.Background(), driver.RawTask{TypeName: "test"}))

	assert.Equal(t, []string{"u1"}, pub.published)
}

// --- Clean -------------------------------------------------------------------

type fakeUserIPPruner struct {
	gotBefore time.Time
	err       error
}

func (f *fakeUserIPPruner) DeleteLastSeenBefore(t time.Time) (int64, error) {
	f.gotBefore = t
	return 1, f.err
}

type fakeRolePruner struct {
	called bool
	err    error
}

func (f *fakeRolePruner) DeleteExpired(_ time.Time) (int64, error) {
	f.called = true
	return 2, f.err
}

type fakeGamePruner struct {
	gotThreshold string
	err          error
}

func (f *fakeGamePruner) DeleteOutdatedGames(threshold string) (int64, error) {
	f.gotThreshold = threshold
	return 4, f.err
}

// fakeCleanIDGen records **every** call. 1 つの clean で複数の閾値を生成する
// ので (reversi と user_pending)、最後の 1 つだけ覚えると先の検査が後の呼び
// 出しに上書きされる。
type fakeCleanIDGen struct {
	got  time.Time
	all  []time.Time
	next string
}

func (f *fakeCleanIDGen) Generate(t time.Time) string {
	f.got = t
	f.all = append(f.all, t)
	if f.next != "" {
		return f.next
	}
	return "threshold-id"
}

type fakeAntennaDeactivator struct {
	gotCutoff time.Time
	called    bool
	err       error
}

func (f *fakeAntennaDeactivator) DeactivateUnusedSince(cutoff time.Time) (int64, error) {
	f.called = true
	f.gotCutoff = cutoff
	return 7, f.err
}

const testAntennaThreshold = 7 * 24 * time.Hour

// fakePendingPruner records the threshold the clean job asks for.
type fakePendingPruner struct {
	gotThreshold string
	called       bool
	err          error
}

func (f *fakePendingPruner) DeleteOlderThan(thresholdID string) (int64, error) {
	f.called = true
	f.gotThreshold = thresholdID
	return 3, f.err
}

// **掃除の猶予は `PendingSignupTTL` より長いこと (#3037 レビュー)。**
//
// `clean.go` のコメントは「短すぎると昇格できるはずの行を掃除が先に消す競合が
// 生まれる」と書いているが、それを固定するものが無かった。
// `TestClean_RunsAllSubtasks` の `WithinDuration` は同じ定数どうしの比較なので
// **恒真**で、10 分に下げても緑のまま通る (その場合、有効な確認リンクが
// `EXPIRED` ではなく `NO_SUCH_CODE` で死ぬ)。
func TestPendingSignupRetentionOutlivesTheTTL(t *testing.T) {
	assert.Greater(t, pendingSignupRetention, signup.PendingSignupTTL,
		"掃除の猶予が確認リンクの寿命を下回っている")
}

func TestClean_RunsAllSubtasks(t *testing.T) {
	ip := &fakeUserIPPruner{}
	role := &fakeRolePruner{}
	game := &fakeGamePruner{}
	idGen := &fakeCleanIDGen{}
	antenna := &fakeAntennaDeactivator{}
	pending := &fakePendingPruner{}
	proc := NewCleanProcessor(ip, role, game, idGen, antenna, testAntennaThreshold, pending)
	require.NoError(t, proc.Handle(context.Background(), driver.RawTask{TypeName: "test"}))

	// user_ip は 90 日より前を prune する。
	assert.WithinDuration(t, time.Now().Add(-userIPRetention), ip.gotBefore, time.Minute)
	assert.True(t, role.called)
	// reversi の閾値 id は now-10min から生成される。
	assert.Equal(t, "threshold-id", game.gotThreshold)
	require.Len(t, idGen.all, 2, "閾値の生成回数が変わっている")
	assert.WithinDuration(t, time.Now().Add(-reversiOutdatedAfter), idGen.all[0], time.Minute)
	// user_pending の閾値は now-24h。
	assert.WithinDuration(t, time.Now().Add(-pendingSignupRetention), idGen.all[1], time.Minute)
	// antenna は now-threshold より古い lastUsedAt を deactivate する。
	assert.True(t, antenna.called)
	assert.WithinDuration(t, time.Now().Add(-testAntennaThreshold), antenna.gotCutoff, time.Minute)
	// **期限切れの pending signup も掃除する (#3037)。** `PromotePending` は
	// 期限切れを拒否するだけで行を消さないので、放置された登録のメール
	// アドレスとパスワードハッシュが無期限に貯まっていた。
	assert.True(t, pending.called, "期限切れの user_pending を掃除していない")
	assert.Equal(t, "threshold-id", pending.gotThreshold)
}

func TestClean_NilDepsNoOp(t *testing.T) {
	require.NoError(t, NewCleanProcessor(nil, nil, nil, nil, nil, testAntennaThreshold, nil).Handle(context.Background(), driver.RawTask{TypeName: "test"}))
}

func TestClean_AntennaThresholdZeroSkips(t *testing.T) {
	antenna := &fakeAntennaDeactivator{}
	// threshold <= 0 は antenna deactivate を無効化する (本家 `> 0` ガード相当)。
	proc := NewCleanProcessor(nil, nil, nil, nil, antenna, 0, nil)
	require.NoError(t, proc.Handle(context.Background(), driver.RawTask{TypeName: "test"}))
	assert.False(t, antenna.called, "threshold=0 では呼ばれない")
}

func TestClean_SubtaskErrorsSwallowed(t *testing.T) {
	proc := NewCleanProcessor(
		&fakeUserIPPruner{err: errors.New("a")},
		&fakeRolePruner{err: errors.New("b")},
		&fakeGamePruner{err: errors.New("c")},
		&fakeCleanIDGen{},
		&fakeAntennaDeactivator{err: errors.New("d")},
		testAntennaThreshold,
		&fakePendingPruner{err: errors.New("e")},
	)
	require.NoError(t, proc.Handle(context.Background(), driver.RawTask{TypeName: "test"}), "個々の失敗は swallow して success")
}

// --- CheckModeratorsActivity -------------------------------------------------

type fakeChecker struct {
	called bool
	err    error
}

func (f *fakeChecker) Check() error { f.called = true; return f.err }

func TestCheckModeratorsActivity_Delegates(t *testing.T) {
	c := &fakeChecker{}
	proc := NewCheckModeratorsActivityProcessor(c)
	require.NoError(t, proc.Handle(context.Background(), driver.RawTask{TypeName: "test"}))
	assert.True(t, c.called)
}

func TestCheckModeratorsActivity_NilSvcNoOp(t *testing.T) {
	require.NoError(t, NewCheckModeratorsActivityProcessor(nil).Handle(context.Background(), driver.RawTask{TypeName: "test"}))
}

func TestCheckModeratorsActivity_ErrorSwallowed(t *testing.T) {
	proc := NewCheckModeratorsActivityProcessor(&fakeChecker{err: errors.New("boom")})
	require.NoError(t, proc.Handle(context.Background(), driver.RawTask{TypeName: "test"}))
}

// 掃除の基準と、検索が画面に出す保持期間は**同じ値でなければならない** (#3104)。
//
// **`TestClean_RunsAllSubtasks` では検出できない。** あちらは `userIPRetention`
// 自身を期待値に使うので、この定数を `45 * 24 * time.Hour` に切り離しても緑のまま
// 通る (実測)。そのとき cron は 45 日で刈るのに `admin/ip/accounts` は
// `retentionDays: 90` を返し、**画面が「90 日より前の接続は残っていない」と
// 嘘をつく**。定義を `core/iplog` の 1 箇所に寄せた意味がここで消える。
func TestUserIPRetentionMatchesIPLog(t *testing.T) {
	assert.Equal(t, iplog.Retention, userIPRetention,
		"user_ip の保持期間が core/iplog から切り離されている。"+
			"掃除の基準と admin/ip/accounts が返す retentionDays がずれる (#3104)")
}
