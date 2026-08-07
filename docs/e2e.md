# E2E テスト環境 (Cypress + Misskey 本家フロントエンド)

mk-go は Misskey 本家の API / ActivityPub 互換性を最優先にしているため、
**本家のフロントエンドがそのまま動くこと**を継続的に検証する必要がある。
このドキュメントは、本家 Cypress e2e スイートを mk-go のバックエンドに
差し向けて動かすための設計と実行手順をまとめる。

Phase 11-1 でこの基盤を導入した。

## ゴール

- Misskey 本家の既存 cypress spec (signup / login / home / post-note ...) を
  無改変で mk-go バックエンドに対して実行する
- mk-go のフロントエンド実装は用意せず、本家のビルド成果物を SPA として配る
- 落ちた spec は個別 issue で追跡し、phase を分けて緑化する

## ライセンス境界

Misskey 本家は AGPL-3.0。AGPL コードを mk-go リポジトリにコピーすると
再配布扱いになり義務が生じるため、**本家コードは 1 行もコピーしない**。
代わりに git submodule で参照する:

```
third_party/misskey/   -> shiroha-a/misskey-ts (fork, tag 2026.7.0-mk.10)
e2e/cypress/           -> mk-go 側のラッパー (cypress.config.ts など。
                           specPattern が submodule を指す)
```

mk-go 自身の LICENSE 整備 (AGPL 化) はフォーク元オーナーの作業範疇なので、
このドキュメントでは扱わない。

## アーキテクチャ

```
+-----------------------------+
|  Cypress (docker)           |
|  electron + @cypress 15.11  |
+-------------+---------------+
              | HTTP, data-cy-* セレクタ経由
              v
+-----------------------------+
|  mk-go  (go run)            |
|  MK_TESTMODE=1              |
|  - GET /                    |  <- 本家フロント SPA (submodule の built/)
|  - POST /api/reset-db       |  <- 本 phase で新設 (TestMode=true のみ)
|  - POST /api/admin/...      |  <- 既存実装
|  - POST /api/signin-flow    |  <- 既存実装
|  - POST /api/signup         |  <- 既存実装
+-------------+---------------+
              |
      +-------+-------+
      |               |
      v               v
+-----------+   +-----------+
| Postgres  |   |  Redis    |
+-----------+   +-----------+
```

Cypress の `resetState` カスタムコマンドが `POST /api/reset-db` を必ず最初に
呼ぶため、mk-go 側にこのエンドポイントが無いと全 spec が死ぬ。本 phase では
`internal/api/test/handler.go` に実装し、**`config.TestMode=true` のときだけ
router に登録する**。

## `TestMode` フラグ

| 設定方法 | キー |
|---|---|
| YAML (`default.yml`) | `testMode: true` |
| 環境変数 | `MK_TESTMODE=1` |

`TestMode=true` の時にだけ以下が有効になる:

- `POST /api/reset-db` ― Redis を FLUSHDB し、`schema_migrations` 以外の
  すべてのユーザーテーブルを `DELETE ... CASCADE` する。最大 3 回までリトライ。
- 起動時に `slog.Warn` で大きな警告ログを吐く

> **本番で絶対に `MK_TESTMODE=1` にしない**。このフラグが立っている状態で
> `/api/reset-db` を叩くと、**DB が即座に空になる**。config loader は
> `TestMode=true` 検出時に WARN ログを出すので、気付けるようにしている。

## セットアップと実行

すべての node / cypress コマンドは Docker 経由で実行する (CLAUDE.md の
「パッケージはホストに入れない」規約)。Makefile がラップしている。

### 1. submodule を初期化する

```bash
make e2e-submodule-init
ls third_party/misskey/cypress/e2e/
```

### 2. 本家フロントエンドをビルドする

```bash
make e2e-frontend-build
```

数分〜10 分かかる。`third_party/misskey/packages/frontend/...` 配下に
ビルド成果物が入る。

### 3. Cypress の依存を入れる

```bash
make e2e-deps
```

### 4. mk-go を TEST MODE で起動する

別端末で:

```bash
MK_TESTMODE=1 \
MISSKEY_FRONTEND_DIR=$(pwd)/third_party/misskey/built/_frontend_vite_ \
MISSKEY_FRONTEND_DIST_DIR=$(pwd)/third_party/misskey/built/_frontend_dist_ \
MISSKEY_CLIENT_ASSETS_DIR=$(pwd)/third_party/misskey/packages/frontend/assets \
./built/misskey -config .config/default.yml
```

起動ログに以下が出ていれば OK:

```
WARN config: TestMode is enabled; destructive test endpoints (e.g. /api/reset-db) are active.
WARN test mode: /api/reset-db endpoint is registered
```

疎通確認:

```bash
curl http://localhost:3000/                       # フロント HTML
curl -X POST http://localhost:3000/api/reset-db   # 204 No Content
```

### 5. Cypress を実行する

```bash
make e2e-run
```

`make e2e-open` は `cypress open` を起動する (X11 forward が必要)。CI では
`e2e-run` を使う。

## 検証済みのエンドポイント互換性

Cypress カスタムコマンド (`third_party/misskey/cypress/support/commands.ts`)
が依存するエンドポイントと mk-go 側の対応:

| コマンド | エンドポイント | mk-go 側 | 備考 |
|---|---|---|---|
| `visitHome` | `GET /` | `internal/server/frontend.go` | 本家 SPA |
| `resetState` | `POST /api/reset-db` | `internal/api/test/handler.go` | Phase 11-1 で新設 |
| `registerUser(admin=false)` | `POST /api/signup` | 既存 | |
| `registerUser(admin=true)` | `POST /api/admin/accounts/create` | `internal/api/admin/handler.go` | `setupPassword` 必須 |
| `login` | `POST /api/signin-flow` | 既存 | |

## 既知の制限

- フロント build キャッシュ: `make e2e-frontend-build` は毎回 `pnpm install`
  からやり直す。CI で毎回は回さず、ビルド成果物は別ジョブでキャッシュする想定
- WebSocket / notification 系の spec: 現状 mk-go は通知 WS リアルタイム非対応
  (`docs/migration-from-ts.md` 参照)。該当 spec は落ちる可能性があり、落ちた
  分は issue 化して phase を分けて対応する
- ローカリゼーション: 本家 cypress は i18n 文字列を直接参照するケースがある
  ため、mk-go 側のロケール差異で落ちる可能性がある

## リスク

- `/api/reset-db` は **DB を全消去する**。`TestMode` の誤設定に最大限注意する。
  config loader は WARN ログを吐き、router は TestMode を再確認してから route
  を登録する二重ガードを入れている
- submodule の tag は mk-go の `MisskeyVersion` 定数と揃える (`2026.7.0`)。fork 独自の変更を積んだぶんは `-mk.N` suffix で区別する (現在 `2026.7.0-mk.10`)。
  ズレるとフロントと API スキーマが噛み合わない可能性がある

## 関連ファイル

- `internal/api/test/handler.go` ― `/api/reset-db` 実装
- `internal/api/test/handler_test.go` ― 91.9% カバー済の単体テスト
- `internal/config/config.go` ― `TestMode` フィールド
- `internal/server/router.go` ― TestMode ガード付き route 登録
- `e2e/cypress/` ― mk-go 側の Cypress ラッパー
- `Makefile` ― `e2e-*` ターゲット
- `third_party/misskey/` ― 本家 submodule (AGPL-3.0)
