package asynqdriver

import (
	"github.com/hibiken/asynq"

	"github.com/shiroha-a/mk/internal/queue/driver"
)

// toAsynqOptions translates a driver.EnqueueOptions into the
// equivalent []asynq.Option list. Empty / zero fields are skipped so
// the asynq library applies its own defaults (e.g. MaxRetry=25).
//
// 注意: `KeepFailed` は asynq に per-job 相当 API が無いので silent
// no-op (= mkq 専用 retention)。driver-neutral API として上位が
// `WithKeepFailed` を渡しても asynq では効果を持たない設計 (#1184)。
// 必要なら asynq archived bucket の age-based retention を別途設定
// する経路で対応する。
func toAsynqOptions(o driver.EnqueueOptions) []asynq.Option {
	out := make([]asynq.Option, 0, 4)
	if o.Queue != "" {
		out = append(out, asynq.Queue(o.Queue))
	}
	if o.MaxRetrySet {
		// 0 を明示的に指定するケース (cleanRemoteNotes / chart 等の
		// retry 不要ジョブ) があるため driver 側の MaxRetrySet で
		// 「default 値ではない」ことを判定する。
		out = append(out, asynq.MaxRetry(o.MaxRetry))
	}
	if o.UniqueTTL > 0 {
		out = append(out, asynq.Unique(o.UniqueTTL))
	}
	if o.ProcessIn > 0 {
		out = append(out, asynq.ProcessIn(o.ProcessIn))
	}
	return out
}
