package server

import (
	"net/url"
	"slices"
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
	// esm.sh は Misskey frontend が shiki (コードブロックの syntax highlight) を
	// 動的 import する先。**upstream の vite 設定がそう作っている**
	// (`externalPackages` で `shiki/langs` `shiki/themes` を CDN に逃がす)。
	//
	// バンドルに切り替えれば外部依存を消せるが、**実測でビルド成果物が
	// 242MB → 508MB に倍増した** (2026-08-09)。Misskey frontend は 30 ロケール
	// 分を個別にビルドするため、shiki の言語 347 + テーマ 66 ファイルが
	// ロケールごとに複製され、JS が 13,576 → 23,610 ファイルに増える。
	// 利用者 1 人あたりの転送量は変わらない (自分のロケール分しか取らない) が、
	// 配布物とディスクが倍になる。
	//
	// 軽量さは mk-go の主要な利点なので、それを損なうより CDN を 1 つ許す方を
	// 選んだ。代償として、コードブロックを表示する閲覧者の IP とリファラが
	// esm.sh に渡り、CDN 側の可用性と完全性に依存する。
	//
	// 言語を絞ってバンドルすれば両立できる可能性はある (未検証)。
	"script-src 'self' 'unsafe-inline' https://esm.sh",
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

// mediaDirectives are the directives that must also allow the object storage
// origin. drive のファイルはそこから直接配信されるため。
var mediaDirectives = []string{"img-src", "media-src"}

// buildFrontendCSP renders the policy string.
//
// extraMediaOrigins には object storage の origin 等、`'self'` 以外に画像 /
// メディアの取得を許す必要がある host を渡す。**空でない限りポリシーは
// operator の設定に依存する**ので、静的な定数にはできない。
//
// `report-uri` は deprecated だが、`report-to` だけにすると Reporting-Endpoints
// header を解さない環境で 1 件も届かない。観測が目的なので両方は出さず、対応
// 範囲の広い `report-uri` に寄せる。
func buildFrontendCSP(withReport bool, extraMediaOrigins ...string) string {
	d := make([]string, 0, len(frontendCSPDirectives)+1)
	for _, dir := range frontendCSPDirectives {
		d = append(d, appendMediaOrigins(dir, extraMediaOrigins))
	}
	if withReport {
		d = append(d, "report-uri "+CSPReportPath)
	}
	return strings.Join(d, "; ")
}

// appendMediaOrigins adds the extra origins to img-src / media-src.
func appendMediaOrigins(directive string, origins []string) string {
	if len(origins) == 0 {
		return directive
	}
	name, _, ok := strings.Cut(directive, " ")
	if !ok {
		return directive
	}
	if !slices.Contains(mediaDirectives, name) {
		return directive
	}
	return directive + " " + strings.Join(origins, " ")
}

// objectStorageOrigin extracts the scheme+host to allow from a configured
// object storage base URL.
//
// **path は落として origin だけにする。** CSP の source expression は path を
// 書けるが、`https://host/bucket` のように書くと「その prefix 配下だけ」を意味し、
// 実際の配信 URL がわずかに違う形 (末尾スラッシュの有無など) で外れる。origin
// 単位で許す方が確実で、object storage は専用ホストなので粒度としても妥当。
//
// 解釈できない値では "" を返す。**ゴミを CSP に混ぜない**のが要点で、不正な
// source expression が 1 つでもあるとブラウザがその directive ごと落とす実装が
// ある。
func objectStorageOrigin(baseURL string) string {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return ""
	}
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" {
		return ""
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// applyFrontendCSP sets the CSP header on an SPA shell response.
//
// mode が未知 / off なら何もしない。**判定できない値で enforce に倒さない**の
// が要点で、設定ミスでフロントが動かなくなるより無効の方が安全。
func applyFrontendCSP(c echo.Context, mode string, extraMediaOrigins ...string) {
	switch mode {
	case CSPModeReportOnly:
		c.Response().Header().Set("Content-Security-Policy-Report-Only",
			buildFrontendCSP(true, extraMediaOrigins...))
	case CSPModeEnforce:
		// enforce でも報告は受け取る。ブロックが起きていることに気付けないと
		// 「一部の機能だけ動かない」という報告しか上がってこない。
		c.Response().Header().Set("Content-Security-Policy",
			buildFrontendCSP(true, extraMediaOrigins...))
	}
}
