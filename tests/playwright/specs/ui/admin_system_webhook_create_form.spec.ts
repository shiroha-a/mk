// /admin/system-webhook で "Create" button → MkSystemWebhookEditor dialog →
// title / url を埋めて Save → /api/admin/system-webhook/create が round-trip
// する write-flow spec。
//
// system-webhook.vue の onCreateWebhookClicked は
// showSystemWebhookEditorDialog({mode: 'create'}) を popup する (line 45-48)。
// dialog の中に MkInput が 3 個 (title / url / secret) と 5 個程度の switch
// (events.* trigger) が並び、最後に primary "Save" button (= onSubmitClicked)
// がある。本 spec は title + url 必須を埋めて Save を click する。

import { readFileSync } from 'node:fs';
import { expect, test } from '@playwright/test';
import { type RootFixture, uiSigninAsRoot } from '../../fixtures/ui_auth';

test.describe('UI: /admin/system-webhook create flow', () => {
  let root: RootFixture;
  test.beforeAll(() => {
    root = JSON.parse(readFileSync('.auth/root.json', 'utf-8'));
  });
  test.setTimeout(60_000);

  test('Create button → editor dialog → admin/system-webhook/create', async ({
    page,
    baseURL,
  }) => {
    await uiSigninAsRoot(page, baseURL, root);
    await page.goto(`${baseURL}/admin/system-webhook`, {
      waitUntil: 'domcontentloaded',
    });

    // "Create webhook" primary button (= ti-plus icon を持つ button) hydrate
    await page.waitForFunction(
      () => {
        const btns = Array.from(document.querySelectorAll('button')) as HTMLButtonElement[];
        return btns.some((b) => b.querySelector('i.ti-plus') !== null);
      },
      { timeout: 20_000 },
    );

    await page.evaluate(() => {
      const btns = Array.from(document.querySelectorAll('button')) as HTMLButtonElement[];
      const create = btns.find((b) => b.querySelector('i.ti-plus') !== null);
      create?.click();
    });

    // dialog 内 text input が 3 個 (title / url / secret) hydrate するまで待つ
    await page.waitForFunction(
      () => {
        const inputs = Array.from(document.querySelectorAll('input')) as HTMLInputElement[];
        return inputs.filter((i) => i.type === 'text').length >= 3;
      },
      { timeout: 10_000 },
    );

    // title / url を投入。3rd (secret) は optional なので空のまま。
    const title = `pw-webhook-${Date.now()}`;
    const url = `https://example.invalid/pw/${Date.now()}`;
    await page.evaluate(
      ({ t, u }) => {
        const inputs = (Array.from(document.querySelectorAll('input')) as HTMLInputElement[]).filter(
          (i) => i.type === 'text',
        );
        const setter = Object.getOwnPropertyDescriptor(
          window.HTMLInputElement.prototype,
          'value',
        )?.set;
        if (inputs[0]) {
          inputs[0].focus();
          setter?.call(inputs[0], t);
          inputs[0].dispatchEvent(new Event('input', { bubbles: true }));
        }
        if (inputs[1]) {
          inputs[1].focus();
          setter?.call(inputs[1], u);
          inputs[1].dispatchEvent(new Event('input', { bubbles: true }));
        }
      },
      { t: title, u: url },
    );

    // Save button (= primary, "Save" text を持つ button)
    const createResp = page.waitForResponse(
      (r) =>
        r.url().includes('/api/admin/system-webhook/create') && r.status() < 300,
      { timeout: 15_000 },
    );
    await page.evaluate(() => {
      const btns = Array.from(document.querySelectorAll('button')) as HTMLButtonElement[];
      const save = btns.find(
        (b) => !b.disabled && (b.textContent ?? '').trim().match(/Save/i),
      );
      save?.click();
    });
    const create = await createResp;
    expect(create.status()).toBeLessThan(300);
  });
});
