package reversi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
)

// recordingDoer records every URL the checker tries to fetch.
type recordingDoer struct {
	seen []string
	body map[string]string
}

func (d *recordingDoer) Do(req *http.Request) (*http.Response, error) {
	d.seen = append(d.seen, req.URL.String())
	body, ok := d.body[req.URL.String()]
	if !ok {
		return &http.Response{StatusCode: http.StatusNotFound, Body: http.NoBody}, nil
	}
	rec := httptest.NewRecorder()
	rec.WriteHeader(http.StatusOK)
	_, _ = rec.WriteString(body)
	return rec.Result(), nil
}

func discoveryJSON(t *testing.T, href string) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"links": []map[string]string{
			{"rel": "http://nodeinfo.diaspora.software/ns/schema/2.1", "href": href},
		},
	})
	require.NoError(t, err)
	return string(b)
}

// **discovery が指す先を検証せずに叩いていた。** リモートが返す JSON なので
// 任意の URL を書ける。到達先は SSRF-safe transport なので private IP へは
// 行かないが、任意の public host / 任意ポートへの GET リレーは成立し、返って
// きた JSON の `reversiVersion` がそのまま Redis に 5 分間入る。
func TestResolveNodeinfoURLRequiresSameHost(t *testing.T) {
	const host = "remote.example"
	const discovery = "https://remote.example/.well-known/nodeinfo"

	t.Run("同じ host なら辿る", func(t *testing.T) {
		d := &recordingDoer{body: map[string]string{
			discovery: discoveryJSON(t, "https://remote.example/nodeinfo/2.1"),
		}}
		c := NewFederationChecker(nil, d)
		require.Equal(t, "https://remote.example/nodeinfo/2.1",
			c.resolveNodeinfoURL(context.Background(), host, discovery))
	})

	for name, href := range map[string]string{
		"別 host": "https://evil.example/nodeinfo/2.1",
		"別ポート":   "https://remote.example:9999/nodeinfo/2.1",
		"内部宛て":   "https://169.254.169.254/latest/meta-data/",
	} {
		t.Run("辿らない: "+name, func(t *testing.T) {
			d := &recordingDoer{body: map[string]string{discovery: discoveryJSON(t, href)}}
			c := NewFederationChecker(nil, d)
			require.Empty(t, c.resolveNodeinfoURL(context.Background(), host, discovery),
				"host 外を指す href を採用している")
			require.Equal(t, []string{discovery}, d.seen,
				"host 外の href を実際に fetch している")
		})
	}
}

// **未配線なら取りに行かない (fail-closed)。** 以前は素の `http.Client` へ
// 落としており、配線を落とした瞬間に SSRF ガードを通らない経路が開いた。
//
// **「取りに行かない」ことを直接見る (レビュー H1)。** `Available` の戻り値だけを
// 見ていると、fail-open に戻しても名前解決に失敗して false になるので素通り
// する — しかもその変異は**単体テストから実ネットワークへ出る**形になる。
func TestFederationCheckerWithoutClientDoesNotFetch(t *testing.T) {
	c := NewFederationChecker(nil, nil)

	// 取得口を直接叩く。ここが fail-open に戻ると、client を自前で作って
	// 外へ出てしまう。
	_, err := c.httpGetJSON(context.Background(), "https://remote.example/.well-known/nodeinfo", true)
	require.ErrorIs(t, err, errNoHTTPClient, "client 未配線なのに取りに行っている")

	// discovery の解決も同じ理由で何も返さない。
	require.Empty(t, c.resolveNodeinfoURL(context.Background(), "remote.example",
		"https://remote.example/.well-known/nodeinfo"))

	require.False(t, c.Available(context.Background(), "remote.example"),
		"client 未配線なのに連合可能と答えている")
}

// redirectingDoer follows one redirect the way a real http.Client does, and
// reports the final URL through resp.Request (which is what the guard reads).
type redirectingDoer struct {
	seen []string
	to   string
	body string
}

func (d *redirectingDoer) Do(req *http.Request) (*http.Response, error) {
	d.seen = append(d.seen, req.URL.String())
	final := req
	if d.to != "" {
		u, err := url.Parse(d.to)
		if err != nil {
			return nil, err
		}
		final = req.Clone(req.Context())
		final.URL = u
		d.seen = append(d.seen, d.to)
	}
	rec := httptest.NewRecorder()
	rec.WriteHeader(http.StatusOK)
	_, _ = rec.WriteString(d.body)
	resp := rec.Result()
	resp.Request = final
	return resp, nil
}

// **href の host を縛っても redirect で抜けられた (レビュー H3)。** この client は
// `CheckRedirect` を設定していないので常に追従する。最終 URL まで見ないと
// 「その host の文書を読んだ」とは言えない。
func TestHTTPGetJSONRefusesCrossHostRedirect(t *testing.T) {
	const asked = "https://remote.example/nodeinfo/2.1"

	t.Run("別 host へ飛ばされたら読まない", func(t *testing.T) {
		d := &redirectingDoer{to: "https://evil.example/ni", body: `{"metadata":{"reversiVersion":"1.1.1"}}`}
		c := NewFederationChecker(nil, d)
		_, err := c.httpGetJSON(context.Background(), asked, true)
		require.ErrorIs(t, err, errRedirectedToAnotherHost,
			"別 host が返した本文を読んでいる")
	})

	t.Run("同じ host の中の redirect は通す", func(t *testing.T) {
		d := &redirectingDoer{to: "https://remote.example/nodeinfo/2.1/", body: `{"ok":true}`}
		c := NewFederationChecker(nil, d)
		body, err := c.httpGetJSON(context.Background(), asked, true)
		require.NoError(t, err)
		require.JSONEq(t, `{"ok":true}`, string(body))
	})

	t.Run("既定ポートの明記は同じ host", func(t *testing.T) {
		d := &redirectingDoer{to: "https://remote.example:443/nodeinfo/2.1", body: `{"ok":true}`}
		c := NewFederationChecker(nil, d)
		_, err := c.httpGetJSON(context.Background(), asked, true)
		require.NoError(t, err)
	})
}

// **http の href も選ぶ (2 周目レビュー M2)。** 1 周目で https 限定をやめたのに、
// reversi 側は reject 一覧から消しただけで positive case が無く、`https` 限定へ
// 戻しても緑のままだった。TLS 終端が前段にある相手のメタデータが取れなくなる。
func TestResolveNodeinfoURLAcceptsHTTPHref(t *testing.T) {
	const host = "remote.example"
	const discovery = "https://remote.example/.well-known/nodeinfo"

	for name, href := range map[string]string{
		"http":         "http://remote.example/nodeinfo/2.1",
		"http の既定ポート":  "http://remote.example:80/nodeinfo/2.1",
		"https の既定ポート": "https://remote.example:443/nodeinfo/2.1",
	} {
		t.Run(name, func(t *testing.T) {
			d := &recordingDoer{body: map[string]string{discovery: discoveryJSON(t, href)}}
			c := NewFederationChecker(nil, d)
			require.Equal(t, href, c.resolveNodeinfoURL(context.Background(), host, discovery),
				"正当な href を落としている")
		})
	}
}

// **discovery の委譲は追う (2 周目レビュー M5)。** `.well-known/*` を別 host へ
// まとめて委譲する構成は実在する。document hop だけ飛び先を縛る。
func TestHTTPGetJSONFollowsDiscoveryRedirect(t *testing.T) {
	d := &redirectingDoer{to: "https://social.remote.example/.well-known/nodeinfo", body: `{"ok":true}`}
	c := NewFederationChecker(nil, d)

	body, err := c.httpGetJSON(context.Background(),
		"https://remote.example/.well-known/nodeinfo", false)
	require.NoError(t, err, "`.well-known` の委譲を落としている")
	require.JSONEq(t, `{"ok":true}`, string(body))
}
