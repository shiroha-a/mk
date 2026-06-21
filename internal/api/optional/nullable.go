// Package optional provides a JSON wrapper that distinguishes an absent field
// from an explicit null, which Go's encoding/json cannot express with a plain
// pointer (both decode to nil).
package optional

import "encoding/json"

// Nullable distinguishes three states of a JSON field: absent (Present=false),
// explicit null (Present=true, Value=nil), and a concrete value (Present=true,
// Value=&v). Upstream Misskey endpoints frequently branch on `ps.x !== undefined`
// (present) vs `ps.x === null` (explicit null) — e.g. channels/update clears
// description on null and i/webhooks/update resets secret to "" on null — which a
// plain *T cannot represent. encoding/json only calls UnmarshalJSON when the key
// is present, so an omitted key leaves the zero value (Present=false).
type Nullable[T any] struct {
	Present bool
	Value   *T
}

// UnmarshalJSON records that the key was present and decodes its value (nil for
// an explicit JSON null).
func (n *Nullable[T]) UnmarshalJSON(data []byte) error {
	n.Present = true
	if string(data) == "null" {
		n.Value = nil
		return nil
	}
	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	n.Value = &v
	return nil
}

// IsNull reports whether the field was present and explicitly null.
func (n Nullable[T]) IsNull() bool { return n.Present && n.Value == nil }
