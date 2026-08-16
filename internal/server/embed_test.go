package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/config"
	"github.com/shiroha-a/mk/internal/frontendutil"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func strptr(s string) *string { return &s }

// 埋め込みは認証を伴わない経路なので、ここが緩むとそのまま IDOR になる。
// upstream ClientServerService の `/embed/notes/:note` と同じ判定を固定する。
func TestEmbedNoteIsPublic(t *testing.T) {
	tests := []struct {
		name string
		note *model.Note
		want bool
	}{
		{
			name: "public local note is embeddable",
			note: &model.Note{Visibility: "public"},
			want: true,
		},
		{
			name: "home visibility is embeddable",
			note: &model.Note{Visibility: "home"},
			want: true,
		},
		{
			// 宛先を指定した投稿。埋め込むと宛先以外の誰でも読める。
			name: "specified visibility is never embeddable",
			note: &model.Note{Visibility: "specified"},
			want: false,
		},
		{
			// フォロワー限定。同上。
			name: "followers visibility is never embeddable",
			note: &model.Note{Visibility: "followers"},
			want: false,
		},
		{
			// リモートノートは自分が権威でないので配らない (upstream と同じ)。
			name: "remote note is not embeddable",
			note: &model.Note{Visibility: "public", UserHost: strptr("remote.example")},
			want: false,
		},
		{
			// host が空文字列はローカル扱い。DB 上 NULL でなく "" が入る経路への保険。
			name: "empty host counts as local",
			note: &model.Note{Visibility: "public", UserHost: strptr("")},
			want: true,
		},
		{
			name: "remote followers note is not embeddable",
			note: &model.Note{Visibility: "followers", UserHost: strptr("remote.example")},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := embedNoteIsPublic(tt.note); got != tt.want {
				t.Errorf("embedNoteIsPublic(%+v) = %v, want %v", tt.note, got, tt.want)
			}
		})
	}
}

// JSON を <script> に埋める以上、閉じタグの早期終了は必ず潰す。ノート本文も
// ユーザー名も攻撃者が自由に書けるので、ここが抜けると XSS になる。
func TestEscapeJSONForScript(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "closing script tag is neutralised",
			input: `{"text":"</script><img src=x onerror=alert(1)>"}`,
			want:  `{"text":"\u003c/script\u003e\u003cimg src=x onerror=alert(1)\u003e"}`,
		},
		{
			name:  "ampersand is escaped",
			input: `{"text":"a&b"}`,
			want:  `{"text":"a\u0026b"}`,
		},
		{
			name:  "plain json is unchanged",
			input: `{"a":1}`,
			want:  `{"a":1}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := escapeJSONForScript(tt.input); got != tt.want {
				t.Errorf("escapeJSONForScript(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// 攻撃者が制御できる文字列がどう来ても、script 要素を閉じる並びは残らない。
func TestEscapeJSONForScriptNeverLeavesClosingTag(t *testing.T) {
	payloads := []string{
		`{"text":"</script>"}`,
		`{"text":"</SCRIPT>"}`,
		`{"text":"</script >"}`,
		`{"name":"<!--</script>"}`,
	}
	for _, p := range payloads {
		got := escapeJSONForScript(p)
		if strings.Contains(got, "<") || strings.Contains(got, ">") {
			t.Errorf("escapeJSONForScript(%q) left raw angle brackets: %q", p, got)
		}
	}
}

// embed shell の `<head>` が upstream `views/base-embed.tsx` と同じ icon link を
// 出すこと。SPA shell と同じ fallback (#2527)。
func TestEmbedShell_IconLinks(t *testing.T) {
	cfg := &config.Config{URL: "https://example.test", Version: "0.0.1-test"}

	render := func(t *testing.T, m *model.Meta) string {
		t.Helper()
		repo := testutil.NewMockMetaRepository()
		if m != nil {
			repo.Meta = m
		}
		h := &embedHandlers{cfg: cfg, metaRepo: repo}

		e := echo.New()
		rec := httptest.NewRecorder()
		c := e.NewContext(httptest.NewRequest(http.MethodGet, "/embed/notes/x", nil), rec)
		require.NoError(t, h.render(c, nil))
		return rec.Body.String()
	}

	t.Run("defaults match upstream base-embed.tsx", func(t *testing.T) {
		body := render(t, nil)

		assert.Contains(t, body, `<link rel="icon" href="/favicon.ico">`)
		assert.Contains(t, body, `<link rel="apple-touch-icon" href="/apple-touch-icon.png">`)
	})

	t.Run("reflects meta icon urls", func(t *testing.T) {
		icon := "https://example.test/files/server-icon.png"
		appIcon := "https://example.test/files/app-512.png"
		body := render(t, &model.Meta{ID: "x", IconURL: &icon, App512IconURL: &appIcon})

		assert.Contains(t, body, `<link rel="icon" href="`+icon+`">`)
		assert.Contains(t, body, `<link rel="apple-touch-icon" href="`+appIcon+`">`)
	})
}

// embed も SPA シェルと同じく loader を埋め込む (#2551)。同じファイル名 (ハッシュ
// 無し) を参照していたので、同じ形で古い版が居座る。
func TestEmbedShell_InlinesLoaderAssets(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "loader"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "loader", "style.css"),
		[]byte("#embed{--marker:inlined-embed-css}"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "loader", "boot.js"),
		[]byte("window.__markerEmbedJs = 1;"), 0o644))
	t.Setenv("MISSKEY_FRONTEND_EMBED_DIR", dir)
	frontendutil.ResetLoaderCacheForTest()

	cfg := &config.Config{URL: "https://example.test", Version: "0.0.1-test"}
	h := &embedHandlers{cfg: cfg, metaRepo: testutil.NewMockMetaRepository()}

	e := echo.New()
	rec := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest(http.MethodGet, "/embed/notes/x", nil), rec)
	require.NoError(t, h.render(c, nil))

	body := rec.Body.String()
	assert.Contains(t, body, "#embed{--marker:inlined-embed-css}")
	assert.Contains(t, body, "window.__markerEmbedJs = 1;")
	assert.NotContains(t, body, `href="/embed_vite/loader/style.css"`)
	assert.NotContains(t, body, `src="/embed_vite/loader/boot.js"`)
}

func TestEmbedShell_FallsBackToLoaderReferences(t *testing.T) {
	t.Setenv("MISSKEY_FRONTEND_EMBED_DIR", t.TempDir())
	frontendutil.ResetLoaderCacheForTest()

	cfg := &config.Config{URL: "https://example.test", Version: "0.0.1-test"}
	h := &embedHandlers{cfg: cfg, metaRepo: testutil.NewMockMetaRepository()}

	e := echo.New()
	rec := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest(http.MethodGet, "/embed/notes/x", nil), rec)
	require.NoError(t, h.render(c, nil))

	body := rec.Body.String()
	assert.Contains(t, body, `<link rel="stylesheet" href="/embed_vite/loader/style.css">`)
	assert.Contains(t, body, `<script src="/embed_vite/loader/boot.js"></script>`)
}
