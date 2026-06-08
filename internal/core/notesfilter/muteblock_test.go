package notesfilter_test

import (
	"errors"
	"testing"

	"github.com/shiroha-a/mk/internal/core/notesfilter"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func strp(s string) *string { return &s }

// --- LoadMuteBlockSets ------------------------------------------------------

func TestLoadMuteBlockSets_NilViewer(t *testing.T) {
	sets, err := notesfilter.LoadMuteBlockSets(nil,
		testutil.NewMockMutingRepository(),
		testutil.NewMockBlockingRepository(),
		testutil.NewMockChannelMutingRepository())
	require.NoError(t, err)
	assert.Empty(t, sets.MutedUserIDs)
	assert.Empty(t, sets.BlockerIDs)
	assert.Empty(t, sets.MutedChannelIDs)
}

func TestLoadMuteBlockSets_NilRepos(t *testing.T) {
	// 未配線リポジトリは該当 dimension を no-op として扱う。
	sets, err := notesfilter.LoadMuteBlockSets(&model.User{ID: "viewer"}, nil, nil, nil)
	require.NoError(t, err)
	assert.Nil(t, sets.MutedUserIDs)
	assert.Nil(t, sets.BlockerIDs)
	assert.Nil(t, sets.MutedChannelIDs)
}

func TestLoadMuteBlockSets_PopulatesAllSets(t *testing.T) {
	muting := testutil.NewMockMutingRepository()
	require.NoError(t, muting.Create(&model.Muting{ID: "m1", MuterID: "viewer", MuteeID: "muted"}))
	blocking := testutil.NewMockBlockingRepository()
	require.NoError(t, blocking.Create(&model.Blocking{ID: "b1", BlockerID: "blocker", BlockeeID: "viewer"}))
	channelMuting := testutil.NewMockChannelMutingRepository()
	require.NoError(t, channelMuting.Create(&model.ChannelMuting{UserID: "viewer", ChannelID: "ch1"}))

	sets, err := notesfilter.LoadMuteBlockSets(&model.User{ID: "viewer"}, muting, blocking, channelMuting)
	require.NoError(t, err)
	assert.Contains(t, sets.MutedUserIDs, "muted")
	assert.Contains(t, sets.BlockerIDs, "blocker")
	assert.Contains(t, sets.MutedChannelIDs, "ch1")
}

func TestLoadMuteBlockSets_NoChannelMutesYieldsNilSet(t *testing.T) {
	sets, err := notesfilter.LoadMuteBlockSets(&model.User{ID: "viewer"},
		testutil.NewMockMutingRepository(),
		testutil.NewMockBlockingRepository(),
		testutil.NewMockChannelMutingRepository())
	require.NoError(t, err)
	assert.Nil(t, sets.MutedChannelIDs, "no channel mutes leaves the set nil so Apply skips it")
}

// failing repos to exercise fail-closed error propagation.

type failingMutingRepo struct{ repository.MutingRepository }

func (failingMutingRepo) ListMuteeIDs(string) ([]string, error) {
	return nil, errors.New("muting boom")
}

type failingChannelMutingRepo struct {
	repository.ChannelMutingRepository
}

func (failingChannelMutingRepo) ListByUser(string) ([]*model.ChannelMuting, error) {
	return nil, errors.New("channel boom")
}

func TestLoadMuteBlockSets_MutingError(t *testing.T) {
	_, err := notesfilter.LoadMuteBlockSets(&model.User{ID: "viewer"},
		failingMutingRepo{testutil.NewMockMutingRepository()},
		testutil.NewMockBlockingRepository(),
		testutil.NewMockChannelMutingRepository())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "load muted users")
}

func TestLoadMuteBlockSets_BlockingError(t *testing.T) {
	blocking := testutil.NewMockBlockingRepository()
	blocking.ExistsErr = errors.New("block boom") // ListBlockerIDs もこのエラーで失敗する
	_, err := notesfilter.LoadMuteBlockSets(&model.User{ID: "viewer"},
		testutil.NewMockMutingRepository(), blocking, testutil.NewMockChannelMutingRepository())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "load blockers")
}

func TestLoadMuteBlockSets_ChannelMutingError(t *testing.T) {
	_, err := notesfilter.LoadMuteBlockSets(&model.User{ID: "viewer"},
		testutil.NewMockMutingRepository(),
		testutil.NewMockBlockingRepository(),
		failingChannelMutingRepo{testutil.NewMockChannelMutingRepository()})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "load muted channels")
}

// --- ApplyMuteBlockChannel --------------------------------------------------

func TestApplyMuteBlockChannel_EmptyNotes(t *testing.T) {
	out := notesfilter.ApplyMuteBlockChannel(nil, notesfilter.MuteBlockSets{
		MutedUserIDs: map[string]struct{}{"x": {}},
	})
	assert.Empty(t, out)
}

func TestApplyMuteBlockChannel_AllEmptySetsIsNoOp(t *testing.T) {
	notes := []*model.Note{{ID: "n1", UserID: "muted"}}
	out := notesfilter.ApplyMuteBlockChannel(notes, notesfilter.MuteBlockSets{})
	assert.Len(t, out, 1)
}

func TestApplyMuteBlockChannel_MutedAuthorReplyRenote(t *testing.T) {
	sets := notesfilter.MuteBlockSets{MutedUserIDs: map[string]struct{}{"muted": {}}}
	notes := []*model.Note{
		{ID: "keep", UserID: "author"},
		{ID: "by-muted", UserID: "muted"},
		{ID: "reply-to-muted", UserID: "author", ReplyUserID: strp("muted")},
		{ID: "renote-of-muted", UserID: "author", RenoteUserID: strp("muted")},
	}
	out := notesfilter.ApplyMuteBlockChannel(notes, sets)
	require.Len(t, out, 1)
	assert.Equal(t, "keep", out[0].ID)
}

func TestApplyMuteBlockChannel_BlockerAuthorReplyRenote(t *testing.T) {
	sets := notesfilter.MuteBlockSets{BlockerIDs: map[string]struct{}{"blocker": {}}}
	notes := []*model.Note{
		{ID: "keep", UserID: "author"},
		{ID: "by-blocker", UserID: "blocker"},
		{ID: "reply-to-blocker", UserID: "author", ReplyUserID: strp("blocker")},
		{ID: "renote-of-blocker", UserID: "author", RenoteUserID: strp("blocker")},
	}
	out := notesfilter.ApplyMuteBlockChannel(notes, sets)
	require.Len(t, out, 1)
	assert.Equal(t, "keep", out[0].ID)
}

func TestApplyMuteBlockChannel_MutedChannelAndRenoteChannel(t *testing.T) {
	sets := notesfilter.MuteBlockSets{MutedChannelIDs: map[string]struct{}{"ch-muted": {}}}
	notes := []*model.Note{
		{ID: "no-channel", UserID: "author"},
		{ID: "other-channel", UserID: "author", ChannelID: strp("ch-ok")},
		{ID: "muted-channel", UserID: "author", ChannelID: strp("ch-muted")},
		{ID: "muted-renote-channel", UserID: "author", RenoteChannelID: strp("ch-muted")},
	}
	out := notesfilter.ApplyMuteBlockChannel(notes, sets)
	require.Len(t, out, 2)
	assert.Equal(t, "no-channel", out[0].ID)
	assert.Equal(t, "other-channel", out[1].ID)
}

func TestApplyMuteBlockChannel_SkipsNilEntries(t *testing.T) {
	sets := notesfilter.MuteBlockSets{MutedUserIDs: map[string]struct{}{"muted": {}}}
	notes := []*model.Note{nil, {ID: "keep", UserID: "author"}}
	out := notesfilter.ApplyMuteBlockChannel(notes, sets)
	require.Len(t, out, 1)
	assert.Equal(t, "keep", out[0].ID)
}
