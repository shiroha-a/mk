package drive

import (
	"bytes"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	coredrive "github.com/shiroha-a/mk/internal/core/drive"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/server/middleware"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var stubError = errors.New("stub error")

func newHandler(t *testing.T) (*Handler, *testutil.MockDriveFileRepository, *testutil.MockDriveFolderRepository) {
	t.Helper()
	fileRepo := testutil.NewMockDriveFileRepository()
	folderRepo := testutil.NewMockDriveFolderRepository()
	folderRepo.FilesRef = fileRepo
	storage := coredrive.NewLocalStorage(t.TempDir(), "https://example.com/files")
	idGen, _ := id.NewGenerator("aidx")
	svc := coredrive.NewService(fileRepo, folderRepo, storage, idGen)
	return NewHandler(svc, idGen), fileRepo, folderRepo
}

func setUser(c echo.Context, id string) {
	c.Set(string(middleware.UserContextKey), &model.User{ID: id})
}

func newJSONReq(t *testing.T, body string) (echo.Context, *httptest.ResponseRecorder) {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	return e.NewContext(req, rec), rec
}

// newMultipartReq builds a multipart form request with the given file body and form fields.
func newMultipartReq(t *testing.T, fileName, content string, fields map[string]string) (echo.Context, *httptest.ResponseRecorder) {
	t.Helper()
	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	if fileName != "" {
		part, err := mw.CreateFormFile("file", fileName)
		require.NoError(t, err)
		_, err = part.Write([]byte(content))
		require.NoError(t, err)
	}
	for k, v := range fields {
		require.NoError(t, mw.WriteField(k, v))
	}
	require.NoError(t, mw.Close())

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/", body)
	req.Header.Set(echo.HeaderContentType, mw.FormDataContentType())
	rec := httptest.NewRecorder()
	return e.NewContext(req, rec), rec
}

// --- FilesCreate ---

func TestFilesCreate_Success(t *testing.T) {
	h, _, _ := newHandler(t)
	c, rec := newMultipartReq(t, "hello.txt", "hello", nil)
	setUser(c, "u1")
	require.NoError(t, h.FilesCreate(c))
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestFilesCreate_AllFormFields(t *testing.T) {
	h, _, folderRepo := newHandler(t)
	uid := "u1"
	folderRepo.Folders["fid"] = &model.DriveFolder{ID: "fid", UserID: &uid}
	c, rec := newMultipartReq(t, "hello.txt", "hello", map[string]string{
		"name":        "renamed.txt",
		"folderId":    "fid",
		"comment":     "this is a comment",
		"isSensitive": "true",
		"force":       "true",
	})
	setUser(c, "u1")
	require.NoError(t, h.FilesCreate(c))
	require.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "renamed.txt", resp["name"])
	assert.Equal(t, true, resp["isSensitive"])
	// upstream `pack(file, { self: true })` (= withUser=false) に整合し、
	// userId / user は null を返す (#812)。
	assert.Nil(t, resp["userId"])
	assert.Nil(t, resp["user"])
}

func TestFilesCreate_NoFile(t *testing.T) {
	h, _, _ := newHandler(t)
	c, rec := newMultipartReq(t, "", "", nil)
	setUser(c, "u1")
	require.NoError(t, h.FilesCreate(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestFilesCreate_FolderNotFound(t *testing.T) {
	h, _, _ := newHandler(t)
	c, rec := newMultipartReq(t, "hello.txt", "hello", map[string]string{"folderId": "ghost"})
	setUser(c, "u1")
	require.NoError(t, h.FilesCreate(c))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestFilesCreate_FolderAccessDenied(t *testing.T) {
	h, _, folderRepo := newHandler(t)
	other := "other"
	folderRepo.Folders["fid"] = &model.DriveFolder{ID: "fid", UserID: &other}
	c, rec := newMultipartReq(t, "hello.txt", "hello", map[string]string{"folderId": "fid"})
	setUser(c, "u1")
	require.NoError(t, h.FilesCreate(c))
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

// failingFileRepo causes Create to fail.
type failingFileRepo struct {
	*testutil.MockDriveFileRepository
}

func (f *failingFileRepo) Create(_ *model.DriveFile) error {
	return stubError
}

func TestFilesCreate_RepoError(t *testing.T) {
	repo := &failingFileRepo{MockDriveFileRepository: testutil.NewMockDriveFileRepository()}
	folderRepo := testutil.NewMockDriveFolderRepository()
	idGen, _ := id.NewGenerator("aidx")
	storage := coredrive.NewLocalStorage(t.TempDir(), "")
	svc := coredrive.NewService(repo, folderRepo, storage, idGen)
	h := NewHandler(svc, idGen)

	c, rec := newMultipartReq(t, "hello.txt", "hello", nil)
	setUser(c, "u1")
	require.NoError(t, h.FilesCreate(c))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

// --- FilesShow ---

func TestFilesShow_Success(t *testing.T) {
	h, fileRepo, _ := newHandler(t)
	uid := "u1"
	fileRepo.Files["f1"] = &model.DriveFile{ID: "f1", UserID: &uid, Name: "hi"}
	c, rec := newJSONReq(t, `{"fileId":"f1"}`)
	setUser(c, "u1")
	require.NoError(t, h.FilesShow(c))
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestFilesShow_InvalidParam(t *testing.T) {
	h, _, _ := newHandler(t)
	c, rec := newJSONReq(t, `{}`)
	setUser(c, "u1")
	require.NoError(t, h.FilesShow(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestFilesShow_NotFound(t *testing.T) {
	h, _, _ := newHandler(t)
	c, rec := newJSONReq(t, `{"fileId":"ghost"}`)
	setUser(c, "u1")
	require.NoError(t, h.FilesShow(c))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestFilesShow_AccessDenied(t *testing.T) {
	h, fileRepo, _ := newHandler(t)
	other := "other"
	fileRepo.Files["f1"] = &model.DriveFile{ID: "f1", UserID: &other}
	c, rec := newJSONReq(t, `{"fileId":"f1"}`)
	setUser(c, "u1")
	require.NoError(t, h.FilesShow(c))
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestFilesShow_EmbedsFolderAndUser(t *testing.T) {
	h, fileRepo, folderRepo := newHandler(t)
	// folder/user/noteRepoを配線 (本番router.go相当)
	h.SetRepos(fileRepo, folderRepo, testutil.NewMockNoteRepository())
	userRepo := testutil.NewMockUserRepository()
	h.SetUserRepo(userRepo)

	uid := "u1"
	folderID := "fl1"
	fileRepo.Files["f1"] = &model.DriveFile{ID: "f1", UserID: &uid, Name: "hi", FolderID: &folderID}
	folderRepo.Folders[folderID] = &model.DriveFolder{ID: folderID, Name: "Pics", UserID: &uid}
	userRepo.Users[uid] = &model.User{ID: uid, Username: "alice"}

	c, rec := newJSONReq(t, `{"fileId":"f1"}`)
	setUser(c, "u1")
	require.NoError(t, h.FilesShow(c))
	require.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	folder, ok := resp["folder"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "Pics", folder["name"])
	user, ok := resp["user"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "alice", user["username"])
}

// --- FilesUpdate ---

func TestFilesUpdate_Success(t *testing.T) {
	h, fileRepo, _ := newHandler(t)
	uid := "u1"
	fileRepo.Files["f1"] = &model.DriveFile{ID: "f1", UserID: &uid, Name: "old"}
	c, rec := newJSONReq(t, `{"fileId":"f1","name":"new","comment":"hi","isSensitive":true}`)
	setUser(c, "u1")
	require.NoError(t, h.FilesUpdate(c))
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestFilesUpdate_InvalidParam(t *testing.T) {
	h, _, _ := newHandler(t)
	c, rec := newJSONReq(t, `{}`)
	setUser(c, "u1")
	require.NoError(t, h.FilesUpdate(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestFilesUpdate_UnsetFolder(t *testing.T) {
	h, fileRepo, _ := newHandler(t)
	uid := "u1"
	folderID := "fid"
	fileRepo.Files["f1"] = &model.DriveFile{ID: "f1", UserID: &uid, FolderID: &folderID}
	c, rec := newJSONReq(t, `{"fileId":"f1","unsetFolder":true}`)
	setUser(c, "u1")
	require.NoError(t, h.FilesUpdate(c))
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestFilesUpdate_SetFolder(t *testing.T) {
	h, fileRepo, folderRepo := newHandler(t)
	uid := "u1"
	fileRepo.Files["f1"] = &model.DriveFile{ID: "f1", UserID: &uid}
	folderRepo.Folders["fid"] = &model.DriveFolder{ID: "fid", UserID: &uid}
	c, rec := newJSONReq(t, `{"fileId":"f1","folderId":"fid"}`)
	setUser(c, "u1")
	require.NoError(t, h.FilesUpdate(c))
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestFilesUpdate_FileNotFound(t *testing.T) {
	h, _, _ := newHandler(t)
	c, rec := newJSONReq(t, `{"fileId":"ghost"}`)
	setUser(c, "u1")
	require.NoError(t, h.FilesUpdate(c))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestFilesUpdate_FolderNotFound(t *testing.T) {
	h, fileRepo, _ := newHandler(t)
	uid := "u1"
	fileRepo.Files["f1"] = &model.DriveFile{ID: "f1", UserID: &uid}
	c, rec := newJSONReq(t, `{"fileId":"f1","folderId":"ghost"}`)
	setUser(c, "u1")
	require.NoError(t, h.FilesUpdate(c))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// failingUpdateFileRepo causes Update to fail with non-mapped error.
type failingUpdateFileRepo struct {
	*testutil.MockDriveFileRepository
}

func (f *failingUpdateFileRepo) Update(_ string, _ map[string]any) error {
	return stubError
}

func TestFilesUpdate_RepoError(t *testing.T) {
	mock := testutil.NewMockDriveFileRepository()
	uid := "u1"
	mock.Files["f1"] = &model.DriveFile{ID: "f1", UserID: &uid}
	repo := &failingUpdateFileRepo{MockDriveFileRepository: mock}
	idGen, _ := id.NewGenerator("aidx")
	svc := coredrive.NewService(repo, testutil.NewMockDriveFolderRepository(), coredrive.NewLocalStorage(t.TempDir(), ""), idGen)
	h := NewHandler(svc, idGen)

	c, rec := newJSONReq(t, `{"fileId":"f1","name":"x"}`)
	setUser(c, "u1")
	require.NoError(t, h.FilesUpdate(c))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

// --- FilesDelete ---

func TestFilesDelete_Success(t *testing.T) {
	h, fileRepo, _ := newHandler(t)
	uid := "u1"
	fileRepo.Files["f1"] = &model.DriveFile{ID: "f1", UserID: &uid}
	c, rec := newJSONReq(t, `{"fileId":"f1"}`)
	setUser(c, "u1")
	require.NoError(t, h.FilesDelete(c))
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

func TestFilesDelete_InvalidParam(t *testing.T) {
	h, _, _ := newHandler(t)
	c, rec := newJSONReq(t, `{}`)
	setUser(c, "u1")
	require.NoError(t, h.FilesDelete(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestFilesDelete_NotFound(t *testing.T) {
	h, _, _ := newHandler(t)
	c, rec := newJSONReq(t, `{"fileId":"ghost"}`)
	setUser(c, "u1")
	require.NoError(t, h.FilesDelete(c))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// --- FilesFindByHash ---

func TestFilesFindByHash_Success(t *testing.T) {
	h, fileRepo, _ := newHandler(t)
	uid := "u1"
	fileRepo.Files["f1"] = &model.DriveFile{ID: "f1", UserID: &uid, MD5: "abc"}
	c, rec := newJSONReq(t, `{"md5":"abc"}`)
	setUser(c, "u1")
	require.NoError(t, h.FilesFindByHash(c))
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestFilesFindByHash_InvalidParam(t *testing.T) {
	h, _, _ := newHandler(t)
	c, rec := newJSONReq(t, `{}`)
	setUser(c, "u1")
	require.NoError(t, h.FilesFindByHash(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestFilesFindByHash_NotFound(t *testing.T) {
	h, _, _ := newHandler(t)
	c, rec := newJSONReq(t, `{"md5":"ghost"}`)
	setUser(c, "u1")
	require.NoError(t, h.FilesFindByHash(c))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// --- FoldersCreate ---

func TestFoldersCreate_Success(t *testing.T) {
	h, _, _ := newHandler(t)
	c, rec := newJSONReq(t, `{"name":"My Folder"}`)
	setUser(c, "u1")
	require.NoError(t, h.FoldersCreate(c))
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestFoldersCreate_DefaultName(t *testing.T) {
	h, _, _ := newHandler(t)
	c, rec := newJSONReq(t, `{}`)
	setUser(c, "u1")
	require.NoError(t, h.FoldersCreate(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "Untitled", resp["name"])
}

func TestFoldersCreate_InvalidJSON(t *testing.T) {
	h, _, _ := newHandler(t)
	c, rec := newJSONReq(t, `{invalid`)
	setUser(c, "u1")
	require.NoError(t, h.FoldersCreate(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestFoldersCreate_ParentNotFound(t *testing.T) {
	h, _, _ := newHandler(t)
	c, rec := newJSONReq(t, `{"name":"x","parentId":"ghost"}`)
	setUser(c, "u1")
	require.NoError(t, h.FoldersCreate(c))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestFoldersCreate_AccessDenied(t *testing.T) {
	h, _, folderRepo := newHandler(t)
	other := "other"
	folderRepo.Folders["p"] = &model.DriveFolder{ID: "p", UserID: &other}
	c, rec := newJSONReq(t, `{"name":"x","parentId":"p"}`)
	setUser(c, "u1")
	require.NoError(t, h.FoldersCreate(c))
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

// failingFolderRepo causes Create to fail
type failingFolderRepo struct {
	*testutil.MockDriveFolderRepository
}

func (f *failingFolderRepo) Create(_ *model.DriveFolder) error {
	return stubError
}

func TestFoldersCreate_RepoError(t *testing.T) {
	repo := &failingFolderRepo{MockDriveFolderRepository: testutil.NewMockDriveFolderRepository()}
	idGen, _ := id.NewGenerator("aidx")
	svc := coredrive.NewService(testutil.NewMockDriveFileRepository(), repo, coredrive.NewLocalStorage(t.TempDir(), ""), idGen)
	h := NewHandler(svc, idGen)
	c, rec := newJSONReq(t, `{"name":"x"}`)
	setUser(c, "u1")
	require.NoError(t, h.FoldersCreate(c))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

// --- FoldersShow ---

func TestFoldersShow_Success(t *testing.T) {
	h, _, folderRepo := newHandler(t)
	uid := "u1"
	folderRepo.Folders["p"] = &model.DriveFolder{ID: "p", Name: "Test", UserID: &uid}
	c, rec := newJSONReq(t, `{"folderId":"p"}`)
	setUser(c, "u1")
	require.NoError(t, h.FoldersShow(c))
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestFoldersShow_InvalidParam(t *testing.T) {
	h, _, _ := newHandler(t)
	c, rec := newJSONReq(t, `{}`)
	setUser(c, "u1")
	require.NoError(t, h.FoldersShow(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestFoldersShow_NotFound(t *testing.T) {
	h, _, _ := newHandler(t)
	c, rec := newJSONReq(t, `{"folderId":"ghost"}`)
	setUser(c, "u1")
	require.NoError(t, h.FoldersShow(c))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestFoldersShow_AccessDenied(t *testing.T) {
	h, _, folderRepo := newHandler(t)
	other := "other"
	folderRepo.Folders["p"] = &model.DriveFolder{ID: "p", UserID: &other}
	c, rec := newJSONReq(t, `{"folderId":"p"}`)
	setUser(c, "u1")
	require.NoError(t, h.FoldersShow(c))
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

// --- FoldersUpdate ---

func TestFoldersUpdate_Success(t *testing.T) {
	h, _, folderRepo := newHandler(t)
	uid := "u1"
	folderRepo.Folders["p"] = &model.DriveFolder{ID: "p", Name: "Old", UserID: &uid}
	c, rec := newJSONReq(t, `{"folderId":"p","name":"New"}`)
	setUser(c, "u1")
	require.NoError(t, h.FoldersUpdate(c))
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestFoldersUpdate_InvalidParam(t *testing.T) {
	h, _, _ := newHandler(t)
	c, rec := newJSONReq(t, `{}`)
	setUser(c, "u1")
	require.NoError(t, h.FoldersUpdate(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestFoldersUpdate_UnsetParent(t *testing.T) {
	h, _, folderRepo := newHandler(t)
	uid := "u1"
	pid := "p"
	folderRepo.Folders["c"] = &model.DriveFolder{ID: "c", UserID: &uid, ParentID: &pid}
	c, rec := newJSONReq(t, `{"folderId":"c","unsetParent":true}`)
	setUser(c, "u1")
	require.NoError(t, h.FoldersUpdate(c))
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestFoldersUpdate_SetParent(t *testing.T) {
	h, _, folderRepo := newHandler(t)
	uid := "u1"
	folderRepo.Folders["p"] = &model.DriveFolder{ID: "p", UserID: &uid}
	folderRepo.Folders["c"] = &model.DriveFolder{ID: "c", UserID: &uid}
	c, rec := newJSONReq(t, `{"folderId":"c","parentId":"p"}`)
	setUser(c, "u1")
	require.NoError(t, h.FoldersUpdate(c))
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestFoldersUpdate_NotFound(t *testing.T) {
	h, _, _ := newHandler(t)
	c, rec := newJSONReq(t, `{"folderId":"ghost"}`)
	setUser(c, "u1")
	require.NoError(t, h.FoldersUpdate(c))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// --- FoldersDelete ---

func TestFoldersDelete_Success(t *testing.T) {
	h, _, folderRepo := newHandler(t)
	uid := "u1"
	folderRepo.Folders["p"] = &model.DriveFolder{ID: "p", UserID: &uid}
	c, rec := newJSONReq(t, `{"folderId":"p"}`)
	setUser(c, "u1")
	require.NoError(t, h.FoldersDelete(c))
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

func TestFoldersDelete_InvalidParam(t *testing.T) {
	h, _, _ := newHandler(t)
	c, rec := newJSONReq(t, `{}`)
	setUser(c, "u1")
	require.NoError(t, h.FoldersDelete(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestFoldersDelete_NotFound(t *testing.T) {
	h, _, _ := newHandler(t)
	c, rec := newJSONReq(t, `{"folderId":"ghost"}`)
	setUser(c, "u1")
	require.NoError(t, h.FoldersDelete(c))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestFoldersDelete_NotEmpty(t *testing.T) {
	h, fileRepo, folderRepo := newHandler(t)
	uid := "u1"
	folderRepo.Folders["p"] = &model.DriveFolder{ID: "p", UserID: &uid}
	pid := "p"
	fileRepo.Files["f1"] = &model.DriveFile{ID: "f1", UserID: &uid, FolderID: &pid}
	c, rec := newJSONReq(t, `{"folderId":"p"}`)
	setUser(c, "u1")
	require.NoError(t, h.FoldersDelete(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// --- New drive listing endpoints ---

func newHandlerWithRepos(t *testing.T) (*Handler, *testutil.MockDriveFileRepository, *testutil.MockDriveFolderRepository) {
	t.Helper()
	h, fileRepo, folderRepo := newHandler(t)
	h.SetRepos(fileRepo, folderRepo, testutil.NewMockNoteRepository())
	return h, fileRepo, folderRepo
}

func TestUsage_Success(t *testing.T) {
	h, fileRepo, _ := newHandlerWithRepos(t)
	uid := "u1"
	fileRepo.Files["f1"] = &model.DriveFile{ID: "f1", UserID: &uid, Size: 1024}
	c, rec := newJSONReq(t, `{}`)
	setUser(c, "u1")
	require.NoError(t, h.Usage(c))
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestUsage_NilRepo(t *testing.T) {
	h, _, _ := newHandler(t)
	c, rec := newJSONReq(t, `{}`)
	setUser(c, "u1")
	require.NoError(t, h.Usage(c))
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestFilesList_Success(t *testing.T) {
	h, fileRepo, _ := newHandlerWithRepos(t)
	uid := "u1"
	fileRepo.Files["f1"] = &model.DriveFile{ID: "f1", UserID: &uid, Name: "test.png"}
	c, rec := newJSONReq(t, `{}`)
	setUser(c, "u1")
	require.NoError(t, h.FilesList(c))
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestFilesList_NilRepo(t *testing.T) {
	h, _, _ := newHandler(t)
	c, rec := newJSONReq(t, `{}`)
	setUser(c, "u1")
	require.NoError(t, h.FilesList(c))
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestFilesFind_Success(t *testing.T) {
	h, fileRepo, _ := newHandlerWithRepos(t)
	uid := "u1"
	fileRepo.Files["f1"] = &model.DriveFile{ID: "f1", UserID: &uid, Name: "test.png"}
	c, rec := newJSONReq(t, `{"name":"test.png"}`)
	setUser(c, "u1")
	require.NoError(t, h.FilesFind(c))
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestFilesFind_InvalidParam(t *testing.T) {
	h, _, _ := newHandlerWithRepos(t)
	c, rec := newJSONReq(t, `{}`)
	setUser(c, "u1")
	require.NoError(t, h.FilesFind(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestFilesFind_NilRepo(t *testing.T) {
	h, _, _ := newHandler(t)
	c, rec := newJSONReq(t, `{"name":"x"}`)
	setUser(c, "u1")
	require.NoError(t, h.FilesFind(c))
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestFilesCheckExistence_True(t *testing.T) {
	h, fileRepo, _ := newHandlerWithRepos(t)
	uid := "u1"
	fileRepo.Files["f1"] = &model.DriveFile{ID: "f1", UserID: &uid, MD5: "abc123"}
	c, rec := newJSONReq(t, `{"md5":"abc123"}`)
	setUser(c, "u1")
	require.NoError(t, h.FilesCheckExistence(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "true")
}

func TestFilesCheckExistence_False(t *testing.T) {
	h, _, _ := newHandlerWithRepos(t)
	c, rec := newJSONReq(t, `{"md5":"ghost"}`)
	setUser(c, "u1")
	require.NoError(t, h.FilesCheckExistence(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "false")
}

func TestFilesCheckExistence_InvalidParam(t *testing.T) {
	h, _, _ := newHandlerWithRepos(t)
	c, rec := newJSONReq(t, `{}`)
	setUser(c, "u1")
	require.NoError(t, h.FilesCheckExistence(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestFilesCheckExistence_NilRepo(t *testing.T) {
	h, _, _ := newHandler(t)
	c, rec := newJSONReq(t, `{"md5":"x"}`)
	setUser(c, "u1")
	require.NoError(t, h.FilesCheckExistence(c))
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestFilesAttachedNotes_Success(t *testing.T) {
	h, _, _ := newHandlerWithRepos(t)
	c, rec := newJSONReq(t, `{"fileId":"f1"}`)
	setUser(c, "u1")
	require.NoError(t, h.FilesAttachedNotes(c))
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestFilesAttachedNotes_InvalidParam(t *testing.T) {
	h, _, _ := newHandlerWithRepos(t)
	c, rec := newJSONReq(t, `{}`)
	setUser(c, "u1")
	require.NoError(t, h.FilesAttachedNotes(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestFilesAttachedNotes_NilRepo(t *testing.T) {
	h, _, _ := newHandler(t)
	c, rec := newJSONReq(t, `{"fileId":"f1"}`)
	setUser(c, "u1")
	require.NoError(t, h.FilesAttachedNotes(c))
	assert.Equal(t, http.StatusOK, rec.Code)
}

// frontend Paginator から渡される sinceId / untilId / limit が
// noteRepo.ListByFileID に正しく forward されることを確認 (#488 回帰
// 防止)。bind 漏れがあると同じ note が無限スクロールで何度も返る。
type spyNoteRepo struct {
	*testutil.MockNoteRepository
	gotFileID  string
	gotSinceID string
	gotUntilID string
	gotLimit   int
}

func (s *spyNoteRepo) ListByFileID(fileID, sinceID, untilID string, limit int) ([]*model.Note, error) {
	s.gotFileID = fileID
	s.gotSinceID = sinceID
	s.gotUntilID = untilID
	s.gotLimit = limit
	return nil, nil
}

func TestFilesAttachedNotes_ForwardsCursorParams(t *testing.T) {
	h, fileRepo, folderRepo := newHandler(t)
	spy := &spyNoteRepo{MockNoteRepository: testutil.NewMockNoteRepository()}
	h.SetRepos(fileRepo, folderRepo, spy)

	c, rec := newJSONReq(t, `{"fileId":"f1","untilId":"u_xxx","limit":7}`)
	setUser(c, "u1")
	require.NoError(t, h.FilesAttachedNotes(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "f1", spy.gotFileID)
	assert.Equal(t, "u_xxx", spy.gotUntilID)
	assert.Equal(t, "", spy.gotSinceID)
	assert.Equal(t, 7, spy.gotLimit)
}

func TestFilesAttachedChatMessages(t *testing.T) {
	h, _, _ := newHandler(t)
	c, rec := newJSONReq(t, `{"fileId":"f1"}`)
	require.NoError(t, h.FilesAttachedChatMessages(c))
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestFilesUploadFromURL(t *testing.T) {
	h, _, _ := newHandler(t)
	c, rec := newJSONReq(t, `{"url":"https://example.com/img.png"}`)
	setUser(c, "u1")
	require.NoError(t, h.FilesUploadFromURL(c))
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

func TestFilesMoveBulk_Success(t *testing.T) {
	h, _, _ := newHandlerWithRepos(t)
	c, rec := newJSONReq(t, `{"fileIds":["f1","f2"]}`)
	setUser(c, "u1")
	require.NoError(t, h.FilesMoveBulk(c))
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

func TestFilesMoveBulk_InvalidParam(t *testing.T) {
	h, _, _ := newHandlerWithRepos(t)
	c, rec := newJSONReq(t, `{}`)
	setUser(c, "u1")
	require.NoError(t, h.FilesMoveBulk(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestFilesMoveBulk_NilRepo(t *testing.T) {
	h, _, _ := newHandler(t)
	c, rec := newJSONReq(t, `{"fileIds":["f1"]}`)
	setUser(c, "u1")
	require.NoError(t, h.FilesMoveBulk(c))
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

func TestStream_Success(t *testing.T) {
	h, _, _ := newHandlerWithRepos(t)
	c, rec := newJSONReq(t, `{}`)
	setUser(c, "u1")
	require.NoError(t, h.Stream(c))
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestStream_NilRepo(t *testing.T) {
	h, _, _ := newHandler(t)
	c, rec := newJSONReq(t, `{}`)
	setUser(c, "u1")
	require.NoError(t, h.Stream(c))
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestFoldersList_Success(t *testing.T) {
	h, _, folderRepo := newHandlerWithRepos(t)
	uid := "u1"
	folderRepo.Folders["d1"] = &model.DriveFolder{ID: "d1", UserID: &uid, Name: "photos"}
	c, rec := newJSONReq(t, `{}`)
	setUser(c, "u1")
	require.NoError(t, h.FoldersList(c))
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestFoldersList_NilRepo(t *testing.T) {
	h, _, _ := newHandler(t)
	c, rec := newJSONReq(t, `{}`)
	setUser(c, "u1")
	require.NoError(t, h.FoldersList(c))
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestFoldersFind_Success(t *testing.T) {
	h, _, folderRepo := newHandlerWithRepos(t)
	uid := "u1"
	folderRepo.Folders["d1"] = &model.DriveFolder{ID: "d1", UserID: &uid, Name: "photos"}
	c, rec := newJSONReq(t, `{"name":"photos"}`)
	setUser(c, "u1")
	require.NoError(t, h.FoldersFind(c))
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestFoldersFind_InvalidParam(t *testing.T) {
	h, _, _ := newHandlerWithRepos(t)
	c, rec := newJSONReq(t, `{}`)
	setUser(c, "u1")
	require.NoError(t, h.FoldersFind(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestFoldersFind_NilRepo(t *testing.T) {
	h, _, _ := newHandler(t)
	c, rec := newJSONReq(t, `{"name":"x"}`)
	setUser(c, "u1")
	require.NoError(t, h.FoldersFind(c))
	assert.Equal(t, http.StatusOK, rec.Code)
}
