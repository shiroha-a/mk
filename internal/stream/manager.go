package stream

import (
	"sync"
	"sync/atomic"

	"github.com/gorilla/websocket"
	"github.com/shiroha-a/mk/internal/model"
)

// HardMuteRulesLookup returns the persisted hardMutedWords (raw jsonb) for
// the given user, or nil when the lookup fails / user has no rules. The
// streaming Manager calls this once at connection setup so timeline channels
// can drop matching notes per-publish (#787).
type HardMuteRulesLookup interface {
	HardMutedWordsForUser(userID string) []byte
}

// Manager owns the set of live streaming connections. Channel registry と
// PubSub bus を握り、各 connection に Dispatcher を割り当てて pubsub →
// channel のルーティングを行う。
type Manager struct {
	mu          sync.RWMutex
	conns       map[string]*Connection
	nextID      atomic.Uint64
	registry    *Registry
	bus         PubSubBus
	notifReader NotificationReader
	hardMute    HardMuteRulesLookup
}

// NewManager constructs a Manager with no live connections. registry / bus が
// nil でも動作する (channel framework を一切使わないテスト用)。
func NewManager(registry *Registry, bus PubSubBus) *Manager {
	return &Manager{
		conns:    make(map[string]*Connection),
		registry: registry,
		bus:      bus,
	}
}

// SetNotificationReader wires a NotificationReader so readNotification
// messages from clients are handled.
func (m *Manager) SetNotificationReader(nr NotificationReader) {
	m.notifReader = nr
}

// SetHardMuteLookup wires a lookup that returns the viewer's persisted
// hardMutedWords (#787). Called at connection setup; nil disables the
// per-publish hard mute filter.
func (m *Manager) SetHardMuteLookup(l HardMuteRulesLookup) {
	m.hardMute = l
}

// Accept implements api/streaming.ConnectionAcceptor. *websocket.Conn から
// Connection を組み立て、Dispatcher 経由で channel framework に橋渡しする。
func (m *Manager) Accept(ws *websocket.Conn, user *model.User) {
	id := m.allocateID()
	c := NewConnection(id, user, ws)
	if m.hardMute != nil && user != nil {
		// 接続後、最初の channel publish より前に rules を attach。fetch 失敗は
		// nil 返却で degrade — streaming は filter 無しで動き続ける (#787)。
		c.SetHardMuteRules(m.hardMute.HardMutedWordsForUser(user.ID))
	}
	dispatcher := NewDispatcher(c, m.registry, m.bus)
	if m.notifReader != nil {
		dispatcher.SetNotificationReader(m.notifReader)
	}
	c.SetMessageHandler(dispatcher.HandleClientMessage)
	c.SetCloseHandler(func() {
		dispatcher.CloseAll()
		m.unregister(id)
	})
	m.register(c)
	// Start ブロックするので呼び出し元 goroutine 上で読み続ける。
	c.Start()
}

// Count returns the number of currently registered connections. 主にテストと
// 運用メトリクス用。
func (m *Manager) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.conns)
}

// Get returns the connection for id, or nil if not registered.
func (m *Manager) Get(id string) *Connection {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.conns[id]
}

// RefreshHardMuteRules re-fetches userID's hardMutedWords from the wired
// lookup and pushes the result to every active connection owned by that
// user (#791). Called by the wordmute reload subscriber when i/update
// publishes a change.
//
// hardMute lookup が未配線 / userID が空 の場合は no-op。線形 scan だが
// reload 頻度は word mute 編集の瞬間のみで O(N) で十分。
func (m *Manager) RefreshHardMuteRules(userID string) {
	if m.hardMute == nil || userID == "" {
		return
	}
	rules := m.hardMute.HardMutedWordsForUser(userID)
	m.mu.RLock()
	targets := make([]*Connection, 0)
	for _, c := range m.conns {
		if u := c.User(); u != nil && u.ID == userID {
			targets = append(targets, c)
		}
	}
	m.mu.RUnlock()
	// SetHardMuteRules は lock 外で呼ぶ — Connection の internal mutex が
	// あるので thread-safe、Manager の mu は早めに release してデッドロック
	// 経路を作らない。
	for _, c := range targets {
		c.SetHardMuteRules(rules)
	}
}

// Shutdown closes every registered connection. サーバー停止時に呼ぶ。
func (m *Manager) Shutdown() {
	// pubsub subscriber goroutine を先に停止して、停止中の connection に
	// reload signal が届かないようにする (#791)。
	m.UnsubscribeWordMuteReload()
	m.mu.Lock()
	conns := make([]*Connection, 0, len(m.conns))
	for _, c := range m.conns {
		conns = append(conns, c)
	}
	m.conns = map[string]*Connection{}
	m.mu.Unlock()
	for _, c := range conns {
		c.Close()
	}
}

func (m *Manager) register(c *Connection) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.conns[c.ID()] = c
}

func (m *Manager) unregister(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.conns, id)
}

// allocateID returns a fresh sequential id for a new connection. WebSocket は
// プロセスローカルなので uint64 で十分。
func (m *Manager) allocateID() string {
	n := m.nextID.Add(1)
	return formatID(n)
}

// formatID converts the numeric counter to a short hex string.
func formatID(n uint64) string {
	const digits = "0123456789abcdef"
	if n == 0 {
		return "0"
	}
	var buf [16]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = digits[n&0xf]
		n >>= 4
	}
	return string(buf[i:])
}
