package testutil

import (
	"time"

	"github.com/shiroha-a/mk/internal/model"
)

// MockBlockingRepository is a test double for repository.BlockingRepository.
type MockBlockingRepository struct {
	Blockings map[string]*model.Blocking // keyed by ID
}

func NewMockBlockingRepository() *MockBlockingRepository {
	return &MockBlockingRepository{Blockings: make(map[string]*model.Blocking)}
}

func (m *MockBlockingRepository) Create(b *model.Blocking) error {
	m.Blockings[b.ID] = b
	return nil
}

func (m *MockBlockingRepository) Delete(b *model.Blocking) error {
	delete(m.Blockings, b.ID)
	return nil
}

func (m *MockBlockingRepository) FindByPair(blockerID, blockeeID string) (*model.Blocking, error) {
	for _, b := range m.Blockings {
		if b.BlockerID == blockerID && b.BlockeeID == blockeeID {
			return b, nil
		}
	}
	return nil, ErrNotFound
}

func (m *MockBlockingRepository) Exists(blockerID, blockeeID string) (bool, error) {
	_, err := m.FindByPair(blockerID, blockeeID)
	return err == nil, nil
}

func (m *MockBlockingRepository) ListByBlocker(blockerID, sinceID, untilID string, limit, offset int) ([]*model.Blocking, error) {
	var rows []*model.Blocking
	for _, b := range m.Blockings {
		if b.BlockerID != blockerID {
			continue
		}
		if sinceID != "" && b.ID <= sinceID {
			continue
		}
		if untilID != "" && b.ID >= untilID {
			continue
		}
		rows = append(rows, b)
	}
	return paginateBlockings(rows, sinceID, untilID, limit, offset), nil
}

// MockMutingRepository is a test double for repository.MutingRepository.
type MockMutingRepository struct {
	Mutings map[string]*model.Muting
}

func NewMockMutingRepository() *MockMutingRepository {
	return &MockMutingRepository{Mutings: make(map[string]*model.Muting)}
}

func (m *MockMutingRepository) Create(rec *model.Muting) error {
	m.Mutings[rec.ID] = rec
	return nil
}

func (m *MockMutingRepository) Delete(rec *model.Muting) error {
	delete(m.Mutings, rec.ID)
	return nil
}

func (m *MockMutingRepository) FindByPair(muterID, muteeID string) (*model.Muting, error) {
	for _, r := range m.Mutings {
		if r.MuterID == muterID && r.MuteeID == muteeID {
			return r, nil
		}
	}
	return nil, ErrNotFound
}

func (m *MockMutingRepository) Exists(muterID, muteeID string) (bool, error) {
	now := time.Now()
	for _, r := range m.Mutings {
		if r.MuterID == muterID && r.MuteeID == muteeID {
			if r.ExpiresAt != nil && r.ExpiresAt.Before(now) {
				return false, nil
			}
			return true, nil
		}
	}
	return false, nil
}

func (m *MockMutingRepository) ListByMuter(muterID, sinceID, untilID string, limit, offset int) ([]*model.Muting, error) {
	var rows []*model.Muting
	for _, r := range m.Mutings {
		if r.MuterID != muterID {
			continue
		}
		if sinceID != "" && r.ID <= sinceID {
			continue
		}
		if untilID != "" && r.ID >= untilID {
			continue
		}
		rows = append(rows, r)
	}
	return paginateMutings(rows, sinceID, untilID, limit, offset), nil
}

// ListMuteeIDs returns muteeIDs for muterID where the mute is active
// (= ExpiresAt nil or in future). production の挙動と同じ filter logic で
// timeline test の user mute 経路を担保する (#874)。
func (m *MockMutingRepository) ListMuteeIDs(muterID string) ([]string, error) {
	if muterID == "" {
		return nil, nil
	}
	var ids []string
	now := time.Now()
	for _, r := range m.Mutings {
		if r.MuterID != muterID {
			continue
		}
		if r.ExpiresAt != nil && !r.ExpiresAt.After(now) {
			continue
		}
		ids = append(ids, r.MuteeID)
	}
	return ids, nil
}

// MockRenoteMutingRepository is a test double for repository.RenoteMutingRepository.
type MockRenoteMutingRepository struct {
	Mutings map[string]*model.RenoteMuting
}

func NewMockRenoteMutingRepository() *MockRenoteMutingRepository {
	return &MockRenoteMutingRepository{Mutings: make(map[string]*model.RenoteMuting)}
}

func (m *MockRenoteMutingRepository) Create(rec *model.RenoteMuting) error {
	m.Mutings[rec.ID] = rec
	return nil
}

func (m *MockRenoteMutingRepository) Delete(rec *model.RenoteMuting) error {
	delete(m.Mutings, rec.ID)
	return nil
}

func (m *MockRenoteMutingRepository) FindByPair(muterID, muteeID string) (*model.RenoteMuting, error) {
	for _, r := range m.Mutings {
		if r.MuterID == muterID && r.MuteeID == muteeID {
			return r, nil
		}
	}
	return nil, ErrNotFound
}

func (m *MockRenoteMutingRepository) Exists(muterID, muteeID string) (bool, error) {
	_, err := m.FindByPair(muterID, muteeID)
	return err == nil, nil
}

// ListMuteeIDs returns muteeIDs of all renote-mute rows for muterID.
// timeline endpoint で renote-mute された user の pure renote を除外する
// filter 用 (#903)。renote_muting には expiresAt が無いので active filter 不要。
func (m *MockRenoteMutingRepository) ListMuteeIDs(muterID string) ([]string, error) {
	if muterID == "" {
		return nil, nil
	}
	var ids []string
	for _, r := range m.Mutings {
		if r.MuterID != muterID {
			continue
		}
		ids = append(ids, r.MuteeID)
	}
	return ids, nil
}

func (m *MockRenoteMutingRepository) ListByMuter(muterID, sinceID, untilID string, limit, offset int) ([]*model.RenoteMuting, error) {
	var rows []*model.RenoteMuting
	for _, r := range m.Mutings {
		if r.MuterID != muterID {
			continue
		}
		if sinceID != "" && r.ID <= sinceID {
			continue
		}
		if untilID != "" && r.ID >= untilID {
			continue
		}
		rows = append(rows, r)
	}
	return paginateRenoteMutings(rows, sinceID, untilID, limit, offset), nil
}

// applyCursorPagination applies the same ordering / limit / cursor-vs-offset
// rules as the real repositories so mock tests exercise upstream
// makePaginationQuery semantics. ASC ordering kicks in when sinceID is set
// without untilID (matches paginationOrder in repository package).
func paginateBlockings(rows []*model.Blocking, sinceID, untilID string, limit, offset int) []*model.Blocking {
	asc := sinceID != "" && untilID == ""
	for i := 0; i < len(rows); i++ {
		for j := i + 1; j < len(rows); j++ {
			less := rows[i].ID < rows[j].ID
			if (asc && !less) || (!asc && less) {
				rows[i], rows[j] = rows[j], rows[i]
			}
		}
	}
	if limit <= 0 {
		limit = 30
	}
	if sinceID == "" && untilID == "" {
		if offset >= len(rows) {
			return nil
		}
		end := min(offset+limit, len(rows))
		return rows[offset:end]
	}
	if limit > len(rows) {
		limit = len(rows)
	}
	return rows[:limit]
}

func paginateMutings(rows []*model.Muting, sinceID, untilID string, limit, offset int) []*model.Muting {
	asc := sinceID != "" && untilID == ""
	for i := 0; i < len(rows); i++ {
		for j := i + 1; j < len(rows); j++ {
			less := rows[i].ID < rows[j].ID
			if (asc && !less) || (!asc && less) {
				rows[i], rows[j] = rows[j], rows[i]
			}
		}
	}
	if limit <= 0 {
		limit = 30
	}
	if sinceID == "" && untilID == "" {
		if offset >= len(rows) {
			return nil
		}
		end := min(offset+limit, len(rows))
		return rows[offset:end]
	}
	if limit > len(rows) {
		limit = len(rows)
	}
	return rows[:limit]
}

func paginateRenoteMutings(rows []*model.RenoteMuting, sinceID, untilID string, limit, offset int) []*model.RenoteMuting {
	asc := sinceID != "" && untilID == ""
	for i := 0; i < len(rows); i++ {
		for j := i + 1; j < len(rows); j++ {
			less := rows[i].ID < rows[j].ID
			if (asc && !less) || (!asc && less) {
				rows[i], rows[j] = rows[j], rows[i]
			}
		}
	}
	if limit <= 0 {
		limit = 30
	}
	if sinceID == "" && untilID == "" {
		if offset >= len(rows) {
			return nil
		}
		end := min(offset+limit, len(rows))
		return rows[offset:end]
	}
	if limit > len(rows) {
		limit = len(rows)
	}
	return rows[:limit]
}
