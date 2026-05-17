// Package note provides core business logic services for notes.
package note

import (
	"errors"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/lib/pq"
	"github.com/shiroha-a/mk/internal/activitypub/mfm"
	"github.com/shiroha-a/mk/internal/entity"
	"github.com/shiroha-a/mk/internal/misc/hashtag"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/misc/keyword"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
)

// Errors returned by NoteCreateService.
var (
	// ErrNoteContentRequired is returned when text, fileIds and renoteId are all empty.
	ErrNoteContentRequired = errors.New("text, fileIds, or renoteId is required")
	// ErrReplyTargetNotFound is returned when the replyId references a missing note.
	ErrReplyTargetNotFound = errors.New("reply target not found")
	// ErrRenoteTargetNotFound is returned when the renoteId references a missing note.
	ErrRenoteTargetNotFound = errors.New("renote target not found")
	// ErrCannotReplyToInvisibleNote is returned when the replier cannot see the reply target.
	ErrCannotReplyToInvisibleNote = errors.New("cannot reply to this note")
	// ErrCannotRenoteInvisibleNote is returned when the renoter cannot see the renote target.
	ErrCannotRenoteInvisibleNote = errors.New("cannot renote this note")
	// ErrChannelNotFound is returned when the channelId references a missing channel.
	ErrChannelNotFound = errors.New("channel not found")
	// ErrCannotRenoteToAPureRenote is returned when the renote target is a pure
	// renote (renote of another note with no original content). Misskey disallows
	// renoting a pure renote to avoid redundant propagation chains.
	ErrCannotRenoteToAPureRenote = errors.New("cannot renote a pure renote")
	// ErrCannotReplyToAPureRenote is returned when the reply target is a pure renote.
	ErrCannotReplyToAPureRenote = errors.New("cannot reply to a pure renote")
	// ErrCannotReplyToSpecifiedVisibility is returned when replying to a
	// "specified" visibility note with a wider visibility (non-specified).
	ErrCannotReplyToSpecifiedVisibility = errors.New("cannot reply to specified visibility note with extended visibility")
	// ErrYouHaveBeenBlocked is returned when the target user (reply/renote target)
	// has blocked the actor.
	ErrYouHaveBeenBlocked = errors.New("you have been blocked by the target user")
	// ErrCannotCreateAlreadyExpiredPoll is returned when poll.ExpiresAt is in
	// the past at creation time.
	ErrCannotCreateAlreadyExpiredPoll = errors.New("poll is already expired")
	// ErrNoSuchFile is returned when one or more fileIds reference missing files.
	ErrNoSuchFile = errors.New("some files are not found")
	// ErrCannotRenoteOutsideOfChannel is returned when the renote target belongs
	// to a channel that disallows renoting outside of its context.
	ErrCannotRenoteOutsideOfChannel = errors.New("cannot renote outside of channel")
	// ErrContainsProhibitedWords is returned when the note text contains a word
	// in meta.prohibitedWords.
	ErrContainsProhibitedWords = errors.New("note contains prohibited words")
	// ErrContainsTooManyMentions is returned when the note exceeds the role
	// policy's mentionLimit.
	ErrContainsTooManyMentions = errors.New("note contains too many mentions")
)

// CreateInput is the input parameter for CreateService.Create.
type CreateInput struct {
	User               *model.User
	Text               *string
	CW                 *string
	Visibility         model.NoteVisibility
	VisibleUserIDs     []string
	LocalOnly          bool
	ReactionAcceptance *string
	FileIDs            []string
	ReplyID            *string
	RenoteID           *string
	ChannelID          *string
	Poll               *PollInput
}

// PollInput represents the poll part of a create note input.
type PollInput struct {
	Choices   []string
	Multiple  bool
	ExpiresAt *time.Time
}

// TimelineFanoutHook is invoked after a note has been persisted so that the
// timeline subsystem can deliver the note to interested feeds. パッケージ間の
// 循環依存を避けるためinterfaceで受け取る (実装は core/timeline)。
type TimelineFanoutHook interface {
	OnNoteCreated(note *model.Note, author *model.User)
}

// NotificationHook is invoked after a note has been persisted so that the
// notification subsystem can create notification entries for mentions, replies,
// renotes, etc. パッケージ間の循環依存を避けるためinterfaceで受け取る。
type NotificationHook interface {
	OnNoteCreated(note *model.Note, author *model.User, replyTarget, renoteTarget *model.Note)
}

// FederationHook is invoked after a note has been persisted so that the
// ActivityPub layer can deliver the note to remote followers.
// パッケージ間の循環依存を避けるためinterfaceで受け取る (実装は core/federation)。
type FederationHook interface {
	OnNoteCreated(note *model.Note, author *model.User)
}

// ChannelHook is invoked when a note is posted to a channel so the channel
// service can validate existence ahead of insertion and bump counters / the
// last-noted timestamp afterwards. パッケージ間の循環依存を避けるため
// interface で受け取る (実装は core/channel)。
type ChannelHook interface {
	// EnsureChannelExists is called before insertion. Returning a non-nil
	// error aborts the note creation.
	EnsureChannelExists(channelID string) error
	// OnNotePosted is called after the note row has been persisted. noteID
	// / authorID are passed so the hook can fan out per-follower unread
	// tracking (hasUnreadChannel) while skipping self-posts.
	OnNotePosted(channelID, noteID, authorID string)
}

// AntennaHook is invoked after a note has been persisted so antenna service
// can fan it out into matching antenna timelines. パッケージ間の循環依存を
// 避けるため interface で受け取る (実装は core/antenna)。
type AntennaHook interface {
	OnNoteCreated(note *model.Note, author *model.User)
}

// IndexHook is invoked after a note has been persisted (or deleted) so the
// search subsystem can update its full-text index. 失敗してもメイン処理に
// 影響させたくないため、戻り値は捨てる前提のベストエフォート呼び出し。
// パッケージ間の循環依存を避けるため interface で受け取る (実装は core/search)。
type IndexHook interface {
	OnNoteCreated(note *model.Note)
	OnNoteDeleted(note *model.Note)
}

// ChartHook is invoked after a note has been persisted so the chart
// subsystem can fan the event into NotesChart, PerUserNotesChart,
// InstanceChart and ActiveUsersChart. パッケージ間の循環依存を避ける
// ため interface で受け取る (実装は core/chart/charthook)。
type ChartHook interface {
	OnNoteCreated(note *model.Note)
}

// HashtagHook is invoked after a note has been persisted so the hashtag
// subsystem can record mentionedUsersCount / mentionedUserIds (#680)。
// 各 tag に対して per-user dedup された upsert を実行する。失敗は best-effort
// で握り潰す (note 作成自体は成功扱い)。
//
// 実装契約 (#719): OnNoteCreated は **non-blocking** でなければならず、
// repo 書き込みが必要な場合は実装側で goroutine を起こすこと。caller
// (note_create_service / federation/resolver) は他 hook と異なり safeGo で
// wrap せず直接呼び出すので、panic recovery も実装側の責務。本 contract は
// federation 経路で tag 数 N に比例して inbox drain time が直列化する退行を
// 構造的に防ぐためにある。
type HashtagHook interface {
	OnNoteCreated(note *model.Note, author *model.User)
}

// WebhookHook is invoked after a note has been persisted so that user
// webhooks subscribed to `note` / `reply` / `renote` / `mention` events can
// deliver the packed note to external endpoints. 循環依存を避けるため
// interface で受け取る (実装は core/webhook)。
type WebhookHook interface {
	OnNoteCreated(note *model.Note, author *model.User, replyTarget, renoteTarget *model.Note)
}

// MainStreamPublisher emits real-time events to a single target user's `main`
// WebSocket channel. Used here to deliver `reply`, `renote`, and `mention`
// events so the frontend can reflect note-creation interactions immediately.
// 循環依存を避けるためinterfaceで受け取る(実装はinternal/stream)。
type MainStreamPublisher interface {
	PublishMainEvent(userID, eventType string, body any)
}

// SilencingProvider reports whether a user has been silenced by their role
// policies (= canPublicNote=false). When wired, CreateService demotes
// `visibility=public` notes from non-channel posts to `home` to match
// upstream Misskey TS NoteCreateService behaviour (#1024). 循環依存を避ける
// ため interface で受け取り、実装は core/role.Service が提供する。
type SilencingProvider interface {
	IsSilenced(userID string) bool
}

// CreateService provides note creation logic.
type CreateService struct {
	noteRepo            repository.NoteRepository
	pollRepo            repository.PollRepository
	followingRepo       repository.FollowingRepository
	idGen               id.Generator
	fanoutHook          TimelineFanoutHook
	notificationHook    NotificationHook
	federationHook      FederationHook
	channelHook         ChannelHook
	antennaHook         AntennaHook
	indexHook           IndexHook
	chartHook           ChartHook
	hashtagHook         HashtagHook
	webhookHook         WebhookHook
	mainStreamPublisher MainStreamPublisher
	userRepo            repository.UserRepository
	blockingRepo        repository.BlockingRepository
	driveFileRepo       repository.DriveFileRepository
	metaRepo            repository.MetaRepository
	channelRepo         repository.ChannelRepository
	silencingProvider   SilencingProvider
}

// SetUserRepo attaches a UserRepository for resolving mention usernames to IDs.
func (s *CreateService) SetUserRepo(r repository.UserRepository) {
	s.userRepo = r
}

// SetSilencingProvider attaches a SilencingProvider so public-visibility
// notes from `canPublicNote=false` users are demoted to `home` (#1024).
// nil 時は demotion skip (= 旧挙動)。
func (s *CreateService) SetSilencingProvider(p SilencingProvider) {
	s.silencingProvider = p
}

// SetBlockingRepo attaches a BlockingRepository for detecting reply/renote
// block violations. When unset the block check is skipped (backward compat).
func (s *CreateService) SetBlockingRepo(r repository.BlockingRepository) {
	s.blockingRepo = r
}

// SetDriveFileRepo attaches a DriveFileRepository for validating fileIds.
// When unset fileId validation is skipped (backward compat).
func (s *CreateService) SetDriveFileRepo(r repository.DriveFileRepository) {
	s.driveFileRepo = r
}

// SetMetaRepo attaches a MetaRepository for prohibited-word detection.
// When unset the check is skipped.
func (s *CreateService) SetMetaRepo(r repository.MetaRepository) {
	s.metaRepo = r
}

// SetChannelRepo attaches a ChannelRepository for the "renote outside of
// channel" rule evaluation. Required alongside ChannelHook for strict checks.
func (s *CreateService) SetChannelRepo(r repository.ChannelRepository) {
	s.channelRepo = r
}

// NewCreateService creates a new CreateService.
// followingRepo は省略可 (nil)。指定された場合、followers可視性ノートへの
// reply/renoteで閲覧権限を厳格にチェックする。
// fanoutHookも省略可 (nil)。設定されていればnote作成成功時にコールバックされる。
func NewCreateService(
	noteRepo repository.NoteRepository,
	pollRepo repository.PollRepository,
	idGen id.Generator,
	followingRepo repository.FollowingRepository,
) *CreateService {
	return &CreateService{
		noteRepo:      noteRepo,
		pollRepo:      pollRepo,
		followingRepo: followingRepo,
		idGen:         idGen,
	}
}

// SetFanoutHook attaches a TimelineFanoutHook to be invoked on note creation.
// 後付けセッターにすることで、Wireのような循環依存問題を起こさず、テストでも
// 簡単に差し替え可能にする。
func (s *CreateService) SetFanoutHook(h TimelineFanoutHook) {
	s.fanoutHook = h
}

// SetNotificationHook attaches a NotificationHook invoked on note creation.
func (s *CreateService) SetNotificationHook(h NotificationHook) {
	s.notificationHook = h
}

// SetFederationHook attaches a FederationHook invoked on note creation.
func (s *CreateService) SetFederationHook(h FederationHook) {
	s.federationHook = h
}

// SetChannelHook attaches a ChannelHook invoked when a note is posted to a
// channel. nil 渡しは無効化と同義。
func (s *CreateService) SetChannelHook(h ChannelHook) {
	s.channelHook = h
}

// SetAntennaHook attaches an AntennaHook invoked after note creation so the
// antenna service can fan the note out into matching antenna timelines.
func (s *CreateService) SetAntennaHook(h AntennaHook) {
	s.antennaHook = h
}

// SetIndexHook attaches an IndexHook invoked after note creation so the
// search backend can index the new note.
func (s *CreateService) SetIndexHook(h IndexHook) {
	s.indexHook = h
}

// SetChartHook attaches a ChartHook invoked after note creation so the
// chart subsystem can record the event in the relevant time series.
func (s *CreateService) SetChartHook(h ChartHook) {
	s.chartHook = h
}

// SetWebhookHook attaches a WebhookHook invoked after note creation so that
// user webhooks subscribed to note / reply / renote / mention events can fire.
func (s *CreateService) SetWebhookHook(h WebhookHook) {
	s.webhookHook = h
}

// SetHashtagHook attaches a HashtagHook invoked after note creation so the
// hashtag subsystem can update per-tag mentionedUsersCount / userIds (#680)。
func (s *CreateService) SetHashtagHook(h HashtagHook) {
	s.hashtagHook = h
}

// SetMainStreamPublisher attaches a publisher used to emit `reply`, `renote`,
// and `mention` events to the main channel. Optional — nil disables emit.
func (s *CreateService) SetMainStreamPublisher(p MainStreamPublisher) {
	s.mainStreamPublisher = p
}

// Create creates a new note. It returns the persisted note (with the User
// relation preloaded when possible).
func (s *CreateService) Create(in CreateInput) (*model.Note, error) {
	if in.User == nil {
		return nil, errors.New("user is required")
	}

	// notes/createのバリデーション: text/fileIds/renoteIdのいずれかが必須
	if (in.Text == nil || *in.Text == "") && in.RenoteID == nil && len(in.FileIDs) == 0 {
		return nil, ErrNoteContentRequired
	}

	visibility := in.Visibility
	if visibility == "" {
		visibility = model.NoteVisibilityPublic
	}

	// canPublicNote=false の user が channel 外の public note を投げた場合、
	// upstream Misskey TS の NoteCreateService と同様に visibility を home
	// に降格する (silencing)。連合互換性のため reject ではなく降格扱い (=
	// 投稿は成功するが timeline 表出範囲だけ絞られる)。channel 内の note は
	// channel 機構自体が露出範囲を管理するので降格しない (#1024)。
	//
	// channel 判定は upstream の `data.channel == null` に厳密対応させて
	// `in.ChannelID == nil` のみとする。空文字 channelID は API として
	// 不正値だが、空文字を「非 channel」と扱うと upstream で channel 扱い
	// される request を mk-go で降格してしまう挙動差が出る (= 互換性破壊)。
	if visibility == model.NoteVisibilityPublic && in.ChannelID == nil {
		if s.silencingProvider != nil && s.silencingProvider.IsSilenced(in.User.ID) {
			visibility = model.NoteVisibilityHome
		}
	}

	// meta.sensitiveWords にマッチする text / CW を持つ public note は home に
	// 降格する (upstream Misskey TS NoteCreateService.ts:467-471 と同一挙動)。
	// channel 内は降格対象外 (上の silencing と同じ理由)。投稿は成功するが
	// public timeline / federation broadcast から外れる effect で、admin が
	// 設定したセンシティブワード設定が実機で効くようにする (drop-in regression
	// fix)。upstream と同じく filter は `/regex/flags` または space 区切り AND
	// match。
	if visibility == model.NoteVisibilityPublic && in.ChannelID == nil {
		if s.matchesSensitiveWords(in.Text, in.CW) {
			visibility = model.NoteVisibilityHome
		}
	}

	// プロhibited wordsチェック (meta.prohibitedWordsマッチ)
	if err := s.checkProhibitedWords(in.Text, in.CW); err != nil {
		return nil, err
	}

	// mentionLimitチェック (role policiesの制限)
	if err := s.checkMentionLimit(in.Text); err != nil {
		return nil, err
	}

	// pollのexpiresAtが既に過去なら弾く
	if in.Poll != nil && in.Poll.ExpiresAt != nil && in.Poll.ExpiresAt.Before(time.Now()) {
		return nil, ErrCannotCreateAlreadyExpiredPoll
	}

	// fileIdsのうち存在しないIDがあれば弾く
	if err := s.checkFileIDs(in.User.ID, in.FileIDs); err != nil {
		return nil, err
	}

	// channelId が指定されていれば存在チェック。channelHook 未設定なら
	// channel 機能が無効なものとして扱い、エラーは返さない。
	if in.ChannelID != nil && *in.ChannelID != "" && s.channelHook != nil {
		if err := s.channelHook.EnsureChannelExists(*in.ChannelID); err != nil {
			return nil, ErrChannelNotFound
		}
	}

	// reply/renote先のノートを取得し、閲覧権限を確認する。
	// 取得したノートは、後段のカウンタ更新と非正規化フィールドの埋め込みに使う。
	// ReplyID と RenoteID の DB fetch は完全に独立しているので並列に走らせて
	// 1 round-trip ぶん latency を削る (#300 2-5)。validation は fetch 後に
	// 直列で走らせ、エラー報告順 (reply → renote) は変えない。
	var (
		replyTarget, renoteTarget *model.Note
		replyFetchErr             error
		renoteFetchErr            error
	)
	{
		var wg sync.WaitGroup
		if in.ReplyID != nil {
			wg.Add(1)
			go func() {
				defer wg.Done()
				replyTarget, replyFetchErr = s.noteRepo.FindByIDWithUser(*in.ReplyID)
			}()
		}
		if in.RenoteID != nil {
			wg.Add(1)
			go func() {
				defer wg.Done()
				renoteTarget, renoteFetchErr = s.noteRepo.FindByIDWithUser(*in.RenoteID)
			}()
		}
		wg.Wait()
	}

	if in.ReplyID != nil {
		if replyFetchErr != nil {
			return nil, ErrReplyTargetNotFound
		}
		t := replyTarget
		// 可視性チェックを先に行う。FindByIDWithUserは無条件に行を返すため、
		// 不可視noteに対して別error (pure renote 等) を返すと「対象noteが
		// 何であるか」を攻撃者が推測できる情報漏洩になる。Devin review #270。
		if !CanSeeNote(in.User, t, s.followingRepo) {
			return nil, ErrCannotReplyToInvisibleNote
		}
		// pure renote (renoteIdあり、text/files/poll/cwなし) への返信は許可しない
		if IsPureRenote(t) {
			return nil, ErrCannotReplyToAPureRenote
		}
		// specified可視性noteへの返信時は visibility も specified でなければ拒否
		if t.Visibility == model.NoteVisibilitySpecified && visibility != model.NoteVisibilitySpecified {
			return nil, ErrCannotReplyToSpecifiedVisibility
		}
		// reply対象のユーザーに block されていたら拒否
		if t.UserID != in.User.ID {
			if err := s.checkBlocked(t.UserID, in.User.ID); err != nil {
				return nil, err
			}
		}
	}
	if in.RenoteID != nil {
		if renoteFetchErr != nil {
			return nil, ErrRenoteTargetNotFound
		}
		t := renoteTarget
		// 可視性チェックを先に行う (Devin review #270 — 情報漏洩防止)。
		if !CanSeeNote(in.User, t, s.followingRepo) {
			return nil, ErrCannotRenoteInvisibleNote
		}
		// pure renoteを更にrenoteするのは禁止 (TS: isRenote && !isQuote)
		if IsPureRenote(t) {
			return nil, ErrCannotRenoteToAPureRenote
		}
		// renote対象のユーザーに block されていたら拒否
		if t.UserID != in.User.ID {
			if err := s.checkBlocked(t.UserID, in.User.ID); err != nil {
				return nil, err
			}
		}
		// チャンネル外への renote 可否: 対象がチャンネル note で、本 note の
		// ChannelID が対象と異なる (= チャンネル外への転送) 場合は channel の
		// allowRenoteToExternal を確認する。
		if t.ChannelID != nil && !sameChannel(t.ChannelID, in.ChannelID) {
			if err := s.checkRenoteOutsideOfChannel(*t.ChannelID); err != nil {
				return nil, err
			}
		}
	}

	now := time.Now()
	noteID := s.idGen.Generate(now)

	// hashtag 抽出: text/cw から #tag を拾い note.tags 列に格納する。
	// hashtags/trend の動的集計や hashtag 検索が機能するためには
	// note.tags が常に正しく埋められている必要がある (#655)。
	var hashtagParts []string
	if in.Text != nil {
		hashtagParts = append(hashtagParts, *in.Text)
	}
	if in.CW != nil {
		hashtagParts = append(hashtagParts, *in.CW)
	}
	tags := hashtag.Extract(hashtagParts...)

	note := &model.Note{
		ID:                 noteID,
		UserID:             in.User.ID,
		Text:               in.Text,
		CW:                 in.CW,
		Visibility:         visibility,
		LocalOnly:          in.LocalOnly,
		ReactionAcceptance: in.ReactionAcceptance,
		ReplyID:            in.ReplyID,
		RenoteID:           in.RenoteID,
		ChannelID:          in.ChannelID,
		FileIDs:            in.FileIDs,
		UserHost:           in.User.Host,
		Tags:               pq.StringArray(tags),
	}

	// reply/renote先の非正規化フィールドを埋める
	if replyTarget != nil {
		note.ReplyUserID = &replyTarget.UserID
		note.ReplyUserHost = replyTarget.UserHost
	}
	if renoteTarget != nil {
		note.RenoteUserID = &renoteTarget.UserID
		note.RenoteUserHost = renoteTarget.UserHost
		note.RenoteChannelID = renoteTarget.ChannelID
	}

	if in.VisibleUserIDs != nil {
		note.VisibleUserIDs = in.VisibleUserIDs
	}

	// メンションの抽出。ユーザー名+ホストをユーザーIDに解決する。
	// ローカル (host="") は host IS NULL で、リモート (host!="") は
	// host 列の等価比較で lookup する。未知のユーザーは単にスキップする
	// (remote webfinger 解決は pre-lookup されている前提)。
	// userRepo が未設定のときは後方互換のため username 文字列をそのまま格納する。
	if in.Text != nil {
		if s.userRepo != nil {
			mentions := ExtractMentionStructs(*in.Text)
			if len(mentions) > 0 {
				// host ごとに 1 query にまとめる (#300 1-5)。
				// 元実装は mention 数 N に対して N 回 FindByUsernameLower を
				// 直列に叩いていたが、host 単位で IN(?) にしてラウンドトリップ
				// を host 種類数まで削減する。
				byHost := make(map[string][]string)
				for _, m := range mentions {
					byHost[m.Host] = append(byHost[m.Host], m.Username)
				}
				// resolved[host][usernameLower] = userID
				resolved := make(map[string]map[string]string, len(byHost))
				for host, names := range byHost {
					var hostPtr *string
					if host != "" {
						h := host
						hostPtr = &h
					}
					users, err := s.userRepo.FindManyByUsernamesAndHost(names, hostPtr)
					if err != nil {
						// 1 host の lookup 失敗は当該 host の mention を
						// 諦めて後続を続行する (元実装も err 時 skip だった)。
						continue
					}
					m := make(map[string]string, len(users))
					for _, u := range users {
						m[u.UsernameLower] = u.ID
					}
					resolved[host] = m
				}
				ids := make([]string, 0, len(mentions))
				for _, mn := range mentions {
					if hostMap, ok := resolved[mn.Host]; ok {
						if id, ok := hostMap[strings.ToLower(mn.Username)]; ok {
							ids = append(ids, id)
						}
					}
				}
				note.Mentions = ids
			}
		} else {
			note.Mentions = ExtractMentions(*in.Text)
		}
	}

	// Custom emoji 名抽出: text + cw を MFM parse して :code: トークンを集める
	// (#629)。連合配信時に renderer.addEmojiTags が note.Emojis を walk して
	// AP Note.tag に Emoji エントリを足すので、ここで埋めないと連合先で
	// custom emoji が画像化されず文字列のまま表示される。
	{
		var text, cw string
		if in.Text != nil {
			text = *in.Text
		}
		if in.CW != nil {
			cw = *in.CW
		}
		if names := mfm.CollectEmojiCodes(text, cw); len(names) > 0 {
			note.Emojis = names
		}
	}

	if err := s.noteRepo.Create(note); err != nil {
		return nil, err
	}

	// reply/renote先のカウンタを更新する。
	// quote renoteの場合 (text/cw/poll/fileを伴うrenote) は renoteCount を増やさず、
	// 純粋なrenoteのみ集計する。これはMisskey本家の挙動に揃えている。
	if replyTarget != nil {
		_ = s.noteRepo.IncrementCount(replyTarget.ID, "repliesCount", 1)
	}
	if renoteTarget != nil && isPureRenote(in) {
		_ = s.noteRepo.IncrementCount(renoteTarget.ID, "renoteCount", 1)
	}

	// 投票が指定されていればPollレコードを作成しnote.hasPollを更新
	if in.Poll != nil && len(in.Poll.Choices) > 0 {
		votes := make([]int64, len(in.Poll.Choices))
		poll := &model.Poll{
			NoteID:         noteID,
			Multiple:       in.Poll.Multiple,
			Choices:        in.Poll.Choices,
			Votes:          votes,
			NoteVisibility: visibility,
			UserID:         in.User.ID,
			UserHost:       in.User.Host,
			ChannelID:      in.ChannelID,
			ExpiresAt:      in.Poll.ExpiresAt,
		}
		if err := s.pollRepo.Create(poll); err != nil {
			return nil, err
		}
		if err := s.noteRepo.Update(note, "hasPoll", true); err != nil {
			return nil, err
		}
		note.HasPoll = true
	}

	// User / Renote / Reply リレーションをpreloadして返す。失敗時は引数の
	// Userをそのまま埋めて返す。fanout / hook 先で renote/reply embed を
	// 触るため full version を使う (#425)。
	finalNote := note
	if loaded, err := s.noteRepo.FindByIDWithRelations(noteID); err == nil && loaded != nil {
		finalNote = loaded
	} else {
		finalNote.User = in.User
	}

	// ベストエフォートフックを非同期で並列実行する。各フックは独立しており、
	// 失敗してもノート作成自体は成功扱い。TS版と同様にfire-and-forgetで配信する。
	if s.fanoutHook != nil {
		safeGo(func() { s.fanoutHook.OnNoteCreated(finalNote, in.User) })
	}
	if s.notificationHook != nil {
		safeGo(func() { s.notificationHook.OnNoteCreated(finalNote, in.User, replyTarget, renoteTarget) })
	}
	if s.federationHook != nil {
		safeGo(func() { s.federationHook.OnNoteCreated(finalNote, in.User) })
	}
	if s.channelHook != nil && in.ChannelID != nil && *in.ChannelID != "" {
		chID := *in.ChannelID
		noteID := finalNote.ID
		authorID := in.User.ID
		safeGo(func() { s.channelHook.OnNotePosted(chID, noteID, authorID) })
	}
	if s.antennaHook != nil {
		safeGo(func() { s.antennaHook.OnNoteCreated(finalNote, in.User) })
	}
	if s.indexHook != nil {
		safeGo(func() { s.indexHook.OnNoteCreated(finalNote) })
	}
	if s.chartHook != nil {
		safeGo(func() { s.chartHook.OnNoteCreated(finalNote) })
	}
	if s.hashtagHook != nil {
		// HashtagHook は実装側 (core/hashtag.Service) が内部で goroutine を
		// 起こす fire-and-forget 設計 (#719)。caller での safeGo wrap は不要。
		s.hashtagHook.OnNoteCreated(finalNote, in.User)
	}
	if s.webhookHook != nil {
		safeGo(func() { s.webhookHook.OnNoteCreated(finalNote, in.User, replyTarget, renoteTarget) })
	}
	// reply / renote / mention 各イベントを対象local userのmainにemit。
	// Misskey本家NoteCreateServiceと同じfan-out方針 (TS lines 792 / 809 / 935)。
	s.publishNoteMainEvents(finalNote, in.User, replyTarget, renoteTarget)

	// notesCount の増加は user 行への直接 UPDATE。userRepo 経由で叩くこと
	// で CachedUserRepository wrapper の invalidate が走り、stale notesCount
	// が profile API に出続ける問題を防ぐ (Devin review #552 BUG-2)。
	// userRepo 未配線の旧経路は noteRepo の同等メソッドにフォールバック。
	safeGo(func() {
		if s.userRepo != nil {
			_ = s.userRepo.IncrementNotesCount(in.User.ID, 1)
			return
		}
		_ = s.noteRepo.IncrementUserNotesCount(in.User.ID, 1)
	})

	return finalNote, nil
}

// publishNoteMainEvents fans out `reply`, `renote`, and `mention` events to
// the relevant local users' main channels. TS本家と同じくdedupはせず、
// 同一userに対して複数イベント(例: 相手へのリプライかつメンション)が
// 並列で届きうる(frontendはイベント種別ごとに意味付けるため)。
func (s *CreateService) publishNoteMainEvents(note *model.Note, author *model.User, replyTarget, renoteTarget *model.Note) {
	if s.mainStreamPublisher == nil {
		return
	}
	packed := entity.PackNote(note, s.idGen)
	// reply: reply先がlocal (UserHost == nil) かつ自分自身へのリプライで
	// ない場合にemit。
	if replyTarget != nil && replyTarget.UserHost == nil && replyTarget.UserID != author.ID {
		s.mainStreamPublisher.PublishMainEvent(replyTarget.UserID, "reply", packed)
	}
	// renote: 同上 (TS: caller ≠ target author条件あり)。
	if renoteTarget != nil && renoteTarget.UserHost == nil && renoteTarget.UserID != author.ID {
		s.mainStreamPublisher.PublishMainEvent(renoteTarget.UserID, "renote", packed)
	}
	// mention: note.Mentionsは(userRepo設定時)user ID配列なので、
	// 各userをfetchしてlocalかつauthor自身でなければemitする。
	// userRepo未設定時はMentionsがusername文字列のため解決不能でスキップ。
	if s.userRepo == nil {
		return
	}
	for _, uid := range note.Mentions {
		if uid == author.ID {
			continue
		}
		u, err := s.userRepo.FindByID(uid)
		if err != nil || u == nil {
			continue
		}
		if u.Host != nil {
			continue // remote user — AP配送(別レイヤ)が担当
		}
		s.mainStreamPublisher.PublishMainEvent(uid, "mention", packed)
	}
}

// safeGo runs fn in a new goroutine, recovering from panics.
// ベストエフォートフックでパニックが発生してもプロセス全体が落ちないようにする。
func safeGo(fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("recovered panic in best-effort hook", "panic", r)
			}
		}()
		fn()
	}()
}

// mentionRegex matches @username and @username@host occurrences anywhere in
// text. Misskey本家のクライアント実装(misskey-js)と同じく、ユーザー名は
// 英数とアンダースコアおよびハイフンからなり、長さは制限しない。
// ホスト部はドメイン形式 (英数・ハイフン・ドット) を許容する。
var mentionRegex = regexp.MustCompile(`@([A-Za-z0-9_-]+)(?:@([A-Za-z0-9.\-]+))?`)

// Mention represents a single user mention extracted from note text.
type Mention struct {
	Username string
	Host     string // ホスト指定がない場合は空文字
}

// ExtractMentions extracts mention usernames from a note text.
// 戻り値はusername (ホストなしの場合) または "username@host" (リモートユーザー指定の場合)。
// Misskeyのnote.mentions列はユーザーIDの配列だが、本サービスではユーザー解決を
// 別レイヤで行う前提で、ここではユーザー名形式のままで返す。重複は除去する。
func ExtractMentions(text string) []string {
	matches := mentionRegex.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(matches))
	var out []string
	for _, m := range matches {
		username := m[1]
		host := ""
		if len(m) >= 3 {
			host = m[2]
		}
		key := username
		if host != "" {
			key = username + "@" + host
		}
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, key)
	}
	return out
}

// ExtractMentionStructs returns the mentions as structured Mention values.
// 主にNotificationServiceなどリモート/ローカル区別を要する呼び出し向け。
func ExtractMentionStructs(text string) []Mention {
	matches := mentionRegex.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(matches))
	var out []Mention
	for _, m := range matches {
		username := m[1]
		host := ""
		if len(m) >= 3 {
			host = m[2]
		}
		key := username + "@" + host
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, Mention{Username: username, Host: host})
	}
	return out
}

// isPureRenote reports whether the create request is a pure renote (no text,
// no cw, no poll, no files). 純粋なrenoteのみがrenoteCountに反映される。
func isPureRenote(in CreateInput) bool {
	if in.RenoteID == nil {
		return false
	}
	if in.Text != nil && *in.Text != "" {
		return false
	}
	if in.CW != nil && *in.CW != "" {
		return false
	}
	if len(in.FileIDs) > 0 {
		return false
	}
	if in.Poll != nil && len(in.Poll.Choices) > 0 {
		return false
	}
	return true
}

// IsPureRenote reports whether the persisted note is a pure renote (no text,
// no cw, no poll, no files). 連合先で Create と Announce を切り替える際に使う。
func IsPureRenote(n *model.Note) bool {
	if n == nil || n.RenoteID == nil {
		return false
	}
	if n.Text != nil && *n.Text != "" {
		return false
	}
	if n.CW != nil && *n.CW != "" {
		return false
	}
	if len(n.FileIDs) > 0 {
		return false
	}
	if n.HasPoll {
		return false
	}
	return true
}

// sameChannel reports whether two optional channel IDs refer to the same channel.
// Both nil or same pointer or same string → true; differing strings → false.
func sameChannel(a, b *string) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

// matchesSensitiveWords reports whether the note's CW or text contains any
// word listed in meta.sensitiveWords. Used by Create to demote public note
// visibility to home.
//
// upstream Misskey TS NoteCreateService.ts:469 はちょうど 1 field だけを
// helper に渡す:
//
//	isKeyWordIncluded(data.cw ?? data.text ?? '', sensitiveWords)
//
// JS の nullish coalescing semantics に従い CW が non-nil なら CW のみ
// (空文字も含めて) を見る = 「CW を設定すれば text の sensitive 検出を
// bypass できる」upstream 仕様。mk-go も drop-in 互換のため同挙動を維持
// する。CW + text 両方 check する mk-go-strict 化は **本 PR の scope 外**
// (= 必要なら別 PR で mk-go independent hardening として議論)。
//
// metaRepo 未設定または sensitiveWords が空 → false (no match)。helper
// errors (meta fetch 失敗) も fail-open で false: 反映漏れの方が「投稿が
// 思わぬ form で blocked される」より影響軽微で、upstream も同タイミング
// で throw しないので挙動も揃う。
func (s *CreateService) matchesSensitiveWords(text, cw *string) bool {
	if s.metaRepo == nil {
		return false
	}
	meta, err := s.metaRepo.Fetch()
	if err != nil || meta == nil || len(meta.SensitiveWords) == 0 {
		return false
	}
	haystack := pickHaystackForSensitive(text, cw)
	if haystack == "" {
		return false
	}
	return keyword.IsKeyWordIncluded(haystack, []string(meta.SensitiveWords))
}

// pickHaystackForSensitive mirrors upstream's `data.cw ?? data.text ?? ”`:
// CW が non-nil ならその値 (空文字を含む) を返し、CW が nil なら text、
// 両方 nil なら空文字。
//
// JS の nullish coalescing と完全に揃える。例えば cw=&"" (admin が CW を
// 明示的に空文字で送ったケース) では cw が "non-nil" 扱いされ、text は
// 評価されない。upstream の known な bypass 経路を保存するための忠実な
// 移植 (mk-go-strict 化は別議論)。
func pickHaystackForSensitive(text, cw *string) string {
	if cw != nil {
		return *cw
	}
	if text != nil {
		return *text
	}
	return ""
}

// checkProhibitedWords scans text + cw for any word listed in
// meta.prohibitedWords. Returns ErrContainsProhibitedWords on match.
// MetaRepo未設定、または prohibitedWords が空ならskipする。
func (s *CreateService) checkProhibitedWords(text, cw *string) error {
	if s.metaRepo == nil {
		return nil
	}
	meta, err := s.metaRepo.Fetch()
	if err != nil || meta == nil || len(meta.ProhibitedWords) == 0 {
		return nil
	}
	haystacks := make([]string, 0, 2)
	if text != nil && *text != "" {
		haystacks = append(haystacks, strings.ToLower(*text))
	}
	if cw != nil && *cw != "" {
		haystacks = append(haystacks, strings.ToLower(*cw))
	}
	for _, word := range meta.ProhibitedWords {
		word = strings.TrimSpace(strings.ToLower(word))
		if word == "" {
			continue
		}
		for _, h := range haystacks {
			if strings.Contains(h, word) {
				return ErrContainsProhibitedWords
			}
		}
	}
	return nil
}

// checkMentionLimit counts mentions in text and compares against the default
// role policy's mentionLimit. Per-user role override is not yet supported
// (TS side merges per-user policies; follow-up issue should wire that).
func (s *CreateService) checkMentionLimit(text *string) error {
	if text == nil || *text == "" {
		return nil
	}
	mentions := ExtractMentionStructs(*text)
	limit := defaultMentionLimit()
	if limit > 0 && len(mentions) > limit {
		return ErrContainsTooManyMentions
	}
	return nil
}

// DefaultMentionLimit mirrors role.DefaultPolicies().mentionLimit. corenote から
// 直接 role.DefaultPolicies() を import すると循環依存になるため、同期を保つ
// 意図で本家TSの現行既定値 (20) を const として直書きしている。federation 経路
// (Resolver.IngestNote) も同じ const を参照することで policy を一元化。
const DefaultMentionLimit = 20

// defaultMentionLimit returns the baseline mentionLimit from role.DefaultPolicies.
// 本家TSではrole per-userのmerge値を使うが、Go側は現状default値でチェック。
func defaultMentionLimit() int {
	return DefaultMentionLimit
}

// checkFileIDs verifies all provided fileIds exist and belong to the user.
// DriveFileRepo未設定ならskipする。
// TS本家は user.id 所有の files のみ拾うが、Go の DriveFileRepo は ID ベース
// の bulk 取得しか持たないため、取得後に userId 一致をチェックする。
// 本家TSは Promise.all で個別解決するため fileIds に重複があっても通る。
// Go 側も `WHERE id IN ?` の SQL 重複排除による false positive を避けるため、
// 返却 file を「所有者一致の ID 集合」に畳んだ上で、入力 ID が全て集合に
// 含まれることを確認する (Devin review #270)。
func (s *CreateService) checkFileIDs(userID string, fileIDs []string) error {
	if len(fileIDs) == 0 || s.driveFileRepo == nil {
		return nil
	}
	files, err := s.driveFileRepo.ListByFileIDs(fileIDs)
	if err != nil {
		return err
	}
	owned := make(map[string]struct{}, len(files))
	for _, f := range files {
		if f.UserID != nil && *f.UserID == userID {
			owned[f.ID] = struct{}{}
		}
	}
	for _, id := range fileIDs {
		if _, ok := owned[id]; !ok {
			return ErrNoSuchFile
		}
	}
	return nil
}

// checkBlocked returns ErrYouHaveBeenBlocked when blockerID has blocked
// blockeeID. BlockingRepo未設定ならskip。
func (s *CreateService) checkBlocked(blockerID, blockeeID string) error {
	if s.blockingRepo == nil {
		return nil
	}
	blocked, err := s.blockingRepo.Exists(blockerID, blockeeID)
	if err != nil {
		return err
	}
	if blocked {
		return ErrYouHaveBeenBlocked
	}
	return nil
}

// checkRenoteOutsideOfChannel resolves the channel referenced by channelID
// and rejects renoting outside of it when the channel forbids it.
// ChannelRepo未設定ならskip (strictチェック不能として許可側に倒す)。
func (s *CreateService) checkRenoteOutsideOfChannel(channelID string) error {
	if s.channelRepo == nil {
		return nil
	}
	ch, err := s.channelRepo.FindByID(channelID)
	if err != nil {
		// チャンネル不明時は TS 側は NO_SUCH_CHANNEL を投げるが、本経路は
		// 既に renote ターゲットが作られている (= channel は存在した時点が
		// ある) 前提なので、取得失敗は allow 側に倒す。
		return nil
	}
	if !ch.AllowRenoteToExternal {
		return ErrCannotRenoteOutsideOfChannel
	}
	return nil
}
