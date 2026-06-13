package entity

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/shiroha-a/mk/internal/model"
)

const (
	testInstanceURL   = "https://mk.example"
	testInternalProxy = "https://mk.example/proxy"
	testExternalProxy = "https://cdn.proxy.example"
	remoteHost        = "remote.example"
)

var testSecret = []byte("test-media-proxy-secret")

func sp(s string) *string { return &s }

// internalCtx mirrors the default deployment: internal /proxy, proxyRemoteFiles on.
func internalCtx() *MediaURLContext {
	return NewMediaURLContext(testInstanceURL, testInternalProxy, testSecret, false, true)
}

// externalCtx mirrors an operator-configured external media proxy.
func externalCtx() *MediaURLContext {
	return NewMediaURLContext(testInstanceURL, testExternalProxy, testSecret, true, true)
}

// disabledCtx mirrors proxyRemoteFiles=false with no external proxy: the only
// config where upstream (and mk-go) emit raw remote URLs.
func disabledCtx() *MediaURLContext {
	return NewMediaURLContext(testInstanceURL, testInternalProxy, testSecret, false, false)
}

// TestProxiedURL_InternalFormatAndSig verifies the internal-proxy URL shape and
// that the appended sig is exactly what the proxy server will accept. A wrong
// sig (or signing the encoded form) would 403 every proxied remote URL.
func TestProxiedURL_InternalFormatAndSig(t *testing.T) {
	c := internalCtx()
	raw := "https://" + remoteHost + "/files/a b.png?x=1&y=2"

	cases := []struct {
		mode proxyMode
		file string
		flag string
	}{
		{modeDefault, "image.webp", ""},
		{modeAvatar, "avatar.webp", "avatar"},
		{modeStatic, "static.webp", "static"},
		{modePreview, "preview.webp", "preview"},
		{modeBadge, "badge.webp", "badge"},
		{modeEmoji, "emoji.webp", "emoji"},
	}
	for _, tc := range cases {
		got := c.ProxiedURL(raw, tc.mode)
		if !strings.HasPrefix(got, testInternalProxy+"/"+tc.file+"?") {
			t.Fatalf("mode %v: prefix mismatch: %s", tc.mode, got)
		}
		u, err := url.Parse(got)
		if err != nil {
			t.Fatalf("mode %v: unparseable: %v", tc.mode, err)
		}
		q := u.Query()
		// url param must round-trip to the RAW origin (this is what Echo decodes
		// on the proxy side and what the sig is computed over).
		if q.Get("url") != raw {
			t.Errorf("mode %v: url round-trip mismatch: %q != %q", tc.mode, q.Get("url"), raw)
		}
		if tc.flag != "" && q.Get(tc.flag) == "" {
			t.Errorf("mode %v: missing mode flag %q", tc.mode, tc.flag)
		}
		if tc.flag == "" {
			// modeDefault must not carry any of the resize flags.
			for _, f := range []string{"avatar", "static", "preview", "badge", "emoji"} {
				if q.Get(f) != "" {
					t.Errorf("modeDefault carried flag %q", f)
				}
			}
		}
		sig := q.Get("sig")
		if sig == "" {
			t.Fatalf("mode %v: missing sig", tc.mode)
		}
		// sig must be hex(HMAC-SHA256(secret, raw)) — exactly what the proxy's
		// VerifyHMAC recomputes over the decoded url param. Real cross-package
		// parity with mediaproxy.SignURL + VerifyHMAC acceptance is asserted in
		// the external mediaurl_parity_test.go (kept out of package entity to
		// avoid the entity<->core/mediaproxy import cycle).
		mac := hmac.New(sha256.New, testSecret)
		mac.Write([]byte(raw))
		if want := hex.EncodeToString(mac.Sum(nil)); sig != want {
			t.Errorf("mode %v: sig mismatch", tc.mode)
		}
	}
}

// TestProxiedURL_ExternalNoSig confirms an external proxy gets no HMAC sig
// (it owns its own auth; leaking our secret structure is pointless/harmful).
func TestProxiedURL_ExternalNoSig(t *testing.T) {
	c := externalCtx()
	raw := "https://" + remoteHost + "/a.png"
	got := c.ProxiedURL(raw, modeAvatar)
	if !strings.HasPrefix(got, testExternalProxy+"/avatar.webp?") {
		t.Fatalf("external prefix mismatch: %s", got)
	}
	u, _ := url.Parse(got)
	if u.Query().Get("sig") != "" {
		t.Errorf("external proxy URL must not carry sig: %s", got)
	}
}

// TestGetPublicURL_Matrix exercises the leak gate across configs and file kinds.
func TestGetPublicURL_Matrix(t *testing.T) {
	remoteLink := &model.DriveFile{
		Type: "image/png", URL: "https://" + remoteHost + "/orig.png",
		URI: sp("https://" + remoteHost + "/orig.png"), UserHost: sp(remoteHost), IsLink: true,
	}
	local := &model.DriveFile{
		Type: "image/png", URL: testInstanceURL + "/files/local.png", StoredInternal: true,
	}

	t.Run("nil context returns raw", func(t *testing.T) {
		var c *MediaURLContext
		if got := c.GetPublicURL(remoteLink, modeDefault); got != remoteLink.URL {
			t.Errorf("nil ctx should be raw, got %s", got)
		}
	})
	t.Run("local file never proxied", func(t *testing.T) {
		for _, c := range []*MediaURLContext{internalCtx(), externalCtx(), disabledCtx()} {
			if got := c.GetPublicURL(local, modeDefault); got != local.URL {
				t.Errorf("local file proxied: %s", got)
			}
		}
	})
	t.Run("remote wrapped via internal proxy", func(t *testing.T) {
		got := internalCtx().GetPublicURL(remoteLink, modeDefault)
		if !strings.HasPrefix(got, testInternalProxy+"/") {
			t.Errorf("remote not wrapped: %s", got)
		}
		if strings.Contains(u(t, got).Host, remoteHost) {
			t.Errorf("emitted URL host is the remote origin: %s", got)
		}
	})
	t.Run("remote wrapped via external proxy uses uri", func(t *testing.T) {
		got := externalCtx().GetPublicURL(remoteLink, modeDefault)
		if !strings.HasPrefix(got, testExternalProxy+"/") {
			t.Errorf("remote not wrapped to external: %s", got)
		}
		if q := u(t, got).Query().Get("url"); q != *remoteLink.URI {
			t.Errorf("external should proxy uri, got url=%s", q)
		}
	})
	t.Run("proxyRemoteFiles off + no external = raw (admin opt-out)", func(t *testing.T) {
		if got := disabledCtx().GetPublicURL(remoteLink, modeDefault); got != remoteLink.URL {
			t.Errorf("disabled ctx should be raw, got %s", got)
		}
	})
	t.Run("remote non-image url stays raw (proxy cannot reserve non-browsersafe MIME)", func(t *testing.T) {
		// A remote PDF/zip would break if wrapped: the proxy's default-mode
		// passThrough rejects non-browsersafe MIME. So the download URL must
		// stay raw (the regression this gate prevents).
		pdf := &model.DriveFile{
			Type: "application/pdf", URL: "https://" + remoteHost + "/doc.pdf",
			URI: sp("https://" + remoteHost + "/doc.pdf"), UserHost: sp(remoteHost), IsLink: true,
		}
		if got := internalCtx().GetPublicURL(pdf, modeDefault); got != pdf.URL {
			t.Errorf("non-image remote url should stay raw, got %s", got)
		}
		if got := internalCtx().GetWebpublicURL(&model.DriveFile{Type: "application/pdf",
			WebpublicURL: sp("https://" + remoteHost + "/w.pdf")}); got == nil || strings.Contains(*got, "proxy") {
			t.Errorf("non-image webpublic should stay raw, got %v", got)
		}
	})
}

// TestGetThumbnailURL covers the #460 fallback preservation + remote wrapping.
func TestGetThumbnailURL(t *testing.T) {
	c := internalCtx()

	t.Run("local image keeps raw webpublic/url fallback", func(t *testing.T) {
		f := &model.DriveFile{Type: "image/png", URL: testInstanceURL + "/files/x.png"}
		got := c.GetThumbnailURL(f)
		if got == nil || *got != f.URL {
			t.Fatalf("local image thumb mismatch: %v", got)
		}
	})
	t.Run("remote image with null thumbnail is proxied, never nil (#460)", func(t *testing.T) {
		f := &model.DriveFile{
			Type: "image/png", URL: "https://" + remoteHost + "/x.png",
			URI: sp("https://" + remoteHost + "/x.png"), UserHost: sp(remoteHost), IsLink: true,
		}
		got := c.GetThumbnailURL(f)
		if got == nil {
			t.Fatal("image thumbnail must not be nil")
		}
		if !strings.HasPrefix(*got, testInternalProxy+"/static.webp?") {
			t.Errorf("remote thumb not static-proxied: %s", *got)
		}
	})
	t.Run("remote image with remote thumbnailUrl is proxied", func(t *testing.T) {
		f := &model.DriveFile{
			Type: "image/png", URL: "https://" + remoteHost + "/x.png",
			ThumbnailURL: sp("https://" + remoteHost + "/thumb.png"),
			URI:          sp("https://" + remoteHost + "/x.png"), UserHost: sp(remoteHost), IsLink: true,
		}
		got := c.GetThumbnailURL(f)
		if got == nil || strings.Contains(u(t, *got).Host, remoteHost) {
			t.Errorf("remote thumbnailUrl not wrapped: %v", got)
		}
	})
	t.Run("non-image local without thumbnail stays nil", func(t *testing.T) {
		f := &model.DriveFile{Type: "application/pdf", URL: testInstanceURL + "/files/x.pdf"}
		if got := c.GetThumbnailURL(f); got != nil {
			t.Errorf("non-image should be nil, got %v", *got)
		}
	})
	t.Run("remote video without thumbnail gets static frame via proxy", func(t *testing.T) {
		f := &model.DriveFile{
			Type: "video/mp4", URL: "https://" + remoteHost + "/v.mp4",
			URI: sp("https://" + remoteHost + "/v.mp4"), UserHost: sp(remoteHost), IsLink: true,
		}
		got := c.GetThumbnailURL(f)
		if got == nil || !strings.HasPrefix(*got, testInternalProxy+"/static.webp?") {
			t.Errorf("remote video thumb mismatch: %v", got)
		}
	})
}

// TestPackDriveFile_Integration wires the global context like the router does.
func TestPackDriveFile_Integration(t *testing.T) {
	SetMediaURLContext(internalCtx())
	defer SetMediaURLContext(nil)

	idGen := newTestIDGen(t)
	remote := &model.DriveFile{
		ID: idGen.Generate(time.Now()), Type: "image/png",
		URL: "https://" + remoteHost + "/orig.png", URI: sp("https://" + remoteHost + "/orig.png"),
		ThumbnailURL: sp("https://" + remoteHost + "/t.png"), UserHost: sp(remoteHost), IsLink: true,
	}
	got := PackDriveFile(remote, idGen)
	if strings.Contains(u(t, got.URL).Host, remoteHost) {
		t.Errorf("packed url leaks remote host: %s", got.URL)
	}
	if got.ThumbnailURL == nil || strings.Contains(u(t, *got.ThumbnailURL).Host, remoteHost) {
		t.Errorf("packed thumbnailUrl leaks remote host: %v", got.ThumbnailURL)
	}
	if !strings.HasPrefix(got.URL, testInternalProxy+"/") {
		t.Errorf("packed url not proxied: %s", got.URL)
	}
}

// TestAvatarBannerProxying covers the user-entity surfaces.
func TestAvatarBannerProxying(t *testing.T) {
	SetMediaURLContext(internalCtx())
	defer SetMediaURLContext(nil)

	t.Run("remote avatar is proxied (avatar mode)", func(t *testing.T) {
		u0 := &model.User{Username: "alice", Host: sp(remoteHost), AvatarURL: sp("https://" + remoteHost + "/a.png")}
		got := IdenticonURL(u0)
		if !strings.HasPrefix(got, testInternalProxy+"/avatar.webp?") {
			t.Errorf("remote avatar not proxied: %s", got)
		}
		if strings.Contains(u(t, got).Host, remoteHost) {
			t.Errorf("avatar leaks remote host: %s", got)
		}
	})
	t.Run("local avatar unchanged", func(t *testing.T) {
		u0 := &model.User{Username: "bob", AvatarURL: sp(testInstanceURL + "/files/a.png")}
		if got := IdenticonURL(u0); got != testInstanceURL+"/files/a.png" {
			t.Errorf("local avatar changed: %s", got)
		}
	})
	t.Run("identicon fallback unchanged", func(t *testing.T) {
		u0 := &model.User{Username: "carol", Host: sp(remoteHost)}
		if got := IdenticonURL(u0); got != "/identicon/carol@"+remoteHost {
			t.Errorf("identicon changed: %s", got)
		}
	})
	t.Run("remote banner is proxied", func(t *testing.T) {
		u0 := &model.User{Username: "dave", Host: sp(remoteHost), BannerID: sp("b1"), BannerURL: sp("https://" + remoteHost + "/b.png")}
		d := PackUserDetailed(u0, nil)
		if d.BannerURL == nil || !strings.HasPrefix(*d.BannerURL, testInternalProxy+"/image.webp?") {
			t.Errorf("remote banner not proxied: %v", d.BannerURL)
		}
	})
	t.Run("nil banner stays nil", func(t *testing.T) {
		u0 := &model.User{Username: "erin"}
		if d := PackUserDetailed(u0, nil); d.BannerURL != nil {
			t.Errorf("nil banner became %v", *d.BannerURL)
		}
	})
}

// TestNoRawRemoteHostEscapes is the leak-safe invariant: across the config x
// file-kind matrix, no emitted URL may have the remote host as its direct
// origin (the host only ever appears inside the proxied url= query param).
func TestNoRawRemoteHostEscapes(t *testing.T) {
	mk := func(isLink bool, thumb *string) *model.DriveFile {
		return &model.DriveFile{
			Type: "image/png", URL: "https://" + remoteHost + "/o.png",
			URI: sp("https://" + remoteHost + "/o.png"), UserHost: sp(remoteHost),
			IsLink: isLink, ThumbnailURL: thumb,
		}
	}
	files := []*model.DriveFile{
		mk(true, nil),
		mk(true, sp("https://"+remoteHost+"/t.png")),
		mk(false, sp("https://"+remoteHost+"/t.png")),
	}
	// disabledCtx intentionally serves raw (admin opt-out), so it is excluded
	// from the leak-safe set — it is the one documented config that leaks.
	for _, c := range []*MediaURLContext{internalCtx(), externalCtx()} {
		for _, f := range files {
			if h := u(t, c.GetPublicURL(f, modeDefault)).Host; strings.Contains(h, remoteHost) {
				t.Errorf("GetPublicURL leaked host %s", h)
			}
			if tu := c.GetThumbnailURL(f); tu != nil {
				if h := u(t, *tu).Host; strings.Contains(h, remoteHost) {
					t.Errorf("GetThumbnailURL leaked host %s", h)
				}
			}
		}
	}
}

// TestMediaURLEdgeCases covers the low-traffic branches (relative/unparseable
// URLs, webpublic wrapping, nil/local fast-paths) so the security-critical
// builder is fully exercised.
func TestMediaURLEdgeCases(t *testing.T) {
	c := internalCtx()

	t.Run("isRemoteOrigin treats relative and same-host as local", func(t *testing.T) {
		for _, s := range []string{"", "/identicon/x", "/files/x.png", testInstanceURL + "/files/x.png"} {
			if c.isRemoteOrigin(s) {
				t.Errorf("%q should be local", s)
			}
		}
		if !c.isRemoteOrigin("https://" + remoteHost + "/x.png") {
			t.Error("remote URL should be remote")
		}
		// control char makes url.Parse fail -> treated as local (never proxied).
		if c.isRemoteOrigin("http://%zz") {
			t.Error("unparseable URL should be treated as local")
		}
	})

	t.Run("GetWebpublicURL wraps remote, passes local/nil", func(t *testing.T) {
		remote := &model.DriveFile{Type: "image/png", WebpublicURL: sp("https://" + remoteHost + "/w.png")}
		got := c.GetWebpublicURL(remote)
		if got == nil || !strings.HasPrefix(*got, testInternalProxy+"/") {
			t.Errorf("remote webpublic not wrapped: %v", got)
		}
		local := &model.DriveFile{Type: "image/png", WebpublicURL: sp(testInstanceURL + "/files/w.png")}
		if got := c.GetWebpublicURL(local); got == nil || *got != *local.WebpublicURL {
			t.Errorf("local webpublic changed: %v", got)
		}
		if got := c.GetWebpublicURL(&model.DriveFile{Type: "image/png"}); got != nil {
			t.Errorf("nil webpublic became %v", *got)
		}
		// nil context passes through.
		var nilCtx *MediaURLContext
		if got := nilCtx.GetWebpublicURL(remote); got != remote.WebpublicURL {
			t.Error("nil ctx should pass webpublic through")
		}
	})

	t.Run("ProxyMediaURL nil-context and local pass through", func(t *testing.T) {
		SetMediaURLContext(nil)
		if got := ProxyMediaURL("https://" + remoteHost + "/x"); got != "https://"+remoteHost+"/x" {
			t.Errorf("nil ctx should be raw: %s", got)
		}
		SetMediaURLContext(c)
		defer SetMediaURLContext(nil)
		if got := ProxyMediaURL(testInstanceURL + "/files/x"); got != testInstanceURL+"/files/x" {
			t.Errorf("local should be raw: %s", got)
		}
	})

	t.Run("ProxyMediaURLPtr handles nil/empty/local/remote (role icon, announcement)", func(t *testing.T) {
		SetMediaURLContext(c)
		defer SetMediaURLContext(nil)
		if got := ProxyMediaURLPtr(nil); got != nil {
			t.Error("nil pointer should stay nil")
		}
		empty := ""
		if got := ProxyMediaURLPtr(&empty); got != &empty {
			t.Error("empty pointer should pass through")
		}
		local := testInstanceURL + "/files/icon.png"
		if got := ProxyMediaURLPtr(&local); got == nil || *got != local {
			t.Errorf("local should be raw: %v", got)
		}
		remote := "https://" + remoteHost + "/icon.png"
		if got := ProxyMediaURLPtr(&remote); got == nil || !strings.HasPrefix(*got, testInternalProxy+"/") {
			t.Errorf("remote should be wrapped: %v", got)
		}
		// nil context: pointer passes through untouched.
		SetMediaURLContext(nil)
		if got := ProxyMediaURLPtr(&remote); got == nil || *got != remote {
			t.Errorf("nil ctx should pass through: %v", got)
		}
	})

	t.Run("proxyIfRemote handles nil receiver and empty pointer", func(t *testing.T) {
		var nilCtx *MediaURLContext
		if got := nilCtx.proxyIfRemote(sp("https://"+remoteHost+"/x"), modeStatic); got == nil || strings.Contains(*got, "proxy") {
			t.Errorf("nil ctx should pass through: %v", got)
		}
		if got := c.proxyIfRemote(nil, modeStatic); got != nil {
			t.Error("nil pointer should stay nil")
		}
	})

	t.Run("disabled ctx leaves remote thumbnail raw (admin opt-out)", func(t *testing.T) {
		f := &model.DriveFile{
			Type: "image/png", URL: "https://" + remoteHost + "/x.png",
			ThumbnailURL: sp("https://" + remoteHost + "/t.png"), UserHost: sp(remoteHost), IsLink: true,
		}
		got := disabledCtx().GetThumbnailURL(f)
		if got == nil || *got != *f.ThumbnailURL {
			t.Errorf("disabled ctx should be raw thumb: %v", got)
		}
	})

	t.Run("remote non-image without thumbnail stays nil", func(t *testing.T) {
		f := &model.DriveFile{
			Type: "application/zip", URL: "https://" + remoteHost + "/x.zip",
			UserHost: sp(remoteHost), IsLink: true,
		}
		if got := c.GetThumbnailURL(f); got != nil {
			t.Errorf("remote non-image should be nil thumb, got %v", *got)
		}
	})

	t.Run("video with remote stored thumbnail is proxied", func(t *testing.T) {
		f := &model.DriveFile{Type: "video/mp4", URL: "https://" + remoteHost + "/v.mp4",
			ThumbnailURL: sp("https://" + remoteHost + "/vt.png")}
		got := c.GetThumbnailURL(f)
		if got == nil || strings.Contains(u(t, *got).Host, remoteHost) {
			t.Errorf("video remote thumb not wrapped: %v", got)
		}
	})
	t.Run("video with local stored thumbnail stays raw", func(t *testing.T) {
		f := &model.DriveFile{Type: "video/mp4", URL: testInstanceURL + "/files/v.mp4",
			ThumbnailURL: sp(testInstanceURL + "/files/vt.png")}
		got := c.GetThumbnailURL(f)
		if got == nil || *got != *f.ThumbnailURL {
			t.Errorf("video local thumb changed: %v", got)
		}
	})
	t.Run("local video without thumbnail returns nil", func(t *testing.T) {
		f := &model.DriveFile{Type: "video/mp4", URL: testInstanceURL + "/files/v.mp4"}
		if got := c.GetThumbnailURL(f); got != nil {
			t.Errorf("local video should have nil thumb, got %v", *got)
		}
	})
}

// u parses s and fails the test on error.
func u(t *testing.T, s string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(s)
	if err != nil {
		t.Fatalf("unparseable URL %q: %v", s, err)
	}
	return parsed
}
