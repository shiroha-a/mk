package middleware

import (
	"context"
	"net/http"
)

// internalCallKey marks a request that did not arrive over the network.
type internalCallKey struct{}

// MarkInternalCall returns a request tagged as originating inside the process.
//
// **プラグインの in-process API 呼び出しで使う。** `pluginCaller.Call` は
// `echo.ServeHTTP` へ直接投げるので、本物のリクエストと同じ middleware chain を
// 通る (これは設計上の意図)。ただし `RemoteAddr` は実在しないため
// `127.0.0.1:0` を置いており、そのままだと
//
//   - `RecordClientIP` が利用者の `user_ip` に `127.0.0.1` を書き込む。
//     `admin/ip/related-accounts` はその共有だけを根拠に候補を出すので、
//     そのプラグインを使った全員が互いの関連候補として最上位に並ぶ
//     (観測時刻が常に「今」なので時間減衰の重みが最大になる)
//   - レート制限の IP バケットが `ip-hash(127.0.0.1)` で全利用者ぶん共有される
//
// という 2 つの副作用が出る。印を付けて両方から外す。
func MarkInternalCall(req *http.Request) *http.Request {
	return req.WithContext(context.WithValue(req.Context(), internalCallKey{}, true))
}

// IsInternalCall reports whether ctx belongs to an in-process API call.
func IsInternalCall(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	v, _ := ctx.Value(internalCallKey{}).(bool)
	return v
}
