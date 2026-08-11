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
	assert.Len(t, report.Results, 7, "全項目ぶんの結果が返る")
}

// warn だけなら OK は落とさない。「見ておくべき」と「壊れている」を区別する。
func TestReport_WarnDoesNotFailOverall(t *testing.T) {
	r := newReport([]Result{okResult("a", ""), warnResult("b", "", "hint")})
	assert.True(t, r.OK)

	r = newReport([]Result{okResult("a", ""), failResult("b", "", "hint")})
	assert.False(t, r.OK)
}
