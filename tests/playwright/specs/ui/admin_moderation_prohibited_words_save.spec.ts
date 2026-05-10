// /admin/moderation の "Prohibited words" MkFolder を expand → MkTextarea
// を編集 → primary Save click → /api/admin/update-meta が round-trip
// する write-flow spec。
//
// admin_moderation_preserved_usernames_save の sister。同 page の
// 7 folder のうち別 section (prohibitedWords) で folder + textarea +
// save pattern が機能することを担保する。

import { readFileSync } from 'node:fs';
import { expect, test } from '@playwright/test';
import { type RootFixture, uiSigninAsRoot } from '../../fixtures/ui_auth';

test.describe('UI: /admin/moderation prohibitedWords save flow', () => {
  let root: RootFixture;
  test.beforeAll(() => {
    root = JSON.parse(readFileSync('.auth/root.json', 'utf-8'));
  });
  test.setTimeout(60_000);

  test('expand Prohibited words folder → edit textarea → Save → /api/admin/update-meta', async ({
    page,
    baseURL,
  }) => {
    await uiSigninAsRoot(page, baseURL, root);
    await page.goto(`${baseURL}/admin/moderation`, {
      waitUntil: 'domcontentloaded',
    });

    await page.waitForFunction(
      () => document.querySelectorAll('[data-cy-folder-header]').length >= 5,
      { timeout: 20_000 },
    );

    // "Prohibited words" folder を expand。"Prohibited words" は
    // "Prohibited words for username" と prefix が被るので、textContent が
    // "Prohibited words" で始まり "username" を含まないものを選ぶ。
    await page.evaluate(() => {
      const headers = Array.from(
        document.querySelectorAll('[data-cy-folder-header]'),
      ) as HTMLElement[];
      const target = headers.find((h) => {
        const t = (h.textContent ?? '').trim();
        return t.startsWith('Prohibited words') && !t.toLowerCase().includes('username');
      });
      target?.click();
    });

    await page.waitForFunction(
      () => document.querySelectorAll('textarea').length >= 1,
      { timeout: 10_000 },
    );

    const newValue = `bad-word-${Date.now()}\noffensive\nspam`;
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
