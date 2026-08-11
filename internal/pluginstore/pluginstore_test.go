package pluginstore

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shiroha-a/mk/internal/testutil"
)

// baseDSN mirrors internal/testutil's connection settings so this package talks
// to the same test database as everything else.
func baseDSN() string {
	return fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=%s",
		testutil.EnvOrDefault("TEST_DB_HOST", "localhost"),
		testutil.EnvOrDefault("TEST_DB_PORT", "5432"),
		testutil.EnvOrDefault("TEST_DB_USER", "mk"),
		testutil.EnvOrDefault("TEST_DB_PASS", "mk"),
		testutil.EnvOrDefault("TEST_DB_NAME", "misskey_test"),
		testutil.EnvOrDefault("TEST_DB_SSLMODE", "disable"))
}

// openStore opens a store for name and drops its schema afterwards.
//
// **後片付けを必ず行う。** schema は DB 全体で共有される名前空間なので、残すと
// 次の実行が「既に適用済み」の状態から始まって結果が変わる。
func openStore(t *testing.T, name string) *Store {
	t.Helper()
	s, err := Open(baseDSN(), name, 2)
	require.NoError(t, err)
	t.Cleanup(func() {
		schema, _ := SchemaName(name)
		admin, err := sql.Open("pgx", baseDSN())
		if err == nil {
			_, _ = admin.Exec(`DROP SCHEMA IF EXISTS ` + quoteIdent(schema) + ` CASCADE`)
			_ = admin.Close()
		}
		_ = s.Close()
	})
	return s
}

func TestSchemaName(t *testing.T) {
	got, err := SchemaName("gameinfo")
	require.NoError(t, err)
	assert.Equal(t, "plugin_gameinfo", got)

	// ハイフンは引用が必要になるので _ に倒す。
	got, err = SchemaName("game-info")
	require.NoError(t, err)
	assert.Equal(t, "plugin_game_info", got)
}

// **identifier は placeholder にできないので、埋め込む前に必ず検証する。**
func TestSchemaName_Rejects(t *testing.T) {
	for _, bad := range []string{
		"", "A", "game_info", "-lead", "trail-", "a/b", "a.b",
		`x"; DROP SCHEMA public; --`,
		"ドライブ",
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", // 33 文字
	} {
		_, err := SchemaName(bad)
		assert.Errorf(t, err, "%q は拒否される", bad)
	}
}

func TestOpen_CreatesSchema(t *testing.T) {
	s := openStore(t, "storetest")

	assert.Equal(t, "plugin_storetest", s.Schema())
	require.NoError(t, s.DB().Ping())

	var n int
	require.NoError(t, s.DB().QueryRow(
		`SELECT count(*) FROM information_schema.schemata WHERE schema_name = $1`,
		s.Schema()).Scan(&n))
	assert.Equal(t, 1, n)
}

// 2 回開いても失敗しない (再起動のたびに呼ばれる)。
func TestOpen_IsIdempotent(t *testing.T) {
	s1 := openStore(t, "storetwice")
	s2, err := Open(baseDSN(), "storetwice", 2)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s2.Close() })

	assert.Equal(t, s1.Schema(), s2.Schema())
}

func TestOpen_RejectsBadName(t *testing.T) {
	_, err := Open(baseDSN(), "BAD", 0)
	require.Error(t, err)
}

func TestOpen_ReportsConnectionFailure(t *testing.T) {
	_, err := Open("host=127.0.0.1 port=1 user=x password=x dbname=x sslmode=disable", "x", 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "schema")
}

// **search_path が自分の schema に固定されていること。** 素直に書いたら本体の
// テーブルに触れない、が守れているかの確認。
func TestOpen_SearchPathIsPinned(t *testing.T) {
	s := openStore(t, "storepath")

	var sp string
	require.NoError(t, s.DB().QueryRow(`SHOW search_path`).Scan(&sp))
	assert.Contains(t, sp, s.Schema())

	// 修飾なしで作った表は自分の schema に入る。
	_, err := s.DB().Exec(`CREATE TABLE t (id int)`)
	require.NoError(t, err)

	var schema string
	require.NoError(t, s.DB().QueryRow(
		`SELECT table_schema FROM information_schema.tables WHERE table_name = 't'`).Scan(&schema))
	assert.Equal(t, s.Schema(), schema, "本体の public ではなく自分の schema に作られる")
}

// --- Migrate ---

func TestMigrate_AppliesInOrder(t *testing.T) {
	s := openStore(t, "storemig")
	ctx := context.Background()

	require.NoError(t, s.Migrate(ctx, []Migration{
		// 宣言順を逆にしても version 順に適用されること。
		{Version: 2, SQL: `ALTER TABLE items ADD COLUMN note text`},
		{Version: 1, SQL: `CREATE TABLE items (id serial PRIMARY KEY)`},
	}))

	var cols int
	require.NoError(t, s.DB().QueryRow(
		`SELECT count(*) FROM information_schema.columns WHERE table_schema = $1 AND table_name = 'items'`,
		s.Schema()).Scan(&cols))
	assert.Equal(t, 2, cols)
}

// 適用済みのものは飛ばす。起動のたびに呼ばれるので、これが効かないと 2 回目で
// 失敗する。
func TestMigrate_SkipsApplied(t *testing.T) {
	s := openStore(t, "storeskip")
	ctx := context.Background()
	ms := []Migration{{Version: 1, SQL: `CREATE TABLE a (id int)`}}

	require.NoError(t, s.Migrate(ctx, ms))
	require.NoError(t, s.Migrate(ctx, ms), "2 回目は飛ばす")

	// 追加分だけが適用される。
	require.NoError(t, s.Migrate(ctx, append(ms, Migration{Version: 2, SQL: `CREATE TABLE b (id int)`})))

	var n int
	require.NoError(t, s.DB().QueryRow(
		`SELECT count(*) FROM information_schema.tables WHERE table_schema = $1 AND table_name IN ('a','b')`,
		s.Schema()).Scan(&n))
	assert.Equal(t, 2, n)
}

// **失敗した migration は記録しない。** 半分だけ適用された状態で「適用済み」に
// なると、次回の起動で復旧できない。
func TestMigrate_FailedMigrationIsNotRecorded(t *testing.T) {
	s := openStore(t, "storefail")
	ctx := context.Background()

	err := s.Migrate(ctx, []Migration{{Version: 1, SQL: `CREATE TABLE ok (id int); SELECT bad_function()`}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "migration 1")

	// 記録されていないので、直した SQL で再実行できる。
	require.NoError(t, s.Migrate(ctx, []Migration{{Version: 1, SQL: `CREATE TABLE ok (id int)`}}))

	var n int
	require.NoError(t, s.DB().QueryRow(
		`SELECT count(*) FROM information_schema.tables WHERE table_schema = $1 AND table_name = 'ok'`,
		s.Schema()).Scan(&n))
	assert.Equal(t, 1, n)
}

func TestMigrate_EmptyIsNoop(t *testing.T) {
	s := openStore(t, "storeempty")
	require.NoError(t, s.Migrate(context.Background(), nil))

	// 管理表すら作らない (無駄な DDL を打たない)。
	var n int
	require.NoError(t, s.DB().QueryRow(
		`SELECT count(*) FROM information_schema.tables WHERE table_schema = $1`,
		s.Schema()).Scan(&n))
	assert.Zero(t, n)
}

// 複数プロセスが同時に起動しても壊れないこと (role 分割 #2459)。
func TestMigrate_ConcurrentIsSerialized(t *testing.T) {
	s := openStore(t, "storeconc")
	ctx := context.Background()
	ms := []Migration{{Version: 1, SQL: `CREATE TABLE only_once (id int)`}}

	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			other, err := Open(baseDSN(), "storeconc", 2)
			if err != nil {
				errs[i] = err
				return
			}
			defer other.Close() //nolint:errcheck // テスト内の後片付け
			errs[i] = other.Migrate(ctx, ms)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		assert.NoErrorf(t, err, "%d 番目", i)
	}

	var applied int
	require.NoError(t, s.DB().QueryRow(`SELECT count(*) FROM schema_migrations`).Scan(&applied))
	assert.Equal(t, 1, applied, "重複して適用されない")
}

func TestValidateMigrations(t *testing.T) {
	_, err := validateMigrations([]Migration{{Version: 0, SQL: "x"}})
	assert.ErrorContains(t, err, "1 以上")

	_, err = validateMigrations([]Migration{{Version: 1, SQL: "  "}})
	assert.ErrorContains(t, err, "SQL が空")

	// 重複した version を許すと、片方だけ適用されて記録され、もう片方が
	// 永久に流れない。
	_, err = validateMigrations([]Migration{{Version: 1, SQL: "a"}, {Version: 1, SQL: "b"}})
	assert.ErrorContains(t, err, "重複")

	got, err := validateMigrations([]Migration{{Version: 3, SQL: "c"}, {Version: 1, SQL: "a"}})
	require.NoError(t, err)
	assert.Equal(t, 1, got[0].Version)
	assert.Equal(t, 3, got[1].Version)
}

func TestMigrate_RejectsInvalidSet(t *testing.T) {
	s := openStore(t, "storeinvalid")
	err := s.Migrate(context.Background(), []Migration{{Version: 1, SQL: "a"}, {Version: 1, SQL: "b"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "storeinvalid")
}

// version の記録に失敗したら migration 全体を失敗させる。SQL だけ通って記録が
// 落ちると、次回に再実行されて二重適用になる。
func TestApplyOne_RecordFailureRollsBack(t *testing.T) {
	s := openStore(t, "storerecord")
	ctx := context.Background()

	// 先に管理表を version の型が合わない形で作っておき、記録だけを失敗させる。
	_, err := s.DB().Exec(`CREATE TABLE schema_migrations (version text PRIMARY KEY, applied_at timestamptz)`)
	require.NoError(t, err)
	_, err = s.DB().Exec(`ALTER TABLE schema_migrations ADD CONSTRAINT only_words CHECK (version ~ '^[a-z]+$')`)
	require.NoError(t, err)

	err = s.Migrate(ctx, []Migration{{Version: 1, SQL: `CREATE TABLE never_kept (id int)`}})
	require.Error(t, err)

	// SQL 側もロールバックされていること。
	var n int
	require.NoError(t, s.DB().QueryRow(
		`SELECT count(*) FROM information_schema.tables WHERE table_schema = $1 AND table_name = 'never_kept'`,
		s.Schema()).Scan(&n))
	assert.Zero(t, n, "記録に失敗したら SQL も残さない")
}

// 管理表が読めない形になっていたらエラーにする (握り潰して「未適用」と
// 見なすと、適用済みのものを再実行してしまう)。
func TestMigrate_UnreadableVersionTableIsError(t *testing.T) {
	s := openStore(t, "storebadtable")
	ctx := context.Background()

	// version が読めない型の管理表を先に作る。
	_, err := s.DB().Exec(`CREATE TABLE schema_migrations (version jsonb PRIMARY KEY, applied_at timestamptz)`)
	require.NoError(t, err)
	_, err = s.DB().Exec(`INSERT INTO schema_migrations (version) VALUES ('{"a":1}')`)
	require.NoError(t, err)

	err = s.Migrate(ctx, []Migration{{Version: 1, SQL: `SELECT 1`}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "適用済み migration を読めません")
}

// context がキャンセル済みなら接続取得の時点で失敗する。
func TestMigrate_RespectsContext(t *testing.T) {
	s := openStore(t, "storectx")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := s.Migrate(ctx, []Migration{{Version: 1, SQL: `SELECT 1`}})
	require.Error(t, err)
}

func TestAdvisoryLockKey(t *testing.T) {
	// 同じ schema なら同じ鍵、違えば違う鍵。
	assert.Equal(t, advisoryLockKey("plugin_a"), advisoryLockKey("plugin_a"))
	assert.NotEqual(t, advisoryLockKey("plugin_a"), advisoryLockKey("plugin_b"))
	assert.GreaterOrEqual(t, advisoryLockKey("plugin_a"), int64(0), "ログで読めるよう正の値にする")
}

func TestQuoteIdent(t *testing.T) {
	assert.Equal(t, `"plugin_x"`, quoteIdent("plugin_x"))
	assert.Equal(t, `"a""b"`, quoteIdent(`a"b`))
}

func TestClose_NilIsSafe(t *testing.T) {
	assert.NoError(t, (&Store{}).Close())
}

func TestMigrate_ReportsClosedPool(t *testing.T) {
	s, err := Open(baseDSN(), "storeclosed", 1)
	require.NoError(t, err)
	require.NoError(t, s.Close())

	err = s.Migrate(context.Background(), []Migration{{Version: 1, SQL: "SELECT 1"}})
	require.Error(t, err)
}
