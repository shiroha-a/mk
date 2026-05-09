// UI 操作 spec で root 認証を行う共通 helper。
//
// signin form (data-cy-signin / data-cy-signin-username / data-cy-signin-password)
// を実 browser で driving して /api/signin-flow まで通す flow を集約する。
// 後続 spec はこの helper を import して認証済 state から始められる。
//
// 使い方:
//   import { uiSigninAsRoot, RootFixture } from '../../fixtures/ui_auth';
//   await uiSigninAsRoot(page, baseURL, root);
//
// pattern:
// - viewport 1600x900 で navbar collapse を回避
// - data-cy-* selector で fragility が低い (upstream Misskey の Cypress と
//   同じ test fixture を共有)
// - 完了条件: navbar の data-cy-open-post-form が visible (= authenticated
//   home の hydration 完了 = signin が backend → frontend まで通った)

import { expect, type Page } from '@playwright/test';

export interface RootFixture {
  id: string;
  token: string;
  username: string;
}

// signin form 経由で alice として認証する。globalSetup が `password1234` で
// root を作成している前提 (tests/playwright/global-setup.ts)。
export async function uiSigninAsRoot(page: Page, baseURL: string, root: RootFixture): Promise<void> {
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

  // hydration 完了の最終確認
  await expect(page.locator('[data-cy-open-post-form]').first()).toBeVisible({ timeout: 15_000 });
}
