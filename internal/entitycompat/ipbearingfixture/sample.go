// Package ipbearingfixture holds artificial types used to pin the "which types
// carry an IP" scanner in internal/entitycompat.
//
// **実データでは枝が実行されない。** `internal/` に IP を持つ型は 4 つあるが、
// どれもタグ付きなので「タグ無しの exported フィールド」の枝は一度も通らず、
// そこを潰しても実データからは何も起こらない — その状態で**タグを書き忘れた
// フィールドを持つ型を参照する漏れが素通りする** (実測)。`encoding/json` は
// タグの無い exported フィールドを Go の名前でそのまま出すので、この枝は要る。
package ipbearingfixture

// TaggedIP carries an IP through a json tag.
type TaggedIP struct {
	IP string `json:"ip"`
}

// UntaggedIP carries an IP through the Go field name (タグ無し)。
type UntaggedIP struct {
	IP string
}

// SkippedIP never reaches the wire.
type SkippedIP struct {
	IP string `json:"-"`
}

// HiddenIP keeps the address unexported, so `encoding/json` drops it.
type HiddenIP struct {
	ip string //nolint:unused // 非公開は出ない
}

// NoIP carries nothing that looks like an address.
type NoIP struct {
	Name string `json:"name"`
}

// AliasedIP is a type alias to a carrier. **別名でも同じものを指す**ので、
// 参照側の検査はこちらも IP を持つ型として扱わなければならない。
type AliasedIP = TaggedIP

// DefinedIP is a defined type whose underlying type is a carrier.
type DefinedIP TaggedIP

// TaggedEmbedIP embeds a harmless type under an IP-looking tag name.
// タグ付きの匿名埋め込みは昇格せず、**タグ名がそのままキー**になる。
type TaggedEmbedIP struct {
	NoIP `json:"ip"`
}

// UntaggedEmbedIP embeds a carrier without a tag, so its `ip` is promoted.
type UntaggedEmbedIP struct {
	TaggedIP
}

// NestedIP carries an address only through a field type (2 ホップ)。
type NestedIP struct {
	Inner *TaggedIP `json:"inner"`
}
