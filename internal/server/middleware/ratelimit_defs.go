package middleware

import (
	"time"

	"github.com/shiroha-a/mk/internal/api/apierr"
)

// DefaultEndpointLimits defines per-endpoint rate limits matching
// Misskey TS upstream. Endpoints not listed here have no rate limit.
//
// Source: third_party/misskey/packages/backend/src/server/api/endpoints/
//
// rateLimitFactor (role policies) は RateLimiter.SetPolicyProvider 経由で
// runtime に反映される (PR #617 / #606 item 4)。
var DefaultEndpointLimits = map[string]*EndpointLimit{
	// ── AP ─────────────────────────────────────────────
	"ap/get":  {Duration: time.Hour, Max: 30},
	"ap/show": {Duration: time.Hour, Max: 30},

	// ── Blocking ───────────────────────────────────────
	"blocking/create": {Duration: time.Hour, Max: 20},
	"blocking/delete": {Duration: time.Hour, Max: 100},

	// ── Bubble Game ────────────────────────────────────
	"bubble-game/register": {Duration: time.Hour, Max: 120, MinInterval: 30 * time.Second},

	// ── Channels ───────────────────────────────────────
	"channels/create": {Duration: time.Hour, Max: 10},

	// ── Chat ───────────────────────────────────────────
	// **create も登録すること。** create-to-user / create-to-room は
	// MessagesCreate への alias なので、create に toUserId / toRoomId を載せると
	// 同じ送信ができる。未登録の path は limiter が素通しする (フォールバック
	// が無い) ため、ここが抜けると上限そのものを迂回できる。
	"chat/messages/create":          {Duration: time.Hour, Max: 500},
	"chat/messages/create-to-room":  {Duration: time.Hour, Max: 500},
	"chat/messages/create-to-user":  {Duration: time.Hour, Max: 500},
	"chat/rooms/create":             {Duration: 24 * time.Hour, Max: 10},
	"chat/rooms/invitations/create": {Duration: 24 * time.Hour, Max: 50},

	// ── Clips ──────────────────────────────────────────
	"clips/add-note": {Duration: time.Hour, Max: 20},

	// ── Drive ──────────────────────────────────────────
	"drive/files/create":          {Duration: time.Hour, Max: 120},
	"drive/files/upload-from-url": {Duration: time.Hour, Max: 60},
	"drive/folders/create":        {Duration: time.Hour, Max: 10},

	// ── Export / Import ────────────────────────────────
	"export-custom-emojis": {Duration: time.Hour, Max: 1},
	"i/export-antennas":    {Duration: time.Hour, Max: 1},
	"i/export-blocking":    {Duration: time.Hour, Max: 1},
	"i/export-clips":       {Duration: 24 * time.Hour, Max: 1},
	"i/export-favorites":   {Duration: 24 * time.Hour, Max: 1},
	"i/export-following":   {Duration: time.Hour, Max: 1},
	"i/export-mute":        {Duration: time.Hour, Max: 1},
	"i/export-notes":       {Duration: 24 * time.Hour, Max: 1},
	"i/export-user-lists":  {Duration: time.Minute, Max: 1},
	"i/import-antennas":    {Duration: time.Hour, Max: 1},
	"i/import-blocking":    {Duration: time.Hour, Max: 1},
	"i/import-following":   {Duration: time.Hour, Max: 1},
	"i/import-muting":      {Duration: time.Hour, Max: 1},
	"i/import-user-lists":  {Duration: time.Hour, Max: 1},

	// ── Flash ──────────────────────────────────────────
	"flash/create": {Duration: time.Hour, Max: 10},
	"flash/update": {Duration: time.Hour, Max: 300},

	// ── Following ──────────────────────────────────────
	"following/create":     {Duration: time.Hour, Max: 100},
	"following/delete":     {Duration: time.Hour, Max: 100},
	"following/invalidate": {Duration: time.Hour, Max: 100},
	"following/update":     {Duration: time.Hour, Max: 100},
	"following/update-all": {Duration: time.Hour, Max: 10},

	// ── Gallery ────────────────────────────────────────
	"gallery/posts/create": {Duration: time.Hour, Max: 20},
	"gallery/posts/update": {Duration: time.Hour, Max: 300},

	// ── I (account) ────────────────────────────────────
	"i/change-password":       {Duration: time.Hour, Max: 10, MinInterval: time.Second},
	"i/move":                  {Duration: 24 * time.Hour, Max: 5},
	"i/notifications":         {Duration: 30 * time.Second, Max: 30},
	"i/notifications-grouped": {Duration: 30 * time.Second, Max: 30},
	"i/update":                {Duration: time.Hour, Max: 20},
	"i/update-email":          {Duration: time.Hour, Max: 3},
	"i/webhooks/test":         {Duration: 15 * time.Minute, Max: 60},

	// ── Muting ─────────────────────────────────────────
	"mute/create":        {Duration: time.Hour, Max: 20},
	"renote-mute/create": {Duration: time.Hour, Max: 20},

	// ── Notes ──────────────────────────────────────────
	"notes/create":               {Duration: time.Hour, Max: 300},
	"notes/delete":               {Duration: time.Hour, Max: 300, MinInterval: time.Second},
	"notes/drafts/create":        {Duration: time.Hour, Max: 300},
	"notes/drafts/update":        {Duration: time.Hour, Max: 300},
	"notes/favorites/create":     {Duration: time.Hour, Max: 20},
	"notes/reactions/delete":     {Duration: time.Hour, Max: 60, MinInterval: 3 * time.Second},
	"notes/thread-muting/create": {Duration: time.Hour, Max: 10},
	"notes/unrenote":             {Duration: time.Hour, Max: 300, MinInterval: time.Second},

	// ── Notifications ──────────────────────────────────
	"notifications/create":            {Duration: time.Minute, Max: 10},
	"notifications/test-notification": {Duration: time.Minute, Max: 10},

	// ── Pages ──────────────────────────────────────────
	"pages/create": {Duration: time.Hour, Max: 10},
	"pages/update": {Duration: time.Hour, Max: 300},

	// ── Password Reset ─────────────────────────────────
	"request-reset-password": {Duration: time.Hour, Max: 3},
	// reset-password 自体 (トークン消費) は brute-force 対策で 1h あたり 30。
	// 確認 link を踏んだ正当ユーザーが何度か失敗してもブロックされない程度。
	"reset-password": {Duration: time.Hour, Max: 30},

	// ── Auth (signup / signin) ─────────────────────────
	// signup spam (大量 user_pending row 作成 + 確認メール乱発) 対策。
	// 1h あたり 5 で十分。実運用では captcha / invitation 制が併用される
	// 想定だが、それらが無効でも spam を抑える last-line guard。
	"signup": {Duration: time.Hour, Max: 5},
	// signup-pending (確認 code 入力) は brute-force 対策で 1h あたり 30
	// (1 確認 code 試行を 30 回まで許容、TTL 内で総当たりされても 16 byte
	// hex の探索空間 (2^128) には到底届かない)。
	"signup-pending": {Duration: time.Hour, Max: 30},
	// signin / signin-flow は credential brute force 対策。upstream
	// SigninApiService に合わせて 1h 10 回 + 1s minInterval、超過時は専用の
	// TOO_MANY_AUTHENTICATION_FAILURES (22d05606-...) を返す (#1829)。
	"signin":      {Duration: time.Hour, Max: 10, MinInterval: time.Second, RejectResponse: apierr.TooManyAuthenticationFailures},
	"signin-flow": {Duration: time.Hour, Max: 10, MinInterval: time.Second, RejectResponse: apierr.TooManyAuthenticationFailures},
	// signin-with-passkey は upstream SigninWithPasskeyApiService に合わせて
	// 30min 200 回 + 250ms minInterval、同 code (#1829)。
	"signin-with-passkey": {Duration: 30 * time.Minute, Max: 200, MinInterval: 250 * time.Millisecond, RejectResponse: apierr.TooManyAuthenticationFailures},

	// ── Auth (承認制の登録) ────────────────────────────
	// 承認制は `/api/signup` を 403 で塞いで自分がゲートになる (#2557) ので、
	// **signup が持っていた last-line guard もここが引き継ぐ**。captcha は
	// 業者が 1 つも有効でないと素通りする (= 既定構成では効かない) ため、
	// これが無いと未認証の入口に上限が 1 つも無くなる。
	//
	// apply は signup と同格。申請行と審査キューが無制限に積まれるのを防ぐ。
	"signup-application/apply": {Duration: time.Hour, Max: 5},
	// 絵文字の登録・インポート申請 (#2934 / #2935)。**未登録の endpoint は
	// 上限が引けず素通しになる**ので明示する。pending の一意制約は
	// `(userId, name)` なので、名前を変えれば 1 人で無制限に審査キューを
	// 積める。#2935 で**任意のノートの絵文字メニューから 2 クリック**になり、
	// 露出が大きく変わった (従来は drive へ上げてから専用ページを開く必要が
	// あった)。signup の申請と同格にする。
	"emoji-application/create": {Duration: time.Hour, Max: 5},
	// form-token は apply を守る署名付きトークンの発行 (#2806)。フォームの
	// 読み込み直しは正常な操作なので apply より緩くするが、**無制限にはしない** —
	// 未登録の endpoint は上限が引けず素通しになる (ratelimit.go の lookup)。
	// status と同格の 1h 30 にしてある。
	"signup-application/form-token": {Duration: time.Hour, Max: 30},
	// register は承認済みの申請から確認メールを出す経路なので、同じく signup
	// と同格にする (signup の 1h 5 が「確認メール乱発」対策なのと同じ理由)。
	"signup-application/register": {Duration: time.Hour, Max: 5},
	// status はクレームコードの照会。コード自体が 256bit なので総当たりは
	// 問題にならないが、参照そのものの上限として signup-pending と同格を置く。
	"signup-application/status": {Duration: time.Hour, Max: 30},

	// ── Admin ──────────────────────────────────────────
	"admin/system-webhook/test":   {Duration: 15 * time.Minute, Max: 60},
	"admin/roles/assignment-show": {Duration: time.Minute, Max: 120},
	// 初回セットアップの窓 (rootUserId 未設定 + 未認証) だけは credential 無しで
	// 通るので、setupPassword の試行回数に上限を置く。**signin の 10 ではなく 30
	// にしてある** — この endpoint は administrator が正規にアカウントを作る経路
	// でもあり、締めすぎると移行時の一括作成を無自覚に 429 で止める。30 でも
	// 総当たりは成立しない。
	"admin/accounts/create": {Duration: time.Hour, Max: 30},

	// ── Misc ───────────────────────────────────────────
	"fetch-external-resources": {Duration: time.Hour, Max: 50},
	// upstream 2026.7.0 GHSA hardening: fetch-rss は 60s/300 回。
	"fetch-rss":             {Duration: time.Minute, Max: 300},
	"roles/assignment-show": {Duration: time.Minute, Max: 60},

	// ── 公開の関係一覧 (#2953) ──────────────────────────
	//
	// **upstream には上限が無い** (`users/following.ts` / `followers.ts` の
	// `meta` に `limit` が無い) mk-go 独自の追加。未認証で全件を引けるので、
	// フォロー一覧を CSV 化してインポートに食わせる収集の速度に上限を置く。
	//
	// **壁ではなく速度制限帯。** 1 アカウントぶんの書き出し (実測 39 件 =
	// 2 リクエスト) は止まらない。止めるのは社会グラフの一括収集のほう。
	//
	// **窓を 1 時間にしない。** store は拒否したリクエストも記録するので、
	// 429 を無視して叩き続けるクライアントは Retry-After を 1 窓ぶんに
	// 押し戻し続ける。長い窓だと**行儀の悪いタブ 1 つで CGNAT 配下が丸ごと
	// 閲覧不能**になる。閲覧系の前例 (`i/notifications` の 30s/30、
	// `roles/assignment-show` の 1m/60) と同じ短い窓に揃える。
	//
	// **30 は人間のスクロールを大きく上回る。** フロントエンドは初回 20 行、
	// 以降 1 リクエスト 30 行 (`paginator.ts` の SECOND_FETCH_LIMIT) なので、
	// 30 req/min は「毎秒 15 行を 1 分間読み続ける」速度に相当する。
	//
	// **「当たっても最大 60 秒」ではない (レビュー M-2)。** 拒否も記録される
	// ので、429 のまま叩き続けると `Retry-After` が
	// 58 秒前後で張り付き、**止めるまで解けない** (実 Redis で実測)。窓を
	// 短くしてもこの性質は消えない — 短い窓は「巻き添えの上限」ではなく
	// 「巻き添えが解けるまでの下限」を短くするだけ。同梱フロントは
	// `fetchOlder` が 429 を握り潰して `canFetchOlder` を true のままにするので、
	// 無限スクロールが視界にある限り自走しうる (frontend 側の別課題)。
	//
	// **未認証だけに絞らない。** オープン登録のインスタンスでは捨て
	// アカウント 1 個で迂回でき、IP をローテートするより安い。絞るほうが
	// 防御として弱くなる。
	"users/following":  {Duration: time.Minute, Max: 30},
	"users/followers":  {Duration: time.Minute, Max: 30},
	"users/lists/push": {Duration: time.Hour, Max: 30},
}
