// /@username の 3-dot menu → menu の "Unblock" item (ti-fw ti-ban) を
// click → /api/blocking/delete が直接 round-trip する write-flow spec。
//
// get-user-menu.ts の block toggle item は user.isBlocking フラグで text
// (block / unblock) と action が切り替わる。block create は os.confirm
// 経由、delete (= unblock) は直接 API。本 spec は setup で root が
// target を block しておき、unblock の最短 path を verify する。

import { readFileSync } from 'node:fs';
import { expect, test } from '@playwright/test';
import { callApi } from '../../fixtures/api';
import { signupUser } from '../../fixtures/auth';
import { type RootFixture, uiSigninAsRoot } from '../../fixtures/ui_auth';

test.describe('UI: user 3-dot menu unblock flow', () => {
  let root: RootFixture;
  test.beforeAll(() => {
    root = JSON.parse(readFileSync('.auth/root.json', 'utf-8'));
  });
  test.setTimeout(90_000);

  test('signup target → root blocks via API → /@target menu → Unblock → /api/blocking/delete', async ({
    page,
    baseURL,
    request,
  }) => {
    const target = await signupUser(request, `pwub${Date.now().toString().slice(-9)}`);
    expect(target.id).toBeTruthy();

    // root が target を block (= isBlocking=true)
    const blockResp = await callApi(request, 'blocking/create', {
      i: root.token,
      userId: target.id,
    });
    expect(blockResp.status()).toBeLessThan(400);

    await uiSigninAsRoot(page, baseURL, root);
    await page.goto(`${baseURL}/@${target.username}`, {
      waitUntil: 'domcontentloaded',
    });

    await page.waitForFunction(
      (u) => document.body.textContent?.includes(u) ?? false,
      target.username,
      { timeout: 20_000 },
    );

    // 3-dot menu click
    await page.waitForFunction(
      () => {
        const btns = Array.from(document.querySelectorAll('button')) as HTMLButtonElement[];
        return btns.some((b) => b.querySelector('i.ti-dots') !== null);
      },
      { timeout: 15_000 },
    );
    await page.evaluate(() => {
      const btns = Array.from(document.querySelectorAll('button')) as HTMLButtonElement[];
      const target = btns.find((b) => b.querySelector('i.ti-dots') !== null);
      target?.click();
    });

    // "Unblock" item (= ti-fw ti-ban) を待って click。block / unblock どちら
    // でも icon は ti-ban (= text 切替のみ) なので、isBlocking=true 状態の
    // menu item は "Unblock" 動作を行う。
    await page.waitForFunction(
      () => {
        const btns = Array.from(document.querySelectorAll('button')) as HTMLButtonElement[];
        return btns.some((b) => b.querySelector('i.ti-fw.ti-ban') !== null);
      },
      { timeout: 10_000 },
    );

    const unblockResp = page.waitForResponse(
      (r) => r.url().includes('/api/blocking/delete') && r.status() < 300,
      { timeout: 15_000 },
    );
    await page.evaluate(() => {
      const btns = Array.from(document.querySelectorAll('button')) as HTMLButtonElement[];
      const target = btns.find((b) => b.querySelector('i.ti-fw.ti-ban') !== null);
      target?.click();
    });
    await unblockResp;

    // API verify: relation isBlocking=false
    const relResp = await callApi(request, 'users/relation', {
      i: root.token,
      userId: target.id,
    });
    expect(relResp.status()).toBe(200);
    const rel = await relResp.json();
    expect(rel.isBlocking).toBe(false);
  });
});
