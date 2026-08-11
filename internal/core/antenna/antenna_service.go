// Package antenna provides the user-facing antenna CRUD service plus a
// per-antenna Redis-stream timeline of matching notes. Misskey 互換の
// ローカル限定機能で、ActivityPub 連携は持たない。
package antenna

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
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
	// ErrNoSuchUserList is returned when src=list references a user list the
	// caller does not own / does not exist (upstream noSuchUserList、#1544)。
	ErrNoSuchUserList = errors.New("no such user list")
)

// AllKeywordsEmpty reports whether every entry of a DNF keyword set is the
// empty string (= upstream `keywords.flat().every(x => x === ”)`)。空配列も
// vacuously true を返す。EMPTY_KEYWORD 検査 (endpoint 層) で用いる。
func AllKeywordsEmpty(kw [][]string) bool {
	for _, row := range kw {
		for _, s := range row {
			if s != "" {
				return false
			}
		}
	}
	return true
}

// validateUserList enforces the upstream noSuchUserList check: when src=list
// references a userListId, the list must exist and be owned by ownerID.
// userListRepo 未配線時は skip (= list source を使わない構成)。
func (s *Service) validateUserList(ownerID string, userListID *string) error {
	if s.userListRepo == nil || userListID == nil || *userListID == "" {
		return nil
	}
	list, err := s.userListRepo.FindByID(*userListID)
	if err != nil || list == nil || list.UserID != ownerID {
		return ErrNoSuchUserList
	}
	return nil
}

// MaxNotesPerAntenna caps how many notes are kept per antenna in the Redis
// stream. 古いものから XADD MAXLEN ~ で削除される。
const MaxNotesPerAntenna = 200

// streamKey returns the Redis key for an antenna's note timeline.
//
// # 構造 (#2465 で Stream から ZSET へ変更)
//
// 以前は Redis Stream で、entry id に **push 時刻**を使っていた。一方カーソルの
// 境界は `idGen.ParseTime(untilID)` = **ノートの作成時刻**から作っていたため、
// この 2 つがずれるとページ送りでノートが落ちた。ノート作成と antenna への
// push は別の瞬間なので、近い値にはなっても順序が入れ替わりうる。
//
// 現在は **score を揃えた ZSET に noteID を入れ、辞書順 (= 時刻順) で引く**。
// カーソルの境界がノート ID そのものになるので、push のタイミングは順序に
// 影響しない。ID が辞書順で時刻順になる前提は aid / aidx / meid / objectid /
// ulid の全方式で成り立ち、SQL 側のページングも同じ前提で `id < ?` を使っている。
//
// キー名は据え置く。型が変わるので旧 Stream が残っている環境では WRONGTYPE に
// なるが、読み書きの両方で検出して張り替える (キー名を変えると旧キーが孤児に
// なって残り続ける)。
func streamKey(antennaID string) string {
	return "antennaTimeline:" + antennaID
}

// isWrongType reports whether err is Redis' WRONGTYPE error.
//
// 旧 Stream から ZSET へ張り替える一度きりの移行で使う。
func isWrongType(err error) bool {
	return err != nil && strings.Contains(err.Error(), "WRONGTYPE")
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
	// sensitiveChannels は excludeNotesInSensitiveChannel の判定に使う。
	// nil なら判定を skip する (= 除外しない)。
	sensitiveChannels SensitiveChannelLookup
}

// SensitiveChannelLookup reports whether a channel is flagged sensitive.
// note は channel を埋めて持たないので、判定のたびに channelId から引く。
type SensitiveChannelLookup interface {
	IsSensitiveChannel(channelID string) bool
}

// SetSensitiveChannelLookup wires the lookup used by
// excludeNotesInSensitiveChannel. 未配線なら除外は行われない。
func (s *Service) SetSensitiveChannelLookup(l SensitiveChannelLookup) {
	s.sensitiveChannels = l
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
	OwnerID                        string
	Name                           string
	Src                            model.AntennaSource
	UserListID                     *string
	Users                          []string
	Keywords                       [][]string
	ExcludeKeywords                [][]string
	CaseSensitive                  bool
	ExcludeBots                    bool
	WithReplies                    bool
	WithFile                       bool
	LocalOnly                      bool
	ExcludeNotesInSensitiveChannel bool
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
	// upstream create.ts: src='list' で userListId 指定時、自分が所有する list か検証。
	if in.Src == model.AntennaSourceList {
		if err := s.validateUserList(in.OwnerID, in.UserListID); err != nil {
			return nil, err
		}
	}
	// Marshal of [][]string never fails, so we ignore the error.
	// upstream は送られてきた DNF をそのまま保存する。空文字を含む行を
	// 落とすと `excludeKeywords: [[""]]` のような入力が `[]` に化けて、
	// クライアントが保存した内容を読み戻せない。空文字は matchKeywords の
	// strings.Contains が常に true になるので、マッチ挙動は upstream と同じ。
	keywords, _ := json.Marshal(in.Keywords)
	exclude, _ := json.Marshal(in.ExcludeKeywords)
	now := s.clock()
	a := &model.Antenna{
		ID:                             s.idGen.Generate(now),
		LastUsedAt:                     now,
		UserID:                         in.OwnerID,
		Name:                           in.Name,
		Src:                            in.Src,
		UserListID:                     in.UserListID,
		Users:                          in.Users,
		Keywords:                       keywords,
		ExcludeKeywords:                exclude,
		CaseSensitive:                  in.CaseSensitive,
		ExcludeBots:                    in.ExcludeBots,
		WithReplies:                    in.WithReplies,
		WithFile:                       in.WithFile,
		LocalOnly:                      in.LocalOnly,
		ExcludeNotesInSensitiveChannel: in.ExcludeNotesInSensitiveChannel,
		IsActive:                       true,
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
	Name                           *string
	Src                            *model.AntennaSource
	UserListID                     *string
	Users                          *[]string
	Keywords                       *[][]string
	ExcludeKeywords                *[][]string
	CaseSensitive                  *bool
	ExcludeBots                    *bool
	WithReplies                    *bool
	WithFile                       *bool
	LocalOnly                      *bool
	IsActive                       *bool
	ExcludeNotesInSensitiveChannel *bool
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
	// upstream update.ts: 新 src か既存 src が list で userListId 指定時、
	// 自分が所有する list か検証する。
	if (in.Src != nil && *in.Src == model.AntennaSourceList) || a.Src == model.AntennaSourceList {
		if err := s.validateUserList(ownerID, in.UserListID); err != nil {
			return nil, err
		}
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
		// #2106 L48 (documented limitation): upstream update.ts は userListId の明示 null で clear、
		// 非 list src で null 化するが、Go の *string は JSON の null と absent を区別できないため
		// mk-go は「空文字列で clear」の convention を採る (JSON null clear / 非 list src 自動 null 化は
		// 未対応)。validateUserList 連携の都合で edge を残す。通常 UI 経路 (src=list 時のみ
		// userListId を送る) では乖離しない。
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
		raw, _ := json.Marshal(*in.Keywords)
		fields["keywords"] = []byte(raw)
	}
	if in.ExcludeKeywords != nil {
		raw, _ := json.Marshal(*in.ExcludeKeywords)
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
	if in.ExcludeNotesInSensitiveChannel != nil {
		fields["excludeNotesInSensitiveChannel"] = *in.ExcludeNotesInSensitiveChannel
	}
	// 本家 update.ts は編集のたびに isActive=true へ復帰させる (deactivate された
	// antenna は編集で再び有効化される)。mk-go は isActive をユーザー param として
	// 公開する拡張があるため、明示指定された場合はそれを尊重し、未指定の編集では
	// 本家同様 isActive=true へ復帰させる (#1604)。
	if in.IsActive != nil {
		fields["isActive"] = *in.IsActive
	} else {
		fields["isActive"] = true
	}
	// 本家 update.ts と同じく、更新のたびに lastUsedAt を bump する (#1604)。これに
	// より最近編集された antenna は clean cron で未使用扱いされず deactivate されな
	// い。
	fields["lastUsedAt"] = s.clock()
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

// RemoveNote removes a specific note from an antenna's timeline (upstream
// antennas/remove-note、#2069 #17463)。該当ノートが無い場合は no-op で成功
// (upstream も同様)。
//
// ZSET の member が noteID そのものなので ZREM 一発で消せる (#2465)。Stream
// だった頃は entry id が push 時刻ベースで noteID と対応しないため、全件走査
// して照合する必要があった。
func (s *Service) RemoveNote(ownerID, antennaID, noteID string) error {
	a, err := s.repo.FindByID(antennaID)
	if err != nil {
		return ErrAntennaNotFound
	}
	if a.UserID != ownerID {
		return ErrAccessDenied
	}
	err = s.client.ZRem(context.Background(), streamKey(antennaID), noteID).Err()
	if isWrongType(err) {
		// 旧 Stream。次の push で張り替わるので消す対象が無いのと同じ扱い。
		return nil
	}
	return err
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
	// 本家 antennas/notes.ts は notes 取得のたびに isActive=true + lastUsedAt=now
	// を bump し、使用中の antenna が clean cron (#1604) で deactivate されないよう
	// にする。非アクティブだった antenna はここで再活性化される。best-effort write
	// で、失敗しても timeline 取得は続行する (この file の他の best-effort write と
	// 同様)。OnNoteCreated は毎回 ListAllActive を読むため、再活性化は次の note 配信
	// から即座に反映され、本家の antennaUpdated 内部イベントに相当する cache 無効化
	// は不要。
	_ = s.repo.UpdateFields(antennaID, map[string]any{"isActive": true, "lastUsedAt": s.clock()})
	// この antenna を読んだので未読行を消す (#2406)。
	//
	// **これが無いと `hasUnreadAntenna` が一度 true になると永久に true のまま**
	// になり、`antenna_note_unread` も単調増加する。matchNote 側が行を作る一方で
	// 消す経路がどこにも無かった。
	//
	// upstream は `getHasUnreadAntenna` が `return false; // TODO` で未読機能ごと
	// 止まっているのでこの問題が起きない。mk-go は未読を実際に計算する分、
	// 既読化も自前で持つ必要がある (docs/divergence.md)。
	//
	// 既存の best-effort write と同じ扱いで、失敗しても timeline 取得は続行する。
	// 消し漏れても次回の閲覧で再試行されるだけで、未読が過剰に出る方向にしか
	// ずれない (既読の note を未読と誤表示することはあっても、その逆は無い)。
	if s.unreadRepo != nil {
		_ = s.unreadRepo.DeleteByAntennaUser(ownerID, antennaID)
	}
	if limit <= 0 {
		limit = 10
	}
	if limit > 100 {
		limit = 100
	}
	// 境界は **ノート ID そのもの** (#2465)。以前は ID から時刻を取り出して
	// stream の entry id (= push 時刻) と突き合わせていたため、作成時刻と push
	// 時刻がずれるとページ送りでノートが落ちた。ID を直接使えばこのずれは
	// 原理的に発生しない。
	//
	// `(` は排他境界。FE は最後に表示した note の id を渡してくるので、その
	// note 自身を含めると無限ループする。これは重要な不変条件。
	min := "-"
	max := "+"
	if untilID != "" {
		max = "(" + untilID
	}
	if sinceID != "" {
		min = "(" + sinceID
	}

	// upstream notes.ts: sinceId 単独 (fetch-newer / scroll-up) は昇順
	// (oldest-first) で sinceId 直後の最古 limit 件を返す。それ以外 (untilId /
	// 無指定) は降順 (newest-first)。常に newest-first だと sinceId 単独ページの
	// 並びと集合が逆になる (#1778)。
	key := streamKey(antennaID)
	by := &redis.ZRangeBy{Min: min, Max: max, Count: int64(limit)}
	var (
		out []string
		err error
	)
	if sinceID != "" && untilID == "" {
		out, err = s.client.ZRangeByLex(ctx, key, by).Result()
	} else {
		out, err = s.client.ZRevRangeByLex(ctx, key, by).Result()
	}
	if err != nil {
		if isWrongType(err) {
			// 旧 Stream が残っている環境。次の push で張り替わるので、
			// ここでは空を返す (読み取りで消すと、書き込みの無い
			// antenna が読むたびに DEL を撃つことになる)。
			return nil, nil
		}
		return nil, err
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
	// 同じ note を全アンテナで評価するので、owner ごとの follow 判定と
	// list メンバーシップは 1 回引いて使い回す。アンテナ数に比例して DB を
	// 叩くと push が note 作成に対して大きく遅れ、直後に antennas/notes を
	// 読むと取りこぼす。
	memo := newMatchMemo()
	for _, a := range rows {
		if !s.matchNote(a, n, author, memo) {
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
		//
		// ここでは ephemeral note の materialize を **あえて行わない** (#2332)。
		// antenna_note_unread.noteId は note への外部キーなので、リレー由来で
		// DB に無いノートでは Upsert が失敗して未読が付かない。それでも
		// materialize しないのは、広い条件の antenna を 1 つ作るだけで全ての
		// リレー投稿が DB に落ち、機能そのものが無効化されてしまうため。
		//
		// antenna のタイムライン自体は Redis の fanout で成立するので、欠ける
		// のは未読バッジの件数だけ。ユーザーが実際に触れば (リアクション等)
		// そこで materialize される。
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
func (s *Service) pushNote(ctx context.Context, antennaID, noteID string, _ time.Time) error {
	key := streamKey(antennaID)
	if err := s.zaddNote(ctx, key, noteID); err != nil {
		if !isWrongType(err) {
			return err
		}
		// 旧 Stream が残っている。一度だけ捨てて張り替える (#2465)。ここで
		// 消えるのは高々 MaxNotesPerAntenna 件の timeline entry で、ノート
		// 本体ではない。次の push から正しい構造で積み上がる。
		if delErr := s.client.Del(ctx, key).Err(); delErr != nil {
			return delErr
		}
		if err := s.zaddNote(ctx, key, noteID); err != nil {
			return err
		}
	}
	// 上限を維持する。score が全て同じなので rank は辞書順 = 時刻順。
	// 新しい方 (末尾) を残すので、古い側の余剰を落とす。
	return s.client.ZRemRangeByRank(ctx, key, 0, -int64(MaxNotesPerAntenna)-1).Err()
}

// zaddNote inserts the note ID with a constant score so ZRANGEBYLEX orders by
// the ID itself.
//
// **score は必ず 0 で揃える。** バラつくと ZRANGEBYLEX の結果が未定義になる。
func (s *Service) zaddNote(ctx context.Context, key, noteID string) error {
	return s.client.ZAdd(ctx, key, redis.Z{Score: 0, Member: noteID}).Err()
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
func (s *Service) matchNote(a *model.Antenna, n *model.Note, author *model.User, memo *matchMemo) bool {
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
	// upstream AntennaService:117 と同じ gate。channel が sensitive なら
	// excludeNotesInSensitiveChannel のアンテナからは落とす。
	if a.ExcludeNotesInSensitiveChannel && n.ChannelID != nil && *n.ChannelID != "" &&
		s.sensitiveChannels != nil && s.sensitiveChannels.IsSensitiveChannel(*n.ChannelID) {
		return false
	}
	if a.WithFile && len(n.FileIDs) == 0 {
		return false
	}
	if !a.WithReplies && n.ReplyID != nil {
		return false
	}
	if !s.matchSource(a, author, ownerFollowsAuthor, memo) {
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

// matchMemo caches the per-note lookups shared across antennas.
type matchMemo struct {
	follows map[string]bool
	lists   map[string]map[string]struct{}
}

func newMatchMemo() *matchMemo {
	return &matchMemo{follows: map[string]bool{}, lists: map[string]map[string]struct{}{}}
}

// ownerFollows reports whether ownerID follows authorID, caching the answer for
// the lifetime of one note fan-out.
func (m *matchMemo) ownerFollows(s *Service, ownerID, authorID string) bool {
	if s.followingRepo == nil {
		return false
	}
	if m == nil {
		ok, err := s.followingRepo.Exists(ownerID, authorID)
		return err == nil && ok
	}
	if v, hit := m.follows[ownerID]; hit {
		return v
	}
	ok, err := s.followingRepo.Exists(ownerID, authorID)
	v := err == nil && ok
	m.follows[ownerID] = v
	return v
}

// listContains reports whether authorID belongs to listID, caching the member
// set for the lifetime of one note fan-out.
func (m *matchMemo) listContains(s *Service, listID, authorID string) bool {
	if s.userListRepo == nil || listID == "" {
		return false
	}
	fetch := func() map[string]struct{} {
		members, err := s.userListRepo.ListMembers(listID)
		if err != nil {
			return nil
		}
		set := make(map[string]struct{}, len(members))
		for _, mem := range members {
			set[mem.UserID] = struct{}{}
		}
		return set
	}
	if m == nil {
		_, ok := fetch()[authorID]
		return ok
	}
	set, hit := m.lists[listID]
	if !hit {
		set = fetch()
		m.lists[listID] = set
	}
	_, ok := set[authorID]
	return ok
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
func (s *Service) matchSource(a *model.Antenna, author *model.User, ownerFollowsAuthor bool, memo *matchMemo) bool {
	switch a.Src {
	case model.AntennaSourceUsers:
		return matchesAntennaAcct(a.Users, author)
	case model.AntennaSourceUsersBlacklist:
		return !matchesAntennaAcct(a.Users, author)
	case model.AntennaSourceHome:
		if ownerFollowsAuthor {
			return true
		}
		if s.followingRepo == nil {
			return false
		}
		return memo.ownerFollows(s, a.UserID, author.ID)
	case model.AntennaSourceList:
		if s.userListRepo == nil || a.UserListID == nil || *a.UserListID == "" {
			return false
		}
		return memo.listContains(s, *a.UserListID, author.ID)
	default:
		// all
		return true
	}
}

// matchesAntennaAcct reports whether the note author is listed in the antenna's
// `users` set.
//
// upstream は保存値を Acct.parse して `username@host` (local は host 無し) に
// 正規化してから比較する。frontend が送るのは `@bob` / `@bob@remote.example`
// の acct 形式なので、username と素で比較すると 1 件も一致しない。
// 大文字小文字は upstream 同様無視する。
func matchesAntennaAcct(users []string, author *model.User) bool {
	if author == nil {
		return false
	}
	want := strings.ToLower(author.Username)
	if author.Host != nil && *author.Host != "" {
		want += "@" + strings.ToLower(*author.Host)
	}
	for _, u := range users {
		if strings.ToLower(strings.TrimPrefix(strings.TrimSpace(u), "@")) == want {
			return true
		}
	}
	return false
}

// matchKeywords evaluates a DNF (Disjunctive Normal Form) keyword set:
// outer is OR, inner is AND. emptyMatches=true なら空セットを「常にマッチ」、
// false なら「常に非マッチ」として扱う (keywords は emptyMatches=true、
// excludeKeywords は emptyMatches=false)。
func (s *Service) matchKeywords(text string, raw []byte, caseSensitive bool, emptyMatches bool) bool {
	groups, err := decodeKeywords(raw)
	if err != nil {
		return emptyMatches
	}
	// upstream AntennaService は判定の直前に空文字の要素と空になった行を
	// 落とす (`map(xs => xs.filter(x => x !== '')).filter(xs => xs.length > 0)`)。
	// 保存値は素通しなので、`[[""]]` のような入力は「キーワード指定なし」と
	// 同じ扱いになる。ここで落とさないと空文字が全 note に部分一致して
	// 全件マッチ (keywords) / 全件除外 (excludeKeywords) になる。
	groups = cleanKeywordGroups(groups)
	if len(groups) == 0 {
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

// cleanKeywordGroups drops empty entries and rows that become empty, mirroring
// upstream AntennaService's clean-up right before matching.
func cleanKeywordGroups(groups [][]string) [][]string {
	out := make([][]string, 0, len(groups))
	for _, row := range groups {
		clean := make([]string, 0, len(row))
		for _, kw := range row {
			if kw != "" {
				clean = append(clean, kw)
			}
		}
		if len(clean) > 0 {
			out = append(out, clean)
		}
	}
	return out
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

// OnMoveAccount appends dst's acct to every antenna whose `users` set lists src.
//
// upstream AntennaService.onMoveAccount 相当 (#2419)。アカウント移行時に
// 「src をアンテナに登録していた人」のアンテナへ dst を**追記**する。
//
// **src は消さない。** 移行の引き継ぎは一貫して「旧側を消さずに新側を足す」
// 方向で、旧アカウントがまだ投稿しうる以上そちらの購読も残す。
//
// best-effort。呼び出し時点で移行そのものは確定しているので、失敗しても
// エラーを返さず log に落とす。
func (s *Service) OnMoveAccount(src, dst *model.User) {
	if src == nil || dst == nil || s.repo == nil {
		return
	}
	antennas, err := s.repo.ListAllActive()
	if err != nil {
		slog.Warn("antenna: list for move failed", "srcID", src.ID, "dstID", dst.ID, "err", err)
		return
	}
	dstAcct := "@" + acctOf(dst)
	for _, a := range antennas {
		// users / usersBlacklist のどちらも `users` 配列を使うので、src を
		// 挙げているものは source の種別を問わず追随させる (upstream も
		// antenna.users だけを見て src を含むか判定している)。
		if !matchesAntennaAcct(a.Users, src) {
			continue
		}
		// 既に dst が入っているアンテナは触らない (再実行しても増えない)。
		if matchesAntennaAcct(a.Users, dst) {
			continue
		}
		next := append(append(pq.StringArray{}, a.Users...), dstAcct)
		if err := s.repo.UpdateFields(a.ID, map[string]any{"users": next}); err != nil {
			slog.Warn("antenna: append moved account failed",
				"antennaID", a.ID, "dstID", dst.ID, "err", err)
			continue
		}
		a.Users = next
	}
}

// acctOf renders `username` (local) or `username@host` (remote), lower-cased to
// match matchesAntennaAcct's comparison.
func acctOf(u *model.User) string {
	acct := strings.ToLower(u.Username)
	if u.Host != nil && *u.Host != "" {
		acct += "@" + strings.ToLower(*u.Host)
	}
	return acct
}
