// Package reaction provides ReactionService for managing note reactions.
package reaction

import (
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/shiroha-a/mk/internal/core/note"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
)

// FallbackReaction is the default reaction used when no reaction is provided.
// 本家Misskeyは Heart "❤" を fallback にしている。
const FallbackReaction = "\u2764"

// Errors returned by Service.
var (
	// ErrNoteNotFound is returned when the target note does not exist.
	ErrNoteNotFound = errors.New("note not found")
	// ErrNoteNotVisible is returned when the user cannot see the target note.
	ErrNoteNotVisible = errors.New("note not visible to user")
	// ErrAlreadyReacted is returned when the user has already reacted with the same reaction.
	ErrAlreadyReacted = errors.New("user has already reacted with this reaction")
	// ErrReactionNotFound is returned when there is no reaction to delete.
	ErrReactionNotFound = errors.New("reaction not found")
	// ErrCannotReactToPureRenote is returned when the user attempts to react to a pure renote.
	ErrCannotReactToPureRenote = errors.New("cannot react to a pure renote")
	// ErrBlocked is returned when the note author has blocked the reactor.
	ErrBlocked = errors.New("blocked by note author")
)

// BlockingChecker reports whether one user has blocked another. パッケージ間の
// 循環依存を避けるためinterfaceで受け取る (実装は core/blocking)。
type BlockingChecker interface {
	IsBlocked(blockerID, blockeeID string) (bool, error)
}

// legacyMap converts legacy text reactions like "like" or "love" to their
// Unicode equivalents. Misskey本家のReactionServiceと同じ表を使用する。
var legacyMap = map[string]string{
	"like":     "👍",
	"love":     "\u2764",
	"laugh":    "😆",
	"hmm":      "🤔",
	"surprise": "😮",
	"congrats": "🎉",
	"angry":    "💢",
	"confused": "😥",
	"rip":      "😇",
	"pudding":  "🍮",
	"star":     "⭐",
}

// customEmojiPattern matches a custom emoji shortcode like ":smile:" or
// ":smile@example.com:".
var customEmojiPattern = regexp.MustCompile(`^:([\w+\-]+)(?:@([\w.\-]+))?:$`)

// NotificationHook is invoked after a reaction is created.
type NotificationHook interface {
	OnReactionCreated(notifieeID, notifierID, noteID, reaction string)
}

// FederationHook is invoked after a reaction is created or removed so that
// the ActivityPub layer can deliver Like / Undo Like activities. パッケージ間
// の循環依存を避けるためinterfaceで受け取る (実装は core/federation)。
type FederationHook interface {
	OnReactionAdded(reactor *model.User, target *model.Note, reaction string)
	OnReactionRemoved(reactor *model.User, target *model.Note, reaction string)
}

// ChartHook is invoked after a reaction is created so the chart
// subsystem can record per-user reaction counts. パッケージ間の循環
// 依存を避けるため interface で受け取る (実装は core/chart/charthook)。
type ChartHook interface {
	OnReactionCreated(reactor *model.User, note *model.Note)
}

// WebhookHook is invoked after a reaction has been created so that user
// webhooks subscribed to the `reaction` event can fire. 循環依存を避けるため
// interface で受け取る (実装は core/webhook)。
type WebhookHook interface {
	OnReactionCreated(note *model.Note, reactor *model.User, reaction string)
}

// NoteStreamEmoji is the custom-emoji descriptor included in the
// `reacted` payload published to `noteStream:<noteID>`. Name は TS upstream の
// wire format に合わせて `<short>@<host>` または `<short>@.` (local) 形式。
type NoteStreamEmoji struct {
	Name string
	URL  string
}

// NoteStreamHook is invoked after a reaction is created or removed so the
// stream layer can publish `reacted` / `unreacted` events to the per-note
// `noteStream:<noteID>` topic. This drives real-time WebSocket updates for
// clients that have called `subNote` / `sn` (#700)。実装は server 配線層で
// stream.NoteEventPublisher を注入する adapter として与える。
type NoteStreamHook interface {
	OnReacted(noteID, userID, reaction string, emoji *NoteStreamEmoji)
	OnUnreacted(noteID, userID, reaction string)
}

// Service manages note reactions.
type Service struct {
	noteRepo         repository.NoteRepository
	reactionRepo     repository.NoteReactionRepository
	emojiRepo        repository.EmojiRepository
	followingRepo    repository.FollowingRepository
	idGen            id.Generator
	notificationHook NotificationHook
	blockingChecker  BlockingChecker
	federationHook   FederationHook
	chartHook        ChartHook
	webhookHook      WebhookHook
	noteStreamHook   NoteStreamHook
	countWriter      ReactionCountWriter
}

// SetCountWriter replaces the default direct writer with a buffered one.
func (s *Service) SetCountWriter(w ReactionCountWriter) {
	s.countWriter = w
}

// CountWriter returns the active ReactionCountWriter (for read merge).
func (s *Service) CountWriter() ReactionCountWriter {
	return s.countWriter
}

// NewService constructs a new ReactionService.
func NewService(
	noteRepo repository.NoteRepository,
	reactionRepo repository.NoteReactionRepository,
	emojiRepo repository.EmojiRepository,
	followingRepo repository.FollowingRepository,
	idGen id.Generator,
) *Service {
	return &Service{
		noteRepo:      noteRepo,
		reactionRepo:  reactionRepo,
		emojiRepo:     emojiRepo,
		followingRepo: followingRepo,
		idGen:         idGen,
		countWriter:   NewDirectWriter(noteRepo),
	}
}

// SetNotificationHook attaches a NotificationHook used after Create succeeds.
func (s *Service) SetNotificationHook(h NotificationHook) {
	s.notificationHook = h
}

// SetBlockingChecker attaches a BlockingChecker used by Create.
func (s *Service) SetBlockingChecker(c BlockingChecker) {
	s.blockingChecker = c
}

// SetFederationHook attaches a FederationHook used after Create / Delete
// succeed.
func (s *Service) SetFederationHook(h FederationHook) {
	s.federationHook = h
}

// SetChartHook attaches a ChartHook invoked after a reaction is
// created so the chart subsystem can record the event.
func (s *Service) SetChartHook(h ChartHook) {
	s.chartHook = h
}

// SetWebhookHook attaches a WebhookHook invoked after a reaction has been
// created so that user webhooks subscribed to the reaction event can fire.
func (s *Service) SetWebhookHook(h WebhookHook) {
	s.webhookHook = h
}

// SetNoteStreamHook attaches a NoteStreamHook invoked after a reaction is
// created or removed so the stream layer can publish reacted/unreacted
// events to the per-note pubsub topic (#700)。
func (s *Service) SetNoteStreamHook(h NoteStreamHook) {
	s.noteStreamHook = h
}

// Create attaches a reaction by user to the target note.
// 同じユーザーが既に同じリアクションをしている場合は ErrAlreadyReacted。
// 異なるリアクションを既にしている場合は古い方を削除して新しい方を作成する。
func (s *Service) Create(user *model.User, noteID, rawReaction string) (string, error) {
	if user == nil {
		return "", errors.New("user is required")
	}

	// webhookHook.OnReactionCreated が target を PackNote 経由で payload に
	// 詰めるため、Renote / Reply embed を含む full 経路を踏む (#425)。軽量な
	// FindByIDWithUser を使うと webhook 受信側が note.renote / reply の
	// 欠落で Misskey TS 互換が崩れる。
	target, err := s.noteRepo.FindByIDWithRelations(noteID)
	if err != nil {
		return "", ErrNoteNotFound
	}
	if !note.CanSeeNote(user, target, s.followingRepo) {
		return "", ErrNoteNotVisible
	}

	// 投稿者にブロックされている場合はリアクション不可
	if s.blockingChecker != nil && target.UserID != user.ID {
		if blocked, err := s.blockingChecker.IsBlocked(target.UserID, user.ID); err != nil {
			return "", err
		} else if blocked {
			return "", ErrBlocked
		}
	}

	// pure renote (text/cw/file/poll を伴わない renote) にはリアクションできない
	if isPureRenote(target) {
		return "", ErrCannotReactToPureRenote
	}

	reaction := s.normalizeReaction(rawReaction, user.Host)

	// 既存リアクションを確認
	if existing, err := s.reactionRepo.FindByPair(user.ID, target.ID); err == nil {
		if existing.Reaction == reaction {
			return "", ErrAlreadyReacted
		}
		// 別のリアクションがすでにあるので置き換える
		if err := s.reactionRepo.Delete(existing); err != nil {
			return "", err
		}
		// 集計列も古いリアクションを-1
		_ = s.countWriter.Increment(target.ID, existing.Reaction, -1)
		// 連合先には古いリアクションを Undo Like で送る
		if s.federationHook != nil {
			s.federationHook.OnReactionRemoved(user, target, existing.Reaction)
		}
	}

	rec := &model.NoteReaction{
		ID:       s.idGen.Generate(time.Now()),
		UserID:   user.ID,
		NoteID:   target.ID,
		Reaction: reaction,
	}
	if err := s.reactionRepo.Create(rec); err != nil {
		// `FindByPair` 実行後の窓で並行 Create (= 別 inbox process / 別
		// reaction 経路) が同 user×note を入れたとき、PG unique constraint
		// で `ErrDuplicateReaction` を取る。AP の Like activity は
		// idempotent に処理する (= 重複は silent ignore する) のが仕様
		// なので、repository level の sentinel を service level の
		// `ErrAlreadyReacted` に変換して caller (= handleLike / API
		// handler) の既存 swallow path を使わせる (#1186)。
		//
		// 注意: mk-go 内で `reactionRepo.Create` を直接呼ぶ caller は
		// 本 service.Create のみ (= 単一 chokepoint)。将来 service 経由
		// しない直接 caller を増やす場合、その経路でも同等の sentinel
		// 変換が必要 (= 直接呼ぶより service 経由に揃えるのが望ましい)。
		if errors.Is(err, repository.ErrDuplicateReaction) {
			return "", ErrAlreadyReacted
		}
		return "", err
	}
	_ = s.countWriter.Increment(target.ID, reaction, 1)

	// 通知発行 (自分自身へのリアクションは内部で抑制される)
	if s.notificationHook != nil && target.UserID != user.ID {
		s.notificationHook.OnReactionCreated(target.UserID, user.ID, target.ID, reaction)
	}
	// AP配信もベストエフォート。
	if s.federationHook != nil {
		s.federationHook.OnReactionAdded(user, target, reaction)
	}
	// チャート集計もベストエフォート。
	if s.chartHook != nil {
		s.chartHook.OnReactionCreated(user, target)
	}
	// Webhook配信もベストエフォート。
	if s.webhookHook != nil {
		s.webhookHook.OnReactionCreated(target, user, reaction)
	}
	// noteStream に reacted を publish して subNote 購読中の WebSocket
	// クライアントへ即時反映する (#700)。
	if s.noteStreamHook != nil {
		s.noteStreamHook.OnReacted(target.ID, user.ID, decodeReactionForStream(reaction), s.resolveStreamEmoji(reaction))
	}

	return reaction, nil
}

// Delete removes the user's reaction from the note.
func (s *Service) Delete(user *model.User, noteID string) error {
	if user == nil {
		return errors.New("user is required")
	}
	target, err := s.noteRepo.FindByID(noteID)
	if err != nil {
		return ErrNoteNotFound
	}
	existing, err := s.reactionRepo.FindByPair(user.ID, target.ID)
	if err != nil {
		return ErrReactionNotFound
	}
	if err := s.reactionRepo.Delete(existing); err != nil {
		return err
	}
	_ = s.countWriter.Increment(target.ID, existing.Reaction, -1)
	if s.federationHook != nil {
		s.federationHook.OnReactionRemoved(user, target, existing.Reaction)
	}
	// noteStream に unreacted を publish して subNote 購読中の WebSocket
	// クライアントへ即時反映する (#700)。
	if s.noteStreamHook != nil {
		s.noteStreamHook.OnUnreacted(target.ID, user.ID, decodeReactionForStream(existing.Reaction))
	}
	return nil
}

// List returns reactions for the given note id.
func (s *Service) List(user *model.User, noteID, untilID, sinceID string, limit int, reaction string) ([]*model.NoteReaction, error) {
	target, err := s.noteRepo.FindByIDWithUser(noteID)
	if err != nil {
		return nil, ErrNoteNotFound
	}
	if !note.CanSeeNote(user, target, s.followingRepo) {
		return nil, ErrNoteNotVisible
	}
	if limit <= 0 {
		limit = 10
	}
	var reactions []string
	if reaction != "" {
		// List はローカルクエリ context (filter 用) なので actor host
		// 補完は不要。nil で従来挙動を維持する。
		reactions = reactionVariants(s.normalizeReaction(reaction, nil))
	}
	return s.reactionRepo.ListByNoteID(target.ID, untilID, sinceID, limit, reactions)
}

// normalizeReaction returns the canonical form of a reaction string.
//   - 空の文字列はFallback (heart) に置換
//   - レガシー文字列(like等)はUnicode絵文字に変換
//   - カスタム絵文字 ":name:" は絵文字テーブルで存在確認後 ":name@.:" に正規化
//   - リモート ":name@host:" はそのまま検証して残す
//   - その他はそのまま (Unicode絵文字想定)
//
// actorHost は reaction を投稿した側のホスト。リモートユーザーが
// `:name:` 形式 (ホスト省略) で送ってきたとき、Misskey TS upstream は
// reactor のホストで emoji table を引き直すので、本実装でも actorHost
// にフォールバックする (#459)。actorHost が nil/空なら従来通りローカル
// として扱う。
func (s *Service) normalizeReaction(raw string, actorHost *string) string {
	if raw == "" {
		return FallbackReaction
	}
	if v, ok := legacyMap[raw]; ok {
		return v
	}
	if m := customEmojiPattern.FindStringSubmatch(raw); m != nil {
		name := m[1]
		host := ""
		if len(m) >= 3 {
			host = m[2]
		}
		// "@." はローカルを表すcanonical suffix (TS互換)
		if host == "." {
			host = ""
		}
		// reaction 文字列に @host が含まれず reactor が remote なら、
		// 文字列 host が省略されているだけで実体はリモート絵文字。
		// upstream ReactionService.create がやっているのと同じく
		// actor.host を採用して emoji table を引く。
		if host == "" && actorHost != nil && *actorHost != "" {
			host = *actorHost
		}
		var hostPtr *string
		if host != "" {
			hostPtr = &host
		}
		if _, err := s.emojiRepo.FindByNameAndHost(name, hostPtr); err == nil {
			if host == "" {
				return ":" + name + "@.:"
			}
			return ":" + name + "@" + host + ":"
		}
		// 見つからなければFallbackにする
		return FallbackReaction
	}
	// Unicode emoji は variation selector (U+FE0F) を strip して upstream
	// Misskey TS と同じ canonical form に揃える (#864)。同じ emoji の異なる
	// encode (例: U+2764 と U+2764 + U+FE0F) を 1 つの key として扱う。
	return stripVariationSelector(raw)
}

// stripVariationSelector removes Unicode emoji variation selector (U+FE0F)
// from the input. emoji-style 表示用の variation selector は実体の emoji
// codepoint を変えないため、reaction key の正規化として safe に削除できる。
// upstream Misskey TS は同様の正規化を行うため、両 backend の reaction
// 文字列を揃えるのに必要 (#864)。
func stripVariationSelector(s string) string {
	return strings.ReplaceAll(s, "\ufe0f", "")
}

// localCanonicalPattern matches the canonical local emoji form `:name@.:`.
var localCanonicalPattern = regexp.MustCompile(`^:([\w+\-]+)@\.:$`)

// reactionVariants returns a slice of reaction strings to match in the DB.
// TS時代のレコードは `:name:` 形式、mk時代は `:name@.:` 形式で保存されて
// いるため、ローカルカスタム絵文字の場合は両方の形式で検索する必要がある。
func reactionVariants(normalized string) []string {
	if m := localCanonicalPattern.FindStringSubmatch(normalized); m != nil {
		return []string{normalized, ":" + m[1] + ":"}
	}
	return []string{normalized}
}

// decodeReactionForStream returns the canonical wire form of a reaction
// string for noteStream payload (`:name@host:` / `:name@.:` for custom emoji,
// raw string for unicode/legacy). 上流 ReactionService.ts の decodeReaction と
// 同形式で、ホスト省略時 `.` を補う。normalizeReaction と違い emoji 存在検証
// やフォールバックは行わない (DB に既に保存された値をそのまま wire 化する用途)。
func decodeReactionForStream(raw string) string {
	m := customEmojiPattern.FindStringSubmatch(raw)
	if m == nil {
		return raw
	}
	name := m[1]
	host := "."
	if len(m) >= 3 && m[2] != "" && m[2] != "." {
		host = m[2]
	}
	return ":" + name + "@" + host + ":"
}

// resolveStreamEmoji returns the NoteStreamEmoji descriptor for a custom
// reaction, looking up the emoji in the repository. Returns nil for
// unicode / legacy reactions or when the emoji is unknown. URL は
// publicUrl 優先で originalUrl に fallback する (上流互換)。
func (s *Service) resolveStreamEmoji(reaction string) *NoteStreamEmoji {
	m := customEmojiPattern.FindStringSubmatch(reaction)
	if m == nil {
		return nil
	}
	name := m[1]
	host := ""
	if len(m) >= 3 {
		host = m[2]
	}
	if host == "." {
		host = ""
	}
	var hostPtr *string
	if host != "" {
		hostPtr = &host
	}
	emoji, err := s.emojiRepo.FindByNameAndHost(name, hostPtr)
	if err != nil || emoji == nil {
		return nil
	}
	url := emoji.PublicURL
	if url == "" {
		url = emoji.OriginalURL
	}
	suffix := host
	if suffix == "" {
		suffix = "."
	}
	return &NoteStreamEmoji{
		Name: name + "@" + suffix,
		URL:  url,
	}
}

// isPureRenote reports whether the given note is a pure renote (no text/cw/files/poll).
func isPureRenote(n *model.Note) bool {
	if n.RenoteID == nil {
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
