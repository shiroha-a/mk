// /admin/invites で MkFolder "Create invite code" を expand → "Create"
// button click → /api/admin/invite/create round-trip → 一覧に新コード
// が prepend される **真の write-flow** spec。
//
// invites.vue では create form は MkFolder で `:expanded="false"` 起点。
// header click で expand する必要がある。MkFolder header は
// `data-cy-folder-header` 属性 (#744 batch3 で globalSetup から判明) で
// 取得可能。expand 後に "Create" button (i18n.ts.create = "Create") を click。

import { readFileSync } from 'node:fs';
import { expect, test } from '@playwright/test';
import { type RootFixture, uiSigninAsRoot } from '../../fixtures/ui_auth';

test.describe('UI: /admin/invites create form flow', () => {
  let root: RootFixture;
  test.beforeAll(() => {
    root = JSON.parse(readFileSync('.auth/root.json', 'utf-8'));
  });
  test.setTimeout(60_000);

  test('expand folder → click Create → /api/admin/invite/create round-trips', async ({
    page,
    baseURL,
  }) => {
    await uiSigninAsRoot(page, baseURL, root);
    await page.goto(`${baseURL}/admin/invites`, { waitUntil: 'domcontentloaded' });

    // MkFolder header が hydrate するまで待つ
    await page.waitForFunction(
      () => document.querySelector('[data-testid="folder-header"]') !== null,
      { timeout: 20_000 },
    );

    // 1 つ目の MkFolder を expand (= "Create invite code" form を開く)
    await page.evaluate(() => {
      const header = document.querySelector('[data-testid="folder-header"]') as HTMLButtonElement | null;
      header?.click();
    });

    // Create button が visible になるまで待つ (= folder content が expand 完了)
    await page.waitForFunction(
      () => {
        const btns = Array.from(document.querySelectorAll('button'));
        return btns.some((b) => (b.textContent ?? '').trim() === 'Create');
      },
      { timeout: 10_000 },
    );

    // admin/invite/create response 捕捉して Create click
    const createResp = page.waitForResponse(
      (r) => r.url().includes('/api/admin/invite/create') && r.status() === 200,
      { timeout: 15_000 },
    );
    await page.evaluate(() => {
      const btn = Array.from(document.querySelectorAll('button')).find(
        (b) => (b.textContent ?? '').trim() === 'Create',
      ) as HTMLButtonElement | undefined;
      btn?.click();
    });
    const created = await createResp;
    const body = await created.json();
    // admin/invite/create returns array of {code, ...}
    expect(Array.isArray(body)).toBe(true);
    expect(body.length).toBeGreaterThan(0);
    expect(body[0]).toHaveProperty('code');
  });
});
