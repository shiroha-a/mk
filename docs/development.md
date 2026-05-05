# 開発ガイド

## 開発環境のセットアップ

### devcontainer (推奨)

VS Codeの[Dev Containers](https://code.visualstudio.com/docs/devcontainers/containers)拡張をインストールして開く。

`.devcontainer/`の構成:
- Go 1.26 + PostgreSQL + Redis (network_mode: host)
- golang-migrate、Node.js 22、pnpmがプリインストール
- `postCreate.sh`で初期化

```bash
# VS Codeで開いたら
make dev
```

### ローカル環境

前提条件:
- Go 1.26+
- PostgreSQL 16+
- Redis 7+
- Docker (testcontainers用、テスト実行に必要)

```bash
git clone --recursive https://github.com/shiroha-a/mk.git
cd mk

# 設定ファイルを作成
cp .config/default.yml.example .config/default.yml
# default.yml を編集してDB/Redis接続先を設定

# マイグレーション適用
export DATABASE_URL="postgres://user:pass@localhost:5432/misskey?sslmode=disable"
make migrate-up

# 起動
make dev
```

## Makefileターゲット

### ビルド・実行

| ターゲット | 内容 |
|---|---|
| `make build` | `./built/misskey`にバイナリ生成 |
| `make dev` | `go run`で直接起動 |
| `make run` | build + 実行 |
| `make tidy` | `go mod tidy` |

### コード品質

| ターゲット | 内容 |
|---|---|
| `make fmt` | `gofmt -s -w .` |
| `make lint` | `go vet ./...` |
| `make test` | `go test ./... -v` |

### マイグレーション

| ターゲット | 内容 |
|---|---|
| `make migrate-up` | 最新まで適用 (`DATABASE_URL`必要) |
| `make migrate-down` | 1段階ロールバック |
| `make migrate-create` | 新規マイグレーションファイル作成 |

### Docker

| ターゲット | 内容 |
|---|---|
| `make docker-build` | Dockerイメージビルド |
| `make docker-up` | `docker compose up -d` |
| `make docker-down` | `docker compose down` |

### テスト関連

| ターゲット | 内容 |
|---|---|
| `make e2e-frontend-build` | Misskeyフロントエンドビルド (Docker内、3-10分) |
| `make e2e-run` | Cypress E2Eテスト実行 |
| `make federation-misskey-up` | 連合テスト用Misskeyインスタンス起動 |

## コーディング規約

- `gofmt -s -w .`で整形 (CIで強制)
- `go vet`を通す (CIで強制)
- 命名はGo標準 (camelCase/PascalCase、略語は全大文字: URL, ID, API)
- Early returnでネストを浅く保つ
- エラーは`fmt.Errorf("context: %w", err)`でラップ
- GoDoc(関数/型のドキュメント)は英語、インラインコメント(実装の背景)は日本語
- 自明な処理の説明コメントは書かない

詳細な規約はCLAUDE.md Section 5を参照。

## ブランチ運用

| ブランチ | 役割 |
|---|---|
| `main` | リリース |
| `develop` | 開発統合 |
| `feature/<phase>-<要約>` | 機能追加 |
| `fix/<対象>-<要約>` | バグ修正 |

すべての作業は対応するissueを先に作成してから着手する。

### コミットメッセージ

- Phase単位の機能追加: `Phase N.M: <要約>`
- 修正: `Fix <対象>: <要約>`
- コミット前に `make fmt && make lint && make test` を実行

### PR作成

- PRタイトル: `Phase〇 <内容>` または作業の要約
- PR本文: Summary、主な変更点、テスト、`Closes #<issue番号>`
- `gh pr create`を使用

## CI/CD

`main`と`develop`へのpush/PRで3ジョブが実行される:

### buildジョブ
`go build ./...`で全パッケージのビルド確認。

### testジョブ
- PostgreSQL 16 + Redis 7をサービスコンテナで起動
- `-race -count=1 -timeout 10m -covermode=atomic`
- パッケージ別カバレッジ閾値: 90%以上 (`internal/api/admin`のみ60%)
- 未達でジョブ失敗

### lintジョブ
- `go vet ./...`
- `gofmt -s -d .`で差分チェック (差分ありで失敗)
