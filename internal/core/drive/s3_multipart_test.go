package drive

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newMultipartStorage(mock *mockS3API, publicRead bool) *S3Storage {
	return NewS3Storage(S3StorageConfig{
		Client:        mock,
		Bucket:        "my-bucket",
		Prefix:        "files/",
		BaseURL:       "https://cdn.example.com",
		SetPublicRead: publicRead,
	})
}

// CreateMultipartUpload は Put と同じ header を付ける。complete 後の object が
// 単発アップロードと区別できない状態になっていること。
func TestS3Storage_CreateMultipartUpload(t *testing.T) {
	mock := &mockS3API{createUploadID: "upload-1"}
	st := newMultipartStorage(mock, false)

	id, err := st.CreateMultipartUpload(context.Background(), "abc123", "video/mp4")
	require.NoError(t, err)
	assert.Equal(t, "upload-1", id)

	require.NotNil(t, mock.createInput)
	assert.Equal(t, "my-bucket", *mock.createInput.Bucket)
	assert.Equal(t, "files/abc123", *mock.createInput.Key)
	assert.Equal(t, "video/mp4", *mock.createInput.ContentType)
	assert.Equal(t, "inline", *mock.createInput.ContentDisposition)
	assert.Equal(t, "max-age=31536000, immutable", *mock.createInput.CacheControl)
	assert.Empty(t, mock.createInput.ACL)
}

func TestS3Storage_CreateMultipartUpload_PublicRead(t *testing.T) {
	mock := &mockS3API{createUploadID: "upload-1"}
	st := newMultipartStorage(mock, true)

	_, err := st.CreateMultipartUpload(context.Background(), "abc123", "image/png")
	require.NoError(t, err)
	assert.Equal(t, types.ObjectCannedACLPublicRead, mock.createInput.ACL)
}

func TestS3Storage_CreateMultipartUpload_Error(t *testing.T) {
	mock := &mockS3API{createErr: errors.New("access denied")}
	st := newMultipartStorage(mock, false)

	_, err := st.CreateMultipartUpload(context.Background(), "abc123", "image/png")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "s3 create multipart upload")
}

// UploadId が空で返る backend は使えないので、その場でエラーにする。空文字を
// 通すと以降の UploadPart / Complete が全部失敗し、原因が分かりにくくなる。
func TestS3Storage_CreateMultipartUpload_EmptyUploadID(t *testing.T) {
	mock := &mockS3API{createUploadID: ""}
	st := newMultipartStorage(mock, false)

	_, err := st.CreateMultipartUpload(context.Background(), "abc123", "image/png")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty upload id")
}

// SigV4 payload hash のため Body は io.Seeker でなければならない (#523 と同じ制約)。
func TestS3Storage_UploadPart(t *testing.T) {
	mock := &mockS3API{uploadETag: `"etag-1"`}
	st := newMultipartStorage(mock, false)

	etag, err := st.UploadPart(context.Background(), "abc123", "upload-1", 3, []byte("payload"))
	require.NoError(t, err)
	assert.Equal(t, `"etag-1"`, etag)

	require.Len(t, mock.uploadInputs, 1)
	in := mock.uploadInputs[0]
	assert.Equal(t, "files/abc123", *in.Key)
	assert.Equal(t, "upload-1", *in.UploadId)
	assert.Equal(t, int32(3), *in.PartNumber)

	seeker, ok := in.Body.(io.Seeker)
	require.True(t, ok, "UploadPartInput.Body must implement io.Seeker for SigV4 payload hash")
	_, err = seeker.Seek(0, io.SeekStart)
	require.NoError(t, err)
	got, err := io.ReadAll(in.Body)
	require.NoError(t, err)
	assert.Equal(t, []byte("payload"), got)
}

func TestS3Storage_UploadPart_Error(t *testing.T) {
	mock := &mockS3API{uploadErr: errors.New("timeout")}
	st := newMultipartStorage(mock, false)

	_, err := st.UploadPart(context.Background(), "abc123", "upload-1", 1, []byte("x"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "s3 upload part 1")
}

func TestS3Storage_UploadPart_EmptyETag(t *testing.T) {
	mock := &mockS3API{uploadETag: ""}
	st := newMultipartStorage(mock, false)

	_, err := st.UploadPart(context.Background(), "abc123", "upload-1", 1, []byte("x"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty etag")
}

// ETag を渡すことで backend が実体と突き合わせられる。渡し忘れると DB の記録と
// storage の実体がずれても complete が通ってしまう。
func TestS3Storage_CompleteMultipartUpload(t *testing.T) {
	mock := &mockS3API{}
	st := newMultipartStorage(mock, false)

	url, err := st.CompleteMultipartUpload(context.Background(), "abc123", "upload-1", []UploadedPart{
		{PartNumber: 1, ETag: `"e1"`},
		{PartNumber: 2, ETag: `"e2"`},
	})
	require.NoError(t, err)
	assert.Equal(t, "https://cdn.example.com/files/abc123", url)

	require.NotNil(t, mock.completeInput)
	assert.Equal(t, "upload-1", *mock.completeInput.UploadId)
	require.Len(t, mock.completeInput.MultipartUpload.Parts, 2)
	assert.Equal(t, int32(1), *mock.completeInput.MultipartUpload.Parts[0].PartNumber)
	assert.Equal(t, `"e1"`, *mock.completeInput.MultipartUpload.Parts[0].ETag)
	assert.Equal(t, int32(2), *mock.completeInput.MultipartUpload.Parts[1].PartNumber)
}

func TestS3Storage_CompleteMultipartUpload_Error(t *testing.T) {
	mock := &mockS3API{completeErr: errors.New("InvalidPart")}
	st := newMultipartStorage(mock, false)

	_, err := st.CompleteMultipartUpload(context.Background(), "abc123", "upload-1", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "s3 complete multipart upload")
}

func TestS3Storage_AbortMultipartUpload(t *testing.T) {
	mock := &mockS3API{}
	st := newMultipartStorage(mock, false)

	require.NoError(t, st.AbortMultipartUpload(context.Background(), "abc123", "upload-1"))
	require.NotNil(t, mock.abortInput)
	assert.Equal(t, "files/abc123", *mock.abortInput.Key)
	assert.Equal(t, "upload-1", *mock.abortInput.UploadId)
}

func TestS3Storage_AbortMultipartUpload_Error(t *testing.T) {
	mock := &mockS3API{abortErr: errors.New("nope")}
	st := newMultipartStorage(mock, false)

	err := st.AbortMultipartUpload(context.Background(), "abc123", "upload-1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "s3 abort multipart upload")
}

// LocalStorage は MultipartStorage を満たさない。これが崩れると、ローカル
// ストレージ構成でも能力告知が出てしまう。
func TestLocalStorage_IsNotMultipartCapable(t *testing.T) {
	var s Storage = NewLocalStorage(t.TempDir(), "https://example.com/files")
	_, ok := s.(MultipartStorage)
	assert.False(t, ok)
}
