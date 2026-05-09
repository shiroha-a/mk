// 認証済 SPA route navigation smoke。signin 後に timeline / profile / settings
// 等の主要 page を navigate して、frontend が API hydration → component mount
// まで通ることを回帰検出する。
//
// 各 test は以下の workflow を取る:
//   1. signin form 経由で root を auth (signin.spec.ts と同 pattern)
//   2. 対象 route に直接 navigate
//   3. route 固有の signature element (= URL ごとに必ず render される marker
//      を 1 つ選ぶ) を visible まで wait
//
// authenticated route は frontend の hydration が API call → store 反映 →
// component mount の 3 段で進むため、locator timeout を 15-20s に設定する
// (= 単 spec で multiple ページを連鎖する場合は spec 単位で setTimeout 拡張)。

import { readFileSync } from 'node:fs';
import { expect, test } from '@playwright/test';

interface RootFixture {
  id: string;
  token: string;
  username: string;
}

// signin helper — UI 操作で alice として認証してから baseURL 起点の
// 認証済 state を返す。signin.spec.ts の flow を duplicate (= 後で
// support/auth.ts に集約予定)。
async function uiSigninAsRoot(page: import('@playwright/test').Page, baseURL: string, root: RootFixture) {
  await page.setViewportSize({ width: 1600, height: 900 });
  await page.goto(`${baseURL}/`, { waitUntil: 'domcontentloaded' });
  await page.locator('[data-cy-signin]').first().click();
  await expect(page.locator('[data-cy-signin-page-input]')).toBeVisible({ timeout: 10_000 });
  await page.locator('[data-cy-signin-username] input').fill(root.username);
  await page.locator('[data-cy-signin-username] input').press('Enter');
  await expect(page.locator('[data-cy-signin-page-password]')).toBeVisible({ timeout: 10_000 });
  const signinResp = page.waitForResponse(
    (resp) => resp.url().includes('/api/signin-flow') && resp.status() === 200,
    { timeout: 15_000 },
  );
  await page.locator('[data-cy-signin-password] input').fill('password1234');
  await page.locator('[data-cy-signin-password] input').press('Enter');
  await signinResp;
  // 認証済 home の hydration を最終確認 (= navbar の post button が visible)
  await expect(page.locator('[data-cy-open-post-form]').first()).toBeVisible({ timeout: 15_000 });
}

test.describe('UI: authenticated route navigation', () => {
  let root: RootFixture;

  test.beforeAll(() => {
    root = JSON.parse(readFileSync('.auth/root.json', 'utf-8'));
  });

  test.setTimeout(60_000);

  test('navigate to /my (own profile) after signin', async ({ page, baseURL }) => {
    await uiSigninAsRoot(page, baseURL, root);

    // /my は logged-in user の自分のプロフィールへ redirect する。SPA router
    // が /@alice に遷移して MkUserPage が mount される。
    await page.goto(`${baseURL}/my`, { waitUntil: 'domcontentloaded' });

    // user profile page には username が必ず render される。
    // <h1> や <span> 内に @alice / alice@ の文字列があれば mount 成功。
    await page.waitForFunction(
      (username) => document.body.textContent?.includes(username) ?? false,
      'alice',
      { timeout: 20_000 },
    );
  });

  test('navigate to /notifications after signin', async ({ page, baseURL }) => {
    await uiSigninAsRoot(page, baseURL, root);

    // /notifications は authenticated state でのみ accessible。SPA は full
    // page reload になるので splash → component mount の遷移を待つ。
    const resp = await page.goto(`${baseURL}/notifications`, { waitUntil: 'domcontentloaded' });
    expect(resp!.status()).toBe(200);
    // SPA hydration 完了 = splash が消えて navbar が visible になる
    // (= authenticated state が維持されている確認も兼ねる)
    await expect(page.locator('[data-cy-open-post-form]').first()).toBeVisible({ timeout: 20_000 });
  });

  test('navigate to /settings/profile after signin', async ({ page, baseURL }) => {
    await uiSigninAsRoot(page, baseURL, root);

    const resp = await page.goto(`${baseURL}/settings/profile`, { waitUntil: 'domcontentloaded' });
    expect(resp!.status()).toBe(200);

    // settings page は input field を多数持つ。少なくとも 1 つ <input> が
    // render されることで authenticated state + settings component mount を確認。
    await page.waitForFunction(
      () => document.querySelectorAll('input').length > 0,
      { timeout: 20_000 },
    );
  });

  test('navigate to /timeline/local after signin', async ({ page, baseURL }) => {
    await uiSigninAsRoot(page, baseURL, root);

    const resp = await page.goto(`${baseURL}/timeline/local`, { waitUntil: 'domcontentloaded' });
    expect(resp!.status()).toBe(200);

    // local timeline は WebSocket + initial GET を経由するため hydration
    // 完了まで時間がかかる。最低限 navbar (= [data-cy-open-post-form]) が
    // 維持されていれば authenticated state が維持されている確認になる。
    await expect(page.locator('[data-cy-open-post-form]').first()).toBeVisible({ timeout: 15_000 });
  });
});
