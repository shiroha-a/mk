/*
 * SPDX-FileCopyrightText: mk-go project
 * SPDX-License-Identifier: AGPL-3.0-only
 */

// /admin/relays で Remove button (ti-trash + "Remove") click →
// /api/admin/relays/remove round-trip する write-flow spec。
//
// admin/relays.vue:19 の MkButton danger は ti-trash + "Remove" text。
// click すると confirm dialog 無しで misskeyApi('admin/relays/remove') を
// 直接叩く (line 57-58)。admin_relay_add_form spec の inverse として、
// remove path を strict 化する。

import { readFileSync } from 'node:fs';
import { expect, test } from '@playwright/test';
import { callApi } from '../../../fixtures/api';
import { type RootFixture, uiSigninAsRoot } from '../../../fixtures/ui_auth';

test.describe('UI: /admin/relays remove button flow', () => {
  let root: RootFixture;
  test.beforeAll(() => {
    root = JSON.parse(readFileSync('.auth/root.json', 'utf-8'));
  });
  test.setTimeout(60_000);

  test('Remove button → /api/admin/relays/remove (no confirm dialog)', async ({
    page,
    baseURL,
    request,
  }) => {
    // 1. test 用 relay を API で add (URL は spec 専用 hostname で重複回避)
    const inbox = `https://pwrelay-${Date.now().toString().slice(-9)}.invalid/inbox`;
    const addResp = await callApi(request, 'admin/relays/add', {
      i: root.token,
      inbox,
    });
    expect(addResp.status()).toBe(200);

    // 2. /admin/relays を開く
    await uiSigninAsRoot(page, baseURL, root);
    await page.goto(`${baseURL}/admin/relays`, {
      waitUntil: 'domcontentloaded',
    });

    // inbox URL が body に出るまで待つ (= list 反映)
    await page.waitForFunction(
      (i) => document.body.textContent?.includes(i) ?? false,
      inbox,
      { timeout: 20_000 },
    );

    // 3. Remove button (= ti-trash + "Remove" text) hydrate を待つ。複数 relay
    // が登録済の可能性があるが、本 spec は 1 つだけ click すれば API が走る
    // ことを verify するので最初の Remove で十分。ただし他 relay を間違って
    // 消さないため、追加した relay の inbox が含まれる panel 内の button を
    // 探すべき。relays.vue では各 relay panel が `_panel` class を持ち、URL
    // text が含まれる。
    await page.waitForFunction(
      (i) => {
        const panels = Array.from(document.querySelectorAll('div')) as HTMLDivElement[];
        return panels.some((p) => {
          if (!(p.textContent ?? '').includes(i)) return false;
          const btns = Array.from(p.querySelectorAll('button')) as HTMLButtonElement[];
          return btns.some((b) => b.querySelector('i.ti-trash') !== null);
        });
      },
      inbox,
      { timeout: 15_000 },
    );

    // 4. 該当 relay の Remove button click → admin/relays/remove
    const removeResp = page.waitForResponse(
      (r) => r.url().includes('/api/admin/relays/remove') && r.status() < 300,
      { timeout: 15_000 },
    );
    await page.evaluate((i) => {
      const panels = Array.from(document.querySelectorAll('div')) as HTMLDivElement[];
      const target = panels.find((p) => {
        if (!(p.textContent ?? '').includes(i)) return false;
        return p.querySelector('button i.ti-trash') !== null;
      });
      if (!target) return;
      const btn = Array.from(target.querySelectorAll('button')).find(
        (b) => b.querySelector('i.ti-trash') !== null,
      ) as HTMLButtonElement | undefined;
      btn?.click();
    }, inbox);
    await removeResp;

    // 5. API 経由で削除確認
    const listResp = await callApi(request, 'admin/relays/list', {
      i: root.token,
    });
    expect(listResp.status()).toBe(200);
    const list = await listResp.json();
    const found = list.find((r: { inbox: string }) => r.inbox === inbox);
    expect(found).toBeUndefined();
  });
});
