package model

// TimelineDBFilter holds filtering conditions for timeline DB fallback queries.
// *bool フィールドは nil のときデフォルト値として扱う (WithRenotes nil=true 等)。
type TimelineDBFilter struct {
	WithFiles             bool
	WithRenotes           *bool    // nil=true
	WithReplies           *bool    // nil=false
	IncludeMyRenotes      *bool    // nil=true
	IncludeRenotedMyNotes *bool    // nil=true
	IncludeLocalRenotes   *bool    // nil=true
	ViewerID              string   // home/hybridフィルタで使用
	MutedChannelIDs       []string // 指定があれば channelId が IN (...) のノートを除外
	// MutedUserIDs は viewer が mute した user の note を SQL 段階で除外
	// する filter (#892 / #874 follow-up)。userId AND renoteUserId 両方を
	// check する (= upstream Misskey TS QueryService の muting JOIN と同
	// semantics)。Redis fanout 経路 (post-fetch) では in-memory 版
	// ApplyFilter が同等処理を担う。
	//
	// production code path では UseMutingSubquery=true で subquery 経路を
	// 使うので本フィールドは空のまま。test と非 viewer 駆動経路では literal
	// list を直接渡す override path として残す。
	MutedUserIDs []string
	// UseMutingSubquery が true の場合、ViewerID を使って muting テーブルへ
	// の subquery で muted user filter を適用する (#894)。MutedUserIDs を
	// literal IN (...) で渡す方式に対し、bind parameter 数が viewer の
	// mute 件数に比例しないため heavy-mute viewer (>1000 mute) でも
	// PostgreSQL の planning コスト / parameter limit に達しない。
	// MutedUserIDs より優先 (両方 set されたら subquery が勝つ)。
	UseMutingSubquery bool
	// UseRenoteMutingSubquery が true の場合、ViewerID を使って
	// renote_muting テーブルへの subquery で **pure renote のみ** を除外
	// する filter を適用する (#903)。MutedUserIDs と異なり投稿者の plain
	// note は通す。upstream Misskey TS の
	// generateMutedUserRelatedRenotesQuery と同 semantics。
	UseRenoteMutingSubquery bool
}
