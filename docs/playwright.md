# Playwright e2e

mk-go のフロントエンド / API を実ブラウザから検証する e2e。spec は
`tests/playwright/specs/` にあり、現在 293 ファイル / 445 テスト
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

**1 スタックあたりは直列でしか回せない。** 293 spec のうち 174 が共有の root (alice) で
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

## CSP enforce で回す

`tests/playwright/instance.yml` は `frontendContentSecurityPolicy: enforce` を持つ
(#2788)。**実ブラウザでしか分からない CSP 回帰**を spec のゲートにするため。

Go 側の `TestFrontendCSP_HashesCoverRenderedInlineScripts` は HTML と CSP header の
突き合わせで、**ブラウザが実際に script を実行できるか**は見ない。inline event
handler が hash では通らない (`'unsafe-hashes'` が要る、#2786) のように、
ブラウザに実行させないと分からない挙動がある。

**`report-only` では駄目。** あちらは違反を報告するだけで script は動くので、
壊れた状態でも spec は緑になる。`specs/mkgo/ui/csp_enforce.spec.ts` は
header が `Content-Security-Policy` (report-only でない) であることを先に確認する
— この確認が無いと `instance.yml` から設定が消えても violation ゼロで緑になり、
「CSP を検査していないのに通る」状態に落ちる。

ゲートとして効いていることは実測済み (7 変異とも spec が落ちる):

| 変異 | 落ちる assertion |
|---|---|
| `instance.yml` を `off` に | CSP header が無い |
| `instance.yml` を `report-only` に | report-only が返っている |
| `script-src` に `'unsafe-inline'` を戻す | `'unsafe-inline'` が戻っている |
| hash を 1 つも登録しない | `script-src` に hash が無い |
| `bootGlobals` を hash 計算から外す | inline script が block された |
| shell に hash 無しの `<script>` を 1 つ足す | inline script が block された |
| `loader.JS` だけ hash から外す (SPA が一切 mount しない) | inline script が block された |

最後の 1 つが**このゲートの主目的そのもの**で、`waitForFunction` の
`TimeoutError` を catch している理由でもある。catch しないと mount 待ちの時間切れが
throw されて assertion に届かず、失敗メッセージが
`page.waitForFunction: Timeout ...` になる — **どの script が何の directive で
落ちたかという一番欲しい情報が、まさにそのときだけ失われる**。

### img-src の違反は既知

全 spec を enforce で回すと `img-src` の violation が 360 件出る (292 spec 時点の実測。以降は再測定していない)。**`script-src` /
`default-src` / `worker-src` はゼロ。** 数え方:

```bash
L='docker logs mk-playwright-mkgo-1'
$L 2>&1 | grep -c msg=csp-report                                     # 総数
$L 2>&1 | grep msg=csp-report | sed -E 's/.*directive=([a-z-]+).*/\1/' | sort | uniq -c
$L 2>&1 | grep msg=csp-report | grep -oE 'blockedUri="?https://[^/"]+' | sed 's|.*//||' | sort | uniq -c
$L 2>&1 | grep msg=csp-report | grep -oE 'documentUri=\S+' | sort | uniq -c
```

内訳:

| 出どころ | 件数 | 実害 |
|---|---|---|
| `/about-misskey` の外部画像 (`assets.misskey-hub.net` 112 + `avatars.githubusercontent.com` 12) | 124 | **クレジット画像が全滅する** |
| spec のフィクスチャが作る実在しないホスト (`example.invalid` / `example.test` / `pwad-*.invalid`) | 236 | 無し。CSP が無くても読めない |

**集計の正規表現でクォートを必須にしないこと。** `slog` の `TextHandler` は値に
`?` や空白を含むときだけクォートする。`avatars.githubusercontent.com/u/...?v=4` は
`blockedUri="https://..."` になるが、`assets.misskey-hub.net/patrons/....jpg` は
クォートが付かない。`blockedUri="https://` で引くと**後者 348 件を丸ごと落として**
「sponsors の違反は出ていない」という誤った結論になる (実際に踏んだ)。上の
コマンドが `"?` にしてあるのはこのため。

`/about-misskey` を単独で開いて 8 秒待つと **62 件**で、
`third_party/misskey/packages/frontend/src/pages/about-misskey.vue` の外部 `<img>`
の枚数と一致する (contributor 6 + sponsors 6 + patron 50)。`loading="lazy"` も
`v-if` も折りたたみも無いので、ページを開いた時点で全部読まれる。上の 124 件は
spec が `/about-misskey` を 2 回開くため。

**CSP は緩めない。** `img-src 'self' data: blob:` はリモート画像を media proxy 経由に
する設計と対で、外部 origin を許すと投稿経由でトラッキング画像を読ませる経路が開く。
`/about-misskey` は upstream 由来のクレジットページで #2700 の作り直し対象なので、
外部画像はそちらで解消する。

**本番では既にこのページの外部画像が出ていない** (同一オリジンの
`/client-assets/about-icon.png` などは出る)**。** `deploy/uds/config/default.yml` は
`frontendContentSecurityPolicy: enforce` を持つ (`off` は `internal/config` の
既定値であって本番設定ではない)。将来の注意点ではなく、**現行の既知の不具合**として
#2700 まで残る。

### embed も enforce の対象

`/embed/` にも同じ CSP が付く (#2789)。既存の `specs/upstream/ui/embed/embed.spec.ts`
が **iframe を張って投稿本文が読めること**まで見るので、CSP で bundle や inline
script が block されればそこで落ちる。実測: embed の inline script を hash 計算から
外すと「iframe 内で描画され本文が読める」が `element(s) not found` で落ちた。

embed 経由の violation は **0 件** (数え方:
`docker logs mk-playwright-mkgo-1 2>&1 | grep msg=csp-report | grep -oE 'documentUri=\S+' | grep /embed/`)。

**ただし spec が開くのは本文だけのローカル投稿の embed 2 ページ。** メディア添付・
引用・カスタム絵文字を含む embed は未検証で、object storage 構成の
`img-src` / `media-src` / `connect-src` も実ブラウザでは踏んでいない
(Go 側の `TestEmbedShell_CSP` が header の値だけを固定している)。

### ゲートの守備範囲

`csp_enforce.spec.ts` が見るのは **未サインインの `/` が mount を終えた時点**まで。
mount 後に遅延ロードされる chunk (route 単位の code splitting、shiki の
`https://esm.sh` 動的 import) やサインイン後の画面で起きる違反は数えない。
issue #2788 の主目的 (shell に hash 無しの inline script が入る) はこれで足りる。

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
