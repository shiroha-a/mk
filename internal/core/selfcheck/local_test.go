package selfcheck

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 未配線の依存は skip。サーバー未起動でも config 検査だけ回せるようにするため、
// nil を fail にはしない。
func TestLocalChecks_SkipWhenUnwired(t *testing.T) {
	ctx := context.Background()
	assert.Equal(t, StatusSkip, CheckDatabase(ctx, LocalDeps{}).Status)
	assert.Equal(t, StatusSkip, CheckRedis(ctx, LocalDeps{}).Status)
}

func TestCheckRedis_OK(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	assert.Equal(t, StatusOK, CheckRedis(context.Background(), LocalDeps{Redis: rdb}).Status)
}

func TestCheckRedis_Unreachable(t *testing.T) {
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
	t.Cleanup(func() { _ = rdb.Close() })

	got := CheckRedis(context.Background(), LocalDeps{Redis: rdb})
	assert.Equal(t, StatusFail, got.Status)
	assert.NotEmpty(t, got.Hint)
}

func TestCheckDatabase_OK(t *testing.T) {
	db := testutil.MustOpenTestDB()
	testutil.ApplyMigrations(db)

	got := CheckDatabase(context.Background(), LocalDeps{DB: db})
	require.Equal(t, StatusOK, got.Status, got.Detail)
	assert.Contains(t, got.Detail, "migration version")
}

// migration の適用漏れは「起動はするが一部機能だけ壊れる」形で出るので、
// 接続できたことをもって正常とみなさない。
func TestCheckDatabase_DetectsMissingMigrations(t *testing.T) {
	db := testutil.MustOpenTestDB()
	testutil.ApplyMigrations(db)

	// 同梱本数を実際より多く見せかける = 適用漏れがある状態。
	got := CheckDatabase(context.Background(), LocalDeps{DB: db, MigrationCount: 1_000_000})
	assert.Equal(t, StatusFail, got.Status)
	assert.Contains(t, got.Hint, "migrate-up")
}

// Run は最初の失敗で打ち切らない。打ち切ると運用者が直しては走らせ直すことに
// なる。
func TestRun_ContinuesAfterFailure(t *testing.T) {
	srv := httptest.NewServer(nil)
	srv.Close() // 到達不能にする

	report := Run(context.Background(), NewChecker(srv.URL), LocalDeps{})
	assert.False(t, report.OK)
	assert.Len(t, report.Results, 8, "全項目ぶんの結果が返る")
}

// warn だけなら OK は落とさない。「見ておくべき」と「壊れている」を区別する。
func TestReport_WarnDoesNotFailOverall(t *testing.T) {
	r := newReport([]Result{okResult("a", ""), warnResult("b", "", "hint")})
	assert.True(t, r.OK)

	r = newReport([]Result{okResult("a", ""), failResult("b", "", "hint")})
	assert.False(t, r.OK)
}

// meta.rootUserId が未設定なら fail を返す。
//
// **未設定だと `admin/accounts/create` の初回セットアップ判定が、ローカル利用者数の
// ガードだけに依存する状態になる。** 運用者がそれに気付ける手段が他に無い。
func TestCheckRootUser(t *testing.T) {
	t.Run("DB 未配線は skip", func(t *testing.T) {
		got := CheckRootUser(context.Background(), LocalDeps{})
		assert.Equal(t, StatusSkip, got.Status)
	})

	// **クエリ本体まで実 DB で踏む。** skip 枝しか通らないテストだと、
	// 壊れても誰も気付かない部分が未実行のまま残る (同パッケージの
	// `TestCheckDatabase_OK` は既に実 DB を使っている)。
	db := testutil.MustOpenTestDB()
	testutil.ApplyMigrations(db)
	// **meta 行が無いと全部 fail になってしまう。** migration は行を作らず、
	// 本番では `EnsureInitial` が起動時に作る。
	require.NoError(t, db.Exec(`INSERT INTO meta (id) VALUES ('x') ON CONFLICT DO NOTHING`).Error)

	t.Run("rootUserId 未設定なら fail", func(t *testing.T) {
		require.NoError(t, db.Exec(`UPDATE meta SET "rootUserId" = NULL`).Error)
		got := CheckRootUser(context.Background(), LocalDeps{DB: db})
		assert.Equal(t, StatusFail, got.Status)
		assert.Contains(t, got.Hint, "update-meta")
	})

	t.Run("空文字も fail", func(t *testing.T) {
		require.NoError(t, db.Exec(`UPDATE meta SET "rootUserId" = ''`).Error)
		assert.Equal(t, StatusFail, CheckRootUser(context.Background(), LocalDeps{DB: db}).Status)
	})

	t.Run("設定済みなら ok", func(t *testing.T) {
		require.NoError(t, db.Exec(`UPDATE meta SET "rootUserId" = 'someroot'`).Error)
		t.Cleanup(func() { db.Exec(`UPDATE meta SET "rootUserId" = NULL`) })
		got := CheckRootUser(context.Background(), LocalDeps{DB: db})
		assert.Equal(t, StatusOK, got.Status)
		assert.Contains(t, got.Detail, "rootUserId")
	})

	// **読めないときは fail。** 「未設定」と区別が付かないので、判定できない
	// ことを ok に倒さない。
	t.Run("meta を読めないなら fail", func(t *testing.T) {
		broken := testutil.MustOpenTestDB()
		testutil.ApplyMigrations(broken)
		require.NoError(t, broken.Exec(`ALTER TABLE meta RENAME TO meta_selfcheck_tmp`).Error)
		t.Cleanup(func() { broken.Exec(`ALTER TABLE meta_selfcheck_tmp RENAME TO meta`) })

		got := CheckRootUser(context.Background(), LocalDeps{DB: broken})
		assert.Equal(t, StatusFail, got.Status)
	})
}
