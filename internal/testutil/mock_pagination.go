package testutil

import "sort"

// SortMockPage orders rows the same way the repository package's
// paginationOrder helper does: ascending when only sinceID is supplied,
// descending otherwise. Mocks that page by id cursor must call this before
// truncating to limit, because the direction decides which rows survive the
// cut, not just how they are arranged.
//
// The id argument extracts the cursor column from a row.
func SortMockPage[T any](rows []T, sinceID, untilID string, id func(T) string) {
	// production の paginationOrder と同じ規則。mock だけ DESC 固定にすると
	// 「テストは通るが production は別の行を返す」型の空振りになる (#2713)。
	asc := sinceID != "" && untilID == ""
	sort.SliceStable(rows, func(i, j int) bool {
		if asc {
			return id(rows[i]) < id(rows[j])
		}
		return id(rows[i]) > id(rows[j])
	})
}
