package activitypub

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/shiroha-a/mk/internal/safehttp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClient_PostSigned(t *testing.T) {
	key, pub := newTestKey(t)

	var seenSig string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenSig = r.Header.Get("Signature")
		r.Header.Set("Host", r.Host)
		if err := VerifyRequest(r, pub); err != nil {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewClient(nil, "mk-go-test")
	resp, err := c.PostSigned(srv.URL+"/inbox", []byte(`{"type":"Create"}`), key)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.NotEmpty(t, seenSig)
}

func TestClient_PostSigned_BadURL(t *testing.T) {
	key, _ := newTestKey(t)
	c := NewClient(nil, "")
	_, err := c.PostSigned("://bad", []byte("x"), key)
	assert.Error(t, err)
}

func TestClient_PostSigned_SignError(t *testing.T) {
	c := NewClient(nil, "")
	_, err := c.PostSigned("https://x.example/", []byte("x"), nil)
	assert.Error(t, err)
}

func TestClient_GetSigned(t *testing.T) {
	key, pub := newTestKey(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Header.Set("Host", r.Host)
		if err := VerifyRequest(r, pub); err != nil {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/activity+json")
		_, _ = w.Write([]byte(`{"id":"https://example.com/users/u1"}`))
	}))
	defer srv.Close()

	c := NewClient(nil, "")
	resp, err := c.GetSigned(srv.URL+"/users/u1", key, "")
	require.NoError(t, err)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	assert.Contains(t, string(body), "u1")
}

func TestClient_GetSigned_WithUserAgent(t *testing.T) {
	key, _ := newTestKey(t)
	var seenUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenUA = r.Header.Get("User-Agent")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewClient(nil, "mk-go-test/1.0")
	resp, err := c.GetSigned(srv.URL+"/", key, "")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, "mk-go-test/1.0", seenUA)
}

func TestClient_GetSigned_AcceptOverride(t *testing.T) {
	key, _ := newTestKey(t)
	var seenAccept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAccept = r.Header.Get("Accept")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewClient(nil, "")
	resp, err := c.GetSigned(srv.URL+"/", key, "application/json")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, "application/json", seenAccept)
}

func TestClient_GetSigned_BadURL(t *testing.T) {
	key, _ := newTestKey(t)
	c := NewClient(nil, "")
	_, err := c.GetSigned("://bad", key, "")
	assert.Error(t, err)
}

func TestClient_GetSigned_SignError(t *testing.T) {
	c := NewClient(nil, "")
	_, err := c.GetSigned("https://x.example/", nil, "")
	assert.Error(t, err)
}

func TestClient_FetchJSON(t *testing.T) {
	key, _ := newTestKey(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c := NewClient(nil, "")
	got, err := c.FetchJSON(srv.URL+"/x", key)
	require.NoError(t, err)
	assert.Contains(t, string(got), "ok")
}

func TestClient_FetchJSON_NonOK(t *testing.T) {
	key, _ := newTestKey(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := NewClient(nil, "")
	_, err := c.FetchJSON(srv.URL, key)
	assert.Error(t, err)
}

func TestClient_FetchJSON_ResponseTooLarge(t *testing.T) {
	// #323: MaxBodyBytes を超えたレスポンスは ErrResponseTooLarge でエラーになる。
	key, _ := newTestKey(t)
	oversized := make([]byte, int(MaxBodyBytes)+10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(oversized)
	}))
	defer srv.Close()

	c := NewClient(nil, "")
	_, err := c.FetchJSON(srv.URL, key)
	require.Error(t, err)
	assert.True(t, errors.Is(err, safehttp.ErrResponseTooLarge))
}

func TestClient_FetchUnsigned_ResponseTooLarge(t *testing.T) {
	oversized := make([]byte, int(MaxBodyBytes)+10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(oversized)
	}))
	defer srv.Close()

	c := NewClient(nil, "")
	_, err := c.FetchUnsigned(srv.URL)
	require.Error(t, err)
	assert.True(t, errors.Is(err, safehttp.ErrResponseTooLarge))
}

func TestClient_FetchJSON_Error(t *testing.T) {
	key, _ := newTestKey(t)
	c := NewClient(nil, "")
	_, err := c.FetchJSON("://bad", key)
	assert.Error(t, err)
}

func TestClient_FetchHTML(t *testing.T) {
	const body = `<html><head><link rel="icon" href="/x.png"></head></html>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Contains(t, r.Header.Get("Accept"), "text/html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	c := NewClient(nil, "mk-test/1.0")
	got, err := c.FetchHTML(srv.URL)
	require.NoError(t, err)
	assert.Equal(t, body, string(got))
}

func TestClient_FetchHTML_NonOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := NewClient(nil, "")
	_, err := c.FetchHTML(srv.URL)
	assert.Error(t, err)
}

func TestClient_FetchHTML_BadURL(t *testing.T) {
	c := NewClient(nil, "")
	_, err := c.FetchHTML("://bad")
	assert.Error(t, err)
}

func TestClient_FetchHTML_NonHTMLContentType(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c := NewClient(nil, "")
	_, err := c.FetchHTML(srv.URL)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unexpected content-type")
}

func TestClient_FetchHTML_ResponseTooLarge(t *testing.T) {
	oversized := make([]byte, int(MaxHTMLBodyBytes)+10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// body sniffingで application/octet-stream に推定されてContent-Type
		// checkに引っかからないよう明示的にtext/htmlをセットする。
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write(oversized)
	}))
	defer srv.Close()

	c := NewClient(nil, "")
	_, err := c.FetchHTML(srv.URL)
	require.Error(t, err)
	assert.True(t, errors.Is(err, safehttp.ErrResponseTooLarge))
}

func TestNewClient_DefaultHTTPClient(t *testing.T) {
	c := NewClient(nil, "")
	assert.NotNil(t, c)
}

func TestClient_FetchUnsigned_OK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/activity+json")
		_, _ = w.Write([]byte(`{"id":"https://example.com/x"}`))
	}))
	defer srv.Close()

	c := NewClient(nil, "test-ua")
	body, err := c.FetchUnsigned(srv.URL + "/x")
	require.NoError(t, err)
	assert.Contains(t, string(body), "https://example.com/x")
}

func TestClient_FetchUnsigned_NonOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := NewClient(nil, "")
	_, err := c.FetchUnsigned(srv.URL)
	assert.Error(t, err)
}

func TestClient_FetchUnsigned_BadURL(t *testing.T) {
	c := NewClient(nil, "")
	_, err := c.FetchUnsigned("://bad")
	assert.Error(t, err)
}

// TestClient_FetchUnsignedJSON_AcceptHeader pins the Accept header used
// for non-AP discovery endpoints (#474). Iceshrimp.NET returns a 406
// envelope when the AP MIME types are sent, so plain JSON Accept must
// be preserved.
func TestClient_FetchUnsignedJSON_AcceptHeader(t *testing.T) {
	var seenAccept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAccept = r.Header.Get("Accept")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"links":[]}`))
	}))
	defer srv.Close()

	c := NewClient(nil, "")
	_, err := c.FetchUnsignedJSON(srv.URL + "/.well-known/nodeinfo")
	require.NoError(t, err)
	assert.Equal(t, "application/json, */*", seenAccept,
		"plain JSON Accept must be sent so strict implementations like Iceshrimp.NET do not 406")
}

func TestClient_FetchUnsignedJSON_NonOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := NewClient(nil, "")
	_, err := c.FetchUnsignedJSON(srv.URL)
	assert.Error(t, err)
}

func TestClient_FetchUnsigned_NetworkError(t *testing.T) {
	c := NewClient(nil, "")
	_, err := c.FetchUnsigned("http://127.0.0.1:1/")
	assert.Error(t, err)
}
