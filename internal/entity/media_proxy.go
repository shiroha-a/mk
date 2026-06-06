package entity

import "strings"

// Media-proxy URL rewriting (issue #1529).
//
// Remote (federated) media URLs must never be handed to clients verbatim:
// the browser would fetch them directly and leak the user's IP to the remote
// server. Instead they are rewritten to point at this instance's media proxy
// (or a configured external proxy), which fetches the upstream object
// server-side. This mirrors upstream Misskey's
// DriveFileEntityService.getProxiedUrl / getPublicUrl / getThumbnailUrl.

// mediaProxyBuilder builds a media-proxy URL for an external media URL. rawURL
// is the external target; mode is "" (image), "static" (thumbnail) or "avatar".
// The returned URL is HMAC-signed so the proxy authorizes it. nil when the
// proxy is not wired (e.g. unit tests) — callers then fall back to the raw URL.
var mediaProxyBuilder func(rawURL, mode string) string

// proxyRemoteFilesFn returns the live `proxyRemoteFiles` meta flag.
var proxyRemoteFilesFn func() bool

// externalMediaProxyEnabled is true when a dedicated external media proxy is
// configured (distinct from the internal ${url}/proxy).
var externalMediaProxyEnabled bool

// SetMediaProxy wires the media-proxy URL builder used to rewrite remote media
// URLs so clients never request external servers directly (issue #1529).
// Passing a nil builder disables proxying (raw URLs are returned), which is the
// default in unit tests.
func SetMediaProxy(builder func(rawURL, mode string) string, proxyRemoteFiles func() bool, externalEnabled bool) {
	mediaProxyBuilder = builder
	proxyRemoteFilesFn = proxyRemoteFiles
	externalMediaProxyEnabled = externalEnabled
}

// shouldProxyRemoteFile reports whether remote DriveFile / banner URLs should be
// routed through the media proxy. Mirrors upstream getPublicUrl/getThumbnailUrl
// gating: an external proxy is configured, or proxyRemoteFiles is enabled.
func shouldProxyRemoteFile() bool {
	if mediaProxyBuilder == nil {
		return false
	}
	if externalMediaProxyEnabled {
		return true
	}
	return proxyRemoteFilesFn != nil && proxyRemoteFilesFn()
}

// isExternalURL reports whether u is an absolute http(s) URL (a proxy
// candidate). Local / relative URLs (e.g. /files/..., /identicon/...) are served
// by this instance and never leak the client IP, so they are left untouched.
func isExternalURL(u string) bool {
	return strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://")
}

// proxyMediaURL returns the media-proxy URL for an external media URL, or the
// raw URL unchanged when the proxy is unavailable or the URL is not external.
func proxyMediaURL(rawURL, mode string) string {
	if mediaProxyBuilder == nil || !isExternalURL(rawURL) {
		return rawURL
	}
	return mediaProxyBuilder(rawURL, mode)
}

// ProxyRemoteMediaURL rewrites an external remote media URL through the media
// proxy when proxying is enabled (issue #1529). It is the exported entry point
// for packers outside this package (remote emoji URLs, federation instance
// icons). Local / relative URLs and the proxying-disabled case return rawURL
// unchanged. Callers gate on "is this a remote object" before calling.
func ProxyRemoteMediaURL(rawURL, mode string) string {
	if !shouldProxyRemoteFile() {
		return rawURL
	}
	return proxyMediaURL(rawURL, mode)
}

// ProxyRemoteMediaURLPtr is the *string variant of ProxyRemoteMediaURL. A nil or
// empty pointer is returned unchanged.
func ProxyRemoteMediaURLPtr(p *string, mode string) *string {
	if p == nil || *p == "" || !shouldProxyRemoteFile() {
		return p
	}
	v := proxyMediaURL(*p, mode)
	return &v
}
