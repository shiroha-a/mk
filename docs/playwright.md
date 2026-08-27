# Playwright e2e

mk-go のフロントエンド / API を実ブラウザから検証する e2e。spec は
`tests/playwright/specs/` にあり、現在 290 ファイル / 439 テスト
(`npx playwright test --list` で数えられる)。

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

ただし **TS backend は `workflow_dispatch` 専用**で、PR では回らない。TS baseline の
価値は「spec が mk-go の挙動を正解として書かれていないか」を検出する一点にあり、
**upstream が変わらない限り答えも変わらない**ので、常時回す意味が薄いため。submodule を
bump したときに回す (`docs/upstream-catch-up.md`)。

## CI での扱い

`.github/workflows/playwright.yml` が `pull_request` (paths フィルタ) と
`workflow_dispatch` で発火する。nightly から PR トリガーへ移行済み (#2291)。

**4 シャード並列** (`--shard=i/4`、`fail-fast: false`)。check 名は
`spec (mk-go 1/4)` 〜 `4/4`。

**1 スタックあたりは直列でしか回せない。** 290 spec のうち 173 が共有の root (alice) で
サインインし、instance meta は全 spec が共有する。Playwright はファイル単位で並列化する
ので `workers` を上げると `profile_iscat_toggle` と `profile_isbot_toggle` が同じ
アカウントを、`admin_branding_save` と `about_page_render` が同じ meta を取り合う。
**並列度はスタックごと分ける = シャードでしか稼げない** (#2609)。

PR の required check には**含めない**。TS image の pull と spec 増加で実行時間が
伸びるうえ、外部 image 由来の flaky 要素があるため。

失敗時は `tests/playwright/test-results/` (trace / screenshot 含む) と docker
compose logs を `playwright-results-<backend>-<shard>` /
`playwright-logs-<backend>-<shard>` として 14 日保持する。

**録画はしない** (`video: 'off'`)。CI は成功 run の成果物を一切アップロードしないので
録画しても捨てるだけで、失敗 run でも実測 webm 256 本のうち失敗に対応するのは 2 本
だけだった。調査材料は trace が担う。手元で欲しいときは `--video=on` を渡す (#2609)。

## spec を書くときの注意

`_spec.ts` は silent skip される。ファイル名は必ず `.spec.ts`。

vite の hash class を selector に使わない (`[class*="_button_"]` 等)。production
ビルドで hash が変わると落ちる。`data-testid` か role / text で取る。

**`.ts` の隣に `.js` を残さない。** import は拡張子なし
(`from '../../../../fixtures/rate_limit'`) で、同名の `.js` があると playwright は
そちらを先に解決する。`tsc` を手で走らせた残骸が典型で、import 先だけでなく
**spec 本体の `.js` も出る** — そちらは shadow されず `.ts` と両方走る (既定の
`testMatch` が `.js` も拾う)。**エラーの有無で切り分けない**
— 症状が「`.ts` を直したのに効かない」だけのこともあれば、欠けた export が
`undefined` として流れて無関係に見える場所で落ちることもある。

`tests/playwright/.gitignore` の `*.js` が止めるのは commit までで、**手元の
shadowing は止まらない** (生成された時点で import 先の `.ts` は読まれていない)。container 実行
(`make playwright-check`) も spec を bind mount するので同じ。疑ったら探して `rm`:

```bash
find tests/playwright -name '*.js' \
  -not -path '*/node_modules/*' -not -path '*/playwright-report/*'
```

素の `git status` には出ず (`--ignored` が要る)、`git clean -fd` も消してくれない。
`playwright-report/` は `--reporter=html` を手で渡したときだけ生成されるが、その
viewer 資産で `.js` が 7 本出るので除外に入れてある。

3 件とも実際に踏んだ罠。

## 関連

- [ci.md](ci.md) — CI 全体の構成
- [dropin-e2e.md](dropin-e2e.md) — drop-in 切替の検証 (別系統)
- [upstream-backend-e2e.md](upstream-backend-e2e.md) — 本家の backend e2e を mk-go に向ける (別系統)
