package chart

import (
	"context"
	"fmt"
	"maps"
	"sort"
	"sync"
	"time"
)

// fakeRepo is an in-memory Repository used for engine unit tests. It
// preserves insert order via a monotonic id and groups rows under
// (span, group, ts) keys. The struct is safe for concurrent use because
// the engine tests run Save() and Commit() from the same goroutine.
type fakeRepo struct {
	mu     sync.Mutex
	nextID int64
	hour   map[string][]*Row // key: group
	day    map[string][]*Row // key: group
	// errOn queues errors per method name. Each Acquire/FindXxx etc.
	// call pops one error from the front of its list. Tests use
	// armError() to push an error and skipFirstError() to skip a call
	// before failing the next one.
	errOn map[string][]error
	// calls records the arguments of every ApplyDeltas invocation in order.
	//
	// deltas と setInts はどちらも toColumnName() の同じ列に展開されるので、
	// キーが重なると PostgreSQL が 42601 (multiple assignments to same column)
	// で落ちる。fakeRepo はマップを順に適用するだけでその衝突を再現しないため、
	// 不変条件はテスト側で見る。
	//
	// appends は span ごとに行の既存要素で filter した結果 (#2652) を確かめる
	// ために記録する。適用後の行を見るだけでは「何を送ったか」が分からない。
	calls []applyDeltasCall
	// groupOrder records the group of every SpanHour FindCurrent, before the
	// armed-error check, so tests can assert the processing order. Save
	// reaches FindCurrent only through claimCurrentLog, which looks the row
	// up twice when it has to insert one, so consecutive duplicates are
	// expected.
	groupOrder []string
}

// applyDeltasCall captures one ApplyDeltas invocation for assertions.
type applyDeltasCall struct {
	span    Span
	deltas  map[string]int64
	appends map[string][]string
	setInts map[string]int64
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{
		hour:  make(map[string][]*Row),
		day:   make(map[string][]*Row),
		errOn: make(map[string][]error),
	}
}

func (r *fakeRepo) tableFor(span Span) map[string][]*Row {
	if span == SpanDay {
		return r.day
	}
	return r.hour
}

// errIfArmed pops the front error for the given method name, if any.
// nil entries in the queue let the test skip a call without failing it
// (used to fail the *Nth* invocation rather than the first).
func (r *fakeRepo) errIfArmed(name string) error {
	q := r.errOn[name]
	if len(q) == 0 {
		return nil
	}
	err := q[0]
	r.errOn[name] = q[1:]
	return err
}

// armError queues an error to be returned by the next call to method.
func (r *fakeRepo) armError(method string, err error) {
	r.errOn[method] = append(r.errOn[method], err)
}

// armSkipThenError queues n successful no-op skips then an error.
func (r *fakeRepo) armSkipThenError(method string, skip int, err error) {
	for range skip {
		r.errOn[method] = append(r.errOn[method], nil)
	}
	r.errOn[method] = append(r.errOn[method], err)
}

func (r *fakeRepo) FindCurrent(_ context.Context, span Span, group string, ts int64) (*Row, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if span == SpanHour {
		r.groupOrder = append(r.groupOrder, group)
	}
	if err := r.errIfArmed("FindCurrent"); err != nil {
		return nil, err
	}
	for _, row := range r.tableFor(span)[group] {
		if row.Date == ts {
			return row, nil
		}
	}
	return nil, ErrRowNotFound
}

func (r *fakeRepo) FindLatest(_ context.Context, span Span, group string) (*Row, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.errIfArmed("FindLatest"); err != nil {
		return nil, err
	}
	rows := r.tableFor(span)[group]
	if len(rows) == 0 {
		return nil, ErrRowNotFound
	}
	// Return the row with the largest Date.
	latest := rows[0]
	for _, row := range rows[1:] {
		if row.Date > latest.Date {
			latest = row
		}
	}
	return latest, nil
}

func (r *fakeRepo) FindBefore(_ context.Context, span Span, group string, ts int64) (*Row, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.errIfArmed("FindBefore"); err != nil {
		return nil, err
	}
	var best *Row
	for _, row := range r.tableFor(span)[group] {
		if row.Date < ts {
			if best == nil || row.Date > best.Date {
				best = row
			}
		}
	}
	if best == nil {
		return nil, ErrRowNotFound
	}
	return best, nil
}

func (r *fakeRepo) FindRange(_ context.Context, span Span, group string, gt, lt int64) ([]*Row, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.errIfArmed("FindRange"); err != nil {
		return nil, err
	}
	var out []*Row
	for _, row := range r.tableFor(span)[group] {
		if row.Date >= gt && row.Date <= lt {
			out = append(out, row)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Date > out[j].Date })
	return out, nil
}

func (r *fakeRepo) Insert(_ context.Context, span Span, group string, ts int64, cols map[string]any) (*Row, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.errIfArmed("Insert"); err != nil {
		return nil, err
	}
	r.nextID++
	row := &Row{
		ID:   r.nextID,
		Date: ts,
		Cols: make(map[string]any, len(cols)),
	}
	if group != "" {
		row.Group.Valid = true
		row.Group.String = group
	}
	maps.Copy(row.Cols, cols)
	r.tableFor(span)[group] = append(r.tableFor(span)[group], row)
	return row, nil
}

func (r *fakeRepo) ApplyDeltas(_ context.Context, span Span, id int64, deltas map[string]int64, uniqueAppends map[string][]string, setInts map[string]int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, applyDeltasCall{
		span:    span,
		deltas:  maps.Clone(deltas),
		appends: maps.Clone(uniqueAppends),
		setInts: maps.Clone(setInts),
	})
	if err := r.errIfArmed("ApplyDeltas"); err != nil {
		return err
	}
	row := r.findByID(span, id)
	if row == nil {
		return fmt.Errorf("fakeRepo: row %d not found", id)
	}
	for k, v := range deltas {
		row.Cols[k] = toInt64(row.Cols[k]) + v
	}
	for k, items := range uniqueAppends {
		key := k + ":unique"
		cur, _ := row.Cols[key].([]string)
		row.Cols[key] = append(cur, items...)
	}
	for k, v := range setInts {
		row.Cols[k] = v
	}
	return nil
}

func (r *fakeRepo) SetColumns(_ context.Context, span Span, id int64, cols map[string]int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.errIfArmed("SetColumns"); err != nil {
		return err
	}
	row := r.findByID(span, id)
	if row == nil {
		return fmt.Errorf("fakeRepo: row %d not found", id)
	}
	for k, v := range cols {
		row.Cols[k] = v
	}
	return nil
}

func (r *fakeRepo) ResetUniqueTempColumns(_ context.Context, span Span, gt, lt int64, columns []string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.errIfArmed("ResetUniqueTempColumns"); err != nil {
		return err
	}
	for _, rows := range r.tableFor(span) {
		for _, row := range rows {
			if row.Date > gt && row.Date < lt {
				for _, c := range columns {
					row.Cols[c+":unique"] = []string{}
				}
			}
		}
	}
	return nil
}

func (r *fakeRepo) findByID(span Span, id int64) *Row {
	for _, rows := range r.tableFor(span) {
		for _, row := range rows {
			if row.ID == id {
				return row
			}
		}
	}
	return nil
}

// fakeClock returns a fixed UTC instant and exposes setters for tests.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock(t time.Time) *fakeClock {
	return &fakeClock{now: t.UTC()}
}

func (c *fakeClock) set(t time.Time) {
	c.mu.Lock()
	c.now = t.UTC()
	c.mu.Unlock()
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}
