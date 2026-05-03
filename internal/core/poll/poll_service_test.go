package poll_test

import (
	"errors"
	"testing"
	"time"

	"github.com/lib/pq"
	"github.com/shiroha-a/mk/internal/core/poll"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var stubError = errors.New("stub error")

func newSvc(t *testing.T) (*poll.Service, *testutil.MockNoteRepository, *testutil.MockPollRepository, *testutil.MockPollVoteRepository) {
	t.Helper()
	noteRepo := testutil.NewMockNoteRepository()
	pollRepo := testutil.NewMockPollRepository()
	voteRepo := testutil.NewMockPollVoteRepository()
	idGen, _ := id.NewGenerator("aidx")
	svc := poll.NewService(noteRepo, pollRepo, voteRepo, nil, idGen)
	return svc, noteRepo, pollRepo, voteRepo
}

func seedPollNote(noteRepo *testutil.MockNoteRepository, pollRepo *testutil.MockPollRepository, noteID, userID string, multiple bool, expiresAt *time.Time) {
	noteRepo.Notes[noteID] = &model.Note{
		ID: noteID, UserID: userID, Visibility: model.NoteVisibilityPublic, HasPoll: true,
	}
	pollRepo.Polls[noteID] = &model.Poll{
		NoteID:    noteID,
		Multiple:  multiple,
		Choices:   pq.StringArray{"A", "B", "C"},
		Votes:     pq.Int64Array{0, 0, 0},
		ExpiresAt: expiresAt,
	}
}

func TestVote_NilUser(t *testing.T) {
	svc, _, _, _ := newSvc(t)
	err := svc.Vote(nil, "n1", 0)
	require.Error(t, err)
}

func TestVote_NoteNotFound(t *testing.T) {
	svc, _, _, _ := newSvc(t)
	err := svc.Vote(&model.User{ID: "u"}, "ghost", 0)
	require.ErrorIs(t, err, poll.ErrNoteNotFound)
}

func TestVote_NotVisible(t *testing.T) {
	svc, noteRepo, _, _ := newSvc(t)
	noteRepo.Notes["n1"] = &model.Note{
		ID: "n1", UserID: "author", Visibility: model.NoteVisibilityFollowers, HasPoll: true,
	}
	err := svc.Vote(&model.User{ID: "viewer"}, "n1", 0)
	require.ErrorIs(t, err, poll.ErrNoteNotVisible)
}

func TestVote_NoPoll(t *testing.T) {
	svc, noteRepo, _, _ := newSvc(t)
	noteRepo.Notes["n1"] = &model.Note{
		ID: "n1", UserID: "author", Visibility: model.NoteVisibilityPublic, HasPoll: false,
	}
	err := svc.Vote(&model.User{ID: "viewer"}, "n1", 0)
	require.ErrorIs(t, err, poll.ErrNoPoll)
}

func TestVote_PollNotFoundForNote(t *testing.T) {
	svc, noteRepo, _, _ := newSvc(t)
	// HasPoll=true だが pollRepo にレコードが無い
	noteRepo.Notes["n1"] = &model.Note{
		ID: "n1", UserID: "author", Visibility: model.NoteVisibilityPublic, HasPoll: true,
	}
	err := svc.Vote(&model.User{ID: "viewer"}, "n1", 0)
	require.ErrorIs(t, err, poll.ErrNoPoll)
}

func TestVote_InvalidChoice(t *testing.T) {
	svc, noteRepo, pollRepo, _ := newSvc(t)
	seedPollNote(noteRepo, pollRepo, "n1", "author", false, nil)
	err := svc.Vote(&model.User{ID: "viewer"}, "n1", 99)
	require.ErrorIs(t, err, poll.ErrInvalidChoice)

	err = svc.Vote(&model.User{ID: "viewer"}, "n1", -1)
	require.ErrorIs(t, err, poll.ErrInvalidChoice)
}

func TestVote_Expired(t *testing.T) {
	svc, noteRepo, pollRepo, _ := newSvc(t)
	past := time.Now().Add(-1 * time.Hour)
	seedPollNote(noteRepo, pollRepo, "n1", "author", false, &past)

	err := svc.Vote(&model.User{ID: "viewer"}, "n1", 0)
	require.ErrorIs(t, err, poll.ErrPollExpired)
}

func TestVote_AlreadyVotedSameChoice(t *testing.T) {
	svc, noteRepo, pollRepo, _ := newSvc(t)
	seedPollNote(noteRepo, pollRepo, "n1", "author", true, nil)

	require.NoError(t, svc.Vote(&model.User{ID: "viewer"}, "n1", 0))
	err := svc.Vote(&model.User{ID: "viewer"}, "n1", 0)
	require.ErrorIs(t, err, poll.ErrAlreadyVoted)
}

func TestVote_SingleAlreadyVoted(t *testing.T) {
	svc, noteRepo, pollRepo, _ := newSvc(t)
	seedPollNote(noteRepo, pollRepo, "n1", "author", false, nil)

	require.NoError(t, svc.Vote(&model.User{ID: "viewer"}, "n1", 0))
	err := svc.Vote(&model.User{ID: "viewer"}, "n1", 1)
	require.ErrorIs(t, err, poll.ErrAlreadyVoted)
}

func TestVote_MultipleAllowed(t *testing.T) {
	svc, noteRepo, pollRepo, _ := newSvc(t)
	seedPollNote(noteRepo, pollRepo, "n1", "author", true, nil)

	require.NoError(t, svc.Vote(&model.User{ID: "viewer"}, "n1", 0))
	require.NoError(t, svc.Vote(&model.User{ID: "viewer"}, "n1", 1))
	assert.Equal(t, int64(1), pollRepo.Polls["n1"].Votes[0])
	assert.Equal(t, int64(1), pollRepo.Polls["n1"].Votes[1])
}

// failingPollVoteRepo lets us trigger CountByUserAndNote / Create errors.
type failingPollVoteRepo struct {
	*testutil.MockPollVoteRepository
	failCount  bool
	failCreate bool
}

func (f *failingPollVoteRepo) CountByUserAndNote(userID, noteID string) (int64, error) {
	if f.failCount {
		return 0, stubError
	}
	return f.MockPollVoteRepository.CountByUserAndNote(userID, noteID)
}

func (f *failingPollVoteRepo) Create(v *model.PollVote) error {
	if f.failCreate {
		return stubError
	}
	return f.MockPollVoteRepository.Create(v)
}

func TestVote_CountError(t *testing.T) {
	noteRepo := testutil.NewMockNoteRepository()
	pollRepo := testutil.NewMockPollRepository()
	seedPollNote(noteRepo, pollRepo, "n1", "author", false, nil)
	idGen, _ := id.NewGenerator("aidx")
	voteRepo := &failingPollVoteRepo{MockPollVoteRepository: testutil.NewMockPollVoteRepository(), failCount: true}
	svc := poll.NewService(noteRepo, pollRepo, voteRepo, nil, idGen)
	err := svc.Vote(&model.User{ID: "viewer"}, "n1", 0)
	assert.ErrorIs(t, err, stubError)
}

func TestVote_CreateError(t *testing.T) {
	noteRepo := testutil.NewMockNoteRepository()
	pollRepo := testutil.NewMockPollRepository()
	seedPollNote(noteRepo, pollRepo, "n1", "author", true, nil)
	idGen, _ := id.NewGenerator("aidx")
	voteRepo := &failingPollVoteRepo{MockPollVoteRepository: testutil.NewMockPollVoteRepository(), failCreate: true}
	svc := poll.NewService(noteRepo, pollRepo, voteRepo, nil, idGen)
	err := svc.Vote(&model.User{ID: "viewer"}, "n1", 0)
	assert.ErrorIs(t, err, stubError)
}

// recordingHook captures vote notification.
type recordingHook struct {
	called   bool
	notifiee string
	notifier string
	noteID   string
	choice   int
}

func (h *recordingHook) OnPollVote(notifieeID, notifierID, noteID string, choice int) {
	h.called = true
	h.notifiee = notifieeID
	h.notifier = notifierID
	h.noteID = noteID
	h.choice = choice
}

// TestVote_DoesNotInvokeNotificationHook: Misskey TS には per-vote の
// notification 種別が無く (frontend が unknown type で空 body の通知を出して
// しまう) ため mk-go でも hook を呼ばない契約 (#690)。NotificationHook 自体は
// 別 path (例: pollEnded) で再利用される可能性があるので interface は残す。
func TestVote_DoesNotInvokeNotificationHook(t *testing.T) {
	svc, noteRepo, pollRepo, _ := newSvc(t)
	seedPollNote(noteRepo, pollRepo, "n1", "author", false, nil)
	hook := &recordingHook{}
	svc.SetNotificationHook(hook)

	require.NoError(t, svc.Vote(&model.User{ID: "viewer"}, "n1", 0))
	assert.False(t, hook.called, "vote must not trigger pollVote notification (Misskey TS has no such type)")
}

func TestVote_HappyPathIncrementsVotes(t *testing.T) {
	svc, noteRepo, pollRepo, _ := newSvc(t)
	seedPollNote(noteRepo, pollRepo, "n1", "author", false, nil)

	require.NoError(t, svc.Vote(&model.User{ID: "viewer"}, "n1", 1))
	assert.Equal(t, int64(1), pollRepo.Polls["n1"].Votes[1])
}
