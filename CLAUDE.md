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

# Drop-in fedibird-mock e2e (#1083) — Fedibird-like ActivityPub mock との
# 双方向 Ed25519 verify を検証する e2e。
make dropin-fedibird-test    # mock ↔ mk-A の Ed25519 inbound/outbound 検証

# 本家 backend e2e (#2347) — Misskey 本家の test/e2e/** をそのまま mk-go に
# 向けて実行する。テスト本体は無改変。詳細は docs/upstream-backend-e2e.md。
make upstream-e2e-deps       # submodule 側の依存を用意 (初回 / submodule bump 後)
make upstream-e2e-up         # e2e 用 PostgreSQL / Redis を起動
make upstream-e2e-migrate    # e2e 用 DB にマイグレーションを適用
make upstream-e2e-test       # mk-go をビルドして vitest を実行 (FILE= で 1 ファイル指定可)
make upstream-e2e            # 上記 4 つを一括実行
make upstream-e2e-down       # volume ごと撤去

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
- バージョン文字列は`internal/config/config.go`の`MisskeyVersion` / `MkGoVersion`定数で管理し、対応するMisskeyバージョンに合わせる（現在: `MisskeyVersion=2026.7.0` / `MkGoVersion=1.1.0`）。
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

- **Issue・PRのタイトルおよび本文は日本語で記述することを厳守する**。コード識別子・エラーコード・ファイルパス・コマンド等の技術用語は原文のまま残してよいが、説明文・見出し・箇条書きの地の文は日本語で書く（英語の本文・見出しを混在させない）。
- **プロジェクトの`CHANGELOG.md`はリリース時にまとめて記述する**。個別のPR・fixごとに`## Unreleased`へ追記せず、リリースのタイミングで該当期間の変更を一括で記載する。

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

#### マージ方法

フィーチャーブランチ → `develop`のPRは**rebase and merge**でマージする（`gh pr merge <N> --rebase --delete-branch`）。

rebase and mergeでは**PRの各コミットがそのまま`develop`の履歴に載る**。したがって：

- コミットは**1つずつビルド・テストが通る順序**で並べる（依存するAPI追加を先、それを使う配線を後）。壊れたコミットが履歴に残ると`git bisect`が効かなくなる
- 確認は使い捨ての`git worktree`を作って各コミットをcheckout→buildするのが安全。作業ツリー上で`git stash`を回す方法は、保留中の別作業を巻き込むので使わない
- 「機能追加 + 無関係なリファクタ」を1コミットに混ぜない（1 PR単位の原則をコミット単位にも適用する）

`main`は**PRをマージしない**（`develop`からのFF pushのみ）。`main`でrebase mergeを使うとSHAが分岐してリリースタグが履歴に乗らなくなるため、こちらの方針とは対象が異なる。

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

### `upstream-backend-e2e` workflow (PR トリガー)

- `.github/workflows/upstream-backend-e2e.yml` で Misskey 本家の backend e2e
  (`third_party/misskey/packages/backend/test/e2e/**`) を mk-go に向けて実行する。
  テスト本体は無改変で、vitest の `globalSetup` / `setupFiles` だけを差し替える。
- `pull_request` で paths (`internal/**` / `cmd/**` / `migration/**` /
  `tests/upstream-e2e/**` / `third_party/misskey` / `Makefile` / `go.mod` /
  `go.sum` / 当 workflow) に該当する変更のみ発火。`workflow_dispatch` で任意の
  ref に対して手動実行も可。
- PR の required check には**含めない** (18-20 min かかり、1200 件超のテストに
  flaky 要素があるため merge ブロッカーには適さない)。非ブロッキングを
  `continue-on-error` で実現しないこと (job が成功扱いになり失敗が不可視になる)。
- 『通らないことが正しい』テストは `tests/upstream-e2e/known-divergences.json` に
  根拠付きで登録し、expected-failure (`task.fails`) として扱う。skip ではないので
  乖離が解消したテストは逆に落ち、一覧の陳腐化に気付ける。
- 失敗時は mk-go のログを `upstream-e2e-mkgo-log` artifact として 14 日保持。

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
個別 fix の履歴は CHANGELOG.md 側に集約しており、本セクションは CLAUDE.md 本体
(Section 1-10 の policy / Makefile target / CI 閾値 / CI workflow 等) を変更した
タイミングのみ記録する。

- **2026-08-07**: Section 3 に本家 backend e2e の Makefile target (`make upstream-e2e` 系 5 つ) を、Section 8 に `upstream-backend-e2e` workflow を追記 (#2347)。Misskey 本家の `test/e2e/**` を無改変で mk-go に向けて回す PR トリガーの workflow で、required check には含めない。既知乖離は skip でなく expected-failure (`task.fails`) で扱う運用も明記。
- **2026-08-04**: Section 7 (Git Workflow) に「マージ方法」を追記。フィーチャーブランチ → `develop` の PR は **rebase and merge** に統一する (それ以前は squash-merge)。各コミットがそのまま develop に載るため、1 コミットずつ build / test が通る順序で並べること、確認は使い捨て `git worktree` で行うこと (作業ツリー上の `git stash` は保留中の別作業を巻き込むので使わない) を併記。`main` は従来どおり PR をマージせず FF push のみで、対象が異なる旨も明記した。
- **2026-06-09**: Section 7 (Git Workflow) に 2 つのルールを追記。(1)「Issue・PR のタイトル・本文は日本語記述を厳守する」(技術用語は原文のまま残してよいが、説明文・見出し・箇条書きの地の文に英語を混在させない)。(2)「`CHANGELOG.md` はリリース時にまとめて記述する」(個別 PR・fix ごとに `## Unreleased` へ追記せず、リリースのタイミングで一括記載する)。
- **2026-05-16**: `Makefile` に `make dropin-fedibird-test` を追加 (#1086)。Section 3 (Development Commands) の Drop-in 系コマンド一覧に Fedibird-like mock との Ed25519 e2e を載せる。
- **2026-05-07**: Playwright nightly CI workflow を Section 8 に追記 (#816)。`.github/workflows/playwright.yml` で Phase 1 spec を毎日 17:00 UTC に develop で実行する、matrix `backend = [mk-go, ts]` 並列、`fail-fast: false`、PR required check には含めない方針を明文化。
- **2026-04-28**: `internal/server`のCIカバレッジ閾値を0%例外に追加 (#462)。`avatar.go`/`avatar_test.go`の追加で同パッケージ初の`_test.go`が入り、`router.go`(2000行超のwire層)込みのpackage全体カバレッジが2.5%で計測されてCIが落ちたため。`testutil`/`e2e`と同じく実挙動はe2e/drop-in testで検証する設計に揃える。個別handlerファイルは`_test.go`単体で90%相当をカバーする運用は維持。
- **2026-04-22**: Section 3 に drop-in frontend e2e Phase 14-3 関連の Makefile target (`make dropin-frontend-mk-up` / `make dropin-frontend-mk-down` / `make dropin-frontend-swap-test`) を追加 (#394)。TS-A 切替後の mk-A でも cypress spec が pass することを検証する swap orchestrator を入口に出す。
- **2026-04-21**: Section 3 に drop-in frontend e2e Phase 14-1 関連の Makefile target (`make dropin-frontend-baseline` / `dropin-frontend-up` / `dropin-frontend-down`) を追加 (#381)。3 Misskey TS インスタンス + cypress runner 構成。
- **2026-04-21**: Section 8 に `dropin-e2e` workflow (nightly) を追記 (#374)。`make dropin-swap-test` を毎日 18:00 UTC で develop に対して実行、PR required check 非対象、失敗時 docker compose logs を 14 日 artifact 化する運用を明文化。
- **2026-04-21**: Section 3 に drop-in e2e Phase 13-2 関連の Makefile target (`make dropin-mk-up` / `dropin-mk-test` / `dropin-mk-down` / `dropin-swap-test`) を追加 (#367)。
- **2026-04-21**: Section 3 に drop-in e2e Phase 13-1 関連の Makefile target (`make dropin-up` / `dropin-test` / `dropin-down`) を追加 (#365)。Section 1 の Tests 配下にも testcontainers-go 周りの拡張ポインタを追記。
- **2026-04-20**: Section 8 の `test`ジョブを 4-way matrix shard 化として書き換え (`test-shards` 4 並列 + `test` aggregator)。総実行時間を約4.7分→約1.5-2分に短縮。各shardは独立サービスコンテナで動作し、ImportPath順modulo分配で決定的にパッケージを割り当てる。
- **2026-04-18**: Section 4 / Section 8 のカバレッジ例外閾値を更新 (#260)。`internal/repository` パッケージのテスト拡充でカバレッジを76.4%→99.9%に引き上げて CI 閾値を 90% に戻し、`internal/api/admin` の閾値を 60%→80% に引き上げ(現状83.8%)。CI step "Run all tests with coverage"に`set -o pipefail`を追加してテスト失敗の握り潰し解消も同時に。
- **2026-04-18**: Section 4 / Section 8 に `internal/repository` パッケージのCIカバレッジ閾値を暫定的に 76% に緩和する例外を追加（#260で90%復帰予定）。
- **2026-04-12**: Section 4 にテストカバレッジ目標を追記（最低90% / 推奨95% / 目標100%）。
- **2026-04-11**: 初版作成。
