// Package selfcheck verifies that this instance looks correct **from the
// outside**, and that its local dependencies are healthy.
//
// 連合が動かないときの切り分けは難しい。「アプリは起動していて内部からは正常に
// 見えるのに、外から見ると WebFinger が 404」「actor に署名鍵が載っていない」と
// いった構成ミスは、**外から自分を見に行かないと分からない** (#2463)。
//
// そのため公開 URL 経由の検査は必ず `config.url` を起点にする。内部 handler を
// 直接呼ぶと、リバースプロキシの設定漏れを検出できない。
package selfcheck

// Status is the outcome of a single check.
type Status string

const (
	// StatusOK means the check passed.
	StatusOK Status = "ok"
	// StatusWarn means it works but should be looked at (証明書の期限が近い等)。
	StatusWarn Status = "warn"
	// StatusFail means federation or operation is broken.
	StatusFail Status = "fail"
	// StatusSkip means the check could not run (依存が未配線)。
	StatusSkip Status = "skip"
)

// Result is one check's outcome.
type Result struct {
	Name   string `json:"name"`
	Status Status `json:"status"`
	Detail string `json:"detail"`
	// Hint は失敗したときの直し方。**これが本機能の価値の中心**なので、
	// fail / warn では必ず埋める。「壊れている」だけ言われても運用者は動けない。
	Hint string `json:"hint,omitempty"`
}

// Report is the full run.
type Report struct {
	Results []Result `json:"results"`
	// OK is false when any check failed. warn は false にしない
	// (「見ておくべき」であって「壊れている」ではない)。
	OK bool `json:"ok"`
}

// newReport computes the aggregate flag.
func newReport(results []Result) Report {
	ok := true
	for _, r := range results {
		if r.Status == StatusFail {
			ok = false
			break
		}
	}
	return Report{Results: results, OK: ok}
}

// okResult / failResult / warnResult / skipResult are small helpers so check
// implementations stay readable.
func okResult(name, detail string) Result {
	return Result{Name: name, Status: StatusOK, Detail: detail}
}

func failResult(name, detail, hint string) Result {
	return Result{Name: name, Status: StatusFail, Detail: detail, Hint: hint}
}

func warnResult(name, detail, hint string) Result {
	return Result{Name: name, Status: StatusWarn, Detail: detail, Hint: hint}
}

func skipResult(name, detail string) Result {
	return Result{Name: name, Status: StatusSkip, Detail: detail}
}
