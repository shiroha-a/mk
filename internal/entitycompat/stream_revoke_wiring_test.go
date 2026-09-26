package entitycompat

import "testing"

// 資格情報の失効で /streaming の接続を閉じる経路は、publish 側 (各 handler) と
// 購読側 (stream.Manager) の両方が揃って初めて効く。
//
// **handler 側は起動時の critical wiring 検査 (i.streamRevoker /
// admin.userStreamRevoker / oauth.streamRevoker) が見るが、購読側は見ない。**
// `SubscribeStreamRevoke` を落としても publish は成功し、ビルドもテストも通るまま
// どの接続も閉じなくなる。失効した token の WebSocket が通知・DM を受け取り続ける
// 状態へ黙って戻るので、配線の 1 行をここで固定する。
func TestStreamRevokeIsWired(t *testing.T) {
	assertWired(t, routerGo, "streamManager.SubscribeStreamRevoke()",
		"失効した token / 凍結・削除した利用者の WebSocket が閉じなくなる。publish は成功するのでエラーも出ない")
	assertWired(t, routerGo, "stream.NewStreamRevokePublisher(streamPubSub)",
		"失効を他プロセスの stream.Manager へ配れなくなる")
}
