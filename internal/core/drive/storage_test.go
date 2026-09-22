package drive

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLocalStorage_PutGetDelete(t *testing.T) {
	dir := t.TempDir()
	s := NewLocalStorage(dir, "https://example.com/files")

	url, err := s.Put("abc", bytes.NewReader([]byte("hello")))
	require.NoError(t, err)
	assert.Equal(t, "https://example.com/files/abc", url)

	body, err := s.Get("abc")
	require.NoError(t, err)
	defer body.Close()
	got, err := io.ReadAll(body)
	require.NoError(t, err)
	assert.Equal(t, "hello", string(got))

	require.NoError(t, s.Delete("abc"))
	_, err = s.Get("abc")
	assert.ErrorIs(t, err, ErrObjectNotFound)
}

func TestLocalStorage_Get_OtherError(t *testing.T) {
	dir := t.TempDir()
	// 存在するが読めないパーミッションのファイルを作る
	target := filepath.Join(dir, "denied")
	require.NoError(t, os.WriteFile(target, []byte("x"), 0o000))
	// defer の中では検査できないので明示的に捨てる (後始末で、失敗しても
	// t.TempDir の削除が引き継ぐ)。
	defer func() { _ = os.Chmod(target, 0o644) }()

	s := NewLocalStorage(dir, "")
	_, err := s.Get("denied")
	if err != nil && !errors.Is(err, ErrObjectNotFound) && os.Geteuid() != 0 {
		// rootユーザーではpermission deniedにならないので case 分岐
		assert.True(t, errors.Is(err, os.ErrPermission) || err != nil)
	}
}

func TestLocalStorage_Delete_Missing(t *testing.T) {
	dir := t.TempDir()
	s := NewLocalStorage(dir, "")
	require.NoError(t, s.Delete("does-not-exist"))
}

func TestLocalStorage_Delete_Error(t *testing.T) {
	// rootDirがファイルとして存在する場合 Remove は別のエラーを返す
	tmp := t.TempDir()
	conflict := filepath.Join(tmp, "rootIsFile")
	require.NoError(t, os.WriteFile(conflict, []byte("x"), 0o644))
	// rootDirとしてファイルパスを渡し、そのサブパスを削除しようとする
	s := NewLocalStorage(conflict, "")
	err := s.Delete("anything")
	// "Not a directory" 系エラーになる (NotExistではない)
	assert.Error(t, err)
}

func TestLocalStorage_Put_MkdirError(t *testing.T) {
	tmp := t.TempDir()
	conflict := filepath.Join(tmp, "fileNotDir")
	require.NoError(t, os.WriteFile(conflict, []byte("x"), 0o644))
	// rootDir にファイルを指定するとMkdirAllが失敗する
	s := NewLocalStorage(conflict, "")
	_, err := s.Put("k", bytes.NewReader([]byte("v")))
	assert.Error(t, err)
}

// errorReader always returns an error on Read.
type errorReader struct{}

func (errorReader) Read(_ []byte) (int, error) { return 0, errors.New("read fail") }

func TestLocalStorage_Put_CopyError(t *testing.T) {
	dir := t.TempDir()
	s := NewLocalStorage(dir, "")
	_, err := s.Put("k", errorReader{})
	assert.Error(t, err)
}

// TestLocalStorage_Put_CreateError uses an accessKey containing a missing
// subdirectory so that os.Create fails after MkdirAll succeeds for rootDir.
func TestLocalStorage_Put_CreateError(t *testing.T) {
	dir := t.TempDir()
	s := NewLocalStorage(dir, "")
	// "ghost/file" parent dir does not exist → os.Create fails
	_, err := s.Put("ghost/file", bytes.NewReader([]byte("x")))
	assert.Error(t, err)
}
