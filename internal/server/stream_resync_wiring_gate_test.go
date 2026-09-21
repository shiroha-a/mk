package server

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 再接続時の取りこぼし回収 (#3132) の配線が `.vue` 側で外れていないか検査する。
//
// **判断そのものは `utility/reconnect-resync.ts` 側に切り出してあり、vitest が
// 68 形の変異で押さえている。** ここで見るのは「SFC がその関数を使っているか」
// だけ — 残った 1 行を書き換えれば、テストを 1 つも落とさずに過去の不具合へ
// 戻せてしまうため。実際に戻せる形が 5 つある:
//
//   - 通知側の `NOTIFICATION_RESYNC_PARAMS` を落とす (背景の穴埋めが既読化を
//     伴い、見ていない通知がバッジごと消える)
//   - `paginatorResyncOptions` を使わず直書きに戻す (入れ替え中の判定、
//     起点なしの倒し方、見送る条件がまとめて失われる)
//   - `_connected_` を `_disconnected_` に戻す (`Stream.reconnect()` 経由の
//     張り直しでは emit されないので、モバイルで最も多い経路が死ぬ)
//   - `off` / `dispose` を落とす (ハンドラのリーク)
//   - 通知側 watcher を `retry()` から `onConnected()` に戻す (切断が無くても撃つ)
//   - spread の後ろで述語を上書きする (テスト済みの判断を無言で差し替える)
//
// **コンポーネントを mount して検査しない。** この 2 つは `MkNote` / DI / store /
// prefer / useStream / misskeyApi を芋づるで引くので、モックが配線本体より
// 大きくなる。同じ `.vue` をソースとして読むゲートは `credit_origins_gate_test.go`
// に前例がある。
//
// **submodule を checkout する job でしか動かせない** (同上)。`make frontend-check`
// が `MK_FRONTEND_GATES_REQUIRE_SUBMODULE` を渡して skip を禁じる。
func TestStreamResyncIsWiredInTimelines(t *testing.T) {
	notifications := readResyncWiringSource(t, "frontend/src/components/MkStreamingNotificationsTimeline.vue")
	notes := readResyncWiringSource(t, "frontend/src/components/MkStreamingNotesTimeline.vue")

	for _, c := range []struct {
		name string
		body string
	}{
		{"notifications", notifications},
		{"notes", notes},
	} {
		t.Run(c.name, func(t *testing.T) {
			assert.Contains(t, c.body, "paginatorResyncOptions(paginator, {",
				"判断を切り出した関数を使っていない。直書きに戻すと、入れ替え中の判定や見送る条件がテストの外へ出る")
			assert.Contains(t, c.body, "createReconnectResync({",
				"再同期を組み立てていない")

			// **`_connected_` であること。** `_disconnected_` は
			// `Stream.reconnect()` (バックグラウンド 20 秒超からの復帰) では
			// emit されないので、そちらに依存すると主経路で動かない。
			// **`.on` と `.off` を別々に数える。** 引数列だけを数えると、`off` を
			// `on` に書き換えた形 (= 二重登録 + ハンドラのリーク) が件数 2 のまま
			// 通ってしまう。
			assert.Equal(t, 1, strings.Count(c.body, ".on('_connected_', reconnectResync.onConnected)"),
				"`_connected_` に onConnected を 1 度だけ繋いでいない")
			assert.Equal(t, 1, strings.Count(c.body, ".off('_connected_', reconnectResync.onConnected)"),
				"`_connected_` から onConnected を 1 度だけ外していない (同じ関数参照であること)")
			// **将来このコンポーネントが正当に `_disconnected_` を使うなら、この行を
			// 消してから使うこと。** いまは「判定に使ってはいけない」を表している。
			assert.NotContains(t, c.body, "_disconnected_",
				"`_disconnected_` は意図的な張り直しで emit されないので判定に使えない")
			assert.Contains(t, c.body, "reconnectResync.dispose()",
				"unmount で dispose していない")

			// spread の後ろに上書きキーが無いこと。`...paginatorResyncOptions(...)`
			// から `});` までに `getNewestId` / `canResync` / `resync` /
			// `getGeneration` が現れたら、テスト済みの述語を差し替えている。
			assertNoOverrideAfterSpread(t, c.body)
		})
	}

	// 通知一覧だけの約束。
	assert.Contains(t, notifications, "params: NOTIFICATION_RESYNC_PARAMS",
		"通知の穴埋めが `markAsRead: false` を渡していない (背景の取得で未読が消える)")
	assert.Contains(t, notifications, "void reconnectResync.retry();",
		"表に戻ったときの拾い直しが `retry()` でない (`onConnected()` だと切断が無くても撃つ)")
	assert.NotContains(t, notifications, "void reconnectResync.onConnected();",
		"watcher から `onConnected()` を呼んでいる (控えが無くても起点を作ってしまう)")

	// ノート側は queue を描画するので、先頭を見ていないときは積む。
	assert.Contains(t, notes, "toQueue: () => !isTop() || isPausingUpdate",
		"ノート側の振り分けがストリーミング受信 (`prepend` / `enqueue`) と揃っていない")
	assert.Contains(t, notifications, "toQueue: () => false",
		"通知側は queue の件数を描画しないので、積むと利用者から見えなくなる")
}

// readResyncWiringSource は submodule 内の SFC をコメント除去して読む。無ければ
// skip する (ただし `MK_FRONTEND_GATES_REQUIRE_SUBMODULE` があるときは失敗させる)。
//
// **コメントアウトして残すのは消すのと同じ。** 同パッケージの他の gate と同じ
// 判断で `stripComments` を通す。`_disconnected_` を不在で見る検査があるので、
// 説明文に出てくる語を拾わないためでもある。
func readResyncWiringSource(t *testing.T, rel string) string {
	t.Helper()
	full := filepath.Join(repoRootDir(t), "third_party", "misskey", "packages", rel)
	raw, err := os.ReadFile(full)
	if err != nil {
		if os.Getenv("MK_FRONTEND_GATES_REQUIRE_SUBMODULE") != "" {
			require.NoErrorf(t, err, "submodule を要求する job なのに %s を読めない", rel)
		}
		t.Skipf("%s が無い (submodule を checkout する job でのみ検査する)", rel)
	}
	return stripComments(string(raw))
}

// **行頭に縛らない。** `}), canResync: () => true,` のように閉じ括弧と同じ行へ
// 書かれた上書きも拾う。`paginatorResyncOptions` へ渡す側 (`toQueue` / `params`)
// はこの集合に無いので、緩めても誤検知しない。
var resyncOptionKeyRe = regexp.MustCompile(`\b(getNewestId|getGeneration|canResync|resync)\s*:`)

// assertNoOverrideAfterSpread は `...paginatorResyncOptions(...)` より後ろで
// 述語を上書きしていないことを見る。スプレッドにした構造上、呼び出し側は
// テスト済みの判断を無言で差し替えられる。
func assertNoOverrideAfterSpread(t *testing.T, body string) {
	t.Helper()
	const spread = "...paginatorResyncOptions(paginator, {"
	i := strings.Index(body, spread)
	require.GreaterOrEqual(t, i, 0, "spread が見つからない")
	// `createReconnectResync({ ... })` の閉じまで。SFC の中で最初に現れる
	// `\n});` を終端とする (この呼び出しはトップレベルなのでインデント 0)。
	rest := body[i:]
	end := strings.Index(rest, "\n});")
	require.GreaterOrEqual(t, end, 0, "createReconnectResync の閉じが見つからない")
	assert.Empty(t, resyncOptionKeyRe.FindAllString(rest[:end], -1),
		"spread の後ろで述語を上書きしている (テスト済みの判断が効かなくなる)")
}
