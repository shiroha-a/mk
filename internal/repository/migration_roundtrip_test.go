package repository

import (
	"fmt"
	"net/url"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	// pgx5 driver と file source は本番の cmd/migrate と同じものを使う。
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/stretchr/testify/require"

	"github.com/shiroha-a/mk/internal/testutil"
)

// migrateURL builds the golang-migrate URL for a dedicated schema.
//
// 既定値は `testutil.testDSN` と揃えてある。ずれると「手元だけ別の DB を叩く」
// 状態になり、しかも接続できてしまうので気付けない。
func migrateURL(schema string) string {
	return fmt.Sprintf("pgx5://%s:%s@%s:%s/%s?sslmode=%s&search_path=%s",
		url.QueryEscape(testutil.EnvOrDefault("TEST_DB_USER", "mk")),
		url.QueryEscape(testutil.EnvOrDefault("TEST_DB_PASS", "mk")),
		testutil.EnvOrDefault("TEST_DB_HOST", "localhost"),
		testutil.EnvOrDefault("TEST_DB_PORT", "5432"),
		url.PathEscape(testutil.EnvOrDefault("TEST_DB_NAME", "misskey_test")),
		url.QueryEscape(testutil.EnvOrDefault("TEST_DB_SSLMODE", "disable")),
		url.QueryEscape(schema),
	)
}

// runMigrate runs fn and treats ErrNoChange as success.
func runMigrate(fn func() error) error {
	if err := fn(); err != nil && err != migrate.ErrNoChange {
		return err
	}
	return nil
}

// **down が実際に戻せることを検査する。**
//
// `make migrate-down` は運用手順に載っていて、CLAUDE.md も「down スクリプトは必ず
// 書く」と要求しているのに、**down が SQL として通ることを確かめるものが無かった**。
// down は書いた時点でしか実行されないので、後から up 側だけ直して対応が崩れても
// 誰も気付けない。壊れているのは**戻したくなった当日**に分かる。
//
// **`testutil.ApplyMigrations` では代用できない。** あちらは冪等な DDL のために
// `db.Exec` のエラーを握り潰す (`continue`) ので、壊れた SQL でも緑になる。ここは
// 本番の `cmd/migrate` と同じ golang-migrate + pgx5 driver に流す。
//
// **専用の schema を使う。** `internal/repository` の schema でやると、down が
// 他のテストの前提にしているテーブルを消す (#2450)。`OpenTestDBSchema` の兄弟
// schema なら閉じている。
//
// 往復にするのが要点で、**down のあとにもう一度 up が通ること**まで見る。down が
// 一部だけ戻して残骸を置いていくと、2 回目の up が「既に在る」で落ちる。
func TestMigrations_UpDownUpRoundTrip(t *testing.T) {
	db, err := testutil.OpenTestDBSchema("migrateroundtrip")
	require.NoError(t, err, "専用 schema を開けない")

	var schema string
	require.NoError(t, db.Raw("SELECT current_schema()").Scan(&schema).Error)
	require.NotEmpty(t, schema, "search_path が効いていない")

	// **毎回 schema を作り直す。** 途中で落ちると残骸が残り、**次の実行が別の理由で
	// 落ちて診断が事実と無関係になる** (実際に変異検証でそうなった)。golang-migrate は
	// version を `schema_migrations` で持つので、中途半端な状態だと起点が狂う。
	// schema 名は `OpenTestDBSchema` が `[a-z0-9_]` へ正規化済み。
	require.NoError(t, db.Exec(`DROP SCHEMA IF EXISTS "`+schema+`" CASCADE`).Error)
	require.NoError(t, db.Exec(`CREATE SCHEMA "`+schema+`"`).Error)

	m, err := migrate.New("file://../../migration", migrateURL(schema))
	require.NoError(t, err, "migrator を作れない")
	defer func() {
		serr, derr := m.Close()
		require.NoError(t, serr)
		require.NoError(t, derr)
	}()

	require.NoError(t, runMigrate(m.Up), "1 回目の up")

	v1, dirty, err := m.Version()
	require.NoError(t, err, "up 後の version を読めない")
	require.False(t, dirty, "up 後に dirty (途中で失敗した migration がある)")

	require.NoError(t, runMigrate(m.Down), "down — **全ての down が SQL として通るか**")

	_, _, err = m.Version()
	require.ErrorIs(t, err, migrate.ErrNilVersion, "down しきれていない")

	require.NoError(t, runMigrate(m.Up), "2 回目の up — down が残骸を置いていると落ちる")

	v2, dirty, err := m.Version()
	require.NoError(t, err)
	require.False(t, dirty, "2 回目の up 後に dirty")
	require.Equal(t, v1, v2, "往復で version が変わった")
}
