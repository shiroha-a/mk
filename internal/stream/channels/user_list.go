package channels

import (
	"encoding/json"

	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/stream"
)

// UserListChannel forwards notes from members of a user list.
type UserListChannel struct {
	ctx    stream.ChannelContext
	topic  string
	filter noteFilter
}

// NewUserList returns a channel factory for "userList".
func NewUserList(ctx stream.ChannelContext) stream.Channel {
	return &UserListChannel{ctx: ctx}
}

func (c *UserListChannel) Init(params json.RawMessage) error {
	// TS Misskey: 認証欠如もlistId欠如もinitでfalseを返す
	user, ok := c.ctx.User().(*model.User)
	if !ok || user == nil {
		return stream.ErrInvalidParams
	}
	var p struct {
		ListID      string `json:"listId"`
		WithReplies *bool  `json:"withReplies"`
	}
	if len(params) > 0 {
		_ = json.Unmarshal(params, &p)
	}
	if p.ListID == "" {
		return stream.ErrInvalidParams
	}
	c.filter = parseNoteFilter(params)
	// withRepliesパラメータがtrueなら上書き
	if p.WithReplies != nil && *p.WithReplies {
		c.filter.WithReplies = true
	}
	c.topic = "userListTimeline:" + p.ListID
	c.ctx.Subscribe(c.topic)
	return nil
}

func (c *UserListChannel) OnRedisEvent(payload []byte) {
	if !c.filter.shouldEmit(payload, c.ctx.HardMuteRules(), viewerIDFromCtx(c.ctx)) {
		return
	}
	_ = c.ctx.Send("note", json.RawMessage(payload))
}

func (c *UserListChannel) OnClientMessage(string, json.RawMessage) {}

func (c *UserListChannel) Dispose() {
	if c.topic != "" {
		c.ctx.Unsubscribe(c.topic)
	}
}
