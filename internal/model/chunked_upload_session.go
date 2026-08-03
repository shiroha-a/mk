package model

import (
	"time"

	"gorm.io/datatypes"
)

// ChunkedUploadSession tracks one in-progress chunked upload (#2313).
//
// The row owns the mapping between the opaque session id handed to the client
// and the S3 multipart UploadId, which is never exposed. Sizes are recorded so
// that every append can be validated against the size declared at start.
//
// GORM の CreatedAt / UpdatedAt 自動更新は使わず、`Updates` で明示的に書く。
// append は楽観ロック付きの UPDATE 一発で完結させたいので、hook 経由の暗黙
// 更新が混ざると挙動が読みにくくなる。
type ChunkedUploadSession struct {
	ID        string    `gorm:"column:id;primaryKey;type:varchar(32)" json:"id"`
	CreatedAt time.Time `gorm:"column:createdAt;autoCreateTime:false" json:"createdAt"`
	UpdatedAt time.Time `gorm:"column:updatedAt;autoUpdateTime:false;autoCreateTime:false" json:"updatedAt"`
	ExpiresAt time.Time `gorm:"column:expiresAt" json:"expiresAt"`

	UserID      string  `gorm:"column:userId;type:varchar(32)" json:"userId"`
	Name        string  `gorm:"column:name;type:varchar(256)" json:"name"`
	Comment     *string `gorm:"column:comment;type:varchar(512)" json:"comment"`
	FolderID    *string `gorm:"column:folderId;type:varchar(32)" json:"folderId"`
	IsSensitive bool    `gorm:"column:isSensitive" json:"isSensitive"`
	Force       bool    `gorm:"column:force" json:"force"`

	// TotalSize is the size declared by the client at start. It bounds the
	// cumulative append size; it is never trusted as the real size.
	TotalSize int64 `gorm:"column:totalSize" json:"totalSize"`
	// ChunkSize is pinned at start so that changing the instance setting
	// mid-upload cannot break R2's uniform part size requirement.
	ChunkSize      int64 `gorm:"column:chunkSize" json:"chunkSize"`
	ReceivedBytes  int64 `gorm:"column:receivedBytes" json:"receivedBytes"`
	ReceivedChunks int   `gorm:"column:receivedChunks" json:"receivedChunks"`

	AccessKey string `gorm:"column:accessKey;type:varchar(256)" json:"accessKey"`
	// UploadID is the S3 multipart UploadId. Nil until the first append, which
	// is where the content type is sniffed and CreateMultipartUpload is issued.
	UploadID    *string `gorm:"column:uploadId;type:text" json:"uploadId"`
	ContentType *string `gorm:"column:contentType;type:varchar(256)" json:"contentType"`

	// Parts holds the uploaded parts as a JSON array of ChunkedUploadPart.
	Parts datatypes.JSON `gorm:"column:parts;type:jsonb;default:'[]'" json:"parts"`

	// Finishing is claimed by the first finish request so concurrent finishes
	// cannot create two DriveFile rows from one session.
	Finishing bool `gorm:"column:finishing" json:"finishing"`

	RequestIP      *string        `gorm:"column:requestIp;type:varchar(128)" json:"requestIp"`
	RequestHeaders datatypes.JSON `gorm:"column:requestHeaders;type:jsonb" json:"requestHeaders"`
}

// TableName returns the table name for GORM.
func (ChunkedUploadSession) TableName() string { return "chunked_upload_session" }

// ChunkedUploadPart records one uploaded part of the underlying S3 multipart
// upload.
//
// SHA256 exists so that a retried append carrying different bytes can be
// rejected instead of silently corrupting the assembled file; ETag is what
// CompleteMultipartUpload needs.
type ChunkedUploadPart struct {
	Index  int    `json:"index"`
	ETag   string `json:"etag"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}
