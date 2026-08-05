package repository

import (
	"errors"

	"github.com/shiroha-a/mk/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// defaultMetaID is the row id used when the singleton meta row has to be
// recreated on the fly. 本家 MetaService.fetch() は 'x' 固定で upsert する。
const defaultMetaID = "x"

// MetaRepository provides data access for server metadata.
type MetaRepository interface {
	Fetch() (*model.Meta, error)
	Update(fields map[string]any) error
	EnsureInitial(id string) error
}

type metaRepository struct {
	db *gorm.DB
}

// NewMetaRepository creates a new MetaRepository.
func NewMetaRepository(db *gorm.DB) MetaRepository {
	return &metaRepository{db: db}
}

func (r *metaRepository) Fetch() (*model.Meta, error) {
	var meta model.Meta
	err := r.db.First(&meta).Error
	if err == nil {
		return &meta, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	// 本家 MetaService.fetch() は行が無ければその場で upsert して作る。mk-go は
	// 起動時の EnsureInitial だけだったため、起動後に行が消えると (DB リセットや
	// 外部ツールによる truncate) 全 API が 500 のまま復旧しなかった。
	if err := r.EnsureInitial(defaultMetaID); err != nil {
		return nil, err
	}
	if err := r.db.First(&meta).Error; err != nil {
		return nil, err
	}
	return &meta, nil
}

func (r *metaRepository) Update(fields map[string]any) error {
	return r.db.Model(&model.Meta{}).Where("TRUE").Updates(fields).Error
}

// EnsureInitial creates the singleton meta row if it does not exist yet.
// Called at server startup so that fresh installs can proceed with
// /api/admin/accounts/create (initial setup) without hitting a 500, and from
// Fetch as a fail-safe when the row disappears afterwards.
func (r *metaRepository) EnsureInitial(id string) error {
	var meta model.Meta
	err := r.db.First(&meta).Error
	if err == nil {
		return nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	// Fetch からも呼ばれるので、同時に複数のリクエストがここへ入りうる。
	// 本家 MetaService.fetch() が upsert を使っているのと同じ理由で、
	// 主キー衝突を無視して冪等にする。
	return r.db.Clauses(clause.OnConflict{DoNothing: true}).Create(&model.Meta{ID: id}).Error
}
