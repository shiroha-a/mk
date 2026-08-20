package charts

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPerUserPvChart_CommitByUser(t *testing.T) {
	engine, repo, _ := newTestEngine(t, SchemaPerUserPv())
	pc := NewPerUserPvChart(engine)
	require.Same(t, engine, pc.Chart())

	require.NoError(t, pc.CommitByUser("owner", "u-a"))
	require.NoError(t, pc.CommitByUser("owner", "u-a")) // duplicate visitor → cardinality 1
	require.NoError(t, pc.CommitByUser("owner", "u-b"))
	require.NoError(t, engine.Save(context.Background()))

	row := repo.hour["owner"][0]
	// unique-temp 配列には重複が積まれない (engine 側のバッファが集合)。
	// 濃度 (upv.user) と延べ回数 (pv.user) は従来どおり。
	uniques, ok := row.Cols["upv.user:unique"].([]string)
	require.True(t, ok)
	assert.ElementsMatch(t, []string{"u-a", "u-b"}, uniques)
	assert.Equal(t, int64(2), toInt64(row.Cols["upv.user"]))
	assert.Equal(t, int64(3), toInt64(row.Cols["pv.user"]))
}

func TestPerUserPvChart_CommitByVisitor(t *testing.T) {
	engine, repo, _ := newTestEngine(t, SchemaPerUserPv())
	pc := NewPerUserPvChart(engine)

	require.NoError(t, pc.CommitByVisitor("owner", "ck1"))
	require.NoError(t, pc.CommitByVisitor("owner", "ck2"))
	require.NoError(t, engine.Save(context.Background()))

	row := repo.hour["owner"][0]
	uniques, ok := row.Cols["upv.visitor:unique"].([]string)
	require.True(t, ok)
	assert.Len(t, uniques, 2)
	assert.Equal(t, int64(2), toInt64(row.Cols["upv.visitor"]))
	assert.Equal(t, int64(2), toInt64(row.Cols["pv.visitor"]))
	// user 系列は触れていない
	assert.Equal(t, int64(0), toInt64(row.Cols["pv.user"]))
}
