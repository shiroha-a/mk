package channels

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDefaultNoteFilter(t *testing.T) {
	f := defaultNoteFilter()
	assert.True(t, f.WithRenotes)
	assert.False(t, f.WithReplies)
	assert.False(t, f.WithFiles)
}

func TestParseNoteFilter_Empty(t *testing.T) {
	f := parseNoteFilter(nil)
	assert.True(t, f.WithRenotes)
}

func TestParseNoteFilter_Override(t *testing.T) {
	f := parseNoteFilter(json.RawMessage(`{"withRenotes":false,"withReplies":true,"withFiles":true}`))
	assert.False(t, f.WithRenotes)
	assert.True(t, f.WithReplies)
	assert.True(t, f.WithFiles)
}

func TestShouldEmit_PureRenoteFiltered(t *testing.T) {
	f := noteFilter{WithRenotes: false, WithReplies: false, WithFiles: false}
	// 純リノート: text=nil, renoteId!=nil, fileIds=[]
	payload := []byte(`{"renoteId":"r1","fileIds":[]}`)
	assert.False(t, f.shouldEmit(payload, nil, ""))
}

func TestShouldEmit_QuoteRenoteAllowed(t *testing.T) {
	f := noteFilter{WithRenotes: false, WithReplies: false, WithFiles: false}
	// 引用リノート: text!=nil, renoteId!=nil → 通過
	payload := []byte(`{"text":"hello","renoteId":"r1"}`)
	assert.True(t, f.shouldEmit(payload, nil, ""))
}

func TestShouldEmit_RenoteWithFilesAllowed(t *testing.T) {
	f := noteFilter{WithRenotes: false, WithReplies: false, WithFiles: false}
	// ファイル添付付きリノート: text=nil, renoteId!=nil, fileIds非空 → 純リノートではない
	payload := []byte(`{"renoteId":"r1","fileIds":["f1"]}`)
	assert.True(t, f.shouldEmit(payload, nil, ""))
}

// #1063: shouldEmit は reply blanket-drop を持たない (upstream Misskey TS の
// home/hybrid/local channel に揃った semantics で、reply 表示可否は
// replyShouldEmit が channel ごとに判定する)。WithReplies フィールドは
// connect param 互換のために残っているが、shouldEmit からは参照しない。
// 旧テスト名 `TestShouldEmit_ReplyFiltered` (`WithReplies=false → drop`) /
// `TestShouldEmit_ReplyAllowed` (`WithReplies=true → pass`) は drift 挙動の
// assertion だったので、`WithReplies` 真偽どちらでも pass-through すること
// を 1 つの test で並列に検証する形に統合した。
func TestShouldEmit_ReplyAlwaysPassthrough(t *testing.T) {
	payload := []byte(`{"text":"reply","replyId":"p1"}`)
	for _, with := range []bool{false, true} {
		f := noteFilter{WithRenotes: true, WithReplies: with, WithFiles: false}
		assert.True(t, f.shouldEmit(payload, nil, ""), "WithReplies=%v should not affect shouldEmit", with)
	}
}

// #1063: replyShouldEmit の 3 escape hatch と followee snapshot gate を覆う。
// upstream `home-timeline.ts` / `hybrid-timeline.ts` / `local-timeline.ts` /
// `global-timeline.ts` の reply 判定を Go で再実装したものなので、ここで
// drift regression を吸収する。
func TestReplyShouldEmit_NonReplyAlwaysPasses(t *testing.T) {
	payload := []byte(`{"userId":"author","text":"hello"}`)
	for _, mode := range []replyGateMode{replyGateHome, replyGateLocal, replyGateGlobal} {
		assert.True(t, replyShouldEmit(payload, "viewer", nil, false, mode))
	}
}

func TestReplyShouldEmit_GlobalNeverFilters(t *testing.T) {
	// reply embed が無くても (= drop 候補のはず) globalTimeline は通す。
	payload := []byte(`{"userId":"author","replyId":"p1","text":"r"}`)
	assert.True(t, replyShouldEmit(payload, "viewer", nil, false, replyGateGlobal))
}

func TestReplyShouldEmit_IsMeEscapeHatch(t *testing.T) {
	// 自分の reply は snapshot 無しでも常に pass (#1047 → #1055 → #1063 の本丸)。
	payload := []byte(`{"userId":"viewer","replyId":"p1","reply":{"userId":"someone","visibility":"public"}}`)
	assert.True(t, replyShouldEmit(payload, "viewer", nil, false, replyGateHome))
	assert.True(t, replyShouldEmit(payload, "viewer", nil, false, replyGateLocal))
}

func TestReplyShouldEmit_ReplyToMeEscapeHatch(t *testing.T) {
	// 自分宛 reply は author を follow していなくても pass。
	payload := []byte(`{"userId":"author","replyId":"p1","reply":{"userId":"viewer","visibility":"public"}}`)
	assert.True(t, replyShouldEmit(payload, "viewer", nil, false, replyGateHome))
	assert.True(t, replyShouldEmit(payload, "viewer", nil, false, replyGateLocal))
}

func TestReplyShouldEmit_SelfThreadEscapeHatch(t *testing.T) {
	// author が自分自身に返信した self-thread は snapshot 無しでも pass。
	payload := []byte(`{"userId":"author","replyId":"p1","reply":{"userId":"author","visibility":"public"}}`)
	assert.True(t, replyShouldEmit(payload, "viewer", nil, false, replyGateHome))
	assert.True(t, replyShouldEmit(payload, "viewer", nil, false, replyGateLocal))
}

func TestReplyShouldEmit_HomeDropsForeignReplyWithoutFolloweeWithReplies(t *testing.T) {
	// 他人 → 他人 reply (3 escape hatch どれも該当しない) は default で drop。
	payload := []byte(`{"userId":"author","replyId":"p1","reply":{"userId":"target","visibility":"public"}}`)
	snap := map[string]bool{"author": false}
	assert.False(t, replyShouldEmit(payload, "viewer", snap, false, replyGateHome))
}

func TestReplyShouldEmit_HomeAllowsForeignReplyWhenFolloweeWithReplies(t *testing.T) {
	// follow している author の reply は followee.withReplies=true なら pass。
	payload := []byte(`{"userId":"author","replyId":"p1","reply":{"userId":"target","visibility":"public"}}`)
	snap := map[string]bool{"author": true}
	assert.True(t, replyShouldEmit(payload, "viewer", snap, false, replyGateHome))
}

func TestReplyShouldEmit_HomeFollowersVisibilityNeedsTargetFollow(t *testing.T) {
	// followee.withReplies=true でも reply 先の visibility=followers で、
	// reply.userId を自分が follow していない & 自分宛でもないなら drop。
	payload := []byte(`{"userId":"author","replyId":"p1","reply":{"userId":"target","visibility":"followers"}}`)
	snap := map[string]bool{"author": true}
	assert.False(t, replyShouldEmit(payload, "viewer", snap, false, replyGateHome))

	// 同条件で target を follow していれば pass。
	snap2 := map[string]bool{"author": true, "target": false}
	assert.True(t, replyShouldEmit(payload, "viewer", snap2, false, replyGateHome))
}

func TestReplyShouldEmit_HomeIgnoresConnectParamWithReplies(t *testing.T) {
	// upstream home-timeline には withReplies connect param が無い。
	// paramWithReplies=true でも snapshot が空なら drop される。
	payload := []byte(`{"userId":"author","replyId":"p1","reply":{"userId":"target","visibility":"public"}}`)
	assert.False(t, replyShouldEmit(payload, "viewer", nil, true, replyGateHome))
}

func TestReplyShouldEmit_LocalAnonymousPassesAllReplies(t *testing.T) {
	// upstream local-timeline は anonymous viewer に対して reply gate を
	// かけない (`if (note.reply && this.user && ...)` の this.user が false)。
	payload := []byte(`{"userId":"author","replyId":"p1","reply":{"userId":"target","visibility":"public"}}`)
	assert.True(t, replyShouldEmit(payload, "", nil, false, replyGateLocal))
}

func TestReplyShouldEmit_LocalConnectParamWithRepliesPasses(t *testing.T) {
	// authenticated viewer & withReplies=true connect param で reply pass。
	payload := []byte(`{"userId":"author","replyId":"p1","reply":{"userId":"target","visibility":"public"}}`)
	assert.True(t, replyShouldEmit(payload, "viewer", nil, true, replyGateLocal))
}

func TestReplyShouldEmit_HybridConnectParamWithRepliesPasses(t *testing.T) {
	// upstream hybrid-timeline は followee.withReplies OR param.withReplies。
	// snapshot が空 (= follow していない) でも param.withReplies=true なら pass。
	payload := []byte(`{"userId":"author","replyId":"p1","reply":{"userId":"target","visibility":"public"}}`)
	assert.True(t, replyShouldEmit(payload, "viewer", nil, true, replyGateHybrid))
}

func TestReplyShouldEmit_MissingReplyEmbedIsConservative(t *testing.T) {
	// reply embed が無い場合 (= reply.userId 参照できない) は 3 escape hatch の
	// うち isMe しか判定できない。conservative に drop して fanout / DB の
	// fallback (= リロード時に表示) に倒す。
	payload := []byte(`{"userId":"author","replyId":"p1"}`)
	assert.False(t, replyShouldEmit(payload, "viewer", nil, false, replyGateHome))
	// isMe escape hatch は機能する。
	payload2 := []byte(`{"userId":"viewer","replyId":"p1"}`)
	assert.True(t, replyShouldEmit(payload2, "viewer", nil, false, replyGateHome))
}

func TestShouldEmit_WithFilesNoFiles(t *testing.T) {
	f := noteFilter{WithRenotes: true, WithReplies: true, WithFiles: true}
	payload := []byte(`{"text":"hello","fileIds":[]}`)
	assert.False(t, f.shouldEmit(payload, nil, ""))
}

func TestShouldEmit_WithFilesHasFiles(t *testing.T) {
	f := noteFilter{WithRenotes: true, WithReplies: true, WithFiles: true}
	payload := []byte(`{"text":"hello","fileIds":["f1"]}`)
	assert.True(t, f.shouldEmit(payload, nil, ""))
}

func TestShouldEmit_InvalidJSON(t *testing.T) {
	f := noteFilter{WithRenotes: false}
	// パース失敗時はそのまま送信
	assert.True(t, f.shouldEmit([]byte(`{invalid`), nil, ""))
}

func TestShouldEmit_NormalNote(t *testing.T) {
	f := noteFilter{WithRenotes: true, WithReplies: false, WithFiles: false}
	payload := []byte(`{"text":"hello"}`)
	assert.True(t, f.shouldEmit(payload, nil, ""))
}

// #787: hardMutedWords による per-publish filter (streaming path)。
func TestShouldEmit_HardMute_TextHit(t *testing.T) {
	f := noteFilter{WithRenotes: true, WithReplies: true, WithFiles: false}
	payload := []byte(`{"userId":"author","text":"this contains POLITICS"}`)
	rules := []byte(`["politics"]`)
	assert.False(t, f.shouldEmit(payload, rules, "viewer"))
}

func TestShouldEmit_HardMute_CWHit(t *testing.T) {
	f := noteFilter{WithRenotes: true, WithReplies: true, WithFiles: false}
	payload := []byte(`{"userId":"author","text":"clean body","cw":"spoiler ahead"}`)
	rules := []byte(`["spoiler"]`)
	assert.False(t, f.shouldEmit(payload, rules, "viewer"))
}

func TestShouldEmit_HardMute_ViewerOwnNoteExempt(t *testing.T) {
	f := noteFilter{WithRenotes: true, WithReplies: true, WithFiles: false}
	// note.userId == viewerID なので self skip で送信される
	payload := []byte(`{"userId":"viewer","text":"politics by self"}`)
	rules := []byte(`["politics"]`)
	assert.True(t, f.shouldEmit(payload, rules, "viewer"))
}

func TestShouldEmit_HardMute_NoRulesIsNoOp(t *testing.T) {
	f := noteFilter{WithRenotes: true, WithReplies: true, WithFiles: false}
	payload := []byte(`{"userId":"author","text":"politics"}`)
	assert.True(t, f.shouldEmit(payload, nil, "viewer"))
	assert.True(t, f.shouldEmit(payload, []byte("[]"), "viewer"))
}
