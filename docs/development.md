# 開発ガイド

## 開発環境のセットアップ

### devcontainer (推奨)

VS Codeの[Dev Containers](https://code.visualstudio.com/docs/devcontainers/containers)拡張をインストールして開く。

`.devcontainer/`の構成:
- Go 1.26 + PostgreSQL + Redis (network_mode: host)
- golang-migrate、Node.js 22、pnpmがプリインストール
- `postCreate.sh`で初期化

`postCreate.sh` が `.config/default.yml` を example から複製し (`.config/*` は gitignore なので clone 直後は存在せず、無いと `failed to load config` で落ちる)、migration まで流す。`TEST_DB_*` は compose が渡すので `.env.test` は要らない。

**`url` は example の `https://example.tld/` のまま残るので、開いたら手で書き換えること。** `mediaProxy` の既定は `url` から組み立てられるため、放置するとリモートの avatar / emoji / 添付が `https://example.tld/proxy?...` になって全部読めない。`MK_URL` で渡す方法は使えない (`MK_*` は viper で設定ファイルより優先されるので `internal/config` のテストが落ちる)。

なお **devcontainer では `make test` で `internal/config` が 2 件落ちる**。compose が渡す `MK_DB_USER` / `MK_DB_PASS` が `TestLoad_DatabaseConfig` を、`MK_REDIS_HOST` が `TestLoad_RedisForPubsub` を、それぞれ fixture より優先して読むため。既知の問題。

```bash
# VS Code で開いたら
make dev
```

### ローカル環境

前提条件:
- Go 1.26+
- PostgreSQL 18推奨 (16以降で動作、CI検証は18)
- Redis 7+
- Docker (テストで Redis を要する箇所が testcontainers を使う)

**テストを回すには PostgreSQL を自分で用意する。** Redis は testcontainers が立てるが、DB を使うテストの大半は外部の PostgreSQL に直接つなぐ。既定の接続先とロール / DB の作り方は [testing.md](testing.md) を参照。

```bash
git clone --recursive https://github.com/shiroha-a/mk.git
cd mk

# 設定ファイルを作成
cp .config/default.yml.example .config/default.yml
# default.yml を編集してDB/Redis接続先を設定

# テスト用 PostgreSQL の接続先が既定 (localhost:5432 / misskey_test / mk) と
# 違うときだけ複製して編集する (→ testing.md)
# cp .env.test.example .env.test

# マイグレーション適用 (接続先は上で編集した default.yml から読む)
make migrate-up

# 起動
make dev
```

## Makefileターゲット

引数なしの `make` (= `make help`) で全ターゲットの一覧が出る。以下はグループごとの説明と、詳細ドキュメントへの入口。

### まとめて実行

| ターゲット | 内容 |
|---|---|
| `make check` | `fmt` → `lint` → `test`。コミット前に必須 |
| `make gates` | 静的 parity ゲート 4 種を一括実行 |
| `make version` | mk-go / 互換 Misskey / submodule のバージョンを表示 |
| `make frontend-check` | 同梱フロントエンドを型チェック (`vue-tsc --noEmit`) **だけ**。ビルド成果物を作らないので安全。CI の同名 job はこれに加えて `make plugins-all` と統合バイナリの build を別 step で走らせる |
| `make diff-check` | 差分比較ハーネスを作り直して実行 (クリーン DB 前提のため) |
| `make playwright-check` | Playwright を作り直して実行 (同上) |
| `make e2e-down-all` | 検証用スタックを一括撤去。**本番 project `mk` は対象外** |

### pull して起動 (ビルド不要)

| ターゲット | 内容 |
|---|---|
| `make image-up` / `image-down` / `image-down-v` / `image-logs` | フロントエンド同梱の `bundled` イメージを pull して起動する (`docker-compose.image.yml`) |
| `make image-build` | `bundled` イメージを手元でビルドする (publish 前の確認用) |

既存の `docker-compose.yml` / `make docker-*` (ソースからビルド) はそのまま使える。置き換えではなく並立する選択肢。

動かすだけならソースを clone する必要すら無い。compose と設定のひな形だけを置いた
[`docker` ブランチ](https://github.com/shiroha-a/mk/tree/docker) が GitHub Actions で
自動生成されている (`.github/workflows/docker-branch.yml`、生成元は
`docker-compose.image.yml` / `.config/docker.yml.example` / `deploy/README.md`)。

```bash
git clone --depth 1 -b docker https://github.com/shiroha-a/mk.git mk
cd mk && docker compose up -d
```

**`docker` ブランチは手で編集しない。** push のたびに履歴ごと作り直されるので、
変更したい場合は生成元を編集する。

### 更新 (運用)

| ターゲット | 内容 |
|---|---|
| `make update` | `git pull --recurse-submodules` して、フロントエンド再ビルドの要否を知らせる |
| `make docker-update` | pull → フロントエンドビルド → イメージ再ビルド → 再起動 (Docker Compose 構成) |
| `make uds-update` | 同上 (UDS 本番構成) |

`docker-update` / `uds-update` は**フロントエンドの再ビルドと再起動を必ずセットで実行する**。mk-go はエントリポイントを起動時に 1 回だけ解決してキャッシュするため、ビルドだけして再起動しないと HTML が消えた古い `scripts/<hash>.js` を指したまま 404 になる。手順の詳細は[デプロイ](deployment.md#アップデート)を参照。

### ビルド・実行

| ターゲット | 内容 |
|---|---|
| `make build` | `./built/misskey`にバイナリ生成 |
| `make dev` | `go run`で直接起動 |
| `make run` | build + 実行 |
| `make clean` | ビルド成果物を削除 |
| `make tidy` | `go mod tidy`。**このリポジトリでは private plugin の解決に失敗するので使えない**。依存追加は `go get`、`go.sum` の充足検証は `GOFLAGS=-mod=readonly go build` (→ [プラグインの書き方](plugins/authoring.md)) |
| `make plugins` | `plugins/` を走査して組み込み用ファイルを生成 (#2480)。`make build` が内部で呼ぶ |
| `make plugins-all` | `disabled` のプラグインも含めて生成 (CI 検証用) |

### コード品質

| ターゲット | 内容 |
|---|---|
| `make fmt` | `gofmt -s -w .` |
| `make lint` | `go vet ./...` |
| `make test` | `go test ./... -v` (PostgreSQL が要る → [testing.md](testing.md)) |
| `make plugin-test` | 同梱プラグインのテスト (別 module なので `./...` に含まれない) |
| `make plugin-doc-check` | `docs/plugins/authoring.md` の Go スニペットが実際にコンパイルできるか |
| `make plugin-dev` | プラグインを編集しながら動かす (`PLUGIN=plugins/status`) |

### マイグレーション

| ターゲット | 内容 |
|---|---|
| `make migrate-up` | 最新まで適用 |
| `make migrate-down` | 1段階ロールバック (`-steps 1`) |
| `go run ./cmd/migrate -direction down` | **全段ロールバック**。`-steps` 未指定は「全部」の意味で、schema が消える |
| `make migrate-create` | 新規マイグレーションファイル作成 |

#### 大規模テーブルへの index 追加

`golang-migrate/v4` の postgres driver は migration を **transaction 外** (auto-commit) で実行する (`runStatement` が `ExecContext` を直接呼ぶ)。そのため大規模テーブルへの index 追加では `CREATE INDEX CONCURRENTLY` を直接書ける:

```sql
-- migration/0000XX_large_index.up.sql
CREATE INDEX CONCURRENTLY IF NOT EXISTS "IDX_xxx" ON "yyy" ("zzz");
```

通常の `CREATE INDEX` は ACCESS EXCLUSIVE lock を取って書き込みを一時 block するが、`CONCURRENTLY` 付きなら Share lock のみで online で適用できる。production の数百万行クラスのテーブルでは推奨。

注意点:
- **`CONCURRENTLY` を含む migration は single-statement にする**。複数 statement を入れて途中で失敗すると、transaction 外実行ゆえ部分適用 (一部 statement だけ反映、残りは未適用) になり手動 cleanup が必要になる。1 ファイル 1 index が安全
- migration ファイル内に複数 statement を入れるなら `x-multi-statement=true` URL 拡張が必要 (現状未使用、`CONCURRENTLY` migration では避けること)
- `CONCURRENTLY` は失敗時に invalid index が残るので down migration で `DROP INDEX IF EXISTS` を必ず書く

### Docker

| ターゲット | 内容 |
|---|---|
| `make docker-build` | Dockerイメージビルド |
| `make docker-up` | `docker compose up -d` |
| `make docker-down` | `docker compose down` |

### 静的 parity ゲート

サーバー・ブラウザ・Docker 不要で走る。CI では `go test ./...` の中でも自動実行される。詳細は[シェイプドリフト検出](shape-drift.md)。

| ターゲット | 検出対象 |
|---|---|
| `make shapecheck` | レスポンス形状の drift。`make shapecheck-gen` で golden snapshot を再生成、`make shapecheck-report` でレポート出力 |
| `make errorid-check` | error id / HTTP status / kind の drift |
| `make limitspec-check` | ページネーションの default / max の drift |
| `make perm-check` | router middleware の権限が Misskey 本家より緩くないか |
| `make apicompat` | [API 互換性マトリクス](api-compat.md)を生成。内部で `make apicompat-routes` (route dump、stack 起動が必要) と `make apicompat-render` を実行する |

### e2e・互換性検証

いずれも隔離した compose project で動く。詳細は各ドキュメント参照。

| ターゲット | 内容 | 詳細 |
|---|---|---|
| `make playwright-up` `playwright-test` `playwright-down` `playwright-logs` | mk-go backend に対する Playwright spec | — |
| `make playwright-ts-up` `playwright-ts-test` `playwright-ts-down` | 同じ spec を Misskey TS backend に対して実行し、drop-in 互換を担保する | — |
| `make diff-up` `diff-test` `diff-down` `diff-logs` | mk-go と TS に同一リクエストを投げてレスポンスを値レベルで diff | [差分比較ハーネス](diff-e2e.md) |
| `make dropin-up` `dropin-test` `dropin-down` `dropin-logs` | TS 2 インスタンスの federation smoke | [Drop-in e2e](dropin-e2e.md) |
| `make dropin-mk-up` `dropin-mk-test` `dropin-mk-down` `dropin-mk-logs` | 上記の backend を mk-go に差し替えた overlay | 同上 |
| `make dropin-swap-test` | TS → mk-go 切替の state preservation を通しで検証 | 同上 |
| `make dropin-mkgo-born-test` | **mk-go 生まれの DB を TS に引き渡せるか** (= ロックインの有無) | 同上 |
| `make dropin-fedibird-test` | Fedibird-like AP mock との Ed25519 双方向 verify | 同上 |
| `make dropin-frontend-baseline` `dropin-frontend-up` `dropin-frontend-down` `dropin-frontend-logs` | 3 TS インスタンス + cypress | [Drop-in frontend e2e](dropin-frontend-e2e.md) |
| `make dropin-frontend-mk-up` `dropin-frontend-mk-down` `dropin-frontend-swap-test` | 上記の mk-go overlay と切替シナリオ | 同上 |
| `make federation-misskey-build` `federation-misskey-up` `federation-misskey-test` `federation-misskey-down` `federation-misskey-logs` | Misskey 本家インスタンスを立てて実際に連合させる | [ActivityPub連合](federation.md) |
| `make federation-misskey-e2e` | 上記を起動から撤去まで通しで実行 (CI の `federation` シナリオと同じ) | 同上 |
| `make e2e-submodule-init` | submodule を初期化 (本家フロントエンドの取得)。e2e 系の前提 | — |
| `make playwright-up` `playwright-test` `playwright-down` | Playwright によるフロントエンド / API テスト | [Playwright](playwright.md) |
| `make upstream-e2e-deps` `upstream-e2e-up` `upstream-e2e-migrate` `upstream-e2e-test` `upstream-e2e-down` | Misskey 本家の backend e2e をテスト本体無改変で mk-go に向けて実行 | [本家 backend e2e](upstream-backend-e2e.md) |

### ベンチマーク

| ターゲット | 内容 | 詳細 |
|---|---|---|
| `make bench-up` `bench-run` `bench-down` `bench-logs` | k6 で mk-go と Misskey 本家に同一負荷をかけて比較 | [pprof プロファイリング](bench-pprof.md) |
| `make queue-bench-all` (`queue-bench-up` `queue-bench-seed` `queue-bench-outbound` `queue-bench-inbound` `queue-bench-report` `queue-bench-down` `queue-bench-logs`) | BullMQ / asynq / mkq の 3-way スループット比較 | [queue-bench](queue-bench.md) |
| `make queue-bench-autoscale-run` `queue-bench-autoscale-down` `queue-bench-autoscale-logs` | worker 数 fixed16 / fixed64 / auto の drain time 比較 | [オートスケール設計](design/auto-scale-job-workers.md) |

### 本番 UDS

| ターゲット | 内容 |
|---|---|
| `make uds-init` `uds-build` `uds-up` `uds-down` `uds-down-v` `uds-logs` `uds-ps` | UNIX ドメインソケット構成の本番スタック操作 ([UDSデプロイ](docker-uds.md)) |
| `make uds-frontend-build` | 本番向けフロントエンドビルド |

> **警告**: `make uds-frontend-build` と `make e2e-frontend-build` は `third_party/misskey/built` に出力する。**本番コンテナがこのディレクトリを bind-mount している**ため、「ビルドが通るか確かめるだけ」のつもりで実行すると配信中のアセットが差し替わる。mk-go はエントリポイントを起動時に 1 回だけ解決してキャッシュするので、ハッシュが変わると HTML が消えたファイルを指したまま **404 でフロントが起動しなくなる**。
>
> - フロントの型チェックだけなら `third_party/misskey/packages/frontend` で `npx vue-tsc --noEmit` / `npx eslint` を直接叩く (Docker 不要で速い)
> - 本番へ反映する意図で実行した場合は、続けてコンテナを再起動すること

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

### 必須チェック (`.github/workflows/ci.yml`)

`main`と`develop`へのpush/PRで実行される。branch protectionのrequired checksは`build` / `test` / `lint`の3つ。

#### buildジョブ
`go build ./...`で全パッケージのビルド確認。

#### test-shardsジョブ + testジョブ
- `shard: [1,2,3,4]`の4-way matrixで並列実行。各shardが独立したPostgreSQL 18 / Redis 7のサービスコンテナを持つ
- 対象パッケージは`go list`でテストファイルを持つものだけに絞り、ImportPath順にソートしてから`NR % 4`で分配する。分配が決定的なので、パッケージが増えても各shardの担当は再現する
- `-race -count=1 -timeout 10m -coverprofile=... -covermode=atomic`
- パッケージ別カバレッジ閾値を各shard内で検証し、1つでも未達ならそのshardが失敗する

| パッケージ | 閾値 | 理由 |
|---|---|---|
| `internal/api/admin` | 80% | `handler_stubs.go`にSMTP / queue / DB集計等の外部依存が多く90%に届かない。現状83.8%と小マージンのため80%でロック |
| `internal/server` | 0% | 大部分が`router.go`のwire層 (handler配線 / middleware設定) で、e2e / drop-in test経由で実挙動を検証する設計。個別handler (`avatar.go`等) は`_test.go`で個別にカバーする運用 |
| `internal/testutil` | 0% | mock / test helper専用でproduction codeを含まない |
| `e2e` 配下 | 0% | 実挙動カバレッジで測る意味が薄い |
| それ以外 | 90% | |

- `test`ジョブは`needs: test-shards` / `if: always()`で全shardを束ね、branch protectionが要求する`test`という単一checkを公開する。いずれかのshardが失敗すれば`exit 1`

#### lintジョブ
- `go vet ./...`
- `gofmt -s -d .`で差分チェック (差分ありで失敗)

### 非ブロッキングのPRチェック

以下はPRで走るが**required checksには入っていない**ので、落ちてもマージはブロックされない。赤いチェックとして表示されるので、内容を確認して別PRで対処する。

| check | workflow | 内容 |
|---|---|---|
| `vulncheck` | CI | 依存・Go stdlib の**到達可能な**既知脆弱性 + Go version の pin 整合 |
| `frontend-check` | CI | fork frontend の型 (`vue-tsc --noEmit`) + `make plugins-all` と統合バイナリの build。**`make frontend-check` は型チェックだけ**なので、job 全体を手元で再現するには `make plugins-all && go build ./cmd/misskey` も要る |
| `plugin-tests` | CI | 同梱プラグインのテスト (別 module なので `go list ./...` に入らない) |
| `build-and-push` / `-bundled` | Docker | image がビルドできるか (PR では push しない) |
| `spec (mk-go 1/4)` 〜 `4/4` | Playwright | ブラウザからの統合互換。TS backend での実行は `workflow_dispatch` のみ |
| `e2e (1/4)` 〜 `4/4` | Upstream backend e2e | 本家の backend e2e が mk-go に対して通るか |
| `diff` | Diff e2e | mk-go と TS の**レスポンスの値**が一致するか |
| `swap-test` / `mkgo-born` / `ed25519-verify` / `federation` | Drop-in e2e | 切替・ロックイン・Ed25519・実連合の 4 シナリオ |

どれが何を守っているかの対比は [ci.md](ci.md) にまとめてある。

### nightly

PR では回らず schedule で実行されるものが 2 つある。

| workflow | 内容 | 時刻 |
|---|---|---|
| `Drop-in frontend e2e` | 3 TS インスタンス + cypress で frontend 視点の drop-in 互換 | 19:00 UTC |
| `Queue-bench smoke` | queue driver がジョブを落としていないか (`ok == sent`) | 17:30 UTC |

### CI失敗時の対応

- カバレッジ不足 → テストケースを追加してから再push
- `gofmt`差分 → `make fmt`を実行してから再push
- テスト失敗 → CIログを読み、ローカルで再現させてから修正する。`--no-verify`等でフックを飛ばさない
- testcontainersのskip-on-failure起因のflakeがあるため、PRと無関係な失敗は再実行で解消することがある
