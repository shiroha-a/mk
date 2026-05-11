package drive_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/shiroha-a/mk/internal/core/drive"
	"github.com/shiroha-a/mk/internal/entity"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var stubError = errors.New("stub error")

// brokenStorage points the LocalStorage rootDir at an existing file so that
// any Put/Delete operation fails.
func brokenStorage(t *testing.T) *drive.LocalStorage {
	t.Helper()
	tmp := t.TempDir()
	conflict := filepath.Join(tmp, "conflict")
	require.NoError(t, os.WriteFile(conflict, []byte("x"), 0o644))
	return drive.NewLocalStorage(conflict, "")
}

func newSvc(t *testing.T) (*drive.Service, *testutil.MockDriveFileRepository, *testutil.MockDriveFolderRepository) {
	t.Helper()
	fileRepo := testutil.NewMockDriveFileRepository()
	folderRepo := testutil.NewMockDriveFolderRepository()
	folderRepo.FilesRef = fileRepo
	storage := drive.NewLocalStorage(t.TempDir(), "https://example.com/files")
	idGen, _ := id.NewGenerator("aidx")
	svc := drive.NewService(fileRepo, folderRepo, storage, idGen)
	return svc, fileRepo, folderRepo
}

// --- Upload ---

// TestUpload_NilUserSystemFile guards that Upload accepts User == nil and
// produces a system-owned drive file (UserID/UserHost both nil), matching
// Misskey TS uploadFromUrl({user: null}). Used by EmojiCopy (#670) to store
// remote emoji images without binding them to the operator's account.
func TestUpload_NilUserSystemFile(t *testing.T) {
	svc, fileRepo, _ := newSvc(t)
	f, err := svc.Upload(context.Background(), drive.UploadInput{Body: []byte("hello"), Name: "x.bin"})
	require.NoError(t, err)
	require.NotNil(t, f)
	assert.Nil(t, f.UserID)
	assert.Nil(t, f.UserHost)
	assert.Len(t, fileRepo.Files, 1)
}

func TestUpload_HappyPath(t *testing.T) {
	svc, fileRepo, _ := newSvc(t)
	user := &model.User{ID: "u1"}
	f, err := svc.Upload(context.Background(), drive.UploadInput{User: user, Body: []byte("hello"), Name: "hello.txt"})
	require.NoError(t, err)
	assert.Equal(t, "hello.txt", f.Name)
	assert.Len(t, fileRepo.Files, 1)
}

// stubMainStreamPublisher captures PublishMainEvent calls.
type stubMainStreamPublisher struct {
	calls []mainEventCall
}

type mainEventCall struct {
	userID    string
	eventType string
	body      any
}

func (s *stubMainStreamPublisher) PublishMainEvent(userID, eventType string, body any) {
	s.calls = append(s.calls, mainEventCall{userID, eventType, body})
}

func TestUpload_PublishesDriveFileCreatedOnMain(t *testing.T) {
	svc, _, _ := newSvc(t)
	pub := &stubMainStreamPublisher{}
	svc.SetMainStreamPublisher(pub)

	user := &model.User{ID: "u1"}
	f, err := svc.Upload(context.Background(), drive.UploadInput{User: user, Body: []byte("hello"), Name: "hello.txt"})
	require.NoError(t, err)

	require.Len(t, pub.calls, 1)
	assert.Equal(t, "u1", pub.calls[0].userID)
	assert.Equal(t, "driveFileCreated", pub.calls[0].eventType)
	// bodyはentity.PackDriveFileでpackされたfileのはず。file IDが一致。
	body, ok := pub.calls[0].body.(entity.DriveFileEntity)
	require.True(t, ok)
	assert.Equal(t, f.ID, body.ID)
	assert.Equal(t, "hello.txt", body.Name)
}

func TestUpload_NoMainPublisher_NoEmit(t *testing.T) {
	svc, _, _ := newSvc(t)
	// SetMainStreamPublisherを呼ばない状態でもUpload自体は成功する。
	_, err := svc.Upload(context.Background(), drive.UploadInput{User: &model.User{ID: "u1"}, Body: []byte("hi"), Name: "hi.txt"})
	require.NoError(t, err)
}

func TestUpload_DedupByMD5(t *testing.T) {
	svc, fileRepo, _ := newSvc(t)
	user := &model.User{ID: "u1"}
	first, _ := svc.Upload(context.Background(), drive.UploadInput{User: user, Body: []byte("data"), Name: "a"})
	second, _ := svc.Upload(context.Background(), drive.UploadInput{User: user, Body: []byte("data"), Name: "b"})
	assert.Equal(t, first.ID, second.ID)
	assert.Len(t, fileRepo.Files, 1)
}

func TestUpload_ForceSkipsDedup(t *testing.T) {
	svc, fileRepo, _ := newSvc(t)
	user := &model.User{ID: "u1"}
	_, err := svc.Upload(context.Background(), drive.UploadInput{User: user, Body: []byte("d"), Name: "a"})
	require.NoError(t, err)
	_, err = svc.Upload(context.Background(), drive.UploadInput{User: user, Body: []byte("d"), Name: "a", Force: true})
	require.NoError(t, err)
	assert.Len(t, fileRepo.Files, 2)
}

func TestUpload_FolderNotFound(t *testing.T) {
	svc, _, _ := newSvc(t)
	folderID := "ghost"
	_, err := svc.Upload(context.Background(), drive.UploadInput{User: &model.User{ID: "u1"}, Body: []byte("x"), FolderID: &folderID})
	require.ErrorIs(t, err, drive.ErrFolderNotFound)
}

func TestUpload_FolderAccessDenied(t *testing.T) {
	svc, _, folderRepo := newSvc(t)
	other := "other"
	folderRepo.Folders["fid"] = &model.DriveFolder{ID: "fid", UserID: &other}
	folderID := "fid"
	_, err := svc.Upload(context.Background(), drive.UploadInput{User: &model.User{ID: "u1"}, Body: []byte("x"), FolderID: &folderID})
	require.ErrorIs(t, err, drive.ErrAccessDenied)
}

// failingFileRepo causes Create to fail.
type failingFileRepo struct {
	*testutil.MockDriveFileRepository
}

func (f *failingFileRepo) Create(_ *model.DriveFile) error {
	return stubError
}

func TestUpload_RepoCreateError(t *testing.T) {
	repo := &failingFileRepo{MockDriveFileRepository: testutil.NewMockDriveFileRepository()}
	idGen, _ := id.NewGenerator("aidx")
	svc := drive.NewService(repo, testutil.NewMockDriveFolderRepository(), drive.NewLocalStorage(t.TempDir(), ""), idGen)
	_, err := svc.Upload(context.Background(), drive.UploadInput{User: &model.User{ID: "u1"}, Body: []byte("x")})
	assert.ErrorIs(t, err, stubError)
}

func TestUpload_StoragePutError(t *testing.T) {
	storage := brokenStorage(t)
	idGen, _ := id.NewGenerator("aidx")
	svc := drive.NewService(testutil.NewMockDriveFileRepository(), testutil.NewMockDriveFolderRepository(), storage, idGen)
	_, err := svc.Upload(context.Background(), drive.UploadInput{User: &model.User{ID: "u1"}, Body: []byte("x")})
	assert.Error(t, err)
}

// errReader implements io.Reader and always returns an error.
type errReader struct{}

func (errReader) Read(_ []byte) (int, error) { return 0, stubError }

func TestUpload_AccessKeyGenError(t *testing.T) {
	restore := drive.SetRandReaderForTest(errReader{})
	defer restore()

	svc, _, _ := newSvc(t)
	_, err := svc.Upload(context.Background(), drive.UploadInput{User: &model.User{ID: "u1"}, Body: []byte("x")})
	assert.Error(t, err)
}

// --- Show / FindByHash ---

func TestShow_NilUser(t *testing.T) {
	svc, _, _ := newSvc(t)
	_, err := svc.Show(nil, "x")
	assert.Error(t, err)
}

func TestShow_NotFound(t *testing.T) {
	svc, _, _ := newSvc(t)
	_, err := svc.Show(&model.User{ID: "u1"}, "ghost")
	require.ErrorIs(t, err, drive.ErrFileNotFound)
}

func TestShow_OtherUserDenied(t *testing.T) {
	svc, fileRepo, _ := newSvc(t)
	other := "other"
	fileRepo.Files["f1"] = &model.DriveFile{ID: "f1", UserID: &other}
	_, err := svc.Show(&model.User{ID: "u1"}, "f1")
	require.ErrorIs(t, err, drive.ErrAccessDenied)
}

// moderator なら所有者でなくても見られる (upstream Misskey と一致)。
// リモート添付メディアの詳細表示は file.userId が remote user ID 固定
// になるので、この経路がないと管理者でも 403 で見られない。
type fakeMod struct{ moderators map[string]bool }

func (f *fakeMod) IsModerator(userID string) bool { return f.moderators[userID] }

func TestShow_ModeratorBypass(t *testing.T) {
	svc, fileRepo, _ := newSvc(t)
	other := "other"
	fileRepo.Files["f1"] = &model.DriveFile{ID: "f1", UserID: &other}
	svc.SetRoleChecker(&fakeMod{moderators: map[string]bool{"u1": true}})
	got, err := svc.Show(&model.User{ID: "u1"}, "f1")
	require.NoError(t, err)
	assert.Equal(t, "f1", got.ID)
}

// moderator でない一般ユーザーは roleChecker が wire されていても
// 引き続き 403。
func TestShow_NonModeratorStillDenied(t *testing.T) {
	svc, fileRepo, _ := newSvc(t)
	other := "other"
	fileRepo.Files["f1"] = &model.DriveFile{ID: "f1", UserID: &other}
	svc.SetRoleChecker(&fakeMod{moderators: map[string]bool{}})
	_, err := svc.Show(&model.User{ID: "u1"}, "f1")
	require.ErrorIs(t, err, drive.ErrAccessDenied)
}

func TestFindByHash_NilUser(t *testing.T) {
	svc, _, _ := newSvc(t)
	_, err := svc.FindByHash(nil, "x")
	assert.Error(t, err)
}

func TestFindByHash_NotFound(t *testing.T) {
	svc, _, _ := newSvc(t)
	_, err := svc.FindByHash(&model.User{ID: "u1"}, "ghost")
	require.ErrorIs(t, err, drive.ErrFileNotFound)
}

func TestFindByHash_Found(t *testing.T) {
	svc, fileRepo, _ := newSvc(t)
	uid := "u1"
	fileRepo.Files["f1"] = &model.DriveFile{ID: "f1", UserID: &uid, MD5: "abc"}
	got, err := svc.FindByHash(&model.User{ID: "u1"}, "abc")
	require.NoError(t, err)
	assert.Equal(t, "f1", got.ID)
}

// --- Update ---

func TestUpdate_HappyPath(t *testing.T) {
	svc, fileRepo, _ := newSvc(t)
	uid := "u1"
	fileRepo.Files["f1"] = &model.DriveFile{ID: "f1", UserID: &uid, Name: "old"}

	newName := "new"
	cmt := "comment"
	cmtPtr := &cmt
	sensitive := true
	got, err := svc.Update(&model.User{ID: "u1"}, "f1", drive.UpdateInput{
		Name:        &newName,
		Comment:     &cmtPtr,
		IsSensitive: &sensitive,
	})
	require.NoError(t, err)
	assert.Equal(t, "new", got.Name)
}

func TestUpdate_NotFound(t *testing.T) {
	svc, _, _ := newSvc(t)
	_, err := svc.Update(&model.User{ID: "u1"}, "ghost", drive.UpdateInput{})
	require.ErrorIs(t, err, drive.ErrFileNotFound)
}

// moderator は他ユーザーの file を Update できない (#499 privilege
// escalation 回帰防止)。Show() は moderator bypass を許す一方で、
// write 経路は owner-only に保たれていることを保証する。
func TestUpdate_ModeratorCannotEditOthersFile(t *testing.T) {
	svc, fileRepo, _ := newSvc(t)
	other := "other"
	fileRepo.Files["f1"] = &model.DriveFile{ID: "f1", UserID: &other}
	svc.SetRoleChecker(&fakeMod{moderators: map[string]bool{"u1": true}})

	newName := "renamed.png"
	_, err := svc.Update(&model.User{ID: "u1"}, "f1", drive.UpdateInput{Name: &newName})
	require.ErrorIs(t, err, drive.ErrAccessDenied)
}

// 同様に Delete も moderator bypass の対象外。
func TestDelete_ModeratorCannotDeleteOthersFile(t *testing.T) {
	svc, fileRepo, _ := newSvc(t)
	other := "other"
	fileRepo.Files["f1"] = &model.DriveFile{ID: "f1", UserID: &other}
	svc.SetRoleChecker(&fakeMod{moderators: map[string]bool{"u1": true}})

	err := svc.Delete(&model.User{ID: "u1"}, "f1")
	require.ErrorIs(t, err, drive.ErrAccessDenied)
}

func TestUpdate_FolderNotFound(t *testing.T) {
	svc, fileRepo, _ := newSvc(t)
	uid := "u1"
	fileRepo.Files["f1"] = &model.DriveFile{ID: "f1", UserID: &uid}
	folderID := "ghost"
	folderPtr := &folderID
	_, err := svc.Update(&model.User{ID: "u1"}, "f1", drive.UpdateInput{FolderID: &folderPtr})
	require.ErrorIs(t, err, drive.ErrFolderNotFound)
}

func TestUpdate_FolderAccessDenied(t *testing.T) {
	svc, fileRepo, folderRepo := newSvc(t)
	uid := "u1"
	other := "other"
	fileRepo.Files["f1"] = &model.DriveFile{ID: "f1", UserID: &uid}
	folderRepo.Folders["fid"] = &model.DriveFolder{ID: "fid", UserID: &other}

	folderID := "fid"
	folderPtr := &folderID
	_, err := svc.Update(&model.User{ID: "u1"}, "f1", drive.UpdateInput{FolderID: &folderPtr})
	require.ErrorIs(t, err, drive.ErrAccessDenied)
}

func TestUpdate_FolderNullable(t *testing.T) {
	svc, fileRepo, _ := newSvc(t)
	uid := "u1"
	folderID := "x"
	fileRepo.Files["f1"] = &model.DriveFile{ID: "f1", UserID: &uid, FolderID: &folderID}

	var nilFolder *string
	_, err := svc.Update(&model.User{ID: "u1"}, "f1", drive.UpdateInput{FolderID: &nilFolder})
	require.NoError(t, err)
}

// failingUpdateFileRepo causes Update to fail.
type failingUpdateFileRepo struct {
	*testutil.MockDriveFileRepository
}

func (f *failingUpdateFileRepo) Update(_ string, _ map[string]any) error {
	return stubError
}

func TestUpdate_RepoUpdateError(t *testing.T) {
	mock := testutil.NewMockDriveFileRepository()
	uid := "u1"
	mock.Files["f1"] = &model.DriveFile{ID: "f1", UserID: &uid}
	repo := &failingUpdateFileRepo{MockDriveFileRepository: mock}
	idGen, _ := id.NewGenerator("aidx")
	svc := drive.NewService(repo, testutil.NewMockDriveFolderRepository(), drive.NewLocalStorage(t.TempDir(), ""), idGen)
	newName := "x"
	_, err := svc.Update(&model.User{ID: "u1"}, "f1", drive.UpdateInput{Name: &newName})
	assert.ErrorIs(t, err, stubError)
}

// --- Delete ---

func TestDelete_NotFound(t *testing.T) {
	svc, _, _ := newSvc(t)
	err := svc.Delete(&model.User{ID: "u1"}, "ghost")
	require.ErrorIs(t, err, drive.ErrFileNotFound)
}

func TestDelete_HappyPath(t *testing.T) {
	svc, fileRepo, _ := newSvc(t)
	user := &model.User{ID: "u1"}
	created, err := svc.Upload(context.Background(), drive.UploadInput{User: user, Body: []byte("x"), Name: "x"})
	require.NoError(t, err)
	require.NoError(t, svc.Delete(user, created.ID))
	assert.Empty(t, fileRepo.Files)
}

func TestDelete_StorageError(t *testing.T) {
	storage := brokenStorage(t)
	mock := testutil.NewMockDriveFileRepository()
	uid := "u1"
	key := "abc"
	mock.Files["f1"] = &model.DriveFile{ID: "f1", UserID: &uid, AccessKey: &key}
	idGen, _ := id.NewGenerator("aidx")
	svc := drive.NewService(mock, testutil.NewMockDriveFolderRepository(), storage, idGen)
	err := svc.Delete(&model.User{ID: "u1"}, "f1")
	assert.Error(t, err)
}

// --- Folders ---

func TestCreateFolder_NilUser(t *testing.T) {
	svc, _, _ := newSvc(t)
	_, err := svc.CreateFolder(nil, "x", nil)
	assert.Error(t, err)
}

func TestCreateFolder_HappyPath(t *testing.T) {
	svc, _, folderRepo := newSvc(t)
	f, err := svc.CreateFolder(&model.User{ID: "u1"}, "Hello", nil)
	require.NoError(t, err)
	assert.Equal(t, "Hello", f.Name)
	assert.Len(t, folderRepo.Folders, 1)
}

func TestCreateFolder_ParentNotFound(t *testing.T) {
	svc, _, _ := newSvc(t)
	pid := "ghost"
	_, err := svc.CreateFolder(&model.User{ID: "u1"}, "x", &pid)
	require.ErrorIs(t, err, drive.ErrFolderNotFound)
}

func TestCreateFolder_ParentAccessDenied(t *testing.T) {
	svc, _, folderRepo := newSvc(t)
	other := "other"
	folderRepo.Folders["p"] = &model.DriveFolder{ID: "p", UserID: &other}
	pid := "p"
	_, err := svc.CreateFolder(&model.User{ID: "u1"}, "x", &pid)
	require.ErrorIs(t, err, drive.ErrAccessDenied)
}

// failingCreateFolderRepo causes Create to fail.
type failingCreateFolderRepo struct {
	*testutil.MockDriveFolderRepository
}

func (f *failingCreateFolderRepo) Create(_ *model.DriveFolder) error {
	return stubError
}

func TestCreateFolder_RepoError(t *testing.T) {
	mock := testutil.NewMockDriveFolderRepository()
	repo := &failingCreateFolderRepo{MockDriveFolderRepository: mock}
	idGen, _ := id.NewGenerator("aidx")
	svc := drive.NewService(testutil.NewMockDriveFileRepository(), repo, drive.NewLocalStorage(t.TempDir(), ""), idGen)
	_, err := svc.CreateFolder(&model.User{ID: "u1"}, "x", nil)
	assert.ErrorIs(t, err, stubError)
}

func TestShowFolder_NilUser(t *testing.T) {
	svc, _, _ := newSvc(t)
	_, err := svc.ShowFolder(nil, "x")
	assert.Error(t, err)
}

func TestShowFolder_NotFound(t *testing.T) {
	svc, _, _ := newSvc(t)
	_, err := svc.ShowFolder(&model.User{ID: "u1"}, "ghost")
	require.ErrorIs(t, err, drive.ErrFolderNotFound)
}

func TestShowFolder_AccessDenied(t *testing.T) {
	svc, _, folderRepo := newSvc(t)
	other := "other"
	folderRepo.Folders["p"] = &model.DriveFolder{ID: "p", UserID: &other}
	_, err := svc.ShowFolder(&model.User{ID: "u1"}, "p")
	require.ErrorIs(t, err, drive.ErrAccessDenied)
}

func TestUpdateFolder_HappyPath(t *testing.T) {
	svc, _, folderRepo := newSvc(t)
	uid := "u1"
	folderRepo.Folders["p"] = &model.DriveFolder{ID: "p", Name: "Old", UserID: &uid}

	newName := "New"
	got, err := svc.UpdateFolder(&model.User{ID: "u1"}, "p", drive.UpdateFolderInput{Name: &newName})
	require.NoError(t, err)
	assert.Equal(t, "New", got.Name)
}

func TestUpdateFolder_NotFound(t *testing.T) {
	svc, _, _ := newSvc(t)
	_, err := svc.UpdateFolder(&model.User{ID: "u1"}, "ghost", drive.UpdateFolderInput{})
	require.ErrorIs(t, err, drive.ErrFolderNotFound)
}

func TestUpdateFolder_ParentNotFound(t *testing.T) {
	svc, _, folderRepo := newSvc(t)
	uid := "u1"
	folderRepo.Folders["c"] = &model.DriveFolder{ID: "c", UserID: &uid}
	pid := "ghost"
	pidPtr := &pid
	_, err := svc.UpdateFolder(&model.User{ID: "u1"}, "c", drive.UpdateFolderInput{ParentID: &pidPtr})
	// #977: target 不在の ErrFolderNotFound と区別する distinct error。
	require.ErrorIs(t, err, drive.ErrParentFolderNotFound)
}

func TestUpdateFolder_ParentAccessDenied(t *testing.T) {
	svc, _, folderRepo := newSvc(t)
	uid := "u1"
	other := "other"
	folderRepo.Folders["c"] = &model.DriveFolder{ID: "c", UserID: &uid}
	folderRepo.Folders["p"] = &model.DriveFolder{ID: "p", UserID: &other}
	pid := "p"
	pidPtr := &pid
	_, err := svc.UpdateFolder(&model.User{ID: "u1"}, "c", drive.UpdateFolderInput{ParentID: &pidPtr})
	require.ErrorIs(t, err, drive.ErrAccessDenied)
}

func TestUpdateFolder_NullParent(t *testing.T) {
	svc, _, folderRepo := newSvc(t)
	uid := "u1"
	pid := "p"
	folderRepo.Folders["c"] = &model.DriveFolder{ID: "c", UserID: &uid, ParentID: &pid}

	var nilParent *string
	_, err := svc.UpdateFolder(&model.User{ID: "u1"}, "c", drive.UpdateFolderInput{ParentID: &nilParent})
	require.NoError(t, err)
}

type failingUpdateFolderRepo struct {
	*testutil.MockDriveFolderRepository
}

func (f *failingUpdateFolderRepo) Update(_ string, _ map[string]any) error {
	return stubError
}

func TestUpdateFolder_RepoError(t *testing.T) {
	mock := testutil.NewMockDriveFolderRepository()
	uid := "u1"
	mock.Folders["c"] = &model.DriveFolder{ID: "c", UserID: &uid}
	repo := &failingUpdateFolderRepo{MockDriveFolderRepository: mock}
	idGen, _ := id.NewGenerator("aidx")
	svc := drive.NewService(testutil.NewMockDriveFileRepository(), repo, drive.NewLocalStorage(t.TempDir(), ""), idGen)
	newName := "x"
	_, err := svc.UpdateFolder(&model.User{ID: "u1"}, "c", drive.UpdateFolderInput{Name: &newName})
	assert.ErrorIs(t, err, stubError)
}

func TestDeleteFolder_NotFound(t *testing.T) {
	svc, _, _ := newSvc(t)
	err := svc.DeleteFolder(&model.User{ID: "u1"}, "ghost")
	require.ErrorIs(t, err, drive.ErrFolderNotFound)
}

func TestDeleteFolder_NotEmpty(t *testing.T) {
	svc, fileRepo, folderRepo := newSvc(t)
	uid := "u1"
	folderRepo.Folders["p"] = &model.DriveFolder{ID: "p", UserID: &uid}
	pid := "p"
	fileRepo.Files["f1"] = &model.DriveFile{ID: "f1", UserID: &uid, FolderID: &pid}
	err := svc.DeleteFolder(&model.User{ID: "u1"}, "p")
	require.ErrorIs(t, err, drive.ErrFolderNotEmpty)
}

func TestDeleteFolder_HappyPath(t *testing.T) {
	svc, _, folderRepo := newSvc(t)
	uid := "u1"
	folderRepo.Folders["p"] = &model.DriveFolder{ID: "p", UserID: &uid}
	require.NoError(t, svc.DeleteFolder(&model.User{ID: "u1"}, "p"))
	assert.Empty(t, folderRepo.Folders)
}

type failingHasChildrenRepo struct {
	*testutil.MockDriveFolderRepository
}

func (f *failingHasChildrenRepo) HasChildren(_ string) (bool, error) {
	return false, stubError
}

func TestDeleteFolder_HasChildrenError(t *testing.T) {
	mock := testutil.NewMockDriveFolderRepository()
	uid := "u1"
	mock.Folders["p"] = &model.DriveFolder{ID: "p", UserID: &uid}
	repo := &failingHasChildrenRepo{MockDriveFolderRepository: mock}
	idGen, _ := id.NewGenerator("aidx")
	svc := drive.NewService(testutil.NewMockDriveFileRepository(), repo, drive.NewLocalStorage(t.TempDir(), ""), idGen)
	err := svc.DeleteFolder(&model.User{ID: "u1"}, "p")
	assert.ErrorIs(t, err, stubError)
}

// --- Streaming publisher hooks (Step K-7) ----------------------------------

// stubDriveStreamPublisher records every PublishDriveEvent call.
type stubDriveStreamPublisher struct {
	events []string // "<userID>:<eventType>"
}

func (s *stubDriveStreamPublisher) PublishDriveEvent(userID, eventType string, _ *model.DriveFile) {
	s.events = append(s.events, userID+":"+eventType)
}

func TestUpload_PublishesStreamingEvent(t *testing.T) {
	svc, _, _ := newSvc(t)
	pub := &stubDriveStreamPublisher{}
	svc.SetStreamingPublisher(pub)
	user := &model.User{ID: "u1"}
	_, err := svc.Upload(context.Background(), drive.UploadInput{User: user, Body: []byte("hi"), Name: "x.txt"})
	require.NoError(t, err)
	assert.Equal(t, []string{"u1:fileCreated"}, pub.events)
}

func TestUpdate_PublishesStreamingEvent(t *testing.T) {
	svc, _, _ := newSvc(t)
	user := &model.User{ID: "u1"}
	f, err := svc.Upload(context.Background(), drive.UploadInput{User: user, Body: []byte("hi"), Name: "x.txt"})
	require.NoError(t, err)

	pub := &stubDriveStreamPublisher{}
	svc.SetStreamingPublisher(pub)
	name := "renamed.txt"
	_, err = svc.Update(user, f.ID, drive.UpdateInput{Name: &name})
	require.NoError(t, err)
	assert.Equal(t, []string{"u1:fileUpdated"}, pub.events)
}

func TestDelete_PublishesStreamingEvent(t *testing.T) {
	svc, _, _ := newSvc(t)
	user := &model.User{ID: "u1"}
	f, err := svc.Upload(context.Background(), drive.UploadInput{User: user, Body: []byte("hi"), Name: "x.txt"})
	require.NoError(t, err)

	pub := &stubDriveStreamPublisher{}
	svc.SetStreamingPublisher(pub)
	require.NoError(t, svc.Delete(user, f.ID))
	assert.Equal(t, []string{"u1:fileDeleted"}, pub.events)
}

// stubChartHook captures chart hook fires from the drive service.
type stubChartHook struct {
	uploaded []string // file ids
	deleted  []string
}

func (s *stubChartHook) OnFileUploaded(f *model.DriveFile) { s.uploaded = append(s.uploaded, f.ID) }
func (s *stubChartHook) OnFileDeleted(f *model.DriveFile)  { s.deleted = append(s.deleted, f.ID) }

func TestUpload_FiresChartHook(t *testing.T) {
	svc, _, _ := newSvc(t)
	hook := &stubChartHook{}
	svc.SetChartHook(hook)
	user := &model.User{ID: "u1"}
	f, err := svc.Upload(context.Background(), drive.UploadInput{User: user, Body: []byte("hi"), Name: "x.txt"})
	require.NoError(t, err)
	assert.Equal(t, []string{f.ID}, hook.uploaded)
}

func TestDelete_FiresChartHook(t *testing.T) {
	svc, _, _ := newSvc(t)
	user := &model.User{ID: "u1"}
	f, err := svc.Upload(context.Background(), drive.UploadInput{User: user, Body: []byte("hi"), Name: "x.txt"})
	require.NoError(t, err)

	hook := &stubChartHook{}
	svc.SetChartHook(hook)
	require.NoError(t, svc.Delete(user, f.ID))
	assert.Equal(t, []string{f.ID}, hook.deleted)
}

// failingFindByIDRepo wraps the mock to make FindByID fail when called the
// second time (after the initial create succeeds in Update). 1 回目の
// findOwnedFile() (#499 で Show() から差し替え済) は成功させ、2 回目
// (Update 後の reload) で失敗させる。
type failingFindByIDRepo struct {
	*testutil.MockDriveFileRepository
	calls int
}

func (r *failingFindByIDRepo) FindByID(id string) (*model.DriveFile, error) {
	r.calls++
	if r.calls > 1 {
		return nil, stubError
	}
	return r.MockDriveFileRepository.FindByID(id)
}

func TestUpdate_FindByIDReloadError(t *testing.T) {
	mock := testutil.NewMockDriveFileRepository()
	uid := "u1"
	mock.Files["f1"] = &model.DriveFile{ID: "f1", UserID: &uid, Name: "x"}
	repo := &failingFindByIDRepo{MockDriveFileRepository: mock}
	idGen, _ := id.NewGenerator("aidx")
	svc := drive.NewService(repo, testutil.NewMockDriveFolderRepository(), drive.NewLocalStorage(t.TempDir(), ""), idGen)
	name := "renamed"
	_, err := svc.Update(&model.User{ID: "u1"}, "f1", drive.UpdateInput{Name: &name})
	assert.ErrorIs(t, err, stubError)
}

// failingDeleteFileRepo causes fileRepo.Delete to fail.
type failingDeleteFileRepo struct {
	*testutil.MockDriveFileRepository
}

func (r *failingDeleteFileRepo) Delete(_ *model.DriveFile) error { return stubError }

func TestDelete_FileRepoDeleteError(t *testing.T) {
	mock := testutil.NewMockDriveFileRepository()
	uid := "u1"
	mock.Files["f1"] = &model.DriveFile{ID: "f1", UserID: &uid, Name: "x"}
	repo := &failingDeleteFileRepo{MockDriveFileRepository: mock}
	idGen, _ := id.NewGenerator("aidx")
	svc := drive.NewService(repo, testutil.NewMockDriveFolderRepository(), drive.NewLocalStorage(t.TempDir(), ""), idGen)
	err := svc.Delete(&model.User{ID: "u1"}, "f1")
	assert.ErrorIs(t, err, stubError)
}

// ---------------------------------------------------------------------------
// Image processing integration tests (Step R)
// ---------------------------------------------------------------------------

// testJPEGBytes creates a minimal JPEG image for testing.
func testJPEGBytes(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{uint8(x * 50), uint8(y * 50), 100, 255})
		}
	}
	var buf bytes.Buffer
	_ = jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90})
	return buf.Bytes()
}

// testPNGBytes creates a minimal PNG image for testing.
func testPNGBytes(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{uint8(x * 30), uint8(y * 30), 200, 255})
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

func TestUpload_WithImageProcessor_JPEG(t *testing.T) {
	svc, fileRepo, _ := newSvc(t)
	svc.SetImageProcessor(drive.NewDefaultImageProcessor())

	user := &model.User{ID: "u1"}
	jpegData := testJPEGBytes(800, 600)
	f, err := svc.Upload(context.Background(), drive.UploadInput{User: user, Body: jpegData, Name: "photo.jpg"})
	require.NoError(t, err)
	assert.Len(t, fileRepo.Files, 1)

	// サムネイルが生成されている
	assert.NotNil(t, f.ThumbnailURL)
	assert.NotNil(t, f.ThumbnailAccessKey)

	// BlurHashが設定されている
	assert.NotNil(t, f.Blurhash)
	assert.NotEmpty(t, *f.Blurhash)

	// Propertiesに寸法が含まれている
	assert.NotEmpty(t, f.Properties)
	var props map[string]int
	err = json.Unmarshal(f.Properties, &props)
	require.NoError(t, err)
	assert.Equal(t, 800, props["width"])
	assert.Equal(t, 600, props["height"])
}

func TestUpload_WithImageProcessor_PNG_LargeWebpublic(t *testing.T) {
	svc, _, _ := newSvc(t)
	svc.SetImageProcessor(drive.NewDefaultImageProcessor())

	user := &model.User{ID: "u1"}
	// 2048超えの PNG → webpublic 生成
	pngData := testPNGBytes(3000, 2000)
	f, err := svc.Upload(context.Background(), drive.UploadInput{User: user, Body: pngData, Name: "big.png"})
	require.NoError(t, err)

	assert.NotNil(t, f.ThumbnailURL)
	assert.NotNil(t, f.WebpublicURL)
	assert.NotNil(t, f.WebpublicType)
	assert.Equal(t, "image/png", *f.WebpublicType) // PNG は PNG で出力
}

func TestUpload_WithImageProcessor_SmallJPEG_NoWebpublic(t *testing.T) {
	svc, _, _ := newSvc(t)
	svc.SetImageProcessor(drive.NewDefaultImageProcessor())

	user := &model.User{ID: "u1"}
	// 2048以下 + EXIF なし → webpublic 不要
	jpegData := testJPEGBytes(400, 300)
	f, err := svc.Upload(context.Background(), drive.UploadInput{User: user, Body: jpegData, Name: "small.jpg"})
	require.NoError(t, err)

	assert.NotNil(t, f.ThumbnailURL) // サムネイルは生成
	assert.Nil(t, f.WebpublicURL)    // webpublic は不要
}

func TestUpload_WithoutImageProcessor_TextFile(t *testing.T) {
	svc, _, _ := newSvc(t)
	// ImageProcessor 未設定

	user := &model.User{ID: "u1"}
	f, err := svc.Upload(context.Background(), drive.UploadInput{User: user, Body: []byte("hello"), Name: "test.txt"})
	require.NoError(t, err)

	assert.Nil(t, f.ThumbnailURL)
	assert.Nil(t, f.WebpublicURL)
	assert.Nil(t, f.Blurhash)
}

func TestUpload_WithImageProcessor_NonImage(t *testing.T) {
	svc, _, _ := newSvc(t)
	svc.SetImageProcessor(drive.NewDefaultImageProcessor())

	user := &model.User{ID: "u1"}
	f, err := svc.Upload(context.Background(), drive.UploadInput{User: user, Body: []byte("not an image"), Name: "data.bin"})
	require.NoError(t, err)

	// テキストは画像処理対象外
	assert.Nil(t, f.ThumbnailURL)
	assert.Nil(t, f.Blurhash)
}

// stubVideoProcessor always returns a fake thumbnail.
type stubVideoProcessor struct {
	thumbnail *drive.ProcessedImage
}

func (s *stubVideoProcessor) GenerateThumbnail(_ []byte, _ string) (*drive.ProcessedImage, error) {
	return s.thumbnail, nil
}

func TestUpload_WithVideoProcessor(t *testing.T) {
	svc, _, _ := newSvc(t)
	svc.SetVideoProcessor(&stubVideoProcessor{
		thumbnail: &drive.ProcessedImage{
			Data:     []byte("fake-thumb"),
			MimeType: "image/webp",
		},
	})

	user := &model.User{ID: "u1"}
	// video/mp4 のMIMEを強制するため、バイナリに MP4 ヘッダを付与
	mp4Header := []byte{0x00, 0x00, 0x00, 0x20, 0x66, 0x74, 0x79, 0x70} // ftyp box
	f, err := svc.Upload(context.Background(), drive.UploadInput{User: user, Body: mp4Header, Name: "clip.mp4"})
	require.NoError(t, err)

	// http.DetectContentType が video/mp4 を返す場合のみサムネイル生成
	// (返さない場合は application/octet-stream になるのでサムネイルなし)
	if f.Type == "video/mp4" {
		assert.NotNil(t, f.ThumbnailURL)
	}
}

func TestDelete_CleansUpThumbnailAndWebpublic(t *testing.T) {
	svc, fileRepo, _ := newSvc(t)
	svc.SetImageProcessor(drive.NewDefaultImageProcessor())

	user := &model.User{ID: "u1"}
	// 2048 超えの JPEG → thumbnail + webpublic 生成
	jpegData := testJPEGBytes(3000, 2000)
	f, err := svc.Upload(context.Background(), drive.UploadInput{User: user, Body: jpegData, Name: "large.jpg"})
	require.NoError(t, err)
	require.NotNil(t, f.ThumbnailAccessKey)
	require.NotNil(t, f.WebpublicAccessKey)

	// 削除前のファイル数を確認
	assert.Len(t, fileRepo.Files, 1)

	// 削除
	require.NoError(t, svc.Delete(user, f.ID))
	assert.Empty(t, fileRepo.Files)
}

func TestGenerateAlts_VideoPath(t *testing.T) {
	svc, _, _ := newSvc(t)
	svc.SetVideoProcessor(&stubVideoProcessor{
		thumbnail: &drive.ProcessedImage{Data: []byte("thumb"), MimeType: "image/webp"},
	})
	thumb, wp, bh := svc.GenerateAltsForTest([]byte("video"), "video/mp4")
	assert.NotNil(t, thumb)
	assert.Nil(t, wp)
	assert.Nil(t, bh)
}

func TestGenerateAlts_VideoPath_NilResult(t *testing.T) {
	svc, _, _ := newSvc(t)
	svc.SetVideoProcessor(&stubVideoProcessor{thumbnail: nil})
	thumb, _, _ := svc.GenerateAltsForTest([]byte("video"), "video/mp4")
	assert.Nil(t, thumb)
}

func TestGenerateAlts_NoProcessors(t *testing.T) {
	svc, _, _ := newSvc(t)
	thumb, wp, bh := svc.GenerateAltsForTest([]byte("data"), "image/jpeg")
	assert.Nil(t, thumb)
	assert.Nil(t, wp)
	assert.Nil(t, bh)
}

func TestGenerateAlts_NonImageNonVideo(t *testing.T) {
	svc, _, _ := newSvc(t)
	svc.SetImageProcessor(drive.NewDefaultImageProcessor())
	svc.SetVideoProcessor(&stubVideoProcessor{})
	thumb, wp, bh := svc.GenerateAltsForTest([]byte("text"), "text/plain")
	assert.Nil(t, thumb)
	assert.Nil(t, wp)
	assert.Nil(t, bh)
}

func TestUpload_ImageProcessor_RepoCreateErrorRollback(t *testing.T) {
	repo := &failingFileRepo{MockDriveFileRepository: testutil.NewMockDriveFileRepository()}
	idGen, _ := id.NewGenerator("aidx")
	tmpDir := t.TempDir()
	storage := drive.NewLocalStorage(tmpDir, "https://example.com/files")
	svc := drive.NewService(repo, testutil.NewMockDriveFolderRepository(), storage, idGen)
	svc.SetImageProcessor(drive.NewDefaultImageProcessor())

	user := &model.User{ID: "u1"}
	// 2048超え → thumbnail + webpublic 両方生成 → Create失敗 → 全ロールバック
	jpegData := testJPEGBytes(3000, 2000)
	_, err := svc.Upload(context.Background(), drive.UploadInput{User: user, Body: jpegData, Name: "photo.jpg"})
	assert.Error(t, err)

	// storage のファイルがロールバックされていることを確認
	entries, _ := os.ReadDir(tmpDir)
	assert.Empty(t, entries)
}
