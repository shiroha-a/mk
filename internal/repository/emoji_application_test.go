package repository

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/shiroha-a/mk/internal/model"
)

func cleanupEmojiApplications(t *testing.T) {
	t.Helper()
	testDB.Exec(`DELETE FROM "emoji_application"`)
}

func seedApplication(t *testing.T, id, userID, name, status string) *model.EmojiApplication {
	t.Helper()
	now := time.Now()
	app := &model.EmojiApplication{
		ID: id, UserID: userID, Kind: model.EmojiApplicationKindOwn,
		Status: status, Name: name, License: "自作",
		CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, NewEmojiApplicationRepository(testDB).Create(app))
	return app
}

// **同じ人が同じ名前で審査待ちを積み増せないこと。** 部分一意索引が効いているか
// を実 DB で見る。番兵に落とさないと、利用者には「サーバーエラー」としか見えない。
func TestEmojiApplicationRepository_DuplicatePending(t *testing.T) {
	cleanupEmojiApplications(t)
	defer cleanupEmojiApplications(t)
	createTestUser(t, "ea_u1")
	repo := NewEmojiApplicationRepository(testDB)

	seedApplication(t, "ea_a1", "ea_u1", "sushi", model.EmojiApplicationPending)

	dup := &model.EmojiApplication{
		ID: "ea_a2", UserID: "ea_u1", Kind: model.EmojiApplicationKindOwn,
		Status: model.EmojiApplicationPending, Name: "sushi", License: "自作",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	require.ErrorIs(t, repo.Create(dup), ErrEmojiApplicationDuplicatePending)

	// **却下された後は同じ名前で出し直せること。** 条件を付けずに一意制約を
	// 張ると、ここが通らなくなる。
	testDB.Exec(`UPDATE "emoji_application" SET "status" = ? WHERE "id" = ?`,
		model.EmojiApplicationRejected, "ea_a1")
	require.NoError(t, repo.Create(dup))
}

// **一覧が呼び出し元の申請だけを返すこと。** 絞りを落とすと
// `/api/emoji-application/list-mine` が全員の申請 (却下理由・モデレーターへの
// 補足を含む) を返す。handler 側のテストは userID を「渡すこと」しか見ていない
// ので、ここで「使うこと」を固定する。
func TestEmojiApplicationRepository_ListByUserScopes(t *testing.T) {
	cleanupEmojiApplications(t)
	defer cleanupEmojiApplications(t)
	createTestUser(t, "ea_mine")
	createTestUser(t, "ea_other")
	repo := NewEmojiApplicationRepository(testDB)

	seedApplication(t, "ea_b1", "ea_mine", "mine1", model.EmojiApplicationPending)
	seedApplication(t, "ea_b2", "ea_other", "theirs", model.EmojiApplicationPending)
	seedApplication(t, "ea_b3", "ea_mine", "mine2", model.EmojiApplicationRejected)

	rows, err := repo.ListByUser("ea_mine", 50, "")
	require.NoError(t, err)
	require.Len(t, rows, 2, "他人の申請が混ざっている")
	for _, r := range rows {
		require.Equal(t, "ea_mine", r.UserID)
	}
	// 新しい順。
	require.Equal(t, "ea_b3", rows[0].ID)
}

func TestEmojiApplicationRepository_ListFilters(t *testing.T) {
	cleanupEmojiApplications(t)
	defer cleanupEmojiApplications(t)
	createTestUser(t, "ea_u2")
	repo := NewEmojiApplicationRepository(testDB)

	seedApplication(t, "ea_c1", "ea_u2", "pend", model.EmojiApplicationPending)
	seedApplication(t, "ea_c2", "ea_u2", "appr", model.EmojiApplicationApproved)
	seedApplication(t, "ea_c3", "ea_u2", "rej", model.EmojiApplicationRejected)

	pending, err := repo.List(EmojiApplicationFilterPending, 50, "")
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, "ea_c1", pending[0].ID)

	processed, err := repo.List(EmojiApplicationFilterProcessed, 50, "")
	require.NoError(t, err)
	require.Len(t, processed, 2, "審査済みが 2 件返らない")

	all, err := repo.List(EmojiApplicationFilterAll, 50, "")
	require.NoError(t, err)
	require.Len(t, all, 3)

	n, err := repo.CountPending()
	require.NoError(t, err)
	require.EqualValues(t, 1, n)

	// keyset ページング。
	page, err := repo.List(EmojiApplicationFilterAll, 50, "ea_c2")
	require.NoError(t, err)
	require.Len(t, page, 1)
	require.Equal(t, "ea_c1", page[0].ID)
}

// **条件付き UPDATE が SQL レベルで効いていること (#2934 レビュー M1 / R2)。**
//
// service のテストは偽物が条件を Go で再実装しているので、本物の SQL を壊しても
// 緑になる。ここが唯一の担保。
func TestEmojiApplicationRepository_UpdateIfPending(t *testing.T) {
	cleanupEmojiApplications(t)
	defer cleanupEmojiApplications(t)
	createTestUser(t, "ea_u3")
	createTestUser(t, "ea_mod")
	repo := NewEmojiApplicationRepository(testDB)

	app := seedApplication(t, "ea_d1", "ea_u3", "sushi", model.EmojiApplicationPending)

	now := time.Now()
	emojiID, mod := "e-1", "ea_mod"
	app.Status = model.EmojiApplicationApproved
	app.EmojiID = &emojiID
	app.ProcessedByID = &mod
	app.ProcessedAt = &now
	app.UpdatedAt = now

	ok, err := repo.UpdateIfPending(app)
	require.NoError(t, err)
	require.True(t, ok)

	stored, err := repo.FindByID("ea_d1")
	require.NoError(t, err)
	require.Equal(t, model.EmojiApplicationApproved, stored.Status)
	require.Equal(t, "e-1", *stored.EmojiID, "emojiId が書かれていない")
	require.Equal(t, "ea_mod", *stored.ProcessedByID)
	require.NotNil(t, stored.ProcessedAt)
	// 触っていない列が壊れていないこと。
	require.Equal(t, "sushi", stored.Name)

	// **2 回目は負ける。** これが last-write-wins を防いでいる本体。
	reason := "潰れて読めません"
	app.Status = model.EmojiApplicationRejected
	app.RejectReason = &reason
	app.EmojiID = nil
	ok, err = repo.UpdateIfPending(app)
	require.NoError(t, err)
	require.False(t, ok, "処理済みの申請を上書きできてしまう")

	after, err := repo.FindByID("ea_d1")
	require.NoError(t, err)
	require.Equal(t, model.EmojiApplicationApproved, after.Status, "承認が却下で上書きされた")
	require.Equal(t, "e-1", *after.EmojiID, "emojiId が消えた")
	require.Nil(t, after.RejectReason)
}

// 却下では emojiId が NULL として書かれること (map の nil が反映される)。
func TestEmojiApplicationRepository_UpdateIfPendingWritesNull(t *testing.T) {
	cleanupEmojiApplications(t)
	defer cleanupEmojiApplications(t)
	createTestUser(t, "ea_u4")
	repo := NewEmojiApplicationRepository(testDB)

	app := seedApplication(t, "ea_e1", "ea_u4", "kusa", model.EmojiApplicationPending)
	reason := "だめ"
	app.Status = model.EmojiApplicationRejected
	app.RejectReason = &reason
	app.UpdatedAt = time.Now()

	ok, err := repo.UpdateIfPending(app)
	require.NoError(t, err)
	require.True(t, ok)

	stored, err := repo.FindByID("ea_e1")
	require.NoError(t, err)
	require.Equal(t, model.EmojiApplicationRejected, stored.Status)
	require.Equal(t, "だめ", *stored.RejectReason)
	require.Nil(t, stored.EmojiID)
}

func TestEmojiApplicationRepository_FindByIDNotFound(t *testing.T) {
	cleanupEmojiApplications(t)
	_, err := NewEmojiApplicationRepository(testDB).FindByID("missing")
	require.True(t, IsNotFound(err))
}
