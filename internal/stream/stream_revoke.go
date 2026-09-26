package stream

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shiroha-a/mk/internal/misc/credkey"
)

// StreamRevokeTopic は資格情報の失効 (トークン再生成 / アクセストークン失効 /
// 凍結 / アカウント削除) を全プロセスの stream.Manager に伝える pubsub topic 名。
//
// WebSocket は接続時に一度だけ認証され、user / scope / role policy を
// Connection に固定する。HTTP 側の tokenCache を落としても既存の接続は
// 生き続けるので、漏洩したトークンで張られた接続が失効後も通知・DM・
// フォロワー限定投稿を受け取り続ける。接続は張られたプロセスにしか居ないため、
// 失効を処理したプロセスから直接閉じることはできず、pubsub で配る
// (#791 の wordmute reload と同型)。
//
// **upstream は現行版でもサーバー側から切っていない。** mk-go 独自の硬化
// (docs/divergence.md)。
const StreamRevokeTopic = "credential:revoke"

// StreamRevokePayload is the JSON wire format for StreamRevokeTopic.
//
// Credential が空なら UserID の全接続、空でなければ UserID の接続のうち
// その資格情報で張られたものだけを閉じる。UserID を必須にしているのは、
// 資格情報の鍵だけで照合すると、鍵の導出を誤ったときに他人の接続を閉じうるため。
type StreamRevokePayload struct {
	UserID     string `json:"userId"`
	Credential string `json:"credential,omitempty"`
}

// RevokeCloseCode is the WebSocket close code sent to revoked connections.
// 1008 (Policy Violation) は「方針に反するため閉じる」の汎用コードで、
// クライアントは再接続を試み、失効済みの資格情報なら upgrade で拒否される。
const RevokeCloseCode = websocket.ClosePolicyViolation

// revokeCloseReason is the close frame reason for revoked connections.
const revokeCloseReason = "credential revoked"

// defaultRevokeSettle は失効 event を受けてから接続を閉じるまでの猶予。
//
// **i/regenerate-token は myTokenRegenerated を main stream へ送ってから失効を
// publish する** が、両者は別 topic で、pubsub の実装は topic ごとに別の購読
// (= 別 goroutine) で配るので、受信側での到着順は保証されない。閉じた後の
// Send は拒否されるので、失効が先に着くと通知が落ちる。猶予を置いて、
// 先に publish された event が接続のキューに載るのを待つ。
// 猶予の間に流れうるのは「失効の直前まで正当に見えていたもの」と同じ範囲で、
// 閉じない場合 (無期限) と比べれば十分に短い。
const defaultRevokeSettle = time.Second

// SetRevokeSettleDelay overrides the delay between receiving a revoke event
// and closing the matching connections. d <= 0 restores the default. 主に
// テストで時計を縮めるための setter。
func (m *Manager) SetRevokeSettleDelay(d time.Duration) {
	m.revokeSettle = d
}

func (m *Manager) settleDelay() time.Duration {
	if m.revokeSettle > 0 {
		return m.revokeSettle
	}
	return defaultRevokeSettle
}

// RevokeStreams closes every live connection owned by userID, or only those
// authenticated with credential when it is non-empty. Returns the number of
// connections closed. userID が空なら何もしない。
//
// 閉じるときは送信キューを吐き切ってから close frame (RevokeCloseCode) を
// 送る (Connection.CloseWithCode)。
func (m *Manager) RevokeStreams(userID, credential string) int {
	if userID == "" {
		return 0
	}
	m.mu.RLock()
	targets := make([]*Connection, 0)
	for _, c := range m.conns {
		u := c.User()
		if u == nil || u.ID != userID {
			continue
		}
		if credential != "" && c.Credential() != credential {
			continue
		}
		targets = append(targets, c)
	}
	m.mu.RUnlock()
	// close handler が unregister で m.mu を取るので、lock の外で閉じる。
	for _, c := range targets {
		c.CloseWithCode(RevokeCloseCode, revokeCloseReason)
	}
	return len(targets)
}

// SubscribeStreamRevoke starts listening on StreamRevokeTopic and closes the
// matching local connections after the settle delay.
//
// **同一 Manager に対して 1 度だけ呼ぶこと** (SubscribeWordMuteReload と同じ
// 制約)。bus が nil なら subscribe しない。
func (m *Manager) SubscribeStreamRevoke() {
	if m.bus == nil {
		return
	}
	m.subscribeManaged(StreamRevokeTopic, func(raw []byte) {
		var payload StreamRevokePayload
		if err := json.Unmarshal(raw, &payload); err != nil {
			slog.Warn("stream: revoke payload unmarshal failed", "err", err)
			return
		}
		if payload.UserID == "" {
			slog.Warn("stream: revoke payload missing userId")
			return
		}
		// 走査も猶予の後に行う。失効の直前に認証を通り、まだ register されて
		// いない接続も拾える。
		time.AfterFunc(m.settleDelay(), func() {
			m.RevokeStreams(payload.UserID, payload.Credential)
		})
	})
}

// UnsubscribeStreamRevoke stops listening on StreamRevokeTopic.
func (m *Manager) UnsubscribeStreamRevoke() {
	if m.bus == nil {
		return
	}
	m.unsubscribeManaged(StreamRevokeTopic)
}

// StreamRevokePublisher publishes revoke events to StreamRevokeTopic. Handlers
// that invalidate a credential call it so connections on every process close.
type StreamRevokePublisher struct {
	pub PubSubPublisher
}

// NewStreamRevokePublisher constructs a StreamRevokePublisher.
func NewStreamRevokePublisher(pub PubSubPublisher) *StreamRevokePublisher {
	return &StreamRevokePublisher{pub: pub}
}

// RevokeUserStreams closes every streaming connection of userID (suspension,
// account deletion).
func (p *StreamRevokePublisher) RevokeUserStreams(userID string) {
	p.publish(StreamRevokePayload{UserID: userID})
}

// RevokeNativeTokenStreams closes the connections of userID that were
// authenticated with the given native login token (i/regenerate-token).
// token が空なら何もしない — 空のまま送ると全接続を閉じる意味になるため。
func (p *StreamRevokePublisher) RevokeNativeTokenStreams(userID, token string) {
	key := credkey.Native(token)
	if key == "" {
		return
	}
	p.publish(StreamRevokePayload{UserID: userID, Credential: key})
}

// RevokeAccessTokenStreams closes the connections of userID that were
// authenticated with the app access token tokenID (i/revoke-token, OAuth code
// replay). tokenID が空なら何もしない。
func (p *StreamRevokePublisher) RevokeAccessTokenStreams(userID, tokenID string) {
	key := credkey.AccessToken(tokenID)
	if key == "" {
		return
	}
	p.publish(StreamRevokePayload{UserID: userID, Credential: key})
}

func (p *StreamRevokePublisher) publish(payload StreamRevokePayload) {
	if p == nil || p.pub == nil || payload.UserID == "" {
		return
	}
	// 文字列だけの struct なので Marshal は失敗しない。
	raw, _ := json.Marshal(payload)
	if err := p.pub.Publish(context.Background(), StreamRevokeTopic, json.RawMessage(raw)); err != nil {
		slog.Warn("stream revoke: publish failed", "userId", payload.UserID, "err", err)
	}
}
