// Package marshalerfixture holds artificial types that implement their own JSON or
// text marshaler.
//
// **自前 marshaler を持つ型は JSON タグの走査から外れる**ので、一覧を固定して増減に
// 気付く形にしてある。実データ (`internal/entity` / `internal/activitypub`) には
// `MarshalJSON` が 1 件あるだけで `MarshalText` も generic なレシーバも無く、その枝は
// 一度も実行されない。**実データだけを見ていると、走査を狭める変異が素通りする** (実測)。
package marshalerfixture

// Plain implements json.Marshaler with a value receiver.
type Plain struct{}

// MarshalJSON implements json.Marshaler.
func (Plain) MarshalJSON() ([]byte, error) { return []byte("{}"), nil }

// Pointer implements json.Marshaler with a pointer receiver.
type Pointer struct{}

// MarshalJSON implements json.Marshaler.
func (*Pointer) MarshalJSON() ([]byte, error) { return []byte("{}"), nil }

// Text implements encoding.TextMarshaler only. `encoding/json` はこちらも優先する
// ので、中身はやはり見えなくなる。
type Text string

// MarshalText implements encoding.TextMarshaler.
func (Text) MarshalText() ([]byte, error) { return []byte("x"), nil }

// Generic has a type parameter, so its receiver is an index expression.
type Generic[T any] struct{ V T }

// MarshalJSON implements json.Marshaler.
func (Generic[T]) MarshalJSON() ([]byte, error) { return []byte("{}"), nil }
