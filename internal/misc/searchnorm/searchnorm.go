// Package searchnorm normalizes strings for case- and width-insensitive
// search/index keys, mirroring Misskey's misc/normalize-for-search.ts
// (`tag.normalize('NFKC').toLowerCase()`).
package searchnorm

import (
	"strings"

	"golang.org/x/text/unicode/norm"
)

// Normalize returns the NFKC-normalized, lower-cased form of s. Used to make
// hashtag containment queries (user.tags `@>`) match regardless of case or
// full-/half-width variants, consistently between store and query sides.
func Normalize(s string) string {
	return strings.ToLower(norm.NFKC.String(s))
}
