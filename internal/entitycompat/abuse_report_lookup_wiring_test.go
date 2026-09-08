package entitycompat

import "testing"

// abuseReport 通知の状態表示は router が lookup を配線しないと成立しない (#2868)。
//
// **fail-closed なので機能ごと死ぬ。** lookup 未配線だと abuseReport 通知は
// 誰にも返らない。read 時に状態を引けないまま「未対応」に見せると、対処済みの
// 通報に別のモデレーターが二重で当たるので、出さないほうを選んでいる。
//
// **配線先は 2 つある。** REST (i/notifications) と WebSocket の publisher で
// 別々に pack するので、片方だけ配線すると「一覧には出るが realtime では
// 出ない」という非対称になる。
func TestAbuseReportLookupIsWired(t *testing.T) {
	assertWired(t, routerGo, "notificationsHandler.SetAbuseReportLookup(abuseNotifStates)",
		"abuseReport 通知が一覧に出なくなる (fail-closed)。")
	assertWired(t, routerGo, "notificationPublisher.SetAbuseReportLookup(abuseNotifLookup)",
		"abuseReport 通知が realtime (WebSocket) で出なくなる。\n"+
			"一覧には出るので、片方だけ壊れていることに気付きにくい。")
}
