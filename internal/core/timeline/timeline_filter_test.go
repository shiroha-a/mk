package timeline

import (
	"testing"

	"github.com/lib/pq"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/stretchr/testify/assert"
)

func boolPtr(b bool) *bool    { return &b }
func strPtr(s string) *string { return &s }

// テスト用ノート生成ヘルパ
func makeNote(id string, opts ...func(*model.Note)) *model.Note {
	n := &model.Note{
		ID:         id,
		UserID:     "author",
		Visibility: model.NoteVisibilityPublic,
	}
	for _, o := range opts {
		o(n)
	}
	return n
}

func withFiles(n *model.Note)               { n.FileIDs = pq.StringArray{"file1"} }
func withRenote(n *model.Note)              { n.RenoteID = strPtr("rn1") }
func withText(n *model.Note)                { n.Text = strPtr("hello") }
func withReply(n *model.Note)               { n.ReplyID = strPtr("rp1"); n.ReplyUserID = strPtr("other") }
func withReplySelf(n *model.Note)           { n.ReplyID = strPtr("rp1"); n.ReplyUserID = strPtr("viewer") }
func withUser(uid string) func(*model.Note) { return func(n *model.Note) { n.UserID = uid } }
func withRenoteUser(uid string) func(*model.Note) {
	return func(n *model.Note) { n.RenoteUserID = strPtr(uid) }
}
func withRenoteUserHost(host string) func(*model.Note) {
	return func(n *model.Note) { n.RenoteUserHost = strPtr(host) }
}
func withLocalRenoteUser(n *model.Note) {
	n.RenoteUserHost = nil
}

func TestIsPureRenote(t *testing.T) {
	assert.True(t, isPureRenote(makeNote("1", withRenote)))
	assert.False(t, isPureRenote(makeNote("2")))
	assert.False(t, isPureRenote(makeNote("3", withRenote, withText)))
	assert.False(t, isPureRenote(makeNote("4", withRenote, withFiles)))
}

func TestApplyFilter_WithFiles(t *testing.T) {
	notes := []*model.Note{
		makeNote("1"),
		makeNote("2", withFiles),
	}
	out := ApplyFilter(notes, "", TimelineFilter{WithFiles: true})
	assert.Len(t, out, 1)
	assert.Equal(t, "2", out[0].ID)
}

func TestApplyFilter_WithRenotes_False(t *testing.T) {
	notes := []*model.Note{
		makeNote("1"),
		makeNote("2", withRenote),            // pure renote → 除外
		makeNote("3", withRenote, withText),  // quote renote → 残る
		makeNote("4", withRenote, withFiles), // quote renote (file) → 残る
	}
	out := ApplyFilter(notes, "", TimelineFilter{WithRenotes: boolPtr(false)})
	assert.Len(t, out, 3)
}

func TestApplyFilter_WithRenotes_Default(t *testing.T) {
	notes := []*model.Note{
		makeNote("1"),
		makeNote("2", withRenote),
	}
	// WithRenotes nil → true (全部残る)
	out := ApplyFilter(notes, "", TimelineFilter{})
	assert.Len(t, out, 2)
}

func TestApplyFilter_WithReplies_False(t *testing.T) {
	notes := []*model.Note{
		makeNote("1"),
		makeNote("2", withReply),     // 他人への返信 → 除外
		makeNote("3", withReplySelf), // 自分への返信 → 残る
	}
	out := ApplyFilter(notes, "viewer", TimelineFilter{WithReplies: boolPtr(false)})
	assert.Len(t, out, 2)
	assert.Equal(t, "1", out[0].ID)
	assert.Equal(t, "3", out[1].ID)
}

func TestApplyFilter_WithReplies_True(t *testing.T) {
	notes := []*model.Note{
		makeNote("1"),
		makeNote("2", withReply),
	}
	out := ApplyFilter(notes, "viewer", TimelineFilter{WithReplies: boolPtr(true)})
	assert.Len(t, out, 2)
}

func TestApplyFilter_WithReplies_NoViewer(t *testing.T) {
	notes := []*model.Note{
		makeNote("1"),
		makeNote("2", withReply),
	}
	// viewerなし + withReplies=false → 全返信除外
	out := ApplyFilter(notes, "", TimelineFilter{WithReplies: boolPtr(false)})
	assert.Len(t, out, 1)
}

func TestApplyFilter_IncludeMyRenotes_False(t *testing.T) {
	notes := []*model.Note{
		makeNote("1"),
		makeNote("2", withRenote, withUser("viewer")),           // viewerのpure renote → 除外
		makeNote("3", withRenote, withUser("other")),            // 他人のpure renote → 残る
		makeNote("4", withRenote, withText, withUser("viewer")), // viewerのquote → 残る
	}
	out := ApplyFilter(notes, "viewer", TimelineFilter{IncludeMyRenotes: boolPtr(false)})
	assert.Len(t, out, 3)
}

func TestApplyFilter_IncludeRenotedMyNotes_False(t *testing.T) {
	notes := []*model.Note{
		makeNote("1"),
		makeNote("2", withRenote, withRenoteUser("viewer")), // viewerのノートのpure renote → 除外
		makeNote("3", withRenote, withRenoteUser("other")),  // 他人のノートのpure renote → 残る
	}
	out := ApplyFilter(notes, "viewer", TimelineFilter{IncludeRenotedMyNotes: boolPtr(false)})
	assert.Len(t, out, 2)
}

func TestApplyFilter_IncludeLocalRenotes_False(t *testing.T) {
	notes := []*model.Note{
		makeNote("1"),
		makeNote("2", withRenote, withLocalRenoteUser),              // ローカルpure renote → 除外
		makeNote("3", withRenote, withRenoteUserHost("remote.tld")), // リモートpure renote → 残る
	}
	out := ApplyFilter(notes, "viewer", TimelineFilter{IncludeLocalRenotes: boolPtr(false)})
	assert.Len(t, out, 2)
}

func TestApplyFilter_CombinedFilters(t *testing.T) {
	notes := []*model.Note{
		makeNote("1", withFiles),                       // ファイル付き、通常ノート → 残る
		makeNote("2"),                                  // ファイルなし → 除外 (withFiles)
		makeNote("3", withRenote),                      // pure renote (ファイルなし) → 除外 (both)
		makeNote("4", withFiles, withRenote, withText), // ファイル付きquote → 残る
	}
	out := ApplyFilter(notes, "", TimelineFilter{
		WithFiles:   true,
		WithRenotes: boolPtr(false),
	})
	assert.Len(t, out, 2)
	assert.Equal(t, "1", out[0].ID)
	assert.Equal(t, "4", out[1].ID)
}

func TestBoolDefault(t *testing.T) {
	assert.True(t, boolDefault(nil, true))
	assert.False(t, boolDefault(nil, false))
	assert.True(t, boolDefault(boolPtr(true), false))
	assert.False(t, boolDefault(boolPtr(false), true))
}

func withChannel(id string) func(*model.Note) {
	return func(n *model.Note) { n.ChannelID = strPtr(id) }
}

func TestApplyFilter_MutedChannelsExcluded(t *testing.T) {
	notes := []*model.Note{
		makeNote("1"),                            // channelId なし → 残る
		makeNote("2", withChannel("ch-muted")),   // mute 対象 → 除外
		makeNote("3", withChannel("ch-allowed")), // 非 mute → 残る
		makeNote("4", withChannel("ch-muted")),   // mute 対象 (重複) → 除外
	}
	out := ApplyFilter(notes, "viewer", TimelineFilter{
		MutedChannelIDs: []string{"ch-muted"},
	})
	assert.Len(t, out, 2)
	assert.Equal(t, "1", out[0].ID)
	assert.Equal(t, "3", out[1].ID)
}

func TestApplyFilter_MutedChannelsEmptyIsNoop(t *testing.T) {
	notes := []*model.Note{
		makeNote("1", withChannel("any")),
		makeNote("2"),
	}
	// 空 slice は全件通す (従来挙動の維持)
	out := ApplyFilter(notes, "", TimelineFilter{})
	assert.Len(t, out, 2)
}

func TestApplyFilter_MutedUsersExcluded(t *testing.T) {
	// author = note 投稿者。muted-author の note は除外される (#874)。
	notes := []*model.Note{
		makeNote("1", withUser("normal")),
		makeNote("2", withUser("muted-author")),
		makeNote("3", withUser("normal2")),
	}
	out := ApplyFilter(notes, "viewer", TimelineFilter{
		MutedUserIDs: []string{"muted-author"},
	})
	assert.Len(t, out, 2)
	assert.Equal(t, "1", out[0].ID)
	assert.Equal(t, "3", out[1].ID)
}

func TestApplyFilter_MutedRenoteSourceExcluded(t *testing.T) {
	// renote 元 user が muted の場合、renote note 自体も除外する
	// (= upstream Misskey TS の muting JOIN と同 semantics、#874)。
	notes := []*model.Note{
		makeNote("1", withUser("normal"), withRenote, withRenoteUser("muted-source")),
		makeNote("2", withUser("normal"), withRenote, withRenoteUser("ok-source")),
	}
	out := ApplyFilter(notes, "viewer", TimelineFilter{
		MutedUserIDs: []string{"muted-source"},
	})
	assert.Len(t, out, 1)
	assert.Equal(t, "2", out[0].ID)
}

func TestApplyFilter_MutedUsersEmptyIsNoop(t *testing.T) {
	notes := []*model.Note{
		makeNote("1", withUser("any")),
		makeNote("2", withUser("any2")),
	}
	// nil / 空 slice は filter 無効 (= 従来挙動)
	out := ApplyFilter(notes, "viewer", TimelineFilter{MutedUserIDs: nil})
	assert.Len(t, out, 2)
	out2 := ApplyFilter(notes, "viewer", TimelineFilter{MutedUserIDs: []string{}})
	assert.Len(t, out2, 2)
}

// renote-mute は投稿者が renote-muted で **かつ** pure renote の場合のみ
// 除外 (#903)。投稿者の plain note / quote renote はそのまま通る。
func TestApplyFilter_RenoteMutedPureRenoteExcluded(t *testing.T) {
	notes := []*model.Note{
		makeNote("1", withUser("muted-renoter"), withRenote),
		makeNote("2", withUser("ok-renoter"), withRenote),
	}
	out := ApplyFilter(notes, "viewer", TimelineFilter{
		RenoteMutedUserIDs: []string{"muted-renoter"},
	})
	assert.Len(t, out, 1)
	assert.Equal(t, "2", out[0].ID)
}

// renote-mute は **plain note は通す** (regular mute との違い、#903)。
// 投稿者が renote-mute されていても、その人の plain な note は表示する。
func TestApplyFilter_RenoteMutedPlainNoteKept(t *testing.T) {
	notes := []*model.Note{
		makeNote("1", withUser("muted-renoter"), withText),   // plain note
		makeNote("2", withUser("muted-renoter"), withRenote), // pure renote → skip
	}
	out := ApplyFilter(notes, "viewer", TimelineFilter{
		RenoteMutedUserIDs: []string{"muted-renoter"},
	})
	assert.Len(t, out, 1)
	assert.Equal(t, "1", out[0].ID, "plain note (text 付き) は renote-mute 対象外")
}

// renote-mute は **quote renote (text 付き / file 付き renote) も通す**
// (#903、isPureRenote が false なので skip 対象外)。
func TestApplyFilter_RenoteMutedQuoteRenoteKept(t *testing.T) {
	notes := []*model.Note{
		makeNote("1", withUser("muted-renoter"), withRenote, withText),  // quote renote
		makeNote("2", withUser("muted-renoter"), withRenote, withFiles), // file 付き renote
		makeNote("3", withUser("muted-renoter"), withRenote),            // pure renote → skip
	}
	out := ApplyFilter(notes, "viewer", TimelineFilter{
		RenoteMutedUserIDs: []string{"muted-renoter"},
	})
	assert.Len(t, out, 2)
	assert.Equal(t, "1", out[0].ID)
	assert.Equal(t, "2", out[1].ID)
}
