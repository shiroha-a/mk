# 純正 Misskey との差分カタログ

mk-go が持つ「純正 Misskey (misskey-dev/misskey) には無い、または挙動が異なる」ものを 1 枚に集約したリファレンス。

- 基準: **mk-go 1.3.0** ⇔ Misskey TS `2026.9.0`
- 最終更新: 2026-09-07

> ベースラインを固定したのは 1.0.0 (= Misskey TS `2026.7.0` 追従完了時点)。1.1.x は
> upstream を追従したのではなく、**mk-go 側の独自変更と互換性 fix** を積んだもので、比較対象の
> Misskey TS は 1.0.0 時点と同じ `2026.7.0` のままだった。**2026.9.0 への追従 (#2877) で
> ベースラインを `2026.9.0` へ更新した。** 個々の記述はまだ 2026.7.0 時点の観察に基づくものが
> 混じりうるので、乖離を判断するときは対象の実装を現 pin (`2026.9.0-mk.16b`) で確認すること。

## このドキュメントの位置づけ

mk-go は drop-in 互換 (同じ DB / Redis / frontend を Misskey TS と共有し、backend だけ差し替えられる) を最優先とする。したがって「差分」は無条件に悪ではなく、次の 4 種類に分かれる。

| 分類 | 意味 | 扱い |
|---|---|---|
| **cherrypick 由来** | yojo-art/cherrypick 系列が純正に加えた拡張を mk-go が取り込んだもの | 維持。vanilla misskey-js golden で厳密 gate しない |
| **mk-go 独自** | mk-go が独自に足した機能 (additive、wire 互換を壊さない) | 維持 |
| **安全側 divergence** | upstream より厳しい / 正確な挙動 | 維持し理由を明記 (mk-go 優位は regress させず理由を明記する方針) |
| **未実装 / 欠落** | upstream にあって mk-go に無い | issue 化して解消する |
| **近似** | 意図も結末も upstream と同じだが、依存ライブラリが違うため数値までは一致しない | §9 に残差を実測値つきで記録する |

1.0.0 = Misskey 2026.7.0 追従完了。**ここで drop-in 互換をベースラインとして固定し、以降 frontend の独自進化を解禁する。** 本ドキュメントはその「固定したベースラインからの距離」の一覧であり、1.0.0 時点のスナップショットとして機能する。

以降 upstream を追従するときは、本ドキュメントとの差分が新たな divergence になる。追従手順は [upstream-catch-up.md](upstream-catch-up.md) を参照。

## サマリ

| 軸 | mk-go 独自 | cherrypick 由来 | 未実装 |
|---|---|---|---|
| API endpoint | GET variant 23 + alias 4 + 分割アップロード 4 + 承認制 7 + 絵文字の申請 6 + exact assignment lookup 2 + admin 観測 5 | chat 15 | **0** |
| API レスポンスの additive field | 6 (`runtime` / `mkGoVersion` / `chunkedUpload` / `approvalRequiredForSignup` / `signupApplicationForm` / `canRequestCustomEmojis`) | reversi packed game の `crc32` 等 | — |
| DB テーブル | 12 (+ bookkeeping 2) | 0 | 0 |
| DB カラム | 17 (+ 未使用の残存列 3) | 3 | 0 |
| ActivityPub | Ed25519 / RemoteStatsFetcher ほか | reversi 連合 / chat 連合 | — |
| config キー | 20 前後 | 0 | — |
| fork frontend の独自変更 | 64 tag (`2026.7.0-mk.0` ～ `2026.9.0-mk.16b`) | — | — |

**upstream endpoint の未実装はゼロ** (coverage 100.0%、444/444)。DB schema も upstream の全テーブル・全共有カラムを superset で保持しており、逆方向の欠落は無い。

---

## 1. API endpoint

upstream の endpoint は `endpoints/` 配下 438 件 + `ApiServerService.ts` の fastify 直登録 6 件 (POST 5 / GET 1) = **444 件**。うち **444 件すべてを実装済み (coverage 100.0%)**。

### 1-1. mk-go にしかない (66)

| 分類 | 件数 | 内容 |
|---|---|---|
| GET variant 追加 | 23 | `charts/*` 12 件、`emoji` / `emojis` / `federation/instances` / `federation/stats` / `fetch-rss` / `get-online-users-count` / `hashtags/trend` / `notes/featured` / `notes/reactions` / `server-info` / `bubble-game/ranking`。**対応する POST は両側にある**。ブラウザから直接叩く利便目的 |
| cherrypick chat 拡張 | 15 | `chat/messages` / `chat/messages/create` / `read` / `update` / `reactions/create` / `reactions/delete`、`chat/rooms/joined` / `unmute` / `transfer-ownership` / `members/ban` / `members/update-membership` / `invitations/accept` / `delete` / `reject`、`chat/unread-count` |
| 分割アップロード | 4 | `drive/files/create-chunked/start` / `append` / `finish` / `abort` (#2313 / #2314)。upstream に分割アップロードが無いため対応物なし。S3 の multipart upload を包むもので、`UploadId` は `chunked_upload_session` に閉じてクライアントへ出さない。能力は `/api/meta` の `chunkedUpload` で告知し、**未対応構成では field ごと出さない**ので純正クライアントは単発アップロードに倒れる |
| 承認制の登録の申請 | 4 | `signup-application/apply` / `status` / `register` / `form-token` (#2569 / #2806)。upstream に承認制が無いため対応物なし。認証不要で、本人性は申請時に発行するクレームコードが担保する (hash で保存し、平文は申請直後に 1 度だけ返す)。**外部サーバーには一切依存しない** — 当初は MiAuth を使っていたが、相手サーバーに消せない access_token 行と通知を残すため廃止した (#2568)。承認制が有効でない構成では 503。`form-token` は captcha の実 provider が 1 つも無いときに `apply` を守る署名付きトークンを発行する (#2806) — 発動条件は既存の meta フラグから両側が導出でき、**新しい meta 列は作らない** (drop-in の復路で fail-open する列を増やさない)。`testcaptcha` は実 provider として数えない (中身は文字列一致で、数えると「マジック文字列だけが効いてトークンは要求されない」最悪の組み合わせになる)。トークンは HMAC 署名 + 最短滞在時間 + Redis の nonce で使い捨て。鍵は `instance_secret` から取るので config 項目は増えない。**これは captcha の代替ではない** — 止まるのは「フォームを取得せずに endpoint を直接叩く」bot だけで、発行 endpoint を並列で叩き 1 回だけ待って一斉に送るスクリプトには**リクエスト数が約 2 倍になる負担しか課さない**。IP を回す攻撃者には大差ない。したがって captcha 未設定時の管理画面の警告・申請の一括却下・生きている申請の総数上限は引き続き必要 |
| 承認制の登録の審査 | 3 | `admin/signup-application/list` / `approve` / `reject` (#2555)。upstream に承認制が無いため対応物なし。scope は `read:admin:invite-codes` / `write:admin:invite-codes` を再利用する (承認はメール確認の経路で `registration_ticket` の発行につながり管轄が同じ。即時作成は #2813 で発行しなくなったが、承認そのものが「誰を入れるか」を決める点は変わらない。`internal/misc/permissions` は upstream misskey-js と完全一致させる契約があり mk-go 固有 scope を足せない) |
| 絵文字の登録申請 | 3 | `emoji-application/create` / `list-mine` / `cancel` (#2934 / #2935)。upstream には申請という概念が無く、絵文字の追加は `canManageCustomEmojis` を持つ人だけの操作。**`create` は `kind` で自作画像 (`own`) とリモート絵文字の取り込み (`remote`) を受ける** (#2935) — 審査する側が見る場所を 2 つに増やさないため 1 endpoint / 1 テーブルを共有し、素材の検証だけが分岐する。リモートでは**ライセンスを任意にする** (取り込み元の host + name 自体が出典で、申請者は `admin/emoji/fetch-remote-meta` を叩けないので、必須にすると参照できない値について嘘を書かせることになる)。**`create` だけ `canRequestCustomEmojis` で gate する** — 一覧と取り下げを塞ぐと、後から policy を外された人が自分の申請を確認も取り下げもできなくなる |
| 絵文字の登録申請の審査 | 3 | `admin/emoji-application/list` / `approve` / `reject` (#2934)。承認は実質 `admin/emoji/add` と同じ操作なので、同じ `canManageCustomEmojis` と **`read:admin:emoji` / `write:admin:emoji` を再利用する** (新しい scope を足すと misskey-js の型にも載せることになり、追従のたびに衝突する。`signup-application` の審査が `read/write:admin:invite-codes` を再利用しているのと同じ判断)。`CreateFromApplication` が MIME allowlist / webpublic variant の優先 / 重複チェックを共有する — 別経路にすると承認が検証を迂回する方法になる。**リモートの承認は `admin/emoji/copy` と同じ経路**を通る (#2935) — colfit の命名規則・drive への取り込み・MIME 検証・broadcast が申請経路だけ抜けるのを防ぐ |
| role assignment exact lookup | 2 | `roles/assignment-show` / `admin/roles/assignment-show` (#2607)。member一覧を走査せず、指定したuser/roleのactive assignmentだけを確認するbuild-time plugin向けhost API。self側は本人、admin側はmoderator以上に限定し、admin側は既存`admin/roles/users`と同じ`read:admin:roles` scopeを使う。**見るのは`role_assignment`行だけなので`target=conditional`のroleでは常に`assigned:false`**になる (行を持たずcondFormulaのread時評価で決まるため)。判別用に`role.target`を返す。既存の`admin/roles/users`も`ListByRole`で同じテーブルを引くので挙動は揃っている。effective判定は#2608側の担当 (#2633) |
| admin の観測系 | 5 | `admin/server-plugins` (組み込みプラグインの一覧、`read:admin:meta`)、`admin/server-metrics` / `admin/self-check` / `admin/federation/delivery-health` / `admin/federation/inbox-health` (いずれも `read:admin:server-info`)。upstream に対応物が無い。**mk-go は連合の配送 / 受信の健全性を Redis に host 単位で記録している** (`internal/core/deliveryhealth`) ので、それを admin 画面から読むための endpoint。Redis 上のカウンタなので flush で消え、drop-in の引き継ぎ対象でもない |
| その他 / alias | 4 | `i/flashs` / `i/flashs/likes` (upstream の `flash/my` / `flash/my-likes` に対する mk-go 側の path alias。両者とも mk-go に実装済み)、`signin` (upstream が `signin-flow` に統合した旧 path の backward-compat shim)、`admin/emoji/fetch-remote-meta` (リモート絵文字のインポート時に、AP では運ばれないカテゴリ・エイリアス・センシティブを相手の REST API から取る。#2698) |

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
| `admin/queue/show-job` / `admin/queue/jobs` (search 経路を含む) | 秘密を含む payload field | mk-go は `data` / `payload` から **署名鍵と webhook の上書き secret を `[redacted]` に伏せる**。upstream の deliver job data は署名鍵を持たない設計 (配送時に `getUserKeypair` で引く) なので伏せる対象が無く、mk-go も同じ設計へ寄せたが、**この変更より前に積まれた job には鍵が残る**ため到達点でも伏せる。webhook の `overrideSecret` も同様に伏せる — `admin/queue/jobs` は queue 名に allowlist を持たないので**他人のものも同じ endpoint から読める**。なお upstream は `secret` を**テスト送信に限らず全 webhook 配送の job data に入れる**ので、mk-go は元から露出が狭い。伏せるのは秘密を運ぶ field だけで、inbox / URL / 署名者など運用で job を追うのに要るものは残す |
| `admin/emoji/update` | `name` の検証 | **upstream より厳しい。** upstream の paramDef は `anyOf[{id 必須}, {name 必須 + pattern}]` なので、**`id` を渡すと `^[a-zA-Z0-9_]+$` は検査されない** (frontend の通常の呼び方がそれ)。結果として `:foo bar:` のような名前が保存でき、AP で broadcast される。mk-go は**名前を変えるときだけ** `add` と同じ検証を掛ける。**名前を変えない更新は素通しする** — frontend は名前を編集していなくても `name` を必ず送るので、無条件に掛けると既に非準拠な名前で保存されている行 (`admin/emoji/copy` や AP 経由の取り込みが作る) のカテゴリすら編集できなくなる |
| `admin/queue/show-job` / `admin/queue/jobs` | `data` | mk-go は payload を `{"type": …, "body": <base64>}` で包んで保存するので、upstream のように Bull の `job.data` をそのまま返すと Data タブが base64 の塊になって読めない。**包みの形は保ったまま `body` だけ decode して返す** (#2689)。upstream の job data はそのまま読める形なので、この包みは mk-go 固有 |
| `admin/queue/show-job` / `admin/queue/jobs` | `failedReason` / `returnValue` | **golden (upstream の宣言 schema) は required だが、upstream の実装自身が満たしていない。** Bull の job は失敗するまで `failedReason` を持たないので upstream の `packJobData` は `undefined` を返し、JSON からは消える。frontend は `v-if="job.failedReason != null"` / `job.returnValue != null` で出し分けるため、schema に寄せて空文字や `{}` を常に出すと**成功した job にも赤い警告アイコン付きの空の Failed reason 行と空の Return value タブ**が出る。upstream の**実装**に合わせて値が無ければ出さない (#2689)。golden は生成物なので直さず、テストは `shapetest.AssertExcept` で理由付きに例外化する |
| `/api/meta` (+ SSR 埋め込み meta) | `mkGoVersion` | mk-go の実装バージョン。`version` は drop-in 互換のため**互換 Misskey バージョン**を返す契約 (第三者クライアントの feature detection / frontend `_error_.vue` の版ずれ検出が依存) なので別 field にした (#2274) |
| `/api/meta` (+ SSR 埋め込み meta) | `mkGoCommit` / `mkGoFrontendVersion` | ビルドした revision (短縮ハッシュ) と、同梱した fork frontend の版 (`git describe --tags`、例 `2026.9.0-mk.3`)。`/about-mkgo` が「mk-go 1.3.0 (abc1234)」「Misskey 2026.9.0-mk.3」として出す (#2700)。**埋めるのはビルド側**で、`go run` や build-arg を渡さない `docker compose build` では空文字になる (キーは出す — 消すと「古い mk-go か埋め忘れか」を区別できない)。`.dockerignore` が `.git` を落とすので Dockerfile 内では git を呼べず、`make uds-build` / `make build` が値を渡す。**frontend を bind mount で差し替えた構成では `mkGoFrontendVersion` が実物とずれる** — 名乗っているのは「このバイナリをビルドしたときの submodule pin」で、`make uds-rebuild` のように両方を同時にビルドする経路でしか一致は保証されない |
| `/api/meta` (+ SSR 埋め込み meta) | `approvalRequiredForSignup` | 承認制の登録 (#2554 / #2555) の有効/無効。登録ページが分岐に使うので公開する (`emailRequiredForSignup` と同じ扱い)。**`features` 側にも出す** — frontend は `features` を feature detection に使うため、片方だけだと検出できない。`admin/meta` にも出す (管理画面のトグルが読む) |
| `/api/meta` (+ SSR 埋め込み meta) | `signupApplicationForm` | 承認制の申請フォームの定義 (#2570)。申請ページが描画に使うので公開する。項目は `{ label, type, required, maxLength }` の配列で、未設定なら空配列。**回答のラベルはここから埋める** — クライアントに送らせると申請者が審査画面に偽のラベルを流し込める |
| `/api/roles/*` / `admin/roles/*` / `/api/meta` の `policies` | `optOutNotificationTypes` | ロール単位で受け取らない通知の種類 (#2898)。**型ごとに `canReceiveXxx` を増やさない** — mk-go 固有の通知を足すたびに policy が増えるため、1 キーの配列にまとめてある (現在 43 キー)。**集約は intersection** で、他の `[]string` policy (`uploadableFileTypes` = set union) と向きが逆。「受け取らない」一覧を union すると複数のロールに属するほど通知が減る = 厳しい方に倒れ、upstream の policy 集約 (bool は OR、数値は max) が緩い方へ倒すのと食い違う。**ロール編集画面には届く相手を明記する** — この folder は全てのロールに出るので、権限を持たないロールでも「切れる」ように見え、通報の通知を一般利用者も受け取ると誤解される (本番確認で指摘された)。実際は通知が作られる時点でモデレーター権限を持つ利用者と初期ユーザーに絞られ、read 時にも再確認される。**intersection を取るのは明示的に値を設定したロールだけ** — `computePolicy` は override を持たないロールに base (= 空の一覧) を積むので、素直に intersection を取ると 1 つでも未設定のロールがあれば設定が消える。`GetUserRoles` は conditional role も含むため、管理者が割り当てていない自動マッチのロール 1 つで効かなくなる (#2898 の初版で実際にそうなっていた)。明示的に空を設定したロールは「このロールでは何も切らない」の宣言なので intersection に参加する。型不一致の候補は集約から除外し、有効な候補が 1 件も無ければ既定へ戻す。TS は未知の policy キーを無視するので drop-in の復路は壊れない (戻すと opt-out が効かなくなり、通知は届くようになる) |
| `/api/roles/*` / `admin/roles/*` / `/api/meta` / `users/show` の `policies` | `canRequestCustomEmojis` | カスタム絵文字の登録を申請できるか (#2934)。**upstream には申請という概念自体が無い**ので TS 側に対応キーが無い。既定は true — 登録は必ずモデレーターの承認を通るので、申請そのものを既定で塞ぐ必要が無い (`canCreateChannel` と同じ考え方)。`canManageCustomEmojis` を持つ人は申請ではなく直接登録できる。`tests/diff/test_endpoints.py` の ignore-list にも登録済み |
| `notifications` / `i/notifications` の `type` | `abuseReport` | 通報が作られたときにモデレーター / 管理者へ送る通知 (#2868)。**upstream は通報を email / system webhook / admin stream でしか流さず、通知欄に残す形を持たない。** `notifier` は通報者で、`reportId` / `targetUserId` を持つ (`reportId` は `admin/abuse-user-reports?reportId=` で該当通報を開くために要る)。**通報コメントは持たない** — 定型フォームの全文 (違反カテゴリ / 対象 / 該当 URL / 詳細) が入るので通知欄に出しても読めず、出さない以上 Redis に本文の複製を残す理由が無い。通知は「誰から通報があったか」だけを伝え、中身はボタンから管理画面で見る。ローカルの `users/report-abuse` と連合経由の `Flag` の**両方**で作る (片方だけだと通報の出どころで通知の有無が変わる)。**read 時に通報を引き直して現在の状態 (`resolved` / `resolvedAs` / `assigneeId`) を載せる** — 通知は作成時点しか持たないので、これが無いと他のモデレーターが対処済みの通報に二重で当たる。通報が削除済み / lookup 未配線なら通知ごと drop する (`roleAssigned` が role 削除で drop するのと同じ形)。**配線先は REST と WebSocket publisher の 2 つ**で、片方だけだと「一覧には出るが realtime では出ない」非対称になる。**read 時にモデレーション権限を再確認する** — 対象ユーザー ID が Extra に入り、通報の存在自体が機微なので、権限を失った元モデレーターが `admin/abuse-user-reports` の 403 を迂回して通知欄から見続けられないようにする (checker 未配線なら返さない fail-closed)。**#2868 以前に積まれた通知が持つ `comment` は read 時に落とす** — Redis stream は `MaxPerUser` で回転するまで残るので、書き込み側を直しただけでは古い通知から本文が出続ける。**notifier のミュート / 凍結では落とさない** — notifier は通報者なので、モデレーターがミュートしている相手からの通報が通知欄に一切現れなくなる。**全指定判定 (`excludeTypes` が全種を覆うか) には含めない** — 含めると upstream の 20 種しか送らないクライアントの「すべて無効」が判定を外れ、1 件も返していないのに既読位置が飛ぶ (#2833 / #2835 が塞いだ害の再オープン。#2898 の初版で実際に踏んだ)。TS へ swap back すると、この型の通知は**ヘッダも本文も空**で描画される (upstream の `MkNotification` に分岐が無い。`pollVote` が実際にそうなっていた既知の型) |
| `notifications` / `i/notifications` の `type` | `emojiApplicationProcessed` | 絵文字の登録申請の結果を申請者へ返す (#2934)。**結果と却下理由は通知に積まず、読み出し時に申請から引き直す** — 通知へ複製すると、申請を消しても文面が Redis に残る (`abuseReport` #2868 と同じ形)。申請が消えていたら通知ごと落とす |
| `/api/meta` | `chunkedUpload` | 分割アップロード (#2313) の能力告知。`{ chunkSize }` を返す。**未対応構成 (オブジェクトストレージ未使用 / `meta.chunkedUploadEnabled=false`) では field ごと出さない**ので、純正 Misskey と同じく `undefined` になりクライアントは単発アップロードにフォールバックする |

### 1-1c. リクエストパラメータの additive

upstream にもある endpoint に、mk-go が任意パラメータを足しているもの。**足すだけ**なので
upstream 由来のクライアントはそのまま通る (省略時は upstream と同じ挙動)。

| endpoint | パラメータ | 内容 |
|---|---|---|
| `admin/abuse-user-reports` | `reportId` | 通報の通知から該当の 1 件を開くための additive パラメータ (#2868)。**指定時は `state` / `reporterOrigin` / `targetUserOrigin` / cursor の絞りを無視する** — 一覧の既定は `state=unresolved` なので、他のモデレーターが先に解決した通報に通知から飛べなくなる。1 件を名指しで引く以上、絞り込みは意味を持たない。存在しない ID は 404 ではなく空配列 (一覧 endpoint なので) |
| `admin/emoji/copy` | `category` / `aliases` / `license` / `isSensitive` | リモート絵文字のインポート時に、確認・編集した値でコピーする (#2698)。**新しい作成 endpoint を作らず `copy` を拡張したのは、`emoji.originalUrl` が必ず `drive_file.url` と一致するという不変条件を再実装しないため** — `DriveFileRepository.DeleteOrphans` の cleanup guard が `NOT EXISTS (emoji.originalUrl = drive_file.url)` で system 所有の絵文字画像を保護しており (#722)、そこを踏み外すと画像が孤児判定で消える。重複チェックの「DB 障害を not-found に丸めない」形 (#2792) も同じ理由で再利用する。**ポインタで受けて「指定なし」と「空を指定」を区別する** — 前者は src の値を保ち、後者は空にする。**値は列に収まる形に整えてから入れる** — 出どころが相手サーバーの `/api/emoji` なので、そのままだと `category` varchar(128) / `license` varchar(1024) / `aliases` varchar(128)[] を超えて SQLSTATE 22001 になる (実測)。AP 経路が同じ 3 列に持っている規則 (#2726、§7 の「リモート由来の文字列を列に入れるときの規則」) と揃え、本文は切り、alias は超えた要素だけ落とす (切ると別の名前になりリアクションの照合に使えない)。**misskey-js の autogen 型には出ない**ので、fork frontend からは `as never` キャストで呼ぶ |
| `/avatar/@acct` | `static` | 静止画設定 (`disableShowingAnimatedImages` / `dataSaver.avatar`) のとき frontend が付ける (#2908)。**upstream には無い** — あちらは media proxy が open proxy なので `getStaticImageUrl` が組む `<mediaProxy>/static.webp?url=<instance>/avatar/@u@h&static=1` がそのまま通る。mk-go の proxy は allowlist が DB に実在する URL だけを通すため、その URL は **403 + `max-age=86400`** になり静止画になるどころか 1 日壊れていた。`/avatar/` 側で受けて署名付きプロキシ URL へ 302 する (`/emoji/:path` の `static` と同じ形、#2905)。**除外は identicon fallback だけ** — `/identicon/<id>` は相対 URL なので proxy の `Fetch` が `fetchRemote` に落ちて `unsupported protocol scheme` で失敗し、404 + `max-age=86400` になる (PNG を生成して返すのでアニメーションもしない)。**同一オリジンは除外しない** — ローカルの drive アバターも GIF / APNG / animated WebP になりうる。allowlist は判断材料にならない (署名付き URL を組むので `Authorize` は allowlist より先に HMAC で通る) |

### 1-2. 未実装 (0)

**upstream endpoint の未実装はゼロ。** 最後まで残っていた `GET /api/v1/instance/peers` (Mastodon 互換の連合ピア一覧) は #2245 で実装した。upstream は `ApiServerService.ts` で fastify 直登録しており `endpoints/` 配下に無いため、matrix 生成ツールの file-walk から漏れて長らく不可視になっていた (現在は `ApiServerService.ts` を正規表現で直接読むので追随漏れが起きない)。

かつて `docs/api-compat.md` に残っていた「TS only (mk-go 未実装) 1」= `/api/reset-db` の偽陽性 (mk-go では `config.TestMode` 時のみ登録されるのに、matrix 生成が default config で route dump するため未実装に見えていた) は解消済み。現行 matrix は `TS only (mk-go 未実装): 0` で、`/api/reset-db` は「両方に存在する endpoint」に入っている。

---

## 2. DB schema

**逆方向の欠落はゼロ** — upstream の `@Entity` 76 テーブルと全共有カラムを mk-go が superset で保持している。

### 2-1. mk-go 独自テーブル (14)

| テーブル | 由来 | 理由 |
|---|---|---|
| `user_keypair_extra` | mk-go 独自 | local user の Ed25519 鍵ペア。既存 `user_keypair` (RSA) を touch せず別テーブルに分離し、**TS へ swap back しても壊れない**設計 |
| `user_publickey_extra` | mk-go 独自 | remote user の追加公開鍵。actor JSON の `assertionMethod[]` (FEP-521a Multikey) を keyId 単位で保持 |
| `antenna_note_unread` | mk-go 独自 | per-user per-note の antenna 未読 |
| `channel_note_unread` | mk-go 独自 | channel follower の未読追跡 |
| `chunked_upload_session` | mk-go 独自 | 分割アップロード (#2313) の進行中セッション。S3 の `UploadId` はここでだけ保持しクライアントには露出しない。`user` への FK は張らない — CASCADE で行だけ消えると `AbortMultipartUpload` されない未完了マルチパートアップロードが孤児として課金され続けるため、期限切れ GC に回収させる |
| `emoji_application` | mk-go 独自 | カスタム絵文字の登録申請 (#2934)。**承認までは `emoji` 行を作らない** — 承認待ちを `emoji` に隠し列で持たせると、TS へ切り替えた瞬間に未承認の絵文字が全部有効になる (TS は mk-go 独自の列を知らない)。`signup_application` (#2555) が `user` に対して採ったのと同じ形。`kind` 列は自作画像 (`own`) とリモート絵文字の取り込み (`remote`、#2935) で 1 テーブルを共有するために持つ — 審査する側が見る場所を 2 つに増やさないため。`remote` では `fileId` ではなく `remoteHost` / `remoteName` が素材を指す。`user` / `drive_file` / `emoji` への FK は張らない (`signup_application` と同じ方針)。**純正へは還元できない行** (申請という概念自体が upstream に無い)。 |
| `signup_application` | mk-go 独自 | 承認制の登録 (#2554 / #2555) の申請。**承認待ちを `user` 行として持たないための箱**で、`user` に承認列を足す設計だと TS へ切り替えた瞬間に承認待ち全員が有効なアカウントになる (TS はその列を知らないので素通りする)。申請の回答は `answers` 列に**提出時のラベルを同梱して**持つ (#2570) — 定義を後から変えても既存の申請がどの設問への答えだったか分かる。本人性は**クレームコードの SHA-256** が担保する (#2569) — 平文で持つと DB が漏れた時点で全申請が乗っ取れる。重複申請を DB では抑止しないので、captcha とレート制限が防波堤になる。TS は未知のテーブルを無視するだけ。**ticket を発行するのはメール確認の経路だけ** (#2813) — 即時作成では発行しても誰も参照しない (一回性は `settleApplicationTx` の行ロックが担保する、#2580) ので、`registration_ticket` の行が 1 つ増えるだけだった。即時作成では `ticketId` を**新しく記録しない**ぶん、監査は `processedById` / `usedById` で辿る (メール必須を切る前に始まっていた確認待ちの残骸だけは、ticket を破棄したあとも値として残る = dangling)。メール確認の経路で ticket が担うのは**前回試行の失効**で、置き換えた古い ticket を消すと確認リンクが失効し最新の試行だけが通る。再試行の直列化は ticket ではなく申請行のロック、`/api/signup` の 30 分の再送防止窓はこの経路を通らない。**内部発行する `registration_ticket` は `createdById` を入れない** (#2805) — 審査した管理者を入れると承認のたびにその管理者名義の招待が 1 枚増え、`invite/create` / `invite/limit` の上限 (`CountByCreatorSince`) を食い、利用者の `invite/list` (`ListByCreator`) にも出る (どちらも `WHERE "createdById" = ?` なので NULL は外れる)。監査は失われない — 審査した管理者は `processedById`、登録者は `usedById` で、どちらも**この表に FK が 1 つも無い**ので user を消しても残る。ticket 行のほうは `createdById` / `usedById` の FK がどちらも `ON DELETE CASCADE` なので、審査した管理者か登録者のどちらかを消せば消える (`ticketId` は dangling になる)。`createdById` を入れないとその引き金が 1 つ減る。**upstream が作らない状態を作る** — `createdById` が NULL の行は管理画面の招待一覧に `createdBy: null` (= `system` 表示) で出る (#2813 以降、この行が新しく増えるのはメール確認の経路だけ)。**`invite/delete` のモデレーターは消せる** (#2812 で upstream の bypass を入れた。それ以前は API から消す手段が無く、TS へ切り替えると消せるようになる非対称があった)。ただし endpoint 自体が `canInvite` の後段にあり、この policy を無条件に通るのは administrator / root だけ (既定は false) なので、**`canInvite` を持たない素のモデレーターは handler に届かず 403 になる** — これは upstream も同じ (`ApiCallService` の bypass も root と administrator のみ)。あわせて非モデレーターが使用済みの招待を消せる緩さも塞ぎ、`invite/list` の `used` を `usedAt` 由来に揃えた (upstream / `admin/invite/list` と同じ。`usedById` 由来だと確認メール待ちの ticket が未使用に見え、削除ボタンを押すと 400 になる)。**#2805 より前に作られた行はそのまま残す** — 上限は `inviteLimitCycle` (既定 7 日) の窓から抜けて自然に解消するが、`ListByCreator` は時間窓を持たないので個人の招待一覧には残り続ける。監査行の書き換えになるため移行は書かない |
| `user_suspension_origin` | mk-go 独自 | 凍結の由来 (`local` / `remote`) を記録する (#2973)。リモート actor の `toot:suspended` を読む (#2951) にあたって、**モデレーターの判断がリモートに巻き戻されない**ようにするために要る。Mastodon は `accounts.suspension_origin` 列で同じことをしている。**`user` に列を足さず別テーブルにしてある** — TS は未知の列も無視するので列追加でも復路は壊れないが、別テーブルなら TS 側から一切見えず、`user` という連合・認証・API のあらゆる経路が触るホットテーブルにも手を入れずに済む (`relay_observed_user` と同じ判断)。TS へ切り替えると由来は失われるが、`user.isSuspended` は残るので**凍結は維持される**。既存の凍結済みユーザーは migration で `local` として登録する (安全側) |
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
| `meta` | `approvalRequiredForSignup` | mk-go 独自 | 承認制の登録 (#2554) の有効化。**これ自体がゲート**で、有効時は `/api/signup` を 403 で閉じる (#2557)。**メール必須と併用できる** (#2571) — 承認済みからの登録も `emailRequiredForSignup` が有効なら `user_pending` に積んで確認メールを送るので、設定と実態が食い違わない (#2565 の排他は撤去済み)。クレームコードは常に必須で、本人性の担保はコードが持つ。有効化する更新では**同じ更新でアカウント作成も開放する** — 「先に開放してから承認制を入れる」順を強制すると、その間に素通しで登録される窓ができるため。開放は安全性の条件ではなく (承認制それ自体がゲート)、訪問者に「招待制」と表示しないための整合。`disableRegistration` と組み合わせる運用にすると、訪問者には「招待制」と表示されて実態と食い違う。承認フローは signup service を直接呼ぶのでこの分岐を通らない。**有効な間は、進行中だった確認メールが通らなくなる** (#2804) — `user_pending` は 24 時間有効で、申請に紐付かない行は承認の確定処理 (#2576) を通らないので、ゲートが無いと「承認を経ていないローカルアカウント」ができてしまう。`/api/signup-pending` は承認制が有効なら申請 ID を持たない pending を `NOT_APPROVED` で弾く (該当者は申請フォームからやり直す)。**行は消さない**ので、TTL 内に承認制を戻せば同じリンクがまた通る。窓が開くのは切り替え**前**に `emailRequiredForSignup` が ON だった構成だけで、OFF なら `/api/signup` が即座にアカウントを作るので待ち行列が存在しない。meta が読めないときも通さないが、そちらは `NOT_APPROVED` ではなく 500 にする — DB 障害を承認の迂回路にせず、かつドメインの答えにも化けさせない (#2799)。**無効に戻す更新では逆に閉じる** (#2803) — 開放はゲートが立っていることが前提の整合なので、ゲートが消える更新で維持すると、招待制 → 承認制 ON → 承認制 OFF の 3 操作でゲートが 1 つも無い全開状態が残る。倒す先を閉じる側にしてあるのは、元が開放だったサーバーが招待制になっても次に管理画面を開けばトグルに出て気づけるのに対し、逆 (全開のまま残る) は開いていることが正常に見えて気づけないため。`disableRegistration` を明示していれば尊重するので、管理画面は無効化のときに「アカウント作成も閉じるか」を確認して選択結果を明示的に送る (開ける側は #2565 の整合の強制なので明示より優先する、という非対称は意図的)。**この 2 列は bool 以外を 400 で弾き、JSON null だけは無指定として落とす** (#2803) — GORM は map の値をそのまま driver へ渡すため `"false"` のような文字列でも列は更新されるが、正規化は bool しか見ないので、弾かないと「承認制は外れたのに登録は全開のまま」が作れる (upstream も ajv の `type: 'boolean'` で string を 400 にする)。null を弾かないのは upstream の paramDef が `nullable: true` で実装も `typeof === 'boolean'` でしか読まず、misskey-js の生成型も `boolean \| null` のため。落とさないと NOT NULL 制約違反で 500 になる (#2803 以前の挙動)。TS はこの列を認識しないので、TS へ戻すと承認制が単に無効になる — **こちらは mk-go を経由しないので上の補正も効かない** (登録が開くので、切り替え前に `disableRegistration` を検討すること) |
| `user` | `isRoot` | mk-go 独自 | upstream は system_account 移行で DROP 済み。`role.Service.isRootUser` の fallback に必要 |
| `meta` | `proxyAccountId` | mk-go 独自 | 同じく upstream は DROP 済み。`admin/update-proxy-account` が書き込む |
| `note_favorite` | `createdAt` | mk-go 独自 | upstream は `deleteCreatedAt` で DROP 済み。`/api/i/favorites` の response 要件で復活 |
| `app` / `auth_session` | `createdAt` | 列のみ残存 | upstream は `deleteCreatedAt` で DROP 済み。mk-go も **読み書きしない** (#2243 で model から除去)。fresh な mk-go DB には列が残るが未使用 |
| `clip` | `notesCount` | 列のみ残存 | 旧・非正規化カウンタ。#2243 で撤去し、件数は upstream 同様 `clip_note` の実カウントで算出する |
| `poll` | `notifiedAt` | mk-go 独自 | pollEnded 通知の二重送信防止 |
| `user_pending` | `invitationTicketId` | mk-go 独自 | 1 招待で複数アカウントを作れる gap を塞ぐ |
| `user_pending` | `signupApplicationId` | mk-go 独自 | 承認制 (#2571) でメール確認を挟むときの申請 ID。確認完了まではアカウントが無いので申請を `completed` にできず、**紐付けが無いと申請が `approved` のまま残って 1 つの承認から複数アカウントを作れる**。`/api/signup-pending` が `PromotePending` の戻り値からこれを読んで申請を完了させる。**承認制が有効な間は、この列の有無がそのままゲートの判定になる** (#2804) — 空にすると承認を経ていない pending として弾かれる側に倒れるので、移行バッチ等で NULL 化しないこと。TS は未知の列を無視するので drop-in の復路は壊れない |
| `meta` | `signupApplicationForm` | mk-go 独自 | 承認制の申請フォームの定義 (#2570)。管理者が項目を決める jsonb 配列。上限は 10 項目 / ラベル 100 文字 / 回答 2000 文字で、**上限を置かないと管理者が自分で壊せる** (項目無制限で申請ページが使えなくなる、最大長無制限で 1 件の申請が DB を膨らませる)。壊れた JSON は空フォーム扱いにして申請ページを 500 で潰さない |
| `meta` | `enableEphemeralRelayNotes` / `ephemeralRelayNoteTtlMinutes` | mk-go 独自 | リレー経由投稿の揮発化 (#2332)。リレーでしか観測しない投稿は Redis に TTL 付きで置き、ローカルユーザーが触ったときだけ DB へ materialize する。既定 false は既存インスタンスの挙動を変えないため — 有効にするとグローバルタイムラインは FTT の窓より過去に遡れなくなる。**どちらかのフラグ (これか `enableRelayOrphanUserCleanup`) が有効なら、owner 無しのリモート添付を掃除する日次ジョブ (`maintenance:orphanAttachmentCleanup`、05:30) も回る** (#2722)。著者が materialize されていないリモート添付は owner 無しで保存され、ephemeral note が TTL で消えても `drive_file` の行は残るため。消すのは **(a) どの DB 上の note からも参照されておらず、(b) 生きている ephemeral note の印も無く、(c) 猶予より古い** link-only の行だけ。**TTL は寿命の上限ではない** (`Touch` が閲覧のたびに打ち直す) ので、猶予だけでは表示中の添付を守れない。生存判定は Redis の印が担い、猶予は「行を作ってから印を打つまでの窓」を覆うだけ。**Redis を `maxmemory` + `allkeys-lru` で運用する場合は注意**: タイムラインの hydrate は note (`ephNote:*`) だけを読み、印 (`ephFile:*`) には触れないので、印だけが先に evict される側に偏り (`/api/notes/show` の `Touch` は印にも `EXPIRE` を打つため LRU の idle time も更新される。偏るのは hydrate しか通らないノート)、その状態で古い行が再利用されていると表示中の添付が消えうる (`volatile-ttl` なら TTL 順なのでこの偏りは出ない)。**この機能を有効にした直後の 1 TTL ぶん**も、既存の ephemeral note には印が無い (`Touch` は `Expire` なので印を作らない)。cron の時刻は TZ 未指定で、mkq (既定) はプロセスの TZ、legacy の asynq は UTC で解釈する |
| `meta` | `enableRelayOrphanUserCleanup` / `relayOrphanUserGraceDays` | mk-go 独自 | リレー由来の孤児 user の掃除 (#2340)。対象の限定には `relay_observed_user` を使う |
| `meta` | `chunkedUploadEnabled` / `chunkedUploadChunkSizeMb` / `chunkedUploadSessionTtlMinutes` / `chunkedUploadMaxSessionsPerUser` / `chunkedUploadMaxPendingMbPerUser` | mk-go 独自 | 分割アップロード (#2313) の設定。**コントロールパネル (オブジェクトストレージ) に出ているのは `chunkedUploadEnabled` / `chunkedUploadChunkSizeMb` / `chunkedUploadSessionTtlMinutes` の 3 つだけ**で、ロール policy の上限になる `chunkedUploadMaxSessionsPerUser` / `chunkedUploadMaxPendingMbPerUser` は `admin/update-meta` を直接呼ぶしかない (#2900 で確認)。TS は未知の列を無視するので drop-in の復路は壊れない |

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

### 3-3a. リモート actor の `suspended` を読む (mk-go 独自)

**upstream は読まない。** `ApPersonService` / `type.ts` に `suspended` の参照は無く、
`ApRendererService` も出力しない。mk-go は**読むだけ**で、こちらからは出さない
(`RenderPerson` は設定しないので wire shape は変わらない。`TestRenderPerson_SuspendedIsNeverEmitted`
で固定)。

**理由は、凍結したときの合図がファミリーで違うこと。**

| 発信元 | 凍結したときの合図 | 読まないと起きること |
|---|---|---|
| Misskey 系 | `Delete` を送る (`UserSuspendService.postSuspend` が `renderDelete` を全 sharedInbox へ) | `isDeleted = true` になる (対処済み) |
| Mastodon 系 | actor に `toot:suspended` を立てて `Update(Actor)` を push + collections が 403 | **何も起きない** |

**結末は Misskey 系と同じにはならない。** 立つ列も残るデータも違う:

| | Misskey 系 (`Delete` 受信) | Mastodon 系 (この機能) |
|---|---|---|
| 立つ列 | `isDeleted` | `isSuspended` |
| ノート・ドライブ・following 行 | **削除される** | 残る (読み取り時に隠れるだけ) |
| 保留中のフォローリクエスト | 残る | **残る** (`cleanupSuspendedUserRelations` は admin 経路だけが呼ぶ) |
| その人宛のローカル利用者の返信・リノート | 表示されたまま | **timeline から消える** |

最後の行は方向が逆になる。`applySuspendedAuthorExclusion` が見るのは `isSuspended` だけで
`isDeleted` を見ないため、**この経路のほうが巻き添えが大きい**。つまり
「リモートの moderation 判断が、こちらの利用者が書いたノートの表示可否を決める」経路が
新しくできる。データは消えないので凍結を解けば戻るが、下記の制約がある。

**自己申告に限られる。** actor document を出したホストと `actor.id` のホストの一致は
`fetchActor` が必須にしているので、第三者が他人に `suspended` を付けることはできない。

**凍結の由来を持つので、モデレーターの判断はリモートに巻き戻らない** (#2973)。
`user_suspension_origin` に `local` / `remote` を記録する。**Mastodon を出発点にしつつ、
解除側も記録する点だけ厳しくしてある** — Mastodon の `unsuspend!` は
`suspension_origin` を `nil` に戻すので、モデレーターが解除しても発信元が立て直せば
再凍結される。mk-go は解除も `local` として刻むので、その巻き戻しが起きない
(この乖離の目的がまさにそこなので、原典より厳しい側に倒している)。判定の骨格は
`ProcessAccountService#set_suspension!` と同じ:

- モデレーターが凍結・解除した行 (`local`) には**触らない**
- こちらが actor を見て凍結した行 (`remote`) は、発信元の解除にも**追従する** (two-way)
- **記録が無い行は `local` 扱い** — この表より前から凍結されている行をリモートに解除
  させないため。誤って `local` にしてもモデレーターが手で戻せるが、逆は回復しにくい
- **由来を記録できないなら凍結もしない** (未配線 / 書き込み失敗)。記録の無い凍結は
  次の refresh で「由来不明 = local」になり、解除できるかどうかが運任せになる
- **由来の読み取りが失敗したら触らない** — 「記録が無い」と取り違えると、接続断の
  瞬間にモデレーターの判断を上書きしうる

**`user` に列を足さず別テーブルにしてある** (`relay_observed_user` / `signup_application`
と同じ判断)。TS は未知の列も無視するので列追加でも復路は壊れないが、別テーブルなら
TS 側から一切見えない。自動での変更は moderation log に載らないので `slog.Info` を出す。

**drop-in で往復すると、`remote` の行だけ古くなる。** TS は由来を知らないので、TS 稼働中に
モデレーターが「発信元がまだ凍結を主張しているリモート利用者」を解除し、その後 mk-go へ
戻すと、行は `remote` のままなので**次の refresh で再凍結される**。`local` が付いている行は
守られるので、影響は「こちらが actor を見て凍結した行」に限られる。

**読めない形は `false` に倒す** (`APLenientBool`)。`APTruthyBool` だと `[]` / `{"a":1}` /
`"maybe"` のような壊れた値が `true` になり、**誤って凍結する** (差が出る入力を実測で確認)。
真と読むのは PostgreSQL の boolean 入力構文 (`"true"` / `"yes"` / `1` など) なので、
実際に `suspended` を出す Mastodon が送る素の JSON `true` より広い。片方向で巻き戻せない
操作としては広い側だが、コードベースの他の AP boolean (`isCat` / `discoverable`) と
判定を揃えてある。

**キー名だけで読み、`@context` は見ない。** 他の AP boolean も同じで、upstream も同じ。
意味の衝突が無いことを Mastodon (凍結時のみ出力) / Akkoma (出力なし) / Misskey (参照なし)
で確認した。Pleroma / GoToSocial は未確認。

### 3-4. リモート note と antenna

**upstream は載せる。** `ApInboxService` の Announce 経路は `fromRelay` 分岐で
renote を作らずに publish するが、その手前で `apNoteService.resolveNote(target)`
を呼ぶため `ApNoteService.createNote` -> `noteCreateService.create` ->
`addNoteToAntennas` (無条件) を通る。Create 転送型のリレーも同じく
`NoteCreateService` を通る。ただし `fetchNote(uri)` で既知なら早期 return する
ので、載るのは初回観測時だけ。

**mk-go はリレー経由だけ外す** (#2743)。直接配送の inbound Create / Announce は
載せる。判定は 2 系統あり、どちらか一方でもリレーなら発火させない。

| 形態 | 判定 |
|---|---|
| Misskey 系リレー (リレー actor 自身が Announce) | `handleAnnounce` の `viaRelay` (announcer が relay actor か) |
| Mastodon 系リレー (元の Create / Announce を転送、署名だけリレー) | `isRelayDelivery(signer)` |

外す理由は 2 つ。

1. **`enableEphemeralRelayNotes` が有効なとき、リレー投稿は DB に行を持たない**
   (Redis に TTL 付きで置く、#2332)。antenna service は ephemeral note を
   **あえて materialize しない**方針なので DB が膨らむことはないが、
   `pushNote` は先に走るため **DB から引けない ID が antenna の ZSET (上限
   200) を埋める**。読み取り側は DB しか見ないので、幽霊 ID のぶんだけ本来
   載る note が押し出される (#2719 と同じ構造)。**この設定は既定 `false`**
2. 量が最も多いのがこの経路。`ListAllActive` は #2752 でキャッシュしたので DB
   クエリは消えたが、`matchNote` はアクティブ antenna 全件に対して評価され、
   ヒットすれば Redis への push が走る。リレーの firehose 全量に載せると
   コストが読めない

したがって既定構成では 2 番目だけが理由になる。**リレー投稿を antenna で拾いたい
運用がある場合は、この判断を見直す余地がある。**

inbound Announce では **renote 行に加えてブースト対象 (target) も** antenna に
渡す (#2751)。renote は text を持たないので、target を渡さないとキーワード指定
または `withFile` の antenna は 1 件もマッチしない。upstream も
`ApInboxService.ts:329` が `resolveNote(target)` 経由で target を
`NoteCreateService` に通すので同じ。

target 側には**新規に取り込んだときだけ**という gate がある (upstream も既知の
note では `createNote` を通らない)。無条件だと再ブーストのたびに古い note が
antenna に湧く。あわせてローカル note は対象外にする (作成時に既に通っている)。

**この gate は「他の経路で先に DB へ入った note」を全部落とす。** upstream では
それらの経路も `resolveNote` → `createNote` → `NoteCreateService.create` を通り、
`addNoteToAntennas` は `silent` の外にあるので**その時点で antenna に載っている**。
mk-go で載らないまま残るのは、次の経路で先に取り込まれた note がブーストされた
場合:

- リレー配送 (上記の方針で antenna に載せていない)
- featured collection の取り込み
- 他 note の reply / quote target としての解決
- `Add` (pin) の対象解決 — upstream も `resolveNote` なので同型
- `/api/ap/show` などの手動解決
- **`Like` / `Undo(Like)` / `Undo(Announce)` の対象解決** — ここは mk-go 固有。
  upstream は Like で未知 note を DB に作らない (`fetchNote`) ので、後続の
  Announce が `resolveNote` → `createNote` → antenna を通る。mk-go は Like の
  時点で行ができるため、後続の Announce では `created=false` になり載らない。
  Like は Announce より高頻度なので、実効の取りこぼしは他の入口より大きい

数え方は「`ResolveNote` が既存行を返しうる入口」= `grep 'ResolveNote(' internal/`
の非テスト call site。リレー以外は upstream でも antenna に載る経路なので、
**差は「その入口で載せていない」ことと掛け算になる**。

なお inbound Create / Announce **以外**の取り込み経路 (featured collection、
reply / quote target の解決、`/api/ap/show`) でも antenna に載せない。これは
fanout hook と同じ扱いで、過去の note が突然 antenna に湧くのを避けるため。

Mastodon 系リレーが転送した Announce は、**従来どおり renote を作って timeline
には流れる**。antenna に載せないだけで、renote 抑止の判定は変えていない。

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
| リモート AP document の単一値 / 配列表現の許容 | `activitypub/types.go` | **upstream 同等 (一部は緩い方向)** (#2662)。対象は `type` (配列 → 先頭。`tag` / `attachment` の**要素**の type も含む)、`attachment` / `tag`、collection の `items` / `orderedItems` (単一 object → 1 件)、`to` / `cc` / `alsoKnownAs` (単一値・`{id}` 要素)、`inReplyTo` / `attributedTo` / `outbox` / `followers` / `following` / `sharedInbox` / `endpoints.sharedInbox` / `featured` / `movedTo` (`{id}` object・配列の先頭)、`url` (`{href}` object・配列の先頭)、`endpoints` / `source` / `icon` / `image` が object でない場合 (空として扱う。`icon` / `image` は 1 つの field が読めなくても読めた分は救う)、`assertionMethod` (単一 object・bare IRI 参照・要素ごとに decode)、`summary` / `name` / `publicKey.publicKeyPem` / `_misskey_*` 拡張 (非 string / 非 bool でも document は通す。JSON-LD の展開形は剥がして値を拾う。`publicKeyPem` は**空になった値で既存の鍵を上書きしない**ようにしてある — 上書きするとその actor からの署名検証が恒久的に失敗する)、Question の選択肢 `type` / `replies` (IRI 参照でもよい) / `replies.totalItems` (`3.0` / `"3"` も整数として読む)、`_misskey_quote` (`{id}` object)、`isCat` / `discoverable` / `manuallyApprovesFollowers` / `sensitive` / `_misskey_*` の bool が bool でない場合 (**PostgreSQL の boolean 入力構文で読む**。upstream が生値を代入する field は TypeORM の丸めが効かず PostgreSQL がキャストするので、`"true"` は true、`"false"` / `"0"` / `"no"` / `"off"` は false。**JS の truthy にはしない** — 「空文字以外は true」にするとこれらが軒並み反転する。数値も同じ扱いで、`node-postgres` は `String(val)` で送るので有効なのは `1` / `0` だけ (`2` は `'2'::boolean` = invalid input syntax なので「読めない」側)。**生値を代入するのは `manuallyApprovesFollowers` (→ `isLocked`) / `discoverable` (→ `isExplorable`) / Note の `sensitive` で、`isCat` は違う** — upstream は `isCat: (person as any).isCat === true` なので `"true"` でも false になる (`requireSigninToViewContents` も同じ形)。mk-go はここを他の bool と揃えて読むので**その分だけ緩い**。JSON-LD の展開形は剥がしてから判定する。**読むのは完全形と PostgreSQL が挙げる 1 文字表記 (`t` / `f` / `y` / `n`) まで** (`'tr'` / `'fals'` のような 2 文字以上の一意な接頭辞も PostgreSQL は受け付けるが、そこまでは追わない。曖昧で PostgreSQL 自身が拒否する `'o'` も読まない)。読めない形は field ごとの既定値に倒し、既定は「読めないと危険な側」で決める: `sensitive` / `manuallyApprovesFollowers` は true (隠す / 承認制)、`_misskey_canChat` は false (DM 拒否)、その他は false)、`sensitive` が bool でない場合 (**読めなければ true に倒す**。これは upstream 追従ではない — upstream は `sensitive` を `attach.sensitive ??= note.sensitive` の1 箇所でしか使わず CW は `summary` からしか作らないが、mk-go は `sensitive` が立った note に空 CW を付ける独自実装なので、false に倒すと**送信側が sensitive と宣言したノートが CW 無しで表示される**。JSON-LD の展開形 `[{"@value": false}]` は剥がしてから判定する)、`quoteUrl` (`{id}` object)。AP はこれらを「単一値でも配列でもよい」と定めており、JSON-LD compaction は `@container: @set` の無い term の単一要素配列を素の値に潰す。`@type` は逆に配列表現が正規で、compaction 後も配列で残る実装がある。upstream は `toArray` / `getApId` / `getOneApHrefNullable` で吸収するが、mk-go は Go の型で決め打ちしていたため **document の unmarshal ごと失敗し、その actor / Note がまったく取り込めなかった**。`APType` / `APObjectList` / `APRawList` / `APIDList` / `APLenientID` / `APLenientHref` で受ける (順に upstream の `getApType` / `toArray` / `toArray` (要素を decode せず `json.RawMessage` のまま持つ版) / `getApIds` / `getOneApId` / `getOneApHrefNullable` に対応)。`APLenientString` / `APLenientBool` / `APTruthyBool` / `APLenientInt` / `APLenientTimestamp` / `MultikeyList` / `Source` / `Endpoints` / `QuestionChoiceReplies` / `Image` / `Note` の寛容な `UnmarshalJSON` には upstream の対応物は無く、**JS が型を検査しないので結果的に通る**ものを Go で同じだけ通すためのもの。**inbox 経由の activity は `activitypub.Normalize` が先に `type` 配列や `{"@id": ...}` を潰す**が、actor / note / featured の生 fetch 経路は Normalize を通らないので、これらの型がその役目を負う。**Note は上の per-field の型に加えて、`Note.UnmarshalJSON` が型不一致を握って「読めた field だけ採用する」。** 後者が担うのは per-field で緩めていない `id` / `content` と `oneOf` / `anyOf` / choice の `name`。 upstream は JS なので型検査をほとんどせず (`content` は `typeof === 'string'` のガードを通って text=null のノートを作り、`oneOf` / `anyOf` が読めなくても `extractPollFromQuestion(...).catch(() => undefined)` で poll 無しのノートができる。choice の `name` が非 string のときは upstream も throw せず `filter(x => x != null)` で残すので、mk-go も空の選択肢を含む poll を作る)、この形なら call site を触らずに同じ挙動になる。**構文エラーは従来どおり弾く。** `published` は `APLenientTimestamp` が単一要素配列 / `{"@value": ...}` / epoch ミリ秒まで読む (`{"@value": ...}` は upstream の `new Date()` では Invalid Date になるので、ここは upstream より緩い)。**upstream は malformed な `published` を `isSafeT(new Date(...).valueOf())` で reject するが、mk-go は落とさず `parseAPPublishedTime` が受信時刻に fallback する** (元からの設計。ここだけ reject に倒すと upstream が受理する形まで巻き込むうえ、`encoding/json` は最初の型エラーしか報告しないので先行 field のエラーで判定が飛ぶ)。**actor 側は catch-all を使わず field ごとに緩める** (どの field を緩めたかが読めなくなるため)。`name` / `summary` は `APLenientString` にしてあるので、**upstream `validateActor` が throw する truthy な非 string (`["Alice"]` / `{"@value": "Alice"}`) も mk-go は受理する**。JSON-LD の展開形を拾うためで、値は `description` 2048 / `user.name` 128 に truncate + NUL 除去して書く。upstream も受理する falsy な非 string (`name: 0`) もこれで通る。**upstream より緩い箇所がいくつかある。** (1) actor の `attachment`: upstream `analyzeAttachments` は `Array.isArray` でない入力に `[]` を返して profile fields を捨てる (upstream 自身が TODO で疑問視している) が、mk-go は 1 件として取り込む。(2) `outbox` / `followers` / `following` / actor の `url`: **mk-go はこれらの値をそもそも読まない**。型を緩めた効果は「document を落とさなくなる」ことだけで、値は捨てる。upstream は `validateActor` でこれらの collection の host も actor に縛るが、mk-go は値を使わないので検証もしない。**実際に配送先になる `inbox` / `sharedInbox` / `endpoints.sharedInbox` は host を actor に縛ってある** (upstream の `punyHost` と同じく punycode と既定ポートだけ正規化し、**`www.` は同一視しない**。mk-go の `normalizeMatchHost` は #1820 の object-host binding 用に upstream の `normalizeSynonymousSubdomain` を取り込んで `www.` を剥がすが、upstream はそれを `assertActivityMatchesUrl` でしか使わない。配送先で同一視すると `www` サブドメインが別管理下にある環境でoutbound をそちらへ向けられる) (前者は `ErrInvalidActor`、後者 2 つは破棄)。`sharedInbox` の選択順も upstream の `x.sharedInbox ?? x.endpoints?.sharedInbox` に揃えた。**検証が無かった頃に取り込まれた既存行は直らない** — `fetchActor` が失敗するので `refreshActor` は `lastFetchedAt` 以外の列を更新せず、`user.inbox` に残った値がそのまま配送先に使われ続ける (profile / 鍵ローテーション / `movedTo` の追従も止まる)。検出は `SELECT id, uri, inbox, "sharedInbox" FROM "user" WHERE host IS NOT NULL AND (inbox IS NULL OR regexp_replace(lower(split_part(split_part(split_part(inbox,'//',2),'/',1),'@',-1)), CASE WHEN inbox LIKE 'https://%' THEN ':443$' ELSE ':80$' END, '') <> lower(host))`。**scheme ごとの既定ポート・userinfo・大文字小文字だけ正規化し `www.` は剥がさない** (剥がすと `sameDeliveryHost` が弾く行を見逃す。`:(443\|80)$` と一括で剥がすと `https://h:80/` のような非既定ポートの行を取り逃す)。実 PostgreSQL で 10 パターンを流して `sameDeliveryHost` と一致することを確認済み。punycode と Unicode IDN が混在する行、`user.host` にポートが入っている行、scheme が大文字の行、fragment 付きの inbox は偽陽性になりうる。**不正な percent escape (`%zz`) は偽陰性** — SQL では正常に見えるが `net/url.Parse` が弾いて `sameDeliveryHost` が false を返す。末尾改行 / 前後の空白 / 途中の tab は `trimWHATWGURL` が upstream の `new URL()` と同じだけ除去して**保存値ごと正規化する**ので、該当行は次の refresh で自動的に直る。**これは proxy でしかない** — 詰まるかどうかは相手が今返している document で決まるので、相手が直していれば自己回復する。**破棄の粒度だけ違う**: upstream は選ばれた 1 つを検証して不正なら両方消すが、mk-go は 2 つを独立に検証して不正な方だけ消す (残る値は host 検証済みなので安全側) (#2662)。(3) `featured` / `sharedInbox` / `endpoints.sharedInbox`: upstream の `getApId` は**配列を見ない** (`value.id` が undefined になって throw) が、mk-go は先頭を採る。`sharedInbox` 側は `validateActor` の中なので upstream では**その actor ごと reject** になる (`ApPersonService.ts:157`。`new URL(sharedInbox)` が throw する形も同じ)。mk-go は先頭を採ったうえで `sameDeliveryHost` に通し、通らなければ**その値だけ**捨てる。配送先の host 検証は同じ値に効くので緩いのは「actor を落とすかどうか」だけ。(4) `movedTo` / `alsoKnownAs`: upstream は生値をそのまま使う (`movedToUri: person.movedTo` / `toArray(person.alsoKnownAs)`) ので `{"id": ...}` 形式は一致判定に通らないが、mk-go は id を剥がすため通る。移行の認可 (`alsoKnownAsContains`) に効くが、値を publish するのは移行先サーバー自身なので権限的な穴にはならない。`to` / `cc` は要素単位で読めないものを落とす (upstream は `getApIds` が throw して Note ごと reject する)。要素を落とすと可視性は**狭い側**に寄る。`to` の `#Public` が読めない形 (`{"type":"Link","href":...}` など `id` を持たない object) で来ると、`cc` に followers があれば `followers`、`cc` も読めなければ `specified` (visibleUserIds が空なので事実上誰にも見えない) まで落ちる。document ごと捨てるより影響が小さいため採った。ただし `attributedTo` / `to` を**読めるようになったこと自体**で結果が変わる入力もある: `Create` activity の `to` が `{"id": #Public}` 形式のとき、修正前は audience の union に載らず specified だった Note が public になる (upstream 一致)。`attributedTo` の object 形式は upstream の inbox 経路 (`ApInboxService` の `actor.uri !== note.attributedTo` 生値比較) より緩いが、抽出後の値が配送 actor と一致する必要があり host 検証も同じ値を使うので偽装耐性は落ちない |
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

`third_party/misskey` fork (`shiroha-a/misskey-ts`) に載せている frontend の custom commit。**原則として**純正へ還元できない (= 純正 backend が対応しない) ものだけを置く方針。

**還元できるものを一時的に置く場合は、その行に必ず明記する。** 純正にも同じ不具合があるものをここへ置くと、この表を「還元不能な差分の一覧」として読む運用 (upstream 追従時に残す / 落とすを判断する材料) が壊れる。純正へ取り込まれた時点で revert する対象なので、行を読んだだけでそれが分かる必要がある。現時点の該当は `2026.7.0-mk.22h` / `2026.7.0-mk.22i` / `2026.7.0-mk.22j` / `2026.9.0-mk.1` / `2026.9.0-mk.2` / `2026.9.0-mk.2a` / `2026.9.0-mk.8e` / `2026.9.0-mk.8f` / `2026.9.0-mk.15` / `2026.9.0-mk.15a` / `2026.9.0-mk.15b` / `2026.9.0-mk.15c` / `2026.9.0-mk.16` / `2026.9.0-mk.16a` / `2026.9.0-mk.16b` の 15 行 (**base を省略しない** — bump で `-mk.N` は 0 に戻るので省略形は曖昧になる)。

**現在の pin は `2026.9.0-mk.16b` (`04f13f55`)。** tag 列は「その変更が最初に入った世代」で、
`2026.7.0-mk.*` の行はすべて 2026.9.0 への載せ替え (`git rebase --onto 2026.9.0 2026.7.0`、
custom commit 50 個) で `2026.9.0-mk.0` に入っている (`2026.9.0-mk.1` 以降は載せ替えの
後に積んだもの)。載せ替えで衝突したのは
`packages/frontend/src/pages/admin/job-queue.vue` の 1 ファイルだけで、
upstream が `jobState` の型を autogen (`AdminQueueJobsRequest['state'][number]`) に
変えたところに fork の `ApiQueueName` キャストが重なったもの。**upstream 側の型を
採り、fork のキャストは残した** — Paused タブは upstream / fork のどちらにも既に
無いので `'paused'` は到達しない。

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
| `2026.7.0-mk.22c` | boot エラー画面の Reload を `addEventListener` にする (#2786)。**inline event handler は CSP の hash では通らない** (`'unsafe-hashes'` が要る) ので、`script-src` から `'unsafe-inline'` を外すと `onclick="location.reload(true);"` が block され、「Failed to initialize Misskey」画面の唯一の復旧手段が押しても反応しなくなる。SPA shell が読む `packages/frontend` 側の inline handler はここ 1 箇所だけ。`packages/frontend-embed/public/loader/boot.js` にも同じ形が残っていたが、`/embed/` に CSP を広げた #2789 で `mk.22d` として直した |
| `2026.7.0-mk.22d` | embed の boot エラー画面の Reload を `addEventListener` にする (#2789)。`mk.22c` の embed 版。`/embed/` にも CSP を付けたので、`onclick` のままだと**埋め込み先の利用者が取れる唯一の復旧手段**が押しても反応しなくなる。これで `packages/frontend` / `packages/frontend-embed` の src と public から inline event handler は消えた |
| `2026.7.0-mk.22e` | 承認制を切るときにアカウント作成をどうするか聞く (#2803)。承認制を入れる更新はアカウント作成を同じ更新で開放するので、外す更新でその開放が残ると**ゲートが 1 つも無い全開状態**になる。アカウント作成のトグル自体を入れるときは確認ダイアログを挟むのに、この経路は素通りするので無警告で起きていた。OFF 側で 3 択 (閉じる / 開けたまま / やめる) を出し、`disableRegistration` を必ず明示して送る — 省略するとサーバー側が閉じる側の既定を補うため「開けたままにする」が選べない。あわせて API 失敗時に ref を戻す (楽観更新のままだと画面だけ招待制になり、実際には申請を受け付け続ける) |
| `2026.7.0-mk.22f` | 申請フォームに署名付きトークンを載せる (#2806)。captcha が 1 つも設定されていないとき、承認制の申請 endpoint には IP レート制限以外の防波堤が無い (既定構成がそれ)。サーバーが `signup-application/form-token` で発行するトークンを受け取り、最短滞在時間が明けるまで送信を抑えて送る。**captcha の代替ではない** — 止まるのは「フォームを取得せずに endpoint を直接叩く」bot だけ。取得前・取得失敗時の扱いは「詰まらせない」側に倒してある (失敗時は説明文と再読み込みを出すが送信は塞がない) |
| `2026.7.0-mk.22g` | 承認制と招待制を重ねたときの説明を実装に合わせる (#2813)。「承認は内部で招待を発行して通すので二重のゲートに意味が無い」と書いていたが、発行するのはメール確認の経路だけになった。実際に起きるのは**登録手段がゼロになる**こと (承認制の入口は `disableRegistration` で 503、`/api/signup` は承認制で 403) |
| `2026.7.0-mk.22h` | WebSocket 接続時に未読通知の件数をサーバー値へ揃える (#2831)。通知バッジの件数は**サーバーが持っておらず**、`unreadNotification` (+1) と `readAllNotifications` (0) の差分イベントだけで同期している。pub/sub なので切断中に発行された分は再送されない。`readAllNotifications` を取りこぼすとサーバー側の既読位置だけが先に進み、暗黙既読 (通知一覧の取得 / WebSocket の `readNotification`) からは「既読位置が動いたとき」という発行条件を満たさなくなるため、**次の通知を受け取って読むまで**バッジが残り続ける (恒久的に固まるわけではない)。`serverDisconnectedBehavior` の既定は `quiet` なので、切断しても何も起きずそのまま stale な `$i` で走り続ける。**`$i` を丸ごと取り直す `refreshCurrentAccount()` は使わない** — 取得に失敗するとサインアウトして localStorage ごと消す経路を持ち (`fetchAccount` が 4xx の error 応答を全て `isAccountDeleted` に倒す)、サーバー再起動の直後は一時的な認証失敗が起こりうるうえ再接続は全タブ全ユーザーで同時に走るため、巻き添えでサインアウトさせうる。未読の 2 フィールドだけを部分適用し、失敗はダイアログにもサインアウトにも倒さない。飛行中に差分イベントが来たら世代カウンタで応答を捨て、in-flight の重複排除 + 30 秒スロットル + 最大 10 秒のジッタを掛ける。**失敗したときは抑止を 5 秒まで巻き戻す** — 再接続はほぼ即時 (`minReconnectionDelay` は 1ms) なので、素の 30 秒だと「起動途中のサーバーに繋がって `/api/i` が 502 → 直後に再接続」で抑止され、そのタブが以降ずっと stale になる。初回接続はブート時の `refreshCurrentAccount` と重複するのでスロットルで抑止する (`_disconnected_` は state が `connected` になった後の close でしか出ないので、一度も繋がらないまま復帰した場合に判別材料にならない)。**これは upstream Misskey にも同じ形で存在する不具合**で、この tag は例外的に「純正へ還元できるもの」を置いている (純正への PR は別途)。backend 側の復帰手段 (`mark-all-as-read` の force) は mk-go 本体で直した |
| `2026.7.0-mk.22i` | `meUpdated` は未読を載せるときだけ世代を上げる (#2831)。`-mk.22h` の resync は「飛行中に未読を書くイベントが来たら応答を捨てる」ために世代カウンタを持つが、**`meUpdated` は未読を載せる producer と載せない producer が混在する**。mk-go では 2FA 系だけが `meDetailedWithUnread` で実値を載せ、プロフィール更新 / pin の経路は `PackUserDetailed` (= `UserDetailed`) を送るのでキー自体が無い (未読 2 フィールドは `MeDetailed` 側の宣言)。部分 merge の `publishMeUpdatedPartial` も指定 field しか持たない。載っていないのに世代を上げると、飛行中の応答が捨てられたうえで**誰も正しい値を書かない**ので、バッジが stale のまま次の再接続まで残る = 直しに来た症状そのものになる。`-mk.22h` と同じく純正へ還元できる行にあたる |
| `2026.7.0-mk.22j` | type-only import を top-level 形式に直す (#2843)。eslint の `import/consistent-type-specifier-style` 違反が mk-go 独自ファイル 2 つ (`plugin-api.ts` / `MkPluginSlot.vue`) に 4 箇所 commit 済みで残っていた。**fork frontend の eslint が CI で一度も実行されていなかった**ため誰も気付いていなかったもので、同 issue で `frontend-check` job に足す前提として直す。upstream 由来のファイルに違反は無い。`-mk.22h` / `-mk.22i` と同じく純正へ還元できる行にあたる |
| `2026.9.0-mk.0` | Misskey 2026.9.0 への載せ替え (#2877)。**独自変更の内容は上の `2026.7.0-mk.*` の行がそのまま移ったもの**。固有の変更は衝突解決の 1 箇所だけで、`packages/frontend/src/pages/admin/job-queue.vue` の `jobState` の型を upstream の autogen (`AdminQueueJobsRequest['state'][number]`) に寄せ、Paused タブの entry を落としている (fork の `ApiQueueName` キャストは残した) |
| `2026.9.0-mk.1` | 通報画面を 5W1H の定型フォームにする (#2879)。通報の宛先は `users/report-abuse` の `comment` という単一の文字列のままで、カテゴリ・該当 URL・発生日時・詳細・補足をクライアント側で 1 つの本文に組み立てる。モデレーターが初動を判断するのに足る情報を、報告者が書き漏らさない形で集めるのが狙い。**純正へ還元できる行にあたる** (純正 backend の変更を要さない) ので、`-mk.22h` / `-mk.22i` / `-mk.22j` と同じく upstream へ出せる。外部コントリビューターからの PR を、レビューで出た 4 点 (上限判定が恒真で自動収集した文脈が無言で消える / リモート利用者とリノート元の host が落ちて該当 URL・メンションが別人を指す / `where` が single-line `<input>` に改行入りで渡り URL が連結される / 上限テストの context がフィールド名を誤っていて狙った状況を再現していない) を直したうえで取り込んだ。**spec の置き場所** (`specs/upstream` → `specs/mkgo`) は fork ではなく mk 本体側の修正 |
| `2026.9.0-mk.2` | 通報コメントのリノート元の作者も acct で組む (#2879)。`-mk.1` は該当 URL と対象ユーザーを直したが、リノート元に渡す作者名だけ `username` のままだった。コメントは `<Mfm>` でレンダーされるので、host を落とすと `@bob` が mention ノードになり**ローカルの別人へリンクする**。`-mk.1` と同じく**純正へ還元できる行** |
| `2026.9.0-mk.2a` | バックグラウンド復帰時に WebSocket を張り直す (#2883)。モバイル PWA を復帰させると、OS がサスペンド中に TCP を切っているのにブラウザが `close` を配送せず `readyState` が `OPEN` のまま残る (zombie socket)。`reconnecting-websocket` は `close` / `error` を観測しないと再接続を始めないので**リトライが一度も走らない**。さらに `Stream.onClose` が動かないため `state` が `'connected'` のままで `_disconnected_` が出ず、`serverDisconnectedBehavior` のリロードもダイアログも `quiet` のバナーも**同時に沈黙する** (3 つとも同じイベント 1 本にぶら下がっている)。heartbeat (`'h'`) は生存確認にならない — サーバーは返事をせず (upstream の `Connection.ts` / `StreamingApiServerService.ts` は protocol ping/pong と `connect` の `pong` フラグしか持たず、mk-go の `HandleClientMessage` にも `h` の case が無い)、死んだソケットへの `send()` は例外を投げない。**ゾンビの検知はせず、20 秒以上隠れていたら生死を判定せず張り直す** — 正確な検知には app レベルのプローブとサーバー応答とタイムアウト調整が要り、応答しないサーバー向けのフォールバックまで要る。誤って生きた接続を張り直す代償は再ハンドシェイクと購読の再送だけで、UI にも出さない。**`reconnect()` は自分で `onClose()` を呼ぶ** — RWS の `reconnect()` が `close` を配送するのは `readyState` が `OPEN` のときだけで、`CLOSED` / ソケット未生成では黙って繋ぎ直し、直後の `_connect()` が `_removeListeners()` でキュー済みの `close` も捨てる。frozen なページでは「CLOSED だが close は未配送」が滞在中ずっと続くので、埋めないと接続だけ張り直って購読ゼロになる (= 直しに来た症状の再現)。**`Pool` の購読リセットは `_disconnected_` の購読ではなく `Stream` からの直接呼び出し** — 通知を抑止すると道連れでリセットが飛び、`connect()` が早期 return して購読が復活しないため。張り直せないまま 30 秒を過ぎたら通常の切断として通知する (黙ったままだと離席中にサーバーが落ちても無表示になる)。**純正へ還元できる行** (純正 backend の変更を要さない) |
| `2026.9.0-mk.3` | `/about-mkgo` を新設し、ソースコードの案内をそこへ集約する (#2700)。実際に動いているのは mk-go なのに、説明・ソース案内・謝辞がすべて upstream Misskey のものだった。**体裁の話ではなく AGPL-3.0 section 13 の不備**で、新規インスタンスでは `/about-misskey` の「これは改変版です」節が「ソースコードはまだ提供されていません」の警告だけになる状態だった (`meta.repositoryUrl` が NULL のため `v-if` が falsy になり、**改変版の**リンクが 1 本も出ない。upstream Misskey 本体 / Crowdin / Patreon へのリンクはページ上部に出るので、ページ全体が空だったわけではない)。案内先が間違っているのではなく、**動いているコードに対応する案内が無い**。`MkSourceCodeAvailablePopup` がこのページへ誘導するので、ポップアップを追った利用者はその警告に行き着く。導線 3 箇所 (サイドバー / `/about` overview / ポップアップ) を `/about-mkgo` へ向け、`/about-misskey` は残して相互に行き来できるようにした。**`about-misskey.vue` は導線 1 ブロックしか触らない** — upstream が頻繁に更新するファイルなので、書き換えると追従のたびにコンフリクトを手で解くことになる。**純正へは還元できない行** (mk-go 固有の説明ページ) |
| `2026.9.0-mk.4` | エントランスの「他のサーバーを探す」を削除する (#2814)。訪問者ダッシュボードの 3 つのメインアクションの真ん中にあり、Misskey Hub のサーバー一覧 (`https://misskey-hub.net/servers/`) を開いていた。**mk-go はあの一覧に載らない** — nodeinfo で `software.name = "mk-go"` を返すので、Misskey として登録されたサーバーを並べる一覧に現れることはない。**「片道リンク」ではなく「行き先に mk-go が存在しないので機能しないリンク」**が正確な言い方 (`target="_blank"` なので元のタブは残る)。**差し替え先が無いので消した**のであって「不要だから」ではない — mk-go のサーバー一覧を作る予定が無い以上、別の一覧へ向ける・設定で切り替えられるようにする、はどれも「いつか一覧ができたら」という存在しない前提をコードに残すだけになる。**upstream 追従で同じ行に差分が出たとき、反射的に戻さないこと。** 失うものはある — upstream があのボタンを置いているのは「ここには入れなかった訪問者の行き先」でもあり、承認制 (#2554) や招待制のサーバーでは削除後の導線が細る。残るのは `⋯` メニューの「お問い合わせ」(`/contact`) で**ゼロにはならない**が、あのボタンは `aria-label` も `title` も持たないアイコンのみ (upstream 由来) なので、支援技術からは実質届かない。それでも行き先が mk-go を載せない一覧である以上、元から解決していない。**インライン `margin-right: 12px` は残す** — `full` は `width: 100%` で cross size が `auto` でない flex item は stretch されないため、この宣言はボタンを短くせず margin box を `.mainActions` の `padding: 32px` の内側へはみ出させるだけで**視覚効果がゼロ** (実測: margin の有無・ボタン 2 個と 3 個のいずれでも幅 536px / x=32 で不変)。横並びだった頃 (`inline` prop) の名残だが、掃除しても得が無く upstream ファイルの差分が増えるだけ。**ロケール定義 `exploreOtherServers` は upstream のものなので残す** — 消しても得は無く追従時の差分が増えるだけ。**純正へは還元できない行** (mk-go が別の software 名を名乗ることが前提) |
| `2026.9.0-mk.5` | エントランスの GitHub リボンを `/about-mkgo` へ向ける (#2890)。upstream は Misskey 本体のリポジトリを指すが、`aria-label` が "View source on GitHub" と名乗るとおりこれは**動いているコードのソース**を示す導線で、mk-go では別実装を指すことになっていた。**#2700 が導線 3 箇所 (サイドバー / `/about` overview / `MkSourceCodeAvailablePopup`) を `/about-mkgo` へ向けたときの取りこぼし**で、AGPL-3.0 section 13 の観点では同じ系統。未ログインのトップ (`isRoot`) でのみ右上に固定表示される。**`instance.repositoryUrl` へ直リンクしない** — operator が改変していない構成では mk-go 本体だけを指し、いま表示している画面 (fork frontend) のソースが案内から漏れる (#2700 が 3 段構造にした理由)。`repositoryUrl` は GitHub とも限らないので、オクトキャットのアイコンと食い違いうる。`<a href>` から `MkA to` に変えたので SPA 内遷移になり `target="_blank"` は落とした。`aria-label` は他の 3 導線と同じ `i18n.ts.aboutMkGo` にする — 同じ行き先に別の名前を付けると、支援技術のリンク一覧で区別できない同名が並ぶ (`sourceCode` は `about.overview.vue` が**外部の** `instance.repositoryUrl` に使っている)。**アイコンは変えない** — 直接の遷移先は GitHub ではなく内部ページだが、そこから mk-go 本体 / フロントエンドの GitHub へ 3 本出る (`serverRepositoryUrl` は operator 申告なので 1 ホップ先が GitHub とは限らない)。オクトキャットを別のアイコンに替えると `github-corner` の装飾ごと作り直すことになり、upstream ファイルの差分が増える。**純正へは還元できない行** (`/about-mkgo` は mk-go 固有ページ) |
| `2026.9.0-mk.6` | `about-mkgo` のアバター非表示の理由を実態に直す (#2892)。「mk-go の CSP では `avatars.githubusercontent.com` が必ず落ちる」と書いていたが、mk 本体が `img-src` にその origin を足したので成立しなくなった。**アバターを出さない判断自体は変えていない** (新規ページなので最初から外部画像を持たせる必要が無い)。理由が古いままだと、この行を読んだ人が誤った前提で判断する。**純正へは還元できない行** (mk-go 固有ページのコメント) |
| `2026.9.0-mk.7` | リモート絵文字を右クリックからインポートできるようにする (#2698)。投稿本文中の絵文字 (`MkCustomEmoji`) とリアクション (`MkReactionsViewer.reaction`) の**両方**に導線を足し、どちらからも同じモーダルを開く (**CherryPick は本文からはモーダル、リアクションからは endpoint 直叩きで揃っていないが踏襲しない**)。`MkRemoteEmojiEditDialog` は表示専用だったものを編集可能にし、カテゴリ・エイリアス・ライセンス・センシティブを `admin/emoji/fetch-remote-meta` の取得値で埋める。**取得に失敗しても取り込みは続く** — 相手が per-name endpoint を持たない (Mastodon 系) のは正常な結果なので、理由を出して手入力に倒す。権限は `$i.isModerator \|\| $i.policies.canManageCustomEmojis`、ローカル絵文字には出さない。**mk-go 独自 endpoint と additive パラメータは misskey-js の autogen 型に無い**ので `as never` キャストを使う (`signup-applications.vue` と同じ理由)。**純正へは還元できない行** (純正 backend に取得 endpoint が無い) |
| `2026.9.0-mk.7a` | リモート絵文字インポートの導線と取得回数を直す (#2698)。敵対的レビューで見つかった 3 点。(a) **本文中の絵文字からのインポートが必ず no-op だった** — `MkCustomEmoji` は `name` (ホスト無しの裸の名前) と `host` を別の prop で受け取るのに `name@host` を期待していたため、メニューは出るのにクリックしても何も起きなかった。(b) **1 回のインポートで相手へ 2 リクエスト**出ていた (ユーティリティとモーダルが別々に取得。キャッシュ無し・timeout 10 秒なので最悪 20 秒)。(c) **管理画面の「詳細」を開くだけで外向き通信が発生し**、しかも Import で既存のカテゴリ・エイリアスが空で潰れていた (props が `id`/`name`/`host`/`license`/`url` しか持たないためフォームの初期値が空)。編集フォームと上書き送信は取得結果を渡された経路 = インポート導線でだけ有効にした |
| `2026.9.0-mk.8` | 通報の通知と、ロール単位の通知 opt-out を出す (#2868 / #2898)。`MkNotification` に `abuseReport` の分岐を足し、ヘッダ・本文・管理画面へのリンクを出す。**リンクが要点** — 通知欄で本文だけ見えても、対処するには結局どの通報かを探すことになる。あわせて**未知の型の受け皿** (`v-else`) を足した — これが無いと mk-go 固有の通知や upstream が後から足した型が**ヘッダも本文も空で描画される** (`pollVote` が実際にそうなっていた)。`roles.policy-editor` には `optOutNotificationTypes` をチェックボックスで出す (型名を手打ちさせない)。**misskey-js の autogen 型は触らない** — openapi から再生成されるので足しても次の生成で消える。mk-go 独自 policy / 通知タイプはキャストで受ける (独自 endpoint を `as never` で呼ぶのと同じ扱い)。i18n は `_mkgoNotification` を新設し、`_mkgoUnsupported` と同じく ja-JP のみ。**純正へは還元できない行** (純正 backend にこの通知タイプと policy が無い) 敵対的レビューで見つかった 3 点も含む。(a) **ロール個別の policy が UI から保存できなかった** — `roles.editor` の「fill missing policy」ループが misskey-js の `rolePolicies` (upstream キューのみ) を回すため mk-go 固有キーの枠が作られず、setter の `!= null` ガードが書き込みを黙って捨てていた。ベースロールだけは `instance.policies` を直接使うので動いており、「全員 opt-out」しか設定できない状態だった。(b) 通知のヘッダを「{name} からの通報」にした — アバターと名前は通報者のものなので、「新しい通報」だけだと通報された側と読み違えやすい。(c) `/admin/abuses?reportId=` が無視されていた (`abuses.vue` が query を読まず、backend にも絞りが無かった)。 本番確認で 4 点直した。(a) **通知に通報コメントを出さない** — 定型フォームの全文 (違反カテゴリ / 対象 / 該当 URL / 詳細) が入るので通知欄では読めない。誰からの通報かだけ伝え、中身は「通報を確認」ボタンから管理画面で見る。(b) **アイコンを警告色からエラー色へ** — `--MI_THEME-warn` は実績の `--eventAchievement` (#cb9a11) とほぼ同じ黄色で、通知一覧で並ぶと区別が付かなかった。(c) **バッジの `padding` を外した** — `.subIcon` は `box-sizing: border-box` + `line-height: 20px` で中身を中央に置くので、padding を足すと内容領域だけ縮んでアイコンが下へはみ出す (他の `t_*` はどれも padding を持たない)。(d) **1 件表示の通報を畳めないようにし、見出しも出さない** — `MkFolder` に `canCollapse` を足した (既定 true)。見出しは開閉のためのものなので畳めない状態では役に立たず、通報の 1 件表示では見出しが持つ情報 (対象 / 通報者 / コメント / 日時) が本体の「対象」「詳細」「通報者」にすべて出るので丸ごと重複していた。**DOM 構造は変えていない** — `MkFolder` は 75 ファイルで使われ Playwright の 18 spec が `folder-header` をクリックするので、`button` を `<component :is>` に置き換える案は採らず、クリックハンドラと chevron の出し分けだけにした。 (e) **対処済みの通報を通知欄で見分けられるようにした** — 未対応は赤いバッジ + `!`、対処済みはグレー + チェックにし、ヘッダに「対処済み」チップを出す |
| `2026.9.0-mk.8a` | 分割アップロードのロールポリシーが設定できないのを直す (#2900)。`canUseChunkedUpload` / `chunkedUploadMaxConcurrentSessions` / `chunkedUploadMaxPendingMb` は導入時 (#2313) から**キー一覧にも編集フォームにも無く、管理画面から設定できなかった** — backend は読んでいる (`internal/core/drive/chunked_upload.go`) ので API からは設定できたが、`canUseChunkedUpload` の既定が `true` なので**特定のロールだけ禁止することが画面からできなかった**。#2898 で入れた「mk-go 固有 policy キーが fork frontend の 2 箇所に列挙されているか」のゲートが検出した。**キャストは汎用ヘルパー 2 つに集約した** (`mkGoPolicyValue` / `mkGoPolicyMeta`) — 固有キーが 4 つになり、キーごとに computed を手書きするとキャストが散らばって片側だけ直す形の穴ができる。**サーバー全体の設定が上限**である旨を caption に明記した (ロールに大きい値を入れても instance 設定は超えられない)。**純正へは還元できない行** (純正 backend にこの policy が無い) |
| `2026.9.0-mk.8b` | リモート絵文字インポートの導線 2 点を直す (#2903)。(a) **モーダルの画像が表示されなかった** — `originalUrl` (相手サーバー上の URL) を `<img src>` にそのまま入れており、mk-go の CSP (`img-src 'self' data: blob:`) でブロックされていた。通常の絵文字表示 (`MkCustomEmoji`) は media proxy を通しているのに、モーダルだけが raw を使っていた。静止画設定 (`disableShowingAnimatedImages`) の扱いも揃えた (**#2905 で backend 側も直した** — それまでは `processAndReturn` の pass-through 判定が `mode` しか見ておらず、`?emoji=1&static=1` でも GIF がそのまま返っていた。`parseMode` の順序を入れ替えるだけでは直らない — `ModeStatic` に倒すとリサイズ寸法まで変わるので、`animated` を `mode` と直交する軸にしてある)。**インポート自体は正しく動いていた**ので、壊れていたのはプレビューだけ。**プロキシの fallback に任せる** — `MkCustomEmoji` は `@error` で `:name:` に落とすが、このモーダルには受け皿が無く、allowlist が 403 を返すと 4 タイルすべてが壊れ画像になって理由も出ない。(b) **同名のローカル絵文字が既にあってもメニューが出ていた** — 押しても `admin/emoji/copy` が重複で弾くだけで、押してみるまで分からなかった。`customEmojisMap` (裸の名前がキー) で判定して本文中・リアクションの両方から出さない。**判定は `hasLocalEmojiWithSameName` に集約した** — `name@host` の分解が 3 箇所に重複しており、その重複のせいでリアクション側を「変数は作ったが条件式に配線し忘れる」形で出しかけた (vue-tsc は通り、CI の eslint は `--quiet` なので未使用変数も出ない)。単体テスト 10 件で固定してある。**純正へは還元できない行** (インポート導線が mk-go 固有) |
| `2026.9.0-mk.8c` | 静止画設定で mention chip のアバターが壊れるのを直す (#2908)。`getStaticImageUrl('/avatar/@u@h')` は `/avatar/` を知らないので `<mediaProxy>/static.webp?url=<instance>/avatar/@u@h&static=1` を組み立てるが、**mk-go の media proxy は allowlist が DB に実在する URL だけを通す**ため 403 + `max-age=86400` になり、静止画になるどころか 1 日壊れていた (`disableShowingAnimatedImages` / `dataSaver.avatar` を有効にした利用者だけが踏む)。`/avatar/` 側が `?static=1` を受けて署名付きプロキシ URL へ 302 するようにしたので、frontend は素の `/avatar/@u@h?static=1` を出せばよくなった。**純正へは還元できない行** (upstream は proxy が open なので元の形で通る) |
| `2026.9.0-mk.8d` | 静止画設定でアバター未設定の利用者のアイコンが壊れるのを直す (#2913)。`getStaticImageUrl` は `/emoji/` と mediaProxy 接頭以外を無条件に `<mediaProxy>/static.webp?url=…&static=1` へ包むが、**`sig` を付けない**ので `Authorize` が HMAC ではなく DB allowlist に落ちる。アバター未設定の利用者の `avatarUrl` は相対の `/identicon/<username>` で (`entity.IdenticonURL`)、allowlist の 4 テーブル UNION のどこにも無いため **403 + `max-age=86400`**。`MkAvatar` は通常のアバター表示すべてを通すので、`disableShowingAnimatedImages` / `dataSaver.avatar` を有効にした利用者には**タイムライン・通知・プロフィールのアイコンが 1 日壊れて見えていた** (本番実測で対象は `avatarUrl` が空の 25,605 / 37,396 = 68.5%)。identicon は生成した PNG なのでアニメーションせず、静止画へ変換する必要がそもそも無いため素通しにする。**同一オリジン限定**にするのが要点 — 他インスタンスの `/identicon/` はこちらの生成物ではないので従来どおりプロキシに通す。#2908 (`/avatar/`) と同じクラスだが、あちらは backend で `static` を受けて解決したのに対し、identicon は変換自体が不要なので frontend 側で止める。**純正へは還元できない行** (upstream は proxy が open なので元の形で通る) |
| `2026.9.0-mk.8e` | モバイル幅でモデレーションノートの追加ボタンだけ中央揃えから外れるのを直す (#2926)。プロフィールページ (`pages/user/home.vue`) の `@container (max-width: 500px)` ブロックは `.avatar` (`margin: auto`) / `.roles` (`justify-content: center`) / `.description` (`text-align: center`) を**個別に**中央へ寄せているが、`.moderationNote` にだけ指定が無く、ボタンだけ左端に残っていた。**親に `text-align: center` を足しても直らない** — `MkButton` の root は `display: block; width: max-content` のブロック要素なので、中央に置くには `margin-inline: auto` か親を flex にするしかない。**回帰ではない** — ボタンが入った upstream `1c0ec222b4` (2023-05) から一度も中央揃えの指定が無く、`2023.12.2` / `2024.5.0` / `2025.2.0` / `2025.4.1` / `2026.5.0` / `2026.9.0` の 6 タグすべてで `margin: 16px 16px 0 16px;` だけだった。**CherryPick は約 1 か月後の 2023-06 に直している** (`acccc705b9`) ので、その実装 (ボタンに `moderationNoteButton` を付けて `div > .moderationNoteButton { margin: 0 auto; }`) を採った。**セレクタは CherryPick と一致させ、理由の日本語コメントだけ足した** (`> div > .moderationNoteButton` で足りるが、突き合わせを楽にするため緩いまま揃えてある) — `yojo-art` / `kokonect-link` の両 fork で同一で、chat / reversi と同じく cherrypick 由来の形に揃えておくほうが将来の突き合わせが楽。**`.moderationNote` 全体を flex にしない** — 編集中は `MkTextarea` が入るので、flex にすると内容幅に縮む。既定幅 (`> 500px`) ではアバターを避ける `margin: 12px 24px 0 154px` の左マージン 154px の起点に残るのが正しく、中央へ寄せるのは 500px 以下だけ。モデレーター以上にしか見えない (`v-if="iAmModerator"`)。**位置は Playwright で幾何として固定した** (`specs/mkgo/ui/profile_moderation_note_button_align.spec.ts`) — CSS が書いてあることを見ても中央に来ているかは分からないので、ボタンと `.moderationNote` の実座標を測る。**`specs/mkgo/` に置くのが要点** — 純正は今も直していないので、`specs/upstream` だけを回す `make playwright-ts-test` (backend = ts) の対象に入れてはいけない。**3 つの assert すべてを変異させて落ちることを実測した** — 修正前のビルド (`2026.9.0-mk.8d`) で中心が 101.53px ずれ、`margin: 0 auto` を container query の外へ漏らすと既定幅で 240.53px ずれ、`.moderationNote` を flex にすると編集中の幅が 11.31px 縮む (後 2 つは `page.addStyleTag` で注入。再ビルドが要らない)。**純正へ還元できる行** (純正にも同じ不具合があり、3 年以上直っていない) |
| `2026.9.0-mk.8f` | リアクションを長押ししてもメニューが出ないのを直す (#2932)。`MkReactionsViewer.reaction.vue` はメニューを `@contextmenu` にだけ配線しており、**iOS Safari は `<button>` の長押しで `contextmenu` を発火しない**ため、#2698 で入れたリモート絵文字のインポート導線に **iOS からは最初から到達できなかった**。タップは `toggleReaction()` に取られているので代わりの入口が無い (本文中の絵文字 `MkCustomEmoji` はメニューが `@click` なのでタップで開く — 同じ機能の 2 つの入口でモバイルの到達性が食い違っていた)。**CherryPick からは採れない** — `yojo-art` は `@contextmenu` のみ、`kokonect-link` は同じ要素に `@touchstart` + 500ms の長押しを持つが、開くのはメニューではなく `stealReaction` で、後続 `click` の握り潰しも移動判定も無い。touch ベースの長押しを `utility/long-press.ts` に切り出して足した (本体側も 13 行を触っており、fork 差分は upstream `2026.9.0` 比で +20 → +50。数え方は `git diff --numstat` の追加行からコメントと空行を除いたもの)。**後続の `click` を握り潰すのが要点** — 潰さないと、メニューを開いた指を離した瞬間にタップとして扱われ、リアクションが付け外しされる。**`capture: true` が要る**: Vue は `@click` を同じ要素に張るので、bubble 段階で受けると登録順 (Vue の patch が先) の分だけ相手が先に走る。**握り潰しの期限は touchend を起点にする** — 「この touch 操作で長押しが成立したか」を真偽値で持ち、指を離した時点で 1 秒の期限へ変換する。真偽値だけだとメニュー側をタップして `click` が来なかったときに立ちっぱなしになり次の操作を 1 回食う。かといって**発火時刻を起点にすると、メニューを読んでから指を離すだけで抜けてリアクションが動く** (敵対的レビュー 2 周目で実測。1 周目の修正が作った回帰だった)。**`contextmenu` が touchend より後に来る経路でも期限を入れる** — 入れないと同じ立ちっぱなしになる (3 周目で実測)。あわせて (a) `touchmove` が 8px を超えたら取り消す (スクロール中の誤発火)、(b) Android Chrome は長押しで `contextmenu` も発火するので、こちらが先なら握り潰し、ブラウザが先ならタイマーを取り消す (二重に開かない)。**どちらの経路でも後続の `click` は握り潰す**。**タッチ由来かは主に「指が触れているか」で見る** (時刻の差だけだと `delay` を長くしたときに黙って無効になる。touchend の直後に来る分だけ 1 秒の窓で拾うので、**タッチの1 秒以内に来たマウスの右クリックは誤ってこちらの分として握り潰される** — ハイブリッド端末でしか踏まないので許容)。**接触点は `targetTouches` で数える** — `touches` は画面全体なので、別の指がどこかに触れているだけで長押しが黙って死ぬ (3 周目で実測)。(c) `-webkit-touch-callout: none` で iOS の「画像を保存」の吹き出しを止める (`user-select` は足さない — `style.scss` の `html:not(.forceSelectableAll)` から既に継承されており、足すと「全てのテキスト要素を選択可能にする」設定だけを打ち消す)。**`menu()` にアンカーを渡せるようにした** — `ev.currentTarget` は dispatch が終わると null になるので、`setTimeout` 越しに呼ぶ長押し側は要素を明示で渡す。**一般利用者の挙動も変わる** — このメニューは絵文字のミュートを誰にでも (未ログインでも) 出すので、限定されているのはインポートの項目だけ (`isModerator` またはロールポリシー `canManageCustomEmojis`)。また iOS では**ゆっくり押して離すとリアクションが付かなくなる** (長押しとして扱われる)。Android は `contextmenu` 経由で元からそうなっていたので、iOS を揃えた形。単体テスト 28 件 (`test/unit/long-press.test.ts`、変異 16 種で検証。**発火後のケースは必ず `touchend` を挟む** — 省くと「どれだけ長く押していたか」の軸が消えて上の回帰を見逃す) と、配線が落ちていないかの静的ゲート (`TestReactionLongPressIsWired`、変異 7 種で検証) で固定した。**iOS の実機で確認済み** (メニューが出る / 指を離してもメニューが残る / リアクションが動かない)。**指を離してもメニューが残るかは手元では判定できなかった** — `MkModal` は開くときアンカー要素を `pointer-events: none` にし (`MkModal.vue:322`)、その上の全画面の背景が `mousedown` でも閉じる (`MkModal.vue:36`)。iOS は `touchend` の後に合成クリックを撃ち、当たり先は指を離した位置の再ヒットテストで決まるので、**背景に落ちて即閉じる可能性があった**。実機では閉じなかったので、WebKit がタッチ中の DOM 変化を見て合成クリックを撃たない側に倒れている。**この前提が変わると静かに壊れる** — 握り潰しは要素にしか張っていないので、背景に落ちる経路は止められない。**純正へ還元できる行**。 |
| `2026.9.0-mk.9` | リモートのリアクションにローカルの同名絵文字で相乗りできるようにする (#2697)。リモートのリアクション (`:foo@host:`) は `canToggle` が false なので押せないが、**ローカルに同じショートコードの絵文字があるならそれでリアクションできる**ようにした。CherryPick 系に同等の実装がある (`reactableRemoteReactionEnabled`)。**backend は変更していない。** リモート利用者が送ってくる `:foo@their.host:` はそのホストの絵文字を指しているので、`resolveReaction` を「ローカル優先」に変えると AP の受信側が別の絵文字として記録する。相乗りは**送る側の話**で、送るショートコードを frontend が選び直せば足りる。この不変条件を固定するテストが無かったので足した (`TestService_Create_SameShortcodeLocalAndRemote`) — 既存の `TestService_Create_CustomEmojiRemote` はリモートだけを seed しているので、**ローカル優先に変えた変異を検出できなかった**。**送るのは `:foo@.:` (ローカルホストマーク付き)。** `:foo:` でも backend はローカルに解決するが、返る `myReaction` は `:foo@.:` に正規化されるので、揃えないと確認ダイアログの文言が「取り消し」ではなく「付け替え」になる。**数は合算されない。** リアクションは文字列キーで持つので、`:foo@host:` のチップを押すと `:foo@.:` のチップが別に増える (既にローカルの同名チップがあればそちらが増える)。**合算は backend 無改造でもできる** — `resolveReactionValue` は任意 host を受けるので、`:foo@host:` をそのまま送れば合算される (この PR のテストの 1 つ目がその挙動を固定している。ただしローカル DB に `(name, host)` の行がある場合に限り、無ければ ❤ に倒れる) — が採らない。理由は AP の受信側ではなく、(a) **純正 TS は任意 host を受けない** (`isCustomEmojiRegexp` が `(?:@\.)?` しか許さず ❤ に落ちる) ので drop-in で戻したときに壊れる、(b) ローカル利用者のリアクションがリモート絵文字として記録される、の 2 つ。**既定は off** (`reactableRemoteReactionEnabled`)。CherryPick は true だが、押せなかったものが押せるようになるので誤タップでリアクションが付く挙動を全員に既定で入れない (`confirmOnReact` が off なら確認も出ない)。設定は「リアクション」の並びに置いた。**名前の分解は #2903 の `bareEmojiName` / `hasLocalEmojiWithSameName` を再利用した** — #2903 が同じ分解の 3 重複を 1 箇所に寄せた経緯があるので増やさない。**「押せる」の述語を 5 箇所で揃えた** — template の class / 関数入口の guard / `v-ripple` / 絵文字パレットへの追加 / 親の並び替え (`showAvailableReactionsFirstInNote`)。揃えないと「押せる見た目なのに押しても何も起きない」「押せるのに ripple が出ない」「押せるのに後ろへ並ぶ」が出る。**パレットに入れる文字列も揃える** — `props.reaction` のままだと `:foo@host:` が永続化され、パレットからだけリモートのキーを送る (上記 (a) の経路に落ちる)。`MkCustomEmoji` と同じ裸の形にした。ローカルのチップでは `bareEmojiName` は恒等 — 絵文字名は backend の `emojiNamePattern` (`^[a-zA-Z0-9_]+$`) で `@` を含まないため。**未ログインは押せる見た目にしない** (設定はアカウントに紐付かない localStorage なので、on にして sign out すると踏む)。**ハイライトは identity (`myReaction == reaction`) のまま。** 送る側に合わせると `:foo@remote:` と `:foo@.:` の 2 チップが同時に「リアクション済み」になり、**自分が入っていない count のチップがハイライトされる** (敵対的レビュー 2 周目で実測。1 周目にそう変えて戻した)。`.reacted` は「自分が付けたリアクション」なので identity が正しい。**帰結として、ローカルの同名で既にリアクションしている人にはリモートのチップが未リアクションに見えるのに、押すと取り消しの確認が出る** — 押せば実際に取り消されるので文言は正しく、相乗りの設計上そうなる。**判定はリモートに限らない** — `@.` を持たない `:foo:` 形式も対象になる。ただし `entity.packReactions` / `use-note-capture` が `:foo@.:` に正規化するので**実際には来ない**。防御的な実装として残してある。**絵文字一覧の更新に追随する (チップ側だけ)** — `customEmojisMap` は素の `Map` なので、computed の中で `customEmojis` を touch していないと #2698 の導線でその場でインポートしてもリロードまで押せない。親の並び替えは `watch(() => props.reactions)` の中で判定するので追随せず、**インポート直後は押せるが並びは後ろのまま**。**ロール限定の絵文字は押せる見た目になる** — 判定は `customEmojisMap.has` だけを見るので、backend が `applyReactionAcceptance` で ❤ に矯正するとリロードまで表示がずれる (ローカルチップでも同じ = 純正相当。upstream の `checkReactionPermissions` が TODO のままなので許容)。**mock では相乗りを emit しない** — 親は emit されたキーでチップを引き当てるので、押したチップと送るキーが違うと別のチップの count を動かす。mock を渡すのは `MkTutorialDialog.*` = 実利用者が通るチュートリアル。**`locales/en-US.yml` も足す** (mk-go 独自キーは fork の慣行で en-US も持つ。#2698 が前例)。**`packages/i18n/src/autogen/locale.ts` はコミットする** — 生成物だが tracked で、`make update` が tracked な状態へ戻すため、コミットしないと `make pull` 後に `make frontend-check` が型エラーで落ちる (`frontend-check` は `pnpm build-pre` を呼ばない)。**`server-plugins.generated.ts` は逆にコミットしない** — 同じ `SUBMODULE_GENERATED` だが、手元のプラグイン構成が焼き付いてそのプラグインを持たない環境でビルドが壊れる。**CherryPick 系にも同種の実装がある** (`reactableRemoteReactionEnabled`) が、`yojo-art` / `kokonect-link` のいずれも `toggleReaction` とは**別経路**で、送るのは `:foo:` (`@.` を付けない)。mk-go は通常の toggle 経路に載せ、`@.` を付ける — 前者は確認ダイアログ・楽観更新・sound を経路ごと共有するため、後者は上記の比較のため。**逐条の優劣は書かない** — 敵対的レビュー 1 周目と 2 周目で**同じ段落を 2 度間違えた** (1 周目は「確認ダイアログが無い」と書いて誤り、2 周目は fork を取り違えた)。他所のコードベース 2 つの監査を表のセルに書くのは、この表の目的 (追従時に残す / 落とすを判断する) に対して割に合わない。単体テスト 9 件 (変異 6 種で検証) と、**「設定はあるのに効かない」を止める静的ゲート** (`TestReactableRemoteReactionIsWired`、変異 14 種で検証) で固定した。既定が off なので配線が落ちても誰も気付かない — #2900 と同じ型。**ゲートは識別子の有無ではなく「接続」を名指しする** — `canReact` / `sendingReaction` という単語を探す形だと、定義を `computed(() => canToggle.value)` に戻す変異が素通りして機能が完全に死ぬ (実測)。あわせて `toggleReaction` の中に `props.reaction` と `emojiName.value` が残っていないことも見る (差し替えた箇所が 12 + 2 = 14 あり、1 つだけ戻す変異を名指しでは捕まえられない)。**純正でも動く形だが、方針として upstream へ提出しないので永久に fork 側に残る** (他の 8 行の「還元できる」= upstream 取り込み待ちの bug fix とは別。この行はリストに入れない)。 |
| `2026.9.0-mk.9a` | 相乗りの設定のラベルが長すぎるのを直す (#2697)。ラベルが 2 段で 100 文字を超えており、設定画面の他の項目 (1 行) から浮いていた。**「リアクションの相乗り」で通じる**のでそれだけにして説明を落とした。**説明を落とすと「数が合算されない」が UI から消える** — 相乗りの性質そのものだが、押せば 1 回で分かることなので設定画面で先に説明するより実際に触ってもらう側に倒した。挙動の理由はこの表の `2026.9.0-mk.9` の行に残っている。**純正へは還元できない行** (この設定自体が mk-go 固有)。 |
| `2026.9.0-mk.10` | 連合ページに配送の健全性のタブを足す (#2944)。`admin/federation/{delivery,inbox}-health` (#2461 / #2471) は実装済みで `router.go` の配線も済んでいたが、**frontend からも Playwright からも一度も呼ばれていなかった** (どちらも grep で 0 件)。集計は動いているのに「特定の相手にだけ連合できない」を調べる入口が無く、403 が返っていることも、それが相手の bot 対策なのかこちらの署名不備なのかも運営者には見えない状態だった。`/admin/federation` の `headerTabs` を「サーバー \| Deliver \| Inbox」の 3 つにする。**タブ名は連合ジョブの画面に合わせた** (`federation-job-queue.vue` も i18n を通さず `Deliver` / `Inbox` のまま。同じものを 2 つの言葉で覚えさせない)。**送信と受信で `OutcomeClass` は完全に別物** — 送信は `outcome.go` の 6 種、受信は `inbound.go` の 8 種で、**重なるものが 1 つも無い**。最初これを読まずに送信側の型で受信も扱い、正常な受信が全部赤枠になりヒントが空欄になる実装を書いた (敵対的レビューで検出)。tone は表で持ち、`unsupported` / `duplicate` は backend が「異常ではない」と明記しているので緑、`blocked` は成功率には失敗として数えられるが**意図した拒否**なので黄にする(赤にすると自分で入れたブロックが障害に見える)。**受信側は `status` を描かない** — `recordInboxTelemetry` が `Outcome.Status` を積まないので常に 0 になり、「応答なし」と出すと mk-go が応答したうえで弾いた事実と逆を指す。**`lastError` は窓に属さない** (`lastErrorTTL` が 7 日で、`Query` は窓のループの外で引く) ので、窓内の失敗が 0 のときは「期間外」と明示して色を落とす。**絞り込みと並び替えはクライアント側** — `DefaultMaxHosts` が 2048 で 1 リクエストに収まるため endpoint に `sort` / `filter` を足さない。既定の並びはサーバー側と同じ (失敗の多い順 → ホスト名順) で、**`p95 の遅い順`だけはサーバー側に無い切り口**を足した (エラーが出ていないのに遅い相手はキューの滞留要因になるが、失敗順では埋もれる)。**窓は 1 時間より長い選択肢を出さない** — `MaxWindow` が 1 時間で、超える値は `Query` が黙って丸めるため、選ばせると指定と結果が食い違う。`MkPluginSlot` はタブの外に置く (中に入れると instances 限定になり、タブを行き来するたびに unmount されてプラグイン側の状態も飛ぶ)。**純正へは還元できない行** (endpoint 自体が mk-go 固有)。 |
| `2026.9.0-mk.11` | 更新ダイアログを mk-go の版で出す (#2939)。`MkUpdated.vue` の「更新されました」は **mk-go を何度更新しても出ていなかった** — 判定が `boot/common.ts` の build 定数 `version` (= `package.json` の Misskey 版) に基づいており、upstream 追従のときしか成立しないため。fork タグは `git describe` で拾って `MkGoFrontendVersion` に渡しているだけでここには入らず、`mk.1` から `mk.9a` まで一度も出ていない。出たとしても中身は upstream のままで、「Misskeyが更新されました！」+ `2026.9.0` を表示し、「更新情報を見る」は misskey-hub の Misskey リリースノートへ飛ぶ (mk-go の変更点はそこに載っていない)。判定の基準を `instance.mkGoVersion` (mk-go のリリース版) に変えた。**`instance` は SSR 埋め込みから同期的に埋まる**ので `fetchInstance()` の完了を待つ必要が無い (ただし比較は `providedAt > cachedAt` の厳密不等なので、端末の時計がサーバーより進んでいると cache が採られ、ダイアログが 1 回分遅れる。誤検知はしない)。**frontend の fork タグ (`mkGoFrontendVersion`) では判定できない** — `compareVersions` が英字サフィックスを見ないので `mk.9a` と `mk.9` が等しい扱いになる (実測)。`mkGoVersion` は素の semver なのでその問題が無い。**localStorage は `lastMkGoVersion` を新設する** — 版体系が別なので同じキーに入れると `compareVersions('2026.9.0', '1.3.0') === 1` で **backend の差し替えを更新と誤検知する**。**保存は判定に使った側だけでなく両方を最新へ揃える** (片側だけ書くと数世代前の値が残り、戻した瞬間に誤検知する)。**どのキーをいつ書くかの判断は `resolveClientUpdate` に置いて vitest で固定した** — 呼び出し側に条件を残すと `!==` を `===` にするだけで **全利用者にページ遷移のたびにダイアログが出続ける**のに、型もテストもゲートも緑のままだった (敵対的レビューで実測)。純正 backend では `mkGoVersion` が無いので従来どおり Misskey の版で判定する。配線は `internal/server/mkgo_updated_gate_test.go` が見る (`make frontend-check` で実行)。**純正へは還元できない行** (`mkGoVersion` は mk-go 固有)。 |
| `2026.9.0-mk.12` | カスタム絵文字の登録申請の画面を足す (#2934)。絵文字の追加は `canManageCustomEmojis` を持つ人だけの操作で、**一般の利用者から頼む導線が無かった** — 運用では DM で画像を送るような外部手段に落ちており、申請の取りこぼしも経緯の記録も残らない。`/emoji-request` を新設し (設定 → その他 から導線、`canRequestCustomEmojis` を持つ人にだけ出す)、審査は `/admin/emojis` に「申請」タブとして足す (既存の絵文字と見比べる場面が多いので同じ画面に置く)。**本文中の実寸プレビューを申請側と審査側の両方に出す** — カスタム絵文字は本文では文字サイズの 1.25 倍でしか描かれないので、大きな升目だけで判断すると本文で潰れて読めないものを通してしまう。**名前の重複を申請時と審査時の両方で出す** — 承認を押してから `DUPLICATE_NAME` で落ちると、押した側にも申請者にも何も残らない。**「確認できなかった」を「重複なし」と区別する** (DB 障害を `false` に潰すと、確認できていないのに承認を押させる)。画像の解決も同じく、消えたのか確認できなかったのかを分けて出す。**却下の理由を必須にする** — 申請者に届くのはこの文面だけなので、空だと直して出し直す手がかりが無くなる。通知は mk-go 独自の枠組み (`_mkgoNotification`) に載せ、承認か却下かと却下理由を出す。**承認と却下でアイコンと色を分ける** — `.subIcon` の既定は `background: panel; color: #fff` なので、light テーマ (panel が白系) では白地に白アイコンになって何も見えない。**自己処理でも通知する** — 通知サービスは notifier == notifiee を弾くので `NotifierID` を渡さない。1 人運用のサーバーでは全件が自己処理になり、弾くと「機能が動いていない」ように見える。ロールの policy は `canRequestCustomEmojis` で、**列挙が要るのは 3 箇所** (`roles.editor.vue` のキー一覧、`roles.policy-editor.vue` のキー一覧と編集フォーム)。**純正へは還元できない行** (申請という概念自体が upstream に無い)。 |
| `2026.9.0-mk.13` | リモート絵文字のインポートを申請できるようにする (#2935)。`importRemoteEmoji` (#2698) の導線は `canManageCustomEmojis` を持つ人にしか出ておらず、**持たない人はリモートの絵文字を見ても頼む手段が無かった**。`MkCustomEmoji` と `MkReactionsViewer.reaction` のメニューを `canImport` / `canRequest` で分岐させ、権限を持つ人はその場でインポート、持たない人は申請へ送る (同じ場所を押して結末だけが違う)。**メタデータは取らない** — `admin/emoji/fetch-remote-meta` はモデレーター専用で申請者には叩けないので、入力は「どの名前で登録したいか」と「なぜ欲しいか」だけにする。結果としてカテゴリとエイリアスは空で登録され、**申請経由のほうが成果物は劣る** (承認後に絵文字管理画面で手直しする運用)。**審査画面のリモート画像は media proxy を通す** — 本番は `img-src 'self' data: blob:` を enforce しており、生の URL は黙ってブロックされて**モデレーターに画像が 1 枚も見えない**。`getProxiedImageUrl(url, 'emoji', false, true)` で同一オリジンに寄せる (allowlist が `emoji.originalUrl` / `publicUrl` を通す)。**名前は送る前に client 側でも検査する** — `emoji-application/create` は 1 時間 5 回のレート制限が掛かっていて、middleware が handler より前に走るので**失敗も数える**。検査しないと打ち間違いを 5 回やっただけで 1 時間ロックされる。**画像の読み込み失敗を「画像がありません」に落とさない** — 行は存在していて描画だけが失敗した状態なので、「もう一度読み込んでください」を出す (このファイルが `remoteGone` と `nameConflict` に対して採っているのと同じ判断)。**純正へは還元できない行** (申請という概念自体が upstream に無い)。 |
| `2026.9.0-mk.14` | プラグインをユーザーのモデレーション画面に描画できるようにする (#2915)。プラグインが描画できる位置は 4 つしかなく、**モデレーターがユーザーを裁く画面に刺さるものが無かった**。`SlotName` に `admin:user` を足し、`pages/admin-user.vue` の概要タブに置く。**`profile:info` では代用できない** — あちらは公開プロフィールなので、モデレーターにだけ見せたい材料をそちらへ出すと全員に見える。`ctx` は既存の `SlotUser` をそのまま渡し、新しい型は作らない。設置は**モデレーション操作の `FormSection` の外**にする — 中に入れると `deleteAccount` のような破壊的ボタンと視覚的に混ざり、プラグインの出力が「この画面の操作」に見える。**`SlotName` は union 型なので、宣言だけして描画先を置き忘れても `vue-tsc` は通る。**症状は「プラグインがそのスロットに出ない」だけでエラーもログも出ず、プラグイン側は自分の不具合を疑うことになる。Go 側の `wiring-check` (#2762) と同じ「宣言したのに配線していない」の形なので、`TestEveryPluginSlotHasAMountPoint` で検査する (`make frontend-check` で実行。submodule を読むので **`make gates` には入れない**、#2892)。見るのは 3 つで、**どれが欠けても同じ「出ないが緑」になる** — 宣言・`<MkPluginSlot>` の設置・**そのファイルの import**。3 つ目が要るのは `MkPluginSlot.vue` が `components/global/` ではなくグローバル登録されていないためで、import を落とすと素の要素として描画され production では警告すら出ない (vue-tsc も eslint も緑のままなのを実測)。コメントアウトして残す形と、`authoring.md` のスロット一覧との片側更新も落ちる。**純正へは還元できない行** (プラグイン機構自体が mk-go 固有)。 |
| `2026.9.0-mk.15` | 429 を受けたら追い読みの自動発火を止める (#2955)。`Paginator` の `fetchOlder` / `fetchNewer` が 429 を他のエラーと一緒に握り潰し、`canFetchOlder` を true のまま残していた。`MkPagination` は `canFetch*` が true の間ボタンを描き、`v-appear` が視界に入るたび再発火するので、**レート制限に当たると再試行が回り続ける**。**サーバー側の制限は「叩くのをやめる」まで解けない** — store が拒否したリクエストも記録するので、429 のまま叩き続けると窓が前へ押し戻され続ける (実測で `Retry-After` が 58 秒前後に張り付いたまま解けなかった)。つまり自動再試行は**自分のバケットを自分で開かないまま固定し続ける**。利用者からは「読み込みが終わらない」に見える。**429 だけを他のエラーと区別する** — ネットワーク断などは従来どおり握り潰す (一時的で再試行すれば直る)。レート制限は逆で、**再試行が状況を悪化させる唯一のケース**。判断は `resolveRateLimitStop` に閉じた (呼び出し側に条件を残すと、`canFetch*` を落とし忘れても型もテストも通る)。**`error` は使わない** — あちらは一覧をエラー表示で置き換えるので既に読めている分が消える。代わりに理由と手動の再試行を出す (落とすだけだと一覧が黙って途中で終わったように見え、「これで全部」と誤解される)。振る舞いは `test/unit/paginator-rate-limit.test.ts` が固定し、**自動追い読みを持つコンポーネントが全て理由を出すこと**を `TestAutoLoadingComponentsShowRateLimit` が見る (`make frontend-check` で実行)。**当初は Go 側で `paginator.ts` の字面を見ていたが、敵対的レビューで 11 変異中 7 件を素通りすることを実測された** — 「`canFetch*` を落とさず印だけ立てる」「向きを取り違える」という修正の本体を壊す変異が通っていた。文字列照合では振る舞いを検証できない。**純正へ還元できる行** — `Paginator` も `MkPagination` も upstream のファイルで、upstream 自身も `i/notifications` に `30s/30` の制限を持つ (`notifications.ts`) ため、**純正にも同じ不具合がある**。取り込まれた時点で revert する対象。 |
| `2026.9.0-mk.15a` | レート制限の停止を全経路で効かせる (#2955)。mk.15 の停止は主対象 (`users/following`) では効いていたが、それ以外に穴があった。**`trim()` が `canFetchOlder` を無条件に true へ戻す** ので、429 で止めた直後に 1 件でもアイテムが届くと解除される — streaming 系は `prepend` / `releaseQueue` でここを通るため、**`i/notifications` (30s/30) では通知が届くたびに復活**していた。**`rateLimited` を戻す経路が再試行ボタンしか無かった** — `init()` は `error` を戻すのに `rateLimited` は触らないので、再読み込みで正常に読めるようになっても「レート制限を超えました」が残る。**`init()` の 429 が汎用エラーに潰れていた** — `MkError` は「何かがおかしいようです」を描く。自動追い読みが止まった直後に利用者が最初にやるのは再読み込みなので**同じ窓の中でこの経路に入る確率が高く**、しかも `MkError` の再試行は `init()` をもう一度撃って窓をさらに押し戻す。**`fetchNewer` にガードが無かった** — streaming 系は `useInterval` で 10〜22 秒ごとに `canFetchNewer` を見ずに撃つので、newer 側の停止が実質無効。**理由の表示が `MkPagination` にしか無かった** — `i/notifications` は独自のボタンを持ち `MkPagination` を経由しないので、ボタンが黙って消えるだけで「これで全部」に見えていた。`MkRateLimitedNotice` に切り出して**自動追い読みを持つ 3 箇所で共有**する。**再試行の判断を `Paginator` へ移した** — コンポーネントは単体テストから駆動できないので、向きの扱いを置くと取り違えても誰も落ちない (実測で素通りした)。**再試行に冷却を置いた** — サーバー側は拒否も記録するので**押すほど窓が延びる**。**純正へ還元できる行** (mk.15 と同じ理由)。 |
| `2026.9.0-mk.15b` | 初回取得の 429 を画面に出し、別方向の成功で印を消さない (#2955)。**v-if チェーンは排他**で notice は最後の枝にしかなく、`init()` の catch は `error` も立てるので、初回取得が 429 のとき必ず `MkError` (=「何かがおかしいようです」) が選ばれ、**理由が画面に出なかった**。自走が止まった直後に人が押すもの (F5 / reload / pull-to-refresh / `MkError` の retry) はほぼ全部 `init()` なので、この経路に入りやすい。`MkError` より前に枝を足した。**ただし「一覧が空のとき」に限る** — 条件を付けずに置くと、読めている状態で 429 に当たったときこの枝が勝って**一覧ごと消える** (この修正が一度作った回帰で、変異検証で検出した)。**`retryAfterRateLimit` が init から復帰できなかった** — `fetchOlder` は `items.length === 0` で早期 return するので、初回の 429 から再試行しても**無反応で印だけ消える**。**別方向の in-flight 成功が印を消していた** — `fetchOlder` の 429 と poll の `fetchNewer` が重なると、後から返った成功が `clearRateLimit` を呼び、`rateLimited=false` かつ `canFetchOlder=false` = 「これで全部」に見える状態になる。取得の成功では解除しないようにした。**純正へ還元できる行** (mk.15 と同じ理由)。 |
| `2026.9.0-mk.15c` | 表示の枝と冷却と停止の向きを直す (#2955)。mk.15b の修正が 2 つの回帰を作っていた。**枝の条件を items の件数で見ていた**ので、streaming が 1 件 prepend した瞬間に枝から外れて `MkError` に戻る — `realtimeMode` は既定 true で init の成否と無関係にチャンネルへ繋ぐため**既定構成で起きる**。理由が消えるだけでなく、届いた 1 件も隠される。条件を `error` に変えた (`error` は init の失敗でしか立たない)。あわせて init の catch で**先に印を落としてから付け直す** — 古い 429 の印が残っているとネットワーク断の失敗でも「レート制限」と誤表示する。**再試行の冷却がコンポーネント側にあり、いちばん必要な init 経路で効いていなかった** — notice は親の v-if の枝なので、再試行で `fetching` が立つと枝が移って**アンマウントされ冷却が消える**。Paginator に移した。**newer で止めたとき older 側の自動発火が止まっていなかった**ので `fetchOlder` にも同じガードを置き、その結果「取得の成功で解除する」経路が到達不能になったので撤去した。**解除の経路は再試行と再読み込みの 2 つだけ**。**純正へ還元できる行** (mk.15 と同じ理由)。 |
| `2026.9.0-mk.16` | リモート絵文字の一覧を media proxy 経由にする (#2957)。カスタム絵文字管理 (beta) のリモート一覧が**1 件も表示されていなかった**。グリッドの image セルは `<img :src="cell.value">` に値をそのまま入れる実装で、渡していたのが相手サーバーの `publicUrl` だったため、`img-src 'self' data: blob:` を enforce している構成で全部ブロックされる (実測: 上位 5 origin で 11,004 件、すべて外部)。**`MkDataCell` は変えない** — 汎用のグリッド部品なので、呼び出し側で変換する (#2935 と同じ形)。ローカル一覧と登録画面は影響を受けない — 自サーバーが配る URL で CSP に入る (**ただし `'self'` とは限らない**。object storage 構成ではローカル絵文字 16 件中 12 件が `objectStorageBaseUrl` のオリジンで、通っているのは `cspMediaExtras` がそれを img-src に足しているから)。編集ダイアログは #2903 で既に proxy 経由で、**一覧だけが取り残されていた**。**この時点では第 4 引数 (`noFallback`) を静止画の設定と取り違えており、mk.16a で直している。** **mk-go 固有の CSP への適応**。ただし CSP が無くても生 URL は閲覧者の IP を相手 host へ渡すので (本番で remote 絵文字の host は 432、1 ページ最大 100 行)、**還元不能ではなく優先度が低い**という位置づけ。 |
| `2026.9.0-mk.16a` | リモート絵文字一覧の proxy の通し方を直す (#2957)。**`getProxiedImageUrl` の第 4 引数を取り違えていた** — `noFallback` であって静止画設定とは無関係なのに、issue・PR・doc に「第 4 引数で静止画設定を尊重する」と書いて伝播させていた。実測では GIF が 229,610 B のまま出ており、`disableShowingAnimatedImages` を on にしていてもここだけアニメーションしていた (`getStaticImageUrl` で包むと 6,304 B の webp)。**`noFallback = true` も誤り** — グリッドの image セルは `@error` の受け皿を持たないので、proxy が 403/404/500 を返すと壊れ画像アイコンと長大な proxy URL (alt) がセルに出る。実測では 80 件中 7 件が 404/500 になり、`fallback=1` 付きなら 120/120 が 200 だった。**`MkRemoteEmojiEditDialog` と同じ形に揃えた** (`MkCustomEmoji` は `@error` を持つので `noFallback` を渡す点が違う)。**インポートログ側の変換は落とした** — `it.item` はグリッドの行で `url` には既に proxy 済みの URL が入っているため。**新規流入を止めるゲートは入れていない** — 2 周の敵対的レビューで、正しい書き方を偽陽性で落とす / 守るべき行を検査対象から外す、という形を繰り返したため別途とした (#2964)。 |
| `2026.9.0-mk.16b` | proxy の通し方のコメントを実測に合わせる (#2957)。1 周目の訂正として書いた主張がまた裏取りされていなかった。**「`MkCustomEmoji` と同じ形」は偽** — あちらは `noFallback` を渡す (`@error` で `:name:` に落とす受け皿があるため)。揃っているのは `MkRemoteEmojiEditDialog` だけ。**「`getProxiedImageUrl` は冪等」も静止画では偽** — proxy 接頭辞を見て元の URL を取り出すので壊れはしないが、`getStaticImageUrl` が足した `static=1` は復元されない。 |

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

`2026.9.0-mk.3` の内訳:

| 箇所 | 変更 |
|---|---|
| `pages/about-mkgo.vue` | 新規。mk-go の説明・バックエンド / フロントエンドの版・ソースコードの案内 (**このサーバーが動かしているコード** / mk-go 本体 / フロントエンド / ライセンス) ・`/about-misskey` への導線・コントリビューター。**帰属の文章は置かない** — ソースコード欄が「フロントエンド (Misskey のフォーク)」を挙げており、AGPL が求める著作権表示は `LICENSE` と各ファイルの SPDX ヘッダーが担っている |
| `pages/about-misskey.vue` | 冒頭に `/about-mkgo` への `FormLink` を 1 つ追加。**upstream のプロジェクトメンバー・スポンサー・パトロンは消さない** (ライセンス上必須なのは `LICENSE` と著作権表示であって謝辞一覧ではないが、upstream が明示的に管理しているものなので残す) |
| `pages/about.overview.vue` | `/about-misskey` へのリンクを `/about-mkgo` に差し替え |
| `ui/_common_/common.ts` | サイドバーの「Misskeyについて」を「mk-goについて」(`/about-mkgo`) に差し替え |
| `components/MkSourceCodeAvailablePopup.vue` | AGPL の告知ポップアップの誘導先を `/about-mkgo` に差し替え |
| `router.definition.ts` | `/about-mkgo` のルート追加。**これが無いとページは 404** |
| `locales/ja-JP.yml` / `locales/en-US.yml` | `aboutMkGo` と `_aboutMkGo` (11 キー) |
| `packages/i18n/src/autogen/locale.ts` | 生成物 (`pnpm --filter i18n generate`)。**`_abuseReportForm` の JSDoc も入る** — `2026.9.0-mk.1` が生成し直していなかった分で、次に誰がビルドしても出る差分 |

**バージョンはバックエンドとフロントエンドを対で出す。** Go で書き直したのは
バックエンドだけなので、`mk-go 1.3.0 (abc1234)` と `Misskey 2026.9.0-mk.3` が
並ぶだけで構成が伝わる (それぞれ `mkGoCommit` / `mkGoFrontendVersion` を使う。
§1-1b)。

**ソースコードの案内は 3 つのリポジトリを並べる。** upstream の `about-misskey` が
「このサーバーの改変版リポジトリ / Misskey 原典」の 2 段なのに対し、mk-go では
「このサーバー (`instance.repositoryUrl`) / mk-go 本体 (サーバーサイド) /
フロントエンド (`shiroha-a/misskey-ts`)」を出し、そのうえで Misskey 原典へ繋ぐ。

- **1 つ目を省くと**、operator が mk-go をさらに改変した場合に AGPL 13 条の
  案内先が間違ったものになる
- **フロントエンドを省くと、いま表示されている画面のソースが案内から漏れる。**
  Go で書き直したのは**サーバーサイドだけ**で、画面は Misskey のフロントエンドに
  mk-go 向けの変更を載せたもの (この表の tag 一覧がその変更にあたる)。mk-go 本体の
  submodule として辿れはするが、13 条が対象にするのは「動いているコード」全体なので
  明示的に出す。ページ本文にも「フロントエンドは Misskey のものを利用している」旨を
  書いてある

**コントリビューターにアバター画像を出していない。** 新規ページなので最初から外部
画像を持たせる必要が無く、名前だけで用は足りる (#2892 で `avatars.githubusercontent.com`
は許可済みなので、出すこと自体は今は可能)。`about-misskey` 側の外部画像 62 枚は
#2892 で `img-src` に 2 origin を足して表示できるようにした (upstream の謝辞を残す
判断と、それが表示されない状態は両立しないため)。

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
| `system` | `maintenance` (cron 群: chart tick/resync/clean, checkExpiredMutings, clean, cleanRemoteNotes, checkModeratorsActivity, instanceRefresh, retentionAggregate, chunkedUploadGc, orphanUserCleanup, orphanAttachmentCleanup) |
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

**プラグイン専用の `plugin:<名前>` キュー (#2818) は意図的にこの規則の外**。名前が構成で変わるので静的な定数には載せられず、**タブとしては出ない**。概要タブのカード (`admin/queue/queues` = 実際に worker が見ているキュー) から辿る。一時停止・再開は API 側が接頭辞で受け付けるので動く。

## 5. 運用・性能機能 (mk-go 独自)

| 項目 | 内容 |
|---|---|
| Redis timeline / antenna の宙吊り ID 除去 | 読み取り時に解決できなかった ID を Redis から取り除く (timeline は #2715 / PR #2718、antenna は #2719)。**upstream は取り除かない** — timeline (`FanoutTimelineEndpointService`) も antenna (`server/api/endpoints/antennas/notes.ts`、こちらは `FanoutTimelineEndpointService` を通らず生の `note.id IN (...)`) も、Redis から取った ID を hydrate して引けなかったぶんを黙って落とすだけ。`FanoutTimelineService.remove` の呼び出し元は `antennas/remove-note` (ユーザー操作) しか無い。**antenna で効く理由は DB fallback が無いこと。** timeline は件数不足時に DB へ fallback するが (`meta.enableFanoutTimelineDbFallback` を off にすると止まる、§5.6)、antenna の読み取りは Redis の ID だけで完結する。押し出し自体は両方にある (antenna は `pushNote` が毎回 `ZRemRangeByRank`、timeline は 10% 確率の `LTrim`) が、**マッチが止まった antenna では新着が積まれないので宙吊り ID が残り続ける**。読み取り窓 (`limit*2`) が全部宙吊りだとページが空で返り、クライアントは次の `untilId` を得られず行き止まりになる。解消は 1 リクエストあたり窓 1 つぶん。**除去は filter を掛ける前の集合で判定する** — visibility / mute / block で落ちた note は生きているため。**消す前に primary で存在を確かめる** (`ExistingNoteIDsOnPrimary`、`dbresolver.Write` で primary 固定) — mk-go はリードレプリカを対応しており (`dbReplications`、既定 `false`)、複製前の行は通常の SELECT で引けない。ID に埋め込まれた時刻での猶予判定は使えない: リモート note の ID は AP の `published` から発番されるため「たった今 INSERT されたが ID の時刻は数時間前」が普通に起きる。**timeline 側も同じ確認を通す** (#2757)。DB fallback があるから安全とは言えない — fallback は Hybrid 以外では別メソッドで、`allowPartial: true` を渡すクライアントには走らず、`enableFanoutTimelineDbFallback` を off にすれば運用側でも止まり (global を除く、§5.6)、Redis list から消えた ID は戻らない |
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

## 5.6. timeline の DB fallback を止めるつまみ

`meta.enableFanoutTimelineDbFallback` (admin から切り替え、既定 **on**) は
**FTT を止めずに、タイムライン取得の DB fallback だけを止める**つまみ。#2762 まで
mk-go は列を持つだけで読み取り経路から参照しておらず、off にしても何も起きなかった。

`enableFanoutTimeline` とは別物。あちらは push (fanout) と read の両方を止めて
DB 直行にする。**両方 off でも DB は引く** — upstream も
`if (!enableFanoutTimeline) return getFromDb()` を endpoint 側に持ち、そこでは
`useDbFallback` を見ない。

**off は珍しい状態ではない。** 同梱 frontend のセットアップウィザード
(`MkServerSetupWizard.vue`) は用途に「1 人用」以外を選ぶと
`enableFanoutTimelineDbFallback: false` を送る (`q_use === 'single'` が条件)。
**group / open で立てたインスタンスは、誰も設定を触っていなくても off**。

### off にすると何が起きるか

upstream の `FanoutTimelineEndpointService` は `useDbFallback` が偽のとき
`ps.dbFallback` を `() => Promise.resolve([])` に差し替える。mk-go も同じで、
`limit` に満たなくてもそのまま返す。

| 状況 | on (既定) | off |
|---|---|---|
| Redis に `limit` 以上ある | Redis から返す | 同じ |
| Redis の持ち分が `limit` 未満 | 足りない分を DB で継ぎ足す | **Redis の分だけ返す** |
| Redis が空 | 全ページを DB から返す | **空を返す** |
| `sinceId` を含むページング | 全ページを DB が処理する | **空を返す** |

最後の行が実際に効く。`sinceId` を含むページングは Redis に十分な ID があっても
必ず DB へ倒れる (`shouldFallbackToDb` が常に真、#2720 で upstream に合わせた)。
frontend の paginator は `fetchNewer` で `sinceId` を投げる
(`packages/frontend/src/utility/paginator.ts`) ので、**off にすると「新しい投稿を
読み込む」操作が空を返す**。timeline JSON cache は cursor 無しのページだけが
対象なので、この経路には効かない。

実際に効くのは **`realtimeMode` を off にした利用者**。
`MkStreamingNotesTimeline.vue` は非 realtime のとき `useInterval` と
`notePosted` イベントから `fetchNewer` を呼ぶので、これが毎回空になり
**新着 (自分の投稿を含む) が一切追加されなくなる**。既定は
`realtimeMode: true` (`packages/frontend/src/store.ts`) で、そちらは streaming が
前置きするため影響は小さい。

負荷を落とすためのつまみであって、無害な最適化ではない。**DB の負荷が実際に
問題になっているときだけ切ること。**

### 「DB を触らなくなる」わけではない

止まるのは fallback クエリだけ。経路によって残るものが違う。

| 経路 | off でも走る DB アクセス |
|---|---|
| 継ぎ足し (Redis に持ち分があり `limit` 未満) | Redis の ID を note に引き直す hydrate (`FindManyByIDsWithUser`) と、解決できなかった ID の primary 確認 (`ExistingNoteIDsOnPrimary`、§5 の宙吊り ID 除去) |
| 全ページ (Redis 空 / `sinceId` 付き) | timeline service は DB を引かない |

ただし**どちらの経路でも handler 側の loader は毎リクエスト走る**。
`internal/api/notes/timeline_handler.go` は service を呼ぶ前に mute / block /
following / channel を読み、いずれもキャッシュを挟んでいない。数え方は
「handler が発行する repository 呼び出しの本数」:

- home / hybrid: **7 本** (フォロー中チャンネルがあれば `loadFollowedChannelIDs`
  が内部で `loadMutedChannelIDs` を呼び直すので 8 本)
- local / global: **5 本**
- service から戻ったあと `applyMuteBlock` → `notesfilter.LoadMuteBlockSets` が
  muting / blocking / channelMuting / user_profile を**もう一度** 4 本読む

いずれもログイン時の数。匿名 viewer では各 loader が nil を返して 0 本になる。
cursor 付きのページは JSON cache も効かないので、off にしても「timeline が DB を
触らなくなる」とは言えない。

### 件数の埋め方が upstream と違う (off で顕在化する)

upstream は Redis list を `lrange 0 -1` で**丸ごと**取り (`FanoutTimelineService.getMulti`)、
`limit` 件が埋まるまで**窓の奥へ読み進めながら** hydrate を繰り返す
(`FanoutTimelineEndpointService.getMiNotes` の while ループ)。mute や解決不能で
落ちたぶんは、その先の ID を追加で読んで埋める。**この再読み込みは
`useDbFallback` では止まらない。**

mk-go にこのループは無い。`filterAndSort` が窓を `limit` 件に切り、hydrate と
filter を 1 回通すだけ (4 経路とも `Get` / `GetMerged` / `GetMulti` から共通で
呼ばれる)。足りなければ DB へ継ぎ足す設計になっている。

on のときは差が見えない (どちらも `limit` 件を返す)。**off にすると upstream の
ほうが件数が揃いやすい** — 窓に生きた ID が残っていれば upstream は埋めるが、
mk-go は先頭 `limit` 件から落ちたぶんをそのまま返す。#2762 でこのつまみが
効くようになったことで観測可能になった差で、つまみ自体が作ったものではない。

### 対象になる timeline

| timeline | upstream | mk-go |
|---|---|---|
| home (`notes/timeline`) | 対象 | 対象 |
| `local-timeline` | 対象 | 対象 |
| `hybrid-timeline` | 対象 | 対象 |
| `global-timeline` | 対象外 (fanout を使わず常に DB) | **対象外** (下記) |
| `user-list-timeline` | 対象 | 対象外 (常に DB) |
| `channels/timeline` / `users/notes` / AP outbox | 対象外 (`useDbFallback: true` 固定) | 対象外 (Service を通らない) |
| `antennas/notes` / `roles/notes` | 対象外 (`FanoutTimelineEndpointService` を通らない) | 対象外 |

`user-list-timeline` だけが逆向きの差。mk-go は list メンバーの visibility を
SQL に push-down している (#1452) ため、Redis の ID から組み立てる経路を持たない。

**`global-timeline` は mk-go では fanout 経路だが、意図的に gate していない**
(#2762)。mk-go が GTL を fanout に載せているのは性能上の拡張であって、upstream
由来のつまみの意味論を変える理由にはならない。gate すると、上記のとおり group /
open で立てた**既定 off のインスタンス**で、誰も設定を触っていないのに GTL が
Redis list の窓 (global は `MaxTimelineLength` 固定の 200 件で、meta では変えられ
ない) を超えて遡れなくなる — `untilId` で窓の外を要求した
時点で Redis が空になり、fallback も止まるため。upstream の同設定のインスタンスは
GTL が無傷なので、この食い違いは mk-go 側の退行として出る。

## 6. セキュリティ関連の差分

| 項目 | upstream | mk-go |
|---|---|---|
| antenna の未読 (`hasUnreadAntenna`) | **機能ごと止まっている。** `UserEntityService.getHasUnreadAntenna` は実装がコメントアウトされ `return false; // TODO` | 実際に `antenna_note_unread` を引いて算出する。**mk-go の方が実装している側** (#2406)。あわせて antenna timeline の閲覧で未読行を消す。upstream は行を作らないので既読化も要らないが、mk-go は自前で持つ必要がある |
| shiki (コードブロックの syntax highlight) の配信元 | `esm.sh` から動的 import (`vite.config.ts` の `externalPackages`) | **同じ** (バンドルに切り替えない)。CSP の `script-src` に `https://esm.sh` を明示的に許可している。**バンドルすると 30 ロケール分が複製されてビルド成果物が 242MB → 508MB に倍増する**ため (2026-08-09 実測。JS が 13,576 → 23,610 ファイル)。利用者 1 人あたりの転送量は変わらないが、軽量さを損なう。代償として、コードブロックを表示する閲覧者の IP とリファラが esm.sh に渡る。言語を絞ってバンドルすれば両立できる可能性はある (未検証) |
| frontend HTML の CSP | **無し** | `frontendContentSecurityPolicy` で opt-in (既定 `off`)。**mk-go 独自の硬化** (#2425)。段階導入のため `report-only` から始め、違反を潰してから `enforce` へ切り替える運用で、**この運用サーバーは既に `enforce`** (`deploy/uds/config/default.yml`)。Playwright も `enforce` で回して実ブラウザのゲートにしている (#2788)。**script 側の `'unsafe-inline'` は #2786 で外した** — SPA shell の inline script (`VERSION` / `CLIENT_ENTRY` の定義と bootloader) は内容が起動時に固定なので、SHA-256 hash を `script-src` に足して通す。hash は HTML に埋める文字列そのものから導くので、片方だけ変えて壊れることはない (`internal/server/frontend.go` の `bootGlobals`)。**style 側は残す** — Vue の `:style` バインディングが 146 箇所あり DOM の inline `style` 属性になる。属性は `style-src-attr` の管轄で hash では救えず (`'unsafe-hashes'` が要る)、外すと UI が広範に壊れる。`frame-ancestors` は含めない — `X-Frame-Options` 側が `/embed/` の除外を持っており、二重管理を避けるため。**`/embed/` にも同じ CSP を付ける** (#2789) — embed は `X-Frame-Options` の除外対象 = 第三者のページに iframe で埋め込まれる唯一の経路で、script が注入されると埋め込み先ではなく**こちらの origin** で動く。captcha の origin だけは足さない (embed はサインアップ経路を持たない) |
| `X-Content-Type-Options` | **明示的には設定しない** (`packages/backend/src/` に `nosniff` を付ける箇所が無い。`built/` の hit は `@fastify/static` の依存で、static route の 404 / 301 応答にだけ出る) | 全応答に `nosniff` を付ける。**mk-go 独自の硬化** (#2782)。#2782 まで drive のファイル配信 (`internal/server/files.go`) とプラグイン proxy (`plugin_wiring.go`) にしか付いておらず、SPA shell も API も素通しだった。MIME sniffing を許すと、こちらが `text/plain` のつもりで返したものが `text/html` として解釈され、埋め込まれた script が origin 上で動く。除外は設けていない — `nosniff` が壊すのは「`Content-Type` が間違っているのに推測で救われていた」応答だけで、それは直すべき側 |
| `Permissions-Policy` | **設定しない** | `microphone=(), geolocation=(), payment=()` を全応答に付ける。**mk-go 独自の硬化** (#2782)。fork frontend の**ソース・`node_modules`・ビルド成果物**を grep して、いずれも使っていないことを確認したうえで落としている。**`camera` / `fullscreen` / `display-capture` は落とさない** — `camera` は `/qr` の読み取りタブが使う (`pages/qr.read.vue` の `chooseCamera` / `QrScanner.listCameras`)。**`getUserMedia` で grep すると 0 件になる** — 実際に呼ぶのは `qr-scanner` パッケージなので、API 名だけで判定せず `node_modules` とビルド成果物も見ること。`fullscreen` は `MkUrlPreview` / `MkYouTubePlayer` の iframe が `allow` に載せ、`useNativeUiForVideoAudioPlayer` ではネイティブ `<video controls>` の全画面ボタンが使う。`display-capture` は Sentry のフィードバック用スクリーンショットが `getDisplayMedia` を呼ぶ。プラグインが frontend に .vue を注入できるので、ここに挙げた機能を使うプラグインが出たらこの値を緩める必要がある |
| `Referrer-Policy` | **設定しない** (`packages/backend/src/server/` に 1 件も無い) | 全応答に `strict-origin-when-cross-origin` を付ける。**mk-go 独自の硬化** (#2404)。無いとノート本文の外部リンクを踏んだ際に閲覧中の URL が path ごと Referer として送られる。Misskey の URL は `/notes/<id>` / `/@user` のように**何を見ていたかがそのまま分かる**形なので、遷移先に閲覧内容が漏れる。`no-referrer` まで強めないのは、cross-origin へ origin だけは送る方が連合先からの流入把握や hotlink 判定を壊さないため |
| identicon の CSP | 付けない | `default-src 'none'; style-src 'unsafe-inline'` を付ける。**mk-go 独自の硬化** (#2404)。upstream が他のアセット route (`/emoji` / `/twemoji` / `/fluent-emoji` / `/files`) に付けているものと同じ値で揃えた。identicon は mk-go が実際に PNG バイトを返す route なので、他と扱いを分ける理由が無い |
| account password のハッシュ強度 | 全経路で bcrypt cost 8 固定 (`bcrypt.genSalt(8)`)。設定不可 | 既定 cost 10 で、`bcryptCost` で 4-31 に変更できる。**さらにログイン成功時に古い強度のハッシュを焼き直す** ので、設定を上げれば既存の利用者も戻ってきた順に移行する。upstream にこの仕組みは無い。cost は `$2a$NN$` に埋まるので、上げても drop-in で TS 側が検証できる |
| account password の受理形式 | bcrypt のみ | bcrypt に加えて **CherryPick 由来の Argon2id を受理する** (#2838)。prefix で dispatch し、受理するのは CherryPick の固定 profile (`v=19` / `m=65536,t=3,p=4` / salt 16 byte / digest 32 byte) だけ。それ以外は fail closed で、`v=` とパラメータだけを warn に出す (salt / digest は出さない、#2849)。**新しい hash は bcrypt しか作らない** ので、signin を 1 度通した account から順に bcrypt へ移行する。検証は 64 MiB を確保するため最大 4 並行に制限し、枠が取れなければ 403 ではなく 503 + `Retry-After` を返す。**ただし passkey で認証できる利用者は 503 にしない** (#2853) — `usePasswordLessLogin` + 2FA 有効で credential を提示している (かつ token を送っていない) signin-flow の要求だけは、枠が取れなくても passkey の検証へ落とす。passkey を持っているのにパスワード検証の輻輳でログインできないのを防ぐため。検証自体は飛ばさないので移行は従来どおり動く。**救うのは step 3 だけ** — challenge を取りに行く step 2 (credential 無し) と、token を添えた要求は 503 のまま。password を一切通さずに入るには入力ページの passkey ボタン (`/api/signin-with-passkey`) を使う。**受理するのは `/api/signin` / `/api/signin-flow` / `/api/i/change-password` の 3 経路だけ**で、他の password 確認 endpoint (2FA 管理 / delete-account / update-email / move 等 8 箇所) は bcrypt のみ。未移行の account はそれらを使えず、72 byte 超や passkey 専用の account は移行が発火しないので恒久的に使えない。upstream Misskey TS は Argon2id を持たないので、**移行後の bcrypt は TS 側でも検証できるが、未移行の Argon2id は検証できない**。逆方向 (mk-go → CherryPick) は非対応 — CherryPick 側の password 確認 endpoint は `argon2.verify` のみで bcrypt を受けないため、signin は通っても他が使えなくなる |
| native session token の強度 | `secureRndstr(16)` = 62 文字集合 16 文字 (約 95 bit) | **同等** (英数字 62 文字集合 16 文字)。長さ 16 は TS の `isNativeUserToken` が長さだけで native / app token を判別するため動かせない。かつて 16 進 16 文字 (64 bit) で upstream より弱かったのを揃えた |
| webhook 本文の完全性 | 共有秘密を `X-Misskey-Hook-Secret` に**平文で載せるだけ**。受信側は本文が改ざんされていないかを確認できない | 同ヘッダは互換のためそのまま送りつつ、`X-Hub-Signature-256: sha256=<hex>` (HMAC-SHA256(secret, body)) を**追加**する。**mk-go 独自の硬化**。未知のヘッダは無視されるだけなので既存の受信側は影響を受けない。秘密が空なら署名しない (空鍵の HMAC は誰でも作れるため) |
| 受信 activity の再投函 | **無し。** ハンドラの冪等性で二重配送を吸収する設計 | 署名検証を通った activity の id を短命 (Date の窓 * 2 + 余裕) に覚えて、同じものの再投函を落とす。**mk-go 独自の硬化**。冪等性は二重配送を吸収できても「古い Undo(Follow) / Undo(Block) を後から差し込む」形は吸収できない。覚えるのは**処理が成功してから**で、先に覚えるとキューの再試行を自分で捨ててしまう。guard 障害では落とさない (fail-open)。id を信用するのは authorizeActor が id の host と actor の host の一致を確かめた後だけ — 未署名経路で覚えると他人の id を先に登録して本物を落とせる |
| HSTS | `config.url` が https かつ `disableHsts` が偽なら `strict-transport-security: max-age=15552000; preload` | **同じ値・同じ条件**。かつて mk-go は `disableHsts` を設定として読んでいたのに header を出しておらず、TS から切り替えると HSTS が黙って消えていた (parity 修正)。`includeSubDomains` を足さないのも upstream に合わせている — 同じドメインの別サブドメインを平文で運用している構成を切替の瞬間に壊さないため |
| `Cross-Origin-Opener-Policy` | テスト専用の `enableCrossOriginIsolation` を立てたときだけ `same-origin` | `crossOriginOpenerPolicy` で opt-in (既定 `off`)。**mk-go 独自の硬化**。既定を off にしてあるのは、外部アプリが認証ページをポップアップで開いて閉じるのを待つ形の連携を切りうるため。MiAuth / OAuth は callbackUrl で完結するので通常は問題にならないが、切れたときの症状 (「認証したのにアプリが気づかない」) から原因に辿り着きにくい |
| `Cross-Origin-Resource-Policy` | 付けない | **付けない (意図的)**。`/files/` に付けると、他インスタンスのブラウザがドライブの画像を直接読む構成で表示が壊れる。Misskey は既定でメディアプロキシ (サーバー側取得) を通すので CORP の対象外だが、フロントが生 URL を使う経路が残っており、壊れ方が「一部の画像だけ出ない」形になって切り分けが難しい。得られる保護に対して代償が大きいので入れない |
| `users/following` / `users/followers` の CORS | `/api` 全体に `origin: '*'` (`ApiServerService.ts` が fastify-cors を `/api` に登録) | **この 2 つだけ CORS ヘッダを出さない (意図的)**。フォロー一覧を一括で抜いて CSV 化し、インポートに食わせる収集を、越境のブラウザから行えなくする (#2953)。対象は POST + JSON で**プリフライトが必須**なので、ヘッダを出さなければブラウザは実リクエストに到達しない。**リクエストは拒否しない** — ヘッダを出さないだけ。CORS が無い時点でブラウザは止まるので拒否しても挙動は変わらず、Origin を付けてくる非ブラウザのクライアントを壊す側にだけ倒れる。**同一オリジンと非ブラウザは無影響** — ブラウザは同一オリジンに CORS 検査を適用せず (同梱フロントの叩き先は `window.location.origin + '/api'`)、ネイティブアプリや連合は応答ヘッダを見ない。**Origin の値は見ない** — 同一オリジンの POST にもブラウザは Origin を付けるので「Origin があれば越境」は誤りで、`url` と突き合わせる形は逆プロキシや別ドメイン運用で壊れる。**壁ではない** — サーバー側プロキシを 1 つ挟めば CORS は無関係になる。狙う費用を上げる措置で、レート制限と対になっている (`TestCORS_NoCORSPathsMatchRateLimitedEndpoints` が片側更新を落とす)。越境 Web クライアントの実在は 1 サーバーで実測し、Referer 由来では該当ゼロだった (直近 168 時間の `/api/` POST 16,407 件のうち他ドメイン由来 0)。**ただし `Referrer-Policy` 次第で越境でも `-` に落ちるので確定ではない** — nginx の `log_format` に `$http_origin` を足して測り直す。**AP のコレクション (`/users/<id>/following`) も同じ理由で CORS を出さない** — `/api` の外にあり同じグラフを返すので、片方だけ塞いでも意味がない。連合はサーバー間なので応答ヘッダを見ず、影響を受けない |
| `users/following` / `users/followers` のレート制限 | **無し** (`users/following.ts` / `followers.ts` の `meta` に `limit` が無い) | **1 分 30 回** (#2953)。未認証で全件を引けるので収集の速度に上限を置く。**窓を長くしない** — store は拒否したリクエストも記録するので、429 を無視して叩き続けるクライアントは `Retry-After` を 1 窓ぶんに押し戻し続ける。1 時間窓だと行儀の悪いタブ 1 つで共有 IP の配下が 1 時間閲覧不能になる。閲覧系の前例 (`i/notifications` の 30s/30、`roles/assignment-show` の 1m/60) と同じ短い窓に揃えた。**30 は人間のスクロールを大きく上回る** — フロントは初回 20 行・以降 30 行 (`SECOND_FETCH_LIMIT`) なので、毎秒 15 行を 1 分読み続ける速度に相当する。**未認証だけに絞らない** — オープン登録では捨てアカウント 1 個で迂回でき、IP のローテートより安いので、絞ると防御が弱くなる |
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
| media proxy の EXIF 回転 | `sharpBmp` に `autoOrient` を渡さず `.rotate()` も呼ばないので**向きを無視する** (sharp 0.35.4 の既定は `autoOrient: false`) | **`imaging.AutoOrientation(true)` で適用する** (#2925)。向き情報を持つ写真が横倒しで出るより正立で出る方が利用者の意図に近い。upstream の `test/resources/rotate.jpg` では平均絶対差 127.40・画素の 50.5% が 32 以上ずれる (縦横が入れ替わるため)。**インターレース truecolor PNG だけは例外** — stdlib へ回すので向きが適用されない |
| `users/get-frequently-replied-users` の集計 | 直近 1000 件の返信を引き、**返信先ノートの id 集合**を作って引き直すので、同じノートへ何度返信しても **1** と数える | **`COUNT(*)` で数える**ので同じノートへ 5 回返信すれば **5**。`weight = count / peak` の順位が変わりうる。窓の取り方 (直近 1000 件・自己返信込み) は upstream に揃えてある (#2877) |
| `i/revoke-token` を凍結アカウントが叩く | **204 で失効できる。** upstream の `isSuspended` 判定は `ApiCallService` の `requireCredential \|\| requireModerator \|\| requireAdmin` ブロックの中にあり、この endpoint は 2026.9.0 でそのどれも宣言しなくなった (アクセストークン自身を失効させるため)。`AuthenticateService` にも suspended チェックは無い | **403 `YOUR_ACCOUNT_SUSPENDED`。** mk-go は `Authenticate` が凍結ユーザーを anonymous に落とす構造 (#1559) なので、分岐を置かないと 401 `CREDENTIAL_REQUIRED` になり upstream の 204 からさらに遠のく。403 のほうが「凍結ゆえに拒否した」ことが伝わるので採った (#2877) |
| `invite/delete` の存在しない ID | `NO_SUCH_INVITE_CODE` (400) | **204 を返す** (= idempotent)。取り消しは「無くなっていること」が目的なので、既に無い状態を失敗にしない。ただし **DB 障害は 204 に潰さず 500 を返す** (#2812) — 取り消し系で 204 を返すと、消えたと思って戻ったあとも ticket が生きている |
| AID/AIDXの上限外timestamp | AIDは8桁を超えて固定長を外れ、AIDXは下位8桁へwrapする | **base36 8桁の最大値へ飽和する。** 固定長を維持し、時系列順序の逆転を防ぐ安全側乖離 (#2672) |
| リモート actor の `movedTo` 消滅 | `movedToUri: person.movedTo ?? null` で null に戻す | **既存値を温存する** (削除は追わない)。一時的な欠落でクリアすると、次の取得が「無→有」の遷移に見えて `movedAt` が打ち直され、移行の時間窓 (2h / 14 日) の基準が壊れるため。移行の取り消しに追従できない代わりに基準が安定する (#2412) |
| リモート actor の `vcard:Address` の長さ | truncate せずそのまま保存 | **128 文字 (rune) で切る**。`user_profile.location` は varchar(128) で、超過値を渡すと insert / update ごと失敗し、同じ書き込みに乗っている `description` まで巻き添えになる (create 経路では profile 行が 1 行も作られず、以後の refresh も同じ失敗を繰り返す)。description の 2048 文字 truncate と同じ扱い (#2661) |
| リモート actor の profile `fields` の件数 | `analyzeAttachments` に上限なし | **16 件で打ち切る**。ローカルの `i/update` が `maxItems: 16` なので揃える。上限が無いと任意件数を送り込める (#2661) |
| リモート actor の `vcard:Address` / profile `fields` の空白 | trim も空排除もせず保存 | **trim して空なら NULL / entry ごと落とす**。ローカルの `i/update` と同形の正規化 (#2661) |
| リモート actor の profile 由来文字列に含まれる NUL | **未処理** (upstream も同じ理由で書き込みが失敗する) | **除去する**。PostgreSQL の text は NUL を受け付けず (SQLSTATE 22021 `invalid byte sequence for encoding "UTF8": 0x00`)、jsonb も拒否する (22P05)。**SQLSTATE は protocol mode で変わる** — 本番の `internal/db` は pgx の extended protocol なので 22021、`internal/testutil` は `PreferSimpleProtocol: true` なので同じ入力が 08P01 (`invalid message format`) になる。運用ログを grep するときは 22021 の方。同じ書き込みに乗っている他の列まで巻き添えになり、create 経路では `user_profile` 行が 1 行も作られない (以後の refresh も同じ失敗を繰り返す)。対象は `vcard:Address` / profile `fields` の name・value / `description` (`_misskey_summary` は `mfm.FromHTML` を通らない)。`user.name` も同様に除去する。`user.avatarUrl` / `user.bannerUrl` は**除去せず値ごと捨てる** (NUL を抜いた URL は別物なので取りに行っても無駄)。`user.tags` / `note.tags` は**正規化後に NUL を含む tag を落とす** (varchar(128)[] は NUL を受け付けない)。`user.emojis` / `note.emojis` は **#2726 で `upsertEmojis` が `emoji.name` を見るようにして塞いだ** — 判定は batch SELECT (`FindManyByNamesAndHost`) の**前**にあり、長すぎる / NUL 入りの名前はその tag だけ落ちる。それ以前は、長さ超過なら insert が落ちて 1 件除外で済むのに対し、**NUL の場合は batch SELECT が先に 22021 で落ちて `return` する**ので、その actor / Note の絵文字が全滅していた (actor 自体は作られる) (#2662)。`preferredUsername` は NUL を含む時点で不正なので除去ではなく actor ごと reject する (#2662) (#2661) |
| リモート actor の `name` の長さ | `truncate(person.name, 128)` (`stringz.substring`) | **128 rune で切る**。切らないと `user.name` (varchar(128)) への書き込みが SQLSTATE 22001 で落ち、actor がまったく作られない。upstream は書記素クラスタ単位の `stringz` なので境界がずれうるが、PostgreSQL の varchar はコードポイントで数えるため rune 単位のほうが上限に忠実 (#2662) |
| リモート Note の `cw` / `text` の長さ | truncate せずそのまま保存 | **512 rune で切る** (`note.cw` は varchar(512))。CW は**相手が自由に決められる値**で、長さの制限は送信側の実装次第 (upstream Misskey 自身は投稿時に 100 で弾くが、AP でそれを強制する仕組みは無い)。溢れると `noteRepo.Create` / Update 経路の `UpdateFields` ごと落ちて `ingestNoteWithCreated` が error を返す。生の DB error なので `isPermanentSkipError` に当たらず、トップレベル配送なら**どのハンドラでも inbox job が retry を使い切って dead になる** (既定 8 回。`defaultInboxJobMaxAttempts`、`internal/server/queue_factory.go`。Collection に包まれていれば `handleCollection` が握るので ack)。`text` は列が text 型なので長さは効かないが、NUL の除去は同じ経路で行う (#2723) |
| リモート actor の `uri` / `host` の長さ | 検証なし | **収まらなければ actor ごと拒否する** (`uri` は varchar(512)、`host` は varchar(128))。身元そのものなので切ると別人になり、捨てると lookup の鍵が無くなる。upstream は `uri` に `person.id` をそのまま入れるので同じ 22001 で失敗しうる。**gate は create 経路にしかない** (`refreshActor` は既存行専用で `uri` / `host` を書かない)。**拒否は多くの場合、署名検証の時点で起きる** — worker は `verifyPayload` で署名者を `ResolveActor` するので、署名者本人が該当するならその inbox job はそこで ack される (ハンドラまで届かない)。署名者以外 (第三者著者の note、引用先、featured のピン先など) を解決して踏んだ場合の結末は呼び出し元次第で、多くは**その値だけ黙って落ちて activity は成功する**。いずれにせよ**その actor から activity が来るたびに 1 回 fetch する状態は続く** (行が作られない以上 `lastFetchedAt` を進める先が無い) (#2723) |
| リモート actor の `inbox` / `sharedInbox` / `featured` / `movedToUri` の長さ | 検証なし | **収まらなければ値ごと捨てて actor は取り込む** (いずれも varchar(512))。icon / banner URL (#2662) と同じ判断。`inbox` を捨てるとその actor への配送はできなくなるが、表示や mention の解決は生きる。**create 側が落ちれば actor が 1 行も作られず、refresh 側が落ちれば `lastFetchedAt` を含む UPDATE ごと失敗して inbound activity 1 件につき outbound fetch が 1 回走り続ける** (#2723) |
| リモート Note / Announce の `id` の長さ | 検証なし (`uri` に生値を入れる) | **収まらなければ document ごと拒否する** (`note.uri` は varchar(512))。切ると別の note を指す URI になり、**同じ activity の重複検出** (`FindByURI`) の鍵も壊れる (Undo(Announce) はこの URI を引かない — `ListRenotesOf` で announcer の renote を探す)。**拒否したあとの結末は呼び出し元で決まる**。ack して drop されるものも、inbox job が dead になるものも、その値だけ黙って落ちて activity 自体は成功するものもある。**一般化しないこと** — 一覧は `ingestNoteWithCreated` の gate のコメントに 1 箇所だけ置いてある (ここに再掲すると片側が古くなる。実際 #2723 では 5 周にわたってどこかがずれた)。gate の利得は原因が 22001 ではなく明示的な拒否として残ること (#2723) |
| リモート actor の `alsoKnownAs` に含まれる NUL | 未処理 (upstream も同じ理由で書き込みが失敗する) | **要素ごと落とす** (`user.alsoKnownAs` は text 列なので長さは効かないが NUL は 22021)。1 要素混ざっただけで actor の INSERT / refresh の UPDATE がまるごと失われる。切らずに捨てるのは、切った URI が移行の認可 (`alsoKnownAsContains`) の一致判定に使えないため (#2723) |
| リモート添付の `type` / `thumbnailUrl` / `blurhash` / `url` の長さ | `type` (sniff) / `thumbnailUrl` / `blurhash` はローカルで決まるので AP の申告値は入らない。**`url` / `uri` は違う** — `cacheRemoteFiles` off (既定) の isLink 経路では `image.url` を生で入れており、列も同じ varchar(1024) なので **upstream も同じ 22001 に晒される** | **列に合わせて扱いを分ける** (#2723)。`url` (varchar(1024) NOT NULL) は実体そのものなので**入らなければその添付を諦める**、`type` (128) は切ると別の MIME type になるので `application/octet-stream` に倒す、`thumbnailUrl` (512) / `blurhash` (128) は表示の補助なので値ごと捨てる。upstream は添付 1 件の失敗で Note ごと落とす (`ApNoteService`) ので、mk-go は元から安全側 |
| リモートインスタンスの nodeinfo の text field | 長さは無検査。ただし値そのものは正規化する — `softwareName` は `.toLowerCase()` (string でなければ `'?'`)、`themeColor` は `tinycolor` で検証して `#rrggbb` に正規化 (不正なら `null`) | **各列の上限 (`softwareName` 64 / `softwareVersion` 64 / `name` 256 / `description` 4096) で切り、NUL を除去する**。mk-go は元から `iconUrl` / `faviconUrl` だけ長さを見ていたが、**同じ `fields` map に載る**これらが無検査だと 1 列溢れただけで UPDATE 全体が落ち、当のガードの目的が同じ関数の中で破られる (#2723)。**`softwareName` / `themeColor` の値の正規化は #2726 で upstream に揃えた** — `softwareName` は lowercase + `'?'` (**JSON の `null` は JS で falsy なので `'?'` も書かない**。upstream の `if (info)` と同じ。**「object ではないから」ではない** — `[]` / `123` は JSON の型としては array / number で object ではないが、truthy なので `'?'` が入る (#2730)。ただし **`strings.ToLower` は JS の `toLowerCase()` と完全には一致しない**。go1.26.6 (`unicode.Version` 15.0.0) と node 22 (Unicode 17.0) で全符号位置を突き合わせた実測では 3 系統: (i) `İ` (U+0130) は両方小文字化するが結果が違う (Go `i` / JS `i` + U+0307)。**ASCII に落ちる差はこれだけ**、(ii) Unicode 版差 55 符号位置 (`Ɤ` U+A7CB など) は JS だけが小文字化する (Go の table が上がれば消える)、(iii) 文脈依存の final sigma は符号位置ごとの比較では見えない (`MISSKEΣ` → Go `misskeσ` / JS `misskeς`)。いずれも software name には現実に出ず、software block の判定 (`MatchSuspendedSoftware`) も両側を lowercase して比べるので回避には使えない)、`themeColor` は tinycolor 互換の parser (`internal/misc/csscolor`) で検証して `#rrggbb` に正規化し、不正なら書かない。`themeColor` だけ clamp を通さないのは、正規化を通った値が必ず 7 文字で列 (varchar(64)) に収まるため。**値の出どころは upstream に揃っていない** (#2726 の範囲外。列を溢れさせる話ではない)。**全量は数えていない** — nodeinfo が読めなかったときの結末まで含めると HTML / manifest 由来の経路にも及ぶので、ここは**この行を読むときに効くものだけ**を挙げる。(a) **型を見るか** — upstream が `typeof === 'string'` を掛けるのは `software.name` だけで、`version` も `openRegistrations` も素通し。`openRegistrations` は boolean 列なので `"yes"` は PostgreSQL が true に変換するが、boolean として読めない文字列は **22P02 で UPDATE ごと落ちて upstream は全列を失う** (この行が扱う失敗モードそのもの)。mk-go は型が違えば**既存値を残す** (`TestFetch_WrongTypesAreDropped`)。(b) **切った結果が空になる値は書かない** — upstream は `softwareName` / `softwareVersion` に空文字を書くが、NUL だけの値では UPDATE ごと落として何も書かないので、既存値を残すほうが upstream の結末に近い (`name` / `description` は upstream 側も `if (name)` で空を弾くので差は無い)。(c) **fallback 元が少ない** — upstream の `getSiteName` / `getDescription` は `metadata.name` / `metadata.description` → remote HTML の `og:title` / `<meta name="description">` / `og:description` → web app manifest まで辿り、`getThemeColor` も `<meta name="theme-color">` / `manifest.theme_color` を見る。mk-go は nodeinfo の `nodeName` / `nodeDescription` / `themeColor` だけ (HTML は icon の抽出にしか使っていない)。(d) **`maintainerName` / `maintainerEmail` を書いていない** — 列はある (`migration/000001_initial.up.sql`) が `metadata.maintainer` を読む経路が無い。(e) **HTML / manifest 由来の値を書かない** — upstream は nodeinfo が取れなくても `dom` / `manifest` から `name` / `description` / `themeColor` を埋める。mk-go は HTML を icon の抽出にしか使わないので、nodeinfo が無い host ではこの 3 つが入らない ((c) の裏返し)。**nodeinfo が取れない / 読めないときに 1 列も書かなかった問題は #2730 で解消した** — `infoUpdatedAt` は常に書き、icon の抽出は nodeinfo の成否と独立に走るようになった (`iconUrl` 自体は HTML から取れたときだけ書く)。nodeinfo 1.0 への fallback と object でない JSON の受理 (`'?'`) も upstream に揃えた。**`/favicon.ico` の決め打ちだけは既存値を上書きしない** — upstream は決め打ちを使う前に HEAD で存在を確かめて無ければ既存値を残すが (`fetchFaviconUrl`)、mk-go は HEAD を持たないので「上書きしない」で代える (#2730) ((c)(d)(e) とも #2723 以前からの挙動) |
| リモート添付の `name` の作り方 | `uploadFromUrl` が**実体を download** し、`pathname.split('/').pop()` (Content-Disposition があればそちら) を `validateFileName` に通し、不合格なら `untitled`。さらに `correctFilename` が**sniff した実型**の拡張子を補う | **置き場は upstream と同じにした** (#2723)。代替テキスト (AP の `name`) は `comment` にだけ入れ、`drive_file.name` は URL の basename から作る。差分は 4 つ。(1) mk-go はリモートメディアの**実体を保存しない** (5.5) ので **Content-Disposition を見ない** (寸法の復元で GET すること自体はある)。**Misskey 同士ではここでずれる** — upstream は自分が配信するファイルに `Content-Disposition: inline; filename=...` を付けるので、upstream 側は原ファイル名を採る。(2) **拡張子の補完をしない** (upstream が付けるのは sniff した実型で、相手の申告した `mediaType` ではないため)。(3) Go の `net/url` は WHATWG URL の正規化をしないので、`/a/%2e%2e` (upstream は畳んで `untitled`) と `/a\b.png` (upstream は `\` を区切り扱いにして `b.png`) がずれる。(4) upstream は `name === comment` のとき comment を落とすが mk-go は残す。Mastodon 系はいずれにも当たらないので一致する。`comment` の 512 は upstream と同じ値 (`DB_MAX_IMAGE_COMMENT_LENGTH`) だが、**数え方は違う** — upstream の `truncate` は `stringz.substring` = 書記素クラスタ単位なので、ZWJ 絵文字を含む alt text では 512 クラスタ = コードポイントでは 512 超になり upstream 側が列を溢れさせる。mk-go は rune 単位で切るので列に忠実 (`user.name` の 128 と同じ扱い)。**この変更より前に取り込んだ行は直らない** — 添付は URI で dedup するので `name` に代替テキストが入ったまま残る。**連合出力は元から無事** (renderer は upstream と同じく `Name: stringValue(f.Comment)` で comment を使う) |
| リモート chat room の `id` / `name` / `summary` と message の `text` / `uri` | **列は upstream にも同じ幅である** (`chat_room.id` 32 / `name` 256 / `description` 2048、`chat_message.text` 4096 / `uri` 512)。`uri` は `ChatService` の insert に載ってはいるが、**呼び出し元 (`chat/messages/create-to-user` / `create-to-room`) が `text` と `file` しか渡さないので upstream では常に NULL**。無いのは **AP で chat を受け取る経路**のほうで、`src/core/activitypub/` に chat の扱いが 1 つも無い (CherryPick 由来の拡張)。したがってリモートが決めた値がこれらの列に入ることが無く、upstream はこの問題に晒されない | **列で扱いを分ける** (#2726)。`chat_room.name` (256) / `description` (2048) / `chat_message.text` (4096) は本文なので切って NUL を落とす。`chat_room.id` (32) は PK かつ membership / invitation の FK なので切らず、**収まらなければ room として認識しない** (`extractChatRoomID` が空を返す)。`chat_message.uri` (512) は dedup の鍵なので**収まらなければ message ごと拒否する** — 捨てて行だけ作ると AP retry のたびに同じ message が増える。拒否はいずれも `ErrUnsupportedActivity` に落として retry させない (溢れたまま Create すると `%w` で包まれて retryable になり、その inbox job が retry を使い切って dead になっていた) |
| リモート Question の `oneOf` / `anyOf` の `name` の長さ | 切らない (列は mk-go と同じ `varchar(256)[]`)。ただし poll を note と同じ transaction で入れるので、同じ入力では **note ごと落ちる** | **256 rune で切って note は残す** (`poll.choices` は varchar(256)[]、#2726)。**切ることで生まれる mk-go 固有の穴が 1 つある**: 先頭 256 rune が同じ 2 つの選択肢は同じ文字列に潰れ、`Update(Question)` の集計 (name → totalItems の map) では片方の値が両方に入り、AP vote の照合は切ったあとの完全一致で最初に見つかった index を採るので 2 つ目への投票が 1 つ目に記録される。upstream は同じ入力で note ごと落とすので踏まない。選択肢を丸ごと捨てるより表示できるほうが害が小さいと判断して許容する。あわせて `pollRepo.Create` の**エラー握り潰しをやめた** — 捨てると note が `hasPoll = true` のまま poll 行だけ無い状態が黙って残る。error を上へ返しても直らない (note は既に Create 済みで、retry は `FindByURI` の dedup hit で早期 return するため poll 作成へ再到達しない) ので、結末は warn に残すのが正しい。AP vote と `Update(Question)` の choice 照合も同じ正規化を通す (生値で引くと切った選択肢に当たらない) |
| リモート actor の `publicKey.id` / `publicKeyPem` / `assertionMethod[].id` の長さ | 検証なし (列は mk-go と同じ `keyId` 256 / `keyPem` 4096) | **収まらなければその鍵を保存しない** (#2726)。行の身元なので切ると別の鍵を指す。`assertionMethod` の entry は他の不正 entry と同じく warn + skip (fail-soft、actor 自体は取り込む)。**in-memory cache は残す** — `PublicKeyForActor` の高速路で、再起動後は actor を引き直して同じ値が入るので永続化の有無で挙動は分かれない。消すと `refreshPublicKey` の backoff が効かず inbound 1 件につき outbound fetch が 1 回走る |
| AP tag 由来の emoji の列 | `Promise.all` の 1 件失敗で **その note の emoji を全部落とす** (`extractEmojis(...).catch(() => [])`) | **1 件だけ落とす**。列ごとに扱いを分ける (#2726): `name` (128) は行の身元 (UNIQUE は name+host) でそのまま `note.emojis` / `user.emojis` (varchar(128)[]) にも載るので、収まらない tag は丸ごと落とす。icon URL (`originalUrl` / `publicUrl` 各 512 NOT NULL) は新規なら tag ごと落とし (空の行は壊れた画像になる。未解決の `:name:` がそのまま出るほうが読める)、**既存行では古い URL を残す**。`uri` (512) は値だけ捨てて行は作る。`license` (1024) は本文なので切って NUL を落とす (wrapper があって `freeText` が null のケースは「明示的に未設定」なので nil のまま) |
| Like の reaction 文字列の長さ | `normalize` が `emojiRegex.exec()` の**先頭 1 つだけ**を採るので溢れない | **収まらなければ ❤ (FallbackReaction) に倒す** (`note_reaction.reaction` は varchar(260)、#2726)。mk-go は「全部が絵文字なら生値を保存」なので**絵文字 300 個の Like** が gate を通って 22001 で落ちていた (Like 配送が retry を使い切って dead)。切ると grapheme が壊れるうえ別の reaction になるので倒す。**複数絵文字の reaction を丸ごと保存する点は元から upstream と違う** (upstream は先頭 1 つ) |
| Flag (通報) の `content` の長さ | 切らない (列は mk-go と同じ varchar(2048)) | **2048 rune で切って NUL を落とす** (#2726)。`content + "\n" + URI 一覧` を無検査で入れており、溢れると Create が落ちて **通報そのものが届かない** (inbox job が retry を使い切る)。並び (content → uris) は upstream のまま — 切るのは末尾なので、URI を先に置くと本文が長いときに趣旨が消える |
| リモートメディアのキャッシュ | `cacheRemoteFiles` が真なら実体を自 Drive へ保存 | **保存しない** (相手の削除の権利 / 違法コンテンツ保持のリスク回避)。詳細と弱点は §5.5 |
| `notes/reactions` の可視性 | requireCredential:false で followers/specified note の reaction list も 200 | `CanSeeNote` gate で 404 |
| reaction / chat の可視性エラー | generic INTERNAL_ERROR (500) に包まれる | 403 ACCESS_DENIED (500 拡散を回避) |
| `admin/promo/create` | visibility check なし | public 以外を reject。**upstream にも mk-go にも promo の表示経路が無い**ので現時点では latent だが (#2781、`docs/api-compatibility.md` の「既知の制限」)、表示が入った瞬間に followers / specified / home note の本文が全 viewer に漏れる IDOR になる。**推測ではない** — upstream が 2022-09 に削除した `inject-promo.ts` は `Notes.findOneByOrFail({ id })` の結果を timeline へ `splice` するだけで、visibility を一切見ていなかった。create 段で先回りして塞いである |
| frontend の `img-src` | CSP を設定しないので全 origin の画像が読める | **`'self' data: blob:` + 固定 2 origin** (設定次第で object storage / 外部 media proxy の origin も加わる、#2501 / #2892)。リモート画像は media proxy 経由にする設計で、外部 origin を許すと投稿経由でトラッキング画像を読ませる経路が開くため。**例外は `avatars.githubusercontent.com` と `assets.misskey-hub.net` の 2 つだけ** — upstream の `/about-misskey` が謝辞のアイコン 62 枚 (メンバー 6 / スポンサー 6 / パトロン 50) をここから直接読み、#2700 で「upstream の謝辞は消さない」判断をした以上、許さないと恒久的に壊れた画像が並ぶ。**media proxy 経由には落とせない** — mk-go の proxy は open proxy ではなく allowlist が DB に実在する URL だけを通すので、静的な URL は 403 (実測)。閲覧者の IP はこの 2 host に渡るが、upstream は CSP 自体が無いので元から同じ。`embed` shell には足さない (`/about-misskey` はそちらに無い) |
| `/api/meta` の `providesTarball` | `publishTarballInsteadOfProvideRepositoryUrl` の設定をそのまま返す。`ClientServerService` (`packages/backend/src/server/web/`) が `built/tarball` を `/tarball/` に静的配信するので、frontend の `/tarball/misskey-<version>.tar.gz` リンクは実在する | **常に false を返す** (`internal/config.Config.ProvidesTarball`、#2700)。mk-go に `/tarball/` を配信するルートが無いので、設定を通すと壊れたリンクを「ソースコード (Tarball)」として表示する。**404 にすらならない** — SPA の catchall (`GET /*`) が拾うので、`misskey-<version>.tar.gz` という名前の HTML が 200 で返る (実測)。AGPL 13 条の案内としては、壊れた tarball を掴ませるより `repositoryUrl` だけのほうが正しい。設定値そのものは読み、有効なときは `config.Load` の resolve で warn を出して無視していることを伝える。**frontend 側に tarball の分岐は 2 箇所ある** (`about-misskey.vue` / `about.overview.vue`)。`/tarball/` を実装するときは `ProvidesTarball()` を直すだけでは足りず、mk-go 独自の `about-mkgo.vue` にも分岐を足すことになる |
| `meta.repositoryUrl` / `meta.feedbackUrl` の既定値 | 列 DEFAULT の `https://github.com/misskey-dev/misskey` が入る。TypeORM は列を INSERT に含めたうえで**値として `DEFAULT` キーワードを書く** (`InsertQueryBuilder` の PostgreSQL 分岐。「未指定の列を含めない」わけではない) ので、列 DEFAULT が効く | **`https://github.com/shiroha-a/mk` を Go 側で入れる** (#2700)。列 DEFAULT (`migration/000029`) は upstream 互換のため据え置いてあるが、**GORM は `*string` の nil を NULL として明示挿入する**ので効かない。放置すると新規インスタンスは `repositoryUrl = NULL` になり、AGPL 13 条の案内が既定で存在しない状態になる。既存インスタンスは `migration/000084` が埋めるが、**対象は NULL と upstream の列 DEFAULT のままの行の両方**。後者は Misskey TS 生まれの DB (drop-in 移行) が必ず持つ値で、operator の申告ではないため、動いているのが mk-go である以上そのままでは「このサーバーのコード」として Misskey 本体を案内することになる。さらに frontend は `repositoryUrl !== 'https://github.com/misskey-dev/misskey'` で改変版の告知ポップアップを出すか決めるので、放置すると**告知そのものが出ない**。`about-mkgo.vue` 側も同じ値を「未設定」として扱う (migration 適用前や operator が明示設定した場合の保険)。**`feedbackUrl` も同じ理由で NULL になる** — `000029` は隣り合う 2 行で両方の列 DEFAULT を設定しており、どちらも効かない。#2891 で `migration/000085` と `EnsureInitial` を足して同じ形で埋めた (既定は mk-go の issues。upstream が Misskey 本体の issues を置いているのと同じ「ソフトウェアへのフィードバック先」)。**`feedbackUrl` は nodeinfo の metadata にも載る**ので、未設定だと他インスタンスからも見えない。**列 DEFAULT を残すのは drop-in の復路のため** — TypeORM は未指定の列に `DEFAULT` を書くので TS 側では実際に効く。`000029` のコメントは「新規インストール時に本家と同じ挙動になる」と書いていたが、GORM 経由では一度も効いていないので #2891 で実態に直した |
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
| 通知一覧の不可視 note | `NoteEntityService.packMany` は入力と 1:1 で null を返さず、可視性は `hideNote` が `text` / `files` 等を blank して `isHidden: true` を立てるだけ。**通知行は残る** (`NotificationEntityService` が note を理由に落とすのは、削除済で `packedNotes` に載らなかった行だけ。notifier 不在 / role 削除済などの理由では別途落ちる) | **通知行ごと落とす**。`collectNotifications` の `FilterVisible` が不可視 note を `noteByID` に載せず、note-required 通知はその行ごと除外される (#1444 / #1953)。通知が来た事実そのものを伏せる分だけ安全側。`noteId` だけの行を返さない点は upstream と同じ。**stream (`#1471`) と Web Push (`#1572`) は行を残して note detail だけ落とす** — 通知イベント自体を落とすと未読が食い違い、push は届かなくなるため。REST に揃えないこと |
| Web Push payload のサイズ | `truncateBody` は `text` を要約に置き換え `cw` / `reply` / `renote` / `user` を落とすだけで、**サイズ検査が無い**。`files` はそのまま載るので、添付の多い note の push は 4 KB 上限を超えて配信されない | 上限 (body 3800 B) を超える場合に `files` → `text` の先頭側 → (最後の手段として) `note` ごと、の順で縮める (#2737)。`text` を切るときは**末尾を残す** — 要約は `本文 + (📎N)` の順に組み立てられるので、末尾から削ると添付件数が消える。`note` ごと落とすのは最後にする — sw.js の `composeNotification` は `data.body.note` を無条件に参照し `noteId` は読まないため、落とすと通知がブラウザ既定の汎用表示に化ける |
| `newChatMessage` の Web Push | `setTimeout(…, 3000)` で 3 秒待ち、redis marker で**未読のときだけ** publish + push する。muted な room member は marker を張らないので対象外 | **未読ゲートを持たず即時に送る** (#2840)。publish 側が元から未読判定を省いており (`core/chat/service.go` のコメント)、push もそれに揃えた。そのため**タブを開いて読んでいる利用者にも OS 通知が出る**。muted な room member は upstream と同じく除外する。また mk-go は upstream に無い AP 受信経路 (DM / room、chat 連合は cherrypick 由来) でも push する。recipient が remote でも enqueue する — processor が subscription 0 件で no-op するので実害は job 1 件ぶん。**payload の上限超過は縮められない** (`fitPayload` は `note` を持つ body しか縮めないので、日本語で概ね 1100 文字を超える chat message は `webpush-go` の pad が `ErrMaxPadExceeded` を返し**送信前に落ちる**。upstream も `truncateBody` が `notification` / `unreadAntennaNote` しか見ないので縮まらず、あちらは push service が 413 を返す。届かない点は同じ) |
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
全部が失われる。失われる範囲も書き込みの単位で決まる: note なら ingest が error を
返し (トップレベル配送ならその inbox job が retry を使い切って dead になる。既定 8 回、
`defaultInboxJobMaxAttempts` in `internal/server/queue_factory.go`)、actor なら 1 行も
作られない、添付は 1 件ずつ Create するので**その添付だけ**消える。

**「dead になる」= その配送が失われるだけ**で、同じ note が返信の解決や Announce
経由の `ResolveNote` で後から取り込まれることはある (原因が直っていれば)。

**#2726 で chat 系 / `poll.choices` / 鍵の `keyId` / AP tag 由来の emoji /
`note_reaction.reaction` / `abuse_user_report.comment` にも適用した。**
個々の判断は上の表に 1 行ずつある。

**`note.url` はリモート note でも保存する** (#2729)。列は varchar(512)。
`uri` (AP object の `id`) とは別 field で、Mastodon 系では `url` が HTML の
permalink を指す。#2726 の時点では**書く経路が 1 つも無く**、mk-go が取り込んだ
note の応答から `url` が丸ごと落ちていた。

読み方は upstream の `getOneApHrefNullable` と同じ — **配列なら先頭要素**、string
ならそれ、object なら `href`。**`id` は見ない** (`getApHrefNullable` は `href` だけ
を読む)。ただし **単一キーの** `{"@id": ...}` は inbox 経路だと手前で string に
潰れるので別扱いになる (2 キーあると潰れない。後述)。

**受理する値の集合が upstream と違う。** upstream の判定は
`checkHttps` = 生文字列の `startsWith('https://')`、または `startsWith('http://')`
かつ `NODE_ENV !== 'production'` で、**false なら note ごと throw する**。

| 値 | upstream | mk-go |
|---|---|---|
| `https://…` | 保存 | 保存 |
| `HTTPS://…` (大文字) | **note ごと reject** (`startsWith` は case-sensitive、実測) | **保存** |
| `http://…` | production は **note ごと reject**、それ以外なら保存 (実測) | **保存** |
| `javascript:` / `ftp:` / 相対 URL 等 | **note ごと reject** | **値だけ捨てて note は作る** |
| JSON-LD の展開形 (`{"@id": …}` / `{"@value": …}` / full IRI のキー) | **捨てる** (`getApHrefNullable` は `href` だけを読む) | **経路依存** (後述) |
| `""` (空文字) | **保存する** (`if (url && !checkHttps(url))` は falsy を素通りし、`if (data.url != null)` で `""` が入る) | **捨てる** (空の permalink はどこも指さない) |
| varchar(512) を超える | 検証なし (22001 で note ごと失う) | **値だけ捨てて note は作る** |
| NUL 入り | 検証なし (22021 で note ごと失う) | **値だけ捨てて note は作る** |

**mk-go は note を落とさない。** 理由は 2 つ。(1) `note.url` は表示用で、身元は
`uri` のほうなので上の規則では **URL / ID 系** = 「収まらなければ値ごと捨てて親の
行は作る」に当たる。(2) permalink の scheme が変なだけで**本文ごと失う**ほうが害が
大きい。

**`javascript:` を保存しない点は upstream と同等**で、mk-go の優位ではない。
upstream は `checkHttps` が false なら note ごと throw するので、結果として
`note.url` に入る値は `https://…` (と dev の `http://…`) か空文字だけになる。
**受理する scheme はむしろ mk-go のほうが広い** (上の表の 2 行)。捨てる判断が
安全側なのは「値を保存する」案に対してであって、upstream に対してではない。

**scheme は case-insensitive に見る** (RFC 3986 上 scheme は case-insensitive。
`internal/core/urlpreview` の `isHTTPScheme` と同じ方針)。upstream は
case-sensitive なので、`HTTPS://` は mk-go だけが受ける。`http://` も mk-go は
production かどうかに関わらず保存する。**どちらも「upstream が note ごと落とす値を
mk-go は note ごと残す」方向**。

**上の 2 行は scheme に由来する差の全量**で、これ以外に scheme の綴りで受理が
分かれる形は無い (`strings.ToLower` が ASCII の `h/t/p/s/:/ /` に落ちる非 ASCII の
符号位置は全 Unicode を走査して 0 件)。**scheme に由来する差はすべて mk-go が
余計に受ける方向**で、逆向き — upstream が保存して mk-go が捨てる — になるのは
空文字の行。**JSON-LD の展開形にも逆向きの形がある** (後述の表の 2 行目)。

**scheme とは別に、JSON-LD の展開形による経路依存の差がある。** `Normalize` は
`{"@value": ...}` を**その中身へ**置き換え (中身が object ならその object が残る)、
**単一キーの** `{"@id": ...}` を string へ潰し、full IRI のキー
(`https://www.w3.org/ns/activitystreams#href`) を `href` へ畳む。置き換えた結果が
string / `href` 付き object なら `APLenientHref` が読むので、**upstream が `href` を
持たないとして捨てる形を mk-go の inbox 経路は読める**。`Normalize` を通らない生
fetch 経路では潰れないため、**同じ note でも入口によって結末が変わる**。AS2 の
`@context` は `url` を `{"@id":"as:url","@type":"@id"}` と定義していて展開形は正規の
表現なので、実在しうる差。

**結末は「入る / 入らない」の 2 通りではない。** `@value` の置き換えは `@id` と違って
**兄弟キーを見ない** (`len(x) == 1` のガードが無い) ので、`href` を持つ object でも
`@value` があればそちらが勝つ。

| 例 | upstream / 生 fetch 経路 | inbox 経路 |
|---|---|---|
| `{"@id":"https://x/1"}` | 捨てる | `https://x/1` を保存 (**余計に入る**) |
| `{"href":"https://x/1","@value":"x"}` | `https://x/1` を保存 | 捨てる (**入るはずが入らない**) |
| `{"href":"https://x/1","@value":"https://y/2"}` | `https://x/1` を保存 | **`https://y/2` を保存** (**別の URL に差し替わる**) |

**3 つ目が最も気付きにくい** — 欠けた permalink は目に見えるが、入っている別の URL は
見えない。どちらの値も同じリモート送信者の管理下にあり `attributedTo` の gate も
通っているので権限境界は跨がないが、クライアントが開くリンク先は変わる。

**この形の全量は数えていない。** `Normalize` の変換規則と `APLenientHref` の
読み方の組み合わせで決まるので、形を列挙しても片方が変われば腐る。揃えるなら
`Normalize` 側の判断になる。

inbound `Update(Note)` でも追従するが、**捨てられた値では上書きしない** — 読めない
`url` が来ただけで、取り込み時に保存した正しい permalink を消さないため。

**#2729 より前に取り込んだ note は直らない。** `ingestNoteWithCreated` は
`FindByURI` が当たった時点で早期 return するので取り込み直しが起きず、actor の
`refreshActor` にあたる note の再取得経路も無い。したがって既存行の `url` は
NULL のままで、埋まるのは相手が `Update(Note)` を送ってきたときだけ。actor 側で
同じことを書いてある箇所と対称。backfill は用意していない — **packer は fallback
しない** (`url: note.url ?? undefined`) が、リンクを開く側が `note.url ?? note.uri`
で代替するので (frontend の `pages/note.vue` / `get-note-menu.ts`)、行き先を失う
わけではない。対象を埋めるには全リモート note を再 fetch することになる。

**`url` が無い note では key ごと落ちる。** `NoteEntity.URL` は
`json:"url,omitempty"` で、upstream も `url: note.url ?? undefined` なので同じ形。
ローカル note では upstream も null なので (`MiNote.url` の列コメント「it will be
null when the note is local」)、そちらは一致する。

`entity/note.go` の `firstNonNil(n.URL, n.URI)` は **`name` 付き note の本文整形
専用**で、pack される `url` field には効かない (`URL: n.URL` のまま)。**mk-go が
取り込んだ note ではこの整形が起きない** — `model.Note.Name` を書く production
code が 1 つも無いので `n.Name != nil && *n.Name != ""` の gate が真にならない
(`note.name` を保存しないこと自体が upstream との別の乖離。upstream は
`ApNoteService` が `name: note.name` を渡し、`NoteCreateService` は `name` を
**無条件に**、`url` は `data.url != null` のときだけ入れる)。

**gate が真になるのは drop-in で Misskey TS が書いた行だけ**で、そこには
`name` はあるが `url` が NULL の行もありうる (上のとおり `url` は条件付き)。
その行に inbound `Update(Note)` が来ると #2729 で `url` が入るので、**本文末尾の
リンクが `uri` から `url` へ変わる**。upstream も `note.url ?? note.uri` なので
同じ方向。

数え方と NUL の扱いは `internal/misc/colfit` に集約してある。以前は federation と
instance に **7 つのヘルパーが散っていた** (federation の `fitsColumn` /
`truncateRunes` / `remoteText` / `sanitizeRemoteText` / `remoteDisplayName`、
instance の `clampInstanceText` / `fitsInstanceColumn`)。rune / byte の数え方と
NUL の扱いが分かれる土壌になっていたので中身を委譲した。**名前付きヘルパーは
7 つとも残してある** — 「その列固有の判断とログ」を持つので (#2726)。

**畳めるように見えて畳めない対がある。**

- `remoteText` と `clampInstanceText` は `max > 0` の呼び出しでは同じ結果になるが、
  それ以外で分かれていた。`max == 0` は前者が切らず後者は空にし、**`max < 0` は
  後者が panic する** (`string(runes[:max])`、実測)。`colfit.Text` は前者を採った
  ので、**`clampInstanceText` は `max <= 0` の挙動が変わっている**。instance の
  呼び出しは 64 / 64 / 256 / 4096 の定数だけなので影響は無い
- `fitsColumn` と `fitsInstanceColumn` は 2 点で違っていた。**NUL を見るかは委譲で
  揃った** (`fitsInstanceColumn` は元々見ていなかったが、`colfit.Fits` が見る。
  URL 列に生の NUL は届かないので空振りする)。**残るのは空文字の扱い** —
  `fitsInstanceColumn` は `v != ""` で空を弾く。この `v != ""` は飾りではなく、
  `iconUrl` / `faviconUrl` を**書くかどうか**を決めている。空を fit 扱いにすると
  既存の icon URL を空文字で上書きするので、ここは畳めない

列長の出どころは `migration/` の SQL (多くは `000001_initial.up.sql`、後から足した
テーブルは個別のファイル)。コード側の定数と独立に同じ数値を書くことになるので、
**実 DB の列長を `information_schema` から読んで突き合わせる回帰テスト**を必ず置く
(`TestNote_CWColumnLimitIs512` / `TestNote_URIColumnLimitIs512` /
`TestNote_URLColumnLimitIs512` /
`TestUser_IdentityColumnLimits` / `TestDriveFile_ColumnLimits` /
`TestInstance_NodeinfoColumnLimits` / `TestChat_RemoteColumnLimits` /
`TestUserPublickey_ColumnLimits` / `TestEmoji_RemoteColumnLimits` /
`TestRemoteTextColumnLimits` / `TestRemoteArrayColumnLimits`)。mock repository は
列制約を持たないため、呼び出し側のテストだけでは「本当に入る長さか」を確かめ
られない (`internal/testutil` の `assertUserColumns` は `"user"` の主な列だけ
本番に揃えてある)。

**配列列 (`poll.choices` / `note.emojis` / `user.emojis`) は `information_schema`
では読めない。** `columns.character_maximum_length` は NULL で、`element_types` も
要素の typmod を持たない (実測)。`pg_catalog` の
`format_type(atttypid, atttypmod)` だけが `character varying(256)[]` を返すので、
そちらで固定する (`TestRemoteArrayColumnLimits`)。

## 8. 逆方向 divergence (mk-go 独自 error を upstream に合わせて廃止したもの)

`admin/emoji/import-zip` の `NO_SUCH_FILE`、clip 削除の `NOT_CLIPPED`、`notes/translate` の `CANNOT_TRANSLATE` はいずれも mk-go 独自 error だったため upstream に合わせて廃止済み。myReaction fetch の「作成 2 秒以内は skip」guard は mk-go では機能劣化になるため意図的に不採用。

---

## 9. 実装が近似になっているもの (意図も結末も同じ・数値がわずかに違う)

upstream の挙動を再現しているが、依存ライブラリが違うため**画素値までは一致しない**もの。乖離として残す判断ではなく「ここまで揃っていて、残差はこれだけ」という記録。

### プッシュ通知のバッジ画像 (`mediaproxy.processBadge`)

upstream は sharp (libvips) で処理する (#2920 で mk-go も同じ形にした)。**JS の記述を読むだけでは 3 つ取り違える。**

**(1) sharp は呼び出し順ではなく固定の pipeline 順で実行する。** `FileServerProxyHandler.ts` の記述は `resize().greyscale().normalise().linear().flatten()` だが、`sharp/src/pipeline.cc` の実行順は **flatten → greyscale → resize/embed → linear → normalise**。記述順どおりに実装すると輝度レンジの狭い絵文字で平均絶対差 28.7/255・43.8% の画素が 32 以上ずれる。**呼び出し順を入れ替えても sharp の出力が 1 バイトも変わらない**ことで確認できる。

**(2) `normalise()` は min-max ではなく 1/99 パーセンタイル。** sharp の既定は `{lower: 1, upper: 99}` で、`operations.cc` は `luminance.percent()` を使う。min-max で実装すると upstream 自身のテスト画像 `test/resources/192.png` で平均絶対差 23.9/255・最大 42・32 を超える画素 16.7% になる。

**(3) `stats()` は pipeline を無視する。** `sharp/src/stats.cc` は入力を開き直すので、entropy は**元画像**の greyscale ヒストグラムの**標準的な Shannon エントロピー**。`linear(0, 100)` で定数化しても `threshold(128)` で 2 値化しても値が変わらず、96 段のグラデーションで `log2(96) = 6.584963` に一致する。判定は「**元画像**が実質単色か」であって、処理後の mask ではない。

揃っているもの:

- contain なので横長・縦長の絵文字でも内容が切り落とされず、余白は黒帯になる (拡大もする。貼り付け位置は切り捨て)
- 透明部分は黒に落ちる
- greyscale は線形光を経由する (係数を sRGB 値へ直接掛けると純赤が 127 ではなく 54 になり、赤/緑の明暗比が 1.73 → 3.37 に変わる)
- linear (1.75x、0-255 でクリップ) の後に 1/99 パーセンタイルの normalise
- 出力は R=G=B=A なので、**暗いところが透明な silhouette** になる
- 元画像がほぼ単色なら 404 を返し、Service Worker (`create-notification.ts`) が `iconUrl('plus')` へ落ちる

**実測 (sharp の出力と画素単位で突き合わせ)**:

| 入力 | 平均絶対差 | 最大差 | 差が 32 を超える画素 | 完全一致 |
|---|---|---|---|---|
| webp 128x128 (実絵文字) | 0.18 / 255 | 4 | 0.0% | 86.9% |
| png 96x96 (実絵文字) | 4.28 / 255 | 10 | 0.0% | 45.1% |
| gif 100x100 (実絵文字) | 0.23 / 255 | 8 | 0.0% | 84.9% |
| upstream の `test/resources/192.png` | 4.43 / 255 | 15 | 0.0% | 17.7% |
| 同 `192.jpg` | 4.77 / 255 | 15 | 0.0% | 16.9% |

entropy も sharp と一致する (gif は完全一致、他は差 0.03 以下)。**404 になるかどうかの判定は揃う。**

残差の出どころ:

| | upstream (sharp / libvips) | mk-go |
|---|---|---|
| リサンプリング | libvips の lanczos3 | `kovidgoyal/imaging` の Lanczos |
| パーセンタイルの量子化 | LAB の L (0-100 の int) | 0-255 のヒストグラム |
| normalise の色空間 | LAB の L を伸ばして chroma と再結合 | greyscale 値を直接伸ばす |

**これ以上は追わない。** 32 を超える画素がゼロで、通知に出る 96x96 のモノクロ silhouette でこの差は判別できない。

### 画像デコーダ由来の差 (badge に限らない)

`decodeImage` は `kovidgoyal/imaging` を使う。#2920 のレビューで挙がった 3 件を #2925 で実測し、2 件は直した。**全モード (emoji / avatar / preview / static / badge) に効く。**

**インターレース (Adam7) の truecolor PNG — 解消 (#2925)。** imaging は colorType=2 / bitDepth=8 / tRNS 無しの PNG を独自の `*nrgb.Image` に読むが、その Adam7 の pass 合成に `*nrgb.Image` の case が無く、**エラーを返さず全画素 0** を返していた (34 通りの組み合わせで実測し、壊れるのはこの 3 条件が揃ったときだけ)。`internal/misc/imagedecode` に判定を置き、該当するものだけ stdlib の `png.Decode` へ回す。

- **条件を広げてはいけない。** インターレースだけで判定すると、壊れていない種別まで stdlib へ回して **ICC→sRGB 変換を落とす** (Display P3 の PNG でチャンネル差が最大 31/255)。upstream の sharp は `icc_transform("srgb")` を通すので、広げると乖離が増える
- **この経路では EXIF の向きと ICC 変換が失われる。** 代わりに得られるのが「真っ黒な画像」なので、そちらの方がましという判断
- **decode は 2 箇所にある。** media proxy と drive の image processor で、片方だけ直すと**ローカルにアップロードされた画像が真っ黒なサムネイルを storage に焼く** (proxy は variant を優先して返すので原本を読み直さない)。規則を `internal/misc/imagedecode` に 1 つだけ置いてある

**`*image.NYCbCrA` (lossy WebP + alpha) の alpha — 解消 (#2925)。** imaging の scanner は、サブサンプル (4:2:0 / 4:2:2 / 4:4:0 = lossy WebP の通常形) の分岐で alpha の index を内側ループで進めないため、**行の全画素がその行の先頭画素の alpha**になる。行頭が透明な画像は**丸ごと透明**になり、badge は見えないまま 200 で配られる (実測: 128x128 で 50% の画素の alpha が誤り、最大差 255)。

`draw.Draw` / `At()` は逆に alpha は正しいが premultiplied なので**完全に透明な画素の RGB が 0 に潰れる**。`normalizeForResize` が Y / Cb / Cr / A の plane を自分で読んで両方とも正しく取る。**resize 経路だけでなく `processBadge` もここを通す**必要がある。

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
