// このファイルはビルドされない (`testdata` 配下)。`scanJSONTags` が
// `encoding/json` と同じものを拾うかを固定するための人工ソース。
package ipscan

type Outer struct {
	Tagged   string `json:"tagged"`
	Untagged string
	Skipped  string `json:"-"`
	hidden   string
	Inner
}

type Inner struct {
	Deep string `json:"deep"`
}
