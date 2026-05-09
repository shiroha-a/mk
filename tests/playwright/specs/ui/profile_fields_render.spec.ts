// /@:user の profile page で i/update-applied description が API hydration
// 経由で render されることを verify する spec。
//
// API spec (specs/users/profile.spec.ts) は users/show のレスポンス shape
// を verify する API-only。本 spec は MkUserHome + MkMfm chain で
// description が body に出ることまで covers する。
//
// 注: profile fields も同 path で render したいが mk-go の i/update が
// fields パラメータを drop している drift がある (#956)。本 spec は #956
// fix まで description のみ verify する。

import { expect, test } from '@playwright/test';
import { callApi } from '../../fixtures/api';
import { signupUser } from '../../fixtures/auth';
import { resetRateLimit } from '../../fixtures/rate_limit';

test.describe('UI: /@:user renders profile description', () => {
  test.beforeAll(() => {
    resetRateLimit();
  });

  test.setTimeout(60_000);

  test('/@<user> shows i/update-applied description', async ({ page, baseURL, request }) => {
    const userName = `profrnd${Date.now().toString().slice(-9)}`;
    const user = await signupUser(request, userName, 'password1234');

    const description = `playwright-profile-desc ${Date.now()}`;

    const updateResp = await callApi(request, 'i/update', {
      i: user.token,
      description,
    });
    expect(updateResp.status()).toBe(200);

    // anonymous でも /@<user> は users/show 経由で description を取得できる
    // (= profile page の public hydration path を smoke)。
    await page.setViewportSize({ width: 1600, height: 900 });
    const resp = await page.goto(`${baseURL}/@${userName}`, { waitUntil: 'domcontentloaded' });
    expect(resp!.status()).toBe(200);

    await page.waitForFunction(
      (d) => document.body.textContent?.includes(d) ?? false,
      description,
      { timeout: 20_000 },
    );
  });
});
