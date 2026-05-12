# CLAUDE.md

このファイルは、このリポジトリで作業する際にClaude Codeが参照するプロジェクト固有のガイドラインです。

本プロジェクトはMisskey（TypeScript/NestJS製の分散型SNS）をGoで書き換えるリライトプロジェクトです。オリジナルMisskeyとのAPI互換性・ActivityPub連合互換性の維持を最優先とします。

タスク管理はGitHub Issues / Pull Requestsで行います（詳細はSection 7）。

## 1. 技術スタック

### コア

| Component | Library | 用途 |
|-----------|---------|------|
| 言語 | **Go 1.26** | `go.mod`でバージョン管理 |
| Webフレームワーク | **Echo v4** (`labstack/echo/v4`) | HTTPルーティング、ミドルウェア、WebSocket |
| ORM | **GORM** (`gorm.io/gorm`) | PostgreSQLアクセス |
| Migration | **golang-migrate** (`golang-migrate/migrate/v4`) | SQLベースのマイグレーション |
| Config | **Viper** (`spf13/viper`) | YAML + 環境変数オーバーライド |
| Logging | **slog** (標準ライブラリ) | 構造化ロギング |

### インフラ

| Component | Library | 用途 |
|-----------|---------|------|
| PostgreSQL Driver | **pgx/v5** (`jackc/pgx/v5`) | PostgreSQL接続 |
| Redis | **go-redis v9** (`redis/go-redis/v9`) | キャッシュ、PubSub |
| Job Queue | **asynq** (`hibiken/asynq`) | Redisベースのジョブキュー（BullMQ相当） |
| Search | **meilisearch-go** | Meilisearch連携 |
| Object Storage | **aws-sdk-go-v2/s3** | S3互換ストレージ |

### 連合 / ActivityPub

- **HTTP Signatures**: 自前実装（`internal/activitypub/`）
- **JSON-LD**: 必要に応じてカスタム実装
- **ActivityStreams Types**: カスタム構造体

### 認証

- **bcrypt** (`golang.org/x/crypto/bcrypt`) - パスワードハッシュ
- **pquerna/otp** - TOTP（2FA）
- **golang-jwt/jwt/v5** - JWTトークン

### テスト

- **testing** (標準) + **testify** (`stretchr/testify`)
- **testcontainers-go** - 実PostgreSQL/Redisを使った統合テスト
- 単体テストでは`internal/testutil/`のモックを使用

## 2. Project Structure

```
/
├── cmd/
│   ├── misskey/            # メインバイナリのエントリポイント
│   └── migrate/            # マイグレーションCLIツール
├── internal/
│   ├── config/             # 設定ローダー（Misskey YAML互換）
│   ├── server/             # HTTPサーバーのセットアップ、ルーティング、ミドルウェア
│   ├── api/                # APIハンドラ（エンドポイント単位でサブディレクトリ）
│   │   ├── admin/          # admin/* 管理API
│   │   ├── ap/             # ap/* ActivityPub解決API
│   │   ├── auth/           # auth/* 認証API
│   │   ├── notes/          # notes/* ノート関連API
│   │   ├── users/          # users/* ユーザー関連API
│   │   ├── i/              # i/* 自アカウントAPI
│   │   ├── drive/          # drive/* ファイル管理API
│   │   ├── federation/     # federation/* 連合情報API
│   │   └── ...             # その他エンドポイント群
│   ├── core/               # ビジネスロジック層（サービス）
│   ├── activitypub/        # ActivityPub実装（Inbox、Deliver、Renderer、Resolver、HTTP署名）
│   ├── model/              # DBモデル（GORM、Misskeyエンティティ対応）
│   ├── repository/         # データアクセス層
│   ├── queue/              # ジョブキュー（asynq）とプロセッサ
│   ├── stream/             # WebSocketストリーミング（チャンネル実装）
│   ├── entity/             # レスポンス用DTO（シリアライゼーション）
│   ├── misc/               # ユーティリティ（ULID生成等）
│   └── testutil/           # テスト用ヘルパー（testcontainers、モック）
├── migration/              # golang-migrate用SQLファイル（`NNNNNN_name.up.sql` / `.down.sql`）
├── .config/                # 設定ファイル（Misskey互換YAML）
│   ├── default.yml.example # ローカル開発用テンプレート (track 対象)
│   ├── docker.yml.example  # Docker Compose用テンプレート (track 対象)
│   ├── default.yml         # operator-local (gitignored)
│   └── docker.yml          # operator-local (gitignored)
├── docs/                   # プロジェクトドキュメント
├── Makefile
├── Dockerfile
├── docker-compose.yml
└── go.mod                  # Moduleパス: github.com/shiroha-a/mk
```

レイヤ責務：
- **api** → **core** → **repository** → **model** の順に依存。逆向きの依存は禁止。
- **entity**はレスポンス変換専用。ドメインロジックを入れない。
- **activitypub**は`core`から呼び出され、連合処理を担う。

## 3. Development Commands

すべて`Makefile`経由で実行できます。

```bash
# ビルド
make build                  # ./built/misskey に実行ファイル生成
make dev                    # go run で直接起動（開発用）
make run                    # build + 実行

# 依存管理
make tidy                   # go mod tidy

# コード品質
make fmt                    # gofmt -s -w . で整形
make lint                   # go vet ./...

# テスト
make test                   # go test ./... -v

# マイグレーション（DATABASE_URL環境変数が必要）
make migrate-up             # 最新まで適用
make migrate-down           # 1段階ロールバック
make migrate-create         # 新規マイグレーションファイル作成（プロンプト対話）

# Docker
make docker-build
make docker-up              # docker compose up -d
make docker-down

# Drop-in e2e (#364 / #365) — Misskey TS 2 インスタンスを立ち上げて
# TS ↔ mk 切替互換性を検証する基盤。詳細は docs/dropin-e2e.md。
make dropin-up              # TS-A / TS-B stack 起動
make dropin-test            # pytest smoke test 実行
make dropin-down            # stack + volume 全削除

# Drop-in mk overlay + swap test (#367) — instance A の backend を mk-go に
# 差し替える e2e シナリオ。
make dropin-mk-up           # base + mk overlay (clean DB から mk-A 起動)
make dropin-mk-test         # mk-A に対する smoke test
make dropin-mk-down         # cleanup
make dropin-swap-test       # TS-then-mk 切替シナリオ (bash orchestrator)

# Drop-in frontend e2e (#380 / Phase 14) — 3 Misskey TS インスタンス + cypress
# 実ブラウザでフロントエンド視点の drop-in 互換を検証する基盤。
make dropin-frontend-baseline    # TS-A/B/C + cypress baseline spec 実行
make dropin-frontend-up          # stack だけ立ち上げ (手動デバッグ用)
make dropin-frontend-down        # volume ごと cleanup
make dropin-frontend-swap-test   # TS-A → mk-A 切替まで含む end-to-end (Phase 14-3)
make dropin-frontend-mk-up       # mk overlay だけ立ち上げ (clean DB の mk-A から起動)
make dropin-frontend-mk-down     # mk overlay cleanup
```

エントリポイント：
- メインサーバー: `./cmd/misskey -config .config/default.yml`
- マイグレーション: `./cmd/migrate -direction up`

## 4. Testing

### 基本方針

- 新規機能追加時は**必ずテストを追加**する。
- CIでは**パッケージごとにカバレッジ閾値**を強制する。原則は以下だが、例外パッケージは個別に緩和閾値を設けている：
  - **最低ライン: 90%** — CIゲート。これを下回るとマージ不可。
  - **推奨ライン: 95%** — 通常のPRではここを目指す。
  - **目標ライン: 100%** — 新規パッケージや小規模パッケージでは積極的に狙う。
  - 例外パッケージ：
    - `internal/api/admin`: 80%以上 — `handler_stubs.go`にSMTP/queue/DB集計等の外部依存が多く90%未到達。現状83.8%で小マージン確保のため80%にロック
    - `internal/testutil`: 0% — mock/test helper専用パッケージ。production codeではなく他テストから呼ばれるだけなのでe2eと同様に閾値対象外
    - `internal/server`: 0% — 大部分が`router.go`のwire層 (handler配線/middleware設定) で、e2e/drop-in test経由で実挙動検証する設計。個別handlerファイル (`avatar.go` / `identicon.go`等) は`_test.go`単体で90%相当をカバーする運用は維持するが、`router.go`のウェイトでpackage全体が数%に張り付くためe2eと同様に閾値対象外 (#462)
- テストファイルは対象と同じパッケージに`_test.go`サフィックスで配置。

### 実行方法

```bash
# 全テスト実行（verbose）
make test

# 特定パッケージ
go test ./internal/api/notes/...

# レース検出 + カバレッジ（CIと同じ条件）
go test -race -count=1 -timeout 10m \
  -coverprofile=coverage.out -covermode=atomic ./...

# カバレッジ閲覧
go tool cover -html=coverage.out
```

### 統合テスト

- `internal/testutil/containers.go`がtestcontainers-goでPostgreSQL/Redisを起動する。
- ローカル実行にはDocker環境が必要。
- CIではGitHub Actionsの`services`でPostgreSQL 16 / Redis 7を起動し、以下の環境変数でDBへ接続：
  - `TEST_DB_HOST`, `TEST_DB_PORT`, `TEST_DB_NAME`, `TEST_DB_USER`, `TEST_DB_PASS`, `TEST_DB_SSLMODE`
  - `TEST_REDIS_HOST`, `TEST_REDIS_PORT`

### モック

- `internal/testutil/`配下にRepository、Drive、Block/Muteなどのモック実装がある。
- 単体テストではモックを使い、統合テストでは実DBを使う。DBをモックしないこと。

## 5. Coding Style

### 基本

- **gofmt**（`gofmt -s -w .`）で整形すること。CIで`gofmt -s -d .`による差分チェックが走る。
- **go vet**を通すこと。CIで強制。
- 命名はGoの標準慣習に従う（`camelCase`/`PascalCase`、略語は全て大文字：`URL`, `ID`, `API`）。
- Early returnを優先し、ネストを浅く保つ。
- エラーは`fmt.Errorf("context: %w", err)`でラップする。

### コメントとドキュメント

ユーザーグローバルルール（`~/.claude/CLAUDE.md`）に準拠：

- **英語で書くもの**：
  - GoDoc（関数/型/パッケージのドキュメンテーションコメント）
  - テストケースの`name`フィールド等、コード内のメタ情報
- **日本語で書くもの**：
  - 実装の背景・理由を説明する**インラインコメント**（なぜこの設計か、どんな罠があるか）
- **書かない**：
  - 自明な処理の説明コメント
  - `// TODO`や`// XXX`の乱用
  - 絵文字（全面禁止）

例：

```go
// CreateNote persists a new note and publishes events to subscribers.
// Returns ErrNoteSizeExceeded if content exceeds the configured limit.
func (s *Service) CreateNote(ctx context.Context, input CreateInput) (*model.Note, error) {
    // Misskeyオリジナル実装では空文字列も許容されるが、
    // ファイル添付もない場合は投稿として無効なためここで弾く
    if input.Text == "" && len(input.FileIDs) == 0 {
        return nil, ErrEmptyNote
    }
    ...
}
```

### 日本語の書式

- 日本語の中では不要な半角スペースを入れない。
  - ◯ `Claude Code入門`
  - × `Claude Code 入門`

## 6. Key Conventions

### Misskey互換性

- **API互換性が最優先**。レスポンスのフィールド名・型・エラーコードはオリジナルMisskeyと一致させる。
- バージョン文字列は`internal/config/config.go`の`Version`定数で管理し、対応するMisskeyバージョンに合わせる（現在: `2026.3.2`）。
- User-Agentは`mk-go/<version> (<url>)`形式 (#774 で `Misskey-Go/<ver>` から rename)。

### ID生成

- デフォルトIDジェネレータは`aidx`（設定ファイルで指定）。
- `internal/misc/id/`のジェネレータを使用し、モデルから直接`uuid`を呼ばない。

### エラーハンドリング

- APIレスポンスのエラーはMisskey互換のエラーコード・IDを返す（例: `NO_SUCH_NOTE`, 特定UUID）。
- 内部エラーは`slog`で構造化ログに記録、ユーザーには汎用メッセージを返す。

### Redisインスタンス分離

Misskeyは用途別に複数のRedis接続を持つ（`default`, `pubsub`, `jobQueue`, `timelines`, `reactions`）。設定で同じエンドポイントに向けられていても、コード上は用途ごとに別クライアントとして扱うこと。

### ActivityPub

- すべての送信リクエストにHTTP Signatureを付与する。
- リモートオブジェクト取得は`internal/activitypub/resolver.go`経由で行い、キャッシュを活用する。
- `allowedPrivateNetworks`設定を尊重し、プライベートIPへの直接アクセスを防ぐ。

## 7. Git Workflow

複数人での開発を前提とし、タスク管理はGitHub Issues、実装の取り込みはPull Requestで行う。

### Issue駆動ワークフロー

すべての作業は**対応するissueを先に作成**してから着手する。

- **Issueタイトル形式**: `Phase〇 <内容>`
  - 例: `Phase 10 管理機能`
- **Phaseが複数のサブフェーズに分かれる場合**、サブフェーズごとに個別のissueを立てる。
  - 例: Phase 10が4段階に分かれるなら、`Phase 10-1 <内容>`, `Phase 10-2 <内容>`, `Phase 10-3 <内容>`, `Phase 10-4 <内容>`の4つを作成する。
- **Issue本文**に含める項目：
  - 背景・目的
  - 実装する機能の詳細（作業内容を細かく記述）
  - 影響範囲
  - 完了条件（チェックリスト推奨）
  - 関連する設計ドキュメント・issueへの参照

Issueの作成・操作には`gh`コマンドを使う（`gh issue create`, `gh issue list`等）。

### ブランチ戦略

- `main`: リリースブランチ
- `develop`: 開発ブランチ。フィーチャーブランチのマージ先
- 作業はissueごとに**フィーチャーブランチ**を切って行う
  - ブランチ名例: `feature/phase-10-1-<要約>` / `fix/<対象>-<要約>`
- リモート破壊的操作（`push --force`、`reset --hard`など）は明示的な指示がない限り実行しない

### コミット

- コミット前には`make fmt && make lint && make test`を通すこと
- Claudeは**コミットを自動作成しない**。ユーザーが明示的に指示した場合のみコミットを作成する
- コミットメッセージは既存の履歴に倣う（例: `Phase 9.2: Remote ActivityPub object resolution`、`Fix CI: twofactor coverage 80% -> 100%`）
- Phase単位の機能追加は`Phase N.M: <要約>`、修正は`Fix <対象>: <要約>`の形式が一般的

### Pull Request

- 実装が完了したらPRを作成し、**必ず対応するissueをcloseする**
  - PR本文に`Closes #<issue番号>`を記載すると、マージ時にissueが自動closeされる
- タイトル・本文フォーマット：
  - **タイトル**: `Phase〇 <内容>` または作業の簡潔な要約
  - **Summary**: 変更の概要と目的
  - **主な変更点**: 変更ファイルの要約、注意点
  - **テスト**: 通ったテスト、追加したテスト、実行方法
  - **Closes**: `Closes #<issue番号>`
  - **その他**: 特記事項
- PR作成は`gh pr create`を使う

## 8. CI/CD

`.github/workflows/ci.yml`で以下のジョブが`main`と`develop`への push/PR で実行されます。

### `build`ジョブ

- `go build ./...`で全パッケージのビルド確認。

### `test-shards`ジョブ + `test` aggregator

- **4-way matrix shard** で並列実行する `test-shards` (`shard: [1,2,3,4]`)。各shardは
  独立したPostgreSQL 16 Alpine / Redis 7 Alpine サービスコンテナを持つ。
- テスト対象は`go list`で絞り込み（テストファイルがあるパッケージのみ）した上で
  `awk 'NF'`で空行除外→ImportPath順にソート→`NR % 4`で各shardに均等割り当て。
  新規パッケージ追加でshard内の構成が変わっても、決定的な分配により再現性は保たれる。
- 実行条件: `-race -count=1 -timeout 10m -coverprofile=coverage-shard-N.out -covermode=atomic`
- **カバレッジ閾値チェック** (各shard内で実行)：
  - `internal/api/admin`配下: 80%以上（SMTP/queue/DB集計等の外部依存で90%未到達のため暫定緩和）
  - `e2e`配下: 0%
  - `internal/testutil`: 0%（mock/test helper専用、production codeを含まないためe2eと同様扱い）
  - `internal/server`: 0%（router.goのwire層中心、e2e/drop-in test経由で実挙動検証する設計のため。個別handlerは`_test.go`で個別カバー）
  - それ以外のパッケージ: 90%以上
  - shard内のいずれかのパッケージが閾値未達なら、そのshardが失敗する。
- カバレッジレポートは`coverage-shard-N`アーティファクトとして各shardからアップロード。
- `test` job は `needs: test-shards / if: always()` で全shardを束ね、ブランチ保護が
  要求する `test` という名前の単一checkを公開する。いずれかのshardが失敗したら
  `needs.test-shards.result != 'success'` で `exit 1`。

### `lint`ジョブ

- `go vet ./...`
- `gofmt -s -d .` で差分がないことを確認。差分があれば失敗。

### `dropin-e2e` workflow (nightly)

- `.github/workflows/dropin-e2e.yml` で `make dropin-swap-test` を毎日 18:00 UTC
  (= JST 03:00) に develop に対して実行する。
- `workflow_dispatch` で任意の ref に対して手動実行も可。
- PR の required check には**含めない** (8-10 min かかり flaky 要素もあるため)。
  drop-in 互換 regression は nightly で検出する運用。
- 失敗時は docker compose logs を `dropin-logs` artifact として 14 日保持。

### `playwright` workflow (nightly)

- `.github/workflows/playwright.yml` で Phase 1 Playwright spec を毎日 17:00
  UTC (= JST 02:00) に develop に対して実行する。
- matrix で `backend = [mk-go, ts]` の 2 job を並列実行 (= 両 backend で
  drop-in shape 互換が維持されることを担保)。`fail-fast: false` で片方失敗
  しても他方は完走。
- `workflow_dispatch` で任意の ref に対して手動実行も可。
- PR の required check には**含めない** (TS image pull + spec 増加で実行
  時間が伸びる + 外部 image flaky 要素)。drop-in shape regression は
  nightly で検出する運用。
- 失敗時は `tests/playwright/test-results/` (trace / screenshot 含む) と
  docker compose logs を `playwright-results-<backend>` /
  `playwright-logs-<backend>` artifact として 14 日保持。

### CI失敗時の対応

- カバレッジ不足 → テストケースを追加してから再push。
- `gofmt`差分 → `make fmt`をローカルで実行してから再push。
- テスト失敗 → CIログを読み、ローカルで再現させてから修正。`--no-verify`等でフックを飛ばさない。

## 9. Environment Variables

### 設定ファイル

- デフォルト: `.config/default.yml`（Misskey互換YAML, gitignored）
  - 初回は `cp .config/default.yml.example .config/default.yml` で複製
- Docker: `.config/docker.yml.example` を Dockerfile が image に焼き込む
  - operator が独自設定したい場合は `cp .config/docker.yml.example .config/docker.yml` してから docker-compose で volume mount で上書き
- CLIフラグ `-config <path>` でパスを指定。

### 環境変数オーバーライド

`MK_`プレフィックス付きの環境変数で設定値を上書きできる。ネストキーは`_`区切り。

| 環境変数 | 対応YAMLキー |
|---------|-------------|
| `MK_URL` | `url` |
| `MK_PORT` | `port` |
| `MK_DB_HOST` | `db.host` |
| `MK_DB_PORT` | `db.port` |
| `MK_DB_DB` | `db.db` |
| `MK_DB_USER` | `db.user` |
| `MK_DB_PASS` | `db.pass` |
| `MK_REDIS_HOST` | `redis.host` |
| `MK_REDIS_PORT` | `redis.port` |
| `MK_REDIS_PASS` | `redis.pass` |
| `MK_ID` | `id` (デフォルト`aidx`) |

新規にオーバーライド対象を増やす場合は`internal/config/config.go`の`bindEnvKeys()`に追加すること（Viperは既知のキーのみ環境変数を適用する）。

### テスト用環境変数（CI）

- `TEST_DB_HOST`, `TEST_DB_PORT`, `TEST_DB_NAME`, `TEST_DB_USER`, `TEST_DB_PASS`, `TEST_DB_SSLMODE`
- `TEST_REDIS_HOST`, `TEST_REDIS_PORT`

ローカルでは`testcontainers-go`が自動でコンテナを起動するため通常は不要。

### マイグレーション用環境変数

- `DATABASE_URL` — `make migrate-up/down`で使用するPostgreSQL接続文字列。

## 10. 開発方針

### Phaseベースの進行

開発はPhase単位で進める。各Phaseの内容・進捗はGitHub Issuesで管理する。新しい作業を始める前に対応するissueを作成し、実装完了時にPRでcloseする（詳細はSection 7）。

### タスクの粒度

- タスクは**1 issue = 1 PRで完結する粒度**に分割する。
- 大きな機能追加は`Phase N.M`のサブフェーズに分けて個別のissueを立て、段階的にマージする。
- 1つのPRで「機能追加 + 無関係なリファクタ」を混ぜない。

### 設計の変更

- 設計方針を変更する場合は、対応するissue（または新規issue）で背景・変更内容を議論・記録してから実装する。
- 実装中に設計の問題に気づいた場合は一度立ち止まり、ユーザーに確認する。

### オリジナルMisskeyの参照

- 実装方針に迷った場合は`.tmp/misskey/`（オリジナルMisskeyのソース、gitignore）を参照する。
- ただし**TypeScriptのパターンをそのままGoに翻訳しない**。Goらしい書き方（インターフェース、明示的エラー、構造体埋め込み）に適応させる。

### 破壊的操作の扱い

- マイグレーションのdownスクリプトは必ず書く。ただしdata lossが発生する場合はその旨コメントする。
- DBテーブル削除、カラム削除は`Phase`をまたぐ段階的移行を検討する。
- 本番に影響する変更はユーザーに事前確認する。

### 補助ツール

- **ライブラリの使い方を調べる際はContext7 MCP**を使って最新情報を取得する。
- 隠しフォルダ（`.tmp`等）を探す際は`List`ではなく`Bash`（`ls -la`）を使う。

---

## 更新記録

本ドキュメントの主要な変更履歴。新規変更時は一番上に追記する（日付降順）。

- **2026-05-13**: `docs/upstream-catch-up.md` に section 1-5 (UDS production stack の再ビルド) を追加。`compose.uds.yaml` は Misskey TS の prebuilt image を pull せず `deploy/uds/Dockerfile.mkgo` の `COPY . .` で submodule の静的アセットを image に焼き込む構成のため、submodule bump 後は `docker compose -f compose.uds.yaml up --build -d` で明示的に再ビルドしないと古い asset が image にキャッシュされたままになる点を明示。`--build` フラグ必須 / `make uds-frontend-build` skip 時の Dockerfile sanity check による early fail も documentation 化。
- **2026-05-13**: PR #998 (2026.3.2 → 2026.5.1 upstream 一括取り込み) と並行して、`docs/upstream-catch-up.md` を新規追加。submodule bump PR マージ後に必要な `git pull --recurse-submodules` / `git submodule update --init --recursive` / `submodule.recurse=true` 設定 / `make uds-frontend-build` / `make migrate-up` の手順と、新 upstream release 取り込み時の tracker → triage doc → sub-issue 化 → Wave 単位 PR の運用フロー (PR #998 を reference 実装として明記) をまとめた。shiroha-a/misskey-ts fork への `<tag>-mk.N` cherry-pick + tag push 手順も documentation 化。
- **2026-05-09**: ドキュメント整備一括 PR (#949 親 tracker)。`docs/2026-5-0-diff.md` → `docs/update/20260500diff.md` に移動 + `docs/update/20260501diff.md` 新規 (#948)、`api-compatibility.md` を 2026-04-17 stale 状態から最新 (Phase 1-4 完了 / drift backlog 40+ 件 / 2026.5.x 互換) に更新 (#950)、`CHANGELOG.md` に Phase 16 (Playwright Phase 1-4) / P5 (drift backlog) / 15 (federation perf) / 14 (drop-in frontend e2e) / 13 (drop-in pytest) entries を追加 (#950)、`README` / `architecture.md` / `federation.md` / `testing.md` / `configuration.md` / `migration-from-ts.md` を Edit ベースで一括更新 (#952)、`docs/design/` に 3 ADR 追加 (LCD strategy / inbox verify-in-worker / RemoteStatsFetcher motivation)。`docs/update/` 命名規則 `yyyymmdd*` で upstream release 差分を積み上げ運用、search backend 既定は SQL ILIKE fallback で Meilisearch は optional (`fulltextSearch.provider` 切替) であることを明示。
- **2026-05-09**: UDS production で観測した 4 件の bug bundle fix (#944 / 旧 #607 → 新 #947 tracker re-scope)。#940 遅延配送 remote note の createdAt drift (AP `published` を採用 + clock skew 5min / 過去 10 年 floor で fallback)、#941 mediaproxy で gif/apng の resize を pass-through 化 (Go std `image.Decode` が 1 frame しか返さない問題回避、emoji リアクション / picker / preview mode で適用、static / badge mode は従来通り decode)、#942 URL preview に `golang.org/x/net/html/charset.NewReader` 経由で Shift_JIS / EUC-JP / ISO-2022-JP の自動正規化、#943 `RemoteStatsFetcher` (mk-go 独自拡張) でリモートユーザーの notesCount / followersCount / followingCount を origin instance の `/api/users/show` から取得して上書き表示 (1h LRU cache / SSRF guard / host validation / silent fallback)。#945 で `sync.Map` → `hashicorp/golang-lru/v2` に置換し size cap 10000 で memory bound 化 (#946)。
- **2026-05-08**: Playwright Phase 1-4 完了 (#744)。Phase 4 PR-A (channels #923) / PR-B (hashtags + roles + notifications control #924) / PR-C (admin/queue + abuse-report #928) / PR-D (admin stats/show/ad/avatar/drive read #930) / PR-E (admin server-info / captcha / invite / announcements / relays / system-webhook #933) / PR-F (i/* 残 + chat/* 残 #934) を一気に消化。**96 spec / 35 directory / 242 endpoint cover (54.3%)** を両 backend (mk-go / Misskey TS) で nightly green 維持。Phase 4 で発見した drift は #925 / #926 / #929 / #931 / #932 / #936 / #937 / #939 を bundle PR で消化 (#927 / #935 / #938)。upstream catch-up tracker は #607 (2026.5.0 まで) を close → #947 (2026.5.1 まで) に re-scope し、`docs/update/yyyymmdd*` 命名規則で release ごとの差分 doc を積み上げる運用に移行。
- **2026-05-07**: Playwright nightly CI workflow (#744 関連) を追加。`.github/workflows/playwright.yml` で Phase 1 spec を毎日 17:00 UTC に develop で実行する。matrix で `backend = [mk-go, ts]` の 2 job を並列実行し、`fail-fast: false` で両 backend の drop-in shape regression を独立に監視する。失敗時は `tests/playwright/test-results/` (trace/screenshot 含む) と docker compose logs を artifact 化 (14 日保持)。dropin-e2e (18:00 UTC) / dropin-frontend-e2e (19:00 UTC) と被らない時刻に scheduling。
- **2026-05-04**: Signin の TOTP / passkey フローを Misskey TS upstream と再互換化 (#705)。`/api/signin-flow` で 2FA + security key ユーザに対して `next: 'passkey'` + `authRequest` (PublicKeyCredentialRequestOptionsJSON) を返すよう修正 (旧: `'captcha-keys'` + `assertion` 2 段ラップ + クライアント sessionId 要求)、challenge を Redis に user-keyed で保存して sessionId round-trip を撤廃、TOTP / WebAuthn 失敗時のエラー ID を本家準拠に揃え (`cdf1235b-...` / `93b86c4b-...`)、step 1 の `next` を 2FA 無効ユーザに常に `'captcha'` を返すよう変更。新たに `/api/signin-with-passkey` (passwordless passkey login の init+verify 2 段) を実装し `BeginPasskeyLogin`/`FinishPasskeyLogin` を `WebAuthnService` に追加。E2E (`e2e/cypress/e2e/webauthn.cy.ts`) も新プロトコルに更新し、TS 互換 spec をローカル spec パスに含めるよう `cypress.config.ts` を拡張。
- **2026-05-01**: inbox worker drain time 短縮 (#569)。\`MarkRequestReceived\` を per-host で 1s buffer に集約する \`InstanceTouchBuffer\` 導入、federation processor の \`handleCreate\` で Reply/Renote 関係が無い fresh note への redundant \`hydrateNoteForFanout\` (DB SELECT) を skip、fanoutHook / notificationHook を local note service と同じく \`safeGo\` で async 化。queue-bench で **asynq drain 29.3s → 22.4s (-24%)、mkq 45.7s → 34.0s (-26%)**。worker concurrency / mkq 上流 Lua 最適化は scope 外 (公正比較維持のため)。
- **2026-05-01**: inbox handler を verify-in-worker 化 (#565)。HTTP handler は body+signature 関連 header を payload に詰めて 202 即返し、signature verify / host block / instance touch / chart hook は inbox worker (queue/processors) 側で実行する Misskey TS 互換アーキテクチャに変更。queue-bench で mk-go の inbound HTTP 受信 rps が **684/685 → 2812/3017 (4.1-4.4x)** に改善し、TS-BullMQ (1064 rps) を **2.6-2.8x 上回る**。これにより mk-go 全 endpoint が TS 同等以上を達成。security trade-off: unsigned/malformed activity は worker で drop されるため queue が一時的に膨らむ可能性あり (CDN/WAF 層で粗い filter を入れる前提)。
- **2026-04-30**: queue-bench (#563) を追加。`tests/queue-bench/` で BullMQ (Misskey TS) / asynq (mk-go) / mkq (mk-go) の deliver / inbox throughput を 3-way 比較する基盤を新設。`make queue-bench-{up,seed,outbound,inbound,report,down}`。faker (Go HTTPS, AP HTTP signature 直接、pre-sign 並列化で sender 律速回避) と blackhole (受信専用、204 即返し) を補助 service として持つ。詳細: `docs/queue-bench.md`。
- **2026-04-28**: `internal/server`のCIカバレッジ閾値を0%例外に追加 (#462)。`avatar.go`/`avatar_test.go`の追加で同パッケージ初の`_test.go`が入り、`router.go`(2000行超のwire層)込みのpackage全体カバレッジが2.5%で計測されてCIが落ちたため。`testutil`/`e2e`と同じく実挙動はe2e/drop-in testで検証する設計に揃える。個別handlerファイルは`_test.go`単体で90%相当をカバーする運用は維持。
- **2026-04-22**: drop-in frontend e2e Phase 14-3 (#394) を追加。`docker-compose.dropin-frontend.mk.yml` overlay と `tests/dropin_frontend/run-frontend-swap-test.sh` orchestrator で、TS-A 切替後の mk-A でも cypress spec が pass することを e2e 検証する。`CYPRESS_MODE=baseline|swap` を spec に渡す `support/mode.ts` と `skipInSwap` helper を追加。#396 (users/lists/push duplicate) / #397 (specified DM mentions) / #379 (delete propagation) / #389 (reply_chain) を swap mode で skip 扱いに。`.github/workflows/dropin-frontend-e2e.yml` で毎日 19:00 UTC nightly 実行。
- **2026-04-21**: drop-in frontend e2e Phase 14-2 (#387) を追加。spec マトリクスに `visibility.cy.ts` / `user_list.cy.ts` / `cross_instance_view.cy.ts` / `delete_note.cy.ts` の 4 本を追加 (12 passing)。`reply_chain.cy.ts` は federation queue back-pressure で brittle なので #389 で調整後に activate 予定 (現状 `describe.skip`)。共通 setup を `support/setup.ts` に切り出し、cypress plugin task `tokenCache:*` で token を spec 間共有して signin rate limit を回避する。
- **2026-04-21**: drop-in frontend e2e Phase 14-1 (#381) を追加。3 Misskey TS インスタンス (A/B/C) + cypress runner 構成 (`docker-compose.dropin-frontend.yml` + `tests/dropin_frontend/`) で baseline smoke spec (`smoke.cy.ts`) を動かす。spec マトリクス拡充は Phase 14-2、mk 差し替え overlay + CI 統合は Phase 14-3。
- **2026-04-21**: drop-in e2e Phase 13-4 (#374) を追加。`.github/workflows/dropin-e2e.yml` で `make dropin-swap-test` を nightly cron (18:00 UTC) + workflow_dispatch で実行。PR の required check には含めず、失敗時は docker compose logs を artifact 化して原因調査できるようにする。
- **2026-04-21**: drop-in e2e Phase 13-3 (#372) を追加。state preservation 機能マトリクスを 6 シナリオに拡充 (home/followers/specified visibility ノート, user list メタ, user list timeline)。`tests/dropin/test_swap_setup.py` と `test_swap_verify.py` に追加。
- **2026-04-21**: drop-in e2e Phase 13-2 (#367) を追加。`docker-compose.dropin.mk.yml` overlay と `tests/dropin/run-swap-test.sh` orchestrator で「TS-A backend を mk-go に差し替えても DB / Redis 上の state が引き継がれる」ことを e2e 検証する。途中で発覚した `note.pageCount` / `note.renoteChannelId` カラム欠如を `migration/000039_dropin_compat.up.sql` で補填。reply / reaction の federation deliver 不足は別バグ #368 として切り出し、対応する 2 テストは `xfail` 済。
- **2026-04-21**: drop-in e2e テスト基盤 Phase 13-1 (#365) を追加。`docker-compose.dropin.yml` と `tests/dropin/` で Misskey TS 2 インスタンスを立ち上げ、pytest で federation smoke test を実行する。Phase 13-2 で mk 差し替え overlay を追加予定。
- **2026-04-20**: CIの`test`ジョブを4-way matrix shardで並列化。総実行時間を約4.7分→約1.5-2分に短縮。各shardは独立サービスコンテナで動作し、ImportPath順modulo分配で決定的にパッケージを割り当てる。
- **2026-04-18**: `internal/repository`パッケージのテストを拡充しカバレッジを76.4%→99.9%に引き上げ、CI閾値を90%に戻す。`internal/api/admin`の閾値を60%→80%に引き上げ(現状83.8%)。CI step "Run all tests with coverage"に`set -o pipefail`を追加してテスト失敗の握り潰し解消 (#260)。
- **2026-04-18**: `internal/repository`パッケージのCIカバレッジ閾値を暫定的に76%に緩和（#260で90%復帰予定）。
- **2026-04-12**: テストカバレッジ目標を追記（最低90% / 推奨95% / 目標100%）。
- **2026-04-11**: 初版作成。
