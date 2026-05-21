package ld_test

import (
	"errors"
	"testing"

	"github.com/shiroha-a/mk/internal/activitypub/ld"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPreloadedLoader_LoadsActivityStreams(t *testing.T) {
	l := ld.NewPreloadedLoader()
	doc, err := l.LoadDocument("https://www.w3.org/ns/activitystreams")
	require.NoError(t, err)
	require.NotNil(t, doc)
	// AS 2.0 context は @context object に "Activity" / "Note" 等を含む。
	m, ok := doc.Document.(map[string]any)
	require.True(t, ok, "AS 2.0 context root must be a JSON object")
	ctx, ok := m["@context"].(map[string]any)
	require.True(t, ok, "AS 2.0 root must have @context object")
	_, hasActivity := ctx["Activity"]
	assert.True(t, hasActivity, "AS 2.0 context must define Activity term")
}

func TestPreloadedLoader_LoadsSecurityV1(t *testing.T) {
	l := ld.NewPreloadedLoader()
	doc, err := l.LoadDocument("https://w3id.org/security/v1")
	require.NoError(t, err)
	require.NotNil(t, doc)
}

func TestPreloadedLoader_LoadsIdentityV1(t *testing.T) {
	l := ld.NewPreloadedLoader()
	doc, err := l.LoadDocument("https://w3id.org/identity/v1")
	require.NoError(t, err)
	require.NotNil(t, doc)
}

func TestPreloadedLoader_RejectsUnknownIRI(t *testing.T) {
	l := ld.NewPreloadedLoader()
	_, err := l.LoadDocument("https://evil.example/malicious-context")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ld.ErrContextNotPreloaded),
		"unknown URL must return ErrContextNotPreloaded sentinel for caller gating")
}

func TestProcessor_NormalizeActivityStreamsNote(t *testing.T) {
	p := ld.NewProcessor()
	// 最小 Create(Note) を URDNA2015 で正規化できることを確認 (= context fetch
	// が PRELOADED 経由で完結する steady state)。
	doc := map[string]any{
		"@context": "https://www.w3.org/ns/activitystreams",
		"id":       "https://example.com/notes/n1",
		"type":     "Note",
		"content":  "hello",
	}
	nq, err := p.Normalize(doc)
	require.NoError(t, err)
	assert.NotEmpty(t, nq, "n-quads output should be non-empty for a Note")
}
