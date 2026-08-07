# docker-compose で動かす UDS-only スタック

Phase 12-1 で入った UNIX domain socket (UDS) 対応を使って、mk-go の全コンポーネントを TCP 無しで動かす参照デプロイメントです。ブラウザ側には本家 Misskey の vite ビルド成果物をそのまま配信するので、`http://localhost/` を開けば Misskey の UI が出ます。

- nginx が受けるのは host の 80 番だけ (HTTP のみ)
- nginx → mk-go は UDS (`/run/mkgo/mkgo.sock`)
- mk-go → postgres は UDS (`/var/run/postgresql/.s.PGSQL.5432`)
- mk-go → valkey は UDS (`/run/valkey/valkey.sock`、`port 0` で TCP 完全無効)

既存の `docker-compose.yml` (TCP 版 quick-start) とは別ファイル (`compose.uds.yaml`) として併存しているので、従来の `docker compose up -d` 体験は壊れません。

## 前提条件

- Docker と docker compose v2
- `third_party/misskey` サブモジュールの初期化 (tag `2026.7.0-mk.10` が pin されています)
- host 側のインストールは不要です。フロントエンドのビルドも docker 経由で行います。

```sh
git submodule update --init --recursive third_party/misskey
```

## 初回セットアップ

### 0. 設定ファイルのコピー

`compose.uds.yaml` と `deploy/uds/config/default.yml` はデプロイ先ごとに URL や公開ポートを書き換える想定で gitignore しています。リポジトリには `.example` 版のみ含まれているので、初回のみ以下でコピーしてください。以降は自分の環境に合わせて自由に編集して構いません。

```sh
make uds-init
```

`uds-init` は order-only prerequisite で実装されているので、ファイルが既にある場合は何もしません (`.example` を更新してもローカル編集は上書きされない)。`uds-build` / `uds-up` / `uds-down` / `uds-down-v` / `uds-logs` / `uds-ps` も同じ prerequisite を持つため、コピー忘れでエラーになることはありません (`uds-frontend-build` は compose / config を参照しないので対象外)。

### 1. 本家フロントエンドのビルド

初回のみ、本家 Misskey の vite ビルドを行います (3〜10 分)。`make uds-frontend-build` は既存の `e2e-frontend-build` と同一のターゲットで、`node:20-bookworm` コンテナの中で `pnpm install --frozen-lockfile && pnpm build` を走らせます。

```sh
make uds-frontend-build
```

終了すると以下のパスに成果物ができます。`compose.uds.yaml` が read-only で bind mount します。

- `third_party/misskey/built/_frontend_vite_/manifest.json`
- `third_party/misskey/built/_frontend_dist_/`

なお `pnpm install --frozen-lockfile` が本家のnode_modulesも生成するため、以下も同時に揃います。これらは `deploy/uds/Dockerfile.mkgo` が `COPY` で runtime image に焼き込み、mk-go の `/twemoji/*` / `/assets/*` ルートから配信します。

- `third_party/misskey/packages/backend/node_modules/@discordapp/twemoji/dist/svg/` (twemoji SVG set、約18MB)
- `third_party/misskey/assets/` (`ai.png` 等、約684KB)

`make uds-frontend-build` を省略すると image ビルド時にこれらの存在チェックが fail してビルドが止まります。

### 2. スタックの起動

```sh
make uds-up
```

以下が順番に立ち上がります。

1. `postgres` — `/var/run/postgresql/.s.PGSQL.5432` を作成
2. `valkey` — `/run/valkey/valkey.sock` を作成 (TCP は `port 0` で無効)
3. `mkgo` — マイグレーション実行後、`/run/mkgo/mkgo.sock` で HTTP listen
4. `nginx` — host の 80 番で受けて mk-go の socket に proxy

```sh
make uds-ps
```

全サービスが `healthy` になれば準備完了です。

### 3. 動作確認

```sh
curl -i http://localhost/
curl -i -X POST http://localhost/api/meta -H 'Content-Type: application/json' -d '{}'
```

ブラウザで `http://localhost/` を開き、Misskey のログイン画面が出ることを確認してください。DevTools の Network タブで `/streaming` が `ws://localhost/streaming` で確立していれば WebSocket の upgrade も成功しています。

## UDS で繋がっていることの確認

どのサービスにも `ports:` を切っていないので、ホスト側で 80 番以外の TCP は一切 listen していないはずです。

```sh
ss -tlnp | grep -E ':(80|3000|5432|6379)'
```

80 番だけが出て、3000 / 5432 / 6379 は出てこないことを確認してください。

socket ファイルの実体も `docker compose exec` で覗けます。

```sh
docker compose -f compose.uds.yaml exec mkgo ls -la /var/run/postgresql /run/valkey /run/mkgo
docker compose -f compose.uds.yaml exec nginx ls -la /run/mkgo
```

## 停止 / クリーンアップ

```sh
# コンテナだけ停止 (named volume のデータは残る)
make uds-down

# named volume まで削除 (DB / valkey / drive files が全部消える)
make uds-down-v
```

## ログ確認

```sh
make uds-logs            # 全サービスのログを follow
docker compose -f compose.uds.yaml logs -f mkgo
docker compose -f compose.uds.yaml logs -f nginx
```

## トラブルシューティング

### `third_party/misskey` 自体が空 (submodule 未初期化)

`compose.uds.yaml` は `./third_party/misskey/built` と `./third_party/misskey/packages/frontend/assets` を read-only で bind mount しています。submodule をまだ取得していない場合、このディレクトリが空になっていて mount source が存在せず `uds-up` がエラーで止まります。

```sh
git submodule update --init --recursive third_party/misskey
make uds-frontend-build
```

submodule のチェックアウト先 (tag `2026.7.0-mk.10`) は本リポジトリで pin 済みです。

### `third_party/misskey/built` が無い

`make uds-frontend-build` を先に実行してください。bind mount の source が存在しないと `uds-up` が失敗します。

### マイグレーションが失敗してコンテナが crash loop する

`mkgo-entrypoint.sh` は `set -e` で migrate を実行してから mk-go server を起動します。migrate が失敗するとそのまま container exit し、`restart: unless-stopped` のため compose が再起動 → 同じエラーで再度 exit、というループに入ります。

検出方法:

```sh
make uds-logs                                            # 全サービスのログを follow
docker compose -f compose.uds.yaml logs mkgo | tail -50  # mkgo だけ、過去ログ含めて
```

`[mkgo-entrypoint] running migrations...` の直後に migrate がエラーを吐いていれば該当します。

対応:

- スキーマが壊れている場合は手動で `psql` で問題を解消する (`./built/migrate -direction down -steps 1` で直前の migration を巻き戻し、修正後に `up` し直す)
- volume 自体がおかしい場合は `make uds-down-v` で named volume を消して綺麗な状態から再構築する (**DB データは全部消える**ので注意)
- 開発中の migration 自体に問題があれば、まず `go run ./cmd/migrate -direction down -steps 1` で 1 段戻してから修正する

### `/healthz` が 404 になる

mk-go 側の実装変更で `/healthz` のパスが変わっている可能性があります。`internal/server/router.go` を grep して、存在するパスに `deploy/uds/Dockerfile.mkgo` の curl 先を合わせてください。

### nginx が `connect() to unix:/run/mkgo/mkgo.sock failed (13: Permission denied)`

`chmodSocket: "666"` が正しく反映されていません。`deploy/uds/config/default.yml` を確認し、mk-go 側のログ `[server] listening on unix:` が `mode=0666` になっているか見てください。

### valkey が `Creating Server TCP listening socket *:6379: bind` で起動しない

`deploy/uds/valkey/valkey.conf` の `port 0` が効いていません。`command:` で正しく `valkey.conf` を読み込めているか、bind mount のパスを確認してください。

## TLS を足したい場合

このデプロイは HTTP のみです。本番で TLS を張りたい場合は以下のいずれかで。

- 上位に Caddy や Cloudflare Tunnel を置いて TLS 終端する (nginx の 80 番はそのまま)
- `deploy/uds/nginx/mkgo.conf` に 443 listen + `ssl_certificate` を足す (`tests/federation/common/nginx-mkgo-ssl.conf` が参考になる)

## 構成ファイル一覧

`compose.uds.yaml` と `deploy/uds/config/default.yml` はリポジトリには `.example` 版のみ存在し、`make uds-init` (または初回の `make uds-*`) で `.example` からコピーされる。以下の表はセットアップ後のローカルファイル名で記載している。

| ファイル | 役割 |
|---------|------|
| `compose.uds.yaml` (`.example` から生成) | 全サービスを繋ぐ compose エントリポイント |
| `deploy/uds/Dockerfile.mkgo` | mk-go runtime image (migrate 同梱 + curl) |
| `deploy/uds/mkgo-entrypoint.sh` | migrate → exec misskey |
| `deploy/uds/nginx/mkgo.conf` | UDS upstream + WebSocket upgrade 付き nginx 設定 |
| `deploy/uds/valkey/valkey.conf` | `port 0` + UNIX socket listen の valkey 設定 |
| `deploy/uds/config/default.yml` (`.example` から生成) | UDS 前提の mk-go 設定 |
