// Package clip provides the user-facing clip CRUD service plus add/remove
// note operations. Misskey 互換のローカル限定機能で、ActivityPub 連携は
// 持たない。
package clip

import (
	"errors"
	"time"

	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
)

// Errors returned by Service.
var (
	// ErrClipNotFound is returned when the requested clip does not exist.
	ErrClipNotFound = errors.New("clip not found")
	// ErrClipNameRequired is returned when name is empty on Create / Update.
	ErrClipNameRequired = errors.New("clip name is required")
	// ErrAccessDenied is returned when a user attempts to mutate a clip they
	// do not own. Public clips are still readable by anyone.
	ErrAccessDenied = errors.New("not the owner of this clip")
	// ErrNoteNotFound is returned when the target note for AddNote does not
	// exist.
	ErrNoteNotFound = errors.New("note not found")
	// ErrAlreadyClipped is returned when a note is already attached to the
	// clip.
	ErrAlreadyClipped = errors.New("note is already clipped")
	// ErrNotClipped is returned when there is no clip_note row to remove.
	ErrNotClipped = errors.New("note is not clipped")
	// ErrTooManyClips は Create で clipLimit 超過 (#1029、upstream
	// tooManyClips)。
	ErrTooManyClips = errors.New("clip limit exceeded")
	// ErrTooManyClipNotes は AddNote で noteEachClipsLimit 超過 (#1029、
	// upstream tooManyClipNotes)。
	ErrTooManyClipNotes = errors.New("clip note limit exceeded")
)

// Service provides clip CRUD plus AddNote / RemoveNote / Notes operations.
type Service struct {
	repo     repository.ClipRepository
	noteRepo repository.ClipNoteRepository
	notes    repository.NoteRepository
	idGen    id.Generator
	clock    func() time.Time
	// rolePolicyProvider は clipLimit / noteEachClipsLimit の gate に使う
	// (#1029)。nil 時は gate skip。
	rolePolicyProvider RolePolicyProvider
}

// RolePolicyProvider abstracts role-policy lookup for clip count limits (#1029)。
type RolePolicyProvider interface {
	GetUserPolicies(userID string) map[string]any
}

// SetRolePolicyProvider wires a RolePolicyProvider so Create / AddNote
// enforce the clipLimit / noteEachClipsLimit role policies (#1029).
func (s *Service) SetRolePolicyProvider(p RolePolicyProvider) {
	s.rolePolicyProvider = p
}

// NewService constructs a clip Service.
func NewService(
	repo repository.ClipRepository,
	noteRepo repository.ClipNoteRepository,
	notes repository.NoteRepository,
	idGen id.Generator,
) *Service {
	return &Service{
		repo:     repo,
		noteRepo: noteRepo,
		notes:    notes,
		idGen:    idGen,
		clock:    time.Now,
	}
}

// SetClock overrides the time source. Intended for tests.
func (s *Service) SetClock(now func() time.Time) {
	if now != nil {
		s.clock = now
	}
}

// CreateInput is the parameter set for Service.Create.
type CreateInput struct {
	OwnerID     string
	Name        string
	Description *string
	IsPublic    bool
}

// Create persists a new clip and returns it.
func (s *Service) Create(in CreateInput) (*model.Clip, error) {
	if in.Name == "" {
		return nil, ErrClipNameRequired
	}
	if in.OwnerID == "" {
		return nil, errors.New("ownerId is required")
	}
	// clipLimit role policy gate (#1029)。
	if s.rolePolicyProvider != nil {
		if limit, ok := s.rolePolicyProvider.GetUserPolicies(in.OwnerID)["clipLimit"].(int); ok && limit >= 0 {
			existing, err := s.repo.ListByUser(in.OwnerID, "", "", 9999, 0)
			if err != nil {
				return nil, err
			}
			if len(existing) >= limit {
				return nil, ErrTooManyClips
			}
		}
	}
	now := s.clock()
	c := &model.Clip{
		ID:          s.idGen.Generate(now),
		UserID:      in.OwnerID,
		Name:        in.Name,
		Description: in.Description,
		IsPublic:    in.IsPublic,
	}
	if err := s.repo.Create(c); err != nil {
		return nil, err
	}
	return c, nil
}

// Show returns a clip by id. requesterID は閲覧者で、空文字なら anonymous
// 扱い。private clip は所有者だけがアクセスできる。
func (s *Service) Show(requesterID, clipID string) (*model.Clip, error) {
	c, err := s.repo.FindByID(clipID)
	if err != nil {
		return nil, ErrClipNotFound
	}
	if !c.IsPublic && c.UserID != requesterID {
		return nil, ErrAccessDenied
	}
	return c, nil
}

// UpdateInput holds the editable fields of a clip.
type UpdateInput struct {
	Name        *string
	Description **string
	IsPublic    *bool
}

// Update applies the non-nil fields to a clip owned by ownerID.
func (s *Service) Update(ownerID, clipID string, in UpdateInput) (*model.Clip, error) {
	c, err := s.repo.FindByID(clipID)
	if err != nil {
		return nil, ErrClipNotFound
	}
	if c.UserID != ownerID {
		return nil, ErrAccessDenied
	}
	fields := map[string]any{}
	if in.Name != nil {
		if *in.Name == "" {
			return nil, ErrClipNameRequired
		}
		fields["name"] = *in.Name
	}
	if in.Description != nil {
		fields["description"] = *in.Description
	}
	if in.IsPublic != nil {
		fields["isPublic"] = *in.IsPublic
	}
	if err := s.repo.UpdateFields(clipID, fields); err != nil {
		return nil, err
	}
	return s.repo.FindByID(clipID)
}

// Delete removes a clip owned by ownerID. clip_note は CASCADE で削除される。
func (s *Service) Delete(ownerID, clipID string) error {
	c, err := s.repo.FindByID(clipID)
	if err != nil {
		return ErrClipNotFound
	}
	if c.UserID != ownerID {
		return ErrAccessDenied
	}
	return s.repo.Delete(c)
}

// ListByUser returns the clips owned by userID with cursor (sinceID/untilID)
// or offset pagination. cursor 指定時は offset 無視。
func (s *Service) ListByUser(userID, sinceID, untilID string, limit, offset int) ([]*model.Clip, error) {
	return s.repo.ListByUser(userID, sinceID, untilID, limit, offset)
}

// AddNote attaches a note to the clip. ownerID 以外は AccessDenied。
func (s *Service) AddNote(ownerID, clipID, noteID string) error {
	c, err := s.repo.FindByID(clipID)
	if err != nil {
		return ErrClipNotFound
	}
	if c.UserID != ownerID {
		return ErrAccessDenied
	}
	if _, err := s.notes.FindByID(noteID); err != nil {
		return ErrNoteNotFound
	}
	if _, err := s.noteRepo.FindByPair(clipID, noteID); err == nil {
		return ErrAlreadyClipped
	}
	// noteEachClipsLimit role policy gate (#1029)。clip 内 note 数の上限を
	// owner の policy で評価する (clip 自体は owner-only mutation なので
	// ownerID と一致する)。
	if s.rolePolicyProvider != nil {
		if limit, ok := s.rolePolicyProvider.GetUserPolicies(ownerID)["noteEachClipsLimit"].(int); ok && limit >= 0 {
			existing, err := s.noteRepo.ListByClip(clipID, "", "", 9999)
			if err != nil {
				return err
			}
			if len(existing) >= limit {
				return ErrTooManyClipNotes
			}
		}
	}
	now := s.clock()
	cn := &model.ClipNote{
		ID:     s.idGen.Generate(now),
		ClipID: clipID,
		NoteID: noteID,
	}
	if err := s.noteRepo.Create(cn); err != nil {
		return err
	}
	_ = s.repo.IncrementCount(clipID, "notesCount", 1)
	_ = s.repo.UpdateFields(clipID, map[string]any{"lastClippedAt": &now})
	return nil
}

// RemoveNote detaches a note from the clip. ownerID 以外は AccessDenied。
func (s *Service) RemoveNote(ownerID, clipID, noteID string) error {
	c, err := s.repo.FindByID(clipID)
	if err != nil {
		return ErrClipNotFound
	}
	if c.UserID != ownerID {
		return ErrAccessDenied
	}
	cn, err := s.noteRepo.FindByPair(clipID, noteID)
	if err != nil {
		return ErrNotClipped
	}
	if err := s.noteRepo.Delete(cn); err != nil {
		return err
	}
	_ = s.repo.IncrementCount(clipID, "notesCount", -1)
	return nil
}

// Notes returns the notes attached to the clip newest first. requesterID は
// 閲覧者で、private clip は owner だけがアクセスできる (Show と同じ規則)。
func (s *Service) Notes(requesterID, clipID, untilID, sinceID string, limit int) ([]*model.Note, error) {
	c, err := s.repo.FindByID(clipID)
	if err != nil {
		return nil, ErrClipNotFound
	}
	if !c.IsPublic && c.UserID != requesterID {
		return nil, ErrAccessDenied
	}
	rows, err := s.noteRepo.ListByClip(clipID, untilID, sinceID, limit)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.NoteID)
	}
	return s.notes.FindManyByIDsWithUser(ids)
}
