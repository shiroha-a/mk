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
	"math"
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
	"github.com/shiroha-a/mk/internal/misc/imagedecode"
	_ "github.com/spakin/netpbm" // PBM/PGM/PPM/PAM input decode (#672 Phase 1)
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
	// badgeMinEntropy は upstream の `stats().entropy < 0.1` と同じ閾値 (#2920)。
	badgeMinEntropy = 0.1
	// sharp の normalise() の既定 (実測)。min-max ではない。
	badgeNormaliseLower = 1.0
	badgeNormaliseUpper = 99.0
	webpQuality         = 77
	avifQuality         = 60       // AVIF default; matches gen2brain/avif.DefaultQuality
	avifSpeed           = 8        // 0..10 — 8 keeps quality close to default while staying responsive
	maxDownload         = 32 << 20 // 32 MB
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
// Fetch retrieves and processes a remote image.
//
// **animated=false はアニメーションを静止画にする (#2905)。** mode と直交する —
// upstream は `?emoji=1&static=1` を「emoji のサイズで、ただし静止画」として扱う
// (`FileServerProxyHandler.ts` の `animated: !('static' in query)`)。mode を
// ModeStatic に倒すとリサイズ寸法まで変わってしまうので、別の軸で持つ。
func (s *Service) Fetch(ctx context.Context, rawURL string, mode ProxyMode, out OutputFormat, animated bool) (*ProxyResult, error) {
	// ローカルファイルの場合はdriveStorageから直接取得
	filesPrefix := s.instanceURL + "/files/"
	if strings.HasPrefix(rawURL, filesPrefix) {
		return s.resolveLocal(ctx, rawURL, filesPrefix, mode, out, animated)
	}

	return s.fetchRemote(ctx, rawURL, mode, out, animated)
}

// resolveLocal fetches a file from local drive storage by access key.
func (s *Service) resolveLocal(ctx context.Context, rawURL, filesPrefix string, mode ProxyMode, out OutputFormat, animated bool) (*ProxyResult, error) {
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
	return s.processAndReturn(ctx, data, contentType, mode, out, rawURL, animated)
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
func (s *Service) fetchRemote(ctx context.Context, rawURL string, mode ProxyMode, out OutputFormat, animated bool) (*ProxyResult, error) {
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

	return s.processAndReturn(ctx, data, contentType, mode, out, rawURL, animated)
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
func (s *Service) processAndReturn(ctx context.Context, data []byte, contentType string, mode ProxyMode, out OutputFormat, sourceURL string, animated bool) (*ProxyResult, error) {
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
	//
	// **animated=false なら pass-through しない (#2905)。** 利用者の
	// 「アニメーション画像を再生しない」設定 (disableShowingAnimatedImages) が
	// `?emoji=1&static=1` として届くが、mode だけを見ていたので無視されていた。
	if isAnimatedFormat(contentType) && animated {
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

// processBadge creates a 96x96 badge PNG, mirroring upstream
// FileServerProxyHandler.processBadge (#2920).
//
// **sharp は呼び出し順ではなく固定の pipeline 順で処理する。** JS の記述は
//
//	resize(...).greyscale().normalise().linear(1.75, -(128*1.75)+128)
//	  .flatten({background: '#000'}).toColorspace('b-w')
//
// だが、`sharp/src/pipeline.cc` の実行順は **flatten → greyscale → resize/embed →
// linear → normalise**。記述順どおりに normalise → linear → flatten と書くと、
// 輝度レンジの狭い絵文字で平均絶対差 28.7/255・43.8% の画素が 32 以上ずれる
// (実測)。**呼び出し順を入れ替えても sharp の出力が 1 バイトも変わらない**ことで
// 固定順であることを確認してある。
//
// **contain であって cover ではない。** 以前は imaging.Fill (cover + 中央クロップ)
// だったので、200x120 の絵文字は横幅の 40% が失われていた。
//
// **最後の XOR は no-op ではない。** 透明な RGBA canvas (全バイト 0) と mask を
// XOR すると R=G=B=A=mask になる。つまり**暗いところが透明**な silhouette に
// なる。通知 UI の背景に乗せる形なのでこれが要る。
func (s *Service) processBadge(data []byte, contentType string) (*ProxyResult, error) {
	// upstream の `requiresImageConversion && !isConvertibleImage` → 404
	// (`Unexpected mime`)。**本番経路では Fetch 側の
	// `isResizeMode(mode) && !isConvertibleImage` が先に同じ 404 を返す**ので
	// ここは到達しないが、processBadge 単体の契約として揃えておく。
	if !isConvertibleImage(contentType) {
		return nil, ErrNotFound
	}

	img, err := decodeImage(data, contentType)
	if err != nil {
		return nil, ErrNotFound
	}
	if exceedsPixelCap(img) {
		return makeDummyPNG(), nil
	}

	// **entropy は元画像で測る。** upstream の `stats()` は
	// `sharp/src/stats.cc` で入力を開き直すので、**pipeline の操作を一切
	// 適用しない**。`linear(0, 100)` で定数化しても `threshold(128)` で 2 値化
	// しても値が変わらないことで確認してある (どれも log2(値の種類) に一致)。
	// つまり判定は「元画像の greyscale が実質単色か」。
	// **Clone は 1 回だけ。** 入力を NRGBA に正規化して Pix を直読みする。
	// **`normalizeForResize` を先に通す** — `imaging.Clone` は `*image.NYCbCrA`
	// の alpha を行単位で壊す (#2925)。
	src := imaging.Clone(normalizeForResize(img))
	if inputEntropy(src) < badgeMinEntropy {
		// upstream の `StatusError('Skip to provide badge', 404)`。SW の
		// create-notification.ts は `res.status !== 200` で iconUrl('plus') へ
		// 落ちるので、判別できないバッジを配るより 404 の方が親切。
		return nil, ErrNotFound
	}

	mask := badgeMask(src)
	out := image.NewNRGBA(image.Rect(0, 0, badgeSize, badgeSize))
	for i, v := range mask {
		out.Pix[i*4] = v
		out.Pix[i*4+1] = v
		out.Pix[i*4+2] = v
		out.Pix[i*4+3] = v
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, out); err != nil {
		return nil, ErrNotFound
	}
	return makeResult(buf.Bytes(), "image/png"), nil
}

// srgbToLinear / linearToSRGB convert one 0-255 sRGB channel to and from linear
// light. vips の `colourspace(B_W)` は線形光を経由するので、係数を sRGB 値へ
// 直接掛けると彩度の高い色で大きくずれる (実測: 純赤は vips 127 に対し 54)。
var srgbToLinearLUT = func() [256]float64 {
	var t [256]float64
	for i := range t {
		c := float64(i) / 255
		if c <= 0.04045 {
			t[i] = c / 12.92
		} else {
			t[i] = math.Pow((c+0.055)/1.055, 2.4)
		}
	}
	return t
}()

// linearToSRGBLUT maps linear luminance (0-1, 16bit 量子化) to the 0-255 sRGB
// value. **画素ごとに math.Pow を呼ぶと重い** — 4000x4000 の入力で 2 秒かかって
// いたところの大半がこれだった。丸め誤差は最終的な 0-255 への丸めに吸収される。
var linearToSRGBLUT = func() [65536]uint8 {
	var t [65536]uint8
	for i := range t {
		v := float64(i) / 65535
		var c float64
		if v <= 0.0031308 {
			c = v * 12.92
		} else {
			c = 1.055*math.Pow(v, 1/2.4) - 0.055
		}
		t[i] = uint8(math.Round(math.Min(255, math.Max(0, c*255))))
	}
	return t
}()

// greyLevel returns the vips-compatible greyscale value of one sRGB pixel.
func greyLevel(r, g, b uint8) uint8 {
	y := 0.2126*srgbToLinearLUT[r] + 0.7152*srgbToLinearLUT[g] + 0.0722*srgbToLinearLUT[b]
	if y <= 0 {
		return 0
	}
	if y >= 1 {
		return 255
	}
	return linearToSRGBLUT[int(y*65535+0.5)]
}

// inputEntropy returns the Shannon entropy (bits) of the source image's
// greyscale histogram, which is what upstream's `stats().entropy` measures.
// **alpha は見ない** — upstream も RGB の greyscale だけを測る (RGB 一様で alpha
// だけ変化する画像の entropy が 0 になることで確認済み)。
func inputEntropy(src *image.NRGBA) float64 {
	// **`img.At()` で舐めない。** 画素ごとに interface boxing が起きるので、
	// 4000x4000 の入力で 32M アロケーション・2.7 秒になる (実測)。
	// `imaging.Clone` で一度 NRGBA にしてから Pix を直読みする。
	//
	// **非乗算で読むのが要点。** `At().RGBA()` は premultiplied を返すので、
	// そのままだと「alpha を見ない」つもりが透明部分だけ 0 に潰れる。
	var hist [256]int
	n := len(src.Pix) / 4
	for i := 0; i < n; i++ {
		hist[greyLevel(src.Pix[i*4], src.Pix[i*4+1], src.Pix[i*4+2])]++
	}
	return histEntropy(hist[:], n)
}

func histEntropy(hist []int, n int) float64 {
	if n == 0 {
		return 0
	}
	total := float64(n)
	entropy := 0.0
	for _, c := range hist {
		if c == 0 {
			continue
		}
		p := float64(c) / total
		entropy -= p * math.Log2(p)
	}
	return entropy
}

func clampByte(v float64) uint8 {
	switch {
	case v <= 0:
		return 0
	case v >= 255:
		return 255
	default:
		return uint8(math.Round(v))
	}
}

// badgeMask runs the upstream badge pipeline and returns the 96x96 mask as one
// byte per pixel (row-major). 順序は sharp の pipeline 順
// (flatten → greyscale → resize/embed → linear → normalise)。
func badgeMask(src *image.NRGBA) []byte {
	// 1. flatten({background: '#000'}) → 2. greyscale
	//    **resize より前**なので、透明部分は先に黒へ落ちる。
	w, h := src.Bounds().Dx(), src.Bounds().Dy()
	flat := image.NewGray(image.Rect(0, 0, w, h))
	for i := 0; i < w*h; i++ {
		// flatten({background: '#000'}) は RGB×alpha。NRGBA は非乗算なので
		// ここで掛ける (`At().RGBA()` の premultiplied 値を使うと、透明部の
		// RGB が読めない decoder で潰れる)。
		a := uint32(src.Pix[i*4+3])
		flat.Pix[i] = greyLevel(
			uint8(uint32(src.Pix[i*4])*a/255),
			uint8(uint32(src.Pix[i*4+1])*a/255),
			uint8(uint32(src.Pix[i*4+2])*a/255))
	}

	// 3. resize(96, 96, {fit: 'contain', withoutEnlargement: false}) + embed(黒)
	//    **拡大もする**。imaging.Fit は縮小しかしないので倍率を自分で出す。
	scale := math.Min(float64(badgeSize)/float64(w), float64(badgeSize)/float64(h))
	nw := max(1, int(math.Round(float64(w)*scale)))
	nh := max(1, int(math.Round(float64(h)*scale)))
	fitted := imaging.Resize(flat, nw, nh, imaging.Lanczos)
	canvas := imaging.New(badgeSize, badgeSize, color.NRGBA{A: 255})
	// **切り捨てで左上寄せ** — sharp の CalculateEmbedPosition と同じ。
	// imaging.PasteCenter は奇数差で右下へ寄るので 1px ずれる。
	canvas = imaging.Paste(canvas, fitted, image.Pt((badgeSize-nw)/2, (badgeSize-nh)/2))

	n := badgeSize * badgeSize
	vals := make([]float64, n)
	for i := 0; i < n; i++ {
		// 4. linear(1.75, -(128*1.75)+128)。sharp は uchar:true なので
		//    この時点で 0-255 にクリップされる。
		v := float64(canvas.Pix[i*4])
		v = math.Round(1.75*v - 96)
		v = math.Min(255, math.Max(0, v))
		vals[i] = v
	}

	// 5. normalise。**min-max ではなく 1/99 パーセンタイル。**
	//    sharp の既定は `{lower: 1, upper: 99}` (実測) で、`operations.cc` は
	//    `luminance.percent(lower)` / `percent(upper)` を使う。min-max で
	//    実装すると、暗部・明部の裾が薄い実写系の画像で upstream との
	//    平均絶対差が 22.7/255・最大 40 になる (upstream 自身のテスト画像
	//    `test/resources/192.png` で実測)。
	return normaliseToBytes(vals)
}

// normaliseToBytes applies sharp's `normalise()` to the 0-255 values.
//
// **min-max ではなく 1/99 パーセンタイル。** sharp の既定は
// `{lower: 1, upper: 99}` (実測) で、`operations.cc` は `luminance.percent(lower)`
// / `percent(upper)` を使う。min-max で実装すると、暗部・明部の裾が薄い実写系の
// 画像で upstream との平均絶対差が 23.9/255・最大 42・32 を超える画素 16.7% に
// なる (upstream 自身のテスト画像 `test/resources/192.png` で実測)。
func normaliseToBytes(vals []float64) []byte {
	out := make([]byte, len(vals))
	lo, hi := percentileRange(vals, badgeNormaliseLower, badgeNormaliseUpper)
	// upstream の `std::abs(max - min) > 1`。**単位が違う** — あちらは LAB の L
	// (0-100) なので、0-255 では 2.55 に相当する。ここを跨ぐのは実質単色の
	// 画像だけで、それらは元画像の entropy guard で先に 404 になる。
	span := hi - lo
	for i, v := range vals {
		if span > 1 {
			v = (v - lo) * 255 / span
		}
		out[i] = clampByte(v)
	}
	return out
}

// percentileRange returns the values at the lower/upper percentiles of the
// 0-255 histogram of vals, mirroring vips' `percent()`.
func percentileRange(vals []float64, lower, upper float64) (float64, float64) {
	var hist [256]int
	for _, v := range vals {
		hist[clampByte(v)]++
	}
	n := len(vals)
	at := func(pct float64) float64 {
		want := pct / 100 * float64(n)
		cum := 0
		for v, c := range hist {
			cum += c
			if float64(cum) >= want {
				return float64(v)
			}
		}
		return 255
	}
	return at(lower), at(upper)
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
	// **インターレースの truecolor PNG は decoder のバグを踏む (#2925)。**
	// 規則は `internal/misc/imagedecode` に 1 つだけ置いてある — drive 側の
	// image processor も同じ decode をしており、片方だけ直すともう片方に
	// 同じバグが残る (初版で実際にそうなっていた)。
	img, err := imagedecode.Decode(data)
	if err != nil {
		return nil, err
	}
	return img, nil
}

// normalizeForResize converts img to NRGBA when the resize library cannot read
// its type correctly.
//
// **`*image.NYCbCrA` を imaging に直接渡すと全画素が 0 になる。** アルファ付きの
// 拡張 WebP (VP8X + ALPH + VP8) を Go の webp デコーダが返す型で、
// kovidgoyal/imaging v1.8.21 の scanner が 4:2:0 の NYCbCrA を読めていない。
// デコード自体は成功しているので、**200 で真っ白 (完全透明) の画像が返る**
// という気づきにくい壊れ方をする (リモート利用者のアイコンが出ない、#2591)。
//
// 単純な VP8 は `*image.YCbCr` になるので影響を受けない。ここで型を絞って
// 変換するのは、NRGBA 化が画素あたりのコピーを 1 回増やすため。
func normalizeForResize(img image.Image) image.Image {
	src, ok := img.(*image.NYCbCrA)
	if !ok {
		return img
	}
	// **`imaging.Clone` にも `draw.Draw` にも渡せない (#2925)。** どちらも
	// 片方を壊す:
	//
	//   - `draw.Draw` / `At()` は premultiplied な値しか出さないので、
	//     **完全に透明な画素の RGB が復元できず 0 に潰れる**
	//   - `imaging.Clone` は RGB を plane から正しく読むが、**サブサンプル
	//     (4:2:0 / 4:2:2 / 4:4:0 = lossy WebP の通常形) の分岐で alpha の
	//     index を内側ループで進めない**ため、行の全画素がその行の先頭画素の
	//     alpha になる (`nrgba/scanner.go`)。upstream のテスト画像
	//     `with-alpha.webp` で 22.5% の画素の alpha が誤り、badge を sharp と
	//     比べると 32 を超える差が 16.6% の画素に出る
	//
	// Y / Cb / Cr / A の plane を自分で読めば両方とも正しく取れる。
	b := src.Bounds()
	dst := image.NewNRGBA(b)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl := color.YCbCrToRGB(
				src.Y[src.YOffset(x, y)],
				src.Cb[src.COffset(x, y)],
				src.Cr[src.COffset(x, y)])
			i := dst.PixOffset(x, y)
			dst.Pix[i] = r
			dst.Pix[i+1] = g
			dst.Pix[i+2] = bl
			dst.Pix[i+3] = src.A[src.AOffset(x, y)]
		}
	}
	return dst
}

// resizeToHeight resizes image to the specified height while preserving aspect
// ratio. 元画像がheight以下の場合は拡大し��い。
func resizeToHeight(img image.Image, height int) image.Image {
	bounds := img.Bounds()
	if bounds.Dy() <= height {
		return img
	}
	return imaging.Resize(normalizeForResize(img), 0, height, imaging.Lanczos)
}

// resizeFit resizes img to fit within maxW x maxH preserving aspect ratio.
func resizeFit(img image.Image, maxW, maxH int) image.Image {
	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if w <= maxW && h <= maxH {
		return img
	}
	return imaging.Fit(normalizeForResize(img), maxW, maxH, imaging.Lanczos)
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
