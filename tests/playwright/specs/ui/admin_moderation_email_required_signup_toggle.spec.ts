// /admin/moderation で 2 番目の MkSwitch (emailRequiredForSignup) を click
// → /api/admin/update-meta が round-trip する write-flow spec。
//
// admin/moderation.vue:22 の MkSwitch v-model="emailRequiredForSignup" は
// @change で onChange_emailRequiredForSignup → admin/update-meta を直接
// 呼ぶ (line 216)。confirm dialog なし、最も simple な toggle pattern。
//
// 1 番目 switch (enableRegistration) は false → true 切替時に warning
// confirm が入るので flow が長くなる。本 spec は dialog 経由しない 2 番目
// を選ぶことで最短 round-trip を verify する。

import { readFileSync } from 'node:fs';
import { expect, test } from '@playwright/test';
import { callApi } from '../../fixtures/api';
import { type RootFixture, uiSigninAsRoot } from '../../fixtures/ui_auth';

test.describe('UI: /admin/moderation emailRequiredForSignup toggle flow', () => {
  let root: RootFixture;
  test.beforeAll(() => {
    root = JSON.parse(readFileSync('.auth/root.json', 'utf-8'));
  });
  test.setTimeout(60_000);

  test('toggle emailRequiredForSignup switch (2nd) → /api/admin/update-meta', async ({
    page,
    baseURL,
    request,
  }) => {
    // setup: 既知 state (false) に reset。click で false→true に切替わる
    // ことを strict assert できるようにする。
    await callApi(request, 'admin/update-meta', {
      i: root.token,
      emailRequiredForSignup: false,
    });

    try {
      await uiSigninAsRoot(page, baseURL, root);
      await page.goto(`${baseURL}/admin/moderation`, {
        waitUntil: 'domcontentloaded',
      });

      // 2 個以上の checkbox が hydrate するまで待つ
      await page.waitForFunction(
        () => document.querySelectorAll('input[type="checkbox"]').length >= 2,
        { timeout: 20_000 },
      );

      const updateResp = page.waitForResponse(
        (r) => r.url().includes('/api/admin/update-meta') && r.status() < 300,
        { timeout: 15_000 },
      );
      await page.evaluate(() => {
        const cbs = Array.from(
          document.querySelectorAll('input[type="checkbox"]'),
        ) as HTMLInputElement[];
        // index 1 = 2 番目 = emailRequiredForSignup
        cbs[1]?.click();
      });
      await updateResp;
    } finally {
      // cleanup: 必ず false に戻す。on のまま残すと以降の signup spec が
      // 全て INVALID_PARAM (emailAddress required) で fail する重大な
      // isolation 破壊を引き起こす。pass / fail どちらでも cleanup を実行。
      await callApi(request, 'admin/update-meta', {
        i: root.token,
        emailRequiredForSignup: false,
      });
    }
  });
});
