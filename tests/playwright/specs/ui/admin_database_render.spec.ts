// /admin/database page で admin/get-table-stats 経由 DB table 一覧が
// MkKeyValue として render されることを verify する spec。
//
// 全 backend で table "user" は必ず存在するので、"user" + "(" + "recs)"
// の組合せが body に出るのを hydration sign にする。

import { readFileSync } from 'node:fs';
import { expect, test } from '@playwright/test';
import { type RootFixture, uiSigninAsRoot } from '../../fixtures/ui_auth';

test.describe('UI: /admin/database page renders table stats', () => {
  let root: RootFixture;

  test.beforeAll(() => {
    root = JSON.parse(readFileSync('.auth/root.json', 'utf-8'));
  });

  test.setTimeout(60_000);

  test('admin/get-table-stats hydrates table list on /admin/database', async ({
    page,
    baseURL,
  }) => {
    await uiSigninAsRoot(page, baseURL, root);
    const resp = await page.goto(`${baseURL}/admin/database`, { waitUntil: 'domcontentloaded' });
    expect(resp!.status()).toBe(200);

    // 各 table は MkKeyValue で `{name}` key + `{size} ({count} recs)`
    // value として render される。"user" は必ず存在 + value 末尾が "recs)"
    // で hardcode されているので両方を verify。
    await page.waitForFunction(
      () => {
        const text = document.body.textContent ?? '';
        return text.includes('user') && text.includes('recs)');
      },
      { timeout: 20_000 },
    );
  });
});
