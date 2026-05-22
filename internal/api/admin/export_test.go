package admin

// Test seams exposed to admin_test (external) package. file 名が `_test.go`
// で終わるので production build には含まれない。

// DelayedTasksFetchPageSize は fetchAllDelayedTasks の page size を test 側
// から参照するための seam。production code から書き換えてはならない。
const DelayedTasksFetchPageSize = delayedTasksFetchPageSize

// SetDelayedTasksMaxPages は test 中だけ page 走査の上限を差し替えるための
// seam。返り値は前の値で、t.Cleanup から呼び戻して元に戻すこと。
//
// 注意: グローバル変数を書き換えるので `t.Parallel()` を使う test 内では
// 呼ばないこと。並列 test が同じ var を書き換えると race し、cap 値が
// 不定になる。
func SetDelayedTasksMaxPages(n int) (prev int) {
	prev = delayedTasksMaxPages
	delayedTasksMaxPages = n
	return prev
}
