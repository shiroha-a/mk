// /admin/moderation の "Blocked hosts" MkFolder を expand → MkTextarea
// を編集 → Save → /api/admin/update-meta が round-trip する write-flow
// spec。preserved_usernames / prohibited_words の sister。
//
// blocked hosts は federation 系で domain ブロックリスト。upstream key は
// `blockedHosts` で、admin/update-meta の同 field 経由で persist される。

import { readFileSync } from 'node:fs';
import { expect, test } from '@playwright/test';
import { callApi } from '../../fixtures/api';
import { type RootFixture, uiSigninAsRoot } from '../../fixtures/ui_auth';

test.describe('UI: /admin/moderation blockedHosts save flow', () => {
  let root: RootFixture;
  test.beforeAll(() => {
    root = JSON.parse(readFileSync('.auth/root.json', 'utf-8'));
  });
  test.setTimeout(60_000);

  test('expand Blocked hosts folder → edit textarea → Save → /api/admin/update-meta', async ({
    page,
    baseURL,
    request,
  }) => {
    // setup: 既知 state (空 list) に reset。
    await callApi(request, 'admin/update-meta', {
      i: root.token,
      blockedHosts: [],
    });

    try {
      await uiSigninAsRoot(page, baseURL, root);
      await page.goto(`${baseURL}/admin/moderation`, {
        waitUntil: 'domcontentloaded',
      });

      await page.waitForFunction(
        () => document.querySelectorAll('[data-cy-folder-header]').length >= 5,
        { timeout: 20_000 },
      );

      // "Blocked hosts" folder を expand
      await page.evaluate(() => {
        const headers = Array.from(
          document.querySelectorAll('[data-cy-folder-header]'),
        ) as HTMLElement[];
        const target = headers.find((h) =>
          (h.textContent ?? '').includes('Blocked hosts'),
        );
        target?.click();
      });

      await page.waitForFunction(
        () => document.querySelectorAll('textarea').length >= 1,
        { timeout: 10_000 },
      );

      const newValue = `pwblock-${Date.now().toString().slice(-9)}.invalid\nfoo.invalid\nbar.invalid`;
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
    } finally {
      // cleanup: blockedHosts に test domain (.invalid) が残っても他 spec
      // への直接影響は無いが、test isolation のため空に戻す。
      await callApi(request, 'admin/update-meta', {
        i: root.token,
        blockedHosts: [],
      });
    }
  });
});
