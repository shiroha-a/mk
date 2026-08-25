package repository

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// instanceTextColumns は fetch_metadata が nodeinfo から書く text 列と、その上限。
// migration/000001_initial.up.sql と一致させる。
var instanceTextColumns = []struct {
	column string
	max    int
}{
	{"softwareName", 64},
	{"softwareVersion", 64},
	{"name", 256},
	{"description", 4096},
	{"themeColor", 64},
	{"iconUrl", 256},
	{"faviconUrl", 256},
}

// 列の上限そのものを schema から固定する (#2723)。
//
// fetch_metadata 側の定数と独立に同じ数値が書かれているだけだと、揃って動かせば
// 全部緑になる。列が変わったらここが落ちる。
func TestInstance_NodeinfoColumnLimits(t *testing.T) {
	for _, tc := range instanceTextColumns {
		var n int
		require.NoError(t, testDB.Raw(`SELECT character_maximum_length FROM information_schema.columns
			WHERE table_schema = current_schema() AND table_name = 'instance' AND column_name = ?`,
			tc.column).Scan(&n).Error)
		assert.Equal(t, tc.max, n,
			"instance.%s の列長が変わっている (internal/core/instance/fetch_metadata.go の定数も直すこと)", tc.column)
	}
}

// fetch_metadata が切った長さで実際に書けること (#2723)。
//
// mock repository は列制約を持たないので、切る側のテストだけでは「本当に入る
// 長さか」を確かめられない。**全角で埋める** — byte で切る実装だと 3 倍になって
// 入らない。
func TestInstance_NodeinfoColumnLimitsAcceptTruncatedValues(t *testing.T) {
	repo := NewInstanceRepository(testDB)
	inst := newTestInstance("i_collimit_1", "collimit.example")
	require.NoError(t, repo.Create(inst))
	defer cleanupInstance(t, inst.ID)

	fields := map[string]any{}
	want := map[string]string{}
	for _, tc := range instanceTextColumns {
		v := strings.Repeat("あ", tc.max)
		want[tc.column] = v
		fields[tc.column] = &v
	}
	require.NoError(t, repo.UpdateFields(inst.Host, fields))

	got, err := repo.FindByHost(inst.Host)
	require.NoError(t, err)
	for _, tc := range instanceTextColumns {
		var stored string
		require.NoError(t, testDB.Raw(
			`SELECT `+quoteColumn(tc.column)+` FROM "instance" WHERE id = ?`, got.ID).
			Scan(&stored).Error)
		assert.Equal(t, want[tc.column], stored, "instance.%s", tc.column)
	}
}
