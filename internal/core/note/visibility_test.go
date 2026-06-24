package note_test

import (
	"errors"
	"testing"

	"github.com/lib/pq"
	"github.com/shiroha-a/mk/internal/core/note"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
)

func TestCanSeeNote_Public(t *testing.T) {
	n := &model.Note{UserID: "author", Visibility: model.NoteVisibilityPublic}
	assert.True(t, note.CanSeeNote(nil, n, nil))
	assert.True(t, note.CanSeeNote(&model.User{ID: "u"}, n, nil))
}

func TestCanSeeNote_Home(t *testing.T) {
	n := &model.Note{UserID: "author", Visibility: model.NoteVisibilityHome}
	assert.True(t, note.CanSeeNote(nil, n, nil))
}

func TestCanSeeNote_Nil(t *testing.T) {
	assert.False(t, note.CanSeeNote(nil, nil, nil))
}

func TestCanSeeNote_FollowersUnauthenticated(t *testing.T) {
	n := &model.Note{UserID: "author", Visibility: model.NoteVisibilityFollowers}
	assert.False(t, note.CanSeeNote(nil, n, nil))
}

func TestCanSeeNote_FollowersAuthor(t *testing.T) {
	n := &model.Note{UserID: "author", Visibility: model.NoteVisibilityFollowers}
	assert.True(t, note.CanSeeNote(&model.User{ID: "author"}, n, nil))
}

func TestCanSeeNote_FollowersWithoutChecker(t *testing.T) {
	n := &model.Note{UserID: "author", Visibility: model.NoteVisibilityFollowers}
	assert.False(t, note.CanSeeNote(&model.User{ID: "viewer"}, n, nil))
}

func TestCanSeeNote_FollowersFollowing(t *testing.T) {
	repo := testutil.NewMockFollowingRepository()
	repo.Followings["f1"] = &model.Following{ID: "f1", FollowerID: "viewer", FolloweeID: "author"}
	n := &model.Note{UserID: "author", Visibility: model.NoteVisibilityFollowers}
	assert.True(t, note.CanSeeNote(&model.User{ID: "viewer"}, n, repo))
}

func TestCanSeeNote_FollowersNotFollowing(t *testing.T) {
	repo := testutil.NewMockFollowingRepository()
	n := &model.Note{UserID: "author", Visibility: model.NoteVisibilityFollowers}
	assert.False(t, note.CanSeeNote(&model.User{ID: "viewer"}, n, repo))
}

// erroringFollowingRepo simulates a DB error during the follow check.
type erroringFollowingRepo struct{ repository.FollowingRepository }

func (erroringFollowingRepo) Exists(_, _ string) (bool, error) {
	return false, errors.New("db down")
}

func TestCanSeeNote_FollowersErrorTreatedAsHidden(t *testing.T) {
	n := &model.Note{UserID: "author", Visibility: model.NoteVisibilityFollowers}
	assert.False(t, note.CanSeeNote(&model.User{ID: "viewer"}, n, erroringFollowingRepo{}))
}

func TestCanSeeNote_SpecifiedUnauthenticated(t *testing.T) {
	n := &model.Note{UserID: "author", Visibility: model.NoteVisibilitySpecified}
	assert.False(t, note.CanSeeNote(nil, n, nil))
}

func TestCanSeeNote_SpecifiedAuthor(t *testing.T) {
	n := &model.Note{UserID: "author", Visibility: model.NoteVisibilitySpecified}
	assert.True(t, note.CanSeeNote(&model.User{ID: "author"}, n, nil))
}

func TestCanSeeNote_SpecifiedAllowed(t *testing.T) {
	n := &model.Note{
		UserID:         "author",
		Visibility:     model.NoteVisibilitySpecified,
		VisibleUserIDs: pq.StringArray{"viewer"},
	}
	assert.True(t, note.CanSeeNote(&model.User{ID: "viewer"}, n, nil))
}

func TestCanSeeNote_SpecifiedDenied(t *testing.T) {
	n := &model.Note{
		UserID:         "author",
		Visibility:     model.NoteVisibilitySpecified,
		VisibleUserIDs: pq.StringArray{"someoneElse"},
	}
	assert.False(t, note.CanSeeNote(&model.User{ID: "viewer"}, n, nil))
}

func TestCanSeeNote_UnknownVisibility(t *testing.T) {
	n := &model.Note{UserID: "author", Visibility: model.NoteVisibility("unknown")}
	assert.False(t, note.CanSeeNote(&model.User{ID: "viewer"}, n, nil))
}

// #2106 N27: followers note で viewer が mention されていれば read 可 (upstream)。
func TestCanSeeNote_FollowersMentioned(t *testing.T) {
	repo := testutil.NewMockFollowingRepository() // 非フォロワー
	n := &model.Note{UserID: "author", Visibility: model.NoteVisibilityFollowers, Mentions: pq.StringArray{"viewer"}}
	assert.True(t, note.CanSeeNote(&model.User{ID: "viewer"}, n, repo))
}

// #2106 N27: followers note で reply 先が viewer なら read 可 (upstream followers 分岐の replyUserId)。
func TestCanSeeNote_FollowersReplyToViewer(t *testing.T) {
	repo := testutil.NewMockFollowingRepository() // 非フォロワー
	rid := "viewer"
	n := &model.Note{UserID: "author", Visibility: model.NoteVisibilityFollowers, ReplyUserID: &rid}
	assert.True(t, note.CanSeeNote(&model.User{ID: "viewer"}, n, repo))
}

// #2106 N27: specified note で mention されていれば visibleUserIds 外でも read 可 (cross-visibility)。
func TestCanSeeNote_SpecifiedMentioned(t *testing.T) {
	n := &model.Note{UserID: "author", Visibility: model.NoteVisibilitySpecified, VisibleUserIDs: pq.StringArray{"other"}, Mentions: pq.StringArray{"viewer"}}
	assert.True(t, note.CanSeeNote(&model.User{ID: "viewer"}, n, nil))
}

// #2106 N27 regression guard: followers note で非フォロワー・非mention・非reply は依然 不可視。
func TestCanSeeNote_FollowersStrangerStillHidden(t *testing.T) {
	repo := testutil.NewMockFollowingRepository()
	n := &model.Note{UserID: "author", Visibility: model.NoteVisibilityFollowers, Mentions: pq.StringArray{"someone-else"}}
	assert.False(t, note.CanSeeNote(&model.User{ID: "stranger"}, n, repo))
}
