package instance

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/shiroha-a/mk/internal/repository"
	"golang.org/x/net/html"
)

// HTTPFetcher abstracts the HTTP clients used to fetch nodeinfo documents and
// remote HTML. 実装は activitypub.Client の薄いラッパで十分。
//
// nodeinfo / .well-known は peer 認証を要求しない discovery endpoint なので
// 署名 GET を付ける必要がない。FetchJSON 経由で plain JSON Accept を投げる
// (#474: Iceshrimp.NET など strict な実装は AP MIME を Accept に渡すと 406)。
type HTTPFetcher interface {
	// FetchJSON は Accept: application/json, */* で nodeinfo を取得する。
	FetchJSON(uri string) ([]byte, error)
	FetchHTML(uri string) ([]byte, error)
}

// FetchMetadataService fetches /.well-known/nodeinfo for a remote host and
// updates the corresponding instance row with the parsed metadata.
type FetchMetadataService struct {
	repo    repository.InstanceRepository
	fetcher HTTPFetcher
	clock   func() time.Time
}

// NewFetchMetadataService constructs a FetchMetadataService.
func NewFetchMetadataService(repo repository.InstanceRepository, fetcher HTTPFetcher) *FetchMetadataService {
	return &FetchMetadataService{repo: repo, fetcher: fetcher, clock: time.Now}
}

// SetClock overrides the time source. Intended for tests.
func (s *FetchMetadataService) SetClock(now func() time.Time) {
	if now != nil {
		s.clock = now
	}
}

// nodeinfoDiscovery is the JSON shape of /.well-known/nodeinfo.
type nodeinfoDiscovery struct {
	Links []struct {
		Rel  string `json:"rel"`
		Href string `json:"href"`
	} `json:"links"`
}

// nodeinfoDocument is the JSON shape of nodeinfo 2.0/2.1.
type nodeinfoDocument struct {
	Software struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"software"`
	OpenRegistrations *bool `json:"openRegistrations"`
	Metadata          struct {
		NodeName        string `json:"nodeName"`
		NodeDescription string `json:"nodeDescription"`
		ThemeColor      string `json:"themeColor"`
	} `json:"metadata"`
}

// preferredRels lists the nodeinfo schema versions in order of preference.
// 2.1 → 2.0 → 1.0 の順で fallback する。
var preferredRels = []string{
	"http://nodeinfo.diaspora.software/ns/schema/2.1",
	"http://nodeinfo.diaspora.software/ns/schema/2.0",
}

// Fetch retrieves nodeinfo for the given host and applies the parsed metadata
// to the instance row. Instance row が存在しない場合は ErrInstanceNotFound。
func (s *FetchMetadataService) Fetch(host string) error {
	if host == "" {
		return errors.New("host is required")
	}
	if _, err := s.repo.FindByHost(host); err != nil {
		return ErrInstanceNotFound
	}

	disc, err := s.fetchDiscovery(host)
	if err != nil {
		return err
	}
	href := selectNodeinfoHref(disc)
	if href == "" {
		return errors.New("no supported nodeinfo schema")
	}

	doc, err := s.fetchDocument(href)
	if err != nil {
		return err
	}

	now := s.clock()
	fields := map[string]any{
		"infoUpdatedAt": &now,
	}
	// **この map に載せる値は全部、列に入ることを確かめてから入れる。** icon /
	// favicon だけ守っても、他の列が 1 つでも溢れれば **UPDATE 全体が落ちて
	// nodeinfo まで失う** — 下のガードの目的が同じ関数の中で破られる (#2723)。
	// 値は攻撃者 (リモートインスタンス) が自由に決められる。
	//
	// text 列は切って NUL を落とす。**URL 列は NUL を見ない** — `url.Parse` が制御
	// 文字を弾き、唯一通る fragment の NUL も `url.URL.String()` が `%00` に escape
	// するので、生の NUL はここまで来ない (fetchIcons。実測で確認)。
	if v := clampInstanceText(doc.Software.Name, maxInstanceSoftwareNameLen); v != "" {
		fields["softwareName"] = &v
	}
	if v := clampInstanceText(doc.Software.Version, maxInstanceSoftwareVersionLen); v != "" {
		fields["softwareVersion"] = &v
	}
	if doc.OpenRegistrations != nil {
		fields["openRegistrations"] = doc.OpenRegistrations
	}
	if v := clampInstanceText(doc.Metadata.NodeName, maxInstanceNameLen); v != "" {
		fields["name"] = &v
	}
	if v := clampInstanceText(doc.Metadata.NodeDescription, maxInstanceDescriptionLen); v != "" {
		fields["description"] = &v
	}
	if v := clampInstanceText(doc.Metadata.ThemeColor, maxInstanceThemeColorLen); v != "" {
		fields["themeColor"] = &v
	}

	// nodeinfoはicon/faviconを含まないため、リモートトップページHTMLから
	// 抽出する。取得失敗は致命ではない (nodeinfo 情報のpersistは継続する)。
	// DB側 varchar(256) 制約に引っかかるとUPDATE全体が失敗してnodeinfoまで
	// 失うため長さチェックを必ずかける (攻撃者制御の長いCDN URLを想定)。
	// **切らずに捨てる** — 切った URL は別物なので取りに行っても無駄。
	iconURL, faviconURL := s.fetchIcons(host)
	if fitsInstanceColumn(iconURL, maxInstanceURLLen) {
		fields["iconUrl"] = &iconURL
	}
	if fitsInstanceColumn(faviconURL, maxInstanceURLLen) {
		fields["faviconUrl"] = &faviconURL
	}

	return s.repo.UpdateFields(host, fields)
}

// instance の列の上限 (migration/000001_initial.up.sql)。溢れると UPDATE 全体が
// 落ちて nodeinfo まで失うので、書く前に必ず通す (#2723)。
const (
	maxInstanceSoftwareNameLen    = 64
	maxInstanceSoftwareVersionLen = 64
	maxInstanceNameLen            = 256
	maxInstanceDescriptionLen     = 4096
	maxInstanceThemeColorLen      = 64
)

// clampInstanceText prepares a remote-supplied text value for its column: NUL を
// 落とし、max rune で切る。
//
// **URL とは扱いが違う。** icon / favicon は「収まらなければ値ごと捨てる」
// (切った URL は別物で取りに行っても無駄) が、表示用のテキストは切っても意味が
// 残るので切る。
func clampInstanceText(raw string, max int) string {
	out := strings.Map(func(r rune) rune {
		if r == 0 {
			return -1
		}
		return r
	}, raw)
	if runes := []rune(out); len(runes) > max {
		out = string(runes[:max])
	}
	return out
}

// maxInstanceURLLen は instance.iconUrl / faviconUrl カラムの varchar(256)
// 制約と一致させる。これを超えるURLは無視する (DB エラーで他 field まで
// 失わないため)。
const maxInstanceURLLen = 256

// fitsInstanceColumn reports whether a non-empty URL can be stored in a
// varchar(max) column.
//
// **rune で数える。** PostgreSQL の varchar はコードポイント数で数えるので、
// byte 長で見ると非 ASCII を含む URL を必要以上に落とす。URL は切ると別物に
// なるので、収まらなければ値ごと捨てる (clampInstanceText と扱いが違う)。
func fitsInstanceColumn(v string, max int) bool {
	return v != "" && len([]rune(v)) <= max
}

// fetchIcons extracts iconUrl (high-res, used by detailed instance views)
// and faviconUrl (small, used by InstanceTicker) from the remote root HTML.
//
// Upstream Misskey distinguishes the two:
//   - faviconUrl: prefer `<link rel="icon">`; only falls back to the
//     conventional `/favicon.ico` when the HTML provides no link tag.
//   - iconUrl:    prefer `<link rel="apple-touch-icon">` (high-res), then
//     `<link rel="icon">`.
//
// Iceshrimp.NET serves `<link rel="icon" href="/favicon.png">` and does
// not expose `/favicon.ico`. Hardcoding the latter as faviconUrl gives a
// 404 in the InstanceTicker — frontend shows broken / empty image (#474).
// Following the link tag fixes the icon for any non-Misskey-TS upstream
// that uses a non-`.ico` favicon convention.
func (s *FetchMetadataService) fetchIcons(host string) (iconURL, faviconURL string) {
	rootURL := "https://" + host + "/"

	body, err := s.fetcher.FetchHTML(rootURL)
	if err != nil {
		// HTML 取得失敗 → 古い挙動 (`/favicon.ico` 決め打ち) でフォールバック。
		// frontend は 404 時に非表示にするだけで害は小さい。
		return "", "https://" + host + "/favicon.ico"
	}

	icon, appleTouchIcon := parseIconLinks(body, rootURL)

	// iconUrl (高解像度): apple-touch-icon があればそちらが綺麗なので優先。
	iconURL = firstNonEmptyStr(appleTouchIcon, icon)

	// faviconUrl: HTML の <link rel="icon"> を最優先 (Iceshrimp.NET 等の
	// 非 .ico 慣習に追従)、無ければ /favicon.ico に決め打ちフォールバック。
	// 256 文字超の icon URL は DB 制約で書けないので、その場合も
	// hardcode フォールバックに落とす。
	defaultFavicon := "https://" + host + "/favicon.ico"
	if fitsInstanceColumn(icon, maxInstanceURLLen) {
		faviconURL = icon
	} else {
		faviconURL = defaultFavicon
	}
	return iconURL, faviconURL
}

// parseIconLinks walks the HTML document and returns absolute URLs for
// the first `<link rel="icon">` and the first `<link rel="apple-touch-icon">`
// (or their precomposed/shortcut equivalents). Either string may be empty
// when the corresponding tag is absent. URL は pageURL を base にして解決。
func parseIconLinks(body []byte, pageURL string) (icon, appleTouchIcon string) {
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return "", ""
	}
	base, err := url.Parse(pageURL)
	if err != nil {
		return "", ""
	}
	var iconHref, appleHref string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "link" {
			rel := strings.ToLower(attrValue(n, "rel"))
			href := attrValue(n, "href")
			if href != "" {
				switch rel {
				case "icon", "shortcut icon":
					if iconHref == "" {
						iconHref = href
					}
				case "apple-touch-icon", "apple-touch-icon-precomposed":
					if appleHref == "" {
						appleHref = href
					}
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return resolveAgainstBase(iconHref, base), resolveAgainstBase(appleHref, base)
}

// resolveAgainstBase resolves href relative to base. Returns empty string
// when href is empty or unparseable so callers can fall through to other
// sources without having to nil-check error returns separately.
func resolveAgainstBase(href string, base *url.URL) string {
	if href == "" {
		return ""
	}
	ref, err := url.Parse(href)
	if err != nil {
		return ""
	}
	return base.ResolveReference(ref).String()
}

func attrValue(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// fetchDiscovery fetches /.well-known/nodeinfo and decodes the link list.
func (s *FetchMetadataService) fetchDiscovery(host string) (*nodeinfoDiscovery, error) {
	body, err := s.fetcher.FetchJSON("https://" + host + "/.well-known/nodeinfo")
	if err != nil {
		return nil, err
	}
	var disc nodeinfoDiscovery
	if err := json.Unmarshal(body, &disc); err != nil {
		return nil, err
	}
	return &disc, nil
}

// fetchDocument fetches the actual nodeinfo document.
func (s *FetchMetadataService) fetchDocument(href string) (*nodeinfoDocument, error) {
	body, err := s.fetcher.FetchJSON(href)
	if err != nil {
		return nil, err
	}
	var doc nodeinfoDocument
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, err
	}
	return &doc, nil
}

// selectNodeinfoHref picks the highest-priority schema URL from the discovery
// document. 未知の rel しか無い場合は空文字を返す。
func selectNodeinfoHref(disc *nodeinfoDiscovery) string {
	for _, want := range preferredRels {
		for _, link := range disc.Links {
			if link.Rel == want && link.Href != "" {
				return link.Href
			}
		}
	}
	return ""
}
