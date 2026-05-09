// /admin/branding page で MkRadios + MkSwitch + MkInput が hydrate される
// ことを verify する spec。
//
// /admin/branding は server settings (entrancePageStyle / iconUrl 等) を
// admin/meta + admin/update-meta 経由で読み書きする。本 spec は controls
// が mount されることだけを smoke する (= hidden form なら必ず複数 input
// が render される)。

import { readFileSync } from 'node:fs';
import { expect, test } from '@playwright/test';
import { type RootFixture, uiSigninAsRoot } from '../../fixtures/ui_auth';

test.describe('UI: /admin/branding page hydrates form controls', () => {
  let root: RootFixture;

  test.beforeAll(() => {
    root = JSON.parse(readFileSync('.auth/root.json', 'utf-8'));
  });

  test.setTimeout(60_000);

  test('branding form controls (radios / switches / inputs) appear', async ({ page, baseURL }) => {
    await uiSigninAsRoot(page, baseURL, root);
    const resp = await page.goto(`${baseURL}/admin/branding`, { waitUntil: 'domcontentloaded' });
    expect(resp!.status()).toBe(200);

    // entrancePageStyle (radios) + iconUrl / bannerUrl 等 (input type=url)
    // + visitor switches など、複数の form 要素が必ず render される。
    // 5+ input/radio/switch が visible になれば form 全体が hydrate された
    // と判定 (= admin/meta が前段で fetch されてから初めて MkRadios が
    // 値を bind する pipeline)。
    await page.waitForFunction(
      () => {
        const inputs = document.querySelectorAll('input').length;
        return inputs >= 5;
      },
      { timeout: 20_000 },
    );
  });
});
