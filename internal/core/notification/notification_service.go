// Package notification provides NotificationService for delivering and reading
// per-user notifications backed by Redis Streams.
package notification

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/repository"
)

// Type enumerates the supported notification types.
// Misskey本家と互換のキー名を使用する。
type Type string

const (
	TypeFollow   Type = "follow"
	TypeMention  Type = "mention"
	TypeReply    Type = "reply"
	TypeRenote   Type = "renote"
	TypeQuote    Type = "quote"
	TypeReaction Type = "reaction"
	// TypeApp: アプリ (access token) が notifications/create で自分自身に作る
	// 任意通知 (#1217)。notifier を持たず、body / header / icon を Extra に
	// 格納して entity の Extra spread で surface する。
	TypeApp Type = "app"
	// TypePollVote: per-vote 通知。Misskey TS には対応 type が無いため
	// frontend が空 body で render してしまい運用上 noise だけが残る (#690)。
	// service 側からは発火しない (poll_service が呼ばないように disable
	// 済み)。type 自体は永続化済み notification の互換のため残す。
	TypePollVote Type = "pollVote"
	// TypePollEnded: アンケート期限切れ時の通知 (#690)。Misskey TS の
	// EndedPollNotificationProcessor 相当。著者 + 投票者 (ローカルのみ) に
	// 1 件ずつ作る。core/poll.ExpiryWorker が周期 ticker で発火させる。
	TypePollEnded           Type = "pollEnded"
	TypeReceiveFollowReq    Type = "receiveFollowRequest"
	TypeFollowRequestAccept Type = "followRequestAccepted"
	TypeExportCompleted     Type = "exportCompleted"
	TypeImportCompleted     Type = "importCompleted"
	// TypeScheduledNotePosted / TypeScheduledNotePostFailed は upstream
	// Misskey TS の \`PostScheduledNoteProcessorService\` が発火する 2 種類
	// の通知 (#1045 Phase 2-B)。posted は \`noteId\` を Extra (or NoteID) に
	// 持ち、postFailed は \`noteDraftId\` を Extra に持つ。
	TypeScheduledNotePosted     Type = "scheduledNotePosted"
	TypeScheduledNotePostFailed Type = "scheduledNotePostFailed"
	// TypeAchievementEarned: 実績解除時の通知 (upstream AchievementService.create
	// が createNotification(userId, 'achievementEarned', {achievement: type}) で
	// 発火)。notifier を持たず、解除した実績名を Extra["achievement"] に格納して
	// entity の Extra spread で surface する。
	TypeAchievementEarned Type = "achievementEarned"
	// TypeNote: notify='normal' フォロワーが、フォロイーの新規ノートを受け取る
	// 通知 (upstream NoteCreateService の createNotification(followerId,'note',
	// {noteId},userId))。reply 以外 + visibility != specified のノートで発火し、
	// pure renote は renote-mute 済みフォロワーには送らない。
	TypeNote Type = "note"
	// TypeLogin: signin 成功時に本人へ送る通知 (upstream SigninService)。
	// notifier を持たない。
	TypeLogin Type = "login"
	// TypeCreateToken: miauth gen-token で access token を生成した時に本人へ送る
	// 通知 (upstream miauth/gen-token)。notifier を持たない。
	TypeCreateToken Type = "createToken"
	// TypeTest: notifications/test-notification が web push / stream の疎通確認用
	// に本人へ送るテスト通知 (upstream notifications/test-notification)。notifier
	// を持たない。
	TypeTest Type = "test"
	// TypeRoleAssigned: local public role が割り当てられた時に被割当ユーザーへ
	// 送る通知 (upstream RoleService.assign)。Extra["roleId"] に role ID を持ち、
	// entity 側で read 時に packed role へ解決する (role 削除済なら通知を drop)。
	TypeRoleAssigned Type = "roleAssigned"
	// TypeChatRoomInvitationReceived: chat room へ招待された時に被招待ユーザーへ
	// 送る通知 (upstream ChatService の招待作成)。notifier は招待者、
	// Extra["invitationId"] に invitation ID を持ち、entity 側で read 時に packed
	// ChatRoomInvitation へ解決する (招待削除済なら通知を drop)。
	TypeChatRoomInvitationReceived Type = "chatRoomInvitationReceived"
)

// MaxPerUser caps how many notifications are kept per user in the Redis stream.
// 上限に達したら古いものから削除される (XADD MAXLEN ~)。
const MaxPerUser = 300

// streamKey returns the Redis Stream key for a user's notification timeline.
// TS drop-in互換のため keyPrefix (通常 `<host>:`) を前置する。
func (s *Service) streamKey(userID string) string {
	return s.keyPrefix + "notificationTimeline:" + userID
}

// readKey returns the Redis key that stores the latest read notification id.
func (s *Service) readKey(userID string) string {
	return s.keyPrefix + "latestReadNotification:" + userID
}

// Notification is the payload stored in Redis.
type Notification struct {
	ID         string    `json:"id"`
	CreatedAt  time.Time `json:"createdAt"`
	Type       Type      `json:"type"`
	NotifierID string    `json:"notifierId,omitempty"`
	NoteID     string    `json:"noteId,omitempty"`
	Reaction   string    `json:"reaction,omitempty"`
	Choice     *int      `json:"choice,omitempty"`
	// NoteVisibility captures the visibility of the referenced note at
	// creation time so /api/i can populate hasUnreadSpecifiedNotes without
	// re-joining to the note table. Empty string when the notification has
	// no note (e.g. follow / followRequestAccepted).
	NoteVisibility string         `json:"noteVisibility,omitempty"`
	Extra          map[string]any `json:"extra,omitempty"`
	// AppAccessTokenID records the access token that created an 'app'
	// notification (upstream notifications/create の appAccessTokenId、#1557)。
	// 内部メタデータで API レスポンスには出さない (entity packer は spread
	// しない)。
	AppAccessTokenID string `json:"appAccessTokenId,omitempty"`
}

// CreateInput is the parameter set for Service.Create.
type CreateInput struct {
	NotifieeID string
	NotifierID string
	Type       Type
	NoteID     string
	// NoteVisibility is persisted on the Notification record so hot-path
	// reads (e.g. /api/i's hasUnreadSpecifiedNotes) can filter without a
	// note-table join. Pass the visibility of the referenced note.
	NoteVisibility string
	Reaction       string
	Choice         *int
	Extra          map[string]any
	// AppAccessTokenID は 'app' 通知を作った access token の ID (#1557)。
	AppAccessTokenID string
}

// StreamingPublisher receives a freshly created notification so that
// WebSocket subscribers can be pushed immediately. パッケージ間の循環依存を
// 避けるため interface で受け取る (実装は internal/stream)。
type StreamingPublisher interface {
	PublishNotification(notifieeID string, n *Notification)
}

// MainStreamPublisher emits real-time events to a single target user's `main`
// WebSocket channel. Used here for `unreadNotification`,
// `readAllNotifications`, and `notificationFlushed` events. 循環依存を
// 避けるため interface で受け取る (実装は internal/stream)。
type MainStreamPublisher interface {
	PublishMainEvent(userID, eventType string, body any)
}

// Packer converts a Notification into the TS-compatible packed body shape
// (map with userId / user / note / reaction / etc.). Injected as an
// interface so core/notification stays independent of entity / repository
// layers. 実装は internal/stream で提供される。
//
// notifieeID は packed body を受け取る viewer の ID (= 通知を受け取る
// ローカルユーザー)。実装側で note embed の visibility check (#1471) に
// 使う: followers / specified note は notifiee が CanSeeNote を満たさない
// ときに note field を落として本文 leak を防ぐ。
type Packer interface {
	Pack(notifieeID string, n *Notification) any
}

// unreadPublishDelay は Notification 永続化から `unreadNotification` を main
// stream に流すまでの待機時間。本家 TS の 2 秒 setTimeout と同じで、その間
// に MarkAllAsRead が呼ばれた (= ユーザーが既読化した) 場合は publish を
// skip し、不要な badge flicker (badge++ → 直後に readAllNotifications で
// 0) を回避する (#420 follow-up)。テストでは SetUnreadPublishDelay(0) で
// 同期化する。
const defaultUnreadPublishDelay = 2 * time.Second

// Service manages notifications.
type Service struct {
	client              *redis.Client
	idGen               id.Generator
	keyPrefix           string // TS drop-in互換用 `<host>:` prefix
	publisher           StreamingPublisher
	mainStreamPublisher MainStreamPublisher
	packer              Packer
	noteUnreadRepo      repository.NoteUnreadRepository
	unreadPublishDelay  time.Duration
}

// NewService constructs a new NotificationService.
//
// keyPrefix (通常は `cfg.Redis.KeyPrefix()` = `<host>:`) は TS 本家と同じキー
// 名前空間 (`<host>:notificationTimeline:*`, `<host>:latestReadNotification:*`)
// を使うために全 Redis キーの前に付与される。空文字列ならprefix無し。
func NewService(client *redis.Client, idGen id.Generator, keyPrefix string) *Service {
	return &Service{
		client:             client,
		idGen:              idGen,
		keyPrefix:          keyPrefix,
		unreadPublishDelay: defaultUnreadPublishDelay,
	}
}

// SetUnreadPublishDelay overrides the delay between Notification persistence
// and `unreadNotification` publish. 0 makes the publish synchronous (used by
// tests so they don't have to wait for time.AfterFunc to fire).
func (s *Service) SetUnreadPublishDelay(d time.Duration) {
	s.unreadPublishDelay = d
}

// SetStreamingPublisher attaches a StreamingPublisher invoked best-effort
// after Create persists a notification.
func (s *Service) SetStreamingPublisher(p StreamingPublisher) {
	s.publisher = p
}

// SetMainStreamPublisher attaches a publisher used to emit `main` channel
// events (`unreadNotification`, `readAllNotifications`,
// `notificationFlushed`). Optional — nil disables emit.
func (s *Service) SetMainStreamPublisher(p MainStreamPublisher) {
	s.mainStreamPublisher = p
}

// SetNoteUnreadRepo attaches a NoteUnreadRepository. When set, HasUnreadSpecifiedNotes
// queries the note_unread table (本家相当) instead of scanning the notification
// stream (#319). Optional — nil keeps the legacy proxy implementation which
// reads NoteVisibility off unread notifications.
func (s *Service) SetNoteUnreadRepo(r repository.NoteUnreadRepository) {
	s.noteUnreadRepo = r
}

// SetPacker attaches a Packer used to convert Notification records into
// the TS-compatible shape for main-stream emits. Optional — when unset
// the raw Notification struct is emitted (backwards compatible).
func (s *Service) SetPacker(p Packer) {
	s.packer = p
}

// Errors returned by Service.
var (
	// ErrSelfNotification is returned when attempting to create a notification where notifier == notifiee.
	ErrSelfNotification = errors.New("cannot notify oneself")
)

// Create writes a notification entry to the user's notification stream.
// notifier == notifiee の場合は何もしない (Misskey本家の挙動を踏襲)。
func (s *Service) Create(ctx context.Context, in CreateInput) (*Notification, error) {
	if in.NotifieeID == "" {
		return nil, errors.New("notifieeId is required")
	}
	if in.NotifierID != "" && in.NotifierID == in.NotifieeID {
		return nil, ErrSelfNotification
	}

	now := time.Now()
	n := &Notification{
		ID:               s.idGen.Generate(now),
		CreatedAt:        now,
		Type:             in.Type,
		NotifierID:       in.NotifierID,
		NoteID:           in.NoteID,
		NoteVisibility:   in.NoteVisibility,
		Reaction:         in.Reaction,
		Choice:           in.Choice,
		Extra:            in.Extra,
		AppAccessTokenID: in.AppAccessTokenID,
	}

	payload, err := json.Marshal(n)
	if err != nil {
		return nil, fmt.Errorf("notification marshal: %w", err)
	}
	// MAXLEN ~ で古い通知を確率的にtrim、IDは toXAddID で `<ms>-*` パターン
	// を渡して Redis に seq を自動採番させる。後段 (scheduleUnreadPublish)
	// で latestReadNotification と lexicographic 比較するときに、`*` パター
	// ンのままだと '*' (ASCII 42) < '0' (ASCII 48) で常に true になり既読
	// 判定が壊れるため、`Result()` で実際の `<ms>-<seq>` を取り直す
	// (#420 Devin review)。
	requestedID := toXAddID(n.ID, now)
	streamID, err := s.client.XAdd(ctx, &redis.XAddArgs{
		Stream: s.streamKey(in.NotifieeID),
		MaxLen: MaxPerUser,
		Approx: true,
		ID:     requestedID,
		Values: map[string]any{"data": string(payload)},
	}).Result()
	if err != nil {
		return nil, fmt.Errorf("notification xadd: %w", err)
	}
	// Packer配線済みなら user/note を fetch する Pack() は1回だけ実行し、
	// publisher / main の両方で packed body を共有する(Devin #314 #1)。
	// notifieeID を渡して Pack 側で note embed の visibility check (#1471)
	// を効かせる; followers / specified note を非閲覧 notifiee の stream に
	// 流さない (REST i/notifications #1444 と対称な doctrine)。
	var packed any = n
	if s.packer != nil {
		packed = s.packer.Pack(in.NotifieeID, n)
		if packed == nil {
			// packer が通知を drop した (roleAssigned で role 削除済など)。upstream
			// は stream へ出す前に filter するので、永続化済みの本通知は realtime
			// publish (PublishNotification / unreadNotification) だけ skip する。
			return n, nil
		}
	}
	if s.publisher != nil {
		// Publisher が packed bodyを受け取れる場合は渡して double-pack
		// を避ける。未対応実装なら raw Notification で fallback。
		if pub, ok := s.publisher.(interface {
			PublishPackedNotification(notifieeID string, packed any)
		}); ok {
			pub.PublishPackedNotification(in.NotifieeID, packed)
		} else {
			s.publisher.PublishNotification(in.NotifieeID, n)
		}
	}
	// 本家 TS NotificationService 互換: 2 秒遅延後に既読位置を再チェックし、
	// その間に MarkAllAsRead が走っていなければ unreadNotification を main
	// stream に publish する (#420 follow-up)。即時 publish だと
	// notifications-grouped 等の冗長 fetch が発火する readAllNotifications と
	// race して badge が一瞬付いて消える挙動になっていた。比較対象は
	// `latestReadNotification` Redis key の値で、これは MarkAllAsRead が
	// `XRevRangeN(...)[0].ID` (= 実 streamID) を書き込むので、Redis が
	// 採番した actual な streamID を渡す。
	s.scheduleUnreadPublish(in.NotifieeID, streamID, packed)
	return n, nil
}

// List returns the notifications for the given user with cursor pagination and
// type filtering, mirroring upstream NotificationService.getNotifications.
//
// Direction: with sinceID set and untilID absent, results are the OLDEST `limit`
// strictly newer than sinceID, in ascending order; otherwise the newest `limit`
// in descending order (QueryService.makePaginationQuery と同じ向き). The type
// filter (includeTypes else excludeTypes) is applied before the limit so that an
// older matching notification surfaces even when the newest `limit` are all
// filtered out (upstream の refetch loop 相当)。
func (s *Service) List(ctx context.Context, userID, sinceID, untilID string, limit int, includeTypes, excludeTypes []string) ([]*Notification, error) {
	if limit <= 0 {
		limit = 10
	}
	if limit > 100 {
		limit = 100
	}

	// upstream は Redis stream id を notification id から導出して COUNT=limit の
	// 範囲取得をループするが、mk-go の stream id は `<ms>-<redis採番seq>` で
	// notification id とは別軸のため、id での精密な範囲指定ができない。代わりに
	// MaxPerUser=300 cap 内で全件を向き付きで取得し、cursor(n.ID)/type で in-app
	// filter して limit 件を取る。stream は実質 300 件上限なので、これは upstream の
	// refetch loop と観測上等価 (返る通知集合と並び順が一致する)。なお XADD MAXLEN は
	// approximate trim (`~`) のため stream が一時的に 300 を僅かに超えうるが、その分は
	// 最古 tail 側であり、深い type-filter ページネーションでの取りこぼしは実害無視可能。
	ascending := sinceID != "" && untilID == ""
	// cursor も type filter も無ければ post-filter で行が落ちないので、newest limit
	// 件だけ取れば足りる (= 旧実装と同コスト、first-page load の hot path)。それ以外
	// は post-filter で limit 未満に減りうるため MaxPerUser cap まで取得してから絞る。
	fetchN := int64(MaxPerUser)
	if sinceID == "" && untilID == "" && len(includeTypes) == 0 && len(excludeTypes) == 0 {
		fetchN = int64(limit)
	}
	key := s.streamKey(userID)
	var (
		res []redis.XMessage
		err error
	)
	if ascending {
		res, err = s.client.XRangeN(ctx, key, "-", "+", fetchN).Result()
	} else {
		res, err = s.client.XRevRangeN(ctx, key, "+", "-", fetchN).Result()
	}
	if err != nil {
		return nil, err
	}

	includeSet := toTypeSet(includeTypes)
	excludeSet := toTypeSet(excludeTypes)

	out := make([]*Notification, 0, limit)
	for _, msg := range res {
		raw, ok := msg.Values["data"].(string)
		if !ok {
			continue
		}
		var n Notification
		if err := json.Unmarshal([]byte(raw), &n); err != nil {
			continue
		}
		// cursor は exclusive (upstream の `(` 境界)。
		if sinceID != "" && n.ID <= sinceID {
			continue
		}
		if untilID != "" && n.ID >= untilID {
			continue
		}
		// type filter。upstream は include があれば include のみ、無ければ exclude
		// を見る (if / else if)。
		if len(includeSet) > 0 {
			if !includeSet[n.Type] {
				continue
			}
		} else if len(excludeSet) > 0 {
			if excludeSet[n.Type] {
				continue
			}
		}
		out = append(out, &n)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

// toTypeSet converts a slice of raw type strings into a set keyed by Type.
// Empty / nil input yields a nil map (no filtering).
func toTypeSet(types []string) map[Type]bool {
	if len(types) == 0 {
		return nil
	}
	set := make(map[Type]bool, len(types))
	for _, t := range types {
		set[Type(t)] = true
	}
	return set
}

// DeleteByTypeAndNotifier removes all notifications of the given type sent by
// notifierID from notifieeID's stream. follow request の accept / reject 直後に
// 古い `receiveFollowRequest` 通知を掃除するために使う (#349 コメント)。
// stream 消費後に再度同じ notifier からリクエストが来たときに、以前の
// 解決済み通知まで復活するのを防ぐ。
func (s *Service) DeleteByTypeAndNotifier(ctx context.Context, notifieeID string, typ Type, notifierID string) error {
	if notifieeID == "" || notifierID == "" {
		return nil
	}
	// 全 stream entry を走査 (MaxPerUser=300 cap なので上限は常識的)。
	res, err := s.client.XRange(ctx, s.streamKey(notifieeID), "-", "+").Result()
	if err != nil {
		return err
	}
	var toDelete []string
	for _, msg := range res {
		raw, ok := msg.Values["data"].(string)
		if !ok {
			continue
		}
		var n Notification
		if err := json.Unmarshal([]byte(raw), &n); err != nil {
			continue
		}
		if n.Type == typ && n.NotifierID == notifierID {
			toDelete = append(toDelete, msg.ID)
		}
	}
	if len(toDelete) == 0 {
		return nil
	}
	if err := s.client.XDel(ctx, s.streamKey(notifieeID), toDelete...).Err(); err != nil {
		return err
	}
	return nil
}

// scheduleUnreadPublish delivers an `unreadNotification` event to the user's
// main stream after `s.unreadPublishDelay`, skipping the publish if the read
// marker has already advanced past `streamID` in the meantime. Mirrors the
// 2-second setTimeout + latestReadNotificationId guard in upstream TS
// (#420 follow-up)。streamID は Redis が採番した `<unix-ms>-<seq>` 形式
// (XAdd の Result) で、`latestReadNotification` Redis key と直接
// lexicographic 比較できる。delay==0 は同期 publish (テスト用)。
func (s *Service) scheduleUnreadPublish(notifieeID, streamID string, packed any) {
	if s.mainStreamPublisher == nil {
		// SetMainStreamPublisher は nil で実行する (= publish 機能を無効化
		// する) ことを明示的に許容している。テストや mainStream が不要な
		// 構成でも Create が呼ばれるたびに Warn ログを出すと noise になる
		// ので silent skip する (#420 Devin review)。
		return
	}
	publish := func() {
		latestRead, err := s.client.Get(context.Background(), s.readKey(notifieeID)).Result()
		if err == nil && latestRead != "" && latestRead >= streamID {
			// 待機中に既読化された → publish skip。badge を burn しない。
			slog.Debug("notification: unreadNotification suppressed (already read)",
				"userId", notifieeID, "streamId", streamID, "latestRead", latestRead)
			return
		}
		s.mainStreamPublisher.PublishMainEvent(notifieeID, "unreadNotification", packed)
		slog.Debug("notification: published unreadNotification",
			"userId", notifieeID, "streamId", streamID)
	}
	if s.unreadPublishDelay <= 0 {
		publish()
		return
	}
	time.AfterFunc(s.unreadPublishDelay, publish)
}

// MarkAllAsRead advances the user's notification read marker to the newest
// stream entry and, only when the marker actually moves forward, publishes
// `readAllNotifications` to the user's main stream. The publish guard
// matches upstream TS NotificationService.readAllNotification — without it
// every `notifications-grouped` fetch would re-emit `readAllNotifications`
// and clobber any pending `unreadNotification` for the same user, leaving
// the badge stuck at 0 (#420 follow-up).
//
// note_unread repository が注入されていれば同時にユーザー分の行を全削除し
// (hasUnreadSpecifiedNotes / hasUnreadMentions を false に戻すため)、
// Redis SET 失敗時は note_unread を温存して再試行で整合性を担保する。
func (s *Service) MarkAllAsRead(ctx context.Context, userID string) error {
	res, err := s.client.XRevRangeN(ctx, s.streamKey(userID), "+", "-", 1).Result()
	if err != nil {
		return err
	}
	if len(res) == 0 {
		// 既読対象が無い: ストリームに通知が無いので publish 不要。
		// note_unread だけは念のため cleanup する。
		s.clearNoteUnread(userID)
		return nil
	}
	latestNotifID := res[0].ID
	// 直前の既読 ID と比較して、実際に進む場合だけ publish する。
	// `Get` の Redis Nil error は「初回 = 未読あり」として扱う。
	prev, gerr := s.client.Get(ctx, s.readKey(userID)).Result()
	hadUnread := gerr != nil || prev == "" || prev < latestNotifID
	if err := s.client.Set(ctx, s.readKey(userID), latestNotifID, 0).Err(); err != nil {
		return err
	}
	// Redis SET成功後にnote_unreadを消す (SET失敗時は温存して次回retryで
	// 両方更新されることを期待する)。
	s.clearNoteUnread(userID)
	if hadUnread && s.mainStreamPublisher != nil {
		s.mainStreamPublisher.PublishMainEvent(userID, "readAllNotifications", nil)
	}
	return nil
}

// clearNoteUnread is a best-effort helper that deletes the user's note_unread
// rows when the repo is wired. Failures are logged but not surfaced; callers
// should invoke this after their own Redis writes succeed so the two stores
// stay in sync (or tolerate the caller's no-write early-return case).
func (s *Service) clearNoteUnread(userID string) {
	if s.noteUnreadRepo == nil {
		return
	}
	if err := s.noteUnreadRepo.DeleteAllByUser(userID); err != nil {
		slog.Warn("note_unread DeleteAllByUser failed", "userID", userID, "err", err)
	}
}

// LatestReadID returns the most recently read notification id for the user.
// 未読履歴がなければ空文字を返す。
func (s *Service) LatestReadID(ctx context.Context, userID string) (string, error) {
	val, err := s.client.Get(ctx, s.readKey(userID)).Result()
	if err == redis.Nil {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return val, nil
}

// UnreadCount returns the number of notifications in the user's stream that
// are newer than the stored latest-read id. When there is no read marker (i.e.
// the user has never marked anything as read) the full stream length is
// returned. Callers typically derive hasUnreadNotification = UnreadCount > 0.
func (s *Service) UnreadCount(ctx context.Context, userID string) (int64, error) {
	readID, err := s.LatestReadID(ctx, userID)
	if err != nil {
		return 0, err
	}
	// Redis Streams は ID 単調増加。XLen で全件数を取り、readID 以前の
	// 件数を差し引く方が O(1) でシンプルだが、readID より古いエントリが
	// MAXLEN で削除されている場合があるので XRevRange で直接数える。
	res, err := s.client.XRevRangeN(ctx, s.streamKey(userID), "+", exclusive(readID), MaxPerUser).Result()
	if err != nil {
		return 0, err
	}
	return int64(len(res)), nil
}

// UnreadSummary is the aggregated result of a single pass over the user's
// notification stream. Populated by UnreadSummary() so /api/i can resolve all
// of its unread-related fields in one Redis round-trip instead of three (#321).
type UnreadSummary struct {
	// TotalCount is UnreadCount equivalent: number of stream entries newer
	// than the user's read marker.
	TotalCount int64
	// HasMentions reports whether any unread entry's type matches one of
	// the MentionTypes passed to UnreadSummary. Caller typically passes
	// {TypeMention, TypeReply} for the /api/i hasUnreadMentions flag.
	HasMentions bool
	// HasSpecifiedNote reports whether any unread specified-visibility note
	// targets the user. Sourced from noteUnreadRepo when wired (本家互換)
	// and derived from the same stream scan (legacy NoteVisibility proxy)
	// otherwise.
	HasSpecifiedNote bool
}

// UnreadSummary computes the /api/i unread bundle in a single stream scan.
// mentionTypes limits the HasMentions calculation; pass nil to skip that
// computation (TotalCount / HasSpecifiedNote still populated). Errors from
// Redis or the note_unread repo are returned unmodified; partial results are
// preserved when possible.
func (s *Service) UnreadSummary(ctx context.Context, userID string, mentionTypes []Type) (UnreadSummary, error) {
	var summary UnreadSummary
	readID, err := s.LatestReadID(ctx, userID)
	if err != nil {
		return summary, err
	}
	res, err := s.client.XRevRangeN(ctx, s.streamKey(userID), "+", exclusive(readID), MaxPerUser).Result()
	if err != nil {
		return summary, err
	}
	summary.TotalCount = int64(len(res))

	// mentionTypes が空なら HasMentions 判定をスキップ。noteUnreadRepo
	// が wired なら HasSpecifiedNote は DB 経由なので stream 走査不要。
	needMentionPass := len(mentionTypes) > 0
	needSpecifiedPass := s.noteUnreadRepo == nil
	if needMentionPass || needSpecifiedPass {
		var wanted map[Type]struct{}
		if needMentionPass {
			wanted = make(map[Type]struct{}, len(mentionTypes))
			for _, t := range mentionTypes {
				wanted[t] = struct{}{}
			}
		}
		for _, msg := range res {
			// 必要な flag が全部埋まったら早期終了
			if (!needMentionPass || summary.HasMentions) && (!needSpecifiedPass || summary.HasSpecifiedNote) {
				break
			}
			raw, ok := msg.Values["data"].(string)
			if !ok {
				continue
			}
			var n Notification
			if err := json.Unmarshal([]byte(raw), &n); err != nil {
				continue
			}
			if needMentionPass && !summary.HasMentions {
				if _, ok := wanted[n.Type]; ok {
					summary.HasMentions = true
				}
			}
			if needSpecifiedPass && !summary.HasSpecifiedNote {
				if n.NoteVisibility == "specified" {
					summary.HasSpecifiedNote = true
				}
			}
		}
	}

	if s.noteUnreadRepo != nil {
		has, err := s.noteUnreadRepo.HasAnySpecified(userID)
		if err != nil {
			return summary, err
		}
		summary.HasSpecifiedNote = has
	}

	return summary, nil
}

// HasUnreadOfTypes reports whether the user has any unread notification whose
// type is in the provided list. Used by /api/i to compute hasUnreadMentions
// (types: mention/reply). Returns false when types is empty.
// readID 以降のエントリを XRevRange で読み、一件でもマッチしたら true。
func (s *Service) HasUnreadOfTypes(ctx context.Context, userID string, types []Type) (bool, error) {
	if len(types) == 0 {
		return false, nil
	}
	readID, err := s.LatestReadID(ctx, userID)
	if err != nil {
		return false, err
	}
	res, err := s.client.XRevRangeN(ctx, s.streamKey(userID), "+", exclusive(readID), MaxPerUser).Result()
	if err != nil {
		return false, err
	}
	wanted := make(map[Type]struct{}, len(types))
	for _, t := range types {
		wanted[t] = struct{}{}
	}
	for _, msg := range res {
		raw, ok := msg.Values["data"].(string)
		if !ok {
			continue
		}
		var n Notification
		if err := json.Unmarshal([]byte(raw), &n); err != nil {
			continue
		}
		if _, ok := wanted[n.Type]; ok {
			return true, nil
		}
	}
	return false, nil
}

// HasUnreadSpecifiedNotes reports whether the user has any unread
// specified-visibility note targeted at them. Used by /api/i to populate
// hasUnreadSpecifiedNotes.
//
// noteUnreadRepoが注入されていればそちら優先 (本家互換、visibleUserIds
// 経由も捕捉できる)。nilの場合はnotification streamをscanするlegacy実装に
// フォールバックする。
func (s *Service) HasUnreadSpecifiedNotes(ctx context.Context, userID string) (bool, error) {
	if s.noteUnreadRepo != nil {
		return s.noteUnreadRepo.HasAnySpecified(userID)
	}
	readID, err := s.LatestReadID(ctx, userID)
	if err != nil {
		return false, err
	}
	res, err := s.client.XRevRangeN(ctx, s.streamKey(userID), "+", exclusive(readID), MaxPerUser).Result()
	if err != nil {
		return false, err
	}
	for _, msg := range res {
		raw, ok := msg.Values["data"].(string)
		if !ok {
			continue
		}
		var n Notification
		if err := json.Unmarshal([]byte(raw), &n); err != nil {
			continue
		}
		if n.NoteVisibility == "specified" {
			return true, nil
		}
	}
	return false, nil
}

// exclusive returns the exclusive lower-bound string for XRevRange, i.e. the
// boundary that means "strictly newer than this id". Empty readID becomes "-"
// (stream head); non-empty uses the "(<id>" exclusive marker.
func exclusive(readID string) string {
	if readID == "" {
		return "-"
	}
	return "(" + readID
}

// Flush deletes all notifications and the read marker for a user (used in tests
// and account deletion). Emits `notificationFlushed` to the user's main stream
// on success. note_unread repoが注入されていれば同時にclearする。
func (s *Service) Flush(ctx context.Context, userID string) error {
	if err := s.client.Del(ctx, s.streamKey(userID)).Err(); err != nil {
		return err
	}
	if err := s.client.Del(ctx, s.readKey(userID)).Err(); err != nil {
		return err
	}
	s.clearNoteUnread(userID)
	if s.mainStreamPublisher != nil {
		s.mainStreamPublisher.PublishMainEvent(userID, "notificationFlushed", nil)
	}
	return nil
}

// toXAddID returns a Redis stream id derived from the notification id.
// Redis Streamsは "ms-seq" 形式を期待する。idGenが生成するIDからのタイム
// スタンプ部分を使い、シーケンスは0固定で十分(MAXLENで管理されるため
// 競合はリトライ不要)。
//
// upstream Misskey #17358 (= 2026.5.1 fix) は ULID 環境下で notification id の
// `additional` 部 (80-bit) をそのまま Redis Stream sequence (uint64) に渡して
// XADD が失敗し続け、通知が ~10s 遅延する bug を修正したが、mk-go では引数
// `time.Time` から ms を直接生成しており ID parse 経路を一切踏まないため
// overflow は構造的に発生しない (= triage #1011 / upstream #17358 close)。
func toXAddID(_ string, t time.Time) string {
	return fmt.Sprintf("%d-*", t.UnixMilli())
}
