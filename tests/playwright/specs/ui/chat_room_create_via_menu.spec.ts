// /chat home の "Start chat" button (ti-plus + primary) → popupMenu →
// "Room chat" parent (ti-users-group) → expand → "Create room"
// (ti-fw ti-plus) → inputText dialog で room 名入力 → OK →
// /api/chat/rooms/create round-trip する write-flow spec。
//
// chat/home.home.vue:67-84 の start() は os.popupMenu で 2 個 (individual /
// room parent) を popup する。Room chat は parent menu item で children に
// "Create room" を持つ。MkMenu.vue:167 の parent button は @mouseenter or
// @click (preferClick mode) で children を expand する。
//
// chat_send_to_room と並ぶ chat 系 popup spec。

import { readFileSync } from 'node:fs';
import { expect, test } from '@playwright/test';
import { type RootFixture, uiSigninAsRoot } from '../../fixtures/ui_auth';

test.describe('UI: /chat room create via menu flow', () => {
  let root: RootFixture;
  test.beforeAll(() => {
    root = JSON.parse(readFileSync('.auth/root.json', 'utf-8'));
  });
  test.setTimeout(60_000);

  test('Start chat → Room chat parent → Create room → inputText OK → /api/chat/rooms/create', async ({
    page,
    baseURL,
  }) => {
    await uiSigninAsRoot(page, baseURL, root);
    await page.goto(`${baseURL}/chat`, { waitUntil: 'domcontentloaded' });

    // Start chat button (= ti-plus icon + "Start chat" text の primary button)
    await page.waitForFunction(
      () => {
        const btns = Array.from(document.querySelectorAll('button')) as HTMLButtonElement[];
        // ti-plus を持ち textContent が "Start chat" を含む button (= page top
        // の primary button)。ti-fw 修飾はないので menu item の ti-plus と
        // 区別される。
        return btns.some(
          (b) =>
            b.querySelector('i.ti-plus') !== null &&
            !b.querySelector('i.ti-fw') &&
            (b.textContent ?? '').toLowerCase().includes('start'),
        );
      },
      { timeout: 20_000 },
    );

    // Start chat click → popup menu (2 items)
    await page.evaluate(() => {
      const btns = Array.from(document.querySelectorAll('button')) as HTMLButtonElement[];
      const target = btns.find(
        (b) =>
          b.querySelector('i.ti-plus') !== null &&
          !b.querySelector('i.ti-fw') &&
          (b.textContent ?? '').toLowerCase().includes('start'),
      );
      target?.click();
    });

    // popup menu の "Room chat" parent (ti-fw ti-users-group) を click。
    // mouseenter で children を expand する mode が default だが、Playwright
    // の click でも同 hander が呼ばれる (MkMenu.vue:171-173)。
    await page.waitForFunction(
      () => {
        const btns = Array.from(document.querySelectorAll('button')) as HTMLButtonElement[];
        return btns.some(
          (b) => b.querySelector('i.ti-fw.ti-users-group') !== null,
        );
      },
      { timeout: 10_000 },
    );
    await page.evaluate(() => {
      const btns = Array.from(document.querySelectorAll('button')) as HTMLButtonElement[];
      const target = btns.find(
        (b) => b.querySelector('i.ti-fw.ti-users-group') !== null,
      );
      // mouseenter event で children が出る場合があるので両方発火する
      target?.dispatchEvent(new MouseEvent('mouseenter', { bubbles: true }));
      target?.click();
    });

    // children menu の "Create room" (ti-fw ti-plus) を click → inputText dialog
    await page.waitForFunction(
      () => {
        const btns = Array.from(document.querySelectorAll('button')) as HTMLButtonElement[];
        return btns.some((b) => b.querySelector('i.ti-fw.ti-plus') !== null);
      },
      { timeout: 10_000 },
    );
    await page.evaluate(() => {
      const btns = Array.from(document.querySelectorAll('button')) as HTMLButtonElement[];
      const target = btns.find((b) => b.querySelector('i.ti-fw.ti-plus') !== null);
      target?.click();
    });

    // inputText dialog の text input が出るまで待つ
    await page.waitForFunction(
      () => {
        const inputs = Array.from(document.querySelectorAll('input')) as HTMLInputElement[];
        return inputs.some((i) => i.type === 'text');
      },
      { timeout: 10_000 },
    );

    // room 名を投入
    const roomName = `pw-chatroom-${Date.now().toString().slice(-9)}`;
    await page.evaluate((n) => {
      const inputs = (Array.from(document.querySelectorAll('input')) as HTMLInputElement[]).filter(
        (i) => i.type === 'text',
      );
      const target = inputs[inputs.length - 1];
      if (!target) return;
      target.focus();
      const setter = Object.getOwnPropertyDescriptor(
        window.HTMLInputElement.prototype,
        'value',
      )?.set;
      setter?.call(target, n);
      target.dispatchEvent(new Event('input', { bubbles: true }));
    }, roomName);

    // MkDialog OK → /api/chat/rooms/create
    const createResp = page.waitForResponse(
      (r) => r.url().includes('/api/chat/rooms/create') && r.status() < 300,
      { timeout: 15_000 },
    );
    await page.evaluate(() => {
      const ok = document.querySelector(
        '[data-testid="modal-dialog-ok"]',
      ) as HTMLButtonElement | null;
      ok?.click();
    });
    const create = await createResp;
    const body = await create.json();
    expect(body.id).toBeTruthy();
    expect(body.name).toBe(roomName);
  });
});
