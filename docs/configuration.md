# 設定リファレンス

## 設定ファイル

設定ファイルはMisskey互換のYAML形式。CLIフラグで指定する:

```bash
./built/misskey -config .config/default.yml
```

### 初回セットアップ

`.config/` 配下には Misskey 本家流儀で `.example` テンプレートを配布している。初回は以下のいずれかを手元の `.config/default.yml` (および `docker.yml`) として複製してから編集する:

```bash
# ローカル開発 / バイナリ直起動
cp .config/default.yml.example .config/default.yml

# Docker / docker-compose
cp .config/docker.yml.example .config/docker.yml
```

`.config/default.yml` と `.config/docker.yml` は `.gitignore` 対象 (operator-local) なので、コピーした手元のファイルが履歴に混入することはない。Docker image は build 時に `.config/docker.yml.example` をコンテナ内の `/app/.config/default.yml` として焼き込むので、運用環境では docker-compose 等で実 config を volume mount で上書きする想定。

## 全設定項目

### 基本設定

| キー | 型 | デフォルト | 説明 |
|---|---|---|---|
| `url` | string | (必須) | サーバーの公開URL (例: `https://misskey.example.com`) |
| `port` | int | `3000` | HTTPポート |
| `socket` | string | - | UNIXドメインソケットパス (設定するとTCPの代わりにUDSでリッスン) |
| `chmodSocket` | string | - | UDSファイルのパーミッション (例: `"770"`) |
| `id` | string | `"aidx"` | IDジェネレータ (`aidx`, `aid`, `meid`, `ulid`, `objectid`) |
| `setupPassword` | string | - | 初期セットアップ時のパスワード |
| `disableHsts` | bool | `false` | HSTSヘッダーを無効化 |
| `enableIpRateLimit` | bool | `true` | IPベースのレート制限を有効化 |
| `disableEndpointRateLimits` | bool | `false` | per-endpoint rate limit table 全体を無効化。Misskey TS の `NODE_ENV=development` 相当で、ベンチマーク等で公正比較する用途専用。**本番で絶対に使わない** |
| `pidFile` | string | - | PIDファイルパス |
| `testMode` | bool | `false` | テスト用エンドポイント(/api/reset-db)を有効化。**本番で絶対に使わない** |
| `enablePprof` | bool | `false` | `/debug/pprof/*` ハンドラを公開。ローカルプロファイリング専用。**本番で絶対に使わない**。`MK_ENABLEPPROF`で上書き可。 |
| `enableMetrics` | bool | `false` | Prometheus `/metrics` エンドポイントを公開。job queue 系 metric (`mk_job_workers_active` / `mk_job_queue_pending` / `mk_job_dispatch_wait_seconds` / `mk_job_processing_seconds` / `mk_job_scale_events_total` / `mk_job_scrape_errors_total`) を expose。認証無しで公開されるため、外部公開する場合は nginx / LB ACL で access 制限すること。詳細は `docs/design/auto-scale-job-workers.md` §6.1。`MK_ENABLEMETRICS`で上書き可。**注: `jobQueueDriver: asynq` 使用時、`mk_job_workers_active` は asynq の単一 pool 構造のため全 queue label が同値 (pool-wide concurrency) を返す。mkq driver は per-queue 実値を返す。** |
| `jobQueueDriver` | string | `"mkq"` | ジョブキュー実装の選択。`mkq` (デフォルト、推奨) または `asynq` (legacy)。`mkq` は BullMQ wire-compatible で admin queue 画面が Misskey TS frontend 前提のまま動く + per-queue concurrency / rate-limit が効く。`asynq` は **将来削除予定** (mkq の安定性確保後) のため新規 deploy は `mkq` 推奨。`MK_JOBQUEUEDRIVER`で上書き可。 |

### データベース (`db.*`)

| キー | 型 | デフォルト | 説明 |
|---|---|---|---|
| `db.host` | string | `"localhost"` | ホスト (`/`始まりならUNIXソケット) |
| `db.port` | int | `5432` | ポート |
| `db.db` | string | `"misskey"` | データベース名 |
| `db.user` | string | `"misskey"` | ユーザー名 |
| `db.pass` | string | - | パスワード |
| `db.disableCache` | bool | `false` | GORMのキャッシュ無効化 |
| `db.extra.ssl` | bool | `false` | SSL接続 |

`dbReplications: true`とすると`dbSlaves`設定でリードレプリカを使用可能。

### Redis (`redis.*`)

基本のRedis設定。全フィールドは各用途別Redis設定にも適用可能。

| キー | 型 | デフォルト | 説明 |
|---|---|---|---|
| `redis.host` | string | `"localhost"` | ホスト |
| `redis.port` | int | `6379` | ポート |
| `redis.pass` | string | - | パスワード |
| `redis.db` | int | `0` | データベース番号 |
| `redis.username` | string | - | ユーザー名 (Redis 6+ ACL) |
| `redis.prefix` | string | - | キープレフィックス |

### 用途別Redis

設定しない場合は`redis`にフォールバック。

| キー | 用途 |
|---|---|
| `redisForPubsub.*` | PubSub (イベント配信) |
| `redisForJobQueue.*` | ジョブキュー (asynq) |
| `redisForTimelines.*` | タイムラインキャッシュ |
| `redisForReactions.*` | リアクションバッファ |

### ジョブキュー制御

| キー | 型 | デフォルト | 説明 |
|---|---|---|---|
| `deliverJobConcurrency` | int | `16` | AP配信worker数。mkq driver では deliver queue 専用、asynq driver では**総 worker pool 上限**として扱う (asynq は per-queue concurrency を持たないため、この値は queue 共通の上限) |
| `inboxJobConcurrency` | int | - | Inbox処理 worker 数 (#534 で非同期化済)。mkq driver では inbox queue 専用 worker。asynq driver では**現状 no-op** (asynq の queue priority weight も静的 1 固定で wire していないため、共有 worker pool 内の inbox tasks は他 queue と equal-weight で競合する) |
| `relationshipJobConcurrency` | int | - | フォロー処理 worker 数 (mk-go は relationship queue を持たないため**現状 no-op**) |
| `deliverJobPerSec` | int | - | AP配信レート上限 (tasks/sec)。設定すると asynq middleware / mkq.WithRateLimit で worker dispatch が back-pressure される |
| `inboxJobPerSec` | int | - | Inbox処理レート上限 (tasks/sec) (#534)。設定すると asynq middleware / mkq.WithRateLimit で worker dispatch が back-pressure される |
| `relationshipJobPerSec` | int | - | mk-go では**現状 no-op** (上記同様) |
| `deliverJobMaxAttempts` | int | driver 既定 | AP配信の**総試行回数** (初回 + retry) のdefault。BullMQ `attempts` と同じ意味で、TS Misskey YAML 互換。EnqueueDeliver で `WithMaxRetry` 未指定時にだけ適用される (#495)。例: `deliverJobMaxAttempts: 8` で 1 回失敗するごとに retry されて最大 8 回試行 |
| `inboxJobMaxAttempts` | int | driver 既定 | Inbox処理の**総試行回数** (#534)。BullMQ `attempts` と同じ意味で、TS Misskey YAML 互換。EnqueueInbox で `WithMaxRetry` 未指定時にだけ適用される |

> **driver 間の差分**:
> - `asynq` driver は worker pool が共有なので `deliverJobConcurrency` は **総 concurrency** として扱われる。queue 間の priority weight は全 queue 静的 1 で固定 (deliver / inbox / push / export / webhook / maintenance すべて equal-weight)。
> - `mkq` driver は queue ごとに worker を分けているので `deliverJobConcurrency` / `inboxJobConcurrency` はそれぞれの queue 専用 worker 数として扱われる。明示指定の無い queue は `Concurrency / len(queues)` の既定値を使う。
> - **per-queue concurrency tuning は `mkq` driver でしか効かない**。asynq で inbox / deliver を独立に絞りたい場合は `mkq` 推奨。
>
> **rate limit (`*JobPerSec`) の挙動差**:
> - `asynq`: handler middleware で `golang.org/x/time/rate.Limiter.Wait` する設計。共有 worker pool で動くため、レート制限中の deliver タスクが多数 pending していると worker が `Wait` で寝てしまい、他 queue (push / export / webhook / maintenance) のタスクが starvation する可能性あり。これは asynq に per-queue pull-rate 制御 API が無いことに起因する根源的制約。
> - `mkq`: `mkq.WithRateLimit` で **worker pull レイヤ** に制御が入るため、レート制限が他 queue の処理を阻害しない。
> - **本格的に rate limit を運用するなら `mkq` driver を推奨**。
>
> **`mkq` driver の rate limit は per-Worker (#1124)**:
> - mk-go の `mkq` driver は queue ごとに **N 個の `mkq.Worker`** を起動する pool-of-Workers 構造で運用される (auto-scale (#1120) と queue 単位 dynamic Resize を実現するため)。
> - `mkq.WithRateLimit` は **個々の Worker に独立に適用** されるため、`deliverJobConcurrency: N` + `deliverJobPerSec: rl` の組み合わせでは **合計 dispatch rate = N × rl** になる。
> - 例: `deliverJobConcurrency: 4` + `deliverJobPerSec: 100` → 実 dispatch 上限は **400 jobs/sec** (= 4 × 100)、設定値 100 ではない。
> - 起動時に該当条件 (rl > 0 かつ concurrency > 1) で `slog.Warn` で effective rate を通知する。auto-scale (#1125) 配線後は Resize 時にも同 effective rate が再計算される。
> - 連合先に rate limit を厳密に守る運用が必要な場合は `deliverJobPerSec` を `<合計目標> / <deliverJobConcurrency>` で割って設定すること。

### メディア

ローカルストレージ (S3未設定時) のファイル保存先は`./drive-files`固定。Docker環境ではコンテナ内の`/app/drive-files`にボリュームマウントが必要。

| キー | 型 | デフォルト | 説明 |
|---|---|---|---|
| `maxFileSize` | int | `262144000` (250MB) | アップロードファイルの最大サイズ (バイト) |
| `mediaProxy` | string | - | メディアプロキシURL |
| `mediaProxySecret` | string | - | メディアプロキシの署名シークレット |
| `videoThumbnailGenerator` | string | - | 動画サムネイル生成サービスURL |

### ネットワーク

| キー | 型 | デフォルト | 説明 |
|---|---|---|---|
| `proxy` | string | - | 外向き HTTP のプロキシ URL (PR #485) |
| `proxySmtp` | string | - | SMTP 配送のプロキシ URL。`http://host:port` (HTTP CONNECT)、`https://host:port`、`socks5://[user:pass@]host:port` (#496) |
| `proxyBypassHosts` | []string | - | プロキシを迂回するホスト (HTTP のみ) |
| `allowedPrivateNetworks` | []string | - | プライベート IP / loopback / metadata service へのアウトバウンド接続を許可する CIDR allowlist。AP fetch / URL preview / mediaproxy / `RemoteStatsFetcher` (#943) で共通に効く。開発時の self-loop 用途 (`127.0.0.0/8` 等)、本番では空のまま運用する |
| `outgoingAddress` | string | - | 外向き HTTP の送信元 IP として bind するアドレス。複数 NIC 環境で federation 配信の source IP を固定する用途 (#496)。不正値は警告のみで kernel auto-pick に fallback |
| `outgoingAddressFamily` | string | `dual` | DNS 解決後の IP family 制限。`"ipv4"` / `"ipv6"` 指定で該当 family のみで dial、`"dual"` または空で両方 (#496) |

### 検索

| キー | 型 | 説明 |
|---|---|---|
| `fulltextSearch.provider` | string | 検索プロバイダ名。`sqlLike` (デフォルト) / `sqlPgroonga` / `meilisearch` |
| `meilisearch.host` | string | Meilisearchホスト |
| `meilisearch.port` | int | Meilisearchポート |
| `meilisearch.apiKey` | string | APIキー |
| `meilisearch.ssl` | bool | SSL接続 |
| `meilisearch.index` | string | インデックス名 |
| `meilisearch.scope` | string | 検索スコープ |

provider 別の挙動:

- `sqlLike` (デフォルト) — `text ILIKE '%q%'` で全文検索する。追加の DB 拡張は不要だが、note 行数が増えると線形スキャンになるため大規模インスタンスでは遅くなる。
- `sqlPgroonga` — PGroonga の `&@~` 演算子で全文検索する。日本語を含む高速な転置 index 検索が利用可能。**事前に PostgreSQL 側で pgroonga 拡張のインストールと note.text への index 作成が必要** (下記)。
- `meilisearch` — 別プロセスの Meilisearch にノートを index する。`meilisearch.host` 必須。未設定時は自動的に `sqlLike` にフォールバックする。

#### PGroonga セットアップ

`sqlPgroonga` を使うには PostgreSQL に PGroonga 拡張が導入されている必要がある (公式 image には含まれていない)。インストール手順は [pgroonga.github.io](https://pgroonga.github.io/install/) を参照。導入後、各データベースで一度だけ拡張を有効化し、note.text に index を貼る:

```sql
-- DB ごとに 1 回だけ
CREATE EXTENSION IF NOT EXISTS pgroonga;

-- note.text 用の全文検索 index
CREATE INDEX IF NOT EXISTS pgroonga_note_text_idx ON note USING pgroonga (text);
```

mk-go 側のマイグレーションには含めていない。pgroonga 拡張の有無は運用環境依存のため、operator が明示的に install/index 作成する責務とする (upstream Misskey TS と同じ方針)。

### パフォーマンス

| キー | 型 | デフォルト | 説明 |
|---|---|---|---|
| `perChannelMaxNoteCacheCount` | int | `1000` | チャンネルあたりのノートキャッシュ上限 |
| `perUserNotificationsMaxCount` | int | `500` | ユーザーあたりの通知キャッシュ上限 |
| `deactivateAntennaThreshold` | int | - | アンテナ非活性化の閾値 |

### ロギング

| キー | 型 | 説明 |
|---|---|---|
| `logging.sql.disableQueryTruncation` | bool | SQLログのクエリ切り詰めを無効化 |
| `logging.sql.enableQueryParamLogging` | bool | SQLログにパラメータ値を含める |

## 環境変数オーバーライド

`MK_`プレフィックス付きの環境変数で設定値を上書きできる。ネストキーは`_`区切り。

| 環境変数 | 対応YAMLキー |
|---|---|
| `MK_URL` | `url` |
| `MK_PORT` | `port` |
| `MK_SOCKET` | `socket` |
| `MK_DB_HOST` | `db.host` |
| `MK_DB_PORT` | `db.port` |
| `MK_DB_DB` | `db.db` |
| `MK_DB_USER` | `db.user` |
| `MK_DB_PASS` | `db.pass` |
| `MK_REDIS_HOST` | `redis.host` |
| `MK_REDIS_PORT` | `redis.port` |
| `MK_REDIS_PASS` | `redis.pass` |
| `MK_REDIS_DB` | `redis.db` |
| `MK_REDIS_USERNAME` | `redis.username` |
| `MK_ID` | `id` |
| `MK_MAXFILESIZE` | `maxFileSize` |
| `MK_MEDIAPROXYSECRET` | `mediaProxySecret` |
| `MK_TESTMODE` | `testMode` |
| `MK_DISABLEENDPOINTRATELIMITS` | `disableEndpointRateLimits` |

用途別Redisも同様 (例: `MK_REDISFORPUBSUB_HOST`)。

新しいオーバーライド対象を追加する場合は`internal/config/config.go`の`bindEnvKeys()`にキーを追加する。

## フロントエンド関連環境変数

| 環境変数 | 用途 |
|---|---|
| `MISSKEY_FRONTEND_DIR` | フロントエンドのルートディレクトリ |
| `MISSKEY_FRONTEND_DIST_DIR` | ビルド済みフロントエンドディレクトリ |
| `MISSKEY_TWEMOJI_DIR` | Twemojiアセットディレクトリ |
| `MISSKEY_CLIENT_ASSETS_DIR` | クライアントアセットディレクトリ |
| `MISSKEY_STATIC_DIR` | 静的ファイルディレクトリ (backend/assets: favicon等) |
| `MISSKEY_REPO_ASSETS_DIR` | リポジトリ直下アセット (ai.png, banner等) |

## テスト用環境変数

CIでのテスト実行時に使用。ローカルではtestcontainersが自動起動するため通常不要。

| 環境変数 | 用途 |
|---|---|
| `TEST_DB_HOST` | テスト用PostgreSQLホスト |
| `TEST_DB_PORT` | テスト用PostgreSQLポート |
| `TEST_DB_NAME` | テスト用データベース名 |
| `TEST_DB_USER` | テスト用ユーザー名 |
| `TEST_DB_PASS` | テスト用パスワード |
| `TEST_DB_SSLMODE` | テスト用SSLモード |
| `TEST_REDIS_HOST` | テスト用Redisホスト |
| `TEST_REDIS_PORT` | テスト用Redisポート |

## マイグレーション用環境変数

| 環境変数 | 用途 |
|---|---|
| `DATABASE_URL` | `make migrate-up/down`で使用するPostgreSQL接続文字列 |

## 設定例

### 最小構成

```yaml
url: http://localhost:3000
port: 3000
db:
  host: localhost
  port: 5432
  db: misskey
  user: misskey
  pass: misskey
redis:
  host: localhost
  port: 6379
id: aidx
```

### 本番構成例

```yaml
url: https://misskey.example.com
port: 3000
db:
  host: db.internal
  port: 5432
  db: misskey
  user: misskey
  pass: strong-password
  extra:
    ssl: true
redis:
  host: redis.internal
  port: 6379
  pass: redis-password
redisForJobQueue:
  host: redis-jobs.internal
  port: 6379
  pass: redis-password
  db: 1
meilisearch:
  host: search.internal
  port: 7700
  apiKey: your-api-key
  index: misskey
id: aidx
maxFileSize: 524288000
```
