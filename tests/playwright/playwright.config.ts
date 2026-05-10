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
    // 失敗時のみ動画を保存する。batch 5 以降は popup menu / contextmenu /
    // MkUserSelectDialog 等 multi-step interaction が増え、失敗時のスクショ
    // 1 枚では原因特定が辛くなった。retain-on-failure は CI artifact 容量
    // を抑えつつ、debug 価値の高い fail run のみ動画を残す。
    // pass 時は browser context close 時に video を破棄するので、storage
    // の累積 footprint は最小。size は 800x600 デフォルトで OK。
    video: 'retain-on-failure',
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
