package drive

import "strings"

// fileTypeBrowserSafe mirrors upstream const.ts FILE_TYPE_BROWSERSAFE: the MIME
// types Misskey considers safe to serve inline to a browser (image / audio /
// video only — never text/html, image/svg+xml, text/xml, application/xml, etc.).
// Anything else must be served as application/octet-stream so a browser will not
// render attacker-controlled markup/script from the file origin (stored XSS).
var fileTypeBrowserSafe = map[string]struct{}{
	"image/png":       {},
	"image/gif":       {},
	"image/jpeg":      {},
	"image/webp":      {},
	"image/avif":      {},
	"image/apng":      {},
	"image/bmp":       {},
	"image/tiff":      {},
	"image/x-icon":    {},
	"audio/opus":      {},
	"video/ogg":       {},
	"audio/ogg":       {},
	"application/ogg": {},
	"video/quicktime": {},
	"video/mp4":       {},
	"audio/mp4":       {},
	"video/x-m4v":     {},
	"audio/x-m4a":     {},
	"video/3gpp":      {},
	"video/3gpp2":     {},
	"video/mpeg":      {},
	"audio/mpeg":      {},
	"video/webm":      {},
	"audio/webm":      {},
	"audio/aac":       {},
	"audio/flac":      {},
	"audio/wav":       {},
	"audio/x-flac":    {},
	"audio/vnd.wave":  {},
}

// BrowserSafeContentType returns contentType unchanged when its MIME (charset
// parameter stripped) is in the browser-safe allowlist, otherwise
// "application/octet-stream". Mirrors upstream FileServerUtils.getSafeContentType
// so non-browsersafe uploads (HTML / SVG / XML / plain text, etc.) cannot be
// rendered as active content from the file origin (#2106 H3/H4).
func BrowserSafeContentType(contentType string) string {
	mime := contentType
	if i := strings.IndexByte(mime, ';'); i >= 0 {
		mime = mime[:i]
	}
	mime = strings.ToLower(strings.TrimSpace(mime))
	if _, ok := fileTypeBrowserSafe[mime]; ok {
		return contentType
	}
	return "application/octet-stream"
}
