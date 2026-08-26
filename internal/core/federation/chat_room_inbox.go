package federation

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	corechat "github.com/shiroha-a/mk/internal/core/chat"
	"github.com/shiroha-a/mk/internal/model"
)

// ChatRoomReceiver wires the chat service's room-federation operations into the
// inbox processor. Implemented by core/chat.Service. Mirrors the CherryPick
// group chat federation handshake (Invite / Accept / Reject of a Group object).
type ChatRoomReceiver interface {
	EnsureRoomViaAP(roomID, name, summary, ownerUserID string) error
	CreateInvitationViaAP(roomID, inviteeUserID string) error
	AddMemberViaAP(roomID, userID string) error
	RemoveInvitationViaAP(roomID, userID string) error
	RemoveMemberViaAP(roomID, userID string) error
	CreateRoomMessageViaAP(uri string, sender *model.User, roomID, text string) error
}

// SetChatRoomReceiver wires the chat room federation receiver for inbound
// Invite / Accept / Reject of chat room Group objects (#1203).
func (p *Processor) SetChatRoomReceiver(r ChatRoomReceiver) {
	p.chatRoomReceiver = r
}

// chatRoomURIRe extracts the room id from a CherryPick chat room URI
// (`https://host/chat/rooms/{id}`). The id is alphanumeric (aidx/ULID).
var chatRoomURIRe = regexp.MustCompile(`/chat/rooms/([a-zA-Z0-9]+)$`)

// chatRoomIDMaxRunes は `chat_room.id` / `chat_room_membership.roomId` /
// `chat_room_invitation.roomId` の varchar(32) (migration/000022_chat.up.sql)。
//
// **相手が自由に決められる値**で、正規表現は長さを縛らない。溢れると
// `CreateRoom` が SQLSTATE 22001 で落ち、呼び出し側が `%w` で包んで返すため
// **その inbox job が retry を使い切って dead になる** (#2726)。room id は行の
// 身元 (PK かつ FK) なので切れない — 収まらなければ room として認識しない。
const chatRoomIDMaxRunes = 32

// extractChatRoomID returns the room id embedded in uri, or "" when uri is not
// a chat room URI or the id cannot be stored.
//
// 正規表現が ASCII 英数字だけを通すので NUL は入らない。長さだけ見る。
func extractChatRoomID(uri string) string {
	m := chatRoomURIRe.FindStringSubmatch(uri)
	if len(m) != 2 {
		return ""
	}
	if !fitsColumn(m[1], chatRoomIDMaxRunes) {
		return ""
	}
	return m[1]
}

// apGroupObject is the AP `Group` object representing a chat room, carried as
// the object of Invite / Accept / Reject activities.
type apGroupObject struct {
	Type         string `json:"type"`
	ID           string `json:"id"`
	Name         string `json:"name"`
	Summary      string `json:"summary"`
	AttributedTo string `json:"attributedTo"`
}

// parseGroupObject decodes a raw AP object and reports whether it is a chat
// room Group object (type=Group with a recognizable /chat/rooms/{id} id).
func parseGroupObject(raw json.RawMessage) (apGroupObject, bool) {
	var g apGroupObject
	if len(raw) == 0 || json.Unmarshal(raw, &g) != nil {
		return apGroupObject{}, false
	}
	if !strings.EqualFold(g.Type, "Group") || extractChatRoomID(g.ID) == "" {
		return apGroupObject{}, false
	}
	return g, true
}

// isChatRoomInvite reports whether an Invite activity carries a chat room
// Group object (as opposed to a reversi Game object).
func (p *Processor) isChatRoomInvite(act genericActivity) bool {
	_, ok := parseGroupObject(act.Object)
	return ok
}

// handleChatRoomInvite processes an inbound Invite whose object is a chat room
// Group: it creates a local copy of the remote room (owned by the inviter) and
// records a pending invitation for the targeted local user. The local user
// must explicitly accept via the chat API; no auto-join happens here.
func (p *Processor) handleChatRoomInvite(act genericActivity) error {
	if p.chatRoomReceiver == nil {
		return ErrUnsupportedActivity
	}
	group, ok := parseGroupObject(act.Object)
	if !ok {
		return ErrUnsupportedActivity
	}
	roomID := extractChatRoomID(group.ID)

	// owner (= inviter) は activity の actor。room copy の owner として保存する。
	owner, err := p.resolver.ResolveActor(act.Actor)
	if err != nil {
		return err
	}

	// target = 招待された local user。Invite の `target` フィールドから読む。
	var env struct {
		Target string `json:"target"`
	}
	_ = json.Unmarshal(act.raw, &env)
	// target 欠落 / invitee が local でないケースは恒久的に解決しないため、
	// retryable error ではなく ErrUnsupportedActivity を返して inbox worker の
	// 無駄な再試行 (retry storm) を防ぐ (#1204 review)。
	if env.Target == "" {
		slog.Warn("chat room invite: missing target", "actor", act.Actor)
		return ErrUnsupportedActivity
	}
	invitee, err := p.resolveTargetUser(env.Target)
	if err != nil || invitee == nil || !invitee.IsLocal() {
		slog.Warn("chat room invite: invitee is not a local user", "target", env.Target)
		return ErrUnsupportedActivity
	}

	if err := p.chatRoomReceiver.EnsureRoomViaAP(roomID, group.Name, group.Summary, owner.ID); err != nil {
		// owner mismatch (= roomId が無関係なローカル room と衝突) は永久に
		// 解決しないので retry させない。それ以外 (DB 一過性エラー等) は
		// retryable のまま伝播させる。
		if errors.Is(err, corechat.ErrRoomOwnerMismatch) {
			slog.Warn("chat room invite: room id collides with an unrelated local room", "roomID", roomID)
			return ErrUnsupportedActivity
		}
		// 列に収まらない room id も恒久的な条件なので retry させない (#2726)。
		// parseGroupObject が先に落とすので通常はここまで来ない。
		if errors.Is(err, corechat.ErrInvalidTarget) {
			slog.Warn("chat room invite: room id cannot be stored", "actor", act.Actor)
			return ErrUnsupportedActivity
		}
		return fmt.Errorf("chat room invite: ensure room: %w", err)
	}
	if err := p.chatRoomReceiver.CreateInvitationViaAP(roomID, invitee.ID); err != nil {
		return fmt.Errorf("chat room invite: create invitation: %w", err)
	}
	return nil
}

// handleChatRoomAccept processes an inbound Accept whose inner object is a chat
// room Invite: a remote user has accepted our room invitation, so we record
// their membership. AddMemberViaAP requires a pending invitation, so a forged
// Accept for a room the actor was never invited to is ignored.
func (p *Processor) handleChatRoomAccept(act, inner genericActivity) error {
	if p.chatRoomReceiver == nil {
		return nil
	}
	group, ok := parseGroupObject(inner.Object)
	if !ok {
		return nil
	}
	accepter, err := p.resolver.ResolveActor(act.Actor)
	if err != nil {
		return err
	}
	return p.chatRoomReceiver.AddMemberViaAP(extractChatRoomID(group.ID), accepter.ID)
}

// handleChatRoomReject processes an inbound Reject of a chat room Invite: the
// remote invitee declined, so the pending invitation is removed.
func (p *Processor) handleChatRoomReject(act, inner genericActivity) error {
	if p.chatRoomReceiver == nil {
		return nil
	}
	group, ok := parseGroupObject(inner.Object)
	if !ok {
		return nil
	}
	rejecter, err := p.resolver.ResolveActor(act.Actor)
	if err != nil {
		return err
	}
	return p.chatRoomReceiver.RemoveInvitationViaAP(extractChatRoomID(group.ID), rejecter.ID)
}

// chatRoomIDFromContext extracts the room id from a group chat message note's
// `@context`. CherryPick group messages set the note-level `@context` to the
// room URI (a JSON string); a normal note carries the standard JSON-LD context
// (an array), which is not a room.
//
// isRoom reports whether the `@context` is a chat room URI **at all**, and is
// true even when roomID comes back empty because the id does not fit
// `chat_room.id`. 呼び出し側はこれで「room だが受け取れない」と「そもそも
// room ではない (= 1-on-1 DM)」を区別する。混ぜると、保存できない id の
// group message が DM 経路へ落ちて別の理由で dead になる (#2726)。
func chatRoomIDFromContext(raw json.RawMessage) (roomID string, isRoom bool) {
	if len(raw) == 0 {
		return "", false
	}
	var ctx string
	if json.Unmarshal(raw, &ctx) != nil {
		// 配列形式 (標準 JSON-LD context) は room ではない。
		return "", false
	}
	if !chatRoomURIRe.MatchString(ctx) {
		return "", false
	}
	return extractChatRoomID(ctx), true
}

// handleChatRoomMessageCreate persists an inbound group chat message into a
// locally-known room. The room must exist locally and the remote sender must
// be a member (enforced by the chat service): unknown room or non-member is a
// permanent condition, so it is reported as ErrUnsupportedActivity (no retry).
func (p *Processor) handleChatRoomMessageCreate(sender *model.User, noteURI, content, roomID string) error {
	// roomID が空 = `@context` は room URI だが id が `chat_room.id` に収まらない
	// (chatRoomIDFromContext)。retry では解決しないので drop する (#2726)。
	if roomID == "" {
		slog.Warn("chat room message: room id does not fit its column", "sender", sender.ID)
		return ErrUnsupportedActivity
	}
	// note id 欠落 / sender が local (loopback) は retry しても解決しない恒久的
	// 条件なので、同関数内の room 不在・非メンバーと同じく ErrUnsupportedActivity
	// (non-retry) に揃える。
	if noteURI == "" {
		slog.Warn("chat room message: missing note id", "sender", sender.ID)
		return ErrUnsupportedActivity
	}
	if sender.IsLocal() {
		slog.Warn("chat room message: sender is local (loopback?)", "sender", sender.ID)
		return ErrUnsupportedActivity
	}
	if p.chatRoomReceiver == nil {
		return ErrUnsupportedActivity
	}
	if err := p.chatRoomReceiver.CreateRoomMessageViaAP(noteURI, sender, roomID, content); err != nil {
		// 未関与の room / 非メンバー送信、および列に収まらない uri は retry しても
		// 解決しないので drop (後者は ErrInvalidTarget、#2726)。
		if errors.Is(err, corechat.ErrNotFound) ||
			errors.Is(err, corechat.ErrForbidden) ||
			errors.Is(err, corechat.ErrInvalidTarget) {
			return ErrUnsupportedActivity
		}
		return err
	}
	return nil
}
