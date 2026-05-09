// /settings/profile で name MkInput を編集 → manualSave button click →
// /api/i で更新確認する **真の write-flow** spec。
//
// MkInput (manualSave) は input 値変更で `<MkButton :class="$style.save">`
// を表示する。click すると updated event → os.apiWithDialog('i/update', ...)
// で name が persist される。本 spec は前後で /api/i.name を比較して
// round-trip を verify する。
//
// 注意: /settings/* は親 layout の MkSuperMenu に search MkInput (type=search)
// があり、page 全体 input[0] はこの search box。form 本体の name input を
// 取るには type !== "search" で filter が必要 (#744 batch3)。

import { readFileSync } from 'node:fs';
import { expect, test } from '@playwright/test';
import { type RootFixture, uiSigninAsRoot } from '../../fixtures/ui_auth';

test.describe('UI: /settings/profile name save flow', () => {
  let root: RootFixture;

  test.beforeAll(() => {
    root = JSON.parse(readFileSync('.auth/root.json', 'utf-8'));
  });

  test.setTimeout(60_000);

  test('edit name MkInput → click save → i/update response reflects new name', async ({
    page,
    baseURL,
  }) => {
    await uiSigninAsRoot(page, baseURL, root);
    await page.goto(`${baseURL}/settings/profile`, { waitUntil: 'domcontentloaded' });

    // name MkInput の <input> 要素を待つ。MkInput は type prop default が
    // 暗黙の "text" (HTML 仕様) で type 属性を render しないため、CSS
    // selector の input[type="text"] では match しない。代わりに JS
    // input.type === "text" を見て filter する (HTMLInputElement.type は
    // type 属性が空でも default で "text" を返す, #744 batch3)。
    await page.waitForFunction(
      () => {
        const inputs = Array.from(document.querySelectorAll('input')) as HTMLInputElement[];
        return inputs.some((i) => i.type === 'text');
      },
      { timeout: 20_000 },
    );

    const newName = `pwname-${Date.now().toString().slice(-9)}`;
    // 最初の text input (= profile.name) に native value setter で書き込み。
    // post_note.spec.ts と同 pattern (Vue v-model に input event を届ける)。
    await page.evaluate((n) => {
      const inputs = (Array.from(document.querySelectorAll('input')) as HTMLInputElement[]).filter(
        (i) => i.type === 'text',
      );
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
    // i/update 応答 body の updated user object で name を verify する。
    // /api/i は middleware の tokenCache (30s TTL, auth.go:42) で stale に
    // なるため round-trip 検証には使わない (#744 batch3 で発覚)。
    const update = await updateResp;
    const updateBody = await update.json();
    expect(updateBody.name).toBe(newName);
  });
});
