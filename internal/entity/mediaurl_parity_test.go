package entity_test

// This test lives in the external entity_test package on purpose: it needs both
// entity (the URL builder) and core/mediaproxy (the proxy-side verifier), but
// core/mediaproxy -> core/drive -> entity, so an internal entity test importing
// mediaproxy would form an import cycle. Driving the builder through its
// exported API (ProxyAvatarURL) keeps the assertion end-to-end while staying
// outside that cycle.

import (
	"net/url"
	"testing"

	"github.com/shiroha-a/mk/internal/core/mediaproxy"
	"github.com/shiroha-a/mk/internal/entity"
)

// TestProxySigAcceptedByMediaproxy guarantees the HMAC the builder appends to an
// internal-proxy URL is byte-identical to what mediaproxy.SignURL produces and
// is accepted by the same VerifyHMAC the proxy handler uses. If entity.signURL
// and mediaproxy.SignURL ever drift, every proxied remote URL would 403 — this
// test fails first.
func TestProxySigAcceptedByMediaproxy(t *testing.T) {
	secret := []byte("parity-secret-9f3a")
	entity.SetMediaURLContext(entity.NewMediaURLContext(
		"https://mk.example", "https://mk.example/proxy", secret, false, true))
	defer entity.SetMediaURLContext(nil)

	// A URL with spaces + query chars stresses the encode/decode round-trip the
	// sig depends on.
	raw := "https://remote.example/files/a b.png?w=1&h=2"
	built := entity.ProxyMediaURL(raw)

	u, err := url.Parse(built)
	if err != nil {
		t.Fatalf("builder produced unparseable URL %q: %v", built, err)
	}
	q := u.Query()
	if got := q.Get("url"); got != raw {
		t.Fatalf("url param did not round-trip: %q != %q", got, raw)
	}
	sig := q.Get("sig")
	if sig == "" {
		t.Fatal("internal-proxy URL is missing its sig")
	}
	if sig != mediaproxy.SignURL(secret, raw) {
		t.Errorf("entity sig != mediaproxy.SignURL — HMAC desync")
	}
	if !mediaproxy.VerifyHMAC(secret, raw, sig) {
		t.Error("mediaproxy.VerifyHMAC rejected the builder's sig")
	}
}
