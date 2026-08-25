package timeline

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/testutil"
)

// ephemeral の TTL が切れると note の実体だけが消え、ID は timeline の Redis list に
// 残る。実運用の home timeline では ID の**約半分**がこの状態になっていた (#2715)。
//
// resolve は解決できなかった ID を呼び出し側へ返し、list から消せるようにする。
func TestResolve_ReportsDanglingIDs(t *testing.T) {
	t.Run("ephemeral wired: gone from both is dangling", func(t *testing.T) {
		svc, _ := newMergeService(t, []string{"b"})
		svc.SetEphemeralLookup(&stubEphemeral{notes: map[string]*model.Note{"c": ephNote("c")}})

		notes, dangling, err := svc.resolve(context.Background(), []string{"c", "b", "a"})
		require.NoError(t, err)
		assert.Len(t, notes, 2)
		assert.Equal(t, []string{"a"}, dangling)
	})

	t.Run("no ephemeral store: a DB miss is dangling", func(t *testing.T) {
		svc, _ := newMergeService(t, []string{"b"})

		notes, dangling, err := svc.resolve(context.Background(), []string{"b", "a"})
		require.NoError(t, err)
		assert.Len(t, notes, 1)
		assert.Equal(t, []string{"a"}, dangling)
	})

	// **Redis 障害では何も返さない。** 一時障害で生きている note の ID を消すと
	// 取り返しがつかない。
	t.Run("ephemeral lookup failure reports nothing", func(t *testing.T) {
		svc, _ := newMergeService(t, []string{"b"})
		svc.SetEphemeralLookup(&stubEphemeral{err: errors.New("redis down")})

		notes, dangling, err := svc.resolve(context.Background(), []string{"b", "a"})
		require.NoError(t, err)
		assert.Len(t, notes, 1)
		assert.Empty(t, dangling, "障害時に消す候補を出してはいけない")
	})

	t.Run("all resolved reports nothing", func(t *testing.T) {
		svc, _ := newMergeService(t, []string{"a", "b"})
		notes, dangling, err := svc.resolve(context.Background(), []string{"b", "a"})
		require.NoError(t, err)
		assert.Len(t, notes, 2)
		assert.Empty(t, dangling)
	})
}

// **境界は「解決できた note」から取る** (#2715)。
//
// 解決できない古い ID を境界にすると、DB fallback が「その ID より古いもの」に
// 絞られ、**新しい投稿が DB にあっても返らなくなる**。実運用ではこれでリレー由来
// でない投稿まで出てこなくなっていた。
func TestFallbackRange_IgnoresUnresolvedIDs(t *testing.T) {
	// Redis は古い ID を返したが 1 件も解決できなかった → 境界を使わない。
	fbSince, fbUntil := fallbackRange(nil, "", "u")
	assert.Equal(t, "", fbSince)
	assert.Equal(t, "u", fbUntil, "解決 0 件なら呼び出し側の until をそのまま渡す")

	// 一部だけ解決できたら、その最古を境界にする (解決できなかった古い ID ではなく)。
	resolved := []*model.Note{{ID: "c"}, {ID: "b"}}
	_, fbUntil = fallbackRange(resolved, "", "u")
	assert.Equal(t, "b", fbUntil)
}

// 解決できない ID が list に残っていても、最新の DB note が返ること (#2715)。
//
// 修正前は Redis が返した最古の ID (= 解決できない古い ID) が DB fallback の
// untilID になり、それより新しい note が DB にあっても返らなかった。
func TestHomeTimeline_DanglingIDsDoNotHideNewerNotes(t *testing.T) {
	repo := testutil.NewMockNoteRepository()
	// 宙吊り ID (aqa…) より **新しい** DB note。
	newer := &model.Note{ID: idGen.Generate(time.Now()), UserID: "lu", Visibility: model.NoteVisibilityPublic}
	repo.Notes[newer.ID] = newer

	testutil.SkipIfNoDocker(t)
	testRedis.FlushAll(context.Background())
	fanout := NewFanoutTimelineService(testRedis.Client, idGen, "")
	fanout.randFn = func() float64 { return 1.0 }
	svc := NewService(fanout, repo, testutil.NewMockFollowingRepository())

	viewer := &model.User{ID: "lu"}
	// 解決できない古い ID だけが list に載っている状態を作る。
	dangling := idGen.Generate(time.Now().Add(-time.Hour))
	require.NoError(t, fanout.Push(context.Background(), HomeTimelineName(viewer.ID), dangling, 100))
	notes, err := svc.HomeTimeline(context.Background(), viewer, "", "", 20, TimelineFilter{})
	require.NoError(t, err)

	ids := make([]string, 0, len(notes))
	for _, n := range notes {
		ids = append(ids, n.ID)
	}
	assert.Contains(t, ids, newer.ID,
		"解決できない古い ID が境界になり、新しい note が隠れている (#2715)")
}

// 解決できない ID を list から実際に取り除くこと (自己修復、#2715)。
//
// 黙って落とすだけだと汚染が永続する。実運用では 30 キー中 27 キーが「全 ID が
// 解決できない」状態まで溜まっていた。
func TestHomeTimeline_PrunesDanglingIDsFromRedis(t *testing.T) {
	testutil.SkipIfNoDocker(t)
	testRedis.FlushAll(context.Background())
	ctx := context.Background()

	repo := testutil.NewMockNoteRepository()
	alive := &model.Note{ID: idGen.Generate(time.Now()), UserID: "lu", Visibility: model.NoteVisibilityPublic}
	repo.Notes[alive.ID] = alive

	fanout := NewFanoutTimelineService(testRedis.Client, idGen, "")
	fanout.randFn = func() float64 { return 1.0 }
	svc := NewService(fanout, repo, testutil.NewMockFollowingRepository())

	viewer := &model.User{ID: "lu"}
	name := HomeTimelineName(viewer.ID)
	dangling := idGen.Generate(time.Now().Add(-time.Hour))
	require.NoError(t, fanout.Push(ctx, name, dangling, 100))
	require.NoError(t, fanout.Push(ctx, name, alive.ID, 100))

	before, err := fanout.Get(ctx, name, "", "", 10)
	require.NoError(t, err)
	require.Contains(t, before, dangling, "前提: 宙吊り ID が list にある")

	_, err = svc.HomeTimeline(ctx, viewer, "", "", 20, TimelineFilter{})
	require.NoError(t, err)

	after, err := fanout.Get(ctx, name, "", "", 10)
	require.NoError(t, err)
	assert.NotContains(t, after, dangling, "解決できない ID が list に残っている (#2715)")
	assert.Contains(t, after, alive.ID, "解決できた ID まで消してはいけない")
}

// **Redis 障害では消さない。** 一時障害で生きている note の ID を消すと
// 取り返しがつかない (#2715)。
func TestHomeTimeline_KeepsIDsWhenEphemeralLookupFails(t *testing.T) {
	testutil.SkipIfNoDocker(t)
	testRedis.FlushAll(context.Background())
	ctx := context.Background()

	fanout := NewFanoutTimelineService(testRedis.Client, idGen, "")
	fanout.randFn = func() float64 { return 1.0 }
	svc := NewService(fanout, testutil.NewMockNoteRepository(), testutil.NewMockFollowingRepository())
	svc.SetEphemeralLookup(&stubEphemeral{err: errors.New("redis down")})

	viewer := &model.User{ID: "lu"}
	name := HomeTimelineName(viewer.ID)
	id := idGen.Generate(time.Now())
	require.NoError(t, fanout.Push(ctx, name, id, 100))

	_, err := svc.HomeTimeline(ctx, viewer, "", "", 20, TimelineFilter{})
	require.NoError(t, err)

	after, err := fanout.Get(ctx, name, "", "", 10)
	require.NoError(t, err)
	assert.Contains(t, after, id, "ephemeral の一時障害で ID を消してはいけない")
}

// 同じ ID が list に重複して載っていても全て取り除くこと (#2715)。
//
// Push は重複を弾かないので、配送のリトライ等で同じ ID が複数回入りうる。
// LREM の count を 1 にすると 1 つ残り、次の読み取りでまた解決に失敗する
// (= 自己修復が完了しない)。
func TestRemoveMany_DropsEveryOccurrence(t *testing.T) {
	testutil.SkipIfNoDocker(t)
	testRedis.FlushAll(context.Background())
	ctx := context.Background()

	fanout := NewFanoutTimelineService(testRedis.Client, idGen, "")
	name := GlobalTimeline
	key := fanout.key(name)
	dup := idGen.Generate(time.Now())
	keep := idGen.Generate(time.Now())
	require.NoError(t, testRedis.Client.LPush(ctx, key, dup, keep, dup).Err())

	fanout.RemoveMany(ctx, []Name{name}, []string{dup})

	got, err := fanout.Get(ctx, name, "", "", 10)
	require.NoError(t, err)
	assert.NotContains(t, got, dup, "重複した ID が残っている")
	assert.Contains(t, got, keep)
}
