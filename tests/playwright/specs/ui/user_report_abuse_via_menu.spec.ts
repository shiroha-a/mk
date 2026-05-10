// /@target の 3-dot menu → "Report abuse" item (ti-fw ti-exclamation-circle)
// → MkAbuseReportWindow popup → MkTextarea に comment 入力 → Send button
// click → /api/users/report-abuse round-trip する write-flow spec。
//
// get-user-menu.ts:423-427 の reportAbuse menu item は popupAsyncWithDialog
// で MkAbuseReportWindow を popup する。window は textarea + Send (primary)
// button のみ。Send click で users/report-abuse を叩く (line 54)。
// 同 menu item は別 user 視点でのみ表示される (= root menu に自分の
// reportAbuse が無い)。signupUser で target を作って root が報告する形。

import { readFileSync } from 'node:fs';
import { expect, test } from '@playwright/test';
import { signupUser } from '../../fixtures/auth';
import { type RootFixture, uiSigninAsRoot } from '../../fixtures/ui_auth';

test.describe('UI: /@target abuse report flow', () => {
  let root: RootFixture;
  test.beforeAll(() => {
    root = JSON.parse(readFileSync('.auth/root.json', 'utf-8'));
  });
  test.setTimeout(90_000);

  test('open menu → Report abuse → comment + Send → /api/users/report-abuse', async ({
    page,
    baseURL,
    request,
  }) => {
    // 1. target user を signup
    const target = await signupUser(request, `pwra${Date.now().toString().slice(-9)}`);
    expect(target.id).toBeTruthy();

    // 2. /@target を root として開く
    await uiSigninAsRoot(page, baseURL, root);
    await page.goto(`${baseURL}/@${target.username}`, {
      waitUntil: 'domcontentloaded',
    });

    await page.waitForFunction(
      (u) => document.body.textContent?.includes(u) ?? false,
      target.username,
      { timeout: 20_000 },
    );

    // 3. 3-dot menu (ti-dots) → Report abuse item (ti-fw ti-exclamation-circle)
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

    await page.waitForFunction(
      () => {
        const btns = Array.from(document.querySelectorAll('button')) as HTMLButtonElement[];
        return btns.some(
          (b) => b.querySelector('i.ti-fw.ti-exclamation-circle') !== null,
        );
      },
      { timeout: 10_000 },
    );

    await page.evaluate(() => {
      const btns = Array.from(document.querySelectorAll('button')) as HTMLButtonElement[];
      const target = btns.find(
        (b) => b.querySelector('i.ti-fw.ti-exclamation-circle') !== null,
      );
      target?.click();
    });

    // 4. MkAbuseReportWindow が popup → textarea が出る。spam content など
    // 任意の文字列を送って round-trip を確認する (admin 側で削除すべき
    // report が積まれるが、test 環境では cleanup 不要)。
    await page.waitForFunction(
      () => document.querySelectorAll('textarea').length >= 1,
      { timeout: 10_000 },
    );

    const comment = `pw-abuse-${Date.now()}`;
    await page.evaluate((c) => {
      const tas = Array.from(document.querySelectorAll('textarea')) as HTMLTextAreaElement[];
      const target = tas[tas.length - 1];
      if (!target) return;
      target.focus();
      const setter = Object.getOwnPropertyDescriptor(
        window.HTMLTextAreaElement.prototype,
        'value',
      )?.set;
      setter?.call(target, c);
      target.dispatchEvent(new Event('input', { bubbles: true }));
    }, comment);

    // 5. Send button (= primary "Send") click → users/report-abuse
    const reportResp = page.waitForResponse(
      (r) => r.url().includes('/api/users/report-abuse') && r.status() < 300,
      { timeout: 15_000 },
    );
    await page.evaluate(() => {
      const btns = Array.from(document.querySelectorAll('button')) as HTMLButtonElement[];
      const send = btns.find(
        (b) => !b.disabled && (b.textContent ?? '').trim().match(/^Send$/i),
      );
      send?.click();
    });
    await reportResp;
  });
});
