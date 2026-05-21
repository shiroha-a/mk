// Package chat provides a thin service layer over ChatRepository that
// dispatches real-time delivery events to the matching WebSocket channel.
//
// Phase 9.8 — メッセージ送信 / 削除 / 編集 / 既読マークの各 API はこの service
// を経由することで、(a) DB への永続化と (b) Redis pubsub 経由の WebSocket
// 配信を同時に行う。本家 Misskey の ChatService.createMessageToUser /
// createMessageToRoom / deleteMessage の挙動を移植している。
package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/shiroha-a/mk/internal/activitypub"
	"github.com/shiroha-a/mk/internal/entity"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
)

// Errors returned by Service.
var (
	// ErrNotFound is returned when a referenced message or room does not exist.
	ErrNotFound = errors.New("chat resource not found")
	// ErrForbidden is returned when the acting user cannot modify the target
	// resource (e.g. deleting someone else's message, posting to a room they
	// are not a member of).
	ErrForbidden = errors.New("chat action forbidden")
	// ErrInvalidTarget is returned when neither toUserId nor toRoomId is
	// provided to CreateMessage helpers.
	ErrInvalidTarget = errors.New("chat target is required")
)

// StreamingPublisher is the Redis pub/sub dispatch interface the service uses
// to broadcast chat events. Implementations live in internal/stream so the
// concrete pub/sub bus can be injected without creating a cycle.
//
// 本家 Misskey の GlobalEventService.publishChatUserStream /
// publishChatRoomStream に対応する。
type StreamingPublisher interface {
	PublishUserMessage(ctx context.Context, fromUserID, toUserID, eventType string, body any)
	PublishRoomMessage(ctx context.Context, roomID, eventType string, body any)
}

// APDeliverer delivers a pre-rendered activity body to a single remote user's
// inbox. Implemented by federation.DeliverService.DeliverToUser.
type APDeliverer interface {
	DeliverToUser(signerUserID string, recipient *model.User, body []byte) error
}

// MainStreamPublisher emits real-time events to a single target user's `main`
// WebSocket channel. Used here to deliver `newChatMessage` events to message
// recipients so the frontend can surface unread-chat indicators immediately.
// 循環依存を避けるため interface で受け取る (実装は internal/stream)。
type MainStreamPublisher interface {
	PublishMainEvent(userID, eventType string, body any)
}

// Event types mirrored by the WebSocket channel. 本家と同名。
const (
	EventMessage = "message"
	EventDeleted = "deleted"
	EventEdited  = "edited"
	EventRead    = "read"
)

// ModeratorChecker reports whether a given user has moderator privileges.
// パッケージ間の循環依存を避けるため interface で受け取る (実装は core/role.Service)。
type ModeratorChecker interface {
	IsModerator(userID string) bool
}

// Service implements chat message CRUD + streaming fan-out.
type Service struct {
	repo                repository.ChatRepository
	idGen               id.Generator
	publisher           StreamingPublisher
	userRepo            repository.UserRepository
	followingRepo       repository.FollowingRepository
	renderer            *activitypub.Renderer
	urls                *activitypub.URLBuilder
	deliverer           APDeliverer
	mainStreamPublisher MainStreamPublisher
	// moderatorChecker: chat/rooms/show 等の権限 gate で「moderator は閲覧可」
	// を判定するために使う (upstream 2026.5.4 互換)。nil 安全。
	moderatorChecker ModeratorChecker
}

// NewService constructs a chat Service.
func NewService(repo repository.ChatRepository, idGen id.Generator) *Service {
	return &Service{repo: repo, idGen: idGen}
}

// SetStreamingPublisher wires a StreamingPublisher so state-changing methods
// can dispatch to the WebSocket channel. nil 渡しで配信は無効になる (既存の
// DB 処理は変わらない)。
func (s *Service) SetStreamingPublisher(p StreamingPublisher) {
	s.publisher = p
}

// SetMainStreamPublisher attaches a publisher used to emit `newChatMessage`
// on the recipient(s)' main channel. Optional — nil disables main-stream emit
// but chat-stream `message` events remain unaffected.
func (s *Service) SetMainStreamPublisher(p MainStreamPublisher) {
	s.mainStreamPublisher = p
}

// SetAPDelivery wires the AP federation layer for chat message delivery to
// remote users. All parameters are required; pass nil to disable federation.
func (s *Service) SetAPDelivery(userRepo repository.UserRepository, renderer *activitypub.Renderer, urls *activitypub.URLBuilder, deliverer APDeliverer) {
	s.userRepo = userRepo
	s.renderer = renderer
	s.urls = urls
	s.deliverer = deliverer
}

// SetModeratorChecker wires a ModeratorChecker so chat/rooms/show 等の権限
// gate で moderator role を持つ user が閲覧できることを判定できる。
// nil なら moderator bypass は無効化 (= owner / member / 招待のみ true)。
func (s *Service) SetModeratorChecker(m ModeratorChecker) {
	s.moderatorChecker = m
}

// HasPermissionToViewRoomInfo は room のメタ情報を閲覧できる権限を持つか判定する
// (upstream Misskey TS 2026.5.4 `ChatService.hasPermissionToViewRoomInfo` 互換)。
// 返り値 true の条件:
//   - room の owner である
//   - room の member である
//   - room への招待を受け取っている (chat_room_invitation 行が存在する)
//   - 自身が moderator (ModeratorChecker 設定時のみ)
//
// 旧 mk-go の chat/rooms/show は権限 check 無しで誰でも room メタ情報を取得できる
// drop-in regression 状態だったため、本 helper を新設して /api/chat/rooms/show
// 等で gate する (#1164 Phase C)。
func (s *Service) HasPermissionToViewRoomInfo(meID string, room *model.ChatRoom) (bool, error) {
	if room == nil {
		return false, nil
	}
	if room.OwnerID == meID {
		return true, nil
	}
	if _, err := s.repo.FindMembership(meID, room.ID); err == nil {
		return true, nil
	}
	if _, err := s.repo.FindInvitation(meID, room.ID); err == nil {
		return true, nil
	}
	if s.moderatorChecker != nil && s.moderatorChecker.IsModerator(meID) {
		return true, nil
	}
	return false, nil
}

// SetFollowingRepo wires the following repository so chatScope=followers /
// following / mutual checks can be enforced (#692). 未配線のままで granular
// scope を持つ recipient へ送信しようとすると `canChat` が fail-closed で
// reject する (旧実装の silent allow は意図しない open relay を作るため
// PR #708 review で改めた)。production では必ず配線する。
func (s *Service) SetFollowingRepo(repo repository.FollowingRepository) {
	s.followingRepo = repo
}

// ErrChatScopeViolation is returned when a chat message is rejected because
// the recipient's chatScope does not allow the sender. CherryPick の
// `Error('recipient is cannot chat (...)')` 相当 (#692)。
var ErrChatScopeViolation = errors.New("chat scope violation")

// ErrChatScopeUnconfigured is returned when canChat() needs to consult the
// following graph (chatScope = followers / following / mutual) but no
// FollowingRepository was wired. Treated as fail-closed (= reject) instead
// of silently degrading to everyone, which would be an open relay (#708 review)。
var ErrChatScopeUnconfigured = errors.New("chat scope check unconfigured: followingRepo missing")

// canChat reports whether `sender` is allowed to send a 1-on-1 chat to
// `recipient` based on recipient.chatScope. Returns nil when allowed.
//
// scope semantics (CherryPick / Misskey TS と一致):
//   - "everyone": 誰からでも受信
//   - "followers": 受信者を follow している人だけ (sender が recipient の follower)
//   - "following": 受信者が follow している人だけ (recipient が sender を follow)
//   - "mutual": 双方向
//   - "none": 完全拒否
//
// remote recipient の場合は、AP に expose されるのは `_misskey_canChat`
// (boolean) だけなので chatScope は "everyone" / "none" の二値しか取り得ない
// (resolver で翻訳済み, #692)。granular な判定は受信側 instance に委ねる。
//
// followingRepo 未配線で granular scope を判定しないといけない局面では
// `ErrChatScopeUnconfigured` を返して fail-closed する (#708 review #2)。
// silent に everyone 降格すると production の wiring 忘れが open relay に
// なり、chatScope 設定が黙って無効化される。
func (s *Service) canChat(sender, recipient *model.User) error {
	if recipient == nil || sender == nil {
		return nil
	}
	if recipient.ChatScope == "none" {
		return ErrChatScopeViolation
	}
	if !recipient.IsLocal() {
		// 連合先のスコープは "everyone" / "none" の二値。connection grain の
		// followers/following/mutual は remote 側で評価されるので mk-go では
		// 'none' だけ事前 reject すれば十分。
		return nil
	}
	switch recipient.ChatScope {
	case "", "everyone":
		return nil
	}
	// granular scope (followers / following / mutual) は follow graph 必須。
	// 未配線なら fail-closed する。
	if s.followingRepo == nil {
		return ErrChatScopeUnconfigured
	}
	switch recipient.ChatScope {
	case "followers":
		ok, err := s.followingRepo.Exists(sender.ID, recipient.ID)
		if err == nil && ok {
			return nil
		}
		return ErrChatScopeViolation
	case "following":
		ok, err := s.followingRepo.Exists(recipient.ID, sender.ID)
		if err == nil && ok {
			return nil
		}
		return ErrChatScopeViolation
	case "mutual":
		ok1, e1 := s.followingRepo.Exists(sender.ID, recipient.ID)
		ok2, e2 := s.followingRepo.Exists(recipient.ID, sender.ID)
		if e1 == nil && e2 == nil && ok1 && ok2 {
			return nil
		}
		return ErrChatScopeViolation
	}
	return nil
}

// --- Message lifecycle ---

// CreateMessageToUser persists a direct-message row and fires the
// `message` event on both conversation directions so that either user's
// connected WebSocket subscribers see the message immediately.
//
// chatScope を強制する (#692)。recipient が local なら granular な
// followers/following/mutual まで判定し、remote なら `_misskey_canChat`
// 由来の "none" だけ事前 reject する (canChat() が分岐)。
func (s *Service) CreateMessageToUser(ctx context.Context, fromUserID, toUserID, text, fileID string) (*model.ChatMessage, error) {
	if fromUserID == "" || toUserID == "" {
		return nil, ErrInvalidTarget
	}
	if s.userRepo != nil {
		recipient, err := s.userRepo.FindByID(toUserID)
		if err == nil {
			sender, err := s.userRepo.FindByID(fromUserID)
			if err == nil {
				if err := s.canChat(sender, recipient); err != nil {
					return nil, err
				}
			}
		}
	}
	msg := &model.ChatMessage{
		ID:         s.idGen.Generate(time.Now()),
		FromUserID: fromUserID,
		ToUserID:   &toUserID,
	}
	if text != "" {
		msg.Text = &text
	}
	if fileID != "" {
		msg.FileID = &fileID
	}
	if err := s.repo.CreateMessage(msg); err != nil {
		return nil, fmt.Errorf("create user message: %w", err)
	}
	if s.publisher != nil {
		s.publisher.PublishUserMessage(ctx, fromUserID, toUserID, EventMessage, s.packMessageStream(msg))
	}
	// Misskey 本家の ChatService.createMessageToUser は、3 秒後に未読判定して
	// newChatMessage を publishMainStream する。本実装では未読判定を省略し、
	// 作成と同時に recipient (toUserID) の main に emit する (sender には
	// 送らない: TS と同じ fan-out 方針)。
	if s.mainStreamPublisher != nil {
		s.mainStreamPublisher.PublishMainEvent(toUserID, "newChatMessage", s.packMessageStream(msg))
	}
	// リモートユーザー宛ならAP配送 (best-effort)
	s.tryDeliverToRemoteUser(msg, fromUserID, toUserID)
	return msg, nil
}

// tryDeliverToRemoteUser attempts AP delivery for a 1-on-1 chat message when
// the recipient is a remote user. Errors are logged and swallowed (DB commit
// is already done).
func (s *Service) tryDeliverToRemoteUser(msg *model.ChatMessage, fromUserID, toUserID string) {
	if s.deliverer == nil || s.userRepo == nil || s.renderer == nil || s.urls == nil {
		return
	}
	recipient, err := s.userRepo.FindByID(toUserID)
	if err != nil || recipient.IsLocal() {
		return
	}
	sender, err := s.userRepo.FindByID(fromUserID)
	if err != nil || !sender.IsLocal() {
		return
	}
	senderURI := s.urls.UserURI(sender.ID)
	recipientURI := ""
	if recipient.URI != nil {
		recipientURI = *recipient.URI
	}
	if recipientURI == "" {
		return
	}
	_ = s.repo.UpdateDeliveryStatus(msg.ID, true, false)
	// CherryPick 互換 wire format: Create + Note(_misskey_talk:true) (#692)。
	// published は ChatMessage.ID (ULID/aidx) のタイムスタンプから導出する。
	published := time.Now().UTC().Format(time.RFC3339)
	if t, err := s.idGen.ParseTime(msg.ID); err == nil {
		published = t.UTC().Format(time.RFC3339)
	}
	cm := s.renderer.RenderChatMessage(msg, senderURI, recipientURI, published)
	body, err := json.Marshal(cm)
	if err != nil {
		_ = s.repo.UpdateDeliveryStatus(msg.ID, false, true)
		slog.Warn("chat: failed to marshal ChatMessage activity", "msgID", msg.ID, "err", err)
		return
	}
	if err := s.deliverer.DeliverToUser(sender.ID, recipient, body); err != nil {
		_ = s.repo.UpdateDeliveryStatus(msg.ID, false, true)
		slog.Warn("chat: failed to deliver ChatMessage to remote user", "msgID", msg.ID, "err", err)
		return
	}
	_ = s.repo.UpdateDeliveryStatus(msg.ID, false, false)
}

// CreateMessageViaAP persists a chat message received via ActivityPub from a
// remote user. The uri parameter is the activity's canonical ID. AP retries
// are common so URI-based dedup is performed (IngestNote と同じパターン).
//
// recipient の chatScope を必ず強制する (#692)。連合受信時は AP retry でこの
// path が複数回踏まれるが、scope 違反は最初の試行で拒絶し、retry でも同じ
// 結果になるので peer 側は dead letter で処理する。
func (s *Service) CreateMessageViaAP(ctx context.Context, uri string, fromUser *model.User, toUserID, text string) (*model.ChatMessage, error) {
	if fromUser == nil || toUserID == "" {
		return nil, ErrInvalidTarget
	}
	// AP retry による重複メッセージ作成を防ぐ
	if uri != "" {
		if existing, err := s.repo.FindMessageByURI(uri); err == nil {
			return existing, nil
		}
	}
	if s.userRepo != nil {
		if recipient, err := s.userRepo.FindByID(toUserID); err == nil && recipient.IsLocal() {
			if err := s.canChat(fromUser, recipient); err != nil {
				return nil, err
			}
		}
	}
	msg := &model.ChatMessage{
		ID:         s.idGen.Generate(time.Now()),
		FromUserID: fromUser.ID,
		ToUserID:   &toUserID,
	}
	if text != "" {
		msg.Text = &text
	}
	if uri != "" {
		msg.URI = &uri
	}
	if err := s.repo.CreateMessage(msg); err != nil {
		return nil, fmt.Errorf("create AP message: %w", err)
	}
	if s.publisher != nil {
		s.publisher.PublishUserMessage(ctx, fromUser.ID, toUserID, EventMessage, packMessage(msg))
	}
	// Remote → local DM を受信した場合も、CreateMessageToUser と同じく
	// recipient の main チャネルに newChatMessage を emit してフロントの
	// 未読インジケータを更新させる。sender は remote なので emit 対象外。
	if s.mainStreamPublisher != nil {
		s.mainStreamPublisher.PublishMainEvent(toUserID, "newChatMessage", s.packMessageStream(msg))
	}
	return msg, nil
}

// CreateMessageToRoom persists a room-message row and fires the `message`
// event on the room topic so every subscribed member receives it.
func (s *Service) CreateMessageToRoom(ctx context.Context, fromUserID, roomID, text, fileID string) (*model.ChatMessage, error) {
	if fromUserID == "" || roomID == "" {
		return nil, ErrInvalidTarget
	}
	// ルームが存在し、送信者がメンバーであることを確認する。
	// 取得した room は後段の emitRoomNewChatMessage にも渡し、重複 fetch を避ける。
	room, err := s.repo.FindRoomByID(roomID)
	if err != nil {
		return nil, ErrNotFound
	}
	isMember, err := s.isRoomMemberWith(room, fromUserID, roomID)
	if err != nil {
		return nil, err
	}
	if !isMember {
		return nil, ErrForbidden
	}
	msg := &model.ChatMessage{
		ID:         s.idGen.Generate(time.Now()),
		FromUserID: fromUserID,
		ToRoomID:   &roomID,
	}
	if text != "" {
		msg.Text = &text
	}
	if fileID != "" {
		msg.FileID = &fileID
	}
	if err := s.repo.CreateMessage(msg); err != nil {
		return nil, fmt.Errorf("create room message: %w", err)
	}
	if s.publisher != nil {
		s.publisher.PublishRoomMessage(ctx, roomID, EventMessage, s.packMessageStream(msg))
	}
	// Room 宛も同様に、sender 以外の全 member (owner 含む) の main に
	// newChatMessage を emit する。Misskey 本家 ChatService
	// .createMessageToRoom と同じ fan-out 方針。
	s.emitRoomNewChatMessage(room, fromUserID, msg)
	return msg, nil
}

// isRoomMemberWith reports whether userID is a member of the given room,
// reusing an already-fetched room object to avoid a second FindRoomByID.
// The owner is treated as an implicit member even without a membership row.
func (s *Service) isRoomMemberWith(room *model.ChatRoom, userID, roomID string) (bool, error) {
	if room != nil && room.OwnerID == userID {
		return true, nil
	}
	if _, err := s.repo.FindMembership(userID, roomID); err == nil {
		return true, nil
	}
	return false, nil
}

// emitRoomNewChatMessage fans out `newChatMessage` to every room member
// except the sender. Room membership 列挙失敗時は警告ログを残して emit を
// スキップする (message 自体の作成は成功として返される前に呼ばれる)。
func (s *Service) emitRoomNewChatMessage(room *model.ChatRoom, fromUserID string, msg *model.ChatMessage) {
	if s.mainStreamPublisher == nil || room == nil {
		return
	}
	members, err := s.repo.ListMembersByRoom(room.ID)
	if err != nil {
		slog.Warn("chat: ListMembersByRoom failed, skipping newChatMessage emit", "roomID", room.ID, "err", err)
		return
	}
	packed := s.packMessageStream(msg)
	// 重複 emit を防ぐため set 相当で ID を管理する (membership + owner)。
	emitted := make(map[string]bool, len(members)+1)
	if room.OwnerID != fromUserID {
		emitted[room.OwnerID] = true
	}
	for _, m := range members {
		if m.UserID == fromUserID || emitted[m.UserID] {
			continue
		}
		emitted[m.UserID] = true
	}
	for uid := range emitted {
		s.mainStreamPublisher.PublishMainEvent(uid, "newChatMessage", packed)
	}
}

// DeleteMessage removes a message and fans out a `deleted` event on the
// appropriate topic (user DM or room). Only the author may delete.
func (s *Service) DeleteMessage(ctx context.Context, userID, messageID string) error {
	msg, err := s.repo.FindMessageByID(messageID)
	if err != nil {
		return ErrNotFound
	}
	if msg.FromUserID != userID {
		return ErrForbidden
	}
	if err := s.repo.DeleteMessage(messageID); err != nil {
		return fmt.Errorf("delete message: %w", err)
	}
	if s.publisher != nil {
		body := map[string]any{"id": messageID}
		if msg.ToRoomID != nil {
			s.publisher.PublishRoomMessage(ctx, *msg.ToRoomID, EventDeleted, body)
		} else if msg.ToUserID != nil {
			s.publisher.PublishUserMessage(ctx, msg.FromUserID, *msg.ToUserID, EventDeleted, body)
		}
	}
	return nil
}

// UpdateMessage edits a message text and fires `edited`. Author-only.
func (s *Service) UpdateMessage(ctx context.Context, userID, messageID, text string) (*model.ChatMessage, error) {
	msg, err := s.repo.FindMessageByID(messageID)
	if err != nil {
		return nil, ErrNotFound
	}
	if msg.FromUserID != userID {
		return nil, ErrForbidden
	}
	msg.Text = &text
	if err := s.repo.UpdateMessage(msg); err != nil {
		return nil, fmt.Errorf("update message: %w", err)
	}
	if s.publisher != nil {
		body := s.packMessageStream(msg)
		if msg.ToRoomID != nil {
			s.publisher.PublishRoomMessage(ctx, *msg.ToRoomID, EventEdited, body)
		} else if msg.ToUserID != nil {
			s.publisher.PublishUserMessage(ctx, msg.FromUserID, *msg.ToUserID, EventEdited, body)
		}
	}
	return msg, nil
}

// MarkReadByMessageID records that userID has read messageID. Misskey 本家は
// 既読を redis cache のみで持ち配信しないが、マルチ端末/タブ同期のため軽微な
// 拡張として `read` イベントを発行する。
func (s *Service) MarkReadByMessageID(ctx context.Context, userID, messageID string) error {
	msg, err := s.repo.FindMessageByID(messageID)
	if err != nil {
		return ErrNotFound
	}
	if err := s.repo.MarkRead(userID, messageID); err != nil {
		return fmt.Errorf("mark read: %w", err)
	}
	if s.publisher != nil {
		body := map[string]any{"id": messageID, "userId": userID}
		if msg.ToRoomID != nil {
			s.publisher.PublishRoomMessage(ctx, *msg.ToRoomID, EventRead, body)
		} else if msg.ToUserID != nil {
			// DM の場合、両端末に既読状態を通知するため双方向へ送る。
			s.publisher.PublishUserMessage(ctx, msg.FromUserID, *msg.ToUserID, EventRead, body)
		}
	}
	return nil
}

// --- Queries used by the WebSocket channel for membership / auth checks ---

// IsRoomMember reports whether userID is a member of roomID. The room owner
// is implicitly a member even if no explicit chat_room_membership row exists.
func (s *Service) IsRoomMember(userID, roomID string) (bool, error) {
	room, err := s.repo.FindRoomByID(roomID)
	if err == nil && room.OwnerID == userID {
		return true, nil
	}
	if _, err := s.repo.FindMembership(userID, roomID); err == nil {
		return true, nil
	}
	return false, nil
}

// packMessage produces a JSON-serializable projection of a ChatMessage for
// outbound WebSocket events. フロント互換のため本家 ChatMessageLite と同じ
// 主要フィールドを含める。createdAt は idGen の初期化未完了時 (テスト等)
// に欠落することがあるが、上位の Service.packMessage 経由なら必ず付く (#692)。
func packMessage(msg *model.ChatMessage) map[string]any {
	out := map[string]any{
		"id":         msg.ID,
		"fromUserId": msg.FromUserID,
	}
	if msg.Text != nil {
		out["text"] = *msg.Text
	}
	if msg.ToUserID != nil {
		out["toUserId"] = *msg.ToUserID
	}
	if msg.ToRoomID != nil {
		out["toRoomId"] = *msg.ToRoomID
	}
	if msg.FileID != nil {
		out["fileId"] = *msg.FileID
	}
	if msg.URI != nil {
		out["uri"] = *msg.URI
	}
	if msg.Reads != nil {
		out["reads"] = []string(msg.Reads)
	}
	if msg.Reactions != nil {
		out["reactions"] = []string(msg.Reactions)
	}
	return out
}

// packMessageStream is the service-level packer that adds createdAt + fromUser
// on top of packMessage. WebSocket streaming や mainStream の `newChatMessage`
// 配信に使う。createdAt 欠落だと FE 側の MkTime が NaN/NaN を表示する (#692)。
func (s *Service) packMessageStream(msg *model.ChatMessage) map[string]any {
	out := packMessage(msg)
	if s.idGen != nil {
		if t, err := s.idGen.ParseTime(msg.ID); err == nil {
			out["createdAt"] = t.UTC().Format("2006-01-02T15:04:05.000Z")
		}
	}
	if msg.FromUser != nil {
		out["fromUser"] = packUserStream(msg.FromUser)
	}
	if msg.ToUser != nil {
		out["toUser"] = packUserStream(msg.ToUser)
	}
	return out
}

// packUserStream is a UserLite-shaped projection for stream payloads.
// FE の MkAvatar / chat 一覧が avatarUrl / avatarBlurhash を読むため、
// fromUser / toUser に含める。avatarUrl 未設定時は `entity.IdenticonURL`
// 経由で identicon URL に fallback する (#692)。
func packUserStream(u *model.User) map[string]any {
	return map[string]any{
		"id":             u.ID,
		"username":       u.Username,
		"name":           u.Name,
		"host":           u.Host,
		"avatarUrl":      entity.IdenticonURL(u),
		"avatarBlurhash": u.AvatarBlurhash,
		"isBot":          u.IsBot,
		"isCat":          u.IsCat,
	}
}
