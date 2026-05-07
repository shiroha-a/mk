// Phase 2 #824 残: admin/emoji/{add, update, list, delete} の lifecycle
// round-trip。
//
// upstream Misskey TS と mk-go は両方とも:
//   - admin/emoji/add { name, fileId } で custom emoji 追加 (200 + entity)
//   - admin/emoji/update { id, name?, category?, aliases?, ... } で
//     name / category 等を更新 (204 No Content)
//   - admin/emoji/list { query?, category? } で filter 一覧
//   - admin/emoji/delete { id } で削除 (204)
//
// 本 spec は両 backend 共通で 1 emoji の lifecycle を round-trip:
//   1. globalSetup root を読み込み
//   2. drive/files/create で tinyPNG upload (= fileId 取得)
//   3. admin/emoji/add → 登録 emoji の id を取得
//   4. admin/emoji/list で uniq name 検索 → 含まれること
//   5. admin/emoji/update で category 設定 → 反映 (再度 list で category 一致)
//   6. admin/emoji/delete → 一覧から消える
//
// PR #867 (#824 PR-A) は add + reaction の round-trip にフォーカスしていた
// ので、本 spec は残 lifecycle (update / list / delete) を補完する scope。

import { readFileSync } from 'node:fs';
import { expect, test } from '@playwright/test';
import { callApi } from '../../fixtures/api';
import { tinyPNG } from '../../fixtures/files';
import { resetRateLimit } from '../../fixtures/rate_limit';

const baseURL = process.env.MK_BASE_URL ?? 'https://mkgo.local';

interface RootFixture {
  id: string;
  token: string;
  username: string;
}

interface AdminEmojiListEntry {
  id: string;
  name: string;
  category?: string | null;
}

test.describe('emoji: admin lifecycle (add / update / list / delete)', () => {
  test.beforeAll(() => {
    resetRateLimit();
  });

  test('admin adds, updates, lists, then deletes a custom emoji', async ({
    request,
  }) => {
    const root: RootFixture = JSON.parse(readFileSync('.auth/root.json', 'utf-8'));
    const emojiName = 'spec_lc_' + Math.random().toString(16).slice(2, 8);

    // upload tinyPNG → fileId 取得
    const uploadResp = await request.post(`${baseURL}/api/drive/files/create`, {
      multipart: {
        i: root.token,
        file: { name: emojiName + '.png', mimeType: 'image/png', buffer: tinyPNG },
      },
      failOnStatusCode: false,
    });
    expect(uploadResp.status()).toBe(200);
    const uploaded = (await uploadResp.json()) as { id: string };

    // add
    const addResp = await callApi(request, 'admin/emoji/add', {
      i: root.token,
      name: emojiName,
      fileId: uploaded.id,
    });
    expect(addResp.status()).toBe(200);
    const added = (await addResp.json()) as { id: string };
    expect(typeof added.id).toBe('string');

    // list で query 検索 → 追加 emoji が含まれること
    const list1 = await callApi(request, 'admin/emoji/list', {
      i: root.token,
      query: emojiName,
      limit: 10,
    });
    expect(list1.status()).toBe(200);
    const list1Body = (await list1.json()) as AdminEmojiListEntry[];
    const found1 = list1Body.find((e) => e.id === added.id);
    expect(found1).toBeDefined();
    expect(found1!.name).toBe(emojiName);

    // update で category を設定
    const newCategory = 'spec-cat-' + Math.random().toString(16).slice(2, 6);
    const updResp = await callApi(request, 'admin/emoji/update', {
      i: root.token,
      id: added.id,
      category: newCategory,
    });
    expect(updResp.status()).toBe(204);

    // 再度 list で category が反映されていること
    const list2 = await callApi(request, 'admin/emoji/list', {
      i: root.token,
      query: emojiName,
      limit: 10,
    });
    expect(list2.status()).toBe(200);
    const list2Body = (await list2.json()) as AdminEmojiListEntry[];
    const found2 = list2Body.find((e) => e.id === added.id);
    expect(found2).toBeDefined();
    expect(found2!.category).toBe(newCategory);

    // delete
    const delResp = await callApi(request, 'admin/emoji/delete', {
      i: root.token,
      id: added.id,
    });
    expect(delResp.status()).toBe(204);

    // 一覧から消えていること
    const list3 = await callApi(request, 'admin/emoji/list', {
      i: root.token,
      query: emojiName,
      limit: 10,
    });
    expect(list3.status()).toBe(200);
    const list3Body = (await list3.json()) as AdminEmojiListEntry[];
    expect(list3Body.some((e) => e.id === added.id)).toBe(false);
  });
});
