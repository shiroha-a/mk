package instance

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/shiroha-a/mk/internal/misc/colfit"
	"github.com/shiroha-a/mk/internal/misc/csscolor"
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

// nodeinfoDocument holds the nodeinfo 2.0/2.1 fields mk-go stores.
//
// **struct へ直接 decode しない。** `{"software":{"name":123}}` のように 1 つでも
// 型が違うと `json.Unmarshal` が error を返し、**他の値も 1 つも保存されない**
// (`Fetch` が中断する)。upstream は `typeof ... === 'string'` で個別に見るので、
// 型が違うフィールドだけ落として残りを保存する。値はリモートが自由に決められる
// ので、こちらも 1 フィールドで全部を失わない形にする (#2726)。
type nodeinfoDocument struct {
	// SoftwareName は document が object なら必ず入る。upstream は string で
	// なければ '?' を入れる (`FetchInstanceMetadataService`)。case は
	// `.toLowerCase()` で潰す — software block の判定は元から case-insensitive
	// なので回避には使えないが、`federation/instances` が返す値が upstream に
	// 近づく。**JSON の `null` は object ではない**ので空のまま (下記)。
	//
	// **「揃う」ではなく「近づく」。** Go の `strings.ToLower` は simple case
	// mapping なので JS の `toLowerCase()` とはずれる。go1.26.6
	// (`unicode.Version` 15.0.0) と node 22 (Unicode 17.0) で全符号位置を
	// 突き合わせた実測では 3 系統:
	//
	//   - `İ` (U+0130) — 両方小文字化するが結果が違う (Go `i` / JS `i`+U+0307)。
	//     **ASCII に落ちる差はこれだけ**
	//   - Unicode 版差 55 符号位置 (`Ɤ` U+A7CB など) — JS だけが小文字化する。
	//     Go の unicode table が上がれば消える
	//   - 文脈依存の final sigma — 符号位置ごとの比較では見えない
	//     (`MISSKEΣ` → Go `misskeσ` / JS `misskeς`)
	//
	// software name にこれらが出ることは現実には無く、
	// `MatchSuspendedSoftware` は両側を lowercase して比べるので判定にも
	// 効かないため、ここでは合わせ込まない (#2726)。
	SoftwareName      string
	SoftwareVersion   string
	OpenRegistrations *bool
	NodeName          string
	NodeDescription   string
	ThemeColor        string
}

// parseNodeinfoDocument decodes a nodeinfo document leniently: fields whose JSON
// type does not match are dropped instead of failing the whole document.
func parseNodeinfoDocument(body []byte) (*nodeinfoDocument, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(body, &top); err != nil {
		return nil, err
	}
	// **`null` の body で `'?'` を書かない。** `json.Unmarshal("null", &map)` は
	// error にならず nil map を返す。upstream は `if (info)` で囲っているので
	// nodeinfo が null なら software 系の列に一切触れない。ここで placeholder を
	// 入れると、**壊れた nodeinfo を返す相手の softwareName を毎回 `?` で
	// 上書きする**。
	if top == nil {
		return &nodeinfoDocument{}, nil
	}
	doc := &nodeinfoDocument{SoftwareName: unknownSoftwareName}
	if sw := jsonObject(top["software"]); sw != nil {
		if name, ok := jsonString(sw["name"]); ok {
			doc.SoftwareName = strings.ToLower(name)
		}
		doc.SoftwareVersion, _ = jsonString(sw["version"])
	}
	if v, ok := jsonBool(top["openRegistrations"]); ok {
		doc.OpenRegistrations = &v
	}
	if meta := jsonObject(top["metadata"]); meta != nil {
		doc.NodeName, _ = jsonString(meta["nodeName"])
		doc.NodeDescription, _ = jsonString(meta["nodeDescription"])
		doc.ThemeColor, _ = jsonString(meta["themeColor"])
	}
	return doc, nil
}

// unknownSoftwareName mirrors upstream's `'?'` placeholder for a nodeinfo whose
// `software.name` is missing or not a string.
const unknownSoftwareName = "?"

// jsonObject decodes raw as a JSON object, or returns nil when it is absent or
// not an object.
func jsonObject(raw json.RawMessage) map[string]json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	return m
}

// jsonString decodes raw as a JSON string. ok is false when it is absent or of
// another type.
func jsonString(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return "", false
	}
	return s, true
}

// jsonBool decodes raw as a JSON boolean. ok is false when it is absent or of
// another type.
func jsonBool(raw json.RawMessage) (bool, bool) {
	if len(raw) == 0 {
		return false, false
	}
	var b bool
	if json.Unmarshal(raw, &b) != nil {
		return false, false
	}
	return b, true
}

// preferredRels lists the nodeinfo schema versions in order of preference.
//
// **2.1 → 2.0 だけ。1.0 へは fallback しない。** upstream は
// `link2_1 ?? link2_0 ?? link1_0` なので、1.0 しか出さない実装の nodeinfo は
// mk-go だけが取りこぼす (docs/divergence.md、#2723 以前からの挙動)。
// コメントは「1.0 も見る」と書いてあったが一覧に無く、実装と食い違っていた。
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
	// text 列は切って NUL を落とす。**URL 列に生の NUL は届かない** — `url.Parse` が
	// 制御文字を弾き、唯一通る fragment の NUL も `url.URL.String()` が `%00` に
	// escape するので、ここまで来ない (fetchIcons。実測で確認)。判定側
	// (`fitsInstanceColumn` → `colfit.Fits`) は NUL も見るが、この経路では
	// 空振りする (#2726 で委譲したときに見るようになった)。
	// `software.name` が string でなければ upstream と同じ '?' が入っている
	// (parseNodeinfoDocument)。**空になった値は書かない**のは #2723 のまま —
	// upstream は `""` をそのまま書くが、`"\u0000"` のような値では update ごと
	// 落として**何も書かない**ので、既存値を残すほうが upstream の結末に近い。
	if v := clampInstanceText(doc.SoftwareName, maxInstanceSoftwareNameLen); v != "" {
		fields["softwareName"] = &v
	}
	if v := clampInstanceText(doc.SoftwareVersion, maxInstanceSoftwareVersionLen); v != "" {
		fields["softwareVersion"] = &v
	}
	if doc.OpenRegistrations != nil {
		fields["openRegistrations"] = doc.OpenRegistrations
	}
	if v := clampInstanceText(doc.NodeName, maxInstanceNameLen); v != "" {
		fields["name"] = &v
	}
	if v := clampInstanceText(doc.NodeDescription, maxInstanceDescriptionLen); v != "" {
		fields["description"] = &v
	}
	// themeColor は upstream と同じく tinycolor で検証して `#rrggbb` に正規化
	// する。不正な値は書かない (upstream は null にする、#2726)。
	//
	// **他の text 列と違って clamp しない。** 書くのは `csscolor.Normalize` が
	// 組み直した `#rrggbb` の 7 文字だけで、入力の文字は 1 つも持ち越さない。
	// 列 (varchar(64)) に収まり、NUL も届かない。
	//
	// **「NUL は色として読めないから落ちる」ではない。** anchor されていないのは
	// 関数形式 (rgb / hsl / hsv) の matcher で、`"rgb(1,2,3)\u0000"` は valid な
	// まま通る (実測)。効いているのは出力を組み直していることのほう。
	// hex と色名は完全一致なので、そちらは位置にも長さにも敏感。
	if v, ok := csscolor.Normalize(doc.ThemeColor); ok {
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
)

// clampInstanceText prepares a remote-supplied text value for its column: NUL を
// 落とし、max rune で切る。
//
// **URL とは扱いが違う。** icon / favicon は「収まらなければ値ごと捨てる」
// (切った URL は別物で取りに行っても無駄) が、表示用のテキストは切っても意味が
// 残るので切る。
func clampInstanceText(raw string, max int) string {
	return colfit.Text(raw, max)
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
//
// colfit.Fits は NUL も見る。**ここへ生の NUL は届かない** (上の Fetch のコメント
// 参照) ので余計な判定だが、数え方を 1 箇所に集めるほうを採る (#2726)。
func fitsInstanceColumn(v string, max int) bool {
	return v != "" && colfit.Fits(v, max)
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
	return parseNodeinfoDocument(body)
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
