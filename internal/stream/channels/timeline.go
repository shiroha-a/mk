// Package channels contains the per-name Channel implementations registered
// with stream.Registry. 各 Channel は決まった Redis topic を購読し、受信した
// payload を `note` envelope としてクライアントに転送する。
package channels

import (
	"encoding/json"

	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/stream"
)

// LocalTimelineChannel forwards local-public/home notes.
type LocalTimelineChannel struct {
	ctx    stream.ChannelContext
	filter noteFilter
}

// NewLocalTimeline は ChannelFactory の形をした生成関数。Registry に
// "localTimeline" として登録する。
func NewLocalTimeline(ctx stream.ChannelContext) stream.Channel {
	return &LocalTimelineChannel{ctx: ctx}
}

// Init subscribes to the local-timeline pubsub topic.
func (c *LocalTimelineChannel) Init(params json.RawMessage) error {
	c.filter = parseNoteFilter(params)
	c.ctx.Subscribe("localTimeline")
	return nil
}

// OnRedisEvent forwards a JSON-encoded note payload as a `note` event.
func (c *LocalTimelineChannel) OnRedisEvent(payload []byte) {
	if !c.filter.shouldEmit(payload, c.ctx.HardMuteRules(), viewerIDFromCtx(c.ctx)) {
		return
	}
	_ = c.ctx.Send("note", json.RawMessage(payload))
}

// OnClientMessage is unused for the local timeline channel.
func (c *LocalTimelineChannel) OnClientMessage(string, json.RawMessage) {}

// Dispose unsubscribes from the topic.
func (c *LocalTimelineChannel) Dispose() {
	c.ctx.Unsubscribe("localTimeline")
}

// GlobalTimelineChannel forwards every public note across the federation.
type GlobalTimelineChannel struct {
	ctx    stream.ChannelContext
	filter noteFilter
}

// NewGlobalTimeline returns a channel factory for "globalTimeline".
func NewGlobalTimeline(ctx stream.ChannelContext) stream.Channel {
	return &GlobalTimelineChannel{ctx: ctx}
}

func (c *GlobalTimelineChannel) Init(params json.RawMessage) error {
	c.filter = parseNoteFilter(params)
	c.ctx.Subscribe("globalTimeline")
	return nil
}
func (c *GlobalTimelineChannel) OnRedisEvent(payload []byte) {
	if !c.filter.shouldEmit(payload, c.ctx.HardMuteRules(), viewerIDFromCtx(c.ctx)) {
		return
	}
	_ = c.ctx.Send("note", json.RawMessage(payload))
}
func (c *GlobalTimelineChannel) OnClientMessage(string, json.RawMessage) {}
func (c *GlobalTimelineChannel) Dispose() {
	c.ctx.Unsubscribe("globalTimeline")
}

// HomeTimelineChannel forwards notes addressed to the authenticated user. 認証
// 済みでない場合は subscribe しない (channel は接続するだけで何も送らない)。
type HomeTimelineChannel struct {
	ctx    stream.ChannelContext
	topic  string
	filter noteFilter
}

// NewHomeTimeline returns a channel factory for "homeTimeline".
func NewHomeTimeline(ctx stream.ChannelContext) stream.Channel {
	return &HomeTimelineChannel{ctx: ctx}
}

func (c *HomeTimelineChannel) Init(params json.RawMessage) error {
	c.filter = parseNoteFilter(params)
	user, ok := c.ctx.User().(*model.User)
	if !ok || user == nil {
		// TS本家はrequireCredential=trueでConnection側で弾くが、
		// Go側は現状 silent no-op としている。Phase 3ではシグネチャ変更に
		// とどめ、認証拒否はPermittedChannel等別系で扱う
		return nil
	}
	c.topic = "homeTimeline:" + user.ID
	c.ctx.Subscribe(c.topic)
	return nil
}

func (c *HomeTimelineChannel) OnRedisEvent(payload []byte) {
	if !c.filter.shouldEmit(payload, c.ctx.HardMuteRules(), viewerIDFromCtx(c.ctx)) {
		return
	}
	_ = c.ctx.Send("note", json.RawMessage(payload))
}

func (c *HomeTimelineChannel) OnClientMessage(string, json.RawMessage) {}

// RequiredPermission implements stream.PermittedChannel.
func (c *HomeTimelineChannel) RequiredPermission() string { return "read:account" }

func (c *HomeTimelineChannel) Dispose() {
	if c.topic != "" {
		c.ctx.Unsubscribe(c.topic)
	}
}

// HybridTimelineChannel combines homeTimeline (for the authenticated user) and
// localTimeline (for everyone). 通常の Misskey クライアントが使う "ソーシャル
// タイムライン" がこれに相当する。
type HybridTimelineChannel struct {
	ctx       stream.ChannelContext
	homeTopic string
	filter    noteFilter
}

// NewHybridTimeline returns a channel factory for "hybridTimeline".
func NewHybridTimeline(ctx stream.ChannelContext) stream.Channel {
	return &HybridTimelineChannel{ctx: ctx}
}

func (c *HybridTimelineChannel) Init(params json.RawMessage) error {
	c.filter = parseNoteFilter(params)
	c.ctx.Subscribe("localTimeline")
	if user, ok := c.ctx.User().(*model.User); ok && user != nil {
		c.homeTopic = "homeTimeline:" + user.ID
		c.ctx.Subscribe(c.homeTopic)
	}
	return nil
}

func (c *HybridTimelineChannel) OnRedisEvent(payload []byte) {
	if !c.filter.shouldEmit(payload, c.ctx.HardMuteRules(), viewerIDFromCtx(c.ctx)) {
		return
	}
	_ = c.ctx.Send("note", json.RawMessage(payload))
}

func (c *HybridTimelineChannel) OnClientMessage(string, json.RawMessage) {}

// RequiredPermission implements stream.PermittedChannel.
func (c *HybridTimelineChannel) RequiredPermission() string { return "read:account" }

func (c *HybridTimelineChannel) Dispose() {
	c.ctx.Unsubscribe("localTimeline")
	if c.homeTopic != "" {
		c.ctx.Unsubscribe(c.homeTopic)
	}
}
