// Package wordmute implements Misskey TS upstream `check-word-mute.ts` for
// hard mute filtering on the server side (#787). The wire format is the
// jsonb array stored in user_profile.mutedWords / hardMutedWords:
//
//	[
//	  "single keyword",     // case-insensitive substring match
//	  ["a", "b"],           // AND group: text must contain every element
//	  "/regex/i"            // RegExp literal: parsed as Go regexp
//	]
//
// Frontend sends and renders the same shape, so persistence layer never
// transforms the structure.
package wordmute

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"

	lru "github.com/hashicorp/golang-lru/v2"
)

// Match returns true if the given combined text should be muted by any of
// the rules encoded in `rulesJSON` (the raw jsonb bytes from user_profile).
// `rulesJSON` may be nil / empty / `null` / `[]` — all of those mean no rule.
//
// `text` is the already-normalized search target. Callers that want to mute
// based on note content should pass `cw + "\n" + text` (matching upstream's
// concatenation). When the text is empty after trim, no rule can hit so the
// function returns false fast.
func Match(rulesJSON []byte, text string) bool {
	text = strings.TrimSpace(text)
	if text == "" {
		return false
	}
	rules := parse(rulesJSON)
	if len(rules) == 0 {
		return false
	}
	lowered := strings.ToLower(text)
	for _, r := range rules {
		switch v := r.(type) {
		case stringRule:
			if strings.Contains(lowered, string(v)) {
				return true
			}
		case andRule:
			hit := true
			for _, kw := range v {
				if !strings.Contains(lowered, kw) {
					hit = false
					break
				}
			}
			if hit {
				return true
			}
		case regexRule:
			if v.re != nil && v.re.MatchString(text) {
				return true
			}
		}
	}
	return false
}

// rule is one parsed entry of the muted-words array.
type rule any

// stringRule represents a single keyword. Stored already-lowercased so the
// hot path can compare against `strings.ToLower(text)` without re-allocating.
type stringRule string

// andRule represents `["a", "b"]` — every element must appear in the text
// (case-insensitive). Stored lowercased.
type andRule []string

// regexRule wraps a compiled `/foo/flag` literal. Compilation failure
// silently downgrades to "never matches" to mirror upstream's try/catch
// (caller should not fail an entire timeline because one entry is malformed).
type regexRule struct {
	re *regexp.Regexp
}

// regexLiteral matches Misskey's `/pattern/flags` wire syntax. Anchors are
// required (no leading whitespace allowed) since the frontend always emits
// the canonical form.
var regexLiteral = regexp.MustCompile(`^/(.+)/([gimsuy]*)$`)

// inlineFlagPrefix detects an existing `(?flags)` or `(?flags:...)` inline
// flag group at the start of a pattern. 既に inline flag を含んでいるなら
// 二重指定を避けるため flag 注入を skip する。`(?:...)` (non-capture) や
// `(?P<name>...)` (named group) は flag 設定ではないので一致させない。
var inlineFlagPrefix = regexp.MustCompile(`^\(\?[imsU]+[):]`)

// parsedCacheSize は parsedCache の size cap。TS upstream の AhoCorasick
// cache (`acCache.size > 1000`) と同じ閾値で、active user × rule revision
// が多くなっても memory が monotonic に grow しないようにする (#790)。
const parsedCacheSize = 1000

// parsedCache stores the parsed rule slice keyed by the *raw* JSON bytes.
// Hot TL paths call Match many times with the same per-user rule set; parsing
// once per request is cheap but parsing per-note would be wasteful。LRU で
// size cap しているため、user が rule を更新して古い JSON byte 列の entry が
// dangling しても oldest から evict されて bound が保たれる。
//
// hashicorp/golang-lru/v2 は内部で mutex を持ち、Add/Get は thread-safe。
// 並行 hot path で何度呼ばれても race にならない。
var parsedCache *lru.Cache[string, []rule]

func init() {
	c, err := lru.New[string, []rule](parsedCacheSize)
	if err != nil {
		// lru.New は size > 0 で panic しない実装なので実質到達しないが、
		// 万一に備えて compile-time に近い fail-fast にする。
		panic("wordmute: failed to init parsedCache: " + err.Error())
	}
	parsedCache = c
}

func parse(raw []byte) []rule {
	if len(raw) == 0 {
		return nil
	}
	key := string(raw)
	if v, ok := parsedCache.Get(key); ok {
		return v
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil
	}
	out := make([]rule, 0, len(entries))
	for _, e := range entries {
		r := parseEntry(e)
		if r != nil {
			out = append(out, r)
		}
	}
	parsedCache.Add(key, out)
	return out
}

func parseEntry(e json.RawMessage) rule {
	var s string
	if err := json.Unmarshal(e, &s); err == nil {
		// Misskey TS upstream allows /regex/flags literal as a string entry.
		if re, isLiteral, err := goRegexFromLiteral(s); isLiteral {
			if err != nil {
				return nil
			}
			return regexRule{re: re}
		}
		return stringRule(strings.ToLower(s))
	}
	var arr []string
	if err := json.Unmarshal(e, &arr); err == nil {
		out := make(andRule, 0, len(arr))
		for _, kw := range arr {
			kw = strings.ToLower(kw)
			if kw == "" {
				continue
			}
			out = append(out, kw)
		}
		if len(out) == 0 {
			return nil
		}
		return out
	}
	return nil
}

// goRegexFromLiteral compiles a /pattern/flags literal into a Go regexp, applying
// the i/m/s inline-flag conversion (g/u/y are no-ops, see parseEntry). Returns
// (nil, false, nil) when s is not a /.../ literal, and (nil, true, err) when it is
// a literal that fails to compile (#2106 L3 validation reuse).
func goRegexFromLiteral(s string) (*regexp.Regexp, bool, error) {
	m := regexLiteral.FindStringSubmatch(s)
	if m == nil {
		return nil, false, nil
	}
	pattern, flags := m[1], m[2]
	var inline string
	if strings.ContainsRune(flags, 'i') {
		inline += "i"
	}
	if strings.ContainsRune(flags, 'm') {
		inline += "m"
	}
	if strings.ContainsRune(flags, 's') {
		inline += "s"
	}
	// 二重指定回避: pattern が既に inline flag group `(?ims)` で始まる場合のみ skip。
	if inline != "" && !inlineFlagPrefix.MatchString(pattern) {
		pattern = "(?" + inline + ")" + pattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, true, err
	}
	return re, true, nil
}

// ErrInvalidRegexp is returned by ValidateMutedWords when a regex-literal entry
// fails to compile. Callers map it to the upstream INVALID_REGEXP API error.
var ErrInvalidRegexp = errors.New("invalid regexp literal in muted words")

// ValidateMutedWords reports whether every /pattern/flags regex-literal entry in
// the muted-words JSON compiles, returning ErrInvalidRegexp on the first failure
// (#2106 L3, upstream i/update.ts validateMuteWordRegex). Non-regex entries (plain
// keywords / AND-group arrays) and shape errors are ignored here (shape is handled
// by the caller's normalize step).
func ValidateMutedWords(raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil
	}
	for _, e := range entries {
		var s string
		if err := json.Unmarshal(e, &s); err != nil {
			continue // array (AND group), not a regex literal
		}
		if _, isLiteral, err := goRegexFromLiteral(s); isLiteral && err != nil {
			return ErrInvalidRegexp
		}
	}
	return nil
}

// MatchNote is a convenience wrapper that mirrors upstream check-word-mute.ts:
// it builds the search text from cw + "\n" + text (both nullable) and skips
// notes authored by the viewer. Caller passes the viewer's own user ID; an
// empty viewerID disables the self-skip.
func MatchNote(rulesJSON []byte, noteUserID, viewerID, noteText, noteCW string) bool {
	if viewerID != "" && noteUserID == viewerID {
		return false
	}
	combined := strings.TrimSpace(noteCW + "\n" + noteText)
	return Match(rulesJSON, combined)
}
