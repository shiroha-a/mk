// Package deliveryhealth records per-host ActivityPub delivery outcomes so
// operators can see *why* federation to a host is degraded, not just whether
// it is up.
//
// upstream Misskey は配送結果を `instance.isNotResponding` (真偽値) にしか
// 残さない。mk-go の deliver processor は 2xx / 410 / 404 / 429 / その他 4xx /
// 5xx / transport error を撃ち分けているので、その情報を捨てずに集計する
// (#2461)。
//
// **本パッケージは観測だけを行う。** 配送を止める判断 (circuit breaker) は
// 含まない。配送停止は連合の一貫性に直接効くので、閾値は実データを見てから
// 決める。
package deliveryhealth

import "time"

// OutcomeClass mirrors the branches of the deliver processor's response
// switch. **新しい判断をここで足さないこと。** 分類を二重管理すると
// 「成功とみなす範囲」が processor 側とずれる。
type OutcomeClass string

const (
	// ClassSuccess is a 2xx response.
	ClassSuccess OutcomeClass = "success"
	// ClassGone is 410 / 404 — 相手がもう存在しない。retry しない。
	ClassGone OutcomeClass = "gone"
	// ClassRateLimited is 429。相手は健在で絞っているだけなので retry する。
	ClassRateLimited OutcomeClass = "rateLimited"
	// ClassClientError is any other 4xx — こちらの投函が受理されていない
	// (署名不備など)。retry しない。
	ClassClientError OutcomeClass = "clientError"
	// ClassServerError is 5xx — 相手側の一時障害。retry する。
	ClassServerError OutcomeClass = "serverError"
	// ClassTransport は HTTP 応答に至らなかったもの (DNS / TCP / TLS /
	// timeout)。status は 0 になる。
	ClassTransport OutcomeClass = "transport"
)

// AllClasses lists every class in a stable order for rendering and tests.
var AllClasses = []OutcomeClass{
	ClassSuccess, ClassGone, ClassRateLimited,
	ClassClientError, ClassServerError, ClassTransport,
}

// Succeeded reports whether the class counts as a delivered attempt.
//
// gone / clientError は「相手が応答したが受理しなかった」もので、配送としては
// 失敗。retry しない点は同じでも、成功率に混ぜると死んでいるホストが健全に
// 見える。
func (c OutcomeClass) Succeeded() bool { return c == ClassSuccess }

// Outcome is one delivery attempt's result.
type Outcome struct {
	Class OutcomeClass
	// Status is the HTTP status code, or 0 when no response was received.
	Status int
	// Latency はリクエスト送出から応答受領 (または失敗確定) まで。
	Latency time.Duration
	// Err は transport 失敗時の要約。任意長の文字列がそのまま Redis と
	// admin API へ流れるので、記録側で切り詰める。
	Err string
}

// maxErrMessageLen bounds the stored error string. エラー文字列には相手の
// 応答由来の内容が混ざりうるので、保存前に必ず切る。
const maxErrMessageLen = 200

// truncateErr shortens an error message for storage.
func truncateErr(s string) string {
	if len(s) <= maxErrMessageLen {
		return s
	}
	// rune 境界で切る (multi-byte を割ると JSON に不正な文字が乗る)。
	r := []rune(s)
	if len(r) <= maxErrMessageLen {
		return s
	}
	return string(r[:maxErrMessageLen])
}
