package drive

import (
	"image"
	"io"
	"os"
)

// SetRandReaderForTest replaces the package-level random source for tests.
// 戻り値は元のreaderで、テスト終了時に呼び出して復元する。
func SetRandReaderForTest(r io.Reader) func() {
	prev := randReader
	randReader = r
	return func() { randReader = prev }
}

// SetWebPEncoderForTest replaces the WebP encoder for testing error paths.
func SetWebPEncoderForTest(f func(image.Image, int) ([]byte, error)) func() {
	prev := webpEncoderFunc
	webpEncoderFunc = f
	return func() { webpEncoderFunc = prev }
}

// SetPNGEncoderForTest replaces the PNG encoder for testing error paths.
func SetPNGEncoderForTest(f func(image.Image) ([]byte, error)) func() {
	prev := pngEncoderFunc
	pngEncoderFunc = f
	return func() { pngEncoderFunc = prev }
}

// SetOsMkdirTempForTest replaces os.MkdirTemp for testing error paths.
func SetOsMkdirTempForTest(f func(string, string) (string, error)) func() {
	prev := osMkdirTemp
	osMkdirTemp = f
	return func() { osMkdirTemp = prev }
}

// SetOsWriteFileForTest replaces os.WriteFile for testing error paths.
func SetOsWriteFileForTest(f func(string, []byte, os.FileMode) error) func() {
	prev := osWriteFile
	osWriteFile = f
	return func() { osWriteFile = prev }
}

// GenerateAltsForTest exposes generateAlts for external tests.
func (s *Service) GenerateAltsForTest(body []byte, mimeType string) (
	thumbnail *ProcessedImage,
	webpublic *ProcessedImage,
	blurhash *string,
) {
	result := s.generateAlts(body, mimeType)
	if result == nil {
		return nil, nil, nil
	}
	return result.thumbnail, result.webpublic, result.blurhash
}

// MimeAllowedByPolicyForTest exposes mimeAllowedByPolicy so external tests
// can exercise the helper's pattern-matching logic directly (#1028).
func MimeAllowedByPolicyForTest(mime string, raw any) bool {
	return mimeAllowedByPolicy(mime, raw)
}
