package server

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	coredrive "github.com/shiroha-a/mk/internal/core/drive"
	"github.com/shiroha-a/mk/internal/model"
)

// stubFilesLookup is a minimal stand-in for filesDriveLookup. We hand-roll
// the stub rather than reuse testutil.MockDriveFileRepository so the test
// stays focused on the handler's storage-switching contract.
type stubFilesLookup struct {
	byKey map[string]*model.DriveFile
	err   error
}

func (s *stubFilesLookup) FindByAnyAccessKey(key string) (*model.DriveFile, error) {
	if s.err != nil {
		return nil, s.err
	}
	f, ok := s.byKey[key]
	if !ok {
		return nil, errors.New("not found")
	}
	return f, nil
}

// memStorage is an in-memory coredrive.Storage so handler tests do not
// touch the filesystem. byKey は accessKey → body の map で、Get で
// io.NopCloser に包んで返す (non-seekable 経路を試すため敢えて *os.File
// を使わない)。
type memStorage struct {
	byKey map[string]string
}

func (m *memStorage) Put(string, io.Reader) (string, error) { return "", nil }
func (m *memStorage) Get(key string) (io.ReadCloser, error) {
	body, ok := m.byKey[key]
	if !ok {
		return nil, coredrive.ErrObjectNotFound
	}
	return io.NopCloser(strings.NewReader(body)), nil
}
func (m *memStorage) Delete(string) error { return nil }

func newFilesTestContext(t *testing.T, key string) (echo.Context, *httptest.ResponseRecorder) {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/files/"+key, nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/files/:accessKey")
	c.SetParamNames("accessKey")
	c.SetParamValues(key)
	return c, rec
}

// useObjectStorage 相当 wiring: primary が S3 (storedInternal=false 行のみ
// 持つ) で local が在来の LocalStorage (storedInternal=true 行を持つ)。
// storedInternal=true の row は local から、それ以外は primary から提供
// されることを契約する (#1414)。
func TestFilesHandler_StoredInternalServesFromLocal(t *testing.T) {
	key := "internal-key"
	lookup := &stubFilesLookup{
		byKey: map[string]*model.DriveFile{
			key: {ID: "f1", StoredInternal: true},
		},
	}
	primary := &memStorage{byKey: map[string]string{}}
	local := &memStorage{byKey: map[string]string{key: "local-body"}}

	c, rec := newFilesTestContext(t, key)
	h := filesHandler(lookup, primary, local)
	require.NoError(t, h(c))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "local-body", rec.Body.String())
	assert.Equal(t, "max-age=31536000, immutable, no-transform", rec.Header().Get("Cache-Control"))
}

func TestFilesHandler_NonInternalServesFromPrimary(t *testing.T) {
	key := "s3-key"
	lookup := &stubFilesLookup{
		byKey: map[string]*model.DriveFile{
			key: {ID: "f2", StoredInternal: false},
		},
	}
	primary := &memStorage{byKey: map[string]string{key: "s3-body"}}
	local := &memStorage{byKey: map[string]string{}}

	c, rec := newFilesTestContext(t, key)
	h := filesHandler(lookup, primary, local)
	require.NoError(t, h(c))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "s3-body", rec.Body.String())
}

// DB に行がなければ primary に倒す (旧挙動互換)。
func TestFilesHandler_LookupMissFallsBackToPrimary(t *testing.T) {
	key := "orphan-key"
	lookup := &stubFilesLookup{byKey: map[string]*model.DriveFile{}}
	primary := &memStorage{byKey: map[string]string{key: "primary-body"}}
	local := &memStorage{byKey: map[string]string{key: "should-not-be-used"}}

	c, rec := newFilesTestContext(t, key)
	h := filesHandler(lookup, primary, local)
	require.NoError(t, h(c))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "primary-body", rec.Body.String())
}

// lookup を未配線にしても primary 経路で素通る (useObjectStorage=false な
// 旧 wiring の最低保証)。
func TestFilesHandler_NilLookupUsesPrimary(t *testing.T) {
	key := "k"
	primary := &memStorage{byKey: map[string]string{key: "p"}}
	local := &memStorage{byKey: map[string]string{key: "l"}}

	c, rec := newFilesTestContext(t, key)
	h := filesHandler(nil, primary, local)
	require.NoError(t, h(c))

	assert.Equal(t, "p", rec.Body.String())
}

// primary が ErrObjectNotFound を返すと 404 を返す。
func TestFilesHandler_PrimaryMissReturns404(t *testing.T) {
	key := "missing"
	lookup := &stubFilesLookup{byKey: map[string]*model.DriveFile{}}
	primary := &memStorage{byKey: map[string]string{}}
	local := &memStorage{byKey: map[string]string{}}

	c, rec := newFilesTestContext(t, key)
	h := filesHandler(lookup, primary, local)
	require.NoError(t, h(c))

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// fileStorage は Get で実 *os.File を返す storage 実装。filesHandler の
// `body.(*os.File)` で modtime を引く branch を踏むために用意する。
type fileStorage struct {
	root string
}

func (s *fileStorage) Put(string, io.Reader) (string, error) { return "", nil }
func (s *fileStorage) Get(key string) (io.ReadCloser, error) {
	f, err := os.Open(filepath.Join(s.root, key))
	if err != nil {
		return nil, coredrive.ErrObjectNotFound
	}
	return f, nil
}
func (s *fileStorage) Delete(string) error { return nil }

// LocalStorage 互換経路 (*os.File を返す storage) で Stat() からの
// modtime 取得と Last-Modified 出力経路まで踏む。
func TestFilesHandler_OsFileSeekablePath(t *testing.T) {
	dir := t.TempDir()
	key := "local-os-key"
	require.NoError(t, os.WriteFile(filepath.Join(dir, key), []byte("local-bytes"), 0o644))

	lookup := &stubFilesLookup{byKey: map[string]*model.DriveFile{
		key: {ID: "f3", StoredInternal: true},
	}}
	primary := &memStorage{byKey: map[string]string{}}
	local := &fileStorage{root: dir}

	c, rec := newFilesTestContext(t, key)
	h := filesHandler(lookup, primary, local)
	require.NoError(t, h(c))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "local-bytes", rec.Body.String())
	assert.NotEmpty(t, rec.Header().Get(echo.HeaderLastModified))
}

// MIME 判定: PNG signature を返した場合は image/png を Content-Type に
// 出すこと (DetectContentType の経路を通すための smoke test)。
func TestFilesHandler_SetsDetectedContentType(t *testing.T) {
	key := "png-key"
	// PNG signature header (8 bytes) で http.DetectContentType が image/png
	// を返す経路を踏む。
	pngHeader := "\x89PNG\r\n\x1a\n"
	primary := &memStorage{byKey: map[string]string{key: pngHeader + "..."}}
	c, rec := newFilesTestContext(t, key)
	h := filesHandler(nil, primary, nil)
	require.NoError(t, h(c))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "image/png", rec.Header().Get(echo.HeaderContentType))
}

// #2106 H3: 非 browser-safe MIME (HTML/SVG 等) は application/octet-stream に矯正し、
// CSP / nosniff 防御 header を付けて stored XSS を防ぐ。
func TestFilesHandler_NonBrowserSafeIsOctetStreamWithCSP(t *testing.T) {
	key := "html-key"
	htmlBody := "<html><body><script>alert(1)</script></body></html>"
	primary := &memStorage{byKey: map[string]string{key: htmlBody}}
	c, rec := newFilesTestContext(t, key)
	h := filesHandler(nil, primary, nil)
	require.NoError(t, h(c))

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/octet-stream", rec.Header().Get(echo.HeaderContentType),
		"HTML must be served as octet-stream, not text/html (XSS)")
	assert.Contains(t, rec.Header().Get("Content-Security-Policy"), "default-src 'none'")
	assert.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
}
