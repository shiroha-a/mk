package repository

import (
	"testing"

	"github.com/shiroha-a/mk/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Misskey TS が作った DB には存在しない列。upstream は
// 1697420555911-deleteCreatedAt で app / auth_session の createdAt を DROP
// しており、clip.notesCount はそもそも Clip entity に無い。
//
// mk-go の migration はこれらを `CREATE TABLE IF NOT EXISTS` の中でしか定義
// しておらず、TS 製の既存テーブルに対しては no-op になる。したがって
// mk-go が列を読み書きすると drop-in 環境だけ
// `column "..." of relation "..." does not exist` で落ちる (#2243)。
//
// 本テストは当該列を実際に DROP して TS 製 DB を再現し、mk-go の書き込み /
// 読み出し経路が成立することを担保する。列を足す migration ではなく
// **列への依存を外す** 方針を取ったので、regression はここで検出する。
var tsMissingColumns = []struct{ table, column, ddl string }{
	{"app", "createdAt", `timestamp with time zone NOT NULL DEFAULT now()`},
	{"auth_session", "createdAt", `timestamp with time zone NOT NULL DEFAULT now()`},
	{"clip", "notesCount", `integer NOT NULL DEFAULT 0`},
}

// withTSShapedSchema drops the mk-go-only columns for the duration of fn and
// restores them afterwards so the shared test DB is left untouched.
func withTSShapedSchema(t *testing.T, fn func()) {
	t.Helper()
	for _, c := range tsMissingColumns {
		require.NoError(t, testDB.Exec(
			`ALTER TABLE "`+c.table+`" DROP COLUMN IF EXISTS "`+c.column+`"`).Error)
	}
	defer func() {
		for _, c := range tsMissingColumns {
			require.NoError(t, testDB.Exec(
				`ALTER TABLE "`+c.table+`" ADD COLUMN IF NOT EXISTS "`+c.column+`" `+c.ddl).Error)
		}
	}()
	fn()
}

func TestDropIn_TSShapedSchema_AppAndAuthSession(t *testing.T) {
	withTSShapedSchema(t, func() {
		repo := NewAuthSessionRepository(testDB)
		user := insertTestUser(t, "u_dropin_1", "dropinuser")
		defer cleanupUser(t, user.ID)

		app := &model.App{
			ID:          "app_dropin_1",
			UserID:      &user.ID,
			Secret:      "secret_dropin_1",
			Name:        "DropinApp",
			Description: "",
			Permission:  model.StringArray{"read:account"},
		}
		require.NoError(t, repo.CreateApp(app), "app INSERT must not reference createdAt")
		defer testDB.Exec(`DELETE FROM "app" WHERE id = ?`, app.ID)

		gotApp, err := repo.FindAppBySecret("secret_dropin_1")
		require.NoError(t, err)
		assert.Equal(t, "DropinApp", gotApp.Name)

		session := &model.AuthSession{
			ID:    "sess_dropin_1",
			Token: "token_dropin_1",
			AppID: app.ID,
		}
		require.NoError(t, repo.CreateSession(session), "auth_session INSERT must not reference createdAt")
		defer testDB.Exec(`DELETE FROM "auth_session" WHERE id = ?`, session.ID)

		gotSession, err := repo.FindSessionByToken("token_dropin_1")
		require.NoError(t, err)
		assert.Equal(t, app.ID, gotSession.AppID)
	})
}

func TestDropIn_TSShapedSchema_Clip(t *testing.T) {
	withTSShapedSchema(t, func() {
		clipRepo := NewClipRepository(testDB)
		clipNoteRepo := NewClipNoteRepository(testDB)
		user := insertTestUser(t, "u_dropin_2", "dropinclip")
		defer cleanupUser(t, user.ID)

		c := newTestClip("clp_dropin_1", user.ID, "dropin")
		require.NoError(t, clipRepo.Create(c), "clip INSERT must not reference notesCount")
		defer cleanupClip(t, c.ID)

		got, err := clipRepo.FindByID(c.ID)
		require.NoError(t, err)
		assert.Equal(t, "dropin", got.Name)

		// notesCount は clip_note の実カウントで出すので、列が無くても数えられる。
		n, err := clipNoteRepo.CountByClip(c.ID)
		require.NoError(t, err)
		assert.EqualValues(t, 0, n)

		note := insertTestNote(t, "n_dropin_1", user.ID)
		defer cleanupNote(t, note.ID)
		require.NoError(t, clipNoteRepo.Create(&model.ClipNote{
			ID: "cn_dropin_1", ClipID: c.ID, NoteID: note.ID,
		}))
		n, err = clipNoteRepo.CountByClip(c.ID)
		require.NoError(t, err)
		assert.EqualValues(t, 1, n)

		rows, err := clipRepo.ListByUser(user.ID, "", "", 10, 0)
		require.NoError(t, err)
		assert.Len(t, rows, 1, "clip SELECT must not reference notesCount")
	})
}
