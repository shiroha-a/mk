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

## CI nightly 実行 (Phase 13-4)

`.github/workflows/dropin-e2e.yml` で `make dropin-swap-test` を以下のタイミングで実行する:

- **schedule**: 毎日 18:00 UTC (= JST 03:00) に develop ブランチに対して
- **workflow_dispatch**: GitHub Actions UI から手動実行 (任意の ref を指定可)

PR の required check には**入れない**。理由:

- 1 回 8-10 min かかり、PR 単位の check として重い
- federation delivery の poll を含み若干 flaky
- drop-in 互換は本質的にバージョン横断の確認なので nightly で十分

失敗時は docker compose のログを `dropin-logs` artifact として 14 日保持する。Actions 上で download して原因調査する。
