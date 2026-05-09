# アーキテクチャ

## レイヤ構成

```
┌─────────────────────────────────────┐
│  api (HTTPハンドラ)                  │  ← Echo v4のルーティング
├─────────────────────────────────────┤
│  core (ビジネスロジック)              │  ← ドメインサービス
├─────────────────────────────────────┤
│  repository (データアクセス)          │  ← GORM + PostgreSQL
├─────────────────────────────────────┤
│  model (DBエンティティ)              │  ← GORMモデル定義
└─────────────────────────────────────┘
```

**依存方向**: api → core → repository → model の一方向のみ。逆方向の依存は禁止。

補助レイヤ:
- **entity** — レスポンス変換専用のDTO。ドメインロジックを入れない
- **activitypub** — coreから呼び出されるActivityPub実装
- **queue** — asynqによる非同期ジョブ処理
- **stream** — WebSocketストリーミング

## パッケージ構成

### `cmd/`

| パッケージ | 用途 |
|---|---|
| `cmd/misskey` | メインサーバーのエントリポイント |
| `cmd/migrate` | マイグレーションCLIツール |

### `internal/api/` (HTTPハンドラ、40パッケージ)

エンドポイント単位で分割。主要なもの:

| パッケージ | 対応エンドポイント |
|---|---|
| `admin` | `admin/*` — 管理API (accounts, roles, queue, relays等) |
| `notes` | `notes/*` — ノートCRUD、タイムライン |
| `users` | `users/*` — ユーザー情報、フォロー |
| `i` | `i/*` — 自アカウント操作 |
| `drive` | `drive/*` — ファイルアップロード・管理 |
| `streaming` | WebSocketストリーミング接続 |
| `inbox` | ActivityPub Inbox (SharedInbox + 個別) |
| `wellknown` | `.well-known/webfinger`, `.well-known/nodeinfo` |
| `signin` | ログインフロー (TOTP/WebAuthn/CAPTCHA対応) |
| `auth` | MiAuth/OAuth認証セッション |

未実装エンドポイントへのリクエストはAPIキャッチオールハンドラが`200 {}`で応答する。

### `internal/core/` (ビジネスロジック、36パッケージ)

ドメイン単位で分割。主要なもの:

| パッケージ | 責務 |
|---|---|
| `note` | ノート作成・削除・更新 |
| `user` | ユーザー作成・更新・削除 |
| `following` | フォロー・アンフォロー・リクエスト承認 |
| `reaction` | リアクション付与・削除 |
| `timeline` | Redis fanoutによるタイムライン配信 |
| `federation` | AP配信・受信・リモートオブジェクト解決、`RemoteStatsFetcher` (リモート users/show counts 取得、#943) |
| `drive` | ファイルストレージ (Local/S3)、画像変換 |
| `mediaproxy` | リモート画像のキャッシュ proxy。GIF/APNG はアニメ pass-through (#941)、AVIF/HEIC/JXL decode 等 |
| `urlpreview` | OG/Twitter/oEmbed プレビュー取得。`charset.NewReader` で Shift_JIS/EUC-JP 等の自動正規化 (#942) |
| `notification` | 通知生成・配信 |
| `chart` | 統計チャートエンジン (12エンジン) |
| `search` | 全文検索 (Meilisearch / SQLフォールバック) |
| `twofactor` | TOTP/WebAuthn認証 |
| `wordmute` | 単語ミュートのパース・キャッシュ (LRU、#790) |

### `internal/repository/` (データアクセス、46ファイル)

エンティティごとにインターフェース + GORM実装 + コンストラクタのパターン。

```go
type NoteRepository interface {
    FindByID(id string) (*model.Note, error)
    Create(note *model.Note) error
    // ...
}

func NewNoteRepository(db *gorm.DB) NoteRepository {
    return &noteRepository{db: db}
}
```

### `internal/model/` (DBモデル、46ファイル)

Misskey-TSのテーブルと1:1対応するGORM構造体。

### `internal/entity/` (レスポンスDTO)

`PackUserDetailed`、`PackUserLite`、`PackNote`等のmodel→JSONレスポンス変換関数を提供。

### `internal/activitypub/`

| ファイル | 責務 |
|---|---|
| `types.go` | ActivityStreams 2.0の型定義 |
| `renderer.go` | Goモデル → AP JSON-LD変換 (Person, Note, Activity) |
| `signature.go` | HTTP Signatures (RSA-SHA256) の署名・検証 |
| `client.go` | AP HTTPクライアント (署名付きfetch) |
| `jsonld.go` | `@context`構築、JSON-LD正規化 |
| `keypair.go` | RSA鍵ペアの生成・解析 |
| `mfm/` | MFM(Misskey Flavored Markdown) → HTML変換 |

### `internal/queue/` (ジョブキュー)

driver は `mkq` (BullMQ wire-compatible、デフォルト) または `asynq` (legacy)。設定 `jobQueueDriver` で切替 (#571 audit、#563 で 3-way bench 後 mkq を default 化)。`processors/`配下にタスク実装:

- `deliver` — AP配信 (リトライ付き)
- `inbox` — AP受信 (#565 で verify-in-worker 化、HTTP handler は 202 即返し)
- `webhook` — Webhook送信
- `webpush` — Web Push通知
- `emoji_import` — カスタム絵文字インポート
- `clean_remote_notes` — リモートノートクリーンアップ
- `reaction_flush` — リアクションバッファのDB書き込み
- `transfer` — アカウント移行

### `internal/safehttp/`

SSRF-safe HTTP transport を提供 (`NewSSRFSafeTransport`)。private IP / loopback / metadata service への接続を DNS resolve 段階で reject。`urlpreview` / `mediaproxy` / federation の `RemoteStatsFetcher` で共通利用。

### `internal/stream/` (WebSocket)

`channels/`配下にストリーミングチャンネル実装:

timeline, drive, notifications, chat_room, chat_user, reversi_game

### `internal/misc/`

- `id/` — IDジェネレータ (aidx, aid, meid, ulid, objectid)。デフォルトはaidx
- `smtp` — SMTP送信
- `random` — セキュアな乱数生成
- `notesummary` — ノート要約生成

## DI (依存性注入)

すべてのサービス生成と配線は`internal/server/router.go`の`setupRoutes()`で行う。

```
server.New()
  └→ setupRoutes()  (router.go, ~1500行)
      ├→ Repository生成 (userRepo, noteRepo, ... 30+個)
      ├→ Coreサービス生成 (noteCreateService, followingService, ...)
      ├→ フック注入 (後述)
      ├→ Handlerの生成とルート登録
      └→ WebSocket/静的ファイル設定
```

DIフレームワークは使わず、すべて手動の関数呼び出しで組み立てる。

## フック注入パターン

循環依存を避けつつクロスカッティングな処理を注入するパターン。`noteCreateService`を例に:

```
noteCreateService
  ├→ SetFanoutHook(timelineFanoutHook)       ← タイムライン配信
  ├→ SetFederationHook(noteDeliveryHook)      ← AP配信
  ├→ SetNotificationHook(notificationHook)    ← 通知生成
  ├→ SetWebhookHook(noteCreateWebhookHook)    ← Webhook発火
  ├→ SetChartHook(chartHooks)                 ← 統計更新
  └→ SetIndexHook(noteIndexHook)              ← 検索インデックス
```

フック一覧:
- **FanoutHook** — タイムラインへの配信
- **FederationHook** — ActivityPubリモート配信
- **NotificationHook** — 通知の生成と配信
- **WebhookHook** — Webhook/SystemWebhook発火
- **ChartHook** — チャート統計の更新
- **IndexHook** — 検索インデックスの更新/削除
- **BlockingChecker** — ブロック判定 (フォロー、リアクション時)

## Redis分離設計

Misskeyは用途別に複数のRedis接続を持つ。設定で同一ホストを指しても論理的に分離される。

| 用途 | 設定キー | デフォルト |
|---|---|---|
| 汎用キャッシュ | `redis` | (必須) |
| PubSub | `redisForPubsub` | `redis`にフォールバック |
| ジョブキュー | `redisForJobQueue` | `redis`にフォールバック |
| タイムライン | `redisForTimelines` | `redis`にフォールバック |
| リアクション | `redisForReactions` | `redis`にフォールバック |

## 設定解決フロー

```
.config/default.yml
       ↓ Viper読み込み
  Source構造体 (YAMLの生値)
       ↓ resolve()
  Config構造体 (解決済み: URL→Scheme/Host分解、ポートデフォルト等)
       ↓
  各サービスに注入
```

環境変数オーバーライド: `MK_`プレフィックス付きの変数で設定値を上書き可能 (例: `MK_DB_HOST`)。詳細は[設定リファレンス](configuration.md)参照。

## マイグレーション

`migration/`ディレクトリにgolang-migrate用のSQLファイルを配置 (000001 ~ 000047、2026-05-09 時点)。

TS版Misskeyの既存テーブルには追加のみで破壊的変更を行わない。Go固有のテーブル追加とカラム追加はすべてIF NOT EXISTS付き。drop-in テスト (#367) で発見した補完カラム (`note.pageCount` / `note.renoteChannelId`) は `000039_dropin_compat.up.sql` で追加済。

```bash
make migrate-up      # 最新まで適用
make migrate-down    # 1段階ロールバック
make migrate-create  # 新規マイグレーション作成
```
