package channels

import (
	"encoding/json"
	"testing"

	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/stream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

	ch.OnRedisEvent([]byte(`{"id":"n1"}`))
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

// --- Antenna ---

func TestAntenna_Lifecycle(t *testing.T) {
	ctx := newCtx(&model.User{ID: "alice"})
	ch := NewAntenna(ctx)
	ch.Init(json.RawMessage(`{"antennaId":"a1"}`))
	assert.Equal(t, []string{"antennaTimeline:a1"}, ctx.subs)

	ch.OnRedisEvent([]byte(`{"id":"n1"}`))
	require.Len(t, ctx.sentType, 1)

	ch.Dispose()
	assert.Equal(t, []string{"antennaTimeline:a1"}, ctx.unsubs)
}

func TestAntenna_NoAuth(t *testing.T) {
	ctx := newCtx(nil)
	ch := NewAntenna(ctx)
	err := ch.Init(json.RawMessage(`{"antennaId":"a1"}`))
	assert.ErrorIs(t, err, stream.ErrInvalidParams)
	assert.Empty(t, ctx.subs)
}

func TestAntenna_MissingID(t *testing.T) {
	ctx := newCtx(&model.User{ID: "alice"})
	ch := NewAntenna(ctx)
	err := ch.Init(json.RawMessage(`{}`))
	assert.ErrorIs(t, err, stream.ErrInvalidParams)
	assert.Empty(t, ctx.subs)
}

// --- ChannelTimeline ---

func TestChannelTimeline_Lifecycle(t *testing.T) {
	ctx := newCtx(nil)
	ch := NewChannelTimeline(ctx)
	ch.Init(json.RawMessage(`{"channelId":"ch1"}`))
	assert.Equal(t, []string{"channel:ch1"}, ctx.subs)

	ch.OnRedisEvent([]byte(`{"id":"n1"}`))
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

// --- RoleTimeline ---

func TestRoleTimeline_Lifecycle(t *testing.T) {
	ctx := newCtx(nil)
	ch := NewRoleTimeline(ctx)
	ch.Init(json.RawMessage(`{"roleId":"r1"}`))
	assert.Equal(t, []string{"roleTimeline:r1"}, ctx.subs)

	ch.OnRedisEvent([]byte(`{"id":"n1"}`))
	require.Len(t, ctx.sentType, 1)

	ch.Dispose()
	assert.Equal(t, []string{"roleTimeline:r1"}, ctx.unsubs)
}

func TestRoleTimeline_MissingID(t *testing.T) {
	ctx := newCtx(nil)
	ch := NewRoleTimeline(ctx)
	// TS本家はroleId欠如時もinit成功とするため、errorは返さずno-op
	err := ch.Init(json.RawMessage(`{}`))
	assert.NoError(t, err)
	assert.Empty(t, ctx.subs)
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
		{"antenna", NewAntenna(newCtx(&model.User{ID: "u1"}))},
		{"channelTimeline", NewChannelTimeline(newCtx(nil))},
		{"userList", NewUserList(newCtx(&model.User{ID: "u1"}))},
		{"roleTimeline", NewRoleTimeline(newCtx(nil))},
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
	ch.OnRedisEvent([]byte(`{"text":"reply","replyId":"p1"}`))
	require.Len(t, ctx.sentType, 1)
	assert.Equal(t, "note", ctx.sentType[0])
}

func TestRoleTimeline_FilteredRenote(t *testing.T) {
	ctx := newCtx(nil)
	ch := NewRoleTimeline(ctx)
	ch.Init(json.RawMessage(`{"roleId":"r1","withRenotes":false}`))
	ch.OnRedisEvent([]byte(`{"renoteId":"r1","fileIds":[]}`))
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
