package wordmute_test

import (
	"testing"

	"github.com/shiroha-a/mk/internal/core/wordmute"
	"github.com/stretchr/testify/assert"
)

func TestMatch_EmptyRules(t *testing.T) {
	assert.False(t, wordmute.Match(nil, "anything"))
	assert.False(t, wordmute.Match([]byte(""), "anything"))
	assert.False(t, wordmute.Match([]byte("[]"), "anything"))
	assert.False(t, wordmute.Match([]byte("null"), "anything"))
}

func TestMatch_EmptyText(t *testing.T) {
	assert.False(t, wordmute.Match([]byte(`["foo"]`), ""))
	assert.False(t, wordmute.Match([]byte(`["foo"]`), "   "))
}

func TestMatch_StringSubstring_CaseInsensitive(t *testing.T) {
	rules := []byte(`["FoO"]`)
	assert.True(t, wordmute.Match(rules, "hello FOO world"))
	assert.True(t, wordmute.Match(rules, "hello foo world"))
	assert.False(t, wordmute.Match(rules, "hello world"))
}

func TestMatch_AndGroup(t *testing.T) {
	rules := []byte(`[["alpha", "beta"]]`)
	assert.True(t, wordmute.Match(rules, "say alpha then beta"))
	assert.False(t, wordmute.Match(rules, "only alpha"))
	assert.False(t, wordmute.Match(rules, "only beta"))
}

func TestMatch_Regex(t *testing.T) {
	rules := []byte(`["/abc[0-9]+/"]`)
	assert.True(t, wordmute.Match(rules, "match abc123 here"))
	assert.False(t, wordmute.Match(rules, "match ABC here"))
}

func TestMatch_RegexCaseInsensitiveFlag(t *testing.T) {
	rules := []byte(`["/abc[0-9]+/i"]`)
	assert.True(t, wordmute.Match(rules, "match ABC42 here"))
	assert.True(t, wordmute.Match(rules, "match abc42 here"))
}

// upstream RE2 (TS) の m / s flag を Go の (?m) / (?s) に変換すること。
func TestMatch_RegexMultilineFlag(t *testing.T) {
	rules := []byte(`["/^bad/m"]`)
	// 1行目がマッチしない場合でも、改行後の行頭にマッチすれば hit。
	assert.True(t, wordmute.Match(rules, "ok line\nbad line"))
}

func TestMatch_RegexDotallFlag(t *testing.T) {
	rules := []byte(`["/foo.bar/s"]`)
	// dotall 無しでは . が \n にマッチしないが、s flag で改行をまたいで hit する。
	assert.True(t, wordmute.Match(rules, "foo\nbar"))
}

func TestMatch_RegexCombinedFlags(t *testing.T) {
	rules := []byte(`["/^foo.bar/ims"]`)
	assert.True(t, wordmute.Match(rules, "header\nFOO\nBAR"))
}

// g flag は Go regexp の default 動作と同義なので prefix を組み立てなくて OK。
func TestMatch_RegexGlobalFlagIsNoOp(t *testing.T) {
	rules := []byte(`["/abc/g"]`)
	assert.True(t, wordmute.Match(rules, "first abc and second abc"))
}

// non-capture group `(?:...)` で始まる pattern には flag を inject する
// (= inline flag prefix と誤認しない)。
func TestMatch_RegexNonCaptureGroupGetsFlagInjected(t *testing.T) {
	// `m` flag を要求 → `(?:foo)` の前に `(?m)` が付かないと改行後の foo に hit しない。
	rules := []byte(`["/^(?:foo)/m"]`)
	assert.True(t, wordmute.Match(rules, "header line\nfoo body"))
}

// named group `(?P<name>...)` で始まる pattern にも flag を inject する。
func TestMatch_RegexNamedGroupGetsFlagInjected(t *testing.T) {
	rules := []byte(`["/(?P<head>FOO)/i"]`)
	assert.True(t, wordmute.Match(rules, "lowercase foo here"))
}

// pattern が既に inline flag group で始まる場合は二重指定を避けて skip する。
func TestMatch_RegexInlineFlagPrefixNotDoubled(t *testing.T) {
	// `(?i)foo` は既に case-insensitive を指定しているので、外側で `(?i)` を
	// 重ねても compile error にしない (Go regexp は (?i)(?i) も valid だが
	// 意図的に skip しておく方が安全)。
	rules := []byte(`["/(?i)foo/i"]`)
	assert.True(t, wordmute.Match(rules, "FOO bar"))
}

func TestMatch_RegexInvalidIsIgnored(t *testing.T) {
	rules := []byte(`["/[unclosed/"]`)
	// Compilation failure must downgrade silently — never panic, never throw.
	assert.False(t, wordmute.Match(rules, "anything"))
}

func TestMatch_MixedRulesOrConnective(t *testing.T) {
	rules := []byte(`["foo", ["a", "b"], "/x[0-9]/"]`)
	assert.True(t, wordmute.Match(rules, "FOO bar"))
	assert.True(t, wordmute.Match(rules, "ab cd a b"))
	assert.True(t, wordmute.Match(rules, "x9 marker"))
	assert.False(t, wordmute.Match(rules, "nothing here"))
}

func TestMatch_AndGroupSkipsEmptyEntries(t *testing.T) {
	rules := []byte(`[["", "foo", ""]]`)
	// 空文字 entry は skip されるので残った "foo" の単一 AND 条件で hit する。
	assert.True(t, wordmute.Match(rules, "foo bar"))
}

func TestMatch_AndGroupAllEmptyIsNoOp(t *testing.T) {
	rules := []byte(`[["", ""]]`)
	// 全 empty は rule なしと同義 (誤爆防止)。
	assert.False(t, wordmute.Match(rules, "anything"))
}

func TestMatch_InvalidJSONReturnsFalse(t *testing.T) {
	assert.False(t, wordmute.Match([]byte(`{"not":"array"}`), "anything"))
	assert.False(t, wordmute.Match([]byte(`not json`), "anything"))
}

func TestMatchNote_SkipsViewerOwnNote(t *testing.T) {
	rules := []byte(`["secret"]`)
	// viewer 自身の note は muted word が含まれていても hit しない。
	assert.False(t, wordmute.MatchNote(rules, "user-1", "user-1", "secret leaked", ""))
	// 他人の note は hit。
	assert.True(t, wordmute.MatchNote(rules, "user-2", "user-1", "secret leaked", ""))
}

func TestMatchNote_CombinesCWAndText(t *testing.T) {
	rules := []byte(`["spoiler"]`)
	// cw に hit、text 空でも hit。
	assert.True(t, wordmute.MatchNote(rules, "u2", "u1", "", "spoiler ahead"))
	// text に hit、cw 空でも hit。
	assert.True(t, wordmute.MatchNote(rules, "u2", "u1", "spoiler ahead", ""))
	// 両方空なら hit しない。
	assert.False(t, wordmute.MatchNote(rules, "u2", "u1", "", ""))
}

func TestMatchNote_EmptyViewerIDDisablesSelfSkip(t *testing.T) {
	rules := []byte(`["foo"]`)
	// viewerID == "" は anonymous viewer を表し、author 一致による skip を無効化する。
	assert.True(t, wordmute.MatchNote(rules, "u1", "", "foo bar", ""))
}

// 同じ rules JSON を 100 回 parse しても問題なく動作する (parseCache の
// concurrent-safety smoke、race detector 経由で実検証)。
func TestMatch_ConcurrentSafe(t *testing.T) {
	rules := []byte(`["foo", ["a","b"]]`)
	for i := 0; i < 100; i++ {
		assert.True(t, wordmute.Match(rules, "foo"))
		assert.True(t, wordmute.Match(rules, "a b"))
	}
}
