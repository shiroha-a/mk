package note

import (
	"testing"

	"github.com/shiroha-a/mk/internal/model"
)

func ptrInt(v int) *int { return &v }

// fixed clock for deterministic time-gate tests.
const hideTestNowMs int64 = 1_700_000_000_000 // 2023-11-14T22:13:20Z

func followsSet(ids ...string) func(string) bool {
	m := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		m[id] = struct{}{}
	}
	return func(id string) bool {
		_, ok := m[id]
		return ok
	}
}

func TestHideEmbedDecision(t *testing.T) {
	viewer := &model.User{ID: "viewer"}

	tests := []struct {
		name    string
		viewer  *model.User
		facts   EmbedFacts
		follows func(string) bool
		want    bool
	}{
		{
			name:   "public is visible to anyone",
			viewer: viewer,
			facts:  EmbedFacts{AuthorID: "author", Visibility: "public", AuthorPrefsKnown: true},
			want:   false,
		},
		{
			name:   "home is visible to anyone",
			viewer: nil,
			facts:  EmbedFacts{AuthorID: "author", Visibility: "home", AuthorPrefsKnown: true},
			want:   false,
		},
		{
			name:   "own note is always visible even if followers",
			viewer: viewer,
			facts:  EmbedFacts{AuthorID: "viewer", Visibility: "followers", AuthorPrefsKnown: true},
			want:   false,
		},
		{
			name:    "followers hidden from non-follower",
			viewer:  viewer,
			facts:   EmbedFacts{AuthorID: "author", Visibility: "followers", AuthorPrefsKnown: true},
			follows: followsSet(),
			want:    true,
		},
		{
			name:    "followers visible to follower",
			viewer:  viewer,
			facts:   EmbedFacts{AuthorID: "author", Visibility: "followers", AuthorPrefsKnown: true},
			follows: followsSet("author"),
			want:    false,
		},
		{
			name:   "followers visible when viewer is mentioned",
			viewer: viewer,
			facts:  EmbedFacts{AuthorID: "author", Visibility: "followers", Mentions: []string{"viewer"}, AuthorPrefsKnown: true},
			want:   false,
		},
		{
			name:   "followers visible when viewer is reply-target author",
			viewer: viewer,
			facts:  EmbedFacts{AuthorID: "author", Visibility: "followers", ReplyTargetAuthorID: "viewer", AuthorPrefsKnown: true},
			want:   false,
		},
		{
			name:   "followers hidden from anonymous",
			viewer: nil,
			facts:  EmbedFacts{AuthorID: "author", Visibility: "followers", AuthorPrefsKnown: true},
			want:   true,
		},
		{
			name:   "specified visible to listed user",
			viewer: viewer,
			facts:  EmbedFacts{AuthorID: "author", Visibility: "specified", VisibleUserIDs: []string{"viewer", "x"}, AuthorPrefsKnown: true},
			want:   false,
		},
		{
			name:   "specified hidden from unlisted user",
			viewer: viewer,
			facts:  EmbedFacts{AuthorID: "author", Visibility: "specified", VisibleUserIDs: []string{"x"}, AuthorPrefsKnown: true},
			want:   true,
		},
		{
			name:   "specified hidden from anonymous",
			viewer: nil,
			facts:  EmbedFacts{AuthorID: "author", Visibility: "specified", VisibleUserIDs: []string{"x"}, AuthorPrefsKnown: true},
			want:   true,
		},
		{
			// treatVisibility: public note past author makeNotesFollowersOnlyBefore window
			// (negative = older than 3600s) is downgraded to followers BEFORE follower check.
			name:    "public past followersOnlyBefore window hidden from non-follower",
			viewer:  viewer,
			facts:   EmbedFacts{AuthorID: "author", Visibility: "public", MakeNotesFollowersOnlyBefore: ptrInt(-3600), CreatedAtMs: hideTestNowMs - 2*3600*1000, AuthorPrefsKnown: true},
			follows: followsSet(),
			want:    true,
		},
		{
			name:    "public past followersOnlyBefore window visible to follower",
			viewer:  viewer,
			facts:   EmbedFacts{AuthorID: "author", Visibility: "public", MakeNotesFollowersOnlyBefore: ptrInt(-3600), CreatedAtMs: hideTestNowMs - 2*3600*1000, AuthorPrefsKnown: true},
			follows: followsSet("author"),
			want:    false,
		},
		{
			name:    "public within followersOnlyBefore window stays public",
			viewer:  viewer,
			facts:   EmbedFacts{AuthorID: "author", Visibility: "public", MakeNotesFollowersOnlyBefore: ptrInt(-3600), CreatedAtMs: hideTestNowMs - 1000, AuthorPrefsKnown: true},
			follows: followsSet(),
			want:    false,
		},
		{
			name:   "requireSignin hides anonymous on otherwise-public embed",
			viewer: nil,
			facts:  EmbedFacts{AuthorID: "author", Visibility: "public", RequireSigninToViewContents: true, AuthorPrefsKnown: true},
			want:   true,
		},
		{
			name:   "requireSignin does not hide authenticated viewer on public embed",
			viewer: viewer,
			facts:  EmbedFacts{AuthorID: "author", Visibility: "public", RequireSigninToViewContents: true, AuthorPrefsKnown: true},
			want:   false,
		},
		{
			name:   "makeNotesHiddenBefore negative-relative hides old note",
			viewer: viewer,
			facts:  EmbedFacts{AuthorID: "author", Visibility: "public", MakeNotesHiddenBefore: ptrInt(-86400), CreatedAtMs: hideTestNowMs - 2*86400*1000, AuthorPrefsKnown: true},
			want:   true,
		},
		{
			name:   "makeNotesHiddenBefore negative-relative keeps recent note",
			viewer: viewer,
			facts:  EmbedFacts{AuthorID: "author", Visibility: "public", MakeNotesHiddenBefore: ptrInt(-86400), CreatedAtMs: hideTestNowMs - 1000, AuthorPrefsKnown: true},
			want:   false,
		},
		{
			name:   "makeNotesHiddenBefore positive-absolute hides note created at/before threshold",
			viewer: viewer,
			facts:  EmbedFacts{AuthorID: "author", Visibility: "public", MakeNotesHiddenBefore: ptrInt(1_600_000_000), CreatedAtMs: 1_500_000_000_000, AuthorPrefsKnown: true},
			want:   true,
		},
		{
			name:   "makeNotesHiddenBefore positive-absolute keeps note created after threshold",
			viewer: viewer,
			facts:  EmbedFacts{AuthorID: "author", Visibility: "public", MakeNotesHiddenBefore: ptrInt(1_600_000_000), CreatedAtMs: 1_700_000_000_000, AuthorPrefsKnown: true},
			want:   false,
		},
		{
			// AuthorPrefsKnown=false: author-pref gates skipped, but note-intrinsic
			// followers gate still applies.
			name:    "prefs unknown still hides followers from non-follower",
			viewer:  viewer,
			facts:   EmbedFacts{AuthorID: "author", Visibility: "followers", AuthorPrefsKnown: false},
			follows: followsSet(),
			want:    true,
		},
		{
			// prefs unknown: a public embed whose author set followersOnlyBefore is NOT
			// downgraded (gate skipped) -> stays visible. Bounded residual, documented.
			name:    "prefs unknown skips followersOnlyBefore downgrade on public",
			viewer:  viewer,
			facts:   EmbedFacts{AuthorID: "author", Visibility: "public", MakeNotesFollowersOnlyBefore: ptrInt(-3600), CreatedAtMs: hideTestNowMs - 2*3600*1000, AuthorPrefsKnown: false},
			follows: followsSet(),
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := HideEmbedDecision(tt.viewer, tt.facts, tt.follows, hideTestNowMs)
			if got != tt.want {
				t.Fatalf("HideEmbedDecision = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestShouldHideNoteByTime(t *testing.T) {
	const now int64 = 1_700_000_000_000
	tests := []struct {
		name         string
		hiddenBefore *int
		createdAtMs  int64
		want         bool
	}{
		{"nil disables gate", nil, now, false},
		{"negative hides older-than-window", ptrInt(-3600), now - 2*3600*1000, true},
		{"negative keeps within-window", ptrInt(-3600), now - 1000, false},
		{"zero hides everything (<=0 relative, 0 elapsed ok)", ptrInt(0), now, true},
		{"positive absolute hides at/before threshold", ptrInt(1_600_000_000), 1_600_000_000 * 1000, true},
		{"positive absolute keeps after threshold", ptrInt(1_600_000_000), 1_600_000_001 * 1000, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldHideNoteByTime(tt.hiddenBefore, tt.createdAtMs, now); got != tt.want {
				t.Fatalf("shouldHideNoteByTime = %v, want %v", got, tt.want)
			}
		})
	}
}
