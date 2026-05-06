package stream

import (
	"encoding/json"
	"log/slog"
)

// WordMuteReloadTopic は i/update が hardMutedWords を変更したときに
// publish する Redis pubsub topic 名 (#791)。stream.Manager は本 topic を
// subscribe して該当 user の connection を refresh する。
const WordMuteReloadTopic = "wordmute:reload"

// WordMuteReloadPayload is the JSON wire format for WordMuteReloadTopic.
// userID 1 つだけだが将来 (= multi-user reload など) の拡張に備えて
// struct で wrap する。
type WordMuteReloadPayload struct {
	UserID string `json:"userId"`
}

// SubscribeWordMuteReload starts listening on the WordMuteReload topic via
// the wired PubSubBus and refreshes the matching connection's rules when
// a payload arrives. Idempotent — calling twice subscribes twice (caller
// must avoid that), and the goroutine lives until bus.Unsubscribe is called.
//
// bus が nil なら subscribe しない (= no-op、reload は永続的に届かないが
// snapshot fetch だけは引き続き機能する)。
func (m *Manager) SubscribeWordMuteReload() {
	if m.bus == nil {
		return
	}
	m.bus.Subscribe(WordMuteReloadTopic, func(raw []byte) {
		var payload WordMuteReloadPayload
		if err := json.Unmarshal(raw, &payload); err != nil {
			slog.Warn("stream: wordmute reload payload unmarshal failed", "err", err)
			return
		}
		if payload.UserID == "" {
			slog.Warn("stream: wordmute reload payload missing userId")
			return
		}
		m.RefreshHardMuteRules(payload.UserID)
	})
}

// UnsubscribeWordMuteReload stops listening on the WordMuteReload topic.
// Manager.Shutdown 経路に組み込んで、サーバー停止時に goroutine を解放する。
func (m *Manager) UnsubscribeWordMuteReload() {
	if m.bus == nil {
		return
	}
	m.bus.Unsubscribe(WordMuteReloadTopic)
}
