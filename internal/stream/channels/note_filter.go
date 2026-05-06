package channels

import (
	"encoding/json"

	"github.com/shiroha-a/mk/internal/core/wordmute"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/stream"
)

// viewerIDFromCtx extracts the authenticated user's ID from a ChannelContext.
// Returns "" for anonymous connections so wordmute.MatchNote treats every
// note as "not authored by self" (i.e. eligible for muting). Both the
// notesfilter / wordmute helpers handle empty viewerID safely.
func viewerIDFromCtx(ctx stream.ChannelContext) string {
	u, ok := ctx.User().(*model.User)
	if !ok || u == nil {
		return ""
	}
	return u.ID
}

// noteFilter provides client-controlled filtering for timeline channels.
// タイムラインチャンネルのconnectパラメータで指定されるフィルタ条件。
type noteFilter struct {
	WithRenotes bool `json:"withRenotes"`
	WithReplies bool `json:"withReplies"`
	WithFiles   bool `json:"withFiles"`
}

// defaultNoteFilter returns a filter with default values matching TS behavior.
func defaultNoteFilter() noteFilter {
	return noteFilter{
		WithRenotes: true,
		WithReplies: false,
		WithFiles:   false,
	}
}

// parseNoteFilter parses filter parameters from the connect params JSON.
func parseNoteFilter(params json.RawMessage) noteFilter {
	f := defaultNoteFilter()
	if len(params) > 0 {
		_ = json.Unmarshal(params, &f)
	}
	return f
}

// notePayload is the minimal structure needed for filtering decisions.
// hardMutedWords (#787) も同 struct で見るので user / cw を含める。
type notePayload struct {
	UserID   string   `json:"userId"`
	Text     *string  `json:"text"`
	CW       *string  `json:"cw"`
	RenoteID *string  `json:"renoteId"`
	ReplyID  *string  `json:"replyId"`
	FileIDs  []string `json:"fileIds"`
}

// shouldEmit returns true if the note passes all filter conditions.
//
// hardMuteRules (#787) には viewer の user_profile.hardMutedWords (jsonb) を
// そのまま渡す。空 / nil なら hard mute は no-op。viewerID は self skip 用、
// streaming は authenticated path のみ呼ばれるので空文字は anonymous 扱い。
func (f *noteFilter) shouldEmit(payload []byte, hardMuteRules []byte, viewerID string) bool {
	var note notePayload
	if err := json.Unmarshal(payload, &note); err != nil {
		// パース失敗時はそのまま送信
		return true
	}

	// 純リノート（テキストなし + renoteIdあり + ファイルなし）をフィルタ
	// timeline_filter.goのisPureRenoteと一致させる
	if !f.WithRenotes && note.Text == nil && note.RenoteID != nil && len(note.FileIDs) == 0 {
		return false
	}

	// リプライをフィルタ
	if !f.WithReplies && note.ReplyID != nil {
		return false
	}

	// ファイル付きのみモード
	if f.WithFiles && len(note.FileIDs) == 0 {
		return false
	}

	// hardMutedWords filter (#787)。rules 空なら no-op。
	if len(hardMuteRules) > 0 {
		text, cw := "", ""
		if note.Text != nil {
			text = *note.Text
		}
		if note.CW != nil {
			cw = *note.CW
		}
		if wordmute.MatchNote(hardMuteRules, note.UserID, viewerID, text, cw) {
			return false
		}
	}

	return true
}
