import { defineConfig } from '@playwright/test';

// #744 Phase 1 PR-1: 基盤と smoke spec のみ。後続 PR で multi-browser /
// projects / use.storageState を拡張する。
export default defineConfig({
  testDir: './specs',
  // globalSetup: Redis を flush して rate limit counter をゼロから始める
  // (#744 PR-2)。signup endpoint の 1h 5 回制限が test 累積で 429 になる
  // のを防ぐ。詳細は global-setup.ts のコメント参照。
  globalSetup: './global-setup.ts',
  // 各 spec はタイムアウト 30s を上限。signup / signin など API レイテンシ
  // しか伴わないので余裕を持たせて 30s で十分。
  timeout: 30_000,
  expect: {
    timeout: 10_000,
  },
  // CI 統合は後続 PR。本 PR はローカル make 経由で動かすので reporter は
  // list のみ。pretty な HTML report は CI 統合時に追加する。
  reporter: [['list']],
  // Phase 1 では全 spec を chromium で 1 回ずつ実行。multi-browser は
  // CI 統合 PR で `projects` を追加する。
  use: {
    baseURL: process.env.MK_BASE_URL ?? 'https://mkgo.local',
    // API テスト中心なので screenshot / trace は失敗時のみで十分。
    screenshot: 'only-on-failure',
    trace: 'retain-on-failure',
    // 全 spec で動画を保存する (pass / fail どちらも tests/playwright/
    // test-results/ に webm 出力)。batch 5 以降は popup menu / contextmenu
    // / MkUserSelectDialog 等 multi-step interaction が増え、失敗時の
    // スクショ 1 枚では原因特定が辛くなった。pass 時の動画も spec の
    // 挙動を後から目視確認する資料として価値があるので、'on' で常に
    // 残す。size は 800x600 デフォルトで十分 (popup menu の click 連鎖が
    // 目視確認できる解像度)。CI artifact 容量との trade-off はあるが、
    // nightly 1 回 / 全 spec の webm 数百 MB は許容範囲。
    video: 'on',
    // nginx tls proxy が self-signed cert を提供する (#817 part2)。
    // Playwright はそれを accept できる必要がある。`request` fixture と
    // `page` fixture の両方に効く。
    ignoreHTTPSErrors: true,
    // i18n 依存 selector (button textContent === 'Save' 等) を持つ spec が
    // batch 5 で多数生まれた (PR #974)。CI runner / 開発者ローカル の OS
    // locale で日本語 UI に切り替わると一斉に fail するので、browser locale
    // を en-US に固定する。Misskey TS frontend は browser locale を
    // i18n key 解決に使うので、これで全 spec の text 前提が安定する。
    locale: 'en-US',
  },
  // CI 並列度の調整は CI 統合 PR で。本 PR はローカル直列実行。
  workers: 1,
  retries: 0,
});
