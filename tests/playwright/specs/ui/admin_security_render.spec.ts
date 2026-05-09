// /admin/security page で MkRadios + MkRange + MkSwitch 等の form
// controls が hydrate されることを smoke する spec。
//
// /admin/security は sensitive media detection / bot protection 等の
// admin/meta 値を read/write する form の集合。本 spec は controls が
// 必要数 mount されることだけを sign にする。

import { readFileSync } from 'node:fs';
import { expect, test } from '@playwright/test';
import { type RootFixture, uiSigninAsRoot } from '../../fixtures/ui_auth';

test.describe('UI: /admin/security page hydrates form controls', () => {
  let root: RootFixture;

  test.beforeAll(() => {
    root = JSON.parse(readFileSync('.auth/root.json', 'utf-8'));
  });

  test.setTimeout(60_000);

  test('security form (radios / switches / inputs) hydrates', async ({ page, baseURL }) => {
    await uiSigninAsRoot(page, baseURL, root);
    const resp = await page.goto(`${baseURL}/admin/security`, { waitUntil: 'domcontentloaded' });
    expect(resp!.status()).toBe(200);

    // sensitiveMediaDetection radios + sensitivity range + bot protection
    // 等で input/checkbox が複数 mount される。最低 3 個でほぼ全 backend で
    // mount される。
    await page.waitForFunction(
      () => document.querySelectorAll('input').length >= 3,
      { timeout: 20_000 },
    );
  });
});
