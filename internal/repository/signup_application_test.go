package repository

import (
	"testing"
	"time"

	"github.com/shiroha-a/mk/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// newTestApplication builds a live application for the given contact.
func newTestApplication(id, host, remoteID string) *model.SignupApplication {
	now := time.Now().UTC().Truncate(time.Second)
	return &model.SignupApplication{
		ID:              id,
		ContactHost:     host,
		ContactRemoteID: remoteID,
		ContactUsername: "alice",
		Status:          model.SignupApplicationPending,
		CreatedAt:       now,
		UpdatedAt:       now,
		ExpiresAt:       now.Add(7 * 24 * time.Hour),
	}
}

func cleanupApplications(t *testing.T) {
	t.Helper()
	require.NoError(t, testDB.Exec(`DELETE FROM "signup_application"`).Error)
}

func TestSignupApplicationRepository_CreateAndFind(t *testing.T) {
	repo := NewSignupApplicationRepository(testDB)
	cleanupApplications(t)
	defer cleanupApplications(t)

	app := newTestApplication("sa_find_1", "remote.example", "r1")
	require.NoError(t, repo.Create(app))

	found, err := repo.FindByID(app.ID)
	require.NoError(t, err)
	assert.Equal(t, "remote.example", found.ContactHost)
	assert.Equal(t, "r1", found.ContactRemoteID)
	assert.Equal(t, model.SignupApplicationPending, found.Status)

	live, err := repo.FindLiveByContact("remote.example", "r1")
	require.NoError(t, err)
	assert.Equal(t, app.ID, live.ID)
}

// 同一連絡先で申請が二重に生きることを防ぐ。**制約が無いと、審査中の申請を
// 持ったまま何度でも積み増せる。**
func TestSignupApplicationRepository_Create_RejectsDuplicateLive(t *testing.T) {
	repo := NewSignupApplicationRepository(testDB)
	cleanupApplications(t)
	defer cleanupApplications(t)

	require.NoError(t, repo.Create(newTestApplication("sa_dup_1", "remote.example", "r1")))

	err := repo.Create(newTestApplication("sa_dup_2", "remote.example", "r1"))
	assert.ErrorIs(t, err, ErrSignupApplicationLiveExists)
}

// 承認済みの申請も「生きている」ので重複を弾く。
func TestSignupApplicationRepository_Create_ApprovedAlsoBlocks(t *testing.T) {
	repo := NewSignupApplicationRepository(testDB)
	cleanupApplications(t)
	defer cleanupApplications(t)

	approved := newTestApplication("sa_appr_1", "remote.example", "r1")
	approved.Status = model.SignupApplicationApproved
	require.NoError(t, repo.Create(approved))

	err := repo.Create(newTestApplication("sa_appr_2", "remote.example", "r1"))
	assert.ErrorIs(t, err, ErrSignupApplicationLiveExists)
}

// 却下・期限切れ・登録完了の後は同じ連絡先で申請し直せること。
//
// **全体に一意制約を張ると、ここが通らなくなる。** 連絡先を失っていない利用者が
// 二度と申請できなくなり、回復手段が無くなる。
func TestSignupApplicationRepository_Create_AllowsReapplyAfterTerminal(t *testing.T) {
	for _, status := range []string{
		model.SignupApplicationRejected,
		model.SignupApplicationExpired,
		model.SignupApplicationCompleted,
	} {
		t.Run(status, func(t *testing.T) {
			repo := NewSignupApplicationRepository(testDB)
			cleanupApplications(t)
			defer cleanupApplications(t)

			old := newTestApplication("sa_re_old", "remote.example", "r1")
			old.Status = status
			require.NoError(t, repo.Create(old))

			assert.NoError(t, repo.Create(newTestApplication("sa_re_new", "remote.example", "r1")))
		})
	}
}

// 別の連絡先での申請は、古い申請の状態に関わらず通ること。
func TestSignupApplicationRepository_Create_OtherContactNotBlocked(t *testing.T) {
	repo := NewSignupApplicationRepository(testDB)
	cleanupApplications(t)
	defer cleanupApplications(t)

	require.NoError(t, repo.Create(newTestApplication("sa_oc_1", "remote.example", "r1")))
	assert.NoError(t, repo.Create(newTestApplication("sa_oc_2", "remote.example", "r2")))
	assert.NoError(t, repo.Create(newTestApplication("sa_oc_3", "other.example", "r1")))
}

func TestSignupApplicationRepository_FindLiveByContact_NotFound(t *testing.T) {
	repo := NewSignupApplicationRepository(testDB)
	cleanupApplications(t)
	defer cleanupApplications(t)

	rejected := newTestApplication("sa_nf_1", "remote.example", "r1")
	rejected.Status = model.SignupApplicationRejected
	require.NoError(t, repo.Create(rejected))

	_, err := repo.FindLiveByContact("remote.example", "r1")
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

// 却下・期限切れの結果を申請者に見せるために、終端状態でも最新を引けること。
func TestSignupApplicationRepository_FindLatestByContact(t *testing.T) {
	repo := NewSignupApplicationRepository(testDB)
	cleanupApplications(t)
	defer cleanupApplications(t)

	older := newTestApplication("sa_lt_old", "remote.example", "r1")
	older.Status = model.SignupApplicationRejected
	older.CreatedAt = older.CreatedAt.Add(-2 * time.Hour)
	require.NoError(t, repo.Create(older))

	newer := newTestApplication("sa_lt_new", "remote.example", "r1")
	newer.Status = model.SignupApplicationExpired
	require.NoError(t, repo.Create(newer))

	found, err := repo.FindLatestByContact("remote.example", "r1")
	require.NoError(t, err)
	assert.Equal(t, "sa_lt_new", found.ID)
}

func TestSignupApplicationRepository_ListAndCount(t *testing.T) {
	repo := NewSignupApplicationRepository(testDB)
	cleanupApplications(t)
	defer cleanupApplications(t)

	pending := newTestApplication("sa_ls_p", "remote.example", "r1")
	require.NoError(t, repo.Create(pending))

	approved := newTestApplication("sa_ls_a", "remote.example", "r2")
	approved.Status = model.SignupApplicationApproved
	require.NoError(t, repo.Create(approved))

	rejected := newTestApplication("sa_ls_r", "remote.example", "r3")
	rejected.Status = model.SignupApplicationRejected
	require.NoError(t, repo.Create(rejected))

	tests := []struct {
		filter string
		want   []string
	}{
		{filter: SignupApplicationFilterPending, want: []string{"sa_ls_p"}},
		{filter: SignupApplicationFilterApproved, want: []string{"sa_ls_a"}},
		{filter: SignupApplicationFilterProcessed, want: []string{"sa_ls_r"}},
	}
	for _, tt := range tests {
		t.Run(tt.filter, func(t *testing.T) {
			rows, err := repo.List(tt.filter, 50, 0)
			require.NoError(t, err)
			ids := make([]string, 0, len(rows))
			for _, r := range rows {
				ids = append(ids, r.ID)
			}
			assert.ElementsMatch(t, tt.want, ids)

			count, err := repo.Count(tt.filter)
			require.NoError(t, err)
			assert.Equal(t, len(tt.want), count)
		})
	}

	// 未知のフィルタは "all" 扱い。**古いクライアントが管理画面を壊せないように。**
	rows, err := repo.List("no-such-filter", 50, 0)
	require.NoError(t, err)
	assert.Len(t, rows, 3)

	count, err := repo.Count(SignupApplicationFilterAll)
	require.NoError(t, err)
	assert.Equal(t, 3, count)
}

func TestSignupApplicationRepository_List_Pagination(t *testing.T) {
	repo := NewSignupApplicationRepository(testDB)
	cleanupApplications(t)
	defer cleanupApplications(t)

	base := time.Now().UTC().Truncate(time.Second)
	for i, id := range []string{"sa_pg_1", "sa_pg_2", "sa_pg_3"} {
		app := newTestApplication(id, "remote.example", id)
		app.CreatedAt = base.Add(time.Duration(i) * time.Minute)
		require.NoError(t, repo.Create(app))
	}

	// 新しい順。
	rows, err := repo.List(SignupApplicationFilterAll, 2, 0)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	assert.Equal(t, "sa_pg_3", rows[0].ID)
	assert.Equal(t, "sa_pg_2", rows[1].ID)

	rows, err = repo.List(SignupApplicationFilterAll, 2, 2)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "sa_pg_1", rows[0].ID)
}

func TestSignupApplicationRepository_ExpireStale(t *testing.T) {
	repo := NewSignupApplicationRepository(testDB)
	cleanupApplications(t)
	defer cleanupApplications(t)

	now := time.Now().UTC().Truncate(time.Second)

	stalePending := newTestApplication("sa_ex_p", "remote.example", "r1")
	stalePending.ExpiresAt = now.Add(-time.Hour)
	require.NoError(t, repo.Create(stalePending))

	staleApproved := newTestApplication("sa_ex_a", "remote.example", "r2")
	staleApproved.Status = model.SignupApplicationApproved
	staleApproved.ExpiresAt = now.Add(-time.Hour)
	require.NoError(t, repo.Create(staleApproved))

	fresh := newTestApplication("sa_ex_f", "remote.example", "r3")
	require.NoError(t, repo.Create(fresh))

	// 終端状態は期限を過ぎていても触らない (履歴を書き換えない)。
	rejected := newTestApplication("sa_ex_r", "remote.example", "r4")
	rejected.Status = model.SignupApplicationRejected
	rejected.ExpiresAt = now.Add(-time.Hour)
	require.NoError(t, repo.Create(rejected))

	changed, err := repo.ExpireStale(now)
	require.NoError(t, err)
	assert.Equal(t, 2, changed)

	for _, tc := range []struct{ id, want string }{
		{"sa_ex_p", model.SignupApplicationExpired},
		{"sa_ex_a", model.SignupApplicationExpired},
		{"sa_ex_f", model.SignupApplicationPending},
		{"sa_ex_r", model.SignupApplicationRejected},
	} {
		found, err := repo.FindByID(tc.id)
		require.NoError(t, err)
		assert.Equal(t, tc.want, found.Status, tc.id)
	}
}

// 期限切れにした後は、同じ連絡先で申請し直せること (部分一意インデックスの
// 条件から外れる)。
func TestSignupApplicationRepository_ExpireStale_FreesContact(t *testing.T) {
	repo := NewSignupApplicationRepository(testDB)
	cleanupApplications(t)
	defer cleanupApplications(t)

	now := time.Now().UTC().Truncate(time.Second)
	stale := newTestApplication("sa_fr_1", "remote.example", "r1")
	stale.ExpiresAt = now.Add(-time.Hour)
	require.NoError(t, repo.Create(stale))

	_, err := repo.ExpireStale(now)
	require.NoError(t, err)

	assert.NoError(t, repo.Create(newTestApplication("sa_fr_2", "remote.example", "r1")))
}

func TestSignupApplicationRepository_UpdateFieldsTx(t *testing.T) {
	repo := NewSignupApplicationRepository(testDB)
	cleanupApplications(t)
	defer cleanupApplications(t)

	app := newTestApplication("sa_up_1", "remote.example", "r1")
	require.NoError(t, repo.Create(app))

	admin := "admin_user_id"
	now := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, repo.DB().Transaction(func(tx *gorm.DB) error {
		locked, err := repo.FindByIDForUpdateTx(tx, app.ID)
		if err != nil {
			return err
		}
		if locked.Status != model.SignupApplicationPending {
			t.Fatalf("unexpected status %q", locked.Status)
		}
		return repo.UpdateFieldsTx(tx, app.ID, map[string]any{
			"status":        model.SignupApplicationApproved,
			"processedById": admin,
			"processedAt":   now,
			"updatedAt":     now,
		})
	}))

	found, err := repo.FindByID(app.ID)
	require.NoError(t, err)
	assert.Equal(t, model.SignupApplicationApproved, found.Status)
	require.NotNil(t, found.ProcessedByID)
	assert.Equal(t, admin, *found.ProcessedByID)
	require.NotNil(t, found.ProcessedAt)
}

func TestSignupApplicationRepository_UpdateFieldsTx_EmptyIsNoop(t *testing.T) {
	repo := NewSignupApplicationRepository(testDB)
	cleanupApplications(t)
	defer cleanupApplications(t)

	app := newTestApplication("sa_up_2", "remote.example", "r1")
	require.NoError(t, repo.Create(app))

	require.NoError(t, repo.DB().Transaction(func(tx *gorm.DB) error {
		return repo.UpdateFieldsTx(tx, app.ID, nil)
	}))

	found, err := repo.FindByID(app.ID)
	require.NoError(t, err)
	assert.Equal(t, model.SignupApplicationPending, found.Status)
}

func TestSignupApplicationRepository_FindByIDForUpdateTx_NotFound(t *testing.T) {
	repo := NewSignupApplicationRepository(testDB)
	cleanupApplications(t)
	defer cleanupApplications(t)

	require.NoError(t, repo.DB().Transaction(func(tx *gorm.DB) error {
		_, err := repo.FindByIDForUpdateTx(tx, "no-such-id")
		assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
		return nil
	}))
}

func TestSignupApplicationRepository_FindByID_NotFound(t *testing.T) {
	repo := NewSignupApplicationRepository(testDB)
	_, err := repo.FindByID("no-such-id")
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

// IsLive は部分一意インデックスの条件と同じ集合を返すこと。**ずれると、DB が
// 弾く組み合わせをサービス層が通そうとして一意制約違反が利用者に露出する。**
func TestSignupApplication_IsLive_MatchesIndexPredicate(t *testing.T) {
	repo := NewSignupApplicationRepository(testDB)
	cleanupApplications(t)
	defer cleanupApplications(t)

	for _, status := range []string{
		model.SignupApplicationPending,
		model.SignupApplicationApproved,
		model.SignupApplicationRejected,
		model.SignupApplicationExpired,
		model.SignupApplicationCompleted,
	} {
		t.Run(status, func(t *testing.T) {
			cleanupApplications(t)
			first := newTestApplication("sa_iv_1", "remote.example", "r1")
			first.Status = status
			require.NoError(t, repo.Create(first))

			// DB が 2 件目を弾くかどうかと IsLive が一致すること。
			err := repo.Create(newTestApplication("sa_iv_2", "remote.example", "r1"))
			assert.Equal(t, first.IsLive(), err != nil,
				"IsLive=%v but Create error=%v", first.IsLive(), err)
		})
	}
}

// abortedTxRepo returns a repository bound to a transaction that PostgreSQL has
// already aborted, so every subsequent statement fails.
//
// DB 障害時の分岐を踏むための道具。**接続を壊す代わりに transaction を abort
// させる**ので、他のテストが使っている接続に影響しない。
func abortedTxRepo(t *testing.T) SignupApplicationRepository {
	t.Helper()
	tx := testDB.Begin()
	require.NoError(t, tx.Error)
	t.Cleanup(func() { tx.Rollback() })
	// わざと失敗させて abort 状態にする。以降の文はすべてエラーになる。
	require.Error(t, tx.Exec(`SELECT 1 FROM "no_such_table_for_error_path"`).Error)
	return NewSignupApplicationRepository(tx)
}

func TestSignupApplicationRepository_ErrorPaths(t *testing.T) {
	t.Run("Create surfaces non-unique errors as-is", func(t *testing.T) {
		repo := abortedTxRepo(t)
		err := repo.Create(newTestApplication("sa_err_1", "remote.example", "r1"))
		require.Error(t, err)
		// 一意制約違反ではないので、専用エラーに化けないこと。
		assert.NotErrorIs(t, err, ErrSignupApplicationLiveExists)
	})

	t.Run("FindLatestByContact", func(t *testing.T) {
		repo := abortedTxRepo(t)
		_, err := repo.FindLatestByContact("remote.example", "r1")
		assert.Error(t, err)
	})

	t.Run("List", func(t *testing.T) {
		repo := abortedTxRepo(t)
		_, err := repo.List(SignupApplicationFilterAll, 10, 0)
		assert.Error(t, err)
	})

	t.Run("Count", func(t *testing.T) {
		repo := abortedTxRepo(t)
		_, err := repo.Count(SignupApplicationFilterAll)
		assert.Error(t, err)
	})

	t.Run("ExpireStale", func(t *testing.T) {
		repo := abortedTxRepo(t)
		_, err := repo.ExpireStale(time.Now())
		assert.Error(t, err)
	})

	t.Run("FindByIDForUpdateTx", func(t *testing.T) {
		repo := abortedTxRepo(t)
		_, err := repo.FindByIDForUpdateTx(repo.DB(), "sa_err_1")
		assert.Error(t, err)
	})
}
