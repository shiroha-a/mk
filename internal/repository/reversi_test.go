package repository

import (
	"testing"

	"github.com/lib/pq"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
)

func TestReversiRepository_DeleteOutdatedGames(t *testing.T) {
	repo := NewReversiRepository(testDB)
	u1 := insertTestUser(t, "u_revd_1", "reversidel1")
	defer cleanupUser(t, u1.ID)
	u2 := insertTestUser(t, "u_revd_2", "reversidel2")
	defer cleanupUser(t, u2.ID)

	mk := func(id string, started bool) *model.ReversiGame {
		return &model.ReversiGame{
			ID: id, User1ID: u1.ID, User2ID: u2.ID, IsStarted: started,
			Map: pq.StringArray{"--------"}, BW: "random", TimeLimitForEachTurn: 90, Logs: datatypes.JSON("[]"),
		}
	}
	// id は時刻順ソート可能 (aidx)。"a..." < "z..." を閾値として使う。
	require.NoError(t, repo.Create(mk("aaa_old_unstarted", false))) // outdated (id < threshold, 未開始)
	require.NoError(t, repo.Create(mk("aaa_old_started", true)))    // 開始済みは消さない
	require.NoError(t, repo.Create(mk("zzz_new_unstarted", false))) // id >= threshold は消さない
	defer testDB.Exec(`DELETE FROM "reversi_game" WHERE id IN (?,?,?)`, "aaa_old_unstarted", "aaa_old_started", "zzz_new_unstarted")

	n, err := repo.DeleteOutdatedGames("bbb_threshold")
	require.NoError(t, err)
	assert.EqualValues(t, 1, n, "id < threshold かつ未開始のみ削除")
	_, err = repo.FindByID("aaa_old_unstarted")
	assert.Error(t, err, "outdated は削除済み")
	_, err = repo.FindByID("aaa_old_started")
	assert.NoError(t, err, "開始済みは残る")
	_, err = repo.FindByID("zzz_new_unstarted")
	assert.NoError(t, err, "新しい game は残る")
}

func TestReversiRepository_CRUD(t *testing.T) {
	repo := NewReversiRepository(testDB)
	u1 := insertTestUser(t, "u_rev_1", "reversiuser1")
	defer cleanupUser(t, u1.ID)
	u2 := insertTestUser(t, "u_rev_2", "reversiuser2")
	defer cleanupUser(t, u2.ID)

	game := &model.ReversiGame{
		ID:                   "rev_g1",
		User1ID:              u1.ID,
		User2ID:              u2.ID,
		Map:                  pq.StringArray{"--------", "--------", "--------", "---wb---", "---bw---", "--------", "--------", "--------"},
		BW:                   "random",
		TimeLimitForEachTurn: 90,
		Logs:                 datatypes.JSON("[]"),
	}
	require.NoError(t, repo.Create(game))
	defer testDB.Exec(`DELETE FROM "reversi_game" WHERE id = ?`, game.ID)

	// FindByID
	got, err := repo.FindByID(game.ID)
	require.NoError(t, err)
	assert.Equal(t, u1.ID, got.User1ID)

	// FindByID not found
	_, err = repo.FindByID("ghost")
	assert.Error(t, err)

	// Update + ListByUser
	got.BW = "1"
	require.NoError(t, repo.Update(got))
	list, err := repo.ListByUser(u1.ID, 10)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(list), 1)

	// ListActive
	active, err := repo.ListActive()
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(active), 1)

	// Delete
	require.NoError(t, repo.Delete(game.ID))
	_, err = repo.FindByID(game.ID)
	assert.Error(t, err)
}
