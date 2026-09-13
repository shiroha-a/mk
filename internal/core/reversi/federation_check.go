package reversi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// ReversiVersion is the local reversi federation version advertised in nodeinfo
// and compared against remote hosts' nodeinfo for compatibility checks.
// CherryPick 本家の `NodeinfoServerService.reversiVersion` と互換なメジャー
// バージョンで揃える (現状 1.1.x)。
const ReversiVersion = "1.1.0-mkgo"

// remoteVersionTTL はリモートホストの reversiVersion を Redis にキャッシュ
// する時間。CherryPick は 5 分で揃えている。nodeinfo は頻繁に変わらないので
// 短すぎず長すぎずの値。
const remoteVersionTTL = 5 * time.Minute

// FederationChecker determines whether a remote host speaks the reversi
// federation protocol by fetching its nodeinfo and reading `metadata.
// reversiVersion`. Redis にキャッシュして連続呼び出しでも HTTP 往復が
// 最大 1 回に収まるようにする (#417 P3)。
type FederationChecker struct {
	redis  redis.Cmdable
	client HTTPDoer
}

// HTTPDoer is the minimal interface the checker uses to issue HTTP GET.
// 既存の safehttp.Client などをそのまま渡せるように narrow interface。
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// NewFederationChecker constructs a FederationChecker.
//
// **client が nil なら nodeinfo を取りに行かない (fail-closed)。** 以前は素の
// `&http.Client{}` へ落としていたが、それは **SSRF ガードを通らない client** で、
// 配線を落とした瞬間に private IP への到達が開く。他の外向き経路
// (`probeImageDimensions`) は未配線なら取得自体を止める形になっており、
// ここだけ fail-open だった。連合対戦が使えなくなるだけなので、閉じる側に倒す。
func NewFederationChecker(r redis.Cmdable, client HTTPDoer) *FederationChecker {
	return &FederationChecker{redis: r, client: client}
}

func federationVersionKey(host string) string {
	return "reversi:federation:version:" + host
}

// Available reports whether the remote host exposes a reversiVersion with a
// matching major. Return values:
//   - true  : federation OK
//   - false : host reachable but reversiVersion absent / incompatible
//   - ("", err) — 別の signature で扱う必要があればそちらを使う
//
// キャッシュ hit なら HTTP を踏まない。cache miss で nodeinfo fetch に失敗
// した場合は false (= unavailable) を返し、短い empty-string を cache して
// しばらく問い合わせを抑える。
func (c *FederationChecker) Available(ctx context.Context, host string) bool {
	if c == nil || host == "" {
		return false
	}
	version := c.remoteVersion(ctx, host)
	return majorCompatible(version)
}

// remoteVersion returns the cached version string or empty when not available.
// cache に値があればそれを使い、無ければ実 fetch + cache write。
func (c *FederationChecker) remoteVersion(ctx context.Context, host string) string {
	if c.redis != nil {
		if v, err := c.redis.Get(ctx, federationVersionKey(host)).Result(); err == nil {
			return v
		}
	}
	version := c.fetchRemoteVersion(ctx, host)
	if c.redis != nil {
		// 空文字も cache する (short-circuit for repeated checks)
		c.redis.Set(ctx, federationVersionKey(host), version, remoteVersionTTL)
	}
	return version
}

// fetchRemoteVersion does the actual nodeinfo fetch. 2 段階 (well-known
// discovery → 実 nodeinfo URL) を踏む。失敗時は空文字を返す。
func (c *FederationChecker) fetchRemoteVersion(ctx context.Context, host string) string {
	discoveryURL := "https://" + host + "/.well-known/nodeinfo"
	nodeinfoURL := c.resolveNodeinfoURL(ctx, host, discoveryURL)
	if nodeinfoURL == "" {
		return ""
	}
	body, err := c.httpGetJSON(ctx, nodeinfoURL)
	if err != nil {
		return ""
	}
	var info struct {
		Metadata struct {
			ReversiVersion string `json:"reversiVersion"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(body, &info); err != nil {
		return ""
	}
	return info.Metadata.ReversiVersion
}

// resolveNodeinfoURL walks the standard /.well-known/nodeinfo discovery
// document and returns the actual nodeinfo JSON URL (preferring 2.1, then
// 2.0)。discovery shape 見つからなければ空文字。
func (c *FederationChecker) resolveNodeinfoURL(ctx context.Context, host, discoveryURL string) string {
	body, err := c.httpGetJSON(ctx, discoveryURL)
	if err != nil {
		return ""
	}
	var doc struct {
		Links []struct {
			Rel  string `json:"rel"`
			Href string `json:"href"`
		} `json:"links"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return ""
	}
	for _, want := range []string{
		"http://nodeinfo.diaspora.software/ns/schema/2.1",
		"http://nodeinfo.diaspora.software/ns/schema/2.0",
	} {
		for _, l := range doc.Links {
			if l.Rel != want || l.Href == "" {
				continue
			}
			// **href をそのまま叩かない。** discovery はリモートが返す JSON
			// なので任意の URL を指せる。到達先は SSRF-safe transport で
			// private IP へは行かないが、任意の public host / 任意ポートへの
			// GET リレーは成立し、返ってきた JSON の `reversiVersion` が
			// そのまま Redis に 5 分間入る。
			if !nodeinfoHrefBelongsTo(l.Href, host) {
				continue
			}
			return l.Href
		}
	}
	return ""
}

// nodeinfoHrefBelongsTo reports whether href is an https URL on host.
//
// 既定ポートの明記 (`:443`) だけは同じ host として扱う — Go の `net/url` は
// ポートを剥がさないので、そうしないと正当な相手を落とす。
func nodeinfoHrefBelongsTo(href, host string) bool {
	u, err := url.Parse(href)
	if err != nil || u.Scheme != "https" {
		return false
	}
	trim := func(h string) string {
		return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(h)), ":443")
	}
	return trim(u.Host) == trim(host)
}

// httpGetJSON is a small GET helper that reads up to 1 MiB of body.
func (c *FederationChecker) httpGetJSON(ctx context.Context, url string) ([]byte, error) {
	// **未配線なら取りに行かない (fail-closed)。** 素の `http.Client` へ落とすと
	// SSRF ガードを通らない client で外へ出てしまう。
	if c.client == nil {
		return nil, errNoHTTPClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, errStatus{code: resp.StatusCode}
	}
	// 1 MiB 上限で偽装 nodeinfo による OOM を避ける。
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
}

// errNoHTTPClient is returned when the checker was constructed without an
// outbound HTTP client (see NewFederationChecker).
var errNoHTTPClient = errors.New("reversi: federation checker has no http client")

type errStatus struct{ code int }

func (e errStatus) Error() string { return http.StatusText(e.code) }

// majorCompatible reports whether the remote reversiVersion's major matches
// our local one. 空文字 (未知) は不可、prefix-pre-dot を比較する。
//
// 例: "1.1.0-mkgo" の major = "1"、"1.2.0-yojo" の major = "1"、
//
//	"2.0.0-foo" の major = "2"。
func majorCompatible(remote string) bool {
	if remote == "" {
		return false
	}
	return firstDotSegment(remote) == firstDotSegment(ReversiVersion)
}

func firstDotSegment(s string) string {
	// 先に "-" で切って (e.g. "1.1.0-mkgo" → "1.1.0") から最初の "." で切る。
	head := s
	if i := strings.IndexByte(head, '-'); i >= 0 {
		head = head[:i]
	}
	if i := strings.IndexByte(head, '.'); i >= 0 {
		head = head[:i]
	}
	return head
}
