package driver

import "errors"

// SkipRetry is a sentinel returned by a HandlerFunc to tell the driver
// the job must not be retried even if attempts remain. Drivers map
// this to their native skip-retry semantics (asynq.SkipRetry,
// mkq.ErrUnrecoverable, etc.).
//
// Handlers typically wrap it with fmt.Errorf("%w: %w", err, driver.SkipRetry)
// so callers can both inspect the underlying cause and observe the skip
// signal via errors.Is.
var SkipRetry = errors.New("queue driver: skip retry")

// ErrResizeNotSupported is returned by Driver.Resize on driver backends
// that do not support dynamic worker pool sizing (= asynq today, until
// upstream adds a Resize-equivalent API). Auto-scale callers should
// type-check this error and silently degrade to "no scale-up" rather
// than treating it as a runtime failure — at-runtime the auto-scaler
// is still useful for the operator-visible metrics even when actual
// resize is a no-op (#1120 tracker ADR §5.2).
var ErrResizeNotSupported = errors.New("queue driver: dynamic Resize not supported by this backend")
