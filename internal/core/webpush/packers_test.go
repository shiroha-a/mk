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

// --- #1575: recipient-visibility gate on the depth-2 renote/reply embed ---

// seedNoteWithUser is seedNote but also attaches a model.User so PackNote emits a
// UserLite (AuthorPrefsKnown=true) for the embed author.
func seedNoteWithUser(repo *testutil.MockNoteRepository, idGen id.Generator, author string, vis model.NoteVisibility, visibleUserIDs []string) *model.Note {
	n := seedNote(repo, idGen, author, vis, visibleUserIDs)
	n.User = &model.User{ID: author, Username: author, UsernameLower: author}
	return n
}

// seedRenoteOf returns a public top-level note (author "host", visible to anyone)
// whose renote target is `target`, both registered in repo.
func seedRenoteOf(repo *testutil.MockNoteRepository, idGen id.Generator, target *model.Note) *model.Note {
	top := &model.Note{
		ID:         idGen.Generate(time.Now()),
		UserID:     "host",
		User:       &model.User{ID: "host", Username: "host", UsernameLower: "host"},
		Visibility: model.NoteVisibilityPublic,
		RenoteID:   &target.ID,
		Renote:     target,
	}
	repo.Notes[top.ID] = top
	return top
}

// seedReplyOf is seedRenoteOf for the reply embed path.
func seedReplyOf(repo *testutil.MockNoteRepository, idGen id.Generator, target *model.Note) *model.Note {
	top := &model.Note{
		ID:         idGen.Generate(time.Now()),
		UserID:     "host",
		User:       &model.User{ID: "host", Username: "host", UsernameLower: "host"},
		Visibility: model.NoteVisibilityPublic,
		ReplyID:    &target.ID,
		Reply:      target,
	}
	repo.Notes[top.ID] = top
	return top
}

func embedOf(out map[string]any, key string) map[string]any {
	e, _ := out[key].(map[string]any)
	return e
}

func TestNoteRepoPacker_EmbedRenoteHiddenFromNonFollower(t *testing.T) {
	idGen, _ := id.NewGenerator("aidx")
	repo := testutil.NewMockNoteRepository()
	follow := testutil.NewMockFollowingRepository()
	target := seedNoteWithUser(repo, idGen, "author", model.NoteVisibilityFollowers, nil)
	top := seedRenoteOf(repo, idGen, target)

	p := webpush.NewNoteRepoPacker(repo, idGen, follow)
	out, ok := p.PackNoteByID(top.ID, "stranger") // can see public top, not author, not follower
	assert.True(t, ok, "public top-level note must be delivered")
	embed := embedOf(out, "renote")
	assert.Equal(t, true, embed["isHidden"], "followers renote embed must be blanked for a non-follower recipient")
	assert.Nil(t, embed["text"], "blanked embed must drop text")
}

func TestNoteRepoPacker_EmbedRenoteVisibleToFollower(t *testing.T) {
	idGen, _ := id.NewGenerator("aidx")
	repo := testutil.NewMockNoteRepository()
	follow := testutil.NewMockFollowingRepository()
	target := seedNoteWithUser(repo, idGen, "author", model.NoteVisibilityFollowers, nil)
	top := seedRenoteOf(repo, idGen, target)
	seedFollow(follow, "viewer", "author")

	p := webpush.NewNoteRepoPacker(repo, idGen, follow)
	out, ok := p.PackNoteByID(top.ID, "viewer")
	assert.True(t, ok)
	embed := embedOf(out, "renote")
	assert.NotEqual(t, true, embed["isHidden"], "followers renote embed must stay visible to a follower recipient")
	assert.Equal(t, "secret", embed["text"])
}

func TestNoteRepoPacker_EmbedRenoteVisibleToEmbedAuthor(t *testing.T) {
	idGen, _ := id.NewGenerator("aidx")
	repo := testutil.NewMockNoteRepository()
	follow := testutil.NewMockFollowingRepository()
	target := seedNoteWithUser(repo, idGen, "author", model.NoteVisibilityFollowers, nil)
	top := seedRenoteOf(repo, idGen, target)

	p := webpush.NewNoteRepoPacker(repo, idGen, follow)
	// recipient == embed author: own-note short-circuit keeps it visible.
	out, ok := p.PackNoteByID(top.ID, "author")
	assert.True(t, ok)
	embed := embedOf(out, "renote")
	assert.NotEqual(t, true, embed["isHidden"], "embed author recipient must see their own embedded note")
}

func TestNoteRepoPacker_EmbedSpecifiedMembership(t *testing.T) {
	idGen, _ := id.NewGenerator("aidx")
	repo := testutil.NewMockNoteRepository()
	target := seedNoteWithUser(repo, idGen, "author", model.NoteVisibilitySpecified, []string{"viewer"})
	top := seedRenoteOf(repo, idGen, target)

	// nil followingRepo: specified needs no follow query.
	p := webpush.NewNoteRepoPacker(repo, idGen, nil)
	out, ok := p.PackNoteByID(top.ID, "viewer")
	assert.True(t, ok)
	assert.NotEqual(t, true, embedOf(out, "renote")["isHidden"], "specified embed visible to a listed recipient")

	out, ok = p.PackNoteByID(top.ID, "stranger")
	assert.True(t, ok)
	assert.Equal(t, true, embedOf(out, "renote")["isHidden"], "specified embed blanked for an unlisted recipient")
}

func TestNoteRepoPacker_EmbedReplyHidden(t *testing.T) {
	idGen, _ := id.NewGenerator("aidx")
	repo := testutil.NewMockNoteRepository()
	follow := testutil.NewMockFollowingRepository()
	target := seedNoteWithUser(repo, idGen, "author", model.NoteVisibilityFollowers, nil)
	top := seedReplyOf(repo, idGen, target)

	p := webpush.NewNoteRepoPacker(repo, idGen, follow)
	out, ok := p.PackNoteByID(top.ID, "stranger")
	assert.True(t, ok)
	assert.Equal(t, true, embedOf(out, "reply")["isHidden"], "followers reply embed must be blanked for a non-follower recipient")
}

func TestNoteRepoPacker_EmbedNilFollowingRepoFailClosed(t *testing.T) {
	idGen, _ := id.NewGenerator("aidx")
	repo := testutil.NewMockNoteRepository()
	target := seedNoteWithUser(repo, idGen, "author", model.NoteVisibilityFollowers, nil)
	top := seedRenoteOf(repo, idGen, target)

	p := webpush.NewNoteRepoPacker(repo, idGen, nil) // followingRepo unwired
	out, ok := p.PackNoteByID(top.ID, "viewer")
	assert.True(t, ok, "public top-level note still delivered")
	assert.Equal(t, true, embedOf(out, "renote")["isHidden"], "nil followingRepo must fail closed for the followers embed")
}

func TestNoteRepoPacker_EmbedBlankViewerFailClosed(t *testing.T) {
	idGen, _ := id.NewGenerator("aidx")
	repo := testutil.NewMockNoteRepository()
	follow := testutil.NewMockFollowingRepository()
	target := seedNoteWithUser(repo, idGen, "author", model.NoteVisibilityFollowers, nil)
	top := seedRenoteOf(repo, idGen, target)

	p := webpush.NewNoteRepoPacker(repo, idGen, follow)
	// blank notifiee: CanSeeNote still passes (public top), but the embed
	// gate has no viewer so the followers embed is blanked.
	out, ok := p.PackNoteByID(top.ID, "")
	assert.True(t, ok)
	assert.Equal(t, true, embedOf(out, "renote")["isHidden"], "blank viewerID must fail closed for the followers embed")
}

func TestNoteRepoPacker_EmbedPublicNotHidden(t *testing.T) {
	idGen, _ := id.NewGenerator("aidx")
	repo := testutil.NewMockNoteRepository()
	follow := testutil.NewMockFollowingRepository()
	target := seedNoteWithUser(repo, idGen, "author", model.NoteVisibilityPublic, nil)
	top := seedRenoteOf(repo, idGen, target)

	p := webpush.NewNoteRepoPacker(repo, idGen, follow)
	out, ok := p.PackNoteByID(top.ID, "stranger")
	assert.True(t, ok)
	embed := embedOf(out, "renote")
	assert.NotEqual(t, true, embed["isHidden"], "public embed must never be blanked")
	assert.Equal(t, "secret", embed["text"])
}

// depth-2 引用先 (renote.renote): public renote(boost) → public quote → followers
// 引用先。引用先が非フォロワー受信者の push payload で blank されること。packer が
// depth-2 を emit するようになったため、depth-1 と同じ embed gate を depth-2 にも
// 適用する必要がある (これが無いと followers 引用先が leak する IDOR)。
func TestNoteRepoPacker_EmbedRenoteRenoteHiddenFromNonFollower(t *testing.T) {
	idGen, _ := id.NewGenerator("aidx")
	repo := testutil.NewMockNoteRepository()
	follow := testutil.NewMockFollowingRepository()
	target := seedNoteWithUser(repo, idGen, "author", model.NoteVisibilityFollowers, nil) // depth-2 引用先
	quote := seedRenoteOf(repo, idGen, target)                                            // depth-1 public quote (quote.Renote=target)
	top := seedRenoteOf(repo, idGen, quote)                                               // depth-0 public boost (top.Renote=quote)

	p := webpush.NewNoteRepoPacker(repo, idGen, follow)
	out, ok := p.PackNoteByID(top.ID, "stranger") // public top, not author, not follower
	assert.True(t, ok, "public top-level note must be delivered")
	renote := embedOf(out, "renote")
	assert.NotEqual(t, true, renote["isHidden"], "depth-1 public quote stays visible")
	rr := embedOf(renote, "renote")
	assert.Equal(t, true, rr["isHidden"], "depth-2 followers quote target must be blanked for a non-follower recipient")
	assert.Nil(t, rr["text"], "blanked depth-2 embed must drop text")
}

// depth-2 引用先がフォロワー受信者には見える (gate が過剰 blank しない回帰)。
func TestNoteRepoPacker_EmbedRenoteRenoteVisibleToFollower(t *testing.T) {
	idGen, _ := id.NewGenerator("aidx")
	repo := testutil.NewMockNoteRepository()
	follow := testutil.NewMockFollowingRepository()
	target := seedNoteWithUser(repo, idGen, "author", model.NoteVisibilityFollowers, nil)
	quote := seedRenoteOf(repo, idGen, target)
	top := seedRenoteOf(repo, idGen, quote)
	seedFollow(follow, "viewer", "author")

	p := webpush.NewNoteRepoPacker(repo, idGen, follow)
	out, ok := p.PackNoteByID(top.ID, "viewer")
	assert.True(t, ok)
	rr := embedOf(embedOf(out, "renote"), "renote")
	assert.NotEqual(t, true, rr["isHidden"], "depth-2 quote target visible to a follower recipient")
	assert.Equal(t, "secret", rr["text"])
}

func TestNoteRepoPacker_EmbedFollowersOnlyBeforeDowngrade(t *testing.T) {
	idGen, _ := id.NewGenerator("aidx")
	repo := testutil.NewMockNoteRepository()
	follow := testutil.NewMockFollowingRepository()
	// public embed whose author opted into makeNotesFollowersOnlyBefore with a
	// relative window of 1s; an old (epoch) createdAt is past the window so it
	// downgrades to followers and gets blanked for a non-follower recipient.
	target := seedNoteWithUser(repo, idGen, "author", model.NoteVisibilityPublic, nil)
	window := 0 // <=0 means relative "seconds since creation"; 0 => always past.
	target.User.MakeNotesFollowersOnlyBefore = &window
	top := seedRenoteOf(repo, idGen, target)

	p := webpush.NewNoteRepoPacker(repo, idGen, follow)
	out, ok := p.PackNoteByID(top.ID, "stranger")
	assert.True(t, ok)
	assert.Equal(t, true, embedOf(out, "renote")["isHidden"], "downgraded public embed must be blanked for a non-follower")

	// the same downgraded embed stays visible to a follower of the author.
	seedFollow(follow, "viewer", "author")
	out, ok = p.PackNoteByID(top.ID, "viewer")
	assert.True(t, ok)
	assert.NotEqual(t, true, embedOf(out, "renote")["isHidden"], "downgraded embed must stay visible to a follower")
}

// errFollowingRepo wraps the mock and forces FilterFollowingsFromAnchor to fail,
// exercising the buildFollowSet query-error fail-closed branch.
type errFollowingRepo struct {
	*testutil.MockFollowingRepository
}

func (e *errFollowingRepo) FilterFollowingsFromAnchor(anchorID string, candidateIDs []string) ([]string, error) {
	return nil, assert.AnError
}

func TestNoteRepoPacker_EmbedFollowQueryErrorFailClosed(t *testing.T) {
	idGen, _ := id.NewGenerator("aidx")
	repo := testutil.NewMockNoteRepository()
	follow := &errFollowingRepo{testutil.NewMockFollowingRepository()}
	target := seedNoteWithUser(repo, idGen, "author", model.NoteVisibilityFollowers, nil)
	top := seedRenoteOf(repo, idGen, target)

	p := webpush.NewNoteRepoPacker(repo, idGen, follow)
	out, ok := p.PackNoteByID(top.ID, "viewer")
	assert.True(t, ok, "public top-level note still delivered")
	assert.Equal(t, true, embedOf(out, "renote")["isHidden"], "follow query error must fail closed for the followers embed")
}

func TestNoteRepoPacker_EmbedRequireSigninHiddenFromAnon(t *testing.T) {
	idGen, _ := id.NewGenerator("aidx")
	repo := testutil.NewMockNoteRepository()
	follow := testutil.NewMockFollowingRepository()
	// public embed whose author requires signin to view contents: a blank
	// (anonymous) recipient must have it blanked.
	target := seedNoteWithUser(repo, idGen, "author", model.NoteVisibilityPublic, nil)
	target.User.RequireSigninToViewContents = true
	top := seedRenoteOf(repo, idGen, target)

	p := webpush.NewNoteRepoPacker(repo, idGen, follow)
	out, ok := p.PackNoteByID(top.ID, "") // anonymous notifiee
	assert.True(t, ok)
	assert.Equal(t, true, embedOf(out, "renote")["isHidden"], "requireSigninToViewContents embed must be blanked for an anonymous recipient")
}

func TestNoteRepoPacker_NoEmbedNoQuery(t *testing.T) {
	idGen, _ := id.NewGenerator("aidx")
	repo := testutil.NewMockNoteRepository()
	follow := testutil.NewMockFollowingRepository()
	n := &model.Note{
		ID:         idGen.Generate(time.Now()),
		UserID:     "u1",
		User:       &model.User{ID: "u1", Username: "u1", UsernameLower: "u1"},
		Visibility: model.NoteVisibilityPublic,
	}
	repo.Notes[n.ID] = n

	p := webpush.NewNoteRepoPacker(repo, idGen, follow)
	out, ok := p.PackNoteByID(n.ID, "viewer")
	assert.True(t, ok)
	_, hasRenote := out["renote"]
	assert.False(t, hasRenote, "note without an embed must not gain a renote key")
}
