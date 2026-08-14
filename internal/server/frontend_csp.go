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
	// **既知の非対応 (#2502): captcha を有効にした構成では enforce にできない。**
	// MkCaptcha は hCaptcha / reCAPTCHA / Turnstile の script を各社の origin から
	// 動的に挿入するため、script-src (Turnstile は connect-src も) で先に落ちて
	// サインアップが壊れる。対応するときは meta の enable フラグに連動した
	// 条件付き追加にする (使っていない captcha 業者を常時許可しない)。
	//
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
	// 画像は media proxy 経由で来る。**internal proxy なら同一オリジン**で、
	// 外部 media proxy 構成ではその origin を extraMediaOrigins で足す (#2501)。
	// data: / blob: はクライアント側でのプレビュー生成 (画像編集・録音等) が使う。
	"img-src 'self' data: blob:",
	// **動画・音声は img と違いリモート origin を直接読む** (#2501)。media proxy
	// が扱うのは画像だけで、リンク参照のリモート添付は MkMediaVideo が
	// `<source :src="file.url">` で相手サーバーから直接取得する。'self' に絞ると
	// リモートの動画・音声が全滅する (実際に本番で全滅していた)。upstream は
	// CSP 自体を設定せず全 origin を許すので、https: を許すのが parity。
	// http: は mixed content でどのみちブロックされるため足さない。
	"media-src 'self' https: data: blob:",
	"font-src 'self' data:",
	// streaming は同一オリジンの WebSocket。ws: も許すのは http 配信の
	// 開発環境で wss: にならないため。esm.sh は script-src で既に信頼している
	// CDN で、実測で .map (source map) 取得の connect-src 違反 report が届いて
	// 観測を汚すため明示する (#2501。取得元はブラウザや拡張に依存し、信頼先は
	// 増えない)。
	"connect-src 'self' ws: wss: https://esm.sh",
	"worker-src 'self' blob:",
	// URL プレビューの埋め込みプレイヤー (MkUrlPreview の player.url iframe) は
	// YouTube 等の任意 origin を指す (#2501)。iframe 側の sandbox 属性で制約
	// されており (upstream 設計)、'self' に絞るとプレイヤーが全滅する。
	"frame-src 'self' https:",
}

// mediaDirectives are the directives that must also allow the extra origins
// (object storage / 外部 media proxy)。drive のファイルはそこから直接配信される。
//
// **connect-src にも足す** (#2501)。通知音にドライブのファイルを設定すると、
// クライアントは `<audio>` ではなく fetch + decodeAudioData で読む (sound.ts)。
// fetch は media-src でなく connect-src の支配下なので、ここに無いと object
// storage 構成で「通知音だけ鳴らない」という気付きにくい壊れ方をする。
var mediaDirectives = []string{"img-src", "media-src", "connect-src"}

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
