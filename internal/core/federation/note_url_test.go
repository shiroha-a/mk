package federation_test

import (
	"strings"
	"testing"

	"github.com/shiroha-a/mk/internal/activitypub"
	"github.com/shiroha-a/mk/internal/core/federation"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ingestNoteWithURL は `url` を持つ Create(Note) を流し、保存された note を返す。
func ingestNoteWithURL(t *testing.T, urlJSON string) *model.Note {
	t.Helper()
	p, _, _, noteRepo := newProcessor(t, aliceActor)
	body := []byte(`{
		"type": "Create",
		"actor": "https://remote.example/users/alice",
		"object": {
			"type": "Note",
			"id": "https://remote.example/users/alice/statuses/1",
			"attributedTo": "https://remote.example/users/alice",
			"content": "hello",
			"url": ` + urlJSON + `,
			"to": ["https://www.w3.org/ns/activitystreams#Public"]
		}
	}`)
	require.NoError(t, p.Process(body))
	require.Len(t, noteRepo.Notes, 1)
	for _, n := range noteRepo.Notes {
		return n
	}
	return nil
}

// **AP の `id` と HTML の permalink は別物。** Mastodon 系では `id` が AP object、
// `url` が Web ページを指すので、保存しないとクライアントが原文へ辿れない (#2729)。
func TestIngest_StoresNoteURL(t *testing.T) {
	got := ingestNoteWithURL(t, `"https://remote.example/@alice/1"`)
	require.NotNil(t, got.URL)
	assert.Equal(t, "https://remote.example/@alice/1", *got.URL)
	// uri は AP object の id のまま (別物であることの確認)。
	require.NotNil(t, got.URI)
	assert.Equal(t, "https://remote.example/users/alice/statuses/1", *got.URI)
}

// 読み方は upstream の `getOneApHrefNullable` と同じ — 配列なら先頭、object なら
// `href`。**`id` は見ない**。
func TestIngest_NoteURLShapes(t *testing.T) {
	cases := map[string]struct {
		in   string
		want string
	}{
		"string":        {`"https://remote.example/@a/1"`, "https://remote.example/@a/1"},
		"object の href": {`{"type":"Link","href":"https://remote.example/@a/1"}`, "https://remote.example/@a/1"},
		"配列は先頭":         {`["https://remote.example/@a/1","https://remote.example/@a/2"]`, "https://remote.example/@a/1"},
		"配列の先頭が object": {`[{"type":"Link","href":"https://remote.example/@a/1"}]`, "https://remote.example/@a/1"},
		"object に href が無ければ捨てる": {`{"type":"Link","id":"https://remote.example/@a/1"}`, ""},
		"空配列は捨てる":                {`[]`, ""},
		"数値は捨てる":                 {`123`, ""},
		"null は捨てる":              {`null`, ""},
		"href が string でなければ捨てる": {`{"href":123}`, ""},
		// **upstream は空文字を保存する** (`if (url && !checkHttps(url))` は
		// falsy を素通りし、`if (data.url != null)` で `""` が入る)。mk-go は
		// 捨てる — 空の permalink はどこも指さない (#2729)。
		"空文字は捨てる": {`""`, ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := ingestNoteWithURL(t, tc.in)
			if tc.want == "" {
				assert.Nil(t, got.URL)
				return
			}
			require.NotNil(t, got.URL)
			assert.Equal(t, tc.want, *got.URL)
		})
	}
}

// **http(s) 以外は値だけ捨てて note は作る** (#2729)。upstream は note ごと
// reject するが、permalink の scheme が変なだけで本文ごと失うほうが害が大きい。
// `javascript:` を保存すると `note.url` を href に流すクライアントで XSS に
// なりうるので、捨てるのは upstream より安全側でもある。
func TestIngest_DropsNonHTTPNoteURL(t *testing.T) {
	for _, raw := range []string{
		`"javascript:alert(1)"`,
		`"data:text/html,<script>alert(1)</script>"`,
		`"ftp://remote.example/x"`,
		`"/@alice/1"`,
	} {
		t.Run(raw, func(t *testing.T) {
			got := ingestNoteWithURL(t, raw)
			assert.Nil(t, got.URL)
			// note 自体は作られる。
			require.NotNil(t, got.URI)
		})
	}
}

// **mk-go が保存する scheme を固定する。** `http://` と大文字 scheme は
// **upstream なら note ごと落ちる**値だが、mk-go は保存する (#2729)。
//
// 固定しないと、divergence の表を読んで「捨てるはず」と実装を直したときに
// **無検出で挙動が変わる**。scheme を case-insensitive に見るのは RFC 3986 の
// ためで、`internal/core/urlpreview` の `isHTTPScheme` と同じ方針。
func TestIngest_KeepsHTTPAndUppercaseSchemeNoteURL(t *testing.T) {
	for _, raw := range []string{
		"https://remote.example/@alice/1",
		"HTTPS://remote.example/@alice/1",
		"http://remote.example/@alice/1",
		"HTTP://remote.example/@alice/1",
	} {
		t.Run(raw, func(t *testing.T) {
			got := ingestNoteWithURL(t, `"`+raw+`"`)
			require.NotNil(t, got.URL)
			// **正規化しない** — 受けた文字列をそのまま保存する。
			assert.Equal(t, raw, *got.URL)
		})
	}
}

// `note.url` は varchar(512)。**切ると別の URL になる**ので値ごと捨てて note は
// 作る (#2723 の「URL / ID 系」の規則)。
func TestIngest_DropsOversizedNoteURL(t *testing.T) {
	// **全角で埋める。** ASCII だけだと byte で数える実装でも同じ結果になり、
	// rune 判定が守られていることを確かめられない (#2729 のレビュー 2 周目)。
	// permalink に非 ASCII が入る形 (`https://host/@ユーザー/…`) は実在する。
	long := "https://remote.example/" + strings.Repeat("あ", 500)
	require.Greater(t, len([]rune(long)), 512)
	got := ingestNoteWithURL(t, `"`+long+`"`)
	assert.Nil(t, got.URL)
	require.NotNil(t, got.URI)

	// 上限ちょうどは通す。**byte では 1500 を超えるので、byte で数える実装なら
	// ここで落ちる。**
	fit := "https://remote.example/" + strings.Repeat("あ", 512-23)
	require.Equal(t, 512, len([]rune(fit)))
	require.Greater(t, len(fit), 512)
	got = ingestNoteWithURL(t, `"`+fit+`"`)
	require.NotNil(t, got.URL)
	assert.Equal(t, fit, *got.URL)
}

// NUL 入りは列に入らないので捨てる (`fitsColumn`)。
func TestIngest_DropsNoteURLWithNUL(t *testing.T) {
	got := ingestNoteWithURL(t, `"https://remote.example/\u0000x"`)
	assert.Nil(t, got.URL)
}

// inbound Update(Note) でも permalink を追従する (#2729)。
func TestUpdateRemoteNote_UpdatesURL(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	uri := "https://remote.example/notes/n1"
	host := "remote.example"
	old := "https://remote.example/@alice/old"
	noteRepo.Notes["n1"] = &model.Note{
		ID: "n1", URI: &uri, UserID: "alice-id", UserHost: &host, URL: &old,
	}
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{}, idGen)

	body := []byte(`{"id":"https://remote.example/notes/n1","type":"Note",
		"attributedTo":"https://remote.example/users/alice","content":"x",
		"url":"https://remote.example/@alice/new"}`)
	got, err := r.UpdateRemoteNote(body, "")
	require.NoError(t, err)
	require.NotNil(t, got.URL)
	assert.Equal(t, "https://remote.example/@alice/new", *got.URL)
	// **`fields` に載ったことまで見る。** 返り値 (`existing`) は `Notes["n1"]` と
	// 同じ pointer なので、in-memory の値を見ても「載せ忘れ」を検出できない。
	// 実 DB では `fields` に無い列は UPDATE 文に出ず、**stream には新しい url が
	// 出て DB は古いまま**になる (#2729 のレビュー 2 周目)。
	require.Len(t, noteRepo.UpdateFieldsCalls, 1)
	// **型アサーションを裸で書かない。** 載っていないときに panic すると
	// testing がバイナリごと止まり、同じパッケージの残りが実行されない。
	v, ok := noteRepo.UpdateFieldsCalls[0]["url"].(*string)
	require.True(t, ok, "UPDATE に url が載っていない")
	assert.Equal(t, "https://remote.example/@alice/new", *v)
}

// **同じ値では UPDATE に載せない** (#2729)。載せると、他に変化が無い
// `Update(Note)` でも毎回 UPDATE を撃つことになる。
func TestUpdateRemoteNote_SameURLIsNotWrittenAgain(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	uri := "https://remote.example/notes/n1"
	host := "remote.example"
	noteRepo.Notes["n1"] = &model.Note{
		ID: "n1", URI: &uri, UserID: "alice-id", UserHost: &host,
	}
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{}, idGen)

	body := []byte(`{"id":"https://remote.example/notes/n1","type":"Note",
		"attributedTo":"https://remote.example/users/alice","content":"x",
		"url":"https://remote.example/@alice/1"}`)
	_, err := r.UpdateRemoteNote(body, "")
	require.NoError(t, err)
	require.Len(t, noteRepo.UpdateFieldsCalls, 1)
	_, ok := noteRepo.UpdateFieldsCalls[0]["url"]
	require.True(t, ok, "1 回目は載る")

	// 2 回目は url が同じなので載らない。
	_, err = r.UpdateRemoteNote(body, "")
	require.NoError(t, err)
	require.Len(t, noteRepo.UpdateFieldsCalls, 2)
	_, ok = noteRepo.UpdateFieldsCalls[1]["url"]
	assert.False(t, ok, "同じ値では UPDATE に載せない")
}

// **捨てられた値では上書きしない。** 読めない `url` が来ただけで、取り込み時に
// 保存した正しい permalink を消してはいけない (#2729)。
func TestUpdateRemoteNote_KeepsURLWhenNewOneIsDropped(t *testing.T) {
	for _, raw := range []string{`"javascript:alert(1)"`, `null`, `{"id":"https://x/y"}`} {
		t.Run(raw, func(t *testing.T) {
			repo := testutil.NewMockUserRepository()
			noteRepo := testutil.NewMockNoteRepository()
			uri := "https://remote.example/notes/n1"
			host := "remote.example"
			stored := "https://remote.example/@alice/good"
			noteRepo.Notes["n1"] = &model.Note{
				ID: "n1", URI: &uri, UserID: "alice-id", UserHost: &host, URL: &stored,
			}
			urls := activitypub.NewURLBuilder("https://example.com")
			idGen, _ := id.NewGenerator("aidx")
			r := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{}, idGen)

			body := []byte(`{"id":"https://remote.example/notes/n1","type":"Note",
				"attributedTo":"https://remote.example/users/alice","content":"x",
				"url":` + raw + `}`)
			got, err := r.UpdateRemoteNote(body, "")
			require.NoError(t, err)
			require.NotNil(t, got.URL)
			assert.Equal(t, stored, *got.URL)
			// `fields` に url を載せない (= UPDATE 文に出さない) ことまで見る。
			for _, f := range noteRepo.UpdateFieldsCalls {
				_, ok := f["url"]
				assert.False(t, ok, "捨てた値を UPDATE に載せない")
			}
		})
	}
}
