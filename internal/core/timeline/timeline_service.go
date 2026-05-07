package timeline

import (
	"context"
	"errors"
	"sort"

	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
)

// MaxTimelineLength caps the number of IDs kept in each Redis timeline list.
// Misskey本家のデフォルトと同じ200。
const MaxTimelineLength = 200

// Errors returned by Service.
var (
	// ErrUnauthenticated is returned by Home/Hybrid timelines when no user is provided.
	ErrUnauthenticated = errors.New("user is required for this timeline")
)

// NoteSource is the minimum interface required by Service to resolve note IDs
// and to fall back to a database scan when Redis is empty.
type NoteSource interface {
	FindManyByIDsWithUser(ids []string) ([]*model.Note, error)
}

// Service exposes the four timeline endpoints (home/local/global/hybrid).
// Reads always go through Redis first; on a miss it falls back to a direct
// repository query.
type Service struct {
	fanout        *FanoutTimelineService
	noteRepo      repository.NoteRepository
	followingRepo repository.FollowingRepository
}

// NewService creates a new timeline Service.
func NewService(fanout *FanoutTimelineService, noteRepo repository.NoteRepository, followingRepo repository.FollowingRepository) *Service {
	return &Service{fanout: fanout, noteRepo: noteRepo, followingRepo: followingRepo}
}

// HomeTimeline returns the timeline for a logged-in user. The home timeline
// shows notes by users they follow plus their own notes.
func (s *Service) HomeTimeline(ctx context.Context, viewer *model.User, untilID, sinceID string, limit int, filter TimelineFilter) ([]*model.Note, error) {
	if viewer == nil {
		return nil, ErrUnauthenticated
	}
	if limit <= 0 {
		limit = 20
	}
	ids, err := s.fanout.Get(ctx, HomeTimelineName(viewer.ID), untilID, sinceID, limit)
	if err != nil {
		return nil, err
	}
	if len(ids) > 0 {
		notes, err := s.resolve(ids)
		if err != nil {
			return nil, err
		}
		notes = ApplyFilter(notes, viewer.ID, filter)
		if !filter.AllowPartial && len(notes) < limit {
			return s.noteRepo.ListHomeTimeline(viewer.ID, limit, sinceID, untilID, toDBFilter(filter, viewer.ID))
		}
		return notes, nil
	}
	return s.noteRepo.ListHomeTimeline(viewer.ID, limit, sinceID, untilID, toDBFilter(filter, viewer.ID))
}

// LocalTimeline returns notes posted by local users with public/home visibility.
func (s *Service) LocalTimeline(ctx context.Context, viewer *model.User, untilID, sinceID string, limit int, filter TimelineFilter) ([]*model.Note, error) {
	if limit <= 0 {
		limit = 20
	}
	viewerID := ""
	if viewer != nil {
		viewerID = viewer.ID
	}
	ids, err := s.fanout.Get(ctx, LocalTimeline, untilID, sinceID, limit)
	if err != nil {
		return nil, err
	}
	if len(ids) > 0 {
		notes, err := s.resolve(ids)
		if err != nil {
			return nil, err
		}
		notes = ApplyFilter(notes, viewerID, filter)
		if !filter.AllowPartial && len(notes) < limit {
			return s.noteRepo.ListLocalTimeline(limit, sinceID, untilID, toDBFilter(filter, viewerID))
		}
		return notes, nil
	}
	return s.noteRepo.ListLocalTimeline(limit, sinceID, untilID, toDBFilter(filter, viewerID))
}

// GlobalTimeline returns all public notes including federated remotes.
func (s *Service) GlobalTimeline(ctx context.Context, viewer *model.User, untilID, sinceID string, limit int, filter TimelineFilter) ([]*model.Note, error) {
	if limit <= 0 {
		limit = 20
	}
	viewerID := ""
	if viewer != nil {
		viewerID = viewer.ID
	}
	ids, err := s.fanout.Get(ctx, GlobalTimeline, untilID, sinceID, limit)
	if err != nil {
		return nil, err
	}
	if len(ids) > 0 {
		notes, err := s.resolve(ids)
		if err != nil {
			return nil, err
		}
		notes = ApplyFilter(notes, viewerID, filter)
		if !filter.AllowPartial && len(notes) < limit {
			return s.noteRepo.ListGlobalTimeline(limit, sinceID, untilID, toDBFilter(filter, viewerID))
		}
		return notes, nil
	}
	return s.noteRepo.ListGlobalTimeline(limit, sinceID, untilID, toDBFilter(filter, viewerID))
}

// HybridTimeline merges home and local timelines into a single feed.
func (s *Service) HybridTimeline(ctx context.Context, viewer *model.User, untilID, sinceID string, limit int, filter TimelineFilter) ([]*model.Note, error) {
	if viewer == nil {
		return nil, ErrUnauthenticated
	}
	if limit <= 0 {
		limit = 20
	}
	multi, err := s.fanout.GetMulti(ctx, []Name{HomeTimelineName(viewer.ID), LocalTimeline}, untilID, sinceID, limit)
	if err != nil {
		return nil, err
	}
	merged := mergeIDs(multi, limit)
	if len(merged) > 0 {
		notes, err := s.resolve(merged)
		if err != nil {
			return nil, err
		}
		notes = ApplyFilter(notes, viewer.ID, filter)
		if !filter.AllowPartial && len(notes) < limit {
			return s.hybridDBFallback(viewer, untilID, sinceID, limit, filter)
		}
		return notes, nil
	}
	return s.hybridDBFallback(viewer, untilID, sinceID, limit, filter)
}

// hybridDBFallback queries the home and local timelines from the database and
// merges them. upstream Misskey TS と同 semantics: hybrid (= social) timeline
// は home (followee + 自分) と local (= 同 instance の public) の和集合を返す。
//
// 旧実装は ListHomeTimeline のみを呼んでおり、follow 関係が無い viewer に
// とって local public note (= 同 instance の他 user の public) が落ちていた
// (#819 で Playwright spec が detect)。本 helper は両 query 結果を ID 単位で
// dedup → ID 降順 sort → limit 截断する。pagination (sinceID/untilID) は両
// query に同じ値を渡すので merged 結果の boundary は upstream と一致する。
//
// 各 query は単独で limit 件まで返すので merged 後の最大件数は 2*limit、
// dedup と truncate を経て最終的に <= limit 件。逆に両 query の和が limit
// に届かなければ best-effort で limit 未満の結果を返す (= upstream parity、
// pagination は keyset 方式で次 page 取得時に補完される設計)。
func (s *Service) hybridDBFallback(viewer *model.User, untilID, sinceID string, limit int, filter TimelineFilter) ([]*model.Note, error) {
	dbFilter := toDBFilter(filter, viewer.ID)
	homeNotes, err := s.noteRepo.ListHomeTimeline(viewer.ID, limit, sinceID, untilID, dbFilter)
	if err != nil {
		return nil, err
	}
	localNotes, err := s.noteRepo.ListLocalTimeline(limit, sinceID, untilID, dbFilter)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(homeNotes)+len(localNotes))
	out := make([]*model.Note, 0, len(homeNotes)+len(localNotes))
	for _, n := range homeNotes {
		if _, dup := seen[n.ID]; dup {
			continue
		}
		seen[n.ID] = struct{}{}
		out = append(out, n)
	}
	for _, n := range localNotes {
		if _, dup := seen[n.ID]; dup {
			continue
		}
		seen[n.ID] = struct{}{}
		out = append(out, n)
	}
	// ID 降順 sort (= upstream と同じ keyset 順)。aidx は時系列で単調増加
	// するので ID 文字列の lexicographic 比較で十分。
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// toDBFilter converts a TimelineFilter to a model.TimelineDBFilter.
func toDBFilter(f TimelineFilter, viewerID string) model.TimelineDBFilter {
	return model.TimelineDBFilter{
		WithFiles:             f.WithFiles,
		WithRenotes:           f.WithRenotes,
		WithReplies:           f.WithReplies,
		IncludeMyRenotes:      f.IncludeMyRenotes,
		IncludeRenotedMyNotes: f.IncludeRenotedMyNotes,
		IncludeLocalRenotes:   f.IncludeLocalRenotes,
		ViewerID:              viewerID,
		MutedChannelIDs:       f.MutedChannelIDs,
		// production の SQL 経路では muting テーブルへの subquery で filter
		// する (#894)。viewer 単位で bind parameter 数が固定 (2) なので
		// heavy-mute viewer (>1000 mute) でも planning コストが膨らまない。
		// Redis cache 経路 (in-memory ApplyFilter) は引き続き
		// TimelineFilter.MutedUserIDs (loadMutedUserIDs で事前取得) を使う。
		// MutedUserIDs literal は test override 用に残す (本 toDBFilter から
		// は伝搬しない)。
		UseMutingSubquery: viewerID != "",
		// renote-mute も同様に subquery 経路を使う (#903)。pure renote 条件
		// は applyTimelineFilter 側で組み立てる。
		UseRenoteMutingSubquery: viewerID != "",
	}
}

// resolve fetches notes from the repository preserving id ordering.
func (s *Service) resolve(ids []string) ([]*model.Note, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	return s.noteRepo.FindManyByIDsWithUser(ids)
}

// mergeIDs flattens multiple ID slices, deduplicates, sorts id desc and caps.
func mergeIDs(slices [][]string, limit int) []string {
	seen := make(map[string]struct{})
	var all []string
	for _, s := range slices {
		for _, id := range s {
			if _, dup := seen[id]; dup {
				continue
			}
			seen[id] = struct{}{}
			all = append(all, id)
		}
	}
	// id降順
	for i := 0; i < len(all); i++ {
		for j := i + 1; j < len(all); j++ {
			if all[i] < all[j] {
				all[i], all[j] = all[j], all[i]
			}
		}
	}
	if limit > 0 && len(all) > limit {
		all = all[:limit]
	}
	return all
}
