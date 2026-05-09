# mk-go

Misskey互換のGoバックエンド実装。TypeScript/NestJS製の[Misskey](https://github.com/misskey-dev/misskey)と同一のDB・Redis・フロントエンドを共有し、バックエンドを差し替えられる。

互換バージョン: **Misskey 2026.3.2** (base) + drift backlog 適用済 (実質 2026.5.x 相当の挙動)

## 特徴

- Go 1.26 / Echo v4 / GORM + pgx / go-redis v9
- Misskeyフロントエンド(SPA)をそのまま配信
- TypeScript版と同じPostgreSQL/Redisを共有、無停止で移行可能
- ActivityPub連合対応（HTTP Signatures、リモートオブジェクト解決、配信キュー）
- ジョブキューは `mkq` (BullMQ wire-compat、デフォルト) または `asynq`
- Playwright e2e で両 backend (mk-go / TS) を nightly 比較、drop-in 互換 regression を検出
- `RemoteStatsFetcher` でリモートユーザーの notesCount / followersCount / followingCount を origin から取得 (mk-go 独自拡張)

## クイックスタート (Docker Compose)

```bash
git clone --recursive https://github.com/shiroha-a/mk.git
cd mk

# フロントエンドビルド (初回のみ、3-10分)
make e2e-frontend-build

# 起動
docker compose up -d

# ブラウザで http://localhost:3000 を開く
```

## ローカルビルド

前提: Go 1.26+、PostgreSQL 16+、Redis 7+、Docker (テスト用)

```bash
git clone --recursive https://github.com/shiroha-a/mk.git
cd mk

# 設定ファイルを作成 (→ docs/configuration.md 参照)
cp .config/default.yml.example .config/default.yml
# default.yml を環境に合わせて編集

# マイグレーション適用
export DATABASE_URL="postgres://user:pass@localhost:5432/misskey?sslmode=disable"
make migrate-up

# ビルド & 起動
make build
./built/misskey -config .config/default.yml

# 開発モード (go run)
make dev
```

## テスト

```bash
# 全テスト (testcontainersでPostgreSQL/Redisが自動起動、Docker必須)
make test

# 特定パッケージ
go test ./internal/api/notes/...

# レース検出 + カバレッジ (CIと同条件)
go test -race -count=1 -timeout 10m \
  -coverprofile=coverage.out -covermode=atomic ./...
```

## ドキュメント

| ドキュメント | 内容 |
|---|---|
| [アーキテクチャ](docs/architecture.md) | レイヤ構成、パッケージ責務、DI、フックパターン |
| [API互換性](docs/api-compatibility.md) | Misskey-TSとの互換性状況 |
| [設定リファレンス](docs/configuration.md) | 全設定項目、環境変数オーバーライド |
| [開発ガイド](docs/development.md) | 環境セットアップ、コーディング規約、CI |
| [テスト](docs/testing.md) | テスト戦略、カバレッジ目標、testcontainers |
| [ActivityPub連合](docs/federation.md) | AP実装、HTTP Signatures、配信パイプライン |
| [デプロイ](docs/deployment.md) | Docker/Compose/systemd、逆プロキシ |
| [コントリビューション](docs/contributing.md) | Issue/PR運用、レビュー基準 |
| [TS版からの移行](docs/migration-from-ts.md) | 既存Misskeyからの移行手順 |
| [E2Eテスト](docs/e2e.md) | Cypressによるフロントエンドテスト |
| [Drop-in e2e (pytest)](docs/dropin-e2e.md) | TS-A backend を mk-A に差し替えた state preservation 検証 |
| [Drop-in frontend e2e (cypress)](docs/dropin-frontend-e2e.md) | 3 TS instance + cypress で frontend 視点の互換 |
| [UDSデプロイ](docs/docker-uds.md) | UNIXドメインソケット構成 |
| [queue-bench](docs/queue-bench.md) | BullMQ / asynq / mkq の 3-way 比較 (#563) |
| [upstream 差分](docs/update/) | Misskey TS 2026.3.2 → 2026.5.0 → 2026.5.1 の backend 差分 (`yyyymmdd*` 命名) |

## ライセンス

[GNU AGPL-3.0](LICENSE)
