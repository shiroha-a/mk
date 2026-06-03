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
	viewerID := viewerIDFromCtx(c.ctx)
	if !c.filter.shouldEmit(payload, c.ctx.HardMuteRules(), viewerID) {
		return
	}
	// followers visibility note の defense-in-depth filter (#1465)。fanout 段
	// で list owner の follow 関係を check してから push する設計だが、過去に
	// stream へ滞留した entry や fanout の設定ミスに対して WS 側でも 1 段
	// gate する。本人 (`viewerID == note.userId`) は CanSeeNote の semantics
	// と同じく無条件 pass、それ以外は FollowingSnapshot で `note.userId` を
	// follow しているか確認する。snapshot が nil (= anonymous は Init で
	// 弾く設計なので来ない / lookup 未配線) の場合は fail-closed で drop する。
	// public / home / specified は fanout 段で適切に gate 済 (specified は
	// shouldFanoutToFollowers が除外) のためここでは何もしない。
	if !userListVisibilityShouldEmit(payload, viewerID, c.ctx.FollowingSnapshot()) {
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

// userListVisibilityPayload は userList channel の visibility gate が
// 参照する最小 fields。`visibility` と `userId` (author) があれば判定できる。
type userListVisibilityPayload struct {
	UserID     string `json:"userId"`
	Visibility string `json:"visibility"`
}

// userListVisibilityShouldEmit returns false when a `followers` visibility note
// is being delivered to a viewer who does not follow the author (and is not the
// author themselves). For `public` / `home` / `specified` payloads it returns
// true unconditionally — fanout already filters those (specified is excluded by
// shouldFanoutToFollowers, public / home reach every list member by design).
//
// Defensive behaviour:
//   - payload parse 失敗 → conservative に drop (= 既存 reply gate と逆の方針
//     だが、visibility 不明 note は IDOR fail-closed が安全)。
//   - snapshot=nil + viewer ≠ author → drop (anonymous は Init で弾くが、
//     followingSnapshot lookup が未配線 / 取得失敗で nil 返却の場合に備える)。
func userListVisibilityShouldEmit(payload []byte, viewerID string, snap map[string]bool) bool {
	var note userListVisibilityPayload
	if err := json.Unmarshal(payload, &note); err != nil {
		return false
	}
	if note.Visibility != "followers" {
		return true
	}
	if viewerID == "" {
		return false
	}
	if note.UserID == viewerID {
		return true
	}
	if snap == nil {
		return false
	}
	_, follows := snap[note.UserID]
	return follows
}
