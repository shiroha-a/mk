package repository

import (
	"context"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/lib/pq"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
)

func cleanupNote(t *testing.T, id string) {
	t.Helper()
	testDB.Exec(`DELETE FROM "poll" WHERE "noteId" = ?`, id)
	testDB.Exec(`DELETE FROM "note" WHERE id = ?`, id)
}

// TestNoteRepository_ListHomeTimeline_MutedChannelFilter verifies that
// model.TimelineDBFilter.MutedChannelIDs is applied at the SQL layer without
// short-circuiting other AND conditions. The OR clause
// `channelId IS NULL OR channelId NOT IN (...)` must be wrapped in
// parentheses; otherwise AND conditions (visibility / following) are bypassed.
// Regression guard from Devin #191 review.
func TestNoteRepository_ListHomeTimeline_MutedChannelFilter(t *testing.T) {
	repo := NewNoteRepository(testDB)
	chRepo := NewChannelRepository(testDB)

	viewer := insertTestUser(t, "u_mute_v", "muteviewer")
	defer cleanupUser(t, viewer.ID)
	author := insertTestUser(t, "u_mute_a", "muteauthor")
	defer cleanupUser(t, author.ID)

	// viewer は author をフォロー (home timeline に含める)
	require.NoError(t, testDB.Exec(
		`INSERT INTO "following" (id, "followerId", "followeeId") VALUES (?, ?, ?)`,
		"flw_mute", viewer.ID, author.ID,
	).Error)
	defer testDB.Exec(`DELETE FROM "following" WHERE id = ?`, "flw_mute")

	mutedCh := newTestChannel("ch_mute_m", "muted-ch", nil)
	require.NoError(t, chRepo.Create(mutedCh))
	defer cleanupChannel(t, mutedCh.ID)
	allowedCh := newTestChannel("ch_mute_a", "allowed-ch", nil)
	require.NoError(t, chRepo.Create(allowedCh))
	defer cleanupChannel(t, allowedCh.ID)

	mkNote := func(id, chID string) *model.Note {
		text := "hi"
		n := &model.Note{
			ID: id, UserID: author.ID, Text: &text,
			Visibility: model.NoteVisibilityPublic,
			Reactions:  datatypes.JSON([]byte("{}")),
		}
		if chID != "" {
			n.ChannelID = &chID
		}
		return n
	}
	plain := mkNote("n_mute_1", "")
	inMuted := mkNote("n_mute_2", mutedCh.ID)
	inAllowed := mkNote("n_mute_3", allowedCh.ID)
	require.NoError(t, repo.Create(plain))
	require.NoError(t, repo.Create(inMuted))
	require.NoError(t, repo.Create(inAllowed))
	defer cleanupNote(t, plain.ID)
	defer cleanupNote(t, inMuted.ID)
	defer cleanupNote(t, inAllowed.ID)

	filter := model.TimelineDBFilter{
		ViewerID:        viewer.ID,
		MutedChannelIDs: []string{mutedCh.ID},
	}
	rows, err := repo.ListHomeTimeline(viewer.ID, 50, "", "", filter)
	require.NoError(t, err)

	ids := make(map[string]bool, len(rows))
	for _, n := range rows {
		ids[n.ID] = true
	}
	assert.True(t, ids[plain.ID], "plain note must be included")
	assert.True(t, ids[inAllowed.ID], "note in allowed channel must be included")
	assert.False(t, ids[inMuted.ID], "note in muted channel must be excluded")
	// OR 節のバグがあると、viewer が follow していない他ユーザーの note も
	// 返ってしまう。ここでは他ユーザーがいないので fan-out テストは省略。
}

// TestNoteRepository_ListHomeTimeline_WithRepliesFalse_SelfThreadOnly は #1047
// で導入された upstream 互換の reply filter semantics を SQL 層で直接検証する。
//
// 期待値:
//   - reply 無し note: 通過
//   - self-thread (replyUserId = note.userId): 通過
//   - 他人宛 reply (replyUserId != note.userId): 除外
//
// ViewerID 引数の有無に関わらず同 semantics となる (旧実装の
// `replyUserId = viewerID` 分岐は撤廃済)。
func TestNoteRepository_ListHomeTimeline_WithRepliesFalse_SelfThreadOnly(t *testing.T) {
	repo := NewNoteRepository(testDB)

	viewer := insertTestUser(t, "u_rep_v", "repviewer")
	defer cleanupUser(t, viewer.ID)
	author := insertTestUser(t, "u_rep_a", "repauthor")
	defer cleanupUser(t, author.ID)
	other := insertTestUser(t, "u_rep_o", "repother")
	defer cleanupUser(t, other.ID)

	// viewer は author をフォロー (home timeline に含める)
	require.NoError(t, testDB.Exec(
		`INSERT INTO "following" (id, "followerId", "followeeId") VALUES (?, ?, ?)`,
		"flw_rep", viewer.ID, author.ID,
	).Error)
	defer testDB.Exec(`DELETE FROM "following" WHERE id = ?`, "flw_rep")

	text := "hi"
	plain := &model.Note{
		ID: "n_rep_plain", UserID: author.ID, Text: &text,
		Visibility: model.NoteVisibilityPublic,
		Reactions:  datatypes.JSON([]byte("{}")),
	}
	// self-thread: author 自身への reply
	selfReplyTargetID := "n_rep_plain"
	selfThread := &model.Note{
		ID: "n_rep_self", UserID: author.ID, Text: &text,
		Visibility:  model.NoteVisibilityPublic,
		Reactions:   datatypes.JSON([]byte("{}")),
		ReplyID:     &selfReplyTargetID,
		ReplyUserID: &author.ID,
	}
	// 他人宛 reply: author → other
	otherReplyTargetID := "n_rep_target_other"
	otherUserID := other.ID
	otherReplyTarget := &model.Note{
		ID: otherReplyTargetID, UserID: other.ID, Text: &text,
		Visibility: model.NoteVisibilityPublic,
		Reactions:  datatypes.JSON([]byte("{}")),
	}
	otherReply := &model.Note{
		ID: "n_rep_other", UserID: author.ID, Text: &text,
		Visibility:  model.NoteVisibilityPublic,
		Reactions:   datatypes.JSON([]byte("{}")),
		ReplyID:     &otherReplyTargetID,
		ReplyUserID: &otherUserID,
	}
	require.NoError(t, repo.Create(plain))
	require.NoError(t, repo.Create(otherReplyTarget))
	require.NoError(t, repo.Create(selfThread))
	require.NoError(t, repo.Create(otherReply))
	defer cleanupNote(t, plain.ID)
	defer cleanupNote(t, otherReplyTarget.ID)
	defer cleanupNote(t, selfThread.ID)
	defer cleanupNote(t, otherReply.ID)

	withReplies := false
	filter := model.TimelineDBFilter{
		ViewerID:    viewer.ID,
		WithReplies: &withReplies,
	}
	rows, err := repo.ListHomeTimeline(viewer.ID, 50, "", "", filter)
	require.NoError(t, err)

	ids := make(map[string]bool, len(rows))
	for _, n := range rows {
		ids[n.ID] = true
	}
	assert.True(t, ids[plain.ID], "plain note (reply 無し) は通過すべき")
	assert.True(t, ids[selfThread.ID], "self-thread (replyUserId = userId) は通過すべき")
	assert.False(t, ids[otherReply.ID], "他人宛 reply (replyUserId != userId) は除外されるべき")
}

func TestNoteRepository_CreateAndFindByID(t *testing.T) {
	repo := NewNoteRepository(testDB)
	user := insertTestUser(t, "u_ncf_1", "noteuser1")
	defer cleanupUser(t, user.ID)

	text := "Hello, world!"
	note := &model.Note{
		ID:         "n_ncf_1",
		UserID:     user.ID,
		Text:       &text,
		Visibility: model.NoteVisibilityPublic,
		Reactions:  datatypes.JSON([]byte("{}")),
	}
	require.NoError(t, repo.Create(note))
	defer cleanupNote(t, note.ID)

	found, err := repo.FindByID(note.ID)
	require.NoError(t, err)
	assert.Equal(t, note.ID, found.ID)
	assert.Equal(t, &text, found.Text)
}

func TestNoteRepository_FindByIDWithUser(t *testing.T) {
	repo := NewNoteRepository(testDB)
	user := insertTestUser(t, "u_nfu_1", "noteuser2")
	defer cleanupUser(t, user.ID)

	note := &model.Note{
		ID:         "n_nfu_1",
		UserID:     user.ID,
		Visibility: model.NoteVisibilityHome,
		Reactions:  datatypes.JSON([]byte("{}")),
	}
	require.NoError(t, repo.Create(note))
	defer cleanupNote(t, note.ID)

	found, err := repo.FindByIDWithUser(note.ID)
	require.NoError(t, err)
	assert.NotNil(t, found.User)
	assert.Equal(t, user.ID, found.User.ID)
	assert.Equal(t, "noteuser2", found.User.Username)
	// 軽量経路 (#425) では Renote/Reply は preload されない。
	assert.Nil(t, found.Renote)
	assert.Nil(t, found.Reply)
}

// FindByIDWithRelations は User に加えて Renote / Renote.User / Reply /
// Reply.User まで preload する full version (#425)。
func TestNoteRepository_FindByIDWithRelations(t *testing.T) {
	repo := NewNoteRepository(testDB)
	author := insertTestUser(t, "u_nfr_a", "nfra")
	defer cleanupUser(t, author.ID)
	rnAuthor := insertTestUser(t, "u_nfr_r", "nfrr")
	defer cleanupUser(t, rnAuthor.ID)

	renoteID := "n_nfr_rn"
	renoteTarget := &model.Note{
		ID:         renoteID,
		UserID:     rnAuthor.ID,
		Visibility: model.NoteVisibilityPublic,
		Reactions:  datatypes.JSON([]byte("{}")),
	}
	require.NoError(t, repo.Create(renoteTarget))
	defer cleanupNote(t, renoteTarget.ID)

	parent := &model.Note{
		ID:         "n_nfr_main",
		UserID:     author.ID,
		RenoteID:   &renoteID,
		Visibility: model.NoteVisibilityPublic,
		Reactions:  datatypes.JSON([]byte("{}")),
	}
	require.NoError(t, repo.Create(parent))
	defer cleanupNote(t, parent.ID)

	found, err := repo.FindByIDWithRelations(parent.ID)
	require.NoError(t, err)
	require.NotNil(t, found.User)
	assert.Equal(t, author.ID, found.User.ID)
	require.NotNil(t, found.Renote, "Renote must be preloaded")
	assert.Equal(t, renoteID, found.Renote.ID)
	require.NotNil(t, found.Renote.User, "Renote.User must be preloaded")
	assert.Equal(t, rnAuthor.ID, found.Renote.User.ID)
}

func TestNoteRepository_FindByIDWithRelations_NotFound(t *testing.T) {
	repo := NewNoteRepository(testDB)
	_, err := repo.FindByIDWithRelations("nonexistent_note_relations")
	assert.Error(t, err)
}

func TestNoteRepository_UpdateFields(t *testing.T) {
	repo := NewNoteRepository(testDB)
	user := insertTestUser(t, "u_nuf_1", "noteuser_uf")
	defer cleanupUser(t, user.ID)

	text := "original"
	note := &model.Note{
		ID:         "n_nuf_1",
		UserID:     user.ID,
		Text:       &text,
		Visibility: model.NoteVisibilityPublic,
		Reactions:  datatypes.JSON([]byte("{}")),
	}
	require.NoError(t, repo.Create(note))
	defer cleanupNote(t, note.ID)

	updated := "edited"
	cw := "spoiler"
	require.NoError(t, repo.UpdateFields(note.ID, map[string]any{
		"text": &updated,
		"cw":   &cw,
	}))

	got, err := repo.FindByID(note.ID)
	require.NoError(t, err)
	require.NotNil(t, got.Text)
	assert.Equal(t, "edited", *got.Text)
	require.NotNil(t, got.CW)
	assert.Equal(t, "spoiler", *got.CW)
}

func TestNoteRepository_UpdateFields_NoOp(t *testing.T) {
	repo := NewNoteRepository(testDB)
	require.NoError(t, repo.UpdateFields("any", nil))
}

func TestNoteRepository_ListByChannelID(t *testing.T) {
	repo := NewNoteRepository(testDB)
	user := insertTestUser(t, "u_nlc_1", "noteuser_lc")
	defer cleanupUser(t, user.ID)

	chRepo := NewChannelRepository(testDB)
	uid := user.ID
	ch := newTestChannel("ch_lc_1", "list-by-channel", &uid)
	require.NoError(t, chRepo.Create(ch))
	defer cleanupChannel(t, ch.ID)

	cid := ch.ID
	for _, id := range []string{"n_lc_1", "n_lc_2", "n_lc_3"} {
		note := &model.Note{
			ID:         id,
			UserID:     user.ID,
			Visibility: model.NoteVisibilityPublic,
			Reactions:  datatypes.JSON([]byte("{}")),
			ChannelID:  &cid,
		}
		require.NoError(t, repo.Create(note))
		defer cleanupNote(t, note.ID)
	}

	rows, err := repo.ListByChannelID(ch.ID, "", "", 10)
	require.NoError(t, err)
	assert.Len(t, rows, 3)

	rows, err = repo.ListByChannelID(ch.ID, "n_lc_3", "", 10)
	require.NoError(t, err)
	assert.Len(t, rows, 2)

	rows, err = repo.ListByChannelID(ch.ID, "", "n_lc_1", 10)
	require.NoError(t, err)
	assert.Len(t, rows, 2)

	rows, err = repo.ListByChannelID(ch.ID, "", "", 0) // 0 → default 30
	require.NoError(t, err)
	assert.Len(t, rows, 3)
}

// TestNoteRepository_ListByChannelIDVisible は channels/timeline の visibility
// push-down (#1440 IDOR) を SQL 段階で検証する。public channel に
// followers / specified visibility の note を混在させ、viewer ごとに見える
// note が変わること、および ListByChannelID (filter なし) は全件返すことを
// 確認する。
func TestNoteRepository_ListByChannelIDVisible(t *testing.T) {
	repo := NewNoteRepository(testDB)
	chRepo := NewChannelRepository(testDB)
	followingRepo := NewFollowingRepository(testDB)

	mkUser := func(id, username string) *model.User {
		u := insertTestUser(t, id, username)
		t.Cleanup(func() { cleanupUser(t, u.ID) })
		return u
	}
	author := mkUser("u_cv_a", "cvauthor")
	follower := mkUser("u_cv_f", "cvfollower")
	allowed := mkUser("u_cv_al", "cvallowed")

	uid := author.ID
	ch := newTestChannel("ch_cv_1", "channel-vis", &uid)
	require.NoError(t, chRepo.Create(ch))
	defer cleanupChannel(t, ch.ID)

	cid := ch.ID
	notes := []*model.Note{
		{ID: "n_cv_pub", UserID: author.ID, ChannelID: &cid, Visibility: model.NoteVisibilityPublic, Reactions: datatypes.JSON([]byte("{}"))},
		{ID: "n_cv_fol", UserID: author.ID, ChannelID: &cid, Visibility: model.NoteVisibilityFollowers, Reactions: datatypes.JSON([]byte("{}"))},
		{ID: "n_cv_spec", UserID: author.ID, ChannelID: &cid, Visibility: model.NoteVisibilitySpecified, VisibleUserIDs: pq.StringArray{allowed.ID}, Reactions: datatypes.JSON([]byte("{}"))},
	}
	for _, n := range notes {
		require.NoError(t, repo.Create(n))
		defer cleanupNote(t, n.ID)
	}

	f := &model.Following{ID: "fl_cv_1", FollowerID: follower.ID, FolloweeID: author.ID}
	require.NoError(t, followingRepo.Create(f))
	defer testDB.Exec(`DELETE FROM "following" WHERE id = ?`, f.ID)

	idsOf := func(rows []*model.Note) []string {
		out := make([]string, 0, len(rows))
		for _, r := range rows {
			out = append(out, r.ID)
		}
		sort.Strings(out)
		return out
	}

	// ListByChannelID (filter なし) は全件返す。
	rows, err := repo.ListByChannelID(ch.ID, "", "", 50)
	require.NoError(t, err)
	assert.Equal(t, []string{"n_cv_fol", "n_cv_pub", "n_cv_spec"}, idsOf(rows))

	// 匿名 viewer: public のみ。followers / specified が漏れない (#1440)。
	rows, err = repo.ListByChannelIDVisible(ch.ID, "", "", "", 50)
	require.NoError(t, err)
	assert.Equal(t, []string{"n_cv_pub"}, idsOf(rows))

	// 非 follower viewer に followers note は見えない。
	rows, err = repo.ListByChannelIDVisible(ch.ID, "u_cv_stranger", "", "", 50)
	require.NoError(t, err)
	assert.Equal(t, []string{"n_cv_pub"}, idsOf(rows))

	// follower viewer は public + followers。
	rows, err = repo.ListByChannelIDVisible(ch.ID, follower.ID, "", "", 50)
	require.NoError(t, err)
	assert.Equal(t, []string{"n_cv_fol", "n_cv_pub"}, idsOf(rows))

	// specified 対象 viewer は public + specified。
	rows, err = repo.ListByChannelIDVisible(ch.ID, allowed.ID, "", "", 50)
	require.NoError(t, err)
	assert.Equal(t, []string{"n_cv_pub", "n_cv_spec"}, idsOf(rows))

	// author 本人は全 visibility を閲覧可。
	rows, err = repo.ListByChannelIDVisible(ch.ID, author.ID, "", "", 50)
	require.NoError(t, err)
	assert.Equal(t, []string{"n_cv_fol", "n_cv_pub", "n_cv_spec"}, idsOf(rows))
}

func TestNoteRepository_FindByURI(t *testing.T) {
	repo := NewNoteRepository(testDB)
	user := insertTestUser(t, "u_nuri_1", "noteuser_uri")
	defer cleanupUser(t, user.ID)

	uri := "https://remote.example/notes/n_nuri_1"
	note := &model.Note{
		ID:         "n_nuri_1",
		UserID:     user.ID,
		Visibility: model.NoteVisibilityPublic,
		Reactions:  datatypes.JSON([]byte("{}")),
		URI:        &uri,
	}
	require.NoError(t, repo.Create(note))
	defer cleanupNote(t, note.ID)

	found, err := repo.FindByURI(uri)
	require.NoError(t, err)
	assert.Equal(t, note.ID, found.ID)

	// 存在しない URI なら error
	_, err = repo.FindByURI("https://remote.example/notes/missing")
	assert.Error(t, err)
}

func TestNoteRepository_Delete(t *testing.T) {
	repo := NewNoteRepository(testDB)
	user := insertTestUser(t, "u_nd_1", "noteuser3")
	defer cleanupUser(t, user.ID)

	note := &model.Note{
		ID:         "n_nd_1",
		UserID:     user.ID,
		Visibility: model.NoteVisibilityPublic,
		Reactions:  datatypes.JSON([]byte("{}")),
	}
	require.NoError(t, repo.Create(note))

	require.NoError(t, repo.Delete(note))

	_, err := repo.FindByID(note.ID)
	assert.Error(t, err)
}

func TestNoteRepository_Update(t *testing.T) {
	repo := NewNoteRepository(testDB)
	user := insertTestUser(t, "u_nu_1", "noteuser4")
	defer cleanupUser(t, user.ID)

	note := &model.Note{
		ID:         "n_nu_1",
		UserID:     user.ID,
		Visibility: model.NoteVisibilityPublic,
		Reactions:  datatypes.JSON([]byte("{}")),
	}
	require.NoError(t, repo.Create(note))
	defer cleanupNote(t, note.ID)

	require.NoError(t, repo.Update(note, "hasPoll", true))

	found, err := repo.FindByID(note.ID)
	require.NoError(t, err)
	assert.True(t, found.HasPoll)
}

func TestNoteRepository_FindByID_NotFound(t *testing.T) {
	repo := NewNoteRepository(testDB)

	_, err := repo.FindByID("nonexistent_note")
	assert.Error(t, err)
}

func TestNoteRepository_FindByIDWithUser_NotFound(t *testing.T) {
	repo := NewNoteRepository(testDB)

	_, err := repo.FindByIDWithUser("nonexistent_note")
	assert.Error(t, err)
}

func TestNoteRepository_ListByUserID(t *testing.T) {
	repo := NewNoteRepository(testDB)
	user := insertTestUser(t, "u_lst_1", "listuser")
	defer cleanupUser(t, user.ID)

	for _, id := range []string{"n_lst_1", "n_lst_2", "n_lst_3"} {
		note := &model.Note{
			ID:         id,
			UserID:     user.ID,
			Visibility: model.NoteVisibilityPublic,
			Reactions:  datatypes.JSON([]byte("{}")),
		}
		require.NoError(t, repo.Create(note))
		defer cleanupNote(t, id)
	}

	// 全件 (id降順)
	out, err := repo.ListByUserID(user.ID, "", "", 10)
	require.NoError(t, err)
	assert.Len(t, out, 3)
	assert.Equal(t, "n_lst_3", out[0].ID)

	// untilIDで絞り込み
	out, err = repo.ListByUserID(user.ID, "n_lst_3", "", 10)
	require.NoError(t, err)
	assert.Len(t, out, 2)

	// sinceIDで絞り込み
	out, err = repo.ListByUserID(user.ID, "", "n_lst_1", 10)
	require.NoError(t, err)
	assert.Len(t, out, 2)
}

// ListByUserIDFiltered は upstream users/notes 互換の 4 種 filter (#1021)。
func TestNoteRepository_ListByUserIDFiltered(t *testing.T) {
	repo := NewNoteRepository(testDB)
	user := insertTestUser(t, "u_lf_1", "lfuser")
	defer cleanupUser(t, user.ID)

	// 4 種類のノートを seed (file 添付 / reply / pure renote / 通常)。
	text := "plain"
	notes := []*model.Note{
		{ID: "n_lf_plain", UserID: user.ID, Text: &text, Visibility: model.NoteVisibilityPublic, Reactions: datatypes.JSON([]byte("{}")), FileIDs: pq.StringArray{}},
		{ID: "n_lf_file", UserID: user.ID, Text: &text, FileIDs: pq.StringArray{"f1"}, Visibility: model.NoteVisibilityPublic, Reactions: datatypes.JSON([]byte("{}"))},
	}
	for _, n := range notes {
		require.NoError(t, repo.Create(n))
		defer cleanupNote(t, n.ID)
	}

	// withFiles=false の defaults (= withReplies=true / withRenotes=true /
	// withChannelNotes=false)。channel=false の filter は本ケースで影響なし。
	out, err := repo.ListByUserIDFiltered(user.ID, "", "", "", 10, false, true, true, false)
	require.NoError(t, err)
	assert.Len(t, out, 2)

	// withFiles=true で file 添付のみ
	out, err = repo.ListByUserIDFiltered(user.ID, "", "", "", 10, true, true, true, false)
	require.NoError(t, err)
	require.Len(t, out, 1)
	assert.Equal(t, "n_lf_file", out[0].ID)
}

// TestNoteRepository_ListByUserIDFiltered_VisibilityPushDown は users/notes の
// visibility push-down (#1418 review) を検証する。author が public / followers /
// specified の 3 note を持ち、viewer ごとに SQL 段階で見える note が変わること
// を確認する (LIMIT 前に絞られるためページネーションが途切れない)。
func TestNoteRepository_ListByUserIDFiltered_VisibilityPushDown(t *testing.T) {
	repo := NewNoteRepository(testDB)
	followingRepo := NewFollowingRepository(testDB)

	mkUser := func(id, username string) *model.User {
		u := insertTestUser(t, id, username)
		t.Cleanup(func() { cleanupUser(t, u.ID) })
		return u
	}
	author := mkUser("u_lfv_a", "lfvauthor")
	follower := mkUser("u_lfv_f", "lfvfollower")
	allowed := mkUser("u_lfv_al", "lfvallowed")
	stranger := mkUser("u_lfv_s", "lfvstranger")

	notes := []*model.Note{
		{ID: "n_lfv_pub", UserID: author.ID, Visibility: model.NoteVisibilityPublic, Reactions: datatypes.JSON([]byte("{}")), FileIDs: pq.StringArray{}},
		{ID: "n_lfv_fol", UserID: author.ID, Visibility: model.NoteVisibilityFollowers, Reactions: datatypes.JSON([]byte("{}")), FileIDs: pq.StringArray{}},
		{ID: "n_lfv_spec", UserID: author.ID, Visibility: model.NoteVisibilitySpecified, VisibleUserIDs: pq.StringArray{allowed.ID}, Reactions: datatypes.JSON([]byte("{}")), FileIDs: pq.StringArray{}},
	}
	for _, n := range notes {
		require.NoError(t, repo.Create(n))
		defer cleanupNote(t, n.ID)
	}

	f := &model.Following{ID: "fl_lfv_1", FollowerID: follower.ID, FolloweeID: author.ID}
	require.NoError(t, followingRepo.Create(f))
	defer testDB.Exec(`DELETE FROM "following" WHERE id = ?`, f.ID)

	idsOf := func(rows []*model.Note) []string {
		out := make([]string, 0, len(rows))
		for _, n := range rows {
			out = append(out, n.ID)
		}
		sort.Strings(out)
		return out
	}

	// 匿名 viewer は public のみ。
	out, err := repo.ListByUserIDFiltered(author.ID, "", "", "", 50, false, true, true, false)
	require.NoError(t, err)
	assert.Equal(t, []string{"n_lfv_pub"}, idsOf(out))

	// stranger (follow なし / specified 対象外) も public のみ。
	out, err = repo.ListByUserIDFiltered(author.ID, stranger.ID, "", "", 50, false, true, true, false)
	require.NoError(t, err)
	assert.Equal(t, []string{"n_lfv_pub"}, idsOf(out))

	// follower は public + followers。
	out, err = repo.ListByUserIDFiltered(author.ID, follower.ID, "", "", 50, false, true, true, false)
	require.NoError(t, err)
	assert.Equal(t, []string{"n_lfv_fol", "n_lfv_pub"}, idsOf(out))

	// specified の visibleUserIds に含まれる viewer は public + specified。
	out, err = repo.ListByUserIDFiltered(author.ID, allowed.ID, "", "", 50, false, true, true, false)
	require.NoError(t, err)
	assert.Equal(t, []string{"n_lfv_pub", "n_lfv_spec"}, idsOf(out))

	// author 本人は全 visibility を閲覧可。
	out, err = repo.ListByUserIDFiltered(author.ID, author.ID, "", "", 50, false, true, true, false)
	require.NoError(t, err)
	assert.Equal(t, []string{"n_lfv_fol", "n_lfv_pub", "n_lfv_spec"}, idsOf(out))
}

func TestNoteRepository_ListByUserIDFiltered_QueryError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	db := testDB.WithContext(ctx)
	repo := NewNoteRepository(db)
	_, err := repo.ListByUserIDFiltered("a", "", "", "", 10, true, true, true, false)
	assert.Error(t, err)
}

func TestNoteRepository_FindManyByIDsWithUser(t *testing.T) {
	repo := NewNoteRepository(testDB)
	user := insertTestUser(t, "u_lst_2", "listuser2")
	defer cleanupUser(t, user.ID)

	for _, id := range []string{"n_fm_1", "n_fm_2"} {
		note := &model.Note{
			ID:         id,
			UserID:     user.ID,
			Visibility: model.NoteVisibilityPublic,
			Reactions:  datatypes.JSON([]byte("{}")),
		}
		require.NoError(t, repo.Create(note))
		defer cleanupNote(t, id)
	}

	out, err := repo.FindManyByIDsWithUser([]string{"n_fm_2", "n_fm_1", "ghost"})
	require.NoError(t, err)
	assert.Len(t, out, 2)
	// 順序がidsの順序を保つ
	assert.Equal(t, "n_fm_2", out[0].ID)
	assert.Equal(t, "n_fm_1", out[1].ID)

	// 空配列
	out, err = repo.FindManyByIDsWithUser(nil)
	require.NoError(t, err)
	assert.Empty(t, out)
}

// TestNoteRepository_FindManyByIDsWithUser_HydratesRelations は hydrated shape
// (User / Renote{.User,.Poll} / Reply{.User,.Poll} / Poll) を全て検証する強い
// oracle。hydration の実装を入れ替える (preload → batch 等) 際の wiring 回帰を
// 捕まえるためのもの。
func TestNoteRepository_FindManyByIDsWithUser_HydratesRelations(t *testing.T) {
	repo := NewNoteRepository(testDB)
	pollRepo := NewPollRepository(testDB)

	ua := insertTestUser(t, "u_hy_a", "hy_author")
	ur := insertTestUser(t, "u_hy_r", "hy_renoter")
	up := insertTestUser(t, "u_hy_p", "hy_replier")
	defer cleanupUser(t, ua.ID)
	defer cleanupUser(t, ur.ID)
	defer cleanupUser(t, up.ID)

	mkNote := func(id, uid string, hasPoll bool) *model.Note {
		n := &model.Note{ID: id, UserID: uid, Visibility: model.NoteVisibilityPublic, HasPoll: hasPoll, Reactions: datatypes.JSON([]byte("{}"))}
		require.NoError(t, repo.Create(n))
		t.Cleanup(func() { cleanupNote(t, id) })
		return n
	}
	mkPoll := func(noteID, uid string) {
		require.NoError(t, pollRepo.Create(&model.Poll{
			NoteID: noteID, Choices: pq.StringArray{"A", "B"}, Votes: pq.Int64Array{0, 0},
			NoteVisibility: model.NoteVisibilityPublic, UserID: uid,
		}))
	}

	// renote 先 / reply 先はそれぞれ別 user + poll を持つ。
	nr := mkNote("n_hy_r", ur.ID, true)
	mkPoll(nr.ID, ur.ID)
	np := mkNote("n_hy_p", up.ID, true)
	mkPoll(np.ID, up.ID)
	// main note: author=ua, renote=nr, reply=np, 自身も poll を持つ。
	main := &model.Note{
		ID: "n_hy_m", UserID: ua.ID, Visibility: model.NoteVisibilityPublic,
		HasPoll: true, RenoteID: &nr.ID, ReplyID: &np.ID, Reactions: datatypes.JSON([]byte("{}")),
	}
	require.NoError(t, repo.Create(main))
	t.Cleanup(func() { cleanupNote(t, main.ID) })
	mkPoll(main.ID, ua.ID)

	out, err := repo.FindManyByIDsWithUser([]string{main.ID})
	require.NoError(t, err)
	require.Len(t, out, 1)
	got := out[0]

	// author + 自 poll
	require.NotNil(t, got.User, "author user must be hydrated")
	assert.Equal(t, ua.ID, got.User.ID)
	require.NotNil(t, got.Poll, "own poll must be hydrated")
	assert.Equal(t, main.ID, got.Poll.NoteID)

	// renote + その user + poll
	require.NotNil(t, got.Renote, "renote must be hydrated")
	assert.Equal(t, nr.ID, got.Renote.ID)
	require.NotNil(t, got.Renote.User, "renote.user must be hydrated")
	assert.Equal(t, ur.ID, got.Renote.User.ID)
	require.NotNil(t, got.Renote.Poll, "renote.poll must be hydrated")
	assert.Equal(t, nr.ID, got.Renote.Poll.NoteID)

	// reply + その user + poll
	require.NotNil(t, got.Reply, "reply must be hydrated")
	assert.Equal(t, np.ID, got.Reply.ID)
	require.NotNil(t, got.Reply.User, "reply.user must be hydrated")
	assert.Equal(t, up.ID, got.Reply.User.ID)
	require.NotNil(t, got.Reply.Poll, "reply.poll must be hydrated")
	assert.Equal(t, np.ID, got.Reply.Poll.NoteID)
}

func TestNoteRepository_QueryErrors(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	db := testDB.WithContext(ctx)
	repo := NewNoteRepository(db)

	_, err := repo.ListByUserID("a", "", "", 10)
	assert.Error(t, err)

	_, err = repo.ListByChannelID("a", "", "", 10)
	assert.Error(t, err)

	_, err = repo.ListByChannelIDVisible("a", "", "", "", 10)
	assert.Error(t, err)

	_, err = repo.ListByChannelIDVisible("a", "viewer", "", "", 10)
	assert.Error(t, err)

	_, err = repo.FindManyByIDsWithUser([]string{"a"})
	assert.Error(t, err)

	_, err = repo.ListRenotesOf("a", "", "", 10)
	assert.Error(t, err)

	_, err = repo.ListRepliesOf("a", "", "", 10)
	assert.Error(t, err)

	_, err = repo.ListChildrenOf("a", "", "", 10)
	assert.Error(t, err)

	_, err = repo.SearchByFilter(model.NoteSearchFilter{Query: "a", Limit: 10})
	assert.Error(t, err)

	err = repo.IncrementCount("a", "renoteCount", 1)
	assert.Error(t, err)

	err = repo.IncrementReaction("a", "x", 1)
	assert.Error(t, err)
}

func TestNoteRepository_IncrementReaction(t *testing.T) {
	repo := NewNoteRepository(testDB)
	user := insertTestUser(t, "u_irx_1", "irxuser")
	defer cleanupUser(t, user.ID)

	note := &model.Note{
		ID:         "n_irx_1",
		UserID:     user.ID,
		Visibility: model.NoteVisibilityPublic,
		Reactions:  datatypes.JSON([]byte("{}")),
	}
	require.NoError(t, repo.Create(note))
	defer cleanupNote(t, note.ID)

	require.NoError(t, repo.IncrementReaction(note.ID, "👍", 2))
	require.NoError(t, repo.IncrementReaction(note.ID, "❤", 1))

	found, err := repo.FindByID(note.ID)
	require.NoError(t, err)
	assert.Contains(t, string(found.Reactions), "👍")
	assert.Contains(t, string(found.Reactions), "❤")

	// 0以下になったキーは削除される
	require.NoError(t, repo.IncrementReaction(note.ID, "👍", -2))
	found, err = repo.FindByID(note.ID)
	require.NoError(t, err)
	assert.NotContains(t, string(found.Reactions), "👍")
}

func TestNoteRepository_IncrementCount(t *testing.T) {
	repo := NewNoteRepository(testDB)
	user := insertTestUser(t, "u_n_inc_1", "incuser1")
	defer cleanupUser(t, user.ID)

	note := &model.Note{
		ID:         "n_inc_1",
		UserID:     user.ID,
		Visibility: model.NoteVisibilityPublic,
		Reactions:  datatypes.JSON([]byte("{}")),
	}
	require.NoError(t, repo.Create(note))
	defer cleanupNote(t, note.ID)

	require.NoError(t, repo.IncrementCount(note.ID, "renoteCount", 2))
	require.NoError(t, repo.IncrementCount(note.ID, "repliesCount", 3))

	found, err := repo.FindByID(note.ID)
	require.NoError(t, err)
	assert.Equal(t, int16(2), found.RenoteCount)
	assert.Equal(t, int16(3), found.RepliesCount)
}

func TestNoteRepository_ListRenotesOf(t *testing.T) {
	repo := NewNoteRepository(testDB)
	user := insertTestUser(t, "u_lr_1", "lruser")
	defer cleanupUser(t, user.ID)

	// 元ノート
	parent := &model.Note{
		ID: "n_lr_parent", UserID: user.ID, Visibility: model.NoteVisibilityPublic,
		Reactions: datatypes.JSON([]byte("{}")),
	}
	require.NoError(t, repo.Create(parent))
	defer cleanupNote(t, parent.ID)

	// 3件のrenote
	parentID := parent.ID
	for _, id := range []string{"n_lr_r1", "n_lr_r2", "n_lr_r3"} {
		n := &model.Note{
			ID: id, UserID: user.ID, Visibility: model.NoteVisibilityPublic,
			RenoteID:  &parentID,
			Reactions: datatypes.JSON([]byte("{}")),
		}
		require.NoError(t, repo.Create(n))
		defer cleanupNote(t, id)
	}

	out, err := repo.ListRenotesOf(parent.ID, "", "", 10)
	require.NoError(t, err)
	assert.Len(t, out, 3)
	assert.Equal(t, "n_lr_r3", out[0].ID)

	out, err = repo.ListRenotesOf(parent.ID, "n_lr_r3", "", 10)
	require.NoError(t, err)
	assert.Len(t, out, 2)

	out, err = repo.ListRenotesOf(parent.ID, "", "n_lr_r1", 10)
	require.NoError(t, err)
	assert.Len(t, out, 2)
}

func TestNoteRepository_ListRepliesOf(t *testing.T) {
	repo := NewNoteRepository(testDB)
	user := insertTestUser(t, "u_lp_1", "lpuser")
	defer cleanupUser(t, user.ID)

	parent := &model.Note{
		ID: "n_lp_parent", UserID: user.ID, Visibility: model.NoteVisibilityPublic,
		Reactions: datatypes.JSON([]byte("{}")),
	}
	require.NoError(t, repo.Create(parent))
	defer cleanupNote(t, parent.ID)

	parentID := parent.ID
	for _, id := range []string{"n_lp_r1", "n_lp_r2"} {
		n := &model.Note{
			ID: id, UserID: user.ID, Visibility: model.NoteVisibilityPublic,
			ReplyID:   &parentID,
			Reactions: datatypes.JSON([]byte("{}")),
		}
		require.NoError(t, repo.Create(n))
		defer cleanupNote(t, id)
	}

	out, err := repo.ListRepliesOf(parent.ID, "", "", 10)
	require.NoError(t, err)
	assert.Len(t, out, 2)

	out, err = repo.ListRepliesOf(parent.ID, "n_lp_r2", "", 10)
	require.NoError(t, err)
	assert.Len(t, out, 1)

	out, err = repo.ListRepliesOf(parent.ID, "", "n_lp_r1", 10)
	require.NoError(t, err)
	assert.Len(t, out, 1)
}

func TestNoteRepository_ListChildrenOf(t *testing.T) {
	repo := NewNoteRepository(testDB)
	user := insertTestUser(t, "u_lc_1", "lcuser")
	defer cleanupUser(t, user.ID)

	parent := &model.Note{
		ID: "n_lc_parent", UserID: user.ID, Visibility: model.NoteVisibilityPublic,
		Reactions: datatypes.JSON([]byte("{}")),
	}
	require.NoError(t, repo.Create(parent))
	defer cleanupNote(t, parent.ID)

	parentID := parent.ID
	// 1件はreply, 1件はquote renote (IDの辞書順を昇順 c1 < c2 にしておく)
	reply := &model.Note{
		ID: "n_lc_c1", UserID: user.ID, Visibility: model.NoteVisibilityPublic,
		ReplyID:   &parentID,
		Reactions: datatypes.JSON([]byte("{}")),
	}
	require.NoError(t, repo.Create(reply))
	defer cleanupNote(t, reply.ID)

	quote := &model.Note{
		ID: "n_lc_c2", UserID: user.ID, Visibility: model.NoteVisibilityPublic,
		RenoteID:  &parentID,
		Reactions: datatypes.JSON([]byte("{}")),
	}
	require.NoError(t, repo.Create(quote))
	defer cleanupNote(t, quote.ID)

	out, err := repo.ListChildrenOf(parent.ID, "", "", 10)
	require.NoError(t, err)
	assert.Len(t, out, 2)

	out, err = repo.ListChildrenOf(parent.ID, "n_lc_c2", "", 10)
	require.NoError(t, err)
	assert.Len(t, out, 1)
	assert.Equal(t, "n_lc_c1", out[0].ID)

	out, err = repo.ListChildrenOf(parent.ID, "", "n_lc_c1", 10)
	require.NoError(t, err)
	assert.Len(t, out, 1)
	assert.Equal(t, "n_lc_c2", out[0].ID)
}

func TestNoteRepository_SearchByFilter(t *testing.T) {
	repo := NewNoteRepository(testDB)
	localUser := insertTestUser(t, "u_se_1", "seuser")
	defer cleanupUser(t, localUser.ID)
	otherLocal := insertTestUser(t, "u_se_2", "seuser2")
	defer cleanupUser(t, otherLocal.ID)
	remoteHost := "remote.example"
	remoteUser := insertRemoteTestUser(t, "u_se_r", "seremote", remoteHost)
	defer cleanupUser(t, remoteUser.ID)

	channelID := "ch_se_1"
	hello := "Hello world this is searchable"
	other := "completely different"
	private := "Hello but private"
	helloChannel := "Hello in channel"
	helloRemote := "Hello from a remote instance"

	notes := []*model.Note{
		{ID: "n_se_1", UserID: localUser.ID, Text: &hello, Visibility: model.NoteVisibilityPublic, Reactions: datatypes.JSON([]byte("{}"))},
		{ID: "n_se_2", UserID: localUser.ID, Text: &other, Visibility: model.NoteVisibilityPublic, Reactions: datatypes.JSON([]byte("{}"))},
		{ID: "n_se_3", UserID: localUser.ID, Text: &private, Visibility: model.NoteVisibilityFollowers, Reactions: datatypes.JSON([]byte("{}"))},
		{ID: "n_se_4", UserID: otherLocal.ID, Text: &hello, Visibility: model.NoteVisibilityHome, Reactions: datatypes.JSON([]byte("{}"))},
		{ID: "n_se_5", UserID: localUser.ID, Text: &helloChannel, Visibility: model.NoteVisibilityPublic, ChannelID: &channelID, Reactions: datatypes.JSON([]byte("{}"))},
		{ID: "n_se_6", UserID: remoteUser.ID, Text: &helloRemote, Visibility: model.NoteVisibilityPublic, UserHost: &remoteHost, Reactions: datatypes.JSON([]byte("{}"))},
	}
	for _, n := range notes {
		require.NoError(t, repo.Create(n))
		defer cleanupNote(t, n.ID)
	}

	// 基本: 部分一致 + visibility フィルタ。followers (n_se_3) は除外。
	out, err := repo.SearchByFilter(model.NoteSearchFilter{Query: "hello", Limit: 10})
	require.NoError(t, err)
	got := idsOf(out)
	assert.ElementsMatch(t, []string{"n_se_1", "n_se_4", "n_se_5", "n_se_6"}, got)

	// userId フィルタ
	out, err = repo.SearchByFilter(model.NoteSearchFilter{Query: "hello", UserID: localUser.ID, Limit: 10})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"n_se_1", "n_se_5"}, idsOf(out))

	// channelId フィルタ
	out, err = repo.SearchByFilter(model.NoteSearchFilter{Query: "hello", ChannelID: channelID, Limit: 10})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"n_se_5"}, idsOf(out))

	// host フィルタ "." → ローカル限定
	out, err = repo.SearchByFilter(model.NoteSearchFilter{Query: "hello", Host: ".", Limit: 10})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"n_se_1", "n_se_4", "n_se_5"}, idsOf(out))

	// host フィルタ — 特定ホスト
	out, err = repo.SearchByFilter(model.NoteSearchFilter{Query: "hello", Host: remoteHost, Limit: 10})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"n_se_6"}, idsOf(out))

	// untilID / sinceID の分岐
	out, err = repo.SearchByFilter(model.NoteSearchFilter{Query: "hello", UntilID: "n_se_4", Limit: 10})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"n_se_1"}, idsOf(out))

	out, err = repo.SearchByFilter(model.NoteSearchFilter{Query: "hello", SinceID: "n_se_4", Limit: 10})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"n_se_5", "n_se_6"}, idsOf(out))

	// Limit デフォルト (0 → 10) を踏むケース
	out, err = repo.SearchByFilter(model.NoteSearchFilter{Query: "hello"})
	require.NoError(t, err)
	assert.Len(t, out, 4)
}

func TestNoteRepository_SearchByFilter_PgroongaOperator(t *testing.T) {
	repo := NewNoteRepository(testDB)
	// pgroonga 拡張は CI / testcontainers の素の Postgres には存在しない。
	// そこで `&@~` 演算子が解決されない (=エラーメッセージに `&@~` が
	// 含まれる) ことを以て「Pgroonga ブランチが選択されその SQL が
	// 実際に発行された」ことを検証する。
	_, err := repo.SearchByFilter(model.NoteSearchFilter{Query: "hello", Pgroonga: true, Limit: 10})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "&@~")
}

// idsOf is a tiny helper to extract note IDs for assertion comparisons.
func idsOf(notes []*model.Note) []string {
	out := make([]string, 0, len(notes))
	for _, n := range notes {
		out = append(out, n.ID)
	}
	return out
}

func TestNoteRepository_ListFeatured(t *testing.T) {
	repo := NewNoteRepository(testDB)
	user := insertTestUser(t, "feat_u", "featuser")
	defer cleanupUser(t, user.ID)

	n := &model.Note{ID: "feat_n1", UserID: user.ID, Visibility: "public", RenoteCount: 10}
	require.NoError(t, testDB.Create(n).Error)
	defer testDB.Exec(`DELETE FROM "note" WHERE id = ?`, n.ID)

	notes, err := repo.ListFeatured("", "", 10, 0)
	require.NoError(t, err)
	assert.NotEmpty(t, notes)

	// default limit
	notes, err = repo.ListFeatured("", "", 0, 0)
	require.NoError(t, err)
	assert.NotEmpty(t, notes)

	// offset
	notes, err = repo.ListFeatured("", "", 10, 1)
	require.NoError(t, err)
	_ = notes
}

// channelId 指定時は当該チャンネルに属するノートだけが返ること (#489)。
// 過去はこの絞り込みが無く、ハイライトに無関係ノートが混入していた。
func TestNoteRepository_ListFeatured_ChannelFilter(t *testing.T) {
	repo := NewNoteRepository(testDB)
	user := insertTestUser(t, "feat_chu", "featchu")
	defer cleanupUser(t, user.ID)

	chID := "feat_ch1"
	require.NoError(t, testDB.Exec(`INSERT INTO channel (id, name, "userId") VALUES (?, ?, ?)`, chID, "feat-ch", user.ID).Error)
	defer testDB.Exec(`DELETE FROM channel WHERE id = ?`, chID)

	chPtr := chID
	chNote := &model.Note{ID: "feat_chn1", UserID: user.ID, Visibility: "public", RenoteCount: 5, ChannelID: &chPtr}
	require.NoError(t, testDB.Create(chNote).Error)
	defer testDB.Exec(`DELETE FROM "note" WHERE id = ?`, chNote.ID)

	otherNote := &model.Note{ID: "feat_othn", UserID: user.ID, Visibility: "public", RenoteCount: 10}
	require.NoError(t, testDB.Create(otherNote).Error)
	defer testDB.Exec(`DELETE FROM "note" WHERE id = ?`, otherNote.ID)

	rows, err := repo.ListFeatured(chID, "", 10, 0)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, chNote.ID, rows[0].ID)
}

func TestNoteRepository_ListFeatured_Error(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	repo := NewNoteRepository(testDB.WithContext(ctx))
	_, err := repo.ListFeatured("", "", 10, 0)
	assert.Error(t, err)
}

func TestNoteRepository_FindRenoteByUser(t *testing.T) {
	repo := NewNoteRepository(testDB)
	user := insertTestUser(t, "unrn_u", "unrnuser")
	defer cleanupUser(t, user.ID)

	orig := &model.Note{ID: "unrn_orig", UserID: user.ID, Visibility: "public"}
	require.NoError(t, testDB.Create(orig).Error)
	defer testDB.Exec(`DELETE FROM "note" WHERE id = ?`, orig.ID)

	renoteID := orig.ID
	rn := &model.Note{ID: "unrn_rn", UserID: user.ID, RenoteID: &renoteID, Visibility: "public"}
	require.NoError(t, testDB.Create(rn).Error)
	defer testDB.Exec(`DELETE FROM "note" WHERE id = ?`, rn.ID)

	found, err := repo.FindRenoteByUser(user.ID, orig.ID)
	require.NoError(t, err)
	assert.Equal(t, rn.ID, found.ID)
}

func TestNoteRepository_FindRenoteByUser_NotFound(t *testing.T) {
	repo := NewNoteRepository(testDB)
	_, err := repo.FindRenoteByUser("ghost", "ghost")
	assert.Error(t, err)
}

func TestNoteRepository_ListMentions(t *testing.T) {
	repo := NewNoteRepository(testDB)
	user := insertTestUser(t, "ment_u", "mentuser")
	mentionee := insertTestUser(t, "ment_m", "mentionee")
	defer cleanupUser(t, user.ID)
	defer cleanupUser(t, mentionee.ID)

	n := &model.Note{ID: "ment_n1", UserID: user.ID, Visibility: "public", Mentions: []string{mentionee.ID}}
	require.NoError(t, testDB.Create(n).Error)
	defer testDB.Exec(`DELETE FROM "note" WHERE id = ?`, n.ID)

	notes, err := repo.ListMentions(mentionee.ID, 10, "", "")
	require.NoError(t, err)
	assert.NotEmpty(t, notes)

	// with cursors
	notes, err = repo.ListMentions(mentionee.ID, 10, "", "zzz")
	require.NoError(t, err)
	assert.NotEmpty(t, notes)

	notes, err = repo.ListMentions(mentionee.ID, 10, "000", "")
	require.NoError(t, err)
	assert.NotEmpty(t, notes)
}

func TestNoteRepository_ListMentions_DefaultLimit(t *testing.T) {
	repo := NewNoteRepository(testDB)
	notes, err := repo.ListMentions("nobody", 0, "", "")
	require.NoError(t, err)
	assert.Empty(t, notes)
}

func TestNoteRepository_ListMentions_Error(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	repo := NewNoteRepository(testDB.WithContext(ctx))
	_, err := repo.ListMentions("x", 10, "", "")
	assert.Error(t, err)
}

func TestNoteRepository_SearchByTag(t *testing.T) {
	repo := NewNoteRepository(testDB)
	user := insertTestUser(t, "tag_u", "taguser")
	defer cleanupUser(t, user.ID)

	n := &model.Note{ID: "tag_n1", UserID: user.ID, Visibility: "public", Tags: []string{"golang", "misskey"}}
	require.NoError(t, testDB.Create(n).Error)
	defer testDB.Exec(`DELETE FROM "note" WHERE id = ?`, n.ID)

	notes, err := repo.SearchByTag("golang", 10, "", "")
	require.NoError(t, err)
	assert.NotEmpty(t, notes)

	notes, err = repo.SearchByTag("nonexistent", 10, "", "")
	require.NoError(t, err)
	assert.Empty(t, notes)

	// default limit
	notes, err = repo.SearchByTag("golang", 0, "", "")
	require.NoError(t, err)
	assert.NotEmpty(t, notes)

	// with cursors
	notes, err = repo.SearchByTag("golang", 10, "000", "zzz")
	require.NoError(t, err)
	assert.NotEmpty(t, notes)
}

func TestNoteRepository_SearchByTag_Error(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	repo := NewNoteRepository(testDB.WithContext(ctx))
	_, err := repo.SearchByTag("x", 10, "", "")
	assert.Error(t, err)
}

// TestNoteRepository_DeleteByUserBatch verifies that one call deletes up to
// batchSize rows and callers are expected to loop.
func TestNoteRepository_DeleteByUserBatch(t *testing.T) {
	repo := NewNoteRepository(testDB)
	user := insertTestUser(t, "u_dbu_v", "dbuviewer")
	defer cleanupUser(t, user.ID)

	ids := []string{"n_dbu_1", "n_dbu_2", "n_dbu_3"}
	text := "hi"
	for _, id := range ids {
		n := &model.Note{
			ID: id, UserID: user.ID, Text: &text,
			Visibility: model.NoteVisibilityPublic,
			Reactions:  datatypes.JSON([]byte("{}")),
		}
		require.NoError(t, repo.Create(n))
		defer cleanupNote(t, id)
	}

	// 1 バッチで 2 件だけ消える。
	n, err := repo.DeleteByUserBatch(user.ID, 2)
	require.NoError(t, err)
	assert.EqualValues(t, 2, n)

	// 残りの 1 件は次のバッチで消える。
	n, err = repo.DeleteByUserBatch(user.ID, 100)
	require.NoError(t, err)
	assert.EqualValues(t, 1, n)

	// 以降はすべて 0 件 (呼び出し側の break 条件)。
	n, err = repo.DeleteByUserBatch(user.ID, 100)
	require.NoError(t, err)
	assert.EqualValues(t, 0, n)

	// userId 空は no-op
	n, err = repo.DeleteByUserBatch("", 10)
	require.NoError(t, err)
	assert.EqualValues(t, 0, n)
}

// TestNoteRepository_ListHomeTimeline_MutedChannelsRespectedWithPrecedence
// guards against the SQL precedence bug where MutedChannelIDs would short
// circuit other AND conditions via an unparenthesized OR. Places two notes
// by the viewer (one in a muted channel) and two notes by an unfollowed
// user (one in an open channel). The home timeline must return only the
// viewer's plain note. With missing parentheses the follow filter would be
// bypassed and the unfollowed user's notes would leak through.
func TestNoteRepository_ListHomeTimeline_MutedChannelsRespectedWithPrecedence(t *testing.T) {
	repo := NewNoteRepository(testDB)
	viewer := insertTestUser(t, "u_mc_v", "mcviewer")
	defer cleanupUser(t, viewer.ID)
	other := insertTestUser(t, "u_mc_o", "mcother")
	defer cleanupUser(t, other.ID)

	mutedChannel := "ch_mc_muted"
	openChannel := "ch_mc_open"
	selfText := "self note"
	selfInMuted := "self in muted channel"
	otherText := "other note (not followed)"
	otherInOpen := "other in open channel (not followed)"

	notes := []*model.Note{
		{ID: "n_mc_1", UserID: viewer.ID, Text: &selfText, Visibility: model.NoteVisibilityPublic, Reactions: datatypes.JSON([]byte("{}"))},
		{ID: "n_mc_2", UserID: viewer.ID, Text: &selfInMuted, Visibility: model.NoteVisibilityPublic, ChannelID: &mutedChannel, Reactions: datatypes.JSON([]byte("{}"))},
		{ID: "n_mc_3", UserID: other.ID, Text: &otherText, Visibility: model.NoteVisibilityPublic, Reactions: datatypes.JSON([]byte("{}"))},
		{ID: "n_mc_4", UserID: other.ID, Text: &otherInOpen, Visibility: model.NoteVisibilityPublic, ChannelID: &openChannel, Reactions: datatypes.JSON([]byte("{}"))},
	}
	for _, n := range notes {
		require.NoError(t, repo.Create(n))
		defer cleanupNote(t, n.ID)
	}

	rows, err := repo.ListHomeTimeline(viewer.ID, 100, "", "", model.TimelineDBFilter{
		ViewerID:        viewer.ID,
		MutedChannelIDs: []string{mutedChannel},
	})
	require.NoError(t, err)

	ids := idsOf(rows)
	assert.ElementsMatch(t, []string{"n_mc_1"}, ids,
		"follow + mute フィルタが OR で短絡されていない (SQL precedence バグのリグレッション)")
}

// TestNoteRepository_ListLocalTimeline_MutedUsersFilter verifies that
// model.TimelineDBFilter.MutedUserIDs is applied at SQL level (#892),
// excluding both notes authored by muted users AND renotes whose original
// author is muted. heavy-mute viewer でも limit ぶん fill されることも
// 同時に確認する (= post-fetch filter 時の UX regression 回避)。
func TestNoteRepository_ListLocalTimeline_MutedUsersFilter(t *testing.T) {
	repo := NewNoteRepository(testDB)
	mutedAuthor := insertTestUser(t, "u_mu_m", "mumuted")
	defer cleanupUser(t, mutedAuthor.ID)
	okAuthor := insertTestUser(t, "u_mu_o", "muok")
	defer cleanupUser(t, okAuthor.ID)
	mutedRenoteSrc := insertTestUser(t, "u_mu_rs", "murenotesrc")
	defer cleanupUser(t, mutedRenoteSrc.ID)

	mkPlain := func(id, uid string) *model.Note {
		text := "x"
		return &model.Note{
			ID: id, UserID: uid, Text: &text,
			Visibility: model.NoteVisibilityPublic,
			Reactions:  datatypes.JSON([]byte("{}")),
		}
	}

	// muted author の note (除外されるべき)
	muted1 := mkPlain("n_mu_m1", mutedAuthor.ID)
	muted2 := mkPlain("n_mu_m2", mutedAuthor.ID)
	require.NoError(t, repo.Create(muted1))
	require.NoError(t, repo.Create(muted2))
	defer cleanupNote(t, muted1.ID)
	defer cleanupNote(t, muted2.ID)

	// muted renote source (= ok author が muted user の note を renote。
	// renoteUserId が muted なので除外されるべき)
	renoteSrc := mkPlain("n_mu_src", mutedRenoteSrc.ID)
	require.NoError(t, repo.Create(renoteSrc))
	defer cleanupNote(t, renoteSrc.ID)
	renote := &model.Note{
		ID: "n_mu_renote", UserID: okAuthor.ID,
		RenoteID: &renoteSrc.ID, RenoteUserID: &mutedRenoteSrc.ID,
		Visibility: model.NoteVisibilityPublic,
		Reactions:  datatypes.JSON([]byte("{}")),
	}
	require.NoError(t, repo.Create(renote))
	defer cleanupNote(t, renote.ID)

	// non-muted note (= ok author の plain note、含まれるべき)
	ok1 := mkPlain("n_mu_ok1", okAuthor.ID)
	ok2 := mkPlain("n_mu_ok2", okAuthor.ID)
	ok3 := mkPlain("n_mu_ok3", okAuthor.ID)
	require.NoError(t, repo.Create(ok1))
	require.NoError(t, repo.Create(ok2))
	require.NoError(t, repo.Create(ok3))
	defer cleanupNote(t, ok1.ID)
	defer cleanupNote(t, ok2.ID)
	defer cleanupNote(t, ok3.ID)

	rows, err := repo.ListLocalTimeline(100, "", "", model.TimelineDBFilter{
		MutedUserIDs: []string{mutedAuthor.ID, mutedRenoteSrc.ID},
	})
	require.NoError(t, err)

	ids := idsOf(rows)
	// muted author の note と muted renote source は除外
	assert.NotContains(t, ids, muted1.ID)
	assert.NotContains(t, ids, muted2.ID)
	assert.NotContains(t, ids, renote.ID)
	// renoteSrc 自体は muted author の plain note として除外される
	assert.NotContains(t, ids, renoteSrc.ID)
	// ok author の plain note 3 件が残る
	assert.Contains(t, ids, ok1.ID)
	assert.Contains(t, ids, ok2.ID)
	assert.Contains(t, ids, ok3.ID)

	// heavy-mute fill: limit=2 を要求した時、SQL push-down なら non-muted note
	// が limit 件返ることを確認する (post-fetch filter だと最大 2 → muted 除外
	// で 0 件になり得たが、SQL 段階で除外しているので必ず 2 件返る)。
	rows, err = repo.ListLocalTimeline(2, "", "", model.TimelineDBFilter{
		MutedUserIDs: []string{mutedAuthor.ID, mutedRenoteSrc.ID},
	})
	require.NoError(t, err)
	assert.Len(t, rows, 2, "SQL push-down で limit ぶん non-muted note が fill されること (#892)")
}

// TestNoteRepository_ListLocalTimeline_MutedUsersSubquery は production
// 経路 (UseMutingSubquery=true + ViewerID) で muting テーブルへ subquery
// する filter が正しく動作することを確認する (#894)。muting row の
// expiresAt が NULL / 未来 / 過去の 3 ケースで active mute だけ filter
// に効くこと、renote 元 user の mute も同時に弾かれることを担保する。
func TestNoteRepository_ListLocalTimeline_MutedUsersSubquery(t *testing.T) {
	repo := NewNoteRepository(testDB)
	mutingRepo := NewMutingRepository(testDB)
	viewer := insertTestUser(t, "u_mus_v", "musv")
	defer cleanupUser(t, viewer.ID)
	mutedAuthor := insertTestUser(t, "u_mus_m", "musm")
	defer cleanupUser(t, mutedAuthor.ID)
	expiredMutedAuthor := insertTestUser(t, "u_mus_e", "muse")
	defer cleanupUser(t, expiredMutedAuthor.ID)
	mutedRenoteSrc := insertTestUser(t, "u_mus_rs", "musrs")
	defer cleanupUser(t, mutedRenoteSrc.ID)
	okAuthor := insertTestUser(t, "u_mus_o", "muso")
	defer cleanupUser(t, okAuthor.ID)

	// active mute (expiresAt nil)
	activeMute := &model.Muting{ID: "mute_us_a", MuterID: viewer.ID, MuteeID: mutedAuthor.ID}
	require.NoError(t, mutingRepo.Create(activeMute))
	defer cleanupMuting(t, activeMute.ID)

	// active mute (renote 元)
	renoteMute := &model.Muting{ID: "mute_us_r", MuterID: viewer.ID, MuteeID: mutedRenoteSrc.ID}
	require.NoError(t, mutingRepo.Create(renoteMute))
	defer cleanupMuting(t, renoteMute.ID)

	// expired mute (subquery で除外されるべき = 該当 user の note は filter 通過する)
	pastTime := time.Now().Add(-1 * time.Hour)
	expiredMute := &model.Muting{ID: "mute_us_e", MuterID: viewer.ID, MuteeID: expiredMutedAuthor.ID, ExpiresAt: &pastTime}
	require.NoError(t, mutingRepo.Create(expiredMute))
	defer cleanupMuting(t, expiredMute.ID)

	mkPlain := func(id, uid string) *model.Note {
		text := "x"
		return &model.Note{
			ID: id, UserID: uid, Text: &text,
			Visibility: model.NoteVisibilityPublic,
			Reactions:  datatypes.JSON([]byte("{}")),
		}
	}

	// muted active author の note (除外)
	mutedNote := mkPlain("n_mus_m", mutedAuthor.ID)
	require.NoError(t, repo.Create(mutedNote))
	defer cleanupNote(t, mutedNote.ID)

	// expired mute author の note (含まれる = subquery が NOW() フィルタを尊重)
	expiredAuthorNote := mkPlain("n_mus_e", expiredMutedAuthor.ID)
	require.NoError(t, repo.Create(expiredAuthorNote))
	defer cleanupNote(t, expiredAuthorNote.ID)

	// renote (= ok author が mutedRenoteSrc を renote。renoteUserId が active
	// mute なので除外)
	renoteSrc := mkPlain("n_mus_src", mutedRenoteSrc.ID)
	require.NoError(t, repo.Create(renoteSrc))
	defer cleanupNote(t, renoteSrc.ID)
	renote := &model.Note{
		ID: "n_mus_renote", UserID: okAuthor.ID,
		RenoteID: &renoteSrc.ID, RenoteUserID: &mutedRenoteSrc.ID,
		Visibility: model.NoteVisibilityPublic,
		Reactions:  datatypes.JSON([]byte("{}")),
	}
	require.NoError(t, repo.Create(renote))
	defer cleanupNote(t, renote.ID)

	// ok plain note (含まれる)
	okNote := mkPlain("n_mus_ok", okAuthor.ID)
	require.NoError(t, repo.Create(okNote))
	defer cleanupNote(t, okNote.ID)

	// production 経路: UseMutingSubquery=true / MutedUserIDs 空
	rows, err := repo.ListLocalTimeline(100, "", "", model.TimelineDBFilter{
		ViewerID:          viewer.ID,
		UseMutingSubquery: true,
	})
	require.NoError(t, err)
	ids := idsOf(rows)
	assert.NotContains(t, ids, mutedNote.ID, "active mute author の note は除外")
	assert.NotContains(t, ids, renoteSrc.ID, "active mute author の plain note (renote 元) も除外")
	assert.NotContains(t, ids, renote.ID, "renoteUserId が active mute の renote も除外")
	assert.Contains(t, ids, expiredAuthorNote.ID, "expired mute は subquery で除外され filter 通過")
	assert.Contains(t, ids, okNote.ID, "ok author の note は含まれる")
}

// TestNoteRepository_ListLocalTimeline_RenoteMutedSubquery は production
// 経路 (UseRenoteMutingSubquery=true + ViewerID) で renote_muting への
// NOT EXISTS subquery が pure renote のみ除外し、plain note / quote
// renote は通すことを直接 verify する (#903)。
func TestNoteRepository_ListLocalTimeline_RenoteMutedSubquery(t *testing.T) {
	repo := NewNoteRepository(testDB)
	renoteMutingRepo := NewRenoteMutingRepository(testDB)
	viewer := insertTestUser(t, "u_rms_v", "rmsv")
	defer cleanupUser(t, viewer.ID)
	mutedRenoter := insertTestUser(t, "u_rms_m", "rmsm")
	defer cleanupUser(t, mutedRenoter.ID)
	srcAuthor := insertTestUser(t, "u_rms_s", "rmss")
	defer cleanupUser(t, srcAuthor.ID)

	// viewer renote-mutes mutedRenoter
	rmRec := &model.RenoteMuting{ID: "rms_mute", MuterID: viewer.ID, MuteeID: mutedRenoter.ID}
	require.NoError(t, renoteMutingRepo.Create(rmRec))
	defer cleanupRenoteMuting(t, rmRec.ID)

	// mutedRenoter の plain note (= renote ではない、含まれるべき)
	plainText := "plain"
	plainNote := &model.Note{
		ID: "n_rms_plain", UserID: mutedRenoter.ID, Text: &plainText,
		Visibility: model.NoteVisibilityPublic,
		Reactions:  datatypes.JSON([]byte("{}")),
	}
	require.NoError(t, repo.Create(plainNote))
	defer cleanupNote(t, plainNote.ID)

	// srcAuthor の original note (renote 元)
	srcText := "src"
	srcNote := &model.Note{
		ID: "n_rms_src", UserID: srcAuthor.ID, Text: &srcText,
		Visibility: model.NoteVisibilityPublic,
		Reactions:  datatypes.JSON([]byte("{}")),
	}
	require.NoError(t, repo.Create(srcNote))
	defer cleanupNote(t, srcNote.ID)

	// mutedRenoter の pure renote (除外されるべき)
	pureRenote := &model.Note{
		ID: "n_rms_pure", UserID: mutedRenoter.ID,
		RenoteID: &srcNote.ID, RenoteUserID: &srcAuthor.ID,
		Visibility: model.NoteVisibilityPublic,
		Reactions:  datatypes.JSON([]byte("{}")),
	}
	require.NoError(t, repo.Create(pureRenote))
	defer cleanupNote(t, pureRenote.ID)

	// mutedRenoter の quote renote (text 付き、含まれるべき)
	quoteText := "quote!"
	quoteRenote := &model.Note{
		ID: "n_rms_quote", UserID: mutedRenoter.ID, Text: &quoteText,
		RenoteID: &srcNote.ID, RenoteUserID: &srcAuthor.ID,
		Visibility: model.NoteVisibilityPublic,
		Reactions:  datatypes.JSON([]byte("{}")),
	}
	require.NoError(t, repo.Create(quoteRenote))
	defer cleanupNote(t, quoteRenote.ID)

	rows, err := repo.ListLocalTimeline(100, "", "", model.TimelineDBFilter{
		ViewerID:                viewer.ID,
		UseRenoteMutingSubquery: true,
	})
	require.NoError(t, err)
	ids := idsOf(rows)

	assert.Contains(t, ids, plainNote.ID, "plain note は renote-mute 対象外")
	assert.Contains(t, ids, srcNote.ID, "src author の note は対象外 (= viewer は src を mute していない)")
	assert.Contains(t, ids, quoteRenote.ID, "quote renote (text 付き) は通る")
	assert.NotContains(t, ids, pureRenote.ID, "muted renoter の pure renote のみ除外")
}

// TestNoteRepository_ListHomeTimeline_MutedUsersWithFollowFilter は HomeTimeline
// で MutedUserIDs と follow 制約が両立することを確認する (#892)。muted user は
// follow していても home から除外されるべき、かつ MutedUserIDs の OR 節バグで
// 非 follower の note が漏れない (= MutedChannel と同 SQL precedence ガード)。
func TestNoteRepository_ListHomeTimeline_MutedUsersWithFollowFilter(t *testing.T) {
	repo := NewNoteRepository(testDB)
	viewer := insertTestUser(t, "u_mhfu_v", "mhfuviewer")
	defer cleanupUser(t, viewer.ID)
	followedMuted := insertTestUser(t, "u_mhfu_fm", "mhfufollowedmuted")
	defer cleanupUser(t, followedMuted.ID)
	followedOK := insertTestUser(t, "u_mhfu_fo", "mhfufollowedok")
	defer cleanupUser(t, followedOK.ID)
	stranger := insertTestUser(t, "u_mhfu_s", "mhfustranger")
	defer cleanupUser(t, stranger.ID)

	// viewer は followedMuted と followedOK を follow するが、followedMuted は
	// mute 済み。stranger は未 follow なので home に含まれてはいけない。
	require.NoError(t, testDB.Exec(
		`INSERT INTO "following" (id, "followerId", "followeeId") VALUES (?, ?, ?)`,
		"flw_mhfu_1", viewer.ID, followedMuted.ID,
	).Error)
	defer testDB.Exec(`DELETE FROM "following" WHERE id = ?`, "flw_mhfu_1")
	require.NoError(t, testDB.Exec(
		`INSERT INTO "following" (id, "followerId", "followeeId") VALUES (?, ?, ?)`,
		"flw_mhfu_2", viewer.ID, followedOK.ID,
	).Error)
	defer testDB.Exec(`DELETE FROM "following" WHERE id = ?`, "flw_mhfu_2")

	mk := func(id, uid string) *model.Note {
		text := "hi"
		return &model.Note{
			ID: id, UserID: uid, Text: &text,
			Visibility: model.NoteVisibilityPublic,
			Reactions:  datatypes.JSON([]byte("{}")),
		}
	}
	muted := mk("n_mhfu_m", followedMuted.ID)
	ok := mk("n_mhfu_o", followedOK.ID)
	leaked := mk("n_mhfu_s", stranger.ID)
	require.NoError(t, repo.Create(muted))
	require.NoError(t, repo.Create(ok))
	require.NoError(t, repo.Create(leaked))
	defer cleanupNote(t, muted.ID)
	defer cleanupNote(t, ok.ID)
	defer cleanupNote(t, leaked.ID)

	rows, err := repo.ListHomeTimeline(viewer.ID, 100, "", "", model.TimelineDBFilter{
		ViewerID:     viewer.ID,
		MutedUserIDs: []string{followedMuted.ID},
	})
	require.NoError(t, err)

	ids := idsOf(rows)
	assert.ElementsMatch(t, []string{ok.ID}, ids,
		"follow + mute フィルタが両立し、stranger が漏れず muted user も除外されること (#892)")
}

func TestNoteRepository_CountReplyTargets(t *testing.T) {
	repo := NewNoteRepository(testDB)
	author := insertTestUser(t, "u_crt_a", "crtA")
	defer cleanupUser(t, author.ID)
	t1 := insertTestUser(t, "u_crt_t1", "crtT1")
	defer cleanupUser(t, t1.ID)
	t2 := insertTestUser(t, "u_crt_t2", "crtT2")
	defer cleanupUser(t, t2.ID)

	// author から t1 に 3 件、t2 に 1 件返信。author 自身への返信は除外される。
	replyID := "n_crt_src"
	require.NoError(t, testDB.Create(&model.Note{ID: replyID, UserID: t1.ID, Visibility: model.NoteVisibilityPublic}).Error)
	defer testDB.Exec(`DELETE FROM "note" WHERE id = ?`, replyID)
	mk := func(id, target string) *model.Note {
		return &model.Note{ID: id, UserID: author.ID, ReplyID: &replyID, ReplyUserID: &target, Visibility: model.NoteVisibilityPublic}
	}
	for _, n := range []*model.Note{
		mk("n_crt_1", t1.ID),
		mk("n_crt_2", t1.ID),
		mk("n_crt_3", t1.ID),
		mk("n_crt_4", t2.ID),
		mk("n_crt_5", author.ID), // 自己返信は無視
	} {
		require.NoError(t, testDB.Create(n).Error)
		defer testDB.Exec(`DELETE FROM "note" WHERE id = ?`, n.ID)
	}

	rows, err := repo.CountReplyTargets(author.ID, 10)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	assert.Equal(t, t1.ID, rows[0].UserID)
	assert.EqualValues(t, 3, rows[0].Count)
	assert.Equal(t, t2.ID, rows[1].UserID)
	assert.EqualValues(t, 1, rows[1].Count)

	// limit <= 0 は 10 にデフォルト。
	rows2, err := repo.CountReplyTargets(author.ID, 0)
	require.NoError(t, err)
	assert.Len(t, rows2, 2)
}

func TestNoteRepository_CountReplyTargets_Error(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	repo := NewNoteRepository(testDB.WithContext(ctx))
	_, err := repo.CountReplyTargets("me", 10)
	assert.Error(t, err)
}

// --- 追加テスト (#260 repository coverage: 0%関数) ---

func TestNoteRepository_ListByFileID(t *testing.T) {
	repo := NewNoteRepository(testDB)
	user := insertTestUser(t, "u_lbfi_1", "lbfiuser")
	defer cleanupUser(t, user.ID)

	text := "with file"
	n := &model.Note{
		ID:         "n_lbfi_1",
		UserID:     user.ID,
		Text:       &text,
		Visibility: model.NoteVisibilityPublic,
		Reactions:  datatypes.JSON([]byte("{}")),
		FileIDs:    pq.StringArray{"file_abc"},
	}
	require.NoError(t, repo.Create(n))
	defer cleanupNote(t, n.ID)

	got, err := repo.ListByFileID("file_abc", "", "", 10)
	require.NoError(t, err)
	assert.NotEmpty(t, got)

	empty, err := repo.ListByFileID("file_nonexistent", "", "", 10)
	require.NoError(t, err)
	assert.Empty(t, empty)
}

// frontend Paginator (cursor mode) との互換性確認: untilID 指定で
// id < untilID DESC, sinceID 指定で id > sinceID ASC を返すこと (#488)。
func TestNoteRepository_ListByFileID_Cursor(t *testing.T) {
	repo := NewNoteRepository(testDB)
	user := insertTestUser(t, "u_lbfi_2", "lbfiuser2")
	defer cleanupUser(t, user.ID)

	text := "with file cursor"
	for _, id := range []string{"n_lbfi_a", "n_lbfi_b", "n_lbfi_c", "n_lbfi_d"} {
		n := &model.Note{
			ID:         id,
			UserID:     user.ID,
			Text:       &text,
			Visibility: model.NoteVisibilityPublic,
			Reactions:  datatypes.JSON([]byte("{}")),
			FileIDs:    pq.StringArray{"file_pg"},
		}
		require.NoError(t, repo.Create(n))
		defer cleanupNote(t, n.ID)
	}

	// untilID="n_lbfi_c" → id < "n_lbfi_c" を DESC 2 件 = [b, a]
	rows, err := repo.ListByFileID("file_pg", "", "n_lbfi_c", 2)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	assert.Equal(t, "n_lbfi_b", rows[0].ID)
	assert.Equal(t, "n_lbfi_a", rows[1].ID)

	// sinceID="n_lbfi_b" → id > "n_lbfi_b" を ASC 2 件 = [c, d]
	rows, err = repo.ListByFileID("file_pg", "n_lbfi_b", "", 2)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	assert.Equal(t, "n_lbfi_c", rows[0].ID)
	assert.Equal(t, "n_lbfi_d", rows[1].ID)
}

func TestNoteRepository_IncrementUserNotesCount(t *testing.T) {
	repo := NewNoteRepository(testDB)
	user := insertTestUser(t, "u_n_inc_2", "incuser2")
	defer cleanupUser(t, user.ID)

	require.NoError(t, repo.IncrementUserNotesCount(user.ID, 3))
	require.NoError(t, repo.IncrementUserNotesCount(user.ID, -1))

	// 値はuserrepoから確認
	ur := NewUserRepository(testDB)
	got, err := ur.FindByID(user.ID)
	require.NoError(t, err)
	assert.Equal(t, 2, got.NotesCount)
}

func TestNoteRepository_ListLocalTimeline(t *testing.T) {
	repo := NewNoteRepository(testDB)
	user := insertTestUser(t, "u_llt_1", "lltuser")
	defer cleanupUser(t, user.ID)

	text := "local"
	n := &model.Note{
		ID:         "n_llt_1",
		UserID:     user.ID,
		Text:       &text,
		Visibility: model.NoteVisibilityPublic,
		Reactions:  datatypes.JSON([]byte("{}")),
	}
	require.NoError(t, repo.Create(n))
	defer cleanupNote(t, n.ID)

	filter := model.TimelineDBFilter{}
	rows, err := repo.ListLocalTimeline(50, "", "", filter)
	require.NoError(t, err)
	ids := make(map[string]bool, len(rows))
	for _, r := range rows {
		ids[r.ID] = true
	}
	assert.True(t, ids[n.ID])

	// since/until分岐を通す
	_, err = repo.ListLocalTimeline(50, "nonexistent", "", filter)
	require.NoError(t, err)
	_, err = repo.ListLocalTimeline(50, "", "nonexistent", filter)
	require.NoError(t, err)
}

func TestNoteRepository_ListGlobalTimeline(t *testing.T) {
	repo := NewNoteRepository(testDB)
	user := insertTestUser(t, "u_lgt_1", "lgtuser")
	defer cleanupUser(t, user.ID)

	text := "global"
	n := &model.Note{
		ID:         "n_lgt_1",
		UserID:     user.ID,
		Text:       &text,
		Visibility: model.NoteVisibilityPublic,
		Reactions:  datatypes.JSON([]byte("{}")),
	}
	require.NoError(t, repo.Create(n))
	defer cleanupNote(t, n.ID)

	filter := model.TimelineDBFilter{}
	rows, err := repo.ListGlobalTimeline(50, "", "", filter)
	require.NoError(t, err)
	ids := make(map[string]bool, len(rows))
	for _, r := range rows {
		ids[r.ID] = true
	}
	assert.True(t, ids[n.ID])

	// since/until分岐
	_, err = repo.ListGlobalTimeline(50, "nonexistent", "", filter)
	require.NoError(t, err)
	_, err = repo.ListGlobalTimeline(50, "", "nonexistent", filter)
	require.NoError(t, err)
}

// TestNoteRepository_SinceIDFlipsOrderASC verifies that sinceID-only
// pagination queries return rows in ASC order, matching upstream Misskey's
// QueryService.makePaginationQuery. Two representative note functions are
// exercised; the helper itself has unit coverage in pagination_test.go.
// Regression guard for #384.
func TestNoteRepository_SinceIDFlipsOrderASC(t *testing.T) {
	repo := NewNoteRepository(testDB)
	user := insertTestUser(t, "asc_u", "ascuser")
	defer cleanupUser(t, user.ID)

	// IDの lexicographic 順に作成: a < b < c
	for _, id := range []string{"asc_n_a", "asc_n_b", "asc_n_c"} {
		n := &model.Note{ID: id, UserID: user.ID, Visibility: "public", Tags: []string{"ascflip"}, Reactions: datatypes.JSON([]byte("{}"))}
		require.NoError(t, testDB.Create(n).Error)
		defer testDB.Exec(`DELETE FROM "note" WHERE id = ?`, id)
	}

	// sinceID単独指定: ASC順 (古い→新しい)。asc_n_a より大なので b, c が返る想定。
	notes, err := repo.SearchByTag("ascflip", 10, "asc_n_a", "")
	require.NoError(t, err)
	require.Len(t, notes, 2)
	assert.Equal(t, "asc_n_b", notes[0].ID)
	assert.Equal(t, "asc_n_c", notes[1].ID)

	// untilID単独指定: DESC順 (既存挙動)。asc_n_c より小なので b, a が返る。
	notes, err = repo.SearchByTag("ascflip", 10, "", "asc_n_c")
	require.NoError(t, err)
	require.Len(t, notes, 2)
	assert.Equal(t, "asc_n_b", notes[0].ID)
	assert.Equal(t, "asc_n_a", notes[1].ID)

	// ListLocalTimeline も同様 (timelineFilter経路)。
	filter := model.TimelineDBFilter{ViewerID: user.ID}
	notes, err = repo.ListLocalTimeline(10, "asc_n_a", "", filter)
	require.NoError(t, err)
	// 関係する3件の中で b, c が order ASC で返ること (他ユーザーのpublicノートも混ざる可能性あり)
	ownOnly := make([]*model.Note, 0, 2)
	for _, n := range notes {
		if strings.HasPrefix(n.ID, "asc_n_") {
			ownOnly = append(ownOnly, n)
		}
	}
	require.Len(t, ownOnly, 2)
	assert.Equal(t, "asc_n_b", ownOnly[0].ID)
	assert.Equal(t, "asc_n_c", ownOnly[1].ID)
}

// #403: nodeinfo usage 用 count clauseを実 PostgreSQL で検証する。
func TestNoteRepository_CountLocalNotes_CountLocalComments(t *testing.T) {
	repo := NewNoteRepository(testDB)
	user := insertTestUser(t, "n_cnt_u", "cntuser")
	defer cleanupUser(t, user.ID)

	// local post 1本
	post := &model.Note{ID: "cnt_post1", UserID: user.ID, Visibility: "public"}
	require.NoError(t, testDB.Create(post).Error)
	defer testDB.Exec(`DELETE FROM "note" WHERE id = ?`, post.ID)

	// local reply 1本
	replyID := "cnt_post1"
	reply := &model.Note{ID: "cnt_reply1", UserID: user.ID, Visibility: "public", ReplyID: &replyID, ReplyUserID: &user.ID}
	require.NoError(t, testDB.Create(reply).Error)
	defer testDB.Exec(`DELETE FROM "note" WHERE id = ?`, reply.ID)

	// remote note (count 対象外)
	remoteHost := "remote.example"
	remote := &model.Note{ID: "cnt_remote1", UserID: user.ID, Visibility: "public", UserHost: &remoteHost}
	require.NoError(t, testDB.Create(remote).Error)
	defer testDB.Exec(`DELETE FROM "note" WHERE id = ?`, remote.ID)

	// fixture 追加前の数との delta を確認する (他 test fixture 残存があり得る)。
	posts, err := repo.CountLocalNotes()
	require.NoError(t, err)
	assert.GreaterOrEqual(t, posts, int64(2)) // local post + reply

	comments, err := repo.CountLocalComments()
	require.NoError(t, err)
	assert.GreaterOrEqual(t, comments, int64(1)) // reply だけ
}

func TestNoteRepository_CountLocal_Error(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	repo := NewNoteRepository(testDB.WithContext(ctx))
	_, err := repo.CountLocalNotes()
	assert.Error(t, err)
	_, err = repo.CountLocalComments()
	assert.Error(t, err)
}
