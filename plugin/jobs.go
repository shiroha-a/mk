package plugin

import (
	"context"
	"encoding/json"
)

// Jobs registers a plugin's background work.
//
// ジョブ名は `plugin:<プラグイン名>:<ジョブ名>` として queue 上の task type に
// なる。本体の task type と衝突しないよう、名前空間は mk-go 側で付ける。
type Jobs interface {
	// Handle registers the handler for a named job.
	Handle(name string, h JobHandler)

	// Schedule runs the named job on a cron expression (5-field, UTC).
	// The job must also be registered with Handle.
	//
	// 定期取得のような用途が主目的。**任意のタイミングで enqueue する経路は
	// 用意していない** — retry / 重複排除 / 優先度といった queue の意味論を
	// 公開すると、それ自体が契約になって内部を変えられなくなる。
	// プロセス内で完結する非同期処理は [Context.Go] で足りる。
	Schedule(cron string, name string, payload any)
}

// JobHandler processes one job. Returning an error fails the job; plugin
// jobs are registered with no retries, so it is not retried.
//
// **ctx を尊重すること。** 1 回の実行には既定 1 時間の上限があり
// (#2658、queueHandlerDeadlineSeconds)、超えると mk-go は待つのをやめる。
// ctx を見ていればそこで正常終了できるが、無視していると goroutine が
// 残り続ける (Go では goroutine を殺せない)。詳細は
// docs/plugins/authoring.md。
type JobHandler func(ctx context.Context, payload json.RawMessage) error
