# Drop-in e2e テスト (#364, Phase 13-1〜)

Misskey TS から mk-go への **drop-in 切替互換** を自動検証する e2e テスト基盤。
#362 で露呈した Redis キー名前空間違い / HTML→MFM 変換の取りこぼしのような
互換ギャップを、手動テストに頼らず CI / on-demand で再発検知することを目的とする。

## 構成

```
tests/dropin/
  gen-certs.sh        # 自己署名証明書 (a, b 用)
  instance_a.yml      # Misskey TS 設定 (instance A)
  instance_b.yml      # Misskey TS 設定 (instance B)
  nginx_a.conf        # SSL 前段 (a domain)
  nginx_b.conf        # SSL 前段 (b domain)
  conftest.py         # pytest fixtures (tests/federation/common/ の Client を再利用)
  test_smoke.py       # Phase 13-1 の smoke test

docker-compose.dropin.yml  # TS-A / TS-B stack
```

### なぜ共通 harness が `tests/federation/common/` にあるのか

mk-go は既に `tests/federation/` で mk-go ↔ Misskey TS 連合テストを持っており、
そこに `MisskeyLikeClient` (httpx ベース) が整備済である。drop-in テストは
さらに TS ↔ TS (後続フェーズで TS ↔ mk) に広げるだけなので client は共有する。

## 実行方法

すべて docker compose 経由。ホストに pnpm / Python をインストールしない。

```bash
# 1. インスタンス起動 (初回は misskey/misskey image の pull で数分)
make dropin-up

# 2. smoke test 実行
make dropin-test

# 3. 片付け (volume まで削除)
make dropin-down

# (任意) ログ追跡
make dropin-logs
```

## Phase 進捗

- [x] Phase 13-1 (#365): TS ↔ TS 基盤 + smoke test
- [x] Phase 13-2 (#367): mk-go 差し替え overlay + swap シナリオ test
- [x] Phase 13-3 (#372): 機能マトリクス拡充 (visibility 種別 / userList / specified DM)
- [x] Phase 13-4 (#374): GitHub Actions nightly 統合

## Phase 13-2: mk-go 差し替え (drop-in swap)

`docker-compose.dropin.mk.yml` overlay と bash orchestrator
(`tests/dropin/run-swap-test.sh`) で「TS-A backend を mk-go に差し替えても DB /
Redis 上の state がそのまま引き継がれる」ことを e2e で検証する。

### 通常実行

```bash
# 完全自動の swap シナリオ test (推奨)
make dropin-swap-test

# orchestrator は以下を順次実行する:
#   1. TS-A + TS-B stack 起動
#   2. pytest test_swap_setup.py    (alice/bob/follow/baseline note)
#   3. docker compose stop app-a    (TS-A backend 停止、DB / Redis は維持)
#   4. overlay で app-a を mk-go ビルドに差し替えて起動
#   5. pytest test_swap_verify.py   (timeline 残存、新規 reply / reaction の連合)
#   6. teardown
```

### 手動運用 (デバッグ向け)

mk overlay を直接立ち上げて確認したい場合:

```bash
make dropin-mk-up      # base + overlay (= mk-A + TS-B)
make dropin-mk-test    # smoke test を mk-A に対して実行
make dropin-mk-down    # cleanup
make dropin-mk-logs    # ログ追跡
```

注意: `dropin-mk-up` は **clean DB** から mk-A を起動するので、TS-A→mk-A の
state 引き継ぎは検証されない。state 検証は `dropin-swap-test` 専用。

## Phase 13-3: 機能マトリクス (現状カバー範囲)

`make dropin-swap-test` の verify 段階で以下を検証する:

| シナリオ | テスト | 状態 |
|---------|--------|------|
| baseline note の home timeline 残存 | `test_post_swap_baseline_note_preserved` | pass |
| home visibility ノート残存 | `test_post_swap_home_visibility_preserved` | pass |
| followers visibility ノート残存 | `test_post_swap_followers_visibility_preserved` | pass |
| specified visibility (DM) 残存 | `test_post_swap_specified_note_preserved` | pass |
| user list メタデータ残存 | `test_post_swap_user_list_preserved` | pass |
| user list timeline 残存 (membership 間接検証) | `test_post_swap_user_list_timeline_preserved` | pass |
| mk-A から remote bob への reply 配信 | `test_post_swap_alice_can_reply` | xfail (#369 待ち) |
| mk-A から remote bob への reaction 配信 | `test_post_swap_alice_can_react` | xfail (#369 待ち) |

未カバー (Phase 13-3.5 以降の検討対象):

- カスタム絵文字 (display name / note 本文の :emoji:) — TS で emoji を作成する admin API 周りが必要
- WebSocket streaming (`/streaming` channel 接続後の note push 受信)
- channel timeline 残存
- 双方向切替 (mk-A → TS-A 戻し)

## トラブルシューティング

### `misskey/misskey:2026.7.0` が pull できない

`docker login` 等の認証不要。pull が失敗する場合は network / rate limit。
compose 内で pull を再試行するか `docker pull misskey/misskey:2026.7.0` で
先読みしておく。

### 連合 follow が timeout する

両 instance が互いに到達できる必要がある。`make dropin-logs` で nginx / app の
エラーを確認する。自己署名証明書のため app-a / app-b は
`NODE_TLS_REJECT_UNAUTHORIZED=0` で起動している。

### 残ったリソースが後続テストを汚染する

`make dropin-down` が volume まで削除する (`down -v`)。named volume `certs` も
同時に削除されるため、次回 `dropin-up` で再生成される。

## CI 実行

`.github/workflows/dropin-e2e.yml` が 4 シナリオを **matrix で並列**に実行する。

| check 名 | make target | 見ているもの |
|---|---|---|
| `swap-test` | `dropin-swap-test` | TS→mk-go 切替で state が保たれるか |
| `mkgo-born` | `dropin-mkgo-born-test` | mk-go 生まれの DB を TS に引き渡せるか |
| `ed25519-verify` | `dropin-fedibird-test` | Fedibird-like mock との Ed25519 双方向 verify |
| `federation` | `federation-misskey-e2e` | 本物の Misskey TS との実連合 |

発火条件:

- **pull_request**: `internal/**` / `migration/**` / `tests/dropin/**` 等の paths に
  該当する変更のみ。ドキュメントだけの PR では回らない
- **workflow_dispatch**: 任意の ref を指定して手動実行

当初は nightly (schedule) だったが PR トリガーへ移行した (#2291)。nightly は失敗に
気付くのが翌日になるうえ、1 日分のマージがまとまってどの変更が壊したか特定しづらい。

`fail-fast: false` なので 1 つが落ちても他は完走する。これは重要で、この 4 つは実際に
**別々の壊れ方をする**。`ed25519-verify` は導入時から 2 箇所壊れていたが、`swap-test` が
緑だったため約 3 か月それに気付けなかった (#2360)。

PR の required check には**入れない**。理由:

- federation delivery の poll を含み若干 flaky で、merge ブロッカーには適さない
- 非ブロッキングを `continue-on-error` で実現しないこと。あれは job を成功扱いにする
  ので失敗が完全に不可視になる。job は正しく失敗させ、required に入れないことで
  非ブロッキングにする

失敗時は docker compose のログを `dropin-logs-<scenario>` artifact として 14 日保持する。
`swap-test` / `mkgo-born` は orchestrator 自身が `down -v` の前に `compose.log` /
`ps.log` を残しており、workflow が後から集めたものは `-post` 付きの別名で入る。
前者があればそちらが本命。

### `swap-test` と `mkgo-born` の違い

似て見えるが **DB を作った側が違う**。

|  | DB を作ったのは | 経路 |
|---|---|---|
| `swap-test` | TypeORM | TS → mk-go → TS |
| `mkgo-born` | **mk-go の migration** | mk-go → TS |

後者の方が厳しい。TS は一度も触っていない schema を受け取るので、カラム型・制約・
enum・index 名・default のどれかが TypeORM の期待とずれていれば起動しない。
`TestMigrationSeed_CoversUpstream` は seed 一覧と upstream migration file の
**静的な突き合わせ**に過ぎず、実際に TS を起動して確かめてはいない。

運用上これは**ロックインの有無そのもの**にあたる。「mk-go で始めた人が Misskey に
移れるか」に答えられるのはこの経路だけで、実際この経路の初回実行で、RSA 秘密鍵が
PKCS#1 のため TS 側の送信連合が全滅する不具合が見つかっている (#2380)。mk-go の
`ParseRSAPrivateKey` は PKCS#1 / PKCS#8 の両方を読めるため、**コードを読む限りでは
何も問題が無いように見える**類のバグだった。

`mkgo-born` が落ちた場合、段階から原因がほぼ特定できる。

| 落ちた段階 | 意味 |
|---|---|
| stage 4b (TS-A healthy 待ちで timeout) | mk-go の migration が作った schema を TypeORM が受け付けなかった |
| stage 4d (migrations digest 不一致) | migration seed (`000029`) に漏れがあり TS が再実行した |
| stage 5 (pytest) | schema は通ったがデータを読めない / 連合が続かない |
