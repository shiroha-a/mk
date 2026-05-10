// /admin/announcements で archived announcement を expand → "Unarchive"
// button click → /api/admin/announcements/update (isActive=true) round-trip
// する write-flow spec。
//
// admin/announcements.vue:33 の unarchive button は v-if=isActive=false で
// 表示され、ti-restore icon + "Unarchive" text。click すると
// admin/announcements/update を isActive=true で叩く (line 184)。
// archive_button spec の inverse として用意。

import { readFileSync } from 'node:fs';
import { expect, test } from '@playwright/test';
import { callApi } from '../../fixtures/api';
import { type RootFixture, uiSigninAsRoot } from '../../fixtures/ui_auth';

test.describe('UI: /admin/announcements unarchive button flow', () => {
  let root: RootFixture;
  test.beforeAll(() => {
    root = JSON.parse(readFileSync('.auth/root.json', 'utf-8'));
  });
  test.setTimeout(60_000);

  test('create + archive via API → expand folder → click Unarchive → admin/announcements/update', async ({
    page,
    baseURL,
    request,
  }) => {
    // 1. announcement を create + archive (isActive=false) via API
    const title = `pwann-unarch-${Date.now().toString().slice(-9)}`;
    const text = `pwann-unarch-body-${Date.now().toString().slice(-9)}`;
    const createResp = await callApi(request, 'admin/announcements/create', {
      i: root.token,
      title,
      text,
    });
    expect(createResp.status()).toBe(200);
    const announcement = await createResp.json();
    const announcementId: string = announcement.id;
    expect(announcementId).toBeTruthy();

    // archive (= isActive=false に切替) via API
    const archiveResp = await callApi(request, 'admin/announcements/update', {
      i: root.token,
      id: announcementId,
      title,
      text,
      isActive: false,
      icon: announcement.icon ?? 'info',
      display: announcement.display ?? 'normal',
      forExistingUsers: announcement.forExistingUsers ?? false,
      silence: announcement.silence ?? false,
      needConfirmationToRead: announcement.needConfirmationToRead ?? false,
      imageUrl: announcement.imageUrl ?? null,
    });
    expect(archiveResp.status()).toBeLessThan(400);

    // 2. /admin/announcements を開いて archived 込みの list を読む
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

    // Unarchive button が visible になるまで待つ
    await page.waitForFunction(
      () => {
        const btns = Array.from(document.querySelectorAll('button'));
        return btns.some((b) => (b.textContent ?? '').includes('Unarchive'));
      },
      { timeout: 10_000 },
    );

    // 4. Unarchive click → admin/announcements/update (isActive=true) round-trip
    const updateResp = page.waitForResponse(
      (r) =>
        r.url().includes('/api/admin/announcements/update') && r.status() < 400,
      { timeout: 15_000 },
    );
    await page.evaluate(() => {
      const btn = Array.from(document.querySelectorAll('button')).find((b) =>
        (b.textContent ?? '').includes('Unarchive'),
      ) as HTMLButtonElement | undefined;
      btn?.click();
    });
    const resp = await updateResp;
    expect(resp.status()).toBeLessThan(400);

    // 5. API 経由で isActive=true verify
    const listResp = await callApi(request, 'admin/announcements/list', {
      i: root.token,
      limit: 50,
    });
    expect(listResp.status()).toBe(200);
    const list = await listResp.json();
    const found = list.find((a: { id: string }) => a.id === announcementId);
    expect(found).toBeTruthy();
    expect(found.isActive).toBe(true);
  });
});
