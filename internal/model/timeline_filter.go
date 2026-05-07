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
	MutedUserIDs []string
}
