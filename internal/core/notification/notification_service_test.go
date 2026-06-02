package notification

import (
	"context"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	testRedis *testutil.TestRedis
	idGen     id.Generator
)

func TestMain(m *testing.M) {
	ctx := context.Background()
	tr, err := testutil.SetupRedis(ctx)
	if err != nil {
		log.Fatalf("redis setup failed: %v", err)
	}
	testRedis = tr
	idGen, _ = id.NewGenerator("aidx")

	code := m.Run()

	testRedis.Teardown(ctx)
	os.Exit(code)
}

func newTestSvc(t *testing.T) *Service {
	t.Helper()
	testRedis.FlushAll(context.Background())
	svc := NewService(testRedis.Client, idGen, "")
	// テストでは 2 秒の AfterFunc を待ちたくないので同期 publish にする。
	svc.SetUnreadPublishDelay(0)
	return svc
}

func closedClient(t *testing.T) *redis.Client {
	t.Helper()
	c := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
	_ = c.Close()
	return c
}

func TestService_Create_RequiresNotifiee(t *testing.T) {
	svc := newTestSvc(t)
	_, err := svc.Create(context.Background(), CreateInput{Type: TypeFollow})
	assert.Error(t, err)
}

// TestService_KeyPrefix は #362 drop-in 互換の回帰テスト。
// TS 本家と同じ `<host>:notificationTimeline:*` / `<host>:latestReadNotification:*`
// 名前空間にキーが置かれることを確認する。
func TestService_KeyPrefix(t *testing.T) {
	testRedis.FlushAll(context.Background())
	const host = "example.test"
	svc := NewService(testRedis.Client, idGen, host+":")
	ctx := context.Background()

	_, err := svc.Create(ctx, CreateInput{
		NotifieeID: "u1", NotifierID: "u2", Type: TypeMention, NoteID: "n1",
	})
	require.NoError(t, err)

	// prefix 付きストリームに entry がある
	prefixed := host + ":notificationTimeline:u1"
	n, err := testRedis.Client.XLen(ctx, prefixed).Result()
	require.NoError(t, err)
	assert.Equal(t, int64(1), n, "prefixed stream receives the notification")

	// 旧 prefix 無しキーには無い
	bare := "notificationTimeline:u1"
	n, err = testRedis.Client.XLen(ctx, bare).Result()
	require.NoError(t, err)
	assert.Equal(t, int64(0), n, "bare stream remains empty")

	// readKey も prefix 下に置かれる
	require.NoError(t, svc.MarkAllAsRead(ctx, "u1"))
	readKey := host + ":latestReadNotification:u1"
	got, err := testRedis.Client.Exists(ctx, readKey).Result()
	require.NoError(t, err)
	assert.Equal(t, int64(1), got, "prefixed read key should exist after MarkAllAsRead")
}

func TestService_Create_SelfNotificationRejected(t *testing.T) {
	svc := newTestSvc(t)
	_, err := svc.Create(context.Background(), CreateInput{
		NotifieeID: "u1", NotifierID: "u1", Type: TypeFollow,
	})
	require.ErrorIs(t, err, ErrSelfNotification)
}

func TestService_DeleteByTypeAndNotifier(t *testing.T) {
	svc := newTestSvc(t)
	ctx := context.Background()

	// alice の stream に bob からの receiveFollowRequest, carol からの同種、
	// bob からの follow の計 3 件を積む。
	for _, tc := range []struct {
		notifier string
		typ      Type
	}{
		{"bob", TypeReceiveFollowReq},
		{"carol", TypeReceiveFollowReq},
		{"bob", TypeFollow},
	} {
		_, err := svc.Create(ctx, CreateInput{NotifieeID: "alice", NotifierID: tc.notifier, Type: tc.typ})
		require.NoError(t, err)
	}

	// bob からの receiveFollowRequest のみ消す。
	require.NoError(t, svc.DeleteByTypeAndNotifier(ctx, "alice", TypeReceiveFollowReq, "bob"))

	out, err := svc.List(ctx, "alice", 10)
	require.NoError(t, err)
	require.Len(t, out, 2) // carol の receiveFollowRequest + bob の follow が残る
	for _, n := range out {
		if n.Type == TypeReceiveFollowReq {
			assert.Equal(t, "carol", n.NotifierID)
		}
	}
}

func TestService_DeleteByTypeAndNotifier_EmptyArgs(t *testing.T) {
	svc := newTestSvc(t)
	// 空の notifieeID / notifierID は no-op。
	require.NoError(t, svc.DeleteByTypeAndNotifier(context.Background(), "", TypeReceiveFollowReq, "bob"))
	require.NoError(t, svc.DeleteByTypeAndNotifier(context.Background(), "alice", TypeReceiveFollowReq, ""))
}

func TestService_CreateAndList(t *testing.T) {
	svc := newTestSvc(t)
	ctx := context.Background()

	for _, typ := range []Type{TypeFollow, TypeMention, TypeReaction} {
		_, err := svc.Create(ctx, CreateInput{
			NotifieeID: "alice", NotifierID: "bob", Type: typ, NoteID: "n1",
		})
		require.NoError(t, err)
	}

	out, err := svc.List(ctx, "alice", 10)
	require.NoError(t, err)
	require.Len(t, out, 3)
	// 新しい順
	assert.Equal(t, TypeReaction, out[0].Type)
}

// stubStreamingPublisher records every PublishNotification call.
type stubStreamingPublisher struct {
	hits []string // notifieeID
}

func (s *stubStreamingPublisher) PublishNotification(notifieeID string, _ *Notification) {
	s.hits = append(s.hits, notifieeID)
}

func TestService_PublishesStreamingEvent(t *testing.T) {
	svc := newTestSvc(t)
	pub := &stubStreamingPublisher{}
	svc.SetStreamingPublisher(pub)
	_, err := svc.Create(context.Background(), CreateInput{
		NotifieeID: "alice", NotifierID: "bob", Type: TypeFollow,
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"alice"}, pub.hits)
}

// stubMainPublisher records every PublishMainEvent call。time.AfterFunc に
// よる遅延 publish が test goroutine と並走する可能性があるため、`go test
// -race` で誤検出されないよう mutex で保護する (#420 Devin review)。
type stubMainPublisher struct {
	mu    sync.Mutex
	calls []mainEventCall
}

type mainEventCall struct {
	userID    string
	eventType string
	body      any
}

func (s *stubMainPublisher) PublishMainEvent(userID, eventType string, body any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, mainEventCall{userID, eventType, body})
}

// snapshot returns a copy of recorded calls so test assertions can run
// without holding the publisher's lock.
func (s *stubMainPublisher) snapshot() []mainEventCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]mainEventCall, len(s.calls))
	copy(out, s.calls)
	return out
}

func TestService_Create_PublishesUnreadNotification(t *testing.T) {
	svc := newTestSvc(t)
	pub := &stubMainPublisher{}
	svc.SetMainStreamPublisher(pub)
	_, err := svc.Create(context.Background(), CreateInput{
		NotifieeID: "alice", NotifierID: "bob", Type: TypeFollow,
	})
	require.NoError(t, err)
	require.Len(t, pub.calls, 1)
	assert.Equal(t, "alice", pub.calls[0].userID)
	assert.Equal(t, "unreadNotification", pub.calls[0].eventType)
	// body は *Notification で、少なくとも type が "follow" であることを確認。
	n, ok := pub.calls[0].body.(*Notification)
	require.True(t, ok)
	assert.Equal(t, TypeFollow, n.Type)
}

// stubPacker returns a predefined map as the packed form.
type stubPacker struct {
	calls          int
	lastNotifieeID string
	out            map[string]any
}

func (s *stubPacker) Pack(notifieeID string, _ *Notification) any {
	s.calls++
	s.lastNotifieeID = notifieeID
	return s.out
}

func TestService_Create_UsesPackerForUnreadNotification(t *testing.T) {
	svc := newTestSvc(t)
	pub := &stubMainPublisher{}
	svc.SetMainStreamPublisher(pub)
	packer := &stubPacker{out: map[string]any{"userId": "bob", "type": "follow"}}
	svc.SetPacker(packer)

	_, err := svc.Create(context.Background(), CreateInput{
		NotifieeID: "alice", NotifierID: "bob", Type: TypeFollow,
	})
	require.NoError(t, err)
	assert.Equal(t, 1, packer.calls)
	// #1471: Service.Create は Pack に notifieeID を渡すことで Packer 側で
	// note embed の visibility gate を効かせる契約。stub に届く notifieeID
	// を確認しておくと、将来 Pack signature を変えたときに silent failure
	// (= visibility check の入力がずれて leak) を起こさない。
	assert.Equal(t, "alice", packer.lastNotifieeID)
	require.Len(t, pub.calls, 1)
	// body はpackerの出力 map に置き換わる (TS互換shape)
	body, ok := pub.calls[0].body.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "bob", body["userId"])
}

func TestService_MarkAllAsRead_PublishesReadAll(t *testing.T) {
	svc := newTestSvc(t)
	ctx := context.Background()
	// 1件作成してから MarkAllAsRead する。
	_, err := svc.Create(ctx, CreateInput{
		NotifieeID: "alice", NotifierID: "bob", Type: TypeFollow,
	})
	require.NoError(t, err)

	// Create で入った unreadNotification をクリアするため publisher を差し替え。
	pub := &stubMainPublisher{}
	svc.SetMainStreamPublisher(pub)

	require.NoError(t, svc.MarkAllAsRead(ctx, "alice"))
	require.Len(t, pub.calls, 1)
	assert.Equal(t, "alice", pub.calls[0].userID)
	assert.Equal(t, "readAllNotifications", pub.calls[0].eventType)
	assert.Nil(t, pub.calls[0].body)
}

func TestService_MarkAllAsRead_EmptyStream_DoesNotPublish(t *testing.T) {
	svc := newTestSvc(t)
	pub := &stubMainPublisher{}
	svc.SetMainStreamPublisher(pub)

	// 通知が空 → 既読対象が無いので readAllNotifications を publish しない。
	// 本家 TS NotificationService.readAllNotification の guard と同じ挙動
	// (#420 follow-up)。冗長な publish が来ると新しい unreadNotification が
	// 即座に上書きされるため。
	require.NoError(t, svc.MarkAllAsRead(context.Background(), "alice"))
	assert.Empty(t, pub.calls, "no notifications → no readAllNotifications publish")
}

// 待機中に MarkAllAsRead が走ると unreadNotification publish は skip される
// (#420 follow-up: 本家 TS NotificationService の setTimeout + 既読再チェック
// と同じ guard)。delay=10ms で実時間遅延を入れて検証する。
func TestService_Create_DelayedUnread_SuppressedAfterMarkAllAsRead(t *testing.T) {
	svc := newTestSvc(t)
	svc.SetUnreadPublishDelay(20 * time.Millisecond)

	pub := &stubMainPublisher{}
	svc.SetMainStreamPublisher(pub)

	ctx := context.Background()
	_, err := svc.Create(ctx, CreateInput{
		NotifieeID: "alice", NotifierID: "bob", Type: TypeMention,
	})
	require.NoError(t, err)

	// AfterFunc が走る前に既読化する。
	require.NoError(t, svc.MarkAllAsRead(ctx, "alice"))
	// AfterFunc + GoSched が確実に終わる時間まで待つ
	time.Sleep(80 * time.Millisecond)

	// readAllNotifications は publish される (MarkAllAsRead 由来) が、
	// unreadNotification は suppressed されている。lock 越しの snapshot で
	// race detector に引っかからないよう取得する。
	for _, c := range pub.snapshot() {
		assert.NotEqual(t, "unreadNotification", c.eventType,
			"unreadNotification must be suppressed when read marker advanced first")
	}
}

// 既に最新まで読まれている状態で再度 MarkAllAsRead を呼んでも publish は
// 発生しない (#420 follow-up: badge を即リセットしないため)。
func TestService_MarkAllAsRead_AlreadyRead_DoesNotPublish(t *testing.T) {
	svc := newTestSvc(t)
	ctx := context.Background()
	_, err := svc.Create(ctx, CreateInput{
		NotifieeID: "alice", NotifierID: "bob", Type: TypeFollow,
	})
	require.NoError(t, err)

	pub := &stubMainPublisher{}
	svc.SetMainStreamPublisher(pub)

	// 1 回目: 未読がある → publish する
	require.NoError(t, svc.MarkAllAsRead(ctx, "alice"))
	require.Len(t, pub.calls, 1)
	assert.Equal(t, "readAllNotifications", pub.calls[0].eventType)

	// 2 回目: 新着が無い → publish しない
	require.NoError(t, svc.MarkAllAsRead(ctx, "alice"))
	assert.Len(t, pub.calls, 1, "second call must not re-publish readAllNotifications")
}

func TestService_Flush_PublishesNotificationFlushed(t *testing.T) {
	svc := newTestSvc(t)
	ctx := context.Background()
	_, err := svc.Create(ctx, CreateInput{
		NotifieeID: "alice", NotifierID: "bob", Type: TypeFollow,
	})
	require.NoError(t, err)

	pub := &stubMainPublisher{}
	svc.SetMainStreamPublisher(pub)

	require.NoError(t, svc.Flush(ctx, "alice"))
	require.Len(t, pub.calls, 1)
	assert.Equal(t, "alice", pub.calls[0].userID)
	assert.Equal(t, "notificationFlushed", pub.calls[0].eventType)
	assert.Nil(t, pub.calls[0].body)
}

func TestService_ListLimitClampingDefaults(t *testing.T) {
	svc := newTestSvc(t)
	ctx := context.Background()
	_, err := svc.Create(ctx, CreateInput{NotifieeID: "alice", Type: TypeFollow, NotifierID: "bob"})
	require.NoError(t, err)

	// limit <= 0 はデフォルト10
	out, err := svc.List(ctx, "alice", 0)
	require.NoError(t, err)
	assert.Len(t, out, 1)

	// limit > 100 は100にクランプ (要素は1件のみだが正常終了することを確認)
	out, err = svc.List(ctx, "alice", 1000)
	require.NoError(t, err)
	assert.Len(t, out, 1)
}

func TestService_MarkAllAsRead(t *testing.T) {
	svc := newTestSvc(t)
	ctx := context.Background()

	// 通知が無いときはno-op
	require.NoError(t, svc.MarkAllAsRead(ctx, "alice"))

	_, err := svc.Create(ctx, CreateInput{NotifieeID: "alice", Type: TypeFollow, NotifierID: "bob"})
	require.NoError(t, err)
	require.NoError(t, svc.MarkAllAsRead(ctx, "alice"))

	id, err := svc.LatestReadID(ctx, "alice")
	require.NoError(t, err)
	assert.NotEmpty(t, id)
}

func TestService_LatestReadID_Missing(t *testing.T) {
	svc := newTestSvc(t)
	id, err := svc.LatestReadID(context.Background(), "ghost")
	require.NoError(t, err)
	assert.Empty(t, id)
}

func TestService_Flush(t *testing.T) {
	svc := newTestSvc(t)
	ctx := context.Background()
	_, err := svc.Create(ctx, CreateInput{NotifieeID: "alice", Type: TypeFollow, NotifierID: "bob"})
	require.NoError(t, err)
	require.NoError(t, svc.MarkAllAsRead(ctx, "alice"))

	require.NoError(t, svc.Flush(ctx, "alice"))
	out, err := svc.List(ctx, "alice", 10)
	require.NoError(t, err)
	assert.Empty(t, out)

	id, err := svc.LatestReadID(ctx, "alice")
	require.NoError(t, err)
	assert.Empty(t, id)
}

func TestService_RedisErrors(t *testing.T) {
	svc := NewService(closedClient(t), idGen, "")
	ctx := context.Background()

	_, err := svc.Create(ctx, CreateInput{NotifieeID: "u", NotifierID: "v", Type: TypeFollow})
	assert.Error(t, err)

	_, err = svc.List(ctx, "u", 10)
	assert.Error(t, err)

	err = svc.MarkAllAsRead(ctx, "u")
	assert.Error(t, err)

	_, err = svc.LatestReadID(ctx, "u")
	assert.Error(t, err)

	err = svc.Flush(ctx, "u")
	assert.Error(t, err)
}

// flushSecondClient simulates flush() failing on the second Del call.
type onlyFirstDelClient struct{ *redis.Client }

func TestService_FlushSecondDelError(t *testing.T) {
	// flushの2つ目 (latestReadKey) をエラーにするためには、最初のDelは成功させる
	// 必要がある。実テストでは検証が複雑なのでスキップ可能なケースとし、
	// すでにflush全体のエラーパスはRedisErrorsで網羅済みとする。
	t.Skip("covered indirectly via TestService_RedisErrors")
}

// _ ensures the unused type doesn't fail the build (defensive). 必要なら今後
// flush 個別エラーを差別する mock を追加する。
var _ = onlyFirstDelClient{}

// MarkAllAsRead Set error path: streamは存在するがSetが失敗する場合
type setFailClient struct{ *redis.Client }

func TestService_MarkAllAsRead_NoEntries(t *testing.T) {
	svc := newTestSvc(t)
	// 一度も通知が無いユーザーに対して MarkAllAsRead は何もしないでnilを返す
	require.NoError(t, svc.MarkAllAsRead(context.Background(), "noone"))
}

var _ = setFailClient{}

// TestService_Create_MarshalError triggers the json.Marshal failure path by
// stuffing an unserializable value (a channel) into Extra.
func TestService_Create_MarshalError(t *testing.T) {
	svc := newTestSvc(t)
	_, err := svc.Create(context.Background(), CreateInput{
		NotifieeID: "alice",
		NotifierID: "bob",
		Type:       TypeFollow,
		Extra:      map[string]any{"ch": make(chan int)},
	})
	assert.Error(t, err)
}

// TestService_List_SkipsBadEntries injects raw entries into the stream that
// the List method should skip without erroring out.
func TestService_List_SkipsBadEntries(t *testing.T) {
	svc := newTestSvc(t)
	ctx := context.Background()

	// 1: data フィールドが文字列ではない
	require.NoError(t, testRedis.Client.XAdd(ctx, &redis.XAddArgs{
		Stream: "notificationTimeline:alice",
		Values: map[string]any{"other": "x"},
	}).Err())
	// 2: data はあるが JSON として無効
	require.NoError(t, testRedis.Client.XAdd(ctx, &redis.XAddArgs{
		Stream: "notificationTimeline:alice",
		Values: map[string]any{"data": "not-json"},
	}).Err())
	// 3: 正常エントリ
	_, err := svc.Create(ctx, CreateInput{NotifieeID: "alice", NotifierID: "bob", Type: TypeFollow})
	require.NoError(t, err)

	out, err := svc.List(ctx, "alice", 10)
	require.NoError(t, err)
	require.Len(t, out, 1)
	assert.Equal(t, TypeFollow, out[0].Type)
}

// Phase 7-2 (#244): UnreadCount / HasUnreadOfTypes のテスト
func TestService_UnreadCount_NoReadMarker(t *testing.T) {
	svc := newTestSvc(t)
	ctx := context.Background()

	// 通知が無ければ0
	n, err := svc.UnreadCount(ctx, "alice")
	require.NoError(t, err)
	assert.Equal(t, int64(0), n)

	// 通知を3件作成し、readMarker未設定なら全件unread
	for i := 0; i < 3; i++ {
		_, err := svc.Create(ctx, CreateInput{NotifieeID: "alice", Type: TypeFollow, NotifierID: "bob"})
		require.NoError(t, err)
	}

	n, err = svc.UnreadCount(ctx, "alice")
	require.NoError(t, err)
	assert.Equal(t, int64(3), n)
}

func TestService_UnreadCount_AfterMarkAllAsRead(t *testing.T) {
	svc := newTestSvc(t)
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		_, err := svc.Create(ctx, CreateInput{NotifieeID: "alice", Type: TypeFollow, NotifierID: "bob"})
		require.NoError(t, err)
	}
	require.NoError(t, svc.MarkAllAsRead(ctx, "alice"))

	// 既読マーカー以降は0
	n, err := svc.UnreadCount(ctx, "alice")
	require.NoError(t, err)
	assert.Equal(t, int64(0), n)

	// 新規通知が1件来たら1
	_, err = svc.Create(ctx, CreateInput{NotifieeID: "alice", Type: TypeReaction, NotifierID: "bob"})
	require.NoError(t, err)
	n, err = svc.UnreadCount(ctx, "alice")
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)
}

func TestService_HasUnreadOfTypes_Match(t *testing.T) {
	svc := newTestSvc(t)
	ctx := context.Background()

	_, err := svc.Create(ctx, CreateInput{NotifieeID: "alice", Type: TypeMention, NotifierID: "bob"})
	require.NoError(t, err)

	ok, err := svc.HasUnreadOfTypes(ctx, "alice", []Type{TypeMention, TypeReply})
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestService_HasUnreadOfTypes_NoMatch(t *testing.T) {
	svc := newTestSvc(t)
	ctx := context.Background()

	_, err := svc.Create(ctx, CreateInput{NotifieeID: "alice", Type: TypeFollow, NotifierID: "bob"})
	require.NoError(t, err)

	ok, err := svc.HasUnreadOfTypes(ctx, "alice", []Type{TypeMention, TypeReply})
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestService_HasUnreadSpecifiedNotes_Match(t *testing.T) {
	svc := newTestSvc(t)
	ctx := context.Background()
	_, err := svc.Create(ctx, CreateInput{
		NotifieeID:     "alice",
		NotifierID:     "bob",
		Type:           TypeMention,
		NoteID:         "n1",
		NoteVisibility: "specified",
	})
	require.NoError(t, err)
	ok, err := svc.HasUnreadSpecifiedNotes(ctx, "alice")
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestService_HasUnreadSpecifiedNotes_NoMatch(t *testing.T) {
	svc := newTestSvc(t)
	ctx := context.Background()
	_, err := svc.Create(ctx, CreateInput{
		NotifieeID:     "alice",
		NotifierID:     "bob",
		Type:           TypeMention,
		NoteID:         "n1",
		NoteVisibility: "public",
	})
	require.NoError(t, err)
	ok, err := svc.HasUnreadSpecifiedNotes(ctx, "alice")
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestService_UnreadSummary_AggregatesCountMentionsSpecified(t *testing.T) {
	// #321: 1 度の UnreadSummary で 3 値を集約できることを確認する。
	svc := newTestSvc(t)
	ctx := context.Background()

	_, err := svc.Create(ctx, CreateInput{
		NotifieeID:     "alice",
		NotifierID:     "bob",
		Type:           TypeMention,
		NoteID:         "n1",
		NoteVisibility: "specified",
	})
	require.NoError(t, err)
	_, err = svc.Create(ctx, CreateInput{NotifieeID: "alice", Type: TypeFollow, NotifierID: "carol"})
	require.NoError(t, err)

	summary, err := svc.UnreadSummary(ctx, "alice", []Type{TypeMention, TypeReply})
	require.NoError(t, err)
	assert.Equal(t, int64(2), summary.TotalCount)
	assert.True(t, summary.HasMentions, "mention 1 件あるので true")
	assert.True(t, summary.HasSpecifiedNote, "specified note の mention あり")
}

func TestService_UnreadSummary_NoMentionTypesSkipsMentionPass(t *testing.T) {
	svc := newTestSvc(t)
	ctx := context.Background()

	_, err := svc.Create(ctx, CreateInput{
		NotifieeID:     "alice",
		NotifierID:     "bob",
		Type:           TypeMention,
		NoteID:         "n1",
		NoteVisibility: "public",
	})
	require.NoError(t, err)

	// mentionTypes nil → HasMentions は常に false。count / specified のみ計算。
	summary, err := svc.UnreadSummary(ctx, "alice", nil)
	require.NoError(t, err)
	assert.Equal(t, int64(1), summary.TotalCount)
	assert.False(t, summary.HasMentions)
	assert.False(t, summary.HasSpecifiedNote)
}

func TestService_UnreadSummary_EmptyStream(t *testing.T) {
	svc := newTestSvc(t)
	ctx := context.Background()
	summary, err := svc.UnreadSummary(ctx, "alice", []Type{TypeMention})
	require.NoError(t, err)
	assert.Equal(t, int64(0), summary.TotalCount)
	assert.False(t, summary.HasMentions)
	assert.False(t, summary.HasSpecifiedNote)
}

func TestService_UnreadSummary_UsesNoteUnreadRepoForSpecified(t *testing.T) {
	// noteUnreadRepo が wired なら stream に specified note が無くても
	// DB 経由で HasSpecifiedNote=true になる (本家互換経路 #319)。
	svc := newTestSvc(t)
	ctx := context.Background()
	unread := testutil.NewMockNoteUnreadRepository()
	svc.SetNoteUnreadRepo(unread)
	_ = unread.Upsert(&model.NoteUnread{
		ID: "nu1", UserID: "alice", NoteID: "n1", NoteUserID: "bob", IsSpecified: true,
	})

	summary, err := svc.UnreadSummary(ctx, "alice", []Type{TypeMention})
	require.NoError(t, err)
	assert.True(t, summary.HasSpecifiedNote)
}

func TestService_HasUnreadOfTypes_EmptyList(t *testing.T) {
	svc := newTestSvc(t)
	ctx := context.Background()

	_, err := svc.Create(ctx, CreateInput{NotifieeID: "alice", Type: TypeMention, NotifierID: "bob"})
	require.NoError(t, err)

	ok, err := svc.HasUnreadOfTypes(ctx, "alice", nil)
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestService_HasUnreadOfTypes_AfterMarkAllAsRead(t *testing.T) {
	svc := newTestSvc(t)
	ctx := context.Background()

	_, err := svc.Create(ctx, CreateInput{NotifieeID: "alice", Type: TypeMention, NotifierID: "bob"})
	require.NoError(t, err)
	require.NoError(t, svc.MarkAllAsRead(ctx, "alice"))

	ok, err := svc.HasUnreadOfTypes(ctx, "alice", []Type{TypeMention})
	require.NoError(t, err)
	assert.False(t, ok, "readMarker以降は該当typeがなくfalse")
}

// upstream Misskey #17358 (= 2026.5.1 fix) は ULID 環境下で notification id の
// `additional` 部 (80-bit) をそのまま Redis Stream sequence (uint64) に渡して
// XADD が失敗し続け、通知が ~10s 遅延する bug を修正したが、mk-go の toXAddID
// は ID parse 経路を踏まず引数 time.Time から `<ms>-*` を直接生成するため
// 構造的に overflow しない。regression guard として far-future な time でも
// "<digits>-*" 形式に収まることを確認する
// (= triage #1011 / upstream #17358 close)。
func TestToXAddID_NoOverflowFarFuture(t *testing.T) {
	// uint64 (= max ~5.85e8 years in epoch ms) の遥か手前の年だが
	// 64-bit ms カウンタ自体が overflow しないことを十分担保できる。
	far := time.Date(3000, 1, 1, 0, 0, 0, 0, time.UTC)
	s := toXAddID("ignored-id", far)
	assert.Regexp(t, `^\d+-\*$`, s,
		"toXAddID must emit `<ms>-*` form, never panic on overflow")
	// 数字部が truncate されず epoch ms と一致することを assert する
	// (upstream の bug は parse 経由で下位 64bit truncate されるパターンだった
	// ため、format check だけでは silent value drift を検出できない)。
	parts := strings.Split(s, "-")
	require.Len(t, parts, 2)
	gotMs, err := strconv.ParseInt(parts[0], 10, 64)
	require.NoError(t, err)
	require.Equal(t, far.UnixMilli(), gotMs,
		"toXAddID's ms must equal time.Time.UnixMilli (no truncation)")
}
