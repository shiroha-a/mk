package channels

import (
	"encoding/json"
	"time"

	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/stream"
)

// NotificationsChannel forwards notifications addressed to the authenticated
// user. Anonymous connections subscribe to nothing (the channel exists but
// stays silent).
type NotificationsChannel struct {
	ctx   stream.ChannelContext
	topic string
}

// NewNotifications returns a channel factory for "notifications".
func NewNotifications(ctx stream.ChannelContext) stream.Channel {
	return &NotificationsChannel{ctx: ctx}
}

func (c *NotificationsChannel) Init(_ json.RawMessage) error {
	user, ok := c.ctx.User().(*model.User)
	if !ok || user == nil {
		return stream.ErrInvalidParams
	}
	c.topic = "notifications:" + user.ID
	c.ctx.Subscribe(c.topic)
	return nil
}

func (c *NotificationsChannel) OnRedisEvent(payload []byte) {
	payload = hideNotificationNote(c.ctx, payload)
	_ = c.ctx.Send("notification", json.RawMessage(payload))
}

func (c *NotificationsChannel) OnClientMessage(string, json.RawMessage) {}

// ShouldShare implements stream.ShareableChannel.
func (c *NotificationsChannel) ShouldShare() bool { return true }

// RequiredPermission implements stream.PermittedChannel.
func (c *NotificationsChannel) RequiredPermission() string { return "read:account" }

func (c *NotificationsChannel) Dispose() {
	if c.topic != "" {
		c.ctx.Unsubscribe(c.topic)
	}
}

// MainChannel is the catch-all per-user channel. It currently forwards both
// notifications and follow events that target the authenticated user. Each
// inbound payload arrives with a hint type so the client can route it.
//
// Misskey 本家の main は notification / mention / unreadMessagingMessage /
// follow / unfollow / followed / receiveFollowRequest / readAllNotifications
// など多種のイベントを受信する。Phase 4.1 では notification と follow event
// に絞る。
type MainChannel struct {
	ctx     stream.ChannelContext
	notif   string
	mainTop string
}

// NewMain returns a channel factory for "main".
func NewMain(ctx stream.ChannelContext) stream.Channel {
	return &MainChannel{ctx: ctx}
}

func (c *MainChannel) Init(_ json.RawMessage) error {
	// TS本家のmain.ts: if (!this.user) return false;
	user, ok := c.ctx.User().(*model.User)
	if !ok || user == nil {
		return stream.ErrInvalidParams
	}
	c.notif = "notifications:" + user.ID
	c.mainTop = "main:" + user.ID
	c.ctx.Subscribe(c.notif)
	c.ctx.Subscribe(c.mainTop)
	return nil
}

// OnRedisEvent forwards a payload to the appropriate frontend event:
//
//   - main:<userID> topic carries a `{type, body}` envelope from
//     MainStreamPublisher (e.g. unreadNotification / readAllNotifications /
//     follow / meUpdated etc). Forward as the wrapped type.
//   - notifications:<userID> topic carries a bare packed Notification body
//     ({id, type:"mention"|"follow"|..., createdAt, ...}). Forward as
//     "notification" so the frontend's `main.on('notification', ...)`
//     listeners (timeline live update) fire.
//
// 旧実装は payload 全体に対して `type` だけを見て分岐していたため、bare
// 通知 body 内の `type:"mention"` を envelope の type として誤って扱い、
// 存在しない `mention` イベントを送出して通知 timeline のライブ更新を
// 落としていた (#420 follow-up)。`type` と `body` の両方が揃っている時だけ
// envelope として扱うことで誤検出を防ぐ。
func (c *MainChannel) OnRedisEvent(payload []byte) {
	var env struct {
		Type string          `json:"type"`
		Body json.RawMessage `json:"body"`
	}
	if err := json.Unmarshal(payload, &env); err == nil && env.Type != "" && len(env.Body) > 0 {
		// maybeRefreshFollowing は元 body (follow/unfollow の user) を見るので
		// note-hide ゲートより前に元の body で実行する。
		c.maybeRefreshFollowing(env.Type, env.Body)
		body := env.Body
		// reply/renote/mention envelope の body は packed note。viewer 可視性で
		// top-level 著者設定 + depth-2 embed を hide する (#1568)。
		if isNoteEnvelope(env.Type) {
			body = json.RawMessage(hideEmbedsForViewer(env.Body, viewerUserFromCtx(c.ctx), c.ctx.FollowingSnapshot(), time.Now().UnixMilli()))
		}
		_ = c.ctx.Send(env.Type, body)
		return
	}
	payload = hideNotificationNote(c.ctx, payload)
	_ = c.ctx.Send("notification", json.RawMessage(payload))
}

// hideNotificationNote applies the per-viewer hideNote gate to the note embedded
// in a bare notification payload (notifications: topic body): the embedded note
// gets the top-level author-pref gate + its depth-2 renote/reply embed gate, the
// rest of the notification envelope is preserved. Returns the original payload
// verbatim when there is no embedded note or nothing is hideable (#1568).
func hideNotificationNote(ctx stream.ChannelContext, payload []byte) []byte {
	var probe struct {
		Note json.RawMessage `json:"note"`
	}
	if err := json.Unmarshal(payload, &probe); err != nil {
		return payload
	}
	if len(probe.Note) == 0 || string(probe.Note) == "null" {
		return payload
	}
	gated, changed := hideNoteRawForViewer(probe.Note, viewerUserFromCtx(ctx), ctx.FollowingSnapshot(), time.Now().UnixMilli())
	if !changed {
		return payload
	}
	var m map[string]any
	if err := json.Unmarshal(payload, &m); err != nil {
		return payload
	}
	m["note"] = json.RawMessage(gated)
	out, err := json.Marshal(m)
	if err != nil {
		return payload
	}
	return out
}

// isNoteEnvelope reports whether a main-stream {type,body} envelope carries a
// packed note as its body. publishNoteMainEvents emits exactly reply / renote /
// mention with body = packed NoteEntity; quote-type notifications flow via the
// bare notifications: topic instead (covered by hideNotificationNote).
func isNoteEnvelope(eventType string) bool {
	switch eventType {
	case "reply", "renote", "mention":
		return true
	default:
		return false
	}
}

// followingSnapshotUpdater is the optional capability a ChannelContext may
// expose to mutate the connection's followee snapshot live. The main channel
// uses it to keep timeline reply gating fresh after the viewer follows or
// unfollows someone, without waiting for a reconnect (#1211).
type followingSnapshotUpdater interface {
	UpdateFollowingSnapshot(followeeID string, following bool)
}

// maybeRefreshFollowing updates the connection's followee snapshot when the
// viewer's own follow list changes. The `follow` / `unfollow` events are
// emitted to the actor's own `main` stream by the following service, so the
// body is the followee user object; only its id is needed. `followed` (someone
// followed me) does not change my follow list and is intentionally ignored.
func (c *MainChannel) maybeRefreshFollowing(eventType string, body json.RawMessage) {
	if eventType != "follow" && eventType != "unfollow" {
		return
	}
	updater, ok := c.ctx.(followingSnapshotUpdater)
	if !ok {
		return
	}
	var u struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(body, &u) != nil || u.ID == "" {
		return
	}
	updater.UpdateFollowingSnapshot(u.ID, eventType == "follow")
}

func (c *MainChannel) OnClientMessage(string, json.RawMessage) {}

// ShouldShare implements stream.ShareableChannel.
func (c *MainChannel) ShouldShare() bool { return true }

// RequiredPermission implements stream.PermittedChannel.
func (c *MainChannel) RequiredPermission() string { return "read:account" }

func (c *MainChannel) Dispose() {
	if c.notif != "" {
		c.ctx.Unsubscribe(c.notif)
	}
	if c.mainTop != "" {
		c.ctx.Unsubscribe(c.mainTop)
	}
}
