package permissions

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsKnown(t *testing.T) {
	assert.True(t, IsKnown("read:account"))
	assert.True(t, IsKnown("write:notes"))
	assert.True(t, IsKnown("read:chat"))
	assert.True(t, IsKnown("write:admin:emoji"))
	assert.False(t, IsKnown("bogus"))
	assert.False(t, IsKnown("read:*"))
	assert.False(t, IsKnown(""))
	assert.False(t, IsKnown("read")) // coarse Mastodon-style scope is not a kind
}

func TestAll_NonEmptyUnique(t *testing.T) {
	require := assert.New(t)
	require.NotEmpty(All)
	seen := map[string]bool{}
	for _, p := range All {
		require.False(seen[p], "duplicate permission kind: %s", p)
		seen[p] = true
	}
	// All の各要素は IsKnown で引ける。
	for _, p := range All {
		require.True(IsKnown(p))
	}
}
