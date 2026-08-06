package drive

import (
	"regexp"
	"strings"

	"github.com/gabriel-vasile/mimetype"
)

// extRegExp matches a trailing extension made of ASCII alphanumerics, matching
// upstream misc/correct-filename.ts.
var extRegExp = regexp.MustCompile(`\.[0-9a-zA-Z]+$`)

// archiveExtsToSkip are container formats whose extension must not be rewritten.
//
// 圧縮形式は中身の判定と外側の拡張子がずれやすく (`.tar.gz` を gzip と判定する
// 等)、拡張子を足すと `foo.tar.gz.gz` のような名前になる。upstream も同じ理由で
// スキップしている (misskey-dev/misskey#11482)。
var archiveExtsToSkip = map[string]struct{}{
	".gz": {}, ".tar": {}, ".tgz": {}, ".bz2": {}, ".xz": {}, ".zip": {}, ".7z": {},
}

// CorrectFilename appends the detected extension when the given filename does
// not already carry a matching one (upstream misc/correct-filename.ts)。
//
// ext は検出された拡張子 ("jpg" / ".jpg" のどちらでも可)。空文字は「未知の
// 形式」を意味し、その場合は拡張子を持たないファイルにだけ `.unknown` を足す。
func CorrectFilename(filename, ext string) string {
	dotExt := ".unknown"
	if ext != "" {
		if strings.HasPrefix(ext, ".") {
			dotExt = ext
		} else {
			dotExt = "." + ext
		}
	}

	match := extRegExp.FindString(filename)
	if match == "" {
		// 拡張子を持たないファイルには検出した拡張子をそのまま足す。
		return filename + dotExt
	}

	filenameExt := strings.ToLower(match)
	switch {
	// 未知の形式で、かつ既に何らかの拡張子がある場合は触らない。
	case ext == "",
		filenameExt == dotExt,
		// jpeg / tiff は同一視する。
		dotExt == ".jpg" && filenameExt == ".jpeg",
		dotExt == ".tif" && filenameExt == ".tiff",
		// dll も exe も portable executable なので判別できない。
		dotExt == ".exe" && filenameExt == ".dll":
		return filename
	}
	if _, skip := archiveExtsToSkip[dotExt]; skip {
		return filename
	}

	// 拡張子はあるが検出結果と食い違う場合は足す (置き換えない)。
	return filename + dotExt
}

// ExtensionForMIME returns the canonical extension (without a dot) for a
// detected MIME type, or "" when unknown.
//
// upstream は file-type の ext をそのまま使う。mk-go の検出器 (mimetype) も
// 拡張子を返すが、`.` 付きで返る点と octet-stream で空になる点だけ揃える。
func ExtensionForMIME(mime string) string {
	if mime == "" || mime == MIMEOctetStream {
		return ""
	}
	ext := mimetype.Lookup(mime)
	if ext == nil {
		return ""
	}
	return strings.TrimPrefix(ext.Extension(), ".")
}
