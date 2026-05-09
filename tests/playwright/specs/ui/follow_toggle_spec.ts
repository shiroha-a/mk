// /@<other-user> page で MkFollowButton を click → /api/users/show で
// isFollowing=true round-trip を verify する **真の write-flow** spec。
//
// MkFollowButton に data-cy は無いが、未 follow 時は i18n.ts.follow
// → "Follow" text を持つ <button> として render される。Playwright の
// getByRole('button', { name: 'Follow' }) で locate して click する。

import { readFileSync } from 'node:fs';
import { expect, test } from '@playwright/test';
import { callApi } from '../../fixtures/api';
import { DEFAULT_TEST_PASSWORD, signupUser } from '../../fixtures/auth';
import { resetRateLimit } from '../../fixtures/rate_limit';
import { type RootFixture, uiSigninAsRoot } from '../../fixtures/ui_auth';

test.describe('UI: follow button click toggles following relation', () => {
  let root: RootFixture;

  test.beforeAll(() => {
    resetRateLimit();
    root = JSON.parse(readFileSync('.auth/root.json', 'utf-8'));
  });

  test.setTimeout(60_000);

  test('navigate /@other → click Follow → users/show.isFollowing=true', async ({
    page,
    baseURL,
    request,
  }) => {
    // root が follow する対象 user を作成
    const otherName = `flwbtn${Date.now().toString().slice(-9)}`;
    const other = await signupUser(request, otherName, DEFAULT_TEST_PASSWORD);

    await uiSigninAsRoot(page, baseURL, root);
    const resp = await page.goto(`${baseURL}/@${otherName}`, { waitUntil: 'domcontentloaded' });
    expect(resp!.status()).toBe(200);

    // profile page hydration 完了 = username が body に出る
    await page.waitForFunction(
      (n) => document.body.textContent?.includes(n) ?? false,
      otherName,
      { timeout: 20_000 },
    );

    // following/create response を捕捉して Follow button click。
    // MkFollowButton は <span :class="$style.text">Follow</span> を含むので
    // exact name="Follow" button を locator で取る。
    const followResp = page.waitForResponse(
      (r) => r.url().includes('/api/following/create') && r.status() === 200,
      { timeout: 15_000 },
    );
    await page.getByRole('button', { name: 'Follow', exact: true }).first().click();
    await followResp;

    // backend で users/show を引いて isFollowing=true を verify
    const showResp = await callApi(request, 'users/show', {
      i: root.token,
      userId: other.id,
    });
    expect(showResp.status()).toBe(200);
    const shown = await showResp.json();
    expect(shown.isFollowing).toBe(true);
  });
});
