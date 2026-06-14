package stream

// MuteBlockSnapshot captures the viewer's mute / block relationships at
// connection setup so the main / notifications channels can apply upstream
// `main.ts` の instance-mute / block / mute gate per-publish without a DB round
// trip (#1711). It mirrors the Connection-level Sets that upstream Misskey
// fetches in `Connection#fetch` (userIdsWhoMeMuting / userIdsWhoBlockingMe /
// userIdsWhoMeMutingRenotes / userMutedInstances / mutingChannels).
//
// 全 set は接続確立時に 1 回 fetch する snapshot で、followingSnapshot と同じく
// mute/block 変更時の live refresh は現状未実装 (= 接続を張り直すまで stale)。
// upstream は 10 秒間隔で再 fetch するが、mk-go では接続単位の精度で parity を
// 回復する。nil snapshot (anonymous / lookup 未配線 / fetch 失敗) は fail-open
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
