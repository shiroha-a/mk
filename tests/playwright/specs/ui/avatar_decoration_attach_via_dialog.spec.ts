// /settings/avatar-decoration で decoration thumbnail を click →
// XDialog popup → "Attach" button (ti-check + "Attach") click →
// /api/i/update が avatarDecorations 配列で round-trip する write-flow
// spec。
//
// avatar-decoration.vue:34-39 の XDecoration を click すると openDecoration
// → os.popup(XDialog) が起動。XDialog は usingIndex=null (= 未装着) なら
// ti-check + "Attach" button を表示 (avatar-decoration.dialog.vue:41)。
// click すると i/update に avatarDecorations を追加して flush する
// (avatar-decoration.vue:96)。
//
// setup: admin/avatar-decorations/create で decoration を作成。spec 後
// 始末で root の avatarDecorations を空にする (= 累積影響回避)。

import { readFileSync } from 'node:fs';
import { expect, test } from '@playwright/test';
import { callApi } from '../../fixtures/api';
import { type RootFixture, uiSigninAsRoot } from '../../fixtures/ui_auth';

test.describe('UI: /settings/avatar-decoration attach via dialog flow', () => {
  let root: RootFixture;
  test.beforeAll(() => {
    root = JSON.parse(readFileSync('.auth/root.json', 'utf-8'));
  });
  test.setTimeout(90_000);

  test('click decoration → XDialog → Attach → /api/i/update with avatarDecorations', async ({
    page,
    baseURL,
    request,
  }) => {
    // 1. setup: 既存装着を全外し (= dialog の usingIndex=null path を確実に)
    await callApi(request, 'i/update', {
      i: root.token,
      avatarDecorations: [],
    });

    // 2. admin/avatar-decorations/create で decoration を作成
    const decorationName = `pw-deco-${Date.now().toString().slice(-9)}`;
    const createResp = await callApi(request, 'admin/avatar-decorations/create', {
      i: root.token,
      name: decorationName,
      description: '',
      url: `https://example.invalid/decoration/${decorationName}.png`,
      roleIdsThatCanBeUsedThisDecoration: [],
    });
    expect(createResp.status()).toBeLessThan(400);

    // 3. /settings/avatar-decoration を開く
    await uiSigninAsRoot(page, baseURL, root);
    await page.goto(`${baseURL}/settings/avatar-decoration`, {
      waitUntil: 'domcontentloaded',
    });

    // 4. decoration 一覧 hydrate を待つ (= name が body に出る)
    await page.waitForFunction(
      (n) => document.body.textContent?.includes(n) ?? false,
      decorationName,
      { timeout: 20_000 },
    );

    // 5. 該当 decoration の card を click (= openDecoration → XDialog)
    // avatar-decoration.decoration.vue の root は `<div :class="$style.root"
    // @click="emit('click')">` で CSS module の root class を持つ。元の
    // `div, button` 全部対象 + textContent.includes selector は body 等の
    // page-level 親要素を pickup して click を listener 持たない要素に投げ、
    // XDialog が出ないまま 90s timeout する flake になっていた。
    // `[class*="decorations"]` (= 親 grid container) 内の `[class*="root"]`
    // (= XDecoration root) で精度高く絞る。CSS module hash の差分は
    // `[class*="..."]` で吸収。
    await page.evaluate((n) => {
      const roots: Element[] = [];
      document.querySelectorAll('[class*="decorations"]').forEach((c) => {
        c.querySelectorAll('[class*="root"]').forEach((r) => roots.push(r));
      });
      const target = roots.find(
        (el) => (el.textContent ?? '').includes(n),
      ) as HTMLElement | undefined;
      target?.click();
    }, decorationName);

    // 6. XDialog の "Attach" button (ti-check + "Attach" text) hydrate を待つ
    await page.waitForFunction(
      () => {
        const btns = Array.from(document.querySelectorAll('button')) as HTMLButtonElement[];
        return btns.some(
          (b) =>
            !b.disabled &&
            b.querySelector('i.ti-check') !== null &&
            (b.textContent ?? '').toLowerCase().includes('attach'),
        );
      },
      { timeout: 15_000 },
    );

    // 7. Attach click → i/update round-trip
    const updateResp = page.waitForResponse(
      (r) => r.url().includes('/api/i/update') && r.status() < 300,
      { timeout: 15_000 },
    );
    await page.evaluate(() => {
      const btns = Array.from(document.querySelectorAll('button')) as HTMLButtonElement[];
      const target = btns.find(
        (b) =>
          !b.disabled &&
          b.querySelector('i.ti-check') !== null &&
          (b.textContent ?? '').toLowerCase().includes('attach'),
      );
      target?.click();
    });
    const update = await updateResp;
    const body = await update.json();
    // body の avatarDecorations 配列に 1 個以上の entry が含まれる。
    expect(Array.isArray(body.avatarDecorations)).toBe(true);
    expect(body.avatarDecorations.length).toBeGreaterThanOrEqual(1);

    // 8. cleanup: avatarDecorations を空に戻す
    await callApi(request, 'i/update', {
      i: root.token,
      avatarDecorations: [],
    });
  });
});
