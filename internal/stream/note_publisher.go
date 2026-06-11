package stream

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"

	coredrive "github.com/shiroha-a/mk/internal/core/drive"
	corenotification "github.com/shiroha-a/mk/internal/core/notification"
	"github.com/shiroha-a/mk/internal/entity"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
)

// PubSubPublisher abstracts the Publish side of core/event.PubSubService.
// 循環依存を避けるため interface で受け取る。
type PubSubPublisher interface {
	Publish(ctx context.Context, channel string, payload any) error
}

// NotePublisher serializes a model.Note via entity.PackNote, embeds the author
// in NoteEntity.User and publishes the JSON to a Redis pubsub topic. これは
// core/timeline.StreamingPublisher を実装する。
//
// フォロワーfanout時に同じノートが複数topicにpublishされるため、直前の
// ノートIDに対する serialized JSON をキャッシュして DB クエリを 1 回に抑える。
type NotePublisher struct {
	pub            PubSubPublisher
	idGen          id.Generator
	emojiLookup    entity.EmojiLookup
	instanceLookup entity.InstanceLookup
	reactionReader entity.BufferedReactionsReader
	// fieldResolver は Files / Channel など PackNoteWithInstance では
	// 解決されない後段 field を埋める。未設定時は従来通り Files が
	// 空配列のまま publish される (旧挙動)。SetFieldResolver で配線。
	fieldResolver *entity.NoteFieldResolver

	mu         sync.Mutex
	cachedID   string
	cachedBody []byte
}

// NewNotePublisher constructs a NotePublisher.
func NewNotePublisher(pub PubSubPublisher, idGen id.Generator) *NotePublisher {
	return &NotePublisher{pub: pub, idGen: idGen}
}

// SetEmojiLookup attaches an EmojiLookup so published notes get their
// custom emoji shortcodes resolved to URLs (#330).
func (p *NotePublisher) SetEmojiLookup(lookup entity.EmojiLookup) {
	p.emojiLookup = lookup
}

// SetInstanceLookup attaches an InstanceLookup so that UserLite.Instance
// (used by frontend InstanceTicker for theme color / software name) is
// populated on streaming payloads (#416). Without this the REST response
// carries the instance info but the pubsub payload does not, causing a
// visual flicker on page reload.
func (p *NotePublisher) SetInstanceLookup(lookup entity.InstanceLookup) {
	p.instanceLookup = lookup
}

// SetReactionReader attaches a BufferedReactionsReader so that streaming
// payloads merge in-flight reaction count buffers before publishing (#647)。
// nil 配線時は DB の note.Reactions のみ反映 (旧挙動)。
func (p *NotePublisher) SetReactionReader(r entity.BufferedReactionsReader) {
	p.reactionReader = r
}

// SetFieldResolver attaches a NoteFieldResolver so that streaming payloads
// include Files / Channel / MyReaction equivalent to the REST GET path.
// Without it, fanout payloads carry an empty `files` array and frontend
// renders the note without attached media until the user reloads
// (#460/#461 follow-up). The resolver is shared with the REST handler
// path so call sites only construct one. nil 配線時は従来通り files 空。
func (p *NotePublisher) SetFieldResolver(r *entity.NoteFieldResolver) {
	p.fieldResolver = r
}

// PublishNote implements core/timeline.StreamingPublisher.
func (p *NotePublisher) PublishNote(topic string, n *model.Note, author *model.User) {
	if p.pub == nil || n == nil || author == nil {
		return
	}

	body := p.packNote(n, author)
	if body == nil {
		return
	}

	if err := p.pub.Publish(context.Background(), topic, json.RawMessage(body)); err != nil {
		slog.Warn("note publisher: publish failed", "topic", topic, "err", err)
	}
}

// packNote returns the serialized JSON for the note. フォロワーfanout時に
// 同じノートが繰返しpublishされるため、直前のノートIDが一致する場合は
// キャッシュを再利用して絵文字 / インスタンス解決の DB クエリを 1 回に抑える。
//
// author を n.User に差し戻してから entity.PackNoteWithInstance に渡すことで、
// Instance / emoji の解決が embed (Renote / Reply) にまで波及する。shallow
// copy なので元の model.Note は変更されない (Renote 等のポインタは共有)。
func (p *NotePublisher) packNote(n *model.Note, author *model.User) []byte {
	p.mu.Lock()
	defer p.mu.Unlock()

	if n.ID == p.cachedID && p.cachedBody != nil {
		return p.cachedBody
	}

	noteForPack := *n
	noteForPack.User = author

	// streaming publish は fanout/background 経路で request-scoped context を
	// 持たない。reactionReader への Redis call は短命なので
	// context.Background() で進める (#657)。将来 PublishNote signature に
	// ctx を追加するなら一緒に伝播させる。
	pn := entity.PackNoteWithInstance(context.Background(), &noteForPack, p.idGen, p.instanceLookup, p.emojiLookup, p.reactionReader)
	// REST 経路と同じ後段 resolver を通して Files / Channel を埋める。
	// viewer 引数は streaming fanout の性質上「自分宛て」が複数 subscriber
	// に展開されるため特定不能。viewer=nil で Apply を呼ぶと
	// MyReaction はスキップされ Files / Channel (= viewer-independent な
	// fields) のみ解決される。MyReaction は subscriber 側の
	// per-connection state に任せる。
	//
	// Apply は []NoteEntity (値型 slice) を受け取り内部で `&notes[i]`
	// 経由で要素を mutate するので、いったん 1-element slice にして
	// から書き戻さないと反映されない (Go の slice literal がコピーを
	// 作るため pn 自体は更新されない)。
	if p.fieldResolver != nil {
		batch := []entity.NoteEntity{pn}
		p.fieldResolver.Apply(batch, nil)
		pn = batch[0]
	}

	body, err := json.Marshal(pn)
	if err != nil {
		slog.Warn("note publisher: marshal failed", "err", err)
		return nil
	}
	p.cachedID = n.ID
	p.cachedBody = body
	return body
}

// NotificationUserRepo abstracts the FindByID call needed to pack a
// Notification's notifier. Narrow interface so we don't pull the whole
// repository package.
type NotificationUserRepo interface {
	FindByID(id string) (*model.User, error)
}

// NotificationNoteRepo abstracts the FindByIDWithRelations call needed to
// pack the referenced note (for reply/mention/reaction/renote/quote/
// pollEnded). Renote/Reply embed が要るので軽量版ではなく full 経路 (#425)。
type NotificationNoteRepo interface {
	FindByIDWithRelations(id string) (*model.Note, error)
}

// NotificationFollowingChecker abstracts the FollowingRepository.Exists call
// needed by Pack's visibility gate (#1471). Narrow interface so we don't pull
// the whole repository package into the stream layer (mirrors the
// NotificationUserRepo / NotificationNoteRepo pattern).
type NotificationFollowingChecker interface {
	Exists(followerID, followeeID string) (bool, error)
}

// NotificationPublisher serializes a notification.Notification and publishes
// it to the per-user `notifications:<id>` topic. Implements
// core/notification.StreamingPublisher. When userRepo / noteRepo are set
// the outbound JSON is packed via entity.PackNotification so the WebSocket
// body matches Misskey's NotificationEntityService.pack shape.
type NotificationPublisher struct {
	pub            PubSubPublisher
	userRepo       NotificationUserRepo
	noteRepo       NotificationNoteRepo
	followingRepo  NotificationFollowingChecker
	idGen          id.Generator
	instanceLookup entity.InstanceLookup
	emojiLookup    entity.EmojiLookup
}

// NewNotificationPublisher constructs a NotificationPublisher.
func NewNotificationPublisher(pub PubSubPublisher) *NotificationPublisher {
	return &NotificationPublisher{pub: pub}
}

// SetRepos wires the narrow repositories and id generator used by
// PublishNotification to pack user/note references. When unset, publish
// falls back to the raw Notification JSON.
func (p *NotificationPublisher) SetRepos(userRepo NotificationUserRepo, noteRepo NotificationNoteRepo, idGen id.Generator) {
	p.userRepo = userRepo
	p.noteRepo = noteRepo
	p.idGen = idGen
}

// SetInstanceLookup attaches an InstanceLookup so user.instance (used by
// frontend InstanceTicker for theme color / software name) is populated on
// notification streaming payloads (#415 follow-up).
func (p *NotificationPublisher) SetInstanceLookup(lookup entity.InstanceLookup) {
	p.instanceLookup = lookup
}

// SetEmojiLookup attaches an EmojiLookup so custom emoji shortcodes resolve
// to URLs in notification streaming payloads.
func (p *NotificationPublisher) SetEmojiLookup(lookup entity.EmojiLookup) {
	p.emojiLookup = lookup
}

// SetFollowingChecker attaches the follow relation checker used by Pack to
// gate note embeds against notifiee visibility (#1471). When unset, Pack
// falls back to a fail-closed branch for `followers` visibility (= note
// embed is dropped for non-author notifiees), mirroring core/note.CanSeeNote
// の `followingChecker == nil` semantics。
func (p *NotificationPublisher) SetFollowingChecker(c NotificationFollowingChecker) {
	p.followingRepo = c
}

// Pack implements core/notification.Packer. Returns the packed map shape
// when repos are wired, otherwise the raw Notification so callers can
// fall back to prior behaviour.
//
// notifieeID は packed body を受け取る viewer。followers / specified
// visibility の note は notifiee が CanSeeNote 相当の判定を満たさない
// 場合 note field を nil にして本文 leak を防ぐ (#1471 IDOR fix)。
// REST `i/notifications` の #1444 fix と対称: notification 行自体は残す
// が note detail を落とすことで非可視 viewer に「通知は来た / 本文は隠す」
// shape を実現する。notifieeID が空 (= Pack の呼び出し元が context を
// 渡し損ねた) ときも fail-closed で followers / specified note を落とす。
func (p *NotificationPublisher) Pack(notifieeID string, n *corenotification.Notification) any {
	if n == nil {
		return nil
	}
	if p.userRepo == nil || p.idGen == nil {
		return n
	}
	var user *model.User
	if n.NotifierID != "" {
		if u, err := p.userRepo.FindByID(n.NotifierID); err == nil {
			user = u
		}
	}
	var note *model.Note
	if n.NoteID != "" && p.noteRepo != nil {
		if n2, err := p.noteRepo.FindByIDWithRelations(n.NoteID); err == nil {
			// visibility gate (#1471): note embed を pack に通す前に
			// notifiee が見られるかを判定。core/note.CanSeeNote と同じ
			// 条件を、stream → repository への直接依存を避けるため inline
			// で再現する (NotificationUserRepo / NotificationNoteRepo と
			// 同じ design pattern)。
			if noteVisibleToNotifiee(notifieeID, n2, p.followingRepo) {
				note = n2
			}
		}
	}
	return entity.PackNotification(n, user, note, p.idGen, p.instanceLookup, p.emojiLookup)
}

// noteVisibleToNotifiee mirrors core/note.CanSeeNote: returns true when
// notifieeID is allowed to see n per its visibility level. Inlined here so
// the stream package does not need to import core/note + repository (matches
// the narrow-interface pattern used by NotificationUserRepo). 条件は
// CanSeeNote と一致させること。
func noteVisibleToNotifiee(notifieeID string, n *model.Note, follow NotificationFollowingChecker) bool {
	if n == nil {
		return false
	}
	switch n.Visibility {
	case model.NoteVisibilityPublic, model.NoteVisibilityHome:
		return true
	case model.NoteVisibilityFollowers:
		if notifieeID == "" {
			return false
		}
		if notifieeID == n.UserID {
			return true
		}
		if follow == nil {
			return false
		}
		ok, err := follow.Exists(notifieeID, n.UserID)
		if err != nil {
			return false
		}
		return ok
	case model.NoteVisibilitySpecified:
		if notifieeID == "" {
			return false
		}
		if notifieeID == n.UserID {
			return true
		}
		for _, id := range n.VisibleUserIDs {
			if id == notifieeID {
				return true
			}
		}
		return false
	}
	return false
}

// PublishNotification serializes the notification and publishes to
// notifications:<id>. Marshal 失敗 / publish 失敗は best-effort で握りつぶす。
// Pack() 経由で TS 互換 shape (repo配線時) またはraw Notification(未配線時)
// に変換して送る。
func (p *NotificationPublisher) PublishNotification(notifieeID string, n *corenotification.Notification) {
	if p.pub == nil || notifieeID == "" || n == nil {
		return
	}
	p.publishPacked(notifieeID, p.Pack(notifieeID, n))
}

// PublishPackedNotification publishes an already-packed body to
// notifications:<id>. Allows callers (e.g. core/notification.Service)
// that invoke Pack once and reuse the result across both the
// notifications stream and the main stream to avoid duplicate DB fetches.
func (p *NotificationPublisher) PublishPackedNotification(notifieeID string, packed any) {
	if p.pub == nil || notifieeID == "" || packed == nil {
		return
	}
	p.publishPacked(notifieeID, packed)
}

// publishPacked marshals payload and publishes to notifications:<id>.
func (p *NotificationPublisher) publishPacked(notifieeID string, payload any) {
	body, err := json.Marshal(payload)
	if err != nil {
		slog.Warn("notification publisher: marshal failed", "err", err)
		return
	}
	topic := "notifications:" + notifieeID
	if err := p.pub.Publish(context.Background(), topic, json.RawMessage(body)); err != nil {
		slog.Warn("notification publisher: publish failed", "topic", topic, "err", err)
	}
}

// DrivePublisher serializes a drive file event and publishes it to the
// per-user `drive:<id>` topic. Implements core/drive.StreamingPublisher.
type DrivePublisher struct {
	pub PubSubPublisher
}

// NewDrivePublisher constructs a DrivePublisher.
func NewDrivePublisher(pub PubSubPublisher) *DrivePublisher {
	return &DrivePublisher{pub: pub}
}

// PublishDriveEvent wraps the drive file in `{type, body}` envelope and
// publishes to drive:<userID>.
func (p *DrivePublisher) PublishDriveEvent(userID, eventType string, file *model.DriveFile) {
	if p.pub == nil || userID == "" || file == nil || eventType == "" {
		return
	}
	envelope := map[string]any{
		"type": eventType,
		"body": file,
	}
	body, err := json.Marshal(envelope)
	if err != nil {
		slog.Warn("drive publisher: marshal failed", "err", err)
		return
	}
	topic := "drive:" + userID
	if err := p.pub.Publish(context.Background(), topic, json.RawMessage(body)); err != nil {
		slog.Warn("drive publisher: publish failed", "topic", topic, "err", err)
	}
}

// PublishDriveFolderEvent wraps a folder life-cycle payload (packed folder
// for folderCreated/folderUpdated, the folder id string for folderDeleted)
// in the `{type, body}` envelope and publishes to drive:<userID> (#1564)。
func (p *DrivePublisher) PublishDriveFolderEvent(userID, eventType string, body any) {
	if p.pub == nil || userID == "" || eventType == "" || body == nil {
		return
	}
	envelope := map[string]any{
		"type": eventType,
		"body": body,
	}
	raw, err := json.Marshal(envelope)
	if err != nil {
		slog.Warn("drive publisher: folder event marshal failed", "err", err)
		return
	}
	topic := "drive:" + userID
	if err := p.pub.Publish(context.Background(), topic, json.RawMessage(raw)); err != nil {
		slog.Warn("drive publisher: folder event publish failed", "topic", topic, "err", err)
	}
}

// 静的アサーション: DrivePublisher が core/drive.StreamingPublisher と
// FolderStreamingPublisher を満たす。
var (
	_ coredrive.StreamingPublisher       = (*DrivePublisher)(nil)
	_ coredrive.FolderStreamingPublisher = (*DrivePublisher)(nil)
)

// 静的アサーション: NotificationPublisher が core/notification.StreamingPublisher
// と core/notification.Packer の両方を満たす。interface signature が
// drift した時点で compile errorで検知する。
var (
	_ corenotification.StreamingPublisher = (*NotificationPublisher)(nil)
	_ corenotification.Packer             = (*NotificationPublisher)(nil)
)
