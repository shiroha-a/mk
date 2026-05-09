# API互換性状況

対象バージョン: **Misskey 2026.3.2** base + drift backlog 適用済 (実質 2026.5.x 相当の挙動)
最終更新: 2026-05-09

本ドキュメントは互換性調査 (#107, #124) と、Playwright Phase 1-4 で発見・修正した drift backlog の結果を集約したもの。

## 概要

- **Phase 1-4 完了**: Playwright e2e で **96 spec / 35 directory / 242 endpoint cover (54.3%)** を両 backend (mk-go / Misskey TS) で daily nightly に検証
- **drift backlog**: spec 整備中に発見した 40+ 件の drop-in 互換 drift を fix 済 (= mk-go 単体で TS frontend / TS API client が壊れずに動く水準)
- **upstream catch-up**: 2026.4.0 / 2026.5.0 / 2026.5.1 への追従は #947 で個別 sub-task として消化中、2026.3.2 → 2026.5.1 の backend 差分は [`docs/update/20260500diff.md`](update/20260500diff.md) / [`docs/update/20260501diff.md`](update/20260501diff.md) を参照

## エンドポイントカバー率

router.go 登録の **448 endpoint** のうち、Playwright spec で round-trip 検証されているのは **242 endpoint (54.3%)**。残りは smoke 範囲外 (= WebSocket / 複雑 mutation / federation delivery / cron / push server) または smoke 化困難な mutation。

| カテゴリ | 主要 endpoint 群 | 状態 |
|---|---|---|
| admin/* | user / role / meta / queue / abuse-report / system-webhook / avatar-decorations / ad / invite / announcements / relays / captcha / drive read / stats | ✅ Phase 4 verified |
| notes/* | create / delete / show / visibility / search / search-by-tag | ✅ Phase 1-2 verified |
| users/* | profile / follow / mute / block / list / search / featured / reactions | ✅ Phase 2 verified |
| i/* | favorites / pin / follow_requests / registry / revoke_token / webhooks / 2FA TOTP / 2FA Passkey | ✅ Phase 1-4 verified |
| drive/* | create / find / find-by-hash / folders / mutate | ✅ Phase 2 verified |
| reactions/* | create / delete / replace / different / users_reactions | ✅ Phase 2 verified |
| chat/* | DM / room / messaging_notification / extra | ✅ Phase 2 / 4 verified (mk-go 独自設計部分含む) |
| notifications/* | reaction / mention / follow / reply / renote / follow_request / mark_all_as_read / control | ✅ Phase 2-3 verified |
| timeline/* | home / local / global / hybrid | ✅ Phase 2 verified |
| emoji/* | custom_emoji_reaction / lifecycle / bulk / import_zip | ✅ Phase 2 verified |
| auth/* | signup / signin / signin-invalid / 2FA TOTP / 2FA Passkey | ✅ Phase 1 verified |
| federation/* / ap/* | shape | ✅ Phase 3 verified |
| channels/* | list / search / featured / followed / show / timeline | ✅ Phase 4 verified |
| hashtags/* | search / tags / list | ✅ Phase 4 verified |
| roles/* | role-policy 系 | ✅ Phase 4 verified |
| pages/* / gallery/* / clips/* / flash/* / reversi/* | content 系 | ✅ Phase 2 verified |
| antennas/* / search/* | shape | ✅ Phase 3 verified |
| その他 (charts, miauth, app, sw, utility, …) | shape | ✅ Phase 3-4 verified |

未実装エンドポイントへのリクエストはキャッチオールハンドラが`200 {}`で応答するため、クライアントがクラッシュすることはない。

## 対応済みの互換性修正

### 第1次調査 (#107) サブissue

| # | 内容 | 状態 |
|---|---|---|
| #109 | AP: MFM→HTML変換、attachment、context拡充 | 完了 |
| #110 | NoteEntity/UserDetailed レスポンス完全化 | 完了 |
| #111 | パスワードリセット、MiAuth gen-token、App API | 完了 |
| #112 | Timelineフィルタリング、signin-flow captcha | 完了 |
| #113 | 欠損テーブル7種 + 補助エンドポイント12+ | 完了 |

### 第2次調査 (#124) サブissue

| # | 内容 | 状態 |
|---|---|---|
| #125 | WebSocket 9チャンネル追加 + タイムラインフィルタパラメータ | 完了 |
| #126 | AP Inbox Block/Flag/Move/Add/Remove | 完了 |
| #127 | AP Accept完全実装 + Question(投票)受信 + EmojiReactバリアント | 完了 |
| #128 | users/lists update + update-membership | 完了 |
| #129 | trustProxyサポート | 完了 |
| #130 | エラーレスポンス標準化 (UUID統一) | 完了 |
| #131 | DBスキーマ欠損カラム追加 | 完了 |
| #132 | WebSocketプロトコル改善 (OAuth2スコープ, pong応答) | 完了 |
| #133 | dbSlaves (リードレプリカ)サポート | 完了 |
| #134 | chart tables, social auth, AP拡張, Sentry | 完了 |

### Playwright Phase 1-4 由来の drift backlog (#798 ~ #944)

Playwright spec を両 backend で走らせる中で観測した「TS と mk-go で挙動が違う」 drift を 40+ 件 fix。代表的なもの (一部 PR バンドル含む):

#### auth / signup / signin
| # | 内容 |
|---|---|
| #798 | signup duplicate username の status code (400 DUPLICATED_USERNAME に統一) |
| #800 | username 長さ制限 (20 char max に統一) |
| #802 | error response body shape (Misskey misc 形式に統一) |

#### note / timeline
| # | 内容 |
|---|---|
| #799 | notes/show で visibility 違反時の挙動 (200 で stub note を返す) |
| #874 | timeline endpoint で user mute filter を追加 (#892 / #894 で perf 最適化) |
| #876 | users/lists/list の N+1 query を batch fetch に最適化 |
| #877 | notes/search の external search backend 必須要件 (Meilisearch 未設定で 400) |
| #878 | users/search-by-username-and-host の suspend filter |

#### drive
| # | 内容 |
|---|---|
| #812 | drive/files/create userId / user / folder shape |
| #818 | drive/files/find / find-by-hash の packMany self path drift |
| #845 | drive/folders/show detail mode (parent / counts) |

#### chat
| # | 内容 |
|---|---|
| #851 / #855 / #860 | chat packMessage / packRoom の null 省略・field set drift |
| #864 | reaction emoji の variation selector 正規化 |

#### users / following / blocking
| # | 内容 |
|---|---|
| #870 | blocking/create / delete return shape (UserDetailed を返す) |
| #871 | users/lists/create response shape (createdAt / userIds / isPublic 含む) |
| #872 | blocked → following/create reject status (400) |

#### settings / token
| # | 内容 |
|---|---|
| #883 | i/regenerate-token return shape (204 No Content) |
| #884 | 旧 API token invalidation (cache 経由でも即時 reject) |
| #885 | i/update-email error status 標準化 |
| #910 | app-issued access token を auth middleware で dual lookup (raw → hash) |
| #913 | i/revoke-token も dual lookup + cache invalidation 化 |

#### admin
| # | 内容 |
|---|---|
| #888 | admin/show-user shape (roles / policies / signins / roleAssigns / isHibernated / lastActiveDate) |
| #889 | admin/roles/create paramDef strict (13 field required) |
| #896 / #900 / #901 / #931 / #932 | pq.StringArray drift (avatar-decorations / system-webhook / flash / antenna / emoji bulk の Updates(map) で空 string[] が NULL 化される問題) |
| #907 | i/registry/set で domain field の handling drift |
| #918 | sw/show-registration を 204 No Content に統一 |
| #915 | federation/show-instance を 204 No Content に統一 |
| #925 | hashtags/* paramDef strictness drift |
| #926 | channels の Playwright LCD を strict 化 |
| #929 | admin/queue + admin/abuse-report の paramDef strictness / idempotency drift |
| #936 | i/webhooks/update の status code drift (204) |
| #937 | i/webhooks/test の paramDef drift (type required + webhookEventTypes enum) |
| #939 | i/webhooks/{create,update} の on array enum check |

#### UDS production 由来 bug
| # | 内容 |
|---|---|
| #940 | 遅延配送 remote note の createdAt drift (AP `published` を採用 + clock skew/floor guard) |
| #941 | カスタム絵文字 (リアクション / picker) のアニメ pass-through (mediaproxy で gif/apng を resize しない) |
| #942 | URL summary 文字化け (Shift_JIS / EUC-JP / ISO-2022-JP の自動正規化) |
| #943 | リモートユーザー counts (notesCount / followersCount / followingCount) を origin から fetch する mk-go 独自拡張 (#945 で LRU cache 化) |

## ActivityPub互換性

### 対応済みActivity

| Activity | 送信 | 受信 |
|---|---|---|
| Create (Note) | o | o |
| Delete (Note) | o | o |
| Update (Note/Person) | o | o |
| Follow | o | o |
| Accept (Follow) | o | o |
| Reject (Follow) | o | o |
| Undo (Follow/Like/Announce/Block) | o | o |
| Like (Reaction) | o | o |
| Announce (Renote) | o | o |
| Block | o | o |
| Flag (Report) | - | o |
| Move (Account Migration) | - | o |
| Add/Remove (Pin) | - | o |

### AP Person

MFM→HTML変換、`featured`、`attachment`(プロフィールフィールド)、`tag`(絵文字タグ)、`image`(バナー)は#109で対応済み。

### AP Note

`content`のHTML化、`attachment`(ファイル)、拡張`@context`(Misskey独自vocabulary)は#109で対応済み。Question(投票)オブジェクトの受信は#127で対応済み。

### AP variant handling

upstream / 他実装が出してくる variant に対するロバスト性:

- **`published` の parse + 異常値 fallback** (#940): RFC3339 / RFC3339Nano、未来 5min skew / 過去 10 年で fallback
- **`attributedTo` / `actor` の string / object 双方受け入れ** (upstream #17340 由来、#947 配下で対応予定)
- **`alsoKnownAs` の array / string 双方受け入れ** (upstream #17275 由来、#947 配下で対応予定)
- **存在しない Actor の Delete を ignore** (upstream #17294 由来、#947 配下で対応予定)
- **リレー由来 Announce の正しい処理** (upstream #17308 由来、#947 配下で対応予定)

## WebSocket/Streaming

### チャンネルカバー率

全19チャンネル中19チャンネル実装済み (**100%**)。#125で欠損9チャンネルを追加。

プロトコル改善 (#132):
- OAuth2スコープに基づくチャンネルアクセス制御
- `pong`応答の実装
- パラメータバリデーション強化

## mk-go 独自の挙動 / 拡張

upstream Misskey TS と異なる、mk-go ならではの実装:

### RemoteStatsFetcher (#943)

Misskey TS の `users/show` は **自インスタンスで観測した範囲** のみで notesCount / followersCount / followingCount を集計するため、リモートユーザーの数値が実体より小さく表示される。mk-go は user.Host が non-local の場合、 origin instance の `/api/users/show` を https POST で叩いて公開 counts を取得し、上書き表示する。

- 1 時間 TTL の LRU cache (size cap 10000、`hashicorp/golang-lru/v2`、#945)
- SSRF guard: `safehttp.NewSSRFSafeTransport` 経由 (private IP / metadata service block)
- host validation: URL injection (`/`, `?`, `#`, `@`, ` ` 混入) を url.Parse で reject
- 失敗時は silent fallback で local 観測値を維持
- フォロー一覧 / フォロワー一覧 endpoint は **自インスタンス上に存在する関係のみ** (= 数値だけ remote、一覧は local の非対称設計)

### mediaproxy のアニメ pass-through (#941)

`?emoji` / `?avatar` / `?preview` mode で gif / apng / vnd.mozilla.apng を **decode → resize しない** で raw bytes pass-through する。Go std の `image.Decode` は 1 frame しか返さないため resize 経路に乗せると静止画化されるのを回避。`?static` / `?badge` mode は明示的な静止画要求なので従来通り decode。

### URL preview の charset 自動正規化 (#942)

`internal/core/urlpreview/fetcher.go` で `golang.org/x/net/html/charset.NewReader` 経由で response の Content-Type charset と HTML `<meta charset>` を自動判定して UTF-8 に正規化。Shift_JIS / EUC-JP / ISO-2022-JP の日本語ページで title / description が文字化けしない。

### inbox processor の verify-in-worker 化 (#565)

upstream は HTTP handler の中で AP signature verify を同期実行するため、悪意ある unsigned activity が手前で増えると HTTP 受信スループットが低下する。mk-go は HTTP handler は body + signature header だけを payload に詰めて 202 即返し、signature verify / host block / instance touch / chart hook を inbox worker (asynq processor) 側で実行することで HTTP 受信 rps が **TS の 2.6-2.8x** (queue-bench で確認)。

### mkq driver default (#571 audit)

job queue driver の既定を asynq から mkq (BullMQ-compatible Go ライブラリ) に変更。queue-bench (`tests/queue-bench/`) で BullMQ / asynq / mkq を 3-way 比較した結果、deliver throughput / inbox throughput ともに mkq が最良。

## エラーレスポンス

#130でエラーレスポンスを標準化:
- 同一エラーコードに対するUUIDを統一
- ヘルパー関数を統合

## 設定ファイル互換性

TS版の`.config/default.yml`をそのまま使用可能。以下の設定もGo版で対応済み:
- 基本接続 (DB, Redis, URL, port)
- trustProxy (#129)
- dbSlaves (#133)
- 各種Redis分離設定
- jobQueueDriver (`mkq` 既定 / `asynq` 選択可、#571)
- allowedPrivateNetworks (SSRF allowlist、開発時の self-loop 許可用)

## DB構造

### テーブル

Go側のマイグレーション (000001〜) はTS版テーブルに対して追加のみで破壊的変更を行わない。TS版のマイグレーションで作成される全テーブルは維持される。

Go版で追加したテーブル:
- `password_reset_request` — パスワードリセット要求
- `signin` — ログイン履歴
- `channel_favorite`, `channel_muting` — チャンネルお気に入り/ミュート
- `clip_favorite`, `user_list_favorite` — クリップ/リストお気に入り
- `retention_aggregation` — リテンション統計
- `system_account` — システムアカウント
- `used_username` — ユーザー名再利用防止
- `note_thread_muting` — スレッドミュート

drop-in テスト (#367) で発見した補完カラム:
- `note.pageCount` (= `note_id` 配下の page 数キャッシュ) を `migration/000039_dropin_compat.up.sql` で追加
- `note.renoteChannelId` を同 migration で追加

### 既知のカラム差分

| テーブル | 状況 |
|---|---|
| `user_profile` | followedMessage, lang, publicReactionsは#131で追加済み |
| `note` | appId (App連携識別)、score (ノートスコア) はGo版では未使用 |
| `abuse_user_report` | resolvedAsのサイズ差 (Go: varchar(16), TS: varchar(128)) |

## 既知の制限

- **Identicon の外見** — TS版と生成アルゴリズムが異なるため、アイコン未設定ユーザーの表示が異なる
- **chat/* の API 設計** — TS版とパス名・パラメータが異なる (mk-go は独自設計)
- **search backend** — Meilisearch 必須 (mk-go の DB SQL LIKE fallback は upstream に揃え無効化済、#877)
- **2026.4.0 / 2026.5.0 / 2026.5.1 由来の追従未対応** — `#947` tracker 配下、`docs/update/2026050{0,1}diff.md` 参照
  - 連合互換性に直接関わるもの (alsoKnownAs / actor 正規化 / リレー Announce / etc.) は高優先 sub-task として個別 PR 化中

詳細は[TS版からの移行ガイド](migration-from-ts.md)の「既知の制限」セクションも参照。

## 関連 issue / tracker

- #107 — 第1次互換性調査 (API/DB/挙動の3軸)
- #124 — 第2次互換性調査 (6軸: +WebSocket, エラー, 設定)
- #744 — Playwright e2e Phase 1-4 tracker (54.3% endpoint coverage 達成)
- #947 — Misskey TS 2026.3.2 → 2026.5.1 への upstream 追従 tracker
- #949 — ドキュメント更新親 issue (本 doc を含む)
