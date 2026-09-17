package drive

import (
	"bytes"
	"encoding/binary"
	"strings"
)

// hasStrippableMetadata reports whether the uploaded bytes carry metadata that
// the webpublic re-encode would drop (EXIF / XMP)。
//
// **webpublic を作るかどうかの判定に使う。** 2048px 以下でも、メタデータが
// あれば webpublic を作って**そちらを他人に見せる** (`GetPublicURL`)。見落とすと
// **撮影位置 (GPS) が公開側に出たままになる**。
//
// 以前は JPEG の APP1 しか見ていなかったので、PNG / WebP / TIFF に GPS を
// 埋めたまま 2048px 以下でアップロードすると webpublic が作られなかった。
// upstream は sharp の `metadata.exif ?? iptc ?? xmp ?? tifftagPhotoshop` で
// 形式を問わず判定する (`DriveService.ts`)。
//
// **判定は「あるかもしれない」側に倒す。** 誤って true にすると webpublic を
// 余計に 1 枚作るだけだが、誤って false にすると位置情報が出る。
func hasStrippableMetadata(body []byte, mime string) bool {
	switch {
	case isJPEGBytes(body):
		return jpegHasMetadata(body)
	case isPNGBytes(body):
		return pngHasMetadata(body)
	case isWebPBytes(body):
		return webpHasMetadata(body)
	case isTIFFBytes(body):
		// **TIFF は IFD そのものがメタデータ。** EXIF タグも GPS タグも本体の
		// ディレクトリに直接載るので、TIFF なら常に「ある」とみなす。
		return true
	}
	// MIME は判定の補助にしか使わない (中身が優先)。ここへ来るのは
	// GIF / BMP / ICO / AVIF など。AVIF は寸法に関わらず webpublic を作る枝が
	// 別にあるので、ここで false を返しても取りこぼさない。
	_ = mime
	return false
}

func isJPEGBytes(b []byte) bool { return len(b) >= 2 && b[0] == 0xFF && b[1] == 0xD8 }

func isPNGBytes(b []byte) bool {
	return len(b) >= 8 && string(b[:8]) == "\x89PNG\r\n\x1a\n"
}

func isWebPBytes(b []byte) bool {
	return len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP"
}

func isTIFFBytes(b []byte) bool {
	if len(b) < 4 {
		return false
	}
	return string(b[:4]) == "II\x2a\x00" || string(b[:4]) == "MM\x00\x2a"
}

// jpegHasMetadata looks for the APP segment signatures listed above.
//
// 先頭 64KB に限るのは従来どおり。EXIF は APP1 セグメント (先頭側) に置かれる。
// jpegMetadataMarkers are the APP segment signatures that carry metadata the
// webpublic re-encode drops.
//
// upstream の条件は `metadata.exif ?? iptc ?? xmp ?? tifftagPhotoshop` で、
// **EXIF だけではない**。Lightroom / Photoshop の書き出しは GPS を
// XMP (`exif:GPSLatitude`) にだけ持つことがあり、EXIF だけ見ていると
// 2048px 以下でそれが素通りする。
var jpegMetadataMarkers = [][]byte{
	[]byte("Exif\x00\x00"),                       // APP1 EXIF
	[]byte("http://ns.adobe.com/xap/1.0/\x00"),   // APP1 XMP
	[]byte("http://ns.adobe.com/xmp/extension/"), // APP1 拡張 XMP
	[]byte("Photoshop 3.0\x00"),                  // APP13 IPTC / tifftagPhotoshop
}

func jpegHasMetadata(body []byte) bool {
	if len(body) < 12 {
		return false
	}
	head := body[:min(len(body), 65536)]
	for _, marker := range jpegMetadataMarkers {
		if bytes.Contains(head, marker) {
			return true
		}
	}
	return false
}

// pngHasMetadata looks for an `eXIf` chunk or an XMP-bearing `iTXt` chunk.
//
// **IDAT で止めない。** `eXIf` は PNG 仕様上 IDAT の前でも後ろでも置ける。
// **`tEXt` / `zTXt` は見ない** — `Software` のような無害な注記が大半で、
// sharp の `metadata.exif / iptc / xmp` のどれにも載らない。
func pngHasMetadata(body []byte) bool {
	found := false
	walkPNGChunks(body, func(typ string, data []byte) bool {
		switch typ {
		case "eXIf":
			found = true
			return false
		case "iTXt":
			// keyword は chunk data の先頭、NUL 終端。
			if i := bytes.IndexByte(data, 0); i >= 0 && string(data[:i]) == "XML:com.adobe.xmp" {
				found = true
				return false
			}
		case "zTXt", "tEXt":
			// **`Raw profile type exif` / `...iptc` / `...xmp` だけ拾う。**
			// ImageMagick は `convert a.jpg a.png` の既定で EXIF をこの形へ
			// 移すので、見ないと変換済みの写真から GPS が漏れる。
			// `Software` のような無害な注記は拾わない — 拾うとほぼ全ての PNG が
			// 再エンコードされる。
			if i := bytes.IndexByte(data, 0); i >= 0 {
				if key := string(data[:i]); strings.HasPrefix(key, "Raw profile type ") {
					found = true
					return false
				}
			}
		}
		return true
	})
	return found
}

// walkPNGChunks calls fn for each chunk until fn returns false.
//
// **長さを信用して読み飛ばさず、毎回残りバイト数を確かめる** — 細工した length で
// 範囲外を読まないようにするため。
func walkPNGChunks(body []byte, fn func(typ string, data []byte) bool) {
	pos := 8 // signature の後ろから
	for {
		if pos+8 > len(body) {
			return
		}
		length := int(binary.BigEndian.Uint32(body[pos : pos+4]))
		typ := string(body[pos+4 : pos+8])
		// **length は uint32 なので 32bit 環境では int が負になりうる。**
		// 先に符号を見てから加算する。
		if length < 0 {
			return
		}
		dataStart := pos + 8
		dataEnd := dataStart + length
		if dataEnd < dataStart || dataEnd > len(body) {
			return
		}
		if !fn(typ, body[dataStart:dataEnd]) {
			return
		}
		next := dataEnd + 4 // CRC
		if next <= pos {
			return
		}
		pos = next
	}
}

// webpHasMetadata looks for an `EXIF` or `XMP ` chunk in the RIFF container.
//
// 拡張チャンクを持つ WebP は VP8X 形式。素の VP8 / VP8L にはメタデータの
// 置き場が無いので、走査は空振りして false になる。
func webpHasMetadata(body []byte) bool {
	pos := 12 // "RIFF" + size + "WEBP"
	for {
		if pos+8 > len(body) {
			return false
		}
		typ := string(body[pos : pos+4])
		size := int(binary.LittleEndian.Uint32(body[pos+4 : pos+8]))
		if size < 0 {
			return false
		}
		if typ == "EXIF" || typ == "XMP " {
			return true
		}
		// チャンクは偶数境界に揃えられる。
		next := pos + 8 + size + (size & 1)
		if next <= pos || next > len(body) {
			return false
		}
		pos = next
	}
}
