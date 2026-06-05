package safehttp

import (
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsPrivateIP_IPv4Private(t *testing.T) {
	tests := []struct {
		name string
		ip   string
	}{
		{"loopback", "127.0.0.1"},
		{"loopback high", "127.255.255.255"},
		{"10.x", "10.0.0.1"},
		{"10.x high", "10.255.255.255"},
		{"172.16.x", "172.16.0.1"},
		{"172.31.x", "172.31.255.255"},
		{"192.168.x", "192.168.1.1"},
		{"link-local", "169.254.1.1"},
		{"zero network", "0.0.0.1"},
		{"CGN", "100.64.0.1"},
		{"documentation 192.0.2.x", "192.0.2.1"},
		{"documentation 198.51.100.x", "198.51.100.1"},
		{"documentation 203.0.113.x", "203.0.113.1"},
		{"benchmark 198.18.x", "198.18.0.1"},
		{"multicast", "224.0.0.1"},
		{"reserved 240.x", "240.0.0.1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.True(t, isPrivateIP(net.ParseIP(tt.ip), nil), "%s should be private", tt.ip)
		})
	}
}

func TestIsPrivateIP_IPv6Private(t *testing.T) {
	tests := []struct {
		name string
		ip   string
	}{
		{"loopback", "::1"},
		{"link-local", "fe80::1"},
		{"unique-local fd", "fd00::1"},
		{"unique-local fc", "fc00::1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.True(t, isPrivateIP(net.ParseIP(tt.ip), nil), "%s should be private", tt.ip)
		})
	}
}

func TestIsPrivateIP_IPv4MappedIPv6(t *testing.T) {
	// ::ffff:127.0.0.1 はIPv4-mapped IPv6であり、中身の127.0.0.1で判定すべき
	ip := net.ParseIP("::ffff:127.0.0.1")
	assert.True(t, isPrivateIP(ip, nil))

	ip = net.ParseIP("::ffff:10.0.0.1")
	assert.True(t, isPrivateIP(ip, nil))
}

func TestIsPrivateIP_PublicIP(t *testing.T) {
	tests := []struct {
		name string
		ip   string
	}{
		{"Google DNS", "8.8.8.8"},
		{"Cloudflare DNS", "1.1.1.1"},
		{"random public", "203.104.209.7"},
		{"IPv6 public", "2001:4860:4860::8888"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.False(t, isPrivateIP(net.ParseIP(tt.ip), nil), "%s should be public", tt.ip)
		})
	}
}

func TestIsPrivateIP_AllowedCIDR(t *testing.T) {
	_, allowedNet, _ := net.ParseCIDR("10.0.0.0/24")
	allowedNets := []*net.IPNet{allowedNet}

	// 10.0.0.1 はプライベートだが allowedNets に含まれるので許可
	assert.False(t, isPrivateIP(net.ParseIP("10.0.0.1"), allowedNets))

	// 10.1.0.1 は allowedNets に含まれないのでブロック
	assert.True(t, isPrivateIP(net.ParseIP("10.1.0.1"), allowedNets))
}

func TestIsPrivateIP_AllowedCIDR_IPv6(t *testing.T) {
	_, allowedNet, _ := net.ParseCIDR("fd00::/64")
	allowedNets := []*net.IPNet{allowedNet}

	assert.False(t, isPrivateIP(net.ParseIP("fd00::1"), allowedNets))
	assert.True(t, isPrivateIP(net.ParseIP("fd01::1"), allowedNets))
}

func TestNewSSRFSafeTransport_InvalidCIDR(t *testing.T) {
	// 不正なCIDRは無視される（panicしない）
	tr := NewSSRFSafeTransport([]string{"invalid-cidr", "10.0.0.0/8"})
	assert.NotNil(t, tr)
}

func TestNewSSRFSafeTransport_NilCIDRs(t *testing.T) {
	tr := NewSSRFSafeTransport(nil)
	assert.NotNil(t, tr)
}

func TestNewSSRFSafeTransport_BlocksLoopback(t *testing.T) {
	// httptestサーバーは127.0.0.1で起動するのでSSRF保護でブロックされるはず
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	tr := NewSSRFSafeTransport(nil)
	client := &http.Client{Transport: tr}

	_, err := client.Get(ts.URL + "/test")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "private IP blocked")
}

func TestNewSSRFSafeTransport_AllowsLoopbackWithCIDR(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	tr := NewSSRFSafeTransport([]string{"127.0.0.0/8"})
	client := &http.Client{Transport: tr}

	resp, err := client.Get(ts.URL + "/test")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestNewSSRFSafeTransport_DNSResolutionFailure(t *testing.T) {
	tr := NewSSRFSafeTransport(nil)
	client := &http.Client{Transport: tr}

	_, err := client.Get("http://nonexistent.invalid/test")
	assert.Error(t, err)
}

// proxy 経路で外向きリクエストが proxy server に届くこと、proxy 自体が
// プライベート IP でも SSRF block されないこと (#485)。
func TestNewSSRFSafeTransport_ForwardsThroughProxy(t *testing.T) {
	hits := 0
	// proxy 役の httptest server。HTTP の forward proxy は absolute-form
	// URL (例: GET http://example.com/foo HTTP/1.1) を受け取るので
	// req.URL を見て応答するだけで proxy として振る舞える。
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		// proxy は absolute-form URL を受け取る
		assert.Equal(t, "example.com", r.URL.Host)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("via-proxy"))
	}))
	defer proxy.Close()

	// httptest は 127.0.0.1 で起動するので allowedCIDRs 無しでも proxy
	// 接続自体は SSRF skip される (proxyAddr 一致)。
	tr := NewSSRFSafeTransport(nil, WithProxy(proxy.URL, nil))
	client := &http.Client{Transport: tr}

	resp, err := client.Get("http://example.com/path")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, 1, hits)
}

// bypass list に列挙された host は proxy を経由せず direct dial になる。
// direct dial は SSRF check 対象なので、loopback への bypass は CIDR 許可
// が無い限りブロックされる。
func TestNewSSRFSafeTransport_BypassRoutesDirectAndKeepsSSRF(t *testing.T) {
	proxyHits := 0
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		proxyHits++
		w.WriteHeader(http.StatusOK)
	}))
	defer proxy.Close()

	directHits := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		directHits++
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	targetURL, err := url.Parse(target.URL)
	require.NoError(t, err)
	// bypass で direct dial になるが、target も loopback なので SSRF を
	// 許可するため CIDR 127.0.0.0/8 を allowedCIDRs に入れる。
	tr := NewSSRFSafeTransport(
		[]string{"127.0.0.0/8"},
		WithProxy(proxy.URL, []string{targetURL.Hostname()}),
	)
	client := &http.Client{Transport: tr}

	resp, err := client.Get(target.URL + "/x")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, 0, proxyHits, "bypass-listed host must not go through proxy")
	assert.Equal(t, 1, directHits)
}

// proxy URL が不正な場合は proxy 設定が無視され direct dial にフォール
// バックする (起動時に panic させない)。
func TestNewSSRFSafeTransport_InvalidProxyURLFallsBack(t *testing.T) {
	tr := NewSSRFSafeTransport(nil, WithProxy("://not-a-url", nil))
	assert.NotNil(t, tr)
	assert.Nil(t, tr.Proxy, "invalid proxy URL must leave Transport.Proxy unset")
}

// WithProxy("", ...) は no-op (proxy 不使用)。
func TestNewSSRFSafeTransport_EmptyProxyURLNoOp(t *testing.T) {
	tr := NewSSRFSafeTransport(nil, WithProxy("", []string{"example.com"}))
	assert.Nil(t, tr.Proxy, "empty proxy URL must not enable Transport.Proxy")
}

// IPv6 host の proxy URL に明示 port が無いとき、u.Host は "[::1]"
// のように bracket 付きで返る。これを net.JoinHostPort にそのまま渡すと
// "[[::1]]:80" の二重 bracket になり、後段の DialContext 比較で proxy
// 一致と判定されず SSRF check が走る → loopback (::1) として block
// されてしまう。u.Hostname() で剥がしてから net.JoinHostPort に渡す
// ことで http.Transport が dial する正規形 ("[::1]:80") と一致する。
//
// 直接 proxyAddr を assert できないので、実 dial を試して
// ErrSSRFBlocked が返らない (= proxy 一致判定が機能している) ことで
// 間接的に検証する。dial 自体はループバック上の閉じた port なので
// "connection refused" 系のエラーになるが、それは bug fix の対象外。
// #496: WithOutgoingAddress / WithAddressFamily の挙動を確認する。
// LocalAddr は実 bind しないと kernel が valid 検証しないので、Transport
// 構築 + Dialer 経由で「不正値は無視」「正常値が反映される」だけ確認。

// 不正な IP 文字列を渡しても panic せず Transport は使えるまま。
// (内部 Dialer.LocalAddr は nil のまま = kernel auto-pick)。
func TestNewSSRFSafeTransport_OutgoingAddressInvalidIgnored(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	tr := NewSSRFSafeTransport([]string{"127.0.0.0/8"}, WithOutgoingAddress("not-an-ip"))
	client := &http.Client{Transport: tr}
	resp, err := client.Get(ts.URL)
	require.NoError(t, err)
	resp.Body.Close()
}

// 正常な loopback IP を LocalAddr に指定してもループバック宛なら通る
// (実 bind を kernel が許可する経路)。
func TestNewSSRFSafeTransport_OutgoingAddressLoopbackBinds(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	tr := NewSSRFSafeTransport([]string{"127.0.0.0/8"}, WithOutgoingAddress("127.0.0.1"))
	client := &http.Client{Transport: tr}
	resp, err := client.Get(ts.URL)
	require.NoError(t, err)
	resp.Body.Close()
}

// addressFamily=ipv4 で IPv6 のみ解決される host への接続を弾く。
// httptest の listener は IPv4 にバインドされるが、LookupIPAddr が ::1 も
// 返してくるかは環境次第。確実な検証には normalize/match を直叩きする。
func TestNormalizeFamily(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"ipv4", "ipv4"},
		{"IPv4", "ipv4"},
		{"ipv6", "ipv6"},
		{" IPv6 ", "ipv6"},
		{"dual", ""},
		{"", ""},
		{"garbage", ""},
	}
	for _, tc := range cases {
		if got := normalizeFamily(tc.in); got != tc.want {
			t.Errorf("normalizeFamily(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestMatchFamily(t *testing.T) {
	if !matchFamily(net.ParseIP("1.2.3.4"), "ipv4") {
		t.Error("1.2.3.4 should match ipv4")
	}
	if matchFamily(net.ParseIP("1.2.3.4"), "ipv6") {
		t.Error("1.2.3.4 should NOT match ipv6")
	}
	if !matchFamily(net.ParseIP("2001:db8::1"), "ipv6") {
		t.Error("2001:db8::1 should match ipv6")
	}
	// IPv4-mapped IPv6 は ipv4 として扱う。
	if !matchFamily(net.ParseIP("::ffff:1.2.3.4"), "ipv4") {
		t.Error("::ffff:1.2.3.4 should match ipv4 (4-in-6)")
	}
}

// addressFamily=ipv4 でも 127.0.0.1 自体は IPv4 なので解決され、その後の
// SSRF check で private IP として block される。family filter 経路を抜けて
// 後段の SSRF 判定に到達することの確認。
func TestNewSSRFSafeTransport_AddressFamilyIPv4PassesThrough(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	tr := NewSSRFSafeTransport(nil, WithAddressFamily("ipv4"))
	client := &http.Client{Transport: tr, Timeout: 2 * time.Second}
	_, err := client.Get(ts.URL)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrSSRFBlocked, "ipv4 family must reach SSRF check, not bypass it")
}

// addressFamily=ipv6 で IPv4-only host (httptest) を解決すると
// no ipv6 address エラーで dial 失敗。
func TestNewSSRFSafeTransport_AddressFamilyIPv6FiltersOutIPv4(t *testing.T) {
	tr := NewSSRFSafeTransport([]string{"127.0.0.0/8"}, WithAddressFamily("ipv6"))
	client := &http.Client{Transport: tr, Timeout: 2 * time.Second}
	// IPv4-only な test 用ドメイン: localhost は実際 ::1 も返すので使えない。
	// 127.0.0.1 を直接渡す (IPv4 数値リテラル → DNS 不要だが Go の resolver
	// が wrap で 127.0.0.1 を返す経路 = familyFilter が IPv4 を弾く)。
	_, err := client.Get("http://127.0.0.1:1/")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no ipv6 address",
		"ipv6 family must drop the only-IPv4 resolution result")
}

func TestNewSSRFSafeTransport_IPv6ProxyDefaultPort(t *testing.T) {
	// #486 regression は「`http://[::1]` (= port 80 default) を proxy URL に
	// 指定したとき SSRF guard が `[[::1]]:80` と二重 bracket せず正しく
	// `[::1]:80` で比較できる」ことを「dial が SSRF block 以外のエラーで
	// 失敗する」観測経由で覆う。原作者コメント (fef6210e) のとおり「dial 自体は
	// ループバック上の閉じた port なので connection refused 系のエラーになる」
	// 前提に依存しているため、開発機で [::]:80 に nginx 等が listen していると
	// dial が成功して err=nil になり require.Error が trip する。
	// CI runner は port 80 に何も listen していないので無条件に通る。
	// 開発機向けの fail-safe として probe を入れ、listener 検出時は skip する。
	if conn, perr := net.DialTimeout("tcp6", "[::1]:80", 200*time.Millisecond); perr == nil {
		_ = conn.Close()
		t.Skip("something is listening on [::1]:80 (e.g. local nginx); cannot observe #486 regression via dial-fail branch")
	}

	tr := NewSSRFSafeTransport(nil, WithProxy("http://[::1]", nil))
	require.NotNil(t, tr)
	require.NotNil(t, tr.Proxy)

	client := &http.Client{Transport: tr, Timeout: 2 * time.Second}
	_, err := client.Get("http://example.com/")
	require.Error(t, err, "dial to ::1:80 will fail (no listener) but must not be SSRF-blocked")
	assert.NotErrorIs(t, err, ErrSSRFBlocked,
		"proxy connection must skip SSRF check even when proxy host is IPv6 loopback without explicit port")
}
