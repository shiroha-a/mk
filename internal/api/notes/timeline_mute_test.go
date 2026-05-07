package notes

import (
	"testing"

	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadMutedChannelIDs(t *testing.T) {
	h, _ := newTestHandler(t)
	repo := testutil.NewMockChannelMutingRepository()
	require.NoError(t, repo.Create(&model.ChannelMuting{ID: "m1", UserID: "viewer", ChannelID: "ch-muted-1"}))
	require.NoError(t, repo.Create(&model.ChannelMuting{ID: "m2", UserID: "viewer", ChannelID: "ch-muted-2"}))
	require.NoError(t, repo.Create(&model.ChannelMuting{ID: "m3", UserID: "other", ChannelID: "ch-other"}))
	h.SetChannelMutingRepo(repo)

	viewer := &model.User{ID: "viewer"}
	ids := h.loadMutedChannelIDs(viewer)
	assert.ElementsMatch(t, []string{"ch-muted-1", "ch-muted-2"}, ids)
}

func TestLoadMutedChannelIDs_AnonymousReturnsNil(t *testing.T) {
	h, _ := newTestHandler(t)
	h.SetChannelMutingRepo(testutil.NewMockChannelMutingRepository())
	assert.Nil(t, h.loadMutedChannelIDs(nil))
}

func TestLoadMutedChannelIDs_RepoUnsetReturnsNil(t *testing.T) {
	h, _ := newTestHandler(t)
	assert.Nil(t, h.loadMutedChannelIDs(&model.User{ID: "viewer"}))
}

func TestLoadMutedChannelIDs_EmptyResultReturnsNil(t *testing.T) {
	h, _ := newTestHandler(t)
	repo := testutil.NewMockChannelMutingRepository()
	h.SetChannelMutingRepo(repo)
	assert.Nil(t, h.loadMutedChannelIDs(&model.User{ID: "viewer"}))
}

// loadRenoteMutedUserIDs は viewer の renote-mute mutee 一覧を返す (#903)。
// MutedChannelIDs と同 pattern で 4 case (基本 / anonymous / repo nil /
// 空結果) を guard する。internal/api/notes の coverage 90% threshold を
// 維持するために必須。
func TestLoadRenoteMutedUserIDs(t *testing.T) {
	h, _ := newTestHandler(t)
	repo := testutil.NewMockRenoteMutingRepository()
	require.NoError(t, repo.Create(&model.RenoteMuting{ID: "rm1", MuterID: "viewer", MuteeID: "muted-1"}))
	require.NoError(t, repo.Create(&model.RenoteMuting{ID: "rm2", MuterID: "viewer", MuteeID: "muted-2"}))
	require.NoError(t, repo.Create(&model.RenoteMuting{ID: "rm3", MuterID: "other", MuteeID: "other-mute"}))
	h.SetRenoteMutingRepo(repo)

	viewer := &model.User{ID: "viewer"}
	ids := h.loadRenoteMutedUserIDs(viewer)
	assert.ElementsMatch(t, []string{"muted-1", "muted-2"}, ids)
}

func TestLoadRenoteMutedUserIDs_AnonymousReturnsNil(t *testing.T) {
	h, _ := newTestHandler(t)
	h.SetRenoteMutingRepo(testutil.NewMockRenoteMutingRepository())
	assert.Nil(t, h.loadRenoteMutedUserIDs(nil))
}

func TestLoadRenoteMutedUserIDs_RepoUnsetReturnsNil(t *testing.T) {
	h, _ := newTestHandler(t)
	assert.Nil(t, h.loadRenoteMutedUserIDs(&model.User{ID: "viewer"}))
}

func TestLoadRenoteMutedUserIDs_EmptyResultReturnsNil(t *testing.T) {
	h, _ := newTestHandler(t)
	repo := testutil.NewMockRenoteMutingRepository()
	h.SetRenoteMutingRepo(repo)
	assert.Nil(t, h.loadRenoteMutedUserIDs(&model.User{ID: "viewer"}))
}
