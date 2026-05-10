// /admin/announcements で announcement folder expand → "Delete" button
// (danger style, ti-trash icon) click → confirm dialog OK →
// /api/admin/announcements/delete round-trip する write-flow spec。
//
// admin/announcements.vue:34 の del() は v-if=announcement.id != null で
// 全 saved announcement に表示される。click すると os.confirm warning →
// 承諾後 admin/announcements/delete を叩く (line 165)。
// archive / unarchive と違って delete は確認 dialog 経由なのでフロー長め。

import { readFileSync } from 'node:fs';
import { expect, test } from '@playwright/test';
import { callApi } from '../../fixtures/api';
import { type RootFixture, uiSigninAsRoot } from '../../fixtures/ui_auth';

test.describe('UI: /admin/announcements delete button flow', () => {
  let root: RootFixture;
  test.beforeAll(() => {
    root = JSON.parse(readFileSync('.auth/root.json', 'utf-8'));
  });
  test.setTimeout(60_000);

  test('create via API → expand folder → click Delete → confirm OK → admin/announcements/delete', async ({
    page,
    baseURL,
    request,
  }) => {
    // 1. announcement を create via API
    const title = `pwann-del-${Date.now().toString().slice(-9)}`;
    const text = `pwann-del-body-${Date.now().toString().slice(-9)}`;
    const createResp = await callApi(request, 'admin/announcements/create', {
      i: root.token,
      title,
      text,
    });
    expect(createResp.status()).toBe(200);
    const announcement = await createResp.json();
    const announcementId: string = announcement.id;
    expect(announcementId).toBeTruthy();

    // 2. /admin/announcements を開く
    await uiSigninAsRoot(page, baseURL, root);
    await page.goto(`${baseURL}/admin/announcements`, {
      waitUntil: 'domcontentloaded',
    });

    await page.waitForFunction(
      (t) => document.body.textContent?.includes(t) ?? false,
      title,
      { timeout: 20_000 },
    );

    // 3. 該当 folder を expand
    await page.evaluate((t) => {
      const headers = Array.from(
        document.querySelectorAll('[data-cy-folder-header]'),
      ) as HTMLButtonElement[];
      const target = headers.find((h) => (h.textContent ?? '').includes(t));
      target?.click();
    }, title);

    // 4. Delete button (= "Delete" text + ti-trash icon) が visible まで待つ
    await page.waitForFunction(
      () => {
        const btns = Array.from(document.querySelectorAll('button')) as HTMLButtonElement[];
        return btns.some(
          (b) =>
            (b.textContent ?? '').includes('Delete') &&
            b.querySelector('i.ti-trash') !== null,
        );
      },
      { timeout: 10_000 },
    );

    // Delete click → confirm dialog 出現
    await page.evaluate(() => {
      const btns = Array.from(document.querySelectorAll('button')) as HTMLButtonElement[];
      const target = btns.find(
        (b) =>
          (b.textContent ?? '').includes('Delete') &&
          b.querySelector('i.ti-trash') !== null,
      );
      target?.click();
    });

    // 5. confirm dialog OK click → API 呼出
    await page.waitForFunction(
      () => document.querySelector('[data-cy-modal-dialog-ok]') !== null,
      { timeout: 10_000 },
    );

    const deleteResp = page.waitForResponse(
      (r) =>
        r.url().includes('/api/admin/announcements/delete') && r.status() < 300,
      { timeout: 15_000 },
    );
    await page.evaluate(() => {
      const ok = document.querySelector(
        '[data-cy-modal-dialog-ok]',
      ) as HTMLButtonElement | null;
      ok?.click();
    });
    await deleteResp;

    // 6. API 経由で list から消失 verify
    const listResp = await callApi(request, 'admin/announcements/list', {
      i: root.token,
      limit: 50,
    });
    expect(listResp.status()).toBe(200);
    const list = await listResp.json();
    const found = list.find((a: { id: string }) => a.id === announcementId);
    expect(found).toBeUndefined();
  });
});
