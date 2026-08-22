package redislog

import (
	"bytes"
	"context"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/redis/go-redis/v9/logging"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// captureWarn swaps in a slog handler at the given level and returns the buffer.
func captureWarn(t *testing.T, level slog.Level) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: level})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// go-redis の内部ログが slog に届くこと。**既定では stderr へ直接書かれて
// 構造化ログに出ない**ので、#2659 の障害では接続失敗が mk-go のログから
// 見えなかった。
func TestLogger_RoutesIntoSlog(t *testing.T) {
	buf := captureWarn(t, slog.LevelWarn)

	// 障害時に実際に出た行と同じ形。
	Logger{}.Printf(context.Background(),
		"redis: connection pool: failed to dial after %d attempts: %v", 5,
		"dial unix /run/valkey/valkey.sock: connect: resource temporarily unavailable")

	logged := buf.String()
	assert.Contains(t, logged, "level=WARN", "接続失敗は INFO だと埋もれる")
	assert.Contains(t, logged, "failed to dial after 5 attempts")
	assert.Contains(t, logged, "resource temporarily unavailable")
	assert.Contains(t, logged, "source=go-redis", "発生源が分かること")
}

// **WARN 未満に落とさない。** level が下がると通常運転のログに埋もれて、
// 「再発時に気付ける」という目的を果たさなくなる。
func TestLogger_IsWarnNotInfo(t *testing.T) {
	// ERROR 閾値では出ない = WARN 以下である。
	buf := captureWarn(t, slog.LevelError)
	Logger{}.Printf(context.Background(), "something")
	assert.Empty(t, buf.String())

	// WARN 閾値では出る = INFO 以下ではない。
	buf = captureWarn(t, slog.LevelWarn)
	Logger{}.Printf(context.Background(), "something")
	assert.Contains(t, buf.String(), "level=WARN")
}

// UseSlog / UseSilent が go-redis に**何を**渡すかを固定する。
//
// go-redis は logger を読み戻せないので、SetLogger を挟んで受け取った値を
// 見る。UseSlog の配線落ちは下の e2e でも落ちるが、**UseSilent 側は e2e では
// 検出できない** (既定 logger は stderr に書くので slog のバッファは空のまま)
// ので、型で縛るのはこちらの担保。
func TestUseSlogAndUseSilent_InstallTheRightLogger(t *testing.T) {
	var installed redisLogger
	prev := setLogger
	setLogger = func(l redisLogger) { installed = l }
	t.Cleanup(func() { setLogger = prev })

	UseSlog()
	assert.IsType(t, Logger{}, installed, "UseSlog は slog 版を入れる")

	UseSilent()
	assert.IsType(t, &logging.VoidLogger{}, installed, "UseSilent は破棄版を入れる")
}

// **go-redis の内部ログが本当に slog に出る**ことを、go-redis 側から確かめる。
//
// 他のテストは Logger.Printf を直接叩いているので、UseSlog の配線が外れても
// 緑のまま通る。ここでは実際に dial を失敗させて go-redis の
// `internal.Logger.Printf` (pool.go の "failed to dial after N attempts") を
// 発火させ、それが slog に届くところまでを通す。#2659 の目的そのもの。
func TestUseSlog_GoRedisInternalLogReachesSlog(t *testing.T) {
	// go-redis の logger はプロセス全体の変数なので、終わったら既定へ戻す。
	t.Cleanup(logging.Enable)

	buf := captureWarn(t, slog.LevelWarn)
	UseSlog()

	// 存在しない unix socket なので dial は即 ENOENT。再試行を 1 回・
	// 間隔 1ms に絞ってテストを速く保つ (既定は 5 回 / 100ms)。
	c := redis.NewClient(&redis.Options{
		Network:            "unix",
		Addr:               filepath.Join(t.TempDir(), "absent.sock"),
		DialerRetries:      1,
		DialerRetryTimeout: time.Millisecond,
		MaxRetries:         -1,
	})
	t.Cleanup(func() { _ = c.Close() })

	err := c.Ping(context.Background()).Err()
	require.Error(t, err, "存在しない socket への Ping は失敗する")

	logged := buf.String()
	assert.Contains(t, logged, "source=go-redis", "go-redis 由来と分かること")
	assert.Contains(t, logged, "failed to dial", "pool の dial 失敗ログが載ること")
	assert.Contains(t, logged, "level=WARN")
}

// UseSilent が go-redis の内部ログを slog に混ぜないこと (上と対になる確認)。
//
// **これ単体では配線落ちを検出できない** — UseSilent が no-op でも go-redis の
// 既定 logger は stderr に書くので、slog のバッファは空のまま通る。何が
// 入るかは TestUseSlogAndUseSilent_InstallTheRightLogger が型で縛っている。
// ここで見ているのは「doctor の経路で slog に混ざらない」ことだけ。
func TestUseSilent_GoRedisInternalLogIsDiscarded(t *testing.T) {
	t.Cleanup(logging.Enable)

	buf := captureWarn(t, slog.LevelDebug)
	UseSilent()

	c := redis.NewClient(&redis.Options{
		Network:            "unix",
		Addr:               filepath.Join(t.TempDir(), "absent.sock"),
		DialerRetries:      1,
		DialerRetryTimeout: time.Millisecond,
		MaxRetries:         -1,
	})
	t.Cleanup(func() { _ = c.Close() })

	require.Error(t, c.Ping(context.Background()).Err())
	assert.Empty(t, buf.String(), "doctor は自前の出力を組み立てるので混ぜない")
}
