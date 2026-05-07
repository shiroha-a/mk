package transfer_test

import (
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/shiroha-a/mk/internal/core/transfer"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Fakes for drive.Storage and DriveFileRepository ---

type fakeStorage struct {
	blobs  map[string][]byte
	getErr error
}

func (f *fakeStorage) Put(_ string, _ io.Reader) (string, error) { return "", nil }
func (f *fakeStorage) Get(accessKey string) (io.ReadCloser, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	b, ok := f.blobs[accessKey]
	if !ok {
		return nil, errors.New("not found")
	}
	return io.NopCloser(strings.NewReader(string(b))), nil
}
func (f *fakeStorage) Delete(_ string) error { return nil }

// fakeDriveFileRepo is a minimal stub sufficient for the reader test.
// Only FindByID is exercised. Other methods return nil errors to satisfy the
// interface; they must not be called from the reader.
type fakeDriveFileRepo struct {
	files map[string]*model.DriveFile
}

func (f *fakeDriveFileRepo) FindByID(id string) (*model.DriveFile, error) {
	if file, ok := f.files[id]; ok {
		return file, nil
	}
	return nil, errors.New("not found")
}

func (f *fakeDriveFileRepo) FindByIDs(ids []string) ([]*model.DriveFile, error) {
	out := make([]*model.DriveFile, 0, len(ids))
	for _, id := range ids {
		if f, ok := f.files[id]; ok {
			out = append(out, f)
		}
	}
	return out, nil
}
func (f *fakeDriveFileRepo) FindByMD5(_, _ string) (*model.DriveFile, error) {
	return nil, errors.New("not found")
}
func (f *fakeDriveFileRepo) FindByURI(_ string) (*model.DriveFile, error) {
	return nil, errors.New("not found")
}
func (f *fakeDriveFileRepo) Create(file *model.DriveFile) error {
	f.files[file.ID] = file
	return nil
}
func (f *fakeDriveFileRepo) Update(_ string, _ map[string]any) error {
	return nil
}
func (f *fakeDriveFileRepo) Delete(_ *model.DriveFile) error { return nil }
func (f *fakeDriveFileRepo) ListByUser(_ string, _ *string, _, _ string, _ int) ([]*model.DriveFile, error) {
	return nil, nil
}
func (f *fakeDriveFileRepo) FindByName(_, _ string, _ *string) ([]*model.DriveFile, error) {
	return nil, nil
}
func (f *fakeDriveFileRepo) ExistsByMD5(_, _ string) (bool, error)                { return false, nil }
func (f *fakeDriveFileRepo) CountByFolder(_ string) (int, error)                  { return 0, nil }
func (f *fakeDriveFileRepo) ListByFileIDs(_ []string) ([]*model.DriveFile, error) { return nil, nil }
func (f *fakeDriveFileRepo) UsageByUser(_ string) (int64, error)                  { return 0, nil }
func (f *fakeDriveFileRepo) UpdateBulkFolder(_ []string, _ *string) error         { return nil }
func (f *fakeDriveFileRepo) ListForAdmin(_, _, _, _, _, _ string, _ int) ([]*model.DriveFile, error) {
	return nil, nil
}
func (f *fakeDriveFileRepo) ListSystemFiles(_, _, _ string, _ int) ([]*model.DriveFile, error) {
	return nil, nil
}
func (f *fakeDriveFileRepo) DeleteOrphans() (int64, error)     { return 0, nil }
func (f *fakeDriveFileRepo) DeleteRemoteCache() (int64, error) { return 0, nil }
func (f *fakeDriveFileRepo) DeleteByUser(_ string) (int64, error) {
	return 0, nil
}
func (f *fakeDriveFileRepo) DeleteByHost(_ string) (int64, error) {
	return 0, nil
}
func (f *fakeDriveFileRepo) FindByAccessKey(_ string) (*model.DriveFile, error) {
	return nil, errors.New("not implemented")
}

// Ensure fakeDriveFileRepo implements the real repository interface so the
// reader accepts it.
var _ repository.DriveFileRepository = (*fakeDriveFileRepo)(nil)

// --- Tests ---

func TestDriveReader_Fetch_Success(t *testing.T) {
	key := "k1"
	repo := &fakeDriveFileRepo{files: map[string]*model.DriveFile{
		"f1": {ID: "f1", AccessKey: &key},
	}}
	store := &fakeStorage{blobs: map[string][]byte{key: []byte("hello")}}
	r := transfer.NewRepoBackedDriveReader(repo, store)
	file, body, err := r.Fetch("f1")
	require.NoError(t, err)
	assert.Equal(t, "f1", file.ID)
	assert.Equal(t, []byte("hello"), body)
}

func TestDriveReader_Fetch_FileNotFound(t *testing.T) {
	repo := &fakeDriveFileRepo{files: map[string]*model.DriveFile{}}
	store := &fakeStorage{}
	r := transfer.NewRepoBackedDriveReader(repo, store)
	_, _, err := r.Fetch("missing")
	assert.Error(t, err)
}

func TestDriveReader_Fetch_NoAccessKey(t *testing.T) {
	repo := &fakeDriveFileRepo{files: map[string]*model.DriveFile{
		"f1": {ID: "f1"},
	}}
	store := &fakeStorage{}
	r := transfer.NewRepoBackedDriveReader(repo, store)
	_, _, err := r.Fetch("f1")
	assert.Error(t, err)
}

func TestDriveReader_Fetch_StorageError(t *testing.T) {
	key := "k1"
	repo := &fakeDriveFileRepo{files: map[string]*model.DriveFile{
		"f1": {ID: "f1", AccessKey: &key},
	}}
	store := &fakeStorage{getErr: errors.New("io boom")}
	r := transfer.NewRepoBackedDriveReader(repo, store)
	_, _, err := r.Fetch("f1")
	assert.Error(t, err)
}

func TestDriveReader_Nil(t *testing.T) {
	var r *transfer.RepoBackedDriveReader
	_, _, err := r.Fetch("x")
	assert.Error(t, err)
}
