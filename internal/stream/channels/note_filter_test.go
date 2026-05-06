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

func TestShouldEmit_ReplyFiltered(t *testing.T) {
	f := noteFilter{WithRenotes: true, WithReplies: false, WithFiles: false}
	payload := []byte(`{"text":"reply","replyId":"p1"}`)
	assert.False(t, f.shouldEmit(payload, nil, ""))
}

func TestShouldEmit_ReplyAllowed(t *testing.T) {
	f := noteFilter{WithRenotes: true, WithReplies: true, WithFiles: false}
	payload := []byte(`{"text":"reply","replyId":"p1"}`)
	assert.True(t, f.shouldEmit(payload, nil, ""))
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
