/*
 * SPDX-FileCopyrightText: mk-go project
 * SPDX-License-Identifier: AGPL-3.0-only
 */

// /@<other-user> page で MkFollowButton を click → /api/users/show で
// isFollowing=true round-trip を verify する **真の write-flow** spec。
//
// MkFollowButton に data-cy は無いが、未 follow 時は i18n.ts.follow
// → "Follow" text を持つ <button> として render される。Playwright の
// getByRole('button', { name: 'Follow' }) で locate して click する。

import { readFileSync } from 'node:fs';
import { expect, test } from '@playwright/test';
import { callApi } from '../../../fixtures/api';
import { DEFAULT_TEST_PASSWORD, signupUser } from '../../../fixtures/auth';
import { resetRateLimit } from '../../../fixtures/rate_limit';
import { type RootFixture, uiSigninAsRoot } from '../../../fixtures/ui_auth';

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

    // **username が body に出ることを hydration の代理にしない。** username は
    // skeleton や header の時点で既に DOM にあるので、MkFollowButton の描画を
    // 待つ条件になっていなかった。実測で click が navigation の 360ms 後に
    // 走り、ボタン未描画のまま次へ進んでいた (#2600)。
    //
    // 待つ対象を「押したいボタンそのもの」にする。**待機の述語を click と
    // 同じにするのが要点。** `getByRole(...).waitFor({state:'visible'})` は
    // 使えない: MkFollowButton は DOM にはあるが Playwright の visible 判定に
    // 入らず 20 秒待っても解決しない (実測)。下の click が programmatic なのも
    // 同じ理由と思われる。判定がずれていると「待てたのに押せない」が起きる。
    await page.waitForFunction(
      () =>
        Array.from(document.querySelectorAll('button')).some(
          (b) => (b.textContent ?? '').trim() === 'Follow',
        ),
      undefined,
      { timeout: 20_000 },
    );

    // MkFollowButton click → prefer.alwaysConfirmFollow=true (def.ts:370)
    // で os.confirm() dialog が出る → OK click が follow API trigger。
    // programmatic dispatchEvent で直接 button の click handler を呼び、
    // dialog 出現を待ってから data-cy-modal-dialog-ok を click する
    // (#744 batch3 で発覚)。
    // **見つからなければその場で落とす。** `btn?.click()` は要素が無くても
    // 黙って何もしないので、失敗が「ダイアログが出ない」という原因から遠い
    // 症状に化ける。実際に 60 秒 timeout の正体がこれだった (#2600)。
    const followClicked = await page.evaluate(() => {
      const btn = Array.from(document.querySelectorAll('button')).find(
        (b) => (b.textContent ?? '').trim() === 'Follow',
      ) as HTMLButtonElement | undefined;
      if (!btn) return false;
      btn.click();
      return true;
    });
    expect(followClicked, 'Follow ボタンが見つからない').toBe(true);
    // confirm dialog 出現待ち
    await page.waitForFunction(
      () => document.querySelector('[data-testid="modal-dialog-ok"]') !== null,
      { timeout: 10_000 },
    );
    const followResp = page.waitForResponse(
      (r) => r.url().includes('/api/following/create') && r.status() === 200,
      { timeout: 15_000 },
    );
    const okClicked = await page.evaluate(() => {
      const ok = document.querySelector('[data-testid="modal-dialog-ok"]') as HTMLButtonElement | null;
      if (!ok) return false;
      ok.click();
      return true;
    });
    expect(okClicked, '確認ダイアログの OK が見つからない').toBe(true);
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
