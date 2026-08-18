package repository

import (
	"testing"

	"github.com/shiroha-a/mk/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #2623: note の自己参照 FK (renoteId / replyId) を落としたので、元ノートが
// 削除されてもリノート / 返信側の参照は NULL 化されない。
//
// FK が `ON DELETE SET NULL` だった頃は renoteId ごと消え、renoteUserId だけが
// 残る「本文も引用先も無いノート」になっていた。frontend は renoteId が残って
// いれば「削除されたノート」として描画できる (MkNote.vue) ので、upstream と
// 同じく参照を残すのが正しい。
func TestNoteRepository_SelfReferenceSurvivesTargetDeletion(t *testing.T) {
	user := insertTestUser(t, "rnfk_u", "rnfkuser")
	defer cleanupUser(t, user.ID)

	targetID := "rnfk_target"
	userID := user.ID

	target := &model.Note{ID: targetID, UserID: user.ID, Visibility: "public"}
	require.NoError(t, testDB.Create(target).Error)

	renote := &model.Note{
		ID: "rnfk_renote", UserID: user.ID, Visibility: "public",
		RenoteID: &targetID, RenoteUserID: &userID,
	}
	reply := &model.Note{
		ID: "rnfk_reply", UserID: user.ID, Visibility: "public",
		ReplyID: &targetID, ReplyUserID: &userID,
	}
	for _, n := range []*model.Note{renote, reply} {
		require.NoError(t, testDB.Create(n).Error)
		defer testDB.Exec(`DELETE FROM "note" WHERE id = ?`, n.ID)
	}

	// 対象ノートを消す。FK が残っていればここで renoteId / replyId が NULL 化される。
	require.NoError(t, testDB.Exec(`DELETE FROM "note" WHERE id = ?`, targetID).Error)

	var gotRenote model.Note
	require.NoError(t, testDB.First(&gotRenote, "id = ?", renote.ID).Error)
	require.NotNil(t, gotRenote.RenoteID, "FK を落としたので renoteId は NULL 化されない")
	assert.Equal(t, targetID, *gotRenote.RenoteID)

	var gotReply model.Note
	require.NoError(t, testDB.First(&gotReply, "id = ?", reply.ID).Error)
	require.NotNil(t, gotReply.ReplyID, "FK を落としたので replyId は NULL 化されない")
	assert.Equal(t, targetID, *gotReply.ReplyID)
}

// #2623: 孤児行を消す migration の条件が、中身のあるノートを巻き込まないこと。
//
// migration/000080 の DELETE と同じ条件をここで実行する。条件を緩めると
// 「リノート由来だが本文を持つノート (引用)」まで消えるので、その境界を固定する。
func TestNoteRepository_OrphanRenoteDeleteScope(t *testing.T) {
	user := insertTestUser(t, "orph_u", "orphuser")
	defer cleanupUser(t, user.ID)

	userID := user.ID
	someID := "orph_someid"
	text := "残す"
	cw := "cw"

	// 消えるべき: 元が pure renote で、対象が SET NULL で消えたもの。
	orphan := &model.Note{
		ID: "orph_target", UserID: user.ID, Visibility: "public",
		RenoteUserID: &userID,
	}
	// 消えないべきもの。
	quote := &model.Note{ // 本文がある (引用)
		ID: "orph_quote", UserID: user.ID, Visibility: "public",
		RenoteUserID: &userID, Text: &text,
	}
	withCW := &model.Note{ // CW がある
		ID: "orph_cw", UserID: user.ID, Visibility: "public",
		RenoteUserID: &userID, CW: &cw,
	}
	withFile := &model.Note{ // 添付がある
		ID: "orph_file", UserID: user.ID, Visibility: "public",
		RenoteUserID: &userID, FileIDs: []string{"f1"},
	}
	withPoll := &model.Note{ // 投票がある
		ID: "orph_poll", UserID: user.ID, Visibility: "public",
		RenoteUserID: &userID, HasPoll: true,
	}
	plain := &model.Note{ // そもそもリノートではない空ノート
		ID: "orph_plain", UserID: user.ID, Visibility: "public",
	}
	intact := &model.Note{ // 参照が生きているリノート
		ID: "orph_intact", UserID: user.ID, Visibility: "public",
		RenoteID: &someID, RenoteUserID: &userID,
	}
	// intact が指す先も必要 (FK は無いが、条件の意味を揃えるため実在させる)。
	base := &model.Note{ID: someID, UserID: user.ID, Visibility: "public", Text: &text}

	all := []*model.Note{base, orphan, quote, withCW, withFile, withPoll, plain, intact}
	for _, n := range all {
		require.NoError(t, testDB.Create(n).Error)
		defer testDB.Exec(`DELETE FROM "note" WHERE id = ?`, n.ID)
	}

	// migration/000080 と同じ条件。
	require.NoError(t, testDB.Exec(`
		DELETE FROM "note"
		WHERE "renoteId" IS NULL
		  AND "renoteUserId" IS NOT NULL
		  AND (text IS NULL OR text = '')
		  AND (cw IS NULL OR cw = '')
		  AND "replyId" IS NULL
		  AND ("fileIds" IS NULL OR "fileIds" = '{}')
		  AND "hasPoll" = false
		  AND "userId" = ?`, user.ID).Error)

	exists := func(id string) bool {
		var n int64
		require.NoError(t, testDB.Raw(`SELECT count(*) FROM "note" WHERE id = ?`, id).Scan(&n).Error)
		return n > 0
	}

	assert.False(t, exists(orphan.ID), "対象が消えた pure renote は削除される")
	assert.True(t, exists(quote.ID), "本文を持つ引用は残す")
	assert.True(t, exists(withCW.ID), "CW を持つノートは残す")
	assert.True(t, exists(withFile.ID), "添付を持つノートは残す")
	assert.True(t, exists(withPoll.ID), "投票を持つノートは残す")
	assert.True(t, exists(plain.ID), "リノートでない空ノートは巻き込まない")
	assert.True(t, exists(intact.ID), "参照が生きているリノートは残す")
	assert.True(t, exists(base.ID), "参照先は残す")
}
