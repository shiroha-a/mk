// Package stream implements the per-connection state for the WebSocket
// streaming endpoint and the Manager that distributes pubsub messages to
// connected clients.
package stream

import (
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shiroha-a/mk/internal/model"
)

// Conn is the subset of *websocket.Conn that Connection actually uses. テスト
// 時に net.Conn を完全にスタブ化するため interface 化している。
type Conn interface {
	ReadMessage() (messageType int, p []byte, err error)
	WriteMessage(messageType int, data []byte) error
	WriteControl(messageType int, data []byte, deadline time.Time) error
	SetReadDeadline(t time.Time) error
	SetPongHandler(h func(appData string) error)
	Close() error
}

// defaultPingInterval は ping/pong による keepalive のデフォルト間隔。
const defaultPingInterval = 30 * time.Second

// readDeadline は Pong 待ちの猶予 (= pingInterval + 余白)。
const readDeadline = 60 * time.Second

// sendQueueSize はクライアントごとの送信バッファ長。これを超えると古いメッセージ
// は破棄され接続は閉じられる。
const sendQueueSize = 64

// MessageHandler is the callback invoked for each parsed client → server
// message. msgType / body は Misskey 風のワイヤプロトコル (e.g. "connect" /
// "disconnect"). Connection 側は dispatch せず、上位の Channel framework に
// 委譲する。
type MessageHandler func(msgType string, body json.RawMessage)

// CloseHandler is invoked once when the connection is fully torn down so that
// the Manager can release per-connection resources (channels, subscriptions).
type CloseHandler func()

// Connection wraps a single WebSocket client and provides thread-safe send
// semantics with a bounded outbound queue. すべての送信は writer goroutine 経由
// で行われるため、複数 goroutine から Send を呼び出してもデッドロックしない。
type Connection struct {
	id           string
	user         *model.User
	permissions  []string
	conn         Conn
	send         chan []byte
	closeC       chan struct{}
	pingInterval time.Duration

	closeOnce sync.Once
	closed    bool
	closedMu  sync.Mutex

	handler      MessageHandler
	closeHandler CloseHandler

	// hardMuteRules は viewer の user_profile.hardMutedWords をそのまま保持する
	// jsonb 由来 byte slice (#787)。streaming channel が per-note dispatch 時に
	// notesfilter.MatchOne でこの rules と照合し、match したら send しない。
	// 認証時に router が一度だけ fetch、また i/update 経由で reload 通知
	// (#791) が来たときに更新される。read (per-publish) と write (reload) が
	// 並行するので hardMuteMu で protect する。
	hardMuteMu    sync.RWMutex
	hardMuteRules []byte
}

// NewConnection wraps an upgraded WebSocket. id は呼び出し側 (Manager) が一意
// に割り当てる。MessageHandler が nil の場合、受信メッセージは破棄される。
func NewConnection(id string, user *model.User, conn Conn) *Connection {
	return &Connection{
		id:           id,
		user:         user,
		conn:         conn,
		send:         make(chan []byte, sendQueueSize),
		closeC:       make(chan struct{}),
		pingInterval: defaultPingInterval,
	}
}

// SetPingInterval overrides the keepalive ping cadence. 主にテストで時計を
// 縮めるための setter。0 以下は無視される。Start を呼ぶ前に設定すること。
func (c *Connection) SetPingInterval(d time.Duration) {
	if d > 0 {
		c.pingInterval = d
	}
}

// ID returns the connection identifier assigned by the Manager.
func (c *Connection) ID() string { return c.id }

// User returns the authenticated user, or nil for anonymous connections.
func (c *Connection) User() *model.User { return c.user }

// SetHardMuteRules attaches the viewer's persisted hardMutedWords (raw jsonb
// from user_profile) so timeline channels can drop matching notes before
// sending them to the client (#787). Called once after authentication and
// then again whenever a wordmute reload event for this user arrives via
// pubsub (#791) so changes propagate without forcing the client to reconnect.
func (c *Connection) SetHardMuteRules(rules []byte) {
	c.hardMuteMu.Lock()
	c.hardMuteRules = rules
	c.hardMuteMu.Unlock()
}

// HardMuteRules returns the currently active hardMutedWords rules. Safe for
// concurrent read while SetHardMuteRules updates the value (#791).
// Returns nil for anonymous connections / when the lookup failed / when the
// user has no rule set.
func (c *Connection) HardMuteRules() []byte {
	c.hardMuteMu.RLock()
	defer c.hardMuteMu.RUnlock()
	return c.hardMuteRules
}

// SetPermissions attaches OAuth2 permission scopes for this connection.
// トークン経由で接続された場合に、AccessToken の permission 配列を渡す想定。
// cookie/session 認証の場合は呼び出さない（空の permissions は権限ありとも無しとも区別できないため、
// チャンネル側の要件チェックに委ねる）。
func (c *Connection) SetPermissions(perms []string) {
	c.permissions = perms
}

// Permissions returns the OAuth2 permission scopes attached to this connection.
func (c *Connection) Permissions() []string {
	return c.permissions
}

// HasPermission reports whether the connection is allowed to access the
// given scope. session/cookie 認証ではトークン permission が無いため、
// nil permissions は「制限なし (フルアクセス)」として扱う。
// 空スライス ([]) は明示的にスコープなしのトークンを意味する。
func (c *Connection) HasPermission(kind string) bool {
	if kind == "" {
		return true
	}
	if c.permissions == nil {
		return true
	}
	return slices.Contains(c.permissions, kind)
}

// SetMessageHandler installs the callback invoked for each inbound client
// message. Must be called before Start.
func (c *Connection) SetMessageHandler(h MessageHandler) {
	c.handler = h
}

// SetCloseHandler installs a callback invoked once when the connection is
// fully closed.
func (c *Connection) SetCloseHandler(h CloseHandler) {
	c.closeHandler = h
}

// Start launches the read / write goroutines. Start blocks until the
// connection is closed; callers typically run it in a dedicated goroutine.
func (c *Connection) Start() {
	go c.writeLoop()
	c.readLoop()
}

// Send queues a JSON-encoded payload for delivery to the client. Returns an
// error if the connection is already closed or the queue is full.
func (c *Connection) Send(payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	c.closedMu.Lock()
	if c.closed {
		c.closedMu.Unlock()
		return errors.New("connection closed")
	}
	select {
	case c.send <- body:
		c.closedMu.Unlock()
		return nil
	default:
		// バッファが溢れたら接続を切断する。クライアントが追いついていない or
		// ネットワークが詰まっているケースで、これ以上保持しても遅延が悪化する
		// だけなので。closedMu は closeInternal が内部で取り直すので先に release。
		c.closedMu.Unlock()
		c.closeInternal()
		return errors.New("send queue full")
	}
}

// Close terminates the connection. 二重 close は安全。
func (c *Connection) Close() {
	c.closeInternal()
}

// closeInternal performs the actual close + cleanup logic. closeOnce で保護
// されているため繰り返し呼ばれても 1 回しか実行されない。
func (c *Connection) closeInternal() {
	c.closeOnce.Do(func() {
		c.closedMu.Lock()
		c.closed = true
		c.closedMu.Unlock()
		close(c.closeC)
		_ = c.conn.Close()
		if c.closeHandler != nil {
			c.closeHandler()
		}
	})
}

// readLoop drives ReadMessage in a loop, dispatching parsed payloads to the
// installed MessageHandler. ループ終了時には writer も巻き込んで全停止する。
func (c *Connection) readLoop() {
	defer c.closeInternal()
	c.conn.SetPongHandler(func(string) error {
		return c.conn.SetReadDeadline(time.Now().Add(readDeadline))
	})
	_ = c.conn.SetReadDeadline(time.Now().Add(readDeadline))
	for {
		_, data, err := c.conn.ReadMessage()
		if err != nil {
			return
		}
		var env struct {
			Type string          `json:"type"`
			Body json.RawMessage `json:"body"`
		}
		if err := json.Unmarshal(data, &env); err != nil {
			slog.Debug("stream: invalid client message", "err", err)
			continue
		}
		if c.handler != nil {
			c.handler(env.Type, env.Body)
		}
	}
}

// writeLoop drains the send queue and emits ping frames at pingInterval.
func (c *Connection) writeLoop() {
	ping := time.NewTicker(c.pingInterval)
	defer ping.Stop()
	for {
		select {
		case <-c.closeC:
			return
		case body := <-c.send:
			if err := c.conn.WriteMessage(websocket.TextMessage, body); err != nil {
				c.closeInternal()
				return
			}
		case <-ping.C:
			if err := c.conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(time.Second)); err != nil {
				c.closeInternal()
				return
			}
		}
	}
}
