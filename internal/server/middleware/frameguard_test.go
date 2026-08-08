package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
)

func TestFrameGuard(t *testing.T) {
	tests := []struct {
		name string
		path string
		want string
	}{
		{"frontend shell is not framable", "/", "DENY"},
		{"user page is not framable", "/@alice", "DENY"},
		{
			// 認可プロンプトを透明な iframe で重ねる攻撃を塞ぐ。ここが抜けると
			// クリックジャッキングの本命が空く。
			name: "oauth consent screen is not framable",
			path: "/oauth/authorize",
			want: "DENY",
		},
		{"api responses carry the header too", "/api/meta", "DENY"},
		{
			// iframe に埋め込まれること自体が目的の経路。upstream も embed
			// route では header を外している。
			name: "embed pages stay framable",
			path: "/embed/notes/abc",
			want: "",
		},
		{"drive files stay framable", "/files/abc.pdf", ""},
		{"media proxy stays framable", "/proxy/image.webp", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := echo.New()
			e.Use(FrameGuard())
			e.Any("/*", func(c echo.Context) error { return c.NoContent(http.StatusOK) })
			e.Any("/", func(c echo.Context) error { return c.NoContent(http.StatusOK) })

			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)

			assert.Equal(t, tt.want, rec.Header().Get("X-Frame-Options"),
				"path=%s", tt.path)
		})
	}
}

// 除外は prefix 一致なので、似た名前の別 path を巻き込まないことを固定する。
// `/embedded-thing` が除外されると、そこだけ守られない状態が静かに生まれる。
func TestFrameGuardSkipIsPrefixScoped(t *testing.T) {
	for _, p := range []string{"/embedded-thing", "/filestore", "/proxying"} {
		if frameGuardSkipped(p) {
			t.Errorf("frameGuardSkipped(%q) = true, want false", p)
		}
	}
}
