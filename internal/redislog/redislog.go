// Package redislog routes go-redis' internal messages into slog.
//
// **既定では slog を通らない。** go-redis は
// `log.New(os.Stderr, "redis: ", log.LstdFlags|log.Lshortfile)` に直接書くので、
// 構造化ログにも収集基盤にも乗らない。#2659 の障害でこの行が
//
//	redis: 2026/08/20 18:42:01 pool.go:617: redis: connection pool: failed to dial
//	  after 5 attempts: dial unix /run/valkey/valkey.sock: connect: resource
//	  temporarily unavailable
//
// prefix と file:line の間に日時が挟まる (LstdFlags) ので、`redis: pool.go:` の
// ような literal では grep できない。
//
// という形だったのはそのため。接続が張れない状態は運用者が最初に知るべき部類
// なので、他のログと同じ経路に乗せる。
package redislog

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/redis/go-redis/v9"
	"github.com/redis/go-redis/v9/logging"
)

// Logger adapts go-redis' internal logging interface to slog.
//
// WARN で出す。go-redis が internal logger に出すのは接続の再試行 / 切断 /
// push 通知の失敗といった「異常だが致命的とは限らない」ものが中心で、
// INFO だと通常運転のログに埋もれる。
type Logger struct{}

// Printf implements go-redis' internal logging interface.
//
// 整形済みの文字列がそのまま msg に入る。go-redis 側の interface が Printf
// なので分解できず、**msg は接続 ID や件数を含む高カーディナリティな値になる**。
// 監視で拾うときは msg の完全一致ではなく source=go-redis で絞ること。
func (Logger) Printf(ctx context.Context, format string, v ...any) {
	slog.WarnContext(ctx, fmt.Sprintf(format, v...), "source", "go-redis")
}

// redisLogger mirrors go-redis' internal logging interface. go-redis takes an
// unexported type, so we cannot name it in a variable or a test double.
type redisLogger interface {
	Printf(ctx context.Context, format string, v ...any)
}

// setLogger installs a logger into go-redis, indirected so tests can assert
// what gets installed. go-redis offers no way to read the current logger back,
// so this is the only seam available.
var setLogger = func(l redisLogger) { redis.SetLogger(l) }

// UseSlog routes go-redis' internal logger into slog.
//
// **プロセス全体に効く** (go-redis 側が package 変数を持っている)。自前の
// 出力を組み立てるサブコマンドは UseSilent を先に呼ぶこと。
func UseSlog() { setLogger(Logger{}) }

// UseSilent discards go-redis' internal logger output. Used by short-lived
// sub-commands that render their own output (doctor).
//
// go-redis の VoidLogger をそのまま使う。同じものを自前で定義しても動くが、
// 破棄用の型は上流が持っているので二重に持たない。`logging.Disable()` でも
// 同じ結果になるものの、そちらは setLogger の seam を通らずテストできない。
func UseSilent() { setLogger(&logging.VoidLogger{}) }
