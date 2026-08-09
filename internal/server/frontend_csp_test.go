package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func cspHeadersFor(t *testing.T, mode string) http.Header {
	t.Helper()
	e := echo.New()
	rec := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest(http.MethodGet, "/", nil), rec)
	applyFrontendCSP(c, mode)
	return rec.Header()
}

// mode ごとに付く header が変わる。未知の値では**何も付けない**のが要点で、
// 設定ミスで enforce に倒れてフロントが動かなくなるより無効の方が安全。
func TestApplyFrontendCSP_HeaderPerMode(t *testing.T) {
	cases := []struct {
		mode       string
		wantHeader string
	}{
		{CSPModeOff, ""},
		{CSPModeReportOnly, "Content-Security-Policy-Report-Only"},
		{CSPModeEnforce, "Content-Security-Policy"},
		{"", ""},
		{"typo", ""},
		{"Report-Only", ""}, // 大文字小文字は揃えない = 未知扱い
	}
	for _, tc := range cases {
		t.Run(tc.mode, func(t *testing.T) {
			h := cspHeadersFor(t, tc.mode)
			if tc.wantHeader == "" {
				assert.Empty(t, h.Get("Content-Security-Policy"))
				assert.Empty(t, h.Get("Content-Security-Policy-Report-Only"))
				return
			}
			assert.NotEmpty(t, h.Get(tc.wantHeader))
			// 片方だけが付く (両方付けるとブラウザが両方評価してしまう)。
			other := "Content-Security-Policy"
			if tc.wantHeader == other {
				other = "Content-Security-Policy-Report-Only"
			}
			assert.Empty(t, h.Get(other))
		})
	}
}

// report-only / enforce のどちらでも report-uri を出す。enforce でブロックが
// 起きていることに気付けないと、「一部の機能だけ動かない」という報告しか
// 上がってこない。
func TestApplyFrontendCSP_AlwaysReports(t *testing.T) {
	for _, mode := range []string{CSPModeReportOnly, CSPModeEnforce} {
		h := cspHeadersFor(t, mode)
		v := h.Get("Content-Security-Policy") + h.Get("Content-Security-Policy-Report-Only")
		assert.Containsf(t, v, "report-uri "+CSPReportPath, "mode=%s", mode)
	}
}

// ポリシーの中身を固定する。**緩める変更は意図的にしか起こしてはいけない**ので、
// 主要 directive の存在と危険な緩和が無いことを assert する。
func TestFrontendCSP_Policy(t *testing.T) {
	policy := buildFrontendCSP(false)

	t.Run("必要な directive が揃っている", func(t *testing.T) {
		for _, d := range []string{
			"default-src 'self'",
			"base-uri 'self'",
			"object-src 'none'",
			"form-action 'self'",
			"script-src", "style-src", "img-src", "media-src",
			"font-src", "connect-src", "worker-src", "frame-src",
		} {
			assert.Containsf(t, policy, d, "missing %q", d)
		}
	})

	t.Run("危険な緩和を含まない", func(t *testing.T) {
		// script-src に 'unsafe-eval' を入れると CSP の意味がほぼ無くなる。
		assert.NotContains(t, policy, "unsafe-eval")
		// ワイルドカード許可はしない ('self' で足りる)。
		assert.NotContains(t, policy, "default-src *")
		assert.NotContains(t, policy, "script-src *")
	})

	t.Run("frame-ancestors を含まない", func(t *testing.T) {
		// X-Frame-Options (frameguard.go) が /embed/ を除外する仕組みを持って
		// いるので、CSP に重ねると除外を二重管理することになる。embed の配線が
		// 入るときに一緒に設計する。
		assert.NotContains(t, policy, "frame-ancestors")
	})

	t.Run("report 無しなら report-uri を含まない", func(t *testing.T) {
		assert.NotContains(t, policy, "report-uri")
	})
}

// 現段階では SSR shell の inline script / SVG の inline style 属性が残っている
// ため 'unsafe-inline' が要る。**nonce / hash 化は別段階**で、先にそれ以外の
// 違反を観測したい。ここを外すのは shell を書き換えてからにする。
func TestFrontendCSP_InlineStillAllowed(t *testing.T) {
	policy := buildFrontendCSP(false)
	for _, d := range strings.Split(policy, "; ") {
		switch {
		case strings.HasPrefix(d, "script-src"), strings.HasPrefix(d, "style-src"):
			assert.Containsf(t, d, "'unsafe-inline'",
				"%s: SSR shell の inline を許すまで外せない", d)
		}
	}
}

func postCSPReport(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, CSPReportPath, strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, "application/csp-report")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	require.NoError(t, cspReportHandler()(c))
	return rec
}

// 何を投げても 204。内容が妥当だったかを攻撃者に返さない。
func TestCSPReportHandler_AlwaysNoContent(t *testing.T) {
	cases := map[string]string{
		"正規の report":    `{"csp-report":{"document-uri":"https://x.example/","violated-directive":"script-src","blocked-uri":"https://evil.example/x.js"}}`,
		"空 body":        ``,
		"壊れた JSON":      `{not json`,
		"csp-report 無し": `{"foo":"bar"}`,
		"配列":            `[1,2,3]`,
		"巨大 body":       `{"csp-report":{"document-uri":"` + strings.Repeat("A", cspReportMaxBody) + `"}}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			rec := postCSPReport(t, body)
			assert.Equal(t, http.StatusNoContent, rec.Code)
			assert.Empty(t, rec.Body.String())
		})
	}
}

// effective-directive しか無い形式 (CSP Level 3 寄りの実装) でも読める。
func TestCSPReportHandler_EffectiveDirectiveFallback(t *testing.T) {
	rec := postCSPReport(t, `{"csp-report":{"effective-directive":"img-src","blocked-uri":"data:"}}`)
	assert.Equal(t, http.StatusNoContent, rec.Code)
}
