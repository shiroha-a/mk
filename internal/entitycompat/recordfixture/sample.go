// Package recordfixture holds artificial call sites used to pin the IP-record
// scanner in internal/entitycompat.
//
// **実データでは枝が実行されない。** 本番コードに「メソッド値として持ち出す」形も
// 「package 変数のクロージャの中で呼ぶ」形も無いので、そこを拾わない実装にしても
// 実データからは何も起こらない — その状態で**署名検証の前に記録を仕込む変異が
// 素通りする** (実測)。人工のソースで枝そのものを固定する。
package recordfixture

// Recorder is the shape the scanner looks for.
type Recorder interface {
	Record(userID, ip string)
}

// Held has a field named Record. **呼び出しではないので call site ではない。**
type Held struct {
	Record struct{ Val int }
}

// CallTwoArgs is the plain form: `r.Record(a, b)`.
func CallTwoArgs(r Recorder) { r.Record("u", "203.0.113.1") }

// CallOneArg must not be counted (引数の数が違う)。
func CallOneArg(r interface{ Record(string) }) { r.Record("u") }

// MethodValue hands the method out without calling it. **これを拾わないと、
// 名前で見る走査は名前で避けられる。**
func MethodValue(r Recorder) {
	f := r.Record
	f("u", "203.0.113.2")
}

// FieldAccess touches a field named Record. **call site ではない。**
func FieldAccess(h Held) int { return h.Record.Val }

// Closure keeps the call inside a package-level variable. `FuncDecl` だけを
// 走査する実装からは見えない。
var Closure = func(r Recorder) { r.Record("u", "203.0.113.3") }
