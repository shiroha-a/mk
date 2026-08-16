// Package miauth speaks the client side of Misskey's MiAuth flow (#2556).
//
// MiAuth は**アプリの事前登録が要らない**ので、任意のリモート Misskey に対して
// 開始できる。承認制の登録 (#2554) では、これを「連絡先アカウントを本当に持って
// いるか」の確認に使う。
//
// 流れ:
//
//  1. session (UUID) を作り、`https://<host>/miauth/<session>?callback=...` へ誘導する
//  2. 相手が許可すると callback に戻ってくる
//  3. `POST https://<host>/api/miauth/<session>/check` で結果を取る
//
// **permission は要求しない。** check の応答にユーザーオブジェクトが入っている
// ので、身元確認にトークンは要らない。相手には「何も許可しない」同意画面が出る。
// 返ってきたトークンは保持しない。
package miauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var (
	// ErrNotAuthorized is returned when the session has not been approved, or
	// has already been consumed.
	//
	// upstream の check は `!token.fetched` を要求し、成功時に fetched を立てる。
	// **つまり単回限り**で、2 度目は ok:false になる。
	ErrNotAuthorized = errors.New("miauth: session is not authorized")
	// ErrUnexpectedResponse is returned when the remote reply is not a MiAuth
	// check response (typically: the host is not a Misskey-family server).
	ErrUnexpectedResponse = errors.New("miauth: unexpected response from the remote host")
	// ErrNotLocalToHost is returned when the authorized account does not belong
	// to the host we contacted.
	ErrNotLocalToHost = errors.New("miauth: the authorized account is not local to that host")
	// ErrInvalidHost is returned when the host is syntactically unusable.
	ErrInvalidHost = errors.New("miauth: invalid host")
)

// maxCheckBody bounds how much of the check response we read. **上限を置かないと、
// 相手が延々と送り続けるだけでこちらのメモリを食える。**
const maxCheckBody = 1 << 20

// Contact is the verified identity behind a MiAuth session.
//
// **Host と RemoteID が一致判定の鍵。** Username は表示専用で、相手サーバーでの
// 改名により変わりうる。
type Contact struct {
	Host      string
	RemoteID  string
	Username  string
	Name      string
	AvatarURL string
}

// Client performs the server-side half of the MiAuth flow.
type Client struct {
	http      *http.Client
	userAgent string
}

// NewClient constructs a Client.
//
// httpClient は **SSRF ガードを通したもの**を渡すこと。host は利用者入力なので、
// 素の http.DefaultClient を渡すと内部ネットワークへ到達させられる。
func NewClient(httpClient *http.Client, userAgent string) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	return &Client{http: httpClient, userAgent: userAgent}
}

// AuthorizeURL builds the URL the applicant is sent to.
//
// permission は付けない (空)。`miauth.vue` は permission が無ければ空配列として
// 扱うので、相手には何の権限も要求しない同意画面が出る。
func AuthorizeURL(host, session, appName, callback string) (string, error) {
	if err := ValidateHost(host); err != nil {
		return "", err
	}
	if session == "" {
		return "", errors.New("miauth: empty session")
	}
	q := url.Values{}
	if appName != "" {
		q.Set("name", appName)
	}
	if callback != "" {
		q.Set("callback", callback)
	}
	u := url.URL{
		Scheme:   "https",
		Host:     host,
		Path:     "/miauth/" + session,
		RawQuery: q.Encode(),
	}
	return u.String(), nil
}

// ValidateHost rejects hosts that cannot be used as an origin.
//
// **利用者のブラウザを飛ばす先になる。** スキーム付き・パス付き・資格情報付きの
// 入力をそのまま URL に埋めると、任意の URL へのリダイレクタになる。
func ValidateHost(host string) error {
	if host == "" {
		return ErrInvalidHost
	}
	if host != strings.TrimSpace(host) {
		return ErrInvalidHost
	}
	// url.Parse は "evil.example/path" を Host 無しで通してしまうので、
	// 構成要素を明示的に弾く。
	if strings.ContainsAny(host, "/\\?#@ \t\r\n") {
		return ErrInvalidHost
	}
	if strings.Contains(host, ":") {
		// port 付きは受けない。連合の host 表記と揃わず、比較がぶれる。
		return ErrInvalidHost
	}
	if !strings.Contains(host, ".") {
		return ErrInvalidHost
	}
	// url.Parse で最終確認する (punycode / 不正文字)。
	u, err := url.Parse("https://" + host)
	if err != nil || u.Host != host {
		return ErrInvalidHost
	}
	return nil
}

// checkResponse mirrors upstream ApiServerService `/miauth/:session/check`.
type checkResponse struct {
	OK   bool `json:"ok"`
	User *struct {
		ID        string  `json:"id"`
		Username  string  `json:"username"`
		Name      *string `json:"name"`
		Host      *string `json:"host"`
		AvatarURL *string `json:"avatarUrl"`
	} `json:"user"`
}

// Check exchanges an approved session for the applicant's identity.
//
// **返ってきた token は使わない。** permission を要求していないので何もできない
// うえ、保持すること自体が負債になる。
func (c *Client) Check(ctx context.Context, host, session string) (*Contact, error) {
	if err := ValidateHost(host); err != nil {
		return nil, err
	}
	endpoint := "https://" + host + "/api/miauth/" + url.PathEscape(session) + "/check"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader("{}"))
	if err != nil {
		return nil, fmt.Errorf("miauth: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if c.userAgent != "" {
		req.Header.Set("User-Agent", c.userAgent)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("miauth: check: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: status %d", ErrUnexpectedResponse, resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxCheckBody))
	if err != nil {
		return nil, fmt.Errorf("miauth: read response: %w", err)
	}
	var parsed checkResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, ErrUnexpectedResponse
	}
	if !parsed.OK || parsed.User == nil {
		// 未承認・拒否・消費済みはすべてここ。upstream は理由を返さない。
		return nil, ErrNotAuthorized
	}
	if parsed.User.ID == "" || parsed.User.Username == "" {
		return nil, ErrUnexpectedResponse
	}
	// **相手サーバー自身のユーザーであることを要求する。** host が入っている
	// なら、そのアカウントは第三のサーバーのものであり、こちらが問い合わせた
	// host は当人を代弁できない。
	if parsed.User.Host != nil && *parsed.User.Host != "" {
		return nil, ErrNotLocalToHost
	}

	contact := &Contact{
		Host:     host,
		RemoteID: parsed.User.ID,
		Username: parsed.User.Username,
	}
	if parsed.User.Name != nil {
		contact.Name = *parsed.User.Name
	}
	if parsed.User.AvatarURL != nil {
		contact.AvatarURL = *parsed.User.AvatarURL
	}
	return contact, nil
}

// Probe reports whether the host answers as a Misskey-family server.
//
// **リダイレクト先として使う前に確かめる。** これが無いと、任意のホストへ利用者を
// 飛ばすオープンリダイレクタになる。ついでに「MiAuth が使えない実装 (Mastodon 等)
// を選んだ」ことを、認証が失敗する前に案内できる。
func (c *Client) Probe(ctx context.Context, host string) error {
	if err := ValidateHost(host); err != nil {
		return err
	}
	endpoint := "https://" + host + "/api/meta"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader("{}"))
	if err != nil {
		return fmt.Errorf("miauth: build probe request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if c.userAgent != "" {
		req.Header.Set("User-Agent", c.userAgent)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrUnexpectedResponse, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: status %d", ErrUnexpectedResponse, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxCheckBody))
	if err != nil {
		return fmt.Errorf("miauth: read probe response: %w", err)
	}
	// Misskey 系なら version を返す。Mastodon の /api/meta は 404 なのでここには来ない。
	var meta struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(body, &meta); err != nil || meta.Version == "" {
		return ErrUnexpectedResponse
	}
	return nil
}
