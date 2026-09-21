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
