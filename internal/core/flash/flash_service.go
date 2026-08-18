// Package flash provides the user-facing Flash (Misskey Play) CRUD service
// plus Like / Unlike, Featured / Search and listing helpers. Misskey 互換の
// ローカル限定機能で、ActivityPub 連携は持たない。
package flash

import (
	"errors"
	"time"

	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
)

// Errors returned by Service.
var (
	// ErrFlashNotFound is returned when the requested Flash does not exist.
	ErrFlashNotFound = errors.New("flash not found")
	// ErrFlashTitleRequired is returned when title is empty on Create / Update.
	ErrFlashTitleRequired = errors.New("flash title is required")
	// ErrFlashScriptRequired is returned when script is empty on Create.
	ErrFlashScriptRequired = errors.New("flash script is required")
	// ErrAccessDenied is returned when a user attempts to mutate a Flash they
	// do not own.
	ErrAccessDenied = errors.New("not the owner of this flash")
	// ErrYourFlash is returned when a user attempts to Like their own Flash
	// (upstream flash/like の yourFlash, #1548)。
	ErrYourFlash = errors.New("cannot like your own flash")
	// ErrAlreadyLiked is returned when a user attempts to Like a Flash they
	// have already liked.
	ErrAlreadyLiked = errors.New("flash is already liked")
	// ErrNotLiked is returned when a user attempts to Unlike a Flash they
	// have not liked.
	ErrNotLiked = errors.New("flash is not liked")
)

// Service provides Flash CRUD plus Like / Unlike / Featured / Search.
type Service struct {
	repo     repository.FlashRepository
	likeRepo repository.FlashLikeRepository
	idGen    id.Generator
	clock    func() time.Time
}

// NewService constructs a Flash Service.
func NewService(
	repo repository.FlashRepository,
	likeRepo repository.FlashLikeRepository,
	idGen id.Generator,
) *Service {
	return &Service{
		repo:     repo,
		likeRepo: likeRepo,
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
	Title       string
	Summary     string
	Script      string
	Permissions []string
	Visibility  string
}

// Create persists a new Flash and returns it.
func (s *Service) Create(in CreateInput) (*model.Flash, error) {
	if in.OwnerID == "" {
		return nil, errors.New("ownerId is required")
	}
	if in.Title == "" {
		return nil, ErrFlashTitleRequired
	}
	if in.Script == "" {
		return nil, ErrFlashScriptRequired
	}
	if in.Visibility == "" {
		in.Visibility = "public"
	}
	now := s.clock()
	f := &model.Flash{
		ID:          s.idGen.Generate(now),
		UpdatedAt:   now,
		Title:       in.Title,
		Summary:     in.Summary,
		UserID:      in.OwnerID,
		Script:      in.Script,
		Permissions: model.StringArray(in.Permissions),
		Visibility:  in.Visibility,
	}
	if err := s.repo.Create(f); err != nil {
		return nil, err
	}
	return f, nil
}

// Show returns a Flash by id. requesterID is currently unused (Flash は
// public のみで、特に閲覧権限の概念はない) but kept for parity with the
// page service to ease future expansion.
func (s *Service) Show(_, flashID string) (*model.Flash, error) {
	f, err := s.repo.FindByID(flashID)
	if err != nil {
		return nil, ErrFlashNotFound
	}
	return f, nil
}

// UpdateInput holds the editable fields of a Flash.
type UpdateInput struct {
	Title       *string
	Summary     *string
	Script      *string
	Permissions *[]string
	Visibility  *string
}

// Update applies the non-nil fields to a Flash owned by ownerID.
func (s *Service) Update(ownerID, flashID string, in UpdateInput) (*model.Flash, error) {
	f, err := s.repo.FindByID(flashID)
	if err != nil {
		return nil, ErrFlashNotFound
	}
	if f.UserID != ownerID {
		return nil, ErrAccessDenied
	}
	fields := map[string]any{}
	if in.Title != nil {
		if *in.Title == "" {
			return nil, ErrFlashTitleRequired
		}
		fields["title"] = *in.Title
	}
	if in.Summary != nil {
		fields["summary"] = *in.Summary
	}
	if in.Script != nil {
		fields["script"] = *in.Script
	}
	if in.Permissions != nil {
		// GORM の Updates(map) で plain []string を渡すと、空 slice が
		// model.StringArray の Valuer を経由しないまま NULL に倒れて
		// NOT NULL 制約違反 (SQLSTATE 23502) になる (#896)。Create と同じ
		// model.StringArray で wrap して空配列 '{}' として書き込ませる。
		fields["permissions"] = model.StringArray(*in.Permissions)
	}
	if in.Visibility != nil {
		fields["visibility"] = *in.Visibility
	}
	fields["updatedAt"] = s.clock()
	if err := s.repo.UpdateFields(flashID, fields); err != nil {
		return nil, err
	}
	return s.repo.FindByID(flashID)
}

// Delete removes a Flash owned by ownerID. flash_like は CASCADE で削除される。
func (s *Service) Delete(ownerID, flashID string) error {
	f, err := s.repo.FindByID(flashID)
	if err != nil {
		return ErrFlashNotFound
	}
	if f.UserID != ownerID {
		return ErrAccessDenied
	}
	return s.repo.Delete(f)
}

// DeleteByID removes a Flash without an owner check. The caller is responsible
// for authorization — used by the moderator-delete path in the handler where
// the owner-or-moderator decision is made before calling (#1548). Returns
// ErrFlashNotFound when the flash does not exist.
func (s *Service) DeleteByID(flashID string) error {
	f, err := s.repo.FindByID(flashID)
	if err != nil {
		return ErrFlashNotFound
	}
	return s.repo.Delete(f)
}

// My returns the flashes owned by userID with cursor (sinceID/untilID) or
// offset pagination. Cursor 指定時は offset 無視。
func (s *Service) My(userID, sinceID, untilID string, limit, offset int) ([]*model.Flash, error) {
	return s.repo.ListByUser(userID, sinceID, untilID, limit, offset)
}

// Featured returns the flashes ordered by likedCount desc, or by id when
// cursor is supplied (frontend Paginator対応)。
func (s *Service) Featured(sinceID, untilID string, limit, offset int) ([]*model.Flash, error) {
	return s.repo.ListFeatured(sinceID, untilID, limit, offset)
}

// Search performs a substring search across title and summary.
func (s *Service) Search(query, sinceID, untilID string, limit, offset int) ([]*model.Flash, error) {
	return s.repo.Search(query, sinceID, untilID, limit, offset)
}

// Like attaches a Like row from userID to flashID.
func (s *Service) Like(userID, flashID string) error {
	if userID == "" {
		return errors.New("userId is required")
	}
	f, err := s.repo.FindByID(flashID)
	if err != nil {
		return ErrFlashNotFound
	}
	// 自分の flash は like できない (upstream flash/like の yourFlash, #1548)。
	// TS の順序 (noSuchFlash -> yourFlash -> alreadyLiked) に合わせ NotFound の
	// 直後・already-liked チェックの前に置く。
	if f.UserID == userID {
		return ErrYourFlash
	}
	if exists, err := s.likeRepo.Exists(userID, flashID); err != nil {
		return err
	} else if exists {
		return ErrAlreadyLiked
	}
	now := s.clock()
	fl := &model.FlashLike{
		ID:      s.idGen.Generate(now),
		UserID:  userID,
		FlashID: flashID,
	}
	if err := s.likeRepo.Create(fl); err != nil {
		return err
	}
	_ = s.repo.IncrementCount(flashID, "likedCount", 1)
	return nil
}

// Unlike removes a Like row.
func (s *Service) Unlike(userID, flashID string) error {
	if userID == "" {
		return errors.New("userId is required")
	}
	if _, err := s.repo.FindByID(flashID); err != nil {
		return ErrFlashNotFound
	}
	fl, err := s.likeRepo.FindByPair(userID, flashID)
	if err != nil {
		return ErrNotLiked
	}
	if err := s.likeRepo.Delete(fl); err != nil {
		return err
	}
	_ = s.repo.IncrementCount(flashID, "likedCount", -1)
	return nil
}

// IsLikedBy reports whether userID has liked flashID. Used by flash/show
// to set `isLiked` on the response so the frontend's like button reflects
// the current state.
func (s *Service) IsLikedBy(userID, flashID string) (bool, error) {
	return s.likeRepo.Exists(userID, flashID)
}

// LikedFlashIDs returns the set of flashIDs that userID has liked, in one
// query. Used by list endpoints to batch-populate `isLiked` (#1548). Empty
// userID / flashIDs returns an empty set without querying.
func (s *Service) LikedFlashIDs(userID string, flashIDs []string) (map[string]bool, error) {
	set := map[string]bool{}
	if userID == "" || len(flashIDs) == 0 {
		return set, nil
	}
	ids, err := s.likeRepo.ListLikedFlashIDs(userID, flashIDs)
	if err != nil {
		return set, err
	}
	for _, id := range ids {
		set[id] = true
	}
	return set, nil
}

// LikedFlash pairs a flash_like row id with the flash it points to. Used by
// flash/my-likes to match upstream's `{id, flash: Flash}` response shape
// (id = flash_like row id, NOT flash id).
type LikedFlash struct {
	LikeID string
	Flash  *model.Flash
}

// MyLikes returns the flashes that userID has liked, newest first, paired
// with the flash_like row id used for cursor pagination. search が空でなければ
// flash の title/summary を ILIKE 絞り込みする (#1548, upstream myLikes search)。
func (s *Service) MyLikes(userID, search, sinceID, untilID string, limit, offset int) ([]LikedFlash, error) {
	likes, err := s.likeRepo.ListByUserSearch(userID, search, sinceID, untilID, limit, offset)
	if err != nil {
		return nil, err
	}
	out := make([]LikedFlash, 0, len(likes))
	for _, l := range likes {
		f, err := s.repo.FindByID(l.FlashID)
		if err != nil {
			// 削除されていたり権限が変わった Flash はスキップする。
			continue
		}
		out = append(out, LikedFlash{LikeID: l.ID, Flash: f})
	}
	return out, nil
}
