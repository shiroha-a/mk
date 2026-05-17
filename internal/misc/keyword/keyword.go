// Package keyword provides upstream Misskey TS compatible keyword matching
// used by meta.sensitiveWords / meta.prohibitedWords / role wordmute /
// featured hashtag filters etc.
//
// Mirrors UtilityService.isKeyWordIncluded in misskey-project/misskey:
//
//	keywords.some(filter => {
//	    if (filter matches /^\/(.+)\/(.*)$/) {
//	        return RegExp(pattern, flags).test(text)
//	    } else {
//	        const words = filter.split(' ')
//	        return words.every(word => text.includes(word))
//	    }
//	})
//
// = いずれかの filter にマッチで true。space 区切り filter は AND、`/regex/flags`
// 形式は regexp.MatchString。
package keyword

import (
	"regexp"
	"strings"
)

// regexpFilterRe matches "/<pattern>/<flags>" — the marker for a regex-style
// filter. We capture pattern and flags separately so we can build a Go
// regexp with the appropriate (?i) etc. prefix.
var regexpFilterRe = regexp.MustCompile(`^/(.+)/(.*)$`)

// IsKeyWordIncluded reports whether `text` matches any filter in `keywords`.
//
// Filter syntax mirrors upstream Misskey TS (UtilityService.isKeyWordIncluded):
//
//   - `/pattern/flags` → regex match (Go regexp; flags `i` / `m` / `s` are
//     converted to `(?i)` / `(?m)` / `(?s)` inline flags. JS-only flags like
//     `g` / `y` are silently dropped since they don't change match semantics.)
//   - otherwise → space-separated words, all of which must appear as
//     substrings in `text` (AND semantics within one filter).
//
// Empty `keywords` or empty `text` returns false (matches upstream early
// return). Malformed regex filters are skipped (= treated as "not matching")
// so admin-side typos don't crash the request — same as upstream's try/catch.
func IsKeyWordIncluded(text string, keywords []string) bool {
	if len(keywords) == 0 || text == "" {
		return false
	}
	for _, filter := range keywords {
		if filter == "" {
			// 空 filter は skip (= 「全部マッチ」の vacuous true を防ぐ)。
			// upstream は split(' ').every で空配列なら true を返すが、空
			// filter は admin UI で意味なしなので mk-go では skip 扱いに
			// 揃える (defensive、test 経路で意図確認)。
			continue
		}
		if m := regexpFilterRe.FindStringSubmatch(filter); m != nil {
			pattern, flags := m[1], m[2]
			if matchRegex(text, pattern, flags) {
				return true
			}
			continue
		}
		// space-separated AND match (= filter "hello world" requires
		// both "hello" AND "world" in text)
		words := strings.Fields(filter)
		if len(words) == 0 {
			continue
		}
		allMatch := true
		for _, w := range words {
			if !strings.Contains(text, w) {
				allMatch = false
				break
			}
		}
		if allMatch {
			return true
		}
	}
	return false
}

// matchRegex compiles the given pattern with JS-compatible flag translation
// and returns whether `text` matches. Compile errors result in `false`
// (matches upstream's catch-and-return-false defensive behavior).
//
// Supported flags: i (case-insensitive) / m (multi-line) / s (dot matches
// newline). JS-only flags (g / y / u / d) are ignored because they don't
// affect whether a match exists.
func matchRegex(text, pattern, flags string) bool {
	var inline strings.Builder
	for _, f := range flags {
		switch f {
		case 'i', 'm', 's':
			inline.WriteRune(f)
		}
	}
	if inline.Len() > 0 {
		pattern = "(?" + inline.String() + ")" + pattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		// 不正な regex は upstream の try/catch と同じく "match しない" 扱い
		return false
	}
	return re.MatchString(text)
}
