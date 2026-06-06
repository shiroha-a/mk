package repository

import (
	"time"

	"github.com/shiroha-a/mk/internal/model"
	"gorm.io/gorm"
)

// RoleRepository provides data access for the `role` table.
type RoleRepository interface {
	Create(role *model.Role) error
	FindByID(id string) (*model.Role, error)
	List() ([]*model.Role, error)
	UpdateFields(id string, fields map[string]any) error
	Delete(id string) error
}

type roleRepository struct {
	db *gorm.DB
}

// NewRoleRepository creates a new RoleRepository.
func NewRoleRepository(db *gorm.DB) RoleRepository {
	return &roleRepository{db: db}
}

func (r *roleRepository) Create(role *model.Role) error {
	return r.db.Create(role).Error
}

func (r *roleRepository) FindByID(id string) (*model.Role, error) {
	var role model.Role
	if err := r.db.Where("id = ?", id).First(&role).Error; err != nil {
		return nil, err
	}
	return &role, nil
}

func (r *roleRepository) List() ([]*model.Role, error) {
	var roles []*model.Role
	if err := r.db.Order("\"displayOrder\" DESC, id ASC").Find(&roles).Error; err != nil {
		return nil, err
	}
	return roles, nil
}

func (r *roleRepository) UpdateFields(id string, fields map[string]any) error {
	return r.db.Model(&model.Role{}).Where("id = ?", id).Updates(fields).Error
}

func (r *roleRepository) Delete(id string) error {
	return r.db.Where("id = ?", id).Delete(&model.Role{}).Error
}

// RoleAssignmentRepository provides data access for the `role_assignment` table.
type RoleAssignmentRepository interface {
	Create(a *model.RoleAssignment) error
	Delete(userID, roleID string) error
	ListByUser(userID string) ([]*model.RoleAssignment, error)
	ListByRole(roleID string, untilID, sinceID string, limit int) ([]*model.RoleAssignment, error)
	Exists(userID, roleID string) (bool, error)
	// CountActiveByRole returns the number of non-expired assignments for a
	// role (expiresAt IS NULL OR > now). Used for the Role packer's usersCount.
	CountActiveByRole(roleID string) (int, error)
}

type roleAssignmentRepository struct {
	db *gorm.DB
}

// NewRoleAssignmentRepository creates a new RoleAssignmentRepository.
func NewRoleAssignmentRepository(db *gorm.DB) RoleAssignmentRepository {
	return &roleAssignmentRepository{db: db}
}

func (r *roleAssignmentRepository) Create(a *model.RoleAssignment) error {
	return r.db.Create(a).Error
}

func (r *roleAssignmentRepository) Delete(userID, roleID string) error {
	return r.db.Where("\"userId\" = ? AND \"roleId\" = ?", userID, roleID).
		Delete(&model.RoleAssignment{}).Error
}

func (r *roleAssignmentRepository) ListByUser(userID string) ([]*model.RoleAssignment, error) {
	var assignments []*model.RoleAssignment
	now := time.Now()
	if err := r.db.Preload("Role").
		Where("\"userId\" = ? AND (\"expiresAt\" IS NULL OR \"expiresAt\" > ?)", userID, now).
		Find(&assignments).Error; err != nil {
		return nil, err
	}
	return assignments, nil
}

func (r *roleAssignmentRepository) ListByRole(roleID string, untilID, sinceID string, limit int) ([]*model.RoleAssignment, error) {
	var assignments []*model.RoleAssignment
	now := time.Now()
	q := r.db.Preload("User").
		Where("\"roleId\" = ? AND (\"expiresAt\" IS NULL OR \"expiresAt\" > ?)", roleID, now)
	if untilID != "" {
		q = q.Where("id < ?", untilID)
	}
	if sinceID != "" {
		q = q.Where("id > ?", sinceID)
	}
	// sinceID 指定時は ASC で keyset cursor を進ませる Misskey TS 互換
	if sinceID != "" && untilID == "" {
		q = q.Order("id ASC")
	} else {
		q = q.Order("id DESC")
	}
	if limit > 0 {
		q = q.Limit(limit)
	}
	if err := q.Find(&assignments).Error; err != nil {
		return nil, err
	}
	return assignments, nil
}

func (r *roleAssignmentRepository) CountActiveByRole(roleID string) (int, error) {
	var count int64
	now := time.Now()
	err := r.db.Model(&model.RoleAssignment{}).
		Where("\"roleId\" = ? AND (\"expiresAt\" IS NULL OR \"expiresAt\" > ?)", roleID, now).
		Count(&count).Error
	return int(count), err
}

func (r *roleAssignmentRepository) Exists(userID, roleID string) (bool, error) {
	var count int64
	now := time.Now()
	err := r.db.Model(&model.RoleAssignment{}).
		Where("\"userId\" = ? AND \"roleId\" = ? AND (\"expiresAt\" IS NULL OR \"expiresAt\" > ?)", userID, roleID, now).
		Count(&count).Error
	return count > 0, err
}
