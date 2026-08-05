package repository

import (
	"testing"

	"github.com/lib/pq"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMetaRepository_Fetch(t *testing.T) {
	repo := NewMetaRepository(testDB)

	name := "Test Instance"
	meta := &model.Meta{
		ID:   "m_f_1",
		Name: &name,
	}
	require.NoError(t, testDB.Create(meta).Error)
	defer testDB.Exec(`DELETE FROM "meta" WHERE id = ?`, meta.ID)

	found, err := repo.Fetch()
	require.NoError(t, err)
	assert.Equal(t, &name, found.Name)
}

// 行が無ければ本家 MetaService.fetch() と同じくその場で作って返す。
// 起動後に truncate されても全 API が 500 のまま固まらないための保証。
func TestMetaRepository_Fetch_CreatesWhenMissing(t *testing.T) {
	repo := NewMetaRepository(testDB)

	testDB.Exec(`DELETE FROM "meta"`)
	defer testDB.Exec(`DELETE FROM "meta"`)

	found, err := repo.Fetch()
	require.NoError(t, err)
	require.NotNil(t, found)
	assert.Equal(t, defaultMetaID, found.ID)

	// 2 回目は作り直さず同じ行を返す。
	again, err := repo.Fetch()
	require.NoError(t, err)
	assert.Equal(t, found.ID, again.ID)

	var count int64
	require.NoError(t, testDB.Model(&model.Meta{}).Count(&count).Error)
	assert.EqualValues(t, 1, count, "行が重複して作られないこと")
}

func TestMetaRepository_Update(t *testing.T) {
	repo := NewMetaRepository(testDB)

	meta := &model.Meta{ID: "m_u_1"}
	require.NoError(t, testDB.Create(meta).Error)
	defer testDB.Exec(`DELETE FROM "meta" WHERE id = ?`, meta.ID)

	newName := "Updated"
	require.NoError(t, repo.Update(map[string]any{"name": newName}))

	found, err := repo.Fetch()
	require.NoError(t, err)
	assert.Equal(t, &newName, found.Name)
}

// pq.StringArray を varchar[] 列に Update できることを担保する。admin
// handler 側の coerceMetaArrayFields が []any → pq.StringArray 変換を
// 行った後で、ここで実 DB 経由で永続化されることを最後の砦として確認。
// #590 で修正したバグの regression test。
func TestMetaRepository_Update_FederationHosts(t *testing.T) {
	repo := NewMetaRepository(testDB)

	meta := &model.Meta{ID: "m_fh_1", Federation: "all"}
	require.NoError(t, testDB.Create(meta).Error)
	defer testDB.Exec(`DELETE FROM "meta" WHERE id = ?`, meta.ID)

	require.NoError(t, repo.Update(map[string]any{
		"federation":      "specified",
		"federationHosts": pq.StringArray{"allowed.example", "trusted.example"},
		"blockedHosts":    pq.StringArray{"bad.example"},
	}))

	found, err := repo.Fetch()
	require.NoError(t, err)
	assert.Equal(t, "specified", found.Federation)
	assert.Equal(t, []string{"allowed.example", "trusted.example"}, []string(found.FederationHosts))
	assert.Equal(t, []string{"bad.example"}, []string(found.BlockedHosts))
}

// 旧バグ ([]any{"x"} を直接渡すと "expression is of type record" で UPDATE
// 失敗) の boundary を実 DB で明示する。Update 自体が error を返し、その
// 結果として string 列の federation も更新されないことを記録する。これに
// より将来 admin handler の coerce を外したり、別経路で同じ落とし穴に
// 嵌まったときに即座に気付ける。
func TestMetaRepository_Update_RawAnySliceFails(t *testing.T) {
	repo := NewMetaRepository(testDB)

	meta := &model.Meta{ID: "m_fhraw_1", Federation: "all"}
	require.NoError(t, testDB.Create(meta).Error)
	defer testDB.Exec(`DELETE FROM "meta" WHERE id = ?`, meta.ID)

	err := repo.Update(map[string]any{
		"federation":      "specified",
		"federationHosts": []any{"allowed.example"},
	})
	require.Error(t, err, "varchar[] 列に []any を直接渡すと PostgreSQL がエラーを返す")

	// 同 transaction 内の string 列も巻き戻る。
	found, err := repo.Fetch()
	require.NoError(t, err)
	assert.Equal(t, "all", found.Federation, "UPDATE 全体がロールバックされる")
}

func TestMetaRepository_EnsureInitial_Creates(t *testing.T) {
	repo := NewMetaRepository(testDB)

	// 空の状態から初期行を作成する。
	testDB.Exec(`DELETE FROM "meta"`)

	require.NoError(t, repo.EnsureInitial("m_ei_1"))
	defer testDB.Exec(`DELETE FROM "meta" WHERE id = ?`, "m_ei_1")

	got, err := repo.Fetch()
	require.NoError(t, err)
	assert.Equal(t, "m_ei_1", got.ID)
}

func TestMetaRepository_EnsureInitial_NoopWhenExists(t *testing.T) {
	repo := NewMetaRepository(testDB)

	testDB.Exec(`DELETE FROM "meta"`)
	existing := &model.Meta{ID: "m_ei_2"}
	require.NoError(t, testDB.Create(existing).Error)
	defer testDB.Exec(`DELETE FROM "meta" WHERE id = ?`, existing.ID)

	// 既存行がある場合は何もせず、別 ID を渡しても既存のまま。
	require.NoError(t, repo.EnsureInitial("m_ei_other"))

	got, err := repo.Fetch()
	require.NoError(t, err)
	assert.Equal(t, "m_ei_2", got.ID)
}
