package federation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/shiroha-a/mk/internal/activitypub"
	coreblocking "github.com/shiroha-a/mk/internal/core/blocking"
	corefollowing "github.com/shiroha-a/mk/internal/core/following"
	corenote "github.com/shiroha-a/mk/internal/core/note"
	corereaction "github.com/shiroha-a/mk/internal/core/reaction"
	corereversi "github.com/shiroha-a/mk/internal/core/reversi"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
)

// nowFn is the time source used by Processor when generating new note IDs.
// テストでの差し替えを容易にするため変数として保持する。
var nowFn = time.Now

// Errors returned by Processor.
var (
	// ErrUnsupportedActivity is returned when an activity type cannot be handled.
	ErrUnsupportedActivity = errors.New("unsupported activity type")
)

// RelayStatusMarker is the minimal interface needed from core/relay.Service
// for the inbox processor to toggle relay status on Accept / Reject
// activities whose id matches `/activities/follow-relay/{id}`.
type RelayStatusMarker interface {
	MarkAccepted(ctx context.Context, id string) error
	MarkRejected(ctx context.Context, id string) error
}

// Processor dispatches inbound activities to the right handler.
type Processor struct {
	resolver         *Resolver
	followingService *corefollowing.Service
	reactionService  *corereaction.Service
	noteDeleteSvc    *corenote.DeleteService
	userRepo         repository.UserRepository
	noteRepo         repository.NoteRepository

	// Block/Flag/Move/Add/Remove federation hooks.
	// SetBlockingService等で注入。nilの場合は対応activityがErrUnsupportedActivityを返す。
	blockingService *coreblocking.Service
	abuseReportRepo repository.AbuseReportRepository
	abuseIDGen      id.Generator
	pinningRepo     repository.UserNotePiningRepository
	pinningIDGen    id.Generator

	// Reversi federation hooks (Phase 9.7). All four are set via
	// SetReversi; if nil, reversi inbox types are treated as unsupported.
	reversiSvc      *corereversi.Service
	reversiRepo     repository.ReversiRepository
	reversiIDGen    id.Generator
	reversiFedCache *corereversi.FederationIDCache
	// reversiStreamPub: 招待受信時に recipient の `reversi:<id>` stream
	// に `invited` イベントを publish する (#417 P2 リアルタイム招待)。
	reversiStreamPub ReversiStreamPublisher

	// Relay follow-accept / -reject hook. nil 時は relay 特化処理をスキップ。
	relayMarker RelayStatusMarker

	// Chat federation (CherryPick互換)
	chatService ChatMessageReceiver

	// Timeline fanout hook for remote notes (#330). ローカルノートは
	// noteCreateService経由でfanoutされるが、リモートノートはIngestNote/
	// handleAnnounce経由でDB直挿入されるためここで明示的にfanoutする。
	fanoutHook TimelineFanoutHook

	// Notification hook for inbound Create / Announce (#415)。fanoutHook と
	// 同じ位置で呼び出して reply / renote / quote / mention 通知を生成する。
	// 配線されていなければ no-op。
	notificationHook NotificationHook

	// localBaseURL はローカルユーザーのcanonical URI prefix
	// (例: "https://go.k7a.org") を保持する。inboxで受信したactivityの
	// object が "{localBaseURL}/users/{id}" 形式のとき、ローカルユーザーを
	// ID で lookup するために使用する (ローカルユーザーの user.uri は NULL
	// なので FindByURI では解決できない)。
	localBaseURL string

	// inboundFollowAcceptor は inbound Follow に対して Accept activity を
	// 送り返す。original Follow のID (リモート側のURL) を参照しないと相手が
	// Acceptをマッチングできないため、processorが受け取った raw activity を
	// そのまま Accept の object に包んで送る。nil なら何もしない (locked
	// follow 経路ではここを通らないのでOK)。
	inboundFollowAcceptor InboundFollowAcceptor
}

// InboundFollowAcceptor delivers an Accept activity in response to an inbound
// Follow. The raw Follow (as received on our inbox) is wrapped as the inner
// object so the remote server can match the Accept to the original Follow id.
type InboundFollowAcceptor interface {
	SendAcceptForInboundFollow(follower, followee *model.User, originalFollow json.RawMessage) error
}

// SetInboundFollowAcceptor wires the sender that delivers Accept activities
// for inbound Follow activities targeting local users.
func (p *Processor) SetInboundFollowAcceptor(a InboundFollowAcceptor) {
	p.inboundFollowAcceptor = a
}

// SetLocalBaseURL configures the local instance's base URL. This is used to
// detect whether an inbound activity's object refers to a local user.
func (p *Processor) SetLocalBaseURL(baseURL string) {
	p.localBaseURL = baseURL
}

// resolveTargetUser looks up a user referenced by URI in an inbound activity.
// ローカルユーザの user.uri は DB 上 NULL なので FindByURI では解決できない。
// 対策として localBaseURL 配下の URI ("{baseURL}/users/{id}") を検出し、
// ID パートを抜き出して FindByID で lookup する。リモートユーザーは従来通り
// FindByURI で解決する。
func (p *Processor) resolveTargetUser(uri string) (*model.User, error) {
	if p.localBaseURL != "" {
		prefix := p.localBaseURL + "/users/"
		if id, ok := strings.CutPrefix(uri, prefix); ok {
			// id の中に "/" が残る (例: "/outbox") ケースがあるので切り落とす。
			if slash := strings.IndexByte(id, '/'); slash >= 0 {
				id = id[:slash]
			}
			if id != "" {
				return p.userRepo.FindByID(id)
			}
		}
	}
	return p.userRepo.FindByURI(uri)
}

// ChatMessageReceiver handles inbound Misskey:ChatMessage activities.
type ChatMessageReceiver interface {
	CreateMessageViaAP(ctx context.Context, uri string, fromUser *model.User, toUserID, text string) (*model.ChatMessage, error)
}

// TimelineFanoutHook is invoked after a remote note has been persisted so
// that timeline/streaming subscribers receive the note. パッケージ間の循環
// 依存を避けるためinterfaceで受け取る (実装は core/timeline.FanoutHook)。
type TimelineFanoutHook interface {
	OnNoteCreated(note *model.Note, author *model.User)
}

// NotificationHook is invoked after an inbound Create / Announce so that
// reply / renote / quote / mention 通知が local な被通知者に対して生成される。
// ローカル作成と同じ interface を使う (実装は core/notification.Hook)。
// notifyLocalUser 側で notifiee が local かどうかを判定するため、ここでは
// 常に fire-and-forget で呼び出せばよい。
type NotificationHook interface {
	OnNoteCreated(note *model.Note, author *model.User, replyTarget, renoteTarget *model.Note)
}

// NewProcessor constructs a Processor. reactionService / noteDeleteSvc は省略
// 可能 (nil)。nil の場合、対応する activity 種別は ErrUnsupportedActivity を返す。
func NewProcessor(
	resolver *Resolver,
	followingService *corefollowing.Service,
	reactionService *corereaction.Service,
	noteDeleteSvc *corenote.DeleteService,
	userRepo repository.UserRepository,
	noteRepo repository.NoteRepository,
) *Processor {
	return &Processor{
		resolver:         resolver,
		followingService: followingService,
		reactionService:  reactionService,
		noteDeleteSvc:    noteDeleteSvc,
		userRepo:         userRepo,
		noteRepo:         noteRepo,
	}
}

// genericActivity is the minimum struct used by the dispatcher to read the
// activity type and actor.
type genericActivity struct {
	Type   string          `json:"type"`
	Actor  string          `json:"actor"`
	Object json.RawMessage `json:"object"`
	ID     string          `json:"id"`
	// Published は Announce などで activity 自体の発火時刻を表す (#940)。
	// 遅延配送された Renote の timeline 並びを origin と揃えるために使う。
	Published string `json:"published"`
	// To は reversi Invite の配送先を読むために保持する。string と []string
	// 両方を受け入れるため RawMessage で保持し、必要な時点で解釈する。
	To json.RawMessage `json:"to"`
	// raw はアクテ���ビティ全体の生JSON。handleLike 等で content や
	// _misskey_reaction な�� genericActivity に含まれないフィールドを
	// 読むために保持する。
	raw json.RawMessage
}

// Process consumes a JSON activity body received in an inbox. Returns nil if
// the activity is accepted (whether or not it produced side effects). Returns
// ErrUnsupportedActivity for types that are accepted but currently not
// processed (callers should still acknowledge the request to the sender).
//
// 入力 JSON は最初に activitypub.Normalize を通してキー名を canonical な短形式
// に揃えてから dispatch する。これにより `as:type` / `https://www.w3.org/ns/
// activitystreams#type` / `@type` のいずれでも同じ struct フィールドにマップ
// される。
//
// **Idempotency invariant (#534)**: each activity handler MUST be idempotent so
// that the inbox queue worker can safely retry on transient failure and so
// that out-of-order delivery (e.g. Update before Create due to federation
// hop reordering) eventually converges. Verified at the time of #534:
//
//   - Follow / Accept: swallow ErrAlreadyFollowing / ErrAlreadyRequested /
//     ErrRequestNotFound (so re-deliveries are no-ops)
//   - Undo Follow / Like / Block: swallow ErrNotFollowing / ErrReactionNotFound /
//     ErrNotBlocking
//   - Like / Block: swallow ErrAlreadyReacted / ErrAlreadyBlocking
//   - Create / Announce: dedup via note URI lookup before insert
//   - Delete: treat "target not found" as success
//   - Update Note / Person: overwrite semantics (last-write-wins). 後着
//     Update が先着 Update を上書きしても peer が次回 fetch した値で
//     収束するので一時的な不整合は許容
//
// New activity handlers added below MUST preserve this invariant; otherwise
// retried inbox jobs will produce duplicate side effects (notifications
// fired twice, counters double-incremented, etc).
func (p *Processor) Process(body []byte) error {
	normalized, err := activitypub.Normalize(body)
	if err != nil {
		return fmt.Errorf("invalid activity json: %w", err)
	}
	body = normalized

	// Normalize は内部で json.Marshal して再エンコードするため、ここでの
	// Unmarshal は構文エラーで失敗しないことが保証される。
	var act genericActivity
	_ = json.Unmarshal(body, &act)
	act.raw = body
	if act.Actor == "" {
		return errors.New("activity missing actor")
	}

	switch strings.ToLower(act.Type) {
	case "follow":
		return p.handleFollow(act)
	case "undo":
		return p.handleUndo(act)
	case "accept":
		return p.handleAccept(act)
	case "create":
		return p.handleCreate(act)
	case "like":
		return p.handleLike(act)
	case "announce":
		return p.handleAnnounce(act)
	case "delete":
		return p.handleDelete(act)
	case "update":
		return p.handleUpdate(act)
	case "reject":
		return p.handleReject(act)
	case "block":
		return p.handleBlock(act)
	case "flag":
		return p.handleFlag(act)
	case "move":
		return p.handleMove(act)
	case "add":
		return p.handleAdd(act)
	case "remove":
		return p.handleRemove(act)
	case "emojireaction", "emojireact":
		return p.handleLike(act)
	case "invite":
		// 非 reversi (Group Invite 等) は未対応扱いで 202 を返させる。
		// reversi Game object 以外の Invite をここで 400 にすると relay
		// 以外の peer との互換性が崩れる (#417 P4 Devin review)。
		if err := p.handleReversiInvite(act); err != nil {
			if errors.Is(err, ErrNotReversiGame) {
				return ErrUnsupportedActivity
			}
			return err
		}
		return nil
	case "join":
		return p.handleReversiJoin(act)
	case "leave":
		return p.handleReversiLeave(act)
	case "misskey:chatmessage":
		return p.handleChatMessage(act)
	}
	return ErrUnsupportedActivity
}

// SetBlockingService wires the blocking service for Block/Undo Block activities.
func (p *Processor) SetBlockingService(svc *coreblocking.Service) {
	p.blockingService = svc
}

// SetAbuseReportRepo wires the abuse report repository for Flag activities.
func (p *Processor) SetAbuseReportRepo(repo repository.AbuseReportRepository, idGen id.Generator) {
	p.abuseReportRepo = repo
	p.abuseIDGen = idGen
}

// SetPinningRepo wires the note pinning repository for Add/Remove activities.
func (p *Processor) SetPinningRepo(repo repository.UserNotePiningRepository, idGen id.Generator) {
	p.pinningRepo = repo
	p.pinningIDGen = idGen
}

// SetRelayMarker wires a RelayStatusMarker so inbound Accept / Reject
// activities whose id matches the follow-relay URI pattern can flip
// the corresponding relay row to accepted / rejected.
func (p *Processor) SetRelayMarker(m RelayStatusMarker) {
	p.relayMarker = m
}

// SetChatService wires a ChatMessageReceiver for inbound Misskey:ChatMessage
// activities (CherryPick v12 federation).
func (p *Processor) SetChatService(svc ChatMessageReceiver) {
	p.chatService = svc
}

// SetFanoutHook wires a TimelineFanoutHook so that remote notes ingested via
// handleCreate / handleAnnounce get pushed to timeline caches and streaming
// subscribers (#330).
func (p *Processor) SetFanoutHook(h TimelineFanoutHook) {
	p.fanoutHook = h
}

// SetNotificationHook wires a NotificationHook so that inbound Create /
// Announce activities generate reply / renote / quote / mention 通知 for
// local notifiees (#415). Without it, remote users が local note を renote /
// reply / mention しても通知が飛ばない (ローカル作成だけが note_create_service
// 経由で通知を作る状態になる)。
func (p *Processor) SetNotificationHook(h NotificationHook) {
	p.notificationHook = h
}

// followRelayIDPattern extracts the relay id embedded in
// `.../activities/follow-relay/{id}` URIs. Must stay in lockstep with
// activitypub.URLBuilder.FollowRelayURI.
var followRelayIDPattern = regexp.MustCompile(`/activities/follow-relay/([\w-]+)$`)

// matchFollowRelayID returns the relay id if uri is a follow-relay URI,
// otherwise the empty string.
func matchFollowRelayID(uri string) string {
	m := followRelayIDPattern.FindStringSubmatch(uri)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

// SetReversi wires the reversi federation dependencies. When any of the
// arguments is nil, reversi inbox activities fall through as unsupported
// (useful for tests that do not care about reversi).
func (p *Processor) SetReversi(
	svc *corereversi.Service,
	repo repository.ReversiRepository,
	idGen id.Generator,
	fedCache *corereversi.FederationIDCache,
) {
	p.reversiSvc = svc
	p.reversiRepo = repo
	p.reversiIDGen = idGen
	p.reversiFedCache = fedCache
}

// SetReversiStreamPublisher wires a per-user reversi stream publisher used
// by handleReversiInvite to push `invited` events in real-time (#417 P2)。
func (p *Processor) SetReversiStreamPublisher(pub ReversiStreamPublisher) {
	p.reversiStreamPub = pub
}

// handleFollow processes an inbound Follow activity.
func (p *Processor) handleFollow(act genericActivity) error {
	follower, err := p.resolver.ResolveActor(act.Actor)
	if err != nil {
		return err
	}
	followeeURI, err := readObjectString(act.Object)
	if err != nil {
		return err
	}
	followee, err := p.resolveTargetUser(followeeURI)
	if err != nil {
		return errors.New("unknown followee")
	}
	result, err := p.followingService.Follow(follower.ID, followee.ID)
	alreadyFollowing := errors.Is(err, corefollowing.ErrAlreadyFollowing)
	// ErrAlreadyRequested はlocked followeeへの再送。既に FollowRequest が
	// 存在するので何もしなくて良い (Accept は承認時に送られる)。エラーとして
	// 上に返すと inbox が 4xx を返して相手が retry を続けるので swallow する。
	alreadyRequested := errors.Is(err, corefollowing.ErrAlreadyRequested)
	if err != nil && !alreadyFollowing && !alreadyRequested {
		return err
	}
	// Accept を返送するのは Following が成立した (新規 or 既存) 場合のみ。
	// 既にフォローされている (ErrAlreadyFollowing) 場合も Accept を再送する:
	// 相手サーバーが Accept を見逃していた or retry で新規 Follow を
	// 送り直した状況で、我々が何も返さないと相手は永遠に pending のままに
	// なる (idempotent な Accept 再送で解消する)。original Follow の raw を
	// そのまま object に包むことでリモート側が送った Follow と matching
	// できる。FollowRequest 止まり (locked followee) の場合は acceptor を
	// 呼ばない (Accept は明示承認後)。
	shouldAccept := alreadyFollowing || (result != nil && result.Following != nil)
	if shouldAccept && p.inboundFollowAcceptor != nil {
		if err := p.inboundFollowAcceptor.SendAcceptForInboundFollow(follower, followee, act.raw); err != nil {
			slog.Warn("inbound follow accept delivery failed",
				"follower", follower.ID, "followee", followee.ID, "err", err)
		}
	}
	return nil
}

// handleUndo processes an Undo activity wrapping a Follow / Like / Announce.
func (p *Processor) handleUndo(act genericActivity) error {
	var inner genericActivity
	if err := json.Unmarshal(act.Object, &inner); err != nil {
		return fmt.Errorf("invalid undo object: %w", err)
	}
	switch strings.ToLower(inner.Type) {
	case "follow":
		return p.handleUndoFollow(act, inner)
	case "like":
		return p.handleUndoLike(act, inner)
	case "announce":
		return p.handleUndoAnnounce(act, inner)
	case "block":
		return p.handleUndoBlock(act, inner)
	case "emojireaction", "emojireact":
		return p.handleUndoLike(act, inner)
	case "invite":
		// CherryPick が pre-start reversi 招待を取り消す際に Undo(Invite) を
		// 送ってくる (#417 P4)。inner.Object が reversi Game なら reversi
		// 側で処理、それ以外 (例: Group Invite) は未対応扱いにして inbox
		// が 202 を返すようにする (400 にすると peer 側の配送 retry を
		// 招いてしまう)。
		if err := p.handleReversiUndoInvite(act, inner); err != nil {
			if errors.Is(err, ErrNotReversiGame) {
				return ErrUnsupportedActivity
			}
			return err
		}
		return nil
	}
	return ErrUnsupportedActivity
}

// handleUndoFollow undoes a previously created Follow.
func (p *Processor) handleUndoFollow(act genericActivity, inner genericActivity) error {
	follower, err := p.resolver.ResolveActor(act.Actor)
	if err != nil {
		return err
	}
	followeeURI, err := readObjectString(inner.Object)
	if err != nil {
		return err
	}
	followee, err := p.resolveTargetUser(followeeURI)
	if err != nil {
		return errors.New("unknown followee")
	}
	if err := p.followingService.Unfollow(follower.ID, followee.ID); err != nil {
		if errors.Is(err, corefollowing.ErrNotFollowing) {
			return nil
		}
		return err
	}
	return nil
}

// handleUndoLike removes a reaction previously added via Like.
//
// Note: reversi game session URI を対象とした Undo(EmojiReaction) は
// handleLike と対称に 202 ack で握り潰す (#417 P5)。dispatch 順も handleLike
// と揃えており、ResolveActor を先頭で実行することで actor cache の populate
// が両 path で同様に行われるようにする (#417 P5 Devin review)。
// reactionService nil の本番 config は無いので unconditional な ResolveActor
// で実害なし。
func (p *Processor) handleUndoLike(act genericActivity, inner genericActivity) error {
	reactor, err := p.resolver.ResolveActor(act.Actor)
	if err != nil {
		return err
	}
	targetURI, err := readObjectString(inner.Object)
	if err != nil {
		// inner.object 欠如した malformed Undo(Like) は handleLike と同じく
		// 202 ack 扱い (旧実装は reactionService nil 経路で ErrUnsupportedActivity
		// を返していた)。
		return ErrUnsupportedActivity
	}
	if corereversi.IsReversiGameSessionURI(targetURI) {
		return nil
	}
	if p.reactionService == nil {
		return ErrUnsupportedActivity
	}
	target, err := p.resolver.ResolveNote(targetURI)
	if err != nil {
		return err
	}
	if err := p.reactionService.Delete(reactor, target.ID); err != nil {
		// 既に削除済みは許容
		if errors.Is(err, corereaction.ErrReactionNotFound) {
			return nil
		}
		return err
	}
	return nil
}

// handleUndoAnnounce removes a previously created Announce (renote).
// inner.Object には元ノートの URI を持つことを想定する。
func (p *Processor) handleUndoAnnounce(act genericActivity, inner genericActivity) error {
	announcer, err := p.resolver.ResolveActor(act.Actor)
	if err != nil {
		return err
	}
	targetURI, err := readObjectString(inner.Object)
	if err != nil {
		return err
	}
	target, err := p.resolver.ResolveNote(targetURI)
	if err != nil {
		return err
	}
	// announcer が pure renote を 1 件でも持っていれば削除する。複数あった場合
	// は最新の 1 件のみで十分 (本家 misskey の挙動と同じ)。
	renotes, err := p.noteRepo.ListRenotesOf(target.ID, "", "", 50)
	if err != nil {
		return err
	}
	for _, n := range renotes {
		if n.UserID != announcer.ID {
			continue
		}
		if !corenote.IsPureRenote(n) {
			continue
		}
		if err := p.noteRepo.Delete(n); err != nil {
			return err
		}
		_ = p.noteRepo.IncrementCount(target.ID, "renoteCount", -1)
		return nil
	}
	return nil
}

// handleAccept processes an inbound Accept activity. リモートfolloweeがローカル
// followerからのフォローリクエストを承認した場合に、フォロー関係を確立する。
// inner.id が follow-relay パターンにマッチすれば relay の Accept とみなし、
// RelayStatusMarker.MarkAccepted を呼び出す (upstream ApInboxService 互換)。
func (p *Processor) handleAccept(act genericActivity) error {
	var inner genericActivity
	if err := json.Unmarshal(act.Object, &inner); err != nil {
		// objectが文字列（Follow IDのURI）の場合もあるが、現状はnilで許容
		return nil
	}
	if !strings.EqualFold(inner.Type, "follow") {
		return nil
	}
	if relayID := matchFollowRelayID(inner.ID); relayID != "" && p.relayMarker != nil {
		return p.relayMarker.MarkAccepted(context.Background(), relayID)
	}
	// actorはリモートfollowee（承認した側）
	followee, err := p.resolver.ResolveActor(act.Actor)
	if err != nil {
		return err
	}
	// inner.actorはローカルfollower（フォローを申請した側）
	followerURI, err := readActorString(inner)
	if err != nil {
		return nil
	}
	// ローカルユーザーはURIカラムがNULLなのでFindByURIでは見つからない。
	// ローカルURI（/users/{id}）からIDを抽出してFindByIDで検索する。
	var follower *model.User
	if localID := p.resolver.ExtractLocalUserID(followerURI); localID != "" {
		follower, err = p.userRepo.FindByID(localID)
	} else {
		follower, err = p.userRepo.FindByURI(followerURI)
	}
	if err != nil {
		return nil
	}
	// フォローリクエストを承認してフォロー関係を確立
	if err := p.followingService.AcceptRequest(followee.ID, follower.ID); err != nil {
		// フォローリクエストが存在しない場合は無視（既にフォロー済み等）
		if errors.Is(err, corefollowing.ErrRequestNotFound) {
			return nil
		}
		return err
	}
	return nil
}

// handleCreate persists an inbound Note or Question carried by a Create activity.
// Question (投票) はNote同様にIngestNoteで処理され、Pollレコードが自動作成される。
// ノート取込み成功後、fanoutHookが配線されていればタイムライン/ストリーミングに
// 配信する (#330)。
//
// `_misskey_talk: true` flag が立った Note は CherryPick / レガシー Misskey の
// 1-on-1 chat federation。notes テーブルではなく chat_messages として処理する
// ため IngestNote をスキップして chatService にルートする (#692)。
func (p *Processor) handleCreate(act genericActivity) error {
	actor, err := p.resolver.ResolveActor(act.Actor)
	if err != nil {
		return err
	}
	// Note の `_misskey_talk` を覗き見て chat か通常 note かを分岐する。
	// IngestNote が走る前に短絡しないと chat message が notes テーブルに
	// 流れ込んでタイムラインを汚す。
	if p.chatService != nil {
		var probe struct {
			Type        string          `json:"type"`
			ID          string          `json:"id"`
			Content     string          `json:"content"`
			MisskeyTalk bool            `json:"_misskey_talk"`
			To          json.RawMessage `json:"to"`
		}
		if err := json.Unmarshal(act.Object, &probe); err == nil && probe.MisskeyTalk && probe.Type == "Note" {
			return p.handleChatCreate(actor, probe.ID, probe.Content, probe.To)
		}
	}
	note, err := p.resolver.IngestNote(act.Object)
	if err != nil {
		return err
	}
	if note != nil {
		// IngestNoteが既存ノートを返した場合 (重複) でもfanout自体は
		// べき等なので呼んで問題ない。authorはnoteに紐付くUserを使うが、
		// IngestNoteはUser fieldを設定しないためactorを使う。
		// Renote/Reply relation は IngestNote 直後では未ロード (ID だけ) な
		// ので、ストリーミング payload で renote 先が null にならないように
		// preload 付きで再取得する (#416)。
		//
		// Reply / Renote が無い「素の Create(Note)」(連合の大半を占める一般
		// 投稿) では preload する relation が無いので reload を skip する
		// (#569)。User 関係だけ caller の actor から手で詰める。これにより
		// inbox worker drain time の支配的コストの 1 つだった redundant
		// SELECT が消える。
		hydrated := note
		if note.ReplyID != nil || note.RenoteID != nil {
			hydrated = hydrateNoteForFanout(p.noteRepo, note)
		} else if note.User == nil {
			note.User = actor
		}
		// fanoutHook / notificationHook はベストエフォートで、local note
		// create 側 (note_create_service) では既に safeGo で非同期発火して
		// いる。federation 側もこれに揃え、worker が Redis LPUSH / publish
		// 待ちで block されないようにする (#569)。順序保証は Misskey TS と
		// 同じく各 hook 側の冪等性で吸収する。
		if p.fanoutHook != nil {
			safeGoFedHook(func() { p.fanoutHook.OnNoteCreated(hydrated, actor) })
		}
		if p.notificationHook != nil {
			safeGoFedHook(func() {
				// reply / quote / mention 通知を local notifiee に対して
				// 生成する。hydrated.Reply / hydrated.Renote は preload
				// 済みなので、notification hook 側で UserID を取れる
				// (#415)。
				p.notificationHook.OnNoteCreated(hydrated, actor, hydrated.Reply, hydrated.Renote)
			})
		}
	}
	return nil
}

// safeGoFedHook runs fn in a new goroutine, recovering from panics. local
// note service の safeGo と同じ振る舞い (`note_create_service.go`)。
func safeGoFedHook(fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("recovered panic in federation best-effort hook", "panic", r)
			}
		}()
		fn()
	}()
}

// hydrateNoteForFanout reloads a freshly-created note via
// FindByIDWithRelations so that Renote/Reply relations are preloaded before
// handing it to the fanout hook. reload が失敗した場合は元の note をその
// まま返す (best-effort)。
func hydrateNoteForFanout(repo interface {
	FindByIDWithRelations(id string) (*model.Note, error)
}, note *model.Note) *model.Note {
	if repo == nil || note == nil {
		return note
	}
	if loaded, err := repo.FindByIDWithRelations(note.ID); err == nil && loaded != nil {
		return loaded
	}
	return note
}

// handleLike attaches a reaction to a local note based on a Like activity.
//
// Note: P5 (#417) で reversi 早期分岐を入れた都合上、reactionService nil
// チェックを reversi 分岐の後に移動している。副作用として:
//   - ResolveActor が無条件で呼ばれる (旧実装は reactionService nil 時に
//     早期 return していた)。reversi 分岐側でも actor 解決は必要なので
//     妥当な変更。reactionService が wire されていない config は production
//     には無いので実害なし (#417 P5 Devin review)。
//   - object が malformed (string でも nested object でもない) Like は
//     reactionService の有無に関わらず ErrUnsupportedActivity (= 202 ack)
//     を返す。旧実装は reactionService 配線時に readObjectString error を
//     400 として返していたが、404 / 400 で peer の retry を誘発するより
//     202 で握り潰すほうが連合衛生的に望ましい (#417 P5 Devin review)。
func (p *Processor) handleLike(act genericActivity) error {
	reactor, err := p.resolver.ResolveActor(act.Actor)
	if err != nil {
		return err
	}
	// アクティビティ全体を Like として解釈し content/_misskey_reaction を取得する。
	// object は URI 文字列の場合とネストされた Like オブジェクトの場合がある。
	var like activitypub.Like
	_ = json.Unmarshal(act.raw, &like)
	if like.Object == "" {
		// object が文字列の場合: Activity.Object は json:"-" でスキップされるため
		// act.Object (RawMessage) から直接読む
		uri, rerr := readObjectString(act.Object)
		if rerr != nil {
			return ErrUnsupportedActivity
		}
		like.Object = uri
	}
	reaction := like.Content
	if like.MisskeyReaction != "" {
		reaction = like.MisskeyReaction
	}
	// reversi game session URI (`/games/{UUID}/{sessionID}`) は CherryPick
	// 拡張の reaction 連合。純正 Misskey フロントは `reacted` を表示する UI を
	// 持たないので mk-go では state 変化させず 202 ack だけ返す (#417 P5)。
	// resolver.ResolveNote が 404 で失敗するのを避けるため Note Like より先に
	// 弾く必要がある。
	if corereversi.IsReversiGameSessionURI(like.Object) {
		// reactor / reaction は note Like 経路で使われるので blank assign 不要。
		// actor 解決は handleLike 冒頭で済ませているのでキャッシュ取込の
		// 副作用はこの早期 return でも残る。
		return nil
	}
	if p.reactionService == nil {
		return ErrUnsupportedActivity
	}
	target, err := p.resolver.ResolveNote(like.Object)
	if err != nil {
		return err
	}
	// Like activity に乗ってきたカスタム絵文字メタデータを emoji table に
	// upsert する。Misskey TS の apNoteService.extractEmojis(...) 相当
	// (#459)。reactionService.Create 自体は emoji 不在でも reaction 文字列
	// を保存できるので、upsert 失敗で Create を止める必要はなく、UI 表示
	// 側の reactionEmojis 解決が FallbackReaction (heart) に落ちる程度。
	if reactor.Host != nil && len(like.Tag) > 0 {
		p.resolver.upsertEmojis(extractEmojiTags(like.Tag), *reactor.Host)
	}
	if _, err := p.reactionService.Create(reactor, target.ID, reaction); err != nil {
		if errors.Is(err, corereaction.ErrAlreadyReacted) {
			return nil
		}
		return err
	}
	return nil
}

// handleAnnounce creates a renote pointing at the announced note.
func (p *Processor) handleAnnounce(act genericActivity) error {
	announcer, err := p.resolver.ResolveActor(act.Actor)
	if err != nil {
		return err
	}
	targetURI, err := readObjectString(act.Object)
	if err != nil {
		return err
	}
	target, err := p.resolver.ResolveNote(targetURI)
	if err != nil {
		return err
	}
	// announce 自身に id があれば URI として保存し、重複検出にも使う
	if act.ID != "" {
		if _, err := p.noteRepo.FindByURI(act.ID); err == nil {
			return nil
		}
	}
	// 遅延配送 Announce では activity.published を採用して timeline 並びを
	// origin と揃える (#940)。空 / parse 不可なら nowFn にフォールバック。
	now := parseAPPublishedTime(act.Published, nowFn())
	renote := &model.Note{
		ID:         p.resolver.idGen.Generate(now),
		UserID:     announcer.ID,
		UserHost:   announcer.Host,
		RenoteID:   &target.ID,
		Visibility: model.NoteVisibilityPublic,
	}
	if act.ID != "" {
		uri := act.ID
		renote.URI = &uri
	}
	renote.RenoteUserID = &target.UserID
	renote.RenoteUserHost = target.UserHost
	if err := p.noteRepo.Create(renote); err != nil {
		return err
	}
	_ = p.noteRepo.IncrementCount(target.ID, "renoteCount", 1)
	hydrated := hydrateNoteForFanout(p.noteRepo, renote)
	if p.fanoutHook != nil {
		p.fanoutHook.OnNoteCreated(hydrated, announcer)
	}
	if p.notificationHook != nil {
		// remote user が local note を renote した時に元投稿者へ通知を出す
		// (#415)。target が renoteTarget。reply 通知は Announce では発生
		// しないので nil を渡す。
		p.notificationHook.OnNoteCreated(hydrated, announcer, nil, target)
	}
	return nil
}

// handleDelete removes a remote note (or actor) referenced by a Delete activity.
func (p *Processor) handleDelete(act genericActivity) error {
	author, err := p.resolver.ResolveActor(act.Actor)
	if err != nil {
		return err
	}
	targetURI, err := readObjectString(act.Object)
	if err != nil {
		return err
	}
	// Actor 自身の Delete (アカウント削除) は現在未対応 — 受信は許容して no-op。
	if author.URI != nil && *author.URI == targetURI {
		return nil
	}
	note, err := p.noteRepo.FindByURI(targetURI)
	if err != nil {
		// 既に存在しないなら成功扱い
		return nil
	}
	if note.UserID != author.ID {
		return errors.New("delete from non-author")
	}
	if p.noteDeleteSvc != nil {
		return p.noteDeleteSvc.Delete(author, note.ID)
	}
	return p.noteRepo.Delete(note)
}

// handleUpdate refreshes a remote actor's stored profile fields, or applies
// an inbound Update Note activity from a federated peer.
//
// Misskey 本家にはノート編集 API が無いが、Mastodon 系の Update Note は受信
// するだけ受信して反映する (`Resolver.UpdateRemoteNote` 経由)。それ以外の
// type (Question / Article 等) は no-op。
// Reversi Game 型の Update (CherryPick 拡張) はこの中で分岐する。
func (p *Processor) handleUpdate(act genericActivity) error {
	// 先に object の type を覗いて Note / Person / Game の判定を行う。
	objectType := peekObjectType(act.Object)
	if strings.EqualFold(objectType, "game") {
		// Game オブジェクトは reversi 拡張専用。未ワイヤなら skip。
		if !p.reversiReady() {
			return nil
		}
		if err := p.handleReversiUpdate(act); err != nil {
			if errors.Is(err, ErrNotReversiGame) {
				return nil
			}
			return err
		}
		return nil
	}
	if strings.EqualFold(objectType, "note") {
		_, err := p.resolver.UpdateRemoteNote(act.Object)
		// ErrInvalidNote は受信側の不備として skip 扱い (200 を返す)。
		if errors.Is(err, ErrInvalidNote) {
			return nil
		}
		return err
	}

	// object が単なる URI なら fetch、object 内に Person があればそれを使う
	var person activitypub.Person
	if err := json.Unmarshal(act.Object, &person); err != nil || person.ID == "" {
		// object が文字列 URI のケース
		uri, rerr := readObjectString(act.Object)
		if rerr != nil {
			return rerr
		}
		person.ID = uri
	}
	if person.Type != "" && !strings.EqualFold(person.Type, "person") {
		// Question / Article 等は未対応
		return nil
	}
	user, err := p.userRepo.FindByURI(person.ID)
	if err != nil {
		// 未取得のリモートユーザーなら無視 (次回 follow/inbox などで取り込まれる)
		return nil
	}
	fields := map[string]any{}
	if person.Name != "" {
		name := person.Name
		fields["name"] = &name
	}
	if len(fields) == 0 {
		return nil
	}
	return p.userRepo.UpdateUser(user.ID, fields)
}

// peekObjectType reads only the "type" field from a JSON object body. パース
// 不能・object でない・type フィールド無しの場合は空文字を返す。
func peekObjectType(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var obj struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return ""
	}
	return obj.Type
}

// handleReject undoes a Follow that was rejected by the followee.
//
// 想定: 自分 (ローカル follower) が remote followee に対して Follow を送ったが
// 拒否された場合に呼ばれる。inner.Follow.actor がローカル follower、object が
// remote followee。inner.id が follow-relay パターンなら relay 関連として
// RelayStatusMarker.MarkRejected を呼ぶ。
func (p *Processor) handleReject(act genericActivity) error {
	var inner genericActivity
	if err := json.Unmarshal(act.Object, &inner); err != nil {
		return fmt.Errorf("invalid reject object: %w", err)
	}
	if !strings.EqualFold(inner.Type, "follow") {
		return ErrUnsupportedActivity
	}
	if relayID := matchFollowRelayID(inner.ID); relayID != "" && p.relayMarker != nil {
		return p.relayMarker.MarkRejected(context.Background(), relayID)
	}
	followee, err := p.resolver.ResolveActor(act.Actor)
	if err != nil {
		return err
	}
	followerURI, err := readActorString(inner)
	if err != nil {
		return err
	}
	// ローカルユーザーは user.uri が NULL なので resolveTargetUser で ID
	// 解決する。これをやらないと FindByURI が fail して reject が silent drop
	// されてしまい、ローカル側の FollowRequest が消えずに永遠に pending の
	// ままになる。
	follower, err := p.resolveTargetUser(followerURI)
	if err != nil {
		return nil
	}
	// 既存のフォローがあれば解除する。pending な follow request も同様。
	if err := p.followingService.Unfollow(follower.ID, followee.ID); err != nil &&
		!errors.Is(err, corefollowing.ErrNotFollowing) {
		return err
	}
	if err := p.followingService.CancelRequest(follower.ID, followee.ID); err != nil &&
		!errors.Is(err, corefollowing.ErrRequestNotFound) {
		return err
	}
	return nil
}

// handleBlock processes an inbound Block activity. リモートユーザーがローカル
// ユーザーをブロックした場合にブロック関係を作成する。
func (p *Processor) handleBlock(act genericActivity) error {
	if p.blockingService == nil {
		return ErrUnsupportedActivity
	}
	blocker, err := p.resolver.ResolveActor(act.Actor)
	if err != nil {
		return err
	}
	blockeeURI, err := readObjectString(act.Object)
	if err != nil {
		return err
	}
	// ローカルユーザーは user.uri が NULL なので FindByURI では解決できない。
	// resolveTargetUser が localBaseURL prefix パターンから ID を抽出する。
	blockee, err := p.resolveTargetUser(blockeeURI)
	if err != nil {
		return errors.New("unknown blockee")
	}
	// ローカルユーザーのみ対象
	if blockee.Host != nil {
		return nil
	}
	if _, err := p.blockingService.Block(blocker.ID, blockee.ID); err != nil {
		if errors.Is(err, coreblocking.ErrAlreadyBlocking) {
			return nil
		}
		return err
	}
	return nil
}

// handleUndoBlock removes a blocking relationship created by a previous Block.
func (p *Processor) handleUndoBlock(act genericActivity, inner genericActivity) error {
	if p.blockingService == nil {
		return ErrUnsupportedActivity
	}
	blocker, err := p.resolver.ResolveActor(act.Actor)
	if err != nil {
		return err
	}
	blockeeURI, err := readObjectString(inner.Object)
	if err != nil {
		return err
	}
	// Block と同様、ローカルユーザー解決は resolveTargetUser で行う。
	blockee, err := p.resolveTargetUser(blockeeURI)
	if err != nil {
		return errors.New("unknown blockee")
	}
	if err := p.blockingService.Unblock(blocker.ID, blockee.ID); err != nil {
		if errors.Is(err, coreblocking.ErrNotBlocking) {
			return nil
		}
		return err
	}
	return nil
}

// handleFlag processes an inbound Flag (abuse report) activity. リモートインスタンスからの
// 通報を保存する。
func (p *Processor) handleFlag(act genericActivity) error {
	if p.abuseReportRepo == nil {
		return ErrUnsupportedActivity
	}
	reporter, err := p.resolver.ResolveActor(act.Actor)
	if err != nil {
		return err
	}
	// objectからURI配列を取得（ユーザーやノートのURI）
	var uris []string
	if err := json.Unmarshal(act.Object, &uris); err != nil {
		// 単一URIの場合
		var single string
		if err2 := json.Unmarshal(act.Object, &single); err2 != nil {
			return errors.New("flag: cannot parse object")
		}
		uris = []string{single}
	}
	if len(uris) == 0 {
		return errors.New("flag: empty object")
	}
	// 最初のURIからターゲットユーザーを特定
	var targetUserID string
	for _, uri := range uris {
		if u, err := p.userRepo.FindByURI(uri); err == nil {
			targetUserID = u.ID
			break
		}
	}
	if targetUserID == "" {
		// ノートURIからユーザーを特定する試み
		for _, uri := range uris {
			if n, err := p.noteRepo.FindByURI(uri); err == nil {
				targetUserID = n.UserID
				break
			}
		}
	}
	if targetUserID == "" {
		return errors.New("flag: cannot resolve target user")
	}
	// contentフィールドを取得
	var content struct {
		Content string `json:"content"`
	}
	_ = json.Unmarshal(act.raw, &content)
	comment := content.Content
	if comment == "" {
		comment = "(flagged via ActivityPub)"
	}
	report := &model.AbuseUserReport{
		ID:             p.abuseIDGen.Generate(nowFn()),
		TargetUserID:   targetUserID,
		ReporterID:     reporter.ID,
		Comment:        comment,
		TargetUserHost: nil,
		ReporterHost:   reporter.Host,
	}
	// ターゲットユーザーのホスト情報を取得
	if target, err := p.userRepo.FindByID(targetUserID); err == nil {
		report.TargetUserHost = target.Host
	}
	if err := p.abuseReportRepo.Create(report); err != nil {
		slog.Warn("failed to create abuse report from flag activity", "err", err)
		return err
	}
	return nil
}

// handleMove processes an inbound Move activity. アカウント移行通知を受けて
// リモートactorのプロフィールを強制再取得する。TTLキャッシュをバイパスして
// movedTo/alsoKnownAsの最新値を反映する。
func (p *Processor) handleMove(act genericActivity) error {
	if _, err := p.resolver.ForceResolveActor(act.Actor); err != nil {
		return err
	}
	return nil
}

// handleAdd processes an inbound Add activity. actorのfeaturedコレクションへの
// ノートピン留めを処理する。
func (p *Processor) handleAdd(act genericActivity) error {
	if p.pinningRepo == nil {
		return ErrUnsupportedActivity
	}
	actor, err := p.resolver.ResolveActor(act.Actor)
	if err != nil {
		return err
	}
	// targetがactorのfeaturedコレクションか確認
	var target struct {
		Target string `json:"target"`
	}
	_ = json.Unmarshal(act.raw, &target)
	if actor.Featured == nil || target.Target != *actor.Featured {
		return nil
	}
	noteURI, err := readObjectString(act.Object)
	if err != nil {
		return err
	}
	// ローカルノートならDBから検索、リモートならResolveNoteでフェッチ+取り込み
	note, err := p.noteRepo.FindByURI(noteURI)
	if err != nil {
		note, err = p.resolver.ResolveNote(noteURI)
		if err != nil {
			return err
		}
	}
	now := nowFn()
	pin := &model.UserNotePining{
		ID:     p.pinningIDGen.Generate(now),
		UserID: actor.ID,
		NoteID: note.ID,
	}
	if err := p.pinningRepo.Create(pin); err != nil {
		// 既にピン留め済み（重複キー）の場合のみ無視
		if existing, _ := p.pinningRepo.FindByPair(actor.ID, note.ID); existing != nil {
			return nil
		}
		return err
	}
	return nil
}

// handleRemove processes an inbound Remove activity. actorのfeaturedコレクションから
// ノートのピン留めを解除する。
func (p *Processor) handleRemove(act genericActivity) error {
	if p.pinningRepo == nil {
		return ErrUnsupportedActivity
	}
	actor, err := p.resolver.ResolveActor(act.Actor)
	if err != nil {
		return err
	}
	var target struct {
		Target string `json:"target"`
	}
	_ = json.Unmarshal(act.raw, &target)
	if actor.Featured == nil || target.Target != *actor.Featured {
		return nil
	}
	noteURI, err := readObjectString(act.Object)
	if err != nil {
		return err
	}
	note, err := p.noteRepo.FindByURI(noteURI)
	if err != nil {
		return nil
	}
	pin, err := p.pinningRepo.FindByPair(actor.ID, note.ID)
	if err != nil {
		return nil
	}
	_ = p.pinningRepo.Delete(pin)
	return nil
}

// readObjectString reads an activity Object field that is either a plain
// string IRI or a nested object with an "id" field.
func readObjectString(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", errors.New("missing object")
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil && s != "" {
		return s, nil
	}
	var obj struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return "", err
	}
	if obj.ID == "" {
		return "", errors.New("object missing id")
	}
	return obj.ID, nil
}

// readActorString reads an activity's actor field, supporting both string and
// object forms (mirrors readObjectString but on the actor field of an inner
// activity such as the Follow inside a Reject).
func readActorString(act genericActivity) (string, error) {
	if act.Actor != "" {
		return act.Actor, nil
	}
	return "", errors.New("inner activity missing actor")
}

// handleChatCreate processes a Note flagged with `_misskey_talk: true`
// inside a Create activity (CherryPick / レガシー Misskey の 1-on-1 chat
// federation, #692)。
//
// `to` は CherryPick の renderChatMessage が string[] で出すが、互換性のため
// 単一文字列 / 配列の両方を受け付ける。配列の場合は先頭エントリを recipient
// として扱う (1-on-1 DM 前提なので残りは無視)。複数 recipient (group chat)
// は別 issue で対応。
func (p *Processor) handleChatCreate(sender *model.User, noteURI, content string, toRaw json.RawMessage) error {
	if noteURI == "" {
		return fmt.Errorf("chat create: missing note id")
	}
	if sender.IsLocal() {
		return fmt.Errorf("chat create: sender %s is local (loopback?)", sender.ID)
	}
	to, err := readRecipientURI(toRaw)
	if err != nil {
		return fmt.Errorf("chat create: %w", err)
	}
	recipient, err := p.userRepo.FindByURI(to)
	if err != nil {
		if localID := p.resolver.ExtractLocalUserID(to); localID != "" {
			recipient, err = p.userRepo.FindByID(localID)
		}
		if err != nil {
			return fmt.Errorf("chat create: resolve recipient: %w", err)
		}
	}
	if !recipient.IsLocal() {
		return fmt.Errorf("chat create: recipient %s is not local", to)
	}
	_, err = p.chatService.CreateMessageViaAP(context.Background(), noteURI, sender, recipient.ID, content)
	return err
}

// readRecipientURI extracts the first recipient URI from a `to` field that
// may be either a JSON string or a JSON array of strings. Empty / missing
// values produce an error so the caller can distinguish "malformed" from
// "valid but addressed to public".
func readRecipientURI(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", errors.New("missing 'to' field")
	}
	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		if single == "" {
			return "", errors.New("'to' is empty")
		}
		return single, nil
	}
	var multi []string
	if err := json.Unmarshal(raw, &multi); err == nil {
		for _, s := range multi {
			if s != "" {
				return s, nil
			}
		}
		return "", errors.New("'to' has no usable recipient")
	}
	return "", errors.New("'to' is neither string nor array")
}

// handleChatMessage processes an inbound Misskey:ChatMessage activity
// (CherryPick v12 federation). The activity itself IS the message object
// (not wrapped in Create), so act.ID is the message URI.
func (p *Processor) handleChatMessage(act genericActivity) error {
	if p.chatService == nil {
		return ErrUnsupportedActivity
	}
	// attributedTo と actor の一致を検証
	var raw struct {
		AttributedTo string `json:"attributedTo"`
		To           string `json:"to"`
		Content      string `json:"content"`
	}
	if err := json.Unmarshal(act.raw, &raw); err != nil {
		return fmt.Errorf("chat message: unmarshal: %w", err)
	}
	if raw.AttributedTo == "" || raw.AttributedTo != act.Actor {
		return fmt.Errorf("chat message: attributedTo (%s) != actor (%s)", raw.AttributedTo, act.Actor)
	}
	if raw.To == "" {
		return fmt.Errorf("chat message: missing 'to' field")
	}
	// actor (送信者) を解決
	sender, err := p.resolver.ResolveActor(act.Actor)
	if err != nil {
		return fmt.Errorf("chat message: resolve sender: %w", err)
	}
	// ローカルユーザーはDB上URI==nilのためFindByURIでは解決できない。
	// handleAcceptと同じパターンでExtractLocalUserID→FindByIDにフォールバック。
	recipient, err := p.userRepo.FindByURI(raw.To)
	if err != nil {
		if localID := p.resolver.ExtractLocalUserID(raw.To); localID != "" {
			recipient, err = p.userRepo.FindByID(localID)
		}
		if err != nil {
			return fmt.Errorf("chat message: resolve recipient: %w", err)
		}
	}
	if !recipient.IsLocal() {
		return fmt.Errorf("chat message: recipient %s is not local", raw.To)
	}
	_, err = p.chatService.CreateMessageViaAP(context.Background(), act.ID, sender, recipient.ID, raw.Content)
	return err
}
