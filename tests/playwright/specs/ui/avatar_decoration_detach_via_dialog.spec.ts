// /settings/avatar-decoration で 装着済 decoration を click → XDialog →
// "Detach" button (ti-x + "Detach") click → /api/i/update が
// avatarDecorations 配列を縮めて round-trip する write-flow spec。
//
// avatar-decoration.dialog.vue:40 の Detach button は usingIndex != null
// (= 装着済) のとき表示される。click すると親の openDecoration callback
// "detach" 経由で i/update に新 avatarDecorations 配列 (= 該当 entry を
// 除いた配列) を送る。
//
// avatar_decoration_attach_via_dialog の sister。setup で必ず 1 装着の
// state にしておき、UI 経由で外して空配列に戻る path を verify する。

import { readFileSync } from 'node:fs';
import { expect, test } from '@playwright/test';
import { callApi } from '../../fixtures/api';
import { type RootFixture, uiSigninAsRoot } from '../../fixtures/ui_auth';

test.describe('UI: /settings/avatar-decoration detach via dialog flow', () => {
  let root: RootFixture;
  test.beforeAll(() => {
    root = JSON.parse(readFileSync('.auth/root.json', 'utf-8'));
  });
  test.setTimeout(90_000);

  test('signupUser-equivalent decoration setup → click attached → Detach → /api/i/update with empty avatarDecorations', async ({
    page,
    baseURL,
    request,
  }) => {
    // 1. admin/avatar-decorations/create で decoration を新規作成
    const decorationName = `pw-deco-d-${Date.now().toString().slice(-9)}`;
    const createResp = await callApi(request, 'admin/avatar-decorations/create', {
      i: root.token,
      name: decorationName,
      description: '',
      url: `https://example.invalid/decoration/${decorationName}.png`,
      roleIdsThatCanBeUsedThisDecoration: [],
    });
    expect(createResp.status()).toBeLessThan(400);
    const createdBody = await createResp.json();
    const decorationId: string = createdBody.id;
    const decorationUrl: string = createdBody.url;
    expect(decorationId).toBeTruthy();

    // 2. root の avatarDecorations を [新 decoration 1 個] に reset
    await callApi(request, 'i/update', {
      i: root.token,
      avatarDecorations: [
        {
          id: decorationId,
          angle: 0,
          flipH: false,
          offsetX: 0,
          offsetY: 0,
        },
      ],
    });

    // 3. /settings/avatar-decoration を開く
    await uiSigninAsRoot(page, baseURL, root);
    await page.goto(`${baseURL}/settings/avatar-decoration`, {
      waitUntil: 'domcontentloaded',
    });

    // 装着 thumbnail (= openAttachedDecoration trigger) hydrate を待つ。
    // 装着済 decoration は <img src="<url>"> として render されるので
    // url を含む img を待つ。
    await page.waitForFunction(
      (u) => {
        const imgs = Array.from(document.querySelectorAll('img')) as HTMLImageElement[];
        return imgs.some((i) => i.src.includes(u));
      },
      decorationUrl,
      { timeout: 20_000 },
    );

    // 4. 装着済 thumbnail (= 上部 attached list の最初の card) を click。
    // attached card は openAttachedDecoration を bind しており、img を含む。
    // 装着済の url 専用 img を持つ card を click することで XDialog が
    // usingIndex != null mode で起動する。
    await page.evaluate((u) => {
      const imgs = Array.from(document.querySelectorAll('img')) as HTMLImageElement[];
      const targetImg = imgs.find((i) => i.src.includes(u));
      if (!targetImg) return;
      // img を含む clickable 親要素を探す。click handler は a / div / button
      // のいずれかにある (avatar-decoration.vue:25-26 では @click bind の
      // div)。
      let node: HTMLElement | null = targetImg;
      for (let depth = 0; depth < 6 && node; depth++) {
        if (node.tagName === 'A' || node.tagName === 'BUTTON' || node.onclick) {
          node.click();
          return;
        }
        node = node.parentElement;
      }
      // fallback: img の最も近い parent を click (Vue listener が capture)
      targetImg.click();
    }, decorationUrl);

    // 5. XDialog の "Detach" button (ti-x + "Detach") hydrate を待つ。
    // attach button は usingIndex == null のときに表示されるが、本 spec は
    // usingIndex != null path なので detach button が出現する。
    await page.waitForFunction(
      () => {
        const btns = Array.from(document.querySelectorAll('button')) as HTMLButtonElement[];
        return btns.some(
          (b) =>
            !b.disabled &&
            b.querySelector('i.ti-x') !== null &&
            (b.textContent ?? '').toLowerCase().includes('detach'),
        );
      },
      { timeout: 15_000 },
    );

    // 6. Detach click → i/update round-trip (avatarDecorations 配列が縮む)
    const updateResp = page.waitForResponse(
      (r) => r.url().includes('/api/i/update') && r.status() < 300,
      { timeout: 15_000 },
    );
    await page.evaluate(() => {
      const btns = Array.from(document.querySelectorAll('button')) as HTMLButtonElement[];
      const target = btns.find(
        (b) =>
          !b.disabled &&
          b.querySelector('i.ti-x') !== null &&
          (b.textContent ?? '').toLowerCase().includes('detach'),
      );
      target?.click();
    });
    const update = await updateResp;
    const body = await update.json();
    expect(Array.isArray(body.avatarDecorations)).toBe(true);
    expect(body.avatarDecorations.length).toBe(0);
  });
});
