package entity

// Note: this file intentionally imports `internal/core/notification` for
// the Notification struct definition, which is the only exception to the
// entity→{model,misc}-only convention in this package. Moving the struct
// to `internal/model` would touch 20+ call sites; the direct import keeps
// the packer API ergonomic and is limited to this one file. Callers that
// want to avoid the transitive dependency should pass primitives instead.

import (
	"github.com/shiroha-a/mk/internal/core/notification"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
)

// NotificationItem bundles a notification record with its already-resolved
// notifier user and referenced note. PackNotifications takes a slice of
// these so the caller can pre-fetch users / notes once and the packer can
// build InstanceResolver / EmojiResolver once for the whole batch (#428)。
type NotificationItem struct {
	N    *notification.Notification
	User *model.User
	Note *model.Note
}

// RoleLookup resolves a role ID to its packed representation, returning false
// when the role no longer exists. Used to embed `role` on roleAssigned
// notifications and to drop the notification when the role was deleted
// (upstream NotificationEntityService が role==null で null を返すのと同じ)。
type RoleLookup func(roleID string) (map[string]any, bool)

// ChatInvitationLookup resolves a chat room invitation ID to its packed
// representation for the given viewer, returning false when the invitation no
// longer exists. Used to embed `invitation` on chatRoomInvitationReceived
// notifications and to drop the notification when the invitation was deleted
// (upstream NotificationEntityService が invitation==null で null を返すのと同じ)。
type ChatInvitationLookup func(invitationID, viewerID string) (map[string]any, bool)

// packOptions carries the optional read-time lookups required by notification
// types that embed a related entity which must be packed fresh. Threaded via
// functional options so existing call sites stay unchanged.
type packOptions struct {
	role           RoleLookup
	chatInvitation ChatInvitationLookup
	// viewer は invitation pack の視点 (= notifiee)。chatRoomInvitationReceived
	// の room.isMuted / invitationExists を viewer 視点で算出するために渡す。
	viewer string
}

// NotificationOption configures optional notification packing behavior.
type NotificationOption func(*packOptions)

// WithRoleLookup supplies the RoleLookup used to pack roleAssigned
// notifications (#1559)。
func WithRoleLookup(fn RoleLookup) NotificationOption {
	return func(o *packOptions) { o.role = fn }
}

// WithChatInvitationLookup supplies the ChatInvitationLookup used to pack
// chatRoomInvitationReceived notifications (#1559)。
func WithChatInvitationLookup(fn ChatInvitationLookup) NotificationOption {
	return func(o *packOptions) { o.chatInvitation = fn }
}

// WithViewer sets the viewer (= notifiee) used by viewer-dependent embeds such
// as the chat invitation's room flags (#1559)。
func WithViewer(viewerID string) NotificationOption {
	return func(o *packOptions) { o.viewer = viewerID }
}

func buildPackOptions(opts []NotificationOption) *packOptions {
	o := &packOptions{}
	for _, fn := range opts {
		if fn != nil {
			fn(o)
		}
	}
	return o
}

// PackNotification converts a Notification record into the map shape
// emitted over the `main` WebSocket channel and returned by
// /api/i/notifications. Matches Misskey's NotificationEntityService.pack
// output: id / createdAt / type / userId / user (PackUserLite) / note
// (PackNote) / reaction / choice. Optional user/note arguments are only
// embedded when non-nil so the caller can pre-fetch once and re-use.
//
// instLookup / emojiLookup を渡すと top-level user と note.user に
// Instance / 絵文字 URL を埋める。nil なら no-op リゾルバとして扱われ、
// 従来互換の挙動になる (#415 notifications page InstanceTicker)。
//
// **Single-item only.** 内部で 1 件用の InstanceResolver / EmojiResolver を
// 都度作るので、ループから呼ぶと N+1 クエリになる。複数件をまとめて pack
// する callsite (例: /api/i/notifications) は PackNotifications を使うこと
// (#428)。
func PackNotification(n *notification.Notification, user *model.User, note *model.Note, idGen id.Generator, instLookup InstanceLookup, emojiLookup EmojiLookup, opts ...NotificationOption) map[string]any {
	if n == nil {
		return nil
	}
	out := PackNotifications([]NotificationItem{{N: n, User: user, Note: note}}, idGen, instLookup, emojiLookup, opts...)
	if len(out) == 0 {
		return nil
	}
	return out[0]
}

// PackNotifications batches PackNotification across a slice of items with a
// single InstanceResolver / EmojiResolver shared across all items. Callers
// must pre-resolve notifier User and target Note (FindByID / FindByIDWithUser)
// per item; the packer is responsible only for resolver-level batching
// (Instance / 絵文字)。
//
// 通知一覧 hot-path (#428) で 1 件ごとに resolver を作っていた N+1 を解消する。
// 20 件ページで Instance lookup × 2 + Emoji lookup × 2 (note 用 + user 用) =
// 約 80 クエリ → 各 1 回に削減される (host / 絵文字名は内部で uniq 化)。
//
// items 内で N == nil の要素は単純に skip する。残りは入力順序を保って返す。
func PackNotifications(items []NotificationItem, idGen id.Generator, instLookup InstanceLookup, emojiLookup EmojiLookup, opts ...NotificationOption) []map[string]any {
	packOpts := buildPackOptions(opts)
	// 全 item の notifier user + 埋め込み note.user / Renote/Reply author を
	// flatten して resolver の入力にする。
	notifierUsers := make([]*model.User, 0, len(items))
	notes := make([]*model.Note, 0, len(items))
	// EmojiResolver は notes ベースで host 別に集約するので、notifier user の
	// User.Emojis を拾うために合成 note shell ({User: u}) を別途 push する
	// (単品ケース packNotificationCore でも同じ shell を経由する形)。
	syntheticUserOnlyNotes := make([]*model.Note, 0, len(items))
	for _, it := range items {
		// it.N == nil の要素は最終的に skip されるので resolver 入力にも
		// 含めない。User / Note だけが詰まった「亡霊」item を渡された場合に
		// 無駄な host / 絵文字 fetch を起こさないための防御。
		if it.N == nil {
			continue
		}
		if it.User != nil {
			notifierUsers = append(notifierUsers, it.User)
			syntheticUserOnlyNotes = append(syntheticUserOnlyNotes, &model.Note{User: it.User})
		}
		if it.Note != nil {
			notes = append(notes, it.Note)
		}
	}
	flat := flattenNotesPlusRelations(notes)
	noteAuthors := CollectNoteAuthors(flat)
	// notifierUsers の backing array は cap=len(items) で確保済み。素朴な
	// append(notifierUsers, ...) だと余剰 cap 内で同じ backing array に書く
	// 可能性があり、後で notifierUsers を再利用したくなった時に aliasing
	// バグになりうる。意識的に新規 backing array を確保しておく。
	allUsers := make([]*model.User, 0, len(notifierUsers)+len(noteAuthors))
	allUsers = append(allUsers, notifierUsers...)
	allUsers = append(allUsers, noteAuthors...)
	instResolver := NewInstanceResolver(instLookup, allUsers...)
	emojiInput := append(append([]*model.Note(nil), flat...), syntheticUserOnlyNotes...)
	emojiResolver := NewEmojiResolver(emojiLookup, emojiInput)

	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
		if packed := packNotificationCore(it.N, it.User, it.Note, idGen, instResolver, emojiResolver, packOpts); packed != nil {
			out = append(out, packed)
		}
	}
	return out
}

// packNotificationCore is the resolver-aware body shared by the single-item
// PackNotification entry point and the batched PackNotifications. Resolvers
// are passed in so the same instance can be re-used across many items.
func packNotificationCore(n *notification.Notification, user *model.User, note *model.Note, idGen id.Generator, instResolver *InstanceResolver, emojiResolver *EmojiResolver, opts *packOptions) map[string]any {
	if n == nil {
		return nil
	}
	const tsFormat = "2006-01-02T15:04:05.000Z"
	out := map[string]any{
		"id":        n.ID,
		"createdAt": n.CreatedAt.UTC().Format(tsFormat),
		"type":      string(n.Type),
	}
	// roleAssigned は Extra["roleId"] を read 時に packed role へ解決する。
	// role 削除済 / lookup 未配線なら通知ごと drop する (TS
	// NotificationEntityService が role==null で null を返すのと同じ挙動)。
	if n.Type == notification.TypeRoleAssigned {
		roleID, _ := n.Extra["roleId"].(string)
		if opts == nil || opts.role == nil {
			return nil
		}
		packed, ok := opts.role(roleID)
		if !ok {
			return nil
		}
		out["role"] = packed
	}
	// chatRoomInvitationReceived は Extra["invitationId"] を read 時に packed
	// invitation へ解決する。削除済 / lookup 未配線なら通知ごと drop する。
	if n.Type == notification.TypeChatRoomInvitationReceived {
		invID, _ := n.Extra["invitationId"].(string)
		if opts == nil || opts.chatInvitation == nil {
			return nil
		}
		packed, ok := opts.chatInvitation(invID, opts.viewer)
		if !ok {
			return nil
		}
		out["invitation"] = packed
	}
	if n.NotifierID != "" {
		// TS本家はnotifierIdを"userId"として返す。
		out["userId"] = n.NotifierID
		if user != nil {
			lite := PackUserLite(user)
			instResolver.FillUserLite(&lite)
			emojiResolver.PopulateUserEmojis(user, &lite)
			out["user"] = lite
		}
	}
	if n.NoteID != "" {
		// reply/mention/reaction/renote/quote/pollEnded等、noteを要する
		// 通知タイプ向け。REST API互換のため noteId は常に含める。
		// 加えて note object が fetch できていればpacked Noteも入れる。
		// applyNoteResolvers で note.user / Renote/Reply の embed にも
		// Instance / 絵文字を適用する。
		out["noteId"] = n.NoteID
		if note != nil && idGen != nil {
			packed := PackNote(note, idGen)
			applyNoteResolvers(note, &packed, instResolver, emojiResolver)
			out["note"] = packed
		}
	}
	if n.Reaction != "" {
		out["reaction"] = n.Reaction
	}
	if n.Choice != nil {
		out["choice"] = *n.Choice
	}
	// extraはTS本家では role/invitation/achievement 等の個別フィールド
	// として展開されるが、Go側はmap[string]anyで透過的にmergeする。
	// core keyと衝突する場合は extra 側をskip (packerの契約を壊さない)。
	for k, v := range n.Extra {
		if _, collides := out[k]; collides {
			continue
		}
		// roleAssigned の roleId / chatRoomInvitationReceived の invitationId は
		// packed entity (out["role"] / out["invitation"]) に解決済なので raw ID を
		// surface しない (TS は packed entity のみ返す)。
		if k == "roleId" || k == "invitationId" {
			continue
		}
		// exportCompleted 通知の exportedEntity は misskey_dart / Misskey TS が
		// singular / camelCase の enum で受ける。作成時に core/transfer 側で正規化
		// 済み (#1249) だが、それ以前に永続化された通知は内部値 ("notes" 等) を
		// 保持しており、misskey_dart の enum decode を全件巻き込んで落とす (#1391)。
		// read 時にも正規化して既存データでクライアントが crash しないようにする。
		if k == "exportedEntity" {
			if s, ok := v.(string); ok {
				out[k] = normalizeExportedEntity(s)
				continue
			}
		}
		out[k] = v
	}
	return out
}

// exportedEntityAliases maps mk-go 内部の export type 名 (plural / kebab-case) を
// exportCompleted 通知の exportedEntity が取るべき Misskey enum 値 (singular /
// camelCase) へ変換する。core/transfer.misskeyExportedEntity (create 時) と同じ
// 対応で、read 時の defense-in-depth として持つ。
var exportedEntityAliases = map[string]string{
	"notes":         "note",
	"favorites":     "favorite",
	"user-lists":    "userList",
	"antennas":      "antenna",
	"clips":         "clip",
	"mute":          "muting",
	"custom-emojis": "customEmoji",
}

// normalizeExportedEntity returns the Misskey enum value for an exportedEntity,
// translating mk-go internal aliases. Already-canonical / unknown values pass
// through unchanged (= following / blocking は元から一致、未知値は壊さない)。
func normalizeExportedEntity(v string) string {
	if canon, ok := exportedEntityAliases[v]; ok {
		return canon
	}
	return v
}
