// Package muting provides UserMutingService and RenoteMutingService.
package muting

import (
	"errors"
	"time"

	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
)

// Errors returned by Service / RenoteService.
var (
	// ErrSelfMute is returned when a user attempts to mute themselves.
	ErrSelfMute = errors.New("cannot mute yourself")
	// ErrMuteeNotFound is returned when the target user does not exist.
	ErrMuteeNotFound = errors.New("mutee not found")
	// ErrAlreadyMuting is returned when the muter already mutes the mutee.
	ErrAlreadyMuting = errors.New("already muting")
	// ErrNotMuting is returned when there is no muting relationship to delete.
	ErrNotMuting = errors.New("not muting")
)

// Service manages user-level mutes (full mute, not renote-only).
type Service struct {
	userRepo   repository.UserRepository
	mutingRepo repository.MutingRepository
	idGen      id.Generator
}

// NewService constructs a UserMutingService.
func NewService(
	userRepo repository.UserRepository,
	mutingRepo repository.MutingRepository,
	idGen id.Generator,
) *Service {
	return &Service{userRepo: userRepo, mutingRepo: mutingRepo, idGen: idGen}
}

// Mute creates a muting relationship. expiresAt may be nil for indefinite mutes.
func (s *Service) Mute(muterID, muteeID string, expiresAt *time.Time) (*model.Muting, error) {
	if muterID == muteeID {
		return nil, ErrSelfMute
	}
	if _, err := s.userRepo.FindByID(muteeID); err != nil {
		return nil, ErrMuteeNotFound
	}

	if exists, err := s.mutingRepo.Exists(muterID, muteeID); err != nil {
		return nil, err
	} else if exists {
		return nil, ErrAlreadyMuting
	}

	// 既に失効している expiresAt (<= now) の mute は no-op で成功扱いにする
	// (upstream mute/create.ts の `if (ps.expiresAt && ps.expiresAt <= Date.now())
	// return;`、#1557)。Muting row を作らないことで対象を re-muteable のまま残す。
	// self / not-found / already-muting の検証より後 (= upstream の順序) に置く。
	if expiresAt != nil && !expiresAt.After(time.Now()) {
		return nil, nil
	}

	rec := &model.Muting{
		ID:        s.idGen.Generate(time.Now()),
		MuterID:   muterID,
		MuteeID:   muteeID,
		ExpiresAt: expiresAt,
	}
	if err := s.mutingRepo.Create(rec); err != nil {
		return nil, err
	}
	return rec, nil
}

// Unmute removes the muting relationship.
func (s *Service) Unmute(muterID, muteeID string) error {
	if muterID == muteeID {
		return ErrSelfMute
	}
	rec, err := s.mutingRepo.FindByPair(muterID, muteeID)
	if err != nil {
		return ErrNotMuting
	}
	return s.mutingRepo.Delete(rec)
}

// IsMuted reports whether muter currently mutes mutee.
func (s *Service) IsMuted(muterID, muteeID string) (bool, error) {
	return s.mutingRepo.Exists(muterID, muteeID)
}

// List returns the user's mutings with cursor (sinceID/untilID) or offset
// pagination. Cursor 指定時は offset 無視 (upstream makePaginationQuery と一致)。
func (s *Service) List(muterID, sinceID, untilID string, limit, offset int) ([]*model.Muting, error) {
	if limit <= 0 {
		limit = 10
	}
	return s.mutingRepo.ListByMuter(muterID, sinceID, untilID, limit, offset)
}

// RenoteService manages renote-only mutes (the mutee can still post original
// notes, but their renotes are hidden from the muter's timelines).
type RenoteService struct {
	userRepo         repository.UserRepository
	renoteMutingRepo repository.RenoteMutingRepository
	idGen            id.Generator
}

// NewRenoteService constructs a RenoteMutingService.
func NewRenoteService(
	userRepo repository.UserRepository,
	renoteMutingRepo repository.RenoteMutingRepository,
	idGen id.Generator,
) *RenoteService {
	return &RenoteService{userRepo: userRepo, renoteMutingRepo: renoteMutingRepo, idGen: idGen}
}

// Mute creates a renote-mute relationship.
func (s *RenoteService) Mute(muterID, muteeID string) (*model.RenoteMuting, error) {
	if muterID == muteeID {
		return nil, ErrSelfMute
	}
	if _, err := s.userRepo.FindByID(muteeID); err != nil {
		return nil, ErrMuteeNotFound
	}
	if exists, err := s.renoteMutingRepo.Exists(muterID, muteeID); err != nil {
		return nil, err
	} else if exists {
		return nil, ErrAlreadyMuting
	}

	rec := &model.RenoteMuting{
		ID:      s.idGen.Generate(time.Now()),
		MuterID: muterID,
		MuteeID: muteeID,
	}
	if err := s.renoteMutingRepo.Create(rec); err != nil {
		return nil, err
	}
	return rec, nil
}

// Unmute removes the renote mute.
func (s *RenoteService) Unmute(muterID, muteeID string) error {
	if muterID == muteeID {
		return ErrSelfMute
	}
	rec, err := s.renoteMutingRepo.FindByPair(muterID, muteeID)
	if err != nil {
		return ErrNotMuting
	}
	return s.renoteMutingRepo.Delete(rec)
}

// IsRenoteMuted reports whether muter has renote-muted mutee.
func (s *RenoteService) IsRenoteMuted(muterID, muteeID string) (bool, error) {
	return s.renoteMutingRepo.Exists(muterID, muteeID)
}

// List returns the user's renote mutings with cursor (sinceID/untilID) or
// offset pagination.
func (s *RenoteService) List(muterID, sinceID, untilID string, limit, offset int) ([]*model.RenoteMuting, error) {
	if limit <= 0 {
		limit = 10
	}
	return s.renoteMutingRepo.ListByMuter(muterID, sinceID, untilID, limit, offset)
}
