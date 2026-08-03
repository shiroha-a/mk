package drive

import (
	"bytes"
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// S3Storage implements MultipartStorage (#2313).
var _ MultipartStorage = (*S3Storage)(nil)

// CreateMultipartUpload begins a multipart upload for accessKey.
//
// contentType は呼び出し側が先頭バイトから sniff して BrowserSafeContentType に
// 通した値を渡す。ここで矯正しないのは Put と挙動を分けないため — Put 側の
// #2106 H4 対策 (public-read の S3 / CDN 直配信で object の Content-Type が描画を
// 支配するので非 browser-safe を octet-stream に倒す) と同じ値になるよう、
// chunked upload 側でも同じ関数を通してから渡すこと。
func (s *S3Storage) CreateMultipartUpload(ctx context.Context, accessKey, contentType string) (string, error) {
	key := s.objectKey(accessKey)
	input := &s3.CreateMultipartUploadInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
		// Put と同じ header を付ける。complete 後の object が単発アップロードと
		// 区別できない状態になるようにする。
		ContentType:        aws.String(contentType),
		ContentDisposition: aws.String("inline"),
		CacheControl:       aws.String("max-age=31536000, immutable"),
	}
	if s.setPublicRead {
		input.ACL = types.ObjectCannedACLPublicRead
	}
	out, err := s.client.CreateMultipartUpload(ctx, input)
	if err != nil {
		return "", fmt.Errorf("s3 create multipart upload %s: %w", key, err)
	}
	if out == nil || out.UploadId == nil || *out.UploadId == "" {
		return "", fmt.Errorf("s3 create multipart upload %s: empty upload id", key)
	}
	return *out.UploadId, nil
}

// UploadPart stores one part of an in-progress multipart upload.
func (s *S3Storage) UploadPart(ctx context.Context, accessKey, uploadID string, partNumber int32, body []byte) (string, error) {
	key := s.objectKey(accessKey)
	// aws-sdk-go-v2 は SigV4 payload hash のため Body に io.Seeker を要求する
	// (#523 と同じ制約)。bytes.Reader を渡す。
	out, err := s.client.UploadPart(ctx, &s3.UploadPartInput{
		Bucket:     aws.String(s.bucket),
		Key:        aws.String(key),
		UploadId:   aws.String(uploadID),
		PartNumber: aws.Int32(partNumber),
		Body:       bytes.NewReader(body),
	})
	if err != nil {
		return "", fmt.Errorf("s3 upload part %d of %s: %w", partNumber, key, err)
	}
	if out == nil || out.ETag == nil || *out.ETag == "" {
		return "", fmt.Errorf("s3 upload part %d of %s: empty etag", partNumber, key)
	}
	return *out.ETag, nil
}

// CompleteMultipartUpload assembles the parts and returns the public URL.
//
// parts は昇順の PartNumber で渡すこと (S3 は順序を要求する)。ETag を渡すので、
// 記録している状態と backend が実際に保持しているパートがずれていれば S3 側が
// InvalidPart で失敗する。壊れたオブジェクトが組み上がることはない。
func (s *S3Storage) CompleteMultipartUpload(ctx context.Context, accessKey, uploadID string, parts []UploadedPart) (string, error) {
	key := s.objectKey(accessKey)
	completed := make([]types.CompletedPart, 0, len(parts))
	for _, p := range parts {
		completed = append(completed, types.CompletedPart{
			PartNumber: aws.Int32(p.PartNumber),
			ETag:       aws.String(p.ETag),
		})
	}
	_, err := s.client.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket:          aws.String(s.bucket),
		Key:             aws.String(key),
		UploadId:        aws.String(uploadID),
		MultipartUpload: &types.CompletedMultipartUpload{Parts: completed},
	})
	if err != nil {
		return "", fmt.Errorf("s3 complete multipart upload %s: %w", key, err)
	}
	return s.publicURL(accessKey), nil
}

// AbortMultipartUpload discards an incomplete multipart upload.
func (s *S3Storage) AbortMultipartUpload(ctx context.Context, accessKey, uploadID string) error {
	key := s.objectKey(accessKey)
	_, err := s.client.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{
		Bucket:   aws.String(s.bucket),
		Key:      aws.String(key),
		UploadId: aws.String(uploadID),
	})
	if err != nil {
		return fmt.Errorf("s3 abort multipart upload %s: %w", key, err)
	}
	return nil
}
