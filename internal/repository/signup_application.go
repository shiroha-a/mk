package repository

import (
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/shiroha-a/mk/internal/model"
	"gorm.io/gorm"
)

// Signup application filter values accepted by
// SignupApplicationRepository.List. Declared as plain string constants so
// callers (admin handlers, testutil mocks) can pass literals without taking a
// dependency on this package's type system.
const (
	// SignupApplicationFilterAll returns every application.
	SignupApplicationFilterAll = "all"
	// SignupApplicationFilterPending returns applications awaiting review.
	SignupApplicationFilterPending = "pending"
	// SignupApplicationFilterApproved returns approved applications that have
	// not been used yet.
	SignupApplicationFilterApproved = "approved"
	// SignupApplicationFilterProcessed returns applications that no longer
	// need action (rejected / expired / completed).
	SignupApplicationFilterProcessed = "processed"
)

// ErrSignupApplicationLiveExists is returned when a second application would
// collide with one that is still live (pending / approved) for the same
// contact.
//
// 部分一意インデックスの違反を呼び出し側が判別できるようにするための番兵。
// 生の gorm エラーを上に流すと、handler が「重複」と「DB 障害」を区別できない。
var ErrSignupApplicationLiveExists = errors.New("a live signup application already exists for this contact")

// SignupApplicationRepository handles persistence for the
// `signup_application` table (#2555).
type SignupApplicationRepository interface {
	// Create inserts a new application. Returns ErrSignupApplicationLiveExists
	// when the contact already has a pending / approved application.
	Create(a *model.SignupApplication) error
	// FindByID returns the application by primary key.
	FindByID(id string) (*model.SignupApplication, error)
	// FindLiveByContact returns the pending / approved application for the
	// contact, if any.
	//
	// **一致判定は (host, remoteID)。** username は相手サーバーでの改名で
	// 変わるので鍵にしない。
	FindLiveByContact(host, remoteID string) (*model.SignupApplication, error)
	// FindLatestByContact returns the most recent application for the contact
	// regardless of status. 却下・期限切れの結果を申請者に見せるために使う。
	FindLatestByContact(host, remoteID string) (*model.SignupApplication, error)
	// FindByIDForUpdateTx returns the application by ID with a row-level write
	// lock (`SELECT ... FOR UPDATE`), inside the caller's transaction.
	// 審査と登録消費を直列化するために使う。
	FindByIDForUpdateTx(tx *gorm.DB, id string) (*model.SignupApplication, error)
	// UpdateFieldsTx applies a partial update inside the caller's transaction.
	UpdateFieldsTx(tx *gorm.DB, id string, fields map[string]any) error
	// List returns applications matching the filter, newest first.
	// Unknown filter values are treated as "all".
	List(filter string, limit, offset int) ([]*model.SignupApplication, error)
	// Count returns the number of applications matching the filter.
	Count(filter string) (int, error)
	// ExpireStale flips live applications past their expiresAt to `expired`
	// and returns how many rows changed.
	ExpireStale(now time.Time) (int, error)
	// DB exposes the underlying handle so services can open transactions that
	// span this repository and the registration ticket store.
	DB() *gorm.DB
}

type signupApplicationRepository struct {
	db *gorm.DB
}

// NewSignupApplicationRepository creates a new SignupApplicationRepository.
func NewSignupApplicationRepository(db *gorm.DB) SignupApplicationRepository {
	return &signupApplicationRepository{db: db}
}

func (r *signupApplicationRepository) DB() *gorm.DB { return r.db }

func (r *signupApplicationRepository) Create(a *model.SignupApplication) error {
	if err := r.db.Create(a).Error; err != nil {
		// PostgreSQL の一意制約違反 (23505) を専用エラーに変換する。生の gorm
		// エラーを上に流すと、handler が「重複」と「DB 障害」を区別できない。
		//
		// このテーブルに一意制約は部分インデックス 1 本しか無いので、制約名まで
		// 見なくても取り違えない。**制約を足すときはここも見直すこと。**
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return ErrSignupApplicationLiveExists
		}
		return err
	}
	return nil
}

func (r *signupApplicationRepository) FindByID(id string) (*model.SignupApplication, error) {
	var a model.SignupApplication
	if err := r.db.Where(`"id" = ?`, id).First(&a).Error; err != nil {
		return nil, err
	}
	return &a, nil
}

func (r *signupApplicationRepository) FindLiveByContact(host, remoteID string) (*model.SignupApplication, error) {
	var a model.SignupApplication
	if err := r.db.
		Where(`"contactHost" = ? AND "contactRemoteId" = ? AND "status" IN ?`,
			host, remoteID, []string{model.SignupApplicationPending, model.SignupApplicationApproved}).
		First(&a).Error; err != nil {
		return nil, err
	}
	return &a, nil
}

func (r *signupApplicationRepository) FindLatestByContact(host, remoteID string) (*model.SignupApplication, error) {
	var a model.SignupApplication
	if err := r.db.
		Where(`"contactHost" = ? AND "contactRemoteId" = ?`, host, remoteID).
		Order(`"createdAt" DESC`).
		First(&a).Error; err != nil {
		return nil, err
	}
	return &a, nil
}

func (r *signupApplicationRepository) FindByIDForUpdateTx(tx *gorm.DB, id string) (*model.SignupApplication, error) {
	var a model.SignupApplication
	if err := tx.Raw(`SELECT * FROM "signup_application" WHERE "id" = ? FOR UPDATE`, id).
		Scan(&a).Error; err != nil {
		return nil, err
	}
	if a.ID == "" {
		return nil, gorm.ErrRecordNotFound
	}
	return &a, nil
}

func (r *signupApplicationRepository) UpdateFieldsTx(tx *gorm.DB, id string, fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}
	return tx.Model(&model.SignupApplication{}).Where(`"id" = ?`, id).Updates(fields).Error
}

// applySignupApplicationFilter narrows a query to the requested status set. Unknown values fall
// through to "all" so a stale client cannot make the admin list fail.
func applySignupApplicationFilter(q *gorm.DB, filter string) *gorm.DB {
	switch filter {
	case SignupApplicationFilterPending:
		return q.Where(`"status" = ?`, model.SignupApplicationPending)
	case SignupApplicationFilterApproved:
		return q.Where(`"status" = ?`, model.SignupApplicationApproved)
	case SignupApplicationFilterProcessed:
		return q.Where(`"status" IN ?`, []string{
			model.SignupApplicationRejected,
			model.SignupApplicationExpired,
			model.SignupApplicationCompleted,
		})
	default:
		return q
	}
}

func (r *signupApplicationRepository) List(filter string, limit, offset int) ([]*model.SignupApplication, error) {
	var rows []*model.SignupApplication
	q := applySignupApplicationFilter(r.db.Model(&model.SignupApplication{}), filter)
	if err := q.Order(`"createdAt" DESC`).Limit(limit).Offset(offset).Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *signupApplicationRepository) Count(filter string) (int, error) {
	var count int64
	q := applySignupApplicationFilter(r.db.Model(&model.SignupApplication{}), filter)
	if err := q.Count(&count).Error; err != nil {
		return 0, err
	}
	return int(count), nil
}

func (r *signupApplicationRepository) ExpireStale(now time.Time) (int, error) {
	res := r.db.Model(&model.SignupApplication{}).
		Where(`"status" IN ? AND "expiresAt" <= ?`,
			[]string{model.SignupApplicationPending, model.SignupApplicationApproved}, now).
		Updates(map[string]any{"status": model.SignupApplicationExpired, "updatedAt": now})
	if res.Error != nil {
		return 0, res.Error
	}
	return int(res.RowsAffected), nil
}
