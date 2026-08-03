package drive

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"io"
)

// FileInfo holds the result of analysing an uploaded file's bytes.
type FileInfo struct {
	Body     []byte
	Size     int
	MD5      string
	MimeType string
}

// AnalyseFile reads the entire body and computes its size, md5 hash, and mime
// type. Phase 2.5 はメモリにバッファするシンプル実装。大きいファイルは将来
// streaming に置き換える。
func AnalyseFile(body io.Reader) (*FileInfo, error) {
	buf := &bytes.Buffer{}
	if _, err := io.Copy(buf, body); err != nil {
		return nil, err
	}
	bytes := buf.Bytes()
	sum := md5.Sum(bytes)
	mime := DetectMIME(bytes)
	return &FileInfo{
		Body:     bytes,
		Size:     len(bytes),
		MD5:      hex.EncodeToString(sum[:]),
		MimeType: mime,
	}, nil
}
