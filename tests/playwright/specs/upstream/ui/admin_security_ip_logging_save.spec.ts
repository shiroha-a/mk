/*
 * SPDX-FileCopyrightText: mk-go project
 * SPDX-License-Identifier: AGPL-3.0-only
 */

// /admin/security の "Log IP address" folder を expand → enableIpLogging
// switch を toggle → form footer の Save button click → /api/admin/update-meta
// が round-trip する write-flow spec。
//
// admin/security.vue:140-157 の ipLoggingForm は MkFolder 内で 1 個の
// MkSwitch を持つ。switch 変更で form.modified=true → footer の
// MkFormFooter (= Save button) が表示される。Save click で
// admin/update-meta が走る (line 211)。
//
// 同 page には他にも sensitiveMediaDetection / emailValidation /
// bannedEmailDomains 等の form folder が並ぶが、それらは collapsed なので
// IP logging を expand すれば本 spec が触る checkbox は唯一の状態になる。

import { readFileSync } from 'node:fs';
import { expect, test } from '@playwright/test';
import { callApi } from '../../../fixtures/api';
import { type RootFixture, uiSigninAsRoot } from '../../../fixtures/ui_auth';

test.describe('UI: /admin/security IP logging form save flow', () => {
  let root: RootFixture;
  test.beforeAll(() => {
    root = JSON.parse(readFileSync('.auth/root.json', 'utf-8'));
  });
  test.setTimeout(60_000);

  test('expand Log IP address folder → toggle switch → Save → /api/admin/update-meta', async ({
    page,
    baseURL,
    request,
  }) => {
    // setup: 既知 state (false) に reset。
    await callApi(request, 'admin/update-meta', {
      i: root.token,
      enableIpLogging: false,
    });

    await uiSigninAsRoot(page, baseURL, root);
    await page.goto(`${baseURL}/admin/security`, {
      waitUntil: 'domcontentloaded',
    });

    // page hydrate を待つ — admin/security の folder header (data-cy-folder-header)
    // が複数 mount するまで。
    await page.waitForFunction(
      () => document.querySelectorAll('[data-testid="folder-header"]').length >= 3,
      { timeout: 20_000 },
    );

    // "Log IP address" を含む folder header を click して expand
    await page.evaluate(() => {
      const headers = Array.from(
        document.querySelectorAll('[data-testid="folder-header"]'),
      ) as HTMLElement[];
      const target = headers.find((h) =>
        (h.textContent ?? '').includes('Log IP address'),
      );
      target?.click();
    });

    // expand 後に新しい checkbox (= enableIpLogging) が DOM に出るのを待つ
    await page.waitForFunction(
      () => document.querySelectorAll('input[type="checkbox"]').length >= 1,
      { timeout: 10_000 },
    );

    // checkbox を click → form.modified=true → footer の Save button 出現
    await page.evaluate(() => {
      const cbs = Array.from(
        document.querySelectorAll('input[type="checkbox"]'),
      ) as HTMLInputElement[];
      cbs[0]?.click();
    });

    // footer の Save button hydrate を待つ
    await page.waitForFunction(
      () => {
        const btns = Array.from(document.querySelectorAll('button'));
        return btns.some((b) => (b.textContent ?? '').includes('Save'));
      },
      { timeout: 10_000 },
    );

    // Save click → admin/update-meta round-trip
    const updateResp = page.waitForResponse(
      (r) => r.url().includes('/api/admin/update-meta') && r.status() < 300,
      { timeout: 15_000 },
    );
    await page.evaluate(() => {
      const btn = Array.from(document.querySelectorAll('button')).find((b) =>
        (b.textContent ?? '').includes('Save'),
      ) as HTMLButtonElement | undefined;
      btn?.click();
    });
    await updateResp;

    // cleanup: 念のため false に戻す。残るとログ容量を肥大させるが、本 spec
    // で on にした影響を最小化する目的。
    await callApi(request, 'admin/update-meta', {
      i: root.token,
      enableIpLogging: false,
    });
  });
});
