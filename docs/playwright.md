# Playwright e2e

mk-go のフロントエンド / API を実ブラウザから検証する e2e。spec は
`tests/playwright/specs/` にあり、現在 269 ファイル / 351 テスト。

Cypress からの移行完了に伴い、frontend e2e はこちらに一本化した (#2437)。本家も
Cypress を廃止して Playwright へ移行しており、参照先が消滅したため mk-go 側の
Cypress ラッパー (`e2e/cypress`) は削除した。

## 実行

```bash
# mk-go backend に対して実行
make playwright-up      # postgres / redis / mkgo を起動
make playwright-test    # spec 実行
make playwright-down    # volume ごと撤去

# Misskey TS backend に対して実行 (期待値そのものの検証)
make playwright-ts-up
make playwright-ts-test
make playwright-ts-down
```

`make playwright-check` は起動から実行までを一括で行う (クリーン DB 前提)。

## なぜ TS backend にも投げるのか

spec は **upstream Misskey TS の API 互換挙動を期待値として書いている**。
mk-go に対してだけ回すと「mk-go がその通り動く」ことしか分からず、**期待値自体が
upstream と食い違っていても気付けない**。

同じ spec を本家 backend にも投げて両方 pass することで、期待値が upstream の
実挙動と一致していることを担保する。

nightly CI (`.github/workflows/playwright.yml`) が `backend = [mk-go, ts]` の
matrix で両方を並列実行する。`fail-fast: false` なので片方が落ちても他方は完走する。

## CI での扱い

PR の required check には**含めない**。TS image の pull と spec 増加で実行時間が
伸びるうえ、外部 image 由来の flaky 要素があるため。drop-in shape の regression は
nightly で検出する運用。

失敗時は `tests/playwright/test-results/` (trace / screenshot 含む) と docker
compose logs を artifact として 14 日保持する。

## spec を書くときの注意

`_spec.ts` は silent skip される。ファイル名は必ず `.spec.ts`。

vite の hash class を selector に使わない (`[class*="_button_"]` 等)。production
ビルドで hash が変わると落ちる。`data-testid` か role / text で取る。

どちらも実際に踏んだ罠。

## 関連

- [ci.md](ci.md) — CI 全体の構成
- [dropin-e2e.md](dropin-e2e.md) — drop-in 切替の検証 (別系統)
- [upstream-backend-e2e.md](upstream-backend-e2e.md) — 本家の backend e2e を mk-go に向ける (別系統)
