package repository

import (
	"strconv"
	"strings"
	"time"

	"github.com/shiroha-a/mk/internal/model"
	"gorm.io/gorm"
)

// aidxTime2000Ms は aidx ID のタイムスタンプ部分 (最初の8文字 base36) の
// epoch 起点 (2000-01-01T00:00:00Z in ms since Unix epoch)。
// misc/id パッケージに同値の非公開定数があるが、本ファイル内で時刻→ID 先頭
// 変換に使うため独自に定義する。
const aidxTime2000Ms int64 = 946684800000

// aidxCutoffID は与えられた time に対応する最小のaidx ID文字列を返す。
// aidxは「時刻base36(8) + nodeID(4) + counter(4)」の 16 文字で、先頭 8 文字が
// ms-since-2000 を base36 で表したもの。そのため lexicographic 比較で時刻順に
// 並ぶ。時刻以降の全IDを 「id >= aidxCutoffID(t)」 で、時刻以前を
// 「id < aidxCutoffID(t)」 で拾える。
func aidxCutoffID(t time.Time) string {
	ms := t.UnixMilli() - aidxTime2000Ms
	if ms < 0 {
		ms = 0
	}
	prefix := strconv.FormatInt(ms, 36)
	if len(prefix) < 8 {
		prefix = strings.Repeat("0", 8-len(prefix)) + prefix
	} else if len(prefix) > 8 {
		// 想定外 (2089年以降) だが安全側で末尾8文字を使う
		prefix = prefix[len(prefix)-8:]
	}
	return prefix + "00000000"
}

// NoteRepository provides data access for notes.
type NoteRepository interface {
	Create(note *model.Note) error
	FindByID(id string) (*model.Note, error)
	// FindByIDWithUser fetches a note with only its author (`User`) preloaded.
	// Renote / Reply の embed までは要らない caller (visibility チェック等) 向け
	// の軽量経路 (#425)。それらが必要な場合は FindByIDWithRelations を使う。
	FindByIDWithUser(id string) (*model.Note, error)
	// FindByIDWithRelations fetches a note with User / Renote / Renote.User /
	// Reply / Reply.User を 1 階層分 preload する。pack/render に流す経路向け
	// (#425)。GORM の preload は明示した relation しか辿らないため N+1 には
	// ならない。
	FindByIDWithRelations(id string) (*model.Note, error)
	FindByURI(uri string) (*model.Note, error)
	Delete(note *model.Note) error
	Update(note *model.Note, column string, value any) error
	UpdateFields(noteID string, fields map[string]any) error
	IncrementCount(noteID, column string, delta int) error
	IncrementReaction(noteID, reaction string, delta int) error
	ListByUserID(userID string, untilID, sinceID string, limit int) ([]*model.Note, error)
	// ListByUserIDFiltered returns notes owned by userID with upstream
	// `users/notes` filter parameters applied. 4 つの bool は upstream paramDef
	// と同じ semantics で各々:
	//   - withFiles: true なら fileIds 非空のみ (デフォルト false)
	//   - withReplies: false なら reply を除外 (デフォルト true)
	//   - withRenotes: false なら pure renote を除外 (デフォルト true)
	//   - withChannelNotes: false なら channel 投稿を除外 (デフォルト false)
	//
	// caller (handler) は JSON pointer field から bool 値を必ず upstream の
	// デフォルトに合わせて詰めること (#1021)。
	//
	// viewerID は visibility push-down のための閲覧者 ID。空文字は匿名
	// (public/home のみ)。core/note.CanSeeNote と同じ可視性条件を LIMIT 前に
	// SQL で絞ることで、post-fetch filter によるページ過少充填と followers
	// 判定の N+1 を避ける (#1418 review)。
	ListByUserIDFiltered(userID, viewerID, untilID, sinceID string, limit int, withFiles, withReplies, withRenotes, withChannelNotes bool) ([]*model.Note, error)
	// ListByChannelID は channel の note を viewer の可視性で絞って返す。
	// channels/timeline は public channel 自体が誰でも閲覧可能だが、そこに投稿
	// された note 個々の visibility (followers / specified) は viewer 単位で
	// 絞らないと非フォロワーに followers note が流出する (IDOR, #1440)。
	// viewerID 空文字は匿名 (public/home のみ)。条件は core/note.CanSeeNote と
	// 一致 (clip_note.ListByClipVisible と同じ式)。#1449 review で
	// ListByChannelIDVisible を別メソッドに切り出さず本 signature に viewerID を
	// 取り込む形に統合した (#1439 / #1441 と同じパターン)。
	ListByChannelID(channelID, viewerID, untilID, sinceID string, limit int) ([]*model.Note, error)
	FindManyByIDsWithUser(ids []string) ([]*model.Note, error)
	ListRenotesOf(noteID string, untilID, sinceID string, limit int) ([]*model.Note, error)
	ListRepliesOf(noteID string, untilID, sinceID string, limit int) ([]*model.Note, error)
	ListChildrenOf(noteID string, untilID, sinceID string, limit int) ([]*model.Note, error)
	SearchByFilter(filter model.NoteSearchFilter) ([]*model.Note, error)
	// ListFeatured returns ranked public notes (renote+reply count). When
	// channelID is non-empty restricts to that channel, mirroring upstream
	// FeaturedService.getInChannelNotesRanking. untilID enables cursor
	// pagination (id < untilID). offset is honoured only when no cursor
	// is given.
	ListFeatured(channelID, untilID string, limit, offset int) ([]*model.Note, error)
	FindRenoteByUser(userID, renoteID string) (*model.Note, error)
	ListMentions(userID string, limit int, sinceID, untilID string) ([]*model.Note, error)
	// SearchByTag returns notes carrying tag that viewerID is allowed to see.
	// viewerID 空文字は匿名 (public/home のみ)。discovery 系の tag 検索は
	// ID 既知公開 (notes/show) doctrine の対象外なので、core/note.CanSeeNote と
	// 同じ可視性条件を LIMIT 前に SQL で絞る (#1439)。
	SearchByTag(tag, viewerID string, limit int, sinceID, untilID string) ([]*model.Note, error)
	// ListByFileID returns notes whose fileIds array contains fileID, with
	// cursor (sinceID/untilID) pagination matching upstream Misskey's
	// makePaginationQuery. limit <= 0 defaults to 10.
	ListByFileID(fileID, sinceID, untilID string, limit int) ([]*model.Note, error)
	IncrementUserNotesCount(userID string, delta int) error
	ListHomeTimeline(userID string, limit int, sinceID, untilID string, filter model.TimelineDBFilter) ([]*model.Note, error)
	ListLocalTimeline(limit int, sinceID, untilID string, filter model.TimelineDBFilter) ([]*model.Note, error)
	ListGlobalTimeline(limit int, sinceID, untilID string, filter model.TimelineDBFilter) ([]*model.Note, error)
	// DeleteExpiredRemoteNotes deletes up to batchSize remote notes older
	// than expiryDays in a single DELETE statement. Returns the count
	// actually removed in this batch. Callers drive the loop themselves so
	// cancellation checkpoints and sleep pacing live in the processor
	// (see CleanRemoteNotesProcessor).
	DeleteExpiredRemoteNotes(expiryDays, batchSize int) (int64, error)
	// DeleteByUserBatch deletes up to batchSize notes authored by userID in a
	// single DELETE statement. Returns the count actually removed. Callers
	// drive the loop themselves so that cancellation checkpoints and sleep
	// pacing live in the processor (see DeleteAccountProcessor).
	DeleteByUserBatch(userID string, batchSize int) (int64, error)
	// ListByUserList returns notes authored by members of the given user list
	// with keyset pagination via paginationOrder (DESC by default; ASC when
	// only sinceID is supplied). Channel notes are excluded.
	ListByUserList(listID string, limit int, sinceID, untilID string) ([]*model.Note, error)
	// CountReplyTargets returns the users that userID most frequently replies
	// to, ordered by reply count descending. Used by
	// users/get-frequently-replied-users.
	CountReplyTargets(userID string, limit int) ([]model.ReplyTargetCount, error)
	// CountLocalNotes returns the number of notes authored by local users
	// (userHost IS NULL). nodeinfo `usage.localPosts` 相当 (#403)。
	CountLocalNotes() (int64, error)
	// CountLocalComments returns the number of reply notes from local users.
	// nodeinfo `usage.localComments` 相当 (#403)。
	CountLocalComments() (int64, error)
}

type noteRepository struct {
	db *gorm.DB
}

// NewNoteRepository creates a new NoteRepository.
func NewNoteRepository(db *gorm.DB) NoteRepository {
	return &noteRepository{db: db}
}

// preloadNoteRelations applies Preload for the author plus 1 level of
// reply/renote targets (with their authors). NoteEntity の packer は
// renote/reply の target note を埋めるためこれらの preload が必須。
// GORM の preload は明示した relation しか辿らないので N+1 には成らない。
//
// Poll は note.hasPoll==true のとき 1:1 で attach する (#690)。Preload は
// 自動的に hasPoll=false の note では何も読まないので overhead は小さい
// (発行されるのは poll table への 1 IN 句 query)。
func preloadNoteRelations(db *gorm.DB) *gorm.DB {
	return db.Preload("User").
		Preload("Renote").
		Preload("Renote.User").
		Preload("Renote.Poll").
		Preload("Reply").
		Preload("Reply.User").
		Preload("Reply.Poll").
		Preload("Poll")
}

func (r *noteRepository) Create(note *model.Note) error {
	return r.db.Create(note).Error
}

func (r *noteRepository) FindByID(id string) (*model.Note, error) {
	var note model.Note
	if err := r.db.First(&note, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &note, nil
}

func (r *noteRepository) FindByIDWithUser(id string) (*model.Note, error) {
	var note model.Note
	if err := r.db.Preload("User").First(&note, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &note, nil
}

func (r *noteRepository) FindByIDWithRelations(id string) (*model.Note, error) {
	var note model.Note
	if err := preloadNoteRelations(r.db).First(&note, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &note, nil
}

// FindByURI looks up a note by its ActivityPub URI. リモート由来の note は
// uri 列に作成元のIRIが入っているため、配信や inbox 処理での重複検出に使う。
func (r *noteRepository) FindByURI(uri string) (*model.Note, error) {
	var note model.Note
	if err := r.db.Where("uri = ?", uri).First(&note).Error; err != nil {
		return nil, err
	}
	return &note, nil
}

func (r *noteRepository) Delete(note *model.Note) error {
	return r.db.Delete(note).Error
}

func (r *noteRepository) Update(note *model.Note, column string, value any) error {
	return r.db.Model(note).Update(column, value).Error
}

// UpdateFields applies a map of column → value updates to the note row keyed
// by id. Update (single column) は既存呼び出し互換のため残す。
func (r *noteRepository) UpdateFields(noteID string, fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}
	return r.db.Model(&model.Note{}).Where("id = ?", noteID).Updates(fields).Error
}

// CountLocalNotes returns the total number of notes authored by local users,
// including replies. nodeinfo `usage.localPosts` は本家 Misskey / Mastodon
// と同じ「local post の総数」定義なので replies も含めて数える。
// localComments (reply subset) との重複は仕様どおり。
// また soft-delete された user 由来の note も含む (userHost IS NULL だけで判定)。
// 本家 Misskey の countNotes も同じ挙動で、orphan note を除外するには
// 別途 JOIN が要るが nodeinfo の用途では不要コスト。
func (r *noteRepository) CountLocalNotes() (int64, error) {
	var count int64
	err := r.db.Model(&model.Note{}).
		Where(`"userHost" IS NULL`).
		Count(&count).Error
	return count, err
}

// CountLocalComments returns the number of reply notes by local users.
func (r *noteRepository) CountLocalComments() (int64, error) {
	var count int64
	err := r.db.Model(&model.Note{}).
		Where(`"userHost" IS NULL`).
		Where(`"replyId" IS NOT NULL`).
		Count(&count).Error
	return count, err
}

// IncrementCount adjusts a counter column on the note row by delta.
// 集計列の更新はGORMのUpdateColumnでSQL式を直接適用する。
func (r *noteRepository) IncrementCount(noteID, column string, delta int) error {
	return r.db.Model(&model.Note{}).
		Where("id = ?", noteID).
		UpdateColumn(column, gorm.Expr("\""+column+"\" + ?", delta)).Error
}

// IncrementReaction increments (or decrements when delta<0) the value of a
// reaction key inside the note.reactions JSONB object. 0以下になったキーは
// JSONBオブジェクトから完全に削除される。
//
// 結果値が正なら jsonb_set でカウントを上書き、0以下なら "reactions - key" で
// キーごと削除する。 CASE 式で1クエリにまとめている。
func (r *noteRepository) IncrementReaction(noteID, reaction string, delta int) error {
	expr := "CASE WHEN COALESCE((reactions->>?)::int, 0) + ? > 0 " +
		"THEN jsonb_set(reactions, ?, to_jsonb(COALESCE((reactions->>?)::int, 0) + ?)::jsonb, true) " +
		"ELSE reactions - ?::text END"
	pathArr := "{" + reaction + "}"
	return r.db.Model(&model.Note{}).
		Where("id = ?", noteID).
		UpdateColumn("reactions",
			gorm.Expr(expr, reaction, delta, pathArr, reaction, delta, reaction)).Error
}

// ListByUserID returns the user's notes ordered by id, optionally
// constrained by sinceID/untilID for keyset pagination.
func (r *noteRepository) ListByUserID(userID string, untilID, sinceID string, limit int) ([]*model.Note, error) {
	var notes []*model.Note
	q := preloadNoteRelations(r.db).Where("\"userId\" = ?", userID)
	if untilID != "" {
		q = q.Where("id < ?", untilID)
	}
	if sinceID != "" {
		q = q.Where("id > ?", sinceID)
	}
	if err := q.Order(paginationOrder(sinceID, untilID, "id")).Limit(limit).Find(&notes).Error; err != nil {
		return nil, err
	}
	return notes, nil
}

// ListByUserIDFiltered は ListByUserID に upstream 互換 filter を加えた版。
// fileIds 非空 / replyId / renoteId / channelId それぞれの SQL filter を
// optional に追加する。bool 引数は upstream paramDef のデフォルトに合わせて
// handler 側で必ず詰めること (例: handler は withReplies=true / withRenotes=true
// / withChannelNotes=false で渡す)。filter struct ではなく bool 引数で受ける
// のは testutil ↔ repository の import cycle を避けるため (#1021)。
func (r *noteRepository) ListByUserIDFiltered(userID, viewerID, untilID, sinceID string, limit int, withFiles, withReplies, withRenotes, withChannelNotes bool) ([]*model.Note, error) {
	var notes []*model.Note
	q := preloadNoteRelations(r.db).Where(`"userId" = ?`, userID)
	// upstream の generateVisibilityQuery 相当を LIMIT 前に push down する。
	// post-fetch filter だと viewer が見られない note を除外した分 1 ページが
	// limit 未満になりページネーションが途切れる + followers 判定が note ごとの
	// N+1 になるため、LIMIT 前に SQL で絞る。条件は core/note.CanSeeNote と
	// 一致させる (note_reaction.ListByUserID と同じ可視性定義)。
	if viewerID == "" {
		q = q.Where(`"visibility" IN ('public','home')`)
	} else {
		q = q.Where(
			`("visibility" IN ('public','home') `+
				`OR "userId" = ? `+
				`OR ("visibility" = 'followers' AND "userId" IN (SELECT f."followeeId" FROM "following" f WHERE f."followerId" = ?)) `+
				`OR ("visibility" = 'specified' AND ? = ANY("visibleUserIds")))`,
			viewerID, viewerID, viewerID)
	}
	if untilID != "" {
		q = q.Where("id < ?", untilID)
	}
	if sinceID != "" {
		q = q.Where("id > ?", sinceID)
	}
	if withFiles {
		// upstream TL filter (`applyTimelineFilter` 同 file 内) と同じ式。
		// 空 array リテラル `'{}'` と比較することで「fileIds 非空」を絞り込める。
		q = q.Where(`"fileIds" != '{}'`)
	}
	if !withReplies {
		q = q.Where(`"replyId" IS NULL`)
	}
	if !withRenotes {
		// pure renote (= text / fileIds / poll 全て空 + renoteId あり) を除外する。
		// quote (= text あり + renoteId) は通常の投稿として残す。
		q = q.Where(`NOT ("renoteId" IS NOT NULL AND "text" IS NULL AND "fileIds" = '{}' AND "hasPoll" = false)`)
	}
	if !withChannelNotes {
		q = q.Where(`"channelId" IS NULL`)
	}
	if err := q.Order(paginationOrder(sinceID, untilID, "id")).Limit(limit).Find(&notes).Error; err != nil {
		return nil, err
	}
	return notes, nil
}

// ListByChannelID returns notes posted to the channel that viewerID can see.
// viewerID="" means anonymous (public/home only). Visibility conditions are
// pushed down before LIMIT to mirror core/note.CanSeeNote and avoid the
// underfilled-page / N+1 follower-check problems of post-fetch filtering
// (#1440).
func (r *noteRepository) ListByChannelID(channelID, viewerID, untilID, sinceID string, limit int) ([]*model.Note, error) {
	var notes []*model.Note
	q := preloadNoteRelations(r.db).Where("\"channelId\" = ?", channelID)
	// core/note.CanSeeNote と同じ可視性条件を LIMIT 前に SQL で絞る。
	// post-fetch filter だとページが過少充填されるのと followers 判定が
	// note ごとの N+1 になるため LIMIT 前に push down する (#1440)。
	if viewerID == "" {
		q = q.Where(`"visibility" IN ('public','home')`)
	} else {
		q = q.Where(
			`("visibility" IN ('public','home') `+
				`OR "userId" = ? `+
				`OR ("visibility" = 'followers' AND "userId" IN (SELECT f."followeeId" FROM "following" f WHERE f."followerId" = ?)) `+
				`OR ("visibility" = 'specified' AND ? = ANY("visibleUserIds")))`,
			viewerID, viewerID, viewerID)
	}
	if untilID != "" {
		q = q.Where("id < ?", untilID)
	}
	if sinceID != "" {
		q = q.Where("id > ?", sinceID)
	}
	if limit <= 0 {
		limit = 30
	}
	if err := q.Order(paginationOrder(sinceID, untilID, "id")).Limit(limit).Find(&notes).Error; err != nil {
		return nil, err
	}
	return notes, nil
}

// ListRenotesOf returns notes whose renoteId equals noteID.
// テキストやファイルを伴わない pure renote だけでなく quote renote も含む。
func (r *noteRepository) ListRenotesOf(noteID string, untilID, sinceID string, limit int) ([]*model.Note, error) {
	var notes []*model.Note
	q := preloadNoteRelations(r.db).Where("\"renoteId\" = ?", noteID)
	if untilID != "" {
		q = q.Where("id < ?", untilID)
	}
	if sinceID != "" {
		q = q.Where("id > ?", sinceID)
	}
	if err := q.Order(paginationOrder(sinceID, untilID, "id")).Limit(limit).Find(&notes).Error; err != nil {
		return nil, err
	}
	return notes, nil
}

// ListRepliesOf returns notes whose replyId equals noteID.
// すべてのユーザーからの返信を返す(ミュート判定はServiceで行う)。
func (r *noteRepository) ListRepliesOf(noteID string, untilID, sinceID string, limit int) ([]*model.Note, error) {
	var notes []*model.Note
	q := preloadNoteRelations(r.db).Where("\"replyId\" = ?", noteID)
	if untilID != "" {
		q = q.Where("id < ?", untilID)
	}
	if sinceID != "" {
		q = q.Where("id > ?", sinceID)
	}
	if err := q.Order(paginationOrder(sinceID, untilID, "id")).Limit(limit).Find(&notes).Error; err != nil {
		return nil, err
	}
	return notes, nil
}

// ListChildrenOf returns notes that are either replies or quote-renotes of the given noteID.
// notes/childrenでスレッドツリーの直下を取得するために使用する。
func (r *noteRepository) ListChildrenOf(noteID string, untilID, sinceID string, limit int) ([]*model.Note, error) {
	var notes []*model.Note
	q := preloadNoteRelations(r.db).
		Where("\"replyId\" = ? OR \"renoteId\" = ?", noteID, noteID)
	if untilID != "" {
		q = q.Where("id < ?", untilID)
	}
	if sinceID != "" {
		q = q.Where("id > ?", sinceID)
	}
	if err := q.Order(paginationOrder(sinceID, untilID, "id")).Limit(limit).Find(&notes).Error; err != nil {
		return nil, err
	}
	return notes, nil
}

// SearchByFilter returns notes matching the filter criteria.
// 検索バックエンド (core/search.SQLLikeProvider) から呼ばれる。
// When f.Pgroonga is true the predicate uses the PGroonga `&@~` full-text
// operator; otherwise it falls back to ILIKE substring match. Visibility is
// always limited to public/home.
func (r *noteRepository) SearchByFilter(f model.NoteSearchFilter) ([]*model.Note, error) {
	var notes []*model.Note
	q := preloadNoteRelations(r.db)
	if f.Pgroonga {
		// upstream TS では `note.text &@~ :q` を使用。`&@~` は PGroonga の
		// "クエリ文字列モード" マッチで、空白区切りの AND/OR 等が解釈される。
		q = q.Where("text &@~ ?", f.Query)
	} else {
		q = q.Where("text ILIKE ?", "%"+f.Query+"%")
	}
	q = q.Where("visibility IN ?", []string{
		string(model.NoteVisibilityPublic),
		string(model.NoteVisibilityHome),
	})
	if f.UserID != "" {
		q = q.Where("\"userId\" = ?", f.UserID)
	}
	if f.ChannelID != "" {
		q = q.Where("\"channelId\" = ?", f.ChannelID)
	}
	if f.Host != "" {
		// "." はローカル限定 (userHost が NULL のもの)
		if f.Host == "." {
			q = q.Where("\"userHost\" IS NULL")
		} else {
			q = q.Where("\"userHost\" = ?", f.Host)
		}
	}
	if f.UntilID != "" {
		q = q.Where("id < ?", f.UntilID)
	}
	if f.SinceID != "" {
		q = q.Where("id > ?", f.SinceID)
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 10
	}
	if err := q.Order(paginationOrder(f.SinceID, f.UntilID, "id")).Limit(limit).Find(&notes).Error; err != nil {
		return nil, err
	}
	return notes, nil
}

// FindManyByIDsWithUser returns the requested notes preserving the order of `ids`.
// Notes that are not found are simply omitted from the result.
func (r *noteRepository) FindManyByIDsWithUser(ids []string) ([]*model.Note, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	// timeline read hot path。GORM の Preload は relation ごとに別クエリを発行する
	// ため preloadNoteRelations (8 relation) は 1 ページ ~9 往復になる (#1368 計測)。
	// ここでは relation を種類ごとに batch IN で引いて Go 側で配線し、往復を 4 query
	// (notes / sub-notes / users / polls) に畳む。出力 shape は preload 版と同一
	// (TestNoteRepository_FindManyByIDsWithUser_HydratesRelations が回帰を捕捉)。
	// preload は Renote/Reply を 1 段しか展開しない (maxNoteEmbedDepth=1) ので
	// sub-note の renote/reply は辿らない。
	var notes []*model.Note
	if err := r.db.Where("id IN ?", ids).Find(&notes).Error; err != nil {
		return nil, err
	}
	if len(notes) == 0 {
		return nil, nil
	}

	// (2) renote / reply 先 (sub-note) を 1 query で取得し配線する。
	subIDSet := make(map[string]struct{})
	for _, n := range notes {
		if n.RenoteID != nil && *n.RenoteID != "" {
			subIDSet[*n.RenoteID] = struct{}{}
		}
		if n.ReplyID != nil && *n.ReplyID != "" {
			subIDSet[*n.ReplyID] = struct{}{}
		}
	}
	subByID := make(map[string]*model.Note, len(subIDSet))
	if len(subIDSet) > 0 {
		var subs []*model.Note
		if err := r.db.Where("id IN ?", mapKeys(subIDSet)).Find(&subs).Error; err != nil {
			return nil, err
		}
		for _, s := range subs {
			subByID[s.ID] = s
		}
		for _, n := range notes {
			if n.RenoteID != nil {
				n.Renote = subByID[*n.RenoteID]
			}
			if n.ReplyID != nil {
				n.Reply = subByID[*n.ReplyID]
			}
		}
	}

	// notes + sub-notes をまとめて user / poll の配線対象にする。
	hydrate := make([]*model.Note, 0, len(notes)+len(subByID))
	hydrate = append(hydrate, notes...)
	for _, s := range subByID {
		hydrate = append(hydrate, s)
	}

	// (3) author user を 1 query で取得し配線する。
	userIDSet := make(map[string]struct{}, len(hydrate))
	for _, n := range hydrate {
		if n.UserID != "" {
			userIDSet[n.UserID] = struct{}{}
		}
	}
	if len(userIDSet) > 0 {
		var users []*model.User
		if err := r.db.Where("id IN ?", mapKeys(userIDSet)).Find(&users).Error; err != nil {
			return nil, err
		}
		userByID := make(map[string]*model.User, len(users))
		for _, u := range users {
			userByID[u.ID] = u
		}
		for _, n := range hydrate {
			n.User = userByID[n.UserID]
		}
	}

	// (4) poll を 1 query で取得し配線する。preload 版と同じく全 note を対象にして
	// HasPoll の正確性に依存しない (poll 行が在れば attach、無ければ nil)。
	noteIDs := make([]string, 0, len(hydrate))
	for _, n := range hydrate {
		noteIDs = append(noteIDs, n.ID)
	}
	var polls []*model.Poll
	if err := r.db.Where(`"noteId" IN ?`, noteIDs).Find(&polls).Error; err != nil {
		return nil, err
	}
	if len(polls) > 0 {
		pollByNote := make(map[string]*model.Poll, len(polls))
		for _, p := range polls {
			pollByNote[p.NoteID] = p
		}
		for _, n := range hydrate {
			if p, ok := pollByNote[n.ID]; ok {
				n.Poll = p
			}
		}
	}

	// (5) ids の順序を保って返す。
	byID := make(map[string]*model.Note, len(notes))
	for _, n := range notes {
		byID[n.ID] = n
	}
	ordered := make([]*model.Note, 0, len(ids))
	for _, id := range ids {
		if n, ok := byID[id]; ok {
			ordered = append(ordered, n)
		}
	}
	return ordered, nil
}

// mapKeys returns the keys of a string-set as a slice (順序不定)。
func mapKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func (r *noteRepository) ListFeatured(channelID, untilID string, limit, offset int) ([]*model.Note, error) {
	if limit <= 0 {
		limit = 10
	}
	q := preloadNoteRelations(r.db).Where("visibility = 'public'")
	if channelID != "" {
		// チャンネル指定時は当該チャンネル内のノートだけを返す
		// (upstream FeaturedService.getInChannelNotesRanking と一致)。
		// 過去はこの WHERE 節が無く、グローバル featured が混ざってチャンネル
		// ハイライトに無関係ノートが出ていた (#489)。
		q = q.Where(`"channelId" = ?`, channelID)
	}
	if untilID != "" {
		q = q.Where(`id < ?`, untilID)
	}
	q = q.Order(`("renoteCount" + "repliesCount") DESC, id DESC`).Limit(limit)
	// cursor 指定時は offset を無視 (upstream makePaginationQuery と一致)
	if untilID == "" && offset > 0 {
		q = q.Offset(offset)
	}
	var notes []*model.Note
	if err := q.Find(&notes).Error; err != nil {
		return nil, err
	}
	return notes, nil
}

func (r *noteRepository) FindRenoteByUser(userID, renoteID string) (*model.Note, error) {
	var note model.Note
	if err := r.db.Where("\"userId\" = ? AND \"renoteId\" = ? AND text IS NULL", userID, renoteID).
		Order("id DESC").First(&note).Error; err != nil {
		return nil, err
	}
	return &note, nil
}

func (r *noteRepository) ListMentions(userID string, limit int, sinceID, untilID string) ([]*model.Note, error) {
	if limit <= 0 {
		limit = 10
	}
	q := preloadNoteRelations(r.db).
		Where("mentions @> ARRAY[?]::varchar[]", userID)
	// visibility push-down: mention 対象 (= viewer = userID) が CanSeeNote で
	// 見られる note のみ返す。mentions 配列は本文の @user パース結果で specified
	// の visibleUserIds とは独立なので、これが無いと followers/specified note を
	// mention されただけの非対象 viewer が本文と author を取得できてしまう (#1441)。
	// viewer は notes/mentions の認証ユーザー = userID 固定。
	q = q.Where(
		`("visibility" IN ('public','home') `+
			`OR "userId" = ? `+
			`OR ("visibility" = 'followers' AND "userId" IN (SELECT f."followeeId" FROM "following" f WHERE f."followerId" = ?)) `+
			`OR ("visibility" = 'specified' AND ? = ANY("visibleUserIds")))`,
		userID, userID, userID)
	q = q.Order(paginationOrder(sinceID, untilID, "id")).Limit(limit)
	if sinceID != "" {
		q = q.Where("id > ?", sinceID)
	}
	if untilID != "" {
		q = q.Where("id < ?", untilID)
	}
	var notes []*model.Note
	if err := q.Find(&notes).Error; err != nil {
		return nil, err
	}
	return notes, nil
}

func (r *noteRepository) SearchByTag(tag, viewerID string, limit int, sinceID, untilID string) ([]*model.Note, error) {
	if limit <= 0 {
		limit = 10
	}
	q := preloadNoteRelations(r.db).
		Where("tags @> ARRAY[?]::varchar[]", tag)
	// visibility push-down: core/note.CanSeeNote と同じ条件を LIMIT 前に絞る。
	// ListByUserIDFiltered / clip の ListByClipVisible と同じ可視性定義 (#1439)。
	if viewerID == "" {
		q = q.Where(`"visibility" IN ('public','home')`)
	} else {
		q = q.Where(
			`("visibility" IN ('public','home') `+
				`OR "userId" = ? `+
				`OR ("visibility" = 'followers' AND "userId" IN (SELECT f."followeeId" FROM "following" f WHERE f."followerId" = ?)) `+
				`OR ("visibility" = 'specified' AND ? = ANY("visibleUserIds")))`,
			viewerID, viewerID, viewerID)
	}
	q = q.Order(paginationOrder(sinceID, untilID, "id")).Limit(limit)
	if sinceID != "" {
		q = q.Where("id > ?", sinceID)
	}
	if untilID != "" {
		q = q.Where("id < ?", untilID)
	}
	var notes []*model.Note
	if err := q.Find(&notes).Error; err != nil {
		return nil, err
	}
	return notes, nil
}

func (r *noteRepository) ListByFileID(fileID, sinceID, untilID string, limit int) ([]*model.Note, error) {
	if limit <= 0 {
		limit = 10
	}
	if limit > 100 {
		limit = 100
	}
	q := preloadNoteRelations(r.db).Where(`"fileIds" @> ARRAY[?]::varchar[]`, fileID)
	if sinceID != "" {
		q = q.Where(`"id" > ?`, sinceID)
	}
	if untilID != "" {
		q = q.Where(`"id" < ?`, untilID)
	}
	var notes []*model.Note
	if err := q.Order(paginationOrder(sinceID, untilID, `"id"`)).Limit(limit).Find(&notes).Error; err != nil {
		return nil, err
	}
	return notes, nil
}

func (r *noteRepository) IncrementUserNotesCount(userID string, delta int) error {
	return r.db.Exec(`UPDATE "user" SET "notesCount" = "notesCount" + ? WHERE "id" = ?`, delta, userID).Error
}

// applyTimelineFilter adds common filter conditions to a GORM query builder.
func applyTimelineFilter(q *gorm.DB, f model.TimelineDBFilter) *gorm.DB {
	if f.WithFiles {
		q = q.Where(`"fileIds" != '{}'`)
	}
	if f.WithRenotes != nil && !*f.WithRenotes {
		// pure renote (テキストもファイルもない renote) を除外
		q = q.Where(`NOT ("renoteId" IS NOT NULL AND text IS NULL AND "fileIds" = '{}')`)
	}
	if f.WithReplies != nil && !*f.WithReplies {
		// upstream Misskey TS Home TL と完全一致: `replyId IS NULL` か、
		// reply の場合は self-thread (= replyUserId = note.userId) のみ残す
		// (#1047)。
		//
		// 自分が他人にした reply (= userId=self / replyUserId=other) はこの
		// DB fallback では除外される。ただし production の cache hit 経路では
		// fanout (= OnNoteCreated) が自分の HomeTL stream に push しているので
		// 通常運用は自分の reply も TL に表示される (= Redis 経路は pass-through)。
		// DB fallback は cache miss / 古い note の case のみ走るので user 体験
		// への影響は限定的、その時点で upstream と完全互換に倒す。
		//
		// 「他人 → 他人 reply」を per-followee で表示するかは fanout 層で
		// `following.withReplies` を見て push 制御する (= upstream 互換)。
		q = q.Where(`("replyId" IS NULL OR "replyUserId" = "note"."userId")`)
	}
	if f.IncludeMyRenotes != nil && !*f.IncludeMyRenotes && f.ViewerID != "" {
		q = q.Where(`NOT ("renoteId" IS NOT NULL AND text IS NULL AND "fileIds" = '{}' AND "userId" = ?)`, f.ViewerID)
	}
	if f.IncludeRenotedMyNotes != nil && !*f.IncludeRenotedMyNotes && f.ViewerID != "" {
		q = q.Where(`NOT ("renoteId" IS NOT NULL AND text IS NULL AND "fileIds" = '{}' AND "renoteUserId" = ?)`, f.ViewerID)
	}
	if f.IncludeLocalRenotes != nil && !*f.IncludeLocalRenotes {
		q = q.Where(`NOT ("renoteId" IS NOT NULL AND text IS NULL AND "fileIds" = '{}' AND "renoteUserHost" IS NULL)`)
	}
	if len(f.MutedChannelIDs) > 0 {
		// channel_mutingに登録されたチャンネルのノートはタイムラインから除外する。
		// GORMは連続.WhereをANDで繋ぐがraw SQL側のORはデフォルトでは
		// 括弧で囲まれないので、明示的に囲まないとAND側の他フィルタを
		// バイパスしてしまう (SQL優先順位: AND > OR)。
		q = q.Where(`("channelId" IS NULL OR "channelId" NOT IN ?)`, f.MutedChannelIDs)
	}
	// muted user filter は 2 経路を持つ (#892 / #894):
	//   1. UseMutingSubquery=true: muting テーブルへの NOT EXISTS (production)。
	//      bind parameter が viewer 単位で 2 つ ($viewerID を 2 度) のみで、
	//      mute 件数に比例しないので heavy-mute viewer でも planning コスト
	//      が膨らまない。
	//   2. MutedUserIDs literal: 既存テスト互換のための override path
	//      (test や非 viewer 駆動経路で muteeID 集合を直接指定したいとき)。
	//      Subquery が使えるなら 1. を優先。
	// 共通の WHERE は: userId が active mute されておらず、かつ renoteUserId
	// が NULL でなければ renoteUserId も active mute されていないこと。
	if f.UseMutingSubquery && f.ViewerID != "" {
		// NOT EXISTS による anti-join (Postgres planner が semi-join 相当に
		// 最適化、IDX_muting_muterId_muteeId 複合 index で seek)。NOT IN
		// subquery より NULL 取り扱いがロバストかつ index 使用が明確。
		// active mute = expiresAt nil or future、mutingRepository.ListMuteeIDs
		// と同 semantics を SQL 内で表現。
		//
		// Postgres NOW() は transaction 開始時刻 (= query 内で constant)。
		// in-memory ApplyFilter (Redis cache 経路) は Go time.Now() を使う
		// が、mutingRepo.ListMuteeIDs と timeline 取得は同一 request 内 (=
		// サブ秒) で連続実行されるため、境界 expiresAt の片方に倒れて 2
		// 経路で挙動が分岐する race window は無視できる。
		notExistsForCol := func(col string) string {
			return `NOT EXISTS (SELECT 1 FROM "muting" m WHERE m."muterId" = ? AND m."muteeId" = ` + col + ` AND (m."expiresAt" IS NULL OR m."expiresAt" > NOW()))`
		}
		q = q.Where(
			notExistsForCol(`"note"."userId"`)+` AND ("renoteUserId" IS NULL OR `+notExistsForCol(`"note"."renoteUserId"`)+`)`,
			f.ViewerID, f.ViewerID,
		)
	} else if len(f.MutedUserIDs) > 0 {
		q = q.Where(`"userId" NOT IN ? AND ("renoteUserId" IS NULL OR "renoteUserId" NOT IN ?)`, f.MutedUserIDs, f.MutedUserIDs)
	}
	// renote-mute filter (#903): 投稿者が renote-muted で **かつ** pure
	// renote の note のみ除外する。投稿者の plain note / quote renote は
	// そのまま通す。upstream Misskey TS の
	// generateMutedUserRelatedRenotesQuery と同 semantics。
	//
	// SQL は subquery 経路のみ実装 (UseRenoteMutingSubquery=true 必須):
	// renote_muting への NOT EXISTS で bind parameter を viewer 単位に固定
	// (#894 user-mute subquery と同 pattern)。user-mute のような literal
	// path (RenoteMutedUserIDs を直接 IN) は現状 test/production とも未使用
	// なので未実装。必要になったら同 file の MutedUserIDs literal 分岐と
	// 同 pattern で追加可能。
	// pure renote condition: text IS NULL AND fileIds = '{}' AND renoteId IS NOT NULL
	if f.UseRenoteMutingSubquery && f.ViewerID != "" {
		q = q.Where(
			`NOT (EXISTS (SELECT 1 FROM "renote_muting" rm WHERE rm."muterId" = ? AND rm."muteeId" = "note"."userId") AND text IS NULL AND "fileIds" = '{}' AND "renoteId" IS NOT NULL)`,
			f.ViewerID,
		)
	}
	return q
}

// ListHomeTimeline returns notes by the user and users they follow.
// DBフォールバック用。Redisが空のときに使う。
func (r *noteRepository) ListHomeTimeline(userID string, limit int, sinceID, untilID string, filter model.TimelineDBFilter) ([]*model.Note, error) {
	q := preloadNoteRelations(r.db).
		Where(`("userId" = ? OR "userId" IN (SELECT "followeeId" FROM "following" WHERE "followerId" = ?)) AND "visibility" IN ('public','home','followers')`, userID, userID).
		Order(paginationOrder(sinceID, untilID, `"id"`)).Limit(limit)
	if sinceID != "" {
		q = q.Where(`"id" > ?`, sinceID)
	}
	if untilID != "" {
		q = q.Where(`"id" < ?`, untilID)
	}
	q = applyTimelineFilter(q, filter)
	var notes []*model.Note
	if err := q.Find(&notes).Error; err != nil {
		return nil, err
	}
	return notes, nil
}

// ListLocalTimeline returns public/home notes by local users.
func (r *noteRepository) ListLocalTimeline(limit int, sinceID, untilID string, filter model.TimelineDBFilter) ([]*model.Note, error) {
	q := preloadNoteRelations(r.db).
		Where(`"userHost" IS NULL AND "visibility" = 'public' AND "channelId" IS NULL`).
		Order(paginationOrder(sinceID, untilID, `"id"`)).Limit(limit)
	if sinceID != "" {
		q = q.Where(`"id" > ?`, sinceID)
	}
	if untilID != "" {
		q = q.Where(`"id" < ?`, untilID)
	}
	q = applyTimelineFilter(q, filter)
	var notes []*model.Note
	if err := q.Find(&notes).Error; err != nil {
		return nil, err
	}
	return notes, nil
}

// ListGlobalTimeline returns all public notes.
// channelId IS NULL でチャンネルノートを除外する (TS互換)。
func (r *noteRepository) ListGlobalTimeline(limit int, sinceID, untilID string, filter model.TimelineDBFilter) ([]*model.Note, error) {
	q := preloadNoteRelations(r.db).
		Where(`"visibility" = 'public' AND "channelId" IS NULL`).
		Order(paginationOrder(sinceID, untilID, `"id"`)).Limit(limit)
	if sinceID != "" {
		q = q.Where(`"id" > ?`, sinceID)
	}
	if untilID != "" {
		q = q.Where(`"id" < ?`, untilID)
	}
	q = applyTimelineFilter(q, filter)
	var notes []*model.Note
	if err := q.Find(&notes).Error; err != nil {
		return nil, err
	}
	return notes, nil
}

// DeleteExpiredRemoteNotes はリモートノート (userHost IS NOT NULL) のうち
// expiryDays より前に作成されたものを最大 batchSize 件削除し、実際に消えた
// 行数を返す。ループは呼び出し側 (CleanRemoteNotesProcessor) が sleep / ctx
// cancellation 付きで回す。
// ON DELETE CASCADE が reactions / replies 等に効く前提。
// mk-go の note テーブルには createdAt カラムが無い (aidx ID から時刻を
// 導出する設計) ため、ID 文字列の lexicographic 比較で時刻切り捨てを行う。
func (r *noteRepository) DeleteExpiredRemoteNotes(expiryDays, batchSize int) (int64, error) {
	if batchSize <= 0 {
		batchSize = 100
	}
	cutoffID := aidxCutoffID(time.Now().Add(-time.Duration(expiryDays) * 24 * time.Hour))
	res := r.db.Exec(`
		DELETE FROM "note" WHERE id IN (
			SELECT id FROM "note"
			WHERE "userHost" IS NOT NULL
			  AND id < ?
			LIMIT ?
		)`, cutoffID, batchSize)
	if res.Error != nil {
		return 0, res.Error
	}
	return res.RowsAffected, nil
}

func (r *noteRepository) DeleteByUserBatch(userID string, batchSize int) (int64, error) {
	if userID == "" {
		return 0, nil
	}
	if batchSize <= 0 {
		batchSize = 100
	}
	res := r.db.Exec(`
		DELETE FROM "note" WHERE id IN (
			SELECT id FROM "note"
			WHERE "userId" = ?
			LIMIT ?
		)`, userID, batchSize)
	if res.Error != nil {
		return 0, res.Error
	}
	return res.RowsAffected, nil
}

// ListByUserList returns notes authored by members of the given user list
// with keyset pagination via paginationOrder. user_list_membership テーブルと
// INNER JOIN してメンバーのノートだけを返す。channel ノートは除外する (TS互換)。
func (r *noteRepository) ListByUserList(listID string, limit int, sinceID, untilID string) ([]*model.Note, error) {
	if limit <= 0 {
		limit = 10
	}
	q := preloadNoteRelations(r.db).
		Joins(`INNER JOIN "user_list_membership" m ON m."userId" = "note"."userId" AND m."userListId" = ?`, listID).
		Where(`"note"."channelId" IS NULL`).
		Where(`"note"."visibility" IN ('public','home','followers')`)
	if sinceID != "" {
		q = q.Where(`"note"."id" > ?`, sinceID)
	}
	if untilID != "" {
		q = q.Where(`"note"."id" < ?`, untilID)
	}
	var notes []*model.Note
	if err := q.Order(paginationOrder(sinceID, untilID, `"note"."id"`)).Limit(limit).Find(&notes).Error; err != nil {
		return nil, err
	}
	return notes, nil
}

func (r *noteRepository) CountReplyTargets(userID string, limit int) ([]model.ReplyTargetCount, error) {
	if limit <= 0 {
		limit = 10
	}
	var rows []model.ReplyTargetCount
	// replyUserIdがNULLのもの (通常起こり得ないが防御)と自己返信は集計から除外する。
	err := r.db.Model(&model.Note{}).
		Select(`"replyUserId", COUNT(*) AS count`).
		Where(`"userId" = ? AND "replyId" IS NOT NULL AND "replyUserId" IS NOT NULL AND "replyUserId" <> ?`, userID, userID).
		Group(`"replyUserId"`).
		Order(`count DESC`).
		Limit(limit).
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	return rows, nil
}
