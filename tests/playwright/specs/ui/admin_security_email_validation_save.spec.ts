// /admin/security の "Active Email Validation" MkFolder を expand →
// switch を toggle → form footer の Save → /api/admin/update-meta が
// round-trip する write-flow spec。
//
// admin/security.vue:69-? の emailValidationForm は MkFolder 内で複数の
// MkSwitch / MkInput を持つ。switch 変更で form.modified=true → footer
// MkFormFooter の Save が出現する pattern。admin_security_ip_logging_save
// の sister として、別 folder に対する form-save flow を担保する。

import { readFileSync } from 'node:fs';
import { expect, test } from '@playwright/test';
import { callApi } from '../../fixtures/api';
import { type RootFixture, uiSigninAsRoot } from '../../fixtures/ui_auth';

test.describe('UI: /admin/security email validation form save flow', () => {
  let root: RootFixture;
  test.beforeAll(() => {
    root = JSON.parse(readFileSync('.auth/root.json', 'utf-8'));
  });
  test.setTimeout(60_000);

  test('expand Active Email Validation folder → toggle switch → Save → /api/admin/update-meta', async ({
    page,
    baseURL,
    request,
  }) => {
    // setup: 既知 state (false) に reset。
    await callApi(request, 'admin/update-meta', {
      i: root.token,
      enableActiveEmailValidation: false,
    });

    try {
      await uiSigninAsRoot(page, baseURL, root);
      await page.goto(`${baseURL}/admin/security`, {
        waitUntil: 'domcontentloaded',
      });

      await page.waitForFunction(
        () => document.querySelectorAll('[data-cy-folder-header]').length >= 3,
        { timeout: 20_000 },
      );

      // "Active Email Validation" folder を expand
      await page.evaluate(() => {
        const headers = Array.from(
          document.querySelectorAll('[data-cy-folder-header]'),
        ) as HTMLElement[];
        const target = headers.find((h) =>
          (h.textContent ?? '').includes('Active Email Validation'),
        );
        target?.click();
      });

      // checkbox が 1 個以上 mount するまで待つ
      await page.waitForFunction(
        () => document.querySelectorAll('input[type="checkbox"]').length >= 1,
        { timeout: 10_000 },
      );

      // 最初の checkbox を toggle (= modified=true → Save 出現)
      await page.evaluate(() => {
        const cbs = Array.from(
          document.querySelectorAll('input[type="checkbox"]'),
        ) as HTMLInputElement[];
        cbs[0]?.click();
      });

      await page.waitForFunction(
        () => {
          const btns = Array.from(document.querySelectorAll('button'));
          return btns.some((b) => (b.textContent ?? '').includes('Save'));
        },
        { timeout: 10_000 },
      );

      const updateResp = page.waitForResponse(
        (r) => r.url().includes('/api/admin/update-meta') && r.status() < 300,
        { timeout: 15_000 },
      );
      await page.evaluate(() => {
        const btn = Array.from(document.querySelectorAll('button')).find((b) =>
          (b.textContent ?? '').includes('Save'),
        ) as HTMLButtonElement | undefined;
        btn?.click();
      });
      await updateResp;
    } finally {
      // cleanup: enable* / verifymail / truemail 系を全部 false に戻す。
      // on のまま残ると以降の signup 系 spec が email validation で fail
      // する isolation 破壊を引き起こす。同 form の他 field も spec 中で
      // toggle される可能性があるので一律 false 化。
      await callApi(request, 'admin/update-meta', {
        i: root.token,
        enableActiveEmailValidation: false,
        enableVerifymailApi: false,
        enableTruemailApi: false,
      });
    }
  });
});
