package chart

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// TickFunc returns the current major or minor "snapshot" values for a
// chart. Major ticks run rarely (typically once per resync to fix
// CASCADE-style drift) and overwrite columns with absolute totals;
// minor ticks run as part of every Save() and may be a no-op for charts
// that do all their bookkeeping via Commit(). Either function may be
// nil to indicate that span has nothing to compute.
type TickFunc func(ctx context.Context, group string, major bool) (map[string]int64, error)

// Chart aggregates Commit() / Tick() events into per-hour and per-day
// buckets. The struct is safe for concurrent Commit() calls; Save() and
// Tick() should not run concurrently with each other but may interleave
// freely with Commit() (the buffer is captured atomically).
//
// One Chart instance owns one Schema and one logical chart name. The
// concrete chart wrappers in `internal/core/chart/charts/*` embed (or
// hold) a *Chart and expose typed Update() / Read() / Write() helpers.
type Chart struct {
	schema Schema
	repo   Repository
	lock   Locker
	clock  Clock
	tick   TickFunc

	bufMu  sync.Mutex
	buffer []bufferedDiff
}

// bufferedDiff is one queued Commit() entry. The diff is stored as the
// raw map; merging across diffs happens on Save().
type bufferedDiff struct {
	group string
	diff  Diff
}

// Config bundles the dependencies a Chart needs at construction time.
// Required fields: Schema, Repo, Lock. Clock defaults to SystemClock,
// Tick defaults to a no-op.
type Config struct {
	Schema Schema
	Repo   Repository
	Lock   Locker
	Clock  Clock
	Tick   TickFunc
}

// New constructs a Chart from Config.
func New(cfg Config) (*Chart, error) {
	if cfg.Schema.Name == "" {
		return nil, ErrSchemaName
	}
	if cfg.Repo == nil {
		return nil, errors.New("chart: Repo is required")
	}
	if cfg.Lock == nil {
		return nil, errors.New("chart: Lock is required")
	}
	clk := cfg.Clock
	if clk == nil {
		clk = SystemClock{}
	}
	return &Chart{
		schema: cfg.Schema,
		repo:   cfg.Repo,
		lock:   cfg.Lock,
		clock:  clk,
		tick:   cfg.Tick,
	}, nil
}

// Name returns the chart's camelCase name.
func (c *Chart) Name() string { return c.schema.Name }

// IsGrouped reports whether this chart aggregates data into per-group
// buckets (per-user, per-host etc.). Used by the chart cron processor
// to skip charts that cannot be ticked without an external enumeration
// source.
func (c *Chart) IsGrouped() bool { return c.schema.Grouped }

// Commit queues a diff to be merged into the current bucket on the next
// Save(). The diff values may be int (any signed int type), int64, or
// []string for uniqueIncrement columns. Unknown keys are silently
// dropped on Save().
//
// `group` must be non-empty when the schema is grouped; it must be
// empty otherwise. Commit returns an error in those mismatch cases so
// callers catch wiring mistakes early.
func (c *Chart) Commit(diff Diff, group string) error {
	if c == nil {
		return nil
	}
	if c.schema.Grouped && group == "" {
		return errors.New("chart: group is required for grouped chart")
	}
	if !c.schema.Grouped && group != "" {
		return errors.New("chart: group must be empty for ungrouped chart")
	}
	// 0 や空配列のエントリは省く (本家と同じ挙動)
	cleaned := make(Diff, len(diff))
	for k, v := range diff {
		switch x := v.(type) {
		case int:
			if x != 0 {
				cleaned[k] = int64(x)
			}
		case int64:
			if x != 0 {
				cleaned[k] = x
			}
		case []string:
			if len(x) > 0 {
				cleaned[k] = x
			}
		}
	}
	if len(cleaned) == 0 {
		return nil
	}
	c.bufMu.Lock()
	c.buffer = append(c.buffer, bufferedDiff{group: group, diff: cleaned})
	c.bufMu.Unlock()
	return nil
}

// Save flushes the buffered Commit() entries to the database. The
// algorithm mirrors the upstream:
//
//  1. Collect all distinct groups in the buffer.
//  2. For each group, claim the current hour and day rows.
//  3. Merge all diffs targeted at that group into a single set of
//     int deltas + unique-temp appends + bake operations, then issue
//     two UPDATE statements (hour + day).
//  4. Drop only the diffs that were applied (other groups' diffs stay
//     in the buffer).
//
// Save is safe to call concurrently with Commit; it captures the
// buffer under the mutex before processing.
func (c *Chart) Save(ctx context.Context) error {
	c.bufMu.Lock()
	if len(c.buffer) == 0 {
		c.bufMu.Unlock()
		return nil
	}
	pending := c.buffer
	c.buffer = nil
	c.bufMu.Unlock()

	// 集計対象 group を一意化。空文字列 ("") は ungrouped charts のキー。
	groups := uniqueGroups(pending)
	for _, g := range groups {
		hour, err := c.claimCurrentLog(ctx, g, SpanHour)
		if err != nil {
			return fmt.Errorf("claim hour for %s: %w", c.schema.Name, err)
		}
		day, err := c.claimCurrentLog(ctx, g, SpanDay)
		if err != nil {
			return fmt.Errorf("claim day for %s: %w", c.schema.Name, err)
		}
		groupDiffs := filterByGroup(pending, g)
		if err := c.applyDiffs(ctx, hour, day, groupDiffs); err != nil {
			return fmt.Errorf("apply diffs for %s/%s: %w", c.schema.Name, g, err)
		}
	}
	return nil
}

// Tick computes a snapshot via the configured TickFunc and writes the
// result directly into the current bucket. The major flag chooses
// between major and minor variants and is forwarded to TickFunc.
func (c *Chart) Tick(ctx context.Context, major bool, group string) error {
	if c.tick == nil {
		return nil
	}
	if c.schema.Grouped && group == "" {
		return errors.New("chart: group is required for grouped chart")
	}
	cols, err := c.tick(ctx, group, major)
	if err != nil {
		return err
	}
	if len(cols) == 0 {
		return nil
	}
	hour, err := c.claimCurrentLog(ctx, group, SpanHour)
	if err != nil {
		return err
	}
	day, err := c.claimCurrentLog(ctx, group, SpanDay)
	if err != nil {
		return err
	}
	if err := c.repo.SetColumns(ctx, SpanHour, hour.ID, cols); err != nil {
		return err
	}
	return c.repo.SetColumns(ctx, SpanDay, day.ID, cols)
}

// Resync runs a major Tick. Exposed for chart management endpoints.
func (c *Chart) Resync(ctx context.Context, group string) error {
	return c.Tick(ctx, true, group)
}

// Clean resets the unique-temp arrays of records older than 1 day but
// younger than 3 days. The intent is to free storage while preserving
// the baked cardinality columns. The bounds match the upstream impl.
func (c *Chart) Clean(ctx context.Context) error {
	cols := c.schema.uniqueColumnNames()
	if len(cols) == 0 {
		return nil
	}
	now := truncateToHour(c.clock.Now())
	gt := now.Unix() - 60*60*24*3
	lt := now.Unix() - 60*60*24
	if err := c.repo.ResetUniqueTempColumns(ctx, SpanHour, gt, lt, cols); err != nil {
		return err
	}
	return c.repo.ResetUniqueTempColumns(ctx, SpanDay, gt, lt, cols)
}

// claimCurrentLog returns the row for the bucket containing now(). If
// no row exists yet a new one is inserted, seeded from the most recent
// row's accumulate columns when present. The insert is wrapped in a
// distributed lock to prevent two writers from creating duplicate rows.
func (c *Chart) claimCurrentLog(ctx context.Context, group string, span Span) (*Row, error) {
	now := truncateToSpan(c.clock.Now(), span)
	ts := now.Unix()
	row, err := c.repo.FindCurrent(ctx, span, group, ts)
	if err == nil {
		return row, nil
	}
	if !errors.Is(err, ErrRowNotFound) {
		return nil, err
	}
	// 新規作成パスへ。最近のログから accumulate 列を引き継ぐ。
	latest, err := c.repo.FindLatest(ctx, span, group)
	if err != nil && !errors.Is(err, ErrRowNotFound) {
		return nil, err
	}
	seed := c.newLogValues(latest)

	lockKey := fmt.Sprintf("%s:%d:%s", c.schema.Name, ts, span)
	if group != "" {
		lockKey += ":" + group
	}
	release, err := c.lock.Acquire(ctx, lockKey, 30*time.Second)
	if err != nil {
		return nil, err
	}
	defer release()

	// ロック取得後にもう一度確認。レースで他のプロセスが作っていれば
	// その row を返す。
	if existing, err := c.repo.FindCurrent(ctx, span, group, ts); err == nil {
		return existing, nil
	} else if !errors.Is(err, ErrRowNotFound) {
		return nil, err
	}

	cols := make(map[string]any, len(seed))
	for k, v := range seed {
		cols[k] = v
	}
	return c.repo.Insert(ctx, span, group, ts, cols)
}

// newLogValues builds the seed map for a new bucket. Accumulate columns
// inherit the previous bucket's value; everything else starts at zero.
// uniqueIncrement columns also start at zero (their temp arrays start
// empty by default in the SQL DDL).
func (c *Chart) newLogValues(latest *Row) map[string]int64 {
	out := make(map[string]int64, len(c.schema.Columns))
	for _, col := range c.schema.Columns {
		if col.Accumulate && latest != nil {
			if v, ok := latest.Cols[col.Name]; ok {
				out[col.Name] = toInt64(v)
				continue
			}
		}
		out[col.Name] = 0
	}
	return out
}

// applyDiffs merges a slice of buffered diffs targeting one group into
// a single (deltas, uniqueAppends, setInts) tuple per span and pushes
// it to the repository. setInts is used for uniqueIncrement bake and
// intersection columns whose absolute value cannot be expressed as a
// delta.
func (c *Chart) applyDiffs(ctx context.Context, hour, day *Row, diffs []bufferedDiff) error {
	merged := mergeDiffs(diffs)
	deltas := make(map[string]int64)
	appends := make(map[string][]string)
	for _, col := range c.schema.Columns {
		v, ok := merged[col.Name]
		if !ok {
			continue
		}
		if col.UniqueIncrement {
			if items, ok := v.([]string); ok && len(items) > 0 {
				appends[col.Name] = items
			}
			continue
		}
		if col.IntersectionOf != nil {
			continue
		}
		if n, ok := v.(int64); ok && n != 0 {
			deltas[col.Name] = n
		}
	}

	hourSetInts := bakeUniqueAndIntersection(c.schema, hour, merged)
	daySetInts := bakeUniqueAndIntersection(c.schema, day, merged)

	if err := c.repo.ApplyDeltas(ctx, SpanHour, hour.ID, deltas, appends, hourSetInts); err != nil {
		return err
	}
	return c.repo.ApplyDeltas(ctx, SpanDay, day.ID, deltas, appends, daySetInts)
}

// mergeDiffs combines a slice of diffs into a single map. Integer
// values are summed; string slices are concatenated.
func mergeDiffs(diffs []bufferedDiff) Diff {
	out := make(Diff)
	for _, d := range diffs {
		for k, v := range d.diff {
			cur, exists := out[k]
			if !exists {
				out[k] = v
				continue
			}
			switch x := v.(type) {
			case int64:
				if y, ok := cur.(int64); ok {
					out[k] = y + x
				}
			case []string:
				if y, ok := cur.([]string); ok {
					out[k] = append(y, x...)
				}
			}
		}
	}
	return out
}

// bakeUniqueAndIntersection computes the absolute cardinality value for
// each uniqueIncrement and intersection column based on the merged diff
// and the current row's unique-temp arrays. The returned map contains
// only the columns the row should be SET to (not deltas).
func bakeUniqueAndIntersection(schema Schema, row *Row, merged Diff) map[string]int64 {
	out := make(map[string]int64)
	rowUniques := func(name string) []string {
		if v, ok := row.Cols[name+":unique"].([]string); ok {
			return v
		}
		// gormは pgsql の text[] を []byte でも返すケースがあるので、
		// nil として扱って空集合と同じにする。
		return nil
	}
	for _, col := range schema.Columns {
		if col.UniqueIncrement {
			set := make(map[string]struct{})
			for _, v := range rowUniques(col.Name) {
				set[v] = struct{}{}
			}
			if items, ok := merged[col.Name].([]string); ok {
				for _, v := range items {
					set[v] = struct{}{}
				}
			}
			out[col.Name] = int64(len(set))
		}
	}
	for _, col := range schema.Columns {
		if col.IntersectionOf == nil {
			continue
		}
		// intersection は集合演算: union(rowTemp, mergedDiff) を各キーごとに
		// 求めて intersect する。
		merge := func(name string) map[string]struct{} {
			s := make(map[string]struct{})
			for _, v := range rowUniques(name) {
				s[v] = struct{}{}
			}
			if items, ok := merged[name].([]string); ok {
				for _, v := range items {
					s[v] = struct{}{}
				}
			}
			return s
		}
		if len(col.IntersectionOf) == 0 {
			out[col.Name] = 0
			continue
		}
		current := merge(col.IntersectionOf[0])
		for _, k := range col.IntersectionOf[1:] {
			target := merge(k)
			for v := range current {
				if _, ok := target[v]; !ok {
					delete(current, v)
				}
			}
		}
		out[col.Name] = int64(len(current))
	}
	return out
}

// uniqueGroups returns the distinct group identifiers in the buffer in
// insertion order. Empty string is preserved as the "ungrouped" key.
func uniqueGroups(diffs []bufferedDiff) []string {
	seen := make(map[string]struct{}, len(diffs))
	out := make([]string, 0, len(diffs))
	for _, d := range diffs {
		if _, ok := seen[d.group]; ok {
			continue
		}
		seen[d.group] = struct{}{}
		out = append(out, d.group)
	}
	return out
}

// filterByGroup returns the subset of diffs targeting the given group.
func filterByGroup(diffs []bufferedDiff, group string) []bufferedDiff {
	out := make([]bufferedDiff, 0, len(diffs))
	for _, d := range diffs {
		if d.group == group {
			out = append(out, d)
		}
	}
	return out
}

// GetChart returns the result for the requested span/amount window.
// The semantics mirror upstream:
//
//   - The window is `amount` buckets long, ending at `cursor` if given
//     or now() otherwise.
//   - Missing buckets are interpolated from the most recent prior log
//     so accumulate columns appear continuous.
//   - The result is keyed by dot-notation column names; values are
//     ordered newest-first (index 0 = end, last index = oldest), matching
//     upstream Misskey TS so the frontend's `data.slice().reverse()` lines
//     up with its oldest-first axis labels.
func (c *Chart) GetChart(ctx context.Context, span Span, amount int, cursor *time.Time, group string) (Result, error) {
	if amount <= 0 {
		amount = 1
	}
	// upstream getChartRaw は window 末尾バケット (lt) を、cursor 指定時は
	// truncate(cursor + 1span - 1ms) = ceil(cursor)、未指定 (now) 時は
	// truncate(now) = floor(now) で求める。後者は getCurrentDate 相当。
	// 両者で丸め方向が違うため cursor の有無で分岐する (#1565)。
	var end time.Time
	// gtBase は DB lower-bound (gt) を amount-1 step back する前のアンカー。
	// upstream は cursor 指定時に floor(cursor) + 1span、未指定時は floor(now)
	// から引く。境界一致 cursor では gtBase が end (=ceil(cursor)=floor(cursor))
	// より 1span 新しくなり、gt が最古表示バケットより 1span 新しい位置に来る。
	// 結果 range query が最古バケットの実ログを取りこぼし、outdated-log
	// backfill (logs.at(-1).date == gt) も抑止されて最古バケットが 0/空に
	// 補間される。非境界 cursor / nil now path では gtBase == end なので
	// gt は最古表示バケットと一致し従来挙動を保つ (#1610 / #1565 follow-up)。
	var gtBase time.Time
	if cursor != nil {
		end = ceilToSpan(*cursor, span)
		gtBase = stepBack(truncateToSpan(*cursor, span), -1, span) // floor(cursor) + 1span
	} else {
		now := truncateToSpan(c.clock.Now(), span)
		end = now
		gtBase = now
	}
	gt := stepBack(gtBase, amount-1, span)

	rows, err := c.repo.FindRange(ctx, span, group, gt.Unix(), end.Unix())
	if err != nil {
		return nil, err
	}

	// 範囲外しか持っていなかった場合の補間用 fallback 行
	if len(rows) == 0 {
		fallback, err := c.repo.FindLatest(ctx, span, group)
		if err == nil {
			rows = []*Row{fallback}
		} else if !errors.Is(err, ErrRowNotFound) {
			return nil, err
		}
	} else if rows[len(rows)-1].Date != gt.Unix() {
		// gt バケットにログが無い (range 最古が gt と一致しない) → gt より古い
		// 最新ログを outdated-log として末尾に追加し補間アンカーにする。
		// 逆に gt バケットにログが在るときは backfill を抑止し、最古表示バケット
		// (gt より 1span 古い場合) を 0/空のまま残す (upstream と同じ挙動)。
		anchor, err := c.repo.FindBefore(ctx, span, group, gt.Unix())
		if err == nil {
			rows = append(rows, anchor)
		} else if !errors.Is(err, ErrRowNotFound) {
			return nil, err
		}
	}

	// バケット時刻 (=希望する時刻列) を生成。upstream Misskey TS の
	// `getChartRaw` は `for i := amount-1; i >= 0; i--` + `chart.unshift`
	// で **newest-first** (index 0 = 最新, 末尾 = 最古) の配列を組み立てる。
	// frontend (`MkChart.vue` の `data.slice().reverse()`) はその newest-first
	// 配列を反転して oldest-first labels に揃えるため、API レスポンスが
	// oldest-first で返ると最新値が左端 (90 日前側) に流れて X 軸末端
	// (現在時刻) に届かない症状になる (#470 / #473)。
	out := make(Result, len(c.schema.Columns))
	for _, col := range c.schema.Columns {
		out[col.Name] = make([]int64, amount)
	}
	for i := 0; i < amount; i++ {
		// i=0 → end (最新), i=amount-1 → end-(amount-1)*span (最古)
		bucket := stepBack(end, i, span)
		row := pickRowAt(rows, bucket.Unix())
		if row == nil {
			row = pickFallback(rows, bucket.Unix())
		}
		for _, col := range c.schema.Columns {
			var v int64
			if row != nil {
				if !col.Accumulate {
					// 補間中の bucket は accumulate 以外 0 にする (本家と同じ)
					if row.Date == bucket.Unix() {
						v = toInt64(row.Cols[col.Name])
					}
				} else {
					v = toInt64(row.Cols[col.Name])
				}
			}
			out[col.Name][i] = v
		}
	}
	return out, nil
}

// pickRowAt returns the row whose timestamp matches ts exactly, or nil.
func pickRowAt(rows []*Row, ts int64) *Row {
	for _, r := range rows {
		if r.Date == ts {
			return r
		}
	}
	return nil
}

// pickFallback returns the most recent row whose timestamp is strictly
// before ts. The slice is assumed to be ordered date DESC.
func pickFallback(rows []*Row, ts int64) *Row {
	for _, r := range rows {
		if r.Date < ts {
			return r
		}
	}
	return nil
}
