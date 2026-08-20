package chart

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
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

// twoIntColumnSchema backs the droppedWork accounting tests: two plain int
// columns plus one uniqueIncrement column.
var twoIntColumnSchema = Schema{
	Name: "twoInt",
	Columns: []ColumnDef{
		{Name: "up"},
		{Name: "down"},
		{Name: "x"},
		{Name: "k", UniqueIncrement: true},
	},
}

// strPtrOf returns a pointer to s. Used to target a specific group (including
// the ungrouped "" key) in applyDeltasFailure.
func strPtrOf(s string) *string { return &s }

// setKeysSorted returns a set's members in a deterministic order. Only used by
// tests that assert on buffer contents.
func setKeysSorted(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// perUserPvSchema mirrors the real per-user PV chart: a uniqueIncrement column
// plus a plain counter. activeUsers は unique 列しか持たず、そこに int を積むと
// applyDiffs が deltas から外すので「捨てた delta」の検証に使えない。
var perUserPvSchema = Schema{
	Name:    "perUserPv",
	Grouped: true,
	Columns: []ColumnDef{
		{Name: "upv.visitor", UniqueIncrement: true},
		{Name: "pv.visitor"},
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

func TestCommit_BufferBoundedByDistinctGroups(t *testing.T) {
	// Commit はイベント 1 件ごとにエントリを増やさない。滞留量は
	// distinct group 数で頭打ちになる (Save は既定 20 分間隔なので、
	// イベント数に比例させると 20 分ぶんが際限なく積まれる)。
	c, repo, _ := newTestChart(t, perUserNotesSchema)
	for i := range 1000 {
		group := fmt.Sprintf("u%d", i%3)
		require.NoError(t, c.Commit(Diff{"inc": 1, "total": 1}, group))
	}

	c.bufMu.Lock()
	assert.Len(t, c.buffer, 3)
	for group, gb := range c.buffer {
		assert.Len(t, gb.ints, 2, "group %s", group)
	}
	c.bufMu.Unlock()

	// 畳んでも合計は変わらない。1000 回を 3 group に配ったので
	// u0 が 334、u1 / u2 が 333。
	require.NoError(t, c.Save(context.Background()))
	assert.Equal(t, int64(334), toInt64(repo.hour["u0"][0].Cols["inc"]))
	assert.Equal(t, int64(333), toInt64(repo.hour["u1"][0].Cols["inc"]))
	assert.Equal(t, int64(333), toInt64(repo.hour["u2"][0].Cols["inc"]))
}

func TestCommit_UniqueBufferBoundedByDistinctKeys(t *testing.T) {
	// unique 列は連結ではなく集合なので、同じ key を何度 Commit しても
	// 滞留量は distinct key 数で頭打ちになる。
	c, repo, _ := newTestChart(t, activeUsersSchema)
	for range 1000 {
		require.NoError(t, c.Commit(Diff{"read": []string{"u1", "u2"}}, ""))
	}

	c.bufMu.Lock()
	require.Len(t, c.buffer, 1)
	assert.Equal(t, []string{"u1", "u2"}, setKeysSorted(c.buffer[""].uniques["read"]))
	c.bufMu.Unlock()

	require.NoError(t, c.Save(context.Background()))
	assert.Equal(t, int64(2), toInt64(repo.hour[""][0].Cols["read"]))
}

func TestSave_ProcessesGroupsInSortedOrder(t *testing.T) {
	// map の反復順はランダムなので、Save は group をソートしてから処理する。
	// これが無いと「どこまで適用されてから失敗したか」が実行ごとに変わり、
	// 部分適用を検証するテストが flaky になる。
	c, repo, _ := newTestChart(t, perUserNotesSchema)
	// **Commit する順序を昇順にしないこと。** Go の map の反復順は
	// 開始バケット / オフセットのランダム化なので、1 バケットに収まる
	// 小さな map では insertion order の「回転」しか出ない (8 キーでも
	// 出現する順序は 8 通りで、8! ではない)。昇順に入れると回転の中に
	// ソート済みの順序が含まれ、sort.Strings を外してもこのテストが
	// 通ってしまう (実測で全 200000 回中 12% が昇順になった)。昇順以外で
	// 入れれば、ソート済みの順序は回転として現れない (実測 0 回)。
	for _, g := range []string{"u3", "u1", "u8", "u2", "u6", "u4", "u7", "u5"} {
		require.NoError(t, c.Commit(Diff{"inc": 1}, g))
	}
	require.NoError(t, c.Save(context.Background()))
	// claimCurrentLog は row を新規作成する経路でロック取得の前後に
	// FindCurrent を 2 回引くので、連続する重複を畳んでから順序を見る。
	var order []string
	for _, g := range repo.groupOrder {
		if len(order) == 0 || order[len(order)-1] != g {
			order = append(order, g)
		}
	}
	assert.Equal(t, []string{"u1", "u2", "u3", "u4", "u5", "u6", "u7", "u8"}, order)
}

func TestApplyDiffs_ColumnNeverAssignedTwiceInOneUpdate(t *testing.T) {
	// ApplyDeltas の 3 マップは 1 本の UPDATE の SET 句に展開されるので、
	// 同じ列が 2 つ以上のマップに現れると PostgreSQL が 42601
	// (multiple assignments to same column) で落ちる。uniqueIncrement 列と
	// intersection 列を deltas から外す `continue` がこの不変条件を守っている。
	c, repo, _ := newTestChart(t, activeUsersSchema)
	require.NoError(t, c.Commit(Diff{
		"read":  []string{"u1"},
		"write": []string{"u1"},
	}, ""))
	// uniqueIncrement 列 (read) と intersection 列 (readWrite) を、通常の
	// 呼び出し元が出さない int としても積む。どちらも deltas から外れて
	// いなければ、同じ列が deltas と setInts の両方に現れる。
	require.NoError(t, c.Commit(Diff{
		"read":      int64(7),
		"readWrite": int64(7),
	}, ""))

	// **前提を固定する。** 現在の実装ではこの検査の対象になる `deltas` は
	// 空のままなので、下のループは変異を入れたときだけ意味を持つ。Commit が
	// 「1 キー 1 型」に整理されて int が捨てられるようになると、テストは
	// 緑のまま無力化される。そうなったらここで落ちて、不変条件を導出し直す
	// きっかけになるようにしておく。
	c.bufMu.Lock()
	require.NotNil(t, c.buffer[""])
	require.NotZero(t, c.buffer[""].ints["read"], "uniqueIncrement 列に int が積まれていない")
	require.NotZero(t, c.buffer[""].ints["readWrite"], "intersection 列に int が積まれていない")
	c.bufMu.Unlock()

	require.NoError(t, c.Save(context.Background()))

	// deltas と setInts はどちらも toColumnName() の列に展開されるので、
	// キーが重なると 1 本の UPDATE で同じ列に 2 回代入することになる。
	// appends だけは toUniqueTempColumnName() の別列なので対象外。
	require.NotEmpty(t, repo.calls)
	for i, call := range repo.calls {
		for k := range call.deltas {
			if _, dup := call.setInts[k]; dup {
				t.Fatalf("call %d (%s): column %q assigned by both deltas and setInts", i, call.span, k)
			}
		}
	}
}

// 1 つの group が恒久的に失敗しても、他の group は flush され続ける。
// 以前は最初のエラーで return していたため、ソート順で後続の group が
// 二度と書かれなかった (#2651)。
func TestSave_OneFailingGroupDoesNotBlockOthers(t *testing.T) {
	c, repo, _ := newTestChart(t, perUserNotesSchema)
	require.NoError(t, c.Commit(Diff{"inc": 1}, "u1"))
	require.NoError(t, c.Commit(Diff{"inc": 2}, "u2"))
	require.NoError(t, c.Commit(Diff{"inc": 3}, "u3"))

	// 先頭の group (u1) の ApplyDeltas を恒久的に落とす (hour が先に落ちるので
	// day には到達しない)。
	repo.failApplyDeltas = &applyDeltasFailure{group: strPtrOf("u1")}
	err := c.Save(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "u1")

	// u2 / u3 は書かれている。
	require.Len(t, repo.hour["u2"], 1)
	require.Len(t, repo.hour["u3"], 1)
	assert.Equal(t, int64(2), toInt64(repo.hour["u2"][0].Cols["inc"]))
	assert.Equal(t, int64(3), toInt64(repo.hour["u3"][0].Cols["inc"]))

	// 失敗した u1 だけがバッファに戻っている。
	c.bufMu.Lock()
	require.Len(t, c.buffer, 1)
	require.NotNil(t, c.buffer["u1"])
	assert.Equal(t, int64(1), c.buffer["u1"].ints["inc"])
	c.bufMu.Unlock()
}

// 失敗が続いてもバッファが単調増加しない。maxSaveAttempts 回で諦める。
func TestSave_DropsGroupAfterRepeatedFailures(t *testing.T) {
	c, repo, _ := newTestChart(t, perUserNotesSchema)
	repo.failApplyDeltas = &applyDeltasFailure{group: strPtrOf("u1")}

	for i := 1; i <= maxSaveAttempts; i++ {
		require.NoError(t, c.Commit(Diff{"inc": 1}, "u1"))
		require.Error(t, c.Save(context.Background()))

		c.bufMu.Lock()
		if i < maxSaveAttempts {
			require.NotNil(t, c.buffer["u1"], "attempt %d: まだ戻す", i)
			assert.Equal(t, i, c.buffer["u1"].attempts)
		} else {
			assert.Empty(t, c.buffer, "attempt %d: 諦めて捨てる", i)
		}
		c.bufMu.Unlock()
	}

	// 諦めたあとに来た Commit は attempts がリセットされた新しいバッファに入る。
	require.NoError(t, c.Commit(Diff{"inc": 1}, "u1"))
	c.bufMu.Lock()
	assert.Equal(t, 0, c.buffer["u1"].attempts)
	c.bufMu.Unlock()
}

// **unique 列を持つ group が「hour は通るが day だけ恒久失敗」でも、
// maxSaveAttempts で捨てる。** uniquesOnly が attempts を引き継がないと、
// この経路の group は永久に retry されて unique 集合が単調増加する。
// federation / activeUsers / perUserPv がこの形になる。
func TestSave_DropsUniqueOnlyRequeueAfterRepeatedDayFailures(t *testing.T) {
	// perUserPv と同じ形 (unique 列 + 素の int カウンタ)。activeUsers では
	// int を積んでも applyDiffs が deltas から外すので、捨てた delta の検証に
	// ならない。
	c, repo, _ := newTestChart(t, perUserPvSchema)
	// day 側だけを恒久的に落とす。hour は通り続ける。
	repo.failApplyDeltas = &applyDeltasFailure{span: SpanDay}
	buf := captureWarnings(t)

	for i := 1; i <= maxSaveAttempts; i++ {
		// unique 列と int 列の両方を積む。int は hour 適用済みで戻せないので
		// 毎周期 notRetryable として記録され、unique だけが retry される。
		require.NoError(t, c.Commit(Diff{
			"upv.visitor": []string{fmt.Sprintf("u%d", i)},
			"pv.visitor":  int64(1),
		}, "owner"))
		require.Error(t, c.Save(context.Background()))

		c.bufMu.Lock()
		if i < maxSaveAttempts {
			require.NotNil(t, c.buffer["owner"], "attempt %d: まだ戻す", i)
			assert.Equal(t, i, c.buffer["owner"].attempts)
			assert.Empty(t, c.buffer["owner"].ints, "int は戻さない")
		} else {
			assert.Empty(t, c.buffer, "attempt %d: 諦めて捨てる", i)
		}
		c.bufMu.Unlock()
	}

	// **1 周期につき warn は 1 行。** group ごとに出すと grouped chart で
	// distinct group 数ぶんの行が一斉に出る。
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	require.Len(t, lines, maxSaveAttempts, "1 周期 1 行")

	// 最初の 2 周期は「戻せなかった int」だけ。unique は戻すので捨てた扱いに
	// しない (記録元を uniquesOnly 前の gb に取り違えるとここが崩れる)。
	for i := range maxSaveAttempts - 1 {
		assert.Contains(t, lines[i], "notRetryable=1", "line %d", i)
		assert.Contains(t, lines[i], "retryLimit=0", "line %d", i)
		assert.Contains(t, lines[i], "intDelta=1", "line %d", i)
		assert.Contains(t, lines[i], "uniqueKeys=0", "line %d", i)
	}

	// 最後の周期は int の破棄と retry limit の 2 レコードが 1 行に畳まれる。
	last := lines[len(lines)-1]
	// 1 group が 2 レコード (int の破棄 + retry limit) を出しても、group は 1。
	assert.Contains(t, last, "groups=1", "group は distinct で数える")
	assert.Contains(t, last, "notRetryable=1")
	assert.Contains(t, last, "retryLimit=1")
	assert.Contains(t, last, "intDelta=1", "捨てた int は 1 周期ぶん")
	assert.Contains(t, last, "uniqueKeys=3", "捨てた unique は 3 周期ぶん")
	// reason ごとに代表例が出る。
	assert.Contains(t, last, "retryLimitExampleError=")
	assert.Contains(t, last, "notRetryableExampleError=")
}

// 捨てたときは warn を出す。**本番ではこれが唯一の signal になりうる**ので、
// 件数・規模・代表エラーが載っていることまで見る。
func TestSave_WarnsWhenDroppingWork(t *testing.T) {
	t.Run("retry limit", func(t *testing.T) {
		c, repo, _ := newTestChart(t, perUserNotesSchema)
		repo.failApplyDeltas = &applyDeltasFailure{group: strPtrOf("u1")}
		for i := 1; i < maxSaveAttempts; i++ {
			require.NoError(t, c.Commit(Diff{"inc": 1}, "u1"))
			require.Error(t, c.Save(context.Background()))
		}

		buf := captureWarnings(t)
		require.NoError(t, c.Commit(Diff{"inc": 1}, "u1"))
		require.Error(t, c.Save(context.Background()))

		out := buf.String()
		assert.Contains(t, out, "dropped buffered work")
		assert.Contains(t, out, "chart=perUserNotes")
		assert.Contains(t, out, "groups=1")
		assert.Contains(t, out, "retryLimit=1")
		assert.Contains(t, out, "notRetryable=0")
		assert.Contains(t, out, "retryLimitExampleGroup=u1")
		assert.Contains(t, out, "poisoned group", "原因のエラーが載っている")
		// 3 周期ぶんの delta が合算されている (列数ではない)。
		assert.Contains(t, out, "intDelta=3", "捨てた規模が載っている")
	})

	// int だけの group が hour 適用済みで day に落ちた場合。戻すものが無いので
	// 黙って消えていたが、instance chart (unique 列なし・day 側があふれやすい)
	// で最も踏みやすい経路なので記録する。
	t.Run("hour applied without uniques", func(t *testing.T) {
		c, repo, _ := newTestChart(t, perUserNotesSchema)
		require.NoError(t, c.Commit(Diff{"inc": 1}, "u1"))
		repo.failApplyDeltas = &applyDeltasFailure{span: SpanDay}

		buf := captureWarnings(t)
		require.Error(t, c.Save(context.Background()))

		out := buf.String()
		assert.Contains(t, out, "dropped buffered work")
		assert.Contains(t, out, "notRetryable=1")
		assert.Contains(t, out, "notRetryableExampleGroup=u1")
		assert.Contains(t, out, "intDelta=1")

		c.bufMu.Lock()
		assert.Empty(t, c.buffer)
		c.bufMu.Unlock()
	})

	// 成功したときは何も出さない。
	t.Run("no warning on success", func(t *testing.T) {
		c, _, _ := newTestChart(t, perUserNotesSchema)
		require.NoError(t, c.Commit(Diff{"inc": 1}, "u1"))
		buf := captureWarnings(t)
		require.NoError(t, c.Save(context.Background()))
		assert.Empty(t, buf.String())
	})

	// 複数 group が同時に捨てられたら 1 行に畳む。grouped chart の全 group が
	// 同じ周期で上限に達すると、group ごとに出していては distinct owner 数ぶんの
	// 行が一斉に出る。
	t.Run("aggregates multiple groups into one line", func(t *testing.T) {
		c, repo, _ := newTestChart(t, perUserNotesSchema)
		require.NoError(t, c.Commit(Diff{"inc": 1}, "u1"))
		require.NoError(t, c.Commit(Diff{"inc": 1}, "u2"))
		repo.failApplyDeltas = &applyDeltasFailure{span: SpanDay}

		buf := captureWarnings(t)
		require.Error(t, c.Save(context.Background()))

		out := buf.String()
		assert.Equal(t, 1, strings.Count(out, "dropped buffered work"))
		assert.Contains(t, out, "groups=2")
		assert.Contains(t, out, "intDelta=2")
	})
}

// 成功した group の attempts は持ち越さない (バッファごと消えるため)。
func TestSave_SuccessClearsGroup(t *testing.T) {
	c, repo, _ := newTestChart(t, perUserNotesSchema)
	repo.failApplyDeltas = &applyDeltasFailure{group: strPtrOf("u1")}
	require.NoError(t, c.Commit(Diff{"inc": 1}, "u1"))
	require.Error(t, c.Save(context.Background()))

	repo.failApplyDeltas = nil
	require.NoError(t, c.Save(context.Background()))

	c.bufMu.Lock()
	assert.Empty(t, c.buffer)
	c.bufMu.Unlock()
	assert.Equal(t, int64(1), toInt64(repo.hour["u1"][0].Cols["inc"]))
}

// hour が通って day で落ちた group は、int の delta を戻さない (二重計上に
// なる)。unique 列は冪等なので戻して day を取り戻す。
func TestSave_HourAppliedDayFailed_RequeuesUniquesOnly(t *testing.T) {
	c, repo, _ := newTestChart(t, activeUsersSchema)
	require.NoError(t, c.Commit(Diff{"read": []string{"u1"}, "readWrite": int64(5)}, ""))
	// hour の ApplyDeltas は成功し、day で落ちる。
	repo.armSkipThenError("ApplyDeltas", 1, errors.New("day boom"))
	require.Error(t, c.Save(context.Background()))

	c.bufMu.Lock()
	require.NotNil(t, c.buffer[""])
	assert.Equal(t, []string{"u1"}, setKeysSorted(c.buffer[""].uniques["read"]),
		"unique 列は戻す")
	assert.Empty(t, c.buffer[""].ints, "int の delta は戻さない (hour に二重計上される)")
	c.bufMu.Unlock()

	// 次の Save で day が追いつき、hour は変わらない。
	require.NoError(t, c.Save(context.Background()))
	assert.Equal(t, int64(1), toInt64(repo.hour[""][0].Cols["read"]))
	assert.Equal(t, int64(1), toInt64(repo.day[""][0].Cols["read"]))
}

// hour が通って day で落ちた group が unique 列を持たないなら、戻すものが無い。
func TestSave_HourAppliedDayFailed_NothingToRequeueWithoutUniques(t *testing.T) {
	c, repo, _ := newTestChart(t, perUserNotesSchema)
	require.NoError(t, c.Commit(Diff{"inc": 1}, "u1"))
	repo.armSkipThenError("ApplyDeltas", 1, errors.New("day boom"))
	require.Error(t, c.Save(context.Background()))

	c.bufMu.Lock()
	assert.Empty(t, c.buffer, "int だけの group は戻さない")
	c.bufMu.Unlock()
	assert.Equal(t, int64(1), toInt64(repo.hour["u1"][0].Cols["inc"]))
}

// エラー文字列の chart/group 表記。ungrouped chart は group が空なので、
// 区切りの "/" を出すと `federation/: ...` になって読みにくい。
func TestSave_ErrorNamesChartAndGroup(t *testing.T) {
	t.Run("grouped", func(t *testing.T) {
		c, repo, _ := newTestChart(t, perUserNotesSchema)
		require.NoError(t, c.Commit(Diff{"inc": 1}, "u1"))
		repo.armError("FindCurrent", errors.New("boom"))
		err := c.Save(context.Background())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "perUserNotes/u1:")
	})

	t.Run("ungrouped", func(t *testing.T) {
		c, repo, _ := newTestChart(t, notesSchema)
		require.NoError(t, c.Commit(Diff{"local.inc": 1}, ""))
		repo.armError("FindCurrent", errors.New("boom"))
		err := c.Save(context.Background())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "notes:")
		assert.NotContains(t, err.Error(), "notes/:")
	})
}

// schema に無い列 / uniqueIncrement でない列に積まれた unique キーは、
// applyDiffs が書かないので捨てた量に数えない (int 側と同じ理由)。
func TestDroppedWork_DoesNotCountUniquesOnNonUniqueColumns(t *testing.T) {
	c, _, _ := newTestChart(t, twoIntColumnSchema)
	gb := newGroupBuffer()
	gb.addUnique("k", []string{"a", "b"}) // uniqueIncrement 列
	gb.addUnique("up", []string{"c"})     // 素の int 列
	gb.addUnique("bogus", []string{"d"})  // schema に無い

	d := c.newDroppedWork("g", droppedReasonRetryLimit, errors.New("boom"), gb)
	assert.Equal(t, 2, d.uniques, "書かれる列のキーだけ数える")
}

// 捨てた delta は **絶対値** で数える。減算 (dec 系の列) を素で足すと打ち消し
// 合って「何も失っていない」ように見える。
func TestDroppedWork_CountsAbsoluteDelta(t *testing.T) {
	gb := newGroupBuffer()
	gb.addInt("up", 3)
	gb.addInt("down", -4)
	gb.addUnique("k", []string{"a", "b"})

	c, _, _ := newTestChart(t, twoIntColumnSchema)
	all := c.newDroppedWork("g", droppedReasonRetryLimit, errors.New("boom"), gb)
	assert.Equal(t, int64(7), all.ints, "3 + |-4|")
	assert.Equal(t, 2, all.uniques)

	// int だけを数える版は unique を含めない。
	ints, ok := c.newDroppedIntWork("g", errors.New("boom"), gb)
	require.True(t, ok)
	assert.Equal(t, int64(7), ints.ints)
	assert.Equal(t, 0, ints.uniques)
	assert.Equal(t, droppedReasonNotRetryable, ints.reason)

	// **同じ列**で相殺して 0 になったら記録しない (intDelta=0 の警告行を
	// 出さない)。別の列どうしは絶対値で足すので相殺しない。
	cancelled := newGroupBuffer()
	cancelled.addInt("x", 1)
	cancelled.addInt("x", -1)
	require.Equal(t, int64(0), cancelled.ints["x"])
	_, ok = c.newDroppedIntWork("g", errors.New("boom"), cancelled)
	assert.False(t, ok, "delta が相殺したら警告しない")
}

// reason ごとの代表例は **最初に見たもの** を採る。group はソート済みなので
// 最小の group で決定的になる。last-wins にすると実行ごとに変わりはしないが、
// どの group が代表なのかが読む人に予測できなくなる。
func TestWarnDropped_ExampleIsFirstSeenPerReason(t *testing.T) {
	c, repo, _ := newTestChart(t, perUserNotesSchema)
	for _, g := range []string{"u1", "u2", "u3"} {
		require.NoError(t, c.Commit(Diff{"inc": 1}, g))
	}
	// 全 group を retry limit まで進める。
	repo.failApplyDeltas = &applyDeltasFailure{}
	for i := 1; i < maxSaveAttempts; i++ {
		require.Error(t, c.Save(context.Background()))
		for _, g := range []string{"u1", "u2", "u3"} {
			require.NoError(t, c.Commit(Diff{"inc": 1}, g))
		}
	}

	buf := captureWarnings(t)
	require.Error(t, c.Save(context.Background()))

	out := buf.String()
	assert.Contains(t, out, "groups=3")
	assert.Contains(t, out, "retryLimitExampleGroup=u1", "ソート順で最初の group")
}

// 返すエラーは有界にする。DB 全断だと失敗 group 数がそのまま件数になり、
// perUserPv のように group 数が distinct owner 数まで伸びる chart では
// エラー文字列だけで数百 KB になる。slog の TextHandler は改行をエスケープ
// するので 1 物理行に収まってしまい、読みたい代表例が埋もれる。
func TestSave_BoundsReportedGroupErrors(t *testing.T) {
	c, repo, _ := newTestChart(t, perUserNotesSchema)
	const groups = maxReportedGroupErrors + 7
	for i := range groups {
		require.NoError(t, c.Commit(Diff{"inc": 1}, fmt.Sprintf("u%02d", i)))
	}
	repo.failApplyDeltas = &applyDeltasFailure{}

	err := c.Save(context.Background())
	require.Error(t, err)
	msg := err.Error()
	// 代表例は maxReportedGroupErrors 件まで。
	assert.Equal(t, maxReportedGroupErrors, strings.Count(msg, "poisoned group"))
	assert.Contains(t, msg, fmt.Sprintf("and %d more group(s) failed", groups-maxReportedGroupErrors))
	// 捨てた件数は warn 側が持つので、エラーの伸びは頭打ちで足りる。
	assert.Less(t, len(msg), 2000)
}

// 件数がちょうど上限なら "and N more" は出さない (境界)。
func TestSave_DoesNotAnnotateAtExactlyTheLimit(t *testing.T) {
	c, repo, _ := newTestChart(t, perUserNotesSchema)
	for i := range maxReportedGroupErrors {
		require.NoError(t, c.Commit(Diff{"inc": 1}, fmt.Sprintf("u%02d", i)))
	}
	repo.failApplyDeltas = &applyDeltasFailure{}

	err := c.Save(context.Background())
	require.Error(t, err)
	assert.Equal(t, maxReportedGroupErrors, strings.Count(err.Error(), "poisoned group"))
	assert.NotContains(t, err.Error(), "more group(s) failed")
}

// 件数が上限以下なら "and N more" は出さない。
func TestSave_DoesNotAnnotateWhenUnderLimit(t *testing.T) {
	c, repo, _ := newTestChart(t, perUserNotesSchema)
	require.NoError(t, c.Commit(Diff{"inc": 1}, "u1"))
	require.NoError(t, c.Commit(Diff{"inc": 1}, "u2"))
	repo.failApplyDeltas = &applyDeltasFailure{}

	err := c.Save(context.Background())
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "more group(s) failed")
}

// 複数 group が失敗したら、全部のエラーを束ねて返す。
func TestSave_AggregatesErrorsFromAllGroups(t *testing.T) {
	c, repo, _ := newTestChart(t, perUserNotesSchema)
	require.NoError(t, c.Commit(Diff{"inc": 1}, "u1"))
	require.NoError(t, c.Commit(Diff{"inc": 2}, "u2"))
	repo.armError("FindCurrent", errors.New("boom1"))
	repo.armError("FindCurrent", errors.New("boom2"))

	err := c.Save(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "u1")
	assert.Contains(t, err.Error(), "u2")
}

// Save 中に走った Commit が同じ group のバッファを作り直していても、戻す側は
// 上書きせずマージする。
func TestRequeueFailed_MergesIntoLiveBuffer(t *testing.T) {
	c, _, _ := newTestChart(t, perUserNotesSchema)
	require.NoError(t, c.Commit(Diff{"inc": 5}, "u1"))

	stale := newGroupBuffer()
	stale.addInt("inc", 3)
	assert.Empty(t, c.requeueFailed("u1", stale, saveNothingApplied, errors.New("boom")))

	c.bufMu.Lock()
	defer c.bufMu.Unlock()
	assert.Equal(t, int64(8), c.buffer["u1"].ints["inc"])
	assert.Equal(t, 1, c.buffer["u1"].attempts, "attempts は大きい方を引き継ぐ")
}

func TestGroupBuffer_MergeFrom(t *testing.T) {
	dst := newGroupBuffer()
	dst.addInt("n", 2)
	dst.addUnique("k", []string{"a"})

	src := newGroupBuffer()
	src.addInt("n", 3)
	src.addInt("m", 1)
	src.addUnique("k", []string{"b"})
	src.addUnique("j", []string{"c"})

	dst.mergeFrom(src)

	assert.Equal(t, int64(5), dst.ints["n"])
	assert.Equal(t, int64(1), dst.ints["m"])
	assert.Equal(t, []string{"a", "b"}, setKeysSorted(dst.uniques["k"]))
	assert.Equal(t, []string{"c"}, setKeysSorted(dst.uniques["j"]))
}

// unique 列を 1 つも持たない側へ unique 付きのバッファを merge する。
// uniquesOnly を戻したときに実際に踏む経路。
func TestGroupBuffer_MergeFromIntoBufferWithoutUniques(t *testing.T) {
	dst := newGroupBuffer()
	dst.addInt("n", 1)

	src := newGroupBuffer()
	src.addUnique("k", []string{"a", "b"})

	dst.mergeFrom(src)
	assert.Equal(t, []string{"a", "b"}, setKeysSorted(dst.uniques["k"]))
}

func TestGroupBuffer_MergeFromSkipsEmptySet(t *testing.T) {
	dst := newGroupBuffer()
	src := newGroupBuffer()
	src.uniques = map[string]map[string]struct{}{"k": {}}
	dst.mergeFrom(src)
	assert.Empty(t, dst.uniques["k"])
}

// Save 中に走った Commit が unique 列を積んでいた group を、hour だけ通った
// 状態から戻す。uniquesOnly の中身が既存のバッファへマージされる。
func TestRequeueFailed_MergesUniquesIntoLiveBuffer(t *testing.T) {
	c, _, _ := newTestChart(t, perUserPvSchema)
	require.NoError(t, c.Commit(Diff{"upv.visitor": []string{"new"}}, "owner"))

	stale := newGroupBuffer()
	stale.addInt("pv.visitor", 9)
	stale.addUnique("upv.visitor", []string{"old"})
	dropped := c.requeueFailed("owner", stale, saveHourApplied, errors.New("boom"))
	require.Len(t, dropped, 1, "戻せない int を記録する")
	assert.Equal(t, droppedReasonNotRetryable, dropped[0].reason)
	assert.Equal(t, int64(9), dropped[0].ints)

	c.bufMu.Lock()
	defer c.bufMu.Unlock()
	assert.Equal(t, []string{"new", "old"}, setKeysSorted(c.buffer["owner"].uniques["upv.visitor"]))
	assert.Empty(t, c.buffer["owner"].ints, "hour 適用済みなので int は戻さない")
}

// uniqueIncrement / intersection 列に積まれた int は applyDiffs が deltas から
// 外すので、そもそも書かれない。「捨てた量」として数えると過大報告になる。
func TestRequeueFailed_DoesNotCountIntsOnNonDeltaColumns(t *testing.T) {
	c, _, _ := newTestChart(t, activeUsersSchema)

	stale := newGroupBuffer()
	stale.addInt("readWrite", 9) // intersection 列
	stale.addInt("read", 9)      // uniqueIncrement 列
	stale.addUnique("read", []string{"u1"})

	dropped := c.requeueFailed("", stale, saveHourApplied, errors.New("boom"))
	assert.Empty(t, dropped, "書かれない delta は捨てた量に数えない")
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

	// unique-temp 配列も保持されている。バッファは集合なので、同じ key を
	// 複数回 Commit しても配列には 1 度しか積まれない (濃度は上の read/write
	// アサーションのとおり変わらない)。
	uniqueRead, _ := row.Cols["read:unique"].([]string)
	assert.ElementsMatch(t, []string{"u1", "u2", "u3"}, uniqueRead)
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

// TestGetChart_BoundaryCursorOldestBucketZeroed は #1610 の核心ケース。
// 境界一致 cursor (12:00) + amount>1 で、最古表示バケットの実ログを
// upstream getChartRaw が range query から取りこぼし 0/空に補間する挙動を
// mk-go でも再現することを確認する。
//
// 本家の DB lower-bound gt は floor(cursor)+1span (=13:00) から amount-1
// step back するため、amount=5 では gt=09:00 となり最古表示バケット 08:00 は
// range 外。gt バケット (09:00) にログが在るため outdated-log backfill も
// 抑止され、08:00 は補間アンカーを持てず 0 になる。修正前は start=08:00 を
// lower-bound にしていたため 08:00 の実値 (inc=8 / total=10) を返していた。
func TestGetChart_BoundaryCursorOldestBucketZeroed(t *testing.T) {
	c, _, clk := newTestChart(t, notesSchema)
	// 08:00..12:00 の 5 連続バケットに書き込む。inc は hour 値、
	// total は accumulate なので 10/20/30/40/50 と累積する。
	for h := 8; h <= 12; h++ {
		clk.set(time.Date(2026, 4, 9, h, 0, 0, 0, time.UTC))
		require.NoError(t, c.Commit(Diff{"local.inc": int64(h), "local.total": int64(10)}, ""))
		require.NoError(t, c.Save(context.Background()))
	}
	clk.set(time.Date(2026, 4, 9, 20, 0, 0, 0, time.UTC))

	aligned := time.Date(2026, 4, 9, 12, 0, 0, 0, time.UTC)
	out, err := c.GetChart(context.Background(), SpanHour, 5, &aligned, "")
	require.NoError(t, err)
	// newest-first: 12:00..09:00 は実値、最古 08:00 (idx4) は 0。
	assert.Equal(t, []int64{12, 11, 10, 9, 0}, out["local.inc"], "oldest bucket must be zeroed (upstream gt quirk)")
	// accumulate 列も最古バケットは carry されず 0 になる。
	assert.Equal(t, []int64{50, 40, 30, 20, 0}, out["local.total"], "oldest accumulate bucket must be zeroed")
}

// TestGetChart_BoundaryCursorGapBackfillsOldest は、境界一致 cursor + gt
// バケット (09:00) が欠損しているとき、outdated-log backfill が gt より古い
// 最新ログを取得し、それが最古表示バケット (08:00) に exact-match して実値で
// 埋まることを確認する。gt バケット欠損の有無で最古バケットの値が変わるのが
// upstream の仕様。
func TestGetChart_BoundaryCursorGapBackfillsOldest(t *testing.T) {
	c, _, clk := newTestChart(t, notesSchema)
	// 09:00 (gt バケット) を欠落させ、08:00 / 10:00 / 11:00 / 12:00 に書き込む。
	// total は accumulate なので 10/20/30/40 と累積する。
	for _, h := range []int{8, 10, 11, 12} {
		clk.set(time.Date(2026, 4, 9, h, 0, 0, 0, time.UTC))
		require.NoError(t, c.Commit(Diff{"local.inc": int64(h * 10), "local.total": int64(10)}, ""))
		require.NoError(t, c.Save(context.Background()))
	}
	clk.set(time.Date(2026, 4, 9, 20, 0, 0, 0, time.UTC))

	aligned := time.Date(2026, 4, 9, 12, 0, 0, 0, time.UTC)
	out, err := c.GetChart(context.Background(), SpanHour, 5, &aligned, "")
	require.NoError(t, err)
	// 12:00=120, 11:00=110, 10:00=100, 09:00=gap→0, 08:00=80 (outdated backfill)。
	assert.Equal(t, []int64{120, 110, 100, 0, 80}, out["local.inc"])
	// accumulate 列: gap の 09:00 は outdated backfill した 08:00 の値を carry。
	assert.Equal(t, []int64{40, 30, 20, 10, 10}, out["local.total"])
}

// TestGetChart_BoundaryCursorGroupedAmountTwo は grouped chart + amount=2 の
// 最小 quirk ケース。境界一致 cursor では gt==end の単一バケット range となり、
// end バケットにログが在ると backfill 抑止で end-1span (最古) が 0 になる。
func TestGetChart_BoundaryCursorGroupedAmountTwo(t *testing.T) {
	c, _, clk := newTestChart(t, perUserNotesSchema)
	for h := 11; h <= 12; h++ {
		clk.set(time.Date(2026, 4, 9, h, 0, 0, 0, time.UTC))
		require.NoError(t, c.Commit(Diff{"inc": int64(h)}, "u1"))
		require.NoError(t, c.Save(context.Background()))
	}
	clk.set(time.Date(2026, 4, 9, 20, 0, 0, 0, time.UTC))

	aligned := time.Date(2026, 4, 9, 12, 0, 0, 0, time.UTC)
	out, err := c.GetChart(context.Background(), SpanHour, 2, &aligned, "u1")
	require.NoError(t, err)
	// 12:00=12 (gt バケット=実値)、最古 11:00 は range 外 + backfill 抑止で 0。
	assert.Equal(t, []int64{12, 0}, out["inc"])
}

// TestGetChart_NonBoundaryCursorIncludesOldest は、非境界 cursor (12:30) では
// gt が最古表示バケットと一致し、最古バケットの実値が従来どおり含まれる
// (#1610 の修正が境界一致ケースのみに限定されている) ことを保証する回帰テスト。
func TestGetChart_NonBoundaryCursorIncludesOldest(t *testing.T) {
	c, _, clk := newTestChart(t, notesSchema)
	// 11:00 / 12:00 / 13:00 に書き込む。
	for h := 11; h <= 13; h++ {
		clk.set(time.Date(2026, 4, 9, h, 0, 0, 0, time.UTC))
		require.NoError(t, c.Commit(Diff{"local.inc": int64(h)}, ""))
		require.NoError(t, c.Save(context.Background()))
	}
	clk.set(time.Date(2026, 4, 9, 20, 0, 0, 0, time.UTC))

	// mid-bucket cursor 12:30 → end=ceil=13:00, gt=11:00 (=最古表示バケット)。
	mid := time.Date(2026, 4, 9, 12, 30, 0, 0, time.UTC)
	out, err := c.GetChart(context.Background(), SpanHour, 3, &mid, "")
	require.NoError(t, err)
	// 最古 11:00 も実値で含まれる。
	assert.Equal(t, []int64{13, 12, 11}, out["local.inc"])
}

// TestGetChart_BoundaryCursorSpanDayOldestZeroed は SpanDay でも境界一致
// cursor の最古バケットが 0 に補間されることを確認する。
func TestGetChart_BoundaryCursorSpanDayOldestZeroed(t *testing.T) {
	c, _, clk := newTestChart(t, notesSchema)
	// 2026-04-05..04-09 の 5 連続 day バケットに書き込む。
	for d := 5; d <= 9; d++ {
		clk.set(time.Date(2026, 4, d, 12, 0, 0, 0, time.UTC))
		require.NoError(t, c.Commit(Diff{"local.inc": int64(d)}, ""))
		require.NoError(t, c.Save(context.Background()))
	}
	clk.set(time.Date(2026, 4, 20, 0, 0, 0, 0, time.UTC))

	aligned := time.Date(2026, 4, 9, 0, 0, 0, 0, time.UTC)
	out, err := c.GetChart(context.Background(), SpanDay, 5, &aligned, "")
	require.NoError(t, err)
	// newest-first: 04-09..04-06 は実値、最古 04-05 (idx4) は 0。
	assert.Equal(t, []int64{9, 8, 7, 6, 0}, out["local.inc"])
}

// TestGetChart_BoundaryCursorAmountOneOvershoots は amount=1 境界一致 cursor の
// エッジ。gt=floor(cursor)+1span が end (=cursor バケット) より新しくなり
// range query が空になるため FindLatest fallback が走る。fallback で得た
// 最新ログが cursor バケットより新しい場合は exact-match せず 0 になる
// (upstream の recentLog overshoot と同じ)。
func TestGetChart_BoundaryCursorAmountOneOvershoots(t *testing.T) {
	c, _, clk := newTestChart(t, notesSchema)
	// 12:00 バケットに 42、13:00 バケットに 99 を書き込む。
	clk.set(time.Date(2026, 4, 9, 12, 0, 0, 0, time.UTC))
	require.NoError(t, c.Commit(Diff{"local.inc": int64(42)}, ""))
	require.NoError(t, c.Save(context.Background()))
	clk.set(time.Date(2026, 4, 9, 13, 0, 0, 0, time.UTC))
	require.NoError(t, c.Commit(Diff{"local.inc": int64(99)}, ""))
	require.NoError(t, c.Save(context.Background()))
	clk.set(time.Date(2026, 4, 9, 20, 0, 0, 0, time.UTC))

	aligned := time.Date(2026, 4, 9, 12, 0, 0, 0, time.UTC)
	out, err := c.GetChart(context.Background(), SpanHour, 1, &aligned, "")
	require.NoError(t, err)
	// FindLatest は最新の 13:00 を返すが cursor バケット 12:00 と一致しないため 0。
	assert.Equal(t, []int64{0}, out["local.inc"])
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

// rowUniqueIndex.set が返す map は **共有**。bake は intersection のループで
// 結果を破壊的に絞り込むので、必ず clone してから使わなければならない。
//
// 同じ unique 列を参照する intersection 列が 2 本あると、共有 map を返した
// 場合に 1 本目の絞り込みが 2 本目に漏れて濃度が過小になる。activeUsers は
// intersection が 1 本しかなくこの形にならないので、専用の schema で試す。
func TestBakeUniqueAndIntersection_DoesNotMutateSharedRowSet(t *testing.T) {
	schema := Schema{
		Name: "twoIntersections",
		Columns: []ColumnDef{
			{Name: "a", UniqueIncrement: true},
			{Name: "b", UniqueIncrement: true},
			{Name: "c", UniqueIncrement: true},
			{Name: "ab", IntersectionOf: []string{"a", "b"}},
			{Name: "ac", IntersectionOf: []string{"a", "c"}},
		},
	}
	row := &Row{Cols: map[string]any{
		"a:unique": []string{"u1", "u2", "u3", "u4"},
		"b:unique": []string{"u1"},
		"c:unique": []string{"u2"},
	}}
	idx := newRowUniqueIndex(row)
	before := len(idx.set("a"))

	got := bakeUniqueAndIntersection(schema, idx, newGroupBuffer())

	// ab = {u1..u4} ∩ {u1} = {u1}、ac = {u1..u4} ∩ {u2} = {u2}。
	// 共有 map を絞り込んでいると ac が (a∩b)∩c = {} になって 0 に落ちる。
	assert.Equal(t, int64(1), got["ab"])
	assert.Equal(t, int64(1), got["ac"], "1 本目の intersection が 2 本目に漏れていない")
	assert.Equal(t, before, len(idx.set("a")), "共有の集合を書き換えていない")
}

// bake するのは **この window に差分があった unique 列だけ** (upstream core.ts の
// `Object.entries(finalDiffs)`)。intersection 列は差分の有無によらず毎回 SET する
// (upstream も `Object.entries(this.schema)` を回す)。
//
// row 側の集合が正しく読めていれば、差分の無い列を bake し直しても同じ値になる
// ので観測できる差は出ない。差が出るのは **行の unique-temp 配列が空なのに
// 濃度列に値がある** ときで、Clean() が古い行の配列をリセットした後がそれに
// あたる。現状 Save は現在バケットしか触らないので到達しないが、upstream と
// 同じ範囲に揃えておく。
func TestBakeUniqueAndIntersection_OnlyBakesColumnsWithDiff(t *testing.T) {
	row := &Row{Cols: map[string]any{
		"read:unique":  []string{"u1", "u2"},
		"write:unique": []string{"u1"},
	}}
	gb := newGroupBuffer()
	gb.addUnique("read", []string{"u3"})

	got := bakeUniqueAndIntersection(activeUsersSchema, newRowUniqueIndex(row), gb)

	assert.Equal(t, int64(3), got["read"], "差分のあった列は row の集合と union して bake")
	_, ok := got["write"]
	assert.False(t, ok, "差分の無い unique 列は SET しない")
	// readWrite = {u1,u2,u3} ∩ {u1} = {u1}
	assert.Equal(t, int64(1), got["readWrite"], "intersection は毎回 SET する")
}

// 行の unique-temp 配列が空で濃度列にだけ値がある状態 (Clean 後に相当) では、
// 差分の無い列まで bake すると 0 で上書きしてしまう。上の範囲制限がそれを防ぐ。
func TestBakeUniqueAndIntersection_DoesNotZeroClearedColumn(t *testing.T) {
	row := &Row{Cols: map[string]any{
		"read:unique":  []string{},
		"write:unique": []string{},
	}}
	gb := newGroupBuffer()
	gb.addUnique("read", []string{"u1"})

	got := bakeUniqueAndIntersection(activeUsersSchema, newRowUniqueIndex(row), gb)

	assert.Equal(t, int64(1), got["read"])
	_, ok := got["write"]
	assert.False(t, ok, "配列が空でも、差分が無ければ 0 を書きに行かない")
}

func TestBakeUniqueAndIntersection_EmptyIntersectionList(t *testing.T) {
	schema := Schema{
		Name: "x",
		Columns: []ColumnDef{
			{Name: "i", IntersectionOf: []string{}},
		},
	}
	row := &Row{Cols: map[string]any{}}
	got := bakeUniqueAndIntersection(schema, newRowUniqueIndex(row), newGroupBuffer())
	assert.Equal(t, int64(0), got["i"])
}

func TestBakeUniqueAndIntersection_RowUniquesNonStringSlice(t *testing.T) {
	// rowUniqueIndex.set の "[]string でない" フォールバック分岐を踏むため
	// あえて任意型を入れる。
	schema := Schema{
		Name: "x",
		Columns: []ColumnDef{
			{Name: "u", UniqueIncrement: true},
		},
	}
	row := &Row{Cols: map[string]any{"u:unique": "not-a-slice"}}
	gb := newGroupBuffer()
	gb.addUnique("u", []string{"a"})
	got := bakeUniqueAndIntersection(schema, newRowUniqueIndex(row), gb)
	assert.Equal(t, int64(1), got["u"])
}

func TestGroupBuffer_UniqueKeysAreUnioned(t *testing.T) {
	gb := newGroupBuffer()
	gb.addUnique("k", []string{"a"})
	gb.addUnique("k", []string{"b", "a"})
	assert.Equal(t, []string{"a", "b"}, setKeysSorted(gb.uniques["k"]))
}

func TestParsePgTextArray(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"empty array", "{}", []string{}},
		{"unquoted", "{u1,u2,u3}", []string{"u1", "u2", "u3"}},
		{"quoted", `{"u1","u2"}`, []string{"u1", "u2"}},
		{"mixed", `{u1,"u 2"}`, []string{"u1", "u 2"}},
		{"comma inside quotes", `{"a,b",c}`, []string{"a,b", "c"}},
		{"escaped quote", `{"a\"b"}`, []string{`a"b`}},
		{"escaped backslash", `{"a\\b"}`, []string{`a\b`}},
		{"braces inside quotes", `{"{x}"}`, []string{"{x}"}},
		{"empty string element", `{""}`, []string{""}},
		// 引用符なしの NULL は SQL NULL。要素として数えない。
		{"sql null dropped", `{a,NULL,b}`, []string{"a", "b"}},
		// 引用符付きの NULL は文字列。
		{"quoted NULL kept", `{a,"NULL"}`, []string{"a", "NULL"}},
		// 配列リテラルでないものは nil (呼び出し側は空集合として扱う)。
		{"not an array", "u1", nil},
		{"empty string", "", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, parsePgTextArray(tc.in))
		})
	}
}

// 書き込み側 (pgArrayLiteral) と読み込み側 (parsePgTextArray) が往復すること。
// 片方だけ直すとエスケープの取り違えに気付けない。
func TestPgArrayLiteral_RoundTrip(t *testing.T) {
	values := []string{
		"u1",
		"host.example",
		`a"b`,
		`c\d`,
		"a,b",
		"{x}",
		"",
		"NULL",
		"日本語",
		" leading and trailing ",
	}
	assert.Equal(t, values, parsePgTextArray(pgArrayLiteral(values)))
}

func TestToStringSlice(t *testing.T) {
	assert.Nil(t, toStringSlice(nil))
	assert.Equal(t, []string{"a"}, toStringSlice([]string{"a"}))
	assert.Equal(t, []string{"a", "b"}, toStringSlice("{a,b}"))
	assert.Equal(t, []string{"a", "b"}, toStringSlice([]byte("{a,b}")))
}

// デコードできない値は **黙って空にせず** warn を出す。黙って空になるのが
// #2652 の壊れ方そのものなので、型が違う経路と配列リテラルとして読めない経路の
// 両方で出ることを固定する。
func TestToStringSlice_WarnsOnUndecodableValue(t *testing.T) {
	cases := []struct {
		name   string
		in     any
		reason string
	}{
		{"unexpected type", 42, "unexpected driver type"},
		{"not an array literal", "not-an-array", "not a PostgreSQL array literal"},
		{"truncated literal", "{a,b", "not a PostgreSQL array literal"},
		{"dimension prefix", "[0:1]={a,b}", "not a PostgreSQL array literal"},
		{"empty string", "", "not a PostgreSQL array literal"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			buf := captureWarnings(t)
			assert.Nil(t, toStringSlice(tc.in))
			assert.Contains(t, buf.String(), "cannot decode unique-temp column")
			// reason は本番ログで「型が変わったのか / 出力形式が変わったのか」を
			// 切り分ける唯一の情報なので、入れ替わりも検出する。
			assert.Contains(t, buf.String(), `reason="`+tc.reason+`"`)
		})
	}
}

// **正常系では warn を出さない。** ここが緩いと、空配列 `{}` (新規バケットや
// Clean 済みの行) で毎回 warn を呼ぶ実装でもテストが通ってしまう。それは
// sync.Once と噛み合うと最悪で、起動直後の偽 warn が Once を使い切り、以後の
// 本物の decode 失敗が**永久に記録されなくなる**。
func TestToStringSlice_DoesNotWarnOnValidValues(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want []string
	}{
		{"empty array", "{}", []string{}},
		{"populated array", "{a,b}", []string{"a", "b"}},
		{"bytes", []byte("{a}"), []string{"a"}},
		{"already a slice", []string{"a"}, []string{"a"}},
		{"empty slice", []string{}, []string{}},
		{"nil", nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			buf := captureWarnings(t)
			assert.Equal(t, tc.want, toStringSlice(tc.in))
			assert.Empty(t, buf.String(), "正常な値で warn を出さない")
		})
	}
}

// captureWarnings redirects slog warnings into a buffer for the duration of the
// test and resets the once-guard so each case observes its own output.
func captureWarnings(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	restore := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	uniqueTempDecodeWarnOnce = sync.Once{}
	t.Cleanup(func() {
		slog.SetDefault(restore)
		uniqueTempDecodeWarnOnce = sync.Once{}
	})
	return &buf
}

// warn はプロセス 1 回だけ。scanRow は行ごと・unique 列ごとに呼ばれるので、
// 抑制しないと公開 GET 1 本で数千行出る。
func TestToStringSlice_WarnsOnlyOnce(t *testing.T) {
	buf := captureWarnings(t)
	for range 100 {
		toStringSlice(42)
	}
	assert.Equal(t, 1, strings.Count(buf.String(), "cannot decode unique-temp column"))
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
	require.Error(t, c.Save(context.Background()))

	// claim の段階では 1 行も書いていないので、int の delta ごと戻る。
	// ここを saveHourApplied として扱うと delta が黙って消える。
	c.bufMu.Lock()
	defer c.bufMu.Unlock()
	require.NotNil(t, c.buffer[""])
	assert.Equal(t, int64(1), c.buffer[""].ints["local.inc"])
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

// unique-temp への append は span ごとに行の既存要素を除き、残りを昇順で送る。
// 追加するものが無ければ array_cat を発行しない。
func TestApplyDiffs_UniqueAppendsFilteredPerSpanSortedAndOmittedWhenEmpty(t *testing.T) {
	c, repo, _ := newTestChart(t, activeUsersSchema)
	// hour と day で行の持っているものを変える。
	hour := &Row{ID: 1, Cols: map[string]any{
		"read:unique":  []string{"u2"},
		"write:unique": []string{"w1"},
	}}
	day := &Row{ID: 2, Cols: map[string]any{
		"read:unique":  []string{},
		"write:unique": []string{"w1"},
	}}
	repo.hour[""] = []*Row{hour}
	repo.day[""] = []*Row{day}

	gb := newGroupBuffer()
	gb.addUnique("read", []string{"u3", "u1", "u2"})
	gb.addUnique("write", []string{"w1"}) // 両 span とも行が既に持っている
	outcome, err := c.applyDiffs(context.Background(), hour, day, gb)
	require.NoError(t, err)
	assert.Equal(t, saveAllApplied, outcome)

	require.Len(t, repo.calls, 2)
	assert.Equal(t, SpanHour, repo.calls[0].span)
	assert.Equal(t, SpanDay, repo.calls[1].span)

	// hour は u2 を持っているので除かれる。昇順。
	assert.Equal(t, []string{"u1", "u3"}, repo.calls[0].appends["read"])
	// day は空なので全部。昇順。
	assert.Equal(t, []string{"u1", "u2", "u3"}, repo.calls[1].appends["read"])

	for i, call := range repo.calls {
		_, ok := call.appends["write"]
		assert.False(t, ok, "call %d: 追加するものが無ければ array_cat を発行しない", i)
	}
}

func TestApplyDiffs_IntersectionColumnInDiffSkipped(t *testing.T) {
	// applyDiffs の `if col.IntersectionOf != nil { continue }` 分岐は
	// intersection 列そのものが int として Commit されたときに踏む。
	// 通常の呼び出し元は出さないキーなので、ここで明示的に踏ませる。
	c, _, _ := newTestChart(t, activeUsersSchema)
	hour := &Row{ID: 1, Cols: map[string]any{}}
	day := &Row{ID: 2, Cols: map[string]any{}}
	repo := c.repo.(*fakeRepo)
	repo.hour[""] = []*Row{hour}
	repo.day[""] = []*Row{day}
	gb := newGroupBuffer()
	gb.addInt("readWrite", 99)
	outcome, err := c.applyDiffs(context.Background(), hour, day, gb)
	require.NoError(t, err)
	assert.Equal(t, saveAllApplied, outcome)
	// intersection 列は delta として書かず、bake した絶対値だけを SET する。
	assert.Equal(t, int64(0), toInt64(hour.Cols["readWrite"]))
}

func TestApplyDiffs_UniqueIncrementSkipsEmptySet(t *testing.T) {
	// applyDiffs の `len(set) > 0` の false 分岐 (unique 列に 1 件も
	// 積まれていない group) を踏む。
	c, _, _ := newTestChart(t, activeUsersSchema)
	hour := &Row{ID: 1, Cols: map[string]any{}}
	day := &Row{ID: 2, Cols: map[string]any{}}
	repo := c.repo.(*fakeRepo)
	repo.hour[""] = []*Row{hour}
	repo.day[""] = []*Row{day}
	gb := newGroupBuffer()
	gb.addInt("read", 1) // uniqueIncrement 列だが int なので appends には出ない
	outcome, err := c.applyDiffs(context.Background(), hour, day, gb)
	require.NoError(t, err)
	assert.Equal(t, saveAllApplied, outcome)
	_, ok := hour.Cols["read:unique"]
	assert.False(t, ok, "空集合では array_cat を発行しない")
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
