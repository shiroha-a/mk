package drive

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// mock S3API
// ---------------------------------------------------------------------------

type mockS3API struct {
	putInput  *s3.PutObjectInput
	putErr    error
	getBody   io.ReadCloser
	getErr    error
	deleteErr error
}

func (m *mockS3API) PutObject(_ context.Context, input *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	m.putInput = input
	return &s3.PutObjectOutput{}, m.putErr
}

func (m *mockS3API) GetObject(_ context.Context, _ *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	if m.getErr != nil {
		return nil, m.getErr
	}
	return &s3.GetObjectOutput{Body: m.getBody}, nil
}

func (m *mockS3API) DeleteObject(_ context.Context, _ *s3.DeleteObjectInput, _ ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	return &s3.DeleteObjectOutput{}, m.deleteErr
}

// ---------------------------------------------------------------------------
// S3Storage tests
// ---------------------------------------------------------------------------

func TestS3Storage_Put_HappyPath(t *testing.T) {
	mock := &mockS3API{}
	st := NewS3Storage(S3StorageConfig{
		Client:  mock,
		Bucket:  "my-bucket",
		Prefix:  "files/",
		BaseURL: "https://cdn.example.com",
	})

	url, err := st.Put("abc123", bytes.NewReader([]byte("hello")))
	require.NoError(t, err)
	assert.Equal(t, "https://cdn.example.com/files/abc123", url)

	// S3 PutObject のパラメータ確認
	require.NotNil(t, mock.putInput)
	assert.Equal(t, "my-bucket", *mock.putInput.Bucket)
	assert.Equal(t, "files/abc123", *mock.putInput.Key)
	assert.Equal(t, "max-age=31536000, immutable", *mock.putInput.CacheControl)
	assert.NotEmpty(t, *mock.putInput.ContentType)
	assert.Empty(t, mock.putInput.ACL) // setPublicRead=false
}

func TestS3Storage_Put_WithPublicRead(t *testing.T) {
	mock := &mockS3API{}
	st := NewS3Storage(S3StorageConfig{
		Client:        mock,
		Bucket:        "bucket",
		BaseURL:       "https://cdn.example.com",
		SetPublicRead: true,
	})

	_, err := st.Put("key", bytes.NewReader([]byte("x")))
	require.NoError(t, err)
	assert.Equal(t, types.ObjectCannedACLPublicRead, mock.putInput.ACL)
}

func TestS3Storage_Put_NoBaseURL(t *testing.T) {
	mock := &mockS3API{}
	st := NewS3Storage(S3StorageConfig{
		Client: mock,
		Bucket: "my-bucket",
		Prefix: "uploads/",
	})

	url, err := st.Put("key1", bytes.NewReader([]byte("data")))
	require.NoError(t, err)
	assert.Equal(t, "https://s3.amazonaws.com/my-bucket/uploads/key1", url)
}

func TestS3Storage_Put_NoPrefix(t *testing.T) {
	mock := &mockS3API{}
	st := NewS3Storage(S3StorageConfig{
		Client:  mock,
		Bucket:  "bucket",
		BaseURL: "https://cdn.example.com",
	})

	url, err := st.Put("key1", bytes.NewReader([]byte("x")))
	require.NoError(t, err)
	assert.Equal(t, "https://cdn.example.com/key1", url)
}

func TestS3Storage_Put_Error(t *testing.T) {
	mock := &mockS3API{putErr: errors.New("access denied")}
	st := NewS3Storage(S3StorageConfig{
		Client:  mock,
		Bucket:  "bucket",
		BaseURL: "https://cdn.example.com",
	})

	_, err := st.Put("key1", bytes.NewReader([]byte("x")))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "s3 put")
}

func TestS3Storage_Put_TrailingSlashBaseURL(t *testing.T) {
	mock := &mockS3API{}
	st := NewS3Storage(S3StorageConfig{
		Client:  mock,
		Bucket:  "bucket",
		BaseURL: "https://cdn.example.com/",
	})

	url, err := st.Put("key1", bytes.NewReader([]byte("x")))
	require.NoError(t, err)
	assert.Equal(t, "https://cdn.example.com/key1", url)
}

func TestS3Storage_Get_HappyPath(t *testing.T) {
	body := io.NopCloser(bytes.NewReader([]byte("file content")))
	mock := &mockS3API{getBody: body}
	st := NewS3Storage(S3StorageConfig{
		Client: mock,
		Bucket: "bucket",
		Prefix: "files/",
	})

	rc, err := st.Get("key1")
	require.NoError(t, err)
	defer rc.Close()
	data, _ := io.ReadAll(rc)
	assert.Equal(t, "file content", string(data))
}

func TestS3Storage_Get_NotFound(t *testing.T) {
	mock := &mockS3API{getErr: errors.New("NoSuchKey")}
	st := NewS3Storage(S3StorageConfig{
		Client: mock,
		Bucket: "bucket",
	})

	_, err := st.Get("missing")
	assert.ErrorIs(t, err, ErrObjectNotFound)
}

func TestS3Storage_Delete_HappyPath(t *testing.T) {
	mock := &mockS3API{}
	st := NewS3Storage(S3StorageConfig{
		Client: mock,
		Bucket: "bucket",
		Prefix: "files/",
	})

	err := st.Delete("key1")
	assert.NoError(t, err)
}

func TestS3Storage_Delete_Error(t *testing.T) {
	mock := &mockS3API{deleteErr: errors.New("forbidden")}
	st := NewS3Storage(S3StorageConfig{
		Client: mock,
		Bucket: "bucket",
	})

	err := st.Delete("key1")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "s3 delete")
}

// S3 SDK は SigV4 payload hash 計算で Body を seek する。Body が
// io.Seeker を満たしていなかった旧実装では SDK が即 fail していた (#523)。
// 修正後の Put が PutObjectInput.Body に bytes.Reader (= io.Seeker 実装) を
// 渡すこと、そして body 全文がそのまま伝わることを担保する。
func TestS3Storage_Put_BodyIsSeekable(t *testing.T) {
	mock := &mockS3API{}
	st := NewS3Storage(S3StorageConfig{
		Client: mock,
		Bucket: "bucket",
	})
	payload := []byte("hello world payload >= 11 bytes")
	// 渡す側は Seeker 非対応な io.Reader にして、Put 内部で seekable に
	// 変換されることを確認する。
	_, err := st.Put("k1", io.MultiReader(bytes.NewReader(payload)))
	require.NoError(t, err)
	require.NotNil(t, mock.putInput)

	body, ok := mock.putInput.Body.(io.Seeker)
	require.True(t, ok, "PutObjectInput.Body must implement io.Seeker for SigV4 payload hash")

	// Body 内容が body 全文と一致すること
	got, err := io.ReadAll(mock.putInput.Body)
	require.NoError(t, err)
	assert.Equal(t, payload, got)

	// seek し直しても再読み込みできる (SDK の retry middleware が seek する)
	_, err = body.Seek(0, io.SeekStart)
	require.NoError(t, err)
	again, _ := io.ReadAll(mock.putInput.Body)
	assert.Equal(t, payload, again)
}

// 512 byte 未満の小さい body でも正しく MIME 判定されて upload されること。
func TestS3Storage_Put_SmallBody(t *testing.T) {
	mock := &mockS3API{}
	st := NewS3Storage(S3StorageConfig{Client: mock, Bucket: "bucket"})
	_, err := st.Put("k", bytes.NewReader([]byte("tiny")))
	require.NoError(t, err)
	require.NotNil(t, mock.putInput)
	assert.NotEmpty(t, *mock.putInput.ContentType)
}

func TestS3Storage_ObjectKey(t *testing.T) {
	st := NewS3Storage(S3StorageConfig{Prefix: "uploads/"})
	assert.Equal(t, "uploads/abc", st.objectKey("abc"))

	st2 := NewS3Storage(S3StorageConfig{})
	assert.Equal(t, "abc", st2.objectKey("abc"))
}

// Misskey TS の admin UI placeholder は "files" (末尾 / なし) なので、
// operator がそのまま入れた値でも `<prefix>/<accessKey>` が生成されること。
// これが破れると drop-in 互換が永続的に壊れる (#525)。
func TestS3Storage_ObjectKey_PrefixWithoutTrailingSlash(t *testing.T) {
	st := NewS3Storage(S3StorageConfig{Prefix: "files"})
	assert.Equal(t, "files/abc123", st.objectKey("abc123"),
		"prefix without trailing slash must still produce 'files/abc123' (TS-compat)")
}

func TestS3Storage_ObjectKey_PrefixWithMultipleTrailingSlashes(t *testing.T) {
	// "files///" のような誤入力でも separator は 1 個だけになる
	st := NewS3Storage(S3StorageConfig{Prefix: "files///"})
	assert.Equal(t, "files/abc", st.objectKey("abc"))
}

// publicURL が prefix の末尾 / 有無に依らず "<base>/files/<key>" の形に
// なることを担保する。
func TestS3Storage_PublicURL_PrefixWithoutTrailingSlash(t *testing.T) {
	mock := &mockS3API{}
	st := NewS3Storage(S3StorageConfig{
		Client:  mock,
		Bucket:  "my-bucket",
		Prefix:  "files",
		BaseURL: "https://cdn.example.com",
	})
	url, err := st.Put("abc123", bytes.NewReader([]byte("x")))
	require.NoError(t, err)
	assert.Equal(t, "https://cdn.example.com/files/abc123", url)
	require.NotNil(t, mock.putInput)
	assert.Equal(t, "files/abc123", *mock.putInput.Key,
		"S3 PutObject Key must be 'files/abc123' even when operator omits the trailing slash")
}

// #2106 H4: 非 browser-safe MIME (HTML/SVG 等) は S3 object でも octet-stream に
// 矯正し Content-Disposition: inline を付ける (public-read CDN 直配信での stored XSS 防止)。
func TestS3Storage_Put_NonBrowserSafeOctetStream(t *testing.T) {
	mock := &mockS3API{}
	st := NewS3Storage(S3StorageConfig{Client: mock, Bucket: "b", BaseURL: "https://cdn.example.com"})
	_, err := st.Put("htmlkey", bytes.NewReader([]byte("<html><body><script>alert(1)</script></body></html>")))
	require.NoError(t, err)
	require.NotNil(t, mock.putInput)
	assert.Equal(t, "application/octet-stream", *mock.putInput.ContentType, "HTML must be stored as octet-stream")
	require.NotNil(t, mock.putInput.ContentDisposition)
	assert.Equal(t, "inline", *mock.putInput.ContentDisposition)
}

func TestS3Storage_Put_ImageKeepsContentType(t *testing.T) {
	mock := &mockS3API{}
	st := NewS3Storage(S3StorageConfig{Client: mock, Bucket: "b", BaseURL: "https://cdn.example.com"})
	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 16)...)
	_, err := st.Put("pngkey", bytes.NewReader(png))
	require.NoError(t, err)
	assert.Equal(t, "image/png", *mock.putInput.ContentType, "browser-safe image keeps its MIME")
}
