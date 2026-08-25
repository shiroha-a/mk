package federation_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shiroha-a/mk/internal/activitypub"
	"github.com/shiroha-a/mk/internal/core/federation"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/testutil"
)

// fkOnceDriveRepo fails the first Create with a foreign_key_violation when the
// row carries a userId, mirroring `drive_file_userId_fkey` against an author
// that was never materialized.
type fkOnceDriveRepo struct {
	*testutil.MockDriveFileRepository
	attempts []*model.DriveFile
}

func (c *fkOnceDriveRepo) Create(f *model.DriveFile) error {
	// 呼び出しごとの値を記録する (retry で userId が落ちたか見るため)。
	snap := *f
	c.attempts = append(c.attempts, &snap)
	if f.UserID != nil {
		return &pgconn.PgError{Code: "23503", Message: `insert or update on table "drive_file" violates foreign key constraint "drive_file_userId_fkey"`}
	}
	return c.MockDriveFileRepository.Create(f)
}

// 代替テキストが長い添付が保存されること (#2717)。
//
// Mastodon は AP の `attachment[].name` に**代替テキスト**を入れてくる。
// `drive_file.name` は varchar(256) なので、切らずに入れると 22001 で落ちて
// **その添付が丸ごと保存されない**。
func TestUpsertAttachments_TruncatesLongAltText(t *testing.T) {
	drive := testutil.NewMockDriveFileRepository()
	idGen, err := id.NewGenerator("aidx")
	require.NoError(t, err)
	r := federation.NewResolver(testutil.NewMockUserRepository(), testutil.NewMockNoteRepository(),
		activitypub.NewURLBuilder("https://example.com"), &stubFetcher{}, idGen)
	r.SetDriveFileRepo(drive)

	// 全角で 700 文字。byte で切ると壊れた UTF-8 になる形にしておく。
	// **先頭と末尾を別の文字にする。** 同じ文字の繰り返しだと「末尾から切る」
	// 実装でも prefix 判定が通ってしまう (#2721 review LOW-2)。
	alt := "さき" + strings.Repeat("あ", 700) + "おわり"
	userID, host := "ru", "remote.example"
	ids := r.UpsertAttachments([]activitypub.Document{{
		URL: "https://media.example/files/long.mp4", MediaType: "video/mp4", Name: alt,
	}}, &userID, &host)

	require.Len(t, ids, 1, "長い alt text で添付が落ちている")
	f := drive.Files[ids[0]]
	require.NotNil(t, f)
	assert.Equal(t, 256, len([]rune(f.Name)), "name が列の上限 (rune) で切られていない")
	assert.True(t, strings.HasPrefix(f.Name, "さき"), "先頭が落ちている")
	assert.True(t, utf8.ValidString(f.Name), "rune 境界で切れていない")
	// **先頭一致も見る。** 長さと UTF-8 妥当性だけだと「末尾から切る」実装を
	// 見逃す (#2721 review LOW-2)。
	assert.True(t, strings.HasPrefix(alt, f.Name), "先頭から切っていない")
	assert.True(t, strings.HasPrefix(alt, *f.Comment), "comment も先頭から切っていない")
	require.NotNil(t, f.Comment)
	assert.Equal(t, 512, len([]rune(*f.Comment)), "comment が列の上限で切られていない")
}

// 著者が materialize されていない (= user 行が無い) 添付でも保存されること
// (#2717)。
//
// リレー由来の投稿は著者の user 行を作らない (#2332) ので `drive_file.userId` の
// FK に当たる。ここで諦めると**その添付が表示されなくなる** — 添付は pack 時に
// DB から引くため、ephemeral note でも行が要る。
func TestUpsertAttachments_FallsBackToUnownedOnForeignKeyViolation(t *testing.T) {
	drive := &fkOnceDriveRepo{MockDriveFileRepository: testutil.NewMockDriveFileRepository()}
	idGen, err := id.NewGenerator("aidx")
	require.NoError(t, err)
	r := federation.NewResolver(testutil.NewMockUserRepository(), testutil.NewMockNoteRepository(),
		activitypub.NewURLBuilder("https://example.com"), &stubFetcher{}, idGen)
	r.SetDriveFileRepo(drive)

	userID, host := "ghost", "remote.example"
	ids := r.UpsertAttachments([]activitypub.Document{{
		URL: "https://media.example/files/a.webp", MediaType: "image/webp",
	}}, &userID, &host)

	require.Len(t, ids, 1, "FK 違反で添付が落ちている")
	require.Len(t, drive.attempts, 2, "owner 無しで作り直していない")
	require.NotNil(t, drive.attempts[0].UserID)
	assert.Nil(t, drive.attempts[1].UserID, "retry でも userId を付けている")

	saved := drive.Files[ids[0]]
	require.NotNil(t, saved)
	assert.Nil(t, saved.UserID)
	require.NotNil(t, saved.UserHost)
	assert.Equal(t, host, *saved.UserHost, "userHost は残すこと (instance 単位の purge が効く)")
}

// FK 違反**以外**では owner を落として作り直さないこと (#2717)。
//
// error 種別を見ずに retry すると、たまたま owner 無しなら通る別の失敗
// (列長超過など) で**所有者情報を黙って捨てる**。ここは「userId があると失敗し、
// 無ければ成功する」repo を置いて、retry していないことを見る。
type nonFKOnOwnerDriveRepo struct {
	*testutil.MockDriveFileRepository
	attempts int
}

func (c *nonFKOnOwnerDriveRepo) Create(f *model.DriveFile) error {
	c.attempts++
	if f.UserID != nil {
		return &pgconn.PgError{Code: "22001", Message: "value too long"}
	}
	return c.MockDriveFileRepository.Create(f)
}

func TestUpsertAttachments_OtherErrorsDoNotDropOwner(t *testing.T) {
	drive := &nonFKOnOwnerDriveRepo{MockDriveFileRepository: testutil.NewMockDriveFileRepository()}
	idGen, err := id.NewGenerator("aidx")
	require.NoError(t, err)
	r := federation.NewResolver(testutil.NewMockUserRepository(), testutil.NewMockNoteRepository(),
		activitypub.NewURLBuilder("https://example.com"), &stubFetcher{}, idGen)
	r.SetDriveFileRepo(drive)

	userID, host := "ru", "remote.example"
	ids := r.UpsertAttachments([]activitypub.Document{{
		URL: "https://media.example/files/b.webp", MediaType: "image/webp",
	}}, &userID, &host)

	assert.Empty(t, ids, "FK 違反でないのに owner を落として作り直している")
	assert.Equal(t, 1, drive.attempts, "retry してはいけない")
}

// NUL を含む alt text でも添付が落ちないこと (#2721 review MEDIUM-1)。
//
// 長さだけ直しても、制御文字が混じると PostgreSQL が 22021 で弾いて同じく
// 添付が丸ごと消える。
func TestUpsertAttachments_StripsNULFromAltText(t *testing.T) {
	drive := testutil.NewMockDriveFileRepository()
	idGen, err := id.NewGenerator("aidx")
	require.NoError(t, err)
	r := federation.NewResolver(testutil.NewMockUserRepository(), testutil.NewMockNoteRepository(),
		activitypub.NewURLBuilder("https://example.com"), &stubFetcher{}, idGen)
	r.SetDriveFileRepo(drive)

	userID, host := "ru", "remote.example"
	ids := r.UpsertAttachments([]activitypub.Document{{
		URL: "https://media.example/files/nul.webp", MediaType: "image/webp", Name: "al\x00t",
	}}, &userID, &host)

	require.Len(t, ids, 1)
	f := drive.Files[ids[0]]
	require.NotNil(t, f)
	assert.NotContains(t, f.Name, "\x00", "NUL が残っている")
	require.NotNil(t, f.Comment)
	assert.NotContains(t, *f.Comment, "\x00")
}
