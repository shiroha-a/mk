package repository

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
	"gorm.io/plugin/dbresolver"

	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/testutil"
)

// TestExistingNoteIDsOnPrimary_ReadsPrimaryNotReplica は #2719 の要を固定する。
//
// antenna の宙吊り ID 除去は「DB に無い」を根拠に破壊的な操作をする。通常の
// SELECT はレプリカに振られるので、複製前の行を「無い」と誤判定すると生きて
// いる note の ID が恒久的に消える。この経路だけは primary を見なければ
// ならない。
//
// **レプリカ遅延を schema で模す。** 同じ DB 内に replica 相当の空 schema を
// 作り、そこを dbresolver の Replicas として登録する。primary にだけ行がある
// 状態で、素の SELECT が空振りし ExistingNoteIDsOnPrimary が引けることを見る。
func TestExistingNoteIDsOnPrimary_ReadsPrimaryNotReplica(t *testing.T) {
	replicaSchema := "repo_primary_probe_replica"
	require.NoError(t, testDB.Exec("CREATE SCHEMA IF NOT EXISTS "+replicaSchema).Error)
	t.Cleanup(func() { testDB.Exec("DROP SCHEMA IF EXISTS " + replicaSchema + " CASCADE") })
	// replica 側には note テーブルだけ作り、行は入れない (= 複製がまだ届いて
	// いない状態)。
	require.NoError(t, testDB.Exec(fmt.Sprintf(
		`CREATE TABLE IF NOT EXISTS %s."note" (LIKE "note" INCLUDING ALL)`, replicaSchema)).Error)

	// testDB は呼び出し元パッケージ専用 schema に pin されている (#2450)。
	// primary 側はそれと同じ schema を見なければならない。
	var primarySchema string
	require.NoError(t, testDB.Raw("SELECT current_schema()").Scan(&primarySchema).Error)
	gdb := openWithReplicaSchema(t, primarySchema, replicaSchema)
	repo := &noteRepository{db: gdb}

	testDB.Exec(`DELETE FROM "user" WHERE id = ?`, "primprobeu01")
	u := insertTestUser(t, "primprobeu01", "primprobeu01")
	t.Cleanup(func() { testDB.Exec(`DELETE FROM "user" WHERE id = ?`, u.ID) })
	id := "9primaryprobe0000001"
	require.NoError(t, testDB.Create(&model.Note{
		ID: id, UserID: u.ID, Visibility: model.NoteVisibilityPublic,
	}).Error)
	t.Cleanup(func() { testDB.Exec(`DELETE FROM "note" WHERE id = ?`, id) })

	// 素の SELECT は replica に振られるので引けない (= 複製遅延の再現)。
	var replicaSide []*model.Note
	require.NoError(t, gdb.Where("id IN ?", []string{id}).Find(&replicaSide).Error)
	require.Empty(t, replicaSide, "テストの前提: 通常の SELECT は replica を見て空振りする")

	// primary 固定なら引ける。
	got, err := repo.ExistingNoteIDsOnPrimary([]string{id})
	require.NoError(t, err)
	assert.Equal(t, []string{id}, got, "primary に在る行を「無い」と誤判定しない")
}

// TestExistingNoteIDsOnPrimary_Subset は返す集合を固定する。
func TestExistingNoteIDsOnPrimary_Subset(t *testing.T) {
	repo := NewNoteRepository(testDB)
	testDB.Exec(`DELETE FROM "user" WHERE id = ?`, "primsubsetu1")
	u := insertTestUser(t, "primsubsetu1", "primsubsetu1")
	t.Cleanup(func() { testDB.Exec(`DELETE FROM "user" WHERE id = ?`, u.ID) })
	id := "9primarysubset000001"
	require.NoError(t, testDB.Create(&model.Note{
		ID: id, UserID: u.ID, Visibility: model.NoteVisibilityPublic,
	}).Error)
	t.Cleanup(func() { testDB.Exec(`DELETE FROM "note" WHERE id = ?`, id) })

	got, err := repo.ExistingNoteIDsOnPrimary([]string{id, "9primarysubsetgone01"})
	require.NoError(t, err)
	assert.Equal(t, []string{id}, got, "在るものだけ返す")

	got, err = repo.ExistingNoteIDsOnPrimary(nil)
	require.NoError(t, err)
	assert.Empty(t, got, "空入力ではクエリを撃たない")
}

// openWithReplicaSchema は primary = 既定 schema、replica = 指定 schema として
// dbresolver を登録した *gorm.DB を返す。
func openWithReplicaSchema(t *testing.T, primarySchema, replicaSchema string) *gorm.DB {
	t.Helper()
	primaryDSN := testutil.TestDSNForSchema(primarySchema)
	replicaDSN := testutil.TestDSNForSchema(replicaSchema)

	// testutil.openDSN と同じ設定にする。PreferSimpleProtocol は #1089
	// (`cached plan must not change result type`) 対策で、同じ DB に
	// ApplyMigrations が additive DDL を回すこの環境では外す理由が無い。
	gdb, err := gorm.Open(postgres.New(postgres.Config{
		DSN: primaryDSN, PreferSimpleProtocol: true,
	}), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	require.NoError(t, err)
	t.Cleanup(func() {
		if sqlDB, err := gdb.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	require.NoError(t, gdb.Use(dbresolver.Register(dbresolver.Config{
		Replicas: []gorm.Dialector{postgres.New(postgres.Config{
			DSN: replicaDSN, PreferSimpleProtocol: true,
		})},
		Policy: dbresolver.RandomPolicy{},
	})))
	return gdb
}
