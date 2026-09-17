package server

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
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

// #2315: primary が現時点でローカルなら local と同じ FS を指すので
// storedInternal 判定は無意味であり、ホットパスの DB クエリを省く。
// backend は admin 設定で動的に切り替わるので、この判定は配線時に固定できない。
func TestFilesHandler_SkipsLookupWhilePrimaryIsLocal(t *testing.T) {
	key := "k"
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, key), []byte("from-local-fs"), 0o644))
	local := coredrive.NewLocalStorage(dir, "https://example.com/files")

	lookup := &countingFilesLookup{inner: &stubFilesLookup{byKey: map[string]*model.DriveFile{
		key: {ID: "f1", StoredInternal: true},
	}}}

	// primary がローカル (object storage 無効) の間は lookup を引かない。
	c, rec := newFilesTestContext(t, key)
	require.NoError(t, filesHandler(lookup, local, local)(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "from-local-fs", rec.Body.String())
	assert.Equal(t, 0, lookup.calls, "primary がローカルなら DB を引かない")
}

// object storage を有効にしたら、同じ配線のまま lookup が効いて
// storedInternal=true の既存ファイルがローカルから提供されること。
func TestFilesHandler_LookupEngagesWhenPrimaryBecomesRemote(t *testing.T) {
	key := "k"
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, key), []byte("from-local-fs"), 0o644))
	local := coredrive.NewLocalStorage(dir, "https://example.com/files")

	current := &model.Meta{ID: "x"}
	primary := coredrive.NewMetaStorage(func() (*model.Meta, error) { return current, nil }, dir, "https://example.com/files")
	lookup := &countingFilesLookup{inner: &stubFilesLookup{byKey: map[string]*model.DriveFile{
		key: {ID: "f1", StoredInternal: true},
	}}}
	h := filesHandler(lookup, primary, local)

	c, rec := newFilesTestContext(t, key)
	require.NoError(t, h(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, 0, lookup.calls, "無効の間は DB を引かない")

	// 再起動なしで有効化する。
	current = &model.Meta{
		ID:                     "x",
		UseObjectStorage:       true,
		ObjectStorageBucket:    strPtr("b"),
		ObjectStorageEndpoint:  strPtr("s3.example.test"),
		ObjectStorageAccessKey: strPtr("AK"),
		ObjectStorageSecretKey: strPtr("SK"),
		ObjectStorageUseSSL:    true,
	}

	c, rec = newFilesTestContext(t, key)
	require.NoError(t, h(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "from-local-fs", rec.Body.String(), "storedInternal=true の既存ファイルはローカルから提供される")
	assert.Equal(t, 1, lookup.calls, "有効化後は DB を引いて local へ振り分ける")
}

// countingFilesLookup records how many times the handler consulted the DB.
type countingFilesLookup struct {
	inner *stubFilesLookup
	calls int
}

func (c *countingFilesLookup) FindByAnyAccessKey(key string) (*model.DriveFile, error) {
	c.calls++
	return c.inner.FindByAnyAccessKey(key)
}

// failingStorage always fails with a non-not-found error, standing in for an
// S3 credential expiry / throttling / 5xx.
type failingStorage struct{ err error }

func (f *failingStorage) Put(string, io.Reader) (string, error) { return "", nil }
func (f *failingStorage) Get(string) (io.ReadCloser, error)     { return nil, f.err }
func (f *failingStorage) Delete(string) error                   { return nil }

// **ストレージ障害を「無い」に潰さない (#2792)。** S3 の認証失効 / throttling /
// 5xx が全部 404 になると、クライアントからは「消えた」と区別が付かないうえ、
// **監視にも 5xx が立たない**。#2990 が `S3Storage.Get` で種別を分けた目的
// そのもので、他の呼び出し側は全部直っているのにここだけ残っていた。
func TestFilesHandler_StorageFailureIs500(t *testing.T) {
	lookup := &stubFilesLookup{byKey: map[string]*model.DriveFile{}}
	primary := &failingStorage{err: errors.New("AccessDenied: token expired")}
	local := &memStorage{byKey: map[string]string{}}

	c, rec := newFilesTestContext(t, "k1")
	h := filesHandler(lookup, primary, local)
	require.NoError(t, h(c))

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

// **wrap された ErrObjectNotFound は 404 のまま。** `errors.Is` ではなく `==`
// で比べる実装だと、ここで 500 に化ける。
func TestFilesHandler_WrappedNotFoundStays404(t *testing.T) {
	lookup := &stubFilesLookup{byKey: map[string]*model.DriveFile{}}
	primary := &failingStorage{err: fmt.Errorf("s3 get k1: %w", coredrive.ErrObjectNotFound)}
	local := &memStorage{byKey: map[string]string{}}

	c, rec := newFilesTestContext(t, "k1")
	h := filesHandler(lookup, primary, local)
	require.NoError(t, h(c))

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// **オブジェクトを名指しできないキーは、引く前に 404 (#3037)。**
//
// `/files` は `api` グループの外で**認証もレートリミットも無い**。列に入らない
// キーを storage へ渡すと `LocalStorage.Get` が `os.IsNotExist` 以外
// (`ENAMETOOLONG` / `EINVAL`) を素のエラーで返し、500 + accessKey を丸ごと
// 載せた Error ログになる。`GET /files/a%00b` を叩くだけで 5xx レートと
// ログを誰でも汚せた (#3025 が塞いだ形の再導入)。
func TestFilesHandler_UnstorableAccessKeyIs404(t *testing.T) {
	primary := &failingStorage{err: errors.New("must not be reached")}
	local := &memStorage{byKey: map[string]string{}}

	for name, key := range map[string]string{
		"NUL":           "a\x00b",
		"invalid UTF-8": "a\x80b",
		"too long":      strings.Repeat("a", 257),
		"empty":         "",
		"dot":           ".",
		"dotdot":        "..",
		"slash":         "a/b",
		"backslash":     `a\b`,
	} {
		t.Run(name, func(t *testing.T) {
			// **URL は percent-encode した形で組む。** 実際の経路では echo が
			// decode して param に入れるので、param には生の値を渡す
			// (`httptest.NewRequest` は生の制御文字を含む URL で panic する)。
			e := echo.New()
			req := httptest.NewRequest(http.MethodGet, "/files/"+url.PathEscape(key), nil)
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)
			c.SetPath("/files/:accessKey")
			c.SetParamNames("accessKey")
			c.SetParamValues(key)

			h := filesHandler(nil, primary, local)
			require.NoError(t, h(c))
			assert.Equal(t, http.StatusNotFound, rec.Code, "storage を触ってしまっている")
		})
	}
}

// **普通のキーは通ったまま。** これが無いと「常に 404」でも上のテストが通る。
func TestFilesHandler_OrdinaryAccessKeyStillServed(t *testing.T) {
	primary := &memStorage{byKey: map[string]string{"abcdef123": "hello"}}
	local := &memStorage{byKey: map[string]string{}}

	c, rec := newFilesTestContext(t, "abcdef123")
	h := filesHandler(nil, primary, local)
	require.NoError(t, h(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "hello", rec.Body.String())
}
