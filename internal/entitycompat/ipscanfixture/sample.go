// Package ipscanfixture holds artificial shapes used to pin the JSON key
// collector in internal/entitycompat.
//
// **実データには現れない形をここに置く。** `internal/entity` /
// `internal/activitypub` には「タグの無い exported フィールド」も
// 「タグ付きの匿名埋め込み」も無いので、そこを取りこぼす実装は実データ相手には
// 何も起こさない (実測で素通りした)。
//
// **`testdata/` に置かない。** あちらは Go ツールチェーンが無視するのでコンパイル
// されず、`encoding/json` が実際に何を出すかと突き合わせられない。期待値を手で
// 書くことになり、収集側と `encoding/json` がずれても気付けない (実測: ずれる形を
// 足しても緑のままだった)。
package ipscanfixture

// Outer exercises every shape the collector has to get right.
type Outer struct {
	Tagged   string `json:"tagged"`
	Untagged string // タグが無ければ `encoding/json` はフィールド名で出す
	Skipped  string `json:"-"`
	hidden   string //nolint:unused // 非公開は出ない

	Inner                    // タグ無しの匿名埋め込み: 中身が昇格する
	Labeled `json:"labeled"` // **タグ付きの匿名埋め込みはタグ名がキーになる** (昇格しない)
	IPList                   // struct でない名前付き型の埋め込み: **型名がキーになる**

	// 非公開型の匿名埋め込み。`encoding/json` は中の exported フィールドを昇格させる。
	// **昇格だけでキーが出る形をここに置くのが要点** — 埋め込まれる型が exported だと、
	// 収集側はその型自身の宣言からも同じキーを出せるので、埋め込みを消しても
	// 突き合わせが食い違わない (実測で素通りした)。
	promoted

	Nested Inner `json:"nested"`
}

// Inner is embedded into Outer without a tag, so its fields are promoted.
type Inner struct {
	Deep string `json:"deep"`
}

// Labeled is embedded into Outer with a tag, so it becomes one nested object.
type Labeled struct {
	LabeledInner string `json:"labeledInner"`
}

// IPList is a named non-struct type. 埋め込むと型名がそのままキーになる。
type IPList []string

// promoted is embedded into Outer as an unexported type. 非公開型なので
// `exportedStructNames` には出ないが、中の exported フィールドは昇格する。
type promoted struct {
	Deep2 string `json:"deep2"`
}
