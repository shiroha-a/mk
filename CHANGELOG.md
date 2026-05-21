# Changelog

## 互換バージョン: Misskey 2026.5.4 (#1164 で submodule bump、2026.5.1-mk.0 → 2026.5.4-mk.0)

### Phase 18 — 2026.5.4 upstream 追従 + LD-Signature 初期実装 (#1164 完了)

submodule を 2026.5.1-mk.0 → 2026.5.4-mk.0 に bump、その間に発生した drift を移植 + 長年の drop-in gap だった LD-Signature 検証の初期実装を 1 PR に集約 (旧 #1118 統合):

- **Phase A** (2026.5.2 由来):
  - `/api/following/list` を新設 (upstream #17385 + #17416)。kind read:following、notification=true で notify IS NOT NULL filter、cursor sinceId/untilId + limit 1-100 default 10。FollowingEntityService.pack 互換の Following entity を `internal/entity/following.go` に追加
  - `/api/following/update` の notify="none" を SQL NULL に正規化 (#17385 完全対応)。これがないと following/list の notification=true filter が機能不全だった drop-in regression を解消
  - twemoji 参照 path を `node_modules/@misskey-dev/emoji-assets/built/twemoji` に追従 (upstream #17381)
- **Phase B**: submodule bump 2026.5.1-mk.0 → 2026.5.4-mk.0
- **Phase C** (2026.5.4 由来):
  - `chat/rooms/show` に HasPermissionToViewRoomInfo gate を追加。owner / member / 招待受信者 / moderator のみ閲覧可、それ以外は noSuchRoom 404。旧 mk-go の RoomsShow は権限 check 完全欠如で room id を知る任意 user が room メタを取得できる drop-in regression / privacy hole だった
  - `/api/announcements/show` の userId-targeted gate を実装。非ログイン user / 他人宛 announcement へのアクセスを 404 で塞ぐ (upstream 2026.5.4 fix + mk-go 単体の旧穴 2 重対応)
- **Phase D** (LD-Signature 初期実装 + 2026.5.4 hardening):
  - `internal/activitypub/ld/` を新設。`piprate/json-gold` v0.8.0 + URDNA2015 canonicalize の土台
  - Misskey TS `PRELOADED_CONTEXTS` 互換の 3 context document (AS 2.0 / Security v1 / Identity v1) を JSON file として embed、PreloadedLoader が HTTP fetch なしで context resolve
  - RsaSignature2017 verify を実装 (= upstream JsonLdService.verifyRsaSignature2017 byte-for-byte 互換、createVerifyData → SHA-256 hex concat → RSA-PKCS1v15 SHA-256 verify)
  - 2026.5.4 hardening を初期実装に組み込む: forbidden directives (`@included` / `@graph` / `@reverse` を含む activity を弾く) / per-call cache 上限 256 entry / Freeze() で SSRF + TOCTOU race 遮断
  - InboxProcessor 経路で全 inbound activity に対する LD-Sig optional verify を追加。signature 無し → skip / verify pass → 通常処理 / verify fail → drop。relay 由来 Announce 挙動 (#1002) は upstream 互換維持 (= LD-Sig pass しても renote 生成せず stream publish のみ)
- 全 11 commit、`make fmt && make lint && make test` 全 pass、federation カバレッジ 90% 以上維持

旧 #1118 (2026.5.1 → 2026.5.3 追従 tracker) は本 PR に統合して close。`docs/update/20260502diff.md` / `docs/update/20260504diff.md` の「mk-go 対応」サマリを「移植完了」に update した。

### Phase 17 — Ed25519 サポート (FEP-521a Multikey, #1067 完了)

mk-go 独自の先行実装として、HTTP Signature の Ed25519 対応を 6 phase で導入。Fedibird (Mastodon フォーク) など FEP-521a 対応サーバーが Ed25519 鍵を expose する流れに先んじて、upstream Misskey TS が未対応の Ed25519 を mk-go では capability-gated で sign/verify する。

- P1 (#1068 / PR #1074): schema migration + 鍵生成 配線。`user_keypair_extra` / `user_publickey_extra` add-only table、signup / systemaccount で RSA と並行発行
- P2 (#1069 / PR #1078): Renderer で `assertionMethod[]` expose + Multikey encode helper (`github.com/multiformats/go-multibase` 利用)
- P3 (#1070 / PR #1079): Resolver で remote actor の `assertionMethod[]` parse → `user_publickey_extra` upsert + keyId dual lookup + stale key 自動削除 (key rotation 対応 / security fix)
- P4 (#1071 / PR #1080): Outbound deliver で capability-gated Ed25519 sign + Redis INCR ベースの 4xx degrade safeguard (閾値 3 / 60s window) + 5min cache + singleflight
- P5 (#1072 / PR #1081): 既存 local user の lazy backfill (= TS で signup された旧 user に対しても actor JSON 初回 fetch で Ed25519 鍵を自動発行) + singleflight 集約
- P6 (#1073 / PR #1084): drop-in test に MUST シナリオ (mk-A 切替後の actor JSON で Ed25519 expose / 再 fetch で安定) を追加
- P6 follow-up SHOULD (#1082 / PR #1085): mk-A → TS-A 戻し時の連合継続を drop-in test (`run-swap-test.sh` stage 7-9) で検証
- P6 follow-up NICE-TO-HAVE (#1083 / PR #1086): Python ベースの Fedibird-like ActivityPub mock (`tests/dropin/fedibird_mock/`) と `run-fedibird-test.sh` orchestrator + `test_fedibird_ed25519.py` 3 経路 (actor fetch / mock→mk-A inbox Ed25519 sign / mk-A→mock outbound Ed25519 sign) を追加

drop-in 互換: 既存 `user_keypair` / `user_publickey` テーブル untouched、Misskey TS は新規 extra テーブル無視 → TS 戻し時の連合継続。`PASSWORD_TOO_LONG` 以外の error code drift も発生せず。

### Phase 16 — Playwright e2e Phase 1-4 (#744 完了)

両 backend (mk-go / Misskey TS) 並列実行で drop-in 互換 regression を nightly 監視する基盤を 4 phase で整備:

- Phase 1 (#795-#840): 基盤 + auth / notes / drive / streaming / 2FA TOTP / 2FA Passkey spec
- Phase 2 (#842-#901): timeline / users / reactions / chat / notification / emoji / pages / gallery / clips / flash / reversi / settings / search / drive 拡張 / admin / role / meta
- Phase 3 (#902-#922): charts / federation / ap / sw / antennas / invite / auth-app / miauth / announcements / utility / i 拡張 / bubble-game / reversi multiplayer
- Phase 4 (#923-#934): channels / hashtags / roles / notifications control / admin/queue / admin/abuse-report / admin/{stats,show,ad,avatar-decorations,drive read} / admin/{server-info,captcha,invite,announcements,relays,system-webhook} / i webhooks / chat extra

達成: **96 spec / 35 directory / 242 endpoint cover (54.3%)**、両 backend で nightly green。

### Phase P5 — Drift backlog (Playwright で発見した 40+ 件)

Playwright spec を両 backend で走らせる中で観測した drop-in 互換 drift を順次 fix:

#### auth / signup / signin
- #798: signup duplicate username の status code (400 DUPLICATED_USERNAME)
- #800: username 長さ制限 (20 char max)
- #802: error response body shape (Misskey misc 形式)

#### note / timeline
- #799: notes/show で visibility 違反時の挙動 (200 で stub note)
- #874 + perf #892 / #894: timeline endpoint で user mute filter 追加 + SQL push-down + muting subquery 化
- #876: users/lists/list の N+1 query を batch fetch に
- #877: notes/search の `fulltextSearch.provider: "none"` opt-in (= upstream TS strict-mode 互換 400 UNAVAILABLE) を追加。mk-go 既定は引き続き `sqlLike` fallback で動く
- #878: users/search-by-username-and-host suspend filter

#### drive
- #812: drive/files/create userId / user / folder shape
- #818: drive/files/find / find-by-hash の packMany self path
- #845: drive/folders/show detail mode (parent / counts)
- #977: drive/folders 系の `NO_SUCH_FOLDER` UUID を endpoint 別 (create `53326628-...` / show `d74ab9eb-...` / update `f7974dac-...` / delete `1069098f-...`) に分割。`folders/update` の parent 不在を `NO_SUCH_PARENT_FOLDER` (`ce104e3a-...`) として区別するため `ErrParentFolderNotFound` を追加

#### chat
- #851 / #855 / #860: chat packMessage / packRoom の null 省略 / field set drift
- #864: reaction emoji の variation selector 正規化

#### users / following / blocking
- #870: blocking/create / delete return shape (UserDetailed 返却)
- #871: users/lists/create response shape (createdAt / userIds / isPublic)
- #872: blocked → following/create reject status (400)
- #984: users/relation を stub から実装に切替。viewer ↔ target の follow / follow-request / block / mute / renote-mute 状態を 5 repo (`following` / `follow_request` / `blocking` / `muting` / `renote_muting`) から実 DB 状態として返す
- #970: `/api/users/show` で viewer===target のとき MeDetailed 拡張 field (isExplorable / noCrawle / emailNotificationTypes 等 14 個) を merge して返すよう拡張。upstream `pack(user, me)` semantics と一致させ、`/api/users/show?username=me` 経由でも `/api/i` と同じ shape を保つ。新規 helper `entity.AsMeDetailed` で pre-built UserDetailed を promote する design
- #988: `canChat` 二重 drift 解消。`PackUserLite` が \`u.ChatScope != "none"\` でなく **role policy の `chatAvailability === "available"`** から derive するように変更 (upstream `UserEntityService.ts:561` 互換)。新規 `entity.CanChatLookup` interface を `internal/entity/can_chat.go` に追加し、`role.CanChatLookupAdapter` で role.Service を bridge する design (= `entity.SetAvatarDecorationLookup` と同 pattern)。`/api/i` Me handler の `resp["canChat"] = true` hardcode も撤去し、self-view でも role policy が反映される
- splash 画面 drift 解消: SPA ロード中の `<div id="splash">` markup を upstream `_splash.tsx` 互換に揃える (#993)。旧実装は mascotImageUrl (= ai.png) を中央に表示し "Loading..." text だけ出していたが、upstream は server iconUrl + 回転 spinner SVG (bg/fg 2 枚) を表示する。修正後: `<img id="splashIcon" src="...">` + `<div id="splashSpinner">` (2 SVG)。default icon は `/static-assets/splash.png` (Misskey ロゴ)、admin が meta.iconUrl を設定していればそれを使う

#### settings / token
- #883: i/regenerate-token return shape (204)
- #884: 旧 API token を cache 経由でも即時 reject
- #885: i/update-email error status 標準化
- #910: app-issued access token を auth middleware で dual lookup (raw → hash)
- #913: i/revoke-token も dual lookup + cache invalidation
- #985: `entity.PackMeDetailed` に `emailNotificationTypes` / `mutingNotificationTypes` / `notificationRecieveConfig` を追加。i/update 経路でも 3 field が返るようになり、frontend の `updateCurrentAccountPartial` が settings/email 等の toggle 後に local state を正しく反映できる。値は `user_profile` の JSON column を unmarshal して取得し、parse 失敗時は upstream default (`["follow","receiveFollowRequest"]` / `[]` / `{}`) に倒す
- #971: `/api/i` Me handler を `PackMeDetailed` ベースに refactor。JSON round-trip で MeDetailed を resp map base に展開し、固有 field (email / mutedWords / twoFactor / clientData / role / unread 等) を merge する design。重複コード 25 行削減、MeDetailed packer 更新時に自動追従

#### admin
- #888: admin/show-user shape (roles / policies / signins / roleAssigns / isHibernated / lastActiveDate)
- #889: admin/roles/create paramDef strict (13 field required)
- #896 / #900 / #901 / #931 / #932: pq.StringArray drift (avatar-decorations / system-webhook / flash / antenna / emoji bulk の Updates(map) で空 string[] が NULL 化される問題を `pq.StringArray()` ラップで解消)
- #907: i/registry/set で domain field の handling
- #915: federation/show-instance を 204 No Content に
- #918: sw/show-registration を 204 No Content に
- #925: hashtags/* paramDef strictness drift
- #926: channels の Playwright LCD strict 化
- #929: admin/queue + admin/abuse-report の paramDef strictness / idempotency
- #936: i/webhooks/update を 204 No Content に
- #937: i/webhooks/test の paramDef (type required + webhookEventTypes enum)
- #939: i/webhooks/{create,update} の on array enum check

#### UDS production 由来
- #940: 遅延配送 remote note の createdAt drift (AP `published` を採用 + clock skew/floor guard)
- #941: カスタム絵文字 (リアクション / picker) のアニメ pass-through (mediaproxy で gif/apng を resize しない)
- #942: URL summary 文字化け解消 (Shift_JIS / EUC-JP / ISO-2022-JP の自動正規化)
- #943: リモートユーザー counts (notesCount / followersCount / followingCount) を origin から fetch する mk-go 独自拡張
- #945: RemoteStatsFetcher cache を `sync.Map` → LRU (size cap 10000) で memory bound 化
- #1156: リモートユーザーのプロフィール「アクティビティ」タブが Heatmap 空 / Notes 折れ線がマイナス方面にのみ動く drop-in regression を fix。federation `handleCreate` / `handleAnnounce` で `OnNoteCreated` chart hook が発火しておらず PerUserNotesChart の +1 が記録されない一方、`handleDelete` 経路 (`NoteDeleteService.Delete`) は -1 を記録していたため日次集計が常に負方向に推移していた。`Resolver.IngestNoteWithCreated` (sibling) を追加して dedup-hit と新規 INSERT を区別し、`Processor.SetNoteChartHook` で chart hook を `safeGoFedHook` 経由で fire-and-forget 発火 (重複配送では fire しない)
- #1167: fluent-emoji 参照 path を `@misskey-dev/emoji-assets/built/fluent-emoji` に追従 (upstream 2026.5.2 #17381 残作業、Phase 18 #1164 commit 3 の twemoji 追従と同時にやるべきだった漏れ補完)。修正前は旧 path (submodule `fluent-emojis/dist`) が残っていたため mk-go だけ achievement 1/2/3 icon が表示できていた drop-in 違反状態。修正後は upstream Misskey TS 2026.5.4 と同じ broken state (= `passedSinceAccountCreated1/2/3` の bronze/silver/gold バッジ icon が 404) に揃え、frontend `achievements.ts` 側で keycap 系を新 path 存在 emoji に rename する upstream / fork fix を待つ運用に

### Phase 15 — Federation performance (#562, #565, #569)

- inbox handler を verify-in-worker 化 (#565): HTTP handler は body + signature header だけ payload に詰めて 202 即返し、signature verify / host block / instance touch / chart hook を inbox worker 側で実行。queue-bench で HTTP 受信 rps が **TS の 2.6-2.8x** 達成
- redundant `hydrateNoteForFanout` SELECT を skip (#569)
- `MarkRequestReceived` を per-host で 1s buffer に集約する `InstanceTouchBuffer` 導入
- fanoutHook / notificationHook を `safeGo` で async 化

### Phase 14 — Drop-in frontend e2e (#380, #381, #387, #394)

3 Misskey TS インスタンス + cypress 構成で frontend 視点の drop-in 互換を検証する基盤:

- Phase 14-1 (#381): 3 TS instance (A/B/C) + cypress runner + baseline smoke spec
- Phase 14-2 (#387): visibility / user_list / cross_instance_view / delete_note spec 拡充
- Phase 14-3 (#394): mk overlay (TS-A → mk-A 切替) で swap mode 動作確認、`CYPRESS_MODE=baseline|swap` で skip 制御
- nightly CI 19:00 UTC

### Phase 13 — Drop-in e2e (pytest, #365, #367, #372, #374)

Misskey TS 2 インスタンス + pytest で federation smoke + 状態継承を検証:

- Phase 13-1 (#365): TS-A / TS-B 2 stack + smoke
- Phase 13-2 (#367): mk-go overlay (TS-A backend を mk-A に差し替え) で state preservation 検証 + `migration/000039_dropin_compat.up.sql` で `note.pageCount` / `note.renoteChannelId` 補填
- Phase 13-3 (#372): state preservation 機能マトリクスを 6 シナリオに拡充
- Phase 13-4 (#374): nightly CI 18:00 UTC

### mkq driver + queue-bench (#571 audit, #563)

- `tests/queue-bench/` で BullMQ (TS) / asynq (mk-go) / mkq (mk-go) を 3-way 比較する基盤
- mkq driver を default 化 (asynq は legacy / future-deprecation candidate)
- 結果: mkq が deliver / inbox throughput とも最良

### docs/update — upstream 差分追跡 (#947, #948)

- `docs/update/20260500diff.md`: 2026.3.2 → 2026.5.0 backend diff (#947 で sub-task 化)
- `docs/update/20260501diff.md`: 2026.5.0 → 2026.5.1 backend diff
- 命名規則 `yyyymmdd*` で `docs/update/` に積み上げ運用

### Phase P3 — 補助エンドポイント + 欠損テーブル

- 欠損テーブル7種追加 (channel_favorite, clip_favorite, retention_aggregation等)
- 補助エンドポイント12+追加 (roles/notes, hashtags, gallery等)

### Phase P2 — 互換性修正 (#107サブissue)

- P2.1: パスワードリセット、MiAuth gen-token、App API、サインイン履歴
- P2.2: タイムラインフィルタリング、signin-flow captcha
- P2.3: NoteEntity/UserDetailedレスポンス完全化
- P2.4: AP MFM→HTML変換、attachment、context拡充

### Phase P1 — 第2次互換性修正 (#124サブissue)

- WebSocket 9チャンネル追加 (100%カバー)
- AP Inbox Block/Flag/Move/Add/Remove
- AP Accept完全実装 + Question(投票)受信
- users/lists update + update-membership
- trustProxyサポート
- エラーレスポンスUUID統一
- DBスキーマ欠損カラム追加
- WebSocketプロトコル改善 (OAuth2スコープ, pong応答)
- dbSlaves (リードレプリカ)サポート
- chart tables, social auth, AP拡張, Sentry

### Phase 11 — E2E + テストモード

- Cypress E2Eテスト基盤
- `/api/reset-db`テスト用エンドポイント

### Phase 10 — 管理機能

- admin/* 全エンドポイント実装

### Phase 9 — 認証 + ActivityPub

- 9.1: TOTP 2要素認証
- 9.2: リモートActivityPubオブジェクト解決

### Phase 1-8 — 基盤構築

- HTTPサーバー、DB/Redis接続、設定ローダー
- ユーザー、ノート、タイムライン、ドライブ
- フォロー、リアクション、通知
- WebSocketストリーミング
- ActivityPub送信/受信
- ジョブキュー (asynq)
- 検索 (Meilisearch/SQLフォールバック)
