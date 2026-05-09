// /admin/emojis page で admin/emoji/add 経由で登録した custom emoji が
// 一覧 render されることを verify する spec。
//
// upstream Misskey は admin の emoji 一覧を MkPagination + emoji 各 row
// で表示する。本 spec は emoji name (= alphanumeric なので body.textContent
// match で安定) を hydration 完了の signal にする。

import { readFileSync } from 'node:fs';
import { expect, test } from '@playwright/test';
import { callApi } from '../../fixtures/api';
import { type RootFixture, uiSigninAsRoot } from '../../fixtures/ui_auth';

test.describe('UI: /admin/emojis page renders admin-side emoji list', () => {
  let root: RootFixture;

  test.beforeAll(() => {
    root = JSON.parse(readFileSync('.auth/root.json', 'utf-8'));
  });

  test.setTimeout(60_000);

  test('admin/emoji/add registers an emoji and /admin/emojis renders its name', async ({ page, baseURL, request }) => {
    // drive に dummy png を upload (emoji image source)
    const driveResp = await request.post(`${baseURL}/api/drive/files/create`, {
      ignoreHTTPSErrors: true,
      multipart: {
        i: root.token,
        file: {
          name: 'pw-emoji.png',
          mimeType: 'image/png',
          buffer: Buffer.from(
            'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYAAAAAYAAjCB0C8AAAAASUVORK5CYII=',
            'base64',
          ),
        },
      },
    });
    expect(driveResp.status()).toBe(200);
    const driveFile = await driveResp.json();
    expect(driveFile.id).toBeTruthy();

    // 一意 emoji name (アルファベット小文字 + 数字)。timestamp 後方 9 桁。
    const emojiName = `pwemo${Date.now().toString().slice(-9)}`;
    const addResp = await callApi(request, 'admin/emoji/add', {
      i: root.token,
      name: emojiName,
      fileId: driveFile.id,
      category: null,
      aliases: [],
      license: null,
      isSensitive: false,
      localOnly: false,
      roleIdsThatCanBeUsedThisEmojiAsReaction: [],
    });
    expect(addResp.status()).toBe(200);

    await uiSigninAsRoot(page, baseURL, root);
    const resp = await page.goto(`${baseURL}/admin/emojis`, { waitUntil: 'domcontentloaded' });
    expect(resp!.status()).toBe(200);

    // emoji 一覧の hydration 完了 = emoji name が body に出る
    await page.waitForFunction(
      (n) => document.body.textContent?.includes(n) ?? false,
      emojiName,
      { timeout: 20_000 },
    );
  });
});
