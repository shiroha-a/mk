package selfcheck

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// instanceActorUsername is the system account mk-go signs outgoing fetches
// with. WebFinger / actor の検査対象にこれを使うのは、**どのインスタンスにも
// 必ず存在する**ため (一般ユーザーは 0 人でもありうる)。
const instanceActorUsername = "instance.actor"

// fetchTimeout bounds each self-check request. 自分自身への 1 往復なので
// 短くてよい。長くすると doctor が固まったように見える。
const fetchTimeout = 10 * time.Second

// maxBodyBytes bounds how much of a response we read. 自分の応答とはいえ、
// 設定ミスで巨大な HTML が返ることがある (SPA シェルへの fallback 等)。
const maxBodyBytes = 1 << 20

// certExpiryWarnDays is how close to expiry we start warning.
//
// 証明書が切れると**連合は署名検証より手前の TLS で全滞留する**。気付くのが
// 当日では遅いので、更新作業の猶予がある時点で出す。
const certExpiryWarnDays = 14

// Checker runs the checks against a fixed base URL.
//
// # SSRF ガードとの関係
//
// 自ホストは loopback / private IP に解決されうるので、**この検査だけは SSRF
// ガードを通さない client を使う**。危険にしないための条件は 1 つで、
// **宛先 URL を config からのみ取り、リクエスト由来の値を一切受け付けない**こと。
// baseURL は構築時に固定し、メソッド引数で上書きできないようにしてある。
type Checker struct {
	baseURL string
	client  *http.Client
}

// NewChecker constructs a Checker for the instance's public URL.
func NewChecker(baseURL string) *Checker {
	return &Checker{
		baseURL: strings.TrimRight(baseURL, "/"),
		client: &http.Client{
			Timeout: fetchTimeout,
			// リダイレクトは追わない。`url` が最終的な公開 URL である前提が
			// 崩れているとき (http -> https へ飛ばす設定など) は、それ自体が
			// 運用者に伝えるべき事実。
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

// CheckConfig validates the configured URL itself.
func (c *Checker) CheckConfig() Result {
	const name = "config.url"
	if c.baseURL == "" {
		return failResult(name, "url が空", "`.config/default.yml` の `url` に公開 URL を設定する")
	}
	u, err := url.Parse(c.baseURL)
	if err != nil {
		return failResult(name, fmt.Sprintf("url を parse できない: %v", err),
			"`url` は `https://example.com` の形式で設定する")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return failResult(name, fmt.Sprintf("scheme が %q", u.Scheme),
			"`url` は http:// または https:// で始める")
	}
	if u.Host == "" {
		return failResult(name, "host が空", "`url` にホスト名を含める")
	}
	if u.Scheme == "http" {
		return warnResult(name, "http で公開されている",
			"連合先の多くは https を前提にする。TLS を有効にして `url` を https へ変える")
	}
	return okResult(name, c.baseURL)
}

// CheckWebFinger verifies the instance actor resolves through the public URL.
func (c *Checker) CheckWebFinger(ctx context.Context) Result {
	const name = "webfinger"
	host, err := c.host()
	if err != nil {
		return skipResult(name, "url が不正なため実行できない")
	}
	resource := "acct:" + instanceActorUsername + "@" + host
	target := c.baseURL + "/.well-known/webfinger?resource=" + url.QueryEscape(resource)

	body, resp, err := c.get(ctx, target, "application/jrd+json")
	if err != nil {
		return failResult(name, fmt.Sprintf("取得できない: %v", err),
			"公開 URL がこのサーバーに届いているか、リバースプロキシの転送先を確認する")
	}
	// 403 は転送の問題ではなく**連合そのものが無効**の合図。実際に走らせて
	// 見つけた分岐で、転送設定を疑うヒントを出すと運用者を誤誘導する。
	if resp.StatusCode == http.StatusForbidden {
		return failResult(name, "status 403 (連合が無効)",
			"インスタンス設定の `federation` が `none` になっている。連合するなら管理画面で有効にする")
	}
	// 404 は**転送は届いている**ことの証拠でもある (転送されていなければ SPA の
	// HTML かプロキシのエラーが返る)。instance.actor は連合が一度でも動くと
	// 作られる遅延生成なので、新規インスタンスでは未作成が正常。転送設定を
	// 疑わせない。これも実際に走らせて見つけた分岐。
	if resp.StatusCode == http.StatusNotFound {
		return warnResult(name, "status 404 (instance.actor が未作成)",
			"`/.well-known/webfinger` 自体は届いている。instance.actor は連合が動くと作られるので、新規インスタンスならこのままでよい")
	}
	if resp.StatusCode != http.StatusOK {
		return failResult(name, fmt.Sprintf("status %d", resp.StatusCode),
			"`/.well-known/webfinger` がリバースプロキシで転送されているか確認する。連合の入口なのでここが落ちると誰からも見つけられない")
	}
	var jrd struct {
		Subject string `json:"subject"`
		Links   []struct {
			Rel  string `json:"rel"`
			Type string `json:"type"`
			Href string `json:"href"`
		} `json:"links"`
	}
	if err := json.Unmarshal(body, &jrd); err != nil {
		return failResult(name, "JSON として読めない (HTML が返っている可能性)",
			"SPA の catchall に落ちている。リバースプロキシが `/.well-known/` を backend へ渡しているか確認する")
	}
	if jrd.Subject != resource {
		return failResult(name, fmt.Sprintf("subject が %q (期待 %q)", jrd.Subject, resource),
			"`url` のホスト名と実際に配信しているホスト名がずれている")
	}
	for _, l := range jrd.Links {
		if l.Rel == "self" && strings.Contains(l.Type, "activity+json") {
			return okResult(name, "self link: "+l.Href)
		}
	}
	return failResult(name, "self link (activity+json) が無い",
		"actor へのリンクが返っていない。連合先が actor を見つけられない")
}

// CheckNodeInfo verifies discovery resolves to a nodeinfo document.
func (c *Checker) CheckNodeInfo(ctx context.Context) Result {
	const name = "nodeinfo"
	body, resp, err := c.get(ctx, c.baseURL+"/.well-known/nodeinfo", "application/json")
	if err != nil {
		return failResult(name, fmt.Sprintf("取得できない: %v", err),
			"公開 URL がこのサーバーに届いているか確認する")
	}
	if resp.StatusCode == http.StatusForbidden {
		return failResult(name, "discovery が status 403 (連合が無効)",
			"インスタンス設定の `federation` が `none` になっている")
	}
	if resp.StatusCode != http.StatusOK {
		return failResult(name, fmt.Sprintf("discovery が status %d", resp.StatusCode),
			"`/.well-known/nodeinfo` が転送されているか確認する。他サーバーの一覧サイトから見えなくなる")
	}
	var disc struct {
		Links []struct {
			Rel  string `json:"rel"`
			Href string `json:"href"`
		} `json:"links"`
	}
	if err := json.Unmarshal(body, &disc); err != nil || len(disc.Links) == 0 {
		return failResult(name, "discovery の links が空",
			"nodeinfo discovery の応答が壊れている")
	}
	// 最後の link が最新版を指す慣習。順序に依存しないよう全部試す。
	var lastErr string
	for i := len(disc.Links) - 1; i >= 0; i-- {
		docBody, docResp, derr := c.get(ctx, disc.Links[i].Href, "application/json")
		if derr != nil || docResp.StatusCode != http.StatusOK {
			lastErr = fmt.Sprintf("%s が引けない", disc.Links[i].Href)
			continue
		}
		var doc struct {
			Software struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"software"`
		}
		if json.Unmarshal(docBody, &doc) == nil && doc.Software.Name != "" {
			return okResult(name, doc.Software.Name+" "+doc.Software.Version)
		}
		lastErr = "software 情報が読めない"
	}
	return failResult(name, lastErr, "discovery が指す nodeinfo 本体が配信されていない")
}

// CheckActor verifies the instance actor document and its signing keys.
func (c *Checker) CheckActor(ctx context.Context) Result {
	const name = "actor"
	host, err := c.host()
	if err != nil {
		return skipResult(name, "url が不正なため実行できない")
	}
	target := c.baseURL + "/users/" + instanceActorUsername
	// mk-go は actor を `/users/<id>` で配る。username 経由の URL は
	// `/@instance.actor` なので、WebFinger の self link を辿るのが本筋だが、
	// ここでは WebFinger の失敗と actor の失敗を切り分けたいので直接引く。
	body, resp, err := c.get(ctx, target, "application/activity+json")
	if err != nil {
		return failResult(name, fmt.Sprintf("取得できない: %v", err),
			"公開 URL がこのサーバーに届いているか確認する")
	}
	if resp.StatusCode == http.StatusNotFound {
		// username での配信に対応していない構成もあるので、WebFinger 経由の
		// 判定に委ねて warn に留める。
		return warnResult(name, "username 経由の actor URL が 404",
			"WebFinger の self link から actor が引ければ連合は成立する。この検査だけでは判断できない")
	}
	if resp.StatusCode != http.StatusOK {
		return failResult(name, fmt.Sprintf("status %d", resp.StatusCode),
			"actor が配信されていない。連合先が公開鍵を取得できず署名検証に失敗する")
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "activity+json") && !strings.Contains(ct, "ld+json") {
		return failResult(name, "Content-Type が "+ct,
			"actor は `application/activity+json` で返す必要がある。SPA シェルに落ちている可能性")
	}
	var actor struct {
		ID        string `json:"id"`
		PublicKey struct {
			ID           string `json:"id"`
			PublicKeyPem string `json:"publicKeyPem"`
		} `json:"publicKey"`
		AssertionMethod []struct {
			Type string `json:"type"`
		} `json:"assertionMethod"`
	}
	if err := json.Unmarshal(body, &actor); err != nil {
		return failResult(name, "JSON として読めない", "actor の応答が壊れている")
	}
	if actor.PublicKey.PublicKeyPem == "" {
		return failResult(name, "publicKey が無い",
			"署名検証に使う RSA 公開鍵が actor に載っていない。連合先が投稿を受理できない")
	}
	if idHost, herr := hostOf(actor.ID); herr == nil && idHost != host {
		return failResult(name, fmt.Sprintf("actor.id の host が %q (url は %q)", idHost, host),
			"`url` と実際に配信しているホスト名がずれている。連合先は actor.id 側を正とするため混線する")
	}
	if len(actor.AssertionMethod) == 0 {
		return warnResult(name, "assertionMethod (Ed25519) が無い",
			"RSA だけでも連合できる。Ed25519 を公開すると対応実装との署名検証が軽くなる")
	}
	return okResult(name, "publicKey + assertionMethod あり")
}

// CheckTLS reports the certificate expiry of the public URL.
func (c *Checker) CheckTLS(ctx context.Context) Result {
	const name = "tls"
	u, err := url.Parse(c.baseURL)
	if err != nil || u.Scheme != "https" {
		return skipResult(name, "https ではないため対象外")
	}
	_, resp, err := c.get(ctx, c.baseURL+"/.well-known/nodeinfo", "application/json")
	if err != nil {
		return skipResult(name, "接続できないため証明書を確認できない")
	}
	if resp.TLS == nil || len(resp.TLS.PeerCertificates) == 0 {
		return skipResult(name, "証明書情報が取得できない")
	}
	leaf := resp.TLS.PeerCertificates[0]
	days := int(time.Until(leaf.NotAfter).Hours() / 24)
	switch {
	case days < 0:
		return failResult(name, "証明書が期限切れ",
			"連合は署名検証より手前の TLS で全滞留する。至急更新する")
	case days <= certExpiryWarnDays:
		return warnResult(name, fmt.Sprintf("証明書の残り %d 日", days),
			"自動更新が動いているか確認する")
	default:
		return okResult(name, fmt.Sprintf("証明書の残り %d 日", days))
	}
}

// host returns the host part of the configured URL.
func (c *Checker) host() (string, error) { return hostOf(c.baseURL) }

// hostOf extracts a URL's host.
func hostOf(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if u.Host == "" {
		return "", fmt.Errorf("selfcheck: no host in %q", raw)
	}
	return u.Host, nil
}

// get performs one self-check request.
func (c *Checker) get(ctx context.Context, target, accept string) ([]byte, *http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Accept", accept)
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return nil, resp, err
	}
	return body, resp, nil
}
