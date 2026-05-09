// /settings/profile で name MkInput を編集 → manualSave button click →
// /api/i で更新確認する **真の write-flow** spec。
//
// MkInput (manualSave) は input 値変更で `<MkButton :class="$style.save">`
// を表示する。click すると updated event → os.apiWithDialog('i/update', ...)
// で name が persist される。本 spec は前後で /api/i.name を比較して
// round-trip を verify する。

import { readFileSync } from 'node:fs';
import { expect, test } from '@playwright/test';
import { callApi } from '../../fixtures/api';
import { type RootFixture, uiSigninAsRoot } from '../../fixtures/ui_auth';

test.describe('UI: /settings/profile name save flow', () => {
  let root: RootFixture;

  test.beforeAll(() => {
    root = JSON.parse(readFileSync('.auth/root.json', 'utf-8'));
  });

  test.setTimeout(60_000);

  test('edit name MkInput → click save → /api/i reflects new name', async ({
    page,
    baseURL,
    request,
  }) => {
    await uiSigninAsRoot(page, baseURL, root);
    await page.goto(`${baseURL}/settings/profile`, { waitUntil: 'domcontentloaded' });

    // name MkInput の <input> 要素を待つ。/settings/profile は MkInput が
    // 多数あるが、name は 1 つ目の MkInput (= profile.name)。本 spec は
    // SearchLabel の "Name" を持つ MkInput を locate する。
    await page.waitForFunction(
      () => {
        // MkInput 内 input は v-model="profile.name" で max=30。最初に出てくる
        // input[type=text] が name 想定。複数 MkInput が並ぶので index で取る。
        const inputs = Array.from(
          document.querySelectorAll('input[type="text"]'),
        ) as HTMLInputElement[];
        return inputs.length > 0;
      },
      { timeout: 20_000 },
    );

    const newName = `pwname-${Date.now().toString().slice(-9)}`;
    // 最初の text input (= profile.name) に native value setter で書き込み。
    // post_note.spec.ts と同 pattern (Vue v-model に input event を届ける)。
    await page.evaluate((n) => {
      const inputs = Array.from(
        document.querySelectorAll('input[type="text"]'),
      ) as HTMLInputElement[];
      const target = inputs[0];
      if (!target) return;
      target.focus();
      const setter = Object.getOwnPropertyDescriptor(
        window.HTMLInputElement.prototype,
        'value',
      )?.set;
      setter?.call(target, n);
      target.dispatchEvent(new Event('input', { bubbles: true }));
      target.dispatchEvent(new Event('change', { bubbles: true }));
    }, newName);

    // manualSave button が出現するまで待つ。MkInput は value 変更で changed
    // computed が true になり save button を mount する。
    await page.waitForFunction(
      () => {
        // save button は MkButton primary。MkInput 内の最初の MkButton
        // (= save) を含む。CSS class $style.save は scoped で predict 困難
        // なので、profile.name の MkInput container に紐づいた button を
        // 探す。簡易には body 内 "Save" button text で取る。
        const btns = Array.from(document.querySelectorAll('button'));
        return btns.some((b) => (b.textContent ?? '').includes('Save'));
      },
      { timeout: 10_000 },
    );

    // i/update response を捕捉して save click
    const updateResp = page.waitForResponse(
      (r) => r.url().includes('/api/i/update') && r.status() === 200,
      { timeout: 15_000 },
    );
    await page.evaluate(() => {
      // 最初の "Save" button を click (= name MkInput の save)
      const btn = Array.from(document.querySelectorAll('button')).find((b) =>
        (b.textContent ?? '').includes('Save'),
      ) as HTMLButtonElement | undefined;
      btn?.click();
    });
    await updateResp;

    // backend の /api/i で name が更新されたか verify
    const meResp = await callApi(request, 'i', { i: root.token });
    expect(meResp.status()).toBe(200);
    const me = await meResp.json();
    expect(me.name).toBe(newName);
  });
});
