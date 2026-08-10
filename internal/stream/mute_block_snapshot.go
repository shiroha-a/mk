package stream

// MuteBlockSnapshot captures the viewer's mute / block relationships at
// connection setup so the main / notifications channels (#1711) and the
// timeline channels (home/local/hybrid/global/userList/channel/hashtag/antenna/
// roleTimeline, #1812) can apply upstream's instance-mute / block / mute /
// renote-mute / channel-mute gate per-publish without a DB round trip. It
// mirrors the Connection-level Sets that upstream Misskey
// fetches in `Connection#fetch` (userIdsWhoMeMuting / userIdsWhoBlockingMe /
// userIdsWhoMeMutingRenotes / userMutedInstances / mutingChannels).
//
// 全 set は接続確立時に 1 回 fetch する snapshot だが、mute / block / renote-mute /
// instance-mute / channel-mute を**操作した**ときは `RelationReloadTopic` 経由で
// 接続中も取り直す (#2400。SubscribeRelationReload / RefreshRelations)。upstream は
// 10 秒間隔の再 fetch で追随するのに対し、mk-go は変更した側の publish で反映する。
//
// **期限付き mute の自然失効だけはこの経路に乗らない。** `checkExpiredMutings`
// cron が行を prune するだけで reload を publish しないため、失効後も接続を
// 張り直すまで snapshot 側では mute されたままになる (upstream は次の再 fetch で
// 拾う)。過剰に filter する方向の degrade なので誤配信にはならない。
//
// nil snapshot (anonymous / lookup 未配線 / fetch 失敗) は fail-open
// 扱い — gate は何も drop せず upstream の「空 Set = 全通過」default に degrade
// する。
type MuteBlockSnapshot struct {
	// Muting は viewer が mute している user id の集合 (= userIdsWhoMeMuting)。
	Muting map[string]struct{}
	// BlockingMe は viewer を block している user id の集合
	// (= userIdsWhoBlockingMe)。これらの user が関わる note は drop する。
	BlockingMe map[string]struct{}
	// RenoteMuting は viewer が renote-mute している user id の集合
	// (= userIdsWhoMeMutingRenotes)。pure renote のみ gate する。
	RenoteMuting map[string]struct{}
	// MutedInstances は viewer が mute している instance host の集合
	// (= userMutedInstances)。host は lowercase 前提 (AP canonical)。
	MutedInstances map[string]struct{}
	// MutingChannels は viewer が mute している channel id の集合
	// (= mutingChannels)。
	MutingChannels map[string]struct{}
}

// MuteBlockSnapshotLookup returns the viewer's mute/block snapshot for the
// given user, used by the streaming Manager to attach it at connection setup
// (#1711). Returns nil for anonymous connections or when the lookup fails — in
// that case the main / notifications channels fall back to no filtering.
type MuteBlockSnapshotLookup interface {
	MuteBlockSnapshotForUser(userID string) *MuteBlockSnapshot
}
