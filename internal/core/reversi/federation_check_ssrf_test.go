package reversi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
		"別 host":   "https://evil.example/nodeinfo/2.1",
		"別ポート":     "https://remote.example:9999/nodeinfo/2.1",
		"http へ降格": "http://remote.example/nodeinfo/2.1",
		"内部宛て":     "https://169.254.169.254/latest/meta-data/",
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
func TestFederationCheckerWithoutClientDoesNotFetch(t *testing.T) {
	c := NewFederationChecker(nil, nil)
	require.False(t, c.Available(context.Background(), "remote.example"),
		"client 未配線なのに連合可能と答えている")
}
