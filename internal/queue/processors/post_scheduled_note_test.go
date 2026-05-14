package processors

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/shiroha-a/mk/internal/core/note"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/queue"
	"github.com/shiroha-a/mk/internal/queue/driver"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubDraftRepo / stubUserRepo / stubPublisher are minimal in-memory doubles
// for PostScheduledNoteProcessor tests.

type stubDraftRepo struct {
	drafts map[string]*model.NoteDraft
	// deleted records the (id, userID) pairs that Delete saw.
	deleted []string
	findErr error
	delErr  error
}

func (s *stubDraftRepo) FindByID(id string) (*model.NoteDraft, error) {
	if s.findErr != nil {
		return nil, s.findErr
	}
	d, ok := s.drafts[id]
	if !ok {
		return nil, errors.New("not found")
	}
	return d, nil
}

func (s *stubDraftRepo) Delete(id, _ string) (int64, error) {
	if s.delErr != nil {
		return 0, s.delErr
	}
	if _, ok := s.drafts[id]; ok {
		delete(s.drafts, id)
		s.deleted = append(s.deleted, id)
		return 1, nil
	}
	return 0, nil
}

type stubUserRepo struct {
	users map[string]*model.User
	err   error
}

func (s *stubUserRepo) FindByID(id string) (*model.User, error) {
	if s.err != nil {
		return nil, s.err
	}
	u, ok := s.users[id]
	if !ok {
		return nil, errors.New("user not found")
	}
	return u, nil
}

type stubPublisher struct {
	calls []note.CreateInput
	err   error
}

func (s *stubPublisher) Create(in note.CreateInput) (*model.Note, error) {
	if s.err != nil {
		return nil, s.err
	}
	s.calls = append(s.calls, in)
	return &model.Note{ID: "n_published", UserID: in.User.ID}, nil
}

func newProcessor(drafts map[string]*model.NoteDraft, users map[string]*model.User) (*PostScheduledNoteProcessor, *stubDraftRepo, *stubPublisher) {
	dr := &stubDraftRepo{drafts: drafts}
	ur := &stubUserRepo{users: users}
	pub := &stubPublisher{}
	return NewPostScheduledNoteProcessor(dr, ur, pub), dr, pub
}

func taskFor(draftID string) driver.Task {
	return queue.NewPostScheduledNoteTask(queue.PostScheduledNotePayload{NoteDraftID: draftID})
}

func TestPostScheduledNote_HappyPath(t *testing.T) {
	now := int64(1234)
	scheduledAt := timeFromMs(now)
	drafts := map[string]*model.NoteDraft{
		"d1": {
			ID:                  "d1",
			UserID:              "u1",
			Visibility:          "public",
			IsActuallyScheduled: true,
			ScheduledAt:         &scheduledAt,
		},
	}
	users := map[string]*model.User{"u1": {ID: "u1", Username: "alice"}}
	p, draftRepo, pub := newProcessor(drafts, users)
	err := p.Handle(context.Background(), taskFor("d1"))
	require.NoError(t, err)
	require.Len(t, pub.calls, 1)
	assert.Equal(t, "u1", pub.calls[0].User.ID)
	// draft は publish 後に削除される
	assert.NotContains(t, draftRepo.drafts, "d1")
	assert.Contains(t, draftRepo.deleted, "d1")
}

// Draft が削除済み (= user 手動 cancel など) でも error 化せず silent success。
func TestPostScheduledNote_DraftMissing(t *testing.T) {
	p, _, pub := newProcessor(map[string]*model.NoteDraft{}, map[string]*model.User{})
	err := p.Handle(context.Background(), taskFor("ghost"))
	require.NoError(t, err)
	assert.Empty(t, pub.calls, "draft が無いなら publish しない")
}

// isActuallyScheduled=false な draft (= 後で unschedule された) は publish せず
// silent skip (draft も残す)。
func TestPostScheduledNote_Unscheduled(t *testing.T) {
	drafts := map[string]*model.NoteDraft{
		"d1": {ID: "d1", UserID: "u1", IsActuallyScheduled: false},
	}
	p, draftRepo, pub := newProcessor(drafts, map[string]*model.User{"u1": {ID: "u1"}})
	err := p.Handle(context.Background(), taskFor("d1"))
	require.NoError(t, err)
	assert.Empty(t, pub.calls)
	assert.Contains(t, draftRepo.drafts, "d1", "unscheduled draft は残す")
}

// publish 失敗時は error を返して asynq retry に任せる。draft は残る。
func TestPostScheduledNote_PublishErrorRetries(t *testing.T) {
	scheduledAt := timeFromMs(1234)
	drafts := map[string]*model.NoteDraft{
		"d1": {
			ID: "d1", UserID: "u1", Visibility: "public",
			IsActuallyScheduled: true, ScheduledAt: &scheduledAt,
		},
	}
	users := map[string]*model.User{"u1": {ID: "u1"}}
	p, draftRepo, pub := newProcessor(drafts, users)
	pub.err = errors.New("publish boom")
	err := p.Handle(context.Background(), taskFor("d1"))
	require.Error(t, err)
	assert.Contains(t, draftRepo.drafts, "d1", "publish 失敗時は draft 残す")
}

// payload decode 失敗は error 返却 (= asynq に retry / dead letter 判断させる)。
func TestPostScheduledNote_PayloadDecodeError(t *testing.T) {
	p, _, _ := newProcessor(map[string]*model.NoteDraft{}, map[string]*model.User{})
	bad := driver.RawTask{TypeName: queue.TaskTypePostScheduledNote, Body: []byte("{not json")}
	err := p.Handle(context.Background(), bad)
	require.Error(t, err)
}

// timeFromMs is a tiny helper to convert ms epoch to time.Time.
func timeFromMs(ms int64) time.Time {
	return time.UnixMilli(ms)
}
