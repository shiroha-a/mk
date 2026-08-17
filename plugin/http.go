package plugin

import (
	"context"
	"fmt"
	"net/http"
)

// Router registers a plugin's HTTP endpoints.
//
// パスは `/api/plugin/<プラグイン名>/` の下に配置される。Misskey 本体の
// エンドポイント空間とは**必ず分離する**: 将来 upstream が同名のエンドポイントを
// 追加したときに衝突すると、API 互換 (本プロジェクトの最優先方針) が壊れる。
//
// 本体が用意するのは認証 (Request.UserID / IsModerator / IsAdministrator) と
// body の上限 (1 MiB) まで。**次の 2 つはプラグイン側の責任になる**:
//
//   - 権限。Router は認証の有無しか見ない。管理用のルートを張っただけでは
//     誰でも叩けるので、ハンドラの先頭で IsModerator などを確認すること。
//   - レート制限。本体の per-endpoint テーブルはプラグインのパスを持たない
//     ので、**登録したルートには上限が掛からない**。重い処理や外部への
//     問い合わせを含むなら、自前で間隔を空けるか結果を持つこと。
type Router interface {
	// GET registers a handler. path is relative to the plugin's namespace and
	// must start with "/".
	GET(path string, h Handler)

	// POST registers a handler. Misskey 本体の API は POST が基本なので、
	// プラグインもそれに倣うと利用側 (misskey-js 等) から扱いやすい。
	POST(path string, h Handler)
}

// Handler serves one plugin request. A nil result with a nil error yields
// 204 No Content; a [Blob] result is written as-is; anything else is
// JSON-encoded with 200.
type Handler func(Request) (any, error)

// Blob is a raw response body, for endpoints that do not return JSON.
//
// 主な用途は画像のプロキシ。本体の CSP は `img-src 'self' data: blob:` なので、
// **外部の画像を <img> で直接読めない**。プラグインが同一オリジンで配信すれば
// CSP を緩めずに済み、取得元にも優しい (キャッシュできる)。
//
//	return plugin.Blob{
//	    ContentType:  "image/png",
//	    Body:         data,
//	    CacheControl: "public, max-age=86400",
//	}, nil
//
// mk-go は `X-Content-Type-Options: nosniff` を必ず付ける。外部から取得した
// ものをそのまま流す場合、ブラウザの MIME 推測で意図しない解釈をされるのを
// 防ぐため。**ContentType は取得元の値をそのまま使わず、扱う型を決めて
// 検証すること。**
//
// 応答の大きさは mk-go では制限しない。取得元からの読み込みは
// `io.LimitReader` などでプラグイン側が必ず上限を設けること。
type Blob struct {
	// ContentType is sent as-is. 空なら application/octet-stream。
	ContentType string

	// Body is the response body.
	Body []byte

	// CacheControl sets the header when non-empty.
	CacheControl string
}

// Request is the plugin-facing view of an HTTP request.
//
// **echo.Context をそのまま渡さない。** Echo は mk-go 内部の選択であり、
// 差し替えたときにプラグインが全滅する。ここで必要最小限だけを写す。
type Request interface {
	// Context returns the request context, already carrying cancellation.
	Context() context.Context

	// Bind decodes the JSON request body into v.
	Bind(v any) error

	// Param returns a path parameter (e.g. ":id").
	Param(name string) string

	// Query returns a query-string value.
	Query(name string) string

	// UserID returns the authenticated user's ID, or "" when the request was
	// not authenticated.
	//
	// ID だけを渡す。model.User を公開すると内部のモデルが契約になってしまう
	// ので、それ以上が要るなら [API] で取得する (可視性判定も自動で効く)。
	UserID() string

	// IsModerator reports whether the caller is a moderator (administrators
	// included).
	//
	// **管理用のルートは必ずこれで守ること。** [Router] は認証の有無しか見ない
	// ので、管理画面を出しただけでは API は誰でも叩ける。画面を隠すのは UI の
	// 都合であって、権限の強制ではない。
	IsModerator() bool

	// IsAdministrator reports whether the caller is an administrator.
	IsAdministrator() bool
}

// StatusError lets a handler choose the HTTP status of an error response.
// Plain errors become 500 with a generic message.
type StatusError struct {
	Status  int
	Message string
}

// Error implements error.
func (e *StatusError) Error() string {
	return fmt.Sprintf("%d: %s", e.Status, e.Message)
}

// Errorf builds a StatusError.
func Errorf(status int, format string, args ...any) *StatusError {
	return &StatusError{Status: status, Message: fmt.Sprintf(format, args...)}
}

// ErrNotFound is a convenience for the most common plugin error.
func ErrNotFound(format string, args ...any) *StatusError {
	return Errorf(http.StatusNotFound, format, args...)
}
