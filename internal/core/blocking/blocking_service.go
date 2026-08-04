// Package blocking provides UserBlockingService for managing user blocks.
package blocking

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
)

// Errors returned by Service.
var (
	// ErrSelfBlock is returned when a user attempts to block themselves.
	ErrSelfBlock = errors.New("cannot block yourself")
	// ErrBlockeeNotFound is returned when the target user does not exist.
	ErrBlockeeNotFound = errors.New("blockee not found")
	// ErrAlreadyBlocking is returned when the blocker already blocks the blockee.
	ErrAlreadyBlocking = errors.New("already blocking")
	// ErrNotBlocking is returned when there is no blocking relationship to delete.
	ErrNotBlocking = errors.New("not blocking")
)

// Service manages user blocking relationships.
type Service struct {
	userRepo      repository.UserRepository
	blockingRepo  repository.BlockingRepository
	followingRepo repository.FollowingRepository
	instanceRepo  repository.InstanceRepository // optional, for #596 incremental counters
	idGen         id.Generator
	// federationHook は local user が remote user を (un)block した際に
	// Block / Undo(Block) を相手 inbox へ配信する (#1560)。nil なら配信しない。
	federationHook FederationHook
	// userMaterializer はリレーでしか観測していない相手を DB へ昇格させる
	// (#2332)。muting.muteeId / blocking.blockeeId が user への外部キー。
	userMaterializer UserMaterializer
}

// FederationHook delivers Block / Undo(Block) AP activities to a remote
// blockee on local block / unblock (#1560)。実装は core/federation。
type FederationHook interface {
	OnBlocked(blockerID, blockeeID string)
	OnUnblocked(blockerID, blockeeID string)
}

// SetFederationHook wires the AP delivery hook used by Block / Unblock (#1560)。
func (s *Service) SetFederationHook(h FederationHook) {
	s.federationHook = h
}

// NewService constructs a UserBlockingService.
// followingRepo は省略可だが、指定されているとブロック時に既存のフォロー関係を
// 自動解除する。
func NewService(
	userRepo repository.UserRepository,
	blockingRepo repository.BlockingRepository,
	followingRepo repository.FollowingRepository,
	idGen id.Generator,
) *Service {
	return &Service{
		userRepo:      userRepo,
		blockingRepo:  blockingRepo,
		followingRepo: followingRepo,
		idGen:         idGen,
	}
}

// SetInstanceRepo wires an InstanceRepository so the auto-unfollow side effect
// of Block also adjusts remote instance followers/following counts (#596)。
// 未配線でも本機能には影響しない (起動時 RecomputeFollowCounts が安全網)。
func (s *Service) SetInstanceRepo(r repository.InstanceRepository) {
	s.instanceRepo = r
}

// Block creates a blocking relationship from blocker to blockee.
// 既存のフォロー関係 (双方向) があれば自動的に解除する。
func (s *Service) Block(blockerID, blockeeID string) (*model.Blocking, error) {
	if blockerID == blockeeID {
		return nil, ErrSelfBlock
	}
	_, uerr := s.userRepo.FindByID(blockeeID)
	if s.materializeUserIfMissing(blockeeID, uerr) {
		_, uerr = s.userRepo.FindByID(blockeeID)
	}
	if uerr != nil {
		return nil, ErrBlockeeNotFound
	}

	if exists, err := s.blockingRepo.Exists(blockerID, blockeeID); err != nil {
		return nil, err
	} else if exists {
		return nil, ErrAlreadyBlocking
	}

	b := &model.Blocking{
		ID:        s.idGen.Generate(time.Now()),
		BlockerID: blockerID,
		BlockeeID: blockeeID,
	}
	if err := s.blockingRepo.Create(b); err != nil {
		return nil, err
	}

	// 既存のフォロー関係を双方向で解除する。fold-in counterも調整する。
	if s.followingRepo != nil {
		s.removeFollowing(blockerID, blockeeID)
		s.removeFollowing(blockeeID, blockerID)
	}

	// remote blockee へ Block activity を配信する (#1560)。
	if s.federationHook != nil {
		s.federationHook.OnBlocked(blockerID, blockeeID)
	}

	return b, nil
}

// Unblock removes a blocking relationship.
func (s *Service) Unblock(blockerID, blockeeID string) error {
	if blockerID == blockeeID {
		return ErrSelfBlock
	}
	b, err := s.blockingRepo.FindByPair(blockerID, blockeeID)
	if err != nil {
		return ErrNotBlocking
	}
	if err := s.blockingRepo.Delete(b); err != nil {
		return err
	}
	// remote blockee へ Undo(Block) を配信する (#1560)。
	if s.federationHook != nil {
		s.federationHook.OnUnblocked(blockerID, blockeeID)
	}
	return nil
}

// IsBlocked reports whether blocker has blocked blockee.
func (s *Service) IsBlocked(blockerID, blockeeID string) (bool, error) {
	return s.blockingRepo.Exists(blockerID, blockeeID)
}

// List returns the user's blockings with cursor (sinceID/untilID) or offset
// pagination. Cursor 指定時は offset 無視 (upstream makePaginationQuery と一致)。
func (s *Service) List(blockerID, sinceID, untilID string, limit, offset int) ([]*model.Blocking, error) {
	if limit <= 0 {
		limit = 10
	}
	return s.blockingRepo.ListByBlocker(blockerID, sinceID, untilID, limit, offset)
}

// removeFollowing deletes a follow edge if it exists and adjusts counters.
// エラーは握り潰し (ベストエフォート)。
//
// IMPORTANT: instance counter 調整ロジックは following.Service の
// adjustInstanceCountsForFollowing と **mirror で維持** すること。循環依存
// 回避のため共通 helper にせず inline しているので、片方変更時には他方も
// 揃える (#596 / PR #626 review)。
func (s *Service) removeFollowing(followerID, followeeID string) {
	f, err := s.followingRepo.FindByPair(followerID, followeeID)
	if err != nil {
		return
	}
	if err := s.followingRepo.Delete(f); err != nil {
		return
	}
	_ = s.userRepo.IncrementFollowingCount(followerID, -1)
	_ = s.userRepo.IncrementFollowersCount(followeeID, -1)
	// remote instance の集計列も -1 する (#596)。block→自動 unfollow 経路で
	// follow row が消えるので following.Service.Unfollow と同じ調整が必要。
	// 失敗は warn-log で観測 signal を残す (following.Service と同様)。
	if s.instanceRepo != nil {
		if f.FollowerHost != nil {
			if err := s.instanceRepo.IncrementFollowersCount(*f.FollowerHost, -1); err != nil {
				slog.Warn("instance counter: followersCount adjust failed (block path)",
					"host", *f.FollowerHost, "delta", -1, "err", err)
			}
		}
		if f.FolloweeHost != nil {
			if err := s.instanceRepo.IncrementFollowingCount(*f.FolloweeHost, -1); err != nil {
				slog.Warn("instance counter: followingCount adjust failed (block path)",
					"host", *f.FolloweeHost, "delta", -1, "err", err)
			}
		}
	}
}

// UserMaterializer promotes a relay-only author out of the ephemeral store
// into a real database row (#2332)。実装は core/ephemeral.Materializer。
//
// ミュート / ブロックは user への外部キーだけを要求し、ノート行は要らない。
// リレーでしか観測していない相手をミュートしようとしたときに、対象が DB に
// 居ないと登録できないため。
type UserMaterializer interface {
	EnsureUser(ctx context.Context, userID string) (*model.User, error)
}

// SetUserMaterializer attaches the ephemeral-author materializer. Optional.
func (s *Service) SetUserMaterializer(m UserMaterializer) {
	s.userMaterializer = m
}

// materializeUserIfMissing promotes an ephemeral author only when the database
// lookup already failed.
func (s *Service) materializeUserIfMissing(userID string, lookupErr error) bool {
	if lookupErr == nil || s.userMaterializer == nil {
		return false
	}
	_, err := s.userMaterializer.EnsureUser(context.Background(), userID)
	return err == nil
}
