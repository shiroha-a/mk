package repository

import (
	"github.com/shiroha-a/mk/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// UserKeypairExtraRepository provides data access for the `user_keypair_extra` table.
type UserKeypairExtraRepository interface {
	Upsert(k *model.UserKeypairExtra) error
	// InsertIfAbsent inserts the row only when no entry exists for the userId.
	// Race-safe primitive for P5 lazy backfill: 並列 actor JSON 生成で複数
	// goroutine が同 user の鍵を同時に生成しても DB 上は最初に書かれた行が
	// 残り、Upsert (UPDATE) のように後勝ちで鍵が置換されない (#1072)。
	InsertIfAbsent(k *model.UserKeypairExtra) error
	FindByUserID(userID string) (*model.UserKeypairExtra, error)
	Delete(userID string) error
}

type userKeypairExtraRepository struct {
	db *gorm.DB
}

// NewUserKeypairExtraRepository creates a new UserKeypairExtraRepository.
func NewUserKeypairExtraRepository(db *gorm.DB) UserKeypairExtraRepository {
	return &userKeypairExtraRepository{db: db}
}

func (r *userKeypairExtraRepository) Upsert(k *model.UserKeypairExtra) error {
	return r.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "userId"}},
		DoUpdates: clause.AssignmentColumns([]string{"ed25519PublicKey", "ed25519PrivateKey"}),
	}).Create(k).Error
}

func (r *userKeypairExtraRepository) InsertIfAbsent(k *model.UserKeypairExtra) error {
	return r.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "userId"}},
		DoNothing: true,
	}).Create(k).Error
}

func (r *userKeypairExtraRepository) FindByUserID(userID string) (*model.UserKeypairExtra, error) {
	var k model.UserKeypairExtra
	if err := r.db.Where(`"userId" = ?`, userID).First(&k).Error; err != nil {
		return nil, err
	}
	return &k, nil
}

func (r *userKeypairExtraRepository) Delete(userID string) error {
	return r.db.Where(`"userId" = ?`, userID).Delete(&model.UserKeypairExtra{}).Error
}
