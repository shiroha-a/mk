package timeline

import "github.com/shiroha-a/mk/internal/model"

// TimelineFilter holds filtering options for timeline queries.
// *bool フィールドは nil のときデフォルト値として扱う。
type TimelineFilter struct {
	WithFiles             bool     // trueならファイル付きノートのみ
	WithRenotes           *bool    // nil=true。falseでpure renote除外
	WithReplies           *bool    // nil=false。local/hybridのみ
	IncludeMyRenotes      *bool    // nil=true。home/hybridのみ
	IncludeRenotedMyNotes *bool    // nil=true。home/hybridのみ
	IncludeLocalRenotes   *bool    // nil=true。home/hybridのみ
	AllowPartial          bool     // trueならRedis結果が不足でもDBフォールバックしない
	MutedChannelIDs       []string // 指定があれば channelId が一致するノートを除外
	// MutedUserIDs は viewer が mute した user の note を除外する filter
	// 用 (#874)。nil なら filter 無効、空 slice なら filter 有効だが除外
	// 対象なし。renote の場合は renoteUserId も check する (= upstream
	// Misskey TS の muting JOIN と同 semantics)。
	MutedUserIDs []string
}

// boolDefault returns *b if non-nil, else def.
func boolDefault(b *bool, def bool) bool {
	if b != nil {
		return *b
	}
	return def
}

// isPureRenote returns true if the note is a pure renote (no text, no files).
func isPureRenote(n *model.Note) bool {
	return n.RenoteID != nil && n.Text == nil && len(n.FileIDs) == 0
}

// ApplyFilter filters notes in-memory according to the given TimelineFilter.
// viewerID is the currently authenticated user's ID (empty string if anonymous).
func ApplyFilter(notes []*model.Note, viewerID string, f TimelineFilter) []*model.Note {
	withRenotes := boolDefault(f.WithRenotes, true)
	withReplies := boolDefault(f.WithReplies, false)
	includeMyRenotes := boolDefault(f.IncludeMyRenotes, true)
	includeRenotedMyNotes := boolDefault(f.IncludeRenotedMyNotes, true)
	includeLocalRenotes := boolDefault(f.IncludeLocalRenotes, true)

	var mutedChannels map[string]struct{}
	if len(f.MutedChannelIDs) > 0 {
		mutedChannels = make(map[string]struct{}, len(f.MutedChannelIDs))
		for _, id := range f.MutedChannelIDs {
			mutedChannels[id] = struct{}{}
		}
	}

	var mutedUsers map[string]struct{}
	if len(f.MutedUserIDs) > 0 {
		mutedUsers = make(map[string]struct{}, len(f.MutedUserIDs))
		for _, id := range f.MutedUserIDs {
			mutedUsers[id] = struct{}{}
		}
	}

	out := make([]*model.Note, 0, len(notes))
	for _, n := range notes {
		if f.WithFiles && len(n.FileIDs) == 0 {
			continue
		}
		if !withRenotes && isPureRenote(n) {
			continue
		}
		if mutedChannels != nil && n.ChannelID != nil {
			if _, muted := mutedChannels[*n.ChannelID]; muted {
				continue
			}
		}
		// user mute filter: 投稿者が muted user なら除外。renote の場合は
		// renote 元 user も check する (= upstream Misskey TS の muting JOIN
		// と同 semantics、#874)。
		if mutedUsers != nil {
			if _, muted := mutedUsers[n.UserID]; muted {
				continue
			}
			if n.RenoteUserID != nil {
				if _, muted := mutedUsers[*n.RenoteUserID]; muted {
					continue
				}
			}
		}
		// withReplies=false: 他人への返信を除外 (自分への返信は残す)
		if !withReplies && n.ReplyID != nil {
			if viewerID == "" || (n.ReplyUserID != nil && *n.ReplyUserID != viewerID) {
				continue
			}
		}
		if isPureRenote(n) {
			// includeMyRenotes=false: 自分がした pure renote を除外
			if !includeMyRenotes && viewerID != "" && n.UserID == viewerID {
				continue
			}
			// includeRenotedMyNotes=false: 自分のノートの pure renote を除外
			if !includeRenotedMyNotes && viewerID != "" && n.RenoteUserID != nil && *n.RenoteUserID == viewerID {
				continue
			}
			// includeLocalRenotes=false: ローカルユーザーの pure renote を除外
			if !includeLocalRenotes && n.RenoteUserHost == nil {
				continue
			}
		}
		out = append(out, n)
	}
	return out
}
