# 純正 Misskey との差分カタログ

mk-go が持つ「純正 Misskey (misskey-dev/misskey) には無い、または挙動が異なる」ものを 1 枚に集約したリファレンス。

- 基準: **mk-go 1.2.1** ⇔ Misskey TS `2026.7.0`
- 最終更新: 2026-08-23

> ベースラインを固定したのは 1.0.0 (= Misskey TS `2026.7.0` 追従完了時点)。以降の 1.1.x は
> upstream を追従したのではなく、**mk-go 側の独自変更と互換性 fix** を積んだもの。したがって
> 比較対象の Misskey TS は 1.0.0 時点と同じ `2026.7.0` のまま。

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
| API endpoint | GET variant 23 + alias 3 + 分割アップロード 4 + 承認制 6 + exact assignment lookup 2 + admin 観測 5 | chat 15 | **0** |
| API レスポンスの additive field | 5 (`runtime` / `mkGoVersion` / `chunkedUpload` / `approvalRequiredForSignup` / `signupApplicationForm`) | reversi packed game の `crc32` 等 | — |
| DB テーブル | 10 (+ bookkeeping 2) | 0 | 0 |
| DB カラム | 17 (+ 未使用の残存列 3) | 3 | 0 |
| ActivityPub | Ed25519 / RemoteStatsFetcher ほか | reversi 連合 / chat 連合 | — |
| config キー | 20 前後 | 0 | — |
| fork frontend の独自変更 | 25 tag (`-mk.0` ～ `-mk.22b`) | — | — |

**upstream endpoint の未実装はゼロ** (coverage 100.0%、444/444)。DB schema も upstream の全テーブル・全共有カラムを superset で保持しており、逆方向の欠落は無い。

---

## 1. API endpoint

upstream の endpoint は `endpoints/` 配下 438 件 + `ApiServerService.ts` の fastify 直登録 6 件 (POST 5 / GET 1) = **444 件**。うち **444 件すべてを実装済み (coverage 100.0%)**。

### 1-1. mk-go にしかない (58)

| 分類 | 件数 | 内容 |
|---|---|---|
| GET variant 追加 | 23 | `charts/*` 12 件、`emoji` / `emojis` / `federation/instances` / `federation/stats` / `fetch-rss` / `get-online-users-count` / `hashtags/trend` / `notes/featured` / `notes/reactions` / `server-info` / `bubble-game/ranking`。**対応する POST は両側にある**。ブラウザから直接叩く利便目的 |
| cherrypick chat 拡張 | 15 | `chat/messages` / `chat/messages/create` / `read` / `update` / `reactions/create` / `reactions/delete`、`chat/rooms/joined` / `unmute` / `transfer-ownership` / `members/ban` / `members/update-membership` / `invitations/accept` / `delete` / `reject`、`chat/unread-count` |
| 分割アップロード | 4 | `drive/files/create-chunked/start` / `append` / `finish` / `abort` (#2313 / #2314)。upstream に分割アップロードが無いため対応物なし。S3 の multipart upload を包むもので、`UploadId` は `chunked_upload_session` に閉じてクライアントへ出さない。能力は `/api/meta` の `chunkedUpload` で告知し、**未対応構成では field ごと出さない**ので純正クライアントは単発アップロードに倒れる |
| 承認制の登録の申請 | 3 | `signup-application/apply` / `status` / `register` (#2569)。upstream に承認制が無いため対応物なし。認証不要で、本人性は申請時に発行するクレームコードが担保する (hash で保存し、平文は申請直後に 1 度だけ返す)。**外部サーバーには一切依存しない** — 当初は MiAuth を使っていたが、相手サーバーに消せない access_token 行と通知を残すため廃止した (#2568)。承認制が有効でない構成では 503 |
| 承認制の登録の審査 | 3 | `admin/signup-application/list` / `approve` / `reject` (#2555)。upstream に承認制が無いため対応物なし。scope は `read:admin:invite-codes` / `write:admin:invite-codes` を再利用する (承認は最終的に `registration_ticket` の発行につながり管轄が同じ。`internal/misc/permissions` は upstream misskey-js と完全一致させる契約があり mk-go 固有 scope を足せない) |
| role assignment exact lookup | 2 | `roles/assignment-show` / `admin/roles/assignment-show` (#2607)。member一覧を走査せず、指定したuser/roleのactive assignmentだけを確認するbuild-time plugin向けhost API。self側は本人、admin側はmoderator以上に限定し、admin側は既存`admin/roles/users`と同じ`read:admin:roles` scopeを使う。**見るのは`role_assignment`行だけなので`target=conditional`のroleでは常に`assigned:false`**になる (行を持たずcondFormulaのread時評価で決まるため)。判別用に`role.target`を返す。既存の`admin/roles/users`も`ListByRole`で同じテーブルを引くので挙動は揃っている。effective判定は#2608側の担当 (#2633) |
| admin の観測系 | 5 | `admin/server-plugins` (組み込みプラグインの一覧、`read:admin:meta`)、`admin/server-metrics` / `admin/self-check` / `admin/federation/delivery-health` / `admin/federation/inbox-health` (いずれも `read:admin:server-info`)。upstream に対応物が無い。**mk-go は連合の配送 / 受信の健全性を Redis に host 単位で記録している** (`internal/core/deliveryhealth`) ので、それを admin 画面から読むための endpoint。Redis 上のカウンタなので flush で消え、drop-in の引き継ぎ対象でもない |
| その他 / alias | 3 | `i/flashs` / `i/flashs/likes` (upstream の `flash/my` / `flash/my-likes` に対する mk-go 側の path alias。両者とも mk-go に実装済み)、`signin` (upstream が `signin-flow` に統合した旧 path の backward-compat shim) |

ランダムマッチ (`reversi/match` の `userId` 無し) は **local user 同士のみ**。待機列 (`reversi:matchAny`) に載るのはこのインスタンスで認証を通した local user だけなので、相手がリモートになることはない。upstream Misskey も yojo-art/cherrypick も**連合ランダムマッチは持っていない**ので意図的に揃えている。名指しの招待 (`userId` 指定) は従来どおり連合する。

なお待機者の確保だけは upstream より厳密にしてある。upstream は `ZRANGE` → `ZREM` の順で **`ZREM` の戻り値を見ない**ため、同じ待機者を 2 人が同時に見つけると両方が対局を作る。mk-go は `ZREM` が 1 を返した呼び出しだけが対局を作る (#2407)。

**reversi は endpoint レベルの差分ゼロ。** mk-go の 7 本 (`games` / `invitations` / `show-game` / `match` / `cancel-match` / `surrender` / `verify`) は upstream 2026.7.0 と完全一致。`crc32` カラムと `reversi/verify` も upstream 標準 (`models/ReversiGame.ts` / `endpoints/reversi/verify.ts`)。**cherrypick 由来の拡張は ActivityPub 層と、packed game レスポンスに `crc32` 等を additive に載せる点に現れる** (§3-1 参照)。

### 1-1b. レスポンスの additive field

| endpoint | field | 内容 |
|---|---|---|
| `admin/self-check` | endpoint 全体 | 公開 URL 経由で WebFinger / nodeinfo / actor を引き、DB / Redis / migration / TLS 期限とあわせて検査する mk-go 独自 endpoint (#2463)。upstream に対応物は無い。`misskey -doctor` と同じ検査を実行する。**検査の宛先は config の `url` に固定**され、リクエストから指定できない (自ホストは loopback / private IP に解決されうるため検査用 client は SSRF ガードを通さない。宛先を外から与えられる口を作らないことがその安全性の前提)。scope は `admin/server-metrics` と同じ理由で `read:admin:server-info` を再利用する |
| `admin/federation/inbox-health` | endpoint 全体 | `delivery-health` の受信側 (#2471)。upstream は受信結果を一切残さないため対応物が無い。inbox processor が活動を捨てる 5 分岐 (署名検証失敗 / ブロック済みホスト / actor 認可失敗 / LD-Signature 検証失敗 / 未対応 activity) を分類して集計する。**ブロック済みホストは元の実装がログすら出さない**ので、ここが唯一の観測点になる。受信側は accepted と unsupported を「受理」に数える (unsupported は相手が正しく送っておりこちらが未対応なだけ)。集約基盤は送信側と共有し、Redis のキー空間だけを分ける |
| `admin/federation/delivery-health` | endpoint 全体 | 配送先ホストごとの成功/失敗の内訳・レイテンシ分布 (p50 / p95 の近似)・直近エラーを返す mk-go 独自 endpoint (#2461)。upstream は配送結果を `instance.isNotResponding` の真偽値にしか残さないため対応物が無い。deliver processor が既に撃ち分けている 6 分類 (success / gone / rateLimited / clientError / serverError / transport) をそのまま集計する。**観測のみで配送を止める判断は含まない**。scope は `read:admin:server-info` を再利用する (`internal/misc/permissions` は upstream misskey-js と完全一致させる契約で mk-go 固有 scope を足せないため、同じく独自 endpoint の `admin/server-metrics` に倣う) |
| `admin/queue/queues` / `admin/queue/queue-stats` | `runtime` | worker 現在数 / auto-scale 範囲・有効性 / dispatch wait・processing の分位数 / 直近失敗数 / scale 履歴。upstream は worker 数を静的 config でしか持たず該当情報が無い。provider 未配線・未知 queue では block ごと省く (#2277) |
| `admin/queue/show-job` / `admin/queue/jobs` | `attemptsAt` | 試行ごとの開始時刻 (unix ミリ秒、古い順)。**BullMQ は per-attempt の時刻を残さない**ので upstream には対応物が無く、job 詳細の Timeline は試行を `at ?` と表示している。mkq が `mkqAttemptsAt` HASH field に記録する (mkq v1.0.8)。未知 field は BullMQ / bull-board が無視するので wire 互換は保たれる。**記録が入る前に失敗した job には無い** (遡って埋められない) ので、その場合は空配列 (#2692) |
| `admin/queue/show-job` / `admin/queue/jobs` | `data` | mk-go は payload を `{"type": …, "body": <base64>}` で包んで保存するので、upstream のように Bull の `job.data` をそのまま返すと Data タブが base64 の塊になって読めない。**包みの形は保ったまま `body` だけ decode して返す** (#2689)。upstream の job data はそのまま読める形なので、この包みは mk-go 固有 |
| `admin/queue/show-job` / `admin/queue/jobs` | `failedReason` / `returnValue` | **golden (upstream の宣言 schema) は required だが、upstream の実装自身が満たしていない。** Bull の job は失敗するまで `failedReason` を持たないので upstream の `packJobData` は `undefined` を返し、JSON からは消える。frontend は `v-if="job.failedReason != null"` / `job.returnValue != null` で出し分けるため、schema に寄せて空文字や `{}` を常に出すと**成功した job にも赤い警告アイコン付きの空の Failed reason 行と空の Return value タブ**が出る。upstream の**実装**に合わせて値が無ければ出さない (#2689)。golden は生成物なので直さず、テストは `shapetest.AssertExcept` で理由付きに例外化する |
| `/api/meta` (+ SSR 埋め込み meta) | `mkGoVersion` | mk-go の実装バージョン。`version` は drop-in 互換のため**互換 Misskey バージョン**を返す契約 (第三者クライアントの feature detection / frontend `_error_.vue` の版ずれ検出が依存) なので別 field にした (#2274) |
| `/api/meta` (+ SSR 埋め込み meta) | `approvalRequiredForSignup` | 承認制の登録 (#2554 / #2555) の有効/無効。登録ページが分岐に使うので公開する (`emailRequiredForSignup` と同じ扱い)。**`features` 側にも出す** — frontend は `features` を feature detection に使うため、片方だけだと検出できない。`admin/meta` にも出す (管理画面のトグルが読む) |
| `/api/meta` (+ SSR 埋め込み meta) | `signupApplicationForm` | 承認制の申請フォームの定義 (#2570)。申請ページが描画に使うので公開する。項目は `{ label, type, required, maxLength }` の配列で、未設定なら空配列。**回答のラベルはここから埋める** — クライアントに送らせると申請者が審査画面に偽のラベルを流し込める |
| `/api/meta` | `chunkedUpload` | 分割アップロード (#2313) の能力告知。`{ chunkSize }` を返す。**未対応構成 (オブジェクトストレージ未使用 / `meta.chunkedUploadEnabled=false`) では field ごと出さない**ので、純正 Misskey と同じく `undefined` になりクライアントは単発アップロードにフォールバックする |

### 1-2. 未実装 (0)

**upstream endpoint の未実装はゼロ。** 最後まで残っていた `GET /api/v1/instance/peers` (Mastodon 互換の連合ピア一覧) は #2245 で実装した。upstream は `ApiServerService.ts` で fastify 直登録しており `endpoints/` 配下に無いため、matrix 生成ツールの file-walk から漏れて長らく不可視になっていた (現在は `ApiServerService.ts` を正規表現で直接読むので追随漏れが起きない)。

かつて `docs/api-compat.md` に残っていた「TS only (mk-go 未実装) 1」= `/api/reset-db` の偽陽性 (mk-go では `config.TestMode` 時のみ登録されるのに、matrix 生成が default config で route dump するため未実装に見えていた) は解消済み。現行 matrix は `TS only (mk-go 未実装): 0` で、`/api/reset-db` は「両方に存在する endpoint」に入っている。

---

## 2. DB schema

**逆方向の欠落はゼロ** — upstream の `@Entity` 76 テーブルと全共有カラムを mk-go が superset で保持している。

### 2-1. mk-go 独自テーブル (12)

| テーブル | 由来 | 理由 |
|---|---|---|
| `user_keypair_extra` | mk-go 独自 | local user の Ed25519 鍵ペア。既存 `user_keypair` (RSA) を touch せず別テーブルに分離し、**TS へ swap back しても壊れない**設計 |
| `user_publickey_extra` | mk-go 独自 | remote user の追加公開鍵。actor JSON の `assertionMethod[]` (FEP-521a Multikey) を keyId 単位で保持 |
| `antenna_note_unread` | mk-go 独自 | per-user per-note の antenna 未読 |
| `channel_note_unread` | mk-go 独自 | channel follower の未読追跡 |
| `chunked_upload_session` | mk-go 独自 | 分割アップロード (#2313) の進行中セッション。S3 の `UploadId` はここでだけ保持しクライアントには露出しない。`user` への FK は張らない — CASCADE で行だけ消えると `AbortMultipartUpload` されない未完了マルチパートアップロードが孤児として課金され続けるため、期限切れ GC に回収させる |
| `signup_application` | mk-go 独自 | 承認制の登録 (#2554 / #2555) の申請。**承認待ちを `user` 行として持たないための箱**で、`user` に承認列を足す設計だと TS へ切り替えた瞬間に承認待ち全員が有効なアカウントになる (TS はその列を知らないので素通りする)。申請の回答は `answers` 列に**提出時のラベルを同梱して**持つ (#2570) — 定義を後から変えても既存の申請がどの設問への答えだったか分かる。本人性は**クレームコードの SHA-256** が担保する (#2569) — 平文で持つと DB が漏れた時点で全申請が乗っ取れる。重複申請を DB では抑止しないので、captcha とレート制限が防波堤になる。TS は未知のテーブルを無視するだけ |
| `relay_observed_user` | mk-go 独自 | リレー経由で初めて観測した remote user の印 (#2340)。孤児掃除の対象をリレー由来に限定するために使う。印が無いと、リレー購読前から居る行やプロフィール閲覧・スレッド遡りで解決された行まで巻き込む。**`user` に列を足さず別テーブルにしてある**: TS は未知の列も無視するので列追加でも復路は壊れないが、別テーブルなら TS 側から一切見えず `check-migrations` にも差分が出ない。`user` は連合・認証・API のあらゆる経路が触るホットテーブルでもあるため、触らずに済ませる |
| `instance_secret` | mk-go 独自 | インスタンスごとに生成する秘密値。最初の用途は media proxy の HMAC 鍵。以前は設定に `mediaProxySecret` が無いとインスタンス URL から導出していたが、**URL は公開情報なので誰でも同じ鍵を計算でき署名を偽造できた**。鍵はプロセス間・再起動をまたいで安定している必要があるので (署名した URL を別プロセスが検証する / 発行済み URL が再起動後も有効)、起動時のメモリ生成では足りず DB に置く |
| `instance_signature_capability` | mk-go 独自 | リモートインスタンスがどの署名方式に対応しているかを host 単位で記録する。判定材料は宣言 (actor の `assertionMethod[]`) / 受信観測 / 送信結果の 3 系統で、それぞれ単独では穴があるので併記する |
| `note_unread` | 準・独自 | upstream DB にも legacy 遺物として残るが 2026.7.0 の `models/` に entity は無く参照 0 件。mk-go はこれを実用し `/api/i` の `hasUnreadSpecifiedNotes` / `hasUnreadMentions` を Redis stream を舐めずに解決する。upstream legacy 版にある `noteChannelId` は mk-go の定義に無い (TS 製 DB では `CREATE TABLE IF NOT EXISTS` が no-op なので実害なし) |
| `migrations` | drop-in 互換 | TypeORM の bookkeeping。mk-go 由来 DB に TS を後から繋いだ時に migration を再実行させないための seed。name は本家と同じ `ClassName+timestamp` 形式で 346 件を保持する (#2244 で短縮形から是正)。漏れは `TestMigrationSeed_CoversUpstream` が CI で検出する |
| `schema_migrations` | tooling | golang-migrate 用 |

`__chart__*` / `__chart_day__*` 24 テーブルは独自ではない (upstream では `models/` ではなく `core/chart/charts/entities/` で定義されるため、`models/` だけを見ると誤検出する)。

### 2-2. 独自カラム (23 = 実使用 20 + 未使用の残存 3)

うち **mk-go が実際に読み書きするのは 20 件** (cherrypick 由来 3 + mk-go 独自 17)。残り 3 件は fresh な mk-go DB に列だけ残る未使用列で、#2243 で依存を外した。

| テーブル | カラム | 由来 | 理由 |
|---|---|---|---|
| `chat_message` | `emojis` / `isDelivering` / `isDeliverFailed` | cherrypick | 連合配送の状態追跡 |
| `meta` | `approvalRequiredForSignup` | mk-go 独自 | 承認制の登録 (#2554) の有効化。**これ自体がゲート**で、有効時は `/api/signup` を 403 で閉じる (#2557)。**メール必須と併用できる** (#2571) — 承認済みからの登録も `emailRequiredForSignup` が有効なら `user_pending` に積んで確認メールを送るので、設定と実態が食い違わない (#2565 の排他は撤去済み)。クレームコードは常に必須で、本人性の担保はコードが持つ。有効化する更新では**同じ更新でアカウント作成も開放する** — 「先に開放してから承認制を入れる」順を強制すると、その間に素通しで登録される窓ができるため。開放は安全性の条件ではなく (承認制それ自体がゲート)、訪問者に「招待制」と表示しないための整合。`disableRegistration` と組み合わせる運用にすると、訪問者には「招待制」と表示されて実態と食い違う。承認フローは signup service を直接呼ぶのでこの分岐を通らない。TS はこの列を認識しないので、TS へ戻すと承認制が単に無効になる (登録が開くので、切り替え前に `disableRegistration` を検討すること) |
| `user` | `isRoot` | mk-go 独自 | upstream は system_account 移行で DROP 済み。`role.Service.isRootUser` の fallback に必要 |
| `meta` | `proxyAccountId` | mk-go 独自 | 同じく upstream は DROP 済み。`admin/update-proxy-account` が書き込む |
| `note_favorite` | `createdAt` | mk-go 独自 | upstream は `deleteCreatedAt` で DROP 済み。`/api/i/favorites` の response 要件で復活 |
| `app` / `auth_session` | `createdAt` | 列のみ残存 | upstream は `deleteCreatedAt` で DROP 済み。mk-go も **読み書きしない** (#2243 で model から除去)。fresh な mk-go DB には列が残るが未使用 |
| `clip` | `notesCount` | 列のみ残存 | 旧・非正規化カウンタ。#2243 で撤去し、件数は upstream 同様 `clip_note` の実カウントで算出する |
| `poll` | `notifiedAt` | mk-go 独自 | pollEnded 通知の二重送信防止 |
| `user_pending` | `invitationTicketId` | mk-go 独自 | 1 招待で複数アカウントを作れる gap を塞ぐ |
| `user_pending` | `signupApplicationId` | mk-go 独自 | 承認制 (#2571) でメール確認を挟むときの申請 ID。確認完了まではアカウントが無いので申請を `completed` にできず、**紐付けが無いと申請が `approved` のまま残って 1 つの承認から複数アカウントを作れる**。`/api/signup-pending` が `PromotePending` の戻り値からこれを読んで申請を完了させる。TS は未知の列を無視するので drop-in の復路は壊れない |
| `meta` | `signupApplicationForm` | mk-go 独自 | 承認制の申請フォームの定義 (#2570)。管理者が項目を決める jsonb 配列。上限は 10 項目 / ラベル 100 文字 / 回答 2000 文字で、**上限を置かないと管理者が自分で壊せる** (項目無制限で申請ページが使えなくなる、最大長無制限で 1 件の申請が DB を膨らませる)。壊れた JSON は空フォーム扱いにして申請ページを 500 で潰さない |
| `meta` | `enableEphemeralRelayNotes` / `ephemeralRelayNoteTtlMinutes` | mk-go 独自 | リレー経由投稿の揮発化 (#2332)。リレーでしか観測しない投稿は Redis に TTL 付きで置き、ローカルユーザーが触ったときだけ DB へ materialize する。既定 false は既存インスタンスの挙動を変えないため — 有効にするとグローバルタイムラインは FTT の窓より過去に遡れなくなる |
| `meta` | `enableRelayOrphanUserCleanup` / `relayOrphanUserGraceDays` | mk-go 独自 | リレー由来の孤児 user の掃除 (#2340)。対象の限定には `relay_observed_user` を使う |
| `meta` | `chunkedUploadEnabled` / `chunkedUploadChunkSizeMb` / `chunkedUploadSessionTtlMinutes` / `chunkedUploadMaxSessionsPerUser` / `chunkedUploadMaxPendingMbPerUser` | mk-go 独自 | 分割アップロード (#2313) の設定。既存の `objectStorage*` と同じくコントロールパネルから編集する。TS は未知の列を無視するので drop-in の復路は壊れない |

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
| featured (ピン留め) の取り込み | `core/federation/featured.go` | **upstream と同等** (`ApPersonService.updateFeatured`) で、actor の新規取得時と更新時に取り込み、上限 5 件・既存を全置換。差分は 4 点 (#2552 / #2684)。うち (1)-(3) は安全側、(4) は取り込みが遅れる方向。(1) upstream は items を**全件**解決してから Note に絞るが、mk-go は走査を 50 件で打ち切る (巨大なコレクションを置くだけで取得を増幅させられるため。得られるピン留めは同じ)。(2) 著者が actor 本人であることを要求する (upstream は見ないので、他人の投稿を自分のプロフィールに並べられる)。(3) 個々の item の解決失敗を読み飛ばす (upstream は `Promise.all` なので 1 件でも失敗するとピン留めが 1 件も入らない)。(4) **いま取り込み中の投稿は skip する** (#2684 / #2686)。著者が自分の投稿をピン留めしていると、featured の解決がその投稿自身を要求する形になる。**入口によって壊れ方が違う**: `ResolveNote(A)` 経由だと note の singleflight が自分の in-flight entry を自分で待って**永久に止まり** (#2684)、inbox 直送 (`IngestNoteWithCreated`) 経由だと同じ note をもう一度 fetch して内側の ingest が先に行を作り、外側の `Create` が UNIQUE に当たって `created=false` になる — 呼び出し側がそれで通知とチャートのフックを飛ばすので**言及・返信の通知が黙って消える** (#2686)。upstream も `Resolver.history` で同じ形の再解決を弾くが、throw が `Promise.all` を reject するので `updateFeatured` ごと落ちて既存のピン集合が残る (all-or-nothing)。mk-go はその 1 件だけ落として残りを反映する。判定は**解決チェーンに閉じた台帳** (`resolveChain`) で行う (#2685)。以前は Resolver に `sync.Map` を 2 つ置いていたが、プロセス全体で共有されるため「自分の祖先が握っている」(待つと自分を待つので解けない) と「無関係な goroutine が握っている」(待つのが正しい) を区別できず、後者も諦めていた。その結果、別の worker が同じ引用先を取り込んでいる最中に引用元が来ると **`renoteId` を落としたまま保存**していた (再取り込みは `FindByURI` で早期 return するので恒久的に失われる)。upstream の `Resolver.history` が activity ごとに作られる Set なのはこのため。チェーンは鍵 → document id の写像で、singleflight の鍵 (取得 URI) と正規化後の id の両方を持つ。**チェーンに閉じた判定だけでは cross-goroutine のデッドロックを防げない** — プロセス全体の台帳だった頃は「他の goroutine が握っていたら諦める」ことで意図せずそれも防いでいたので、待つようにすると相互に引用し合う 2 投稿を 2 worker が同時に解決したときに待ちが循環し、待ちを打ち切る手段が無ければ両方が永久に止まる。そこで待つ直前に wait-for グラフ (`core/federation/resolve_waits.go`) を辿り、**循環になる場合だけ**諦める。循環でない待ちは待って引けるので、renoteId 欠落は戻らない。**グラフには note と actor の両方の in-flight を載せる** — actor の解決は他の actor を待つ (`processRemoteMove` が移行先を解決するので、**互いを `movedTo` に指す 2 つの actor** を 2 worker が同時に取得すると actor どうしで循環する) し、note の解決も著者解決で actor を待つので、待ちの辺は 2 つの group をまたぐ。片方だけモデル化すると、もう片方で待っているチェーンが「走っている」と見えて循環を見逃す。note → actor → note の形は featured が待たなくなったので現状は作れないが、**待つ経路が 1 つ増えれば再び成立する**ので、それに依存して actor 側を外さない。**検出には保険を付けてある**: 待ちには上限 (5 分) があり、モデルに載っていない待ちで循環しても永久には止まらない。**上限は join ごとではなく解決木ごとの合計**で、木の全枝が 1 つの予算を共有する (join ごとにすると、著者・返信・引用と待つ回数だけ積み上がる)。**予算は「待ちに費やした時間」で減る** — 根で `now + 上限` の期限を打つ形にすると fetch のような待ち以外の作業でも減り、上限より長くかかる解決の途中で正当な待ちに出会うと 1ms も待たずに諦めることになる。この上限のために `singleflight` ではなく自前の group (`core/federation/resolve_group.go`) を使う (`singleflight.Do` は待ちを打ち切れず、`DoChan` は fn の panic を**意図的に recover 不能な形で**別 goroutine へ飛ばすのでプロセスごと落ちる)。**featured の取り込みは最初から待たない** — best-effort な経路が待ちの辺を張ると、(1) その間 actor の鍵を握り続け、(2) その辺が循環に見えたときに**本命の note の解決**が代わりに弾かれる。待たずに既存行へ落とすので、プロセス全体台帳だった頃と同じ挙動になる。**待たないのは枝ごと**で、その解決から下 (取り込む投稿の著者 actor の解決など) も一切待たない。相乗りする瞬間だけ待たない形にすると、自分が先頭になったときに内側で待ってしまう。**取得 URI と id が食い違う別名 URL** (featured が `/@user/x` を載せていて document の id が `/notes/x`) では、取得 URI で引く手前の判定は空振りする。id は fetch しないと判らないので、`resolveNoteOnce` が**取得したあとに**同じ判定をやり直す (#2695)。したがって**この形では**二重取り込みが起きず、inbox 直送の `created` も落ちない (別名がからむもう 1 つの形については後述)。ただしピン自体はその回落ちる (正規形と同じで、次の actor 更新で拾い直す)。**この取得後の判定は featured の取り込みから入った呼び出しにだけ効かせる** — best-effort の印は枝ごと引き継がれるので chain の印で判定すると featured の内側で走る引用解決にも効いてしまい、ピンが取り込み中の投稿を別名 URL で引用しているだけで `renoteId` が恒久的に落ちる。入口が何だったかは `resolveNoteDepthOpt` の `mayWait` が持っているので、それを引数で渡す。**その代償は `created`** — ピンが取り込み中の投稿を**別名 URL で引用**している形 (featured には正規 URI で載っている) では引用解決がこの判定を素通りして内側で先に行を作るので、外側の `Create` は UNIQUE に当たり `created=false` のままになる。#2686 の通知欠落はその形では残る。`renoteId` の恒久的な欠落のほうが重いので意図してそちらを取っている。skip する前に既存行を引く (`ReplaceByUser` が delete-then-insert なので、落とすと集合ごと書き直して生きたピンが消える) ので、**取得 URI か確定した document id で行が引ける限り、既に取り込み済みのピンは消えない**。これは in-flight で skip する枝だけでなく、**解決がエラーで落ちた枝にも掛ける** (待ちを断った `ErrResolveWouldBlock` はここを通る)。ただし別名 URL でここまで来て引けるのは、`resolveNoteOnce` が probe (fetch と id の確定) まで到達した場合だけ。待ちを断った枝 (best-effort な枝は `onJoin` が必ず `ErrResolveWouldBlock` に上書きするので、この 1 つだけ) や fetch 失敗では id が判らないので取得 URI でしか引けず、**別名 URL の生きたピンは依然として落ちうる** (#2695 で残した穴)。ピンが落ちるのは既存行を引けなかった場合で、通常は行がまだ無い初回。いずれも次の actor 更新で拾い直される (actor TTL 既定 24 時間)。またノート解決は depth 1 から始め、**その内側で作られた actor では featured を引かない** (引用先 → その著者 → その featured と入れ子になると 1 段ごとに 5 分岐する取得の連鎖になる) |
| outbound User-Agent | `config/config.go` | `mk-go/<ver> (<url>)` |
| AP object id の https スキーム非強制 | `core/federation/resolver.go` | **意図的な未実装** (#2507)。upstream の `checkHttps` は非 https の object id を reject する (テスト環境除く)。mk-go は id/attributedTo の host 一致 + SSRF guard で検証するがスキームは見ない。http ベースの e2e stack (dropin / federation) が前提のため、強制するなら upstream 同様の環境ゲートが要る。ブラウザ / AP クライアントは非 https の Location を追わないため実害は限定的 |
| リモート AP document の単一値 / 配列表現の許容 | `activitypub/types.go` | **upstream 同等 (一部は緩い方向)** (#2662)。対象は `type` (配列 → 先頭。`tag` / `attachment` の**要素**の type も含む)、`attachment` / `tag`、collection の `items` / `orderedItems` (単一 object → 1 件)、`to` / `cc` / `alsoKnownAs` (単一値・`{id}` 要素)、`inReplyTo` / `attributedTo` / `outbox` / `followers` / `following` / `sharedInbox` / `endpoints.sharedInbox` / `featured` / `movedTo` (`{id}` object・配列の先頭)、`url` (`{href}` object・配列の先頭)、`endpoints` / `source` / `icon` / `image` が object でない場合 (空として扱う。`icon` / `image` は 1 つの field が読めなくても読めた分は救う)、`assertionMethod` (単一 object・bare IRI 参照・要素ごとに decode)、`summary` / `name` / `publicKey.publicKeyPem` / `_misskey_*` 拡張 (非 string / 非 bool でも document は通す。JSON-LD の展開形は剥がして値を拾う。`publicKeyPem` は**空になった値で既存の鍵を上書きしない**ようにしてある — 上書きするとその actor からの署名検証が恒久的に失敗する)、Question の選択肢 `type` / `replies` (IRI 参照でもよい) / `replies.totalItems` (`3.0` / `"3"` も整数として読む)、`_misskey_quote` (`{id}` object)、`isCat` / `discoverable` / `manuallyApprovesFollowers` / `sensitive` / `_misskey_*` の bool が bool でない場合 (**PostgreSQL の boolean 入力構文で読む**。upstream が生値を代入する field は TypeORM の丸めが効かず PostgreSQL がキャストするので、`"true"` は true、`"false"` / `"0"` / `"no"` / `"off"` は false。**JS の truthy にはしない** — 「空文字以外は true」にするとこれらが軒並み反転する。数値も同じ扱いで、`node-postgres` は `String(val)` で送るので有効なのは `1` / `0` だけ (`2` は `'2'::boolean` = invalid input syntax なので「読めない」側)。**生値を代入するのは `manuallyApprovesFollowers` (→ `isLocked`) / `discoverable` (→ `isExplorable`) / Note の `sensitive` で、`isCat` は違う** — upstream は `isCat: (person as any).isCat === true` なので `"true"` でも false になる (`requireSigninToViewContents` も同じ形)。mk-go はここを他の bool と揃えて読むので**その分だけ緩い**。JSON-LD の展開形は剥がしてから判定する。**読むのは完全形と PostgreSQL が挙げる 1 文字表記 (`t` / `f` / `y` / `n`) まで** (`'tr'` / `'fals'` のような 2 文字以上の一意な接頭辞も PostgreSQL は受け付けるが、そこまでは追わない。曖昧で PostgreSQL 自身が拒否する `'o'` も読まない)。読めない形は field ごとの既定値に倒し、既定は「読めないと危険な側」で決める: `sensitive` / `manuallyApprovesFollowers` は true (隠す / 承認制)、`_misskey_canChat` は false (DM 拒否)、その他は false)、`sensitive` が bool でない場合 (**読めなければ true に倒す**。これは upstream 追従ではない — upstream は `sensitive` を `attach.sensitive ??= note.sensitive` の1 箇所でしか使わず CW は `summary` からしか作らないが、mk-go は `sensitive` が立った note に空 CW を付ける独自実装なので、false に倒すと**送信側が sensitive と宣言したノートが CW 無しで表示される**。JSON-LD の展開形 `[{"@value": false}]` は剥がしてから判定する)、`quoteUrl` (`{id}` object)。AP はこれらを「単一値でも配列でもよい」と定めており、JSON-LD compaction は `@container: @set` の無い term の単一要素配列を素の値に潰す。`@type` は逆に配列表現が正規で、compaction 後も配列で残る実装がある。upstream は `toArray` / `getApId` / `getOneApHrefNullable` で吸収するが、mk-go は Go の型で決め打ちしていたため **document の unmarshal ごと失敗し、その actor / Note がまったく取り込めなかった**。`APType` / `APObjectList` / `APRawList` / `APIDList` / `APLenientID` / `APLenientHref` で受ける (順に upstream の `getApType` / `toArray` / `toArray` (要素を decode せず `json.RawMessage` のまま持つ版) / `getApIds` / `getOneApId` / `getOneApHrefNullable` に対応)。`APLenientString` / `APLenientBool` / `APTruthyBool` / `APLenientInt` / `APLenientTimestamp` / `MultikeyList` / `Source` / `Endpoints` / `QuestionChoiceReplies` / `Image` / `Note` の寛容な `UnmarshalJSON` には upstream の対応物は無く、**JS が型を検査しないので結果的に通る**ものを Go で同じだけ通すためのもの。**inbox 経由の activity は `activitypub.Normalize` が先に `type` 配列や `{"@id": ...}` を潰す**が、actor / note / featured の生 fetch 経路は Normalize を通らないので、これらの型がその役目を負う。**Note は上の per-field の型に加えて、`Note.UnmarshalJSON` が型不一致を握って「読めた field だけ採用する」。** 後者が担うのは per-field で緩めていない `id` / `content` と `oneOf` / `anyOf` / choice の `name`。 upstream は JS なので型検査をほとんどせず (`content` は `typeof === 'string'` のガードを通って text=null のノートを作り、`oneOf` / `anyOf` が読めなくても `extractPollFromQuestion(...).catch(() => undefined)` で poll 無しのノートができる。choice の `name` が非 string のときは upstream も throw せず `filter(x => x != null)` で残すので、mk-go も空の選択肢を含む poll を作る)、この形なら call site を触らずに同じ挙動になる。**構文エラーは従来どおり弾く。** `published` は `APLenientTimestamp` が単一要素配列 / `{"@value": ...}` / epoch ミリ秒まで読む (`{"@value": ...}` は upstream の `new Date()` では Invalid Date になるので、ここは upstream より緩い)。**upstream は malformed な `published` を `isSafeT(new Date(...).valueOf())` で reject するが、mk-go は落とさず `parseAPPublishedTime` が受信時刻に fallback する** (元からの設計。ここだけ reject に倒すと upstream が受理する形まで巻き込むうえ、`encoding/json` は最初の型エラーしか報告しないので先行 field のエラーで判定が飛ぶ)。**actor 側は catch-all を使わず field ごとに緩める** (どの field を緩めたかが読めなくなるため)。`name` / `summary` は `APLenientString` にしてあるので、**upstream `validateActor` が throw する truthy な非 string (`["Alice"]` / `{"@value": "Alice"}`) も mk-go は受理する**。JSON-LD の展開形を拾うためで、値は `description` 2048 / `user.name` 128 に truncate + NUL 除去して書く。upstream も受理する falsy な非 string (`name: 0`) もこれで通る。**upstream より緩い箇所がいくつかある。** (1) actor の `attachment`: upstream `analyzeAttachments` は `Array.isArray` でない入力に `[]` を返して profile fields を捨てる (upstream 自身が TODO で疑問視している) が、mk-go は 1 件として取り込む。(2) `outbox` / `followers` / `following` / `url`: **mk-go はこれらの値をそもそも読まない**。型を緩めた効果は「document を落とさなくなる」ことだけで、値は捨てる。upstream は `validateActor` でこれらの collection の host も actor に縛るが、mk-go は値を使わないので検証もしない。**実際に配送先になる `inbox` / `sharedInbox` / `endpoints.sharedInbox` は host を actor に縛ってある** (upstream の `punyHost` と同じく punycode と既定ポートだけ正規化し、**`www.` は同一視しない**。mk-go の `normalizeMatchHost` は #1820 の object-host binding 用に upstream の `normalizeSynonymousSubdomain` を取り込んで `www.` を剥がすが、upstream はそれを `assertActivityMatchesUrl` でしか使わない。配送先で同一視すると `www` サブドメインが別管理下にある環境でoutbound をそちらへ向けられる) (前者は `ErrInvalidActor`、後者 2 つは破棄)。`sharedInbox` の選択順も upstream の `x.sharedInbox ?? x.endpoints?.sharedInbox` に揃えた。**検証が無かった頃に取り込まれた既存行は直らない** — `fetchActor` が失敗するので `refreshActor` は `lastFetchedAt` 以外の列を更新せず、`user.inbox` に残った値がそのまま配送先に使われ続ける (profile / 鍵ローテーション / `movedTo` の追従も止まる)。検出は `SELECT id, uri, inbox, "sharedInbox" FROM "user" WHERE host IS NOT NULL AND (inbox IS NULL OR regexp_replace(lower(split_part(split_part(split_part(inbox,'//',2),'/',1),'@',-1)), CASE WHEN inbox LIKE 'https://%' THEN ':443$' ELSE ':80$' END, '') <> lower(host))`。**scheme ごとの既定ポート・userinfo・大文字小文字だけ正規化し `www.` は剥がさない** (剥がすと `sameDeliveryHost` が弾く行を見逃す。`:(443\|80)$` と一括で剥がすと `https://h:80/` のような非既定ポートの行を取り逃す)。実 PostgreSQL で 10 パターンを流して `sameDeliveryHost` と一致することを確認済み。punycode と Unicode IDN が混在する行、`user.host` にポートが入っている行、scheme が大文字の行、fragment 付きの inbox は偽陽性になりうる。**不正な percent escape (`%zz`) は偽陰性** — SQL では正常に見えるが `net/url.Parse` が弾いて `sameDeliveryHost` が false を返す。末尾改行 / 前後の空白 / 途中の tab は `trimWHATWGURL` が upstream の `new URL()` と同じだけ除去して**保存値ごと正規化する**ので、該当行は次の refresh で自動的に直る。**これは proxy でしかない** — 詰まるかどうかは相手が今返している document で決まるので、相手が直していれば自己回復する。**破棄の粒度だけ違う**: upstream は選ばれた 1 つを検証して不正なら両方消すが、mk-go は 2 つを独立に検証して不正な方だけ消す (残る値は host 検証済みなので安全側) (#2662)。(3) `featured` / `sharedInbox` / `endpoints.sharedInbox`: upstream の `getApId` は**配列を見ない** (`value.id` が undefined になって throw) が、mk-go は先頭を採る。`sharedInbox` 側は `validateActor` の中なので upstream では**その actor ごと reject** になる (`ApPersonService.ts:157`。`new URL(sharedInbox)` が throw する形も同じ)。mk-go は先頭を採ったうえで `sameDeliveryHost` に通し、通らなければ**その値だけ**捨てる。配送先の host 検証は同じ値に効くので緩いのは「actor を落とすかどうか」だけ。(4) `movedTo` / `alsoKnownAs`: upstream は生値をそのまま使う (`movedToUri: person.movedTo` / `toArray(person.alsoKnownAs)`) ので `{"id": ...}` 形式は一致判定に通らないが、mk-go は id を剥がすため通る。移行の認可 (`alsoKnownAsContains`) に効くが、値を publish するのは移行先サーバー自身なので権限的な穴にはならない。`to` / `cc` は要素単位で読めないものを落とす (upstream は `getApIds` が throw して Note ごと reject する)。要素を落とすと可視性は**狭い側**に寄る。`to` の `#Public` が読めない形 (`{"type":"Link","href":...}` など `id` を持たない object) で来ると、`cc` に followers があれば `followers`、`cc` も読めなければ `specified` (visibleUserIds が空なので事実上誰にも見えない) まで落ちる。document ごと捨てるより影響が小さいため採った。ただし `attributedTo` / `to` を**読めるようになったこと自体**で結果が変わる入力もある: `Create` activity の `to` が `{"id": #Public}` 形式のとき、修正前は audience の union に載らず specified だった Note が public になる (upstream 一致)。`attributedTo` の object 形式は upstream の inbox 経路 (`ApInboxService` の `actor.uri !== note.attributedTo` 生値比較) より緩いが、抽出後の値が配送 actor と一致する必要があり host 検証も同じ値を使うので偽装耐性は落ちない |
| `ap/show` が Note 化に失敗したとき | `api/ap/handler.go` | **生の AP document を `{"type":"Note","object": <raw>}` として 200 で返す** (upstream は `createNote` が失敗すれば throw / `NO_SUCH_OBJECT` で、生 AP JSON を Misskey の Note として返すことはない)。受け付ける type は upstream の `validPost` 9 種に揃えてあるので、ingest が失敗しやすい `Video` / `Event` でもこの経路に来る。frontend は `user` / `userId` / `createdAt` の無い object を掴む |
| リモートの hashtag が NFKC 展開で長くなる場合 | `misc/hashtag/extract.go` | **正規化後に 128 code point を超えたら落とす** (#2662)。upstream は note-tag 経路が正規化**前**に `filter(<=128)` するだけ、user-tag 経路には長さ判定が無いので、`㍿` x100 (100 rune) が NFKC で 400 rune に膨らんで `tags varchar(128)[]` への INSERT ごと落ちる。mk-go は落として actor / Note は取り込む |
| リモート actor の icon / banner URL の長さ | `core/federation/resolver.go` | **列に収まらなければ落とす** (#2662)。列長は upstream と同じ (`user.avatarUrl` は varchar(1024)、`user.bannerUrl` は varchar(512))。upstream も `getPublicUrl(avatar, 'avatar')` の戻り (リモート非キャッシュなら元 URL を query に埋めたプロキシ URL) を入れるので**同じ 22001 で失敗しうる**が、**upstream はそれを user 行を作った後の `update` でやり try/catch で握る**ので actor は残って画像だけ落ちる (`ApPersonService.createPerson` の avatar/banner ブロック)。mk-go は同じ INSERT に載せているため、落とさないと**その actor が 1 行も作られない**。URL は truncate すると壊れるだけなので、画像を諦めて actor は取り込む = upstream の最終状態と同じにする |
| リモート actor の `preferredUsername` の検証 | `core/federation/resolver.go` | **upstream 同等** (#2662)。`validateActor` と同じ条件 (`typeof string`、`1..128`、`^\w([\w-.]*\w)?$`) を満たさなければ `ErrInvalidActor`。素通しすると `user.username` / `usernameLower` (varchar(128) NOT NULL) への書き込みが落ち、原因の分かりにくい DB エラーになる。**この検証は refresh 経路にも効く。** 検証が無かった頃に取り込まれた「条件を満たさない既存行」は、以後 `refreshActor` が更新に失敗し続ける。取得の増幅は抑えてある (`ErrInvalidActor` なら `lastFetchedAt` を進め、鍵の取り直しは `keyFetchBackoff` で 5 分に 1 回まで) が、**その actor の profile は更新されず、鍵ローテーションにも追従できない**。既存行は `SELECT id, uri, username FROM "user" WHERE host IS NOT NULL AND username !~ '^[A-Za-z0-9_]([A-Za-z0-9_.-]*[A-Za-z0-9_])?$'` で特定できる (実 PostgreSQL で検証済み)。**Go の正規表現をそのまま貼らないこと。** PostgreSQL の ARE は bracket 内の `\w` が外側の括弧を失うので `[\w-.]` が `_`(0x5F)→`.`(0x2E) の逆順レンジになり `invalid character range` で実行自体が失敗する。並べ替えて `[\w.-]` にしても、PostgreSQL の `\w` は UTF8 DB ではUnicode 文字を含むため `日本` のような**非 ASCII username を「正常」と報告する** (Go の `\w` は ASCII なので `validRemoteUsername` は false)。文字クラスを明示するのが唯一安全。`length(username) > 128` は列が varchar(128) なので死節。`user` 行の削除はノート・フォロー関係まで巻き込むので、消すなら影響を確認してからにすること |
| リモート actor の `vcard:bday` / `vcard:Address` が string でないとき | `activitypub/types.go` | **upstream より緩い** (#2662)。upstream は TS の型が `string` なだけで実行時検証が無く、`vcard:bday` は `.match()` が TypeError になり、`vcard:Address` は非 string がそのまま `location` に代入される。mk-go は document を通す。**JSON-LD の展開形 (`{"@value": ...}` / `["x"]` / `[{"@value": "x"}]`) は剥がして値を拾い**、それでも読めない形は捨てる (表示用の付加情報でしかないため) |
| AP dereference route の一部欠落 | `server/router.go` | **保留** (#2507)。`/follows/<follower>/<id>` (Follow activity id)・`/users/<id>/likes/<id>` (Like id)・`/emojis/<name>` (emoji tag id) は外向きに広告するが dereference route が無く 404。Follow / Like の id は Accept / Undo の相関にしか使われず他実装が dereference する事例は稀、emoji は tag に inline embed 済みで dereference 不要のため。`<note URI>/activity` は #2507 で実装済み。signature の keyId (`/users/<id>#main-key`) は actor 本体の fragment なので actor route で解決され、upstream の `/users/:user/publickey` 相当は不要 |
| 通報 (Flag) の comment 書式 | `core/federation/processor.go` | **意図的**。upstream は `` `${content}\n${JSON.stringify(uris, null, 2)}` `` (2 space の pretty print、`ApInboxService.ts:576`) だが mk-go は compact。`abuse_user_report.comment` の本文だけの差で、既存の通報との一貫性を優先して揃えていない (#2665) |

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
| `bcryptCost` | account password のハッシュ強度 (既定 10、範囲 4-31)。upstream は全経路 cost 8 固定で設定不可 |
| `crossOriginOpenerPolicy` | `Cross-Origin-Opener-Policy` の値 (既定 `off`)。upstream はテスト専用の cross-origin-isolation モードでしか出さない |
| `MK_*` 環境変数オーバーライド | upstream に同等機構なし |

逆方向 (upstream にあって mk-go に無い): `threadPoolSize`、`logging.format` / `logging.level` / `logging.domains` / `logging.access` (2026.7.0 のログ基盤刷新分。`logging.sql.*` は mk-go にもある)、`sentryForBackend.disabledIntegrations`。

---

## 4-1. WebSocket streaming チャンネル

| チャンネル | 内容 |
|---|---|
| `notifications` | **mk-go 独自**。upstream の 18 チャンネルに無い (upstream は `main` に通知を流す) ので mk-go は 19。通知だけを購読したいクライアント向け。**これに依存するクライアントは Misskey TS では動かない**ので、drop-in で戻す可能性があるなら `main` を使うこと |

upstream の 18 チャンネルは**すべて実装済み**で、名前も upstream に揃えてある。
以下は wire 上のチャンネル名 (`connect` の `channel` に渡す値 = upstream の `chName`)。
**ソースのファイル名は kebab-case だが、チャンネル名は camelCase** なので取り違えないこと
(`chat-room.ts` の `chName` は `chatRoom`)。

```text
admin antenna channel chatRoom chatUser drive globalTimeline hashtag
homeTimeline hybridTimeline localTimeline main queueStats reversi
reversiGame roleTimeline serverStats userList
```

この一覧と上の表の合計が `internal/server` の `streamRegistry` 登録名と一致すること
は `TestDivergenceDoc_StreamChannelsMatchRegistry` が固定する。ただし固定できるのは
**mk-go 側だけ**で、「upstream は 18」「名前も upstream に揃えてある」の検証は入って
いない (`test-shards` は submodule を checkout しない)。upstream が増減した場合は
submodule bump の PR で人が見る。

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
| `2026.7.0-mk.11` | インスタンス情報ページにプラグイン用のスロットを追加 |
| `2026.7.0-mk.12` | 起動時のスピナーを mk-go 独自のものにする |
| `2026.7.0-mk.13` | 承認制の登録の審査画面を追加 |
| `2026.7.0-mk.14` | 承認制の登録の申請ページを追加 |
| `2026.7.0-mk.15` | 承認制の設定をモデレーションへ移動 |
| `2026.7.0-mk.16` | 登録可否の矛盾する組み合わせを選べないようにする |
| `2026.7.0-mk.17` | 承認制はメール必須が OFF なら設定できるようにする |
| `2026.7.0-mk.18` | 承認制の申請をクレームコード方式にする |
| `2026.7.0-mk.19` | ビルド生成物 `server-plugins.generated.ts` のローカル版を戻す revert |
| `2026.7.0-mk.20` | 申請フォームの項目を管理者が定義できるようにする |
| `2026.7.0-mk.21` | 申請の登録で返りうるエラーコードを表示する |
| `2026.7.0-mk.22` | 承認済みの登録をメール確認に対応させる |
| `2026.7.0-mk.22a` | ジョブキューの Timeline から架空の試行時刻を消す (#2689)。バグ修正はこれ以降 `mk.<N><英字>` で刻む |
| `2026.7.0-mk.22b` | ジョブキューの Timeline に再試行を実時刻で並べる (#2692)。`mk.22a` で行ごと消してしまい再試行が見えなくなっていたのを、mkq が記録するようになった実時刻で戻す |

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
| `pages/admin/job-queue.job.vue` | Timeline の試行を `attemptsAt` の実時刻で並べる。upstream は `timestamp + i` という架空の時刻 (作成 i ミリ秒後) でイベントを作り表示だけ `at ?` にしていたが、Bull は attempt ごとの時刻を保存しないので**並べるための時刻がそもそも無い**。全試行が「作成直後」に固まって時系列として嘘になり `(+delta)` も無意味だった (#2689)。mkq が記録するようになったので実時刻で出す (#2692)。記録が無い job は回数だけを Processed 行に添える (架空の時刻には戻さない) |

`runtime` block が無い応答 (純正 backend) では該当 UI を出さない。

---

## 4-3. job queue の構成差分

upstream は用途ごとに **10 queue** に分けるが、mk-go は **8 queue** に集約している (`internal/queue/driver/mkqdriver` の `QueueNames`)。処理する仕事は同じで、束ね方だけが違う。

| upstream の queue | mk-go の実体 |
|---|---|
| `deliver` | `deliver` |
| `inbox` | `inbox` |
| `system` | `maintenance` (cron 群: chart tick/resync/clean, checkExpiredMutings, clean, cleanRemoteNotes, checkModeratorsActivity, instanceRefresh, retentionAggregate, chunkedUploadGc) |
| `endedPollNotification` | **queue ではなく常駐 goroutine** (`corepoll.ExpiryWorker`、60 秒間隔) |
| `postScheduledNote` | `deliver` の `note:postScheduled` |
| `db` | `export` の `export` / `import` / `importCustomEmojis`、`deliver` の `maintenance:deleteAccount` |
| `relationship` | `relationship` |
| `userWebhookDeliver` | `webhook` の `webhook:user` |
| `systemWebhookDeliver` | `webhook` の `webhook:system` |
| `objectStorage` | `objectStorage` |
| — | `push` (Web Push 配信、upstream は system queue 内で処理) |

`objectStorage` は `deleteFile` / `cleanRemoteFiles` とも upstream と同じ job 構成 (#2325)。振り分けも upstream に揃えてあり、ローカル FS 保存 (`storedInternal=true`) の実体は同期削除、object storage 上の実体だけを queue に逃がす。`clean-remote-files` は「job 1 本が内部でバッチ削除を回す」形も upstream と同じで、リモートキャッシュの件数ぶん job を積んで Redis を圧迫することはない。ただし mk-go はそもそもリモートメディアをキャッシュしないので、この job の対象は構造的に 0 件になる (§5.5)。job 構成を upstream に揃えてあるのは drop-in 復路のため。

`note:postScheduled` / `maintenance:deleteAccount` が task type の接頭辞と違う `deliver` に載っているのは意図的なもの。いずれも実行結果が連合配送につながるジョブで、worker 2 本の `maintenance` より 16 本の `deliver` の方が捌ける。task type と queue の対応は `internal/queue/routing_test.go` が表として固定しており、変えると落ちる (#2327)。

cron の多重実行防止は **job option ではなく mkq の job ID 設計**で担保している。mkq は発火 job に決定的な ID (`repeat:<scheduleID>:<nextMillis>`) を振り、`updateJobScheduler-12.lua` が `EXISTS` で重複を弾いて `duplicated` イベントを記録する。加えて `producerId == currentDelayedJobId` の判定で、直前の発火を処理した worker だけが次を積める。

そのため `Scheduler.Register` に渡す `WithUnique` / `WithMaxRetry` / `WithProcessIn` は mkq driver では drop されるが、**現状の呼び出し方では実害が無い** (#2405)。`WithMaxRetry` は mk-go の cron が全て 0 = リトライ無しを渡しており mkq の既定と同じ、`WithProcessIn` はどの cron も渡していない。asynq driver は 3 つとも honour するが、結果として観測される挙動は一致する。この性質は `TestScheduler_RepeatedRegisterDoesNotDuplicate` で固定してある。

`relationship` は #2403 まで `deliver` に相乗りしていたが、専用 queue に分離した。大量 follow (アカウント移行 / import) が `deliver` の worker を占有して AP 配信そのものを詰まらせ、片方を絞るともう片方も絞られる状態だったため。worker 数は 4 で、upstream の 16 とは違う。relationship job は DB bound (following 行 + カウンタ + stream publish) で外向き HTTP は `deliver` へ再 enqueue されるだけなので、`db.maxOpenConns` (既定 25) を HTTP 経路と共有する以上 16 を割くと Web 側のテールレイテンシに響く。`relationshipJobConcurrency` / `relationshipJobPerSec` はこの分離で初めて実効を持つようになった (それ以前は config として読むだけの no-op)。

`objectStorage` の worker 数だけは upstream の 16 に対し mk-go は 4。実体削除は S3 への I/O 待ちが主で 1 worker あたりの効率が良く、一括削除の並列度を job 数で稼ぐ設計でもないため、`deliver` と同じ理由 (worker 数 ≒ Redis 接続数) で抑えている。

再試行は **mk-go の方が手厚い**。upstream は `attempts` を設定しないので単発試行で終わり、失敗した実体は failed job として残るだけで自動復旧しない。mk-go は指数バックオフ付きで 4 回まで再試行する (object storage の一時的な 5xx / タイムアウトは待てば回復するため)。queue 自体が使えないときは同期削除にフォールバックし、実体を取りこぼさない。

**管理画面のタブはこの構成に合わせて fork 側で書き換えている** (`misskey-js` の `queueTypes`、`2026.7.0-mk.8`)。upstream のタブは API 応答ではなくこの定数から生成されるため、書き換えないと mk-go に存在しないタブが常時ゼロ表示になり、実在する `push` / `export` / `webhook` / `maintenance` / `objectStorage` が画面から見えなくなる (#2323)。**mk-go の queue を増減したら fork の `queueTypes` も合わせること。**

## 5. 運用・性能機能 (mk-go 独自)

| 項目 | 内容 |
|---|---|
| inbox verify-in-worker 化 | HTTP handler は body + signature header を payload 化して即 202、署名 verify / host block / instance touch は worker 側。HTTP 受信 rps が **TS の 2.6〜2.8 倍** |
| mkq queue driver | BullMQ wire 互換の Go 実装。queue-bench で BullMQ / asynq / mkq を 3-way 比較 (送信 rps は mkq 優位、drain time は asynq 優位。詳細は [queue-bench.md](queue-bench.md)) |
| AIMD auto-scale worker | per-queue の動的 Resize + Prometheus metrics。worker 現在数 / 範囲 / scale 履歴は admin UI にも出す (#2277) |
| Prometheus `/metrics` | `mk_job_workers_active` / `mk_job_queue_pending` / `mk_job_dispatch_wait_seconds` ほか。**無認証公開なので LB/nginx ACL 必須**。admin から読めない分は `admin/queue/*` の `runtime` block が補う (#2277) |
| `admin/server-metrics` | mk-go プロセス自身の統計 (goroutine / heap / GC / uptime / version) を返す mk-go 独自 endpoint (#2395)。upstream に対応物は無い。`admin/server-info` はホストマシンの静的スペックを返すもので別物。control panel のダッシュボードから 10s ポーリングで表示する (`ReadMemStats` が stop-the-world を伴うため間隔を詰めない)。DB / Redis の接続プールは当初含めていたが、常時ほぼ一定で画面のノイズになるため UI ごと落とした |
| timeline JSON cache | first-page per-viewer cache (opt-in) |
| mediaproxy のアニメ pass-through | `?emoji` / `?avatar` / `?preview` で gif/apng を decode せず raw 返し (Go std の `image.Decode` は 1 frame しか返さず静止画化するため) |
| URL preview の charset 自動正規化 | Content-Type + `<meta charset>` から UTF-8 化。Shift_JIS / EUC-JP / ISO-2022-JP で文字化けしない (upstream は外部 `summaly` package に委譲しているため同等機能の有無は未確認) |
| instance touch buffer | 同一 remote host の連続 inbox 受信を集約。**upstream も `CollapsedQueue` で per-host に集約している**。差分は flush 窓が mk-go 1s / upstream 5 分という点だけ |
| chart tick の DB 再集計 | **upstream も同機構を持つ** (`TickChartsProcessorService` / `ResyncChartsProcessorService`)。mk-go は cron 実装が異なるだけで差分ではない |
| VAPID 鍵の自動生成 | Service Worker 有効化時に鍵が両方空なら生成して meta に注入。operator 指定鍵は尊重。明示的な空 / null 送信は「ローテーション指示」として扱い再生成する。fork frontend は保存後に `admin/meta` を引き直して生成鍵を表示する (#2272) |
| `+host` / `-host` sort key | `federation/instances` の host 昇順/降順 |
| `signatureCapability` | `federation/instances` / `federation/show-instance` の additive field (#2393)。相手サーバーが対応する署名方式を「宣言 (actor の assertionMethod)」「受信観測 (verify に成功した鍵種別 / LD-Signature の受信)」「配送観測 (Ed25519 署名の配送が 2xx)」の 3 系統から判定して返す。観測が無い host は null。記録先は mk-go 独自の `instance_signature_capability` テーブルで、TS は本テーブルを認識しない。`federation/stats` は公開エンドポイントなので常に null (追加クエリを撃たない) |
| `notes` の noteIds bulk lookup | upstream の public-note timeline に加え `{noteIds:[...]}` bulk (max 100、visibility filter 付き) を同 endpoint で両立 |
| `webpublicUrl` | drive entity の拡張 field (proxy 化済で IP leak なし) |
| mention による reply filter escape | viewer が `note.mentions` に含まれれば withReplies 設定に関係なく reply gate を pass。streaming と fanout の両方に実装 |
| streaming publish 時の suspended フィルタ | 凍結ユーザー (本人 / reply 先 / renote 先) の note を WebSocket publish から除外する (#2624)。**upstream は streaming に suspended フィルタを持たない** (`packages/backend/src/server/api/stream/` に `isSuspended` の参照が無い)。upstream で顕在化しないのは suspended ユーザーが投稿できないためで、mk-go では**凍結したリモートユーザーの note を対象にした inbound Announce が相手インスタンスから届き続ける**ため、取得経路にしかフィルタが無いと「リアルタイムには流れるがリロードで消える」という食い違いになっていた。gate は `internal/stream` の publish 1 箇所に置く (home / local / global / userList / channel / hashtag / roleTimeline / antenna が全て同じ publisher を通る)。**Redis の timeline list には従来どおり積む** — fanout 側で打ち切ると凍結を解除しても list に ID が無いままになり、取得は list が limit を満たす限り DB へ fallback しないため復活しなくなる。あわせて channel 一覧 (`ListByChannelID`) と hashtag 一覧 (`SearchByTag`) にも同じ 3 author の除外を追加した (これらは `applyTimelineFilter` を通らないため、publish だけ止めると逆向きの食い違いになる) |
| effective-policy provider | build-time pluginがnative role解決へ動的に寄与するmk-go独自機構。成功結果は明示的invalidationまでLRUへ保持する。寄与はnative roleやDBへ永続化されないため、plugin停止・buildからの除外・Misskey TSへの切り戻しで消え、利用者の実効権限が変わる。特に制限方向の寄与は切り戻しで権限を緩めうる。停止後も維持すべき判定はnative roleとして永続化し、切り戻し前に`admin/server-plugins`の`effectivePolicies`宣言とnative fallbackを確認する |

---

## 5.5. リモートメディアをローカルにキャッシュしない (意図的)

upstream は `cacheRemoteFiles` が真のとき、連合で流れてきたメディアの実体を自サーバーの
Drive へ保存する。**mk-go はこれを実装しない。** 未実装ではなく意図的な設計判断。

### 挙動

| 対象 | upstream | mk-go |
|---|---|---|
| ノート添付 | `cacheRemoteFiles` が真なら実体を Drive へ保存 | **link 行のみ** (`isLink=true` / `size=0` / `md5=""`)。実 fetch しない |
| リモートの avatar / banner | Drive へ保存しうる | **URL 文字列を `user.avatarUrl` に持つだけ**。drive_file 行を作らない |
| 表示 | ローカルのキャッシュを配信 | メディアプロキシが都度取得して中継 (保存しない) |

`meta.cacheRemoteFiles` / `cacheRemoteSensitiveFiles` の**列と API field は残す**
(drop-in 互換のため)。値は保存・返却されるがダウンロード判定には使わない。
関連する admin UI は無効表示にして理由を出している (fork frontend)。

`admin/drive/clean-remote-files` も実装は残るが、対象 (`isLink=false` の remote file) が
構造的に存在しないため常に 0 件。

### 理由

1. **相手の削除の権利**。キャッシュを持つとそのコピーのライフサイクルを自分が所有する。
   相手が消しても、Delete 配送が届かない・連合が切れている・ノートは残してファイルだけ
   差し替えた等のケースでコピーが残り続ける
2. **リスク**。連合を流れてくる違法コンテンツが自ストレージに保存される。都度取得して
   中継するのとは実務上の立場が違う
3. ストレージ増加の抑制 (上の 2 つに比べれば副次的)

### キャッシュしないことの弱点と、その埋まり方

| 一般的な弱点 | mk-go の状況 |
|---|---|
| クライアントの IP が相手サーバーに漏れる | メディアプロキシが吸収する (drive / avatar / banner 等は server-proxy 経由) |
| 閲覧のたびに再取得して帯域を食う | プロキシ応答に `Cache-Control` (最長 3 日) が付き、CDN / ブラウザがキャッシュする |
| 相手サーバーが消えると表示が壊れる | **埋まらない。** キャッシュしない設計の本質的なトレードオフとして受け入れる |

なおエッジキャッシュにも複製は載るが、性質が違う。TTL で自動失効する一時的なインフラ層で
あって、バックアップにも入る永続レコードではない。「削除の権利」の観点ではこの差が効く。

### 影響する upstream 機能

  - `DriveService.expireOldFile` (容量超過時の LRU 退去) — **不要**。退去すべき実体が
    存在しない。実装しかけたが、キャッシュしない以上 dead code になるため破棄した
  - remote user への `driveCapacityMb` gate — 同様に意味を持たない。`size=0` の link 行は
    使用量に乗らない

## 6. セキュリティ関連の差分

| 項目 | upstream | mk-go |
|---|---|---|
| antenna の未読 (`hasUnreadAntenna`) | **機能ごと止まっている。** `UserEntityService.getHasUnreadAntenna` は実装がコメントアウトされ `return false; // TODO` | 実際に `antenna_note_unread` を引いて算出する。**mk-go の方が実装している側** (#2406)。あわせて antenna timeline の閲覧で未読行を消す。upstream は行を作らないので既読化も要らないが、mk-go は自前で持つ必要がある |
| shiki (コードブロックの syntax highlight) の配信元 | `esm.sh` から動的 import (`vite.config.ts` の `externalPackages`) | **同じ** (バンドルに切り替えない)。CSP の `script-src` に `https://esm.sh` を明示的に許可している。**バンドルすると 30 ロケール分が複製されてビルド成果物が 242MB → 508MB に倍増する**ため (2026-08-09 実測。JS が 13,576 → 23,610 ファイル)。利用者 1 人あたりの転送量は変わらないが、軽量さを損なう。代償として、コードブロックを表示する閲覧者の IP とリファラが esm.sh に渡る。言語を絞ってバンドルすれば両立できる可能性はある (未検証) |
| frontend HTML の CSP | **無し** | `frontendContentSecurityPolicy` で opt-in (既定 `off`)。**mk-go 独自の硬化** (#2425)。段階導入のため `report-only` から始め、違反を潰してから `enforce` へ切り替える運用。現段階では SSR shell の inline script / SVG の inline style 属性が残っているので `'unsafe-inline'` を許している (nonce / hash 化は別段階)。`frame-ancestors` は含めない — `X-Frame-Options` 側が `/embed/` の除外を持っており、二重管理を避けるため |
| `Referrer-Policy` | **設定しない** (`packages/backend/src/server/` に 1 件も無い) | 全応答に `strict-origin-when-cross-origin` を付ける。**mk-go 独自の硬化** (#2404)。無いとノート本文の外部リンクを踏んだ際に閲覧中の URL が path ごと Referer として送られる。Misskey の URL は `/notes/<id>` / `/@user` のように**何を見ていたかがそのまま分かる**形なので、遷移先に閲覧内容が漏れる。`no-referrer` まで強めないのは、cross-origin へ origin だけは送る方が連合先からの流入把握や hotlink 判定を壊さないため |
| identicon の CSP | 付けない | `default-src 'none'; style-src 'unsafe-inline'` を付ける。**mk-go 独自の硬化** (#2404)。upstream が他のアセット route (`/emoji` / `/twemoji` / `/fluent-emoji` / `/files`) に付けているものと同じ値で揃えた。identicon は mk-go が実際に PNG バイトを返す route なので、他と扱いを分ける理由が無い |
| account password のハッシュ強度 | 全経路で bcrypt cost 8 固定 (`bcrypt.genSalt(8)`)。設定不可 | 既定 cost 10 で、`bcryptCost` で 4-31 に変更できる。**さらにログイン成功時に古い強度のハッシュを焼き直す** ので、設定を上げれば既存の利用者も戻ってきた順に移行する。upstream にこの仕組みは無い。cost は `$2a$NN$` に埋まるので、上げても drop-in で TS 側が検証できる |
| native session token の強度 | `secureRndstr(16)` = 62 文字集合 16 文字 (約 95 bit) | **同等** (英数字 62 文字集合 16 文字)。長さ 16 は TS の `isNativeUserToken` が長さだけで native / app token を判別するため動かせない。かつて 16 進 16 文字 (64 bit) で upstream より弱かったのを揃えた |
| webhook 本文の完全性 | 共有秘密を `X-Misskey-Hook-Secret` に**平文で載せるだけ**。受信側は本文が改ざんされていないかを確認できない | 同ヘッダは互換のためそのまま送りつつ、`X-Hub-Signature-256: sha256=<hex>` (HMAC-SHA256(secret, body)) を**追加**する。**mk-go 独自の硬化**。未知のヘッダは無視されるだけなので既存の受信側は影響を受けない。秘密が空なら署名しない (空鍵の HMAC は誰でも作れるため) |
| 受信 activity の再投函 | **無し。** ハンドラの冪等性で二重配送を吸収する設計 | 署名検証を通った activity の id を短命 (Date の窓 * 2 + 余裕) に覚えて、同じものの再投函を落とす。**mk-go 独自の硬化**。冪等性は二重配送を吸収できても「古い Undo(Follow) / Undo(Block) を後から差し込む」形は吸収できない。覚えるのは**処理が成功してから**で、先に覚えるとキューの再試行を自分で捨ててしまう。guard 障害では落とさない (fail-open)。id を信用するのは authorizeActor が id の host と actor の host の一致を確かめた後だけ — 未署名経路で覚えると他人の id を先に登録して本物を落とせる |
| HSTS | `config.url` が https かつ `disableHsts` が偽なら `strict-transport-security: max-age=15552000; preload` | **同じ値・同じ条件**。かつて mk-go は `disableHsts` を設定として読んでいたのに header を出しておらず、TS から切り替えると HSTS が黙って消えていた (parity 修正)。`includeSubDomains` を足さないのも upstream に合わせている — 同じドメインの別サブドメインを平文で運用している構成を切替の瞬間に壊さないため |
| `Cross-Origin-Opener-Policy` | テスト専用の `enableCrossOriginIsolation` を立てたときだけ `same-origin` | `crossOriginOpenerPolicy` で opt-in (既定 `off`)。**mk-go 独自の硬化**。既定を off にしてあるのは、外部アプリが認証ページをポップアップで開いて閉じるのを待つ形の連携を切りうるため。MiAuth / OAuth は callbackUrl で完結するので通常は問題にならないが、切れたときの症状 (「認証したのにアプリが気づかない」) から原因に辿り着きにくい |
| `Cross-Origin-Resource-Policy` | 付けない | **付けない (意図的)**。`/files/` に付けると、他インスタンスのブラウザがドライブの画像を直接読む構成で表示が壊れる。Misskey は既定でメディアプロキシ (サーバー側取得) を通すので CORP の対象外だが、フロントが生 URL を使う経路が残っており、壊れ方が「一部の画像だけ出ない」形になって切り分けが難しい。得られる保護に対して代償が大きいので入れない |
| drive requestHeaders の credential 除去 | 全 header を生保存 (`drive/files/create.ts`) | `authorization` / `cookie` / `set-cookie` / `x-api-key` / `api-key` / `proxy-authorization` を保存しない deny-list。**mk-go 独自の硬化** |
| TOTP replay guard | **2026.6.0 で実装済** (`UserAuthService.validateOtp` が Redis `SET NX EX` で使用済トークンを記録、TTL 90s) | 同等機構を持つ (mk-go が先行実装)。**差分なし** |
| inbox admission の署名対象 header 強制 | `(request-target)` / `host` / `date` / `digest` の要求、Host 一致、SHA-256 body 照合を実施 (`ActivityPubServerService.inbox`) | 同等。**mk-go 固有なのは body 照合を定数時間比較 (`subtle.ConstantTimeCompare`) にしている点のみ** |

> TOTP replay guard と inbox admission は、かつて mk-go 独自の硬化だったが upstream が追いついて現在は同等。コード内の「upstream は持たない」旨のコメントは陳腐化している箇所があるので、見つけたら更新すること。

---

## 7. 意図的な安全側 divergence

いずれも upstream より厳しい / 正確な方向。error `code` / `id` は upstream と一致させ、status のみ異なるものが多い。

なお「API エラーの HTTP status を 404 / 403 で返す」差分は解消済み。upstream は
`ApiError` の kind 既定が `client` なので対象が存在しない場合も 400 を返す。
mk-go は意味的に正確な 404 / 403 を返していたが、upstream から切り替えたときに
status で分岐するクライアントが壊れるため、drop-in 互換を優先して 400 に揃えた
(本家 e2e を mk-go に向けて回した際に検出、44 種 / 230 箇所)。

| 項目 | upstream | mk-go |
|---|---|---|
| AID/AIDXの上限外timestamp | AIDは8桁を超えて固定長を外れ、AIDXは下位8桁へwrapする | **base36 8桁の最大値へ飽和する。** 固定長を維持し、時系列順序の逆転を防ぐ安全側乖離 (#2672) |
| リモート actor の `movedTo` 消滅 | `movedToUri: person.movedTo ?? null` で null に戻す | **既存値を温存する** (削除は追わない)。一時的な欠落でクリアすると、次の取得が「無→有」の遷移に見えて `movedAt` が打ち直され、移行の時間窓 (2h / 14 日) の基準が壊れるため。移行の取り消しに追従できない代わりに基準が安定する (#2412) |
| リモート actor の `vcard:Address` の長さ | truncate せずそのまま保存 | **128 文字 (rune) で切る**。`user_profile.location` は varchar(128) で、超過値を渡すと insert / update ごと失敗し、同じ書き込みに乗っている `description` まで巻き添えになる (create 経路では profile 行が 1 行も作られず、以後の refresh も同じ失敗を繰り返す)。description の 2048 文字 truncate と同じ扱い (#2661) |
| リモート actor の profile `fields` の件数 | `analyzeAttachments` に上限なし | **16 件で打ち切る**。ローカルの `i/update` が `maxItems: 16` なので揃える。上限が無いと任意件数を送り込める (#2661) |
| リモート actor の `vcard:Address` / profile `fields` の空白 | trim も空排除もせず保存 | **trim して空なら NULL / entry ごと落とす**。ローカルの `i/update` と同形の正規化 (#2661) |
| リモート actor の profile 由来文字列に含まれる NUL | **未処理** (upstream も同じ理由で書き込みが失敗する) | **除去する**。PostgreSQL の text は NUL を受け付けず (SQLSTATE 22021 `invalid byte sequence for encoding "UTF8": 0x00`)、jsonb も拒否する (22P05)。**SQLSTATE は protocol mode で変わる** — 本番の `internal/db` は pgx の extended protocol なので 22021、`internal/testutil` は `PreferSimpleProtocol: true` なので同じ入力が 08P01 (`invalid message format`) になる。運用ログを grep するときは 22021 の方。同じ書き込みに乗っている他の列まで巻き添えになり、create 経路では `user_profile` 行が 1 行も作られない (以後の refresh も同じ失敗を繰り返す)。対象は `vcard:Address` / profile `fields` の name・value / `description` (`_misskey_summary` は `mfm.FromHTML` を通らない)。`user.name` も同様に除去する。`user.avatarUrl` / `user.bannerUrl` は**除去せず値ごと捨てる** (NUL を抜いた URL は別物なので取りに行っても無駄)。`user.tags` / `note.tags` は**正規化後に NUL を含む tag を落とす** (varchar(128)[] は NUL を受け付けない)。`user.emojis` は未処理。**長さ超過**なら `emoji.name varchar(128)` への insert が落ちて `upsertEmojis` が `continue` し 1 件だけ除外されるが、**NUL の場合は先に batch SELECT (`FindManyByNamesAndHost`) が 22021 で落ちて `return` する**ので、その actor / Note の絵文字が全滅する (actor 自体は作られる) (#2662)。`preferredUsername` は NUL を含む時点で不正なので除去ではなく actor ごと reject する (#2662) (#2661) |
| リモート actor の `name` の長さ | `truncate(person.name, 128)` (`stringz.substring`) | **128 rune で切る**。切らないと `user.name` (varchar(128)) への書き込みが SQLSTATE 22001 で落ち、actor がまったく作られない。upstream は書記素クラスタ単位の `stringz` なので境界がずれうるが、PostgreSQL の varchar はコードポイントで数えるため rune 単位のほうが上限に忠実 (#2662) |
| リモート Note の `cw` / `text` の長さ | truncate せずそのまま保存 | **512 rune で切る** (`note.cw` は varchar(512))。CW は**相手が自由に決められる値**で、長さの制限は送信側の実装次第 (upstream Misskey 自身は投稿時に 100 で弾くが、AP でそれを強制する仕組みは無い)。溢れると `noteRepo.Create` / Update 経路の `UpdateFields` ごと落ちて `ingestNoteWithCreated` が error を返し、**その inbox job が retry を使い切って dead になる** (既定 8 回。`defaultInboxJobMaxAttempts`、`internal/server/queue_factory.go`)。`text` は列が text 型なので長さは効かないが、NUL の除去は同じ経路で行う (#2723) |
| リモート actor の `uri` / `host` の長さ | 検証なし | **収まらなければ actor ごと拒否する** (`uri` は varchar(512)、`host` は varchar(128))。身元そのものなので切ると別人になり、捨てると lookup の鍵が無くなる。upstream は `uri` に `person.id` をそのまま入れるので同じ 22001 で失敗しうる。**gate は create 経路にしかない** (`refreshActor` は既存行専用で `uri` / `host` を書かない)。**拒否は多くの場合、署名検証の時点で起きる** — worker は `verifyPayload` で署名者を `ResolveActor` するので、署名者本人が該当するならその inbox job はそこで ack される (ハンドラまで届かない)。署名者以外 (第三者著者の note を解決する経路など) の結末はハンドラによる。いずれにせよ**その actor から activity が来るたびに 1 回 fetch する状態は続く** (行が作られない以上 `lastFetchedAt` を進める先が無い) (#2723) |
| リモート actor の `inbox` / `sharedInbox` / `featured` / `movedToUri` の長さ | 検証なし | **収まらなければ値ごと捨てて actor は取り込む** (いずれも varchar(512))。icon / banner URL (#2662) と同じ判断。`inbox` を捨てるとその actor への配送はできなくなるが、表示や mention の解決は生きる。**create 側が落ちれば actor が 1 行も作られず、refresh 側が落ちれば `lastFetchedAt` を含む UPDATE ごと失敗して inbound activity 1 件につき outbound fetch が 1 回走り続ける** (#2723) |
| リモート Note / Announce の `id` の長さ | 検証なし (`uri` に生値を入れる) | **収まらなければ document ごと拒否する** (`note.uri` は varchar(512))。切ると別の note を指す URI になり、**同じ activity の重複検出** (`FindByURI`) の鍵も壊れる (Undo(Announce) はこの URI を引かない — `ListRenotesOf` で announcer の renote を探す)。**拒否したあとの結末はハンドラで違う。** `isPermanentSkipError` を通す 4 つ (Like / Announce / Undo(Like) / Undo(Announce)) が対象 note を解決して踏んだ場合は **ack して drop** し、`handleCreate` の object・`handleAnnounce` 自身の `id`・`handleAdd` のピン留め対象は error を surface するので inbox job が retry を使い切って dead になる。**Collection に包まれて配送された場合は常に ack** (`handleCollection` が item の error を握る)。`isPermanentSkipError` はハンドラが**下位の**失敗を握るためのもので、ハンドラ自身の戻り値には効かない。gate の利得は原因が 22001 ではなく明示的な拒否として残ること (#2723) |
| リモート actor の `alsoKnownAs` に含まれる NUL | 未処理 (upstream も同じ理由で書き込みが失敗する) | **要素ごと落とす** (`user.alsoKnownAs` は text 列なので長さは効かないが NUL は 22021)。1 要素混ざっただけで actor の INSERT / refresh の UPDATE がまるごと失われる。切らずに捨てるのは、切った URI が移行の認可 (`alsoKnownAsContains`) の一致判定に使えないため (#2723) |
| リモート添付の `type` / `thumbnailUrl` / `blurhash` / `url` の長さ | `type` (sniff) / `thumbnailUrl` / `blurhash` はローカルで決まるので AP の申告値は入らない。**`url` / `uri` は違う** — `cacheRemoteFiles` off (既定) の isLink 経路では `image.url` を生で入れており、列も同じ varchar(1024) なので **upstream も同じ 22001 に晒される** | **列に合わせて扱いを分ける** (#2723)。`url` (varchar(1024) NOT NULL) は実体そのものなので**入らなければその添付を諦める**、`type` (128) は切ると別の MIME type になるので `application/octet-stream` に倒す、`thumbnailUrl` (512) / `blurhash` (128) は表示の補助なので値ごと捨てる。upstream は添付 1 件の失敗で Note ごと落とす (`ApNoteService`) ので、mk-go は元から安全側 |
| リモートインスタンスの nodeinfo の text field | 長さは無検査。ただし値そのものは正規化する — `softwareName` は `.toLowerCase()` (string でなければ `'?'`)、`themeColor` は `tinycolor` で検証して `#rrggbb` に正規化 (不正なら `null`) | **各列の上限 (`softwareName` 64 / `softwareVersion` 64 / `name` 256 / `description` 4096 / `themeColor` 64) で切り、NUL を除去する**。mk-go は元から `iconUrl` / `faviconUrl` だけ長さを見ていたが、**同じ `fields` map に載る**これらが無検査だと 1 列溢れただけで UPDATE 全体が落ち、当のガードの目的が同じ関数の中で破られる。**値の正規化は upstream に揃えていない** — `softwareName` を lowercase せず、`themeColor` も色として検証しないので任意文字列が入る (`themeColor` は upstream だと正規化の結果として列を溢れることが構造的に無い)。software block の判定 (`MatchSuspendedSoftware`) は case-insensitive なので回避には使えないが、`federation/instances` が返す値は upstream と変わる (#2723) |
| リモート添付の `name` の作り方 | `uploadFromUrl` が**実体を download** し、`pathname.split('/').pop()` (Content-Disposition があればそちら) を `validateFileName` に通し、不合格なら `untitled`。さらに `correctFilename` が**sniff した実型**の拡張子を補う | **置き場は upstream と同じにした** (#2723)。代替テキスト (AP の `name`) は `comment` にだけ入れ、`drive_file.name` は URL の basename から作る。差分は 4 つ。(1) mk-go はリモートメディアの**実体を保存しない** (5.5) ので **Content-Disposition を見ない** (寸法の復元で GET すること自体はある)。**Misskey 同士ではここでずれる** — upstream は自分が配信するファイルに `Content-Disposition: inline; filename=...` を付けるので、upstream 側は原ファイル名を採る。(2) **拡張子の補完をしない** (upstream が付けるのは sniff した実型で、相手の申告した `mediaType` ではないため)。(3) Go の `net/url` は WHATWG URL の正規化をしないので、`/a/%2e%2e` (upstream は畳んで `untitled`) と `/a\b.png` (upstream は `\` を区切り扱いにして `b.png`) がずれる。(4) upstream は `name === comment` のとき comment を落とすが mk-go は残す。Mastodon 系はいずれにも当たらないので一致する。`comment` の 512 は upstream と同じ値 (`DB_MAX_IMAGE_COMMENT_LENGTH`) だが、**数え方は違う** — upstream の `truncate` は `stringz.substring` = 書記素クラスタ単位なので、ZWJ 絵文字を含む alt text では 512 クラスタ = コードポイントでは 512 超になり upstream 側が列を溢れさせる。mk-go は rune 単位で切るので列に忠実 (`user.name` の 128 と同じ扱い)。**この変更より前に取り込んだ行は直らない** — 添付は URI で dedup するので `name` に代替テキストが入ったまま残る。**連合出力は元から無事** (renderer は upstream と同じく `Name: stringValue(f.Comment)` で comment を使う) |
| リモートメディアのキャッシュ | `cacheRemoteFiles` が真なら実体を自 Drive へ保存 | **保存しない** (相手の削除の権利 / 違法コンテンツ保持のリスク回避)。詳細と弱点は §5.5 |
| `notes/reactions` の可視性 | requireCredential:false で followers/specified note の reaction list も 200 | `CanSeeNote` gate で 404 |
| reaction / chat の可視性エラー | generic INTERNAL_ERROR (500) に包まれる | 403 ACCESS_DENIED (500 拡散を回避) |
| `admin/promo/create` | visibility check なし | public 以外を reject (将来の IDOR 先回り) |
| `/embed/clips/:clip` | clip の存在だけを見る (非公開 clip も埋め込める) | `isPublic` も見る。埋め込みは無認証で誰でも読める経路なので、本人だけが見えるはずの clip を配らない (#2389) |
| `federation/stats` の moderationNote | moderator には見せる | 公開 endpoint なので常に隠す |
| moderator inactive 判定 | 空集合で登録を無効化しうる | lastActiveDate 保持者 0 人なら何もしない |
| SSRF の IPv4-mapped IPv6 | `::ffff:0:0/96` を一律遮断 | 埋め込み v4 を IPv4 レンジで評価し private 埋め込みのみ遮断 (over-block より精密)。NAT64 / RFC6145 は別途遮断 |
| `renoteCount` の減算 | 減算しない (`incRenoteCount` しか無く、renote 削除時も据え置き) | Undo(Announce) で減算する。unrenote 後もカウントが残り続ける方が不自然なため (増分条件は upstream と一致させてあるので対称、#2283) |
| `users/search-by-username-and-host` | `UserSearchService` が 4 query の UNION。`updatedAt IS NULL` を拾うのは**フォロー済み分岐だけ**なので、未フォローかつ未投稿の user は検索に一切出ない | `usernameLower` 前方一致 + `followersCount DESC` の単純検索。新規 user もフォロー前に見つかる (#2286) |
| reversi surrender | pending game も終局させられる | NOT_STARTED で弾く (勝ち逃げ防止) |
| アンテナの `src: 'home'` | e4144a1 以降 `all` と同じ結果になる (upstream の e2e にも「BUG e4144a1 以降 home 指定は壊れている」と明記されている) | フォロー中ユーザーのみに絞る正しい実装を維持 |
| home / hybrid / local channel の reply gate | `withReplies` 系の条件を満たさない返信は流さない | 加えて **viewer が mentions に含まれる返信は流す** escape hatch を持つ (#1195)。ただし specified note の宛先 (`visibleUserIds`) は本文で mention されたわけではないので除外する |
| webhook の note embed gate | note/reply/renote で skipHide | 全イベントで gate、viewer/repo nil は fail-closed |
| streaming / 通知の未知 visibility | — | fail-closed (誤配信しない) |
| URL preview の scheme 判定 | 生文字列の case-sensitive `startsWith` | case-insensitive (RFC 3986 準拠)。非 http(s) の thumbnail / icon は値を落とす |
| `cleanRemoteNotes` のクリップ保持 | `note.clippedCount = 0` で判定 | 加えて `clip_note` を直接 `NOT EXISTS` で見る。mk-go はクリップ件数の非正規化カウンタを維持せず `clip_note` を数える設計 (#2243) なので `clippedCount` は常に 0 で、upstream の条件をそのまま移植するとクリップ済みノートを保護できない (#2329)。`clippedCount` / `pageCount` の比較自体は TS から切り戻したインスタンスのために残してある |
| `securityKeysAvailable` | unset-mfa で触らない (`securityKeys` を毎回 count するため陳腐化しない) | 全鍵削除に合わせ false にする (mk-go は列をキャッシュとして読むため) |
| fetch-rss の URL 正規化 | WHATWG `new URL()` | host 小文字化 / default port 除去 / 空 path 補完まで再現。**IDN の punycode 変換 (UTS#46) は行わない** (取得は成功するが Unicode 表記と punycode 表記で cache key が分かれる)。空 userinfo (`http://@example.com/`) は upstream が許可するのに対し拒否 |
| `MK_ONLY_SERVER` / `MK_ONLY_QUEUE` の値 | `if (process.env[...])` の truthy 判定。**`=false` と書いても有効になる** (無効化するには変数ごと消すしかない) | `1/true/yes/on` を真、`0/false/no/off/空` を偽として解釈する。未知の値は起動時エラー。`=1` を使う既存構成は影響を受けず、`=false` と書いた運用者だけが意図どおりに動く (#2459) |
| 同上を両方指定したとき | `onlyServer` を優先して黙って続行 | **起動エラー**。矛盾した設定は運用ミスで、起動してから「配送が動かない」と気付く方が高くつく (#2459) |
| `MK_ONLY_QUEUE` ノードの listener | 一切 listen しない | `/healthz` (と `enableMetrics` 時の `/metrics`) だけを持つ最小 mux を listen する。upstream 相当だと `-healthcheck` が必ず失敗し、**コンテナのヘルスチェックを外さないと運用できないノード**になるため。API 面は生えない (`s.echo` を流用せず別 mux を立てる、#2459) |

### リモート由来の文字列を列に入れるときの規則

**値の性質で分ける。一律に truncate しない** (#2723)。以下は個別の判断ではなく、
新しく列を足すときにも同じ結論になるための規則。

| 種類 | 扱い | 理由 |
|---|---|---|
| **本文系** (`note.cw` / `user_profile.description` / `user.name` / `user_profile.location` / `instance.description` 等) | rune 単位で **truncate**。**列に長さ制約が無ければ切らない** (`note.text` は text 型なので NUL の除去だけ) | 切っても意味が残る。列はコードポイントで数えるので byte で切らない |
| **URL / ID 系** (`user.inbox` / `sharedInbox` / `featured` / `movedToUri` / `avatarUrl` / `drive_file.thumbnailUrl` 等) | 収まらなければ**値ごと捨てて親の行は作る**。**切ることはしない** (`user.alsoKnownAs` のように列に長さ制約が無ければ長い値も残し、NUL を含む要素だけ落とす) | 切った URL は別物で、取りに行っても無駄なうえ壊れた参照が残る |
| **身元そのもの** (`user.uri` / `user.host` / `preferredUsername` / `note.uri` / `drive_file.url`) | 収まらなければ **document ごと拒否** (添付は 1 件ずつなのでその添付だけ) | 切ると別のものを指し、捨てると lookup / dedup の鍵が無くなる |
| **NUL** | 種類を問わず**除去**。ただし URL / ID 系は上の規則どおり値ごと捨てる | PostgreSQL は varchar / text に NUL を入れると 22021 で落ちる |

**「同じ書き込みに載っている他の列を巻き添えにしない」が目的**なので、判断の単位は
列ではなく **INSERT / UPDATE 1 回**になる。1 列でも溢れれば、その書き込みに乗っている
全部が失われる。失われる範囲も書き込みの単位で決まる: note ならその inbox job が
retry を使い切って dead になり (既定 8 回。`defaultInboxJobMaxAttempts`、
`internal/server/queue_factory.go`)、actor なら 1 行も作られない、添付は 1 件ずつ
Create するので**その添付だけ**消える。

**「dead になる」= その配送が失われるだけ**で、同じ note が返信の解決や Announce
経由の `ResolveNote` で後から取り込まれることはある (原因が直っていれば)。

**まだ規則を適用していない列がある。** chat 系 / `poll.choices` / 鍵の `keyId` /
AP tag 由来の emoji の URL 列などが無検査のまま。**一覧は #2726** — #2723 の
レビューで横断調査して見つかった分で、全量ではない (ここに再掲すると片側だけ
古くなるので、列挙は issue に一本化する)。

列長の出どころは `migration/` の SQL (多くは `000001_initial.up.sql`、後から足した
テーブルは個別のファイル)。コード側の定数と独立に同じ数値を書くことになるので、
**実 DB の列長を `information_schema` から読んで突き合わせる回帰テスト**を必ず置く
(`TestNote_CWColumnLimitIs512` / `TestNote_URIColumnLimitIs512` /
`TestUser_IdentityColumnLimits` / `TestDriveFile_ColumnLimits` /
`TestInstance_NodeinfoColumnLimits`)。mock repository は列制約を持たないため、
呼び出し側のテストだけでは「本当に入る長さか」を確かめられない
(`internal/testutil` の `assertUserColumns` は `"user"` の主な列だけ本番に揃えてある)。

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
- **本ドキュメントの件数**: `TestDivergenceDoc_*` 6 件が CI で強制する (§1-1 の内部整合と生成物との突き合わせ、§2-1 / §2-2 の実 schema との突き合わせ、§4-1 の streaming チャンネル、§4-2 の fork tag)。別途 `TestAPICompatDoc_MatchesRouter` が §1-1 の突き合わせ先 (`docs/api-compat.md`) を router.go と照合し、**錨が腐らないこと**を担保する。
  - §1-1 は (a) 見出し・表・サマリの内部整合と、(b) **`docs/api-compat.md` (= `make apicompat` の生成物) との突き合わせ**。(a) だけでは 3 箇所が揃って同じだけ間違っている状態を通す (develop では §1-1 が 53、生成物が 49、真値が 58 だった。5 件のうち 4 件は生成物の側には載っていたので、突き合わせていれば気付けた、#2640)
  - §2-1 / §2-2 は**実 schema (migration + `golden_upstream_columns.json`) との突き合わせ**。件数だけでなく行の有無も見るので、テーブル・カラムを足して表を更新し忘れると落ちる (#2634)
  - §4-2 の fork frontend tag は冒頭サマリの件数・範囲・連番と突き合わせる。**submodule 側が進んだことは検出できない** (`test-shards` job は submodule を checkout しないため)。サマリと表を両方据え置くとすり抜ける
- **値レベルの差分**: `make diff-test` (mk-go ↔ TS の応答を値単位で diff)
- **本家 e2e に対する適合**: `make upstream-e2e` (Misskey 本家の `test/e2e/**` を無改変で mk-go に向けて実行)。**意図的な差分は `tests/upstream-e2e/known-divergences.json` に根拠付きで登録し、expected-failure として扱う。** skip ではないので、乖離が解消して通るようになったら逆に落ちて気付ける。本ドキュメントに載せた divergence のうち API 挙動に現れるものは、原則この一覧にも entry がある ([upstream-backend-e2e.md](upstream-backend-e2e.md))
- **コード内の divergence 注記**: `grep -rn "#2106 L" internal/` で全件を辿れる
- **upstream 追従時**: `docs/update/` に release ごとの diff doc を追加し、そこで確定した divergence を本ドキュメントへ反映する。golden の再生成 (`make shapecheck-gen`) と TypeORM seed の追加も必要 ([upstream-catch-up.md](upstream-catch-up.md))
- **fork frontend の変更**: `third_party/misskey` に custom commit を積んで tag を打ち、mk 側の submodule pin を bump する。純正へ還元できない (= 純正 backend が対応しない) ものだけを置く方針。tag は機能追加が `X.Y.Z-mk.N`、**バグ修正はその N に英字を足す** (`-mk.22` の修正なら `-mk.22a`、次が `-mk.22b`)

## 関連ドキュメント

- [`api-compat.md`](api-compat.md) — endpoint 突き合わせ matrix (自動生成)
- [`shape-drift.md`](shape-drift.md) — entity shape drift gate
- [`federation.md`](federation.md) — 連合実装の詳細
- [`configuration.md`](configuration.md) — 設定キー一覧
- [`migration-from-ts.md`](migration-from-ts.md) — TS からの移行手順
- [`upstream-catch-up.md`](upstream-catch-up.md) — upstream 追従の手順とチェックリスト
- [`upstream-backend-e2e.md`](upstream-backend-e2e.md) — 本家 backend e2e を mk-go に向けて回す基盤と、既知乖離の運用
- [`ci.md`](ci.md) — CI で回る項目と、落ちたときの切り分け
- [`update/`](update/) — upstream release ごとの差分 doc
