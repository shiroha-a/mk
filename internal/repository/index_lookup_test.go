package repository

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// indexDef returns the definition of the named index **in the caller's schema**.
//
// **`schemaname` で絞ること (#2777)。** `pg_indexes` は schema を跨いで全件返す。
// テスト DB は #2450 でパッケージごとに schema を分けているので、同名の index が
// 多数の schema に同時に存在する (実測: `clip_favorite` / `drive_file` が 17
// schema、`note` が 19 schema)。絞らないと 2 つ壊れる:
//
//   - **他 schema の index を自分のものと取り違える。** 自分の schema で
//     migration が適用されていなくても、他 schema の同名 index が返って
//     テストが緑になる (regression guard が空振りする)
//   - **他パッケージの `ApplyMigrations` と競合して落ちる。** `go test` は
//     パッケージのテストバイナリを並行実行するので、view が定義を取りに行った
//     relation が既に消えていると
//     `could not open relation with OID (SQLSTATE XX000)` になる。CI の shard は
//     PostgreSQL を 1 つしか立てないので手元と同じ条件が揃い、required check の
//     `test` を不定期に落とす (PR #2778 で実際に踏んだ)
func indexDef(t *testing.T, table, index string) string {
	t.Helper()
	// **件数と schema 名の両方を見る。**
	//
	//   - `Scan(&string)` で受けない。GORM は `*string` に対し**全行を走査して
	//     dest を上書きし続ける**ので、複数行が返ると**最後の 1 行**が残る。
	//     実測では絞りを外すと `internal_repository_ts` (17 件中 17 番目) の
	//     定義が返り、`NotEmpty` も `Contains "USING gin"` も満たして**テストが
	//     緑のまま通っていた** — これが (a) の空振りの実例
	//   - 件数だけでも足りない。**自分の schema に migration が未適用で、他 schema
	//     にだけ同名 index がある**ときは 1 件で通ってしまう。それはこの guard が
	//     捕まえるべき状態そのものなので、schema 名も突き合わせる
	var rows []struct {
		Schemaname string
		Indexdef   string
	}
	require.NoError(t, testDB.Raw(
		`SELECT schemaname, indexdef FROM pg_indexes
		 WHERE schemaname = current_schema() AND tablename = ? AND indexname = ?`,
		table, index).Scan(&rows).Error)
	require.LessOrEqual(t, len(rows), 1,
		"%s.%s が %d 件返った。current_schema() で絞れていない (他 schema の同名 index が混ざっている)",
		table, index, len(rows))
	if len(rows) == 0 {
		return ""
	}
	require.Equal(t, currentSchema(t), rows[0].Schemaname,
		"%s.%s で他 schema の index を拾っている", table, index)
	return rows[0].Indexdef
}

func currentSchema(t *testing.T) string {
	t.Helper()
	var s string
	require.NoError(t, testDB.Raw(`SELECT current_schema()`).Scan(&s).Error)
	return s
}

// indexDef が本当に current_schema() で絞っていること。
//
// **兄弟 schema に同名の index を作って確かめる。** 「複数 schema があるときだけ
// 差が出る」性質なので、たまたま schema が 1 つの環境 (fresh な CI 等) では
// 絞りを外しても症状が出ない。ここで自分で条件を作れば決定的に検証できる。
func TestIndexDef_IsSchemaScoped(t *testing.T) {
	const probe = "internal_repository_idxprobe"
	require.NoError(t, testDB.Exec(`DROP SCHEMA IF EXISTS "`+probe+`" CASCADE`).Error)
	require.NoError(t, testDB.Exec(`CREATE SCHEMA "`+probe+`"`).Error)
	t.Cleanup(func() { testDB.Exec(`DROP SCHEMA IF EXISTS "` + probe + `" CASCADE`) })

	// 同名テーブル + 同名 index。index 名は schema 内でのみ一意なので作れる。
	// 定義は本物と変える (取り違えたときに気付けるように)。
	require.NoError(t, testDB.Exec(`CREATE TABLE "`+probe+`"."clip_favorite" ("decoyColumn" text)`).Error)
	require.NoError(t, testDB.Exec(
		`CREATE INDEX "IDX_clip_favorite_clipId" ON "`+probe+`"."clip_favorite" ("decoyColumn")`).Error)

	// **ここは絞らない。** 「複数 schema に同名 index がある」という前提そのものを
	// 確かめる箇所なので絞ってはいけない。`count` は `indexdef` を projection
	// しないため `pg_get_indexdef` が評価されず、OID エラーの対象にもならない
	// (実測: DDL churn 下で `indexdef` を引くと 121/150 が落ちるのに対し
	// `count(distinct schemaname)` は 0/150)。
	var schemas int64
	require.NoError(t, testDB.Raw(
		`SELECT count(distinct schemaname) FROM pg_indexes -- unscoped: 複数 schema にあることを数える箇所
		 WHERE tablename = 'clip_favorite' AND indexname = ?`,
		"IDX_clip_favorite_clipId").Scan(&schemas).Error)
	require.GreaterOrEqual(t, schemas, int64(2), "前提: 同名 index が複数 schema にある")

	// 絞りが外れていれば indexDef の件数チェックか schema 名チェックが落ちる。
	def := indexDef(t, "clip_favorite", "IDX_clip_favorite_clipId")
	assert.NotEmpty(t, def)
	assert.Contains(t, def, `"clipId"`, "自分の schema の定義が返ること")
	assert.NotContains(t, def, "decoyColumn", "他 schema の同名 index を拾わないこと")
}
