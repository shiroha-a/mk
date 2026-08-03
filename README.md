# mk-go

Misskey互換のGoバックエンド実装。TypeScript/NestJS製の[Misskey](https://github.com/misskey-dev/misskey)と同一のDB・Redis・フロントエンドを共有し、バックエンドを差し替えられる。

互換バージョン: **Misskey 2026.7.0** (mk-go `1.0.0`)

## 特徴

- Go 1.26 / Echo v4 / GORM + pgx / go-redis v9
- Misskeyフロントエンド(SPA)をそのまま配信
- TypeScript版と同じPostgreSQL/Redisを共有、無停止で移行可能
- ActivityPub連合対応（HTTP Signatures、リモートオブジェクト解決、配信キュー）
- ジョブキューは `mkq` (BullMQ wire-compat、デフォルト) または `asynq`
- Playwright e2e (370 spec) を PR ごとに実行。upstream 追従時は Misskey TS backend に対しても回して drop-in 互換を検証
- `RemoteStatsFetcher` でリモートユーザーの notesCount / followersCount / followingCount を origin から取得 (mk-go 独自拡張)

## クイックスタート (Docker Compose)

```bash
git clone --recursive https://github.com/shiroha-a/mk.git
cd mk
```

### 1. フロントエンドをビルドする (初回のみ、3-10分)

```bash
make e2e-frontend-build
```

SPA の JS/CSS は約 200MB あるため image に焼き込まず、`third_party/misskey/built` を bind-mount で渡している。これを省くとフロントエンドのアセットが 404 になり画面が表示されない。

### 2. 設定ファイルを用意する

```bash
cp .config/docker.yml.example .config/docker.yml
# .config/docker.yml の url を実際のアドレスに変更する
```

`url` は必須項目で、既定値は `https://example.tld/` のまま。編集したら `docker-compose.yml` の **`app` と `migrate` の両方**にある volumes のコメントを外す。

```yaml
- ./.config/docker.yml:/app/.config/default.yml:ro
```

`migrate` 側にも同じ mount を入れないと、マイグレーションと本体が別の DB を見にいく。

ローカルで試すだけなら、この手順は省略して既定の設定のまま起動してもよい。

### 3. ドライブ用ディレクトリの所有権を設定する

```bash
mkdir -p files && sudo chown -R 991:991 files
```

コンテナは Misskey TS 互換の UID/GID 991 で動くため、この所有権でないとファイルをアップロードできない。

### 4. 起動する

```bash
docker compose up -d
```

DB マイグレーションは one-shot の `migrate` サービスが自動適用し、その完走を待って `app` が起動する。手動で流す必要はない。

ブラウザで `http://localhost:3000` を開き、最初のアカウントを作成する。

より詳しい構成 (UDS、prebuilt image、systemd、逆プロキシ) は[デプロイ](docs/deployment.md)を参照。

## アップデート

```bash
# 1. submodule ごと更新する (--recurse-submodules を忘れない)
git pull --recurse-submodules

# 2. third_party/misskey が動いていたらフロントエンドを再ビルドする
make e2e-frontend-build

# 3. 再ビルドして起動しなおす (マイグレーションは自動適用される)
docker compose build
docker compose up -d
```

注意点:

- **`git pull` だけでは submodule が更新されない**。親リポのポインタが動くだけで `third_party/misskey/` の中身は古いまま。`git config submodule.recurse true` を一度実行しておくと以後は自動で追従する
- **フロントエンドを再ビルドしたら必ず mk-go を再起動する**。エントリポイントを起動時に 1 回だけ解決してキャッシュするため、再起動しないと消えた古いファイルを参照し続けて 404 になる
- ブラウザ側に Service Worker が残っている場合はハードリロードする

バイナリ直接実行や UDS 構成でのアップデート手順は[デプロイ](docs/deployment.md#アップデート)を参照。

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
| [API互換性マトリクス](docs/api-compat.md) | エンドポイント単位の実装状況 (`make apicompat` で自動生成) |
| [差分カタログ](docs/divergence.md) | 純正Misskeyに無い機能・意図的に異なる挙動の一覧 |
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
| [差分比較ハーネス](docs/diff-e2e.md) | mk-go と TS の実APIレスポンスを値レベルでdiff |
| [シェイプドリフト検出](docs/shape-drift.md) | レスポンス形状・エラーID・権限のドリフトを検出する静的ゲート |
| [UDSデプロイ](docs/docker-uds.md) | UNIXドメインソケット構成 |
| [queue-bench](docs/queue-bench.md) | BullMQ / asynq / mkq の 3-way 比較 (#563) |
| [ベンチプロファイリング](docs/bench-pprof.md) | k6負荷時のpprof取得と解析 |
| [upstream追従手順](docs/upstream-catch-up.md) | Misskey TSの新リリース取り込みとsubmodule bump |
| [設計メモ](docs/design/) | オートスケール、inbox verify、mkq等の設計判断 |
| [upstream 差分](docs/update/) | Misskey TS 2026.3.2 → 2026.7.0 の backend 差分 (`yyyymmdd*` 命名) |

## ライセンス

[GNU AGPL-3.0](LICENSE)
