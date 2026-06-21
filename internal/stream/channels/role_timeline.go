package channels

import (
	"encoding/json"

	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/stream"
)

// RoleExplorableChecker reports whether a role's timeline is publicly
// streamable (isExplorable). Implemented by core/role.Service.
type RoleExplorableChecker interface {
	IsExplorable(roleID string) bool
}

// RoleTimelineFactory builds RoleTimelineChannels carrying an isExplorable
// checker so OnRedisEvent can gate non-explorable roles (#1549).
type RoleTimelineFactory struct {
	explorable RoleExplorableChecker
}

// NewRoleTimelineFactory constructs a RoleTimelineFactory.
func NewRoleTimelineFactory(explorable RoleExplorableChecker) *RoleTimelineFactory {
	return &RoleTimelineFactory{explorable: explorable}
}

// New implements stream.ChannelFactory.
func (f *RoleTimelineFactory) New(ctx stream.ChannelContext) stream.Channel {
	return &RoleTimelineChannel{ctx: ctx, explorable: f.explorable}
}

// RoleTimelineChannel forwards notes from users with a specific role.
type RoleTimelineChannel struct {
	ctx        stream.ChannelContext
	explorable RoleExplorableChecker
	topic      string
	roleID     string
	filter     noteFilter
}

func (c *RoleTimelineChannel) Init(params json.RawMessage) error {
	var p struct {
		RoleID string `json:"roleId"`
	}
	if len(params) > 0 {
		_ = json.Unmarshal(params, &p)
	}
	// TS本家のrole-timeline.tsはroleId欠如時にreturn (init自体は成功) するためerrorは返さない
	if p.RoleID == "" {
		return nil
	}
	c.filter = parseNoteFilter(params)
	c.roleID = p.RoleID
	c.topic = "roleTimeline:" + p.RoleID
	c.ctx.Subscribe(c.topic)
	return nil
}

func (c *RoleTimelineChannel) OnRedisEvent(payload []byte) {
	// 本家 role-timeline.ts: isExplorable role かつ visibility==public のみ emit。
	// isExplorable は runtime 可変なので per-event で check する (publish 側では
	// gate しない)。checker 未配線は fail-closed。
	if c.explorable == nil || !c.explorable.IsExplorable(c.roleID) {
		return
	}
	if noteVisibility(payload) != string(model.NoteVisibilityPublic) {
		return
	}
	// anon viewer + 著者 requireSigninToViewContents は note を丸ごと drop する
	// (upstream role-timeline.ts:53-55 の note/renote/reply 3 連 gate、channel /
	// hashtag と同じ。role-timeline にだけ移植が漏れていた、#1780)。
	if anonRequireSigninDrop(payload, viewerIDFromCtx(c.ctx)) {
		return
	}
	if !c.filter.shouldEmit(payload, c.ctx.HardMuteRules(), viewerIDFromCtx(c.ctx)) {
		return
	}
	if noteMutedOrBlocked(payload, c.ctx.MuteBlockSnapshot()) {
		return
	}
	payload = hideEmbeds(c.ctx, payload)
	// pure renote の renote.myReaction を viewer 毎に inject (#2058)。
	payload = injectRenoteMyReaction(payload, viewerIDFromCtx(c.ctx))
	_ = c.ctx.Send("note", json.RawMessage(payload))
}

func (c *RoleTimelineChannel) OnClientMessage(string, json.RawMessage) {}

func (c *RoleTimelineChannel) Dispose() {
	if c.topic != "" {
		c.ctx.Unsubscribe(c.topic)
	}
}
