package entity

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/lib/pq"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
)

func newTestIDGen(t *testing.T) id.Generator {
	t.Helper()
	g, err := id.NewGenerator("aidx")
	require.NoError(t, err)
	return g
}

func TestPackNote_Basic(t *testing.T) {
	idGen := newTestIDGen(t)

	text := "Hello, world!"
	cw := "CW"
	noteID := idGen.Generate(time.Now())
	note := &model.Note{
		ID:         noteID,
		UserID:     "user1",
		Text:       &text,
		CW:         &cw,
		Visibility: model.NoteVisibilityPublic,
		Reactions:  datatypes.JSON([]byte("{}")),
		FileIDs:    pq.StringArray{"file1", "file2"},
		Tags:       pq.StringArray{"tag1"},
	}

	entity := PackNote(note, idGen)

	assert.Equal(t, noteID, entity.ID)
	assert.Equal(t, "user1", entity.UserID)
	assert.Equal(t, &text, entity.Text)
	assert.Equal(t, &cw, entity.CW)
	assert.Equal(t, "public", entity.Visibility)
	assert.Equal(t, []string{"file1", "file2"}, entity.FileIDs)
	assert.Equal(t, []string{"tag1"}, entity.Tags)
	assert.NotEmpty(t, entity.CreatedAt)
	// #1639: local note (UserHost==nil) は emojis を出さない (nil → 省略)。
	assert.Nil(t, entity.Emojis)
	assert.NotNil(t, entity.Files)
	assert.Empty(t, entity.Files)
}

func TestPackNote_NilArrays(t *testing.T) {
	idGen := newTestIDGen(t)
	noteID := idGen.Generate(time.Now())

	note := &model.Note{
		ID:         noteID,
		UserID:     "user1",
		Visibility: model.NoteVisibilityHome,
		Reactions:  datatypes.JSON([]byte("{}")),
	}

	entity := PackNote(note, idGen)

	// FileIDsはnilでも空スライスに変換、Tagsはomitemptyでnil維持
	assert.NotNil(t, entity.FileIDs)
	assert.Empty(t, entity.FileIDs)
	assert.Nil(t, entity.Tags) // omitempty: nil → JSON省略
}

func TestPackNote_WithUser(t *testing.T) {
	idGen := newTestIDGen(t)
	noteID := idGen.Generate(time.Now())

	note := &model.Note{
		ID:         noteID,
		UserID:     "user1",
		Visibility: model.NoteVisibilityPublic,
		Reactions:  datatypes.JSON([]byte("{}")),
		User: &model.User{
			ID:                "user1",
			Username:          "testuser",
			AvatarDecorations: datatypes.JSON([]byte("[]")),
		},
	}

	entity := PackNote(note, idGen)

	assert.Equal(t, "user1", entity.User.ID)
	assert.Equal(t, "testuser", entity.User.Username)
}

func TestPackNote_ReactionCount(t *testing.T) {
	idGen := newTestIDGen(t)
	noteID := idGen.Generate(time.Now())

	note := &model.Note{
		ID:         noteID,
		UserID:     "user1",
		Visibility: model.NoteVisibilityPublic,
		Reactions:  datatypes.JSON([]byte(`{"👍":3,"❤️":2,"🎉":1}`)),
	}

	entity := PackNote(note, idGen)
	assert.Equal(t, 6, entity.ReactionCount)
}

func TestPackNote_ReactionCount_Empty(t *testing.T) {
	idGen := newTestIDGen(t)
	noteID := idGen.Generate(time.Now())

	note := &model.Note{
		ID:         noteID,
		UserID:     "user1",
		Visibility: model.NoteVisibilityPublic,
		Reactions:  datatypes.JSON([]byte("{}")),
	}

	entity := PackNote(note, idGen)
	assert.Equal(t, 0, entity.ReactionCount)
}

func TestPackNote_ReactionCount_NilReactions(t *testing.T) {
	idGen := newTestIDGen(t)
	noteID := idGen.Generate(time.Now())

	note := &model.Note{
		ID:         noteID,
		UserID:     "user1",
		Visibility: model.NoteVisibilityPublic,
	}

	entity := PackNote(note, idGen)
	assert.Equal(t, 0, entity.ReactionCount)
}

func TestPackNote_ReactionCount_InvalidJSON(t *testing.T) {
	idGen := newTestIDGen(t)
	noteID := idGen.Generate(time.Now())

	note := &model.Note{
		ID:         noteID,
		UserID:     "user1",
		Visibility: model.NoteVisibilityPublic,
		Reactions:  datatypes.JSON([]byte("invalid")),
	}

	entity := PackNote(note, idGen)
	assert.Equal(t, 0, entity.ReactionCount)
}

func TestPackNote_VisibleUserIDs_Mentions_HasPoll(t *testing.T) {
	idGen := newTestIDGen(t)
	noteID := idGen.Generate(time.Now())

	note := &model.Note{
		ID:             noteID,
		UserID:         "user1",
		Visibility:     model.NoteVisibilitySpecified,
		Reactions:      datatypes.JSON([]byte("{}")),
		VisibleUserIDs: pq.StringArray{"user2", "user3"},
		Mentions:       pq.StringArray{"user2"},
		HasPoll:        true,
	}

	entity := PackNote(note, idGen)

	assert.Equal(t, []string{"user2", "user3"}, entity.VisibleUserIDs)
	assert.Equal(t, []string{"user2"}, entity.Mentions)
	assert.True(t, entity.HasPoll)
}

func TestPackNote_NilVisibleUserIDs_Mentions(t *testing.T) {
	idGen := newTestIDGen(t)
	noteID := idGen.Generate(time.Now())

	note := &model.Note{
		ID:         noteID,
		UserID:     "user1",
		Visibility: model.NoteVisibilityPublic,
		Reactions:  datatypes.JSON([]byte("{}")),
	}

	entity := PackNote(note, idGen)

	// #1561: public note では visibleUserIds は省略 (specified 限定) / mentions は
	// 空なら省略 / hasPoll は false なら省略 → いずれも nil で omitempty が落とす。
	assert.Nil(t, entity.VisibleUserIDs, "public note omits visibleUserIds")
	assert.Nil(t, entity.Mentions, "empty mentions omitted")
	assert.False(t, entity.HasPoll)

	// JSON でも該当 key が出ないこと。
	b, err := json.Marshal(entity)
	require.NoError(t, err)
	s := string(b)
	assert.NotContains(t, s, `"visibleUserIds"`)
	assert.NotContains(t, s, `"mentions"`)
	assert.NotContains(t, s, `"hasPoll"`)
}

func TestNormalizeReactionKey(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"local legacy", ":smile:", ":smile@.:"},
		{"local canonical", ":smile@.:", ":smile@.:"},
		{"remote", ":smile@remote.example:", ":smile@remote.example:"},
		{"unicode emoji", "👍", "👍"},
		{"heart", "❤", "❤"},
		{"empty", "", ""},
		{"hyphen name", ":cat-smile:", ":cat-smile@.:"},
		{"plus name", ":thumbs+up:", ":thumbs+up@.:"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, NormalizeReactionKey(tc.in))
		})
	}
}

func TestPackNote_ReactionsKeyNormalized(t *testing.T) {
	idGen := newTestIDGen(t)
	noteID := idGen.Generate(time.Now())

	// TS時代の `:lifelog:` とmk時代の `:lifelog@.:` が混在
	note := &model.Note{
		ID:         noteID,
		UserID:     "user1",
		Visibility: model.NoteVisibilityPublic,
		Reactions:  datatypes.JSON([]byte(`{":lifelog:":1,":lifelog@.:" :1,"👍":3}`)),
	}

	e := PackNote(note, idGen)

	var reactions map[string]float64
	require.NoError(t, json.Unmarshal(e.Reactions, &reactions))
	// `:lifelog:` と `:lifelog@.:` が統合される
	assert.Equal(t, float64(2), reactions[":lifelog@.:"])
	assert.Equal(t, float64(3), reactions["👍"])
	_, hasLegacy := reactions[":lifelog:"]
	assert.False(t, hasLegacy)
	assert.Equal(t, 5, e.ReactionCount)
}

func TestPackNote_WithRenoteAndReply(t *testing.T) {
	idGen := newTestIDGen(t)
	renoteID := idGen.Generate(time.Now())
	replyID := idGen.Generate(time.Now())
	noteID := idGen.Generate(time.Now())

	renoteText := "original"
	replyText := "parent reply"
	text := "quoting"

	note := &model.Note{
		ID:         noteID,
		UserID:     "user1",
		Text:       &text,
		RenoteID:   &renoteID,
		ReplyID:    &replyID,
		Visibility: model.NoteVisibilityPublic,
		Reactions:  datatypes.JSON([]byte("{}")),
		Renote: &model.Note{
			ID:         renoteID,
			UserID:     "user2",
			Text:       &renoteText,
			Visibility: model.NoteVisibilityPublic,
			Reactions:  datatypes.JSON([]byte("{}")),
			User: &model.User{
				ID:                "user2",
				Username:          "author2",
				AvatarDecorations: datatypes.JSON([]byte("[]")),
			},
		},
		Reply: &model.Note{
			ID:         replyID,
			UserID:     "user3",
			Text:       &replyText,
			Visibility: model.NoteVisibilityPublic,
			Reactions:  datatypes.JSON([]byte("{}")),
		},
	}

	e := PackNote(note, idGen)

	require.NotNil(t, e.Renote)
	assert.Equal(t, renoteID, e.Renote.ID)
	assert.Equal(t, &renoteText, e.Renote.Text)
	assert.Equal(t, "author2", e.Renote.User.Username)

	require.NotNil(t, e.Reply)
	assert.Equal(t, replyID, e.Reply.ID)
	assert.Equal(t, &replyText, e.Reply.Text)
}

func TestPackNotes_EmbeddedRenoteHasInstanceAndEmoji(t *testing.T) {
	idGen := newTestIDGen(t)
	renoteID := idGen.Generate(time.Now())
	noteID := idGen.Generate(time.Now())
	remoteHost := "remote.example"

	renote := &model.Note{
		ID:         renoteID,
		UserID:     "user2",
		Visibility: model.NoteVisibilityPublic,
		Reactions:  datatypes.JSON([]byte("{}")),
		UserHost:   &remoteHost,
		Emojis:     []string{"wave"},
		User: &model.User{
			ID:                "user2",
			Username:          "remoteuser",
			Host:              &remoteHost,
			AvatarDecorations: datatypes.JSON([]byte("[]")),
			Emojis:            []string{"wave"},
		},
	}
	note := &model.Note{
		ID:         noteID,
		UserID:     "user1",
		RenoteID:   &renoteID,
		Visibility: model.NoteVisibilityPublic,
		Reactions:  datatypes.JSON([]byte("{}")),
		User: &model.User{
			ID:                "user1",
			Username:          "localuser",
			AvatarDecorations: datatypes.JSON([]byte("[]")),
		},
		Renote: renote,
	}

	instLookup := &stubInstanceLookup{data: map[string]*model.Instance{
		remoteHost: {Host: remoteHost, Name: strPtr("Remote")},
	}}
	emojiLookup := &stubEmojiLookup{data: map[string][]*model.Emoji{
		remoteHost: {{Name: "wave", PublicURL: "https://remote.example/emoji/wave.png"}},
	}}

	out := PackNotes(context.Background(), []*model.Note{note}, idGen, instLookup, emojiLookup, nil)
	require.Len(t, out, 1)
	require.NotNil(t, out[0].Renote)
	require.NotNil(t, out[0].Renote.User.Instance)
	assert.Equal(t, strPtr("Remote"), out[0].Renote.User.Instance.Name)
	require.NotNil(t, out[0].Renote.Emojis)
	assert.Equal(t, "https://remote.example/emoji/wave.png", (*out[0].Renote.Emojis)["wave"])
	assert.Equal(t, "https://remote.example/emoji/wave.png", out[0].Renote.User.Emojis["wave"])
}

func TestPackNoteWithInstance_EmbeddedRenote(t *testing.T) {
	idGen := newTestIDGen(t)
	renoteID := idGen.Generate(time.Now())
	noteID := idGen.Generate(time.Now())
	remoteHost := "remote.example"

	note := &model.Note{
		ID:         noteID,
		UserID:     "user1",
		RenoteID:   &renoteID,
		Visibility: model.NoteVisibilityPublic,
		Reactions:  datatypes.JSON([]byte("{}")),
		Renote: &model.Note{
			ID:         renoteID,
			UserID:     "user2",
			Visibility: model.NoteVisibilityPublic,
			Reactions:  datatypes.JSON([]byte("{}")),
			UserHost:   &remoteHost,
			User: &model.User{
				ID:                "user2",
				Username:          "remoteuser",
				Host:              &remoteHost,
				AvatarDecorations: datatypes.JSON([]byte("[]")),
			},
		},
	}

	instLookup := &stubInstanceLookup{data: map[string]*model.Instance{
		remoteHost: {Host: remoteHost, Name: strPtr("Remote")},
	}}

	out := PackNoteWithInstance(context.Background(), note, idGen, instLookup, nil, nil)
	require.NotNil(t, out.Renote)
	require.NotNil(t, out.Renote.User.Instance)
	assert.Equal(t, strPtr("Remote"), out.Renote.User.Instance.Name)
}

// stubBufferedReader is a test double for BufferedReactionsReader.
type stubBufferedReader struct {
	data map[string]map[string]int64
	err  error
}

func (s *stubBufferedReader) GetBufferedMany(_ context.Context, ids []string) (map[string]map[string]int64, error) {
	if s.err != nil {
		return nil, s.err
	}
	out := make(map[string]map[string]int64)
	for _, id := range ids {
		if d, ok := s.data[id]; ok {
			out[id] = d
		}
	}
	return out, nil
}

// reader が non-nil なら DB の reactions と buffered deltas を merge して
// 返す (#647)。emoji も merged keys から resolve される。
func TestPackNotes_MergesBufferedReactions(t *testing.T) {
	idGen := newTestIDGen(t)
	noteID := idGen.Generate(time.Now())
	remoteHost := "remote.example"

	note := &model.Note{
		ID:         noteID,
		UserID:     "user1",
		Visibility: model.NoteVisibilityPublic,
		Reactions:  datatypes.JSON([]byte(`{":existing@.:": 1}`)),
		User:       &model.User{ID: "user1", Username: "alice", AvatarDecorations: datatypes.JSON([]byte("[]"))},
	}
	emojiLookup := &stubEmojiLookup{data: map[string][]*model.Emoji{
		remoteHost: {{Name: "yikes", PublicURL: "https://remote.example/emoji/yikes.png"}},
	}}
	reader := &stubBufferedReader{data: map[string]map[string]int64{
		noteID: {":yikes@" + remoteHost + ":": 1},
	}}

	out := PackNotes(context.Background(), []*model.Note{note}, idGen, nil, emojiLookup, reader)
	require.Len(t, out, 1)
	// merged reactions JSON は両方のキーを含む
	var got map[string]int
	require.NoError(t, json.Unmarshal(out[0].Reactions, &got))
	assert.Equal(t, 1, got[":existing@.:"])
	assert.Equal(t, 1, got[":yikes@"+remoteHost+":"])
	assert.Equal(t, 2, out[0].ReactionCount)
	// reactionEmojis に buffered 由来の emoji 解決結果が入る
	assert.Equal(t, "https://remote.example/emoji/yikes.png", out[0].ReactionEmojis["yikes@"+remoteHost])
}

// 2 件の note が同一 *Note の renote を共有する場合 (repository の batch hydration
// で起こりうる)、buffered delta が二重加算されないこと。mergeBufferedReactions は
// 加算的なので、flat list に同一ポインタが 2 回現れても適用は 1 回に限定する。
func TestPackNotes_SharedRenote_NoDoubleMerge(t *testing.T) {
	idGen := newTestIDGen(t)
	renoteID := idGen.Generate(time.Now().Add(-time.Hour))
	// 共有される renote 先 (base reaction 1)。
	shared := &model.Note{
		ID:         renoteID,
		UserID:     "ru",
		Visibility: model.NoteVisibilityPublic,
		Reactions:  datatypes.JSON([]byte(`{":base@.:": 1}`)),
		User:       &model.User{ID: "ru", Username: "renoter", AvatarDecorations: datatypes.JSON([]byte("[]"))},
	}
	mkRenoter := func(i int) *model.Note {
		return &model.Note{
			ID: idGen.Generate(time.Now().Add(time.Duration(i) * time.Second)), UserID: "u",
			Visibility: model.NoteVisibilityPublic, RenoteID: &renoteID,
			User:   &model.User{ID: "u", Username: "u", AvatarDecorations: datatypes.JSON([]byte("[]"))},
			Renote: shared, // 両者が同一ポインタを共有
		}
	}
	n1, n2 := mkRenoter(0), mkRenoter(1)
	// shared renote に buffered delta +2。
	reader := &stubBufferedReader{data: map[string]map[string]int64{
		renoteID: {":base@.:": 2},
	}}

	out := PackNotes(context.Background(), []*model.Note{n1, n2}, idGen, nil, nil, reader)
	require.Len(t, out, 2)
	// 二重加算なら 1+2+2=5 になる。正しくは 1+2=3 (1 回のみ適用)。
	for _, e := range out {
		require.NotNil(t, e.Renote)
		var got map[string]int
		require.NoError(t, json.Unmarshal(e.Renote.Reactions, &got))
		assert.Equal(t, 3, got[":base@.:"], "shared renote の buffered delta は二重加算されない")
	}
}

// 0 以下になった merged value は出力から除外される。
func TestPackNotes_BufferedNegativeDelta_RemovesKey(t *testing.T) {
	idGen := newTestIDGen(t)
	noteID := idGen.Generate(time.Now())

	note := &model.Note{
		ID:         noteID,
		UserID:     "user1",
		Visibility: model.NoteVisibilityPublic,
		Reactions:  datatypes.JSON([]byte(`{":foo@.:": 1}`)),
		User:       &model.User{ID: "user1", Username: "alice", AvatarDecorations: datatypes.JSON([]byte("[]"))},
	}
	reader := &stubBufferedReader{data: map[string]map[string]int64{
		noteID: {":foo@.:": -1},
	}}

	out := PackNotes(context.Background(), []*model.Note{note}, idGen, nil, nil, reader)
	require.Len(t, out, 1)
	var got map[string]int
	if len(out[0].Reactions) > 0 {
		_ = json.Unmarshal(out[0].Reactions, &got)
	}
	assert.NotContains(t, got, ":foo@.:")
	assert.Equal(t, 0, out[0].ReactionCount)
}

// reader == nil は旧挙動 (DB のみ)。
func TestPackNotes_NilReader_NoMerge(t *testing.T) {
	idGen := newTestIDGen(t)
	noteID := idGen.Generate(time.Now())

	note := &model.Note{
		ID:         noteID,
		UserID:     "user1",
		Visibility: model.NoteVisibilityPublic,
		Reactions:  datatypes.JSON([]byte(`{":foo@.:": 3}`)),
		User:       &model.User{ID: "user1", Username: "alice", AvatarDecorations: datatypes.JSON([]byte("[]"))},
	}
	out := PackNotes(context.Background(), []*model.Note{note}, idGen, nil, nil, nil)
	require.Len(t, out, 1)
	assert.Equal(t, 3, out[0].ReactionCount)
}

// reader が error を返した場合は stale な DB 値で fall back して continue。
func TestPackNotes_ReaderError_FallsBackToDB(t *testing.T) {
	idGen := newTestIDGen(t)
	noteID := idGen.Generate(time.Now())

	note := &model.Note{
		ID:         noteID,
		UserID:     "user1",
		Visibility: model.NoteVisibilityPublic,
		Reactions:  datatypes.JSON([]byte(`{":foo@.:": 2}`)),
		User:       &model.User{ID: "user1", Username: "alice", AvatarDecorations: datatypes.JSON([]byte("[]"))},
	}
	reader := &stubBufferedReader{err: errors.New("redis down")}

	out := PackNotes(context.Background(), []*model.Note{note}, idGen, nil, nil, reader)
	require.Len(t, out, 1)
	assert.Equal(t, 2, out[0].ReactionCount, "reader error 時は DB 値で続行")
}

// embed された Renote の reactions も merge 対象。
func TestPackNotes_MergesBufferedReactions_OnEmbeddedRenote(t *testing.T) {
	idGen := newTestIDGen(t)
	renoteID := idGen.Generate(time.Now())
	noteID := idGen.Generate(time.Now())

	renote := &model.Note{
		ID:         renoteID,
		UserID:     "user2",
		Visibility: model.NoteVisibilityPublic,
		Reactions:  datatypes.JSON([]byte("{}")),
		User:       &model.User{ID: "user2", Username: "bob", AvatarDecorations: datatypes.JSON([]byte("[]"))},
	}
	note := &model.Note{
		ID:         noteID,
		UserID:     "user1",
		RenoteID:   &renoteID,
		Visibility: model.NoteVisibilityPublic,
		Reactions:  datatypes.JSON([]byte("{}")),
		User:       &model.User{ID: "user1", Username: "alice", AvatarDecorations: datatypes.JSON([]byte("[]"))},
		Renote:     renote,
	}
	reader := &stubBufferedReader{data: map[string]map[string]int64{
		renoteID: {":heart@.:": 5},
	}}

	out := PackNotes(context.Background(), []*model.Note{note}, idGen, nil, nil, reader)
	require.Len(t, out, 1)
	require.NotNil(t, out[0].Renote)
	assert.Equal(t, 5, out[0].Renote.ReactionCount)
}

// embed された Reply の reactions も merge 対象 (Renote 経路と対称)。
func TestPackNotes_MergesBufferedReactions_OnEmbeddedReply(t *testing.T) {
	idGen := newTestIDGen(t)
	replyID := idGen.Generate(time.Now())
	noteID := idGen.Generate(time.Now())

	reply := &model.Note{
		ID:         replyID,
		UserID:     "user2",
		Visibility: model.NoteVisibilityPublic,
		Reactions:  datatypes.JSON([]byte(`{":existing@.:": 1}`)),
		User:       &model.User{ID: "user2", Username: "bob", AvatarDecorations: datatypes.JSON([]byte("[]"))},
	}
	note := &model.Note{
		ID:         noteID,
		UserID:     "user1",
		ReplyID:    &replyID,
		Visibility: model.NoteVisibilityPublic,
		Reactions:  datatypes.JSON([]byte("{}")),
		User:       &model.User{ID: "user1", Username: "alice", AvatarDecorations: datatypes.JSON([]byte("[]"))},
		Reply:      reply,
	}
	reader := &stubBufferedReader{data: map[string]map[string]int64{
		replyID: {":heart@.:": 3},
	}}

	out := PackNotes(context.Background(), []*model.Note{note}, idGen, nil, nil, reader)
	require.Len(t, out, 1)
	require.NotNil(t, out[0].Reply)
	assert.Equal(t, 4, out[0].Reply.ReactionCount, "embed reply の DB(1) + buffered(3) が merge される")
}

func TestFlattenNotesPlusRelations_NilSafe(t *testing.T) {
	out := flattenNotesPlusRelations([]*model.Note{nil, {ID: "a"}})
	require.Len(t, out, 1)
	assert.Equal(t, "a", out[0].ID)
}

func TestPackNote_CreatedAtParsing(t *testing.T) {
	idGen := newTestIDGen(t)
	noteID := idGen.Generate(time.Now())

	note := &model.Note{
		ID:         noteID,
		UserID:     "user1",
		Visibility: model.NoteVisibilityPublic,
		Reactions:  datatypes.JSON([]byte("{}")),
	}

	entity := PackNote(note, idGen)

	// createdAtはISO 8601形式
	assert.Contains(t, entity.CreatedAt, "T")
	assert.Contains(t, entity.CreatedAt, "Z")
}
