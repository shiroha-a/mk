package plugin

import (
	"context"
	"encoding/json"
)

// Jobs registers a plugin's background work.
//
// ジョブ名は `plugin:<プラグイン名>:<ジョブ名>` として queue 上の task type に
// なり、`plugin:<プラグイン名>` という専用のキューで動く。本体の task type と
// 衝突しないよう、名前空間は mk-go 側で付ける。
//
// 任意のタイミングで積むには [Context.Queue] を使う。**Routes からも呼べる**
// ので、HTTP ハンドラの中で重い処理を後回しにできる。
type Jobs interface {
	// Handle registers the handler for a named job.
	Handle(name string, h JobHandler)

	// Schedule runs the named job on a cron expression (5-field, UTC).
	// The job must also be registered with Handle.
	//
	// 定期取得のような用途。任意のタイミングで積むなら [Context.Queue]、
	// プロセス内で完結してよい非同期処理なら [Context.Go]。
	Schedule(cron string, name string, payload any)
}

// JobHandler processes one job. Returning an error fails the job.
//
// 再試行は既定で無し。[Queue.Enqueue] に [WithMaxAttempts] を渡したものだけが
// 再試行される (cron は常に再試行しない)。
//
// **ctx を尊重すること。** 1 回の実行には既定 1 時間の上限があり
// (#2658、queueHandlerDeadlineSeconds。プラグインのジョブにも同じ値が効く)、
// 超えると mk-go は待つのをやめる。
// ctx を見ていればそこで正常終了できるが、無視していると goroutine が
// 残り続ける (Go では goroutine を殺せない)。詳細は
// docs/plugins/authoring.md。
type JobHandler func(ctx context.Context, payload json.RawMessage) error
