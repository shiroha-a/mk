package entity

import "time"

// isoMillisLayout is the fixed millisecond ISO8601 layout produced by
// JavaScript's Date.toISOString() (always 3 fractional digits, trailing Z, UTC).
// Upstream Misskey serializes every timestamp through toISOString(), so mk-go
// must match this exact layout rather than stdlib RFC3339Nano (variable
// fractional digits, numeric offset for non-UTC locations).
const isoMillisLayout = "2006-01-02T15:04:05.000Z"

// ISOMillis formats t as a Misskey-compatible ISO8601 millisecond timestamp,
// mirroring upstream's Date.toISOString(). The value is normalized to UTC first.
func ISOMillis(t time.Time) string {
	return t.UTC().Format(isoMillisLayout)
}

// ISOMillisPtr returns nil for a nil pointer, otherwise the ISOMillis string,
// mirroring upstream's `x ? x.toISOString() : null`. The return type is `any`
// so callers can drop it straight into a map[string]any response.
func ISOMillisPtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return ISOMillis(*t)
}
