package channels

import (
	"encoding/json"

	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/stream"
)

// AntennaChannel forwards notes captured by a user-defined antenna.
type AntennaChannel struct {
	ctx   stream.ChannelContext
	topic string
}

// NewAntenna returns a channel factory for "antenna".
func NewAntenna(ctx stream.ChannelContext) stream.Channel {
	return &AntennaChannel{ctx: ctx}
}

func (c *AntennaChannel) Init(params json.RawMessage) error {
	// 認証必須
	user, ok := c.ctx.User().(*model.User)
	if !ok || user == nil {
		return stream.ErrInvalidParams
	}
	var p struct {
		AntennaID string `json:"antennaId"`
	}
	if len(params) > 0 {
		_ = json.Unmarshal(params, &p)
	}
	if p.AntennaID == "" {
		return stream.ErrInvalidParams
	}
	c.topic = "antennaTimeline:" + p.AntennaID
	c.ctx.Subscribe(c.topic)
	return nil
}

func (c *AntennaChannel) OnRedisEvent(payload []byte) {
	payload = hideEmbeds(c.ctx, payload)
	_ = c.ctx.Send("note", json.RawMessage(payload))
}

func (c *AntennaChannel) OnClientMessage(string, json.RawMessage) {}

// RequiredPermission implements stream.PermittedChannel.
func (c *AntennaChannel) RequiredPermission() string { return "read:account" }

func (c *AntennaChannel) Dispose() {
	if c.topic != "" {
		c.ctx.Unsubscribe(c.topic)
	}
}
