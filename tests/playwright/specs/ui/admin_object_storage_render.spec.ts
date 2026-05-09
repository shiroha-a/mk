// /admin/object-storage page で S3 互換 storage settings form が hydrate
// されることを smoke する spec。

import { readFileSync } from 'node:fs';
import { expect, test } from '@playwright/test';
import { type RootFixture, uiSigninAsRoot } from '../../fixtures/ui_auth';

test.describe('UI: /admin/object-storage page hydrates storage form', () => {
  let root: RootFixture;

  test.beforeAll(() => {
    root = JSON.parse(readFileSync('.auth/root.json', 'utf-8'));
  });

  test.setTimeout(60_000);

  test('object storage form hydrates with bucket / endpoint inputs', async ({ page, baseURL }) => {
    await uiSigninAsRoot(page, baseURL, root);
    const resp = await page.goto(`${baseURL}/admin/object-storage`, {
      waitUntil: 'domcontentloaded',
    });
    expect(resp!.status()).toBe(200);

    // page title (i18n.ts.objectStorage → "Object Storage") + S3 設定 input
    // (baseUrl / endpoint / region / bucket / prefix / accessKey / secret 等)
    await page.waitForFunction(
      () => {
        const text = document.body.textContent ?? '';
        const inputs = document.querySelectorAll('input').length;
        return text.includes('Object Storage') && inputs >= 3;
      },
      { timeout: 20_000 },
    );
  });
});
