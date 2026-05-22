package federation

import (
	"bytes"
	"encoding/json"
)

// tryUnwrapSingletonArray inspects body and, if it is a JSON array with
// exactly one element, returns that element's raw bytes and true.
// All other shapes (= object, empty array, 2+ element array, null,
// malformed JSON, empty bytes) return nil and false so the caller can
// proceed with body unchanged.
//
// 背景: Foundkey 系 fork instance は valid な AS Activity を 1 要素
// JSON array で wrap して送信してくるケースがある:
//
//	[{"@context":[...],"type":"Delete","actor":"...","object":{...}}]
//
// AS 2.0 仕様では inbox direct POST に array を送る規定は無く、上流
// Misskey TS / mk-go ともに object 前提で decode するので、wrapper の
// せいで「activity missing actor」相当の silent failure が起きる。
// 本 helper で wrap を剥がして downstream に object を渡す (#1185)。
//
// 2+ 要素 array (= batch activity) は AS 2.0 で OrderedCollection 等
// 別 endpoint 経由が想定されており、inbox direct POST には来ない前提。
// 来たら conservative に no-op (= false) で素通しして既存挙動を保つ
// (= 将来 batch 対応する余地を残す)。
func tryUnwrapSingletonArray(body []byte) ([]byte, bool) {
	trimmed := bytes.TrimLeft(body, " \t\n\r")
	if len(trimmed) == 0 || trimmed[0] != '[' {
		return nil, false
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(body, &arr); err != nil {
		return nil, false
	}
	if len(arr) != 1 {
		return nil, false
	}
	return []byte(arr[0]), true
}
