package testutil

import (
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lib/pq"
	"github.com/shiroha-a/mk/internal/model"
	"gorm.io/gorm"
)

// MockUserRepository is a test double for repository.UserRepository.
type MockUserRepository struct {
	Users                 map[string]*model.User        // keyed by ID
	Tokens                map[string]*model.User        // keyed by token
	Profiles              map[string]*model.UserProfile // keyed by userID
	FindByUsernameLowerFn func(username string, host *string) (*model.User, error)
	// RecommendationFollowing maps viewerID -> list of followeeIDs to exclude
	// from ListUserRecommendations. Set by tests to emulate the "already
	// following" filter.
	RecommendationFollowing map[string][]string
}

func NewMockUserRepository() *MockUserRepository {
	return &MockUserRepository{
		Users:                   make(map[string]*model.User),
		Tokens:                  make(map[string]*model.User),
		Profiles:                make(map[string]*model.UserProfile),
		RecommendationFollowing: make(map[string][]string),
	}
}

func (m *MockUserRepository) Create(u *model.User) error {
	m.Users[u.ID] = u
	return nil
}

func (m *MockUserRepository) FindByID(id string) (*model.User, error) {
	u, ok := m.Users[id]
	if !ok {
		return nil, ErrNotFound
	}
	return u, nil
}

func (m *MockUserRepository) FindByURI(uri string) (*model.User, error) {
	for _, u := range m.Users {
		if u.URI != nil && *u.URI == uri {
			return u, nil
		}
	}
	return nil, ErrNotFound
}

func (m *MockUserRepository) FindByToken(token string) (*model.User, error) {
	u, ok := m.Tokens[token]
	if !ok {
		return nil, ErrNotFound
	}
	return u, nil
}

func (m *MockUserRepository) FindByUsernameLower(username string, host *string) (*model.User, error) {
	if m.FindByUsernameLowerFn != nil {
		return m.FindByUsernameLowerFn(username, host)
	}
	for _, u := range m.Users {
		if u.UsernameLower == username {
			if host == nil && u.Host == nil {
				return u, nil
			}
			if host != nil && u.Host != nil && *host == *u.Host {
				return u, nil
			}
		}
	}
	return nil, ErrNotFound
}

func (m *MockUserRepository) FindProfileByUserID(userID string) (*model.UserProfile, error) {
	p, ok := m.Profiles[userID]
	if !ok {
		return nil, ErrNotFound
	}
	return p, nil
}

func (m *MockUserRepository) FindProfileByVerifyCode(code string) (*model.UserProfile, error) {
	for _, p := range m.Profiles {
		if p.EmailVerifyCode != nil && *p.EmailVerifyCode == code {
			return p, nil
		}
	}
	return nil, ErrNotFound
}

func (m *MockUserRepository) FindProfileByEmail(email string) (*model.UserProfile, error) {
	for _, p := range m.Profiles {
		if p.Email != nil && *p.Email == email {
			return p, nil
		}
	}
	return nil, ErrNotFound
}

func (m *MockUserRepository) IncrementFollowingCount(userID string, delta int) error {
	if u, ok := m.Users[userID]; ok {
		u.FollowingCount += delta
	}
	return nil
}

func (m *MockUserRepository) IncrementFollowersCount(userID string, delta int) error {
	if u, ok := m.Users[userID]; ok {
		u.FollowersCount += delta
	}
	return nil
}

func (m *MockUserRepository) SearchByUsername(query string, limit, offset int) ([]*model.User, error) {
	var matches []*model.User
	for _, u := range m.Users {
		if len(u.UsernameLower) >= len(query) && u.UsernameLower[:len(query)] == query {
			matches = append(matches, u)
		}
	}
	if offset >= len(matches) {
		return nil, nil
	}
	end := min(offset+limit, len(matches))
	return matches[offset:end], nil
}

func (m *MockUserRepository) UpdateUser(userID string, fields map[string]any) error {
	u, ok := m.Users[userID]
	if !ok {
		return ErrNotFound
	}
	applyUserFields(u, fields)
	return nil
}

func (m *MockUserRepository) CreateProfile(profile *model.UserProfile) error {
	m.Profiles[profile.UserID] = profile
	return nil
}

func (m *MockUserRepository) ListUsers(filter model.UserListFilter) ([]*model.User, error) {
	var result []*model.User
	for _, u := range m.Users {
		switch filter.Origin {
		case "local":
			if u.Host != nil {
				continue
			}
		case "remote":
			if u.Host == nil {
				continue
			}
		}
		switch filter.State {
		case "suspended":
			if !u.IsSuspended {
				continue
			}
		case "alive":
			if u.IsSuspended {
				continue
			}
		}
		result = append(result, u)
	}
	limit := filter.Limit
	if limit <= 0 {
		limit = 10
	}
	offset := filter.Offset
	if offset >= len(result) {
		return nil, nil
	}
	end := min(offset+limit, len(result))
	return result[offset:end], nil
}

// ListRemoteInboxes returns dedup'd inbox URLs for every remote user in the
// mock store. sharedInbox を優先。
func (m *MockUserRepository) ListRemoteInboxes() ([]string, error) {
	seen := make(map[string]struct{})
	var out []string
	for _, u := range m.Users {
		if u.Host == nil {
			continue
		}
		var inbox string
		if u.SharedInbox != nil && *u.SharedInbox != "" {
			inbox = *u.SharedInbox
		} else if u.Inbox != nil && *u.Inbox != "" {
			inbox = *u.Inbox
		}
		if inbox == "" {
			continue
		}
		if _, dup := seen[inbox]; dup {
			continue
		}
		seen[inbox] = struct{}{}
		out = append(out, inbox)
	}
	return out, nil
}

func (m *MockUserRepository) CountOnlineUsers() (int64, error) {
	return 0, nil
}

// Followings はテスト側で MockUserRepository.Followings[viewerID] に followeeID
// のリストを入れておくと、ListUserRecommendations から除外される。
func (m *MockUserRepository) ListUserRecommendations(viewerID string, activeSince time.Time, limit, offset int) ([]*model.User, error) {
	if limit <= 0 {
		limit = 10
	}
	excluded := make(map[string]struct{})
	excluded[viewerID] = struct{}{}
	for _, fid := range m.RecommendationFollowing[viewerID] {
		excluded[fid] = struct{}{}
	}
	var rows []*model.User
	for _, u := range m.Users {
		if _, skip := excluded[u.ID]; skip {
			continue
		}
		if u.Host != nil {
			continue
		}
		if u.IsLocked || !u.IsExplorable {
			continue
		}
		if u.UpdatedAt == nil || u.UpdatedAt.Before(activeSince) {
			continue
		}
		rows = append(rows, u)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].FollowersCount > rows[j].FollowersCount })
	if offset >= len(rows) {
		return nil, nil
	}
	rows = rows[offset:]
	if len(rows) > limit {
		rows = rows[:limit]
	}
	return rows, nil
}

func (m *MockUserRepository) UpdateProfile(userID string, fields map[string]any) error {
	p, ok := m.Profiles[userID]
	if !ok {
		// 既存プロフィールがなければ作成する(本物のDBではFK制約があるが、テストのモックでは緩い)
		p = &model.UserProfile{UserID: userID}
		m.Profiles[userID] = p
	}
	applyProfileFields(p, fields)
	return nil
}

// applyUserFields は単純な型の代表例にだけ対応する。新しいフィールドを使う場合はここに追加する。
func applyUserFields(u *model.User, fields map[string]any) {
	for k, v := range fields {
		switch k {
		case "name":
			if s, ok := v.(*string); ok {
				u.Name = s
			}
		case "inbox":
			if s, ok := v.(*string); ok {
				u.Inbox = s
			}
		case "sharedInbox":
			if s, ok := v.(*string); ok {
				u.SharedInbox = s
			}
		case "lastFetchedAt":
			if t, ok := v.(*time.Time); ok {
				u.LastFetchedAt = t
			}
		case "isLocked":
			if b, ok := v.(bool); ok {
				u.IsLocked = b
			}
		case "isBot":
			if b, ok := v.(bool); ok {
				u.IsBot = b
			}
		case "isCat":
			if b, ok := v.(bool); ok {
				u.IsCat = b
			}
		case "isExplorable":
			if b, ok := v.(bool); ok {
				u.IsExplorable = b
			}
		case "hideOnlineStatus":
			if b, ok := v.(bool); ok {
				u.HideOnlineStatus = b
			}
		case "isSuspended":
			if b, ok := v.(bool); ok {
				u.IsSuspended = b
			}
		case "isDeleted":
			if b, ok := v.(bool); ok {
				u.IsDeleted = b
			}
		case "token":
			if s, ok := v.(string); ok {
				u.Token = &s
			}
		case "movedToUri":
			// core/move が string を直接渡す運用。nil は未対応 (現状の要件にない)。
			if s, ok := v.(string); ok {
				u.MovedToURI = &s
			}
		case "movedAt":
			if t, ok := v.(time.Time); ok {
				u.MovedAt = &t
			}
		case "alsoKnownAs":
			if s, ok := v.(string); ok {
				u.AlsoKnownAs = &s
			}
		}
	}
}

func applyProfileFields(p *model.UserProfile, fields map[string]any) {
	for k, v := range fields {
		switch k {
		case "description":
			if s, ok := v.(*string); ok {
				p.Description = s
			}
		case "location":
			if s, ok := v.(*string); ok {
				p.Location = s
			}
		case "birthday":
			if s, ok := v.(*string); ok {
				p.Birthday = s
			}
		case "lang":
			if s, ok := v.(*string); ok {
				p.Lang = s
			}
		case "followedMessage":
			if s, ok := v.(*string); ok {
				p.FollowedMessage = s
			}
		case "publicReactions":
			if b, ok := v.(bool); ok {
				p.PublicReactions = b
			}
		case "alwaysMarkNsfw":
			if b, ok := v.(bool); ok {
				p.AlwaysMarkNsfw = b
			}
		case "autoSensitive":
			if b, ok := v.(bool); ok {
				p.AutoSensitive = b
			}
		case "noCrawle":
			if b, ok := v.(bool); ok {
				p.NoCrawle = b
			}
		case "preventAiLearning":
			if b, ok := v.(bool); ok {
				p.PreventAiLearning = b
			}
		case "password":
			if s, ok := v.(string); ok {
				p.Password = &s
			}
		case "achievements":
			if s, ok := v.(string); ok {
				p.Achievements = []byte(s)
			}
		case "twoFactorTempSecret":
			switch val := v.(type) {
			case string:
				p.TwoFactorTempSecret = &val
			case nil:
				p.TwoFactorTempSecret = nil
			}
		case "twoFactorSecret":
			switch val := v.(type) {
			case string:
				p.TwoFactorSecret = &val
			case nil:
				p.TwoFactorSecret = nil
			}
		case "twoFactorEnabled":
			if b, ok := v.(bool); ok {
				p.TwoFactorEnabled = b
			}
		case "twoFactorBackupSecret":
			switch val := v.(type) {
			case pq.StringArray:
				p.TwoFactorBackupSecret = val
			case []string:
				p.TwoFactorBackupSecret = pq.StringArray(val)
			case nil:
				p.TwoFactorBackupSecret = nil
			}
		case "securityKeysAvailable":
			if b, ok := v.(bool); ok {
				p.SecurityKeysAvailable = b
			}
		case "usePasswordLessLogin":
			if b, ok := v.(bool); ok {
				p.UsePasswordLessLogin = b
			}
		case "room":
			switch val := v.(type) {
			case string:
				p.Room = []byte(val)
			case []byte:
				p.Room = val
			}
		case "clientData":
			switch val := v.(type) {
			case string:
				p.ClientData = []byte(val)
			case []byte:
				p.ClientData = val
			}
		case "email":
			switch val := v.(type) {
			case string:
				p.Email = &val
			case *string:
				p.Email = val
			case nil:
				p.Email = nil
			}
		case "emailVerified":
			if b, ok := v.(bool); ok {
				p.EmailVerified = b
			}
		case "emailVerifyCode":
			switch val := v.(type) {
			case string:
				p.EmailVerifyCode = &val
			case *string:
				p.EmailVerifyCode = val
			case nil:
				p.EmailVerifyCode = nil
			}
		}
	}
}

// MockNoteRepository is a test double for repository.NoteRepository.
type MockNoteRepository struct {
	Notes          map[string]*model.Note
	ReactionCounts map[string]map[string]int // noteID -> reaction -> count
}

func NewMockNoteRepository() *MockNoteRepository {
	return &MockNoteRepository{
		Notes:          make(map[string]*model.Note),
		ReactionCounts: make(map[string]map[string]int),
	}
}

func (m *MockNoteRepository) Create(note *model.Note) error {
	m.Notes[note.ID] = note
	return nil
}

func (m *MockNoteRepository) FindByID(id string) (*model.Note, error) {
	n, ok := m.Notes[id]
	if !ok {
		return nil, ErrNotFound
	}
	return n, nil
}

func (m *MockNoteRepository) FindByIDWithUser(id string) (*model.Note, error) {
	return m.FindByID(id)
}

func (m *MockNoteRepository) FindByURI(uri string) (*model.Note, error) {
	for _, n := range m.Notes {
		if n.URI != nil && *n.URI == uri {
			return n, nil
		}
	}
	return nil, ErrNotFound
}

func (m *MockNoteRepository) Delete(note *model.Note) error {
	delete(m.Notes, note.ID)
	return nil
}

func (m *MockNoteRepository) Update(note *model.Note, column string, value any) error {
	return nil
}

// UpdateFields applies field updates to the in-memory note. テストで参照される
// 列だけを反映する; 拡張時はここに追記する。
func (m *MockNoteRepository) UpdateFields(noteID string, fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}
	n, ok := m.Notes[noteID]
	if !ok {
		return ErrNotFound
	}
	for k, v := range fields {
		switch k {
		case "text":
			if s, ok := v.(*string); ok {
				n.Text = s
			}
		case "cw":
			if s, ok := v.(*string); ok {
				n.CW = s
			}
		case "mentions":
			if a, ok := v.([]string); ok {
				n.Mentions = a
			}
		}
	}
	return nil
}

// IncrementCount mutates the in-memory note's counter column for tests.
func (m *MockNoteRepository) IncrementCount(noteID, column string, delta int) error {
	n, ok := m.Notes[noteID]
	if !ok {
		return ErrNotFound
	}
	switch column {
	case "renoteCount":
		n.RenoteCount += int16(delta)
	case "repliesCount":
		n.RepliesCount += int16(delta)
	}
	return nil
}

// IncrementReaction adjusts an in-memory reaction count map.
// テスト用に Reactions を JSON Map にデコードして加算する。
func (m *MockNoteRepository) IncrementReaction(noteID, reaction string, delta int) error {
	n, ok := m.Notes[noteID]
	if !ok {
		return ErrNotFound
	}
	if m.ReactionCounts == nil {
		m.ReactionCounts = make(map[string]map[string]int)
	}
	if m.ReactionCounts[noteID] == nil {
		m.ReactionCounts[noteID] = make(map[string]int)
	}
	c := m.ReactionCounts[noteID][reaction] + delta
	if c <= 0 {
		delete(m.ReactionCounts[noteID], reaction)
	} else {
		m.ReactionCounts[noteID][reaction] = c
	}
	_ = n // 実際のJSONBは更新しない
	return nil
}

// ListRenotesOf returns notes whose renoteId equals noteID.
func (m *MockNoteRepository) ListRenotesOf(noteID string, untilID, sinceID string, limit int) ([]*model.Note, error) {
	return m.listFiltered(func(n *model.Note) bool {
		return n.RenoteID != nil && *n.RenoteID == noteID
	}, untilID, sinceID, limit), nil
}

// ListRepliesOf returns notes whose replyId equals noteID.
func (m *MockNoteRepository) ListRepliesOf(noteID string, untilID, sinceID string, limit int) ([]*model.Note, error) {
	return m.listFiltered(func(n *model.Note) bool {
		return n.ReplyID != nil && *n.ReplyID == noteID
	}, untilID, sinceID, limit), nil
}

// ListChildrenOf returns notes that reply to or quote the given noteID.
func (m *MockNoteRepository) ListChildrenOf(noteID string, untilID, sinceID string, limit int) ([]*model.Note, error) {
	return m.listFiltered(func(n *model.Note) bool {
		if n.ReplyID != nil && *n.ReplyID == noteID {
			return true
		}
		if n.RenoteID != nil && *n.RenoteID == noteID {
			return true
		}
		return false
	}, untilID, sinceID, limit), nil
}

// SearchByFilter returns public/home notes matching the filter (text ILIKE +
// optional userId / channelId / host)。`Host == "."` はローカル限定。
func (m *MockNoteRepository) SearchByFilter(f model.NoteSearchFilter) ([]*model.Note, error) {
	q := strings.ToLower(f.Query)
	limit := f.Limit
	if limit <= 0 {
		limit = 10
	}
	return m.listFiltered(func(n *model.Note) bool {
		if n.Visibility != model.NoteVisibilityPublic && n.Visibility != model.NoteVisibilityHome {
			return false
		}
		if n.Text == nil {
			return false
		}
		if !strings.Contains(strings.ToLower(*n.Text), q) {
			return false
		}
		if f.UserID != "" && n.UserID != f.UserID {
			return false
		}
		if f.ChannelID != "" {
			if n.ChannelID == nil || *n.ChannelID != f.ChannelID {
				return false
			}
		}
		if f.Host != "" {
			if f.Host == "." {
				if n.UserHost != nil {
					return false
				}
			} else {
				if n.UserHost == nil || *n.UserHost != f.Host {
					return false
				}
			}
		}
		return true
	}, f.UntilID, f.SinceID, limit), nil
}

// listFiltered iterates the in-memory notes, applies filter, sorts by id desc,
// and returns up to `limit` entries.
func (m *MockNoteRepository) listFiltered(filter func(*model.Note) bool, untilID, sinceID string, limit int) []*model.Note {
	var out []*model.Note
	for _, n := range m.Notes {
		if !filter(n) {
			continue
		}
		if untilID != "" && n.ID >= untilID {
			continue
		}
		if sinceID != "" && n.ID <= sinceID {
			continue
		}
		out = append(out, n)
	}
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[i].ID < out[j].ID {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// ListByChannelID returns notes posted to the given channel sorted by id desc.
func (m *MockNoteRepository) ListByChannelID(channelID string, untilID, sinceID string, limit int) ([]*model.Note, error) {
	return m.listFiltered(func(n *model.Note) bool {
		return n.ChannelID != nil && *n.ChannelID == channelID
	}, untilID, sinceID, limit), nil
}

func (m *MockNoteRepository) ListByUserID(userID string, untilID, sinceID string, limit int) ([]*model.Note, error) {
	var notes []*model.Note
	for _, n := range m.Notes {
		if n.UserID != userID {
			continue
		}
		if untilID != "" && n.ID >= untilID {
			continue
		}
		if sinceID != "" && n.ID <= sinceID {
			continue
		}
		notes = append(notes, n)
	}
	// id降順でソート
	for i := 0; i < len(notes); i++ {
		for j := i + 1; j < len(notes); j++ {
			if notes[i].ID < notes[j].ID {
				notes[i], notes[j] = notes[j], notes[i]
			}
		}
	}
	if limit > 0 && len(notes) > limit {
		notes = notes[:limit]
	}
	return notes, nil
}

func (m *MockNoteRepository) FindManyByIDsWithUser(ids []string) ([]*model.Note, error) {
	out := make([]*model.Note, 0, len(ids))
	for _, id := range ids {
		if n, ok := m.Notes[id]; ok {
			out = append(out, n)
		}
	}
	return out, nil
}

func (m *MockNoteRepository) ListFeatured(limit, offset int) ([]*model.Note, error) {
	var result []*model.Note
	for _, n := range m.Notes {
		if string(n.Visibility) == "public" {
			result = append(result, n)
		}
	}
	if limit <= 0 {
		limit = 10
	}
	if offset >= len(result) {
		return nil, nil
	}
	end := min(offset+limit, len(result))
	return result[offset:end], nil
}

func (m *MockNoteRepository) FindRenoteByUser(userID, renoteID string) (*model.Note, error) {
	for _, n := range m.Notes {
		if n.UserID == userID && n.RenoteID != nil && *n.RenoteID == renoteID && n.Text == nil {
			return n, nil
		}
	}
	return nil, ErrNotFound
}

func (m *MockNoteRepository) ListMentions(userID string, limit int, _, _ string) ([]*model.Note, error) {
	var result []*model.Note
	for _, n := range m.Notes {
		for _, mention := range n.Mentions {
			if mention == userID {
				result = append(result, n)
				break
			}
		}
	}
	if limit <= 0 {
		limit = 10
	}
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

func (m *MockNoteRepository) SearchByTag(tag string, limit int, _, _ string) ([]*model.Note, error) {
	var result []*model.Note
	for _, n := range m.Notes {
		for _, t := range n.Tags {
			if t == tag {
				result = append(result, n)
				break
			}
		}
	}
	if limit <= 0 {
		limit = 10
	}
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

func (m *MockNoteRepository) ListByFileID(_ string) ([]*model.Note, error)  { return nil, nil }
func (m *MockNoteRepository) IncrementUserNotesCount(_ string, _ int) error { return nil }

func (m *MockNoteRepository) ListHomeTimeline(_ string, limit int, _, _ string, _ model.TimelineDBFilter) ([]*model.Note, error) {
	return m.listPublic(limit)
}

func (m *MockNoteRepository) ListLocalTimeline(limit int, _, _ string, _ model.TimelineDBFilter) ([]*model.Note, error) {
	return m.listPublic(limit)
}

func (m *MockNoteRepository) ListGlobalTimeline(limit int, _, _ string, _ model.TimelineDBFilter) ([]*model.Note, error) {
	return m.listPublic(limit)
}

func (m *MockNoteRepository) DeleteExpiredRemoteNotes(_, _ int) (int64, error) {
	return 0, nil
}

func (m *MockNoteRepository) DeleteByUserBatch(userID string, batchSize int) (int64, error) {
	if batchSize <= 0 {
		batchSize = 100
	}
	n := int64(0)
	for id, note := range m.Notes {
		if n >= int64(batchSize) {
			break
		}
		if note.UserID == userID {
			delete(m.Notes, id)
			n++
		}
	}
	return n, nil
}

func (m *MockNoteRepository) CountReplyTargets(userID string, limit int) ([]model.ReplyTargetCount, error) {
	if limit <= 0 {
		limit = 10
	}
	counts := make(map[string]int64)
	for _, n := range m.Notes {
		if n.UserID != userID || n.ReplyID == nil || n.ReplyUserID == nil {
			continue
		}
		if *n.ReplyUserID == userID {
			continue
		}
		counts[*n.ReplyUserID]++
	}
	rows := make([]model.ReplyTargetCount, 0, len(counts))
	for uid, c := range counts {
		rows = append(rows, model.ReplyTargetCount{UserID: uid, Count: c})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Count > rows[j].Count })
	if len(rows) > limit {
		rows = rows[:limit]
	}
	return rows, nil
}

func (m *MockNoteRepository) listPublic(limit int) ([]*model.Note, error) {
	var result []*model.Note
	for _, n := range m.Notes {
		if n.Visibility == "public" || n.Visibility == "home" {
			result = append(result, n)
		}
	}
	if limit > 0 && len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

// MockNoteFavoriteRepository is a test double for repository.NoteFavoriteRepository.
type MockNoteFavoriteRepository struct {
	Favorites map[string]*model.NoteFavorite // keyed by "userId:noteId"
}

func NewMockNoteFavoriteRepository() *MockNoteFavoriteRepository {
	return &MockNoteFavoriteRepository{Favorites: make(map[string]*model.NoteFavorite)}
}

func (m *MockNoteFavoriteRepository) Create(f *model.NoteFavorite) error {
	m.Favorites[f.UserID+":"+f.NoteID] = f
	return nil
}

func (m *MockNoteFavoriteRepository) Delete(userID, noteID string) error {
	delete(m.Favorites, userID+":"+noteID)
	return nil
}

func (m *MockNoteFavoriteRepository) Exists(userID, noteID string) (bool, error) {
	_, ok := m.Favorites[userID+":"+noteID]
	return ok, nil
}

func (m *MockNoteFavoriteRepository) ListByUser(userID string, limit, offset int) ([]*model.NoteFavorite, error) {
	var result []*model.NoteFavorite
	for _, f := range m.Favorites {
		if f.UserID == userID {
			result = append(result, f)
		}
	}
	return result, nil
}

// MockNoteReactionRepository is a test double for repository.NoteReactionRepository.
type MockNoteReactionRepository struct {
	Reactions map[string]*model.NoteReaction // keyed by id
	CreateErr error                          // optional error to return on Create
}

func NewMockNoteReactionRepository() *MockNoteReactionRepository {
	return &MockNoteReactionRepository{Reactions: make(map[string]*model.NoteReaction)}
}

func (m *MockNoteReactionRepository) Create(r *model.NoteReaction) error {
	if m.CreateErr != nil {
		return m.CreateErr
	}
	m.Reactions[r.ID] = r
	return nil
}

func (m *MockNoteReactionRepository) Delete(r *model.NoteReaction) error {
	delete(m.Reactions, r.ID)
	return nil
}

func (m *MockNoteReactionRepository) FindByPair(userID, noteID string) (*model.NoteReaction, error) {
	for _, r := range m.Reactions {
		if r.UserID == userID && r.NoteID == noteID {
			return r, nil
		}
	}
	return nil, ErrNotFound
}

func (m *MockNoteReactionRepository) FindByUserAndNoteIDs(userID string, noteIDs []string) (map[string]*model.NoteReaction, error) {
	result := make(map[string]*model.NoteReaction)
	noteIDSet := make(map[string]bool, len(noteIDs))
	for _, id := range noteIDs {
		noteIDSet[id] = true
	}
	for _, r := range m.Reactions {
		if r.UserID == userID && noteIDSet[r.NoteID] {
			result[r.NoteID] = r
		}
	}
	return result, nil
}

func (m *MockNoteReactionRepository) ListByNoteID(noteID, untilID, sinceID string, limit int, reactions []string) ([]*model.NoteReaction, error) {
	var rows []*model.NoteReaction
	for _, r := range m.Reactions {
		if r.NoteID != noteID {
			continue
		}
		if len(reactions) > 0 {
			matched := false
			for _, rx := range reactions {
				if r.Reaction == rx {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
		}
		if untilID != "" && r.ID >= untilID {
			continue
		}
		if sinceID != "" && r.ID <= sinceID {
			continue
		}
		rows = append(rows, r)
	}
	// id降順
	for i := 0; i < len(rows); i++ {
		for j := i + 1; j < len(rows); j++ {
			if rows[i].ID < rows[j].ID {
				rows[i], rows[j] = rows[j], rows[i]
			}
		}
	}
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}
	return rows, nil
}

// MockEmojiRepository is a test double for repository.EmojiRepository.
type MockEmojiRepository struct {
	// keyed by "name@host" (host="" for local)
	Emojis map[string]*model.Emoji
}

func NewMockEmojiRepository() *MockEmojiRepository {
	return &MockEmojiRepository{Emojis: make(map[string]*model.Emoji)}
}

func (m *MockEmojiRepository) Create(e *model.Emoji) error {
	key := e.Name + "@"
	if e.Host != nil {
		key += *e.Host
	}
	m.Emojis[key] = e
	return nil
}

func (m *MockEmojiRepository) FindByID(id string) (*model.Emoji, error) {
	for _, e := range m.Emojis {
		if e.ID == id {
			return e, nil
		}
	}
	return nil, ErrNotFound
}

func (m *MockEmojiRepository) UpdateFields(id string, fields map[string]any) error {
	for _, e := range m.Emojis {
		if e.ID == id {
			for k, v := range fields {
				switch k {
				case "name":
					if s, ok := v.(string); ok {
						e.Name = s
					}
				case "category":
					if s, ok := v.(string); ok {
						e.Category = &s
					}
				case "license":
					if s, ok := v.(string); ok {
						e.License = &s
					}
				case "aliases":
					if arr, ok := v.([]string); ok {
						e.Aliases = arr
					}
				case "isSensitive":
					if b, ok := v.(bool); ok {
						e.IsSensitive = b
					}
				case "updatedAt":
					if ts, ok := v.(time.Time); ok {
						e.UpdatedAt = &ts
					}
				}
			}
			return nil
		}
	}
	return ErrNotFound
}

func (m *MockEmojiRepository) Delete(id string) error {
	for k, e := range m.Emojis {
		if e.ID == id {
			delete(m.Emojis, k)
			return nil
		}
	}
	return nil
}

func (m *MockEmojiRepository) ListWithFilter(query, category string, local bool, limit, offset int) ([]*model.Emoji, error) {
	var result []*model.Emoji
	for _, e := range m.Emojis {
		if local && e.Host != nil {
			continue
		}
		result = append(result, e)
	}
	if limit <= 0 {
		limit = 50
	}
	if offset >= len(result) {
		return nil, nil
	}
	end := min(offset+limit, len(result))
	return result[offset:end], nil
}

func (m *MockEmojiRepository) ListLocal() ([]*model.Emoji, error) {
	var result []*model.Emoji
	for _, e := range m.Emojis {
		if e.Host == nil {
			result = append(result, e)
		}
	}
	return result, nil
}

func (m *MockEmojiRepository) FindByNameAndHost(name string, host *string) (*model.Emoji, error) {
	key := name + "@"
	if host != nil {
		key += *host
	}
	e, ok := m.Emojis[key]
	if !ok {
		return nil, ErrNotFound
	}
	return e, nil
}

func (m *MockEmojiRepository) FindManyByIDs(ids []string) ([]*model.Emoji, error) {
	set := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		set[id] = struct{}{}
	}
	out := make([]*model.Emoji, 0)
	for _, e := range m.Emojis {
		if _, ok := set[e.ID]; ok {
			out = append(out, e)
		}
	}
	return out, nil
}

func (m *MockEmojiRepository) UpdateFieldsMany(ids []string, fields map[string]any) error {
	if len(ids) == 0 || len(fields) == 0 {
		return nil
	}
	set := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		set[id] = struct{}{}
	}
	for _, e := range m.Emojis {
		if _, ok := set[e.ID]; !ok {
			continue
		}
		for k, v := range fields {
			switch k {
			case "category":
				if s, ok := v.(string); ok {
					e.Category = &s
				}
			case "license":
				if s, ok := v.(string); ok {
					e.License = &s
				}
			case "aliases":
				if arr, ok := v.([]string); ok {
					e.Aliases = arr
				}
			case "updatedAt":
				if ts, ok := v.(time.Time); ok {
					e.UpdatedAt = &ts
				}
			case "isSensitive":
				if b, ok := v.(bool); ok {
					e.IsSensitive = b
				}
			}
		}
	}
	return nil
}

func (m *MockEmojiRepository) DeleteMany(ids []string) error {
	for _, id := range ids {
		// 各 emoji は name@ と name@host の両方のキーで参照されうる。
		// Emojis[id] での逆引きは ID が key の場合にしか当たらないため、走査して削除する。
		for k, e := range m.Emojis {
			if e.ID == id {
				delete(m.Emojis, k)
			}
		}
	}
	return nil
}

func (m *MockEmojiRepository) ListRemoteWithFilter(query, host string, limit, offset int) ([]*model.Emoji, error) {
	out := make([]*model.Emoji, 0)
	seen := make(map[string]bool)
	for _, e := range m.Emojis {
		if seen[e.ID] {
			continue
		}
		if e.Host == nil {
			continue
		}
		if host != "" && *e.Host != host {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(e.Name), strings.ToLower(query)) {
			continue
		}
		seen[e.ID] = true
		out = append(out, e)
	}
	// id DESC で安定ソート
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[i].ID < out[j].ID {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	if limit <= 0 {
		limit = 30
	}
	if offset >= len(out) {
		return []*model.Emoji{}, nil
	}
	end := offset + limit
	if end > len(out) {
		end = len(out)
	}
	return out[offset:end], nil
}

func (m *MockEmojiRepository) ListV2(filter model.EmojiV2Filter) ([]*model.Emoji, error) {
	all := m.filterV2(filter)
	m.sortV2(all, filter.SortKeys)

	limit := filter.Limit
	if limit <= 0 {
		limit = 10
	}
	if limit > 100 {
		limit = 100
	}

	offset := 0
	if filter.Page > 0 {
		offset = (filter.Page - 1) * limit
	}
	if offset >= len(all) {
		return []*model.Emoji{}, nil
	}
	end := offset + limit
	if end > len(all) {
		end = len(all)
	}
	return all[offset:end], nil
}

func (m *MockEmojiRepository) CountV2(filter model.EmojiV2Filter) (int64, error) {
	return int64(len(m.filterV2(filter))), nil
}

// filterV2 extracts emojis matching the v2 filter criteria.
func (m *MockEmojiRepository) filterV2(filter model.EmojiV2Filter) []*model.Emoji {
	seen := make(map[string]bool)
	var out []*model.Emoji
	for _, e := range m.Emojis {
		if seen[e.ID] {
			continue
		}
		if filter.Query != nil {
			fq := filter.Query
			if fq.HostType == "local" && e.Host != nil {
				continue
			}
			if fq.HostType == "remote" && e.Host == nil {
				continue
			}
			if fq.Name != "" && !mockILIKE(e.Name, fq.Name) {
				continue
			}
			if fq.Host != "" {
				if e.Host == nil || !mockILIKE(*e.Host, fq.Host) {
					continue
				}
			}
			if fq.Category != "" {
				if e.Category == nil || !mockILIKE(*e.Category, fq.Category) {
					continue
				}
			}
			if fq.Type != "" {
				if e.Type == nil || !mockILIKE(*e.Type, fq.Type) {
					continue
				}
			}
			if fq.Aliases != "" {
				aliasStr := strings.Join(e.Aliases, ",")
				if !mockILIKE(aliasStr, fq.Aliases) {
					continue
				}
			}
			if fq.License != "" {
				if e.License == nil || !mockILIKE(*e.License, fq.License) {
					continue
				}
			}
			if fq.IsSensitive != nil && e.IsSensitive != *fq.IsSensitive {
				continue
			}
			if fq.LocalOnly != nil && e.LocalOnly != *fq.LocalOnly {
				continue
			}
		}
		if filter.SinceID != "" && e.ID <= filter.SinceID {
			continue
		}
		if filter.UntilID != "" && e.ID >= filter.UntilID {
			continue
		}
		seen[e.ID] = true
		out = append(out, e)
	}
	return out
}

// sortV2 sorts emojis according to the given sortKeys.
// Falls back to id DESC when no valid sort key is found.
func (m *MockEmojiRepository) sortV2(emojis []*model.Emoji, sortKeys []string) {
	// 有効なsortKeyが1つもなければid DESCにfallback
	hasValid := false
	for _, sk := range sortKeys {
		if len(sk) >= 2 {
			hasValid = true
			break
		}
	}
	if !hasValid {
		sortKeys = []string{"-id"}
	}
	sort.SliceStable(emojis, func(i, j int) bool {
		for _, sk := range sortKeys {
			if len(sk) < 2 {
				continue
			}
			asc := sk[0] == '+'
			col := sk[1:]
			cmp := mockEmojiCompare(emojis[i], emojis[j], col)
			if cmp == 0 {
				continue
			}
			if asc {
				return cmp < 0
			}
			return cmp > 0
		}
		return false
	})
}

func mockEmojiCompare(a, b *model.Emoji, col string) int {
	switch col {
	case "id":
		return strings.Compare(a.ID, b.ID)
	case "name":
		return strings.Compare(a.Name, b.Name)
	case "host":
		ah, bh := "", ""
		if a.Host != nil {
			ah = *a.Host
		}
		if b.Host != nil {
			bh = *b.Host
		}
		return strings.Compare(ah, bh)
	case "category":
		ac, bc := "", ""
		if a.Category != nil {
			ac = *a.Category
		}
		if b.Category != nil {
			bc = *b.Category
		}
		return strings.Compare(ac, bc)
	default:
		return 0
	}
}

func mockILIKE(value, substr string) bool {
	return strings.Contains(strings.ToLower(value), strings.ToLower(substr))
}

// MockMetaRepository is a test double for repository.MetaRepository.
type MockMetaRepository struct {
	Meta *model.Meta
}

func NewMockMetaRepository() *MockMetaRepository {
	return &MockMetaRepository{}
}

func (m *MockMetaRepository) Fetch() (*model.Meta, error) {
	if m.Meta == nil {
		return nil, ErrNotFound
	}
	return m.Meta, nil
}

func (m *MockMetaRepository) Update(fields map[string]any) error {
	if m.Meta == nil {
		m.Meta = &model.Meta{ID: "x"}
	}
	for k, v := range fields {
		switch k {
		case "rootUserId":
			if s, ok := v.(string); ok {
				m.Meta.RootUserID = &s
			}
		case "proxyAccountId":
			switch vv := v.(type) {
			case string:
				m.Meta.ProxyAccountID = &vv
			case nil:
				m.Meta.ProxyAccountID = nil
			}
		case "enableHcaptcha":
			if b, ok := v.(bool); ok {
				m.Meta.EnableHcaptcha = b
			}
		case "enableRecaptcha":
			if b, ok := v.(bool); ok {
				m.Meta.EnableRecaptcha = b
			}
		case "enableTurnstile":
			if b, ok := v.(bool); ok {
				m.Meta.EnableTurnstile = b
			}
		case "hcaptchaSiteKey":
			if s, ok := v.(string); ok {
				m.Meta.HcaptchaSiteKey = &s
			}
		case "hcaptchaSecretKey":
			if s, ok := v.(string); ok {
				m.Meta.HcaptchaSecretKey = &s
			}
		case "recaptchaSiteKey":
			if s, ok := v.(string); ok {
				m.Meta.RecaptchaSiteKey = &s
			}
		case "recaptchaSecretKey":
			if s, ok := v.(string); ok {
				m.Meta.RecaptchaSecretKey = &s
			}
		case "turnstileSiteKey":
			if s, ok := v.(string); ok {
				m.Meta.TurnstileSiteKey = &s
			}
		case "turnstileSecretKey":
			if s, ok := v.(string); ok {
				m.Meta.TurnstileSecretKey = &s
			}
		}
	}
	return nil
}

func (m *MockMetaRepository) EnsureInitial(id string) error {
	if m.Meta == nil {
		m.Meta = &model.Meta{ID: id}
	}
	return nil
}

// MockAccessTokenRepository is a test double for repository.AccessTokenRepository.
type MockAccessTokenRepository struct {
	Tokens map[string]*model.AccessToken // keyed by hash
}

func NewMockAccessTokenRepository() *MockAccessTokenRepository {
	return &MockAccessTokenRepository{Tokens: make(map[string]*model.AccessToken)}
}

func (m *MockAccessTokenRepository) FindByHash(hash string) (*model.AccessToken, error) {
	t, ok := m.Tokens[hash]
	if !ok {
		return nil, ErrNotFound
	}
	return t, nil
}

func (m *MockAccessTokenRepository) FindByID(id string) (*model.AccessToken, error) {
	for _, t := range m.Tokens {
		if t.ID == id {
			return t, nil
		}
	}
	return nil, ErrNotFound
}

func (m *MockAccessTokenRepository) ListByUserID(userID string) ([]*model.AccessToken, error) {
	out := make([]*model.AccessToken, 0)
	for _, t := range m.Tokens {
		if t.UserID == userID {
			out = append(out, t)
		}
	}
	return out, nil
}

func (m *MockAccessTokenRepository) DeleteByID(id string) error {
	for k, t := range m.Tokens {
		if t.ID == id {
			delete(m.Tokens, k)
			return nil
		}
	}
	return nil
}

// MockUserNotePiningRepository is a test double for repository.UserNotePiningRepository.
type MockUserNotePiningRepository struct {
	Pinings map[string]*model.UserNotePining // keyed by ID
}

func NewMockUserNotePiningRepository() *MockUserNotePiningRepository {
	return &MockUserNotePiningRepository{Pinings: make(map[string]*model.UserNotePining)}
}

func (m *MockUserNotePiningRepository) Create(p *model.UserNotePining) error {
	m.Pinings[p.ID] = p
	return nil
}

func (m *MockUserNotePiningRepository) Delete(p *model.UserNotePining) error {
	delete(m.Pinings, p.ID)
	return nil
}

func (m *MockUserNotePiningRepository) FindByPair(userID, noteID string) (*model.UserNotePining, error) {
	for _, p := range m.Pinings {
		if p.UserID == userID && p.NoteID == noteID {
			return p, nil
		}
	}
	return nil, ErrNotFound
}

func (m *MockUserNotePiningRepository) ListByUser(userID string) ([]*model.UserNotePining, error) {
	var rows []*model.UserNotePining
	for _, p := range m.Pinings {
		if p.UserID == userID {
			rows = append(rows, p)
		}
	}
	// 実装 (repository/user_note_pining.go) は ORDER BY id DESC を使っている。
	// map 反復は非決定的なので、同じ順序でソートして将来の順序依存テストに
	// 備える。
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID > rows[j].ID })
	return rows, nil
}

func (m *MockUserNotePiningRepository) CountByUser(userID string) (int, error) {
	count := 0
	for _, p := range m.Pinings {
		if p.UserID == userID {
			count++
		}
	}
	return count, nil
}

// MockPollRepository is a test double for repository.PollRepository.
type MockPollRepository struct {
	Polls map[string]*model.Poll
}

func NewMockPollRepository() *MockPollRepository {
	return &MockPollRepository{Polls: make(map[string]*model.Poll)}
}

func (m *MockPollRepository) Create(poll *model.Poll) error {
	m.Polls[poll.NoteID] = poll
	return nil
}

func (m *MockPollRepository) FindByNoteID(noteID string) (*model.Poll, error) {
	p, ok := m.Polls[noteID]
	if !ok {
		return nil, ErrNotFound
	}
	return p, nil
}

func (m *MockPollRepository) IncrementVote(noteID string, choice int, delta int) error {
	p, ok := m.Polls[noteID]
	if !ok {
		return ErrNotFound
	}
	if choice < 0 || choice >= len(p.Votes) {
		return nil
	}
	p.Votes[choice] += int64(delta)
	return nil
}

// MockPollVoteRepository is a test double for repository.PollVoteRepository.
type MockPollVoteRepository struct {
	Votes map[string]*model.PollVote // keyed by id
}

func NewMockPollVoteRepository() *MockPollVoteRepository {
	return &MockPollVoteRepository{Votes: make(map[string]*model.PollVote)}
}

func (m *MockPollVoteRepository) Create(v *model.PollVote) error {
	m.Votes[v.ID] = v
	return nil
}

func (m *MockPollVoteRepository) FindByUserAndChoice(userID, noteID string, choice int) (*model.PollVote, error) {
	for _, v := range m.Votes {
		if v.UserID == userID && v.NoteID == noteID && v.Choice == choice {
			return v, nil
		}
	}
	return nil, ErrNotFound
}

func (m *MockPollVoteRepository) CountByUserAndNote(userID, noteID string) (int64, error) {
	var n int64
	for _, v := range m.Votes {
		if v.UserID == userID && v.NoteID == noteID {
			n++
		}
	}
	return n, nil
}

func (m *MockPollVoteRepository) ListByNoteID(noteID string) ([]*model.PollVote, error) {
	var rows []*model.PollVote
	for _, v := range m.Votes {
		if v.NoteID == noteID {
			rows = append(rows, v)
		}
	}
	return rows, nil
}

// MockInstanceRepository is a test double for repository.InstanceRepository.
type MockInstanceRepository struct {
	Instances map[string]*model.Instance // keyed by host
	CreateErr error
	UpdateErr error
}

// NewMockInstanceRepository creates an empty MockInstanceRepository.
func NewMockInstanceRepository() *MockInstanceRepository {
	return &MockInstanceRepository{Instances: make(map[string]*model.Instance)}
}

func (m *MockInstanceRepository) Create(i *model.Instance) error {
	if m.CreateErr != nil {
		return m.CreateErr
	}
	m.Instances[i.Host] = i
	return nil
}

func (m *MockInstanceRepository) FindByHost(host string) (*model.Instance, error) {
	i, ok := m.Instances[host]
	if !ok {
		return nil, ErrNotFound
	}
	return i, nil
}

func (m *MockInstanceRepository) UpdateFields(host string, fields map[string]any) error {
	if m.UpdateErr != nil {
		return m.UpdateErr
	}
	if len(fields) == 0 {
		return nil
	}
	inst, ok := m.Instances[host]
	if !ok {
		return ErrNotFound
	}
	applyInstanceFields(inst, fields)
	return nil
}

func (m *MockInstanceRepository) IncrementCount(host, column string, delta int) error {
	inst, ok := m.Instances[host]
	if !ok {
		return ErrNotFound
	}
	switch column {
	case "usersCount":
		inst.UsersCount += delta
	case "notesCount":
		inst.NotesCount += delta
	case "followingCount":
		inst.FollowingCount += delta
	case "followersCount":
		inst.FollowersCount += delta
	}
	return nil
}

// List returns all stored instances filtered by the most common predicates.
// 並び順は host 昇順 (テストの安定性のため)。
func (m *MockInstanceRepository) List(filter model.InstanceListFilter) ([]*model.Instance, error) {
	var rows []*model.Instance
	for _, inst := range m.Instances {
		if filter.Host != "" && !strings.Contains(inst.Host, filter.Host) {
			continue
		}
		if filter.Suspended != nil {
			suspended := inst.SuspensionState != model.SuspensionStateNone
			if suspended != *filter.Suspended {
				continue
			}
		}
		if filter.NotResponding != nil && inst.IsNotResponding != *filter.NotResponding {
			continue
		}
		if filter.Federating != nil && *filter.Federating &&
			inst.FollowingCount == 0 && inst.FollowersCount == 0 {
			continue
		}
		if filter.Subscribing != nil && *filter.Subscribing && inst.FollowersCount == 0 {
			continue
		}
		if filter.Publishing != nil && *filter.Publishing && inst.FollowingCount == 0 {
			continue
		}
		rows = append(rows, inst)
	}
	for i := 0; i < len(rows); i++ {
		for j := i + 1; j < len(rows); j++ {
			if rows[i].Host > rows[j].Host {
				rows[i], rows[j] = rows[j], rows[i]
			}
		}
	}
	limit := filter.Limit
	if limit <= 0 || limit > 100 {
		limit = 30
	}
	if filter.Offset >= len(rows) {
		return nil, nil
	}
	end := filter.Offset + limit
	if end > len(rows) {
		end = len(rows)
	}
	return rows[filter.Offset:end], nil
}

func applyInstanceFields(i *model.Instance, fields map[string]any) {
	for k, v := range fields {
		switch k {
		case "name":
			if s, ok := v.(*string); ok {
				i.Name = s
			}
		case "description":
			if s, ok := v.(*string); ok {
				i.Description = s
			}
		case "softwareName":
			if s, ok := v.(*string); ok {
				i.SoftwareName = s
			}
		case "softwareVersion":
			if s, ok := v.(*string); ok {
				i.SoftwareVersion = s
			}
		case "iconUrl":
			if s, ok := v.(*string); ok {
				i.IconURL = s
			}
		case "faviconUrl":
			if s, ok := v.(*string); ok {
				i.FaviconURL = s
			}
		case "themeColor":
			if s, ok := v.(*string); ok {
				i.ThemeColor = s
			}
		case "openRegistrations":
			if b, ok := v.(*bool); ok {
				i.OpenRegistrations = b
			}
		case "infoUpdatedAt":
			if t, ok := v.(*time.Time); ok {
				i.InfoUpdatedAt = t
			}
		case "latestRequestReceivedAt":
			if t, ok := v.(*time.Time); ok {
				i.LatestRequestReceivedAt = t
			}
		case "isNotResponding":
			if b, ok := v.(bool); ok {
				i.IsNotResponding = b
			}
		case "notRespondingSince":
			if t, ok := v.(*time.Time); ok {
				i.NotRespondingSince = t
			}
		case "suspensionState":
			if s, ok := v.(model.SuspensionState); ok {
				i.SuspensionState = s
			}
		case "moderationNote":
			if s, ok := v.(string); ok {
				i.ModerationNote = s
			}
		}
	}
}

// MockClipRepository is a test double for repository.ClipRepository.
type MockClipRepository struct {
	Clips     map[string]*model.Clip
	CreateErr error
	UpdateErr error
}

// NewMockClipRepository creates an empty MockClipRepository.
func NewMockClipRepository() *MockClipRepository {
	return &MockClipRepository{Clips: make(map[string]*model.Clip)}
}

func (m *MockClipRepository) Create(c *model.Clip) error {
	if m.CreateErr != nil {
		return m.CreateErr
	}
	m.Clips[c.ID] = c
	return nil
}

func (m *MockClipRepository) FindByID(id string) (*model.Clip, error) {
	c, ok := m.Clips[id]
	if !ok {
		return nil, ErrNotFound
	}
	return c, nil
}

func (m *MockClipRepository) UpdateFields(clipID string, fields map[string]any) error {
	if m.UpdateErr != nil {
		return m.UpdateErr
	}
	if len(fields) == 0 {
		return nil
	}
	c, ok := m.Clips[clipID]
	if !ok {
		return ErrNotFound
	}
	applyClipFields(c, fields)
	return nil
}

func (m *MockClipRepository) Delete(c *model.Clip) error {
	delete(m.Clips, c.ID)
	return nil
}

func (m *MockClipRepository) ListByUser(userID string, limit, offset int) ([]*model.Clip, error) {
	var rows []*model.Clip
	for _, c := range m.Clips {
		if c.UserID == userID {
			rows = append(rows, c)
		}
	}
	for i := 0; i < len(rows); i++ {
		for j := i + 1; j < len(rows); j++ {
			if rows[i].ID < rows[j].ID {
				rows[i], rows[j] = rows[j], rows[i]
			}
		}
	}
	if limit <= 0 || limit > 100 {
		limit = 30
	}
	if offset >= len(rows) {
		return nil, nil
	}
	end := offset + limit
	if end > len(rows) {
		end = len(rows)
	}
	return rows[offset:end], nil
}

func (m *MockClipRepository) ListPublicByUser(userID string, limit, offset int) ([]*model.Clip, error) {
	var rows []*model.Clip
	for _, c := range m.Clips {
		if c.UserID == userID && c.IsPublic {
			rows = append(rows, c)
		}
	}
	for i := 0; i < len(rows); i++ {
		for j := i + 1; j < len(rows); j++ {
			if rows[i].ID < rows[j].ID {
				rows[i], rows[j] = rows[j], rows[i]
			}
		}
	}
	if limit <= 0 || limit > 100 {
		limit = 30
	}
	if offset >= len(rows) {
		return nil, nil
	}
	end := offset + limit
	if end > len(rows) {
		end = len(rows)
	}
	return rows[offset:end], nil
}

func (m *MockClipRepository) IncrementCount(clipID, column string, delta int) error {
	c, ok := m.Clips[clipID]
	if !ok {
		return ErrNotFound
	}
	if column == "notesCount" {
		c.NotesCount += delta
	}
	return nil
}

func applyClipFields(c *model.Clip, fields map[string]any) {
	for k, v := range fields {
		switch k {
		case "name":
			if s, ok := v.(string); ok {
				c.Name = s
			}
		case "description":
			if s, ok := v.(*string); ok {
				c.Description = s
			}
		case "isPublic":
			if b, ok := v.(bool); ok {
				c.IsPublic = b
			}
		case "lastClippedAt":
			if t, ok := v.(*time.Time); ok {
				c.LastClippedAt = t
			}
		}
	}
}

// MockClipNoteRepository is a test double for repository.ClipNoteRepository.
type MockClipNoteRepository struct {
	Entries map[string]*model.ClipNote
}

// NewMockClipNoteRepository creates an empty MockClipNoteRepository.
func NewMockClipNoteRepository() *MockClipNoteRepository {
	return &MockClipNoteRepository{Entries: make(map[string]*model.ClipNote)}
}

func (m *MockClipNoteRepository) Create(cn *model.ClipNote) error {
	m.Entries[cn.ID] = cn
	return nil
}

func (m *MockClipNoteRepository) Delete(cn *model.ClipNote) error {
	delete(m.Entries, cn.ID)
	return nil
}

func (m *MockClipNoteRepository) FindByPair(clipID, noteID string) (*model.ClipNote, error) {
	for _, cn := range m.Entries {
		if cn.ClipID == clipID && cn.NoteID == noteID {
			return cn, nil
		}
	}
	return nil, ErrNotFound
}

func (m *MockClipNoteRepository) ListByClip(clipID string, untilID, sinceID string, limit int) ([]*model.ClipNote, error) {
	var rows []*model.ClipNote
	for _, cn := range m.Entries {
		if cn.ClipID != clipID {
			continue
		}
		if untilID != "" && cn.ID >= untilID {
			continue
		}
		if sinceID != "" && cn.ID <= sinceID {
			continue
		}
		rows = append(rows, cn)
	}
	for i := 0; i < len(rows); i++ {
		for j := i + 1; j < len(rows); j++ {
			if rows[i].ID < rows[j].ID {
				rows[i], rows[j] = rows[j], rows[i]
			}
		}
	}
	if limit <= 0 || limit > 100 {
		limit = 30
	}
	if len(rows) > limit {
		rows = rows[:limit]
	}
	return rows, nil
}

// MockPageRepository is a test double for repository.PageRepository.
type MockPageRepository struct {
	Pages     map[string]*model.Page
	CreateErr error
	UpdateErr error
}

// NewMockPageRepository creates an empty MockPageRepository.
func NewMockPageRepository() *MockPageRepository {
	return &MockPageRepository{Pages: make(map[string]*model.Page)}
}

func (m *MockPageRepository) Create(p *model.Page) error {
	if m.CreateErr != nil {
		return m.CreateErr
	}
	m.Pages[p.ID] = p
	return nil
}

func (m *MockPageRepository) FindByID(id string) (*model.Page, error) {
	p, ok := m.Pages[id]
	if !ok {
		return nil, ErrNotFound
	}
	return p, nil
}

func (m *MockPageRepository) FindByUserAndName(userID, name string) (*model.Page, error) {
	for _, p := range m.Pages {
		if p.UserID == userID && p.Name == name {
			return p, nil
		}
	}
	return nil, ErrNotFound
}

func (m *MockPageRepository) UpdateFields(pageID string, fields map[string]any) error {
	if m.UpdateErr != nil {
		return m.UpdateErr
	}
	if len(fields) == 0 {
		return nil
	}
	p, ok := m.Pages[pageID]
	if !ok {
		return ErrNotFound
	}
	applyPageFields(p, fields)
	return nil
}

func (m *MockPageRepository) Delete(p *model.Page) error {
	delete(m.Pages, p.ID)
	return nil
}

func (m *MockPageRepository) ListByUser(userID string, limit, offset int) ([]*model.Page, error) {
	var rows []*model.Page
	for _, p := range m.Pages {
		if p.UserID == userID {
			rows = append(rows, p)
		}
	}
	// updatedAt 降順だが、テストの安定性のため updatedAt が同じ場合は ID 降順で解決する。
	for i := 0; i < len(rows); i++ {
		for j := i + 1; j < len(rows); j++ {
			if rows[i].UpdatedAt.Before(rows[j].UpdatedAt) ||
				(rows[i].UpdatedAt.Equal(rows[j].UpdatedAt) && rows[i].ID < rows[j].ID) {
				rows[i], rows[j] = rows[j], rows[i]
			}
		}
	}
	return paginatePages(rows, limit, offset), nil
}

func (m *MockPageRepository) ListPublicByUser(userID string, limit, offset int) ([]*model.Page, error) {
	var rows []*model.Page
	for _, p := range m.Pages {
		if p.UserID == userID && p.Visibility == model.PageVisibilityPublic {
			rows = append(rows, p)
		}
	}
	for i := 0; i < len(rows); i++ {
		for j := i + 1; j < len(rows); j++ {
			if rows[i].UpdatedAt.Before(rows[j].UpdatedAt) ||
				(rows[i].UpdatedAt.Equal(rows[j].UpdatedAt) && rows[i].ID < rows[j].ID) {
				rows[i], rows[j] = rows[j], rows[i]
			}
		}
	}
	return paginatePages(rows, limit, offset), nil
}

func (m *MockPageRepository) ListFeatured(limit, offset int) ([]*model.Page, error) {
	var rows []*model.Page
	for _, p := range m.Pages {
		if p.Visibility == model.PageVisibilityPublic {
			rows = append(rows, p)
		}
	}
	for i := 0; i < len(rows); i++ {
		for j := i + 1; j < len(rows); j++ {
			if rows[i].LikedCount < rows[j].LikedCount ||
				(rows[i].LikedCount == rows[j].LikedCount && rows[i].ID < rows[j].ID) {
				rows[i], rows[j] = rows[j], rows[i]
			}
		}
	}
	return paginatePages(rows, limit, offset), nil
}

func (m *MockPageRepository) IncrementCount(pageID, column string, delta int) error {
	p, ok := m.Pages[pageID]
	if !ok {
		return ErrNotFound
	}
	if column == "likedCount" {
		p.LikedCount += delta
	}
	return nil
}

func paginatePages(rows []*model.Page, limit, offset int) []*model.Page {
	if limit <= 0 || limit > 100 {
		limit = 30
	}
	if offset >= len(rows) {
		return nil
	}
	end := min(offset+limit, len(rows))
	return rows[offset:end]
}

func applyPageFields(p *model.Page, fields map[string]any) {
	for k, v := range fields {
		switch k {
		case "title":
			if s, ok := v.(string); ok {
				p.Title = s
			}
		case "name":
			if s, ok := v.(string); ok {
				p.Name = s
			}
		case "summary":
			if s, ok := v.(*string); ok {
				p.Summary = s
			}
		case "alignCenter":
			if b, ok := v.(bool); ok {
				p.AlignCenter = b
			}
		case "hideTitleWhenPinned":
			if b, ok := v.(bool); ok {
				p.HideTitleWhenPinned = b
			}
		case "font":
			if s, ok := v.(string); ok {
				p.Font = s
			}
		case "eyeCatchingImageId":
			if s, ok := v.(*string); ok {
				p.EyeCatchingImageID = s
			}
		case "content":
			if b, ok := v.([]byte); ok {
				p.Content = b
			}
		case "variables":
			if b, ok := v.([]byte); ok {
				p.Variables = b
			}
		case "script":
			if s, ok := v.(string); ok {
				p.Script = s
			}
		case "visibility":
			if vis, ok := v.(model.PageVisibility); ok {
				p.Visibility = vis
			}
		case "updatedAt":
			if t, ok := v.(time.Time); ok {
				p.UpdatedAt = t
			}
		}
	}
}

// MockFlashRepository is a test double for repository.FlashRepository.
type MockFlashRepository struct {
	Flashes   map[string]*model.Flash
	CreateErr error
	UpdateErr error
}

// NewMockFlashRepository creates an empty MockFlashRepository.
func NewMockFlashRepository() *MockFlashRepository {
	return &MockFlashRepository{Flashes: make(map[string]*model.Flash)}
}

func (m *MockFlashRepository) Create(f *model.Flash) error {
	if m.CreateErr != nil {
		return m.CreateErr
	}
	m.Flashes[f.ID] = f
	return nil
}

func (m *MockFlashRepository) FindByID(id string) (*model.Flash, error) {
	f, ok := m.Flashes[id]
	if !ok {
		return nil, ErrNotFound
	}
	return f, nil
}

func (m *MockFlashRepository) UpdateFields(flashID string, fields map[string]any) error {
	if m.UpdateErr != nil {
		return m.UpdateErr
	}
	if len(fields) == 0 {
		return nil
	}
	f, ok := m.Flashes[flashID]
	if !ok {
		return ErrNotFound
	}
	applyFlashFields(f, fields)
	return nil
}

func (m *MockFlashRepository) Delete(f *model.Flash) error {
	delete(m.Flashes, f.ID)
	return nil
}

func (m *MockFlashRepository) ListByUser(userID string, limit, offset int) ([]*model.Flash, error) {
	var rows []*model.Flash
	for _, f := range m.Flashes {
		if f.UserID == userID {
			rows = append(rows, f)
		}
	}
	sortFlashesByUpdatedDesc(rows)
	return paginateFlashes(rows, limit, offset), nil
}

func (m *MockFlashRepository) ListPublicByUser(userID string, limit, offset int) ([]*model.Flash, error) {
	var rows []*model.Flash
	for _, f := range m.Flashes {
		if f.UserID == userID && f.Visibility == "public" {
			rows = append(rows, f)
		}
	}
	sortFlashesByUpdatedDesc(rows)
	return paginateFlashes(rows, limit, offset), nil
}

func (m *MockFlashRepository) ListFeatured(limit, offset int) ([]*model.Flash, error) {
	var rows []*model.Flash
	for _, f := range m.Flashes {
		rows = append(rows, f)
	}
	for i := 0; i < len(rows); i++ {
		for j := i + 1; j < len(rows); j++ {
			if rows[i].LikedCount < rows[j].LikedCount ||
				(rows[i].LikedCount == rows[j].LikedCount && rows[i].ID < rows[j].ID) {
				rows[i], rows[j] = rows[j], rows[i]
			}
		}
	}
	return paginateFlashes(rows, limit, offset), nil
}

func (m *MockFlashRepository) Search(query string, limit, offset int) ([]*model.Flash, error) {
	var rows []*model.Flash
	q := strings.ToLower(query)
	for _, f := range m.Flashes {
		if strings.Contains(strings.ToLower(f.Title), q) ||
			strings.Contains(strings.ToLower(f.Summary), q) {
			rows = append(rows, f)
		}
	}
	sortFlashesByUpdatedDesc(rows)
	return paginateFlashes(rows, limit, offset), nil
}

func (m *MockFlashRepository) IncrementCount(flashID, column string, delta int) error {
	f, ok := m.Flashes[flashID]
	if !ok {
		return ErrNotFound
	}
	if column == "likedCount" {
		f.LikedCount += delta
	}
	return nil
}

func sortFlashesByUpdatedDesc(rows []*model.Flash) {
	for i := range rows {
		for j := i + 1; j < len(rows); j++ {
			if rows[i].UpdatedAt.Before(rows[j].UpdatedAt) ||
				(rows[i].UpdatedAt.Equal(rows[j].UpdatedAt) && rows[i].ID < rows[j].ID) {
				rows[i], rows[j] = rows[j], rows[i]
			}
		}
	}
}

func paginateFlashes(rows []*model.Flash, limit, offset int) []*model.Flash {
	if limit <= 0 || limit > 100 {
		limit = 30
	}
	if offset >= len(rows) {
		return nil
	}
	end := min(offset+limit, len(rows))
	return rows[offset:end]
}

func applyFlashFields(f *model.Flash, fields map[string]any) {
	for k, v := range fields {
		switch k {
		case "title":
			if s, ok := v.(string); ok {
				f.Title = s
			}
		case "summary":
			if s, ok := v.(string); ok {
				f.Summary = s
			}
		case "script":
			if s, ok := v.(string); ok {
				f.Script = s
			}
		case "permissions":
			if arr, ok := v.([]string); ok {
				f.Permissions = arr
			}
		case "visibility":
			if s, ok := v.(string); ok {
				f.Visibility = s
			}
		case "updatedAt":
			if t, ok := v.(time.Time); ok {
				f.UpdatedAt = t
			}
		}
	}
}

// MockFlashLikeRepository is a test double for repository.FlashLikeRepository.
type MockFlashLikeRepository struct {
	Likes map[string]*model.FlashLike
}

// NewMockFlashLikeRepository creates an empty MockFlashLikeRepository.
func NewMockFlashLikeRepository() *MockFlashLikeRepository {
	return &MockFlashLikeRepository{Likes: make(map[string]*model.FlashLike)}
}

func (m *MockFlashLikeRepository) Create(l *model.FlashLike) error {
	m.Likes[l.ID] = l
	return nil
}

func (m *MockFlashLikeRepository) Delete(l *model.FlashLike) error {
	delete(m.Likes, l.ID)
	return nil
}

func (m *MockFlashLikeRepository) FindByPair(userID, flashID string) (*model.FlashLike, error) {
	for _, l := range m.Likes {
		if l.UserID == userID && l.FlashID == flashID {
			return l, nil
		}
	}
	return nil, ErrNotFound
}

func (m *MockFlashLikeRepository) Exists(userID, flashID string) (bool, error) {
	_, err := m.FindByPair(userID, flashID)
	return err == nil, nil
}

func (m *MockFlashLikeRepository) ListByUser(userID string, limit, offset int) ([]*model.FlashLike, error) {
	var rows []*model.FlashLike
	for _, l := range m.Likes {
		if l.UserID == userID {
			rows = append(rows, l)
		}
	}
	for i := 0; i < len(rows); i++ {
		for j := i + 1; j < len(rows); j++ {
			if rows[i].ID < rows[j].ID {
				rows[i], rows[j] = rows[j], rows[i]
			}
		}
	}
	if limit <= 0 || limit > 100 {
		limit = 30
	}
	if offset >= len(rows) {
		return nil, nil
	}
	end := min(offset+limit, len(rows))
	return rows[offset:end], nil
}

// MockPageLikeRepository is a test double for repository.PageLikeRepository.
type MockPageLikeRepository struct {
	Likes map[string]*model.PageLike
}

// NewMockPageLikeRepository creates an empty MockPageLikeRepository.
func NewMockPageLikeRepository() *MockPageLikeRepository {
	return &MockPageLikeRepository{Likes: make(map[string]*model.PageLike)}
}

func (m *MockPageLikeRepository) Create(l *model.PageLike) error {
	m.Likes[l.ID] = l
	return nil
}

func (m *MockPageLikeRepository) Delete(l *model.PageLike) error {
	delete(m.Likes, l.ID)
	return nil
}

func (m *MockPageLikeRepository) FindByPair(userID, pageID string) (*model.PageLike, error) {
	for _, l := range m.Likes {
		if l.UserID == userID && l.PageID == pageID {
			return l, nil
		}
	}
	return nil, ErrNotFound
}

func (m *MockPageLikeRepository) Exists(userID, pageID string) (bool, error) {
	_, err := m.FindByPair(userID, pageID)
	return err == nil, nil
}

func (m *MockPageLikeRepository) ListByUser(userID string, limit, offset int) ([]*model.PageLike, error) {
	out := make([]*model.PageLike, 0)
	for _, l := range m.Likes {
		if l.UserID == userID {
			out = append(out, l)
		}
	}
	// tiny in-memory paging; sort not necessary for unit tests
	if offset >= len(out) {
		return nil, nil
	}
	end := offset + limit
	if limit <= 0 || end > len(out) {
		end = len(out)
	}
	return out[offset:end], nil
}

// MockAntennaRepository is a test double for repository.AntennaRepository.
type MockAntennaRepository struct {
	Antennas  map[string]*model.Antenna
	CreateErr error
	UpdateErr error
}

// NewMockAntennaRepository creates an empty MockAntennaRepository.
func NewMockAntennaRepository() *MockAntennaRepository {
	return &MockAntennaRepository{Antennas: make(map[string]*model.Antenna)}
}

func (m *MockAntennaRepository) Create(a *model.Antenna) error {
	if m.CreateErr != nil {
		return m.CreateErr
	}
	m.Antennas[a.ID] = a
	return nil
}

func (m *MockAntennaRepository) FindByID(id string) (*model.Antenna, error) {
	a, ok := m.Antennas[id]
	if !ok {
		return nil, ErrNotFound
	}
	return a, nil
}

func (m *MockAntennaRepository) UpdateFields(antennaID string, fields map[string]any) error {
	if m.UpdateErr != nil {
		return m.UpdateErr
	}
	if len(fields) == 0 {
		return nil
	}
	a, ok := m.Antennas[antennaID]
	if !ok {
		return ErrNotFound
	}
	applyAntennaFields(a, fields)
	return nil
}

func (m *MockAntennaRepository) Delete(a *model.Antenna) error {
	delete(m.Antennas, a.ID)
	return nil
}

func (m *MockAntennaRepository) ListByUser(userID string) ([]*model.Antenna, error) {
	var rows []*model.Antenna
	for _, a := range m.Antennas {
		if a.UserID == userID {
			rows = append(rows, a)
		}
	}
	for i := 0; i < len(rows); i++ {
		for j := i + 1; j < len(rows); j++ {
			if rows[i].ID < rows[j].ID {
				rows[i], rows[j] = rows[j], rows[i]
			}
		}
	}
	return rows, nil
}

func (m *MockAntennaRepository) ListAllActive() ([]*model.Antenna, error) {
	var rows []*model.Antenna
	for _, a := range m.Antennas {
		if a.IsActive {
			rows = append(rows, a)
		}
	}
	return rows, nil
}

func applyAntennaFields(a *model.Antenna, fields map[string]any) {
	for k, v := range fields {
		switch k {
		case "name":
			if s, ok := v.(string); ok {
				a.Name = s
			}
		case "src":
			if s, ok := v.(model.AntennaSource); ok {
				a.Src = s
			}
		case "users":
			if arr, ok := v.([]string); ok {
				a.Users = arr
			}
		case "keywords":
			if b, ok := v.([]byte); ok {
				a.Keywords = b
			}
		case "excludeKeywords":
			if b, ok := v.([]byte); ok {
				a.ExcludeKeywords = b
			}
		case "caseSensitive":
			if b, ok := v.(bool); ok {
				a.CaseSensitive = b
			}
		case "excludeBots":
			if b, ok := v.(bool); ok {
				a.ExcludeBots = b
			}
		case "withReplies":
			if b, ok := v.(bool); ok {
				a.WithReplies = b
			}
		case "withFile":
			if b, ok := v.(bool); ok {
				a.WithFile = b
			}
		case "isActive":
			if b, ok := v.(bool); ok {
				a.IsActive = b
			}
		case "localOnly":
			if b, ok := v.(bool); ok {
				a.LocalOnly = b
			}
		case "lastUsedAt":
			if t, ok := v.(time.Time); ok {
				a.LastUsedAt = t
			}
		}
	}
}

// MockChannelRepository is a test double for repository.ChannelRepository.
type MockChannelRepository struct {
	Channels  map[string]*model.Channel
	CreateErr error
	UpdateErr error
}

// NewMockChannelRepository creates an empty MockChannelRepository.
func NewMockChannelRepository() *MockChannelRepository {
	return &MockChannelRepository{Channels: make(map[string]*model.Channel)}
}

func (m *MockChannelRepository) Create(c *model.Channel) error {
	if m.CreateErr != nil {
		return m.CreateErr
	}
	m.Channels[c.ID] = c
	return nil
}

func (m *MockChannelRepository) FindByID(id string) (*model.Channel, error) {
	c, ok := m.Channels[id]
	if !ok {
		return nil, ErrNotFound
	}
	return c, nil
}

func (m *MockChannelRepository) FindByIDs(ids []string) ([]*model.Channel, error) {
	var result []*model.Channel
	for _, id := range ids {
		if ch, ok := m.Channels[id]; ok {
			result = append(result, ch)
		}
	}
	return result, nil
}

func (m *MockChannelRepository) UpdateFields(channelID string, fields map[string]any) error {
	if m.UpdateErr != nil {
		return m.UpdateErr
	}
	if len(fields) == 0 {
		return nil
	}
	c, ok := m.Channels[channelID]
	if !ok {
		return ErrNotFound
	}
	applyChannelFields(c, fields)
	return nil
}

func (m *MockChannelRepository) IncrementCount(channelID, column string, delta int) error {
	c, ok := m.Channels[channelID]
	if !ok {
		return ErrNotFound
	}
	switch column {
	case "notesCount":
		c.NotesCount += delta
	case "usersCount":
		c.UsersCount += delta
	}
	return nil
}

// List returns channels matching the most common filter predicates. テストの
// 安定性のため id 昇順で返す。
func (m *MockChannelRepository) List(filter model.ChannelListFilter) ([]*model.Channel, error) {
	var rows []*model.Channel
	for _, c := range m.Channels {
		if filter.OwnerID != "" {
			if c.UserID == nil || *c.UserID != filter.OwnerID {
				continue
			}
		}
		if filter.Query != "" && !strings.Contains(c.Name, filter.Query) {
			continue
		}
		if filter.IsArchived != nil && c.IsArchived != *filter.IsArchived {
			continue
		}
		rows = append(rows, c)
	}
	for i := 0; i < len(rows); i++ {
		for j := i + 1; j < len(rows); j++ {
			if rows[i].ID > rows[j].ID {
				rows[i], rows[j] = rows[j], rows[i]
			}
		}
	}
	limit := filter.Limit
	if limit <= 0 || limit > 100 {
		limit = 30
	}
	if filter.Offset >= len(rows) {
		return nil, nil
	}
	end := filter.Offset + limit
	if end > len(rows) {
		end = len(rows)
	}
	return rows[filter.Offset:end], nil
}

func applyChannelFields(c *model.Channel, fields map[string]any) {
	for k, v := range fields {
		switch k {
		case "name":
			if s, ok := v.(string); ok {
				c.Name = s
			}
		case "description":
			if s, ok := v.(*string); ok {
				c.Description = s
			}
		case "color":
			if s, ok := v.(string); ok {
				c.Color = s
			}
		case "isArchived":
			if b, ok := v.(bool); ok {
				c.IsArchived = b
			}
		case "isSensitive":
			if b, ok := v.(bool); ok {
				c.IsSensitive = b
			}
		case "lastNotedAt":
			if t, ok := v.(*time.Time); ok {
				c.LastNotedAt = t
			}
		}
	}
}

// MockChannelFollowingRepository is a test double for the channel_following
// repository.
type MockChannelFollowingRepository struct {
	Followings map[string]*model.ChannelFollowing
}

// NewMockChannelFollowingRepository creates an empty MockChannelFollowingRepository.
func NewMockChannelFollowingRepository() *MockChannelFollowingRepository {
	return &MockChannelFollowingRepository{Followings: make(map[string]*model.ChannelFollowing)}
}

func (m *MockChannelFollowingRepository) Create(f *model.ChannelFollowing) error {
	m.Followings[f.ID] = f
	return nil
}

func (m *MockChannelFollowingRepository) Delete(f *model.ChannelFollowing) error {
	delete(m.Followings, f.ID)
	return nil
}

func (m *MockChannelFollowingRepository) FindByPair(followerID, channelID string) (*model.ChannelFollowing, error) {
	for _, f := range m.Followings {
		if f.FollowerID == followerID && f.FolloweeID == channelID {
			return f, nil
		}
	}
	return nil, ErrNotFound
}

func (m *MockChannelFollowingRepository) Exists(followerID, channelID string) (bool, error) {
	_, err := m.FindByPair(followerID, channelID)
	return err == nil, nil
}

func (m *MockChannelFollowingRepository) ListFollowed(userID string, limit, offset int) ([]*model.ChannelFollowing, error) {
	var rows []*model.ChannelFollowing
	for _, f := range m.Followings {
		if f.FollowerID == userID {
			rows = append(rows, f)
		}
	}
	for i := 0; i < len(rows); i++ {
		for j := i + 1; j < len(rows); j++ {
			if rows[i].ID < rows[j].ID {
				rows[i], rows[j] = rows[j], rows[i]
			}
		}
	}
	if limit <= 0 || limit > 100 {
		limit = 30
	}
	if offset >= len(rows) {
		return nil, nil
	}
	end := offset + limit
	if end > len(rows) {
		end = len(rows)
	}
	return rows[offset:end], nil
}

// ListFollowerIDs returns followerIds for a given channelID.
func (m *MockChannelFollowingRepository) ListFollowerIDs(channelID string, limit int) ([]string, error) {
	var ids []string
	for _, f := range m.Followings {
		if f.FolloweeID == channelID {
			ids = append(ids, f.FollowerID)
		}
	}
	if limit <= 0 {
		limit = 1000
	}
	if len(ids) > limit {
		ids = ids[:limit]
	}
	return ids, nil
}

// MockAntennaNoteUnreadRepository is a test double for
// repository.AntennaNoteUnreadRepository.
type MockAntennaNoteUnreadRepository struct {
	Rows []*model.AntennaNoteUnread
}

// NewMockAntennaNoteUnreadRepository returns an empty repository.
func NewMockAntennaNoteUnreadRepository() *MockAntennaNoteUnreadRepository {
	return &MockAntennaNoteUnreadRepository{}
}

// Create records the unread row.
func (m *MockAntennaNoteUnreadRepository) Create(row *model.AntennaNoteUnread) error {
	m.Rows = append(m.Rows, row)
	return nil
}

// HasAnyByUser returns whether any row exists for the userID.
func (m *MockAntennaNoteUnreadRepository) HasAnyByUser(userID string) (bool, error) {
	for _, r := range m.Rows {
		if r.UserID == userID {
			return true, nil
		}
	}
	return false, nil
}

// DeleteByAntennaUser removes all rows for the (user, antenna) pair.
func (m *MockAntennaNoteUnreadRepository) DeleteByAntennaUser(userID, antennaID string) error {
	out := m.Rows[:0]
	for _, r := range m.Rows {
		if r.UserID == userID && r.AntennaID == antennaID {
			continue
		}
		out = append(out, r)
	}
	m.Rows = out
	return nil
}

// MockChannelNoteUnreadRepository is a test double for
// repository.ChannelNoteUnreadRepository.
type MockChannelNoteUnreadRepository struct {
	Rows []*model.ChannelNoteUnread
}

// NewMockChannelNoteUnreadRepository returns an empty repository.
func NewMockChannelNoteUnreadRepository() *MockChannelNoteUnreadRepository {
	return &MockChannelNoteUnreadRepository{}
}

// Create records the unread row.
func (m *MockChannelNoteUnreadRepository) Create(row *model.ChannelNoteUnread) error {
	m.Rows = append(m.Rows, row)
	return nil
}

// HasAnyByUser returns whether any row exists for the userID.
func (m *MockChannelNoteUnreadRepository) HasAnyByUser(userID string) (bool, error) {
	for _, r := range m.Rows {
		if r.UserID == userID {
			return true, nil
		}
	}
	return false, nil
}

// DeleteByChannelUser removes all rows for the (user, channel) pair.
func (m *MockChannelNoteUnreadRepository) DeleteByChannelUser(userID, channelID string) error {
	out := m.Rows[:0]
	for _, r := range m.Rows {
		if r.UserID == userID && r.ChannelID == channelID {
			continue
		}
		out = append(out, r)
	}
	m.Rows = out
	return nil
}

// MockUserKeypairRepository is a test double for repository.UserKeypairRepository.
type MockUserKeypairRepository struct {
	Keypairs map[string]*model.UserKeypair // keyed by userID
}

func NewMockUserKeypairRepository() *MockUserKeypairRepository {
	return &MockUserKeypairRepository{Keypairs: make(map[string]*model.UserKeypair)}
}

func (m *MockUserKeypairRepository) Create(k *model.UserKeypair) error {
	m.Keypairs[k.UserID] = k
	return nil
}

func (m *MockUserKeypairRepository) FindByUserID(userID string) (*model.UserKeypair, error) {
	k, ok := m.Keypairs[userID]
	if !ok {
		return nil, ErrNotFound
	}
	return k, nil
}

// MockFollowingRepository is a test double for repository.FollowingRepository.
type MockFollowingRepository struct {
	Followings map[string]*model.Following // keyed by ID
	// RemoteInboxes stores per-followee inbox lists used by
	// ListRemoteFollowerInboxes. テスト側で明示的に登録する。
	RemoteInboxes map[string][]string
	// Birthdays maps followeeID -> "YYYY-MM-DD" string used by
	// ListFollowingByBirthday. 未登録のユーザーは誕生日なしとして扱う。
	Birthdays map[string]string
}

func NewMockFollowingRepository() *MockFollowingRepository {
	return &MockFollowingRepository{
		Followings:    make(map[string]*model.Following),
		RemoteInboxes: make(map[string][]string),
		Birthdays:     make(map[string]string),
	}
}

// ListRemoteFollowerInboxes returns the inbox URLs registered for the given
// followee. テストでは MockFollowingRepository.RemoteInboxes を直接埋めて使う。
func (m *MockFollowingRepository) DeleteAllByUser(userID string) (int64, error) {
	n := int64(0)
	for k, f := range m.Followings {
		if f.FollowerID == userID || f.FolloweeID == userID {
			delete(m.Followings, k)
			n++
		}
	}
	return n, nil
}

func (m *MockFollowingRepository) ListRemoteFollowerInboxes(userID string) ([]string, error) {
	return m.RemoteInboxes[userID], nil
}

func (m *MockFollowingRepository) Create(f *model.Following) error {
	m.Followings[f.ID] = f
	return nil
}

func (m *MockFollowingRepository) Delete(f *model.Following) error {
	delete(m.Followings, f.ID)
	return nil
}

func (m *MockFollowingRepository) FindByPair(followerID, followeeID string) (*model.Following, error) {
	for _, f := range m.Followings {
		if f.FollowerID == followerID && f.FolloweeID == followeeID {
			return f, nil
		}
	}
	return nil, ErrNotFound
}

func (m *MockFollowingRepository) Exists(followerID, followeeID string) (bool, error) {
	_, err := m.FindByPair(followerID, followeeID)
	if err != nil {
		return false, nil
	}
	return true, nil
}

func (m *MockFollowingRepository) ListFollowers(userID string, limit, offset int) ([]*model.Following, error) {
	var rows []*model.Following
	for _, f := range m.Followings {
		if f.FolloweeID == userID {
			rows = append(rows, f)
		}
	}
	return paginate(rows, limit, offset), nil
}

func (m *MockFollowingRepository) ListFollowing(userID string, limit, offset int) ([]*model.Following, error) {
	var rows []*model.Following
	for _, f := range m.Followings {
		if f.FollowerID == userID {
			rows = append(rows, f)
		}
	}
	return paginate(rows, limit, offset), nil
}

func (m *MockFollowingRepository) ListFollowersByHost(host string, limit, offset int) ([]*model.Following, error) {
	var rows []*model.Following
	for _, f := range m.Followings {
		if f.FollowerHost != nil && *f.FollowerHost == host {
			rows = append(rows, f)
		}
	}
	return paginate(rows, limit, offset), nil
}

func (m *MockFollowingRepository) ListFollowingByHost(host string, limit, offset int) ([]*model.Following, error) {
	var rows []*model.Following
	for _, f := range m.Followings {
		if f.FolloweeHost != nil && *f.FolloweeHost == host {
			rows = append(rows, f)
		}
	}
	return paginate(rows, limit, offset), nil
}

func (m *MockFollowingRepository) UpdateRelation(followerID, followeeID string, fields map[string]any) error {
	for _, f := range m.Followings {
		if f.FollowerID == followerID && f.FolloweeID == followeeID {
			applyFollowingFields(f, fields)
			return nil
		}
	}
	return nil
}

func (m *MockFollowingRepository) UpdateAllByFollower(followerID string, fields map[string]any) error {
	for _, f := range m.Followings {
		if f.FollowerID == followerID {
			applyFollowingFields(f, fields)
		}
	}
	return nil
}

func (m *MockFollowingRepository) ListFollowingByBirthday(followerID string, beginMMDD, endMMDD, limit, offset int) ([]model.FollowingBirthday, error) {
	if limit <= 0 {
		limit = 10
	}
	wrap := beginMMDD > endMMDD
	var out []model.FollowingBirthday
	for _, f := range m.Followings {
		if f.FollowerID != followerID {
			continue
		}
		bd := m.Birthdays[f.FolloweeID]
		if len(bd) != 10 {
			continue
		}
		// bd は "YYYY-MM-DD" 形式。mmdd を抽出して範囲判定する。
		mm := int(bd[5]-'0')*10 + int(bd[6]-'0')
		dd := int(bd[8]-'0')*10 + int(bd[9]-'0')
		mmdd := mm*100 + dd
		in := false
		if wrap {
			in = mmdd >= beginMMDD || mmdd <= endMMDD
		} else {
			in = mmdd >= beginMMDD && mmdd <= endMMDD
		}
		if !in {
			continue
		}
		out = append(out, model.FollowingBirthday{FolloweeID: f.FolloweeID, Birthday: bd})
	}
	sort.Slice(out, func(i, j int) bool {
		a := out[i].Birthday[5:7] + out[i].Birthday[8:10]
		b := out[j].Birthday[5:7] + out[j].Birthday[8:10]
		return a < b
	})
	if offset >= len(out) {
		return nil, nil
	}
	out = out[offset:]
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func applyFollowingFields(f *model.Following, fields map[string]any) {
	for k, v := range fields {
		switch k {
		case "notify":
			if s, ok := v.(string); ok {
				f.Notify = &s
			}
		case "withReplies":
			if b, ok := v.(bool); ok {
				f.WithReplies = b
			}
		}
	}
}

// MockFollowRequestRepository is a test double for repository.FollowRequestRepository.
type MockFollowRequestRepository struct {
	Requests map[string]*model.FollowRequest
}

func NewMockFollowRequestRepository() *MockFollowRequestRepository {
	return &MockFollowRequestRepository{Requests: make(map[string]*model.FollowRequest)}
}

func (m *MockFollowRequestRepository) Create(r *model.FollowRequest) error {
	m.Requests[r.ID] = r
	return nil
}

func (m *MockFollowRequestRepository) Delete(r *model.FollowRequest) error {
	delete(m.Requests, r.ID)
	return nil
}

func (m *MockFollowRequestRepository) FindByPair(followerID, followeeID string) (*model.FollowRequest, error) {
	for _, r := range m.Requests {
		if r.FollowerID == followerID && r.FolloweeID == followeeID {
			return r, nil
		}
	}
	return nil, ErrNotFound
}

func (m *MockFollowRequestRepository) Exists(followerID, followeeID string) (bool, error) {
	_, err := m.FindByPair(followerID, followeeID)
	if err != nil {
		return false, nil
	}
	return true, nil
}

func (m *MockFollowRequestRepository) ListReceived(userID string, limit, offset int) ([]*model.FollowRequest, error) {
	var rows []*model.FollowRequest
	for _, r := range m.Requests {
		if r.FolloweeID == userID {
			rows = append(rows, r)
		}
	}
	return paginateRequests(rows, limit, offset), nil
}

func (m *MockFollowRequestRepository) ListSent(userID string, limit, offset int) ([]*model.FollowRequest, error) {
	var rows []*model.FollowRequest
	for _, r := range m.Requests {
		if r.FollowerID == userID {
			rows = append(rows, r)
		}
	}
	return paginateRequests(rows, limit, offset), nil
}

// CountReceived returns the number of pending requests addressed to userID.
func (m *MockFollowRequestRepository) CountReceived(userID string) (int64, error) {
	var n int64
	for _, r := range m.Requests {
		if r.FolloweeID == userID {
			n++
		}
	}
	return n, nil
}

func paginate(rows []*model.Following, limit, offset int) []*model.Following {
	if offset >= len(rows) {
		return nil
	}
	end := min(offset+limit, len(rows))
	return rows[offset:end]
}

func paginateRequests(rows []*model.FollowRequest, limit, offset int) []*model.FollowRequest {
	if offset >= len(rows) {
		return nil
	}
	end := min(offset+limit, len(rows))
	return rows[offset:end]
}

// ---------------------------------------------------------------------------
// MockUserListRepository
// ---------------------------------------------------------------------------

type MockUserListRepository struct {
	Lists   map[string]*model.UserList
	Members []*model.UserListMembership
}

func NewMockUserListRepository() *MockUserListRepository {
	return &MockUserListRepository{Lists: make(map[string]*model.UserList)}
}

func (m *MockUserListRepository) Create(list *model.UserList) error {
	m.Lists[list.ID] = list
	return nil
}

func (m *MockUserListRepository) FindByID(id string) (*model.UserList, error) {
	l, ok := m.Lists[id]
	if !ok {
		return nil, ErrNotFound
	}
	return l, nil
}

func (m *MockUserListRepository) ListByUser(userID string) ([]*model.UserList, error) {
	var result []*model.UserList
	for _, l := range m.Lists {
		if l.UserID == userID {
			result = append(result, l)
		}
	}
	return result, nil
}

func (m *MockUserListRepository) Delete(id string) error {
	delete(m.Lists, id)
	return nil
}

func (m *MockUserListRepository) AddMember(mem *model.UserListMembership) error {
	m.Members = append(m.Members, mem)
	return nil
}

func (m *MockUserListRepository) RemoveMember(listID, userID string) error {
	var filtered []*model.UserListMembership
	for _, mem := range m.Members {
		if !(mem.UserListID == listID && mem.UserID == userID) {
			filtered = append(filtered, mem)
		}
	}
	m.Members = filtered
	return nil
}

func (m *MockUserListRepository) ListMembers(listID string) ([]*model.UserListMembership, error) {
	var result []*model.UserListMembership
	for _, mem := range m.Members {
		if mem.UserListID == listID {
			result = append(result, mem)
		}
	}
	return result, nil
}

func (m *MockUserListRepository) UpdateList(id string, fields map[string]any) error {
	list, ok := m.Lists[id]
	if !ok {
		return ErrNotFound
	}
	if name, ok := fields["name"]; ok {
		list.Name = name.(string)
	}
	if isPublic, ok := fields["isPublic"]; ok {
		list.IsPublic = isPublic.(bool)
	}
	return nil
}

func (m *MockUserListRepository) UpdateMembership(listID, userID string, withReplies bool) error {
	for _, mem := range m.Members {
		if mem.UserListID == listID && mem.UserID == userID {
			mem.WithReplies = withReplies
			return nil
		}
	}
	return gorm.ErrRecordNotFound
}

func (m *MockUserListRepository) ListsContainingMember(ownerID, memberUserID string) ([]*model.UserList, error) {
	listIDs := make(map[string]struct{})
	for _, mem := range m.Members {
		if mem.UserID == memberUserID {
			listIDs[mem.UserListID] = struct{}{}
		}
	}
	out := make([]*model.UserList, 0)
	for _, list := range m.Lists {
		if list.UserID != ownerID {
			continue
		}
		if _, ok := listIDs[list.ID]; ok {
			out = append(out, list)
		}
	}
	// id DESC
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[i].ID < out[j].ID {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// MockAnnouncementRepository
// ---------------------------------------------------------------------------

type MockAnnouncementRepository struct {
	Items map[string]*model.Announcement
	Reads map[string]bool // keyed by "userId:announcementId"
}

func NewMockAnnouncementRepository() *MockAnnouncementRepository {
	return &MockAnnouncementRepository{
		Items: make(map[string]*model.Announcement),
		Reads: make(map[string]bool),
	}
}

func (m *MockAnnouncementRepository) Create(a *model.Announcement) error {
	m.Items[a.ID] = a
	return nil
}

func (m *MockAnnouncementRepository) FindByID(id string) (*model.Announcement, error) {
	a, ok := m.Items[id]
	if !ok {
		return nil, ErrNotFound
	}
	return a, nil
}

func (m *MockAnnouncementRepository) List(activeOnly bool, limit, offset int, sinceID, untilID string) ([]*model.Announcement, error) {
	var result []*model.Announcement
	for _, a := range m.Items {
		if activeOnly && !a.IsActive {
			continue
		}
		if sinceID != "" && a.ID <= sinceID {
			continue
		}
		if untilID != "" && a.ID >= untilID {
			continue
		}
		result = append(result, a)
	}
	if limit <= 0 {
		limit = 10
	}
	if sinceID == "" && untilID == "" {
		if offset >= len(result) {
			return nil, nil
		}
		end := min(offset+limit, len(result))
		return result[offset:end], nil
	}
	if limit > len(result) {
		limit = len(result)
	}
	return result[:limit], nil
}

func (m *MockAnnouncementRepository) ListGlobal(activeOnly bool, limit, offset int, sinceID, untilID string) ([]*model.Announcement, error) {
	var result []*model.Announcement
	for _, a := range m.Items {
		if a.UserID != nil {
			continue // per-user announcementは除外
		}
		if activeOnly && !a.IsActive {
			continue
		}
		if sinceID != "" && a.ID <= sinceID {
			continue
		}
		if untilID != "" && a.ID >= untilID {
			continue
		}
		result = append(result, a)
	}
	if limit <= 0 {
		limit = 10
	}
	if sinceID == "" && untilID == "" {
		if offset >= len(result) {
			return nil, nil
		}
		end := min(offset+limit, len(result))
		return result[offset:end], nil
	}
	if limit > len(result) {
		limit = len(result)
	}
	return result[:limit], nil
}

func (m *MockAnnouncementRepository) ListForUser(userID string, activeOnly bool, limit, offset int, sinceID, untilID string) ([]*model.Announcement, error) {
	var result []*model.Announcement
	for _, a := range m.Items {
		// per-user announcementのうち他ユーザー宛ては除外
		if a.UserID != nil && *a.UserID != userID {
			continue
		}
		if activeOnly && !a.IsActive {
			continue
		}
		if sinceID != "" && a.ID <= sinceID {
			continue
		}
		if untilID != "" && a.ID >= untilID {
			continue
		}
		result = append(result, a)
	}
	if limit <= 0 {
		limit = 10
	}
	if sinceID == "" && untilID == "" {
		if offset >= len(result) {
			return nil, nil
		}
		end := min(offset+limit, len(result))
		return result[offset:end], nil
	}
	if limit > len(result) {
		limit = len(result)
	}
	return result[:limit], nil
}

func (m *MockAnnouncementRepository) UpdateFields(id string, fields map[string]any) error {
	a, ok := m.Items[id]
	if !ok {
		return ErrNotFound
	}
	if v, ok := fields["title"]; ok {
		a.Title = v.(string)
	}
	if v, ok := fields["isActive"]; ok {
		a.IsActive = v.(bool)
	}
	return nil
}

func (m *MockAnnouncementRepository) Delete(id string) error {
	delete(m.Items, id)
	return nil
}

func (m *MockAnnouncementRepository) MarkRead(read *model.AnnouncementRead) error {
	m.Reads[read.UserID+":"+read.AnnouncementID] = true
	return nil
}

func (m *MockAnnouncementRepository) IsRead(userID, announcementID string) (bool, error) {
	return m.Reads[userID+":"+announcementID], nil
}

func (m *MockAnnouncementRepository) UnreadForUser(userID string) ([]*model.Announcement, error) {
	var result []*model.Announcement
	for _, a := range m.Items {
		// per-user announcementのうち他ユーザー宛ては除外 (読めないので未読扱いしない)
		if a.UserID != nil && *a.UserID != userID {
			continue
		}
		if a.IsActive && !m.Reads[userID+":"+a.ID] {
			result = append(result, a)
		}
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// MockRegistryRepository
// ---------------------------------------------------------------------------

// MockRegistryRepository is a test double for repository.RegistryRepository.
type MockRegistryRepository struct {
	Items map[string]*model.RegistryItem // keyed by "userId:key:scope"
}

func NewMockRegistryRepository() *MockRegistryRepository {
	return &MockRegistryRepository{Items: make(map[string]*model.RegistryItem)}
}

func (m *MockRegistryRepository) rkey(userID, key string, scope []string) string {
	return userID + ":" + key + ":" + strings.Join(scope, ",")
}

func (m *MockRegistryRepository) Get(userID, key string, scope []string, _ *string) (*model.RegistryItem, error) {
	item, ok := m.Items[m.rkey(userID, key, scope)]
	if !ok {
		return nil, ErrNotFound
	}
	return item, nil
}

func (m *MockRegistryRepository) Set(item *model.RegistryItem) error {
	m.Items[m.rkey(item.UserID, item.Key, item.Scope)] = item
	return nil
}

func (m *MockRegistryRepository) GetAll(userID string, scope []string, _ *string) ([]*model.RegistryItem, error) {
	var result []*model.RegistryItem
	prefix := userID + ":"
	for k, item := range m.Items {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			result = append(result, item)
		}
	}
	return result, nil
}

func (m *MockRegistryRepository) KeysWithType(userID string, scope []string, domain *string) (map[string]string, error) {
	items, _ := m.GetAll(userID, scope, domain)
	result := make(map[string]string, len(items))
	for _, item := range items {
		result[item.Key] = "object"
	}
	return result, nil
}

func (m *MockRegistryRepository) Remove(userID, key string, scope []string, _ *string) error {
	delete(m.Items, m.rkey(userID, key, scope))
	return nil
}

func (m *MockRegistryRepository) ScopesWithDomain(userID string) ([]model.RegistryScopeDomain, error) {
	seen := map[string]bool{}
	out := make([]model.RegistryScopeDomain, 0)
	for _, item := range m.Items {
		if item.UserID != userID {
			continue
		}
		d := ""
		if item.Domain != nil {
			d = *item.Domain
		}
		k := strings.Join(item.Scope, ",") + "|" + d
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, model.RegistryScopeDomain{Scope: []string(item.Scope), Domain: item.Domain})
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// MockAbuseReportRepository
// ---------------------------------------------------------------------------

type MockAbuseReportRepository struct {
	Reports map[string]*model.AbuseUserReport
}

func NewMockAbuseReportRepository() *MockAbuseReportRepository {
	return &MockAbuseReportRepository{Reports: make(map[string]*model.AbuseUserReport)}
}

func (m *MockAbuseReportRepository) Create(r *model.AbuseUserReport) error {
	m.Reports[r.ID] = r
	return nil
}

func (m *MockAbuseReportRepository) FindByID(id string) (*model.AbuseUserReport, error) {
	r, ok := m.Reports[id]
	if !ok {
		return nil, ErrNotFound
	}
	return r, nil
}

func (m *MockAbuseReportRepository) List(resolved *bool, limit, offset int) ([]*model.AbuseUserReport, error) {
	var result []*model.AbuseUserReport
	for _, r := range m.Reports {
		if resolved != nil && r.Resolved != *resolved {
			continue
		}
		result = append(result, r)
	}
	if limit <= 0 {
		limit = 10
	}
	if offset >= len(result) {
		return nil, nil
	}
	end := min(offset+limit, len(result))
	return result[offset:end], nil
}

func (m *MockAbuseReportRepository) UpdateFields(id string, fields map[string]any) error {
	r, ok := m.Reports[id]
	if !ok {
		return ErrNotFound
	}
	if v, ok := fields["resolved"]; ok {
		r.Resolved = v.(bool)
	}
	if v, ok := fields["resolvedAs"]; ok {
		s := v.(string)
		r.ResolvedAs = &s
	}
	return nil
}

// ---------------------------------------------------------------------------
// MockModerationLogRepository
// ---------------------------------------------------------------------------

type MockModerationLogRepository struct {
	Logs []*model.ModerationLog
}

func NewMockModerationLogRepository() *MockModerationLogRepository {
	return &MockModerationLogRepository{}
}

func (m *MockModerationLogRepository) Create(log *model.ModerationLog) error {
	m.Logs = append(m.Logs, log)
	return nil
}

func (m *MockModerationLogRepository) List(limit, offset int) ([]*model.ModerationLog, error) {
	if limit <= 0 {
		limit = 10
	}
	if offset >= len(m.Logs) {
		return nil, nil
	}
	end := min(offset+limit, len(m.Logs))
	return m.Logs[offset:end], nil
}

// ---------------------------------------------------------------------------
// MockRoleRepository
// ---------------------------------------------------------------------------

// MockRoleRepository is a test double for repository.RoleRepository.
type MockRoleRepository struct {
	Roles map[string]*model.Role
}

func NewMockRoleRepository() *MockRoleRepository {
	return &MockRoleRepository{Roles: make(map[string]*model.Role)}
}

func (m *MockRoleRepository) Create(role *model.Role) error {
	m.Roles[role.ID] = role
	return nil
}

func (m *MockRoleRepository) FindByID(id string) (*model.Role, error) {
	r, ok := m.Roles[id]
	if !ok {
		return nil, ErrNotFound
	}
	return r, nil
}

func (m *MockRoleRepository) List() ([]*model.Role, error) {
	result := make([]*model.Role, 0, len(m.Roles))
	for _, r := range m.Roles {
		result = append(result, r)
	}
	return result, nil
}

func (m *MockRoleRepository) UpdateFields(id string, fields map[string]any) error {
	r, ok := m.Roles[id]
	if !ok {
		return ErrNotFound
	}
	if v, ok := fields["name"]; ok {
		r.Name = v.(string)
	}
	if v, ok := fields["description"]; ok {
		r.Description = v.(string)
	}
	if v, ok := fields["isModerator"]; ok {
		r.IsModerator = v.(bool)
	}
	if v, ok := fields["isAdministrator"]; ok {
		r.IsAdministrator = v.(bool)
	}
	if v, ok := fields["isPublic"]; ok {
		r.IsPublic = v.(bool)
	}
	return nil
}

func (m *MockRoleRepository) Delete(id string) error {
	delete(m.Roles, id)
	return nil
}

// ---------------------------------------------------------------------------
// MockRoleAssignmentRepository
// ---------------------------------------------------------------------------

// MockRoleAssignmentRepository is a test double for repository.RoleAssignmentRepository.
type MockRoleAssignmentRepository struct {
	Assignments map[string]*model.RoleAssignment // keyed by "userId:roleId"
	RoleRepo    *MockRoleRepository
}

func NewMockRoleAssignmentRepository(roleRepo *MockRoleRepository) *MockRoleAssignmentRepository {
	return &MockRoleAssignmentRepository{
		Assignments: make(map[string]*model.RoleAssignment),
		RoleRepo:    roleRepo,
	}
}

func (m *MockRoleAssignmentRepository) key(userID, roleID string) string {
	return userID + ":" + roleID
}

func (m *MockRoleAssignmentRepository) Create(a *model.RoleAssignment) error {
	m.Assignments[m.key(a.UserID, a.RoleID)] = a
	return nil
}

func (m *MockRoleAssignmentRepository) Delete(userID, roleID string) error {
	delete(m.Assignments, m.key(userID, roleID))
	return nil
}

func (m *MockRoleAssignmentRepository) ListByUser(userID string) ([]*model.RoleAssignment, error) {
	var result []*model.RoleAssignment
	now := time.Now()
	for _, a := range m.Assignments {
		if a.UserID == userID && (a.ExpiresAt == nil || a.ExpiresAt.After(now)) {
			// ロール情報を付与
			if m.RoleRepo != nil {
				if r, ok := m.RoleRepo.Roles[a.RoleID]; ok {
					a.Role = r
				}
			}
			result = append(result, a)
		}
	}
	return result, nil
}

func (m *MockRoleAssignmentRepository) ListByRole(roleID string, limit, offset int) ([]*model.RoleAssignment, error) {
	var result []*model.RoleAssignment
	now := time.Now()
	for _, a := range m.Assignments {
		if a.RoleID == roleID && (a.ExpiresAt == nil || a.ExpiresAt.After(now)) {
			result = append(result, a)
		}
	}
	if offset >= len(result) {
		return nil, nil
	}
	end := min(offset+limit, len(result))
	if limit <= 0 {
		end = len(result)
	}
	return result[offset:end], nil
}

func (m *MockRoleAssignmentRepository) Exists(userID, roleID string) (bool, error) {
	a, ok := m.Assignments[m.key(userID, roleID)]
	if !ok {
		return false, nil
	}
	now := time.Now()
	if a.ExpiresAt != nil && a.ExpiresAt.Before(now) {
		return false, nil
	}
	return true, nil
}

// MockUserMemoRepository is a test double for repository.UserMemoRepository.
type MockUserMemoRepository struct {
	// keyed by "userID:targetUserID"
	Memos map[string]*model.UserMemo
}

// NewMockUserMemoRepository creates an empty MockUserMemoRepository.
func NewMockUserMemoRepository() *MockUserMemoRepository {
	return &MockUserMemoRepository{Memos: make(map[string]*model.UserMemo)}
}

func (m *MockUserMemoRepository) CreateOrUpdate(memo *model.UserMemo) error {
	m.Memos[memo.UserID+":"+memo.TargetUserID] = memo
	return nil
}

func (m *MockUserMemoRepository) FindByPair(userID, targetUserID string) (*model.UserMemo, error) {
	if memo, ok := m.Memos[userID+":"+targetUserID]; ok {
		return memo, nil
	}
	return nil, ErrNotFound
}

func (m *MockUserMemoRepository) Delete(userID, targetUserID string) error {
	delete(m.Memos, userID+":"+targetUserID)
	return nil
}

// MockPasswordResetRequestRepository is a test double for repository.PasswordResetRequestRepository.
type MockPasswordResetRequestRepository struct {
	Requests map[string]*model.PasswordResetRequest // keyed by ID
}

func NewMockPasswordResetRequestRepository() *MockPasswordResetRequestRepository {
	return &MockPasswordResetRequestRepository{Requests: make(map[string]*model.PasswordResetRequest)}
}

func (m *MockPasswordResetRequestRepository) Create(req *model.PasswordResetRequest) error {
	m.Requests[req.ID] = req
	return nil
}

func (m *MockPasswordResetRequestRepository) FindByToken(token string) (*model.PasswordResetRequest, error) {
	for _, r := range m.Requests {
		if r.Token == token {
			return r, nil
		}
	}
	return nil, ErrNotFound
}

func (m *MockPasswordResetRequestRepository) Delete(id string) error {
	delete(m.Requests, id)
	return nil
}

// MockSigninRepository is a test double for repository.SigninRepository.
type MockSigninRepository struct {
	mu      sync.Mutex
	Signins []*model.Signin
}

func NewMockSigninRepository() *MockSigninRepository {
	return &MockSigninRepository{}
}

func (m *MockSigninRepository) Create(s *model.Signin) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Signins = append(m.Signins, s)
	return nil
}

// Len returns the number of signin records (thread-safe).
func (m *MockSigninRepository) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.Signins)
}

func (m *MockSigninRepository) ListByUserID(userID string, limit int, untilID, sinceID string) ([]*model.Signin, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var rows []*model.Signin
	for _, s := range m.Signins {
		if s.UserID != userID {
			continue
		}
		if untilID != "" && s.ID >= untilID {
			continue
		}
		if sinceID != "" && s.ID <= sinceID {
			continue
		}
		rows = append(rows, s)
	}
	// 最新順（IDの降順）にするため逆順にする
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
	if len(rows) > limit {
		rows = rows[:limit]
	}
	return rows, nil
}

// MockAuthSessionRepository is a test double for repository.AuthSessionRepository.
type MockAuthSessionRepository struct {
	Apps         map[string]*model.App         // keyed by ID
	Sessions     map[string]*model.AuthSession // keyed by ID
	AccessTokens map[string]*model.AccessToken // keyed by ID
}

func NewMockAuthSessionRepository() *MockAuthSessionRepository {
	return &MockAuthSessionRepository{
		Apps:         make(map[string]*model.App),
		Sessions:     make(map[string]*model.AuthSession),
		AccessTokens: make(map[string]*model.AccessToken),
	}
}

func (m *MockAuthSessionRepository) FindAppBySecret(secret string) (*model.App, error) {
	for _, a := range m.Apps {
		if a.Secret == secret {
			return a, nil
		}
	}
	return nil, ErrNotFound
}

func (m *MockAuthSessionRepository) CreateApp(app *model.App) error {
	m.Apps[app.ID] = app
	return nil
}

func (m *MockAuthSessionRepository) CreateSession(session *model.AuthSession) error {
	m.Sessions[session.ID] = session
	return nil
}

func (m *MockAuthSessionRepository) FindSessionByToken(token string) (*model.AuthSession, error) {
	for _, s := range m.Sessions {
		if s.Token == token {
			if s.App == nil && s.AppID != "" {
				if a, ok := m.Apps[s.AppID]; ok {
					s.App = a
				}
			}
			return s, nil
		}
	}
	return nil, ErrNotFound
}

func (m *MockAuthSessionRepository) FindSessionByTokenAndAppID(token, appID string) (*model.AuthSession, error) {
	for _, s := range m.Sessions {
		if s.Token == token && s.AppID == appID {
			if s.App == nil {
				if a, ok := m.Apps[s.AppID]; ok {
					s.App = a
				}
			}
			return s, nil
		}
	}
	return nil, ErrNotFound
}

func (m *MockAuthSessionRepository) UpdateSessionUserID(sessionID, userID string) error {
	if s, ok := m.Sessions[sessionID]; ok {
		s.UserID = &userID
	}
	return nil
}

func (m *MockAuthSessionRepository) DeleteSession(sessionID string) error {
	delete(m.Sessions, sessionID)
	return nil
}

func (m *MockAuthSessionRepository) FindAccessTokenByAppAndUser(appID, userID string) (*model.AccessToken, error) {
	for _, t := range m.AccessTokens {
		if t.AppID != nil && *t.AppID == appID && t.UserID == userID {
			return t, nil
		}
	}
	return nil, ErrNotFound
}

func (m *MockAuthSessionRepository) CreateAccessToken(token *model.AccessToken) error {
	m.AccessTokens[token.ID] = token
	return nil
}

func (m *MockAuthSessionRepository) FindAppByID(id string) (*model.App, error) {
	a, ok := m.Apps[id]
	if !ok {
		return nil, ErrNotFound
	}
	return a, nil
}

func (m *MockAuthSessionRepository) ListAppsByUserID(userID string, limit, offset int) ([]*model.App, error) {
	var apps []*model.App
	for _, a := range m.Apps {
		if a.UserID != nil && *a.UserID == userID {
			apps = append(apps, a)
		}
	}
	if offset >= len(apps) {
		return []*model.App{}, nil
	}
	apps = apps[offset:]
	if len(apps) > limit {
		apps = apps[:limit]
	}
	return apps, nil
}

// MockUserPublickeyRepository is a test double for federation.PublickeyStore.
type MockUserPublickeyRepository struct {
	// keyed by userID
	Keys map[string]*model.UserPublickey
}

// NewMockUserPublickeyRepository creates an empty MockUserPublickeyRepository.
func NewMockUserPublickeyRepository() *MockUserPublickeyRepository {
	return &MockUserPublickeyRepository{Keys: make(map[string]*model.UserPublickey)}
}

func (m *MockUserPublickeyRepository) Upsert(pk *model.UserPublickey) error {
	m.Keys[pk.UserID] = pk
	return nil
}

func (m *MockUserPublickeyRepository) FindByUserID(userID string) (*model.UserPublickey, error) {
	if pk, ok := m.Keys[userID]; ok {
		return pk, nil
	}
	return nil, ErrNotFound
}

// MockChannelFavoriteRepository is a test double for repository.ChannelFavoriteRepository.
type MockChannelFavoriteRepository struct {
	Favorites map[string]*model.ChannelFavorite // keyed by "userId:channelId"
}

func NewMockChannelFavoriteRepository() *MockChannelFavoriteRepository {
	return &MockChannelFavoriteRepository{Favorites: make(map[string]*model.ChannelFavorite)}
}

func (m *MockChannelFavoriteRepository) Create(fav *model.ChannelFavorite) error {
	m.Favorites[fav.UserID+":"+fav.ChannelID] = fav
	return nil
}

func (m *MockChannelFavoriteRepository) Delete(userID, channelID string) error {
	delete(m.Favorites, userID+":"+channelID)
	return nil
}

func (m *MockChannelFavoriteRepository) ListByUser(userID string) ([]*model.ChannelFavorite, error) {
	var result []*model.ChannelFavorite
	for _, f := range m.Favorites {
		if f.UserID == userID {
			result = append(result, f)
		}
	}
	return result, nil
}

func (m *MockChannelFavoriteRepository) Exists(userID, channelID string) (bool, error) {
	_, ok := m.Favorites[userID+":"+channelID]
	return ok, nil
}

// MockChannelMutingRepository is a test double for repository.ChannelMutingRepository.
type MockChannelMutingRepository struct {
	Mutings map[string]*model.ChannelMuting // keyed by "userId:channelId"
}

func NewMockChannelMutingRepository() *MockChannelMutingRepository {
	return &MockChannelMutingRepository{Mutings: make(map[string]*model.ChannelMuting)}
}

func (m *MockChannelMutingRepository) Create(mut *model.ChannelMuting) error {
	m.Mutings[mut.UserID+":"+mut.ChannelID] = mut
	return nil
}

func (m *MockChannelMutingRepository) Delete(userID, channelID string) error {
	delete(m.Mutings, userID+":"+channelID)
	return nil
}

func (m *MockChannelMutingRepository) ListByUser(userID string) ([]*model.ChannelMuting, error) {
	var result []*model.ChannelMuting
	for _, mut := range m.Mutings {
		if mut.UserID == userID {
			result = append(result, mut)
		}
	}
	return result, nil
}

func (m *MockChannelMutingRepository) Exists(userID, channelID string) (bool, error) {
	_, ok := m.Mutings[userID+":"+channelID]
	return ok, nil
}

// MockClipFavoriteRepository is a test double for repository.ClipFavoriteRepository.
type MockClipFavoriteRepository struct {
	Favorites map[string]*model.ClipFavorite // keyed by "userId:clipId"
}

func NewMockClipFavoriteRepository() *MockClipFavoriteRepository {
	return &MockClipFavoriteRepository{Favorites: make(map[string]*model.ClipFavorite)}
}

func (m *MockClipFavoriteRepository) Create(fav *model.ClipFavorite) error {
	m.Favorites[fav.UserID+":"+fav.ClipID] = fav
	return nil
}

func (m *MockClipFavoriteRepository) Delete(userID, clipID string) error {
	delete(m.Favorites, userID+":"+clipID)
	return nil
}

func (m *MockClipFavoriteRepository) ListByUser(userID string) ([]*model.ClipFavorite, error) {
	var result []*model.ClipFavorite
	for _, f := range m.Favorites {
		if f.UserID == userID {
			result = append(result, f)
		}
	}
	return result, nil
}

func (m *MockClipFavoriteRepository) Exists(userID, clipID string) (bool, error) {
	_, ok := m.Favorites[userID+":"+clipID]
	return ok, nil
}

// MockUserListFavoriteRepository is a test double for repository.UserListFavoriteRepository.
type MockUserListFavoriteRepository struct {
	Favorites map[string]*model.UserListFavorite // keyed by "userId:userListId"
}

func NewMockUserListFavoriteRepository() *MockUserListFavoriteRepository {
	return &MockUserListFavoriteRepository{Favorites: make(map[string]*model.UserListFavorite)}
}

func (m *MockUserListFavoriteRepository) Create(fav *model.UserListFavorite) error {
	m.Favorites[fav.UserID+":"+fav.UserListID] = fav
	return nil
}

func (m *MockUserListFavoriteRepository) Delete(userID, listID string) error {
	delete(m.Favorites, userID+":"+listID)
	return nil
}

func (m *MockUserListFavoriteRepository) ListByUser(userID string) ([]*model.UserListFavorite, error) {
	var result []*model.UserListFavorite
	for _, f := range m.Favorites {
		if f.UserID == userID {
			result = append(result, f)
		}
	}
	return result, nil
}

func (m *MockUserListFavoriteRepository) Exists(userID, listID string) (bool, error) {
	_, ok := m.Favorites[userID+":"+listID]
	return ok, nil
}

// MockRetentionAggregationRepository is a test double for repository.RetentionAggregationRepository.
type MockRetentionAggregationRepository struct {
	Records []*model.RetentionAggregation
}

func NewMockRetentionAggregationRepository() *MockRetentionAggregationRepository {
	return &MockRetentionAggregationRepository{}
}

func (m *MockRetentionAggregationRepository) ListRecent(limit int) ([]*model.RetentionAggregation, error) {
	if limit >= len(m.Records) {
		return m.Records, nil
	}
	return m.Records[:limit], nil
}

// MockSystemAccountRepository is a test double for repository.SystemAccountRepository.
type MockSystemAccountRepository struct {
	Accounts map[string]*model.SystemAccount // keyed by type
}

func NewMockSystemAccountRepository() *MockSystemAccountRepository {
	return &MockSystemAccountRepository{Accounts: make(map[string]*model.SystemAccount)}
}

func (m *MockSystemAccountRepository) FindByType(typ string) (*model.SystemAccount, error) {
	if sa, ok := m.Accounts[typ]; ok {
		return sa, nil
	}
	return nil, ErrNotFound
}

func (m *MockSystemAccountRepository) Create(sa *model.SystemAccount) error {
	m.Accounts[sa.Type] = sa
	return nil
}

// MockNoteThreadMutingRepository is a test double for repository.NoteThreadMutingRepository.
type MockNoteThreadMutingRepository struct {
	Mutings map[string]*model.NoteThreadMuting // keyed by "userId:threadId"
}

func NewMockNoteThreadMutingRepository() *MockNoteThreadMutingRepository {
	return &MockNoteThreadMutingRepository{Mutings: make(map[string]*model.NoteThreadMuting)}
}

func (m *MockNoteThreadMutingRepository) Create(mut *model.NoteThreadMuting) error {
	m.Mutings[mut.UserID+":"+mut.ThreadID] = mut
	return nil
}

func (m *MockNoteThreadMutingRepository) Delete(userID, threadID string) error {
	delete(m.Mutings, userID+":"+threadID)
	return nil
}

func (m *MockNoteThreadMutingRepository) Exists(userID, threadID string) (bool, error) {
	_, ok := m.Mutings[userID+":"+threadID]
	return ok, nil
}

// MockUsedUsernameRepository is a test double for repository.UsedUsernameRepository.
type MockUsedUsernameRepository struct {
	Usernames map[string]bool
}

func NewMockUsedUsernameRepository() *MockUsedUsernameRepository {
	return &MockUsedUsernameRepository{Usernames: make(map[string]bool)}
}

func (m *MockUsedUsernameRepository) Create(username string) error {
	m.Usernames[username] = true
	return nil
}

func (m *MockUsedUsernameRepository) Exists(username string) (bool, error) {
	return m.Usernames[username], nil
}

// MockRelayRepository is a test double for repository.RelayRepository.
type MockRelayRepository struct {
	Relays map[string]*model.Relay
}

func NewMockRelayRepository() *MockRelayRepository {
	return &MockRelayRepository{Relays: make(map[string]*model.Relay)}
}

func (m *MockRelayRepository) Create(r *model.Relay) error {
	m.Relays[r.ID] = r
	return nil
}

func (m *MockRelayRepository) FindByID(id string) (*model.Relay, error) {
	if r, ok := m.Relays[id]; ok {
		return r, nil
	}
	return nil, ErrNotFound
}

func (m *MockRelayRepository) List() ([]*model.Relay, error) {
	out := make([]*model.Relay, 0, len(m.Relays))
	for _, r := range m.Relays {
		out = append(out, r)
	}
	return out, nil
}

func (m *MockRelayRepository) ListByStatus(status string) ([]*model.Relay, error) {
	out := make([]*model.Relay, 0)
	for _, r := range m.Relays {
		if r.Status == status {
			out = append(out, r)
		}
	}
	return out, nil
}

func (m *MockRelayRepository) UpdateStatus(id, status string) error {
	r, ok := m.Relays[id]
	if !ok {
		return ErrNotFound
	}
	r.Status = status
	return nil
}

func (m *MockRelayRepository) Delete(id string) error {
	delete(m.Relays, id)
	return nil
}

// MockSystemWebhookRepository is a test double for
// repository.SystemWebhookRepository.
type MockSystemWebhookRepository struct {
	Webhooks  map[string]*model.SystemWebhook
	CreateErr error
	UpdateErr error
}

// NewMockSystemWebhookRepository creates an empty mock.
func NewMockSystemWebhookRepository() *MockSystemWebhookRepository {
	return &MockSystemWebhookRepository{Webhooks: make(map[string]*model.SystemWebhook)}
}

func (m *MockSystemWebhookRepository) Create(w *model.SystemWebhook) error {
	if m.CreateErr != nil {
		return m.CreateErr
	}
	m.Webhooks[w.ID] = w
	return nil
}

func (m *MockSystemWebhookRepository) FindByID(id string) (*model.SystemWebhook, error) {
	w, ok := m.Webhooks[id]
	if !ok {
		return nil, ErrNotFound
	}
	return w, nil
}

func (m *MockSystemWebhookRepository) List() ([]*model.SystemWebhook, error) {
	rows := make([]*model.SystemWebhook, 0, len(m.Webhooks))
	for _, w := range m.Webhooks {
		rows = append(rows, w)
	}
	// id DESC で安定ソート (本実装と整合)
	for i := 0; i < len(rows); i++ {
		for j := i + 1; j < len(rows); j++ {
			if rows[i].ID < rows[j].ID {
				rows[i], rows[j] = rows[j], rows[i]
			}
		}
	}
	return rows, nil
}

func (m *MockSystemWebhookRepository) ListActive() ([]*model.SystemWebhook, error) {
	rows := make([]*model.SystemWebhook, 0)
	for _, w := range m.Webhooks {
		if w.IsActive {
			rows = append(rows, w)
		}
	}
	return rows, nil
}

func (m *MockSystemWebhookRepository) Update(w *model.SystemWebhook) error {
	if m.UpdateErr != nil {
		return m.UpdateErr
	}
	if _, ok := m.Webhooks[w.ID]; !ok {
		return ErrNotFound
	}
	m.Webhooks[w.ID] = w
	return nil
}

func (m *MockSystemWebhookRepository) UpdateAdminFields(id string, fields map[string]any) error {
	if m.UpdateErr != nil {
		return m.UpdateErr
	}
	if len(fields) == 0 {
		return nil
	}
	w, ok := m.Webhooks[id]
	if !ok {
		// GORM Updates(map) はレコード欠損で ErrRecordNotFound を返さず
		// 0 行影響で nil を返す。本実装と整合させるためここも nil を返す。
		return nil
	}
	for k, v := range fields {
		switch k {
		case "name":
			if s, ok := v.(string); ok {
				w.Name = s
			}
		case "url":
			if s, ok := v.(string); ok {
				w.URL = s
			}
		case "secret":
			if s, ok := v.(string); ok {
				w.Secret = s
			}
		case "on":
			if arr, ok := v.([]string); ok {
				w.On = arr
			}
		case "isActive":
			if b, ok := v.(bool); ok {
				w.IsActive = b
			}
		case "updatedAt":
			if ts, ok := v.(time.Time); ok {
				w.UpdatedAt = ts
			}
		}
	}
	return nil
}

func (m *MockSystemWebhookRepository) UpdateLatestStatus(id string, sentAt time.Time, status int) error {
	w, ok := m.Webhooks[id]
	if !ok {
		return ErrNotFound
	}
	w.LatestSentAt = &sentAt
	w.LatestStatus = &status
	return nil
}

func (m *MockSystemWebhookRepository) Delete(id string) error {
	delete(m.Webhooks, id)
	return nil
}

// MockAbuseReportNotificationRecipientRepository is a test double for
// repository.AbuseReportNotificationRecipientRepository.
type MockAbuseReportNotificationRecipientRepository struct {
	Recipients map[string]*model.AbuseReportNotificationRecipient
	CreateErr  error
	UpdateErr  error
}

// NewMockAbuseReportNotificationRecipientRepository creates an empty mock.
func NewMockAbuseReportNotificationRecipientRepository() *MockAbuseReportNotificationRecipientRepository {
	return &MockAbuseReportNotificationRecipientRepository{
		Recipients: make(map[string]*model.AbuseReportNotificationRecipient),
	}
}

func (m *MockAbuseReportNotificationRecipientRepository) Create(r *model.AbuseReportNotificationRecipient) error {
	if m.CreateErr != nil {
		return m.CreateErr
	}
	m.Recipients[r.ID] = r
	return nil
}

func (m *MockAbuseReportNotificationRecipientRepository) FindByID(id string) (*model.AbuseReportNotificationRecipient, error) {
	r, ok := m.Recipients[id]
	if !ok {
		return nil, ErrNotFound
	}
	return r, nil
}

func (m *MockAbuseReportNotificationRecipientRepository) List() ([]*model.AbuseReportNotificationRecipient, error) {
	rows := make([]*model.AbuseReportNotificationRecipient, 0, len(m.Recipients))
	for _, r := range m.Recipients {
		rows = append(rows, r)
	}
	for i := 0; i < len(rows); i++ {
		for j := i + 1; j < len(rows); j++ {
			if rows[i].ID < rows[j].ID {
				rows[i], rows[j] = rows[j], rows[i]
			}
		}
	}
	return rows, nil
}

func (m *MockAbuseReportNotificationRecipientRepository) Update(id string, fields map[string]any) error {
	if m.UpdateErr != nil {
		return m.UpdateErr
	}
	if len(fields) == 0 {
		return nil
	}
	r, ok := m.Recipients[id]
	if !ok {
		// GORM Updates(map) はレコード欠損で ErrRecordNotFound を返さず
		// 0 行影響で nil を返す。本実装と整合させるためここも nil を返す。
		return nil
	}
	for k, v := range fields {
		switch k {
		case "name":
			if s, ok := v.(string); ok {
				r.Name = s
			}
		case "method":
			if s, ok := v.(string); ok {
				r.Method = s
			}
		case "isActive":
			if b, ok := v.(bool); ok {
				r.IsActive = b
			}
		case "userId":
			if s, ok := v.(string); ok {
				r.UserID = &s
			} else if v == nil {
				r.UserID = nil
			}
		case "systemWebhookId":
			if s, ok := v.(string); ok {
				r.SystemWebhookID = &s
			} else if v == nil {
				r.SystemWebhookID = nil
			}
		}
	}
	return nil
}

func (m *MockAbuseReportNotificationRecipientRepository) Delete(id string) error {
	delete(m.Recipients, id)
	return nil
}

// MockAdRepository is a test double for repository.AdRepository.
type MockAdRepository struct {
	Ads       map[string]*model.Ad
	CreateErr error
	ListErr   error
}

// NewMockAdRepository creates an empty MockAdRepository.
func NewMockAdRepository() *MockAdRepository {
	return &MockAdRepository{Ads: make(map[string]*model.Ad)}
}

func (m *MockAdRepository) ListActive(now time.Time) ([]*model.Ad, error) {
	if m.ListErr != nil {
		return nil, m.ListErr
	}
	rows := make([]*model.Ad, 0)
	for _, a := range m.Ads {
		if !a.StartsAt.After(now) && a.ExpiresAt.After(now) {
			rows = append(rows, a)
		}
	}
	sortAdsByIDDesc(rows)
	return rows, nil
}

func (m *MockAdRepository) Create(a *model.Ad) error {
	if m.CreateErr != nil {
		return m.CreateErr
	}
	m.Ads[a.ID] = a
	return nil
}

func (m *MockAdRepository) FindByID(id string) (*model.Ad, error) {
	a, ok := m.Ads[id]
	if !ok {
		return nil, ErrNotFound
	}
	return a, nil
}

func (m *MockAdRepository) List(limit, offset int) ([]*model.Ad, error) {
	if m.ListErr != nil {
		return nil, m.ListErr
	}
	rows := make([]*model.Ad, 0, len(m.Ads))
	for _, a := range m.Ads {
		rows = append(rows, a)
	}
	sortAdsByIDDesc(rows)
	if limit <= 0 {
		limit = 30
	}
	if offset < 0 {
		offset = 0
	}
	if offset >= len(rows) {
		return []*model.Ad{}, nil
	}
	end := offset + limit
	if end > len(rows) {
		end = len(rows)
	}
	return rows[offset:end], nil
}

func (m *MockAdRepository) UpdateFields(id string, fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}
	a, ok := m.Ads[id]
	if !ok {
		return nil // GORM Updates(map) は行欠損でも nil を返す
	}
	for k, v := range fields {
		switch k {
		case "url":
			if s, ok := v.(string); ok {
				a.URL = s
			}
		case "imageUrl":
			if s, ok := v.(string); ok {
				a.ImageURL = s
			}
		case "memo":
			if s, ok := v.(string); ok {
				a.Memo = s
			}
		case "place":
			if s, ok := v.(string); ok {
				a.Place = s
			}
		case "priority":
			if s, ok := v.(string); ok {
				a.Priority = s
			}
		case "ratio":
			if n, ok := v.(int); ok {
				a.Ratio = n
			}
		case "dayOfWeek":
			if n, ok := v.(int); ok {
				a.DayOfWeek = n
			}
		case "isSensitive":
			if b, ok := v.(bool); ok {
				a.IsSensitive = b
			}
		case "startsAt":
			if ts, ok := v.(time.Time); ok {
				a.StartsAt = ts
			}
		case "expiresAt":
			if ts, ok := v.(time.Time); ok {
				a.ExpiresAt = ts
			}
		}
	}
	return nil
}

func (m *MockAdRepository) Delete(id string) error {
	delete(m.Ads, id)
	return nil
}

func sortAdsByIDDesc(rows []*model.Ad) {
	for i := 0; i < len(rows); i++ {
		for j := i + 1; j < len(rows); j++ {
			if rows[i].ID < rows[j].ID {
				rows[i], rows[j] = rows[j], rows[i]
			}
		}
	}
}

// MockAvatarDecorationRepository is a test double for
// repository.AvatarDecorationRepository.
type MockAvatarDecorationRepository struct {
	Decorations map[string]*model.AvatarDecoration
	CreateErr   error
}

// NewMockAvatarDecorationRepository creates an empty mock.
func NewMockAvatarDecorationRepository() *MockAvatarDecorationRepository {
	return &MockAvatarDecorationRepository{Decorations: make(map[string]*model.AvatarDecoration)}
}

func (m *MockAvatarDecorationRepository) Create(d *model.AvatarDecoration) error {
	if m.CreateErr != nil {
		return m.CreateErr
	}
	m.Decorations[d.ID] = d
	return nil
}

func (m *MockAvatarDecorationRepository) FindByID(id string) (*model.AvatarDecoration, error) {
	d, ok := m.Decorations[id]
	if !ok {
		return nil, ErrNotFound
	}
	return d, nil
}

func (m *MockAvatarDecorationRepository) List() ([]*model.AvatarDecoration, error) {
	rows := make([]*model.AvatarDecoration, 0, len(m.Decorations))
	for _, d := range m.Decorations {
		rows = append(rows, d)
	}
	for i := 0; i < len(rows); i++ {
		for j := i + 1; j < len(rows); j++ {
			if rows[i].ID < rows[j].ID {
				rows[i], rows[j] = rows[j], rows[i]
			}
		}
	}
	return rows, nil
}

func (m *MockAvatarDecorationRepository) UpdateFields(id string, fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}
	d, ok := m.Decorations[id]
	if !ok {
		return nil
	}
	for k, v := range fields {
		switch k {
		case "name":
			if s, ok := v.(string); ok {
				d.Name = s
			}
		case "description":
			if s, ok := v.(string); ok {
				d.Description = s
			}
		case "url":
			if s, ok := v.(string); ok {
				d.URL = s
			}
		case "roleIdsThatCanBeUsedThisDecoration":
			if arr, ok := v.([]string); ok {
				d.RoleIDs = arr
			}
		case "updatedAt":
			if ts, ok := v.(time.Time); ok {
				d.UpdatedAt = &ts
			}
		}
	}
	return nil
}

func (m *MockAvatarDecorationRepository) Delete(id string) error {
	delete(m.Decorations, id)
	return nil
}

// MockRegistrationTicketRepository is a test double for
// repository.RegistrationTicketRepository.
type MockRegistrationTicketRepository struct {
	Tickets   map[string]*model.RegistrationTicket
	CreateErr error
}

// NewMockRegistrationTicketRepository creates an empty mock.
func NewMockRegistrationTicketRepository() *MockRegistrationTicketRepository {
	return &MockRegistrationTicketRepository{Tickets: make(map[string]*model.RegistrationTicket)}
}

func (m *MockRegistrationTicketRepository) Create(t *model.RegistrationTicket) error {
	if m.CreateErr != nil {
		return m.CreateErr
	}
	m.Tickets[t.ID] = t
	return nil
}

func (m *MockRegistrationTicketRepository) List(filter string, limit, offset int, now time.Time) ([]*model.RegistrationTicket, error) {
	rows := make([]*model.RegistrationTicket, 0, len(m.Tickets))
	for _, t := range m.Tickets {
		switch filter {
		case "unused":
			if t.UsedByID != nil {
				continue
			}
		case "used":
			if t.UsedByID == nil {
				continue
			}
		case "expired":
			if t.ExpiresAt == nil || !t.ExpiresAt.Before(now) {
				continue
			}
		}
		rows = append(rows, t)
	}
	for i := 0; i < len(rows); i++ {
		for j := i + 1; j < len(rows); j++ {
			if rows[i].ID < rows[j].ID {
				rows[i], rows[j] = rows[j], rows[i]
			}
		}
	}
	if limit <= 0 {
		limit = 30
	}
	if offset < 0 {
		offset = 0
	}
	if offset >= len(rows) {
		return []*model.RegistrationTicket{}, nil
	}
	end := offset + limit
	if end > len(rows) {
		end = len(rows)
	}
	return rows[offset:end], nil
}

func (m *MockRegistrationTicketRepository) Delete(id string) error {
	delete(m.Tickets, id)
	return nil
}
