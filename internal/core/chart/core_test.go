package chart

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// notesSchema mirrors the upstream "notes" entity in a slimmed-down
// form sufficient for engine-level tests.
var notesSchema = Schema{
	Name: "notes",
	Columns: []ColumnDef{
		{Name: "local.total", Accumulate: true},
		{Name: "local.inc"},
		{Name: "local.dec"},
		{Name: "local.diffs.normal"},
		{Name: "remote.total", Accumulate: true},
	},
}

// activeUsersSchema covers the uniqueIncrement + intersection paths.
var activeUsersSchema = Schema{
	Name: "activeUsers",
	Columns: []ColumnDef{
		{Name: "readWrite", IntersectionOf: []string{"read", "write"}},
		{Name: "read", UniqueIncrement: true},
		{Name: "write", UniqueIncrement: true},
	},
}

// perUserNotesSchema is a small grouped schema.
var perUserNotesSchema = Schema{
	Name:    "perUserNotes",
	Grouped: true,
	Columns: []ColumnDef{
		{Name: "total", Accumulate: true},
		{Name: "inc"},
	},
}

func newTestChart(t *testing.T, schema Schema) (*Chart, *fakeRepo, *fakeClock) {
	t.Helper()
	repo := newFakeRepo()
	clk := newFakeClock(time.Date(2026, 4, 9, 12, 30, 0, 0, time.UTC))
	c, err := New(Config{
		Schema: schema,
		Repo:   repo,
		Lock:   NewMemoryLocker(),
		Clock:  clk,
	})
	require.NoError(t, err)
	return c, repo, clk
}

func TestNew_RequiresFields(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Error("expected error for empty schema name")
	}
	if _, err := New(Config{Schema: Schema{Name: "x"}}); err == nil {
		t.Error("expected error for missing repo")
	}
	if _, err := New(Config{Schema: Schema{Name: "x"}, Repo: newFakeRepo()}); err == nil {
		t.Error("expected error for missing lock")
	}
	c, err := New(Config{
		Schema: Schema{Name: "x"},
		Repo:   newFakeRepo(),
		Lock:   NewMemoryLocker(),
	})
	require.NoError(t, err)
	assert.Equal(t, "x", c.Name())
}

func TestCommit_Validation(t *testing.T) {
	c, _, _ := newTestChart(t, notesSchema)
	// ungrouped: group must be empty
	if err := c.Commit(Diff{"local.inc": 1}, "u1"); err == nil {
		t.Error("expected error for non-empty group on ungrouped chart")
	}

	g, _, _ := newTestChart(t, perUserNotesSchema)
	if err := g.Commit(Diff{"inc": 1}, ""); err == nil {
		t.Error("expected error for empty group on grouped chart")
	}
}

func TestCommit_NilChart(t *testing.T) {
	var c *Chart
	require.NoError(t, c.Commit(nil, ""))
}

func TestCommit_ZerosAndEmptySlicesDropped(t *testing.T) {
	c, _, _ := newTestChart(t, notesSchema)
	require.NoError(t, c.Commit(Diff{
		"local.inc":          int(0),
		"local.dec":          int64(0),
		"local.diffs.normal": []string{},
	}, ""))
	c.bufMu.Lock()
	defer c.bufMu.Unlock()
	if len(c.buffer) != 0 {
		t.Errorf("expected empty buffer, got %d entries", len(c.buffer))
	}
}

func TestSave_NoBuffer(t *testing.T) {
	c, _, _ := newTestChart(t, notesSchema)
	require.NoError(t, c.Save(context.Background()))
}

func TestSave_AccumulatesAndPersists(t *testing.T) {
	c, repo, _ := newTestChart(t, notesSchema)
	require.NoError(t, c.Commit(Diff{"local.inc": 1, "local.diffs.normal": 1, "local.total": 1}, ""))
	require.NoError(t, c.Commit(Diff{"local.inc": 2, "local.total": 2}, ""))
	require.NoError(t, c.Save(context.Background()))

	// 同じバケットに書き込まれているはず
	rows := repo.hour[""]
	require.Len(t, rows, 1)
	row := rows[0]
	assert.Equal(t, int64(3), toInt64(row.Cols["local.inc"]))
	assert.Equal(t, int64(1), toInt64(row.Cols["local.diffs.normal"]))
	assert.Equal(t, int64(3), toInt64(row.Cols["local.total"]))

	// day side も同じ
	dayRows := repo.day[""]
	require.Len(t, dayRows, 1)
	assert.Equal(t, int64(3), toInt64(dayRows[0].Cols["local.inc"]))
}

func TestSave_AccumulateInheritance(t *testing.T) {
	c, repo, clk := newTestChart(t, notesSchema)
	// 1時間目: total += 5
	require.NoError(t, c.Commit(Diff{"local.total": 5}, ""))
	require.NoError(t, c.Save(context.Background()))

	// 2時間進める → 新しい hour bucket では accumulate を引き継いで 5 から始まる
	clk.advance(2 * time.Hour)
	require.NoError(t, c.Commit(Diff{"local.total": 3}, ""))
	require.NoError(t, c.Save(context.Background()))

	require.Len(t, repo.hour[""], 2)
	// rows are insert-ordered: oldest first
	first := repo.hour[""][0]
	second := repo.hour[""][1]
	assert.Equal(t, int64(5), toInt64(first.Cols["local.total"]))
	// second bucket inherits 5 then adds 3 -> 8
	assert.Equal(t, int64(8), toInt64(second.Cols["local.total"]))
}

func TestSave_GroupedChart(t *testing.T) {
	c, repo, _ := newTestChart(t, perUserNotesSchema)
	require.NoError(t, c.Commit(Diff{"inc": 1, "total": 1}, "u1"))
	require.NoError(t, c.Commit(Diff{"inc": 2, "total": 2}, "u2"))
	require.NoError(t, c.Commit(Diff{"inc": 3, "total": 3}, "u1"))
	require.NoError(t, c.Save(context.Background()))

	require.Len(t, repo.hour["u1"], 1)
	require.Len(t, repo.hour["u2"], 1)
	assert.Equal(t, int64(4), toInt64(repo.hour["u1"][0].Cols["inc"]))
	assert.Equal(t, int64(2), toInt64(repo.hour["u2"][0].Cols["inc"]))
}

func TestSave_UniqueIncrementAndIntersection(t *testing.T) {
	c, repo, _ := newTestChart(t, activeUsersSchema)
	require.NoError(t, c.Commit(Diff{"read": []string{"u1", "u2"}, "write": []string{"u1"}}, ""))
	require.NoError(t, c.Commit(Diff{"read": []string{"u2", "u3"}, "write": []string{"u3"}}, ""))
	require.NoError(t, c.Save(context.Background()))

	row := repo.hour[""][0]
	// read set = {u1,u2,u3} → cardinality 3
	assert.Equal(t, int64(3), toInt64(row.Cols["read"]))
	// write set = {u1,u3} → 2
	assert.Equal(t, int64(2), toInt64(row.Cols["write"]))
	// intersection = {u1}∩{u2,u3,...} → wait it's read∩write = {u1,u3} → 2
	assert.Equal(t, int64(2), toInt64(row.Cols["readWrite"]))

	// unique-temp 配列も保持されている
	uniqueRead, _ := row.Cols["read:unique"].([]string)
	assert.ElementsMatch(t, []string{"u1", "u2", "u2", "u3"}, uniqueRead)
}

func TestSave_IntersectionAcrossBucketAndDiff(t *testing.T) {
	c, repo, _ := newTestChart(t, activeUsersSchema)
	require.NoError(t, c.Commit(Diff{"read": []string{"u1"}, "write": []string{"u1"}}, ""))
	require.NoError(t, c.Save(context.Background()))

	// 2回目の Commit。row には既に {u1} が積まれている。新しい diff は read=u2 のみ
	// read=∪({u1,u2}) write=∪({u1}) → intersection={u1}
	require.NoError(t, c.Commit(Diff{"read": []string{"u2"}}, ""))
	require.NoError(t, c.Save(context.Background()))
	row := repo.hour[""][0]
	assert.Equal(t, int64(1), toInt64(row.Cols["readWrite"]))
}

func TestSave_RepoErrorBubblesUp(t *testing.T) {
	c, repo, _ := newTestChart(t, notesSchema)
	repo.armError("FindCurrent", errors.New("boom"))
	require.NoError(t, c.Commit(Diff{"local.inc": 1}, ""))
	if err := c.Save(context.Background()); err == nil {
		t.Fatal("expected error")
	}
}

func TestTick_NoTickFunc(t *testing.T) {
	c, _, _ := newTestChart(t, notesSchema)
	require.NoError(t, c.Tick(context.Background(), true, ""))
}

func TestTick_GroupValidation(t *testing.T) {
	c, _, _ := newTestChart(t, perUserNotesSchema)
	c.tick = func(_ context.Context, _ string, _ bool) (map[string]int64, error) { return nil, nil }
	if err := c.Tick(context.Background(), true, ""); err == nil {
		t.Fatal("expected error for empty group on grouped chart")
	}
}

func TestTick_TickFuncErrorBubbles(t *testing.T) {
	c, _, _ := newTestChart(t, notesSchema)
	c.tick = func(_ context.Context, _ string, _ bool) (map[string]int64, error) {
		return nil, errors.New("tick boom")
	}
	if err := c.Tick(context.Background(), false, ""); err == nil {
		t.Fatal("expected error from tick func")
	}
}

func TestTick_NoColsIsNoOp(t *testing.T) {
	c, repo, _ := newTestChart(t, notesSchema)
	c.tick = func(_ context.Context, _ string, _ bool) (map[string]int64, error) {
		return map[string]int64{}, nil
	}
	require.NoError(t, c.Tick(context.Background(), false, ""))
	assert.Empty(t, repo.hour)
}

func TestTick_WritesAbsoluteValues(t *testing.T) {
	c, repo, _ := newTestChart(t, notesSchema)
	c.tick = func(_ context.Context, _ string, major bool) (map[string]int64, error) {
		assert.True(t, major)
		return map[string]int64{"local.total": 42, "remote.total": 7}, nil
	}
	require.NoError(t, c.Resync(context.Background(), ""))
	row := repo.hour[""][0]
	assert.Equal(t, int64(42), toInt64(row.Cols["local.total"]))
	assert.Equal(t, int64(7), toInt64(row.Cols["remote.total"]))
}

func TestClean_NoUniqueColumns(t *testing.T) {
	c, _, _ := newTestChart(t, notesSchema)
	require.NoError(t, c.Clean(context.Background()))
}

func TestClean_ResetsUniqueTempInsideWindow(t *testing.T) {
	c, repo, clk := newTestChart(t, activeUsersSchema)

	// Insert 3 rows: 4 days ago, 2 days ago (in window), today.
	for i, daysAgo := range []int{4, 2, 0} {
		clk.set(time.Date(2026, 4, 9-daysAgo, 12, 0, 0, 0, time.UTC))
		require.NoError(t, c.Commit(Diff{"read": []string{"u1"}}, ""))
		require.NoError(t, c.Save(context.Background()))
		_ = i
	}
	clk.set(time.Date(2026, 4, 9, 12, 0, 0, 0, time.UTC))
	require.NoError(t, c.Clean(context.Background()))

	// Find the 2-days-ago row and check the unique-temp was reset.
	cleared := 0
	for _, row := range repo.hour[""] {
		if v, ok := row.Cols["read:unique"].([]string); ok && len(v) == 0 {
			cleared++
		}
	}
	assert.Equal(t, 1, cleared)
}

func TestGetChart_BasicWindow(t *testing.T) {
	c, _, clk := newTestChart(t, notesSchema)
	// 3 連続 hour に書き込み
	for i := range 3 {
		clk.set(time.Date(2026, 4, 9, 10+i, 30, 0, 0, time.UTC))
		require.NoError(t, c.Commit(Diff{"local.inc": int64(i + 1)}, ""))
		require.NoError(t, c.Save(context.Background()))
	}
	clk.set(time.Date(2026, 4, 9, 12, 30, 0, 0, time.UTC))

	out, err := c.GetChart(context.Background(), SpanHour, 3, nil, "")
	require.NoError(t, err)
	// upstream の getChartRaw と同じく newest-first で返す (#470 / #473)。
	// hour12=3 → idx0, hour11=2 → idx1, hour10=1 → idx2。
	assert.Equal(t, []int64{3, 2, 1}, out["local.inc"])
}

func TestGetChart_InterpolatesGapsForAccumulate(t *testing.T) {
	c, _, clk := newTestChart(t, notesSchema)
	// 1 つだけ書き込んで、その後 2 時間ジャンプ
	clk.set(time.Date(2026, 4, 9, 10, 0, 0, 0, time.UTC))
	require.NoError(t, c.Commit(Diff{"local.total": 5}, ""))
	require.NoError(t, c.Save(context.Background()))

	clk.set(time.Date(2026, 4, 9, 12, 30, 0, 0, time.UTC))
	out, err := c.GetChart(context.Background(), SpanHour, 3, nil, "")
	require.NoError(t, err)
	// total は accumulate なので 5 が継続。inc は 0 で埋まる。
	assert.Equal(t, []int64{5, 5, 5}, out["local.total"])
	assert.Equal(t, []int64{0, 0, 0}, out["local.inc"])
}

func TestGetChart_FallbackWhenNoRowsInRange(t *testing.T) {
	c, _, clk := newTestChart(t, notesSchema)
	// 3日前に1つだけ書き込んだあと、現在に飛んで 1 時間幅で取得
	clk.set(time.Date(2026, 4, 6, 12, 0, 0, 0, time.UTC))
	require.NoError(t, c.Commit(Diff{"local.total": 9}, ""))
	require.NoError(t, c.Save(context.Background()))

	clk.set(time.Date(2026, 4, 9, 12, 30, 0, 0, time.UTC))
	out, err := c.GetChart(context.Background(), SpanHour, 2, nil, "")
	require.NoError(t, err)
	// fallback row の値が accumulate 列に伝播する
	assert.Equal(t, []int64{9, 9}, out["local.total"])
}

func TestGetChart_CursorOverridesNow(t *testing.T) {
	c, _, clk := newTestChart(t, notesSchema)
	clk.set(time.Date(2026, 4, 9, 10, 0, 0, 0, time.UTC))
	require.NoError(t, c.Commit(Diff{"local.inc": 7}, ""))
	require.NoError(t, c.Save(context.Background()))

	clk.set(time.Date(2026, 4, 9, 23, 0, 0, 0, time.UTC))
	cursor := time.Date(2026, 4, 9, 10, 0, 0, 0, time.UTC)
	out, err := c.GetChart(context.Background(), SpanHour, 1, &cursor, "")
	require.NoError(t, err)
	assert.Equal(t, []int64{7}, out["local.inc"])
}

// TestGetChart_CursorCeilsToBucket は、非境界の cursor が落ちるバケットを
// 切り上げ (ceil) で window 末尾に選ぶことを確認する (#1565)。upstream
// getChartRaw は末尾を truncate(cursor + 1span - 1ms) で求めるため、12:30 の
// ような mid-bucket cursor は 13:00 バケットを指す (floor の 12:00 ではない)。
func TestGetChart_CursorCeilsToBucket(t *testing.T) {
	c, _, clk := newTestChart(t, notesSchema)
	// 13:00 バケットに inc=5 を記録。
	clk.set(time.Date(2026, 4, 9, 13, 0, 0, 0, time.UTC))
	require.NoError(t, c.Commit(Diff{"local.inc": int64(5)}, ""))
	require.NoError(t, c.Save(context.Background()))
	clk.set(time.Date(2026, 4, 9, 23, 0, 0, 0, time.UTC))

	// mid-bucket cursor 12:30 → ceil で 13:00 バケット (=5)。
	mid := time.Date(2026, 4, 9, 12, 30, 0, 0, time.UTC)
	out, err := c.GetChart(context.Background(), SpanHour, 1, &mid, "")
	require.NoError(t, err)
	assert.Equal(t, []int64{5}, out["local.inc"], "mid-bucket cursor must ceil to 13:00")

	// 境界一致 cursor 12:00 は据え置きで 12:00 バケット (データ無し=0)。
	aligned := time.Date(2026, 4, 9, 12, 0, 0, 0, time.UTC)
	out2, err := c.GetChart(context.Background(), SpanHour, 1, &aligned, "")
	require.NoError(t, err)
	assert.Equal(t, []int64{0}, out2["local.inc"], "boundary cursor stays at 12:00")
}

// TestGetChart_CursorCeilsToDayBucket は SpanDay でも mid-day の cursor が
// 翌日 0:00 のバケットへ ceil されることを確認する (#1565)。
func TestGetChart_CursorCeilsToDayBucket(t *testing.T) {
	c, _, clk := newTestChart(t, notesSchema)
	// 2026-04-10 のバケットに inc=9 を記録。
	clk.set(time.Date(2026, 4, 10, 5, 0, 0, 0, time.UTC))
	require.NoError(t, c.Commit(Diff{"local.inc": int64(9)}, ""))
	require.NoError(t, c.Save(context.Background()))
	clk.set(time.Date(2026, 4, 20, 0, 0, 0, 0, time.UTC))

	// mid-day cursor 2026-04-09T12:00 → ceil で 2026-04-10 バケット (=9)。
	mid := time.Date(2026, 4, 9, 12, 0, 0, 0, time.UTC)
	out, err := c.GetChart(context.Background(), SpanDay, 1, &mid, "")
	require.NoError(t, err)
	assert.Equal(t, []int64{9}, out["local.inc"], "mid-day cursor must ceil to 2026-04-10")
}

func TestGetChart_SpanDay(t *testing.T) {
	c, _, clk := newTestChart(t, notesSchema)
	for i := range 3 {
		clk.set(time.Date(2026, 4, 7+i, 12, 0, 0, 0, time.UTC))
		require.NoError(t, c.Commit(Diff{"local.inc": int64(10 + i)}, ""))
		require.NoError(t, c.Save(context.Background()))
	}
	clk.set(time.Date(2026, 4, 9, 12, 0, 0, 0, time.UTC))
	out, err := c.GetChart(context.Background(), SpanDay, 3, nil, "")
	require.NoError(t, err)
	// newest-first: 04-09=12 → idx0, 04-08=11 → idx1, 04-07=10 → idx2。
	assert.Equal(t, []int64{12, 11, 10}, out["local.inc"])
}

func TestGetChart_RepoErrorBubbles(t *testing.T) {
	c, repo, _ := newTestChart(t, notesSchema)
	repo.armError("FindRange", errors.New("boom"))
	if _, err := c.GetChart(context.Background(), SpanHour, 1, nil, ""); err == nil {
		t.Fatal("expected error")
	}
}

func TestGetChart_AmountClampedToOne(t *testing.T) {
	c, _, _ := newTestChart(t, notesSchema)
	out, err := c.GetChart(context.Background(), SpanHour, 0, nil, "")
	require.NoError(t, err)
	assert.Len(t, out["local.inc"], 1)
}

func TestGetChart_FindLatestErrorBubbles(t *testing.T) {
	c, repo, _ := newTestChart(t, notesSchema)
	repo.armError("FindLatest", errors.New("latest boom"))
	if _, err := c.GetChart(context.Background(), SpanHour, 1, nil, ""); err == nil {
		t.Fatal("expected error")
	}
}

func TestGetChart_FindBeforeErrorBubbles(t *testing.T) {
	c, repo, clk := newTestChart(t, notesSchema)
	// Seed a row at the end of the window so the FindBefore branch is hit.
	clk.set(time.Date(2026, 4, 9, 12, 0, 0, 0, time.UTC))
	require.NoError(t, c.Commit(Diff{"local.inc": 1}, ""))
	require.NoError(t, c.Save(context.Background()))

	repo.armError("FindBefore", errors.New("before boom"))
	if _, err := c.GetChart(context.Background(), SpanHour, 5, nil, ""); err == nil {
		t.Fatal("expected error from FindBefore")
	}
}

func TestSave_SecondClaimErrorBubbles(t *testing.T) {
	c, repo, _ := newTestChart(t, notesSchema)
	require.NoError(t, c.Commit(Diff{"local.inc": 1}, ""))
	// FindCurrent succeeds (returns NotFound) → FindLatest is invoked.
	repo.armError("FindLatest", errors.New("latest boom"))
	if err := c.Save(context.Background()); err == nil {
		t.Fatal("expected error from FindLatest path")
	}
}

func TestBakeUniqueAndIntersection_EmptyIntersectionList(t *testing.T) {
	schema := Schema{
		Name: "x",
		Columns: []ColumnDef{
			{Name: "i", IntersectionOf: []string{}},
		},
	}
	row := &Row{Cols: map[string]any{}}
	got := bakeUniqueAndIntersection(schema, row, Diff{})
	assert.Equal(t, int64(0), got["i"])
}

func TestBakeUniqueAndIntersection_RowUniquesNonStringSlice(t *testing.T) {
	// rowUniques の "[]string でない" フォールバック分岐を踏むため
	// あえて任意型を入れる。
	schema := Schema{
		Name: "x",
		Columns: []ColumnDef{
			{Name: "u", UniqueIncrement: true},
		},
	}
	row := &Row{Cols: map[string]any{"u:unique": "not-a-slice"}}
	got := bakeUniqueAndIntersection(schema, row, Diff{"u": []string{"a"}})
	assert.Equal(t, int64(1), got["u"])
}

func TestMergeDiffs_StringConcatenation(t *testing.T) {
	merged := mergeDiffs([]bufferedDiff{
		{diff: Diff{"k": []string{"a"}}},
		{diff: Diff{"k": []string{"b"}}},
	})
	if v, ok := merged["k"].([]string); !ok || len(v) != 2 {
		t.Fatalf("got %v", merged)
	}
}

func TestPgArrayLiteral_EscapesQuotesAndBackslashes(t *testing.T) {
	got := pgArrayLiteral([]string{`a"b`, `c\d`, `simple`})
	want := `{"a\"b","c\\d","simple"}`
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestPgArrayLiteral_Empty(t *testing.T) {
	if got := pgArrayLiteral(nil); got != "{}" {
		t.Errorf("got %q", got)
	}
}

func TestToInt64_Variants(t *testing.T) {
	cases := []struct {
		in   any
		want int64
	}{
		{int64(5), 5},
		{int32(5), 5},
		{int(5), 5},
		{float64(5.7), 5},
		{[]byte("12"), 12},
		{"unknown", 0},
		{nil, 0},
	}
	for _, c := range cases {
		if got := toInt64(c.in); got != c.want {
			t.Errorf("toInt64(%v) = %d, want %d", c.in, got, c.want)
		}
	}
}

// hookLocker wraps a Locker and runs a callback inside Acquire so the
// test can simulate "another writer inserted a row while we were
// waiting on the lock".
type hookLocker struct {
	inner Locker
	hook  func()
}

func (h *hookLocker) Acquire(ctx context.Context, key string, ttl time.Duration) (func(), error) {
	rel, err := h.inner.Acquire(ctx, key, ttl)
	if err == nil && h.hook != nil {
		h.hook()
	}
	return rel, err
}

// TestClaimCurrentLog_PostLockFindCurrentRecovers exercises the
// double-check inside claimCurrentLog: after the lock is held the
// engine re-queries FindCurrent. If a peer inserted a row in the
// meantime that row is returned instead of inserting a duplicate.
func TestClaimCurrentLog_PostLockFindCurrentRecovers(t *testing.T) {
	repo := newFakeRepo()
	clk := newFakeClock(time.Date(2026, 4, 9, 12, 0, 0, 0, time.UTC))
	bucketTs := truncateToHour(clk.Now()).Unix()
	dayTs := truncateToDay(clk.Now()).Unix()
	hook := &hookLocker{inner: NewMemoryLocker()}
	hook.hook = func() {
		// 一度だけ実行: hour と day の両方の bucket を「別の writer が
		// 作ったように」 repo に挿入する。
		if len(repo.hour[""]) == 0 {
			repo.hour[""] = []*Row{{ID: 901, Date: bucketTs, Cols: map[string]any{}}}
			repo.day[""] = []*Row{{ID: 902, Date: dayTs, Cols: map[string]any{}}}
		}
	}
	c, err := New(Config{
		Schema: notesSchema,
		Repo:   repo,
		Lock:   hook,
		Clock:  clk,
	})
	require.NoError(t, err)
	require.NoError(t, c.Commit(Diff{"local.inc": 1}, ""))
	require.NoError(t, c.Save(context.Background()))
	// 既存 row が使われ、新しい行は作られない (hour と day それぞれ 1 件)
	assert.Len(t, repo.hour[""], 1)
	assert.Len(t, repo.day[""], 1)
}

// TestClaimCurrentLog_PostLockFindCurrentError checks the error
// propagation from the post-lock FindCurrent call.
func TestClaimCurrentLog_PostLockFindCurrentError(t *testing.T) {
	c, repo, _ := newTestChart(t, notesSchema)
	// First FindCurrent fails with NotFound (queue nil), then second
	// FindCurrent fails with arbitrary error.
	repo.armSkipThenError("FindCurrent", 1, errors.New("post-lock boom"))
	require.NoError(t, c.Commit(Diff{"local.inc": 1}, ""))
	if err := c.Save(context.Background()); err == nil {
		t.Fatal("expected error from post-lock FindCurrent")
	}
}

func TestSystemClock_Now(t *testing.T) {
	got := SystemClock{}.Now()
	if got.IsZero() {
		t.Fatal("expected non-zero time")
	}
	if got.Location().String() != "UTC" {
		t.Errorf("expected UTC, got %v", got.Location())
	}
}

func TestSave_DayClaimErrorBubbles(t *testing.T) {
	c, repo, _ := newTestChart(t, notesSchema)
	require.NoError(t, c.Commit(Diff{"local.inc": 1}, ""))
	// Hour claim's Insert succeeds (1st queued nil), day claim's
	// Insert fails. This exercises the "claim day" error branch.
	repo.armSkipThenError("Insert", 1, errors.New("day insert boom"))
	if err := c.Save(context.Background()); err == nil {
		t.Fatal("expected error from day Insert")
	}
}

func TestSave_DayApplyDeltasErrorBubbles(t *testing.T) {
	c, repo, _ := newTestChart(t, notesSchema)
	require.NoError(t, c.Commit(Diff{"local.inc": 1}, ""))
	// Both claims succeed, hour ApplyDeltas succeeds (skip), day
	// ApplyDeltas fails.
	repo.armSkipThenError("ApplyDeltas", 1, errors.New("day apply boom"))
	if err := c.Save(context.Background()); err == nil {
		t.Fatal("expected error from day ApplyDeltas")
	}
}

func TestSave_ApplyDayDeltasError(t *testing.T) {
	c, repo, _ := newTestChart(t, notesSchema)
	require.NoError(t, c.Commit(Diff{"local.inc": 1}, ""))
	// 1 つだけ ApplyDeltas にエラーを armed する。fakeRepo の armed は
	// FIFO ではなくキー一意。最初の hour Apply は成功させ、day Apply は
	// 別途 armed しても良いが、armed が一段しかないため hour 側で消費
	// されてしまう。代わりに hour 側で失敗させて applyDiffs の "hour Apply"
	// 経路をエラーで踏む。
	repo.armError("ApplyDeltas", errors.New("apply boom"))
	if err := c.Save(context.Background()); err == nil {
		t.Fatal("expected error")
	}
}

func TestTick_HourClaimErrorBubbles(t *testing.T) {
	c, repo, _ := newTestChart(t, notesSchema)
	c.tick = func(_ context.Context, _ string, _ bool) (map[string]int64, error) {
		return map[string]int64{"local.total": 1}, nil
	}
	repo.armError("Insert", errors.New("insert boom"))
	if err := c.Tick(context.Background(), false, ""); err == nil {
		t.Fatal("expected error")
	}
}

func TestTick_DayClaimErrorBubbles(t *testing.T) {
	c, repo, _ := newTestChart(t, notesSchema)
	c.tick = func(_ context.Context, _ string, _ bool) (map[string]int64, error) {
		return map[string]int64{"local.total": 1}, nil
	}
	repo.armSkipThenError("Insert", 1, errors.New("day insert boom"))
	if err := c.Tick(context.Background(), false, ""); err == nil {
		t.Fatal("expected error from day claim")
	}
}

func TestTick_HourSetColumnsErrorBubbles(t *testing.T) {
	c, repo, _ := newTestChart(t, notesSchema)
	c.tick = func(_ context.Context, _ string, _ bool) (map[string]int64, error) {
		return map[string]int64{"local.total": 1}, nil
	}
	// Pre-create a row so claimCurrentLog skips Insert.
	_, err := repo.Insert(context.Background(), SpanHour, "", truncateToHour(c.clock.Now()).Unix(), map[string]any{
		"local.total": int64(0),
	})
	require.NoError(t, err)
	_, err = repo.Insert(context.Background(), SpanDay, "", truncateToDay(c.clock.Now()).Unix(), map[string]any{
		"local.total": int64(0),
	})
	require.NoError(t, err)

	repo.armError("SetColumns", errors.New("set boom"))
	if err := c.Tick(context.Background(), false, ""); err == nil {
		t.Fatal("expected hour SetColumns error")
	}
}

func TestClean_HourErrorBubbles(t *testing.T) {
	c, repo, _ := newTestChart(t, activeUsersSchema)
	repo.armError("ResetUniqueTempColumns", errors.New("reset boom"))
	if err := c.Clean(context.Background()); err == nil {
		t.Fatal("expected error")
	}
}

func TestClean_DayErrorBubbles(t *testing.T) {
	c, repo, _ := newTestChart(t, activeUsersSchema)
	repo.armSkipThenError("ResetUniqueTempColumns", 1, errors.New("day reset boom"))
	if err := c.Clean(context.Background()); err == nil {
		t.Fatal("expected error from day Reset")
	}
}

func TestTick_DaySetColumnsErrorBubbles(t *testing.T) {
	c, repo, _ := newTestChart(t, notesSchema)
	c.tick = func(_ context.Context, _ string, _ bool) (map[string]int64, error) {
		return map[string]int64{"local.total": 1}, nil
	}
	// Pre-create rows so claimCurrentLog skips Insert.
	_, err := repo.Insert(context.Background(), SpanHour, "", truncateToHour(c.clock.Now()).Unix(), map[string]any{
		"local.total": int64(0),
	})
	require.NoError(t, err)
	_, err = repo.Insert(context.Background(), SpanDay, "", truncateToDay(c.clock.Now()).Unix(), map[string]any{
		"local.total": int64(0),
	})
	require.NoError(t, err)
	repo.armSkipThenError("SetColumns", 1, errors.New("day set boom"))
	if err := c.Tick(context.Background(), false, ""); err == nil {
		t.Fatal("expected day SetColumns error")
	}
}

func TestClaimCurrentLog_FindLatestErrorBubbles(t *testing.T) {
	c, repo, _ := newTestChart(t, notesSchema)
	// FindCurrent → not found, then FindLatest → arbitrary error.
	repo.armError("FindLatest", errors.New("latest boom"))
	require.NoError(t, c.Commit(Diff{"local.inc": 1}, ""))
	if err := c.Save(context.Background()); err == nil {
		t.Fatal("expected error")
	}
}

// errLocker is a Locker that always returns the supplied error.
type errLocker struct{ err error }

func (l errLocker) Acquire(_ context.Context, _ string, _ time.Duration) (func(), error) {
	return nil, l.err
}

func TestClaimCurrentLog_LockAcquireErrorBubbles(t *testing.T) {
	repo := newFakeRepo()
	clk := newFakeClock(time.Date(2026, 4, 9, 12, 0, 0, 0, time.UTC))
	c, err := New(Config{
		Schema: notesSchema,
		Repo:   repo,
		Lock:   errLocker{err: errors.New("lock boom")},
		Clock:  clk,
	})
	require.NoError(t, err)
	require.NoError(t, c.Commit(Diff{"local.inc": 1}, ""))
	if err := c.Save(context.Background()); err == nil {
		t.Fatal("expected lock error")
	}
}

// TestGetChart_FindBeforeAnchorAppended exercises the path where the
// requested range contains some rows but the earliest row is *not* the
// start of the range — claiming an anchor row from FindBefore that
// returns successfully.
func TestGetChart_FindBeforeAnchorAppended(t *testing.T) {
	c, _, clk := newTestChart(t, notesSchema)
	// Far-past row that becomes the anchor.
	clk.set(time.Date(2026, 4, 9, 8, 0, 0, 0, time.UTC))
	require.NoError(t, c.Commit(Diff{"local.total": 4}, ""))
	require.NoError(t, c.Save(context.Background()))
	// Mid-range row at hour 11 (within the upcoming 3-bucket window).
	clk.set(time.Date(2026, 4, 9, 11, 0, 0, 0, time.UTC))
	require.NoError(t, c.Commit(Diff{"local.total": 7}, ""))
	require.NoError(t, c.Save(context.Background()))

	// Now query a 3-bucket window ending at hour 12. Range = [10, 11, 12].
	// hour10/12 are empty; hour11 exists; hour8 is the anchor before
	// the start of the range.
	clk.set(time.Date(2026, 4, 9, 12, 0, 0, 0, time.UTC))
	out, err := c.GetChart(context.Background(), SpanHour, 3, nil, "")
	require.NoError(t, err)
	// newest-first:
	//   idx0 = hour12 (interpolated, accumulate keeps 11)
	//   idx1 = hour11 (real)                         → 4 + 7 = 11
	//   idx2 = hour10 (interpolated from anchor)     → 4
	assert.Equal(t, []int64{11, 11, 4}, out["local.total"])
}

func TestGetChart_FindLatestNotFoundLeavesEmpty(t *testing.T) {
	c, _, _ := newTestChart(t, notesSchema)
	// 何も書き込まずに GetChart を呼ぶ → FindRange empty → FindLatest
	// not found → rows は nil のまま → 全 0 で埋まる。
	out, err := c.GetChart(context.Background(), SpanHour, 2, nil, "")
	require.NoError(t, err)
	assert.Equal(t, []int64{0, 0}, out["local.inc"])
	assert.Equal(t, []int64{0, 0}, out["local.total"])
}

func TestApplyDiffs_UniqueIncrementSkipsNonStringSlice(t *testing.T) {
	// applyDiffs の "v が []string でない" 分岐を踏むため、buffered diff
	// に直接 int を渡す。Commit() を経由するとフィルタされるので、
	// 内部 API である applyDiffs を直接呼ぶ。
	c, _, _ := newTestChart(t, activeUsersSchema)
	hour := &Row{ID: 1, Cols: map[string]any{}}
	day := &Row{ID: 2, Cols: map[string]any{}}
	repo := c.repo.(*fakeRepo)
	repo.hour[""] = []*Row{hour}
	repo.day[""] = []*Row{day}
	require.NoError(t, c.applyDiffs(context.Background(), hour, day, []bufferedDiff{
		{diff: Diff{"read": int64(99)}}, // wrong type for uniqueIncrement column
	}))
}

func TestApplyDiffs_IntersectionColumnInDiffSkipped(t *testing.T) {
	// applyDiffs の `if col.IntersectionOf != nil { continue }` 分岐は
	// merged に intersection 列のキーが含まれているときに踏む。Commit
	// 経由ではこのキーは出ないので、直接 applyDiffs を呼ぶ。
	c, _, _ := newTestChart(t, activeUsersSchema)
	hour := &Row{ID: 1, Cols: map[string]any{}}
	day := &Row{ID: 2, Cols: map[string]any{}}
	repo := c.repo.(*fakeRepo)
	repo.hour[""] = []*Row{hour}
	repo.day[""] = []*Row{day}
	require.NoError(t, c.applyDiffs(context.Background(), hour, day, []bufferedDiff{
		{diff: Diff{"readWrite": int64(99)}}, // intersection 列を直接渡す
	}))
}

func TestApplyDiffs_UniqueIncrementSkipsEmptySlice(t *testing.T) {
	// applyDiffs の `len(items) > 0` の false 分岐を踏むため、空の
	// []string を直接渡す。Commit はフィルタするのでこちらも直接呼ぶ。
	c, _, _ := newTestChart(t, activeUsersSchema)
	hour := &Row{ID: 1, Cols: map[string]any{}}
	day := &Row{ID: 2, Cols: map[string]any{}}
	repo := c.repo.(*fakeRepo)
	repo.hour[""] = []*Row{hour}
	repo.day[""] = []*Row{day}
	require.NoError(t, c.applyDiffs(context.Background(), hour, day, []bufferedDiff{
		{diff: Diff{"read": []string{}}},
	}))
}

func TestUniqueGroups_DuplicateAreCollapsed(t *testing.T) {
	in := []bufferedDiff{
		{group: "a"},
		{group: "b"},
		{group: "a"},
	}
	got := uniqueGroups(in)
	assert.Equal(t, []string{"a", "b"}, got)
}

func TestChart_IsGrouped(t *testing.T) {
	plain, err := New(Config{
		Schema: Schema{Name: "plain", Columns: []ColumnDef{{Name: "x"}}},
		Repo:   newFakeRepo(),
		Lock:   NewMemoryLocker(),
	})
	require.NoError(t, err)
	assert.False(t, plain.IsGrouped())

	grouped, err := New(Config{
		Schema: Schema{Name: "g", Grouped: true, Columns: []ColumnDef{{Name: "x"}}},
		Repo:   newFakeRepo(),
		Lock:   NewMemoryLocker(),
	})
	require.NoError(t, err)
	assert.True(t, grouped.IsGrouped())
}
