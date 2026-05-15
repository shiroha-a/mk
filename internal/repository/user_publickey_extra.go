package repository

import (
	"github.com/shiroha-a/mk/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// UserPublickeyExtraRepository provides data access for the `user_publickey_extra`
// table. Unlike UserPublickeyRepository (which keyes by userId only), this
// repository keyes by (userId, keyId) composite PK so that a single actor can
// publish multiple keys (e.g. RSA + Ed25519).
type UserPublickeyExtraRepository interface {
	Upsert(pk *model.UserPublickeyExtra) error
	FindByUserAndKeyID(userID, keyID string) (*model.UserPublickeyExtra, error)
	FindByKeyID(keyID string) (*model.UserPublickeyExtra, error)
	ListByUserID(userID string) ([]model.UserPublickeyExtra, error)
	// DeleteByKeyID deletes a single (userID, keyID) row. resolver が actor
	// fetch 時に assertionMethod[] から消えた keyId を purge する用途 (key
	// rotation で古い Ed25519 鍵による rogue verify を防ぐ #1070 follow-up)。
	DeleteByKeyID(userID, keyID string) error
	DeleteByUserID(userID string) error
}

type userPublickeyExtraRepository struct {
	db *gorm.DB
}

// NewUserPublickeyExtraRepository creates a new UserPublickeyExtraRepository.
func NewUserPublickeyExtraRepository(db *gorm.DB) UserPublickeyExtraRepository {
	return &userPublickeyExtraRepository{db: db}
}

func (r *userPublickeyExtraRepository) Upsert(pk *model.UserPublickeyExtra) error {
	return r.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "userId"}, {Name: "keyId"}},
		DoUpdates: clause.AssignmentColumns([]string{"keyPem", "alg"}),
	}).Create(pk).Error
}

func (r *userPublickeyExtraRepository) FindByUserAndKeyID(userID, keyID string) (*model.UserPublickeyExtra, error) {
	var pk model.UserPublickeyExtra
	if err := r.db.Where(`"userId" = ? AND "keyId" = ?`, userID, keyID).First(&pk).Error; err != nil {
		return nil, err
	}
	return &pk, nil
}

func (r *userPublickeyExtraRepository) FindByKeyID(keyID string) (*model.UserPublickeyExtra, error) {
	var pk model.UserPublickeyExtra
	if err := r.db.Where(`"keyId" = ?`, keyID).First(&pk).Error; err != nil {
		return nil, err
	}
	return &pk, nil
}

func (r *userPublickeyExtraRepository) ListByUserID(userID string) ([]model.UserPublickeyExtra, error) {
	var rows []model.UserPublickeyExtra
	if err := r.db.Where(`"userId" = ?`, userID).Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *userPublickeyExtraRepository) DeleteByKeyID(userID, keyID string) error {
	return r.db.Where(`"userId" = ? AND "keyId" = ?`, userID, keyID).Delete(&model.UserPublickeyExtra{}).Error
}

func (r *userPublickeyExtraRepository) DeleteByUserID(userID string) error {
	return r.db.Where(`"userId" = ?`, userID).Delete(&model.UserPublickeyExtra{}).Error
}
