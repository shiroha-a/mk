// /admin/object-storage で "Save" button click → /api/admin/update-meta
// round-trip する **真の write-flow** spec。
//
// admin/object-storage.vue は S3-compat 設定 form。Save click で現在値を
// commit する (object-storage.vue:101 / line 137 で apiWithDialog)。

import { readFileSync } from 'node:fs';
import { expect, test } from '@playwright/test';
import { type RootFixture, uiSigninAsRoot } from '../../fixtures/ui_auth';

test.describe('UI: /admin/object-storage save flow', () => {
  let root: RootFixture;
  test.beforeAll(() => {
    root = JSON.parse(readFileSync('.auth/root.json', 'utf-8'));
  });
  test.setTimeout(60_000);

  test('click Save → /api/admin/update-meta round-trips', async ({ page, baseURL }) => {
    await uiSigninAsRoot(page, baseURL, root);
    await page.goto(`${baseURL}/admin/object-storage`, { waitUntil: 'domcontentloaded' });

    await page.waitForFunction(
      () => {
        const btns = Array.from(document.querySelectorAll('button'));
        return btns.some((b) => (b.textContent ?? '').includes('Save'));
      },
      { timeout: 20_000 },
    );

    const updateResp = page.waitForResponse(
      (r) => r.url().includes('/api/admin/update-meta') && r.status() < 400,
      { timeout: 15_000 },
    );
    await page.evaluate(() => {
      const btn = Array.from(document.querySelectorAll('button')).find((b) =>
        (b.textContent ?? '').includes('Save'),
      ) as HTMLButtonElement | undefined;
      btn?.click();
    });
    const resp = await updateResp;
    expect(resp.status()).toBeLessThan(400);
  });
});
