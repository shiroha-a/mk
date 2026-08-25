package maintenance

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"github.com/shiroha-a/mk/internal/model"
)

// seedHostUser inserts a remote user with the given (usernameLower, host).
func seedHostUser(t *testing.T, id, username, host string) {
	t.Helper()
	h := host
	require.NoError(t, testDB.Create(&model.User{
		ID: id, Username: username, UsernameLower: username, Host: &h,
		AvatarDecorations: datatypes.JSON([]byte("[]")),
	}).Error)
	t.Cleanup(func() { testDB.Exec(`DELETE FROM "user" WHERE id = ?`, id) })
}

func userHost(t *testing.T, id string) string {
	t.Helper()
	var got string
	require.NoError(t, testDB.Raw(`SELECT host FROM "user" WHERE id = ?`, id).Scan(&got).Error)
	return got
}

var userHostColumn = HostColumn{Table: "user", KeysetColumn: "id", Column: "host"}

// 非正規化で保存された host が正規化されること (#2706)。
//
// `url.Parse` は小文字化も punycode 化もしないので、`Mixed.Example` のような表記で
// 入った行が残る。連合ゲートや timeline の instance-mute は完全一致なので取りこぼす。
func TestBackfillHostColumnBatch_Normalizes(t *testing.T) {
	testDB.Exec(`DELETE FROM "user" WHERE id LIKE 'hb_%'`)
	seedHostUser(t, "hb_mixed", "hbmixed", "Mixed.Example")
	seedHostUser(t, "hb_uni", "hbuni", "パイ.example")
	seedHostUser(t, "hb_ok", "hbok", "already.example")

	res, err := BackfillHostColumnBatch(testDB, userHostColumn, "hb_", 100, false)
	require.NoError(t, err)
	assert.Equal(t, 3, res.Scanned)
	assert.Equal(t, 2, res.Updated, "既に正規化済みの行は touch しない")
	assert.Equal(t, 0, res.Conflicts)
	assert.Equal(t, "hb_uni", res.LastKey)

	assert.Equal(t, "mixed.example", userHost(t, "hb_mixed"))
	assert.Equal(t, "xn--eckve.example", userHost(t, "hb_uni"), "punycode 変換は SQL では書けない")
	assert.Equal(t, "already.example", userHost(t, "hb_ok"))

	// **冪等。** 2 回目は 1 件も更新しない (途中で失敗しても再実行して安全)。
	res, err = BackfillHostColumnBatch(testDB, userHostColumn, "hb_", 100, false)
	require.NoError(t, err)
	assert.Equal(t, 3, res.Scanned)
	assert.Equal(t, 0, res.Updated)
}

// dry-run は件数だけ数えて書かないこと。
func TestBackfillHostColumnBatch_DryRun(t *testing.T) {
	testDB.Exec(`DELETE FROM "user" WHERE id LIKE 'hd_%'`)
	seedHostUser(t, "hd_1", "hdone", "Mixed.Example")

	res, err := BackfillHostColumnBatch(testDB, userHostColumn, "hd_", 100, true)
	require.NoError(t, err)
	assert.Equal(t, 1, res.Scanned)
	assert.Equal(t, 1, res.Updated)
	assert.Equal(t, "Mixed.Example", userHost(t, "hd_1"), "dry-run で書き換わっている")
}

// 表記違いで 2 行に増えている場合、正規化すると一意制約に当たる。
// **マージせず skip して数える** — 1 行に畳むには note / following / drive_file など
// 多数の FK を張り替える必要があり、バッチでやるには危険すぎる (#2706)。
func TestBackfillHostColumnBatch_ConflictIsSkippedNotFatal(t *testing.T) {
	testDB.Exec(`DELETE FROM "user" WHERE id LIKE 'hc_%'`)
	// 同じ usernameLower で、正規化すると衝突する 2 行。
	seedHostUser(t, "hc_1", "hcdup", "dup.example")
	seedHostUser(t, "hc_2", "hcdup", "Dup.Example")
	// 衝突しない行も混ぜて、1 件の衝突でバッチが止まらないことを見る。
	seedHostUser(t, "hc_3", "hcother", "Other.Example")

	res, err := BackfillHostColumnBatch(testDB, userHostColumn, "hc_", 100, false)
	require.NoError(t, err, "衝突でバッチ全体を落とさないこと")
	assert.Equal(t, 3, res.Scanned)
	assert.Equal(t, 1, res.Conflicts)
	assert.Equal(t, 1, res.Updated, "衝突しない行は進むこと")

	assert.Equal(t, "dup.example", userHost(t, "hc_1"))
	assert.Equal(t, "Dup.Example", userHost(t, "hc_2"), "衝突した行はそのまま残る")
	assert.Equal(t, "other.example", userHost(t, "hc_3"))
}

// keyset で再開できること。LastKey を次の fromKey に渡す。
func TestBackfillHostColumnBatch_Resumable(t *testing.T) {
	testDB.Exec(`DELETE FROM "user" WHERE id LIKE 'hr_%'`)
	seedHostUser(t, "hr_1", "hrone", "One.Example")
	seedHostUser(t, "hr_2", "hrtwo", "Two.Example")
	seedHostUser(t, "hr_3", "hrthree", "Three.Example")

	res, err := BackfillHostColumnBatch(testDB, userHostColumn, "hr_", 2, false)
	require.NoError(t, err)
	require.Equal(t, 2, res.Scanned)
	require.Equal(t, "hr_2", res.LastKey)

	res, err = BackfillHostColumnBatch(testDB, userHostColumn, res.LastKey, 2, false)
	require.NoError(t, err)
	assert.Equal(t, 1, res.Scanned)
	assert.Equal(t, "hr_3", res.LastKey)

	assert.Equal(t, "one.example", userHost(t, "hr_1"))
	assert.Equal(t, "two.example", userHost(t, "hr_2"))
	assert.Equal(t, "three.example", userHost(t, "hr_3"))

	// 尽きたら Scanned=0 / LastKey="" で止まる (CLI のループ終了条件)。
	res, err = BackfillHostColumnBatch(testDB, userHostColumn, "hr_3", 2, false)
	require.NoError(t, err)
	assert.Equal(t, 0, res.Scanned)
	assert.Empty(t, res.LastKey)
}

// HostColumns に無い組は拒否すること。整形した SQL に外から識別子が入らない。
func TestBackfillHostColumnBatch_RejectsUnknownColumn(t *testing.T) {
	_, err := BackfillHostColumnBatch(testDB, HostColumn{
		Table: "user", KeysetColumn: "id", Column: `host"; DROP TABLE "user"; --`,
	}, "", 10, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown host column")
}

// 一覧が実在するテーブル・列であること。1 つでも綴りを間違えると、そこだけ
// 非正規化のまま残って同じ症状が別の場所で出る。
func TestHostColumns_AllExist(t *testing.T) {
	require.NotEmpty(t, HostColumns)
	for _, c := range HostColumns {
		t.Run(c.Table+"."+c.Column, func(t *testing.T) {
			var n int64
			// **schema を絞る。** OpenTestDB はパッケージ専用の schema に繋ぐが、
			// information_schema は全 schema を見るので、絞らないと同名テーブルを
			// 数え上げて必ず 1 にならない。
			err := testDB.Raw(`SELECT count(*) FROM information_schema.columns
				WHERE table_schema = current_schema() AND table_name = ? AND column_name = ?`,
				c.Table, c.Column).Scan(&n).Error
			require.NoError(t, err)
			assert.EqualValues(t, 1, n, "列が存在しない")

			err = testDB.Raw(`SELECT count(*) FROM information_schema.columns
				WHERE table_schema = current_schema() AND table_name = ? AND column_name = ?`,
				c.Table, c.KeysetColumn).Scan(&n).Error
			require.NoError(t, err)
			assert.EqualValues(t, 1, n, "keyset 列が存在しない")
		})
	}
}

// SELECT が失敗したら error を返すこと (テーブルが無い schema 等)。
func TestBackfillHostColumnBatch_SelectError(t *testing.T) {
	// 一覧に載っている組だが、テーブルが解決できない search_path で引く。
	// **SET LOCAL はトランザクション内でしか効かない** (外で撃つと黙って無視され、
	// 検査が空振りする)。
	err := testDB.Transaction(func(tx *gorm.DB) error {
		if e := tx.Exec(`SET LOCAL search_path TO nonexistent_schema_2706`).Error; e != nil {
			return e
		}
		_, e := BackfillHostColumnBatch(tx, userHostColumn, "", 10, false)
		return e
	})
	require.Error(t, err)
}

// UPDATE が unique violation 以外で失敗したら、握り潰さず error を返すこと。
// 握り潰すと「conflicts が 0 なのに更新されていない」状態になり、運用者が
// backfill 済みだと誤認する。
func TestBackfillHostColumnBatch_UpdateErrorIsFatal(t *testing.T) {
	testDB.Exec(`DELETE FROM "user" WHERE id LIKE 'he_%'`)
	seedHostUser(t, "he_1", "heone", "Mixed.Example")

	// host 列に 4 文字までの制約を足して、正規化後の値が入らないようにする。
	require.NoError(t, testDB.Exec(
		`ALTER TABLE "user" ADD CONSTRAINT "chk_2706_host_len" CHECK (length(host) <= 4) NOT VALID`).Error)
	t.Cleanup(func() {
		testDB.Exec(`ALTER TABLE "user" DROP CONSTRAINT IF EXISTS "chk_2706_host_len"`)
	})

	_, err := BackfillHostColumnBatch(testDB, userHostColumn, "he_", 10, false)
	require.Error(t, err, "unique violation 以外の error を握り潰している")
	assert.False(t, isUniqueViolation(err))
}

// isUniqueViolation は driver の SQLSTATE と GORM の翻訳済み error の両方を見る。
func TestIsUniqueViolation(t *testing.T) {
	assert.True(t, isUniqueViolation(&pgconn.PgError{Code: "23505"}))
	assert.True(t, isUniqueViolation(fmt.Errorf("wrapped: %w", &pgconn.PgError{Code: "23505"})))
	assert.False(t, isUniqueViolation(&pgconn.PgError{Code: "23503"}), "FK 違反は衝突ではない")
	assert.True(t, isUniqueViolation(gorm.ErrDuplicatedKey))
	assert.False(t, isUniqueViolation(errors.New("boom")))
	assert.False(t, isUniqueViolation(nil))
}
