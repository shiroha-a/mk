package federation

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shiroha-a/mk/internal/activitypub"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/testutil"
)

// remoteText は NUL を落とし、指定があれば rune 単位で切ること (#2723)。
func TestRemoteText(t *testing.T) {
	nul := string(rune(0))
	cases := []struct {
		name string
		raw  string
		max  int
		want string
	}{
		{"passes through", "hello", 10, "hello"},
		{"drops NUL", "he" + nul + "llo", 10, "hello"},
		{"truncates by rune", strings.Repeat("\u3042", 20), 5, strings.Repeat("\u3042", 5)},
		{"max<=0 keeps length", strings.Repeat("\u3042", 20), 0, strings.Repeat("\u3042", 20)},
		{"NUL without truncation", "a" + nul + "b", 0, "ab"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := remoteText(tc.raw, tc.max)
			assert.Equal(t, tc.want, got)
			assert.True(t, utf8.ValidString(got))
			assert.NotContains(t, got, nul)
		})
	}
}

// ingestCWEnv builds a resolver whose fetches resolve one remote actor.
func ingestCWEnv(t *testing.T) (*Resolver, *testutil.MockNoteRepository) {
	t.Helper()
	noteRepo := testutil.NewMockNoteRepository()
	idGen, err := id.NewGenerator("aidx")
	require.NoError(t, err)
	fetcher := &countingFetcher{docs: map[string]string{
		"https://remote.example/users/cw": featuredActorDoc("remote.example", "cw", ""),
	}}
	r := NewResolver(testutil.NewMockUserRepository(), noteRepo,
		activitypub.NewURLBuilder("https://example.com"), fetcher, idGen)
	return r, noteRepo
}

// quoteJSON renders s as a JSON string literal.
func quoteJSON(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// 長い CW でも取り込みが失敗しないこと (#2723)。
//
// `note.cw` は varchar(512)。CW は利用者が自由に書くので長文が普通に来る。
// 切らずに入れると Create ごと 22001 で落ち、ingest が error を返して
// **inbox job が恒久 retry になる** — 添付 1 枚が消えるより重い。
func TestIngestNote_TruncatesLongCW(t *testing.T) {
	r, noteRepo := ingestCWEnv(t)
	// 先頭と末尾を別の文字にする (末尾から切る実装を見逃さないため)。
	cw := "\u3055\u304d" + strings.Repeat("\u3042", 900) + "\u304a\u308f\u308a"
	doc := `{"@context":"https://www.w3.org/ns/activitystreams","id":"https://remote.example/notes/cw1",` +
		`"type":"Note","attributedTo":"https://remote.example/users/cw","content":"<p>hi</p>",` +
		`"summary":` + quoteJSON(cw) + `,"to":["https://www.w3.org/ns/activitystreams#Public"]}`

	note, err := r.IngestNote([]byte(doc))
	require.NoError(t, err, "長い CW で取り込みが失敗している")
	require.NotNil(t, note)
	require.NotNil(t, note.CW)
	// **定数ではなく列の実値 (512) で assert する。** 定数を assert すると、
	// 定数を動かしたときに両側が一緒に動いて検査が空振りする。列の上限そのものは
	// internal/repository の TestNote_CWColumnLimit... が schema から固定する。
	assert.Equal(t, 512, len([]rune(*note.CW)), "列の上限で切られていない")
	assert.True(t, strings.HasPrefix(*note.CW, "\u3055\u304d"), "先頭から切っていない")
	assert.Len(t, noteRepo.Notes, 1)
}

// CW / text の NUL を落とすこと (#2723)。
//
// `note.text` は無制限の text 列なので長さは問題にならないが、NUL は 22021 で
// 同じく行ごと落ちる。`content` 経路は mfm.FromHTML が落とすが、
// `_misskey_content` は FromHTML を通らない。
func TestIngestNote_StripsNULFromTextAndCW(t *testing.T) {
	r, _ := ingestCWEnv(t)
	nul := string(rune(0))
	doc := `{"@context":"https://www.w3.org/ns/activitystreams","id":"https://remote.example/notes/cw2",` +
		`"type":"Note","attributedTo":"https://remote.example/users/cw",` +
		`"_misskey_content":` + quoteJSON("te"+nul+"xt") + `,"summary":` + quoteJSON("c"+nul+"w") + `,` +
		`"to":["https://www.w3.org/ns/activitystreams#Public"]}`

	note, err := r.IngestNote([]byte(doc))
	require.NoError(t, err)
	require.NotNil(t, note)
	require.NotNil(t, note.Text)
	assert.Equal(t, "text", *note.Text, "text の NUL が残っている")
	require.NotNil(t, note.CW)
	assert.Equal(t, "cw", *note.CW, "cw の NUL が残っている")
}

// Update 経路も同じく切り、NUL を落とすこと (#2723)。
//
// create だけ直しても、`Update(Note)` で長い CW が来ると UpdateFields ごと落ちて
// 同じ恒久 retry になる。
func TestUpdateRemoteNote_TruncatesCWAndStripsNUL(t *testing.T) {
	r, noteRepo := ingestCWEnv(t)
	nul := string(rune(0))

	// まず短い CW で取り込む。
	base := `{"@context":"https://www.w3.org/ns/activitystreams","id":"https://remote.example/notes/up1",` +
		`"type":"Note","attributedTo":"https://remote.example/users/cw","content":"<p>hi</p>",` +
		`"summary":"short","to":["https://www.w3.org/ns/activitystreams#Public"]}`
	note, err := r.IngestNote([]byte(base))
	require.NoError(t, err)
	require.NotNil(t, note)

	// 長い CW + NUL 入りの本文で Update する。
	cw := "\u3055\u304d" + strings.Repeat("\u3042", 900)
	upd := `{"@context":"https://www.w3.org/ns/activitystreams","id":"https://remote.example/notes/up1",` +
		`"type":"Note","attributedTo":"https://remote.example/users/cw",` +
		`"_misskey_content":` + quoteJSON("te"+nul+"xt") + `,"summary":` + quoteJSON(cw) + `,` +
		`"to":["https://www.w3.org/ns/activitystreams#Public"]}`
	updated, err := r.UpdateRemoteNote([]byte(upd), "https://remote.example/users/cw")
	require.NoError(t, err, "Update で取り込みが失敗している")
	require.NotNil(t, updated)

	require.NotNil(t, updated.CW)
	assert.Equal(t, 512, len([]rune(*updated.CW)), "Update 経路で切られていない")
	assert.True(t, strings.HasPrefix(*updated.CW, "\u3055\u304d"), "先頭から切っていない")
	require.NotNil(t, updated.Text)
	assert.Equal(t, "text", *updated.Text, "Update 経路で text の NUL が残っている")

	stored := noteRepo.Notes[note.ID]
	require.NotNil(t, stored)
	require.NotNil(t, stored.CW)
	assert.Equal(t, 512, len([]rune(*stored.CW)))
}
