import { defineConfig } from '@playwright/test';

// #744 Phase 1 PR-1: 基盤と smoke spec のみ。後続 PR で multi-browser /
// projects / use.storageState を拡張する。
export default defineConfig({
  testDir: './specs',
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
    baseURL: process.env.MK_BASE_URL ?? 'http://mkgo:3000',
    // API テスト中心なので screenshot / trace は失敗時のみで十分。
    screenshot: 'only-on-failure',
    trace: 'retain-on-failure',
  },
  // CI 並列度の調整は CI 統合 PR で。本 PR はローカル直列実行。
  workers: 1,
  retries: 0,
});
