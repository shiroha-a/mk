package channels

import (
	"encoding/json"

	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/stream"
)

// UserListChannel forwards notes from members of a user list.
//
// upstream `user-list.ts` は per-membership の withReplies (= 各 list member
// の MiUserListMembership.withReplies) で reply を gate する設計だが、mk-go
// にはまだ list membership に紐づく withReplies model が無いため、本 channel
// は connect 時の `withReplies` boolean param のみで gate する暫定実装。
//
// #1063 で noteFilter.shouldEmit から reply blanket-drop を撤廃した副作用で、
// 現状は `withReplies=false` でも reply が pass-through する。upstream の
// 「per-member withReplies が default false で reply を drop」semantics より
// 緩い (= 全 member の reply を見せる) degrade だが、旧 mk-go の「list 全体で
// reply を drop」よりは upstream 寄り。完全互換 (= per-member gate) は
// `model.UserListMembership.WithReplies` 追加と Init での map snapshot
// 取得を伴うので別 issue で扱う想定。
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
