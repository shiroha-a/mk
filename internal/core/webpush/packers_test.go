package webpush_test

import (
	"testing"
	"time"

	"github.com/shiroha-a/mk/internal/core/webpush"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
)

func TestUserRepoPacker_Found(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	repo.Users["u1"] = &model.User{ID: "u1", Username: "alice", UsernameLower: "alice"}

	p := webpush.NewUserRepoPacker(repo)
	out, ok := p.PackUserByID("u1")
	assert.True(t, ok)
	assert.Equal(t, "alice", out["username"])
}

func TestUserRepoPacker_NotFound(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	p := webpush.NewUserRepoPacker(repo)
	_, ok := p.PackUserByID("missing")
	assert.False(t, ok)
}

func TestUserRepoPacker_NilRepo(t *testing.T) {
	p := webpush.NewUserRepoPacker(nil)
	_, ok := p.PackUserByID("x")
	assert.False(t, ok)
}

func TestUserRepoPacker_NilReceiver(t *testing.T) {
	var p *webpush.UserRepoPacker
	_, ok := p.PackUserByID("x")
	assert.False(t, ok)
}

func TestNoteRepoPacker_NilReceiver(t *testing.T) {
	var p *webpush.NoteRepoPacker
	_, ok := p.PackNoteByID("x", "viewer")
	assert.False(t, ok)
}

func TestNoteRepoPacker_NilRepo(t *testing.T) {
	idGen, _ := id.NewGenerator("aidx")
	p := webpush.NewNoteRepoPacker(nil, idGen, nil)
	_, ok := p.PackNoteByID("x", "viewer")
	assert.False(t, ok)
}

func TestNoteRepoPacker_Found(t *testing.T) {
	idGen, _ := id.NewGenerator("aidx")
	repo := testutil.NewMockNoteRepository()
	now := time.Now()
	n := &model.Note{
		ID:         idGen.Generate(now),
		UserID:     "u1",
		Visibility: model.NoteVisibilityPublic,
	}
	text := "hello"
	n.Text = &text
	repo.Notes[n.ID] = n

	p := webpush.NewNoteRepoPacker(repo, idGen, nil)
	out, ok := p.PackNoteByID(n.ID, "viewer") // public -> visible to any viewer
	assert.True(t, ok)
	assert.Equal(t, n.ID, out["id"])
	assert.Equal(t, "hello", out["text"])
}

func TestNoteRepoPacker_NotFound(t *testing.T) {
	idGen, _ := id.NewGenerator("aidx")
	repo := testutil.NewMockNoteRepository()
	p := webpush.NewNoteRepoPacker(repo, idGen, nil)
	_, ok := p.PackNoteByID("missing", "viewer")
	assert.False(t, ok)
}

// --- #1572: recipient-visibility gate on the embedded note ---

// seedNote adds a note of the given visibility (authored by `author`) to repo.
func seedNote(repo *testutil.MockNoteRepository, idGen id.Generator, author string, vis model.NoteVisibility, visibleUserIDs []string) *model.Note {
	n := &model.Note{
		ID:             idGen.Generate(time.Now()),
		UserID:         author,
		Visibility:     vis,
		VisibleUserIDs: visibleUserIDs,
	}
	text := "secret"
	n.Text = &text
	repo.Notes[n.ID] = n
	return n
}

func seedFollow(f *testutil.MockFollowingRepository, follower, followee string) {
	f.Followings[follower+"->"+followee] = &model.Following{FollowerID: follower, FolloweeID: followee}
}

func TestNoteRepoPacker_FollowersHiddenFromNonFollower(t *testing.T) {
	idGen, _ := id.NewGenerator("aidx")
	repo := testutil.NewMockNoteRepository()
	follow := testutil.NewMockFollowingRepository()
	n := seedNote(repo, idGen, "author", model.NoteVisibilityFollowers, nil)

	p := webpush.NewNoteRepoPacker(repo, idGen, follow)
	_, ok := p.PackNoteByID(n.ID, "stranger") // does not follow author, not author
	assert.False(t, ok, "followers note must be dropped from a non-follower recipient")
}

func TestNoteRepoPacker_FollowersVisibleToFollower(t *testing.T) {
	idGen, _ := id.NewGenerator("aidx")
	repo := testutil.NewMockNoteRepository()
	follow := testutil.NewMockFollowingRepository()
	n := seedNote(repo, idGen, "author", model.NoteVisibilityFollowers, nil)
	seedFollow(follow, "viewer", "author")

	p := webpush.NewNoteRepoPacker(repo, idGen, follow)
	_, ok := p.PackNoteByID(n.ID, "viewer")
	assert.True(t, ok, "followers note must be visible to a follower recipient")
}

func TestNoteRepoPacker_FollowersVisibleToAuthor(t *testing.T) {
	idGen, _ := id.NewGenerator("aidx")
	repo := testutil.NewMockNoteRepository()
	follow := testutil.NewMockFollowingRepository()
	n := seedNote(repo, idGen, "author", model.NoteVisibilityFollowers, nil)

	p := webpush.NewNoteRepoPacker(repo, idGen, follow)
	// self-notification (reaction / pollEnded etc.): author must see their own note.
	_, ok := p.PackNoteByID(n.ID, "author")
	assert.True(t, ok, "author recipient must always see their own note")
}

func TestNoteRepoPacker_SpecifiedMembership(t *testing.T) {
	idGen, _ := id.NewGenerator("aidx")
	repo := testutil.NewMockNoteRepository()
	n := seedNote(repo, idGen, "author", model.NoteVisibilitySpecified, []string{"viewer"})

	p := webpush.NewNoteRepoPacker(repo, idGen, nil)
	_, ok := p.PackNoteByID(n.ID, "viewer")
	assert.True(t, ok, "specified note must be visible to a listed recipient")
	_, ok = p.PackNoteByID(n.ID, "stranger")
	assert.False(t, ok, "specified note must be dropped from an unlisted recipient")
}

func TestNoteRepoPacker_NilFollowingRepoFailClosed(t *testing.T) {
	idGen, _ := id.NewGenerator("aidx")
	repo := testutil.NewMockNoteRepository()
	n := seedNote(repo, idGen, "author", model.NoteVisibilityFollowers, nil)

	p := webpush.NewNoteRepoPacker(repo, idGen, nil) // followingRepo unwired
	_, ok := p.PackNoteByID(n.ID, "viewer")
	assert.False(t, ok, "nil followingRepo must fail closed for followers notes")
}

func TestNoteRepoPacker_BlankViewerFailClosed(t *testing.T) {
	idGen, _ := id.NewGenerator("aidx")
	repo := testutil.NewMockNoteRepository()
	follow := testutil.NewMockFollowingRepository()
	n := seedNote(repo, idGen, "author", model.NoteVisibilityFollowers, nil)

	p := webpush.NewNoteRepoPacker(repo, idGen, follow)
	_, ok := p.PackNoteByID(n.ID, "") // blank notifiee
	assert.False(t, ok, "blank viewerID must fail closed for followers notes")
}
