package ld_test

import (
	"errors"
	"testing"

	"github.com/shiroha-a/mk/internal/activitypub/ld"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheckForForbiddenDirectives_DetectsAtGraph(t *testing.T) {
	p := ld.NewProcessor()
	err := p.CheckForForbiddenDirectives(map[string]any{
		"@graph": []any{
			map[string]any{"id": "https://example.com/notes/n1"},
		},
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, ld.ErrForbiddenDirective)
}

func TestCheckForForbiddenDirectives_DetectsAtReverse(t *testing.T) {
	p := ld.NewProcessor()
	err := p.CheckForForbiddenDirectives(map[string]any{
		"@context": "https://www.w3.org/ns/activitystreams",
		"type":     "Note",
		"@reverse": map[string]any{"creator": "https://evil.example/spoof"},
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, ld.ErrForbiddenDirective)
}

func TestCheckForForbiddenDirectives_DetectsAtIncluded(t *testing.T) {
	p := ld.NewProcessor()
	err := p.CheckForForbiddenDirectives(map[string]any{
		"@included": []any{
			map[string]any{"type": "Note"},
		},
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, ld.ErrForbiddenDirective)
}

func TestCheckForForbiddenDirectives_AllowsCleanDocument(t *testing.T) {
	p := ld.NewProcessor()
	err := p.CheckForForbiddenDirectives(map[string]any{
		"@context": "https://www.w3.org/ns/activitystreams",
		"type":     "Note",
		"id":       "https://example.com/notes/n1",
		"content":  "hello",
		"to":       []any{"https://www.w3.org/ns/activitystreams#Public"},
	})
	require.NoError(t, err)
}

func TestCheckForForbiddenDirectives_DetectsNested(t *testing.T) {
	p := ld.NewProcessor()
	err := p.CheckForForbiddenDirectives(map[string]any{
		"object": []any{
			map[string]any{
				"nested": map[string]any{
					"@graph": "deep!",
				},
			},
		},
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, ld.ErrForbiddenDirective)
}

func TestFreeze_BlocksSubsequentContextFetch(t *testing.T) {
	p := ld.NewProcessor()
	// Freeze する前は preload 経由で取得できる
	_, err := p.LoadDocument("https://www.w3.org/ns/activitystreams")
	require.NoError(t, err)

	p.Freeze()

	// Freeze 後は cache hit のみ許可。cache 内の URL は引き続き返るが、
	// cache 未登録の URL は ErrCacheFrozen で fail-closed。
	_, err = p.LoadDocument("https://w3id.org/security/v1")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ld.ErrCacheFrozen),
		"freeze 後に未 cache の URL fetch は ErrCacheFrozen を返すこと")
}

func TestFreeze_CachedURLsRemainAccessible(t *testing.T) {
	p := ld.NewProcessor()
	_, err := p.LoadDocument("https://www.w3.org/ns/activitystreams")
	require.NoError(t, err)
	p.Freeze()
	// すでに cache 済の URL は freeze 後も読める (= verify 中の compact /
	// normalize で同じ context を re-resolve するときに死なないため)。
	doc, err := p.LoadDocument("https://www.w3.org/ns/activitystreams")
	require.NoError(t, err)
	require.NotNil(t, doc)
}
