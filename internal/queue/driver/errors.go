package driver

import "errors"

// ErrSkipRetry is a sentinel returned by a HandlerFunc to tell the driver
// the job must not be retried even if attempts remain. Drivers map
// this to their native skip-retry semantics (mkq.ErrUnrecoverable).
//
// Handlers typically wrap it with fmt.Errorf("%w: %w", err, driver.ErrSkipRetry)
// so callers can both inspect the underlying cause and observe the skip
// signal via errors.Is.
var ErrSkipRetry = errors.New("queue driver: skip retry")

// ErrResizeNotSupported is returned by Driver.Resize when the driver has
// no worker pool object to resize at all.
//
// **「Start 前」ではなく「Server() 前」。** mkq driver がこれを返すのは
// `Driver.Server()` を一度も呼んでいないときだけで (mkqdriver/driver.go の
// `d.dServer == nil`)、`Server()` 済みで `Start()` 前なら pool map が空なので
// 返るのは `mkqdriver: Resize: unknown queue %q` のほう。production の配線は
// `newServer` が構築時に `queue.NewServer(driver)` = `Server()` を呼ぶので、
// **この sentinel には到達しない** (#2985 の敵対的レビューで実測)。
//
// したがってこれを「auto-scale が使えない driver」の判定には使わないこと。
// 以前 startAutoScale がそうしていたが、上記の理由で一度も発火しなかった。
var ErrResizeNotSupported = errors.New("queue driver: dynamic Resize not supported by this backend")
