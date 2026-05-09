// /my/antennas/create で MkAntennaEditor の name input → Save click →
// /api/antennas/create round-trip → /my/antennas 一覧で新 antenna が
// render される **真の write-flow** spec。

import { readFileSync } from 'node:fs';
import { expect, test } from '@playwright/test';
import { type RootFixture, uiSigninAsRoot } from '../../fixtures/ui_auth';

test.describe('UI: /my/antennas/create form flow', () => {
  let root: RootFixture;
  test.beforeAll(() => {
    root = JSON.parse(readFileSync('.auth/root.json', 'utf-8'));
  });
  test.setTimeout(60_000);

  test('navigate /my/antennas/create → fill name → save → list shows new antenna', async ({
    page,
    baseURL,
  }) => {
    await uiSigninAsRoot(page, baseURL, root);
    await page.goto(`${baseURL}/my/antennas/create`, { waitUntil: 'domcontentloaded' });

    // name MkInput が hydrate するまで待つ
    await page.waitForFunction(
      () => document.querySelectorAll('input').length > 0,
      { timeout: 20_000 },
    );

    // name input (= 最初の input) に書き込み (post_note と同 pattern)
    const antennaName = `pwantui-${Date.now().toString().slice(-9)}`;
    await page.evaluate((n) => {
      const target = document.querySelector('input') as HTMLInputElement | null;
      if (!target) return;
      target.focus();
      const setter = Object.getOwnPropertyDescriptor(
        window.HTMLInputElement.prototype,
        'value',
      )?.set;
      setter?.call(target, n);
      target.dispatchEvent(new Event('input', { bubbles: true }));
    }, antennaName);

    // antennas/create response を捕捉して Save click
    const createResp = page.waitForResponse(
      (r) => r.url().includes('/api/antennas/create') && r.status() === 200,
      { timeout: 15_000 },
    );
    await page.evaluate(() => {
      const btn = Array.from(document.querySelectorAll('button')).find((b) =>
        (b.textContent ?? '').includes('Save'),
      ) as HTMLButtonElement | undefined;
      btn?.click();
    });
    await createResp;

    // SPA は /my/antennas に router.push する。新 antenna name が一覧に
    // 出るのを polling 待ち。
    await page.waitForFunction(
      (n) => document.body.textContent?.includes(n) ?? false,
      antennaName,
      { timeout: 20_000 },
    );
  });
});
