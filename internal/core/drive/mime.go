package drive

import (
	"bytes"
	"encoding/xml"
	"io"
	"net/http"
	"strings"

	"github.com/gabriel-vasile/mimetype"
)

// MIMEOctetStream is the fallback media type for content we cannot identify.
const MIMEOctetStream = "application/octet-stream"

// MIMESVG is the media type for SVG documents. Kept out of the browser-safe
// allowlist on purpose (inline SVG is active content).
const MIMESVG = "image/svg+xml"

// MIMESniffLen is how many leading bytes a caller should keep when it can only
// afford to buffer a prefix (e.g. the first chunk of a chunked upload).
// mimetype reads at most 3072 bytes, so anything beyond this cannot change the
// result.
const MIMESniffLen = 3072

// svgSizeLimit bounds how much we are willing to XML-parse when probing for
// SVG. upstream FileInfoService.checkSvg uses the same 1MiB cutoff.
const svgSizeLimit = 1 * 1024 * 1024

// DetectMIME identifies the media type of body from its **contents only**.
//
// ファイル名の拡張子は一切見ない。拡張子を信用すると、`.png` と名付けた HTML を
// 上げるだけで BrowserSafeContentType の allowlist を通り抜けられてしまう
// (#2106 H3/H4 の stored XSS 対策が無効化される)。
//
// upstream `FileInfoService.detectType` は `file-type` パッケージ 1 本で
// 判定するが、Go には等価な単一実装が無い。実測すると得意分野が割れる:
//
//   - `http.DetectContentType` は ISO-BMFF のブランドを `mp4` かどうかでしか
//     見ないため、.mov / .heic / .avif / .m4a / .3gp などを軒並み octet-stream
//     に落とす。.flac / .aac / .tiff / .mpeg も判定できない
//   - `mimetype` はそれらを正しく判定する一方、ID3 タグ付き MP3 と SVG を
//     octet-stream にする
//
// どちらか一方では取りこぼすので、mimetype を主・標準 sniffer をフォールバック
// にして両方の穴を塞ぐ。SVG 判定と MIME 正規化は upstream の checkSvg / fixMime
// に対応する (#2319)。
func DetectMIME(body []byte) string {
	if len(body) == 0 {
		// upstream も 0 バイトは判定にかけず octet-stream にする。
		return MIMEOctetStream
	}

	detected := stripMIMEParams(mimetype.Detect(body).String())
	if detected == "" || detected == MIMEOctetStream {
		detected = stripMIMEParams(http.DetectContentType(body))
	}

	// 検出器が SVG と言ってきても鵜呑みにしない。mimetype の SVG 判定は
	// 部分文字列一致なので、本文のどこかに `<svg` と書いてあるだけの別文書
	// (svg を含む HTML など) も SVG と report する。誤認すると image 扱いに
	// なり、サムネイル生成やセンシティブ判定の経路が変わる。
	if detected == MIMESVG {
		if looksLikeSVG(body) {
			return MIMESVG
		}
		detected = stripMIMEParams(http.DetectContentType(body))
	}

	// XML / プレーンテキスト / 判定不能は SVG かもしれない (upstream detectType の
	// 「XMLはSVGかもしれない」「種類が不明でもSVGかもしれない」の 2 分岐に対応)。
	// upstream の file-type は SVG を text として素通しするので、後者の分岐に
	// 落ちてくる。mk-go では標準 sniffer が text/plain を返す形で現れる。
	if mayBeSVG(detected) && looksLikeSVG(body) {
		return MIMESVG
	}
	return fixMIME(detected)
}

// mayBeSVG reports whether a detected type is vague enough that the content
// could still be an SVG document.
func mayBeSVG(mime string) bool {
	return mime == MIMEOctetStream || isXMLMIME(mime) || mime == "text/plain"
}

// stripMIMEParams drops the parameter part of a media type
// ("text/xml; charset=utf-8" -> "text/xml") and lower-cases it.
func stripMIMEParams(mime string) string {
	if i := strings.IndexByte(mime, ';'); i >= 0 {
		mime = mime[:i]
	}
	return strings.ToLower(strings.TrimSpace(mime))
}

// isXMLMIME reports whether mime denotes a generic XML document, which may
// actually be an SVG.
func isXMLMIME(mime string) bool {
	return mime == "text/xml" || mime == "application/xml"
}

// fixMIME normalises aliases onto the spelling Misskey stores, mirroring
// upstream `FileInfoService.fixMime`.
//
// `audio/x-flac` / `audio/vnd.wave` が allowlist に残っているのは、正規化が
// 入る前に保存された行のための後方互換 (upstream も同じ理由で残している)。
// 新規アップロードはここで正規化されるので、以後は現れない。
//
// `audio/wave` は upstream の fixMime に無いが、`http.DetectContentType` が
// WAV に対して返す綴りで、そのままでは allowlist に無いため octet-stream に
// 矯正されて再生できなくなる。mk-go 固有の fallback 経路で生じるので追加する。
func fixMIME(mime string) string {
	switch mime {
	case "audio/x-flac":
		return "audio/flac"
	case "audio/vnd.wave", "audio/wave", "audio/x-wav":
		return "audio/wav"
	}
	return mime
}

// looksLikeSVG reports whether body is an XML document whose **root** element
// is <svg>, i.e. the document *is* an SVG rather than merely containing one.
//
// 部分文字列一致ではなく実際に XML として読む。コメント / XML 宣言 / DOCTYPE が
// 先行しても正しく判定でき、逆に次のものは SVG と見なさない:
//
//   - 本文のどこかに "<svg" と書いてあるだけのテキスト (最初の開始要素より前に
//     非空白のテキストがある)
//   - ルートが別要素の XML や、svg を内包する HTML
//
// upstream の `is-svg` も「文書全体が svg であること」を要求するので挙動が揃う。
// 誤認すると image 扱いになり、サムネイル生成やセンシティブ判定の経路が変わる。
//
// Go の encoding/xml は外部実体を展開せず、未定義実体はエラーにするので
// billion laughs にはならない。加えて upstream 同様サイズ上限を設ける。
func looksLikeSVG(body []byte) bool {
	if len(body) == 0 || len(body) > svgSizeLimit {
		return false
	}
	dec := xml.NewDecoder(bytes.NewReader(body))
	dec.Strict = false
	for {
		tok, err := dec.Token()
		if err != nil {
			// io.EOF を含め、最初の開始要素に辿り着く前に終わったら SVG ではない。
			return false
		}
		switch t := tok.(type) {
		case xml.StartElement:
			return strings.EqualFold(t.Name.Local, "svg")
		case xml.CharData:
			// ルート要素より前に実体のあるテキストが来る = XML 文書ではない。
			if len(bytes.TrimSpace(t)) > 0 {
				return false
			}
		}
	}
}

// DetectMIMEFromReader identifies the media type from the leading bytes of r
// without consuming more than MIMESniffLen from it. Returns the sniffed prefix
// so callers can re-assemble the stream.
func DetectMIMEFromReader(r io.Reader) (string, []byte, error) {
	prefix := make([]byte, MIMESniffLen)
	n, err := io.ReadFull(r, prefix)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return "", nil, err
	}
	prefix = prefix[:n]
	return DetectMIME(prefix), prefix, nil
}
