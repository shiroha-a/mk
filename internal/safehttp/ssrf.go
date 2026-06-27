// Package safehttp provides shared HTTP helpers used across outbound
// fetchers (ActivityPub, WebFinger, URL preview, media proxy). Centralises
// SSRF protection and response size caps so hardening improvements propagate
// everywhere in one place.
package safehttp

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// privateRanges lists IPv4/IPv6 CIDR blocks considered private or reserved.
var privateRanges []*net.IPNet

func init() {
	// privateRanges は upstream Misskey TS (HttpRequestService.isPrivateIp) が
	// ipaddr.js の `range() !== 'unicast'` で遮断する「非 unicast」集合に揃える。
	// 単なる RFC1918/loopback だけでなく、embedded/relay で内部 IPv4 へ到達しうる
	// NAT64 / 6to4 / rfc6145 や、multicast / documentation / tunneling 等の特殊用途
	// レンジも遮断する。operator が正当に使うレンジは config.allowedPrivateNetworks
	// で opt-out できる (isPrivateIP は allowedNets を privateRanges より先に評価)。
	cidrs := []string{
		// === IPv4 private / reserved (RFC5735 / RFC6890 系) ===
		"0.0.0.0/8",       // unspecified / this-host
		"10.0.0.0/8",      // private (RFC1918)
		"100.64.0.0/10",   // carrier-grade NAT (RFC6598)
		"127.0.0.0/8",     // loopback
		"169.254.0.0/16",  // link-local
		"172.16.0.0/12",   // private (RFC1918)
		"192.0.0.0/24",    // IETF protocol assignments
		"192.0.2.0/24",    // documentation TEST-NET-1
		"192.31.196.0/24", // AS112-v4 (RFC7535)
		"192.52.193.0/24", // AMT anycast (RFC7450)
		"192.88.99.0/24",  // 6to4 relay anycast, deprecated (RFC7526)
		"192.168.0.0/16",  // private (RFC1918)
		"192.175.48.0/24", // AS112 direct delegation (RFC7534)
		"198.18.0.0/15",   // benchmarking (RFC2544)
		"198.51.100.0/24", // documentation TEST-NET-2
		"203.0.113.0/24",  // documentation TEST-NET-3
		"224.0.0.0/4",     // multicast
		"240.0.0.0/4",     // reserved (255.255.255.255 broadcast を内包)
		// === IPv6 private / reserved (ipaddr.js の non-unicast 集合に対応) ===
		// IPv4 の 0.0.0.0/8 と対称に IPv6 unspecified (::) も遮断する。これが無いと
		// `http://[::]:PORT/` が isPrivateIP をすり抜け、Linux の connect(::) が
		// loopback (::1) にルートされて SSRF 保護を回避できる。
		"::/128",    // unspecified
		"::1/128",   // loopback
		"fc00::/7",  // unique-local (RFC4193)
		"fe80::/10", // link-local
		"fec0::/10", // deprecated site-local (RFC3879)
		"ff00::/8",  // multicast (IPv4 224.0.0.0/4 と対称)
		"100::/64",  // discard-only (RFC6666)
		// embedded/relay で内部 IPv4 へ到達しうる SSRF 直結レンジ。To4() は
		// ::ffff:x.x.x.x (ipv4-mapped) しか展開しないため、下記は CIDR で明示遮断する。
		"0:0:0:0:ffff:0:0:0/96", // IPv4-translatable (RFC6145), embeds v4
		"64:ff9b::/96",          // NAT64 well-known prefix (RFC6052), embeds v4
		"64:ff9b:1::/48",        // NAT64 local-use (RFC8215), embeds v4
		"2002::/16",             // 6to4 (RFC3056), embeds v4
		// 2001::/23 は teredo (2001::/32, v4 を埋め込む) を含み、benchmarking /
		// amt / orchid / drone / as112v6(2001:4:112) も 1 本に集約する。本番 unicast
		// (例 Google 2001:4860) は第2 hextet > 0x01ff なので含まれない。
		"2001::/23",
		"2001:db8::/32",     // documentation (RFC3849)
		"2620:4f:8000::/48", // AS112 (RFC7534)
		"3fff::/20",         // documentation (RFC9637)
		"5f00::/16",         // SRv6 SID (RFC9602)
	}
	for _, cidr := range cidrs {
		_, ipNet, err := net.ParseCIDR(cidr)
		if err != nil {
			panic("invalid CIDR in privateRanges: " + cidr)
		}
		privateRanges = append(privateRanges, ipNet)
	}
}

// ErrSSRFBlocked is returned when a connection to a private/reserved IP is blocked.
var ErrSSRFBlocked = fmt.Errorf("safehttp: connection to private IP blocked")

// transportOptions accumulates state from functional Option values.
type transportOptions struct {
	proxyURL      string
	bypassHosts   []string
	localAddr     string
	addressFamily string
}

// Option configures NewSSRFSafeTransport. Use WithProxy to enable forward
// proxy support; pass no options for the default SSRF-safe direct transport.
type Option func(*transportOptions)

// WithProxy wires a forward HTTP proxy and an optional bypass list. proxyURL
// must be a full URL parseable by url.Parse (e.g. http://127.0.0.1:3128). When
// proxyURL is empty the option is a no-op so callers can pass config values
// straight through. bypassHosts is matched as exact case-sensitive hostname
// equality, mirroring upstream Misskey's `proxyBypassHosts.includes(...)`.
func WithProxy(proxyURL string, bypassHosts []string) Option {
	return func(o *transportOptions) {
		o.proxyURL = proxyURL
		o.bypassHosts = bypassHosts
	}
}

// WithOutgoingAddress binds outbound TCP connections to the given local IP
// (cfg.OutgoingAddress) — useful on multi-NIC hosts where federation should
// originate from a specific source. Empty string is a no-op (kernel picks).
// Invalid IP は warn ログを出してそのまま no-op (起動時 fail-fast より
// best-effort 起動を優先する)。
func WithOutgoingAddress(addr string) Option {
	return func(o *transportOptions) { o.localAddr = addr }
}

// WithAddressFamily restricts DNS resolution / dial path to one address
// family. Accepts "ipv4" / "ipv6" / "dual" (or empty = dual). 不正値は
// dual にフォールバック。upstream Misskey の `outgoingAddressFamily` と
// 同じ semantics で、IPv6 障害のリモートを IPv4 強制で迂回する用途。
func WithAddressFamily(family string) Option {
	return func(o *transportOptions) { o.addressFamily = family }
}

// NewSSRFSafeTransport returns an *http.Transport with a custom DialContext
// that resolves DNS first and rejects connections to private/reserved IPs.
// allowedCIDRs は config.AllowedPrivateNetworks に対応し、明示的に許可する CIDR リスト。
// Functional opts (現状 WithProxy のみ) で外向き forward proxy 経由を有効化できる。
func NewSSRFSafeTransport(allowedCIDRs []string, opts ...Option) *http.Transport {
	o := transportOptions{}
	for _, fn := range opts {
		fn(&o)
	}

	var allowedNets []*net.IPNet
	for _, cidr := range allowedCIDRs {
		_, ipNet, err := net.ParseCIDR(cidr)
		if err != nil {
			continue
		}
		allowedNets = append(allowedNets, ipNet)
	}

	// proxy 設定があれば事前に URL を一度だけ parse する。失敗したものは
	// 無視 (proxy 不使用) して direct fallback。起動時に config 経由で
	// 渡るので毎リクエスト parse する必要は無い。
	var proxyURL *url.URL
	var proxyAddr string // host:port 形式 (DialContext での比較用)
	if o.proxyURL != "" {
		if u, err := url.Parse(o.proxyURL); err == nil && u.Host != "" {
			proxyURL = u
			proxyAddr = u.Host
			// URL に明示ポートが無い場合は scheme 既定を補う。後段の
			// addr 比較で `host:80` のような正規化形と一致させるため。
			// IPv6 host は u.Host が "[::1]" のように bracket 付きで
			// 返るため net.JoinHostPort には bracket を剥がした
			// u.Hostname() を渡す (二重 bracket を避ける)。
			if _, _, splitErr := net.SplitHostPort(proxyAddr); splitErr != nil {
				if u.Scheme == "https" {
					proxyAddr = net.JoinHostPort(u.Hostname(), "443")
				} else {
					proxyAddr = net.JoinHostPort(u.Hostname(), "80")
				}
			}
		}
	}
	bypass := make(map[string]struct{}, len(o.bypassHosts))
	for _, h := range o.bypassHosts {
		if h != "" {
			bypass[h] = struct{}{}
		}
	}

	dialer := &net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
	}
	// outgoingAddress: 指定があれば本ホストの該当 IP を source として bind
	// する。parse 失敗は warn のみ (操作ミスで全 outbound を止めない方針)。
	if o.localAddr != "" {
		if ip := net.ParseIP(o.localAddr); ip != nil {
			dialer.LocalAddr = &net.TCPAddr{IP: ip}
		} else {
			slog.Warn("safehttp: invalid outgoingAddress, falling back to kernel auto-pick",
				"addr", o.localAddr)
		}
	}

	// outgoingAddressFamily: ipv4/ipv6/dual (空文字 = dual と同義)。
	// resolved IPs を family で絞る dial 側 helper を closure に閉じ込める。
	familyFilter := normalizeFamily(o.addressFamily)

	tr := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			// proxy 経由のリクエストでは Transport.Proxy callback の結果に
			// 沿って http.Transport が proxy host:port で DialContext を
			// 呼ぶ。proxy はオペレーターが明示設定したエンドポイントなので
			// SSRF check (private IP 拒否) は適用しない。bypass 経路や
			// proxy 未指定の direct dial には従来通り SSRF を適用する。
			if proxyAddr != "" && addr == proxyAddr {
				return dialer.DialContext(ctx, network, addr)
			}

			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, fmt.Errorf("safehttp: invalid address %q: %w", addr, err)
			}

			// DNS解決して実IPを取得。Goのresolverは「nil err + 空slice」を
			// 返さない契約のため、以降のloopは必ず1回以上実行される。
			ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, fmt.Errorf("safehttp: DNS lookup failed for %q: %w", host, err)
			}

			// outgoingAddressFamily が ipv4/ipv6 指定なら該当 family で
			// 絞り込み (#496)。dual / 空文字なら全 IP を維持。filter 結果が
			// 0 件になった場合は明示的にエラーを返して曖昧な fallback を
			// 避ける (operator が ipv4 強制したのに AAAA しか無い host 等)。
			if familyFilter != "" {
				// in-place slice filter: ips[:0] が underlying array を共有する
				// が、この closure 内では ips は LookupIPAddr の戻り (毎回新規)
				// で書き込み index は読み込み index 以下、かつ goroutine
				// 共有もしないので safe。
				filtered := ips[:0]
				for _, ipAddr := range ips {
					if matchFamily(ipAddr.IP, familyFilter) {
						filtered = append(filtered, ipAddr)
					}
				}
				if len(filtered) == 0 {
					return nil, fmt.Errorf("safehttp: no %s address resolved for %q", familyFilter, host)
				}
				ips = filtered
			}

			// 全解決IPがプライベートでないか検証
			for _, ipAddr := range ips {
				if isPrivateIP(ipAddr.IP, allowedNets) {
					return nil, ErrSSRFBlocked
				}
			}

			// 検証済みIPで接続（最初に成功したものを使う）
			for _, ipAddr := range ips {
				target := net.JoinHostPort(ipAddr.IP.String(), port)
				conn, dialErr := dialer.DialContext(ctx, network, target)
				if dialErr == nil {
					return conn, nil
				}
				err = dialErr
			}
			return nil, err
		},
		MaxIdleConns:        100,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
		// http.DefaultTransport と同様に HTTP/2 negotiation を有効にする。
		// 既定の *http.Transport{} ではデフォルト false のため、AP fetch が
		// HTTP/1.1 のみに退化するのを避ける (#323 Devin review)。
		ForceAttemptHTTP2: true,
	}
	if proxyURL != nil {
		// upstream Misskey の HttpRequestService.getAgentByUrl と同じく
		// proxyBypassHosts に含まれる hostname (exact match) は proxy を
		// 経由せず direct で出す。それ以外は proxyURL に CONNECT/forward。
		tr.Proxy = func(req *http.Request) (*url.URL, error) {
			if _, ok := bypass[req.URL.Hostname()]; ok {
				return nil, nil
			}
			return proxyURL, nil
		}
	}
	return tr
}

// normalizeFamily lowercases and validates the family string. Returns
// "ipv4" / "ipv6" for valid filters, or "" for dual / empty / unrecognized
// values (treat as no-filter, matching upstream Misskey behaviour).
func normalizeFamily(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "ipv4":
		return "ipv4"
	case "ipv6":
		return "ipv6"
	}
	return ""
}

// matchFamily reports whether ip belongs to the requested address family.
// IPv4-mapped IPv6 (::ffff:x.x.x.x) は IPv4 として扱う (Go の resolver は
// 4-in-6 形式で返すことがあり、operator の意図に合致するように)。
func matchFamily(ip net.IP, family string) bool {
	if v4 := ip.To4(); v4 != nil {
		return family == "ipv4"
	}
	return family == "ipv6"
}

// isPrivateIP returns true if ip falls within a private/reserved range
// and is NOT covered by any of the allowedNets exceptions.
func isPrivateIP(ip net.IP, allowedNets []*net.IPNet) bool {
	// IPv4-mapped IPv6 (::ffff:x.x.x.x) の中身を取り出してIPv4として判定する。
	// upstream Misskey (ipaddr.js range='ipv4Mapped') は public 埋め込みも含め
	// ::ffff:0:0/96 を一律遮断するが、mk-go は埋め込み v4 を IPv4 レンジで評価する
	// ため private 埋め込み (::ffff:127.0.0.1 等) は遮断しつつ public 埋め込み
	// (::ffff:8.8.8.8) は許可する。dial 先 IP は public で内部到達しないため SSRF 的
	// には完全かつ upstream の over-block より精密、という意図的な divergence。
	// なお RFC6145 (0:0:0:0:ffff:0:0:0/96) や NAT64 (64:ff9b::/96) は To4() では
	// 展開されないため privateRanges 側で明示遮断している。
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}

	// allowedNets に含まれるIPは許可
	for _, allowed := range allowedNets {
		if allowed.Contains(ip) {
			return false
		}
	}

	// プライベート/予約済みレンジに含まれるか判定
	for _, r := range privateRanges {
		if r.Contains(ip) {
			return true
		}
	}
	return false
}
