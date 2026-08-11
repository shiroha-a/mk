package deliveryhealth

// Inbound outcome classes mirror the branches of the inbox processor
// (#2471). **新しい判断をここで足さないこと。** 分類を二重管理すると
// 「受理とみなす範囲」が processor 側とずれる。
//
// 送信側 (#2461) と同じ集約基盤に載せるが、Redis のキー空間は
// DirectionInbound で分ける。混ざると「配送できない host」と「受信を拒否した
// host」が同じ行に足し込まれ、どちらの話か分からなくなる。
const (
	// ClassAccepted は処理まで到達したもの。
	ClassAccepted OutcomeClass = "accepted"
	// ClassUnsupported は未対応の activity 種別。**異常ではない**
	// (相手が mk-go の実装していない活動を送っただけ)。
	ClassUnsupported OutcomeClass = "unsupported"
	// ClassSignatureFailed は HTTP 署名の検証失敗。相手の鍵が変わった /
	// 署名実装が壊れている等。
	ClassSignatureFailed OutcomeClass = "signatureFailed"
	// ClassBlocked は管理者がブロックしたホスト。**意図した拒否**。
	// 元の実装はログすら出さないので、ここが唯一の観測点になる。
	ClassBlocked OutcomeClass = "blocked"
	// ClassActorUnauthorized は署名者と body の actor が食い違うもの
	// (actor spoofing 対策で落とす)。
	ClassActorUnauthorized OutcomeClass = "actorUnauthorized"
	// ClassLDSignatureFailed は LD-Signature の検証失敗。
	ClassLDSignatureFailed OutcomeClass = "ldSignatureFailed"
	// ClassProcessingError は処理中の失敗。retry される。
	ClassProcessingError OutcomeClass = "processingError"
)

// AllInboundClasses lists every inbound class in a stable order.
var AllInboundClasses = []OutcomeClass{
	ClassAccepted, ClassUnsupported, ClassSignatureFailed, ClassBlocked,
	ClassActorUnauthorized, ClassLDSignatureFailed, ClassProcessingError,
}

// inboundSuccessClasses are the classes that count as "we took it".
//
// unsupported を成功に数えるかは判断が要る。**数える。** 相手は正しく送って
// おり、こちらが対応していないだけなので、「その host との連合が壊れている」
// 側に積むと誤読を招く。blocked は意図した拒否だが、成功率に混ぜると
// 「ブロックしたのに健全」に見えるので失敗側に置く。
var inboundSuccessClasses = map[OutcomeClass]struct{}{
	ClassAccepted:    {},
	ClassUnsupported: {},
}

// SucceededInbound reports whether the class counts as accepted traffic.
//
// Succeeded() (送信側) とは判定が違うので別メソッドにする。1 つに混ぜると
// 方向によって意味の変わる関数になり、呼び出し側が取り違える。
func (c OutcomeClass) SucceededInbound() bool {
	_, ok := inboundSuccessClasses[c]
	return ok
}
