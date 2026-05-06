package stream

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/shiroha-a/mk/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubBus implements PubSubBus and records subscribe/unsubscribe calls.
type stubBus struct {
	mu         sync.Mutex
	subs       map[string]func([]byte)
	unsubs     []string
	subscribed []string
}

func newStubBus() *stubBus {
	return &stubBus{subs: map[string]func([]byte){}}
}

func (b *stubBus) Subscribe(topic string, handler func([]byte)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.subs[topic] = handler
	b.subscribed = append(b.subscribed, topic)
}

func (b *stubBus) Unsubscribe(topic string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.subs, topic)
	b.unsubs = append(b.unsubs, topic)
}

func (b *stubBus) deliver(topic string, payload []byte) {
	b.mu.Lock()
	h := b.subs[topic]
	b.mu.Unlock()
	if h != nil {
		h(payload)
	}
}

// fakeChannel is a Channel that records every callback for assertion.
type fakeChannel struct {
	ctx          ChannelContext
	initParams   json.RawMessage
	initErr      error
	disposeCount int
	redisEvents  [][]byte
	clientMsgs   []string
	subscribed   []string
}

func (f *fakeChannel) Init(params json.RawMessage) error {
	f.initParams = params
	if f.initErr != nil {
		return f.initErr
	}
	f.subscribed = append(f.subscribed, "topic-a")
	f.ctx.Subscribe("topic-a")
	return nil
}
func (f *fakeChannel) Dispose() { f.disposeCount++ }
func (f *fakeChannel) OnRedisEvent(payload []byte) {
	f.redisEvents = append(f.redisEvents, append([]byte(nil), payload...))
}
func (f *fakeChannel) OnClientMessage(msgType string, _ json.RawMessage) {
	f.clientMsgs = append(f.clientMsgs, msgType)
}

// fakeChannelHolder lets tests grab a reference to the channel created by the
// factory. The factory closure stores into Holder.Value when invoked.
type fakeChannelHolder struct {
	Value *fakeChannel
}

func newDispatcherWithFake(t *testing.T) (*Dispatcher, *Connection, *Registry, *stubBus, *fakeChannelHolder) {
	t.Helper()
	bus := newStubBus()
	registry := NewRegistry()
	holder := &fakeChannelHolder{}
	registry.Register("test", func(ctx ChannelContext) Channel {
		ch := &fakeChannel{ctx: ctx}
		holder.Value = ch
		return ch
	})
	conn := NewConnection("c1", &model.User{ID: "alice"}, newFakeConn())
	d := NewDispatcher(conn, registry, bus)
	return d, conn, registry, bus, holder
}

func TestRegistry_RegisterAndLookup(t *testing.T) {
	r := NewRegistry()
	r.Register("foo", func(ctx ChannelContext) Channel { return &fakeChannel{ctx: ctx} })
	assert.NotNil(t, r.Lookup("foo"))
	assert.Nil(t, r.Lookup("missing"))
}

func TestDispatcher_HandleConnect(t *testing.T) {
	d, _, _, bus, _ := newDispatcherWithFake(t)
	d.HandleClientMessage("connect", json.RawMessage(`{"id":"abc","channel":"test","params":{}}`))

	// fake channel が Init で subscribe するので bus に topic-a が登録される
	bus.mu.Lock()
	defer bus.mu.Unlock()
	assert.Contains(t, bus.subscribed, "topic-a")
}

func TestDispatcher_HandleConnectInvalidJSON(t *testing.T) {
	d, _, _, _, _ := newDispatcherWithFake(t)
	d.HandleClientMessage("connect", json.RawMessage(`{not json`))
}

func TestDispatcher_HandleConnectMissingFields(t *testing.T) {
	d, _, _, _, _ := newDispatcherWithFake(t)
	d.HandleClientMessage("connect", json.RawMessage(`{}`))
}

func TestDispatcher_HandleConnectUnknownChannel(t *testing.T) {
	d, _, _, _, _ := newDispatcherWithFake(t)
	d.HandleClientMessage("connect", json.RawMessage(`{"id":"x","channel":"missing"}`))
}

func TestDispatcher_HandleConnectNoRegistry(t *testing.T) {
	conn := NewConnection("c1", nil, newFakeConn())
	d := NewDispatcher(conn, nil, newStubBus())
	d.HandleClientMessage("connect", json.RawMessage(`{"id":"x","channel":"test"}`))
}

func TestDispatcher_HandleConnectDuplicateID(t *testing.T) {
	d, _, _, _, _ := newDispatcherWithFake(t)
	d.HandleClientMessage("connect", json.RawMessage(`{"id":"abc","channel":"test"}`))
	d.HandleClientMessage("connect", json.RawMessage(`{"id":"abc","channel":"test"}`))
}

func TestDispatcher_HandleDisconnect(t *testing.T) {
	d, _, _, bus, holder := newDispatcherWithFake(t)
	d.HandleClientMessage("connect", json.RawMessage(`{"id":"abc","channel":"test"}`))
	require.NotNil(t, holder.Value)
	d.HandleClientMessage("disconnect", json.RawMessage(`{"id":"abc"}`))
	assert.Equal(t, 1, holder.Value.disposeCount)
	bus.mu.Lock()
	defer bus.mu.Unlock()
	assert.Contains(t, bus.unsubs, "topic-a")
}

func TestDispatcher_HandleDisconnectInvalidJSON(t *testing.T) {
	d, _, _, _, _ := newDispatcherWithFake(t)
	d.HandleClientMessage("disconnect", json.RawMessage(`{bad`))
}

func TestDispatcher_HandleDisconnectUnknown(t *testing.T) {
	d, _, _, _, _ := newDispatcherWithFake(t)
	d.HandleClientMessage("disconnect", json.RawMessage(`{"id":"missing"}`))
}

func TestDispatcher_HandleChannelMessage(t *testing.T) {
	d, _, _, _, holder := newDispatcherWithFake(t)
	d.HandleClientMessage("connect", json.RawMessage(`{"id":"abc","channel":"test"}`))
	require.NotNil(t, holder.Value)
	d.HandleClientMessage("ch", json.RawMessage(`{"id":"abc","type":"ping","body":{}}`))
	assert.Equal(t, []string{"ping"}, holder.Value.clientMsgs)
}

func TestDispatcher_HandleChannelMessageBadJSON(t *testing.T) {
	d, _, _, _, _ := newDispatcherWithFake(t)
	d.HandleClientMessage("ch", json.RawMessage(`{bad`))
}

func TestDispatcher_HandleChannelMessageUnknown(t *testing.T) {
	d, _, _, _, _ := newDispatcherWithFake(t)
	d.HandleClientMessage("ch", json.RawMessage(`{"id":"missing","type":"ping"}`))
}

func TestDispatcher_HandleUnknownEnvelope(t *testing.T) {
	d, _, _, _, _ := newDispatcherWithFake(t)
	d.HandleClientMessage("noise", json.RawMessage(`{}`))
}

func TestDispatcher_FanoutDeliversRedisEvents(t *testing.T) {
	d, _, _, bus, holder := newDispatcherWithFake(t)
	d.HandleClientMessage("connect", json.RawMessage(`{"id":"abc","channel":"test"}`))
	require.NotNil(t, holder.Value)
	bus.deliver("topic-a", []byte(`{"event":"hi"}`))
	require.Len(t, holder.Value.redisEvents, 1)
	assert.Contains(t, string(holder.Value.redisEvents[0]), "hi")
}

func TestDispatcher_CloseAllDisposesEverything(t *testing.T) {
	d, _, _, bus, holder := newDispatcherWithFake(t)
	d.HandleClientMessage("connect", json.RawMessage(`{"id":"abc","channel":"test"}`))
	require.NotNil(t, holder.Value)
	d.CloseAll()
	assert.Equal(t, 1, holder.Value.disposeCount)
	bus.mu.Lock()
	defer bus.mu.Unlock()
	assert.Contains(t, bus.unsubs, "topic-a")
}

func TestDispatcher_DuplicateSubscribeIsNoOp(t *testing.T) {
	d, _, _, bus, _ := newDispatcherWithFake(t)
	d.HandleClientMessage("connect", json.RawMessage(`{"id":"abc","channel":"test"}`))
	// Init で 1 回 subscribe 済み。同じ topic を再度 subscribe しても増えない。
	d.subscribe("abc", "topic-a")
	bus.mu.Lock()
	defer bus.mu.Unlock()
	count := 0
	for _, t := range bus.subscribed {
		if t == "topic-a" {
			count++
		}
	}
	assert.Equal(t, 1, count)
}

func TestDispatcher_SubscribeUnknownChannelIsNoOp(t *testing.T) {
	d, _, _, _, _ := newDispatcherWithFake(t)
	d.subscribe("missing", "topic-a")
}

func TestDispatcher_UnsubscribeMissingTopicIsNoOp(t *testing.T) {
	d, _, _, _, _ := newDispatcherWithFake(t)
	d.HandleClientMessage("connect", json.RawMessage(`{"id":"abc","channel":"test"}`))
	d.unsubscribe("abc", "topic-zz")
}

func TestDispatcher_UnsubscribeUnknownChannelIsNoOp(t *testing.T) {
	d, _, _, _, _ := newDispatcherWithFake(t)
	d.unsubscribe("missing", "topic-a")
}

func TestDispatcher_SharedTopicReferenceCount(t *testing.T) {
	bus := newStubBus()
	registry := NewRegistry()
	registry.Register("test", func(ctx ChannelContext) Channel {
		return &fakeChannel{ctx: ctx}
	})
	conn := NewConnection("c1", nil, newFakeConn())
	d := NewDispatcher(conn, registry, bus)

	// 2 つの channel が同じ topic を共有
	d.HandleClientMessage("connect", json.RawMessage(`{"id":"a","channel":"test"}`))
	d.HandleClientMessage("connect", json.RawMessage(`{"id":"b","channel":"test"}`))

	bus.mu.Lock()
	subscribedCount := 0
	for _, t := range bus.subscribed {
		if t == "topic-a" {
			subscribedCount++
		}
	}
	bus.mu.Unlock()
	// 1 度しか subscribe されない (reference count 共有)
	assert.Equal(t, 1, subscribedCount)

	// 1 つ disconnect しても unsubscribe されない
	d.HandleClientMessage("disconnect", json.RawMessage(`{"id":"a"}`))
	bus.mu.Lock()
	assert.Empty(t, bus.unsubs)
	bus.mu.Unlock()

	// 残り 1 つを disconnect すると unsubscribe される
	d.HandleClientMessage("disconnect", json.RawMessage(`{"id":"b"}`))
	bus.mu.Lock()
	assert.Contains(t, bus.unsubs, "topic-a")
	bus.mu.Unlock()
}

func TestDispatcher_NilBusOnSubscribe(t *testing.T) {
	conn := NewConnection("c1", nil, newFakeConn())
	registry := NewRegistry()
	registry.Register("test", func(ctx ChannelContext) Channel {
		return &fakeChannel{ctx: ctx}
	})
	d := NewDispatcher(conn, registry, nil)
	d.HandleClientMessage("connect", json.RawMessage(`{"id":"abc","channel":"test"}`))
	d.HandleClientMessage("disconnect", json.RawMessage(`{"id":"abc"}`))
}

// channelContextSendingTest exercises the channelContext Send / User helpers.
type sendingChannel struct {
	ctx     ChannelContext
	got     string
	gotUser any
}

func (s *sendingChannel) Init(json.RawMessage) error {
	s.gotUser = s.ctx.User()
	_ = s.ctx.Send("note", map[string]any{"text": "hi"})
	s.got = "ok"
	return nil
}
func (s *sendingChannel) Dispose()                                {}
func (s *sendingChannel) OnRedisEvent([]byte)                     {}
func (s *sendingChannel) OnClientMessage(string, json.RawMessage) {}

func TestChannelContext_NilConnUserAndSend(t *testing.T) {
	bus := newStubBus()
	registry := NewRegistry()
	registry.Register("test", func(ctx ChannelContext) Channel { return &fakeChannel{ctx: ctx} })
	d := NewDispatcher(nil, registry, bus)
	d.HandleClientMessage("connect", json.RawMessage(`{"id":"abc","channel":"test"}`))
	// channelContext.User() is nil, Send returns nil error.
	d.mu.RLock()
	entry := d.channels["abc"]
	d.mu.RUnlock()
	require.NotNil(t, entry)
	ctx := &channelContext{dispatcher: d, id: "abc"}
	assert.Nil(t, ctx.User())
	assert.NoError(t, ctx.Send("note", nil))
	assert.Equal(t, "abc", ctx.ID())
}

func TestChannelContext_UnsubscribeViaContext(t *testing.T) {
	d, _, _, bus, holder := newDispatcherWithFake(t)
	d.HandleClientMessage("connect", json.RawMessage(`{"id":"abc","channel":"test"}`))
	require.NotNil(t, holder.Value)
	holder.Value.ctx.Unsubscribe("topic-a")
	bus.mu.Lock()
	defer bus.mu.Unlock()
	assert.Contains(t, bus.unsubs, "topic-a")
}

func TestDispatcher_UnsubscribeRemovesEntryAndStopsBus(t *testing.T) {
	d, _, _, bus, holder := newDispatcherWithFake(t)
	d.HandleClientMessage("connect", json.RawMessage(`{"id":"abc","channel":"test"}`))
	require.NotNil(t, holder.Value)
	d.unsubscribe("abc", "topic-a")
	bus.mu.Lock()
	defer bus.mu.Unlock()
	assert.Contains(t, bus.unsubs, "topic-a")
}

func TestDispatcher_UnsubscribeNilBusIsNoOp(t *testing.T) {
	registry := NewRegistry()
	holder := &fakeChannelHolder{}
	registry.Register("test", func(ctx ChannelContext) Channel {
		ch := &fakeChannel{ctx: ctx}
		holder.Value = ch
		return ch
	})
	conn := NewConnection("c1", nil, newFakeConn())
	d := NewDispatcher(conn, registry, nil)
	d.HandleClientMessage("connect", json.RawMessage(`{"id":"abc","channel":"test"}`))
	require.NotNil(t, holder.Value)
	d.unsubscribe("abc", "topic-a")
}

func TestChannelContext_SendQueuesEnvelope(t *testing.T) {
	bus := newStubBus()
	registry := NewRegistry()
	var sc *sendingChannel
	registry.Register("test", func(ctx ChannelContext) Channel {
		sc = &sendingChannel{ctx: ctx}
		return sc
	})
	fc := newFakeConn()
	conn := NewConnection("c1", &model.User{ID: "alice"}, fc)
	d := NewDispatcher(conn, registry, bus)
	go conn.Start()
	defer conn.Close()

	d.HandleClientMessage("connect", json.RawMessage(`{"id":"abc","channel":"test"}`))
	require.NotNil(t, sc)
	require.NotNil(t, sc.gotUser)
	user, ok := sc.gotUser.(*model.User)
	require.True(t, ok)
	assert.Equal(t, "alice", user.ID)

	require.Eventually(t, func() bool { return fc.writeCount() >= 1 }, time.Second, 10*time.Millisecond)
}

// --- pong ack ---

func TestDispatcher_ConnectPongAck(t *testing.T) {
	bus := newStubBus()
	registry := NewRegistry()
	registry.Register("test", func(ctx ChannelContext) Channel { return &fakeChannel{ctx: ctx} })

	fc := newFakeConn()
	conn := NewConnection("c1", &model.User{ID: "alice"}, fc)
	d := NewDispatcher(conn, registry, bus)

	go conn.Start()
	defer conn.Close()

	d.HandleClientMessage("connect", json.RawMessage(`{"id":"abc","channel":"test","pong":true}`))

	// `connected` envelope should be queued on the connection
	require.Eventually(t, func() bool { return fc.writeCount() >= 1 }, time.Second, 10*time.Millisecond)
	fc.mu.Lock()
	wrote := append([]byte(nil), fc.writes[0]...)
	fc.mu.Unlock()
	var env map[string]any
	require.NoError(t, json.Unmarshal(wrote, &env))
	assert.Equal(t, "connected", env["type"])
	body := env["body"].(map[string]any)
	assert.Equal(t, "abc", body["id"])
}

func TestDispatcher_ConnectNoPong_NoAck(t *testing.T) {
	bus := newStubBus()
	registry := NewRegistry()
	registry.Register("test", func(ctx ChannelContext) Channel { return &fakeChannel{ctx: ctx} })

	fc := newFakeConn()
	conn := NewConnection("c1", &model.User{ID: "alice"}, fc)
	d := NewDispatcher(conn, registry, bus)

	go conn.Start()
	defer conn.Close()

	d.HandleClientMessage("connect", json.RawMessage(`{"id":"abc","channel":"test"}`))
	// No `connected` envelope when pong is absent
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, 0, fc.writeCount())
}

// --- ShouldShare ---

type shareableFakeChannel struct {
	fakeChannel
}

func (s *shareableFakeChannel) ShouldShare() bool { return true }

func TestDispatcher_ShareableChannel_DuplicateIgnored(t *testing.T) {
	bus := newStubBus()
	registry := NewRegistry()
	registry.Register("shared", func(ctx ChannelContext) Channel {
		return &shareableFakeChannel{fakeChannel: fakeChannel{ctx: ctx}}
	})

	conn := NewConnection("c1", &model.User{ID: "alice"}, newFakeConn())
	d := NewDispatcher(conn, registry, bus)

	d.HandleClientMessage("connect", json.RawMessage(`{"id":"a","channel":"shared"}`))
	d.HandleClientMessage("connect", json.RawMessage(`{"id":"b","channel":"shared"}`))

	// 1回目の接続でtopic-aがsubscribeされ、2回目はスキップされるので
	// subscribe回数は1のはず
	bus.mu.Lock()
	defer bus.mu.Unlock()
	count := 0
	for _, t := range bus.subscribed {
		if t == "topic-a" {
			count++
		}
	}
	assert.Equal(t, 1, count)
}

func TestDispatcher_NonShareableChannel_AllowsMultiple(t *testing.T) {
	bus := newStubBus()
	registry := NewRegistry()
	registry.Register("test", func(ctx ChannelContext) Channel { return &fakeChannel{ctx: ctx} })

	conn := NewConnection("c1", &model.User{ID: "alice"}, newFakeConn())
	d := NewDispatcher(conn, registry, bus)

	d.HandleClientMessage("connect", json.RawMessage(`{"id":"a","channel":"test"}`))
	d.HandleClientMessage("connect", json.RawMessage(`{"id":"b","channel":"test"}`))

	// non-shareable なら2つのエントリが作られる
	d.mu.RLock()
	defer d.mu.RUnlock()
	assert.Len(t, d.channels, 2)
}

// --- OAuth2 scope check (PermittedChannel) ---

type permittedFakeChannel struct {
	fakeChannel
	kind string
}

func (p *permittedFakeChannel) RequiredPermission() string { return p.kind }

func TestConnection_Permissions_HasPermission(t *testing.T) {
	conn := NewConnection("c1", &model.User{ID: "alice"}, newFakeConn())
	conn.SetPermissions([]string{"read:account", "write:notes"})

	assert.Equal(t, []string{"read:account", "write:notes"}, conn.Permissions())
	assert.True(t, conn.HasPermission("read:account"))
	assert.False(t, conn.HasPermission("read:drive"))
	// 空文字列は常に true
	assert.True(t, conn.HasPermission(""))
}

func TestConnection_HasPermission_NilMeansUnrestricted(t *testing.T) {
	// session/cookie 認証では SetPermissions が呼ばれず nil のまま。
	// この場合はトークンスコープによる制限がないため、全ての kind で true。
	conn := NewConnection("c1", &model.User{ID: "alice"}, newFakeConn())
	assert.True(t, conn.HasPermission("read:account"))
	assert.True(t, conn.HasPermission("write:admin"))
}

func TestConnection_HasPermission_EmptySliceDenies(t *testing.T) {
	// 明示的に空スライスをセット → スコープなしトークン → 全 kind で false
	conn := NewConnection("c1", &model.User{ID: "alice"}, newFakeConn())
	conn.SetPermissions([]string{})
	assert.False(t, conn.HasPermission("read:account"))
	// 空 kind だけは常に true
	assert.True(t, conn.HasPermission(""))
}

func TestDispatcher_PermittedChannel_Granted(t *testing.T) {
	bus := newStubBus()
	registry := NewRegistry()
	registry.Register("permitted", func(ctx ChannelContext) Channel {
		return &permittedFakeChannel{fakeChannel: fakeChannel{ctx: ctx}, kind: "read:account"}
	})

	conn := NewConnection("c1", &model.User{ID: "alice"}, newFakeConn())
	conn.SetPermissions([]string{"read:account"})
	d := NewDispatcher(conn, registry, bus)

	d.HandleClientMessage("connect", json.RawMessage(`{"id":"a","channel":"permitted"}`))

	d.mu.RLock()
	defer d.mu.RUnlock()
	assert.Len(t, d.channels, 1)
}

func TestDispatcher_PermittedChannel_Denied(t *testing.T) {
	bus := newStubBus()
	registry := NewRegistry()
	registry.Register("permitted", func(ctx ChannelContext) Channel {
		return &permittedFakeChannel{fakeChannel: fakeChannel{ctx: ctx}, kind: "read:account"}
	})

	conn := NewConnection("c1", &model.User{ID: "alice"}, newFakeConn())
	conn.SetPermissions([]string{"write:notes"})
	d := NewDispatcher(conn, registry, bus)

	d.HandleClientMessage("connect", json.RawMessage(`{"id":"a","channel":"permitted"}`))

	d.mu.RLock()
	defer d.mu.RUnlock()
	assert.Len(t, d.channels, 0)
}

func TestDispatcher_PermittedChannel_SessionAuthAllowed(t *testing.T) {
	bus := newStubBus()
	registry := NewRegistry()
	registry.Register("permitted", func(ctx ChannelContext) Channel {
		return &permittedFakeChannel{fakeChannel: fakeChannel{ctx: ctx}, kind: "read:account"}
	})

	// permissions を set しない (session/cookie 認証相当) → フルアクセス扱い
	conn := NewConnection("c1", &model.User{ID: "alice"}, newFakeConn())
	d := NewDispatcher(conn, registry, bus)

	d.HandleClientMessage("connect", json.RawMessage(`{"id":"a","channel":"permitted"}`))

	d.mu.RLock()
	defer d.mu.RUnlock()
	assert.Len(t, d.channels, 1)
}

func TestDispatcher_PermittedChannel_EmptyPermissionsDenied(t *testing.T) {
	bus := newStubBus()
	registry := NewRegistry()
	registry.Register("permitted", func(ctx ChannelContext) Channel {
		return &permittedFakeChannel{fakeChannel: fakeChannel{ctx: ctx}, kind: "read:account"}
	})

	// 明示的に空スライスをセット (スコープなしトークン) → 拒否
	conn := NewConnection("c1", &model.User{ID: "alice"}, newFakeConn())
	conn.SetPermissions([]string{})
	d := NewDispatcher(conn, registry, bus)

	d.HandleClientMessage("connect", json.RawMessage(`{"id":"a","channel":"permitted"}`))

	d.mu.RLock()
	defer d.mu.RUnlock()
	assert.Len(t, d.channels, 0)
}

func TestDispatcher_PermittedChannel_EmptyKindAllowed(t *testing.T) {
	bus := newStubBus()
	registry := NewRegistry()
	registry.Register("optional", func(ctx ChannelContext) Channel {
		return &permittedFakeChannel{fakeChannel: fakeChannel{ctx: ctx}, kind: ""}
	})

	conn := NewConnection("c1", &model.User{ID: "alice"}, newFakeConn())
	d := NewDispatcher(conn, registry, bus)

	d.HandleClientMessage("connect", json.RawMessage(`{"id":"a","channel":"optional"}`))

	d.mu.RLock()
	defer d.mu.RUnlock()
	assert.Len(t, d.channels, 1)
}

func TestDispatcher_NonPermittedChannel_AlwaysAllowed(t *testing.T) {
	bus := newStubBus()
	registry := NewRegistry()
	registry.Register("plain", func(ctx ChannelContext) Channel { return &fakeChannel{ctx: ctx} })

	// PermittedChannel を実装していないチャンネルは permission なくても接続可能
	conn := NewConnection("c1", &model.User{ID: "alice"}, newFakeConn())
	d := NewDispatcher(conn, registry, bus)

	d.HandleClientMessage("connect", json.RawMessage(`{"id":"a","channel":"plain"}`))

	d.mu.RLock()
	defer d.mu.RUnlock()
	assert.Len(t, d.channels, 1)
}

func TestDispatcher_ShareablePongAck(t *testing.T) {
	bus := newStubBus()
	registry := NewRegistry()
	registry.Register("shared", func(ctx ChannelContext) Channel {
		return &shareableFakeChannel{fakeChannel: fakeChannel{ctx: ctx}}
	})

	fc := newFakeConn()
	conn := NewConnection("c1", &model.User{ID: "alice"}, fc)
	d := NewDispatcher(conn, registry, bus)

	go conn.Start()
	defer conn.Close()

	// 1回目: 作成される
	d.HandleClientMessage("connect", json.RawMessage(`{"id":"a","channel":"shared","pong":true}`))
	// 2回目: 既存共有なのでエントリは作られないが、pongはACKされる
	d.HandleClientMessage("connect", json.RawMessage(`{"id":"b","channel":"shared","pong":true}`))

	require.Eventually(t, func() bool { return fc.writeCount() >= 2 }, time.Second, 10*time.Millisecond)
}

// --- Init error path ---

func TestDispatcher_InitError_RollsBackChannel(t *testing.T) {
	bus := newStubBus()
	registry := NewRegistry()
	registry.Register("test", func(ctx ChannelContext) Channel {
		return &fakeChannel{ctx: ctx, initErr: ErrInvalidParams}
	})

	conn := NewConnection("c1", &model.User{ID: "alice"}, newFakeConn())
	d := NewDispatcher(conn, registry, bus)

	d.HandleClientMessage("connect", json.RawMessage(`{"id":"abc","channel":"test"}`))

	// Init が error を返したら channel は登録されない
	d.mu.RLock()
	defer d.mu.RUnlock()
	assert.Len(t, d.channels, 0)
}

func TestDispatcher_InitError_NoPongAck(t *testing.T) {
	bus := newStubBus()
	registry := NewRegistry()
	registry.Register("test", func(ctx ChannelContext) Channel {
		return &fakeChannel{ctx: ctx, initErr: ErrInvalidParams}
	})

	fc := newFakeConn()
	conn := NewConnection("c1", &model.User{ID: "alice"}, fc)
	d := NewDispatcher(conn, registry, bus)

	go conn.Start()
	defer conn.Close()

	// pong=true でも Init error 時は ack を送らない (TS 互換)
	d.HandleClientMessage("connect", json.RawMessage(`{"id":"abc","channel":"test","pong":true}`))
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, 0, fc.writeCount())
}

// initSubscribingChannel is a test channel that Subscribe時に失敗する Init を
// 持つ。Dispatcher が removeChannel で topic refcount を適切に戻すことを
// 確認するための helper。
type initSubscribingChannel struct {
	ctx          ChannelContext
	disposeCount int
}

func (c *initSubscribingChannel) Init(json.RawMessage) error {
	c.ctx.Subscribe("dangling-topic")
	return ErrInvalidParams
}
func (c *initSubscribingChannel) OnRedisEvent([]byte)                     {}
func (c *initSubscribingChannel) OnClientMessage(string, json.RawMessage) {}
func (c *initSubscribingChannel) Dispose()                                { c.disposeCount++ }

func TestDispatcher_InitError_CleansUpSubscriptions(t *testing.T) {
	bus := newStubBus()
	registry := NewRegistry()
	var created *initSubscribingChannel
	registry.Register("test", func(ctx ChannelContext) Channel {
		created = &initSubscribingChannel{ctx: ctx}
		return created
	})

	conn := NewConnection("c1", &model.User{ID: "alice"}, newFakeConn())
	d := NewDispatcher(conn, registry, bus)

	d.HandleClientMessage("connect", json.RawMessage(`{"id":"abc","channel":"test"}`))

	// Init 中に Subscribe した topic が Dispatcher の map から除去され、
	// bus からも Unsubscribe されている必要がある。
	d.mu.RLock()
	_, topicRemains := d.topics["dangling-topic"]
	d.mu.RUnlock()
	assert.False(t, topicRemains)

	bus.mu.Lock()
	defer bus.mu.Unlock()
	assert.Contains(t, bus.unsubs, "dangling-topic")
	// Dispose も呼ばれる
	assert.Equal(t, 1, created.disposeCount)
}

// --- readNotification / subNote / unsubNote tests ---

type stubNotifReader struct {
	called int
	lastID string
}

func (s *stubNotifReader) ReadAll(userID string) error {
	s.called++
	s.lastID = userID
	return nil
}

func TestDispatcher_ReadNotification(t *testing.T) {
	conn := NewConnection("test", &model.User{ID: "alice"}, newFakeConn())
	bus := newStubBus()
	d := NewDispatcher(conn, nil, bus)
	nr := &stubNotifReader{}
	d.SetNotificationReader(nr)

	d.HandleClientMessage("readNotification", nil)
	assert.Equal(t, 1, nr.called)
	assert.Equal(t, "alice", nr.lastID)
}

func TestDispatcher_ReadNotification_NoReader(t *testing.T) {
	conn := NewConnection("test", &model.User{ID: "alice"}, newFakeConn())
	d := NewDispatcher(conn, nil, nil)
	// notifReader未設定でもpanic しない
	d.HandleClientMessage("readNotification", nil)
}

func TestDispatcher_SubNote_UnsubNote(t *testing.T) {
	conn := NewConnection("test", &model.User{ID: "alice"}, newFakeConn())
	bus := newStubBus()
	d := NewDispatcher(conn, nil, bus)

	// subscribe
	d.HandleClientMessage("s", json.RawMessage(`{"id":"n1"}`))
	bus.mu.Lock()
	_, subbed := bus.subs["noteStream:n1"]
	bus.mu.Unlock()
	assert.True(t, subbed)

	// duplicate subscribe: refcount increments, no duplicate bus.Subscribe
	d.HandleClientMessage("subNote", json.RawMessage(`{"id":"n1"}`))
	bus.mu.Lock()
	assert.Equal(t, 1, countOccurrences(bus.subscribed, "noteStream:n1"))
	bus.mu.Unlock()

	// unsubscribe once: refcount decrements but still > 0
	d.HandleClientMessage("un", json.RawMessage(`{"id":"n1"}`))
	bus.mu.Lock()
	_, stillSubbed := bus.subs["noteStream:n1"]
	bus.mu.Unlock()
	assert.True(t, stillSubbed)

	// unsubscribe again: refcount reaches 0, bus.Unsubscribe called
	d.HandleClientMessage("unsubNote", json.RawMessage(`{"id":"n1"}`))
	bus.mu.Lock()
	_, stillSubbed2 := bus.subs["noteStream:n1"]
	bus.mu.Unlock()
	assert.False(t, stillSubbed2)
}

func TestDispatcher_SubNote_InvalidBody(t *testing.T) {
	conn := NewConnection("test", &model.User{ID: "alice"}, newFakeConn())
	d := NewDispatcher(conn, nil, newStubBus())
	// empty body → no panic
	d.HandleClientMessage("s", json.RawMessage(`{}`))
	d.HandleClientMessage("un", json.RawMessage(`{}`))
}

// TS互換性検証: forwardNoteEvent は noteStream:{id} に届いた
// {type, body} envelope を、クライアントに `{type: "noteUpdated",
// body: {id, type, body}}` という外殻で送信する必要がある (TS
// NoteUpdatedEvent discriminated union)。各 subtype (reacted /
// unreacted / deleted / pollVoted) で形状が維持されることを確認する。
func TestDispatcher_ForwardNoteEvent_TSEnvelope(t *testing.T) {
	cases := []struct {
		name   string
		inner  string
		evType string
	}{
		{"reacted", `{"type":"reacted","body":{"reaction":":smile:","userId":"u1"}}`, "reacted"},
		{"unreacted", `{"type":"unreacted","body":{"reaction":":smile:","userId":"u1"}}`, "unreacted"},
		{"deleted", `{"type":"deleted","body":{"deletedAt":"2026-04-19T00:00:00.000Z"}}`, "deleted"},
		{"pollVoted", `{"type":"pollVoted","body":{"choice":1,"userId":"u1"}}`, "pollVoted"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fc := newFakeConn()
			conn := NewConnection("test", &model.User{ID: "alice"}, fc)
			go conn.Start()
			defer conn.Close()
			bus := newStubBus()
			d := NewDispatcher(conn, nil, bus)
			d.HandleClientMessage("s", json.RawMessage(`{"id":"n1"}`))

			bus.deliver("noteStream:n1", []byte(tc.inner))

			require.Eventually(t, func() bool { return fc.writeCount() >= 1 }, time.Second, 5*time.Millisecond)
			fc.mu.Lock()
			writes := append([][]byte(nil), fc.writes...)
			fc.mu.Unlock()
			require.Len(t, writes, 1)

			var outer struct {
				Type string `json:"type"`
				Body struct {
					ID   string          `json:"id"`
					Type string          `json:"type"`
					Body json.RawMessage `json:"body"`
				} `json:"body"`
			}
			require.NoError(t, json.Unmarshal(writes[0], &outer))
			assert.Equal(t, "noteUpdated", outer.Type)
			assert.Equal(t, "n1", outer.Body.ID)
			assert.Equal(t, tc.evType, outer.Body.Type)
			// inner body は json.RawMessage として原形保存される (TS schema を
			// そのまま frontend に渡す)。
			assert.NotEmpty(t, outer.Body.Body)
		})
	}
}

func TestDispatcher_ForwardNoteEvent_InvalidPayload(t *testing.T) {
	fc := newFakeConn()
	conn := NewConnection("test", &model.User{ID: "alice"}, fc)
	go conn.Start()
	defer conn.Close()
	bus := newStubBus()
	d := NewDispatcher(conn, nil, bus)
	d.HandleClientMessage("s", json.RawMessage(`{"id":"n1"}`))

	// invalid JSON / 空 type のどちらも Send しない (早期 return)。
	bus.deliver("noteStream:n1", []byte(`not-json`))
	bus.deliver("noteStream:n1", []byte(`{"type":""}`))

	// すぐには届かない可能性もあるため少し待ってから確認。
	time.Sleep(50 * time.Millisecond)
	fc.mu.Lock()
	n := len(fc.writes)
	fc.mu.Unlock()
	assert.Zero(t, n)
}

func countOccurrences(slice []string, target string) int {
	n := 0
	for _, s := range slice {
		if s == target {
			n++
		}
	}
	return n
}

// #787: channelContext.HardMuteRules forwards from Connection.HardMuteRules.
func TestChannelContext_HardMuteRules(t *testing.T) {
	conn := NewConnection("c1", nil, newFakeConn())
	conn.SetHardMuteRules([]byte(`["foo"]`))
	d := NewDispatcher(conn, nil, newStubBus())
	ctx := &channelContext{dispatcher: d, id: "ch1"}
	if got := ctx.HardMuteRules(); string(got) != `["foo"]` {
		t.Fatalf("HardMuteRules = %q", got)
	}
}

// nil connection (= disconnected) でも panic せず nil を返す。
func TestChannelContext_HardMuteRules_NilConn(t *testing.T) {
	d := &Dispatcher{}
	ctx := &channelContext{dispatcher: d, id: "ch1"}
	if got := ctx.HardMuteRules(); got != nil {
		t.Fatalf("expected nil, got %q", got)
	}
}
