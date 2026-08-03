package drive

import (
	"context"
	"errors"
)

// ErrMultipartUnsupported is returned when a chunked upload is attempted
// against a Storage backend that does not implement MultipartStorage.
var ErrMultipartUnsupported = errors.New("storage backend does not support multipart upload")

// UploadedPart identifies one completed part of a multipart upload.
//
// ETag is echoed back to the backend at completion time; the backend verifies
// it against the bytes it actually holds, so a mismatch between our recorded
// state and the stored parts fails the completion rather than assembling a
// corrupt object.
type UploadedPart struct {
	PartNumber int32
	ETag       string
}

// MultipartStorage is an optional capability of Storage: uploading one object
// across several requests. It backs the chunked upload endpoints (#2313), which
// exist to get past reverse-proxy body size limits (Cloudflare caps bodies at
// 100MB on Free/Pro).
//
// Storage 本体 (Put / Get / Delete) は変更していない。LocalStorage は本 interface
// を実装しないので、型アサーションで能力を判定して未対応構成では機能ごと無効に
// できる (= /api/meta の能力告知を出さない → frontend は単発アップロードに倒れる)。
//
// パートサイズの制約は実装ごとに揃っていない。AWS S3 は最終パート以外 5 MiB 以上
// を要求し、Cloudflare R2 は加えて**最終パート以外すべて同一サイズ**を要求する。
// 呼び出し側は固定サイズのパートを順番に送ること。
type MultipartStorage interface {
	// CreateMultipartUpload begins a multipart upload and returns the backend's
	// upload id. contentType is fixed here, so callers must sniff it from the
	// first bytes before calling.
	CreateMultipartUpload(ctx context.Context, accessKey, contentType string) (string, error)
	// UploadPart stores one part. partNumber is 1-based, as required by S3.
	UploadPart(ctx context.Context, accessKey, uploadID string, partNumber int32, body []byte) (string, error)
	// CompleteMultipartUpload assembles the parts and returns the public URL of
	// the resulting object, matching what Put returns.
	CompleteMultipartUpload(ctx context.Context, accessKey, uploadID string, parts []UploadedPart) (string, error)
	// AbortMultipartUpload discards an incomplete upload. Object storage bills
	// for incomplete multipart uploads, so this must run on every abandonment
	// path.
	AbortMultipartUpload(ctx context.Context, accessKey, uploadID string) error
}
