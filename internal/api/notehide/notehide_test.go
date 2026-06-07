package notehide

import (
	"testing"

	"github.com/shiroha-a/mk/internal/entity"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/testutil"
)

const heNowMs int64 = 1_700_000_000_000

func heStr(s string) *string { return &s }

type countingFollowRepo struct {
	*testutil.MockFollowingRepository
	calls int
}

func (c *countingFollowRepo) FilterFollowingsFromAnchor(anchorID string, candidateIDs []string) ([]string, error) {
	c.calls++
	return c.MockFollowingRepository.FilterFollowingsFromAnchor(anchorID, candidateIDs)
}

func followsRepo(pairs ...[2]string) *countingFollowRepo {
	m := testutil.NewMockFollowingRepository()
	for i, p := range pairs {
		m.Followings[string(rune('a'+i))] = &model.Following{FollowerID: p[0], FolloweeID: p[1]}
	}
	return &countingFollowRepo{MockFollowingRepository: m}
}

func followersEmbed(id, author string) *entity.NoteEntity {
	return &entity.NoteEntity{
		ID: id, UserID: author, CreatedAt: "2026-01-02T03:04:05.000Z",
		User: entity.UserLite{ID: author}, Visibility: "followers",
		Text: heStr("secret " + id), FileIDs: []string{}, Files: []any{},
		VisibleUserIDs: []string{}, Mentions: []string{},
	}
}

func noteWithRenote(parentID string, embed *entity.NoteEntity) entity.NoteEntity {
	return entity.NoteEntity{ID: parentID, UserID: "parent-author", CreatedAt: "2026-01-02T03:04:05.000Z", Renote: embed}
}

func TestHideEmbedsAt_SingleQueryForMixedAuthors(t *testing.T) {
	viewer := &model.User{ID: "viewer"}
	repo := followsRepo([2]string{"viewer", "a1"})
	packed := []entity.NoteEntity{
		noteWithRenote("p1", followersEmbed("e1", "a1")),
		noteWithRenote("p2", followersEmbed("e2", "a2")),
		noteWithRenote("p3", followersEmbed("e3", "a3")),
	}
	hideEmbedsAt(viewer, packed, repo, heNowMs)

	if repo.calls != 1 {
		t.Fatalf("expected exactly 1 batched follow query, got %d", repo.calls)
	}
	if packed[0].Renote.IsHidden || packed[0].Renote.Text == nil {
		t.Error("embed of followed author must stay visible")
	}
	if !packed[1].Renote.IsHidden || packed[1].Renote.Text != nil {
		t.Error("embed of non-followed author must be hidden")
	}
	if !packed[2].Renote.IsHidden {
		t.Error("embed e3 must be hidden")
	}
}

func TestHideEmbedsAt_PublicBatchZeroQuery(t *testing.T) {
	viewer := &model.User{ID: "viewer"}
	repo := followsRepo()
	pub := &entity.NoteEntity{ID: "e", UserID: "a", CreatedAt: "2026-01-02T03:04:05.000Z", User: entity.UserLite{ID: "a"}, Visibility: "public", Text: heStr("hi"), FileIDs: []string{}, Files: []any{}, VisibleUserIDs: []string{}, Mentions: []string{}}
	packed := []entity.NoteEntity{noteWithRenote("p", pub)}
	hideEmbedsAt(viewer, packed, repo, heNowMs)

	if repo.calls != 0 {
		t.Fatalf("public-only batch must issue ZERO follow queries, got %d", repo.calls)
	}
	if packed[0].Renote.IsHidden {
		t.Error("public embed must not be hidden")
	}
}

func TestHideEmbedsAt_AnonymousHidesFollowersKeepsPublic(t *testing.T) {
	repo := followsRepo()
	pub := &entity.NoteEntity{ID: "pub", UserID: "a", CreatedAt: "2026-01-02T03:04:05.000Z", User: entity.UserLite{ID: "a"}, Visibility: "public", Text: heStr("hi"), FileIDs: []string{}, Files: []any{}, VisibleUserIDs: []string{}, Mentions: []string{}}
	packed := []entity.NoteEntity{
		noteWithRenote("p1", followersEmbed("e1", "a1")),
		noteWithRenote("p2", pub),
	}
	hideEmbedsAt(nil, packed, repo, heNowMs)

	if repo.calls != 0 {
		t.Fatalf("anonymous viewer must not query, got %d", repo.calls)
	}
	if !packed[0].Renote.IsHidden {
		t.Error("anonymous must not see followers embed")
	}
	if packed[1].Renote.IsHidden {
		t.Error("anonymous must still see public embed")
	}
}

func TestHideEmbedsAt_OwnEmbedVisibleNoQuery(t *testing.T) {
	viewer := &model.User{ID: "viewer"}
	repo := followsRepo()
	own := followersEmbed("e", "viewer")
	packed := []entity.NoteEntity{noteWithRenote("p", own)}
	hideEmbedsAt(viewer, packed, repo, heNowMs)

	if repo.calls != 0 {
		t.Fatalf("own embed needs no query, got %d", repo.calls)
	}
	if packed[0].Renote.IsHidden {
		t.Error("own followers embed must stay visible")
	}
}

func TestHideEmbedsAt_SpecifiedMembership(t *testing.T) {
	viewer := &model.User{ID: "viewer"}
	repo := followsRepo()
	in := &entity.NoteEntity{ID: "in", UserID: "a", CreatedAt: "2026-01-02T03:04:05.000Z", User: entity.UserLite{ID: "a"}, Visibility: "specified", Text: heStr("dm"), FileIDs: []string{}, Files: []any{}, VisibleUserIDs: []string{"viewer"}, Mentions: []string{}}
	out := &entity.NoteEntity{ID: "out", UserID: "a", CreatedAt: "2026-01-02T03:04:05.000Z", User: entity.UserLite{ID: "a"}, Visibility: "specified", Text: heStr("dm"), FileIDs: []string{}, Files: []any{}, VisibleUserIDs: []string{"someone-else"}, Mentions: []string{}}
	packed := []entity.NoteEntity{noteWithRenote("p1", in), noteWithRenote("p2", out)}
	hideEmbedsAt(viewer, packed, repo, heNowMs)

	if repo.calls != 0 {
		t.Fatalf("specified is decided from visibleUserIds, ZERO query expected, got %d", repo.calls)
	}
	if packed[0].Renote.IsHidden {
		t.Error("specified embed listing viewer must be visible")
	}
	if !packed[1].Renote.IsHidden {
		t.Error("specified embed not listing viewer must be hidden")
	}
}

func TestParseCreatedAtMs(t *testing.T) {
	if got := parseCreatedAtMs("", 42); got != 42 {
		t.Errorf("empty -> fallback, got %d", got)
	}
	if got := parseCreatedAtMs("not-a-time", 42); got != 42 {
		t.Errorf("invalid -> fallback, got %d", got)
	}
	if got := parseCreatedAtMs("2023-11-14T22:13:20.000Z", 0); got != 1_700_000_000_000 {
		t.Errorf("valid RFC3339-ms parse, got %d", got)
	}
}

func TestEmbedFactsFromEntity_AuthorPrefs(t *testing.T) {
	tr := true
	hb := -3600
	fb := -7200
	e := &entity.NoteEntity{
		ID: "e", UserID: "a", CreatedAt: "2023-11-14T22:13:20.000Z",
		Visibility: "public", VisibleUserIDs: []string{"x"}, Mentions: []string{"m"},
		User: entity.UserLite{ID: "a", RequireSigninToViewContents: &tr, MakeNotesHiddenBefore: &hb, MakeNotesFollowersOnlyBefore: &fb},
	}
	f := embedFactsFromEntity(e, 0)
	if !f.AuthorPrefsKnown || !f.RequireSigninToViewContents || f.MakeNotesHiddenBefore == nil || *f.MakeNotesHiddenBefore != -3600 || f.MakeNotesFollowersOnlyBefore == nil {
		t.Errorf("author prefs not populated: %+v", f)
	}
	if f.AuthorID != "a" || f.CreatedAtMs != 1_700_000_000_000 {
		t.Errorf("base facts wrong: %+v", f)
	}
}

func TestEmbedFactsFromEntity_NoAuthorPrefs(t *testing.T) {
	// embed.User.ID == "" => prefs not known.
	e := &entity.NoteEntity{ID: "e", UserID: "a", CreatedAt: "2023-11-14T22:13:20.000Z", Visibility: "followers"}
	f := embedFactsFromEntity(e, 99)
	if f.AuthorPrefsKnown {
		t.Error("AuthorPrefsKnown must be false when embed.User unset")
	}
}

func TestHideEmbedsAt_MakeNotesHiddenBeforeHidesEvenFollower(t *testing.T) {
	viewer := &model.User{ID: "viewer"}
	repo := followsRepo([2]string{"viewer", "author"}) // viewer follows author
	hb := -1                                           // hide notes older than 1s
	old := &entity.NoteEntity{
		ID: "old", UserID: "author", CreatedAt: "2020-01-01T00:00:00.000Z", // far in the past vs heNowMs
		User: entity.UserLite{ID: "author", MakeNotesHiddenBefore: &hb}, Visibility: "public",
		Text: heStr("old"), FileIDs: []string{}, Files: []any{}, VisibleUserIDs: []string{}, Mentions: []string{},
	}
	packed := []entity.NoteEntity{noteWithRenote("p", old)}
	hideEmbedsAt(viewer, packed, repo, heNowMs)
	if !packed[0].Renote.IsHidden {
		t.Error("makeNotesHiddenBefore must hide an old embed even for a follower")
	}
}

func TestHideEmbeds_PackageLevelRepo(t *testing.T) {
	// exercise the exported HideEmbeds + package-level repo wiring.
	prev := followingRepo
	defer func() { followingRepo = prev }()
	SetFollowingRepo(testutil.NewMockFollowingRepository())
	viewer := &model.User{ID: "viewer"}
	packed := []entity.NoteEntity{noteWithRenote("p", followersEmbed("e", "author"))}
	HideEmbeds(viewer, packed)
	if !packed[0].Renote.IsHidden {
		t.Error("followers embed must be hidden via package-level HideEmbeds for a non-follower")
	}
}
