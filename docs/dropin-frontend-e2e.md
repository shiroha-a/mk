# Drop-in frontend e2e テスト (#380, Phase 14-)

Misskey TS の実フロントエンドが期待する挙動を cypress で固定し、TS-A backend
を mk-go に差し替えた時も同じ挙動が得られるかを検証する e2e 基盤。

## 目的

- `pytest` ベースの Phase 13 e2e (`tests/dropin/`) は API レベルでの state
  preservation を確認するが、フロントエンド描画層のバグ (画像表示 / 削除反映 /
  emoji 描画 等) までは捕捉しにくい。
- cypress で実ブラウザ (electron) から TS フロントエンドを動かし、観測可能な
  挙動を regression test として固定する。
- 親 issue: #380、サブ: #381 (Phase 14-1、本ドキュメントの対象), #382〜 (Phase 14-2 以降)。

## 構成

```
  TS-C ─┐
        ├─ mutual follow + activities ─┐
  TS-B ─┤                              │
        └────────────────────┬─────────┤
                             ▼         ▼
                            TS-A → [swap, Phase 14-3] → mk-A
```

3 インスタンス (A / B / C) + 各々独立の Postgres / Redis / nginx + cypress runner。
Phase 14-1 では 3 台とも TS のまま (baseline)。mk 差し替え overlay は Phase 14-3。

```
tests/dropin_frontend/
  gen-certs.sh             # a / b / c + bundle.pem 用自己署名証明書
  instance_a.yml           # TS 用 default.yml (A)
  instance_b.yml
  instance_c.yml
  nginx_a.conf             # SSL 前段 (alias: a)
  nginx_b.conf
  nginx_c.conf
  cypress/
    cypress.config.ts      # electron self-signed cert 許容
    tsconfig.json
    package.json
    support/
      e2e.ts               # uncaught 抑制
      api.ts               # cy.request wrapper (createRootOrSignin, retryUntil 等)
    e2e/
      smoke.cy.ts          # baseline spec (Phase 14-1)
  run-frontend-baseline.sh # bash orchestrator

docker-compose.dropin-frontend.yml
```

## 実行

```bash
# baseline (3 TS のみ) 1 回走らせて cypress spec が全 pass することを確認
make dropin-frontend-baseline

# 手動で stack だけ上げて中に入りたいとき
make dropin-frontend-up
docker compose -f docker-compose.dropin-frontend.yml --profile test run --rm cypress-runner
make dropin-frontend-down

# ログ追跡
make dropin-frontend-logs
```

## Phase 進捗

- [x] Phase 14-1 (#381): 3 TS 基盤 + cypress smoke spec
- [x] Phase 14-2 (#387): spec マトリクス拡充 (visibility / userList / cross-instance / delete)
- [x] Phase 14-3 (#394): mk-go 差し替え overlay + swap orchestrator + nightly CI

## Phase 14-2 カバー spec

| spec | 内容 | 状態 |
|------|------|------|
| `smoke.cy.ts` | ping / webfinger / A follows B / C note → A | pass |
| `visibility.cy.ts` | public / home / followers / specified (DM) 4 種 | pass |
| `user_list.cy.ts` | alice が list 作成 + bob 追加 + list timeline 取得 | pass |
| `cross_instance_view.cy.ts` | charlie note を A/B 両方から observe して一致確認 | pass |
| `delete_note.cy.ts` | charlie 削除 → A/B timeline から消える | pass |
| `reply_chain.cy.ts` | charlie → bob reply の 2 hop | skip (#389 で調整後 activate) |
| `federation_allowlist.cy.ts` | A の federation mode を `specified [B]` / `none` に切替えて A→B 通過 / A→C ブロックを検証 | pass (#536) |

attachment / emoji / reaction は Phase 14-2.5 以降 (admin emoji 投入フロー / 
remote image ingest (#378) / reaction deliver (#369) の条件整備が必要)。

## Phase 14-3: mk-go 差し替え overlay + swap orchestrator

`docker-compose.dropin-frontend.mk.yml` overlay + `tests/dropin_frontend/run-frontend-swap-test.sh` orchestrator で、**TS-A backend を mk-go に差し替えた後も cypress spec が pass する** ことを検証する。

### 実行

```bash
# 完全自動の swap シナリオ test (推奨)
make dropin-frontend-swap-test

# orchestrator の流れ:
#   1. TS-A / TS-B / TS-C stack 起動 (baseline)
#   2. CYPRESS_MODE=baseline で cypress run (12 passing)
#   3. docker compose stop app-a (TS-A backend 停止、DB / Redis は維持)
#   4. overlay で app-a を mk-go に差し替えて起動
#   5. CYPRESS_MODE=swap で cypress run (8 passing + 5 skipped)
#   6. teardown
```

### swap モードで skip される spec (既知のバグ)

| spec / test | skip 理由 |
|------------|-----------|
| `delete_note.cy.ts` | mk-A が inbound Delete activity を fanout cache から purge しない (#379) |
| `reply_chain.cy.ts` | federation queue back-pressure で flaky (Phase 14-2 から継続 skip, #389) |
| `user_list.cy.ts` 2 本 | `users/lists/push` が既 member 時に 500 を返す (#396) |
| `visibility.cy.ts` specified DM | inbound specified Note が mk-A の mentions に現れない (#397) |

baseline (all TS) ではこれらも全 pass するため、skip は `CYPRESS_MODE=swap` 時のみ発動する。対応 issue 修正後に `skipInSwap` を外す。

### CI nightly

`.github/workflows/dropin-frontend-e2e.yml` で毎日 19:00 UTC (JST 04:00) に develop ブランチに対して `make dropin-frontend-swap-test` を実行する。失敗時は docker compose logs + cypress 成果物 (screenshots / videos) を `dropin-frontend-logs` artifact として 14 日保持する。

`workflow_dispatch` で mode 入力 (baseline / swap) を選択可能。

## トラブルシューティング

### cypress が self-signed cert を拒否する

`cypress.config.ts` で `setupNodeEvents` 内から `--ignore-certificate-errors`
を electron に渡している。spec 側は何もしなくて良い。

### `misskey/misskey:2026.7.0` の pull 失敗

`docker login` 不要。network / rate limit の可能性。`make dropin-frontend-up`
前に `docker pull misskey/misskey:2026.7.0` で先読みする手もある。

### cypress runner のログが流れない

`--rm` で使い捨て起動なので終了時にログが消える。persistent に確認したい時は
`--profile test run cypress-runner` (without `--rm`) で起動したあと
`docker logs <name>` で追う。

### 3 インスタンスが互いに到達できない

docker compose の `networks: [dropin_frontend]` で共有しているため通常は
自動解決する。`a` / `b` / `c` という alias で nginx が待ち受けるので、
federation URL は `https://a/`, `https://b/`, `https://c/` を使う。
