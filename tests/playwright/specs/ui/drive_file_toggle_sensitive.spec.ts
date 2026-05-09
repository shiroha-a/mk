// /my/drive/file/:fileId で markAsSensitive ボタンを click → /api/drive/files/update
// が走り isSensitive が反転することを verify する write-flow spec。
//
// drive.file.info.vue の toggleSensitive() は misskeyApi('drive/files/update',
// { fileId, isSensitive: !file.isSensitive }) を直接呼ぶ (line 136)。dialog
// は出ないので、button click → response 待ちで完結する。
// button は v-if/v-else で 2 つ用意されており、現在 isSensitive=false のとき
// `<i class="ti ti-eye-exclamation">` を持つ button (markAsSensitive) を
// click する。

import { readFileSync } from 'node:fs';
import { expect, test } from '@playwright/test';
import { type RootFixture, uiSigninAsRoot } from '../../fixtures/ui_auth';

test.describe('UI: /my/drive/file/:fileId toggle sensitive flow', () => {
  let root: RootFixture;
  test.beforeAll(() => {
    root = JSON.parse(readFileSync('.auth/root.json', 'utf-8'));
  });
  test.setTimeout(60_000);

  test('click eye-exclamation button → /api/drive/files/update isSensitive=true', async ({
    page,
    baseURL,
    request,
  }) => {
    // 1. test 用 file を upload (default で isSensitive=false)
    const fileName = `pw-sens-${Date.now()}.png`;
    const uploadResp = await request.post(`${baseURL}/api/drive/files/create`, {
      ignoreHTTPSErrors: true,
      multipart: {
        i: root.token,
        file: {
          name: fileName,
          mimeType: 'image/png',
          buffer: Buffer.from(
            'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYAAAAAYAAjCB0C8AAAAASUVORK5CYII=',
            'base64',
          ),
        },
      },
    });
    expect(uploadResp.status()).toBe(200);
    const file = await uploadResp.json();
    const fileId: string = file.id;
    expect(fileId).toBeTruthy();

    // dedupe で既存 file が isSensitive=true で返ってくる可能性があるため、
    // まず API で false にリセットして spec 状態を deterministic にする。
    await request.post(`${baseURL}/api/drive/files/update`, {
      ignoreHTTPSErrors: true,
      data: { i: root.token, fileId, isSensitive: false },
    });

    // 2. detail page を開いて hydrate を待つ
    await uiSigninAsRoot(page, baseURL, root);
    const resp = await page.goto(`${baseURL}/my/drive/file/${fileId}`, {
      waitUntil: 'domcontentloaded',
    });
    expect(resp!.status()).toBe(200);

    // markAsSensitive button (= ti-eye-exclamation icon、isSensitive=false の v-else 分岐)
    await page.waitForFunction(
      () => {
        const btns = Array.from(document.querySelectorAll('button')) as HTMLButtonElement[];
        return btns.some((b) => b.querySelector('i.ti-eye-exclamation') !== null);
      },
      { timeout: 20_000 },
    );

    // 3. button click → /api/drive/files/update 走る
    const updateResp = page.waitForResponse(
      (r) => r.url().includes('/api/drive/files/update') && r.status() < 300,
      { timeout: 15_000 },
    );
    await page.evaluate(() => {
      const btns = Array.from(document.querySelectorAll('button')) as HTMLButtonElement[];
      const target = btns.find((b) => b.querySelector('i.ti-eye-exclamation') !== null);
      target?.click();
    });
    await updateResp;

    // 4. API 経由で isSensitive=true を verify
    const showResp = await request.post(`${baseURL}/api/drive/files/show`, {
      ignoreHTTPSErrors: true,
      data: { i: root.token, fileId },
    });
    expect(showResp.status()).toBe(200);
    const shown = await showResp.json();
    expect(shown.id).toBe(fileId);
    expect(shown.isSensitive).toBe(true);
  });
});
