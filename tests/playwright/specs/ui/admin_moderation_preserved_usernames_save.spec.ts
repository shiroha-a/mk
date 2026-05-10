// /admin/moderation の "Preserved usernames" MkFolder を expand →
// MkTextarea を編集 → Save click → /api/admin/update-meta が round-trip
// する write-flow spec。
//
// admin/moderation.vue:40-50 の同 folder は MkTextarea + Save MkButton primary
// の組。同 page には preservedUsernames / sensitiveWords / prohibitedWords
// 等 7 つの folder が並ぶが、それぞれ 1 textarea + 1 Save なので folder
// 単位で expand すれば識別可能。本 spec は preservedUsernames を代表として
// 取り、folder + textarea + save pattern を担保する。

import { readFileSync } from 'node:fs';
import { expect, test } from '@playwright/test';
import { type RootFixture, uiSigninAsRoot } from '../../fixtures/ui_auth';

test.describe('UI: /admin/moderation preservedUsernames save flow', () => {
  let root: RootFixture;
  test.beforeAll(() => {
    root = JSON.parse(readFileSync('.auth/root.json', 'utf-8'));
  });
  test.setTimeout(60_000);

  test('expand folder → edit textarea → Save → /api/admin/update-meta', async ({
    page,
    baseURL,
  }) => {
    await uiSigninAsRoot(page, baseURL, root);
    await page.goto(`${baseURL}/admin/moderation`, {
      waitUntil: 'domcontentloaded',
    });

    // folder header が hydrate するまで待つ
    await page.waitForFunction(
      () => document.querySelectorAll('[data-cy-folder-header]').length >= 5,
      { timeout: 20_000 },
    );

    // "Preserved usernames" folder を expand
    await page.evaluate(() => {
      const headers = Array.from(
        document.querySelectorAll('[data-cy-folder-header]'),
      ) as HTMLElement[];
      const target = headers.find((h) =>
        (h.textContent ?? '').includes('Preserved usernames'),
      );
      target?.click();
    });

    // textarea が DOM に出るまで待つ
    await page.waitForFunction(
      () => document.querySelectorAll('textarea').length >= 1,
      { timeout: 10_000 },
    );

    // textarea の値を変更 (= modified=true → Save が effective)
    const newValue = `pwadmin\nadmin\nroot\nsystem\n${Date.now()}`;
    await page.evaluate((v) => {
      const tas = Array.from(document.querySelectorAll('textarea')) as HTMLTextAreaElement[];
      const target = tas[0];
      if (!target) return;
      target.focus();
      const setter = Object.getOwnPropertyDescriptor(
        window.HTMLTextAreaElement.prototype,
        'value',
      )?.set;
      setter?.call(target, v);
      target.dispatchEvent(new Event('input', { bubbles: true }));
    }, newValue);

    // Save button click → admin/update-meta
    const updateResp = page.waitForResponse(
      (r) => r.url().includes('/api/admin/update-meta') && r.status() < 300,
      { timeout: 15_000 },
    );
    await page.evaluate(() => {
      const btns = Array.from(document.querySelectorAll('button')) as HTMLButtonElement[];
      const save = btns.find(
        (b) => !b.disabled && (b.textContent ?? '').includes('Save'),
      );
      save?.click();
    });
    await updateResp;
  });
});
