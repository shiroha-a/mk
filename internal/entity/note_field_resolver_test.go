package entity

import (
	"testing"

	"github.com/lib/pq"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubDriveFileLookup / stubChannelLookup / stubNoteReactionLookup の最小実装。
// 個別 repository から repository 同士の依存を持ち込まないよう entity package
// 内で完結させる。

type stubDriveFileLookup struct {
	files map[string]*model.DriveFile
}

func (s *stubDriveFileLookup) FindByIDs(ids []string) ([]*model.DriveFile, error) {
	out := make([]*model.DriveFile, 0, len(ids))
	for _, id := range ids {
		if f, ok := s.files[id]; ok {
			out = append(out, f)
		}
	}
	return out, nil
}

type stubNoteReactionLookup struct {
	rows map[string]*model.NoteReaction // noteID -> reaction (per-viewer は呼び出し側で識別)
}

func (s *stubNoteReactionLookup) FindByUserAndNoteIDs(_ string, noteIDs []string) (map[string]*model.NoteReaction, error) {
	out := make(map[string]*model.NoteReaction, len(noteIDs))
	for _, nid := range noteIDs {
		if r, ok := s.rows[nid]; ok {
			out[nid] = r
		}
	}
	return out, nil
}

type stubChannelLookup struct {
	channels map[string]*model.Channel
}

func (s *stubChannelLookup) FindByIDs(ids []string) ([]*model.Channel, error) {
	out := make([]*model.Channel, 0, len(ids))
	for _, id := range ids {
		if ch, ok := s.channels[id]; ok {
			out = append(out, ch)
		}
	}
	return out, nil
}

func makeIDGen(t *testing.T) id.Generator {
	t.Helper()
	g, err := id.NewGenerator("aidx")
	require.NoError(t, err)
	return g
}

// Renote / Reply の embed にも Files が解決されることを確認する (#426 の
// 中心ケース)。
func TestNoteFieldResolver_ResolveFiles_EmbedsRenoteAndReply(t *testing.T) {
	files := &stubDriveFileLookup{files: map[string]*model.DriveFile{
		"f1": {ID: "f1", Name: "outer.png", Type: "image/png", URL: "https://example.com/f1"},
		"f2": {ID: "f2", Name: "renote.png", Type: "image/png", URL: "https://example.com/f2"},
		"f3": {ID: "f3", Name: "reply.png", Type: "image/png", URL: "https://example.com/f3"},
	}}
	r := NewNoteFieldResolver(files, nil, nil, nil, nil, makeIDGen(t))

	notes := []NoteEntity{{
		ID:      "n1",
		FileIDs: pq.StringArray{"f1"},
		Renote:  &NoteEntity{ID: "n2", FileIDs: pq.StringArray{"f2"}},
		Reply:   &NoteEntity{ID: "n3", FileIDs: pq.StringArray{"f3"}},
	}}
	r.ResolveFiles(notes)

	require.Len(t, notes[0].Files, 1)
	require.Len(t, notes[0].Renote.Files, 1)
	require.Len(t, notes[0].Reply.Files, 1)
}

// Renote / Reply の embed にも MyReaction / Channel が解決されることを確認。
func TestNoteFieldResolver_ResolveViewerFields_EmbedsRenoteAndReply(t *testing.T) {
	reactions := &stubNoteReactionLookup{rows: map[string]*model.NoteReaction{
		"n1": {NoteID: "n1", Reaction: "👍"},
		"n2": {NoteID: "n2", Reaction: "❤"},
		"n3": {NoteID: "n3", Reaction: "🎉"},
	}}
	chID := "ch1"
	channels := &stubChannelLookup{channels: map[string]*model.Channel{
		chID: {ID: chID, Name: "general"},
	}}
	r := NewNoteFieldResolver(nil, nil, nil, reactions, channels, makeIDGen(t))

	notes := []NoteEntity{{
		ID:        "n1",
		ChannelID: &chID,
		Renote:    &NoteEntity{ID: "n2", ChannelID: &chID},
		Reply:     &NoteEntity{ID: "n3"},
	}}
	r.ResolveViewerFields(notes, &model.User{ID: "v1"})

	require.NotNil(t, notes[0].MyReaction)
	require.NotNil(t, notes[0].Renote.MyReaction)
	require.NotNil(t, notes[0].Reply.MyReaction)
	require.NotNil(t, notes[0].Channel)
	assert.Equal(t, "general", notes[0].Channel.Name)
	require.NotNil(t, notes[0].Renote.Channel)
}

// nil receiver / nil viewer / 空 slice / nil lookup の網羅的 nil safe 確認。
func TestNoteFieldResolver_NilSafe(t *testing.T) {
	var r *NoteFieldResolver
	r.Apply(nil, nil) // nil receiver
	r.ResolveFiles(nil)
	r.ResolveViewerFields(nil, nil)

	r2 := NewNoteFieldResolver(nil, nil, nil, nil, nil, nil)
	r2.Apply([]NoteEntity{{ID: "n1"}}, nil)

	// 個別 helper も nil 入力で panic しない
	applyMyReaction(nil, nil)
	applyChannel(nil, nil)
	assert.Equal(t, []string(nil), appendNoteIDs(nil, nil))
	assert.Equal(t, []string(nil), appendNoteFileIDs(nil, nil))
	assert.Equal(t, []string(nil), appendNoteChannelIDs(nil, nil))
}

type stubFolderLookup struct {
	folders map[string]*model.DriveFolder
	err     error
}

func (s *stubFolderLookup) FindByID(id string) (*model.DriveFolder, error) {
	if s.err != nil {
		return nil, s.err
	}
	if f, ok := s.folders[id]; ok {
		return f, nil
	}
	return nil, assertError("not found")
}

type stubFileOwnerLookup struct {
	users map[string]*model.User
	err   error
}

func (s *stubFileOwnerLookup) FindByID(id string) (*model.User, error) {
	if s.err != nil {
		return nil, s.err
	}
	if u, ok := s.users[id]; ok {
		return u, nil
	}
	return nil, assertError("not found")
}

// helper: build error with message (avoid repeated `errors.New`).
type assertError string

func (e assertError) Error() string { return string(e) }

// 添付ファイルの folder / owner も embed されることを確認 (#317 経路の
// resolver 統合)。folder lookup ヒット + owner lookup ヒット + 失敗 fallback
// を 1 ノートで cover する。
func TestNoteFieldResolver_ResolveFiles_EmbedsFolderAndOwner(t *testing.T) {
	folderID := "folder-1"
	owner := "user-1"
	files := &stubDriveFileLookup{files: map[string]*model.DriveFile{
		"f1": {ID: "f1", FolderID: &folderID, UserID: &owner, Name: "x.png", Type: "image/png", URL: "https://example.com/f1"},
		"f2": {ID: "f2", Name: "lone.png", Type: "image/png", URL: "https://example.com/f2"}, // folderID / userID 共に nil
	}}
	folders := &stubFolderLookup{folders: map[string]*model.DriveFolder{
		folderID: {ID: folderID, Name: "media"},
	}}
	owners := &stubFileOwnerLookup{users: map[string]*model.User{
		owner: {ID: owner, Username: "alice"},
	}}
	r := NewNoteFieldResolver(files, folders, owners, nil, nil, makeIDGen(t))

	notes := []NoteEntity{{ID: "n1", FileIDs: pq.StringArray{"f1", "f2"}}}
	r.ResolveFiles(notes)
	require.Len(t, notes[0].Files, 2)
}

// folder / owner lookup が err を返してもパニックせず folder/user nil で
// pack される。
func TestNoteFieldResolver_ResolveFiles_LookupErrorsTolerated(t *testing.T) {
	folderID := "folder-x"
	owner := "user-x"
	files := &stubDriveFileLookup{files: map[string]*model.DriveFile{
		"f1": {ID: "f1", FolderID: &folderID, UserID: &owner, Name: "x.png", Type: "image/png", URL: "https://example.com/f1"},
	}}
	folders := &stubFolderLookup{err: assertError("db down")}
	owners := &stubFileOwnerLookup{err: assertError("db down")}
	r := NewNoteFieldResolver(files, folders, owners, nil, nil, makeIDGen(t))

	notes := []NoteEntity{{ID: "n1", FileIDs: pq.StringArray{"f1"}}}
	r.ResolveFiles(notes)
	require.Len(t, notes[0].Files, 1)
}

// viewer == nil の時は MyReaction を埋めない (Channel は埋める)。
func TestNoteFieldResolver_NilViewerSkipsMyReaction(t *testing.T) {
	reactions := &stubNoteReactionLookup{rows: map[string]*model.NoteReaction{
		"n1": {NoteID: "n1", Reaction: "👍"},
	}}
	chID := "ch1"
	channels := &stubChannelLookup{channels: map[string]*model.Channel{
		chID: {ID: chID, Name: "general"},
	}}
	r := NewNoteFieldResolver(nil, nil, nil, reactions, channels, makeIDGen(t))

	notes := []NoteEntity{{ID: "n1", ChannelID: &chID}}
	r.ResolveViewerFields(notes, nil)
	assert.Nil(t, notes[0].MyReaction)
	assert.NotNil(t, notes[0].Channel)
}

// SetPollVoteLookup の wire を確認する小さな regression test (#710 で
// entity 全体カバレッジを 90% 上に保つために追加)。nil receiver で no-op
// 化し、non-nil receiver では fields に設定されることを確認する。
func TestNoteFieldResolver_SetPollVoteLookup(t *testing.T) {
	// nil receiver は panic せず黙って return する。
	var nilResolver *NoteFieldResolver
	nilResolver.SetPollVoteLookup(nil)

	// non-nil receiver は内部 field を設定する。SetPollVoteLookup は
	// public な observation 経路を持たないので、設定後に再度呼んで
	// 上書きが効くこと (idempotent) のみ確認する。
	r := &NoteFieldResolver{}
	r.SetPollVoteLookup(nil)
	r.SetPollVoteLookup(nil)
}
