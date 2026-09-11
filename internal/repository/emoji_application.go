package repository

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"

	"github.com/shiroha-a/mk/internal/model"
)

// Emoji application filter values accepted by EmojiApplicationRepository.List.
//
// Declared as plain string constants so callers (admin handlers, testutil
// mocks) can pass literals without depending on this package's type system.
const (
	// EmojiApplicationFilterAll returns every application.
	EmojiApplicationFilterAll = "all"
	// EmojiApplicationFilterPending returns applications awaiting review.
	EmojiApplicationFilterPending = "pending"
	// EmojiApplicationFilterProcessed returns applications that no longer need
	// action (approved / rejected / canceled).
	EmojiApplicationFilterProcessed = "processed"
)

// ErrEmojiApplicationDuplicatePending is returned when the applicant already
// has a pending application under the same name.
//
// **番兵で区別する。** 一意制約違反をそのまま 500 にすると、利用者には
// 「サーバーエラー」としか見えず、二重送信なのか障害なのか分からない。
var ErrEmojiApplicationDuplicatePending = errors.New("emoji application already pending for this name")

// EmojiApplicationRepository reads and writes `emoji_application` rows (#2934).
type EmojiApplicationRepository interface {
	Create(app *model.EmojiApplication) error
	FindByID(id string) (*model.EmojiApplication, error)
	// List returns applications newest first. filter is one of the
	// EmojiApplicationFilter* constants; anything else is treated as "all".
	List(filter string, limit int, untilID string) ([]model.EmojiApplication, error)
	ListByUser(userID string, limit int, untilID string) ([]model.EmojiApplication, error)
	CountPending() (int64, error)
	// UpdateIfPending writes the row only while it is still pending, and
	// reports whether it did.
	//
	// **条件付き UPDATE でないと last-write-wins になる (#2934 レビュー M1)。**
	// 読んでから書くだけだと、2 人のモデレーターが同じ申請を開いていた場合に
	// 後の書き込みが前を上書きする — 承認で emoji を作った直後に却下が被さり、
	// **絵文字は存在するのに申請は「却下」で emojiId も消える**。窓は「一覧を
	// 開いてから押すまで」の全期間なので、実際に起きる。
	UpdateIfPending(app *model.EmojiApplication) (bool, error)
}

type emojiApplicationRepository struct {
	db *gorm.DB
}

// NewEmojiApplicationRepository constructs the GORM-backed repository.
func NewEmojiApplicationRepository(db *gorm.DB) EmojiApplicationRepository {
	return &emojiApplicationRepository{db: db}
}

func (r *emojiApplicationRepository) Create(app *model.EmojiApplication) error {
	err := r.db.Create(app).Error
	if err == nil {
		return nil
	}
	// 部分一意索引 (userId, name) WHERE status = 'pending' の違反だけを
	// 区別する。ほかの制約違反は素通しして呼び出し側で 500 にする。
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" &&
		pgErr.ConstraintName == "IDX_emoji_application_pending_name" {
		return ErrEmojiApplicationDuplicatePending
	}
	return err
}

func (r *emojiApplicationRepository) FindByID(id string) (*model.EmojiApplication, error) {
	var app model.EmojiApplication
	if err := r.db.Where(`"id" = ?`, id).First(&app).Error; err != nil {
		return nil, err
	}
	return &app, nil
}

func (r *emojiApplicationRepository) List(filter string, limit int, untilID string) ([]model.EmojiApplication, error) {
	q := r.db.Model(&model.EmojiApplication{})
	switch filter {
	case EmojiApplicationFilterPending:
		q = q.Where(`"status" = ?`, model.EmojiApplicationPending)
	case EmojiApplicationFilterProcessed:
		q = q.Where(`"status" <> ?`, model.EmojiApplicationPending)
	}
	return r.scanPage(q, limit, untilID)
}

func (r *emojiApplicationRepository) ListByUser(userID string, limit int, untilID string) ([]model.EmojiApplication, error) {
	q := r.db.Model(&model.EmojiApplication{}).Where(`"userId" = ?`, userID)
	return r.scanPage(q, limit, untilID)
}

// scanPage applies the shared ordering and keyset pagination.
//
// **id で切るのは createdAt で切らないため。** aidx は時刻順に単調なので同じ
// 並びになり、同一時刻の行を取りこぼさない。
func (r *emojiApplicationRepository) scanPage(q *gorm.DB, limit int, untilID string) ([]model.EmojiApplication, error) {
	if untilID != "" {
		q = q.Where(`"id" < ?`, untilID)
	}
	if limit <= 0 {
		limit = 30
	}
	var out []model.EmojiApplication
	if err := q.Order(`"id" DESC`).Limit(limit).Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

func (r *emojiApplicationRepository) CountPending() (int64, error) {
	var n int64
	err := r.db.Model(&model.EmojiApplication{}).
		Where(`"status" = ?`, model.EmojiApplicationPending).
		Count(&n).Error
	return n, err
}

func (r *emojiApplicationRepository) UpdateIfPending(app *model.EmojiApplication) (bool, error) {
	// Save ではなく Updates + Where。Save は主キーだけで WHERE を組むので
	// 条件を足せない。
	res := r.db.Model(&model.EmojiApplication{}).
		Where(`"id" = ? AND "status" = ?`, app.ID, model.EmojiApplicationPending).
		Updates(map[string]any{
			"status":        app.Status,
			"emojiId":       app.EmojiID,
			"processedById": app.ProcessedByID,
			"processedAt":   app.ProcessedAt,
			"rejectReason":  app.RejectReason,
			"updatedAt":     app.UpdatedAt,
		})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}
