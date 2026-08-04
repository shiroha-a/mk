# 純正 Misskey との差分カタログ

mk-go が持つ「純正 Misskey (misskey-dev/misskey) には無い、または挙動が異なる」ものを 1 枚に集約したリファレンス。

- 基準: **mk-go 1.0.0** (= Misskey TS `2026.7.0` 追従完了時点) ⇔ Misskey TS `2026.7.0`
- 最終更新: 2026-08-03

> 注: `MisskeyVersion = 2026.7.0` と `third_party/misskey` submodule (`2026.7.0-mk.4`) は bump 済み。`MkGoVersion` 定数の `1.0.0` 化はリリース作業 (別 issue) で行うため、この時点のコードでは `0.9.2` のままになっている。本ドキュメントの内容は 1.0.0 として固定するベースラインそのもの。

## このドキュメントの位置づけ

mk-go は drop-in 互換 (同じ DB / Redis / frontend を Misskey TS と共有し、backend だけ差し替えられる) を最優先とする。したがって「差分」は無条件に悪ではなく、次の 4 種類に分かれる。

| 分類 | 意味 | 扱い |
|---|---|---|
| **cherrypick 由来** | yojo-art/cherrypick 系列が純正に加えた拡張を mk-go が取り込んだもの | 維持。vanilla misskey-js golden で厳密 gate しない |
| **mk-go 独自** | mk-go が独自に足した機能 (additive、wire 互換を壊さない) | 維持 |
| **安全側 divergence** | upstream より厳しい / 正確な挙動 | 維持し理由を明記 ([[feedback_parity_mkgo_better_keep_document]] 方針) |
| **未実装 / 欠落** | upstream にあって mk-go に無い | issue 化して解消する |

1.0.0 = Misskey 2026.7.0 追従完了。**ここで drop-in 互換をベースラインとして固定し、以降 frontend の独自進化を解禁する。** 本ドキュメントはその「固定したベースラインからの距離」の一覧であり、1.0.0 時点のスナップショットとして機能する。

以降 upstream を追従するときは、本ドキュメントとの差分が新たな divergence になる。追従手順は [upstream-catch-up.md](upstream-catch-up.md) を参照。

## サマリ

| 軸 | mk-go 独自 | cherrypick 由来 | 未実装 |
|---|---|---|---|
| API endpoint | GET variant 23 + alias 3 | chat 15 | **0** |
| API レスポンスの additive field | 3 (`runtime` / `mkGoVersion` / `chunkedUpload`) | reversi packed game の `crc32` 等 | — |
| DB テーブル | 6 (+ bookkeeping 2) | 0 | 0 |
| DB カラム | 10 (+ 未使用の残存列 3) | 3 | 0 |
| ActivityPub | Ed25519 / RemoteStatsFetcher ほか | reversi 連合 / chat 連合 | — |
| config キー | 20 前後 | 0 | — |
| fork frontend の独自変更 | 7 tag (`-mk.1` ～ `-mk.7`) | — | — |

**upstream endpoint の未実装はゼロ** (coverage 99.8%、残 1 件は TestMode 限定登録の偽陽性)。DB schema も upstream の全テーブル・全共有カラムを superset で保持しており、逆方向の欠落は無い。

---

## 1. API endpoint

upstream の endpoint は `endpoints/` 配下 438 件 + `ApiServerService.ts` の fastify 直登録 6 件 (POST 5 / GET 1) = **444 件**。うち **443 件を実装済み (coverage 99.8%)**。

### 1-1. mk-go にしかない (41)

| 分類 | 件数 | 内容 |
|---|---|---|
| GET variant 追加 | 23 | `charts/*` 12 件、`emoji` / `emojis` / `federation/instances` / `federation/stats` / `fetch-rss` / `get-online-users-count` / `hashtags/trend` / `notes/featured` / `notes/reactions` / `server-info` / `bubble-game/ranking`。**対応する POST は両側にある**。ブラウザから直接叩く利便目的 |
| cherrypick chat 拡張 | 15 | `chat/messages` / `chat/messages/create` / `read` / `update` / `reactions/create` / `reactions/delete`、`chat/rooms/joined` / `unmute` / `transfer-ownership` / `members/ban` / `members/update-membership` / `invitations/accept` / `delete` / `reject`、`chat/unread-count` |
| その他 / alias | 3 | `i/flashs` / `i/flashs/likes` (upstream の `flash/my` / `flash/my-likes` に対する mk-go 側の path alias。両者とも mk-go に実装済み)、`signin` (upstream が `signin-flow` に統合した旧 path の backward-compat shim) |

**reversi は endpoint レベルの差分ゼロ。** mk-go の 7 本 (`games` / `invitations` / `show-game` / `match` / `cancel-match` / `surrender` / `verify`) は upstream 2026.7.0 と完全一致。`crc32` カラムと `reversi/verify` も upstream 標準 (`models/ReversiGame.ts` / `endpoints/reversi/verify.ts`)。**cherrypick 由来の拡張は ActivityPub 層と、packed game レスポンスに `crc32` 等を additive に載せる点に現れる** (§3-1 参照)。

### 1-1b. レスポンスの additive field

| endpoint | field | 内容 |
|---|---|---|
| `admin/queue/queues` / `admin/queue/queue-stats` | `runtime` | worker 現在数 / auto-scale 範囲・有効性 / dispatch wait・processing の分位数 / 直近失敗数 / scale 履歴。upstream は worker 数を静的 config でしか持たず該当情報が無い。provider 未配線・未知 queue では block ごと省く (#2277) |
| `/api/meta` (+ SSR 埋め込み meta) | `mkGoVersion` | mk-go の実装バージョン。`version` は drop-in 互換のため**互換 Misskey バージョン**を返す契約 (第三者クライアントの feature detection / frontend `_error_.vue` の版ずれ検出が依存) なので別 field にした (#2274) |
| `/api/meta` | `chunkedUpload` | 分割アップロード (#2313) の能力告知。`{ chunkSize }` を返す。**未対応構成 (オブジェクトストレージ未使用 / `meta.chunkedUploadEnabled=false`) では field ごと出さない**ので、純正 Misskey と同じく `undefined` になりクライアントは単発アップロードにフォールバックする |

### 1-2. 未実装 (0)

**upstream endpoint の未実装はゼロ。** 最後まで残っていた `GET /api/v1/instance/peers` (Mastodon 互換の連合ピア一覧) は #2245 で実装した。upstream は `ApiServerService.ts` で fastify 直登録しており `endpoints/` 配下に無いため、matrix 生成ツールの file-walk から漏れて長らく不可視になっていた (現在は `ApiServerService.ts` を正規表現で直接読むので追随漏れが起きない)。

`docs/api-compat.md` に残る「TS only (mk-go 未実装) 1」= `/api/reset-db` は**偽陽性**。mk-go では `config.TestMode` 時のみ登録されるが、matrix 生成は default config で route dump するため未実装に見える。

---

## 2. DB schema

**逆方向の欠落はゼロ** — upstream の `@Entity` 76 テーブルと全共有カラムを mk-go が superset で保持している。

### 2-1. mk-go 独自テーブル (8)

| テーブル | 由来 | 理由 |
|---|---|---|
| `user_keypair_extra` | mk-go 独自 | local user の Ed25519 鍵ペア。既存 `user_keypair` (RSA) を touch せず別テーブルに分離し、**TS へ swap back しても壊れない**設計 |
| `user_publickey_extra` | mk-go 独自 | remote user の追加公開鍵。actor JSON の `assertionMethod[]` (FEP-521a Multikey) を keyId 単位で保持 |
| `antenna_note_unread` | mk-go 独自 | per-user per-note の antenna 未読 |
| `channel_note_unread` | mk-go 独自 | channel follower の未読追跡 |
| `chunked_upload_session` | mk-go 独自 | 分割アップロード (#2313) の進行中セッション。S3 の `UploadId` はここでだけ保持しクライアントには露出しない。`user` への FK は張らない — CASCADE で行だけ消えると `AbortMultipartUpload` されない未完了マルチパートアップロードが孤児として課金され続けるため、期限切れ GC に回収させる |
| `note_unread` | 準・独自 | upstream DB にも legacy 遺物として残るが 2026.7.0 の `models/` に entity は無く参照 0 件。mk-go はこれを実用し `/api/i` の `hasUnreadSpecifiedNotes` / `hasUnreadMentions` を Redis stream を舐めずに解決する。upstream legacy 版にある `noteChannelId` は mk-go の定義に無い (TS 製 DB では `CREATE TABLE IF NOT EXISTS` が no-op なので実害なし) |
| `migrations` | drop-in 互換 | TypeORM の bookkeeping。mk-go 由来 DB に TS を後から繋いだ時に migration を再実行させないための seed。name は本家と同じ `ClassName+timestamp` 形式で 346 件を保持する (#2244 で短縮形から是正)。漏れは `TestMigrationSeed_CoversUpstream` が CI で検出する |
| `schema_migrations` | tooling | golang-migrate 用 |

`__chart__*` / `__chart_day__*` 24 テーブルは独自ではない (upstream では `models/` ではなく `core/chart/charts/entities/` で定義されるため、`models/` だけを見ると誤検出する)。

### 2-2. 独自カラム (16 = 実使用 13 + 未使用の残存 3)

うち **mk-go が実際に読み書きするのは 13 件** (cherrypick 由来 3 + mk-go 独自 10)。残り 3 件は fresh な mk-go DB に列だけ残る未使用列で、#2243 で依存を外した。

| テーブル | カラム | 由来 | 理由 |
|---|---|---|---|
| `chat_message` | `emojis` / `isDelivering` / `isDeliverFailed` | cherrypick | 連合配送の状態追跡 |
| `user` | `isRoot` | mk-go 独自 | upstream は system_account 移行で DROP 済み。`role.Service.isRootUser` の fallback に必要 |
| `meta` | `proxyAccountId` | mk-go 独自 | 同じく upstream は DROP 済み。`admin/update-proxy-account` が書き込む |
| `note_favorite` | `createdAt` | mk-go 独自 | upstream は `deleteCreatedAt` で DROP 済み。`/api/i/favorites` の response 要件で復活 |
| `app` / `auth_session` | `createdAt` | 列のみ残存 | upstream は `deleteCreatedAt` で DROP 済み。mk-go も **読み書きしない** (#2243 で model から除去)。fresh な mk-go DB には列が残るが未使用 |
| `clip` | `notesCount` | 列のみ残存 | 旧・非正規化カウンタ。#2243 で撤去し、件数は upstream 同様 `clip_note` の実カウントで算出する |
| `poll` | `notifiedAt` | mk-go 独自 | pollEnded 通知の二重送信防止 |
| `user_pending` | `invitationTicketId` | mk-go 独自 | 1 招待で複数アカウントを作れる gap を塞ぐ |
| `meta` | `chunkedUploadEnabled` / `chunkedUploadChunkSizeMb` / `chunkedUploadSessionTtlMinutes` / `chunkedUploadMaxSessionsPerUser` / `chunkedUploadMaxPendingMbPerUser` | mk-go 独自 | 分割アップロード (#2313) の設定。既存の `objectStorage*` と同じくコントロールパネルから編集する。TS は未知の列を無視するので drop-in の復路は壊れない |

`relay_observed_user` (#2340) は mk-go 独自テーブル。リレー経由で初めて観測した remote user を記録し、孤児掃除の対象をリレー由来に限定する。**`user` に列を足さず別テーブルにしてある**: TS は未知の列も無視するので列追加でも復路は壊れないが、別テーブルなら TS 側から一切見えず `check-migrations` にも差分が出ない。`user` は連合・認証・API のあらゆる経路が触るホットテーブルでもあるため、触らずに済ませる。

### 2-3. index の差分

| index | 差分の内容 |
|---|---|
| `chat_message(fromUserId, toUserId)` 複合 | **upstream に無い** (upstream は各列の単独 `@Index()` のみ)。mk-go 独自の最適化 |
| `drive_file(url)` / `drive_file(webpublicUrl)` / `drive_file(thumbnailUrl)` | **upstream に無い**。後 2 者は partial |
| `clip_favorite(clipId)` | **upstream に無い** (upstream は `UNIQUE(userId, clipId)` と `userId` 単独 index のみで、`clipId` 先頭の index が無い) |
| `user.tags` の GIN | upstream は btree (`@Index()`) で配列 containment に効かないため GIN に変更 |
| `drive_file(uri)` | upstream にも index がある。mk-go の差分は **partial (`WHERE uri IS NOT NULL`) にしている点と index 名** |
| `note.uri` UNIQUE | upstream にも UNIQUE がある。差分は **partial にしている点と index 名** |
| `note.tags` / `note.mentions` / `note.fileIds` の GIN | **upstream にも同じ GIN がある** (`IDX_NOTE_TAGS` / `IDX_NOTE_MENTIONS` / `IDX_NOTE_FILE_IDS`)。mk-go は名前だけが違う (`IDX_note_tags` / `IDX_note_mentions` / `IDX_note_fileIds` — case に加え `fileIds` は語区切りも異なる) |

#### index 命名の非対称と、その解消 (#2246)

mk-go は index を `IDX_<table>_<col>` で命名するが、upstream は TypeORM 生成の hash 名 (`IDX_e5848eac...`) を使う。`CREATE INDEX IF NOT EXISTS` は **index 名**で存在判定するため、定義が同一でも名前が違えば新規作成され、TS 製 DB では index が全面的に二重化していた。

Misskey TS 2026.7.0 が作った DB に mk-go の全 migration を適用した実測:

| | index 数 |
|---|---|
| TS のみ | 442 |
| mk-go migration 適用後 | 639 (+197) |
| `000068_drop_redundant_indexes` 適用後 | **474** (165 本を削除、upstream 由来の削除は 0 本) |

`000068` は「mk-go の migration が作る index のうち、同一テーブルに定義が一致する upstream 由来の index が存在するもの」だけを実行時に落とす。**upstream 由来の index には一切触れない** (TS へ戻したとき本家が再作成できず復路が壊れるため)。mk-go 由来 DB では同一定義の別名 index が無いので何も落ちない。

上表の partial 化 3 件 (`note.uri` / `drive_file(uri)` / `user(usernameLower, host)`) も、upstream の full index が同じ役割を果たすと個別に判断して削除対象に含めている。一方 `note_unread` の partial 2 件と `user.tags` の GIN、`user(usernameLower) WHERE host IS NULL` は **意図的に定義が違う**ので残す。

新規に同型の重複を作らないよう、`TestIndexNaming_NoNewUpstreamDuplicates` が CI で検出する。upstream に同内容の index があるなら **upstream の index 名をそのまま使う** (`000058_channel_muting_expires_at.up.sql` が前例)。

#### migration の冪等性

mk-go の migration は TS 製の既存 DB にも流れるため、`CREATE TABLE` / `ADD COLUMN` / `CREATE INDEX` は `IF NOT EXISTS`、`DROP *` は `IF EXISTS` が必須。欠けると upstream が既に作った構造と衝突して migration が dirty 停止し、**drop-in 手順そのものが完走しない**。実際 `000048` が upstream 2026.5.0 の `AddCategoryToAvatarDecorations` と衝突しており、2026.5.0 以降の TS 製 DB からの drop-in が不可能だった (#2246 で修正)。`TestMigrationIdempotency_RequiresIfExists` が CI で強制する。

同じ「`IF NOT EXISTS` が drop-in で意図どおり効かない」クラスとして、`CREATE TABLE` 内でしか定義されていない upstream 非存在カラムも問題になる (TS 製 DB では列が生えない)。`TestSchemaDrift_CreateOnlyColumns` が検出する (#2243)。

---

## 3. ActivityPub

### 3-1. reversi / chat の連合まわり

cherrypick 由来の拡張が中心。比較のため関連する upstream 標準機能も併記する。

| 項目 | 実装 | 内容 |
|---|---|---|
| **reversi 連合対戦** | `core/reversi/federation.go` | 固定 `GameTypeUUID = 1c086295-...` を持つ独自 AP object `{type:"Game", game_type_uuid, extent_flags, game_state{...}}`。`game_state.type` は settings / ready_states / putstone |
| reversi 盤面 CRC32 | `core/reversi/game.go` / `service.go` / `api/reversi/handler.go` | DB カラムと `reversi/verify` は **upstream 標準** (`MiReversiGame.crc32`)。ただし **packed game に `crc32` を載せるのは mk-go (cherrypick 系統) 側の拡張** — upstream の `ReversiGameEntityService.packDetail` / json-schema には無い |
| reversi の pack 粒度 | `api/reversi/handler.go` | upstream の `reversi/games` は Lite (`packLiteMany`、`form1`/`form2`/`logs`/`map` を含まない) を返すが、mk-go は cherrypick 系統 + 連合拡張を持つため**全 endpoint で Detailed 相当の `packGame` を共有**する (#2106 L15)。上記 crc32 と併せて reversi は vanilla golden gate の対象外 ([shape-drift.md](shape-drift.md)) |
| reversi 受信 dispatch | `core/federation/reversi_inbox.go` | `invite` / `join` / `leave` を受信。Invite 受信時にローカル game 行を自動作成、session→gameID は Redis mapping。**招待を受ける側は純正 frontend でも表示できる**ので、fork 側の変更は招待を出す側 (対戦相手選択) のみ (#2270) |
| `reversiVersion` (nodeinfo) | `api/nodeinfo/handler.go` | CherryPick 側がメジャーバージョン一致で連合可否を判定するため 1.1.x を維持 |
| **1-on-1 chat 連合** | `activitypub/renderer.go` / `core/chat/service.go` | DM を `Create + Note(_misskey_talk: true)` で配送。未対応実装では単なる Note として黙殺される設計。**純正は `core/ChatService.ts:381-384` で remote 配送がコメントアウトされており federation しない**ため、純正 frontend は remote を一律ブロックしていた (fork 側で解禁、#2270) |
| **group chat room 連合** | `activitypub/renderer.go` / `core/federation/chat_room_inbox.go` | chat room を AP `Group` object (`https://host/chat/rooms/{id}`) として Invite / Accept / Reject / Remove。room owner が local の場合のみ Invite を署名配送する (remote owner の room は秘密鍵が無い) |
| `_misskey_canChat` | `activitypub/types.go` / `core/federation/resolver.go` | chat 連合の capability flag。欠落時は everyone 扱い (chat 非対応実装を "none" に倒すと送信前 reject で UX が悪化するため、安全側ではなく寛容側に倒す判断)。`false` は `user.chatScope = "none"` に落とし、fork frontend はこれを見て「相手が受け付けない」表示にする |

### 3-2. Ed25519 / FEP-521a (mk-go 独自)

| 項目 | 実装 | 内容 |
|---|---|---|
| Multikey encode/decode | `activitypub/multikey.go` | `z` + base58btc(`0xed 0x01` ‖ 32 byte) の FEP-521a / W3C VC Data Integrity 形式 |
| `assertionMethod[]` 出力 | `activitypub/renderer.go` | Ed25519 鍵を持つ local user に `#ed25519-key` fragment の Multikey を expose |
| 受信側 capability 判定 | `core/federation/deliver_service.go` | `user_publickey_extra` に Ed25519 行があれば Ed25519 署名。未配線 / DB error / 行なしは**すべて RSA へ安全側 fallback**。TTL 5min cache + singleflight |

e2e は `make dropin-fedibird-test` (Fedibird-like mock との双方向 Ed25519 verify)。

### 3-3. その他

| 項目 | 実装 | 分類 |
|---|---|---|
| `RemoteStatsFetcher` | `core/federation/remote_stats.go` | **mk-go 独自**。remote user の notesCount / followersCount / followingCount を origin の `/api/users/show` から取得。LRU 10000 / positive TTL 1h / negative 5min、SSRF guard 経由、失敗時 silent fallback |
| inbox admission | `activitypub/inbox_admission.go` | **upstream と同等** (`ActivityPubServerService.inbox` も 4 header 要求 + Host 一致 + SHA-256 照合を実施)。mk-go 固有なのは body 照合を定数時間比較にしている点のみ |
| 軽量 JSON-LD 正規化 | `activitypub/jsonld.go` | mk-go 独自実装。json-gold のフルパイプラインを避け、Mastodon 系 prefix / IRI 直記述 / type 配列 / 言語マップを canonical 短形式に揃える。CherryPick group chat 用 `@context` は破棄せず保持 |
| Collection unroll 制限 | `core/federation/processor.go` | 安全側。深さ 1、item の host 一致を要求 (spoofing 防止)、URI 文字列 item は fetch 増幅回避で skip |
| `published` の異常値 fallback | `core/federation/published_time.go` | mk-go 独自 hardening。clock skew 5min / 過去 10 年 floor |
| outbound User-Agent | `config/config.go` | `mk-go/<ver> (<url>)` |

---

## 4. 設定ファイル (YAML) の独自キー

| キー | 用途 |
|---|---|
| `jobQueueDriver` | queue 実装選択。`mkq` (既定・BullMQ wire 互換) / `asynq` (legacy、廃止予定)。未知値は起動時 error |
| `jobQueueAutoScale` / `maxWorkers` / `minWorkers` / `maxWorkersGlobal` / `autoScaleCooldownSeconds` | AIMD auto-scale controller。`mkq` driver のみ |
| `deliverJobKeepFailed` / `inboxJobKeepFailed` / `deliverJobKeepCompleted` / `inboxJobKeepCompleted` | queue bucket の retention 件数 |
| `nsfwDetectorUrl` / `nsfwDetectorAuthHeader` / `nsfwDetectorTimeout` | mk-go 独自の汎用 NSFW detector 契約 (`POST` 生バイト → `{"score": float64}`)。**upstream 2026.7.0 の公式 sensitive-detector (meta 駆動) が未設定のときの fallback** |
| `videoThumbnailGeneratorMode` | `post` (既定、multipart POST) / `get` (Misskey TS 仕様互換) |
| `mediaProxySecret` | mediaproxy URL の HMAC 署名鍵 |
| `disableEndpointRateLimits` | bench 用。有効時に起動 warn |
| `testMode` / `enablePprof` / `enableMetrics` | 破壊的 endpoint / pprof / Prometheus の有効化。いずれも起動時 warn |
| `enableTimelineCache` / `timelineCacheTtlSeconds` | TL 1 ページ目の viewer 別短 TTL cache (opt-in) |
| `db.maxOpenConns` ほか pool tuning / `redis*.poolSize` | Go 固有 |
| `redis*.path` | ioredis 互換の UDS alias。同じ config を TS/mk で共有する drop-in 切替のため |
| `MK_*` 環境変数オーバーライド | upstream に同等機構なし |

逆方向 (upstream にあって mk-go に無い): `threadPoolSize`、`logging.format` / `logging.level` / `logging.domains` / `logging.access` (2026.7.0 のログ基盤刷新分。`logging.sql.*` は mk-go にもある)、`sentryForBackend.disabledIntegrations`。

---

## 4-2. fork frontend の独自変更

`third_party/misskey` fork (`shiroha-a/misskey-ts`) に載せている frontend の custom commit。純正へ還元できない (= 純正 backend が対応しない) ものだけを置く方針。

| tag | 内容 |
|---|---|
| `2026.7.0-mk.0` | `MkModal` の content children[0] null guard |
| `2026.7.0-mk.1` | mk-go が実装済みの chat / reversi 連合を UI で解禁 (#2270) |
| `2026.7.0-mk.2` | 自動生成した VAPID 鍵を admin 画面へ即時反映 (#2272) |
| `2026.7.0-mk.3` | バージョン表示を mk-go の実装版にする (#2274) |
| `2026.7.0-mk.4` | job queue の worker runtime (auto-scale / 遅延) を admin UI に表示 (#2277) |
| `2026.7.0-mk.5` | mk-go 向けフロントエンドアセット専用イメージを publish する CI (#2306)。frontend の挙動は変えず、`Dockerfile.assets` + workflow の追加のみ |
| `2026.7.0-mk.6` | 分割アップロードへの対応 (#2314) |
| `2026.7.0-mk.7` | ジョブキューのタブを mk-go の queue 構成に合わせる (#2323) |
| `2026.7.0-mk.8` | `objectStorage` queue の追加に伴うタブの追従 (#2325) |
| `2026.7.0-mk.9` | リレー投稿の揮発化設定をリレー画面に追加 (#2335) |
| `2026.7.0-mk.10` | リレー由来ユーザーの整理設定を追加し、表示上の実装用語を平易な表現に置換 (#2340) |

`2026.7.0-mk.1` の内訳:

| 箇所 | 変更 |
|---|---|
| `pages/chat/room.vue` | 「相手のアカウントで DM が使えない」warning の条件から `host !== null` を外し `chatScope === 'none'` で判定。room 招待の相手選択から `localOnly` を外す |
| `pages/chat/home.home.vue` | チャット開始の相手選択から `localOnly` を外す (純正の `// TODO: localOnly は連合に対応したら消す` の解消) |
| `utility/get-user-menu.ts` | 「チャットする」を `host == null` で隠すのをやめる |
| `pages/reversi/index.vue` | 対戦相手選択から `localOnly` を外す |

`2026.7.0-mk.2` の内訳:

| 箇所 | 変更 |
|---|---|
| `pages/admin/settings.vue` | Service Worker 設定の保存後に `admin/meta` を引き直してフォームへ書き戻す。`update-meta` は 204 で生成鍵を返さず `meta` はページ表示時の 1 回しか読まないため、放置すると入力欄が空のままになり (a) 生成された公開鍵を確認できない (b) 次の保存で空文字が再送されて鍵が作り直され既存の push 購読が全部無効になる |

`2026.7.0-mk.6` の内訳:

| 箇所 | 変更 |
|---|---|
| `utility/drive.ts` | `/api/meta` が `chunkedUpload` を告知しており、かつファイルサイズが告知された `chunkSize` を超える場合に分割アップロード経路 (`drive/files/create-chunked/*`) を使う。閾値もチャンクサイズもサーバー告知に従い、フロントエンドにハードコードしない (S3 互換サービスごとに最小パートサイズや「最終パート以外は同一サイズ」等の制約が異なるため)。進捗はチャンク合算で単発と同じ体験を維持し、中断時はセッション破棄 API を呼ぶ。エラーダイアログの分岐は単発経路と共通化 |
| `pages/admin/object-storage.vue` | 分割アップロードの有効/無効・チャンクサイズ・セッション有効期限を追加。`admin/meta` に該当 field が無い純正 backend では UI ごと隠す。**チャンクサイズがリバースプロキシの上限を超えていると有効にしても失敗する**旨を警告として表示 |
| `locales/ja-JP.yml` | 分割アップロード固有のエラー / 設定文言 (`_chunkedUpload`) |

純正 Misskey には分割アップロードの backend が無いため `chunkedUpload` は常に `undefined` になり、従来の単発アップロードに倒れる。

チャット / reversi の解禁 (`-mk.1`) はいずれも純正は backend が federation しない (`core/ChatService.ts` の remote 配送はコメントアウト) ため、純正へ PR しても意味がない。upstream 追従時は cherry-pick で持ち越す。

`2026.7.0-mk.3` の内訳:

| 箇所 | 変更 |
|---|---|
| `pages/about.overview.vue` | サーバー情報に `mk-go` 行を追加。Misskey 欄の値を build 時定数から `instance.version` (サーバー申告値) に変更 |
| `pages/about-misskey.vue` | Misskey の版の下に `mk-go vX.Y.Z` を併記。本ページは Misskey 本体の説明なので見出しは Misskey のまま |

いずれも `mkGoVersion` が無い場合 (純正 backend) は従来表示へフォールバックする。

`2026.7.0-mk.4` の内訳:

| 箇所 | 変更 |
|---|---|
| `pages/admin/job-queue.vue` | queue 一覧カードに Workers 行、Overview に auto-scale 状態 / dispatch wait / processing の p50・p95 / 直近失敗数 / scale 履歴を追加 |

`runtime` block が無い応答 (純正 backend) では該当 UI を出さない。

---

## 4-3. job queue の構成差分

upstream は用途ごとに **10 queue** に分けるが、mk-go は **7 queue** に集約している (`internal/queue/driver/mkqdriver` の `QueueNames`)。処理する仕事は同じで、束ね方だけが違う。

| upstream の queue | mk-go の実体 |
|---|---|
| `deliver` | `deliver` |
| `inbox` | `inbox` |
| `system` | `maintenance` (cron 群: chart tick/resync/clean, checkExpiredMutings, clean, cleanRemoteNotes, checkModeratorsActivity, instanceRefresh, retentionAggregate, chunkedUploadGc) |
| `endedPollNotification` | **queue ではなく常駐 goroutine** (`corepoll.ExpiryWorker`、60 秒間隔) |
| `postScheduledNote` | `deliver` の `note:postScheduled` |
| `db` | `export` の `export` / `import` / `importCustomEmojis`、`deliver` の `maintenance:deleteAccount` |
| `relationship` | `deliver` の `relationship:{follow,unfollow,block,unblock}` |
| `userWebhookDeliver` | `webhook` の `webhook:user` |
| `systemWebhookDeliver` | `webhook` の `webhook:system` |
| `objectStorage` | `objectStorage` |
| — | `push` (Web Push 配信、upstream は system queue 内で処理) |

`objectStorage` は `deleteFile` / `cleanRemoteFiles` とも upstream と同じ job 構成 (#2325)。振り分けも upstream に揃えてあり、ローカル FS 保存 (`storedInternal=true`) の実体は同期削除、object storage 上の実体だけを queue に逃がす。`clean-remote-files` は「job 1 本が内部でバッチ削除を回す」形も upstream と同じで、リモートキャッシュの件数ぶん job を積んで Redis を圧迫することはない。

`note:postScheduled` / `maintenance:deleteAccount` / `relationship:*` が task type の接頭辞と違う `deliver` に載っているのは意図的なもの。いずれも実行結果が連合配送につながるジョブで、worker 2 本の `maintenance` より 16 本の `deliver` の方が捌ける。task type と queue の対応は `internal/queue/routing_test.go` が表として固定しており、変えると落ちる (#2327)。

worker 数だけは upstream の 16 に対し mk-go は 4。実体削除は S3 への I/O 待ちが主で 1 worker あたりの効率が良く、一括削除の並列度を job 数で稼ぐ設計でもないため、`deliver` と同じ理由 (worker 数 ≒ Redis 接続数) で抑えている。

再試行は **mk-go の方が手厚い**。upstream は `attempts` を設定しないので単発試行で終わり、失敗した実体は failed job として残るだけで自動復旧しない。mk-go は指数バックオフ付きで 4 回まで再試行する (object storage の一時的な 5xx / タイムアウトは待てば回復するため)。queue 自体が使えないときは同期削除にフォールバックし、実体を取りこぼさない。

**管理画面のタブはこの構成に合わせて fork 側で書き換えている** (`misskey-js` の `queueTypes`、`2026.7.0-mk.8`)。upstream のタブは API 応答ではなくこの定数から生成されるため、書き換えないと mk-go に存在しない 8 タブが常時ゼロ表示になり、実在する `push` / `export` / `webhook` / `maintenance` / `objectStorage` が画面から見えなくなる (#2323)。**mk-go の queue を増減したら fork の `queueTypes` も合わせること。**

## 5. 運用・性能機能 (mk-go 独自)

| 項目 | 内容 |
|---|---|
| inbox verify-in-worker 化 | HTTP handler は body + signature header を payload 化して即 202、署名 verify / host block / instance touch は worker 側。HTTP 受信 rps が **TS の 2.6〜2.8 倍** |
| mkq queue driver | BullMQ wire 互換の Go 実装。queue-bench で BullMQ / asynq / mkq を 3-way 比較 (送信 rps は mkq 優位、drain time は asynq 優位。詳細は [queue-bench.md](queue-bench.md)) |
| AIMD auto-scale worker | per-queue の動的 Resize + Prometheus metrics。worker 現在数 / 範囲 / scale 履歴は admin UI にも出す (#2277) |
| Prometheus `/metrics` | `mk_job_workers_active` / `mk_job_queue_pending` / `mk_job_dispatch_wait_seconds` ほか。**無認証公開なので LB/nginx ACL 必須**。admin から読めない分は `admin/queue/*` の `runtime` block が補う (#2277) |
| timeline JSON cache | first-page per-viewer cache (opt-in) |
| mediaproxy のアニメ pass-through | `?emoji` / `?avatar` / `?preview` で gif/apng を decode せず raw 返し (Go std の `image.Decode` は 1 frame しか返さず静止画化するため) |
| URL preview の charset 自動正規化 | Content-Type + `<meta charset>` から UTF-8 化。Shift_JIS / EUC-JP / ISO-2022-JP で文字化けしない (upstream は外部 `summaly` package に委譲しているため同等機能の有無は未確認) |
| instance touch buffer | 同一 remote host の連続 inbox 受信を集約。**upstream も `CollapsedQueue` で per-host に集約している**。差分は flush 窓が mk-go 1s / upstream 5 分という点だけ |
| chart tick の DB 再集計 | **upstream も同機構を持つ** (`TickChartsProcessorService` / `ResyncChartsProcessorService`)。mk-go は cron 実装が異なるだけで差分ではない |
| VAPID 鍵の自動生成 | Service Worker 有効化時に鍵が両方空なら生成して meta に注入。operator 指定鍵は尊重。明示的な空 / null 送信は「ローテーション指示」として扱い再生成する。fork frontend は保存後に `admin/meta` を引き直して生成鍵を表示する (#2272) |
| `+host` / `-host` sort key | `federation/instances` の host 昇順/降順 |
| `notes` の noteIds bulk lookup | upstream の public-note timeline に加え `{noteIds:[...]}` bulk (max 100、visibility filter 付き) を同 endpoint で両立 |
| `webpublicUrl` | drive entity の拡張 field (proxy 化済で IP leak なし) |
| mention による reply filter escape | viewer が `note.mentions` に含まれれば withReplies 設定に関係なく reply gate を pass。streaming と fanout の両方に実装 |

---

## 6. セキュリティ関連の差分

| 項目 | upstream | mk-go |
|---|---|---|
| drive requestHeaders の credential 除去 | 全 header を生保存 (`drive/files/create.ts`) | `authorization` / `cookie` / `set-cookie` / `x-api-key` / `api-key` / `proxy-authorization` を保存しない deny-list。**mk-go 独自の硬化** |
| TOTP replay guard | **2026.6.0 で実装済** (`UserAuthService.validateOtp` が Redis `SET NX EX` で使用済トークンを記録、TTL 90s) | 同等機構を持つ (mk-go が先行実装)。**差分なし** |
| inbox admission の署名対象 header 強制 | `(request-target)` / `host` / `date` / `digest` の要求、Host 一致、SHA-256 body 照合を実施 (`ActivityPubServerService.inbox`) | 同等。**mk-go 固有なのは body 照合を定数時間比較 (`subtle.ConstantTimeCompare`) にしている点のみ** |

> TOTP replay guard と inbox admission は、かつて mk-go 独自の硬化だったが upstream が追いついて現在は同等。コード内の「upstream は持たない」旨のコメントは陳腐化している箇所があるので、見つけたら更新すること。

---

## 7. 意図的な安全側 divergence

いずれも upstream より厳しい / 正確な方向。error `code` / `id` は upstream と一致させ、status のみ異なるものが多い。

| 項目 | upstream | mk-go |
|---|---|---|
| admin 系 error の HTTP status | `ACCESS_DENIED` / `NO_SUCH_USER` とも 400 (kind 既定 `client`) | 403 / 404 (意味的に正確、mk-go 全 admin endpoint で統一) |
| `notes/reactions` の可視性 | requireCredential:false で followers/specified note の reaction list も 200 | `CanSeeNote` gate で 404 |
| reaction / chat の可視性エラー | generic INTERNAL_ERROR (500) に包まれる | 403 ACCESS_DENIED (500 拡散を回避) |
| `admin/promo/create` | visibility check なし | public 以外を reject (将来の IDOR 先回り) |
| `federation/stats` の moderationNote | moderator には見せる | 公開 endpoint なので常に隠す |
| moderator inactive 判定 | 空集合で登録を無効化しうる | lastActiveDate 保持者 0 人なら何もしない |
| SSRF の IPv4-mapped IPv6 | `::ffff:0:0/96` を一律遮断 | 埋め込み v4 を IPv4 レンジで評価し private 埋め込みのみ遮断 (over-block より精密)。NAT64 / RFC6145 は別途遮断 |
| `renoteCount` の減算 | 減算しない (`incRenoteCount` しか無く、renote 削除時も据え置き) | Undo(Announce) で減算する。unrenote 後もカウントが残り続ける方が不自然なため (増分条件は upstream と一致させてあるので対称、#2283) |
| `users/search-by-username-and-host` | `UserSearchService` が 4 query の UNION。`updatedAt IS NULL` を拾うのは**フォロー済み分岐だけ**なので、未フォローかつ未投稿の user は検索に一切出ない | `usernameLower` 前方一致 + `followersCount DESC` の単純検索。新規 user もフォロー前に見つかる (#2286) |
| reversi surrender | pending game も終局させられる | NOT_STARTED で弾く (勝ち逃げ防止) |
| webhook の note embed gate | note/reply/renote で skipHide | 全イベントで gate、viewer/repo nil は fail-closed |
| streaming / 通知の未知 visibility | — | fail-closed (誤配信しない) |
| URL preview の scheme 判定 | 生文字列の case-sensitive `startsWith` | case-insensitive (RFC 3986 準拠)。非 http(s) の thumbnail / icon は値を落とす |
| `cleanRemoteNotes` のクリップ保持 | `note.clippedCount = 0` で判定 | 加えて `clip_note` を直接 `NOT EXISTS` で見る。mk-go はクリップ件数の非正規化カウンタを維持せず `clip_note` を数える設計 (#2243) なので `clippedCount` は常に 0 で、upstream の条件をそのまま移植するとクリップ済みノートを保護できない (#2329)。`clippedCount` / `pageCount` の比較自体は TS から切り戻したインスタンスのために残してある |
| `securityKeysAvailable` | unset-mfa で触らない (`securityKeys` を毎回 count するため陳腐化しない) | 全鍵削除に合わせ false にする (mk-go は列をキャッシュとして読むため) |
| fetch-rss の URL 正規化 | WHATWG `new URL()` | host 小文字化 / default port 除去 / 空 path 補完まで再現。**IDN の punycode 変換 (UTS#46) は行わない** (取得は成功するが Unicode 表記と punycode 表記で cache key が分かれる)。空 userinfo (`http://@example.com/`) は upstream が許可するのに対し拒否 |

## 8. 逆方向 divergence (mk-go 独自 error を upstream に合わせて廃止したもの)

`admin/emoji/import-zip` の `NO_SUCH_FILE`、clip 削除の `NOT_CLIPPED`、`notes/translate` の `CANNOT_TRANSLATE` はいずれも mk-go 独自 error だったため upstream に合わせて廃止済み。myReaction fetch の「作成 2 秒以内は skip」guard は mk-go では機能劣化になるため意図的に不採用。

---

## メンテナンス

- **API endpoint の差分**: `make apicompat` で `docs/api-compat.md` を自動生成する (DB / Redis 稼働が必要)。upstream 側の fastify 直登録 endpoint は `ApiServerService.ts` から自動抽出するので、submodule bump 時の追随漏れは起きない。
  生成には DB / Redis 稼働が必要なので、使い捨ての postgres / valkey を `docker run` で立てて `-dump-routes` を回す (compose を使うと本番 UDS の project へ合流する事故があるため使わない)
- **entity shape の差分**: `docs/shape-drift.md` の L0 / L2 / L3 gate が CI で自動検出する
- **DB schema / migration の drop-in 安全性**: 以下の gate が CI で強制する (詳細は [shape-drift.md](shape-drift.md))
  - `TestSchemaDrift_CreateOnlyColumns` — `CREATE TABLE` 内でしか定義されていない upstream 非存在カラム (TS 製 DB では生えない)
  - `TestMigrationSeed_CoversUpstream` — TypeORM `migrations` テーブルの seed 漏れ (TS 復帰時の再実行)
  - `TestMigrationIdempotency_RequiresIfExists` — DDL の `IF [NOT] EXISTS` 漏れ (drop-in で migration が dirty 停止)
  - `TestIndexNaming_NoNewUpstreamDuplicates` — upstream と同内容の index を別名で追加 (TS 製 DB で二重化)
- **値レベルの差分**: `make diff-test` (mk-go ↔ TS の応答を値単位で diff)
- **コード内の divergence 注記**: `grep -rn "#2106 L" internal/` で全件を辿れる
- **upstream 追従時**: `docs/update/` に release ごとの diff doc を追加し、そこで確定した divergence を本ドキュメントへ反映する。golden の再生成 (`make shapecheck-gen`) と TypeORM seed の追加も必要 ([upstream-catch-up.md](upstream-catch-up.md))
- **fork frontend の変更**: `third_party/misskey` に custom commit を積んで `X.Y.Z-mk.N` tag を打ち、mk 側の submodule pin を bump する。純正へ還元できない (= 純正 backend が対応しない) ものだけを置く方針

## 関連ドキュメント

- [`api-compat.md`](api-compat.md) — endpoint 突き合わせ matrix (自動生成)
- [`shape-drift.md`](shape-drift.md) — entity shape drift gate
- [`federation.md`](federation.md) — 連合実装の詳細
- [`configuration.md`](configuration.md) — 設定キー一覧
- [`migration-from-ts.md`](migration-from-ts.md) — TS からの移行手順
- [`upstream-catch-up.md`](upstream-catch-up.md) — upstream 追従の手順とチェックリスト
- [`update/`](update/) — upstream release ごとの差分 doc
