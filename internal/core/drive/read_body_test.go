package drive_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/shiroha-a/mk/internal/core/drive"
	"github.com/shiroha-a/mk/internal/model"
)

// **ストレージから実体を読めること (#2966)。** 承認時に絵文字の画像を
// system 所有へ複製するのに使う。HTTP で自分の公開 URL を叩く方式は、
// SSRF ガードと衝突し、非公開 URL の構成では取れず、DB 上の行と実体の対応も
// 確かめられないので採らない。
func TestReadFileBody(t *testing.T) {
	svc, _, _ := newSvc(t)
	want := []byte("emoji-bytes")
	f, err := svc.Upload(context.Background(), drive.UploadInput{Body: want, Name: "e.png"})
	require.NoError(t, err)

	got, err := svc.ReadFileBody(f, 1<<20)
	require.NoError(t, err)
	require.Equal(t, want, got)
}

// **上限を超える本体は読まない。** 上限なしにすると 1 リクエストで任意サイズを
// メモリに載せられる。
func TestReadFileBodyRejectsOversized(t *testing.T) {
	svc, _, _ := newSvc(t)
	f, err := svc.Upload(context.Background(), drive.UploadInput{Body: []byte("0123456789"), Name: "e.png"})
	require.NoError(t, err)

	_, err = svc.ReadFileBody(f, 4)
	require.Error(t, err, "上限を超える本体を読んでいる")
}

// 実体を持たない行は読めない (黙って空を返さない)。
func TestReadFileBodyWithoutBody(t *testing.T) {
	svc, _, _ := newSvc(t)
	key := "k"
	for _, tc := range []struct {
		name string
		f    *model.DriveFile
	}{
		{"nil", nil},
		{"リンク (accessKey だけ)", &model.DriveFile{IsLink: true, AccessKey: &key}},
		{"accessKey なし", &model.DriveFile{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.ReadFileBody(tc.f, 1<<20)
			require.ErrorIs(t, err, drive.ErrObjectNotFound)
		})
	}
}

// **実体があってもリンクは読まない。** `isLink` の行は URL を指しているだけで、
// accessKey にキャッシュが残っていても「そのファイルの本体」ではない。
// 実体の有無で結果が変わらない seed だと、ガードを外す変異が素通りする (実測)。
func TestReadFileBodyRefusesLinkWithStoredObject(t *testing.T) {
	svc, _, _ := newSvc(t)
	f, err := svc.Upload(context.Background(), drive.UploadInput{Body: []byte("cached"), Name: "e.png"})
	require.NoError(t, err)
	// 実体はそのままに、行だけリンク扱いにする。
	f.IsLink = true

	_, err = svc.ReadFileBody(f, 1<<20)
	require.ErrorIs(t, err, drive.ErrObjectNotFound, "リンクの行から本体を読んでいる")
}

// **system 所有のファイルを消せること (#2966)。** `Delete` は所有者チェックを
// 通すので、誰のものでもないファイルには使えない。
func TestDeleteSystemFile(t *testing.T) {
	svc, fileRepo, _ := newSvc(t)
	f, err := svc.Upload(context.Background(), drive.UploadInput{Body: []byte("x"), Name: "e.png"})
	require.NoError(t, err)
	require.Nil(t, f.UserID, "system 所有として作られていない")

	require.NoError(t, svc.DeleteSystemFile(f.ID))
	_, err = fileRepo.FindByID(f.ID)
	require.Error(t, err, "行が残っている")

	// 実体も消えていること。
	_, err = svc.ReadFileBody(f, 1<<20)
	require.Error(t, err, "ストレージに実体が残っている")
}

// **利用者のファイルは消さない。** 呼び出し側が id を取り違えたときに
// 申請者のファイルを消すのが最悪の壊れ方なので、ここで止める。
func TestDeleteSystemFileRefusesUserOwned(t *testing.T) {
	svc, fileRepo, _ := newSvc(t)
	owner := &model.User{ID: "u1", Username: "alice"}
	f, err := svc.Upload(context.Background(), drive.UploadInput{User: owner, Body: []byte("x"), Name: "e.png"})
	require.NoError(t, err)
	require.NotNil(t, f.UserID)

	require.Error(t, svc.DeleteSystemFile(f.ID), "利用者のファイルを消している")
	got, err := fileRepo.FindByID(f.ID)
	require.NoError(t, err)
	require.NotNil(t, got, "利用者のファイルが消えている")
}

// 存在しない id は成功として扱う (二重に呼ばれても壊れない)。
func TestDeleteSystemFileIsIdempotent(t *testing.T) {
	svc, _, _ := newSvc(t)
	require.NoError(t, svc.DeleteSystemFile("nope"))
	require.NoError(t, svc.DeleteSystemFile(""))
}
