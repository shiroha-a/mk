package urlpreview

import (
	"io"
	"net/url"
	"strings"

	"golang.org/x/net/html"
)

// ParseHTML extracts OGP and Twitter card metadata from HTML content.
// 本家 Misskey の summaly と同じ優先順位: og:* > twitter:* > <title>/<link>。
//
// pageURL は base として使われ、href / og:image / icon / og:url 等の相対
// パスは絶対 URL に解決される (#639)。oEmbed 用 alternate link は meta
// map の `oembed:json` / `oembed:xml` キーに格納され、Fetcher 側で 2nd
// fetch のターゲットにする。
func ParseHTML(r io.Reader, pageURL string) *Result {
	doc, err := html.Parse(r)
	if err != nil {
		return &Result{URL: pageURL, Player: PlayerResult{Allow: []string{}}}
	}
	base, _ := url.Parse(pageURL) // base は err でも nil なら resolveURL no-op

	meta := extractMeta(doc)

	title := firstNonEmpty(meta["og:title"], meta["twitter:title"], meta["title"])
	desc := firstNonEmpty(meta["og:description"], meta["twitter:description"], meta["description"])
	thumb := resolveURL(base, firstNonEmpty(meta["og:image"], meta["twitter:image"], meta["twitter:image:src"]))
	icon := resolveURL(base, meta["icon"])
	sitename := firstNonEmpty(meta["og:site_name"])
	apID := resolveURL(base, firstNonEmpty(meta["ap:id"]))

	canonical := resolveURL(base, meta["og:url"])
	// #2106 L45: upstream は summary.url が http(s) でなければ throw する。canonical (og:url) が
	// javascript:/data: 等の危険な scheme の場合、API レスポンスの url field に素通ししないよう
	// 安全な pageURL にフォールバックする (defense-in-depth)。
	if canonical == "" || !isHTTPOrHTTPSURL(canonical) {
		canonical = pageURL
	}
	result := &Result{
		URL:    canonical,
		Player: PlayerResult{Allow: []string{}},
	}
	if title != "" {
		result.Title = &title
	}
	if desc != "" {
		result.Description = &desc
	}
	if thumb != "" {
		result.Thumbnail = &thumb
	}
	if icon != "" {
		result.Icon = &icon
	}
	if sitename != "" {
		result.Sitename = &sitename
	}
	if apID != "" {
		result.ActivityPub = &apID
	}
	// oEmbed discovery URL は meta から取り出して Result の補助 field に
	// 渡し、Fetcher が 2nd request でフェッチする。preview.go 側で公開し
	// ない (内部用)。
	result.oEmbedURL = resolveURL(base, meta["oembed:json"])
	return result
}

// resolveURL turns rel into an absolute URL relative to base.
//   - Empty rel → "" (caller handles no-icon / no-thumbnail branch)
//   - nil base → return rel as-is (caller is responsible for already-absolute)
//   - parse failure → "" so frontend doesn't try to render a malformed URL
//     (review #3)
func resolveURL(base *url.URL, rel string) string {
	if rel == "" {
		return ""
	}
	if base == nil {
		return rel
	}
	r, err := url.Parse(rel)
	if err != nil {
		return ""
	}
	return base.ResolveReference(r).String()
}

type metaMap map[string]string

func extractMeta(n *html.Node) metaMap {
	m := make(metaMap)
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "meta":
				key, val := metaKeyVal(n)
				if key != "" && val != "" {
					m[key] = val
				}
			case "title":
				if n.FirstChild != nil && n.FirstChild.Type == html.TextNode {
					m["title"] = strings.TrimSpace(n.FirstChild.Data)
				}
			case "link":
				rel, href := attrVal(n, "rel"), attrVal(n, "href")
				if (rel == "icon" || rel == "shortcut icon") && href != "" {
					m["icon"] = href
				}
				// alternate + application/activity+json → AP ID
				if rel == "alternate" && strings.Contains(attrVal(n, "type"), "activity+json") && href != "" {
					m["ap:id"] = href
				}
				// oEmbed discovery (#639)。JSON のみサポート: XML は
				// 仕様上有効だが現状 unmarshaler が無く fallback parser
				// 整備するまで無視 (review #1)。
				if rel == "alternate" && href != "" && attrVal(n, "type") == "application/json+oembed" {
					m["oembed:json"] = href
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return m
}

func metaKeyVal(n *html.Node) (string, string) {
	name := attrVal(n, "property")
	if name == "" {
		name = attrVal(n, "name")
	}
	content := attrVal(n, "content")
	return strings.ToLower(name), content
}

func attrVal(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
