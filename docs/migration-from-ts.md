# Misskey-TSからmk-goへの移行ガイド

本ガイドでは、既存のMisskey-TSインスタンスのバックエンドをmk-goに置き換え、同じデータベース・Redis・フロントエンド資産を共有させる手順を説明する。

## 前提条件

- Go 1.26+
- PostgreSQL 16+ (既存のMisskey-TSデータベース)
- Redis 7+
- git

## 1. クローンとビルド

```bash
git clone --recursive https://github.com/shiroha-a/mk.git mk-go
cd mk-go
go build -o built/misskey ./cmd/misskey
```

`--recursive` でsubmodule (`third_party/misskey`) も取得される。

## 2. フロントエンド資産の準備

mk-goはMisskey-TSと同じフロントエンドを利用する。2つの方法がある。

### 方法A: submoduleのフロントエンドをビルド (推奨)

```bash
make e2e-frontend-build
```

Docker内でフロントエンドがビルドされ、成果物は `third_party/misskey/built/` 配下に配置される。mk-goはデフォルトでこのパスを参照するため、環境変数の設定は不要。

### 方法B: 既存のMisskey-TSのビルド済み資産を使う

既にMisskey-TSが動作しているサーバーでは、そのビルド済み資産を環境変数で指定できる:

```bash
export MISSKEY_FRONTEND_DIR=/path/to/misskey/built/_frontend_vite_
export MISSKEY_FRONTEND_DIST_DIR=/path/to/misskey/built/_frontend_dist_
export MISSKEY_TWEMOJI_DIR=/path/to/misskey/packages/backend/node_modules/@discordapp/twemoji/dist/svg
export MISSKEY_CLIENT_ASSETS_DIR=/path/to/misskey/packages/frontend/assets
export MISSKEY_STATIC_DIR=/path/to/misskey/packages/backend/assets
```

## 3. 設定

`.config/default.yml` を作成する:

```yaml
url: https://your-instance.example.com
port: 3000

db:
  host: localhost
  port: 5432
  db: misskey        # 既存のMisskey-TSデータベース
  user: misskey
  pass: your_password

redis:
  host: localhost
  port: 6379

id: aidx             # Misskey-TS側のID生成方式と一致させること
```

> **重要:** `id` フィールドはMisskey-TSインスタンスで使われているID生成方式と必ず一致させること。Misskey-TS側の `.config/default.yml` を確認して正しい値を設定する。よく使われる値: `aidx`, `aid`, `meid`, `ulid`, `objectid`。

## 4. データベースマイグレーション

mk-goは既存のMisskey-TSテーブルには手を加えず、独自のテーブルを追加する形で共存する。既存データは保持される。

```bash
# ローカルビルドの場合
go run ./cmd/migrate -direction up

# Docker imageの場合 (migrateバイナリが同梱されている)
docker compose exec app /app/migrate -config .config/default.yml -direction up
```

これによりGo側で必要な追加テーブル (`app`, `auth_session`, `webhook`, `sw_subscription`, `chat_room`, `chat_message`, `bubble_game_record` 等) が作成される。

> **補足:** mk-goのマイグレーションは追加のみで、既存のMisskey-TSテーブルを変更しない。両バックエンドは同じデータベース上で共存できる。

## 5. Misskey-TSの停止

```bash
# 既存のMisskey-TSサーバーを停止する
# (停止方法はデプロイ手段による: systemd、pm2、docker 等)
systemctl stop misskey
# または
pm2 stop misskey
# または
docker compose stop web
```

## 6. mk-goの起動

```bash
./built/misskey -config .config/default.yml
```

方法Bで環境変数を使う場合は `source .env` してから起動する。

## 7. 動作確認

1. ブラウザで `https://your-instance.example.com` を開く
2. エントランスページがスタイル付きで正しく表示されることを確認する
3. 既存アカウントでログインする
4. 以下を確認する:
   - タイムラインに自分のノートが表示される
   - プロフィールページが正しく表示される
   - 通知が動作する
   - ファイルアップロードが動作する
   - リアクションが動作する

## Docker Composeで新規構築する場合

新しいインスタンスをDocker Composeで立ち上げる場合は以下で起動できる:

```bash
# 1. 起動前に必ずフロントエンドをビルドする (submodule + node_modules を取得し、
#    third_party/misskey/built/ に SPA 成果物を生成する)。
make e2e-frontend-build

# 2. 起動 (初回は image build も走る)
docker compose up -d --build
```

`docker-compose.yml` には one-shot の `migrate` サービスが含まれており、app 起動前に
DB マイグレーションが自動適用される (空 DB でも、TS から swap した既存 DB でも冪等)。
そのため上記の 2 ステップだけで mk-go が PostgreSQL および Redis と共に起動する。
詳細は `docker-compose.yml` を参照。

`make e2e-frontend-build` で生成した `third_party/misskey/built`(SPA の vite 成果物、約200MB)は、`docker-compose.yml` が bind-mount でコンテナに渡す（`MISSKEY_FRONTEND_DIR` 等で参照）。static-assets / twemoji / fluent-emoji / repo-assets は image に焼き込まれるためマウント不要。**この frontend ビルドを忘れると SPA の JS/CSS が 404 になりフロントエンドが表示されない**ので注意。

> bare-metal 起動(バイナリを repo root から実行)の場合は、mk-go がデフォルトで `third_party/misskey/built/` 等の相対パスを参照するため環境変数の設定は不要。docker では WORKDIR が `/app` で相対パスが効かないため、上記の bind-mount + 環境変数で渡す。

### Docker container の UID

mk-go のコンテナは Misskey-TS と同じ **UID/GID 991** で起動する。`./files` (drive ファイルストレージ) を host volume mount している場合、ホスト側ディレクトリは UID 991 が書き込めるパーミッションでなければならない。

- **TS から swap する場合**: 既に `./files` の中身が UID 991 で書かれているのでそのまま動く (drop-in 互換)
- **mk-go-only から旧 root 構成 (#621 以前) で運用していた場合**: 一度だけ `sudo chown -R 991:991 ./files` で所有権を揃える必要がある

## Misskey-TSへのロールバック

Misskey-TSに戻す場合の手順:

1. mk-goを停止する
2. 従来通りMisskey-TSを起動する

データベースは双方向に互換性があり、mk-goが追加したテーブルはMisskey-TSからは無視される。

## drop-in 互換性の現状 (2026-05-09 時点)

Playwright Phase 1-4 完了 (#744) で **96 spec / 35 directory / 242 endpoint cover (54.3%)** を mk-go と Misskey TS の両 backend で nightly 検証中。発見した drop-in 互換 drift は 40+ 件すべて解消済 (詳細: [api-compatibility.md](api-compatibility.md))。

- **API endpoint 互換**: 主要 endpoint (admin / notes / users / i / drive / chat / reactions / timeline / emoji / auth / federation / channels / hashtags / roles 等) は両 backend で同 status / 同 shape を返す
- **WebSocket チャンネル**: 19/19 実装済 (#125)
- **ActivityPub 連合**: 主要 Activity (Create / Delete / Update / Follow / Accept / Reject / Undo / Like / Announce / Block / Flag / Move / Add/Remove) は送受信対応 (詳細: [federation.md](federation.md))

## 既知の制限

### 未実装 / 設計差異

- **公開サインアップのメール認証フロー** — `emailRequiredForSignup` 有効時の pending user → メール確認フローは未実装
- **Reversi リアルタイム対戦** — ゲーム一覧 / WebSocket チャンネル / multiplayer は実装済 (Phase 3 spec verified)、初期実装 (#417) 由来の手動検証経路あり
- **サーバーマシン統計** — `enableServerMachineStats` 有効時に CPU/メモリ/ディスク情報を返すが、gopsutil相当の詳細度はない
- **chat/* の API 設計** — TS版とパス名・パラメータが異なる (mk-go 独自設計)
- **Identicon** — 生成される自動アバターの見た目が若干異なる
- **search backend** — `notes/search` の provider は `fulltextSearch.provider` で切替。既定 `sqlLike` で **Meilisearch 不要のまま動く** (PostgreSQL `ILIKE` fallback)。upstream TS strict-mode (400 UNAVAILABLE) で揃えたい operator は `provider: "none"` を opt-in で選べる (#877)。Meilisearch / pgroonga は optional
- **upstream 2026.7.0 まで追従済** — 2026.3.2 → 2026.5.1 → 2026.5.4 → 2026.6.0 → 2026.7.0 と段階的に追従した。各 release の差分は [docs/update/](update/) (`yyyymmdd*` 命名) を参照

### mk-go 独自挙動 (TS にない拡張)

- **リモートユーザー counts**: `users/show` でリモートユーザーの notesCount / followersCount / followingCount を origin instance の `/api/users/show` から取得して上書き (#943)。TS は自インスタンス観測値のみ表示するため数値が小さくなる問題を解消。フォロー一覧 / フォロワー一覧は引き続き local user のみ
- **mediaproxy アニメ pass-through**: GIF / APNG をリアクション / 絵文字ピッカー / プレビューで静止化せずに pass-through (#941)
- **URL preview の charset 自動正規化**: Shift_JIS / EUC-JP / ISO-2022-JP 等のページで title / description が文字化けしない (#942)
- **inbox handler の verify-in-worker 化** (#565): HTTP 受信スループットが TS の 2.6-2.8x

### Misskey-TSとの差異

- **タイムライン** — Redisキャッシュが空の場合 (サーバー再起動直後等) はDBクエリにフォールバックする
- **通知** — WebSocketによるリアルタイム配信は対応しているが、一部のイベントタイプで差異がある可能性がある

## トラブルシューティング

### ページが「Loading...」のまま進まない

- フロントエンド資産のパスが正しいか確認する
- 方法Aの場合: `ls third_party/misskey/built/_frontend_vite_/manifest.json`
- 方法Bの場合: `ls $MISSKEY_FRONTEND_DIR/manifest.json`

### スタイル/CSSが崩れる

- プロダクションビルドを使用していることを確認する (Viteのdevモードではない)
- `manifest.json` のエントリにCSSファイルが含まれていることを確認する: `cat third_party/misskey/built/_frontend_vite_/manifest.json | grep css`

### 絵文字が表示されない

- twemoji SVGファイルが配置されているか確認する
- 方法A: `ls third_party/misskey/packages/backend/node_modules/@discordapp/twemoji/dist/svg/1f44d.svg`
- 方法B: `ls $MISSKEY_TWEMOJI_DIR/1f44d.svg`

### favicon/アイコンが表示されない

- static assets のパスを確認する
- 方法A: `ls third_party/misskey/packages/backend/assets/icons/192.png`
- 方法B: `ls $MISSKEY_STATIC_DIR/icons/192.png`

### 再起動後にタイムラインが空になる

- これは期待通りの挙動。新規ノートは即時にタイムラインへ反映される
- 既存ノートは初回のDBフォールバッククエリ実行後に表示される

### ファイルアップロードが `CREDENTIAL_REQUIRED` で失敗する

- 認証ミドルウェアが `multipart/form-data` リクエストを正しく処理できているか確認する
- サーバーログで認証エラーを確認する

## 関連ドキュメント

- [docs/e2e.md](./e2e.md) — `third_party/misskey` submodule 経由で Misskey 本家の Cypress e2e スイートを mk-go に向けて実行する手順
- [docs/upstream-catch-up.md](./upstream-catch-up.md) — Misskey TS upstream の新 release を mk-go に取り込む際の triage / submodule bump / Wave 単位 PR 運用と、submodule bump PR マージ後の `git pull --recurse-submodules` 等の追従手順
