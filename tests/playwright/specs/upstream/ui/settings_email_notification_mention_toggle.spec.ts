/*
 * SPDX-FileCopyrightText: mk-go project
 * SPDX-License-Identifier: AGPL-3.0-only
 */

// /settings/email で 2 番目の MkSwitch (emailNotification_mention) を click
// → watch で /api/i/update が emailNotificationTypes 配列で round-trip
// する write-flow spec。
//
// email.vue:37 の MkSwitch v-model="emailNotification_mention" は watch
// (line 114) 経由で saveNotificationSettings → i/update を呼ぶ。
// page 内 checkbox 順は receiveAnnouncementEmail / mention / reply /
// quote / follow / receiveFollowRequest の 6 個。本 spec は 2 番目
// (index 1) を click することで「最初の switch だけ動作する偽陽性」を排除。

import { readFileSync } from 'node:fs';
import { expect, test } from '@playwright/test';
import { type RootFixture, uiSigninAsRoot } from '../../../fixtures/ui_auth';
import { clickWhenReady } from '../../../fixtures/ui_click';

test.describe('UI: /settings/email emailNotification_mention toggle flow', () => {
  let root: RootFixture;
  test.beforeAll(() => {
    root = JSON.parse(readFileSync('.auth/root.json', 'utf-8'));
  });
  test.setTimeout(60_000);

  test('toggle emailNotification_mention switch (2nd) → /api/i/update', async ({
    page,
    baseURL,
  }) => {
    await uiSigninAsRoot(page, baseURL, root);
    await page.goto(`${baseURL}/settings/email`, {
      waitUntil: 'domcontentloaded',
    });

    // 6 個以上の checkbox が hydrate するまで待つ
    await page.waitForFunction(
      () => document.querySelectorAll('input[type="checkbox"]').length >= 6,
      { timeout: 20_000 },
    );

    const updateResp = page.waitForResponse(
      (r) => r.url().includes('/api/i/update') && r.status() < 300,
      { timeout: 15_000 },
    );
    await clickWhenReady(page, '2 番目の checkbox', () => {
      const cbs = Array.from(
        document.querySelectorAll('input[type="checkbox"]'),
      ) as HTMLInputElement[];
      // index 1 = 2 番目 = emailNotification_mention
      return cbs[1];
    });
    const update = await updateResp;
    const body = await update.json();
    // #985 drift fix で PackMeDetailed に emailNotificationTypes が追加され、
    // i/update response にも乗るようになった (default ["follow",
    // "receiveFollowRequest"] または profile JSON column の値)。
    expect(body.id).toBeTruthy();
    expect(Array.isArray(body.emailNotificationTypes)).toBe(true);
  });
});
