// /admin/email-settings page で SMTP settings form が hydrate される
// ことを smoke する spec。

import { readFileSync } from 'node:fs';
import { expect, test } from '@playwright/test';
import { type RootFixture, uiSigninAsRoot } from '../../fixtures/ui_auth';

test.describe('UI: /admin/email-settings page hydrates SMTP form', () => {
  let root: RootFixture;

  test.beforeAll(() => {
    root = JSON.parse(readFileSync('.auth/root.json', 'utf-8'));
  });

  test.setTimeout(60_000);

  test('email server form hydrates with SMTP inputs', async ({ page, baseURL }) => {
    await uiSigninAsRoot(page, baseURL, root);
    const resp = await page.goto(`${baseURL}/admin/email-settings`, {
      waitUntil: 'domcontentloaded',
    });
    expect(resp!.status()).toBe(200);

    // page title (i18n.ts.emailServer → "Email server") + SMTP host/port/
    // user/pass などの input が必要数 mount される
    await page.waitForFunction(
      () => {
        const text = document.body.textContent ?? '';
        const inputs = document.querySelectorAll('input').length;
        return text.includes('Email server') && inputs >= 3;
      },
      { timeout: 20_000 },
    );
  });
});
