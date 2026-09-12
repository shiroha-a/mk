package repository

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/testutil"
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

// seedApplicationAt inserts an application with an explicit createdAt so the
// rolling windows can be exercised without sleeping.
func seedApplicationAt(t *testing.T, id, userID, name, status string, createdAt time.Time) {
	t.Helper()
	app := &model.EmojiApplication{
		ID: id, UserID: userID, Kind: model.EmojiApplicationKindOwn,
		Status: status, Name: name, License: "自作",
		CreatedAt: createdAt, UpdatedAt: createdAt,
	}
	require.NoError(t, NewEmojiApplicationRepository(testDB).Create(app))
}

func quotaApp(id, userID, name string, createdAt time.Time) *model.EmojiApplication {
	return &model.EmojiApplication{
		ID: id, UserID: userID, Kind: model.EmojiApplicationKindOwn,
		Status: model.EmojiApplicationPending, Name: name, License: "自作",
		CreatedAt: createdAt, UpdatedAt: createdAt,
	}
}

// **境界を実 DB で固定する (#2958)。** 上限ちょうどの手前までは通り、達したら
// 弾く。`<` と `<=` を取り違えると 1 件多く通るが、単体では気付けない。
func TestEmojiApplicationRepository_CreateWithQuota_Boundary(t *testing.T) {
	cleanupEmojiApplications(t)
	defer cleanupEmojiApplications(t)
	createTestUser(t, "ea_q1")
	repo := NewEmojiApplicationRepository(testDB)
	now := time.Now()
	limits := QuotaLimits{Windows: []QuotaWindow{{Name: "day", Duration: 24 * time.Hour, Max: 2}}}

	require.NoError(t, repo.CreateWithQuota(quotaApp("ea_q1a", "ea_q1", "a", now), limits))
	require.NoError(t, repo.CreateWithQuota(quotaApp("ea_q1b", "ea_q1", "b", now), limits))

	err := repo.CreateWithQuota(quotaApp("ea_q1c", "ea_q1", "c", now), limits)
	var qe *QuotaExceededError
	require.ErrorAs(t, err, &qe)
	require.Equal(t, "day", qe.Window.Name)
	require.Equal(t, 2, qe.Used)
	// 最古の 1 件が窓を出る時刻。ミリ秒までは一致しないので幅で見る。
	require.WithinDuration(t, now.Add(24*time.Hour), qe.RetryAt, time.Minute)

	// **弾いたときは行を作らないこと。** rollback が効いていないと、上限に
	// 達した後も申請が積み上がる。
	var n int64
	require.NoError(t, testDB.Model(&model.EmojiApplication{}).
		Where(`"userId" = ?`, "ea_q1").Count(&n).Error)
	require.Equal(t, int64(2), n)
}

// **窓の外は数えない。** 期間の引き算を間違えると、一度上限に達した人が
// 永久に申請できなくなる。
func TestEmojiApplicationRepository_CreateWithQuota_WindowExpiry(t *testing.T) {
	cleanupEmojiApplications(t)
	defer cleanupEmojiApplications(t)
	createTestUser(t, "ea_q2")
	repo := NewEmojiApplicationRepository(testDB)
	now := time.Now()
	seedApplicationAt(t, "ea_q2a", "ea_q2", "a", model.EmojiApplicationRejected, now.Add(-25*time.Hour))

	limits := QuotaLimits{Windows: []QuotaWindow{{Name: "day", Duration: 24 * time.Hour, Max: 1}}}
	require.NoError(t, repo.CreateWithQuota(quotaApp("ea_q2b", "ea_q2", "b", now), limits))
}

// **却下・取り下げでも枠は戻らない (#2958 の完了条件)。** 戻すと、申請と
// 取り下げを繰り返すだけでモデレーターへの通知を無限に作れる。
func TestEmojiApplicationRepository_CreateWithQuota_CountsAllStatuses(t *testing.T) {
	cleanupEmojiApplications(t)
	defer cleanupEmojiApplications(t)
	createTestUser(t, "ea_q3")
	repo := NewEmojiApplicationRepository(testDB)
	now := time.Now()
	for i, st := range []string{
		model.EmojiApplicationRejected,
		model.EmojiApplicationCanceled,
		model.EmojiApplicationApproved,
	} {
		seedApplicationAt(t, "ea_q3"+string(rune('a'+i)), "ea_q3",
			string(rune('a'+i)), st, now.Add(-time.Duration(i+1)*time.Hour))
	}

	limits := QuotaLimits{Windows: []QuotaWindow{{Name: "week", Duration: 7 * 24 * time.Hour, Max: 3}}}
	err := repo.CreateWithQuota(quotaApp("ea_q3z", "ea_q3", "z", now), limits)
	var qe *QuotaExceededError
	require.ErrorAs(t, err, &qe)
	require.Equal(t, 3, qe.Used)
}

// **数えるのは自分の分だけ。** 絞りを落とすと、誰かが上限まで申請した時点で
// インスタンス全員が申請できなくなる。
func TestEmojiApplicationRepository_CreateWithQuota_ScopedToUser(t *testing.T) {
	cleanupEmojiApplications(t)
	defer cleanupEmojiApplications(t)
	createTestUser(t, "ea_q4")
	createTestUser(t, "ea_q4other")
	repo := NewEmojiApplicationRepository(testDB)
	now := time.Now()
	seedApplicationAt(t, "ea_q4x", "ea_q4other", "x", model.EmojiApplicationPending, now)

	limits := QuotaLimits{Windows: []QuotaWindow{{Name: "day", Duration: 24 * time.Hour, Max: 1}}}
	require.NoError(t, repo.CreateWithQuota(quotaApp("ea_q4a", "ea_q4", "a", now), limits))
}

// **0 は無制限 (#2958 の完了条件)。** 既定値が 0 なので、ここが上限として
// 効くと既存のインスタンスで申請が全て塞がる。
func TestEmojiApplicationRepository_CreateWithQuota_ZeroIsUnlimited(t *testing.T) {
	cleanupEmojiApplications(t)
	defer cleanupEmojiApplications(t)
	createTestUser(t, "ea_q5")
	repo := NewEmojiApplicationRepository(testDB)
	now := time.Now()
	limits := QuotaLimits{Windows: []QuotaWindow{
		{Name: "day", Duration: 24 * time.Hour, Max: 0},
		{Name: "week", Duration: 7 * 24 * time.Hour, Max: 0},
	}}
	for i := 0; i < 3; i++ {
		require.NoError(t, repo.CreateWithQuota(
			quotaApp("ea_q5"+string(rune('a'+i)), "ea_q5", string(rune('a'+i)), now), limits))
	}
	// windows 自体が空でも同じ
	require.NoError(t, repo.CreateWithQuota(quotaApp("ea_q5z", "ea_q5", "z", now), QuotaLimits{}))
}

// **狭い窓が先に効くこと。** 日次 1 / 月次 5 のとき、2 件目は日次で弾かれる。
// 窓を評価する順序に依存していると、返す period が実態と食い違う。
func TestEmojiApplicationRepository_CreateWithQuota_NarrowestWindowWins(t *testing.T) {
	cleanupEmojiApplications(t)
	defer cleanupEmojiApplications(t)
	createTestUser(t, "ea_q6")
	repo := NewEmojiApplicationRepository(testDB)
	now := time.Now()
	limits := QuotaLimits{Windows: []QuotaWindow{
		{Name: "month", Duration: 30 * 24 * time.Hour, Max: 5},
		{Name: "day", Duration: 24 * time.Hour, Max: 1},
	}}
	require.NoError(t, repo.CreateWithQuota(quotaApp("ea_q6a", "ea_q6", "a", now), limits))
	err := repo.CreateWithQuota(quotaApp("ea_q6b", "ea_q6", "b", now), limits)
	var qe *QuotaExceededError
	require.ErrorAs(t, err, &qe)
	require.Equal(t, "day", qe.Window.Name)
}

// **複数の窓が同時に満杯なら、いちばん遅く空くものを案内すること (#2958)。**
// 最初に満杯だったものを返すと、案内した時刻に叩いてもまだ別の窓が満杯で、
// もう一度 429 になる (1 周目で最古を返していたのと同じ失敗形)。
func TestEmojiApplicationRepository_CreateWithQuota_LatestFullWindowWins(t *testing.T) {
	cleanupEmojiApplications(t)
	defer cleanupEmojiApplications(t)
	createTestUser(t, "ea_q13")
	repo := NewEmojiApplicationRepository(testDB)
	now := time.Now()
	// week の窓 (168h) にだけ入る 2 件と、day の窓にも入る 3 件。
	// day は 3/3 で満杯、week は 5/5 で満杯。
	for i, h := range []int{72, 60, 6, 4, 2} {
		seedApplicationAt(t, "ea_q13"+string(rune('a'+i)), "ea_q13",
			string(rune('a'+i)), model.EmojiApplicationPending, now.Add(-time.Duration(h)*time.Hour))
	}
	limits := QuotaLimits{Windows: []QuotaWindow{
		{Name: "day", Duration: 24 * time.Hour, Max: 3},
		{Name: "week", Duration: 7 * 24 * time.Hour, Max: 5},
	}}

	err := repo.CreateWithQuota(quotaApp("ea_q13z", "ea_q13", "z", now), limits)
	var qe *QuotaExceededError
	require.ErrorAs(t, err, &qe)
	// day は最古 (now-6h) が抜ければ空く → now+18h。
	// week は最古 (now-72h) が抜ければ空く → now+96h。**遅い方を返す。**
	require.Equal(t, "week", qe.Window.Name)
	require.WithinDuration(t, now.Add(96*time.Hour), qe.RetryAt, time.Minute)

	// 案内した時刻には本当に通ること (両方の窓に空きができている)。
	require.NoError(t, repo.CreateWithQuota(quotaApp("ea_q13z", "ea_q13", "z", qe.RetryAt), limits))
}

// **案内した時刻ちょうどで通ること。** 窓の判定は `createdAt >= since` なので
// 境界の行はまだ窓の中にいる。`entity.ISOMillis` は切り捨てるので、境界を
// そのまま広告すると、その値で再スケジュールするクライアントが必ず空振りする。
func TestEmojiApplicationRepository_CreateWithQuota_RetryAtIsInclusive(t *testing.T) {
	cleanupEmojiApplications(t)
	defer cleanupEmojiApplications(t)
	createTestUser(t, "ea_q14")
	repo := NewEmojiApplicationRepository(testDB)
	now := time.Now()
	seedApplicationAt(t, "ea_q14a", "ea_q14", "a", model.EmojiApplicationPending, now.Add(-time.Hour))

	limits := QuotaLimits{Windows: []QuotaWindow{{Name: "day", Duration: 24 * time.Hour, Max: 1}}}
	err := repo.CreateWithQuota(quotaApp("ea_q14z", "ea_q14", "z", now), limits)
	var qe *QuotaExceededError
	require.ErrorAs(t, err, &qe)
	require.NoError(t, repo.CreateWithQuota(quotaApp("ea_q14z", "ea_q14", "z", qe.RetryAt), limits))
}

// **重複の番兵は上限経路でも維持すること。** CreateWithQuota が Create を
// 置き換えるので、ここで潰すと「審査待ちが既にある」が 500 に化ける。
func TestEmojiApplicationRepository_CreateWithQuota_DuplicatePending(t *testing.T) {
	cleanupEmojiApplications(t)
	defer cleanupEmojiApplications(t)
	createTestUser(t, "ea_q7")
	repo := NewEmojiApplicationRepository(testDB)
	now := time.Now()
	limits := QuotaLimits{Windows: []QuotaWindow{{Name: "day", Duration: 24 * time.Hour, Max: 10}}}
	require.NoError(t, repo.CreateWithQuota(quotaApp("ea_q7a", "ea_q7", "same", now), limits))
	require.ErrorIs(t,
		repo.CreateWithQuota(quotaApp("ea_q7b", "ea_q7", "same", now), limits),
		ErrEmojiApplicationDuplicatePending)
}

// **同時に投げても上限を超えないこと (#2958 の完了条件)。** 数える側と作る側を
// 別のトランザクションに分けると、両方が「空きあり」を読んで両方通る。
func TestEmojiApplicationRepository_CreateWithQuota_Concurrent(t *testing.T) {
	cleanupEmojiApplications(t)
	defer cleanupEmojiApplications(t)
	createTestUser(t, "ea_q8")
	now := time.Now()
	limits := QuotaLimits{Windows: []QuotaWindow{{Name: "day", Duration: 24 * time.Hour, Max: 3}}}

	const n = 8
	// **共有プールを張り替えない。** `testDB` は同パッケージの全テストが使う
	// ので、`SetMaxIdleConns` を書き換えて戻す形にすると、戻し漏れが後続の
	// テストへ波及する (#2795 が `internal/server` で踏んだ型)。しかも
	// `database/sql` は `MaxIdleConns` を読み戻せないので save/restore が
	// 書けない。goroutine ごとに専用のハンドルを開けば、既定のプール
	// (MaxIdleConns=2) のままでも 8 本が本当に同時に走る。
	repos := make([]EmojiApplicationRepository, n)
	for i := range repos {
		db, err := testutil.OpenTestDB()
		require.NoError(t, err)
		sqlDB, err := db.DB()
		require.NoError(t, err)
		t.Cleanup(func() { _ = sqlDB.Close() })
		repos[i] = NewEmojiApplicationRepository(db)
	}

	// **合図の前に接続を掴ませる。** go func を起こした直後に close すると、
	// まだ `<-start` へ到達していない goroutine がいる。接続確立の往復が
	// 入ると 1 本目がトランザクションを終えてから最後が始まり、**advisory
	// lock を外しても緑になる** (実測で 30 回中 14 回)。
	var ready sync.WaitGroup
	ready.Add(n)
	start := make(chan struct{})
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func(i int) {
			var one int
			_ = repos[i].(*emojiApplicationRepository).db.Raw("SELECT 1").Scan(&one).Error
			ready.Done()
			<-start
			errs <- repos[i].CreateWithQuota(
				quotaApp("ea_q8"+string(rune('a'+i)), "ea_q8", string(rune('a'+i)), now), limits)
		}(i)
	}
	ready.Wait()
	close(start)

	var ok, rejected int
	for i := 0; i < n; i++ {
		err := <-errs
		var qe *QuotaExceededError
		switch {
		case err == nil:
			ok++
		case errors.As(err, &qe):
			rejected++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	require.Equal(t, 3, ok)
	require.Equal(t, n-3, rejected)

	var stored int64
	require.NoError(t, testDB.Model(&model.EmojiApplication{}).
		Where(`"userId" = ?`, "ea_q8").Count(&stored).Error)
	require.Equal(t, int64(3), stored)
}

// **`RetryAt` は「最古の 1 件が窓を出る時刻」(#2958)。** 上限ちょうどのときは
// 最古が抜ければ 1 件空く。**時刻を散らして seed するのが要点** — 全件を同じ
// 時刻で作ると `MIN` / `MAX` / `now` が区別できず、この計算が空虚になる。
func TestEmojiApplicationRepository_CreateWithQuota_RetryAtAtLimit(t *testing.T) {
	cleanupEmojiApplications(t)
	defer cleanupEmojiApplications(t)
	createTestUser(t, "ea_q9")
	repo := NewEmojiApplicationRepository(testDB)
	now := time.Now()
	seedApplicationAt(t, "ea_q9a", "ea_q9", "a", model.EmojiApplicationPending, now.Add(-20*time.Hour))
	seedApplicationAt(t, "ea_q9b", "ea_q9", "b", model.EmojiApplicationRejected, now.Add(-4*time.Hour))

	limits := QuotaLimits{Windows: []QuotaWindow{{Name: "day", Duration: 24 * time.Hour, Max: 2}}}
	err := repo.CreateWithQuota(quotaApp("ea_q9z", "ea_q9", "z", now), limits)
	var qe *QuotaExceededError
	require.ErrorAs(t, err, &qe)
	// 最古 (now-20h) が窓を出るのは now+4h。**最新 (now-4h) を採ると now+20h**
	// になるので、許容は 4 時間より十分小さく取る。
	require.WithinDuration(t, now.Add(4*time.Hour), qe.RetryAt, time.Minute)
}

// **上限を超えている状態では最古の 1 件では足りない (#2958)。** 上限を後から
// 下げた / 上限の緩いロールを外したときに、**新しい上限を既に超えている
// 利用者**がこの状態になる (下げても届いていない人は 429 にすらならない)。最古を返すと、
// 案内した時刻に叩いてもまた 429 になり、その再試行が 1 時間あたりの API
// レート制限を食い潰す。
func TestEmojiApplicationRepository_CreateWithQuota_RetryAtOverLimit(t *testing.T) {
	cleanupEmojiApplications(t)
	defer cleanupEmojiApplications(t)
	createTestUser(t, "ea_q10")
	repo := NewEmojiApplicationRepository(testDB)
	now := time.Now()
	// **窓の外にも 1 件置く。** これが無いと「窓で絞らずに数え上げる」変異を
	// 検出できない (絞りを外しても並びの先頭が変わらないため)。
	seedApplicationAt(t, "ea_q10old", "ea_q10", "old", model.EmojiApplicationApproved, now.Add(-30*time.Hour))
	// **他人の行を窓の中に置く。** これが無いと「利用者で絞らずに数え上げる」
	// 変異を検出できない。絞りを落とすと他人の申請が自分の再試行時刻を決める。
	createTestUser(t, "ea_q10other")
	seedApplicationAt(t, "ea_q10o", "ea_q10other", "o", model.EmojiApplicationPending, now.Add(-22*time.Hour))
	for i, h := range []int{20, 16, 12, 8, 4} {
		seedApplicationAt(t, "ea_q10"+string(rune('a'+i)), "ea_q10",
			string(rune('a'+i)), model.EmojiApplicationPending, now.Add(-time.Duration(h)*time.Hour))
	}

	limits := QuotaLimits{Windows: []QuotaWindow{{Name: "day", Duration: 24 * time.Hour, Max: 2}}}
	err := repo.CreateWithQuota(quotaApp("ea_q10z", "ea_q10", "z", now), limits)
	var qe *QuotaExceededError
	require.ErrorAs(t, err, &qe)
	require.Equal(t, 5, qe.Used)
	// 5 件のうち 4 件 (used - Max + 1) が抜けて初めて 1 < 2 になる。4 件目に
	// 古いのは now-8h なので、空くのは now+16h。**最古 (now-20h) を採ると
	// now+4h** で、その時刻にはまだ 4 件残っている。
	require.WithinDuration(t, now.Add(16*time.Hour), qe.RetryAt, time.Minute)

	// 案内した時刻に本当に通ること (ここを見ないと offset がずれても気付けない)。
	later := qe.RetryAt.Add(time.Second)
	require.NoError(t, repo.CreateWithQuota(quotaApp("ea_q10z", "ea_q10", "z", later), limits))
}

// **自作画像とリモート絵文字は枠を共有する (#2958 の完了条件)。** 種別ごとに
// 分けると、片方の枠を使い切ってももう片方から申請し続けられる。審査する側の
// 負担は種別では変わらない。
func TestEmojiApplicationRepository_CreateWithQuota_CountsBothKinds(t *testing.T) {
	cleanupEmojiApplications(t)
	defer cleanupEmojiApplications(t)
	createTestUser(t, "ea_q11")
	repo := NewEmojiApplicationRepository(testDB)
	now := time.Now()
	host, rname := "remote.example", "kusa"
	require.NoError(t, testDB.Create(&model.EmojiApplication{
		ID: "ea_q11r", UserID: "ea_q11", Kind: model.EmojiApplicationKindRemote,
		Status: model.EmojiApplicationPending, Name: "r", License: "取り込み元",
		RemoteHost: &host, RemoteName: &rname,
		CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour),
	}).Error)
	seedApplicationAt(t, "ea_q11o", "ea_q11", "o", model.EmojiApplicationPending, now.Add(-2*time.Hour))

	limits := QuotaLimits{Windows: []QuotaWindow{{Name: "day", Duration: 24 * time.Hour, Max: 2}}}
	// own 1 + remote 1 = 2 で上限。種別で分けていると own は 1 件しか無いので通る。
	err := repo.CreateWithQuota(quotaApp("ea_q11z", "ea_q11", "z", now), limits)
	var qe *QuotaExceededError
	require.ErrorAs(t, err, &qe)
	require.Equal(t, 2, qe.Used)

	// リモート側から出しても同じ枠を見ること (逆向きも塞ぐ)。
	remote := &model.EmojiApplication{
		ID: "ea_q11z2", UserID: "ea_q11", Kind: model.EmojiApplicationKindRemote,
		Status: model.EmojiApplicationPending, Name: "z2", License: "取り込み元",
		RemoteHost: &host, RemoteName: &rname, CreatedAt: now, UpdatedAt: now,
	}
	require.ErrorAs(t, repo.CreateWithQuota(remote, limits), &qe)
}

// 番兵の文面が窓の名前を含むこと。errors.As で分岐できない呼び出し元 (ログ) が
// 唯一の手がかりにする。
func TestQuotaExceededError_Message(t *testing.T) {
	err := &QuotaExceededError{Window: QuotaWindow{Name: "week"}, Used: 3}
	require.Contains(t, err.Error(), "week")
}

// **createdAt が未設定なら現在時刻で窓を切ること。** ゼロ値のまま引き算すると
// 窓の起点が西暦 1 年になり、**過去の申請を全部数えて誰も申請できなくなる**。
// service は必ず埋めるので通常は通らない経路だが、fallback が逆向きに壊れて
// いても気付けないので固定する。
func TestEmojiApplicationRepository_CreateWithQuota_ZeroCreatedAtUsesNow(t *testing.T) {
	cleanupEmojiApplications(t)
	defer cleanupEmojiApplications(t)
	createTestUser(t, "ea_q12")
	repo := NewEmojiApplicationRepository(testDB)
	// 窓 (直前 24 時間) の外にだけ 1 件置く。
	seedApplicationAt(t, "ea_q12a", "ea_q12", "a", model.EmojiApplicationPending, time.Now().Add(-30*time.Hour))

	app := quotaApp("ea_q12z", "ea_q12", "z", time.Time{})
	app.CreatedAt = time.Time{}
	app.UpdatedAt = time.Now()
	limits := QuotaLimits{Windows: []QuotaWindow{{Name: "day", Duration: 24 * time.Hour, Max: 1}}}
	require.NoError(t, repo.CreateWithQuota(app, limits))
}

// **審査待ちの件数で弾けること (#2977)。** 既存の一意索引は `(userId, name)`
// WHERE pending なので、名前を変えれば審査待ちは何件でも積める。
func TestEmojiApplicationRepository_CreateWithQuota_PendingLimit(t *testing.T) {
	cleanupEmojiApplications(t)
	defer cleanupEmojiApplications(t)
	createTestUser(t, "ea_p1")
	repo := NewEmojiApplicationRepository(testDB)
	now := time.Now()
	limits := QuotaLimits{MaxPending: 2}

	require.NoError(t, repo.CreateWithQuota(quotaApp("ea_p1a", "ea_p1", "a", now), limits))
	require.NoError(t, repo.CreateWithQuota(quotaApp("ea_p1b", "ea_p1", "b", now), limits))

	err := repo.CreateWithQuota(quotaApp("ea_p1c", "ea_p1", "c", now), limits)
	var pe *PendingLimitExceededError
	require.ErrorAs(t, err, &pe)
	require.Equal(t, 2, pe.Used)
	require.Equal(t, 2, pe.Limit)

	// 弾いたときは行を作らないこと。
	var n int64
	require.NoError(t, testDB.Model(&model.EmojiApplication{}).
		Where(`"userId" = ?`, "ea_p1").Count(&n).Error)
	require.Equal(t, int64(2), n)

	// **Used と Limit を取り違えないこと。** 上限を後から下げると
	// `Used > Limit` になる。同じ値だけを試していると入れ替えても気付けない。
	err = repo.CreateWithQuota(quotaApp("ea_p1d", "ea_p1", "d", now), QuotaLimits{MaxPending: 1})
	require.ErrorAs(t, err, &pe)
	require.Equal(t, 2, pe.Used)
	require.Equal(t, 1, pe.Limit)
}

// **却下・取り下げ・承認で枠が戻ること (#2977 の完了条件)。** 期間上限とは逆で、
// こちらが絞るのはモデレーターが見る一覧の長さなので、処理済みは数えない。
func TestEmojiApplicationRepository_CreateWithQuota_PendingLimitCountsOnlyPending(t *testing.T) {
	cleanupEmojiApplications(t)
	defer cleanupEmojiApplications(t)
	createTestUser(t, "ea_p2")
	repo := NewEmojiApplicationRepository(testDB)
	now := time.Now()
	for i, st := range []string{
		model.EmojiApplicationRejected,
		model.EmojiApplicationCanceled,
		model.EmojiApplicationApproved,
	} {
		seedApplicationAt(t, "ea_p2"+string(rune('a'+i)), "ea_p2",
			string(rune('a'+i)), st, now.Add(-time.Duration(i+1)*time.Hour))
	}

	// 処理済み 3 件があっても、審査待ちは 0 なので通る。
	require.NoError(t, repo.CreateWithQuota(
		quotaApp("ea_p2z", "ea_p2", "z", now), QuotaLimits{MaxPending: 1}))
}

// **数えるのは自分の分だけ。** 絞りを落とすと、誰かが上限まで積んだ時点で
// インスタンス全員が申請できなくなる。
func TestEmojiApplicationRepository_CreateWithQuota_PendingLimitScopedToUser(t *testing.T) {
	cleanupEmojiApplications(t)
	defer cleanupEmojiApplications(t)
	createTestUser(t, "ea_p3")
	createTestUser(t, "ea_p3other")
	repo := NewEmojiApplicationRepository(testDB)
	now := time.Now()
	seedApplicationAt(t, "ea_p3x", "ea_p3other", "x", model.EmojiApplicationPending, now)

	require.NoError(t, repo.CreateWithQuota(
		quotaApp("ea_p3a", "ea_p3", "a", now), QuotaLimits{MaxPending: 1}))
}

// **0 は無制限。** 既定値なので、ここが上限として効くと全インスタンスで申請が
// 塞がる。窓も pending も無ければ Create へ委譲する経路になる。
func TestEmojiApplicationRepository_CreateWithQuota_PendingLimitZeroIsUnlimited(t *testing.T) {
	cleanupEmojiApplications(t)
	defer cleanupEmojiApplications(t)
	createTestUser(t, "ea_p4")
	repo := NewEmojiApplicationRepository(testDB)
	now := time.Now()
	for i := 0; i < 3; i++ {
		require.NoError(t, repo.CreateWithQuota(
			quotaApp("ea_p4"+string(rune('a'+i)), "ea_p4", string(rune('a'+i)), now),
			QuotaLimits{MaxPending: 0}))
	}
}

// **自作画像とリモート絵文字は枠を共有する (#2977 の完了条件)。**
func TestEmojiApplicationRepository_CreateWithQuota_PendingLimitCountsBothKinds(t *testing.T) {
	cleanupEmojiApplications(t)
	defer cleanupEmojiApplications(t)
	createTestUser(t, "ea_p5")
	repo := NewEmojiApplicationRepository(testDB)
	now := time.Now()
	host, rname := "remote.example", "kusa"
	require.NoError(t, testDB.Create(&model.EmojiApplication{
		ID: "ea_p5r", UserID: "ea_p5", Kind: model.EmojiApplicationKindRemote,
		Status: model.EmojiApplicationPending, Name: "r", License: "取り込み元",
		RemoteHost: &host, RemoteName: &rname, CreatedAt: now, UpdatedAt: now,
	}).Error)

	err := repo.CreateWithQuota(quotaApp("ea_p5z", "ea_p5", "z", now), QuotaLimits{MaxPending: 1})
	var pe *PendingLimitExceededError
	require.ErrorAs(t, err, &pe)
	require.Equal(t, 1, pe.Used)
}

// **両方満杯なら期間上限を返すこと (#2977)。** 審査待ちを返すと「取り下げれば
// 出せる」と案内することになるが、**期間上限も満杯なら取り下げても通らない**。
// しかも期間上限は全ステータスを数えるので**取り下げた行は枠を占有したまま
// 戻らない** — 案内に従うと申請を 1 件失ったうえに枠も消費する。
//
// `maxPerDay = maxPending` は運営者がいちばん自然に置く設定なので、これは
// 例外的な状況ではない。
func TestEmojiApplicationRepository_CreateWithQuota_WindowBeatsPendingLimit(t *testing.T) {
	cleanupEmojiApplications(t)
	defer cleanupEmojiApplications(t)
	createTestUser(t, "ea_p6")
	repo := NewEmojiApplicationRepository(testDB)
	now := time.Now()
	seedApplicationAt(t, "ea_p6a", "ea_p6", "a", model.EmojiApplicationPending, now.Add(-time.Hour))

	limits := QuotaLimits{
		Windows:    []QuotaWindow{{Name: "day", Duration: 24 * time.Hour, Max: 1}},
		MaxPending: 1,
	}
	err := repo.CreateWithQuota(quotaApp("ea_p6z", "ea_p6", "z", now), limits)
	var qe *QuotaExceededError
	require.ErrorAs(t, err, &qe, "審査待ちが先に返っている")
	var pe *PendingLimitExceededError
	require.False(t, errors.As(err, &pe))

	// **`RetryAt` を出さないこと (#2977)。** 契約は「申請が通るようになる
	// 時刻」だが、審査待ちも満杯ならその時刻でも通らない。審査待ちが空く時刻は
	// モデレーター次第で予告できないので、嘘の時刻を広告しない。
	require.True(t, qe.RetryAt.IsZero(), "叩いても通らない時刻を広告している")

	// **取り下げても期間上限は空かないこと。** 「取り下げれば出せる」という
	// 案内が成り立たない根拠で、順序をこちらに倒している理由そのもの。
	require.NoError(t, testDB.Model(&model.EmojiApplication{}).
		Where(`"id" = ?`, "ea_p6a").Update("status", model.EmojiApplicationCanceled).Error)
	err = repo.CreateWithQuota(quotaApp("ea_p6z", "ea_p6", "z", now), limits)
	require.ErrorAs(t, err, &qe)
}

// **期間上限だけが満杯なら時刻を出すこと (#2977)。** 審査待ちに空きがあれば
// その時刻で本当に通るので、#2958 の契約どおり案内する。両方満杯のときだけ
// 時刻を落とすのであって、期間上限の案内そのものを弱めてはいけない。
func TestEmojiApplicationRepository_CreateWithQuota_WindowOnlyKeepsRetryAt(t *testing.T) {
	cleanupEmojiApplications(t)
	defer cleanupEmojiApplications(t)
	createTestUser(t, "ea_p9")
	repo := NewEmojiApplicationRepository(testDB)
	now := time.Now()
	// 期間上限は満杯 (1/1)、審査待ちは空き (処理済みなので 0/1)。
	seedApplicationAt(t, "ea_p9a", "ea_p9", "a", model.EmojiApplicationRejected, now.Add(-time.Hour))

	limits := QuotaLimits{
		Windows:    []QuotaWindow{{Name: "day", Duration: 24 * time.Hour, Max: 1}},
		MaxPending: 1,
	}
	err := repo.CreateWithQuota(quotaApp("ea_p9z", "ea_p9", "z", now), limits)
	var qe *QuotaExceededError
	require.ErrorAs(t, err, &qe)
	require.False(t, qe.RetryAt.IsZero(), "期間上限だけなら時刻を出せる")
	require.WithinDuration(t, now.Add(23*time.Hour), qe.RetryAt, time.Minute)

	// 案内した時刻に本当に通ること。
	require.NoError(t, repo.CreateWithQuota(quotaApp("ea_p9z", "ea_p9", "z", qe.RetryAt), limits))
}

// **審査待ちだけが満杯なら審査待ちを返すこと (#2977)。** 期間上限に空きが
// あるのに「いつ空くか」を案内すると、取り下げれば今すぐ出せることが伝わらない。
func TestEmojiApplicationRepository_CreateWithQuota_PendingLimitWhenWindowHasRoom(t *testing.T) {
	cleanupEmojiApplications(t)
	defer cleanupEmojiApplications(t)
	createTestUser(t, "ea_p8")
	repo := NewEmojiApplicationRepository(testDB)
	now := time.Now()
	seedApplicationAt(t, "ea_p8a", "ea_p8", "a", model.EmojiApplicationPending, now.Add(-time.Hour))

	limits := QuotaLimits{
		Windows:    []QuotaWindow{{Name: "day", Duration: 24 * time.Hour, Max: 10}},
		MaxPending: 1,
	}
	err := repo.CreateWithQuota(quotaApp("ea_p8z", "ea_p8", "z", now), limits)
	var pe *PendingLimitExceededError
	require.ErrorAs(t, err, &pe)

	// 取り下げれば今すぐ出せること (こちらは案内どおりに解決する)。
	require.NoError(t, testDB.Model(&model.EmojiApplication{}).
		Where(`"id" = ?`, "ea_p8a").Update("status", model.EmojiApplicationCanceled).Error)
	require.NoError(t, repo.CreateWithQuota(quotaApp("ea_p8z", "ea_p8", "z", now), limits))
}

// **同時に投げても審査待ちの上限を超えないこと (#2977 の完了条件)。**
func TestEmojiApplicationRepository_CreateWithQuota_PendingLimitConcurrent(t *testing.T) {
	cleanupEmojiApplications(t)
	defer cleanupEmojiApplications(t)
	createTestUser(t, "ea_p7")
	now := time.Now()
	limits := QuotaLimits{MaxPending: 3}

	const n = 8
	// 共有プールを触らない理由は _Concurrent と同じ。
	repos := make([]EmojiApplicationRepository, n)
	for i := range repos {
		db, err := testutil.OpenTestDB()
		require.NoError(t, err)
		sqlDB, err := db.DB()
		require.NoError(t, err)
		t.Cleanup(func() { _ = sqlDB.Close() })
		repos[i] = NewEmojiApplicationRepository(db)
	}

	var ready sync.WaitGroup
	ready.Add(n)
	start := make(chan struct{})
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func(i int) {
			var one int
			_ = repos[i].(*emojiApplicationRepository).db.Raw("SELECT 1").Scan(&one).Error
			ready.Done()
			<-start
			errs <- repos[i].CreateWithQuota(
				quotaApp("ea_p7"+string(rune('a'+i)), "ea_p7", string(rune('a'+i)), now), limits)
		}(i)
	}
	ready.Wait()
	close(start)

	var ok, rejected int
	for i := 0; i < n; i++ {
		err := <-errs
		var pe *PendingLimitExceededError
		switch {
		case err == nil:
			ok++
		case errors.As(err, &pe):
			rejected++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	require.Equal(t, 3, ok)
	require.Equal(t, n-3, rejected)

	var stored int64
	require.NoError(t, testDB.Model(&model.EmojiApplication{}).
		Where(`"userId" = ?`, "ea_p7").Count(&stored).Error)
	require.Equal(t, int64(3), stored)
}

// 番兵の文面が「審査待ち」を指すこと。
func TestPendingLimitExceededError_Message(t *testing.T) {
	err := &PendingLimitExceededError{Used: 3, Limit: 3}
	require.Contains(t, err.Error(), "awaiting review")
}
