package miauth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hostRewriteTransport sends every request to the test server, so a Client
// configured for "remote.example" actually talks to httptest.
//
// Client は https を組み立てるので、素の httptest URL では届かない。**host は
// 呼び出し側の入力であり、そこを検証しているのが要点**なので、host 名は保った
// まま宛先だけ差し替える。
type hostRewriteTransport struct {
	base    *url.URL
	lastURL string
}

func (t *hostRewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.lastURL = req.URL.String()
	out := req.Clone(req.Context())
	out.URL.Scheme = t.base.Scheme
	out.URL.Host = t.base.Host
	return http.DefaultTransport.RoundTrip(out)
}

func newTestClient(t *testing.T, handler http.HandlerFunc) (*Client, *hostRewriteTransport) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	base, err := url.Parse(srv.URL)
	require.NoError(t, err)
	tr := &hostRewriteTransport{base: base}
	return NewClient(&http.Client{Transport: tr}, "mk-go/test"), tr
}

func TestCheck_Success(t *testing.T) {
	c, tr := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "mk-go/test", r.Header.Get("User-Agent"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"token":"secret","user":{"id":"9abc","username":"alice","name":"Alice","host":null,"avatarUrl":"https://remote.example/a.png"}}`))
	})

	got, err := c.Check(context.Background(), "remote.example", "sess-1")
	require.NoError(t, err)
	assert.Equal(t, "remote.example", got.Host)
	assert.Equal(t, "9abc", got.RemoteID)
	assert.Equal(t, "alice", got.Username)
	assert.Equal(t, "Alice", got.Name)
	assert.Equal(t, "https://remote.example/a.png", got.AvatarURL)

	// 問い合わせ先が正しいこと。
	assert.Equal(t, "https://remote.example/api/miauth/sess-1/check", tr.lastURL)
}

// 未承認・拒否・消費済みはすべて ok:false で返る。upstream の check は
// `!token.fetched` を要求するので**単回限り**。
func TestCheck_NotAuthorized(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":false}`))
	})

	_, err := c.Check(context.Background(), "remote.example", "sess-1")
	assert.ErrorIs(t, err, ErrNotAuthorized)
}

// **相手サーバー自身のユーザーであることを要求する。** host が入っているなら
// そのアカウントは第三のサーバーのもので、問い合わせた host は当人を代弁できない。
func TestCheck_RejectsForeignAccount(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true,"user":{"id":"9abc","username":"alice","host":"victim.example"}}`))
	})

	_, err := c.Check(context.Background(), "remote.example", "sess-1")
	assert.ErrorIs(t, err, ErrNotLocalToHost)
}

func TestCheck_UnexpectedResponses(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{
			name: "not json",
			handler: func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(`<html>not misskey</html>`))
			},
		},
		{
			name: "missing id",
			handler: func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(`{"ok":true,"user":{"username":"alice"}}`))
			},
		},
		{
			name: "missing username",
			handler: func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(`{"ok":true,"user":{"id":"9abc"}}`))
			},
		},
		{
			name: "non-200",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusNotFound)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := newTestClient(t, tt.handler)
			_, err := c.Check(context.Background(), "remote.example", "sess-1")
			assert.ErrorIs(t, err, ErrUnexpectedResponse)
		})
	}
}

func TestCheck_TransportError(t *testing.T) {
	c := NewClient(&http.Client{Transport: errTransport{}}, "")
	_, err := c.Check(context.Background(), "remote.example", "sess-1")
	assert.Error(t, err)
}

type errTransport struct{}

func (errTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, assertErr
}

var assertErr = &url.Error{Op: "Post", URL: "https://remote.example", Err: http.ErrHandlerTimeout}

func TestCheck_ValidatesHost(t *testing.T) {
	c := NewClient(nil, "")
	_, err := c.Check(context.Background(), "not a host", "sess-1")
	assert.ErrorIs(t, err, ErrInvalidHost)
}

// **利用者のブラウザを飛ばす先になる。** スキーム付き・パス付き・資格情報付きの
// 入力を通すと、任意の URL へのリダイレクタになる。
func TestValidateHost(t *testing.T) {
	valid := []string{"remote.example", "sub.remote.example", "xn--eckve.example"}
	for _, h := range valid {
		t.Run("valid/"+h, func(t *testing.T) {
			assert.NoError(t, ValidateHost(h))
		})
	}

	invalid := []string{
		"",
		"localhost",
		"remote.example/path",
		"remote.example:3000",
		"https://remote.example",
		"user@remote.example",
		"remote.example?x=1",
		"remote.example#frag",
		"remote.example\\evil.example",
		" remote.example",
		"remote.example ",
		"remote.example\n",
		// url.Parse が弾く形 (不正な percent-encoding)。前段の文字種チェックを
		// すり抜けるので、最後の url.Parse が要る。
		"remote.exa%zz",
	}
	for _, h := range invalid {
		t.Run("invalid/"+h, func(t *testing.T) {
			assert.ErrorIs(t, ValidateHost(h), ErrInvalidHost, "host %q should be rejected", h)
		})
	}
}

func TestAuthorizeURL(t *testing.T) {
	got, err := AuthorizeURL("remote.example", "sess-1", "Example", "https://mk.example/signup/callback")
	require.NoError(t, err)

	u, err := url.Parse(got)
	require.NoError(t, err)
	assert.Equal(t, "https", u.Scheme)
	assert.Equal(t, "remote.example", u.Host)
	assert.Equal(t, "/miauth/sess-1", u.Path)
	assert.Equal(t, "Example", u.Query().Get("name"))
	assert.Equal(t, "https://mk.example/signup/callback", u.Query().Get("callback"))
	// **permission は付けない。** 相手には何の権限も要求しない同意画面が出る。
	assert.False(t, u.Query().Has("permission"), "permission should not be requested")
}

func TestAuthorizeURL_Errors(t *testing.T) {
	_, err := AuthorizeURL("bad host", "sess-1", "", "")
	assert.ErrorIs(t, err, ErrInvalidHost)

	_, err = AuthorizeURL("remote.example", "", "", "")
	assert.Error(t, err)
}

// 経路にスラッシュを含む session を入れられても、パスを踏み外さないこと。
func TestCheck_EscapesSession(t *testing.T) {
	c, tr := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":false}`))
	})
	_, _ = c.Check(context.Background(), "remote.example", "../../evil")
	assert.True(t, strings.HasPrefix(tr.lastURL, "https://remote.example/api/miauth/"), tr.lastURL)
	assert.NotContains(t, tr.lastURL, "/api/evil")
}

func TestProbe(t *testing.T) {
	t.Run("misskey-family host", func(t *testing.T) {
		c, tr := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"version":"2026.7.0"}`))
		})
		require.NoError(t, c.Probe(context.Background(), "remote.example"))
		assert.Equal(t, "https://remote.example/api/meta", tr.lastURL)
	})

	// MiAuth を持たない実装を選んだことを、認証が失敗する前に案内できるようにする。
	t.Run("not a misskey host", func(t *testing.T) {
		c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		})
		assert.ErrorIs(t, c.Probe(context.Background(), "mastodon.example"), ErrUnexpectedResponse)
	})

	t.Run("json without version", func(t *testing.T) {
		c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"hello":"world"}`))
		})
		assert.ErrorIs(t, c.Probe(context.Background(), "remote.example"), ErrUnexpectedResponse)
	})

	t.Run("not json", func(t *testing.T) {
		c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`<html>`))
		})
		assert.ErrorIs(t, c.Probe(context.Background(), "remote.example"), ErrUnexpectedResponse)
	})

	t.Run("transport error", func(t *testing.T) {
		c := NewClient(&http.Client{Transport: errTransport{}}, "")
		assert.ErrorIs(t, c.Probe(context.Background(), "remote.example"), ErrUnexpectedResponse)
	})

	t.Run("validates host", func(t *testing.T) {
		c := NewClient(nil, "")
		assert.ErrorIs(t, c.Probe(context.Background(), "bad host"), ErrInvalidHost)
	})
}

func TestNewClient_DefaultsHTTPClient(t *testing.T) {
	c := NewClient(nil, "")
	require.NotNil(t, c.http)
	assert.NotZero(t, c.http.Timeout, "既定の client は timeout を持つこと")
}
