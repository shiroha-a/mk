// Package antenna provides the user-facing antenna CRUD service plus a
// per-antenna Redis-stream timeline of matching notes. Misskey 互換の
// ローカル限定機能で、ActivityPub 連携は持たない。
package antenna

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/lib/pq"
	"github.com/redis/go-redis/v9"
	corenote "github.com/shiroha-a/mk/internal/core/note"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
)

// Errors returned by Service.
var (
	// ErrAntennaNotFound is returned when the requested antenna does not exist.
	ErrAntennaNotFound = errors.New("antenna not found")
	// ErrAntennaNameRequired is returned when name is empty on Create / Update.
	ErrAntennaNameRequired = errors.New("antenna name is required")
	// ErrTooManyAntennas is returned by Create when the user already owns
	// `antennaLimit` antennas (#1029, upstream tooManyAntennas)。
	ErrTooManyAntennas = errors.New("antenna limit exceeded")
	// ErrAccessDenied is returned when a user attempts to update / delete an
	// antenna they do not own.
	ErrAccessDenied = errors.New("not the owner of this antenna")
	// ErrInvalidSource is returned when src is not a known antenna source.
	ErrInvalidSource = errors.New("invalid antenna source")
)

// MaxNotesPerAntenna caps how many notes are kept per antenna in the Redis
// stream. 古いものから XADD MAXLEN ~ で削除される。
const MaxNotesPerAntenna = 200

// streamKey returns the Redis Stream key for an antenna's note timeline.
func streamKey(antennaID string) string {
	return "antennaTimeline:" + antennaID
}

// StreamingPublisher publishes a matched antenna note to the WebSocket
// streaming pub/sub topic so that live `antenna` channel subscribers receive a
// push. パッケージ間の循環依存を避けるため interface で受け取る (実装は
// internal/stream.NotePublisher)。topic は streamKey(antennaID) で生成する
// "antennaTimeline:<id>" 論理名で、AntennaChannel が Subscribe する topic と一致する。
type StreamingPublisher interface {
	PublishNote(topic string, n *model.Note, author *model.User)
}

// Service provides antenna CRUD plus matchNote / OnNoteCreated push.
type Service struct {
	repo          repository.AntennaRepository
	userRepo      repository.UserRepository
	followingRepo repository.FollowingRepository
	userListRepo  repository.UserListRepository
	unreadRepo    repository.AntennaNoteUnreadRepository
	client        *redis.Client
	idGen         id.Generator
	clock         func() time.Time
	// rolePolicyProvider は Create で antennaLimit gate に使う (#1029)。
	// nil 時は gate skip (旧挙動互換)。
	rolePolicyProvider RolePolicyProvider
	// publisher は match した note を pubsub topic へ publish する (#1573)。
	// nil 時は Redis Stream への XAdd のみ行い realtime 配信は skip (旧挙動)。
	publisher StreamingPublisher
}

// RolePolicyProvider abstracts role-policy lookup for antenna count limits (#1029).
type RolePolicyProvider interface {
	GetUserPolicies(userID string) map[string]any
}

// SetRolePolicyProvider wires a RolePolicyProvider so Create enforces the
// `antennaLimit` role policy (#1029).
func (s *Service) SetRolePolicyProvider(p RolePolicyProvider) {
	s.rolePolicyProvider = p
}

// NewService constructs an antenna Service. userRepo は現状未使用だが、将来
// 拡張のため受け取る (nil 許容)。home / list source を有効にするには、別途
// SetFollowingRepo / SetUserListRepo で repository を注入する必要がある。
func NewService(
	repo repository.AntennaRepository,
	userRepo repository.UserRepository,
	client *redis.Client,
	idGen id.Generator,
) *Service {
	return &Service{
		repo:     repo,
		userRepo: userRepo,
		client:   client,
		idGen:    idGen,
		clock:    time.Now,
	}
}

// SetFollowingRepo enables the `home` antenna source by providing a
// way to check whether the antenna owner follows the note author.
// nil でも antenna service は動くが `home` source は常に不一致扱いになる
// (matchSource 実装と揃える)。
func (s *Service) SetFollowingRepo(r repository.FollowingRepository) {
	s.followingRepo = r
}

// SetUserListRepo enables the `list` antenna source. nil なら list sources は
// 登録/マッチともに reject される。
func (s *Service) SetUserListRepo(r repository.UserListRepository) {
	s.userListRepo = r
}

// SetUnreadRepo attaches an AntennaNoteUnreadRepository. When set, each
// note pushed to an antenna also creates an unread row for the antenna
// owner so /api/i can populate hasUnreadAntenna. Optional — nil disables
// unread tracking (Redis timeline is unaffected).
func (s *Service) SetUnreadRepo(r repository.AntennaNoteUnreadRepository) {
	s.unreadRepo = r
}

// SetStreamingPublisher attaches a StreamingPublisher invoked alongside the
// Redis stream XAdd in OnNoteCreated so that live `antenna` channel subscribers
// receive a push (#1573). Without it the per-antenna Redis Stream is still
// written (so REST `antennas/notes` works) but no pub/sub publish happens and
// realtime delivery is silently disabled. nil-safe: unset disables only the
// pub/sub publish.
func (s *Service) SetStreamingPublisher(p StreamingPublisher) {
	s.publisher = p
}

// SetClock overrides the time source. Intended for tests.
func (s *Service) SetClock(now func() time.Time) {
	if now != nil {
		s.clock = now
	}
}

// CreateInput is the parameter set for Service.Create.
type CreateInput struct {
	OwnerID         string
	Name            string
	Src             model.AntennaSource
	UserListID      *string
	Users           []string
	Keywords        [][]string
	ExcludeKeywords [][]string
	CaseSensitive   bool
	ExcludeBots     bool
	WithReplies     bool
	WithFile        bool
	LocalOnly       bool
}

// Create persists a new antenna and returns it.
func (s *Service) Create(in CreateInput) (*model.Antenna, error) {
	if in.Name == "" {
		return nil, ErrAntennaNameRequired
	}
	if in.OwnerID == "" {
		return nil, errors.New("ownerId is required")
	}
	if !validSource(in.Src) {
		return nil, ErrInvalidSource
	}
	// antennaLimit role policy gate (#1029)。policy 経由で取得した上限と
	// 現在保有数を比較。provider 未配線 / policy 不在は gate skip (旧挙動)。
	if s.rolePolicyProvider != nil {
		if limit, ok := s.rolePolicyProvider.GetUserPolicies(in.OwnerID)["antennaLimit"].(int); ok && limit >= 0 {
			count, err := s.repo.CountByUser(in.OwnerID)
			if err != nil {
				return nil, err
			}
			if count >= int64(limit) {
				return nil, ErrTooManyAntennas
			}
		}
	}
	// Marshal of [][]string never fails, so we ignore the error.
	keywords, _ := json.Marshal(normalizeKeywords(in.Keywords))
	exclude, _ := json.Marshal(normalizeKeywords(in.ExcludeKeywords))
	now := s.clock()
	a := &model.Antenna{
		ID:              s.idGen.Generate(now),
		LastUsedAt:      now,
		UserID:          in.OwnerID,
		Name:            in.Name,
		Src:             in.Src,
		UserListID:      in.UserListID,
		Users:           in.Users,
		Keywords:        keywords,
		ExcludeKeywords: exclude,
		CaseSensitive:   in.CaseSensitive,
		ExcludeBots:     in.ExcludeBots,
		WithReplies:     in.WithReplies,
		WithFile:        in.WithFile,
		LocalOnly:       in.LocalOnly,
		IsActive:        true,
	}
	if err := s.repo.Create(a); err != nil {
		return nil, err
	}
	return a, nil
}

// Show returns the antenna by id, with an ownership check.
func (s *Service) Show(ownerID, antennaID string) (*model.Antenna, error) {
	a, err := s.repo.FindByID(antennaID)
	if err != nil {
		return nil, ErrAntennaNotFound
	}
	if a.UserID != ownerID {
		return nil, ErrAccessDenied
	}
	return a, nil
}

// UpdateInput holds the editable fields of an antenna. nil の field は更新
// されない。
type UpdateInput struct {
	Name            *string
	Src             *model.AntennaSource
	UserListID      *string
	Users           *[]string
	Keywords        *[][]string
	ExcludeKeywords *[][]string
	CaseSensitive   *bool
	ExcludeBots     *bool
	WithReplies     *bool
	WithFile        *bool
	LocalOnly       *bool
	IsActive        *bool
}

// Update applies the non-nil fields to an antenna owned by ownerID.
func (s *Service) Update(ownerID, antennaID string, in UpdateInput) (*model.Antenna, error) {
	a, err := s.repo.FindByID(antennaID)
	if err != nil {
		return nil, ErrAntennaNotFound
	}
	if a.UserID != ownerID {
		return nil, ErrAccessDenied
	}
	fields := map[string]any{}
	if in.Name != nil {
		if *in.Name == "" {
			return nil, ErrAntennaNameRequired
		}
		fields["name"] = *in.Name
	}
	if in.Src != nil {
		if !validSource(*in.Src) {
			return nil, ErrInvalidSource
		}
		fields["src"] = *in.Src
	}
	if in.UserListID != nil {
		// 空文字列での nullify を許すため ポインタ非 nil なら上書き対象扱い。
		fields["userListId"] = in.UserListID
	}
	if in.Users != nil {
		// model.Antenna.Users は pq.StringArray なので plain []string で
		// 渡すと GORM が空 slice を NULL に倒す drift がある (#896 と同 pattern)。
		// Create と同じく pq.StringArray で wrap して空配列 '{}' として
		// 書き込ませる。
		fields["users"] = pq.StringArray(*in.Users)
	}
	if in.Keywords != nil {
		// Marshal of [][]string never fails.
		raw, _ := json.Marshal(normalizeKeywords(*in.Keywords))
		fields["keywords"] = []byte(raw)
	}
	if in.ExcludeKeywords != nil {
		raw, _ := json.Marshal(normalizeKeywords(*in.ExcludeKeywords))
		fields["excludeKeywords"] = []byte(raw)
	}
	if in.CaseSensitive != nil {
		fields["caseSensitive"] = *in.CaseSensitive
	}
	if in.ExcludeBots != nil {
		fields["excludeBots"] = *in.ExcludeBots
	}
	if in.WithReplies != nil {
		fields["withReplies"] = *in.WithReplies
	}
	if in.WithFile != nil {
		fields["withFile"] = *in.WithFile
	}
	if in.LocalOnly != nil {
		fields["localOnly"] = *in.LocalOnly
	}
	if in.IsActive != nil {
		fields["isActive"] = *in.IsActive
	}
	if err := s.repo.UpdateFields(antennaID, fields); err != nil {
		return nil, err
	}
	return s.repo.FindByID(antennaID)
}

// Delete removes an antenna owned by ownerID. Redis stream も併せて削除する。
func (s *Service) Delete(ownerID, antennaID string) error {
	a, err := s.repo.FindByID(antennaID)
	if err != nil {
		return ErrAntennaNotFound
	}
	if a.UserID != ownerID {
		return ErrAccessDenied
	}
	if err := s.repo.Delete(a); err != nil {
		return err
	}
	_ = s.client.Del(context.Background(), streamKey(antennaID)).Err()
	return nil
}

// ListByUser returns the antennas owned by userID.
func (s *Service) ListByUser(userID string) ([]*model.Antenna, error) {
	return s.repo.ListByUser(userID)
}

// Notes returns note ids matched by the antenna, newest first, optionally
// constrained by `untilID` (strict upper bound — return notes strictly older)
// or `sinceID` (strict lower bound — return notes strictly newer)。
// limit <= 0 ならデフォルト 10、上限 100。
//
// Redis Stream の entry id は `<unix_ms>-<seq>` 形式で、pushNote が note の
// 作成時刻 (= idGen.ParseTime で取れる) から派生させて発番している。よって
// untilID / sinceID で渡された noteID を ParseTime → unix_ms に変換し、Redis
// Stream の exclusive range syntax (`(<id>`) で上限/下限を指定できる。
//
// untilID / sinceID が空なら全 range を見て最新 N 件を返す (旧挙動互換)。
// パース失敗時は安全側に bound を緩める (= 全 range)。
func (s *Service) Notes(ctx context.Context, ownerID, antennaID string, limit int, sinceID, untilID string) ([]string, error) {
	if _, err := s.Show(ownerID, antennaID); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 10
	}
	if limit > 100 {
		limit = 100
	}
	end := "+"
	start := "-"
	// `(<ms>-0` exclusive bound は entry id >= "<ms>-0" を全て除外する。
	// pushNote が `<ms>-*` で seq を自動採番するので同 ms に複数 entry が
	// 並びうるが、それらは boundary note と「タイ」とみなしてまとめて除外
	// する (untilID の場合: 厳密に "古い" のみ返す; sinceID の場合は逆)。
	// FE は最後に表示した note の id を渡してくるので、その note 自身を
	// 含めると無限ループするのを避ける重要な不変条件。
	if untilID != "" {
		if t, err := s.idGen.ParseTime(untilID); err == nil {
			end = fmt.Sprintf("(%d-0", t.UnixMilli())
		}
	}
	if sinceID != "" {
		if t, err := s.idGen.ParseTime(sinceID); err == nil {
			start = fmt.Sprintf("(%d-0", t.UnixMilli())
		}
	}
	res, err := s.client.XRevRangeN(ctx, streamKey(antennaID), end, start, int64(limit)).Result()
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(res))
	for _, msg := range res {
		raw, ok := msg.Values["noteId"].(string)
		if !ok {
			continue
		}
		out = append(out, raw)
	}
	return out, nil
}

// OnNoteCreated walks every active antenna, evaluates matchNote, and pushes
// matching notes to the per-antenna Redis stream. Best-effort: list / push の
// 失敗はログだけで上には伝搬しない (呼び出し元はノート作成成功の prerequisite を
// 既に満たしているため)。
func (s *Service) OnNoteCreated(n *model.Note, author *model.User) {
	if n == nil || author == nil {
		return
	}
	rows, err := s.repo.ListAllActive()
	if err != nil {
		return
	}
	now := s.clock()
	for _, a := range rows {
		if !s.matchNote(a, n, author) {
			continue
		}
		_ = s.pushNote(context.Background(), a.ID, n.ID, now)
		// realtime 配信 (#1573): Redis Stream への XAdd (= REST `antennas/notes`
		// 用) とは別に、pubsub topic "antennaTimeline:<id>" へ packed note を
		// publish する。Stream と Pub/Sub は別プリミティブで、AntennaChannel は
		// 後者を Subscribe しているため、これが無いと WS antenna channel に
		// live note が1件も届かない。本家 AntennaService.addNoteToAntenna が
		// redisForTimelines.xadd と globalEventService.publishAntennaStream の
		// 両系統を呼ぶのと一致させる。可視性は push 時点で matchNote ->
		// CanSeeNote (#1464) で owner 視点 gate 済みだが、subscriber 側でも
		// stale-narrowing を OnRedisEvent で re-filter する (#1573 課題2)。
		if s.publisher != nil {
			s.publisher.PublishNote(streamKey(a.ID), n, author)
		}
		// unread tracking: antenna の所有者に対して未読 row を挿入する。
		// Self-authored note は未読扱いしない (TS本家の挙動に合わせる)。
		if s.unreadRepo != nil && a.UserID != author.ID {
			_ = s.unreadRepo.Create(&model.AntennaNoteUnread{
				ID:        s.idGen.Generate(now),
				AntennaID: a.ID,
				NoteID:    n.ID,
				UserID:    a.UserID,
			})
		}
	}
}

// pushNote appends a note id to the antenna's Redis stream with MAXLEN trim.
//
// Stream entry id は `<unix_ms>-*` 形式で発番する: ms 部分は note の作成時刻
// から派生させて時系列順序を保ち、seq 部分は Redis に auto-increment させる。
// 旧実装の `<unix_ms>-0` 固定だと同一 ms に同じアンテナへ複数 note を push
// した際に Redis Stream の monotonic 制約に違反して XADD が失敗していた
// (#693 PR review #1)。`*` を使うと同 ms 内で seq=0,1,2,... と自動採番されて
// 衝突しない。
func (s *Service) pushNote(ctx context.Context, antennaID, noteID string, now time.Time) error {
	streamID := fmt.Sprintf("%d-*", now.UnixMilli())
	return s.client.XAdd(ctx, &redis.XAddArgs{
		Stream: streamKey(antennaID),
		MaxLen: MaxNotesPerAntenna,
		Approx: true,
		ID:     streamID,
		Values: map[string]any{"noteId": noteID},
	}).Err()
}

// matchNote evaluates whether the note satisfies the antenna's filter set.
// 評価順 (短絡):
//  1. visibility が antenna owner から見える (= CanSeeNote 相当) かを check
//  2. localOnly でリモート著者なら false
//  3. excludeBots で bot 著者なら false
//  4. withFile が真でファイル添付がなければ false
//  5. withReplies が偽で reply なら false
//  6. source 別に user フィルタを適用
//  7. keywords (DNF) のいずれかにマッチしなければ false
//  8. excludeKeywords (DNF) のいずれかにマッチすれば false
//
// 旧実装は visibility を一切見ずに matched 判定で antenna stream へ push して
// いたため、`src=all` / `src=users` 等の broad source antenna が「antenna owner が
// follow していない author の followers / specified note」を pickup して
// content leak する IDOR があった (#1464)。CanSeeNote 相当を push 段で
// 1 回 gate することで REST `antennas/notes` と WS `antenna` channel の両方で
// 漏洩を断つ。
func (s *Service) matchNote(a *model.Antenna, n *model.Note, author *model.User) bool {
	// visibility gate: antenna owner を viewer とみなして CanSeeNote 判定する。
	// followingRepo 未配線時は CanSeeNote の semantics 通り `followers` を
	// 投稿者本人以外には見せない fail-closed (= matchSource `home` と同じ方針)。
	owner := &model.User{ID: a.UserID}
	if !corenote.CanSeeNote(owner, n, s.followingRepo) {
		return false
	}
	// followers visibility かつ owner != author で CanSeeNote を通った場合、
	// 内部で `followingRepo.Exists(owner.ID, author.ID) == true` が確定している。
	// `home` source は同じ pair の follow を再 query するため、ヒントとして
	// matchSource に渡し重複呼び出しを避ける (#1467 review nit)。
	ownerFollowsAuthor := n.Visibility == model.NoteVisibilityFollowers && a.UserID != author.ID
	if a.LocalOnly && author.Host != nil && *author.Host != "" {
		return false
	}
	if a.ExcludeBots && author.IsBot {
		return false
	}
	if a.WithFile && len(n.FileIDs) == 0 {
		return false
	}
	if !a.WithReplies && n.ReplyID != nil {
		return false
	}
	if !s.matchSource(a, author, ownerFollowsAuthor) {
		return false
	}
	text := noteText(n)
	if !s.matchKeywords(text, a.Keywords, a.CaseSensitive, true) {
		return false
	}
	if s.matchKeywords(text, a.ExcludeKeywords, a.CaseSensitive, false) {
		return false
	}
	return true
}

// matchSource applies the antenna's source filter.
//
//   - users: whitelist — author.username が a.Users に含まれていれば match
//   - users_blacklist: blacklist — 含まれていなければ match
//   - home: a の owner がフォロー中のユーザーのみ match (followingRepo 必須)
//   - list: a.UserListID に紐づいた UserListMembership に author が含まれる
//   - all: すべて match
//
// followingRepo / userListRepo が未注入のときは対応ソースを match 不成立
// とみなす (all へのフォールバックではなく、設定ミスが検出しやすい側)。
//
// ownerFollowsAuthor は matchNote 内 visibility gate (CanSeeNote) で既に
// `Exists(owner, author) == true` が確定している場合 true。`home` source の
// 重複 Exists 呼び出しをスキップするヒント (#1467 review nit)。
func (s *Service) matchSource(a *model.Antenna, author *model.User, ownerFollowsAuthor bool) bool {
	switch a.Src {
	case model.AntennaSourceUsers:
		return slices.Contains(a.Users, author.Username)
	case model.AntennaSourceUsersBlacklist:
		return !slices.Contains(a.Users, author.Username)
	case model.AntennaSourceHome:
		if ownerFollowsAuthor {
			return true
		}
		if s.followingRepo == nil {
			return false
		}
		ok, err := s.followingRepo.Exists(a.UserID, author.ID)
		return err == nil && ok
	case model.AntennaSourceList:
		if s.userListRepo == nil || a.UserListID == nil || *a.UserListID == "" {
			return false
		}
		members, err := s.userListRepo.ListMembers(*a.UserListID)
		if err != nil {
			return false
		}
		for _, m := range members {
			if m.UserID == author.ID {
				return true
			}
		}
		return false
	default:
		// all
		return true
	}
}

// matchKeywords evaluates a DNF (Disjunctive Normal Form) keyword set:
// outer is OR, inner is AND. emptyMatches=true なら空セットを「常にマッチ」、
// false なら「常に非マッチ」として扱う (keywords は emptyMatches=true、
// excludeKeywords は emptyMatches=false)。
func (s *Service) matchKeywords(text string, raw []byte, caseSensitive bool, emptyMatches bool) bool {
	groups, err := decodeKeywords(raw)
	if err != nil || len(groups) == 0 {
		return emptyMatches
	}
	target := text
	if !caseSensitive {
		target = strings.ToLower(text)
	}
	for _, group := range groups {
		if len(group) == 0 {
			continue
		}
		matched := true
		for _, kw := range group {
			needle := kw
			if !caseSensitive {
				needle = strings.ToLower(kw)
			}
			if !strings.Contains(target, needle) {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

// noteText returns the note's text + cw concatenated for keyword search.
func noteText(n *model.Note) string {
	var sb strings.Builder
	if n.CW != nil {
		sb.WriteString(*n.CW)
		sb.WriteString(" ")
	}
	if n.Text != nil {
		sb.WriteString(*n.Text)
	}
	return sb.String()
}

// decodeKeywords parses the JSON-encoded DNF keyword groups.
func decodeKeywords(raw []byte) ([][]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var groups [][]string
	if err := json.Unmarshal(raw, &groups); err != nil {
		return nil, err
	}
	return groups, nil
}

// normalizeKeywords drops empty rows / empty entries from a DNF set so the
// stored representation does not waste rows that always match.
func normalizeKeywords(in [][]string) [][]string {
	out := make([][]string, 0, len(in))
	for _, row := range in {
		clean := make([]string, 0, len(row))
		for _, kw := range row {
			if strings.TrimSpace(kw) != "" {
				clean = append(clean, kw)
			}
		}
		if len(clean) > 0 {
			out = append(out, clean)
		}
	}
	return out
}

// validSource reports whether s is one of the allowed antenna source values.
func validSource(s model.AntennaSource) bool {
	switch s {
	case model.AntennaSourceAll, model.AntennaSourceHome,
		model.AntennaSourceUsers, model.AntennaSourceUsersBlacklist,
		model.AntennaSourceList:
		return true
	}
	return false
}
