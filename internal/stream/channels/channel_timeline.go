package channels

import (
	"encoding/json"

	"github.com/shiroha-a/mk/internal/stream"
)

// ChannelTimelineChannel forwards notes posted to a specific channel.
type ChannelTimelineChannel struct {
	ctx    stream.ChannelContext
	topic  string
	filter noteFilter
}

// NewChannelTimeline returns a channel factory for "channel".
func NewChannelTimeline(ctx stream.ChannelContext) stream.Channel {
	return &ChannelTimelineChannel{ctx: ctx}
}

func (c *ChannelTimelineChannel) Init(params json.RawMessage) error {
	var p struct {
		ChannelID string `json:"channelId"`
	}
	if len(params) > 0 {
		_ = json.Unmarshal(params, &p)
	}
	// TS本家のchannel.tsはchannelId欠如時にreturn (init自体は成功) するためerrorは返さない
	if p.ChannelID == "" {
		return nil
	}
	c.filter = parseNoteFilter(params)
	c.topic = "channel:" + p.ChannelID
	c.ctx.Subscribe(c.topic)
	return nil
}

func (c *ChannelTimelineChannel) OnRedisEvent(payload []byte) {
	if !c.filter.shouldEmit(payload, c.ctx.HardMuteRules(), viewerIDFromCtx(c.ctx)) {
		return
	}
	payload = hideEmbeds(c.ctx, payload)
	_ = c.ctx.Send("note", json.RawMessage(payload))
}

func (c *ChannelTimelineChannel) OnClientMessage(string, json.RawMessage) {}

func (c *ChannelTimelineChannel) Dispose() {
	if c.topic != "" {
		c.ctx.Unsubscribe(c.topic)
	}
}
