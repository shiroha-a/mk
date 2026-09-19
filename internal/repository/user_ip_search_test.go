package repository

import (
	"sync"
	"testing"
	"time"

	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// ipSearchDB は検索テスト専用の兄弟 schema。
//
// **`HasAnyHistory` がテーブル全体の絶対的な事実を返す**ので、同じパッケージの
// 他テストが残した `user_ip` の行が混ざると期待値が成立しない (CLAUDE.md
// 「DB を使うテストの分離」)。専用 schema を一度だけ作って使い回す。
var (
	ipSearchDB    *gorm.DB
	ipSearchOnce  sync.Once
	ipSearchDBErr error
)

func ipSearchTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	ipSearchOnce.Do(func() {
		db, err := testutil.OpenTestDBSchema("ipsearch")
		if err != nil {
			ipSearchDBErr = err
			return
		}
		testutil.ApplyMigrations(db)
		ipSearchDB = db
	})
	require.NoError(t, ipSearchDBErr)
	require.NotNil(t, ipSearchDB)
	return ipSearchDB
}

func resetIPSearchFixtures(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NoError(t, db.Exec(`DELETE FROM "user_ip"`).Error)
}

// observeAt inserts one observation directly so the test controls the times.
func observeAt(t *testing.T, db *gorm.DB, userID, ip string, first, last time.Time, count int) {
	t.Helper()
	require.NoError(t, db.Create(&model.UserIP{
		UserID: userID, IP: ip,
		CreatedAt: first, LastSeenAt: last, ObservationCount: count,
	}).Error)
}

func userIDsOfRows(rows []UserIPAccountRow) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.UserID)
	}
	return out
}

// 並びは最終観測の降順 → 初回観測の降順 → userId 昇順。
//
// **`userId` の tiebreaker が要る。** 同じ時刻の行で順序が実行ごとに変わると、
// offset で 2 ページ目を引いたときに同じ行が 2 回出たり抜けたりする。
func TestUserIPSearch_ListAccountsByIPOrder(t *testing.T) {
	db := ipSearchTestDB(t)
	resetIPSearchFixtures(t, db)
	repo := NewUserIPSearchRepository(db)

	now := time.Now().Truncate(time.Millisecond)
	// 最終観測が新しい順に並ぶ。
	observeAt(t, db, "u_old", "192.0.2.1", now.Add(-72*time.Hour), now.Add(-48*time.Hour), 1)
	observeAt(t, db, "u_new", "192.0.2.1", now.Add(-24*time.Hour), now.Add(-time.Hour), 2)
	// 最終観測が同じ 2 件。初回観測が新しい方が先。
	observeAt(t, db, "u_tie_b", "192.0.2.1", now.Add(-10*time.Hour), now.Add(-2*time.Hour), 1)
	observeAt(t, db, "u_tie_a", "192.0.2.1", now.Add(-5*time.Hour), now.Add(-2*time.Hour), 1)
	// 最終観測も初回観測も同じ 2 件。userId 昇順。
	observeAt(t, db, "u_zz", "192.0.2.1", now.Add(-3*time.Hour), now.Add(-3*time.Hour), 1)
	observeAt(t, db, "u_aa", "192.0.2.1", now.Add(-3*time.Hour), now.Add(-3*time.Hour), 1)
	// 別の IP は混ざらない。
	observeAt(t, db, "u_other", "198.51.100.1", now, now, 1)

	rows, err := repo.ListAccountsByIP("192.0.2.1", now.Add(-90*24*time.Hour), 50, 0)
	require.NoError(t, err)
	assert.Equal(t,
		[]string{"u_new", "u_tie_a", "u_tie_b", "u_aa", "u_zz", "u_old"},
		userIDsOfRows(rows))
}

// 観測の値をそのまま返す。
func TestUserIPSearch_ReturnsObservationFields(t *testing.T) {
	db := ipSearchTestDB(t)
	resetIPSearchFixtures(t, db)
	repo := NewUserIPSearchRepository(db)

	first := time.Now().Add(-48 * time.Hour).Truncate(time.Millisecond)
	last := time.Now().Add(-time.Hour).Truncate(time.Millisecond)
	observeAt(t, db, "u1", "192.0.2.1", first, last, 7)

	rows, err := repo.ListAccountsByIP("192.0.2.1", first.Add(-time.Hour), 10, 0)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "u1", rows[0].UserID)
	assert.WithinDuration(t, first, rows[0].FirstSeen, time.Millisecond)
	assert.WithinDuration(t, last, rows[0].LastSeen, time.Millisecond)
	assert.Equal(t, 7, rows[0].ObservationCount)
}

// **窓は最終観測で切る。** 初回観測で切ると「初めて見たのは窓の外だが今も
// 使っている」候補が落ちる (#3103 の保持と同じ理由)。
func TestUserIPSearch_WindowFiltersByLastSeen(t *testing.T) {
	db := ipSearchTestDB(t)
	resetIPSearchFixtures(t, db)
	repo := NewUserIPSearchRepository(db)

	now := time.Now().Truncate(time.Millisecond)
	// 初回は窓の外だが最終は窓の中 → 出る。
	observeAt(t, db, "u_live", "192.0.2.1", now.Add(-100*24*time.Hour), now.Add(-time.Hour), 2)
	// 初回も最終も窓の外 → 出ない。
	observeAt(t, db, "u_stale", "192.0.2.1", now.Add(-100*24*time.Hour), now.Add(-95*24*time.Hour), 1)

	rows, err := repo.ListAccountsByIP("192.0.2.1", now.Add(-90*24*time.Hour), 10, 0)
	require.NoError(t, err)
	assert.Equal(t, []string{"u_live"}, userIDsOfRows(rows))
}

// limit と offset が効く。効かないと画面が無制限に行を受け取る。
func TestUserIPSearch_LimitAndOffset(t *testing.T) {
	db := ipSearchTestDB(t)
	resetIPSearchFixtures(t, db)
	repo := NewUserIPSearchRepository(db)

	now := time.Now().Truncate(time.Millisecond)
	for i := 0; i < 5; i++ {
		// 最終観測をずらして順序を決定的にする。
		observeAt(t, db, string(rune('a'+i)), "192.0.2.1",
			now.Add(-time.Duration(i+1)*time.Hour), now.Add(-time.Duration(i+1)*time.Hour), 1)
	}
	since := now.Add(-90 * 24 * time.Hour)

	page1, err := repo.ListAccountsByIP("192.0.2.1", since, 2, 0)
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b"}, userIDsOfRows(page1))

	page2, err := repo.ListAccountsByIP("192.0.2.1", since, 2, 2)
	require.NoError(t, err)
	assert.Equal(t, []string{"c", "d"}, userIDsOfRows(page2))

	// 端を越えたら空。
	beyond, err := repo.ListAccountsByIP("192.0.2.1", since, 2, 10)
	require.NoError(t, err)
	assert.Empty(t, beyond)
}

// 引数の丸め。limit <= 0 を 0 のまま渡すと `LIMIT 0` で常に空になる。
func TestUserIPSearch_ClampsArguments(t *testing.T) {
	db := ipSearchTestDB(t)
	resetIPSearchFixtures(t, db)
	repo := NewUserIPSearchRepository(db)

	now := time.Now().Truncate(time.Millisecond)
	observeAt(t, db, "u1", "192.0.2.1", now, now, 1)
	since := now.Add(-time.Hour)

	rows, err := repo.ListAccountsByIP("192.0.2.1", since, 0, 0)
	require.NoError(t, err)
	assert.Len(t, rows, 1, "limit <= 0 が 1 に丸められていない")

	rows, err = repo.ListAccountsByIP("192.0.2.1", since, 10, -5)
	require.NoError(t, err)
	assert.Len(t, rows, 1, "offset < 0 が 0 に丸められていない")
}

// 一致が無ければ空。エラーにしない (「一致なし」は正常な結果)。
func TestUserIPSearch_NoMatchIsEmpty(t *testing.T) {
	db := ipSearchTestDB(t)
	resetIPSearchFixtures(t, db)

	rows, err := NewUserIPSearchRepository(db).ListAccountsByIP(
		"203.0.113.77", time.Now().Add(-24*time.Hour), 10, 0)
	require.NoError(t, err)
	assert.Empty(t, rows)
}

// **「一致なし」と「そもそも記録が無い」を区別する材料。**
func TestUserIPSearch_HasAnyHistory(t *testing.T) {
	db := ipSearchTestDB(t)
	resetIPSearchFixtures(t, db)
	repo := NewUserIPSearchRepository(db)

	has, err := repo.HasAnyHistory()
	require.NoError(t, err)
	assert.False(t, has, "行が無いのに記録ありと答えている")

	// 窓の外の行しか無くても「記録はある」。記録の有無と窓は別の問い。
	old := time.Now().Add(-365 * 24 * time.Hour)
	observeAt(t, db, "u1", "192.0.2.1", old, old, 1)

	has, err = repo.HasAnyHistory()
	require.NoError(t, err)
	assert.True(t, has)
}

// SQL が壊れていれば err で返る (空の結果にしない)。
func TestUserIPSearch_PropagatesError(t *testing.T) {
	db := ipSearchTestDB(t)
	tx := db.Begin()
	require.NoError(t, tx.Error)
	defer tx.Rollback()
	require.NoError(t, tx.Exec(`SET LOCAL search_path TO pg_temp`).Error)
	repo := NewUserIPSearchRepository(tx)

	rows, err := repo.ListAccountsByIP("192.0.2.1", time.Now(), 10, 0)
	require.Error(t, err)
	assert.Nil(t, rows)
	assert.Contains(t, err.Error(), "user_ip accounts by ip")

	has, err := repo.HasAnyHistory()
	require.Error(t, err)
	assert.False(t, has)
	assert.Contains(t, err.Error(), "user_ip has any history")
}
