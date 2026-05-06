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
	"regexp"
	"strings"
	"sync"
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

// parsedCache stores the parsed rule slice keyed by the *raw* JSON bytes.
// Hot TL paths call Match many times with the same per-user rule set; parsing
// once per request is cheap but parsing per-note would be wasteful.
//
// 上限: 同じ rule JSON は 1 entry に dedupe されるが、ユーザーが rule を
// 更新するたびに新しい key が増え、古い key は GC されず dangling する
// (= rule revision に対して monotonic に grow する)。大規模インスタンスで
// 長時間運用するなら LRU 化を検討する (TS upstream は `acCache.size > 1000`
// で oldest を delete している)。現状は実装簡略のため単純 sync.Map で受けて
// いる。
var parsedCache sync.Map // string -> []rule

func parse(raw []byte) []rule {
	if len(raw) == 0 {
		return nil
	}
	key := string(raw)
	if v, ok := parsedCache.Load(key); ok {
		return v.([]rule)
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
	parsedCache.Store(key, out)
	return out
}

func parseEntry(e json.RawMessage) rule {
	var s string
	if err := json.Unmarshal(e, &s); err == nil {
		// Misskey TS upstream allows /regex/flags literal as a string entry.
		// Flags は upstream RE2 (TS) 仕様の i/m/s をサポートする。Go の regexp は
		// inline flag (?ims) が必要なので prefix で組み立てる。g (global) は Go
		// regexp の default 動作 (= 全 match) と等価なので無視。u/y は近い等価
		// 機能が無いが、Go regexp は default で UTF-8 解釈するため u は実質的
		// に常時 ON、y (sticky) は streaming 用途では使われないので no-op。
		if m := regexLiteral.FindStringSubmatch(s); m != nil {
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
			// 二重指定回避: pattern が既に inline flag group `(?ims)` で始まる
			// 場合のみ skip する。`(?:...)` / `(?P<n>...)` 等の non-flag group
			// に対しては flag を inject しないと意図した m / s 等が消えてしまう。
			if inline != "" && !inlineFlagPrefix.MatchString(pattern) {
				pattern = "(?" + inline + ")" + pattern
			}
			re, err := regexp.Compile(pattern)
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
