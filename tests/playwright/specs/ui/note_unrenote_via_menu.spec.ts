// 自分の renote を /notes/:renoteId で開いて renoteTime button →
// renote menu の "Unrenote" item (ti-fw ti-trash, danger) → /api/notes/delete
// が renote note 自体を削除する形で round-trip する write-flow spec。
//
// MkNote.vue:638-651 の getUnrenote は ti-trash + i18n.ts.unrenote text。
// click すると notes/delete を renote note の id で叩く (line 644)。
// 下流で globalEvents.emit('noteDeleted')。confirm 無し最短 flow。
//
// setup: 自分の note を 1 つ作成 → API で renote を作成 (renoteId 付き
// notes/create) → /notes/:renoteId で renote 表示 → 該当 renote の
// time button click。

import { readFileSync } from 'node:fs';
import { expect, test } from '@playwright/test';
import { callApi } from '../../fixtures/api';
import { type RootFixture, uiSigninAsRoot } from '../../fixtures/ui_auth';

test.describe('UI: own note unrenote via menu flow', () => {
  let root: RootFixture;
  test.beforeAll(() => {
    root = JSON.parse(readFileSync('.auth/root.json', 'utf-8'));
  });
  test.setTimeout(60_000);

  test('open renote → renote time menu → Unrenote → /api/notes/delete', async ({
    page,
    baseURL,
    request,
  }) => {
    // 1. 元 note を create
    const noteText = `pw-orig-${Date.now()}`;
    const origResp = await callApi(request, 'notes/create', {
      i: root.token,
      text: noteText,
      visibility: 'home',
    });
    expect(origResp.status()).toBe(200);
    const origNoteId = (await origResp.json()).createdNote.id;
    expect(origNoteId).toBeTruthy();

    // 2. 自分で renote を create (= renoteId のみ、text なし)
    const renoteResp = await callApi(request, 'notes/create', {
      i: root.token,
      renoteId: origNoteId,
      visibility: 'home',
    });
    expect(renoteResp.status()).toBe(200);
    const renoteNoteId = (await renoteResp.json()).createdNote.id;
    expect(renoteNoteId).toBeTruthy();

    // 3. /notes/:renoteNoteId を開く (= renote 表示)
    await uiSigninAsRoot(page, baseURL, root);
    await page.goto(`${baseURL}/notes/${renoteNoteId}`, {
      waitUntil: 'domcontentloaded',
    });

    // 元 note の text が body に出るまで待つ (= renote header 経由で
    // renoted by + 元 note 本文が render される)
    await page.waitForFunction(
      (t) => document.body.textContent?.includes(t) ?? false,
      noteText,
      { timeout: 20_000 },
    );

    // 4. renoteTime button (= ti-repeat icon を持つ button、renote note
    // 自体の header にある) を click → showRenoteMenu。
    // MkNote.vue:28-30 で renote header section 内に <button ref="renoteTime">
    // + <i class="ti ti-repeat"> + 時間表示。footer の renote button (=
    // post composer の renote 起動) も ti-repeat だが、そちらは class が
    // footerButton 系。本 spec は renote 詳細ページで renote header の
    // button を取りたい。両方とも ti-fw 修飾無しなので、最初の ti-repeat
    // button (= header) を取る (= 通常 footer より先に DOM 配置)。
    await page.waitForFunction(
      () => {
        const btns = Array.from(document.querySelectorAll('button')) as HTMLButtonElement[];
        return btns.some(
          (b) =>
            b.querySelector('i.ti-repeat') !== null &&
            !b.querySelector('i.ti-fw'),
        );
      },
      { timeout: 15_000 },
    );
    await page.evaluate(() => {
      const btns = Array.from(document.querySelectorAll('button')) as HTMLButtonElement[];
      const target = btns.find(
        (b) =>
          b.querySelector('i.ti-repeat') !== null &&
          !b.querySelector('i.ti-fw'),
      );
      target?.click();
    });

    // 5. popup menu の "Unrenote" item (ti-fw ti-trash, danger) を click
    await page.waitForFunction(
      () => {
        const btns = Array.from(document.querySelectorAll('button')) as HTMLButtonElement[];
        return btns.some((b) => b.querySelector('i.ti-fw.ti-trash') !== null);
      },
      { timeout: 10_000 },
    );

    const deleteResp = page.waitForResponse(
      (r) => r.url().includes('/api/notes/delete') && r.status() < 300,
      { timeout: 15_000 },
    );
    await page.evaluate(() => {
      const btns = Array.from(document.querySelectorAll('button')) as HTMLButtonElement[];
      const target = btns.find((b) => b.querySelector('i.ti-fw.ti-trash') !== null);
      target?.click();
    });
    await deleteResp;

    // 6. API 経由で renote note が削除されたこと verify
    const showResp = await callApi(request, 'notes/show', {
      i: root.token,
      noteId: renoteNoteId,
    });
    expect(showResp.status()).toBeGreaterThanOrEqual(400);

    // cleanup: 元 note も削除
    await callApi(request, 'notes/delete', {
      i: root.token,
      noteId: origNoteId,
    });
  });
});
