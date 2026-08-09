package server

import (
	"strings"

	"github.com/labstack/echo/v4"
)

// CSP modes accepted by the `frontendContentSecurityPolicy` config key.
const (
	// CSPModeOff sends no header. 既定。
	CSPModeOff = "off"
	// CSPModeReportOnly sends `Content-Security-Policy-Report-Only`, which
	// never blocks anything and only reports violations.
	CSPModeReportOnly = "report-only"
	// CSPModeEnforce sends `Content-Security-Policy`.
	CSPModeEnforce = "enforce"
)

// CSPReportPath is where browsers post violation reports.
//
// `/api` 配下に置かない。あちらは auth / body limit / rate limit の既定が
// API 用に組んであり、認証不要で外から叩かれるこの endpoint とは要件が違う。
const CSPReportPath = "/csp-report"

// frontendCSPDirectives is the policy applied to the SPA shell.
//
// **緩すぎると違反が出ず観測の意味が無い**ので、最終的に enforce したい形から
// 始めている。report-only の間に出た違反を潰してから enforce へ切り替える。
//
// `'unsafe-inline'` を script/style に入れているのは、SSR shell が inline script
// (`VERSION` / `CLIENT_ENTRY` の定義) と SVG の inline style 属性を持つため。
// **これらを nonce / hash へ移すのは別段階**で、先にそれ以外の違反を見たい。
// 最初から nonce 化すると、shell 由来の違反ばかりが出て他が埋もれる。
//
// `frame-ancestors` は**入れない**。`X-Frame-Options: DENY` を
// `middleware/frameguard.go` が既に付けており、そちらは `/embed/` を除外する
// 仕組みを持つ。CSP に重ねると除外を二重管理することになるので、embed の配線が
// 入るときに一緒に設計する。
var frontendCSPDirectives = []string{
	"default-src 'self'",
	"base-uri 'self'",
	"object-src 'none'",
	"form-action 'self'",
	"script-src 'self' 'unsafe-inline'",
	"style-src 'self' 'unsafe-inline'",
	// media は media proxy 経由で同一オリジンに来る。data: / blob: は
	// クライアント側でのプレビュー生成 (画像編集・録音等) が使う。
	"img-src 'self' data: blob:",
	"media-src 'self' data: blob:",
	"font-src 'self' data:",
	// streaming は同一オリジンの WebSocket。ws: も許すのは http 配信の
	// 開発環境で wss: にならないため。
	"connect-src 'self' ws: wss:",
	"worker-src 'self' blob:",
	"frame-src 'self'",
}

// buildFrontendCSP renders the policy string, appending the report directive
// when reporting is wanted.
//
// `report-uri` は deprecated だが、`report-to` だけにすると Reporting-Endpoints
// header を解さない環境で 1 件も届かない。観測が目的なので両方は出さず、対応
// 範囲の広い `report-uri` に寄せる。
func buildFrontendCSP(withReport bool) string {
	d := frontendCSPDirectives
	if withReport {
		d = append(append([]string{}, d...), "report-uri "+CSPReportPath)
	}
	return strings.Join(d, "; ")
}

// applyFrontendCSP sets the CSP header on an SPA shell response.
//
// mode が未知 / off なら何もしない。**判定できない値で enforce に倒さない**の
// が要点で、設定ミスでフロントが動かなくなるより無効の方が安全。
func applyFrontendCSP(c echo.Context, mode string) {
	switch mode {
	case CSPModeReportOnly:
		c.Response().Header().Set("Content-Security-Policy-Report-Only", buildFrontendCSP(true))
	case CSPModeEnforce:
		// enforce でも報告は受け取る。ブロックが起きていることに気付けないと
		// 「一部の機能だけ動かない」という報告しか上がってこない。
		c.Response().Header().Set("Content-Security-Policy", buildFrontendCSP(true))
	}
}
