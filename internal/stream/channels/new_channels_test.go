package channels

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/stream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var errAntennaLookup = errors.New("antenna lookup failed")

// mockRoleChecker is a test double for AdminRoleChecker.
type mockRoleChecker struct {
	admins map[string]bool
}

func (m *mockRoleChecker) IsAdministrator(userID string) bool {
	return m.admins[userID]
}

func newAdminCh(ctx stream.ChannelContext, isAdmin bool) stream.Channel {
	userID := ""
	if u, ok := ctx.User().(*model.User); ok && u != nil {
		userID = u.ID
	}
	checker := &mockRoleChecker{admins: map[string]bool{userID: isAdmin}}
	return NewAdminFactory(checker).New(ctx)
}

// mockExplorable is a test double for RoleExplorableChecker.
type mockExplorable struct{ explorable map[string]bool }

func (m mockExplorable) IsExplorable(roleID string) bool { return m.explorable[roleID] }

// newRoleCh builds a roleTimeline channel where role "r1" is explorable per the
// flag, via the gated factory (#1549).
func newRoleCh(ctx stream.ChannelContext, explorable bool) stream.Channel {
	return NewRoleTimelineFactory(mockExplorable{explorable: map[string]bool{"r1": explorable}}).New(ctx)
}

// stubAntennaOwners is a test double for AntennaOwnerLookup. A miss returns
// (nil, nil) (Init rejects via a == nil); set err to exercise the lookup-error path.
type stubAntennaOwners struct {
	byID map[string]*model.Antenna
	err  error
}

func (s *stubAntennaOwners) FindByID(id string) (*model.Antenna, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.byID[id], nil
}

// newAntennaCh builds an antenna channel whose lookup knows antenna "a1" owned
// by ownerID, via the gated factory (#1569).
func newAntennaCh(ctx stream.ChannelContext, ownerID string) stream.Channel {
	owners := &stubAntennaOwners{byID: map[string]*model.Antenna{
		"a1": {ID: "a1", UserID: ownerID},
	}}
	return NewAntennaFactory(owners).New(ctx)
}

// interface conformance checks
var (
	_ stream.Channel = (*HashtagChannel)(nil)
	_ stream.Channel = (*AntennaChannel)(nil)
	_ stream.Channel = (*ChannelTimelineChannel)(nil)
	_ stream.Channel = (*UserListChannel)(nil)
	_ stream.Channel = (*RoleTimelineChannel)(nil)
	_ stream.Channel = (*AdminChannel)(nil)
	_ stream.Channel = (*ServerStatsChannel)(nil)
	_ stream.Channel = (*QueueStatsChannel)(nil)
	_ stream.Channel = (*ReversiChannel)(nil)
)

// --- Hashtag ---

func TestHashtag_Lifecycle(t *testing.T) {
	ctx := newCtx(nil)
	ch := NewHashtag(ctx)
	ch.Init(json.RawMessage(`{"q":[["golang"]]}`))
	assert.Equal(t, []string{"hashtag:golang"}, ctx.subs)

	ch.OnRedisEvent([]byte(`{"id":"n1","tags":["golang"],"visibility":"public"}`))
	require.Len(t, ctx.sentType, 1)
	assert.Equal(t, "note", ctx.sentType[0])

	ch.Dispose()
	assert.Equal(t, []string{"hashtag:golang"}, ctx.unsubs)
}

func TestHashtag_EmptyQ(t *testing.T) {
	ctx := newCtx(nil)
	ch := NewHashtag(ctx)
	err := ch.Init(json.RawMessage(`{}`))
	assert.ErrorIs(t, err, stream.ErrInvalidParams)
	assert.Empty(t, ctx.subs)
	ch.Dispose()
}

// #1549: hashtag now receives live notes (publisher added) and re-evaluates the
// OR-of-ANDs query + normalizes + dedupes per subscriber.
func TestHashtag_MultiTagSubscribeUnion(t *testing.T) {
	ctx := newCtx(nil)
	ch := NewHashtag(ctx)
	// q = (a AND b) OR (c): subscribe distinct tags a,b,c.
	ch.Init(json.RawMessage(`{"q":[["a","b"],["c"]]}`))
	assert.ElementsMatch(t, []string{"hashtag:a", "hashtag:b", "hashtag:c"}, ctx.subs)
}

func TestHashtag_OrOfAndsMatch(t *testing.T) {
	t.Run("AND group requires all tags", func(t *testing.T) {
		ctx := newCtx(nil)
		ch := NewHashtag(ctx)
		ch.Init(json.RawMessage(`{"q":[["a","b"]]}`))
		ch.OnRedisEvent([]byte(`{"id":"n1","tags":["a"],"visibility":"public"}`)) // only a
		assert.Empty(t, ctx.sentType, "AND group needs both a and b")
		ch.OnRedisEvent([]byte(`{"id":"n2","tags":["a","b","x"],"visibility":"public"}`))
		require.Len(t, ctx.sentType, 1, "superset of {a,b} matches")
	})
	t.Run("OR group matches either", func(t *testing.T) {
		ctx := newCtx(nil)
		ch := NewHashtag(ctx)
		ch.Init(json.RawMessage(`{"q":[["a"],["b"]]}`))
		ch.OnRedisEvent([]byte(`{"id":"n1","tags":["b"],"visibility":"public"}`))
		require.Len(t, ctx.sentType, 1)
	})
}

func TestHashtag_NormalizationParity(t *testing.T) {
	ctx := newCtx(nil)
	ch := NewHashtag(ctx)
	ch.Init(json.RawMessage(`{"q":[["GoLang"]]}`)) // subscribes hashtag:golang
	assert.Equal(t, []string{"hashtag:golang"}, ctx.subs)
	// payload tag in different case/width still matches after NFKC+lower.
	ch.OnRedisEvent([]byte(`{"id":"n1","tags":["ＧＯＬＡＮＧ"],"visibility":"public"}`))
	require.Len(t, ctx.sentType, 1, "full-width upper tag must match normalized subscription")
}

func TestHashtag_DedupeAcrossTopics(t *testing.T) {
	ctx := newCtx(nil)
	ch := NewHashtag(ctx)
	ch.Init(json.RawMessage(`{"q":[["a"],["b"]]}`))
	// same note arrives via both hashtag:a and hashtag:b (fanout fires per topic).
	payload := []byte(`{"id":"dup1","tags":["a","b"],"visibility":"public"}`)
	ch.OnRedisEvent(payload)
	ch.OnRedisEvent(payload)
	require.Len(t, ctx.sentType, 1, "a note matching two subscribed tags must deliver once")
}

func TestHashtag_FollowersVisibilityGate(t *testing.T) {
	ctx := newCtx(&model.User{ID: "alice"})
	ctx.followingSnap = map[string]bool{} // not a follower
	ch := NewHashtag(ctx)
	ch.Init(json.RawMessage(`{"q":[["a"]]}`))
	ch.OnRedisEvent([]byte(`{"id":"n1","tags":["a"],"userId":"author","visibility":"followers"}`))
	assert.Empty(t, ctx.sentType, "followers note from non-followed author must be dropped")
}

// --- Antenna ---

func TestAntenna_Lifecycle(t *testing.T) {
	ctx := newCtx(&model.User{ID: "alice"})
	ch := newAntennaCh(ctx, "alice") // a1 owned by alice
	require.NoError(t, ch.Init(json.RawMessage(`{"antennaId":"a1"}`)))
	assert.Equal(t, []string{"antennaTimeline:a1"}, ctx.subs)

	ch.OnRedisEvent([]byte(`{"id":"n1"}`))
	require.Len(t, ctx.sentType, 1)

	ch.Dispose()
	assert.Equal(t, []string{"antennaTimeline:a1"}, ctx.unsubs)
}

func TestAntenna_NoAuth(t *testing.T) {
	ctx := newCtx(nil)
	ch := newAntennaCh(ctx, "alice")
	err := ch.Init(json.RawMessage(`{"antennaId":"a1"}`))
	assert.ErrorIs(t, err, stream.ErrInvalidParams)
	assert.Empty(t, ctx.subs)
}

func TestAntenna_MissingID(t *testing.T) {
	ctx := newCtx(&model.User{ID: "alice"})
	ch := newAntennaCh(ctx, "alice")
	err := ch.Init(json.RawMessage(`{}`))
	assert.ErrorIs(t, err, stream.ErrInvalidParams)
	assert.Empty(t, ctx.subs)
}

// TestAntenna_NonOwnerRejected is the #1569 cross-user IDOR regression guard: a
// user must not be able to subscribe to another user's antenna feed.
func TestAntenna_NonOwnerRejected(t *testing.T) {
	ctx := newCtx(&model.User{ID: "bob"})
	ch := newAntennaCh(ctx, "alice") // a1 owned by alice, bob connects
	err := ch.Init(json.RawMessage(`{"antennaId":"a1"}`))
	assert.ErrorIs(t, err, stream.ErrInvalidParams)
	assert.Empty(t, ctx.subs, "non-owner must not subscribe to another user's antenna")
}

func TestAntenna_UnknownAntennaRejected(t *testing.T) {
	ctx := newCtx(&model.User{ID: "alice"})
	ch := newAntennaCh(ctx, "alice")
	err := ch.Init(json.RawMessage(`{"antennaId":"does-not-exist"}`))
	assert.ErrorIs(t, err, stream.ErrInvalidParams)
	assert.Empty(t, ctx.subs)
}

// TestAntenna_NilOwnersFailClosed guarantees an unwired factory rejects every
// subscription rather than silently opening the gate.
func TestAntenna_NilOwnersFailClosed(t *testing.T) {
	ctx := newCtx(&model.User{ID: "alice"})
	ch := NewAntennaFactory(nil).New(ctx)
	err := ch.Init(json.RawMessage(`{"antennaId":"a1"}`))
	assert.ErrorIs(t, err, stream.ErrInvalidParams)
	assert.Empty(t, ctx.subs)
}

// TestAntenna_LookupErrorRejected covers the FindByID error path (fail-closed).
func TestAntenna_LookupErrorRejected(t *testing.T) {
	ctx := newCtx(&model.User{ID: "alice"})
	owners := &stubAntennaOwners{err: errAntennaLookup}
	ch := NewAntennaFactory(owners).New(ctx)
	err := ch.Init(json.RawMessage(`{"antennaId":"a1"}`))
	assert.ErrorIs(t, err, stream.ErrInvalidParams)
	assert.Empty(t, ctx.subs)
}

// --- ChannelTimeline ---

func TestChannelTimeline_Lifecycle(t *testing.T) {
	ctx := newCtx(nil)
	ch := NewChannelTimeline(ctx)
	ch.Init(json.RawMessage(`{"channelId":"ch1"}`))
	assert.Equal(t, []string{"channel:ch1"}, ctx.subs)

	ch.OnRedisEvent([]byte(`{"id":"n1","channelId":"ch1","visibility":"public"}`))
	require.Len(t, ctx.sentType, 1)

	ch.Dispose()
	assert.Equal(t, []string{"channel:ch1"}, ctx.unsubs)
}

func TestChannelTimeline_MissingID(t *testing.T) {
	ctx := newCtx(nil)
	ch := NewChannelTimeline(ctx)
	// TS本家はchannelId欠如時もinit成功とするため、errorは返さずno-op
	err := ch.Init(json.RawMessage(`{}`))
	assert.NoError(t, err)
	assert.Empty(t, ctx.subs)
}

// #1549: channel timeline now receives live notes (publisher added) and gates
// per-subscriber. These cover the new OnRedisEvent gates.
func TestChannelTimeline_WrongChannelDropped(t *testing.T) {
	ctx := newCtx(&model.User{ID: "alice"})
	ch := NewChannelTimeline(ctx)
	ch.Init(json.RawMessage(`{"channelId":"ch1"}`))
	ch.OnRedisEvent([]byte(`{"id":"n1","channelId":"ch2","visibility":"public"}`))
	assert.Empty(t, ctx.sentType, "note for a different channel must be dropped")
}

func TestChannelTimeline_MalformedDropped(t *testing.T) {
	ctx := newCtx(&model.User{ID: "alice"})
	ch := NewChannelTimeline(ctx)
	ch.Init(json.RawMessage(`{"channelId":"ch1"}`))
	ch.OnRedisEvent([]byte(`{not json`))
	assert.Empty(t, ctx.sentType, "malformed payload must be dropped (fail-closed)")
}

func TestChannelTimeline_FollowersVisibilityGate(t *testing.T) {
	t.Run("non-follower dropped", func(t *testing.T) {
		ctx := newCtx(&model.User{ID: "alice"})
		ctx.followingSnap = map[string]bool{} // alice does not follow author
		ch := NewChannelTimeline(ctx)
		ch.Init(json.RawMessage(`{"channelId":"ch1"}`))
		ch.OnRedisEvent([]byte(`{"id":"n1","channelId":"ch1","userId":"author","visibility":"followers"}`))
		assert.Empty(t, ctx.sentType, "followers note from non-followed author must be dropped")
	})
	t.Run("follower emitted", func(t *testing.T) {
		ctx := newCtx(&model.User{ID: "alice"})
		ctx.followingSnap = map[string]bool{"author": false}
		ch := NewChannelTimeline(ctx)
		ch.Init(json.RawMessage(`{"channelId":"ch1"}`))
		ch.OnRedisEvent([]byte(`{"id":"n1","channelId":"ch1","userId":"author","visibility":"followers"}`))
		require.Len(t, ctx.sentType, 1)
		assert.Equal(t, "note", ctx.sentType[0])
	})
	t.Run("self-authored emitted with nil snapshot", func(t *testing.T) {
		ctx := newCtx(&model.User{ID: "alice"})
		ch := NewChannelTimeline(ctx)
		ch.Init(json.RawMessage(`{"channelId":"ch1"}`))
		ch.OnRedisEvent([]byte(`{"id":"n1","channelId":"ch1","userId":"alice","visibility":"followers"}`))
		require.Len(t, ctx.sentType, 1)
	})
}

// --- UserList ---

func TestUserList_Lifecycle(t *testing.T) {
	ctx := newCtx(&model.User{ID: "alice"})
	ch := NewUserList(ctx)
	ch.Init(json.RawMessage(`{"listId":"l1"}`))
	assert.Equal(t, []string{"userListTimeline:l1"}, ctx.subs)

	ch.OnRedisEvent([]byte(`{"id":"n1"}`))
	require.Len(t, ctx.sentType, 1)

	ch.Dispose()
	assert.Equal(t, []string{"userListTimeline:l1"}, ctx.unsubs)
}

func TestUserList_NoAuth(t *testing.T) {
	ctx := newCtx(nil)
	ch := NewUserList(ctx)
	err := ch.Init(json.RawMessage(`{"listId":"l1"}`))
	assert.ErrorIs(t, err, stream.ErrInvalidParams)
	assert.Empty(t, ctx.subs)
}

func TestUserList_MissingID(t *testing.T) {
	ctx := newCtx(&model.User{ID: "alice"})
	ch := NewUserList(ctx)
	err := ch.Init(json.RawMessage(`{}`))
	assert.ErrorIs(t, err, stream.ErrInvalidParams)
	assert.Empty(t, ctx.subs)
}

func TestUserList_WithReplies(t *testing.T) {
	ctx := newCtx(&model.User{ID: "alice"})
	ch := NewUserList(ctx)
	ch.Init(json.RawMessage(`{"listId":"l1","withReplies":true}`))
	assert.Equal(t, []string{"userListTimeline:l1"}, ctx.subs)

	// リプライが通過する
	ch.OnRedisEvent([]byte(`{"text":"reply","replyId":"p1"}`))
	require.Len(t, ctx.sentType, 1)
}

// #1063: UserList channel は per-membership の withReplies (= upstream
// `MiUserListMembership.withReplies`) を持つべきだが mk-go 側に model が
// 無いため未実装。本 PR で noteFilter.shouldEmit から reply blanket-drop
// を撤廃した副作用で、connect param `withReplies=false` でも reply が
// pass-through する暫定挙動になっている。完全 upstream 互換 (= per-member
// gate) は別 issue で対応する想定。本テストは暫定挙動の regression guard
// で、将来 per-member gate を入れたら drop 側に書き換える。
func TestUserList_WithRepliesFalse_ReplyPassthrough(t *testing.T) {
	ctx := newCtx(&model.User{ID: "alice"})
	ch := NewUserList(ctx)
	ch.Init(json.RawMessage(`{"listId":"l1","withReplies":false}`))
	ch.OnRedisEvent([]byte(`{"text":"reply","replyId":"p1"}`))
	require.Len(t, ctx.sentType, 1)
	assert.Equal(t, "note", ctx.sentType[0])
}

// #1465: followers visibility note は viewer が author を follow している
// 場合のみ emit される (defense-in-depth)。fanout 段で list owner の follow
// を check してから push する設計だが、stream 残留 entry に対するフォール
// バックとして channel 側でも 1 段 gate する。
func TestUserList_FollowersVisibility_NonFollowerDropped(t *testing.T) {
	ctx := newCtx(&model.User{ID: "alice"})
	ctx.followingSnap = map[string]bool{} // alice は author を follow していない
	ch := NewUserList(ctx)
	ch.Init(json.RawMessage(`{"listId":"l1"}`))

	ch.OnRedisEvent([]byte(`{"id":"n1","userId":"author","visibility":"followers"}`))
	assert.Empty(t, ctx.sentType, "non-follower viewer must not receive followers note")
}

func TestUserList_FollowersVisibility_FollowerAccepted(t *testing.T) {
	ctx := newCtx(&model.User{ID: "alice"})
	ctx.followingSnap = map[string]bool{"author": false} // value (withReplies) は関係ない
	ch := NewUserList(ctx)
	ch.Init(json.RawMessage(`{"listId":"l1"}`))

	ch.OnRedisEvent([]byte(`{"id":"n1","userId":"author","visibility":"followers"}`))
	require.Len(t, ctx.sentType, 1)
	assert.Equal(t, "note", ctx.sentType[0])
}

func TestUserList_FollowersVisibility_SelfAuthored(t *testing.T) {
	ctx := newCtx(&model.User{ID: "alice"})
	// snap は nil でも本人 short-circuit で通る
	ch := NewUserList(ctx)
	ch.Init(json.RawMessage(`{"listId":"l1"}`))

	ch.OnRedisEvent([]byte(`{"id":"n1","userId":"alice","visibility":"followers"}`))
	require.Len(t, ctx.sentType, 1)
}

func TestUserList_FollowersVisibility_NilSnapshotFailClosed(t *testing.T) {
	ctx := newCtx(&model.User{ID: "alice"})
	// followingSnap=nil (= snapshot lookup 未配線 / 取得失敗) で他人の followers
	// note は drop される。本人は別 case (上) で通すことを確認済。
	ch := NewUserList(ctx)
	ch.Init(json.RawMessage(`{"listId":"l1"}`))

	ch.OnRedisEvent([]byte(`{"id":"n1","userId":"author","visibility":"followers"}`))
	assert.Empty(t, ctx.sentType, "nil snapshot must fail-closed for followers note from non-self author")
}

func TestUserList_PublicVisibility_NoFollowCheck(t *testing.T) {
	ctx := newCtx(&model.User{ID: "alice"})
	// public は snap 無くても、follow 関係に関係なく通る
	ch := NewUserList(ctx)
	ch.Init(json.RawMessage(`{"listId":"l1"}`))

	ch.OnRedisEvent([]byte(`{"id":"n1","userId":"author","visibility":"public"}`))
	require.Len(t, ctx.sentType, 1)
}

func TestUserList_HomeVisibility_NoFollowCheck(t *testing.T) {
	ctx := newCtx(&model.User{ID: "alice"})
	ch := NewUserList(ctx)
	ch.Init(json.RawMessage(`{"listId":"l1"}`))

	ch.OnRedisEvent([]byte(`{"id":"n1","userId":"author","visibility":"home"}`))
	require.Len(t, ctx.sentType, 1)
}

// visibility 欠如 payload は parse 自体は通るが visibility != "followers" の
// branch で素通りする (regression guard / 後方互換)。
func TestUserList_NoVisibilityField_Passthrough(t *testing.T) {
	ctx := newCtx(&model.User{ID: "alice"})
	ch := NewUserList(ctx)
	ch.Init(json.RawMessage(`{"listId":"l1"}`))

	ch.OnRedisEvent([]byte(`{"id":"n1"}`))
	require.Len(t, ctx.sentType, 1)
}

// 不正 payload はパース失敗で conservative drop される (IDOR fail-closed)。
func TestUserList_BrokenPayload_Dropped(t *testing.T) {
	ctx := newCtx(&model.User{ID: "alice"})
	ch := NewUserList(ctx)
	ch.Init(json.RawMessage(`{"listId":"l1"}`))

	ch.OnRedisEvent([]byte(`{not json`))
	assert.Empty(t, ctx.sentType)
}

// #1491 audit 指摘 5: userListVisibilityShouldEmit は visibility == "followers"
// 以外の payload を素通りする (user_list.go:112)。コメントには「specified は
// fanout (shouldFanoutToFollowers) で除外されるからここに来ない」と書いてある
// が、その assumption は untested。fanout 側の refactor がうっかり specified
// payload を user_list stream へ送り始めた瞬間に WS gate も passthrough して
// しまうので、その日の defense-in-depth がどう動くかを test で確定させておく。
//
// 現実装は specified payload を非宛先 viewer にも emit する (passthrough)。
// この test が落ちる条件 = ゲートが visibility 別 path を追加して specified を
// 個別に判定するようになったとき、そのときは併せて fanout 側のテスト
// (TestFanoutHook_FanoutToUserLists_SpecifiedVisibilitySkipped) を見直すべき。
func TestUserList_SpecifiedVisibility_AssumesFanoutSkips(t *testing.T) {
	ctx := newCtx(&model.User{ID: "alice"})
	ch := NewUserList(ctx)
	ch.Init(json.RawMessage(`{"listId":"l1"}`))

	// alice 宛ではない specified payload。fanout 側で normally この shape は
	// user_list stream に流れない (= shouldFanoutToFollowers で specified が
	// 除外される) が、もし流れた場合 WS gate は passthrough する現状動作。
	ch.OnRedisEvent([]byte(`{"id":"n1","userId":"author","visibility":"specified","visibleUserIds":["other"]}`))
	require.Len(t, ctx.sentType, 1,
		"現状の userListVisibilityShouldEmit は specified payload を passthrough する "+
			"(fanout 側 shouldFanoutToFollowers が specified を弾く前提)。fanout の "+
			"assumption が崩れた瞬間に WS leak になるので、本 test を grep して "+
			"両層をセットで見直すこと。")
}

// --- RoleTimeline ---

func TestRoleTimeline_Lifecycle(t *testing.T) {
	ctx := newCtx(nil)
	ch := newRoleCh(ctx, true) // r1 explorable
	ch.Init(json.RawMessage(`{"roleId":"r1"}`))
	assert.Equal(t, []string{"roleTimeline:r1"}, ctx.subs)

	ch.OnRedisEvent([]byte(`{"id":"n1","visibility":"public"}`))
	require.Len(t, ctx.sentType, 1)

	ch.Dispose()
	assert.Equal(t, []string{"roleTimeline:r1"}, ctx.unsubs)
}

func TestRoleTimeline_MissingID(t *testing.T) {
	ctx := newCtx(nil)
	ch := newRoleCh(ctx, true)
	// TS本家はroleId欠如時もinit成功とするため、errorは返さずno-op
	err := ch.Init(json.RawMessage(`{}`))
	assert.NoError(t, err)
	assert.Empty(t, ctx.subs)
}

// #1549: roleTimeline now receives live notes; only isExplorable roles +
// public-visibility notes are emitted (本家 role-timeline.ts parity).
func TestRoleTimeline_NonExplorableDropped(t *testing.T) {
	ctx := newCtx(nil)
	ch := newRoleCh(ctx, false) // r1 NOT explorable
	ch.Init(json.RawMessage(`{"roleId":"r1"}`))
	ch.OnRedisEvent([]byte(`{"id":"n1","visibility":"public"}`))
	assert.Empty(t, ctx.sentType, "non-explorable role must not stream notes")
}

func TestRoleTimeline_NonPublicDropped(t *testing.T) {
	ch := newRoleCh(newCtx(nil), true)
	ch.Init(json.RawMessage(`{"roleId":"r1"}`))
	for _, vis := range []string{"home", "followers", "specified"} {
		ctx := newCtx(nil)
		c := newRoleCh(ctx, true)
		c.Init(json.RawMessage(`{"roleId":"r1"}`))
		c.OnRedisEvent([]byte(`{"id":"n1","visibility":"` + vis + `"}`))
		assert.Empty(t, ctx.sentType, "roleTimeline emits public only, got "+vis)
	}
	_ = ch
}

func TestRoleTimeline_ExplorablePublicEmitted(t *testing.T) {
	ctx := newCtx(nil)
	ch := newRoleCh(ctx, true)
	ch.Init(json.RawMessage(`{"roleId":"r1"}`))
	ch.OnRedisEvent([]byte(`{"id":"n1","visibility":"public"}`))
	require.Len(t, ctx.sentType, 1)
	assert.Equal(t, "note", ctx.sentType[0])
}

// --- Admin ---

func TestAdmin_Lifecycle(t *testing.T) {
	ctx := newCtx(&model.User{ID: "admin1"})
	ch := newAdminCh(ctx, true)
	ch.Init(nil)
	assert.Equal(t, []string{"adminStream"}, ctx.subs)

	// エンベロープ展開
	ch.OnRedisEvent([]byte(`{"type":"newReport","body":{"id":"r1"}}`))
	require.Len(t, ctx.sentType, 1)
	assert.Equal(t, "newReport", ctx.sentType[0])

	ch.Dispose()
	assert.Equal(t, []string{"adminStream"}, ctx.unsubs)
}

func TestAdmin_NoAuth(t *testing.T) {
	ctx := newCtx(nil)
	ch := newAdminCh(ctx, false)
	err := ch.Init(nil)
	assert.ErrorIs(t, err, stream.ErrInvalidParams)
	assert.Empty(t, ctx.subs)
}

func TestAdmin_NotAdmin(t *testing.T) {
	ctx := newCtx(&model.User{ID: "normaluser"})
	ch := newAdminCh(ctx, false)
	err := ch.Init(nil)
	assert.ErrorIs(t, err, stream.ErrInvalidParams)
	assert.Empty(t, ctx.subs)
}

func TestAdmin_RawPayload(t *testing.T) {
	ctx := newCtx(&model.User{ID: "admin1"})
	ch := newAdminCh(ctx, true)
	ch.Init(nil)
	// エンベロープでないペイロード
	ch.OnRedisEvent([]byte(`{"data":"raw"}`))
	require.Len(t, ctx.sentType, 1)
	assert.Equal(t, "adminEvent", ctx.sentType[0])
}

// --- ServerStats ---

func TestServerStats_Lifecycle(t *testing.T) {
	ctx := newCtx(nil)
	ch := NewServerStats(ctx)
	ch.Init(nil)
	assert.Equal(t, []string{"serverStats"}, ctx.subs)

	ch.OnRedisEvent([]byte(`{"cpu":0.5}`))
	require.Len(t, ctx.sentType, 1)
	assert.Equal(t, "stats", ctx.sentType[0])

	ch.Dispose()
	assert.Equal(t, []string{"serverStats"}, ctx.unsubs)
}

func TestServerStats_RequestLog(t *testing.T) {
	ctx := newCtx(nil)
	ch := NewServerStats(ctx)
	ch.Init(nil)
	ch.OnClientMessage("requestLog", json.RawMessage(`{"id":"req1","length":50}`))
	require.Len(t, ctx.sentType, 1)
	assert.Equal(t, "statsLog", ctx.sentType[0])
}

func TestServerStats_RequestLog_IgnoresOtherTypes(t *testing.T) {
	ctx := newCtx(nil)
	ch := NewServerStats(ctx)
	ch.Init(nil)
	ch.OnClientMessage("other", nil)
	assert.Empty(t, ctx.sentType)
}

// --- QueueStats ---

func TestQueueStats_Lifecycle(t *testing.T) {
	ctx := newCtx(nil)
	ch := NewQueueStats(ctx)
	ch.Init(nil)
	assert.Equal(t, []string{"queueStats"}, ctx.subs)

	ch.OnRedisEvent([]byte(`{"deliver":10}`))
	require.Len(t, ctx.sentType, 1)
	assert.Equal(t, "stats", ctx.sentType[0])

	ch.Dispose()
	assert.Equal(t, []string{"queueStats"}, ctx.unsubs)
}

func TestQueueStats_RequestLog(t *testing.T) {
	ctx := newCtx(nil)
	ch := NewQueueStats(ctx)
	ch.Init(nil)
	ch.OnClientMessage("requestLog", json.RawMessage(`{"id":"req1","length":50}`))
	require.Len(t, ctx.sentType, 1)
	assert.Equal(t, "statsLog", ctx.sentType[0])
}

func TestQueueStats_RequestLog_IgnoresOtherTypes(t *testing.T) {
	ctx := newCtx(nil)
	ch := NewQueueStats(ctx)
	ch.Init(nil)
	ch.OnClientMessage("other", nil)
	assert.Empty(t, ctx.sentType)
}

// --- Reversi ---

func TestReversi_Lifecycle(t *testing.T) {
	ctx := newCtx(&model.User{ID: "alice"})
	ch := NewReversi(ctx)
	ch.Init(nil)
	assert.Equal(t, []string{"reversi:alice"}, ctx.subs)

	ch.OnRedisEvent([]byte(`{"type":"invited","body":{"gameId":"g1"}}`))
	require.Len(t, ctx.sentType, 1)
	assert.Equal(t, "invited", ctx.sentType[0])

	ch.Dispose()
	assert.Equal(t, []string{"reversi:alice"}, ctx.unsubs)
}

func TestReversi_NoAuth(t *testing.T) {
	ctx := newCtx(nil)
	ch := NewReversi(ctx)
	err := ch.Init(nil)
	assert.ErrorIs(t, err, stream.ErrInvalidParams)
	assert.Empty(t, ctx.subs)
}

// --- OnClientMessage no-op coverage ---

func TestNoOpClientMessages(t *testing.T) {
	body := json.RawMessage(`{}`)
	cases := []struct {
		name string
		ch   stream.Channel
	}{
		{"hashtag", NewHashtag(newCtx(nil))},
		{"antenna", newAntennaCh(newCtx(&model.User{ID: "u1"}), "u1")},
		{"channelTimeline", NewChannelTimeline(newCtx(nil))},
		{"userList", NewUserList(newCtx(&model.User{ID: "u1"}))},
		{"roleTimeline", newRoleCh(newCtx(nil), true)},
		{"admin", newAdminCh(newCtx(&model.User{ID: "u1"}), true)},
		{"serverStats", NewServerStats(newCtx(nil))},
		{"queueStats", NewQueueStats(newCtx(nil))},
		{"reversi", NewReversi(newCtx(&model.User{ID: "u1"}))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.ch.OnClientMessage("test", body)
		})
	}
}

// --- Filter branch coverage for new channels ---

func TestHashtag_FilteredRenote(t *testing.T) {
	ctx := newCtx(nil)
	ch := NewHashtag(ctx)
	ch.Init(json.RawMessage(`{"q":[["go"]],"withRenotes":false}`))
	ch.OnRedisEvent([]byte(`{"renoteId":"r1","fileIds":[]}`))
	assert.Empty(t, ctx.sentType)
}

// #1063: ChannelTimeline (Misskey の channel 機能 timeline) は upstream
// `channel.ts` に reply gate を持たない。withReplies connect param に関わらず
// reply は pass-through する。旧テストは「withReplies=false で reply を drop」
// する drift 挙動を assertion していたので新 semantics に揃える。
func TestChannelTimeline_ReplyPassthrough(t *testing.T) {
	ctx := newCtx(nil)
	ch := NewChannelTimeline(ctx)
	ch.Init(json.RawMessage(`{"channelId":"ch1","withReplies":false}`))
	ch.OnRedisEvent([]byte(`{"text":"reply","replyId":"p1","channelId":"ch1","visibility":"public"}`))
	require.Len(t, ctx.sentType, 1)
	assert.Equal(t, "note", ctx.sentType[0])
}

func TestRoleTimeline_FilteredRenote(t *testing.T) {
	ctx := newCtx(nil)
	ch := newRoleCh(ctx, true)
	ch.Init(json.RawMessage(`{"roleId":"r1","withRenotes":false}`))
	// public + explorable で gate を通過させ、純リノート filter (shouldEmit) で drop。
	ch.OnRedisEvent([]byte(`{"renoteId":"r1","fileIds":[],"visibility":"public"}`))
	assert.Empty(t, ctx.sentType)
}

func TestUserList_FilteredNoFiles(t *testing.T) {
	ctx := newCtx(&model.User{ID: "alice"})
	ch := NewUserList(ctx)
	ch.Init(json.RawMessage(`{"listId":"l1","withFiles":true}`))
	ch.OnRedisEvent([]byte(`{"text":"hello","fileIds":[]}`))
	assert.Empty(t, ctx.sentType)
}

func TestUserList_MissingListID(t *testing.T) {
	ctx := newCtx(&model.User{ID: "alice"})
	ch := NewUserList(ctx)
	ch.Init(json.RawMessage(`{}`))
	assert.Empty(t, ctx.subs)
}

func TestReversi_RawPayload(t *testing.T) {
	ctx := newCtx(&model.User{ID: "alice"})
	ch := NewReversi(ctx)
	ch.Init(nil)
	// エンベロープでないペイロード
	ch.OnRedisEvent([]byte(`{"data":"raw"}`))
	require.Len(t, ctx.sentType, 1)
	assert.Equal(t, "reversiEvent", ctx.sentType[0])
}

// --- Timeline filter integration ---

func TestLocalTimeline_FilterPureRenote(t *testing.T) {
	ctx := newCtx(nil)
	ch := NewLocalTimeline(ctx)
	ch.Init(json.RawMessage(`{"withRenotes":false}`))

	// 純リノートはフィルタされる
	ch.OnRedisEvent([]byte(`{"renoteId":"r1","fileIds":[]}`))
	assert.Empty(t, ctx.sentType)

	// 通常ノートは通過
	ch.OnRedisEvent([]byte(`{"text":"hello"}`))
	require.Len(t, ctx.sentType, 1)
}

// #1063: upstream `global-timeline.ts` は reply gate を持たない。reply は
// 常に pass-through する。旧テストは drift 挙動の assertion だったので新
// semantics に揃える。
func TestGlobalTimeline_ReplyPassthrough(t *testing.T) {
	ctx := newCtx(nil)
	ch := NewGlobalTimeline(ctx)
	ch.Init(json.RawMessage(`{"withReplies":false}`))

	ch.OnRedisEvent([]byte(`{"text":"reply","replyId":"p1"}`))
	require.Len(t, ctx.sentType, 1)
	assert.Equal(t, "note", ctx.sentType[0])
}

func TestHomeTimeline_WithFiles(t *testing.T) {
	ctx := newCtx(&model.User{ID: "alice"})
	ch := NewHomeTimeline(ctx)
	ch.Init(json.RawMessage(`{"withFiles":true}`))

	// ファイルなしはフィルタされる
	ch.OnRedisEvent([]byte(`{"text":"hello","fileIds":[]}`))
	assert.Empty(t, ctx.sentType)

	// ファイルありは通過
	ch.OnRedisEvent([]byte(`{"text":"hello","fileIds":["f1"]}`))
	require.Len(t, ctx.sentType, 1)
}

func TestHybridTimeline_FilterDefault(t *testing.T) {
	ctx := newCtx(&model.User{ID: "alice"})
	ch := NewHybridTimeline(ctx)
	ch.Init(nil) // デフォルトフィルタ

	// 通常ノートは通過
	ch.OnRedisEvent([]byte(`{"text":"hello"}`))
	require.Len(t, ctx.sentType, 1)

	// リプライはデフォルトでフィルタ
	ch.OnRedisEvent([]byte(`{"text":"reply","replyId":"p1"}`))
	assert.Len(t, ctx.sentType, 1) // 増えない
}
