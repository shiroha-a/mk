package charts

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFederationChart_DeliveredSucceeded(t *testing.T) {
	engine, repo, _ := newTestEngine(t, SchemaFederation())
	fc := NewFederationChart(engine)
	require.Same(t, engine, fc.Chart())

	require.NoError(t, fc.Delivered("example.com", true))
	require.NoError(t, engine.Save(context.Background()))

	row := repo.hour[""][0]
	uniques, ok := row.Cols["deliveredInstances:unique"].([]string)
	require.True(t, ok)
	assert.Equal(t, []string{"example.com"}, uniques)
	// バケット内 unique cardinality は intersection bake を経由
	assert.Equal(t, int64(1), toInt64(row.Cols["deliveredInstances"]))
	assert.Equal(t, int64(0), toInt64(row.Cols["stalled"]))
}

func TestFederationChart_DeliveredFailed(t *testing.T) {
	engine, repo, _ := newTestEngine(t, SchemaFederation())
	fc := NewFederationChart(engine)

	require.NoError(t, fc.Delivered("bad.example", false))
	require.NoError(t, engine.Save(context.Background()))

	row := repo.hour[""][0]
	stalled, ok := row.Cols["stalled:unique"].([]string)
	require.True(t, ok)
	assert.Equal(t, []string{"bad.example"}, stalled)
	assert.Equal(t, int64(1), toInt64(row.Cols["stalled"]))
	assert.Equal(t, int64(0), toInt64(row.Cols["deliveredInstances"]))
}

func TestFederationChart_Inbox(t *testing.T) {
	engine, repo, _ := newTestEngine(t, SchemaFederation())
	fc := NewFederationChart(engine)

	require.NoError(t, fc.Inbox("peer.test"))
	require.NoError(t, fc.Inbox("peer.test")) // duplicate; cardinality should still be 1
	require.NoError(t, fc.Inbox("other.test"))
	require.NoError(t, engine.Save(context.Background()))

	row := repo.hour[""][0]
	// unique-temp 配列には重複が積まれない (engine 側のバッファが集合)。
	// 濃度は下のアサーションのとおり変わらない。
	inboxes, _ := row.Cols["inboxInstances:unique"].([]string)
	assert.ElementsMatch(t, []string{"peer.test", "other.test"}, inboxes)
	assert.Equal(t, int64(2), toInt64(row.Cols["inboxInstances"]))
}
