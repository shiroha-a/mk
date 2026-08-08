package stream

import (
	"encoding/json"
	"log/slog"
)

// RelationReloadTopic は follow / mute / block 等の relation が変わったときに
// publish する Redis pubsub topic 名 (#2400)。stream.Manager は本 topic を
// subscribe して該当 user の connection が持つ snapshot を取り直す。
//
// 接続確立時の snapshot だけだと、mute / block した直後でも既存の WebSocket に
// 対象 user の event が届き続ける (再接続するまで stale)。upstream は 10 秒間隔で
// 再 fetch するので、この差は「再接続するまで永久」対「最大 10 秒」になる。
//
// 仕組みは #791 (hardMutedWords の reload) と同型。新方式を作らず揃えてある。
const RelationReloadTopic = "relation:reload"

// RelationScope selects which snapshot to rebuild.
//
// 要素ごとに topic を分けていない。publish 側が「どの set が変わったか」を知って
// いても、snapshot の取得は FollowingSnapshotForUser / MuteBlockSnapshotForUser の
// 2 粒度でしか行えないため、それより細かい scope を持たせても DB 往復は減らない。
type RelationScope string

const (
	// RelationScopeFollowing は followingSnapshot だけを取り直す。
	RelationScopeFollowing RelationScope = "following"
	// RelationScopeMuteBlock は MuteBlockSnapshot だけを取り直す。
	RelationScopeMuteBlock RelationScope = "muteblock"
	// RelationScopeAll は両方取り直す。未知の scope 値もこれにフォールバック
	// する (新しい publisher が古い subscriber に当たっても取りこぼさない)。
	RelationScopeAll RelationScope = "all"
)

// RelationReloadPayload is the JSON wire format for RelationReloadTopic.
type RelationReloadPayload struct {
	UserID string        `json:"userId"`
	Scope  RelationScope `json:"scope"`
}

// SubscribeRelationReload starts listening on the RelationReload topic and
// rebuilds the matching connections' snapshots when a payload arrives.
//
// **同一 Manager に対して 1 度だけ呼ぶこと**。複数回呼ぶと handler が二重登録
// されて reload event を重複処理する (SubscribeWordMuteReload と同じ制約)。
//
// bus が nil なら subscribe しない (= no-op)。既存構成が壊れないよう、reload が
// 届かないだけで接続時の snapshot fetch は引き続き機能する。
func (m *Manager) SubscribeRelationReload() {
	if m.bus == nil {
		return
	}
	m.bus.Subscribe(RelationReloadTopic, func(raw []byte) {
		var payload RelationReloadPayload
		if err := json.Unmarshal(raw, &payload); err != nil {
			slog.Warn("stream: relation reload payload unmarshal failed", "err", err)
			return
		}
		if payload.UserID == "" {
			slog.Warn("stream: relation reload payload missing userId")
			return
		}
		m.RefreshRelations(payload.UserID, payload.Scope)
	})
}

// UnsubscribeRelationReload stops listening on the RelationReload topic.
func (m *Manager) UnsubscribeRelationReload() {
	if m.bus == nil {
		return
	}
	m.bus.Unsubscribe(RelationReloadTopic)
}

// RefreshRelations rebuilds the snapshots of every connection owned by userID.
//
// 毎回 snapshot を作り直して**全置換**するので冪等。重複 event は無駄な DB 往復に
// なるだけで壊れない。置換方式なのは Connection の doc が要求している契約で、
// 既存 map を mutate すると in-flight な reader と race する。
//
// lookup 失敗時は nil が入り、既存どおり fail-open (= 何も filter しない) に
// degrade する。ここを fail-closed へ倒すと障害時にタイムラインが空になる方向へ
// 変わるため、本 issue の対象外とした (#2400)。
func (m *Manager) RefreshRelations(userID string, scope RelationScope) {
	if userID == "" {
		return
	}
	// 未知の scope は両方取り直す方へ倒す。新しい publisher が古い subscriber に
	// 当たっても取りこぼさない。
	refreshFollowing := scope != RelationScopeMuteBlock
	refreshMuteBlock := scope != RelationScopeFollowing

	m.mu.RLock()
	targets := make([]*Connection, 0)
	for _, c := range m.conns {
		if u := c.User(); u != nil && u.ID == userID {
			targets = append(targets, c)
		}
	}
	m.mu.RUnlock()
	if len(targets) == 0 {
		return
	}

	// lookup は Manager の mu を離してから行う。DB 往復を lock 内でやると
	// 接続 accept / 切断が待たされる。
	var following map[string]bool
	if refreshFollowing && m.followingLookup != nil {
		following = m.followingLookup.FollowingSnapshotForUser(userID)
	}
	var muteBlock *MuteBlockSnapshot
	if refreshMuteBlock && m.muteBlockLookup != nil {
		muteBlock = m.muteBlockLookup.MuteBlockSnapshotForUser(userID)
	}

	for _, c := range targets {
		if refreshFollowing && m.followingLookup != nil {
			c.SetFollowingSnapshot(following)
		}
		if refreshMuteBlock && m.muteBlockLookup != nil {
			c.SetMuteBlockSnapshot(muteBlock)
		}
	}
}

// RelationReloadPublisher publishes relation-change notifications so streaming
// connections can rebuild their snapshots (#2400). 実装は router の adapter。
//
// mutation 側 (core/muting、core/blocking、api/* handler) は本 interface だけを
// 知り、pubsub の存在を知らない。
type RelationReloadPublisher interface {
	PublishRelationReload(userID string, scope RelationScope)
}
