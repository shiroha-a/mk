// /my/drive/file/:fileId page で drive file の info tab が hydrate されて
// file 名 / type / size 等が render されることを verify する spec。
//
// upload は API (drive/files/create multipart) で行い、UI 側は drive file
// 詳細 page の hydration (drive/files/show 経由) を smoke する。drive file
// 編集 / 削除の UI 操作は data-cy が無く fragility が高いため scope 外。

import { readFileSync } from 'node:fs';
import { expect, test } from '@playwright/test';
import { type RootFixture, uiSigninAsRoot } from '../../fixtures/ui_auth';

test.describe('UI: /my/drive/file/:fileId hydrates file metadata', () => {
  let root: RootFixture;

  test.beforeAll(() => {
    root = JSON.parse(readFileSync('.auth/root.json', 'utf-8'));
  });

  test.setTimeout(60_000);

  test('drive file detail page renders the file name', async ({ page, baseURL, request }) => {
    // 同 SHA256 の buffer を upload しても mk-go drive/files/create は
    // dedupe で既存 entry を返してくることがある (= test 累積で 1x1 PNG が
    // 既に登録済 + 同 hash で hit)。そのため API が返した file の name で
    // 検証する (= 自分が指定した name と一致するとは限らない)。本 spec の
    // scope は drive file detail page hydration の smoke なので、ID で
    // 引いた file の name が body に出れば十分。
    const fileName = `pw-drive-detail-${Date.now()}.png`;
    const driveResp = await request.post(`${baseURL}/api/drive/files/create`, {
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
    expect(driveResp.status()).toBe(200);
    const file = await driveResp.json();
    expect(file.id, 'drive/files/create should return file id').toBeTruthy();
    const actualName: string = file.name;
    expect(typeof actualName).toBe('string');

    await uiSigninAsRoot(page, baseURL, root);
    const resp = await page.goto(`${baseURL}/my/drive/file/${file.id}`, {
      waitUntil: 'domcontentloaded',
    });
    expect(resp!.status()).toBe(200);

    // info tab の <h2 :class="$style.fileName">{{ file.name }}</h2> が
    // hydrate して filename 文字列が body に出る。
    await page.waitForFunction(
      (n) => document.body.textContent?.includes(n) ?? false,
      actualName,
      { timeout: 20_000 },
    );

    // MIME type "image/png" も file info の MkKeyValue で render される。
    await page.waitForFunction(
      () => document.body.textContent?.includes('image/png') ?? false,
      { timeout: 20_000 },
    );
  });
});
