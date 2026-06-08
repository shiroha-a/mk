package repository

import (
	"github.com/shiroha-a/mk/internal/model"
	"gorm.io/gorm"
)

// ReversiRepository handles reversi_game persistence.
type ReversiRepository interface {
	Create(game *model.ReversiGame) error
	FindByID(id string) (*model.ReversiGame, error)
	Update(game *model.ReversiGame) error
	ListByUser(userID string, limit int) ([]*model.ReversiGame, error)
	// ListByUserCursor returns user's games (User1 or User2) with keyset
	// pagination via sinceID / untilID。limit は上限 (0 → 10)。id DESC 順。
	ListByUserCursor(userID, sinceID, untilID string, limit int) ([]*model.ReversiGame, error)
	ListActive() ([]*model.ReversiGame, error)
	// ListStartedCursor returns only started games with keyset pagination。
	// "my=false" な /reversi/games 用。
	ListStartedCursor(sinceID, untilID string, limit int) ([]*model.ReversiGame, error)
	Delete(id string) error
	// DeleteOutdatedGames removes not-yet-started games whose id is older than
	// thresholdID. Mirrors upstream reversiService.cleanOutdatedGames (id <
	// idService.gen(now-10min) AND isStarted=false): abandoned matchmaking
	// games never started within ~10 minutes are pruned by the clean cron
	// (#1563). Returns rows deleted.
	DeleteOutdatedGames(thresholdID string) (int64, error)
}

type reversiRepository struct {
	db *gorm.DB
}

// NewReversiRepository creates a new ReversiRepository.
func NewReversiRepository(db *gorm.DB) ReversiRepository {
	return &reversiRepository{db: db}
}

func (r *reversiRepository) Create(game *model.ReversiGame) error {
	return r.db.Create(game).Error
}

func (r *reversiRepository) DeleteOutdatedGames(thresholdID string) (int64, error) {
	res := r.db.Where(`"id" < ? AND "isStarted" = ?`, thresholdID, false).Delete(&model.ReversiGame{})
	return res.RowsAffected, res.Error
}

func (r *reversiRepository) FindByID(id string) (*model.ReversiGame, error) {
	var game model.ReversiGame
	if err := r.db.Preload("User1").Preload("User2").Where(`"id" = ?`, id).First(&game).Error; err != nil {
		return nil, err
	}
	return &game, nil
}

// FindByFederationID resolves the reversi game row identified by a federation
// session ID (populated when a match crosses an AP boundary). Used by the
// inbox processor to route incoming Invite/Join/Update/Leave activities.
func (r *reversiRepository) FindByFederationID(federationID string) (*model.ReversiGame, error) {
	var game model.ReversiGame
	if err := r.db.Preload("User1").Preload("User2").
		Where(`"federationId" = ?`, federationID).
		First(&game).Error; err != nil {
		return nil, err
	}
	return &game, nil
}

func (r *reversiRepository) Update(game *model.ReversiGame) error {
	return r.db.Save(game).Error
}

func (r *reversiRepository) ListByUser(userID string, limit int) ([]*model.ReversiGame, error) {
	if limit <= 0 {
		limit = 10
	}
	var games []*model.ReversiGame
	if err := r.db.Preload("User1").Preload("User2").
		Where(`"user1Id" = ? OR "user2Id" = ?`, userID, userID).
		Order(`"id" DESC`).Limit(limit).Find(&games).Error; err != nil {
		return nil, err
	}
	return games, nil
}

func (r *reversiRepository) ListActive() ([]*model.ReversiGame, error) {
	var games []*model.ReversiGame
	if err := r.db.Preload("User1").Preload("User2").
		Where(`"isEnded" = false`).
		Order(`"id" DESC`).Limit(50).Find(&games).Error; err != nil {
		return nil, err
	}
	return games, nil
}

func (r *reversiRepository) ListByUserCursor(userID, sinceID, untilID string, limit int) ([]*model.ReversiGame, error) {
	if limit <= 0 {
		limit = 10
	}
	q := r.db.Preload("User1").Preload("User2").
		Where(`"user1Id" = ? OR "user2Id" = ?`, userID, userID)
	if sinceID != "" {
		q = q.Where(`"id" > ?`, sinceID)
	}
	if untilID != "" {
		q = q.Where(`"id" < ?`, untilID)
	}
	var games []*model.ReversiGame
	if err := q.Order(`"id" DESC`).Limit(limit).Find(&games).Error; err != nil {
		return nil, err
	}
	return games, nil
}

func (r *reversiRepository) ListStartedCursor(sinceID, untilID string, limit int) ([]*model.ReversiGame, error) {
	if limit <= 0 {
		limit = 10
	}
	q := r.db.Preload("User1").Preload("User2").Where(`"isStarted" = true`)
	if sinceID != "" {
		q = q.Where(`"id" > ?`, sinceID)
	}
	if untilID != "" {
		q = q.Where(`"id" < ?`, untilID)
	}
	var games []*model.ReversiGame
	if err := q.Order(`"id" DESC`).Limit(limit).Find(&games).Error; err != nil {
		return nil, err
	}
	return games, nil
}

func (r *reversiRepository) Delete(id string) error {
	return r.db.Where(`"id" = ?`, id).Delete(&model.ReversiGame{}).Error
}
