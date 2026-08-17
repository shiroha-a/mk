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
	// register は承認済みの申請から確認メールを出す経路なので、同じく signup
	// と同格にする (signup の 1h 5 が「確認メール乱発」対策なのと同じ理由)。
	"signup-application/register": {Duration: time.Hour, Max: 5},
	// status はクレームコードの照会。コード自体が 256bit なので総当たりは
	// 問題にならないが、参照そのものの上限として signup-pending と同格を置く。
	"signup-application/status": {Duration: time.Hour, Max: 30},

	// ── Admin ──────────────────────────────────────────
	"admin/system-webhook/test": {Duration: 15 * time.Minute, Max: 60},
	// 初回セットアップの窓 (rootUserId 未設定 + 未認証) だけは credential 無しで
	// 通るので、setupPassword の試行回数に上限を置く。**signin の 10 ではなく 30
	// にしてある** — この endpoint は administrator が正規にアカウントを作る経路
	// でもあり、締めすぎると移行時の一括作成を無自覚に 429 で止める。30 でも
	// 総当たりは成立しない。
	"admin/accounts/create": {Duration: time.Hour, Max: 30},

	// ── Misc ───────────────────────────────────────────
	"fetch-external-resources": {Duration: time.Hour, Max: 50},
	// upstream 2026.7.0 GHSA hardening: fetch-rss は 60s/300 回。
	"fetch-rss":        {Duration: time.Minute, Max: 300},
	"users/lists/push": {Duration: time.Hour, Max: 30},
}
