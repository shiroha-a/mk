// /chat/room/:id の "Members" tab → "Invite user" button →
// MkUserSelectDialog → username 入力 + user 選択 + OK →
// /api/chat/rooms/invitations/create round-trip する write-flow spec。
//
// MkPageHeader tab ti-users (= members) → room.members.vue の
// "Invite user" button (ti-plus + "Invite user") → emit inviteUser →
// room.vue:369 の inviteUser() で os.selectUser → MkUserSelectDialog
// (MkModalWindow + username input + user list + OK)。
// OK button は MkModalWindow の `withOkButton`、ti-check icon を持つ
// (= avatar_decoration_attach_via_dialog で確立 pattern)。

import { readFileSync } from 'node:fs';
import { expect, test } from '@playwright/test';
import { callApi } from '../../fixtures/api';
import { signupUser } from '../../fixtures/auth';
import { type RootFixture, uiSigninAsRoot } from '../../fixtures/ui_auth';

test.describe('UI: /chat/room invite via selectUser flow', () => {
  let root: RootFixture;
  test.beforeAll(() => {
    root = JSON.parse(readFileSync('.auth/root.json', 'utf-8'));
  });
  test.setTimeout(90_000);

  test('Invite user → username search → select + OK → /api/chat/rooms/invitations/create', async ({
    page,
    baseURL,
    request,
  }) => {
    // 1. invitee を signup + 検索 hit させるため fresh username
    const invitee = await signupUser(request, `pwinv${Date.now().toString().slice(-9)}`);
    expect(invitee.id).toBeTruthy();

    // 2. test 用 chat room を create
    const roomName = `pw-invite-room-${Date.now().toString().slice(-9)}`;
    const roomResp = await callApi(request, 'chat/rooms/create', {
      i: root.token,
      name: roomName,
    });
    expect(roomResp.status()).toBe(200);
    const roomId = (await roomResp.json()).id;

    // 3. /chat/room/:id を root として開く
    await uiSigninAsRoot(page, baseURL, root);
    await page.goto(`${baseURL}/chat/room/${roomId}`, {
      waitUntil: 'domcontentloaded',
    });

    // 4. Members tab (= ti-users icon を持つ tab button) を click。
    // MkPageHeader の tab button は通常 page header section にある。
    await page.waitForFunction(
      () => {
        const btns = Array.from(document.querySelectorAll('button')) as HTMLButtonElement[];
        return btns.some((b) => b.querySelector('i.ti-users') !== null);
      },
      { timeout: 20_000 },
    );
    await page.evaluate(() => {
      const btns = Array.from(document.querySelectorAll('button')) as HTMLButtonElement[];
      const target = btns.find((b) => b.querySelector('i.ti-users') !== null);
      target?.click();
    });

    // 5. "Invite user" button (= room.members.vue:8、ti-plus icon、
    // ti-fw 修飾無し = page top の primary button) を待って click
    await page.waitForFunction(
      () => {
        const btns = Array.from(document.querySelectorAll('button')) as HTMLButtonElement[];
        return btns.some(
          (b) =>
            b.querySelector('i.ti-plus') !== null &&
            !b.querySelector('i.ti-fw') &&
            (b.textContent ?? '').toLowerCase().includes('invite'),
        );
      },
      { timeout: 15_000 },
    );
    await page.evaluate(() => {
      const btns = Array.from(document.querySelectorAll('button')) as HTMLButtonElement[];
      const target = btns.find(
        (b) =>
          b.querySelector('i.ti-plus') !== null &&
          !b.querySelector('i.ti-fw') &&
          (b.textContent ?? '').toLowerCase().includes('invite'),
      );
      target?.click();
    });

    // 6. MkUserSelectDialog の username input が現れる (autofocus)。
    // 同 dialog は localOnly=true mode なので host input は無く、username
    // input 1 個だけ。type は default (text)。
    await page.waitForFunction(
      () => {
        const inputs = Array.from(document.querySelectorAll('input')) as HTMLInputElement[];
        return inputs.filter((i) => i.type === 'text').length >= 1;
      },
      { timeout: 10_000 },
    );

    // 7. invitee.username を type → search 結果が出る。
    // 旧実装は native `dispatchEvent(new Event('input'))` で v-model bind を
    // trigger していたが、何らかの timing で MkInput の `@update:modelValue`
    // → search() chain が走らないケースがあり、search 結果が DOM に出ず
    // 後段の wait が timeout していた。Playwright の `locator.fill()` は
    // 内部で keystroke emulation + input event を自動 dispatch するので、
    // Vue v-model が確実に反応する。`.last()` で dialog 内の最新 mount
    // input を取る。
    await page.locator('input[type="text"]').last().fill(invitee.username);

    // 8. 検索結果リストに invitee が出るまで待つ
    await page.waitForFunction(
      (u) => document.body.textContent?.includes(u) ?? false,
      invitee.username,
      { timeout: 15_000 },
    );

    // 9. 検索結果の user item を click (= selected = user)
    // user item は `_button` class + MkAcct で username text を含む。
    await page.evaluate((u) => {
      const els = Array.from(document.querySelectorAll('div')) as HTMLDivElement[];
      const target = els.find(
        (el) =>
          el.classList.contains('_button') &&
          (el.textContent ?? '').includes(u),
      );
      target?.click();
    }, invitee.username);

    // 10. MkModalWindow の OK button (ti-check) を click → API
    const inviteResp = page.waitForResponse(
      (r) =>
        r.url().includes('/api/chat/rooms/invitations/create') &&
        r.status() < 300,
      { timeout: 15_000 },
    );
    await page.evaluate(() => {
      const btns = Array.from(document.querySelectorAll('button')) as HTMLButtonElement[];
      const ok = btns.find(
        (b) => !b.disabled && b.querySelector('i.ti-check') !== null,
      );
      ok?.click();
    });
    await inviteResp;
  });
});
