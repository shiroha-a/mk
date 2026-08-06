package drive

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// upstream misc/correct-filename.ts と同じ判定になること。
func TestCorrectFilename(t *testing.T) {
	tests := []struct {
		name     string
		filename string
		ext      string
		want     string
	}{
		{"拡張子が無ければ足す", "Belmond", "jpg", "Belmond.jpg"},
		{"拡張子が食い違えば足す", "Belmond.png", "jpg", "Belmond.png.jpg"},
		{"拡張子が一致すれば触らない", "Belmond.jpg", "jpg", "Belmond.jpg"},
		{"大文字の拡張子も一致とみなす", "Belmond.JPG", "jpg", "Belmond.JPG"},
		{"ドット付きの ext も受ける", "Belmond", ".jpg", "Belmond.jpg"},
		{"jpeg と jpg は同一視", "Belmond.jpeg", "jpg", "Belmond.jpeg"},
		{"tiff と tif は同一視", "scan.tiff", "tif", "scan.tiff"},
		{"未知の形式で拡張子があれば触らない", "archive.dat", "", "archive.dat"},
		{"未知の形式で拡張子が無ければ .unknown", "archive", "", "archive.unknown"},
		{"圧縮形式は拡張子を足さない", "backup.tar", "gz", "backup.tar"},
		{"zip も足さない", "photos", "zip", "photos.zip"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, CorrectFilename(tt.filename, tt.ext))
		})
	}
}

func TestCorrectFilename_DllExeNotRewritten(t *testing.T) {
	assert.Equal(t, "lib.dll", CorrectFilename("lib.dll", "exe"))
}

func TestExtensionForMIME(t *testing.T) {
	assert.Equal(t, "jpg", ExtensionForMIME("image/jpeg"))
	assert.Equal(t, "png", ExtensionForMIME("image/png"))
	assert.Empty(t, ExtensionForMIME(""), "空 MIME は未知扱い")
	assert.Empty(t, ExtensionForMIME(MIMEOctetStream), "octet-stream は未知扱い")
	assert.Empty(t, ExtensionForMIME("application/x-not-a-real-type"))
}
