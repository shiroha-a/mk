package federation

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	corechat "github.com/shiroha-a/mk/internal/core/chat"
)

// ChatRoomReceiver wires the chat service's room-federation operations into the
// inbox processor. Implemented by core/chat.Service. Mirrors the CherryPick
// group chat federation handshake (Invite / Accept / Reject of a Group object).
type ChatRoomReceiver interface {
	EnsureRoomViaAP(roomID, name, summary, ownerUserID string) error
	CreateInvitationViaAP(roomID, inviteeUserID string) error
	AddMemberViaAP(roomID, userID string) error
	RemoveInvitationViaAP(roomID, userID string) error
}

// SetChatRoomReceiver wires the chat room federation receiver for inbound
// Invite / Accept / Reject of chat room Group objects (#1203).
func (p *Processor) SetChatRoomReceiver(r ChatRoomReceiver) {
	p.chatRoomReceiver = r
}

// chatRoomURIRe extracts the room id from a CherryPick chat room URI
// (`https://host/chat/rooms/{id}`). The id is alphanumeric (aidx/ULID).
var chatRoomURIRe = regexp.MustCompile(`/chat/rooms/([a-zA-Z0-9]+)$`)

func extractChatRoomID(uri string) string {
	m := chatRoomURIRe.FindStringSubmatch(uri)
	if len(m) != 2 {
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
