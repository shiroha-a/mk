package drive

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"testing"

	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
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

	// multipart (#2313)
	createInput    *s3.CreateMultipartUploadInput
	createUploadID string
	createErr      error
	uploadInputs   []*s3.UploadPartInput
	uploadETag     string
	uploadErr      error
	completeInput  *s3.CompleteMultipartUploadInput
	completeErr    error
	abortInput     *s3.AbortMultipartUploadInput
	abortErr       error
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

func (m *mockS3API) CreateMultipartUpload(_ context.Context, input *s3.CreateMultipartUploadInput, _ ...func(*s3.Options)) (*s3.CreateMultipartUploadOutput, error) {
	m.createInput = input
	if m.createErr != nil {
		return nil, m.createErr
	}
	id := m.createUploadID
	return &s3.CreateMultipartUploadOutput{UploadId: &id}, nil
}

func (m *mockS3API) UploadPart(_ context.Context, input *s3.UploadPartInput, _ ...func(*s3.Options)) (*s3.UploadPartOutput, error) {
	m.uploadInputs = append(m.uploadInputs, input)
	if m.uploadErr != nil {
		return nil, m.uploadErr
	}
	etag := m.uploadETag
	return &s3.UploadPartOutput{ETag: &etag}, nil
}

func (m *mockS3API) CompleteMultipartUpload(_ context.Context, input *s3.CompleteMultipartUploadInput, _ ...func(*s3.Options)) (*s3.CompleteMultipartUploadOutput, error) {
	m.completeInput = input
	if m.completeErr != nil {
		return nil, m.completeErr
	}
	return &s3.CompleteMultipartUploadOutput{}, nil
}

func (m *mockS3API) AbortMultipartUpload(_ context.Context, input *s3.AbortMultipartUploadInput, _ ...func(*s3.Options)) (*s3.AbortMultipartUploadOutput, error) {
	m.abortInput = input
	if m.abortErr != nil {
		return nil, m.abortErr
	}
	return &s3.AbortMultipartUploadOutput{}, nil
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

// **「無い」と「読めない」を分ける (#2990)。** すべて ErrObjectNotFound に潰すと、
// 認証切れや provider の 5xx が「そんなオブジェクトは無い」に化け、呼び出し側が
// 404 を返す / 申請を却下扱いにする / 「絵文字を消せ」と案内する、という危険側の
// 判断をする (#2792)。
func TestS3Storage_Get_ErrorClassification(t *testing.T) {
	notFound := func(code string, status int) error {
		return &awshttp.ResponseError{
			ResponseError: &smithyhttp.ResponseError{
				Response: &smithyhttp.Response{Response: &http.Response{StatusCode: status}},
				Err:      &smithy.GenericAPIError{Code: code},
			},
		}
	}
	bareStatus := func(status int) error {
		return &awshttp.ResponseError{
			ResponseError: &smithyhttp.ResponseError{
				Response: &smithyhttp.Response{Response: &http.Response{StatusCode: status}},
				Err:      errors.New("could not parse response body"),
			},
		}
	}
	for _, tc := range []struct {
		name       string
		err        error
		wantAbsent bool
	}{
		{"NoSuchKey (S3 本家)", &types.NoSuchKey{}, true},
		{"NotFound + HTTP 404", notFound("NotFound", http.StatusNotFound), true},
		{"NotFound コードだけ", &smithy.GenericAPIError{Code: "NotFound"}, true},
		// **コードが読めないときだけ status を見る。** endpoint が別の HTTP
		// サーバーを指していて `smithy.APIError` にすらならない 404。
		{"APIError にならない HTTP 404", bareStatus(http.StatusNotFound), true},
		// 空のコードで返す `S3API` 実装 (差し替え可能なので有りうる)。
		{"コードが空 + HTTP 404", notFound("", http.StatusNotFound), true},
		{"コードが空 + HTTP 500", notFound("", http.StatusInternalServerError), false},
		// **`NoSuchBucket` は 404 だが「無い」ではない。** バケット名の設定誤りで、
		// 倒すと 1 文字の typo で全オブジェクトが「消えた」と判定される。
		{"NoSuchBucket + HTTP 404", notFound("NoSuchBucket", http.StatusNotFound), false},
		{"知らないコード + HTTP 404", notFound("SlowDown", http.StatusNotFound), false},
		{"AccessDenied (403)", notFound("AccessDenied", http.StatusForbidden), false},
		{"provider の 5xx", notFound("InternalError", http.StatusInternalServerError), false},
		{"接続断", errors.New("dial tcp: connection refused"), false},
		// 文字列に NoSuchKey を含むだけの素のエラーは「無い」と断定できない。
		{"素のエラー (文字列だけ一致)", errors.New("NoSuchKey"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := NewS3Storage(S3StorageConfig{Client: &mockS3API{getErr: tc.err}, Bucket: "bucket"})
			_, err := st.Get("k")
			require.Error(t, err)
			if tc.wantAbsent {
				assert.ErrorIs(t, err, ErrObjectNotFound)
				return
			}
			assert.NotErrorIs(t, err, ErrObjectNotFound,
				"障害を not-found に潰している (呼び出し側が危険側に倒れる)")
			assert.ErrorIs(t, err, tc.err, "元のエラーが辿れない")
		})
	}
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
