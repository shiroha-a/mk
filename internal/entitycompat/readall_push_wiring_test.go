package entitycompat

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// readAllNotifications の Web Push は router が producer を配線しないと出ない。
//
// **producer だけ無い状態が実際に長く続いていた** (#2831)。`PushReadAllNotifications`
// の実装も `sw_subscription` の列も `/api/sw/*` の受け口も、queue の processor まで
// 揃っているのに**呼ぶ側がどこにも無く**、自分のテストからしか呼ばれていなかった。
// SW 側は upstream と同じく readAllNotifications を受けて表示中の OS 通知を閉じる
// 実装を持っているので、別端末のトーストが「すべて既読」で消えないまま残っていた。
//
// nil は「push しない」なので、**この行を落としても build もテストも通る**。
// 症状は「他の端末の通知が消えない」だけで、エラーもログも出ない。
//
// 判定はソースの文字列一致で、同 package の `TestPluginPeerBodyLimitIsWired` と
// 同じ形。**コメント行は数えない** (コメントアウトして残すのは消すのと同じ)。
// **引数まで含めて照合する** — 関数名だけ見ると `SetReadAllPusher(nil)` が通り抜けて、
// push が死んだまま緑になる。
func TestReadAllNotificationsPusherIsWired(t *testing.T) {
	path := filepath.Join(repoRoot(t), "internal/server/router.go")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read router.go: %v", err)
	}
	var live []string
	for _, line := range strings.Split(string(src), "\n") {
		if trimmed := strings.TrimSpace(line); !strings.HasPrefix(trimmed, "//") {
			live = append(live, trimmed)
		}
	}
	const wiring = "notificationService.SetReadAllPusher(webPushService)"
	if !strings.Contains(strings.Join(live, "\n"), wiring) {
		t.Errorf("internal/server/router.go に %q の配線が無い (コメントは数えない)。\n"+
			"readAllNotifications の Web Push が出なくなり、他端末に表示中の\n"+
			"OS 通知が「すべて既読」でも閉じなくなる。エラーもログも出ない。", wiring)
	}
}
