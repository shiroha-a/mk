package channels

import (
	"encoding/json"

	"github.com/shiroha-a/mk/internal/stream"
)

// HashtagChannel forwards notes matching a specific hashtag.
type HashtagChannel struct {
	ctx    stream.ChannelContext
	topic  string
	filter noteFilter
}

// NewHashtag returns a channel factory for "hashtag".
func NewHashtag(ctx stream.ChannelContext) stream.Channel {
	return &HashtagChannel{ctx: ctx}
}

func (c *HashtagChannel) Init(params json.RawMessage) error {
	var p struct {
		Q [][]string `json:"q"`
	}
	if len(params) > 0 {
		_ = json.Unmarshal(params, &p)
	}
	// TS Misskey は q が配列でない/要素がないと init が false を返して接続拒否
	if len(p.Q) == 0 || len(p.Q[0]) == 0 {
		return stream.ErrInvalidParams
	}
	c.filter = parseNoteFilter(params)
	// 最初のタグでトピック購読
	c.topic = "hashtag:" + p.Q[0][0]
	c.ctx.Subscribe(c.topic)
	return nil
}

func (c *HashtagChannel) OnRedisEvent(payload []byte) {
	if !c.filter.shouldEmit(payload, c.ctx.HardMuteRules(), viewerIDFromCtx(c.ctx)) {
		return
	}
	payload = hideEmbeds(c.ctx, payload)
	_ = c.ctx.Send("note", json.RawMessage(payload))
}

func (c *HashtagChannel) OnClientMessage(string, json.RawMessage) {}

func (c *HashtagChannel) Dispose() {
	if c.topic != "" {
		c.ctx.Unsubscribe(c.topic)
	}
}
