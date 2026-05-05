package server

import (
	"net"
	"net/http"
	"testing"

	"github.com/shiroha-a/mk/internal/config"
	"github.com/stretchr/testify/assert"
)

// parseTrustedDefault は extractIPFallback / IPExtractor wrap のテスト用に
// DefaultTrustProxy を *net.IPNet 配列に展開するヘルパ。
func parseTrustedDefault(t *testing.T) []*net.IPNet {
	t.Helper()
	return config.ParseTrustProxy(config.DefaultTrustProxy)
}

// UDS 経由 (RemoteAddr 空 + nginx が XFF を埋めるケース) で client IP が
// 抽出できることを確認する。Echo 標準 extractor が壊れる #703 の根本原因
// に対応する fallback の主目的。
func TestExtractIPFallback_UDSWithXFF(t *testing.T) {
	trusted := parseTrustedDefault(t)
	req, _ := http.NewRequest(http.MethodGet, "/", nil)
	// nginx -> mkgo (UDS) では RemoteAddr は "" のまま、XFF に
	// Cloudflare → nginx の経路が積まれる。
	req.RemoteAddr = ""
	req.Header.Set("X-Forwarded-For", "2405:6585:d720:e4:a090:d3d9:7372:f96a, 172.21.0.1")

	got := extractIPFallback(req, trusted)
	assert.Equal(t, "2405:6585:d720:e4:a090:d3d9:7372:f96a", got)
}

// 角括弧付き IPv6 (Cloudflare が時々送る形式) も剥がして parse できる。
func TestExtractIPFallback_BracketedIPv6(t *testing.T) {
	trusted := parseTrustedDefault(t)
	req, _ := http.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Forwarded-For", "[2405:6585:d720:e4:a090:d3d9:7372:f96a], 172.21.0.1")

	got := extractIPFallback(req, trusted)
	assert.Equal(t, "2405:6585:d720:e4:a090:d3d9:7372:f96a", got)
}

// XFF が無く X-Real-IP のみ来るケース (一部 proxy / Cloudflare 単独) でも
// 取れる。
func TestExtractIPFallback_RealIPOnly(t *testing.T) {
	trusted := parseTrustedDefault(t)
	req, _ := http.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Real-IP", "203.0.113.7")

	got := extractIPFallback(req, trusted)
	assert.Equal(t, "203.0.113.7", got)
}

// XFF / X-Real-IP どちらも無い場合 (例: 内部ヘルスチェック等) は空文字を
// 返す。これは fallback 経路として正しい挙動 (Echo 標準も同じ振る舞い)。
func TestExtractIPFallback_NoHeaders(t *testing.T) {
	trusted := parseTrustedDefault(t)
	req, _ := http.NewRequest(http.MethodGet, "/", nil)

	got := extractIPFallback(req, trusted)
	assert.Equal(t, "", got)
}

// XFF 中に解析不能な値が混じっていても、その後ろの解析可能な untrusted IP
// を拾える (Echo 標準は早期 return で directIP を返してしまう)。
func TestExtractIPFallback_SkipsUnparseableEntries(t *testing.T) {
	trusted := parseTrustedDefault(t)
	req, _ := http.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Forwarded-For", "203.0.113.42, garbage, 172.21.0.1")

	got := extractIPFallback(req, trusted)
	assert.Equal(t, "203.0.113.42", got)
}

// 全 IP が trusted ranges 内なら left-most の parseable IP を返す。
// 内部だけで完結する request 用の best-effort fallback。
func TestExtractIPFallback_AllTrustedReturnsLeftmost(t *testing.T) {
	trusted := parseTrustedDefault(t)
	req, _ := http.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Forwarded-For", "10.1.2.3, 172.16.0.5")

	got := extractIPFallback(req, trusted)
	assert.Equal(t, "10.1.2.3", got)
}

// X-Real-IP に角括弧付き IPv6 が来てもそのまま剥がす。
func TestExtractIPFallback_RealIPBracketedIPv6(t *testing.T) {
	trusted := parseTrustedDefault(t)
	req, _ := http.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Real-IP", "[2001:db8::1]")

	got := extractIPFallback(req, trusted)
	assert.Equal(t, "2001:db8::1", got)
}

// XFF 全部解析不能なら X-Real-IP に fallback する。
func TestExtractIPFallback_XFFAllUnparseableFallbackToRealIP(t *testing.T) {
	trusted := parseTrustedDefault(t)
	req, _ := http.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Forwarded-For", "garbage1, garbage2")
	req.Header.Set("X-Real-IP", "203.0.113.99")

	got := extractIPFallback(req, trusted)
	assert.Equal(t, "203.0.113.99", got)
}

// 通常の TCP listen 経路 (RemoteAddr あり) では inner extractor の結果を
// そのまま返し fallback は走らない。wire path の regression を防ぐ。
func TestBuildIPExtractor_TCPDirectPath(t *testing.T) {
	extractor := buildIPExtractor(parseTrustedDefault(t))
	req, _ := http.NewRequest(http.MethodGet, "/", nil)
	// 通常 TCP では net.SplitHostPort で取れる形式
	req.RemoteAddr = "203.0.113.42:54321"

	got := extractor(req)
	assert.Equal(t, "203.0.113.42", got)
}

// inner extractor が untrusted な valid IP を返した場合そのまま返す。
// trustProxy 越しに正規にアクセスされたケース。
func TestBuildIPExtractor_TrustedProxyXFF(t *testing.T) {
	extractor := buildIPExtractor(parseTrustedDefault(t))
	req, _ := http.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "172.21.0.5:12345" // trusted (private)
	req.Header.Set("X-Forwarded-For", "203.0.113.7, 172.21.0.1")

	got := extractor(req)
	assert.Equal(t, "203.0.113.7", got)
}

// UDS 経由 (RemoteAddr="") で inner が空を返すケースを fallback が拾う。
// 本 PR の regression を直接 lock down する critical path。
func TestBuildIPExtractor_UDSFallbackPath(t *testing.T) {
	extractor := buildIPExtractor(parseTrustedDefault(t))
	req, _ := http.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "" // UDS
	req.Header.Set("X-Forwarded-For", "2405:6585:d720:e4:a090:d3d9:7372:f96a, 172.21.0.1")

	got := extractor(req)
	assert.Equal(t, "2405:6585:d720:e4:a090:d3d9:7372:f96a", got)
}

// trusted 引数が nil/空でも buildIPExtractor 自体は構築できる
// (router 側では len(nets) > 0 で配線するため通常は呼ばれないが、
// API として nil-safe であることの保険)。
func TestBuildIPExtractor_NoTrustedRanges(t *testing.T) {
	extractor := buildIPExtractor(nil)
	req, _ := http.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "203.0.113.42:54321"

	got := extractor(req)
	assert.Equal(t, "203.0.113.42", got)
}
