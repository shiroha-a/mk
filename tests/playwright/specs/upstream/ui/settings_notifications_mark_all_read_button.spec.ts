/*
 * SPDX-FileCopyrightText: mk-go project
 * SPDX-License-Identifier: AGPL-3.0-only
 */

// /settings/notifications で "Mark all notifications as read" button click
// → /api/notifications/mark-all-as-read round-trip する **真の write-flow**
// spec。
//
// /my/notifications header の同名 action とは別経路 (settings 内 button)
// で、source は notifications.vue (settings) line 46 の MkButton。

import { readFileSync } from 'node:fs';
import { expect, test } from '@playwright/test';
import { type RootFixture, uiSigninAsRoot } from '../../../fixtures/ui_auth';

test.describe('UI: /settings/notifications mark all read button flow', () => {
  let root: RootFixture;
  test.beforeAll(() => {
    root = JSON.parse(readFileSync('.auth/root.json', 'utf-8'));
  });
  test.setTimeout(60_000);

  test('click "Mark as read" button → /api/notifications/mark-all-as-read', async ({
    page,
    baseURL,
  }) => {
    await uiSigninAsRoot(page, baseURL, root);
    await page.goto(`${baseURL}/settings/notifications`, {
      waitUntil: 'domcontentloaded',
    });

    // "Mark all notifications as read" button が hydrate するまで待つ。
    // i18n.ts.markAsReadAllNotifications。
    await page.waitForFunction(
      () => {
        const btns = Array.from(document.querySelectorAll('button'));
        return btns.some((b) =>
          (b.textContent ?? '').includes('Mark all notifications as read'),
        );
      },
      { timeout: 20_000 },
    );

    const markResp = page.waitForResponse(
      (r) =>
        r.url().includes('/api/notifications/mark-all-as-read') && r.status() < 300,
      { timeout: 15_000 },
    );
    await page.evaluate(() => {
      const btn = Array.from(document.querySelectorAll('button')).find((b) =>
        (b.textContent ?? '').includes('Mark all notifications as read'),
      ) as HTMLButtonElement | undefined;
      btn?.click();
    });
    const resp = await markResp;
    expect(resp.status()).toBeLessThan(300);
  });
});
