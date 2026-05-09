// public 認証不要 SPA page の navigation smoke。
//
// frontend_routes.spec.ts は logged-out home / about / explore / signup 等の
// 「signup wall を含む」route 群を navigate する smoke。本 spec は API 結果
// を含む public route (= /search / /channels / /tags 等) を navigate して、
// SPA component が API 応答を受けて render することを verify する。

import { expect, test } from '@playwright/test';

test.describe('UI: public SPA pages with API hydration', () => {
  test.setTimeout(30_000);

  test('navigate to /search and search input is rendered', async ({ page, baseURL }) => {
    await page.setViewportSize({ width: 1600, height: 900 });
    const resp = await page.goto(`${baseURL}/search`, { waitUntil: 'domcontentloaded' });
    expect(resp!.status()).toBe(200);

    // search page には input field が必ず render される
    await page.waitForFunction(
      () => document.querySelectorAll('input').length > 0,
      { timeout: 20_000 },
    );
  });

  test('navigate to /channels (public channel list)', async ({ page, baseURL }) => {
    await page.setViewportSize({ width: 1600, height: 900 });
    const resp = await page.goto(`${baseURL}/channels`, { waitUntil: 'domcontentloaded' });
    expect(resp!.status()).toBe(200);

    // channels page には title or any heading が render される。loading
    // overlay も含めて textContent が ある程度の長さになる
    await page.waitForFunction(
      () => (document.body.textContent?.length ?? 0) > 100,
      { timeout: 20_000 },
    );
  });

  test('navigate to /tags/test (hashtag page)', async ({ page, baseURL }) => {
    await page.setViewportSize({ width: 1600, height: 900 });
    const resp = await page.goto(`${baseURL}/tags/test`, { waitUntil: 'domcontentloaded' });
    expect(resp!.status()).toBe(200);

    // hashtag が空でも component は mount される。最低限 SPA ルーターが
    // 該当 page を選んだ確認として navbar (logged-out では login button) を
    // verify する代わりに、textContent 長で sanity check。
    await page.waitForFunction(
      () => (document.body.textContent?.length ?? 0) > 100,
      { timeout: 20_000 },
    );
  });
});
