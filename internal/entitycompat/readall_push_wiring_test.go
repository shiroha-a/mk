package entitycompat

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// routerCalls returns every call expression in router.go, normalised by
// stripping whitespace so the caller can match on a single-line form.
//
// **AST で読む理由。** 初版は行の文字列一致で「`//` で始まる行は数えない」と
// していたが、それだと `/* ... */` で囲んだ配線が生きたものとして通る
// (実測)。パーサはコメントをそもそも構文木に載せないので、囲み方に依存しない。
func routerCalls(t *testing.T) map[string]bool {
	t.Helper()
	path := filepath.Join(repoRoot(t), "internal/server/router.go")
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse router.go: %v", err)
	}
	out := make(map[string]bool)
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		var buf bytes.Buffer
		if err := printer.Fprint(&buf, fset, call); err != nil {
			return true
		}
		out[strings.Join(strings.Fields(buf.String()), "")] = true
		return true
	})
	// **1 つも読めなかったら落とす。** 書式が変わって拾えなくなると、
	// 検査していないのに緑になる。
	if len(out) == 0 {
		t.Fatal("router.go から call expression を 1 つも読めなかった")
	}
	return out
}

// assertWired fails when router.go does not contain the given call verbatim.
func assertWired(t *testing.T, calls map[string]bool, wiring, symptom string) {
	t.Helper()
	if !calls[strings.Join(strings.Fields(wiring), "")] {
		t.Errorf("internal/server/router.go に %q の配線が無い (コメントは数えない)。\n%s", wiring, symptom)
	}
}

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
// **引数まで含めて照合する** — 関数名だけ見ると `SetReadAllPusher(nil)` が通り抜けて、
// push が死んだまま緑になる。
func TestReadAllNotificationsPusherIsWired(t *testing.T) {
	assertWired(t, routerCalls(t), "notificationService.SetReadAllPusher(webPushService)",
		"readAllNotifications の Web Push が出なくなり、他端末に表示中の\n"+
			"OS 通知が「すべて既読」でも閉じなくなる。エラーもログも出ない。")
}

// chat の Web Push も router が producer を配線しないと出ない (#2840)。
//
// **新設した TestWebPushProducersAreWired では捕まらない。** あちらは
// `internal/core/chat` の中に `s.chatPusher.PushNewChatMessage(` があるかを
// 見るだけで、その `chatPusher` に実物が入るかは見ていない。router の 1 行を
// 消しても build もテストも gate も全部緑になることを実測した。
//
// **SetUserRepo も要る。** push の body には SW が無条件に読む `fromUser` が
// 必要で、create 経路の row は `FromUser` を Preload しないため repo から
// 引き直す。引けないときは body を壊すより push を落とす実装なので、
// この行が無いとチャットの通知が**全滅する** (`slog.Warn` は出る)。
// `SetAPDelivery` が同じ repo を上書きするので消しても動いてしまい、
// 「実は効いていない配線」に見える状態を避けるためここで固定する。
func TestChatPusherIsWired(t *testing.T) {
	calls := routerCalls(t)
	assertWired(t, calls, "chatService.SetChatPusher(webPushService)",
		"チャットの Web Push が出なくなる。エラーもログも出ない")
	assertWired(t, calls, "chatService.SetUserRepo(userRepo)",
		"push body の fromUser を引けず、チャットの Web Push が全滅する")
}
