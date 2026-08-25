package repository

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shiroha-a/mk/internal/model"
)

// resolver が切っている長さで、実際の列制約を通ること (#2717)。
//
// mock は列制約を持たないので、resolver 側のテストだけでは
// 「varchar(256) に入る長さか」を確かめられない。上限そのものは
// migration/000001_initial.up.sql が持っているので、実 DB で突き合わせる。
func TestDriveFile_ColumnLimitsAcceptTruncatedValues(t *testing.T) {
	repo := NewDriveFileRepository(testDB)
	id := "dfl_limits_0000000000000000000a"
	t.Cleanup(func() { testDB.Exec(`DELETE FROM "drive_file" WHERE id = ?`, id) })

	// resolver の driveFileNameMaxRunes / driveFileCommentMaxRunes と同じ値。
	// **全角で埋める** — byte で切る実装だと 3 倍になって入らない。
	name := strings.Repeat("あ", 256)
	comment := strings.Repeat("あ", 512)
	host := "remote.example"

	err := repo.Create(&model.DriveFile{
		ID: id, Name: name, Comment: &comment, Type: "image/webp",
		URL: "https://media.example/files/limits.webp", UserHost: &host,
		IsLink: true,
	})
	require.NoError(t, err, "resolver が切っている長さが列に入らない")

	got, err := repo.FindByID(id)
	require.NoError(t, err)
	assert.Equal(t, 256, len([]rune(got.Name)))
	require.NotNil(t, got.Comment)
	assert.Equal(t, 512, len([]rune(*got.Comment)))
}

// userId が NULL の行を作れること (#2717)。
//
// 著者が materialize されていない添付は owner 無しで保存する。列は
// `varchar(32) REFERENCES "user"("id") ON DELETE SET NULL` で NULL 可。
func TestDriveFile_AcceptsNullOwner(t *testing.T) {
	repo := NewDriveFileRepository(testDB)
	id := "dfl_noowner_000000000000000000a"
	t.Cleanup(func() { testDB.Exec(`DELETE FROM "drive_file" WHERE id = ?`, id) })

	host := "remote.example"
	require.NoError(t, repo.Create(&model.DriveFile{
		ID: id, Name: "file", Type: "image/webp",
		URL: "https://media.example/files/noowner.webp", UserHost: &host, IsLink: true,
	}))

	got, err := repo.FindByID(id)
	require.NoError(t, err)
	assert.Nil(t, got.UserID)
	require.NotNil(t, got.UserHost)
	assert.Equal(t, host, *got.UserHost)
}
