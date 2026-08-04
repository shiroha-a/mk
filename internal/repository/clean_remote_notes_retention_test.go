package repository

import (
	"testing"

	"github.com/shiroha-a/mk/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
)

// TestNoteRepository_DeleteExpiredRemoteNotes_Retention covers the retention
// guards ported from upstream CleanRemoteNotesProcessorService (#2329).
//
// 期限切れのリモートノートでも、ローカルユーザーが手を加えたものは消さない。
// これが無いと、クリップに入れた・ピン留めした・お気に入りにした投稿が本人の
// 操作と無関係に消える。
func TestNoteRepository_DeleteExpiredRemoteNotes_Retention(t *testing.T) {
	nr := NewNoteRepository(testDB)
	host := "retention.example"

	remoteUser := &model.User{
		ID: "rtn_remote", Username: "rtn_remote", UsernameLower: "rtn_remote",
		Host: &host, AvatarDecorations: datatypes.JSON([]byte("[]")),
	}
	require.NoError(t, testDB.Create(remoteUser).Error)
	defer cleanupUser(t, remoteUser.ID)

	localUser := &model.User{
		ID: "rtn_local", Username: "rtn_local", UsernameLower: "rtn_local",
		AvatarDecorations: datatypes.JSON([]byte("[]")),
	}
	require.NoError(t, testDB.Create(localUser).Error)
	defer cleanupUser(t, localUser.ID)

	// aidx の先頭 8 文字が時刻。"00000000" は 2000-01-01 なので必ず期限切れ。
	newOldNote := func(t *testing.T, suffix string) *model.Note {
		t.Helper()
		n := &model.Note{
			ID: "00000000" + suffix, UserID: remoteUser.ID, UserHost: &host,
			Visibility: model.NoteVisibilityPublic, Reactions: datatypes.JSON([]byte("{}")),
		}
		require.NoError(t, nr.Create(n))
		t.Cleanup(func() { cleanupNote(t, n.ID) })
		return n
	}

	cases := []struct {
		name string
		// protect attaches the row that must keep the note alive. nil = 対照群。
		protect  func(t *testing.T, noteID string)
		survives bool
	}{
		{
			name:     "何も付いていない期限切れノートは削除される",
			protect:  nil,
			survives: false,
		},
		{
			// upstream は clippedCount で判定するが mk-go はカウンタを維持しない
			// ため (#2243)、clip_note を直接見ないとクリップを保護できない。
			name: "クリップに入っているノートは残る",
			protect: func(t *testing.T, noteID string) {
				clip := &model.Clip{ID: "rtn_clip_" + noteID[8:], UserID: localUser.ID, Name: "c"}
				require.NoError(t, testDB.Create(clip).Error)
				t.Cleanup(func() { testDB.Delete(&model.Clip{}, "id = ?", clip.ID) })
				cn := &model.ClipNote{ID: "rtn_cn_" + noteID[8:], NoteID: noteID, ClipID: clip.ID}
				require.NoError(t, testDB.Create(cn).Error)
				t.Cleanup(func() { testDB.Delete(&model.ClipNote{}, "id = ?", cn.ID) })
			},
			survives: true,
		},
		{
			name: "プロフィールにピン留めされたノートは残る",
			protect: func(t *testing.T, noteID string) {
				p := &model.UserNotePining{ID: "rtn_pin_" + noteID[8:], UserID: localUser.ID, NoteID: noteID}
				require.NoError(t, testDB.Create(p).Error)
				t.Cleanup(func() { testDB.Delete(&model.UserNotePining{}, "id = ?", p.ID) })
			},
			survives: true,
		},
		{
			name: "お気に入りに入っているノートは残る",
			protect: func(t *testing.T, noteID string) {
				f := &model.NoteFavorite{ID: "rtn_fav_" + noteID[8:], UserID: localUser.ID, NoteID: noteID}
				require.NoError(t, testDB.Create(f).Error)
				t.Cleanup(func() { testDB.Delete(&model.NoteFavorite{}, "id = ?", f.ID) })
			},
			survives: true,
		},
		{
			name: "ローカルユーザーがリアクションしたノートは残る",
			protect: func(t *testing.T, noteID string) {
				r := &model.NoteReaction{ID: "rtn_rx_" + noteID[8:], UserID: localUser.ID, NoteID: noteID, Reaction: "👍"}
				require.NoError(t, testDB.Create(r).Error)
				t.Cleanup(func() { testDB.Delete(&model.NoteReaction{}, "id = ?", r.ID) })
			},
			survives: true,
		},
		{
			// リモートユーザーのリアクションは保護しない (upstream の
			// `"user"."host" IS NULL` 条件)。連合先の反応で無限に溜まるため。
			name: "リモートユーザーのリアクションだけなら削除される",
			protect: func(t *testing.T, noteID string) {
				r := &model.NoteReaction{ID: "rtn_rrx_" + noteID[8:], UserID: remoteUser.ID, NoteID: noteID, Reaction: "👍"}
				require.NoError(t, testDB.Create(r).Error)
				t.Cleanup(func() { testDB.Delete(&model.NoteReaction{}, "id = ?", r.ID) })
			},
			survives: false,
		},
	}

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			note := newOldNote(t, "_rtn_"+string(rune('a'+i)))
			if tc.protect != nil {
				tc.protect(t, note.ID)
			}

			_, err := nr.DeleteExpiredRemoteNotes(1, 100)
			require.NoError(t, err)

			_, ferr := nr.FindByID(note.ID)
			if tc.survives {
				assert.NoError(t, ferr, "保護されたノートが削除された")
			} else {
				assert.Error(t, ferr, "保護対象でないノートが残っている")
			}
		})
	}
}

// TestNoteRepository_DeleteExpiredRemoteNotes_CountersBlockDeletion covers the
// upstream `clippedCount = 0` / `pageCount = 0` conditions. mk-go はこれらの
// カウンタを維持しないが、TS から切り戻したインスタンスでは値が入っているので
// 条件は残してある。
func TestNoteRepository_DeleteExpiredRemoteNotes_CountersBlockDeletion(t *testing.T) {
	nr := NewNoteRepository(testDB)
	host := "counters.example"

	u := &model.User{
		ID: "ctr_remote", Username: "ctr_remote", UsernameLower: "ctr_remote",
		Host: &host, AvatarDecorations: datatypes.JSON([]byte("[]")),
	}
	require.NoError(t, testDB.Create(u).Error)
	defer cleanupUser(t, u.ID)

	for _, tc := range []struct {
		name   string
		column string
	}{
		{"clippedCount が立っているノートは残る", "clippedCount"},
		{"pageCount が立っているノートは残る", "pageCount"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := &model.Note{
				ID: "00000000_ctr_" + tc.column[:3], UserID: u.ID, UserHost: &host,
				Visibility: model.NoteVisibilityPublic, Reactions: datatypes.JSON([]byte("{}")),
			}
			require.NoError(t, nr.Create(n))
			defer cleanupNote(t, n.ID)
			require.NoError(t, testDB.Exec(`UPDATE "note" SET "`+tc.column+`" = 1 WHERE id = ?`, n.ID).Error)

			_, err := nr.DeleteExpiredRemoteNotes(1, 100)
			require.NoError(t, err)

			_, ferr := nr.FindByID(n.ID)
			assert.NoError(t, ferr, "カウンタが立っているノートが削除された")
		})
	}
}
