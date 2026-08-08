package federation_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const ephDedupNoteDoc = `{
	"@context": "https://www.w3.org/ns/activitystreams",
	"id": "https://remote.example/notes/1",
	"type": "Note",
	"attributedTo": "https://remote.example/users/alice",
	"content": "relay hello",
	"to": ["https://www.w3.org/ns/activitystreams#Public"]
}`

// #2397 の本体。複数のリレーから同じ投稿が届いても ephemeral ノートは 1 件に
// 収まること。DB だけを見る dedup では miss して別 ID を採番するため、
// タイムラインに同じ投稿が 2 回流れていた。
func TestResolveNoteEphemeral_SecondRelayReusesSameNote(t *testing.T) {
	r, sink, noteRepo, _ := ephResolverDocs(t, map[string]string{
		"https://remote.example/notes/1":     ephDedupNoteDoc,
		"https://remote.example/users/alice": ephActorDoc,
	})

	first, err := r.ResolveNoteEphemeral("https://remote.example/notes/1")
	require.NoError(t, err)
	require.NotNil(t, first)

	// 2 つ目のリレーから同じ URI が届く。
	second, err := r.ResolveNoteEphemeral("https://remote.example/notes/1")
	require.NoError(t, err)
	require.NotNil(t, second)

	assert.Equal(t, first.ID, second.ID, "同じ URI には同じ ephemeral ID を返す")
	assert.Len(t, sink.notes, 1, "ephemeral store の行が増えない")
	assert.Empty(t, noteRepo.Notes, "DB 行も作らない")
}

// 3 つ目以降も同様。リレーを複数購読している構成で件数に比例して増えない。
func TestResolveNoteEphemeral_ManyRelaysStillOneNote(t *testing.T) {
	r, sink, _, _ := ephResolverDocs(t, map[string]string{
		"https://remote.example/notes/1":     ephDedupNoteDoc,
		"https://remote.example/users/alice": ephActorDoc,
	})

	var ids []string
	for i := 0; i < 5; i++ {
		n, err := r.ResolveNoteEphemeral("https://remote.example/notes/1")
		require.NoError(t, err)
		require.NotNil(t, n)
		ids = append(ids, n.ID)
	}
	for _, got := range ids {
		assert.Equal(t, ids[0], got)
	}
	assert.Len(t, sink.notes, 1)
}

// 引用先も再取り込みしない。ここが漏れると renoteId の指す先が投稿ごとにずれ、
// 引用先が「削除された投稿」に見える原因になる。
func TestResolveNoteEphemeral_QuoteTargetReusedAcrossNotes(t *testing.T) {
	quoted := `{
		"@context": "https://www.w3.org/ns/activitystreams",
		"id": "https://remote.example/notes/quoted",
		"type": "Note",
		"attributedTo": "https://remote.example/users/alice",
		"content": "original",
		"to": ["https://www.w3.org/ns/activitystreams#Public"]
	}`
	quoting := func(id string) string {
		return `{
			"@context": "https://www.w3.org/ns/activitystreams",
			"id": "https://remote.example/notes/` + id + `",
			"type": "Note",
			"attributedTo": "https://remote.example/users/alice",
			"content": "quoting",
			"_misskey_quote": "https://remote.example/notes/quoted",
			"to": ["https://www.w3.org/ns/activitystreams#Public"]
		}`
	}
	r, sink, _, _ := ephResolverDocs(t, map[string]string{
		"https://remote.example/notes/quoted": quoted,
		"https://remote.example/notes/q1":     quoting("q1"),
		"https://remote.example/notes/q2":     quoting("q2"),
		"https://remote.example/users/alice":  ephActorDoc,
	})

	first, err := r.ResolveNoteEphemeral("https://remote.example/notes/q1")
	require.NoError(t, err)
	require.NotNil(t, first.RenoteID, "引用先が紐付くこと")

	second, err := r.ResolveNoteEphemeral("https://remote.example/notes/q2")
	require.NoError(t, err)
	require.NotNil(t, second.RenoteID)

	assert.Equal(t, *first.RenoteID, *second.RenoteID,
		"同じ引用先には同じ ephemeral ID を使う")
	// quoted 1 件 + quoting 2 件。引用先が再取り込みされていれば 4 件になる。
	assert.Len(t, sink.notes, 3)
}
