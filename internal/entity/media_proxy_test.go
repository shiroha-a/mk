package entity

import (
	"strings"
	"testing"
	"time"

	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
)

// withTestMediaProxy wires a recognizable media-proxy builder for the duration
// of the test and restores the disabled default afterwards.
func withTestMediaProxy(t *testing.T, proxyRemoteFiles, external bool) {
	t.Helper()
	SetMediaProxy(func(rawURL, mode string) string {
		name := mode
		if name == "" {
			name = "image"
		}
		return "https://mk.test/proxy/" + name + ".webp?url=" + rawURL + "&sig=SIG"
	}, func() bool { return proxyRemoteFiles }, external)
	t.Cleanup(func() { SetMediaProxy(nil, nil, false) })
}

func strp(s string) *string { return &s }

func TestPackDriveFile_RemoteIsProxied(t *testing.T) {
	withTestMediaProxy(t, true, false)
	idGen, _ := id.NewGenerator("aidx")
	ext := "https://remote.example/files/cat.png"
	f := &model.DriveFile{
		ID: idGen.Generate(time.Now()), Name: "cat.png", Type: "image/png",
		URL: ext, URI: strp(ext), UserHost: strp("remote.example"), IsLink: true,
	}
	got := PackDriveFile(f, idGen)
	if !strings.HasPrefix(got.URL, "https://mk.test/proxy/image.webp?url=") {
		t.Fatalf("url not proxied: %q", got.URL)
	}
	if strings.Contains(got.URL, "remote.example/files") == false {
		// the external target is embedded as the proxied ?url= param
		t.Fatalf("proxied url missing target: %q", got.URL)
	}
	if got.ThumbnailURL == nil || !strings.HasPrefix(*got.ThumbnailURL, "https://mk.test/proxy/static.webp?url=") {
		t.Fatalf("thumbnail not proxied (static): %v", got.ThumbnailURL)
	}
	// the raw external host must never appear as the *scheme+host* of the url
	// (only inside the ?url= param of the proxy URL).
	if strings.HasPrefix(got.URL, ext) {
		t.Fatalf("url leaks external host directly: %q", got.URL)
	}
}

func TestPackDriveFile_LocalUnchanged(t *testing.T) {
	withTestMediaProxy(t, true, false)
	idGen, _ := id.NewGenerator("aidx")
	local := "https://mk.test/files/abc"
	f := &model.DriveFile{
		ID: idGen.Generate(time.Now()), Name: "a.png", Type: "image/png",
		URL: local, // UserHost nil => local
	}
	got := PackDriveFile(f, idGen)
	if got.URL != local {
		t.Fatalf("local url should be unchanged, got %q", got.URL)
	}
}

func TestPackDriveFile_NoBuilderRawFallback(t *testing.T) {
	// builder unset (default) => raw URL even for remote (unit-test safe default)
	idGen, _ := id.NewGenerator("aidx")
	ext := "https://remote.example/files/cat.png"
	f := &model.DriveFile{
		ID: idGen.Generate(time.Now()), Name: "cat.png", Type: "image/png",
		URL: ext, URI: strp(ext), UserHost: strp("remote.example"), IsLink: true,
	}
	got := PackDriveFile(f, idGen)
	if got.URL != ext {
		t.Fatalf("without builder url should be raw, got %q", got.URL)
	}
}

func TestPackDriveFile_ProxyRemoteFilesDisabled(t *testing.T) {
	// proxyRemoteFiles=false and no external proxy => admin opted out, raw URL.
	withTestMediaProxy(t, false, false)
	idGen, _ := id.NewGenerator("aidx")
	ext := "https://remote.example/files/cat.png"
	f := &model.DriveFile{
		ID: idGen.Generate(time.Now()), Name: "cat.png", Type: "image/png",
		URL: ext, URI: strp(ext), UserHost: strp("remote.example"), IsLink: true,
	}
	got := PackDriveFile(f, idGen)
	if got.URL != ext {
		t.Fatalf("with proxyRemoteFiles off url should be raw, got %q", got.URL)
	}
}

func TestPackDriveFile_ExternalProxyForcesProxyEvenIfRemoteFilesOff(t *testing.T) {
	// externalMediaProxyEnabled=true forces proxying regardless of proxyRemoteFiles.
	withTestMediaProxy(t, false, true)
	idGen, _ := id.NewGenerator("aidx")
	ext := "https://remote.example/files/cat.png"
	f := &model.DriveFile{
		ID: idGen.Generate(time.Now()), Name: "cat.png", Type: "image/png",
		URL: ext, URI: strp(ext), UserHost: strp("remote.example"), IsLink: true,
	}
	got := PackDriveFile(f, idGen)
	if !strings.HasPrefix(got.URL, "https://mk.test/proxy/image.webp?url=") {
		t.Fatalf("external proxy should force proxying, got %q", got.URL)
	}
}

func TestPackDriveFile_RemoteWebpublicProxied(t *testing.T) {
	withTestMediaProxy(t, true, false)
	idGen, _ := id.NewGenerator("aidx")
	ext := "https://remote.example/files/cat.png"
	wp := "https://remote.example/files/cat.webp"
	f := &model.DriveFile{
		ID: idGen.Generate(time.Now()), Name: "cat.png", Type: "image/png",
		URL: ext, URI: strp(ext), WebpublicURL: strp(wp),
		UserHost: strp("remote.example"), IsLink: true,
	}
	got := PackDriveFile(f, idGen)
	if got.WebpublicURL == nil || !strings.HasPrefix(*got.WebpublicURL, "https://mk.test/proxy/image.webp?url=") {
		t.Fatalf("remote webpublicUrl not proxied: %v", got.WebpublicURL)
	}
}

// A remote user whose avatar URL is somehow non-external (relative/local) must
// be left untouched (proxyMediaURL only rewrites absolute http(s) URLs).
func TestIdenticonURL_RemoteNonExternalAvatarUnchanged(t *testing.T) {
	withTestMediaProxy(t, true, false)
	u := &model.User{ID: "u1", Username: "bob", Host: strp("remote.example"), AvatarURL: strp("/files/local-av.png")}
	got := IdenticonURL(u)
	if got != "/files/local-av.png" {
		t.Fatalf("non-external avatar should be unchanged, got %q", got)
	}
}

func TestIdenticonURL_RemoteAvatarProxied(t *testing.T) {
	withTestMediaProxy(t, true, false)
	u := &model.User{ID: "u1", Username: "bob", Host: strp("remote.example"), AvatarURL: strp("https://remote.example/avatar.png")}
	got := IdenticonURL(u)
	if !strings.HasPrefix(got, "https://mk.test/proxy/avatar.webp?url=") {
		t.Fatalf("remote avatar not proxied (avatar mode): %q", got)
	}
}

func TestIdenticonURL_LocalAvatarUnchanged(t *testing.T) {
	withTestMediaProxy(t, true, false)
	u := &model.User{ID: "u1", Username: "alice", AvatarURL: strp("https://mk.test/files/av.png")}
	got := IdenticonURL(u)
	if got != "https://mk.test/files/av.png" {
		t.Fatalf("local avatar should be unchanged, got %q", got)
	}
}

func TestIdenticonURL_RemoteAvatarAlwaysProxiedEvenIfRemoteFilesOff(t *testing.T) {
	// avatar mode is always proxied (upstream getPublicUrl avatar branch),
	// independent of proxyRemoteFiles.
	withTestMediaProxy(t, false, false)
	u := &model.User{ID: "u1", Username: "bob", Host: strp("remote.example"), AvatarURL: strp("https://remote.example/avatar.png")}
	got := IdenticonURL(u)
	if !strings.HasPrefix(got, "https://mk.test/proxy/avatar.webp?url=") {
		t.Fatalf("remote avatar should always be proxied, got %q", got)
	}
}

func TestIdenticonURL_NoAvatarFallsBackToIdenticon(t *testing.T) {
	withTestMediaProxy(t, true, false)
	u := &model.User{ID: "u1", Username: "bob", Host: strp("remote.example")}
	got := IdenticonURL(u)
	if got != "/identicon/bob@remote.example" {
		t.Fatalf("expected identicon fallback, got %q", got)
	}
}

func TestPackUserDetailed_RemoteBannerProxied(t *testing.T) {
	withTestMediaProxy(t, true, false)
	u := &model.User{ID: "u1", Username: "bob", Host: strp("remote.example"), BannerURL: strp("https://remote.example/banner.png")}
	d := PackUserDetailed(u, nil)
	if d.BannerURL == nil || !strings.HasPrefix(*d.BannerURL, "https://mk.test/proxy/image.webp?url=") {
		t.Fatalf("remote banner not proxied: %v", d.BannerURL)
	}
}

func TestPackEmojiDetailed_RemoteProxied(t *testing.T) {
	withTestMediaProxy(t, true, false)
	e := &model.Emoji{ID: "e1", Name: "wave", Host: strp("remote.example"), PublicURL: "https://remote.example/e/wave.png"}
	got := PackEmojiDetailed(e)
	if !strings.HasPrefix(got.URL, "https://mk.test/proxy/image.webp?url=") {
		t.Fatalf("remote emoji detailed not proxied: %q", got.URL)
	}
}

func TestPackEmojiDetailed_LocalRaw(t *testing.T) {
	withTestMediaProxy(t, true, false)
	e := &model.Emoji{ID: "e1", Name: "local", PublicURL: "https://mk.test/e/local.png"}
	got := PackEmojiDetailed(e)
	if got.URL != "https://mk.test/e/local.png" {
		t.Fatalf("local emoji detailed should be raw: %q", got.URL)
	}
}

func TestEmojiResolver_RemoteEmojiProxied(t *testing.T) {
	withTestMediaProxy(t, true, false)
	lookup := &stubEmojiLookup{data: map[string][]*model.Emoji{
		"remote.example": {{ID: "e1", Name: "wave", Host: strp("remote.example"), PublicURL: "https://remote.example/e/wave.png"}},
	}}
	note := &model.Note{ID: "n1", UserHost: strp("remote.example"), Emojis: []string{"wave"}}
	r := NewEmojiResolver(lookup, []*model.Note{note})
	ent := &NoteEntity{}
	r.PopulateNoteEmojis(note, ent)
	if !strings.HasPrefix(ent.Emojis["wave"], "https://mk.test/proxy/image.webp?url=") {
		t.Fatalf("remote emoji in note map not proxied: %q", ent.Emojis["wave"])
	}
}

func TestEmojiResolver_LocalEmojiRaw(t *testing.T) {
	withTestMediaProxy(t, true, false)
	lookup := &stubEmojiLookup{data: map[string][]*model.Emoji{
		"": {{ID: "e1", Name: "local", PublicURL: "https://mk.test/e/local.png"}},
	}}
	note := &model.Note{ID: "n1", Emojis: []string{"local"}} // UserHost nil => local
	r := NewEmojiResolver(lookup, []*model.Note{note})
	ent := &NoteEntity{}
	r.PopulateNoteEmojis(note, ent)
	if ent.Emojis["local"] != "https://mk.test/e/local.png" {
		t.Fatalf("local emoji in note map should be raw: %q", ent.Emojis["local"])
	}
}

func TestInstanceLiteFromModel_RemoteIconProxied(t *testing.T) {
	withTestMediaProxy(t, true, false)
	inst := &model.Instance{Host: "remote.example", IconURL: strp("https://remote.example/icon.png"), FaviconURL: strp("https://remote.example/fav.ico")}
	lite := instanceLiteFromModel(inst)
	if lite.IconURL == nil || !strings.HasPrefix(*lite.IconURL, "https://mk.test/proxy/image.webp?url=") {
		t.Fatalf("instance icon not proxied: %v", lite.IconURL)
	}
	if lite.FaviconURL == nil || !strings.HasPrefix(*lite.FaviconURL, "https://mk.test/proxy/image.webp?url=") {
		t.Fatalf("instance favicon not proxied: %v", lite.FaviconURL)
	}
}
