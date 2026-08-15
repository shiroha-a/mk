package mediaproxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strings"
	"time"

	// 注意 (#672 Phase 1): TGA decoder は `blezek/tga` (auto-register 無し)
	// を採用し、`decodeImage` 内で MIME 判定で manual dispatch している。
	// 同 fork 元の `github.com/ftrvxmtrx/tga` は init() で
	// `image.RegisterFormat("tga", "", ...)` (magic bytes 空) するため、
	// blank import すると他 image format (PNG/JPEG/WebP/...) の自動 dispatch
	// を破壊する。**`_ "github.com/ftrvxmtrx/tga"` を絶対追加しないこと**。
	"github.com/blezek/tga"
	"github.com/gen2brain/avif"
	_ "github.com/gen2brain/heic" // HEIC/HEIF input decode (iPhone uploads)
	_ "github.com/gen2brain/jpegxl"
	"github.com/gen2brain/webp"
	"github.com/kovidgoyal/imaging"
	_ "github.com/mrjoshuak/go-jpeg2000" // JP2/J2K input decode (#734)
	_ "github.com/spakin/netpbm"         // PBM/PGM/PPM/PAM input decode (#672 Phase 1)
	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"

	coredrive "github.com/shiroha-a/mk/internal/core/drive"
	"github.com/shiroha-a/mk/internal/safehttp"
)

// ProxyMode enumerates the image processing modes.
type ProxyMode int

const (
	ModeDefault ProxyMode = iota
	ModeEmoji
	ModeAvatar
	ModeStatic
	ModePreview
	ModeBadge
)

// 画像処理パラメータ (Misskey TS準拠)
const (
	emojiHeight   = 128
	avatarHeight  = 320
	staticWidth   = 498
	staticHeight  = 422
	previewWidth  = 200
	previewHeight = 200
	badgeSize     = 96
	webpQuality   = 77
	avifQuality   = 60       // AVIF default; matches gen2brain/avif.DefaultQuality
	avifSpeed     = 8        // 0..10 — 8 keeps quality close to default while staying responsive
	maxDownload   = 32 << 20 // 32 MB
)

// maxDecodedPixels caps width*height *after* decode so a pixel-bomb input
// (a few hundred kB AVIF/HEIC/JXL that decodes to 30000x30000 = 3.6 GB
// raster) cannot OOM the proxy. Mirrors sharp's `limitInputPixels`
// default. 8192*8192 = 64 MP keeps every realistic phone/camera input
// flowing while rejecting clearly malicious payloads (#637 review UR-016).
//
// var (not const) so tests can shadow the limit without synthesizing a 64 MP
// fixture. Production paths must not assign to it.
var maxDecodedPixels int64 = 8192 * 8192

var (
	ErrUnauthorized = errors.New("mediaproxy: unauthorized URL")
	ErrNotFound     = errors.New("mediaproxy: resource not found")
	ErrBadRequest   = errors.New("mediaproxy: bad request")
	ErrTooLarge     = errors.New("mediaproxy: file too large")
)

// browsersafeMIMEs lists MIME types safe to serve inline in browsers.
var browsersafeMIMEs = map[string]bool{
	"image/png":  true,
	"image/gif":  true,
	"image/jpeg": true,
	"image/webp": true,
	"image/avif": true,
	"image/apng": true,
	"image/bmp":  true,
	"image/tiff": true,
	// HEIC/HEIF: Safari 16+ で表示可能。Firefox/Chrome は表示できないが
	// pass-through で返した先で frontend が WebP/AVIF 変換 URL に置換する
	// 想定 (#637 M4)。
	"image/heic": true,
	"image/heif": true,
	// JPEG XL: Safari 17+ 対応、Chrome は flag つきで実験中 (#637 M5)。
	"image/jxl": true,
	// `image/x-icon` は古い慣例、`image/vnd.microsoft.icon` は IANA media
	// type registry に登録されている公式名 (RFC 紐付け無し)。リモート
	// Misskey/Mastodon の favicon.ico は後者で返ってくるホストが多いため
	// 両方許可する (#418)。
	"image/x-icon":             true,
	"image/vnd.microsoft.icon": true,
	"image/vnd.mozilla.apng":   true,
	// Netpbm 系 (PBM/PGM/PPM): pure Go decode (#672 Phase 1)。browser native
	// 表示は無いが、convertible 判定で WebP/AVIF に変換されて配信される。
	"image/x-portable-bitmap":  true,
	"image/x-portable-graymap": true,
	"image/x-portable-pixmap":  true,
	"image/x-portable-anymap":  true,
	// TGA: pure Go decode (#672 Phase 1)。同上。
	"image/x-tga":   true,
	"image/x-targa": true,
	// JPEG 2000 (JP2 file format / J2K codestream): pure Go decode (#734)。
	// mrjoshuak/go-jpeg2000 が image.RegisterFormat 経由で JP2 / J2K の magic
	// bytes (`\x00\x00\x00\x0cjP\x20\x20\r\n\x87\n` / `\xff\x4f\xff\x51`) を
	// 登録するので decodeImage 経由で透過的に処理される。convertible 経路で
	// WebP/AVIF へ変換出力。
	"image/jp2":      true,
	"image/jpeg2000": true,
	// JPX (Part 2 拡張) は library が "not fully supported" としているため
	// convertible 経路には乗せず、pass-through のみ受け入れる。
	"image/jpx": true,
	// JPEG XR / MNG: decode 用 pure Go library が無いため pass-through で
	// 配信し browser ネイティブ対応 (Edge legacy / 一部 viewer plugin) に
	// 委譲する (#672 Phase 1 partial)。完全 transcode は cgo 依存となる
	// ため scope 外。
	"image/jxr":          true,
	"image/vnd.ms-photo": true,
	"video/x-mng":        true,
	"image/x-mng":        true,
	"video/x-jng":        true,
	"audio/opus":         true,
	"video/ogg":          true,
	"audio/ogg":          true,
	"application/ogg":    true,
	"video/quicktime":    true,
	"video/mp4":          true,
	"audio/mp4":          true,
	"video/x-m4v":        true,
	"audio/x-m4a":        true,
	"video/3gpp":         true,
	"video/3gpp2":        true,
	"video/mpeg":         true,
	"audio/mpeg":         true,
	"video/webm":         true,
	"audio/webm":         true,
	"audio/aac":          true,
	"audio/flac":         true,
	"audio/wav":          true,
}

// ProxyResult is the output of proxy resolution + processing.
type ProxyResult struct {
	Body        io.ReadCloser
	ContentType string
}

// DriveFileLookup is the minimal subset of repository.DriveFileRepository
// the proxy needs to resolve a `/files/<accessKey>` request to its cached
// thumbnail / webpublic variant when one exists (#637 M1)。
type DriveFileLookup interface {
	FindByAccessKey(accessKey string) (DriveFileVariants, error)
}

// DriveFileVariants exposes only the access-key fields the proxy needs.
// repository 側でこの shape を直接返さない (model.DriveFile が大きすぎる)
// ので、wire 層で adapter を書いて変換する。
//
// MimeType は upload 時に確定した値で、`http.DetectContentType` が認識
// しない HEIC/HEIF/AVIF/JXL を proxy が誤って `application/octet-stream`
// として扱うのを防ぐ (#637 review UR-017)。
type DriveFileVariants struct {
	AccessKey          *string
	ThumbnailAccessKey *string
	WebpublicAccessKey *string
	MimeType           string
}

// Service handles media proxy authorization and fetching.
type Service struct {
	instanceURL  string
	driveStorage coredrive.Storage
	allowlist    AllowlistChecker
	hmacSecret   []byte
	httpClient   *http.Client
	userAgent    string
	driveLookup  DriveFileLookup // optional, #637 M1
	// localStorage は driveStorage が object storage に切り替わったあとも
	// `storedInternal=true` な既存ファイルを提供するための fallback (#2315)。
	// optional — nil なら fallback しない。
	localStorage     coredrive.Storage
	videoThumbGen    string       // optional, #637 M2 (videoThumbnailGenerator base URL)
	videoThumbMode   string       // "post" (default) | "get" — wire selection
	videoThumbClient *http.Client // built lazily from videoThumbGen, supports unix:// scheme
}

// NewService creates a new media proxy Service.
// allowedPrivateNetworks は SSRF 保護で許可するプライベート CIDR リスト (config.AllowedPrivateNetworks)。
// transportOpts は forward proxy 等の追加 transport 設定 (safehttp.WithProxy など)。
func NewService(instanceURL, userAgent string, driveStorage coredrive.Storage, allowlist AllowlistChecker, hmacSecret []byte, allowedPrivateNetworks []string, transportOpts ...safehttp.Option) *Service {
	return &Service{
		instanceURL:  instanceURL,
		driveStorage: driveStorage,
		allowlist:    allowlist,
		hmacSecret:   hmacSecret,
		httpClient: &http.Client{
			Timeout:   30 * time.Second,
			Transport: NewSSRFSafeTransport(allowedPrivateNetworks, transportOpts...),
		},
		userAgent: userAgent,
	}
}

// SetDriveLookup attaches a DriveFileLookup so the proxy can substitute the
// cached thumbnail / webpublic variant for `?preview` and `?static` requests
// against local files (#637 M1)。
func (s *Service) SetDriveLookup(l DriveFileLookup) {
	s.driveLookup = l
}

// SetLocalStorage wires the always-local backend used as a fallback for rows
// stored before object storage was enabled (#2315). Optional.
func (s *Service) SetLocalStorage(st coredrive.Storage) {
	s.localStorage = st
}

// SetDriveStorage replaces the storage backend (test ergonomics, #637 M1
// review nit).
func (s *Service) SetDriveStorage(st coredrive.Storage) {
	s.driveStorage = st
}

// SetVideoThumbnailGenerator wires an external thumbnail generator with the
// default wire mode (POST multipart). For UDS deployments pass
// `unix:///path/to/socket` and the proxy will dial the socket directly,
// ignoring the URL host. Empty disables the feature (default — proxy
// returns dummy PNG for video MIME, #637 M2).
//
// このコール先は operator-trusted endpoint (内部 sidecar / 自前 SaaS 想定)
// のため、`config.proxy` (#638 outbound forward proxy) は意図的に経由しな
// い。urlpreview の summalyProxy 経由 client (`internal/core/urlpreview/
// fetcher.go`) と同じ pattern。`config.proxy` を強制したい運用は別途
// nginx / sidecar 側で出力経路を制御する。
func (s *Service) SetVideoThumbnailGenerator(genURL string) {
	s.SetVideoThumbnailGeneratorWithMode(genURL, "post")
}

// SetVideoThumbnailGeneratorWithMode is the explicit form. mode = "post"
// (multipart upload, nekonoverse/video-thumb compat) or "get" (URL-based
// Misskey TS videoThumbnailGenerator API). 不正値は "post" にフォール
// バック。
func (s *Service) SetVideoThumbnailGeneratorWithMode(genURL, mode string) {
	s.videoThumbGen = genURL
	switch mode {
	case "get":
		s.videoThumbMode = "get"
	default:
		s.videoThumbMode = "post"
	}
	s.videoThumbClient = newVideoThumbnailClient(genURL)
}

// SignURL generates an HMAC-SHA256 signature for the given URL.
func (s *Service) SignURL(rawURL string) string {
	return SignURL(s.hmacSecret, rawURL)
}

// Authorize checks whether the given URL is permitted for proxying.
// HMAC署名があれば先に検証し、なければDBのallowlistを参照する。
func (s *Service) Authorize(ctx context.Context, rawURL, sig string) error {
	if sig != "" && VerifyHMAC(s.hmacSecret, rawURL, sig) {
		return nil
	}

	allowed, err := s.allowlist.IsAllowedURL(ctx, rawURL)
	if err != nil {
		slog.Error("allowlist check failed", "url", rawURL, "error", err)
		return ErrUnauthorized
	}
	if !allowed {
		return ErrUnauthorized
	}
	return nil
}

// Fetch downloads the remote URL (or resolves a local file), applies image
// processing per the requested mode, and returns the result. out selects the
// encoder format (FormatWebP / FormatAVIF) for resize-class modes.
func (s *Service) Fetch(ctx context.Context, rawURL string, mode ProxyMode, out OutputFormat) (*ProxyResult, error) {
	// ローカルファイルの場合はdriveStorageから直接取得
	filesPrefix := s.instanceURL + "/files/"
	if strings.HasPrefix(rawURL, filesPrefix) {
		return s.resolveLocal(ctx, rawURL, filesPrefix, mode, out)
	}

	return s.fetchRemote(ctx, rawURL, mode, out)
}

// resolveLocal fetches a file from local drive storage by access key.
func (s *Service) resolveLocal(ctx context.Context, rawURL, filesPrefix string, mode ProxyMode, out OutputFormat) (*ProxyResult, error) {
	primaryKey := strings.TrimPrefix(rawURL, filesPrefix)
	// パスに/が含まれる場合は先頭のセグメントだけを使う
	if idx := strings.Index(primaryKey, "/"); idx >= 0 {
		primaryKey = primaryKey[:idx]
	}
	if primaryKey == "" {
		return nil, ErrBadRequest
	}

	accessKey := primaryKey
	storedMIME := ""

	// 既に存在する thumbnail / webpublic variant を提供できる mode なら、
	// 元データを再 decode + resize せずに variant を直接返して CPU を節約
	// する (#637 M1)。SetDriveLookup されていない / resize しないモード /
	// 該当 variant が無い場合は従来通り元データを取得して proxy 側で
	// resize する。default mode で DB を引かないようにする (#637 review
	// UR-014)。
	if s.driveLookup != nil && isResizeMode(mode) {
		if swapped, mt, ok := s.swapToVariant(primaryKey, mode); ok {
			accessKey = swapped
			storedMIME = mt
		} else {
			// swap しないが lookup は成功している場合、stored MIME を後段
			// MIME 判定で活用する (HEIC/AVIF/JXL の DetectContentType
			// 誤判定対策、#637 review UR-017)。
			if v, err := s.driveLookup.FindByAccessKey(primaryKey); err == nil {
				storedMIME = v.MimeType
			}
		}
	}

	body, err := s.driveStorage.Get(accessKey)
	// Variant blob が S3 lifecycle 等で消えた場合、primary を再フェッチして
	// resize 経路に戻す (#637 review UR-001)。primary 本体が存在しない /
	// その他 storage error はそのまま伝える。
	if errors.Is(err, coredrive.ErrObjectNotFound) && accessKey != primaryKey {
		body, err = s.driveStorage.Get(primaryKey)
		if err == nil {
			accessKey = primaryKey
			storedMIME = "" // primary の MIME は後段で判定し直す
		}
	}
	// オブジェクトストレージ有効化より前 (あるいは TS 時代) に保存された
	// `storedInternal=true` な行はローカル FS に実体がある。それらの url は
	// `<instanceURL>/files/<key>` のままなので、有効化した瞬間にこの経路へ来て
	// object storage を空振りし、タイムラインの既存画像が全部壊れる (#2315)。
	// `/files/:accessKey` は #1414 で同じ fallback を持っているが、media proxy
	// 側が漏れていた。
	//
	// drive_file を引いて storedInternal を見る手もあるが、default mode で DB を
	// 引かない設計 (#637 review UR-014) を崩したくないので、object-not-found の
	// ときだけローカルを見に行く。ホットパスには何も足さない。
	if errors.Is(err, coredrive.ErrObjectNotFound) && s.localStorage != nil && !coredrive.StorageIsLocal(s.driveStorage) {
		if b, lerr := s.localStorage.Get(primaryKey); lerr == nil {
			body, err = b, nil
			accessKey = primaryKey
			storedMIME = ""
		}
	}
	if err != nil {
		if errors.Is(err, coredrive.ErrObjectNotFound) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("mediaproxy: local file access: %w", err)
	}

	// ローカルファイルの場合はMIME判定して画像処理
	data, readErr := io.ReadAll(io.LimitReader(body, maxDownload+1))
	body.Close()
	if readErr != nil {
		return nil, fmt.Errorf("mediaproxy: read local file: %w", readErr)
	}
	if int64(len(data)) > maxDownload {
		return nil, ErrTooLarge
	}

	// stored MIME が分かっていればそちらを優先 (HEIC/AVIF/JXL は
	// http.DetectContentType が認識せず octet-stream を返す)。fetchRemote
	// と同じ挙動に揃える。
	contentType := storedMIME
	if isUnknownBinary(contentType) {
		contentType = http.DetectContentType(data)
	}
	return s.processAndReturn(ctx, data, contentType, mode, out, rawURL)
}

// swapToVariant looks up the DriveFile by access key and returns the
// thumbnail / webpublic access key when one matches the requested mode,
// along with the variant's expected MIME (which `http.DetectContentType`
// can sniff but stored MIME is more reliable). Returns (key, mime, true)
// on swap, ("", "", false) otherwise.
func (s *Service) swapToVariant(accessKey string, mode ProxyMode) (string, string, bool) {
	v, err := s.driveLookup.FindByAccessKey(accessKey)
	if err != nil {
		return "", "", false
	}
	// 既に primary 以外 (= 既に variant の access key) を要求されているなら
	// 二重に swap しない。
	if v.AccessKey == nil || *v.AccessKey != accessKey {
		return "", "", false
	}
	// Variant の MIME は driveFile 上に直接持たないので空文字を返し、
	// 呼び出し側で `http.DetectContentType` フォールバックさせる。stored
	// thumbnail/webpublic は通常 image/jpeg or image/webp なので sniffer
	// で正しく判別できる。
	switch mode {
	case ModeEmoji, ModeAvatar, ModePreview, ModeBadge:
		// 小サイズ系: thumbnail があれば優先、無ければ webpublic を試す。
		if v.ThumbnailAccessKey != nil && *v.ThumbnailAccessKey != "" {
			return *v.ThumbnailAccessKey, "", true
		}
		if v.WebpublicAccessKey != nil && *v.WebpublicAccessKey != "" {
			return *v.WebpublicAccessKey, "", true
		}
	case ModeStatic:
		// 498x422 用 mid-size mode。thumbnail (典型的に小さすぎる) には
		// fallback しない: upscale 表示や ratio 崩れを防ぐ (#637 review
		// 自 #2)。webpublic が無ければ swap せず原本を resize する。
		if v.WebpublicAccessKey != nil && *v.WebpublicAccessKey != "" {
			return *v.WebpublicAccessKey, "", true
		}
	}
	return "", "", false
}

// fetchRemote downloads a file from a remote URL.
func (s *Service) fetchRemote(ctx context.Context, rawURL string, mode ProxyMode, out OutputFormat) (*ProxyResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("mediaproxy: create request: %w", err)
	}
	req.Header.Set("User-Agent", s.userAgent)
	req.Header.Set("Accept", "image/*,*/*;q=0.5")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, ErrNotFound
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
		return nil, ErrNotFound
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("mediaproxy: remote returned %d", resp.StatusCode)
	}

	// Content-Lengthが明示されていてmaxDownloadを超える場合は即拒否
	if resp.ContentLength > maxDownload {
		return nil, ErrTooLarge
	}

	// maxDownload+1バイト読み、実際にmaxDownloadを超えたらエラー
	// Content-Lengthが嘘や未設定の場合でもサイレント切り捨てを防ぐ
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxDownload+1))
	if err != nil {
		return nil, fmt.Errorf("mediaproxy: read remote: %w", err)
	}
	if int64(len(data)) > maxDownload {
		return nil, ErrTooLarge
	}

	// Content-Type ヘッダーには `; charset=utf-8` 等のパラメータが付くことが
	// あるので、media type だけを切り出してから allowlist と比較する
	// (#418 Devin review)。`mime.ParseMediaType` 失敗時は元のヘッダ値を
	// そのまま使い、後段の DetectContentType フォールバックに任せる。
	rawCT := resp.Header.Get("Content-Type")
	contentType := rawCT
	if mt, _, err := mime.ParseMediaType(rawCT); err == nil {
		contentType = mt
	}
	if isUnknownBinary(contentType) {
		contentType = http.DetectContentType(data)
	}

	return s.processAndReturn(ctx, data, contentType, mode, out, rawURL)
}

// OutputFormat selects the encoder used by resize-class processing modes.
type OutputFormat int

const (
	// FormatWebP is the default browser-safe encoder.
	FormatWebP OutputFormat = iota
	// FormatAVIF requests AVIF output. caller-side preconditions: client
	// either set ?avif=1 or sent `Accept: image/avif,...`. AVIF は CPU
	// 重めなので明示要求時のみ使う (Misskey TS と同 semantics)。
	FormatAVIF
)

// processAndReturn applies image processing per mode and returns the result
// in the requested output format (WebP / AVIF). Badge と passThrough は
// format negotiation の対象外で常に PNG / 元 MIME を返す。
//
// video/* MIME は image pipeline に直接乗らないので、download 済の bytes
// をそのまま videoThumbnailGenerator に POST multipart で投げて静止画
// thumbnail を取得し、再帰的に image pipeline に通す。generator 未設定 /
// 呼び出し失敗のときは dummy PNG にフォールバックして frontend を壊さない
// (#637 M2)。bytes 経由なので local /files/ source も remote URL も同じ
// path で扱える。
func (s *Service) processAndReturn(ctx context.Context, data []byte, contentType string, mode ProxyMode, out OutputFormat, sourceURL string) (*ProxyResult, error) {
	if isVideoMIME(contentType) && isResizeMode(mode) {
		if s.videoThumbClient == nil {
			return makeDummyPNG(), nil
		}
		// GET mode は generator が sourceURL を fetch する前提なので、
		// local /files/<key> を投げても (UDS-only stack 等の) generator
		// から到達できないことが多い。早期 fallback で無駄な RT を省く。
		// POST mode では bytes 直送なので skip 不要。
		if s.videoThumbMode == "get" && strings.HasPrefix(sourceURL, s.instanceURL+"/files/") {
			return makeDummyPNG(), nil
		}
		frame, frameMIME, err := s.fetchVideoThumbnail(ctx, data, contentType, sourceURL)
		if err != nil {
			slog.Warn("mediaproxy: video thumbnail generator failed",
				"url", sourceURL, "err", err)
			return makeDummyPNG(), nil
		}
		data = frame
		contentType = frameMIME
	}
	// アニメ形式 (GIF / APNG) は image.Decode が 1 frame しか返さないため
	// resize 経路に乗せると静止画化される (#941)。emoji / avatar / preview の
	// ようにアニメ表示が期待される mode では pass-through で保持する。
	// static / badge は明示的に静止画を要求しているので従来通り decode する。
	if isAnimatedFormat(contentType) {
		switch mode {
		case ModeEmoji, ModeAvatar, ModePreview:
			return s.passThrough(data, contentType)
		}
	}
	// #2106 N19: resize 系 mode (emoji/avatar/static/preview/badge) は変換可能な image を
	// 要求する。video 静止画化 + animated passThrough を経た後でも convertible でない MIME
	// (text/html, application/pdf 等) は upstream FileServerProxyHandler の "Unexpected mime"
	// と同じく 404 にする。生 bytes を宣言 content-type で配信すると passThrough 経路の
	// browsersafe allowlist を回避でき、CSP の無い /proxy 上で XSS 補助面になる。
	if isResizeMode(mode) && !isConvertibleImage(contentType) {
		return nil, ErrNotFound
	}
	switch mode {
	case ModeEmoji:
		return s.processResize(data, contentType, 0, emojiHeight, out)
	case ModeAvatar:
		return s.processResize(data, contentType, 0, avatarHeight, out)
	case ModeStatic:
		return s.processResize(data, contentType, staticWidth, staticHeight, out)
	case ModePreview:
		return s.processResize(data, contentType, previewWidth, previewHeight, out)
	case ModeBadge:
		return s.processBadge(data, contentType)
	default:
		return s.passThrough(data, contentType)
	}
}

// isResizeMode reports whether the mode triggers image-pipeline processing
// (so a video source needs a still frame extracted first).
func isResizeMode(mode ProxyMode) bool {
	switch mode {
	case ModeEmoji, ModeAvatar, ModeStatic, ModePreview, ModeBadge:
		return true
	}
	return false
}

// processResize decodes the image, resizes it, and encodes to WebP or AVIF.
// width=0の場合はheightのみでアスペクト比を維持する。out=FormatAVIF の
// ときは AVIF で書き出し、エンコード失敗時は WebP に fallback する。
func (s *Service) processResize(data []byte, contentType string, width, height int, out OutputFormat) (*ProxyResult, error) {
	if !isConvertibleImage(contentType) {
		// 変換できない画像フォーマットはそのまま返す
		return makeResult(data, contentType), nil
	}

	img, err := decodeImage(data, contentType)
	if err != nil {
		// デコード失敗時は元データをそのまま返す
		return makeResult(data, contentType), nil
	}
	if exceedsPixelCap(img) {
		// pixel-bomb (32MB AVIF/HEIC/JXL が 30000x30000 に展開される類) は
		// resize/encode で多 GB のバッファを抱えるので dummy PNG にして
		// proxy の OOM を防ぐ (#637 review UR-016)。
		return makeDummyPNG(), nil
	}

	var resized image.Image
	if width == 0 {
		// height指定のみ: アスペクト比を維持して高さに合わせる
		resized = resizeToHeight(img, height)
	} else {
		resized = resizeFit(img, width, height)
	}

	if out == FormatAVIF {
		encoded, err := encodeAVIF(resized)
		if err == nil {
			return makeResult(encoded, "image/avif"), nil
		}
		// AVIF 失敗時は WebP に fallback。原因は libavif (wazero) 側の
		// memory limit / 入力色空間など多岐に渡るので observability の
		// ため warn level で残す (UR review nit #6)。
		slog.Warn("mediaproxy: avif encode failed, falling back to webp",
			"err", err)
	}
	encoded, err := encodeWebP(resized)
	if err != nil {
		return makeResult(data, contentType), nil
	}
	return makeResult(encoded, "image/webp"), nil
}

// exceedsPixelCap returns true when the decoded image's pixel count exceeds
// maxDecodedPixels. Used to refuse processing pixel-bomb inputs.
func exceedsPixelCap(img image.Image) bool {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return true
	}
	return int64(w)*int64(h) > maxDecodedPixels
}

// processBadge creates a 96x96 greyscale PNG badge.
func (s *Service) processBadge(data []byte, contentType string) (*ProxyResult, error) {
	if !isConvertibleImage(contentType) {
		return makeResult(data, contentType), nil
	}

	img, err := decodeImage(data, contentType)
	if err == nil && exceedsPixelCap(img) {
		return makeDummyPNG(), nil
	}
	if err != nil {
		return makeResult(data, contentType), nil
	}

	// 96x96にリサイズしてグレースケール変換
	resized := imaging.Fill(img, badgeSize, badgeSize, imaging.Center, imaging.Lanczos)
	grey := imaging.Grayscale(resized)

	var buf bytes.Buffer
	if err := png.Encode(&buf, grey); err != nil {
		return makeResult(data, contentType), nil
	}
	return makeResult(buf.Bytes(), "image/png"), nil
}

// passThrough validates the MIME type and returns data as-is.
func (s *Service) passThrough(data []byte, contentType string) (*ProxyResult, error) {
	// image/svg+xmlはセキュリティ上そのまま返さない（XSSリスク）
	if contentType == "image/svg+xml" {
		return s.svgFallback(data)
	}

	if !browsersafeMIMEs[contentType] {
		return nil, fmt.Errorf("mediaproxy: rejected MIME type: %s", contentType)
	}

	return makeResult(data, contentType), nil
}

// svgFallback converts SVG to a 1x1 transparent PNG placeholder.
// SVGラスタライズはv2で実装予定。
func (s *Service) svgFallback(_ []byte) (*ProxyResult, error) {
	return makeDummyPNG(), nil
}

// DummyPNG returns a 1x1 transparent PNG for fallback responses.
func DummyPNG() *ProxyResult {
	return makeDummyPNG()
}

func makeDummyPNG() *ProxyResult {
	img := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.Transparent)
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return makeResult(buf.Bytes(), "image/png")
}

func makeResult(data []byte, contentType string) *ProxyResult {
	return &ProxyResult{
		Body:        io.NopCloser(bytes.NewReader(data)),
		ContentType: contentType,
	}
}

// isUnknownBinary reports whether a Content-Type carries no type information,
// so the bytes themselves must be sniffed instead.
//
// **`application/octet-stream` だけを見てはいけない。** 標準はそちらだが、
// S3 互換ストレージの一部は `binary/octet-stream` を返す。どちらも「型が
// 分からない」以上の意味を持たないので、中身から判定し直す対象は同じ。
//
// 実際に、この 2 つを区別していたせいで `binary/octet-stream` を返す
// インスタンスの画像が resize 経路で `isConvertibleImage` に弾かれ、
// アイコン・バナー・カスタム絵文字がまとめて 404 になっていた (#2541)。
func isUnknownBinary(contentType string) bool {
	switch contentType {
	case "", "application/octet-stream", "binary/octet-stream":
		return true
	}
	return false
}

// isAnimatedFormat returns true for image MIME types that natively encode
// multi-frame animation (GIF, APNG variants)。
//
// Go の image.Decode は 1 frame しか取り出さないため、resize 経路に乗せると
// 静止画化されてしまう (#941)。emoji / avatar / preview のように「アニメ
// 表示が期待される」mode では pass-through すべき MIME 群。
func isAnimatedFormat(mime string) bool {
	switch mime {
	case "image/gif", "image/apng",
		"image/vnd.mozilla.apng":
		return true
	}
	return false
}

// isConvertibleImage returns true if the MIME type can be decoded and converted.
func isConvertibleImage(mime string) bool {
	switch mime {
	case "image/jpeg", "image/png", "image/gif",
		"image/webp", "image/bmp", "image/tiff",
		// IANA 公式名 (image/vnd.microsoft.icon) と古い慣例 (image/x-icon)
		// を両方許可する (#418)。
		"image/x-icon", "image/vnd.microsoft.icon",
		// APNG は 2 綴りある。drive 側の判定器が `image/apng` を返すように
		// なった (#2319) ので両方受ける。
		"image/vnd.mozilla.apng", "image/apng",
		// gen2brain wazero ベースの decoder で対応 (#637 M3/M4/M5):
		// image/avif (in/out), image/heic, image/heif, image/jxl は input 専用
		// として decode → WebP/AVIF 出力経路に乗せる。
		"image/avif", "image/heic", "image/heif", "image/jxl",
		// #672 Phase 1: pure Go decoder で input 対応。
		// Netpbm 系 (spakin/netpbm) と TGA (ftrvxmtrx/tga) は Go の標準
		// image.Decode に format register されるので decodeImage 経由で
		// 透過的に扱える。output は WebP/AVIF/PNG への transcode 経路に乗る。
		"image/x-portable-bitmap", "image/x-portable-graymap",
		"image/x-portable-pixmap", "image/x-portable-anymap",
		"image/x-tga", "image/x-targa",
		// #734: pure Go JPEG 2000 decoder (mrjoshuak/go-jpeg2000)。
		// JP2/J2K 両方 magic bytes 登録済みなので imaging.Decode 経由。
		// JPX (Part 2) は library 未対応のため除外 (browsersafe 側で
		// pass-through のみ受け入れる)。
		"image/jp2", "image/jpeg2000":
		return true
	default:
		return false
	}
}

func decodeImage(data []byte, contentType string) (image.Image, error) {
	// TGA は magic bytes が無いため image.RegisterFormat 経由の自動 dispatch
	// が他フォーマットを破壊する (ftrvxmtrx/tga の既知問題)。MIME type で
	// 明示判定して blezek/tga (auto-register 無し) の Decode を直接呼ぶ
	// (#672 Phase 1)。
	if contentType == "image/x-tga" || contentType == "image/x-targa" {
		img, err := tga.Decode(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		return img, nil
	}
	img, err := imaging.Decode(bytes.NewReader(data), imaging.AutoOrientation(true))
	if err != nil {
		return nil, err
	}
	return img, nil
}

// resizeToHeight resizes image to the specified height while preserving aspect
// ratio. 元画像がheight以下の場合は拡大し��い。
func resizeToHeight(img image.Image, height int) image.Image {
	bounds := img.Bounds()
	if bounds.Dy() <= height {
		return img
	}
	return imaging.Resize(img, 0, height, imaging.Lanczos)
}

// resizeFit resizes img to fit within maxW x maxH preserving aspect ratio.
func resizeFit(img image.Image, maxW, maxH int) image.Image {
	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if w <= maxW && h <= maxH {
		return img
	}
	return imaging.Fit(img, maxW, maxH, imaging.Lanczos)
}

func encodeWebP(img image.Image) ([]byte, error) {
	// gen2brain/webp は内部で image.Image を任意の型から libwebp に渡せるが、
	// 互換性のため明示的に NRGBA に正規化しておく (chai2010 時代と同等の前段)。
	bounds := img.Bounds()
	nrgba := image.NewNRGBA(bounds)
	draw.Draw(nrgba, bounds, img, bounds.Min, draw.Src)

	var buf bytes.Buffer
	if err := webp.Encode(&buf, nrgba, webp.Options{Quality: webpQuality}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// encodeAVIF encodes img as AVIF using gen2brain/avif (wazero based, no cgo).
// quality / speed は WebP より重いので avifSpeed=8 を default にしておく。
func encodeAVIF(img image.Image) ([]byte, error) {
	bounds := img.Bounds()
	nrgba := image.NewNRGBA(bounds)
	draw.Draw(nrgba, bounds, img, bounds.Min, draw.Src)

	var buf bytes.Buffer
	if err := avif.Encode(&buf, nrgba, avif.Options{Quality: avifQuality, Speed: avifSpeed}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
