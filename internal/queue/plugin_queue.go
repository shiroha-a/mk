package queue

import (
	"context"
	"fmt"
	"regexp"

	"github.com/shiroha-a/mk/internal/queue/driver"
)

/*
 * プラグイン専用のキュー (#2818)。
 *
 * **プラグインごとに分ける。** 相乗りさせると、1 つのプラグインが詰まった
 * ときに他のプラグインと本体のジョブが巻き添えになる。名前を分けておけば
 * admin/queue から per-queue に一時停止も再開もできる。
 *
 * cron (#2478) はこれまで maintenance queue に相乗りしていたが、同じ理由で
 * こちらへ移す。
 */

// PluginQueuePrefix namespaces every plugin queue.
//
// **`:` を含めるのが要点。** プラグイン名は小文字英数字とハイフンに限られる
// (plugin.validName) ので、この接頭辞を持つ名前は本体のキューと衝突しない。
const PluginQueuePrefix = "plugin:"

// pluginNamePattern mirrors plugin.validName.
//
// **キュー名は Redis のキーになる**ので、ここでも形を確かめる。plugin 側の
// 検証を通っていれば必ず一致するが、本パッケージは plugin を import しない
// (レイヤが逆になる) ため独立して持つ。
var pluginNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// **AllQueueNames には入れない。** あちらは mk-go が常に持つキューの一覧で、
// プラグインのキューは構成で変わる。名前が要る側は driver の Inspector
// (実際に worker が見ているキュー) を引くか、PluginQueuePrefix で判定すること
// — 静的な一覧に混ぜると「設定に無いプラグインのタブが出る」ことになる。

// PluginQueueName returns the queue a plugin's jobs live in.
func PluginQueueName(plugin string) string { return PluginQueuePrefix + plugin }

// PluginTaskType namespaces one job so it cannot collide with mk-go's own
// task types, or with another plugin's.
//
// **enqueue 側と handler 側で同じものを使う。** 別々に組み立てると、片方を
// 変えたときにジョブが「処理者なし」で捨てられる。
func PluginTaskType(plugin, job string) string { return PluginQueuePrefix + plugin + ":" + job }

// PluginQueueNames returns the queue names for the given plugins, skipping
// names that cannot be part of a queue key.
func PluginQueueNames(plugins []string) []string {
	if len(plugins) == 0 {
		return nil
	}
	out := make([]string, 0, len(plugins))
	for _, p := range plugins {
		if !pluginNamePattern.MatchString(p) {
			continue
		}
		out = append(out, PluginQueueName(p))
	}
	if len(out) == 0 {
		// **空 slice を返さない。** driver の Config は len(QueueNames)==0 を
		// 「既定の一覧を使う」と読むので、空でも非 nil だと意味が変わる。
		return nil
	}
	return out
}

// EnqueuePlugin adds a job to a plugin's own queue.
//
// **再試行は既定で無し。** 冪等かどうかはプラグインしか知らないので、黙って
// 有効にはしない (plugin.WithMaxAttempts で明示する)。
func (c *Client) EnqueuePlugin(ctx context.Context, plugin, job string, body []byte, opts ...driver.EnqueueOption) error {
	if !pluginNamePattern.MatchString(plugin) {
		return fmt.Errorf("queue: プラグイン名 %q が不正です", plugin)
	}
	if job == "" {
		return fmt.Errorf("queue: ジョブ名が空です")
	}
	all := append([]driver.EnqueueOption{
		driver.WithQueue(PluginQueueName(plugin)),
		driver.WithMaxRetry(0),
	}, opts...)
	return c.inner.Enqueue(ctx, PluginTaskType(plugin, job), body, all...)
}
