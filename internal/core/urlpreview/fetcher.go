package urlpreview

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/shiroha-a/mk/internal/safehttp"
	"golang.org/x/net/html/charset"
)

var (
	ErrDisabled    = errors.New("urlpreview: feature disabled")
	ErrTooLarge    = errors.New("urlpreview: content too large")
	ErrFetchFailed = errors.New("urlpreview: fetch failed")
	// ErrPrivateIP is returned when the target host resolves to a private /
	// reserved IP. 互換性のため公開していた sentinel は維持しつつ、実体は
	// safehttp.ErrSSRFBlocked と同じ値にして AP / mediaproxy と統一する
	// (#638)。
	ErrPrivateIP = safehttp.ErrSSRFBlocked
)

const cachePrefix = "url-preview:"
const cacheTTL = 24 * time.Hour

// Config holds URL preview settings read from Meta.
type Config struct {
	Enabled              bool
	AllowRedirect        bool
	TimeoutMs            int
	MaxContentLength     int64
	RequireContentLength bool
	SummaryProxyURL      string
	UserAgent            string
}

// Fetcher fetches and parses URL previews with caching and SSRF protection.
type Fetcher struct {
	cfg         Config
	redis       *redis.Client
	keyPrefix   string // TS drop-in互換用 `<host>:` prefix
	client      *http.Client
	proxyClient *http.Client
}

// SetHTTPClient replaces the HTTP client (for tests that need to bypass
// the SSRF dialer or use a custom transport).
func (f *Fetcher) SetHTTPClient(c *http.Client) {
	f.client = c
}

// NewFetcher creates a URL preview Fetcher.
//
// keyPrefix (通常は `cfg.Redis.KeyPrefix()` = `<host>:`) は TS 本家と同じ
// キー名前空間の下にキャッシュキーを置くために前置される。空文字列なら
// prefix 無し。
//
// allowedPrivateNetworks は SSRF guard で例外的に許可する CIDR (config の
// allowedPrivateNetworks をそのまま渡す)。transportOpts は forward proxy /
// outgoing address / address family などの outbound 設定で、AP fetch や
// media proxy と挙動を揃えるため `safehttp.WithProxy(...)` などを渡す
// (#638)。
func NewFetcher(cfg Config, rdb *redis.Client, keyPrefix string, allowedPrivateNetworks []string, transportOpts ...safehttp.Option) *Fetcher {
	timeout := time.Duration(cfg.TimeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	client := &http.Client{
		Timeout:   timeout,
		Transport: safehttp.NewSSRFSafeTransport(allowedPrivateNetworks, transportOpts...),
	}
	if !cfg.AllowRedirect {
		client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		}
	}

	return &Fetcher{
		cfg:       cfg,
		redis:     rdb,
		keyPrefix: keyPrefix,
		client:    client,
		// proxyClient は管理者が設定した summalyProxy への呼び出し用。
		// operator-trusted endpoint (社内 summaly や localhost に立てた
		// proxy) を指定するケースがあるため SSRF guard は適用しない。
		// ここに forward proxy を掛けると 127.0.0.1 への内向き呼び出しまで
		// proxy 経由になって壊れるので、明示的に裸の Client を使う。
		proxyClient: &http.Client{Timeout: timeout},
	}
}

// Fetch returns the URL preview, using Redis cache when available.
func (f *Fetcher) Fetch(ctx context.Context, rawURL string) (*Result, error) {
	if !f.cfg.Enabled {
		return nil, ErrDisabled
	}

	// 外部summarizer proxyが設定されていればそちらに委譲する。
	if f.cfg.SummaryProxyURL != "" {
		return f.fetchViaProxy(ctx, rawURL)
	}

	// Redisキャッシュ確認
	cacheKey := f.keyPrefix + cachePrefix + hashURL(rawURL)
	if f.redis != nil {
		if cached, err := f.redis.Get(ctx, cacheKey).Bytes(); err == nil {
			var result Result
			if json.Unmarshal(cached, &result) == nil {
				return &result, nil
			}
		}
	}

	result, err := f.fetchAndParse(ctx, rawURL)
	if err != nil {
		return nil, err
	}

	// Redisキャッシュ保存
	if f.redis != nil {
		if data, err := json.Marshal(result); err == nil {
			f.redis.Set(ctx, cacheKey, data, cacheTTL)
		}
	}

	return result, nil
}

func (f *Fetcher) fetchAndParse(ctx context.Context, rawURL string) (*Result, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrFetchFailed, err)
	}
	ua := f.cfg.UserAgent
	if ua == "" {
		ua = "Misskey URL Preview"
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept", "text/html,*/*;q=0.5")

	resp, err := f.client.Do(req)
	if err != nil {
		if errors.Is(err, ErrPrivateIP) {
			return nil, ErrPrivateIP
		}
		return nil, fmt.Errorf("%w: %v", ErrFetchFailed, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%w: status %d", ErrFetchFailed, resp.StatusCode)
	}

	// Content-Lengthチェック
	if f.cfg.RequireContentLength {
		cl := resp.Header.Get("Content-Length")
		if cl == "" {
			return nil, ErrTooLarge
		}
	}
	if f.cfg.MaxContentLength > 0 {
		if cl, err := strconv.ParseInt(resp.Header.Get("Content-Length"), 10, 64); err == nil && cl > f.cfg.MaxContentLength {
			return nil, ErrTooLarge
		}
	}

	// HTMLのみ解析する。それ以外はURLだけ返す。
	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "text/html") && !strings.Contains(ct, "application/xhtml") {
		return &Result{URL: rawURL, Player: PlayerResult{Allow: []string{}}}, nil
	}

	// charset.NewReader が Content-Type の charset と HTML <meta charset> を
	// 自動判定して UTF-8 に正規化する。Shift_JIS / EUC-JP / ISO-2022-JP の
	// 日本語ページで title / description が文字化けする drift を解消 (#942)。
	limited := io.LimitReader(resp.Body, f.cfg.MaxContentLength)
	body, err := charset.NewReader(limited, ct)
	if err != nil {
		// charset 判定不能 / unsupported encoding は raw bytes でフォールバック。
		// ParseHTML は UTF-8 仮定だが、ASCII / UTF-8 互換ページなら問題なく動く。
		body = limited
	}
	result := ParseHTML(body, rawURL)

	// oEmbed discovery: HTML 中に <link rel="alternate" type=
	// "application/json+oembed"> があれば追加で fetch して PlayerResult を
	// 埋める (#639)。失敗は preview 全体を壊さない (best-effort)。
	if result.oEmbedURL != "" {
		if player, err := f.fetchOEmbed(ctx, result.oEmbedURL); err == nil {
			result.Player = player
		}
	}
	return result, nil
}

// fetchViaProxy delegates to an external summarizer service.
//
// proxyは管理者が設定した信頼済みURLなのでSSRFチェックを適用しないが、
// rawURLはクエリパラメータとして安全にエスケープする。
func (f *Fetcher) fetchViaProxy(ctx context.Context, rawURL string) (*Result, error) {
	proxyURL := f.cfg.SummaryProxyURL + "?url=" + url.QueryEscape(rawURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, proxyURL, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrFetchFailed, err)
	}
	resp, err := f.proxyClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrFetchFailed, err)
	}
	defer resp.Body.Close()

	var result Result
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("%w: parse proxy response: %v", ErrFetchFailed, err)
	}
	return &result, nil
}

func hashURL(u string) string {
	h := sha256.Sum256([]byte(u))
	return hex.EncodeToString(h[:])
}
