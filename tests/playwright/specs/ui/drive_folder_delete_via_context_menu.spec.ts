// /my/drive で folder を右クリック → context menu (popupMenu) → "Delete"
// item (ti-fw ti-trash) → /api/drive/folders/delete 直接 round-trip する
// write-flow spec。
//
// MkDrive.folder.vue:280 onContextmenu は popupMenu で 5+ items を出す。
// Delete item は ti-trash + danger で、deleteFolder() は confirm 無しで
// drive/folders/delete を直接叩く (line 251)。空 folder のみ削除可。
//
// Playwright で右クリックを模擬するには contextmenu event を dispatch
// する。Playwright の page.click は left button のみ、page.locator.click
// で button: 'right' option もあるが、構造ベース selector 中心の本 PR
// では native event dispatch で揃える。

import { readFileSync } from 'node:fs';
import { expect, test } from '@playwright/test';
import { callApi } from '../../fixtures/api';
import { type RootFixture, uiSigninAsRoot } from '../../fixtures/ui_auth';

test.describe('UI: /my/drive folder delete via context menu flow', () => {
  let root: RootFixture;
  test.beforeAll(() => {
    root = JSON.parse(readFileSync('.auth/root.json', 'utf-8'));
  });
  test.setTimeout(60_000);

  test('contextmenu folder → Delete → /api/drive/folders/delete', async ({
    page,
    baseURL,
    request,
  }) => {
    // 1. test 用 empty folder を create via API
    const folderName = `pw-folder-del-${Date.now()}`;
    const createResp = await callApi(request, 'drive/folders/create', {
      i: root.token,
      name: folderName,
    });
    expect(createResp.status()).toBe(200);
    const folderId = (await createResp.json()).id;
    expect(folderId).toBeTruthy();

    // 2. /my/drive を開く
    await uiSigninAsRoot(page, baseURL, root);
    await page.goto(`${baseURL}/my/drive`, {
      waitUntil: 'domcontentloaded',
    });

    // folder name が body に出るまで待つ
    await page.waitForFunction(
      (n) => document.body.textContent?.includes(n) ?? false,
      folderName,
      { timeout: 20_000 },
    );

    // 3. 該当 folder element を探して contextmenu event を dispatch。
    // MkDrive.folder.vue:11 で folder root に @contextmenu.stop bind。
    // folder name を含む element の中で contextmenu listener を持つ
    // root を探す方法は複雑なので、name text を含む最も内側の div を
    // 取り、上方に折りたたんで contextmenu を発火する。
    await page.evaluate((n) => {
      const els = Array.from(document.querySelectorAll('div, button, a')) as HTMLElement[];
      const target = els.find(
        (el) =>
          (el.textContent ?? '').trim() === n ||
          // textContent が完全一致しなければ name + 周辺 (例: アイコン子要素)
          ((el.textContent ?? '').includes(n) && el.children.length <= 3),
      );
      if (!target) return;
      // contextmenu event を target 自体と親 5 階層に dispatch
      // (Vue の listener は親が capture する設計のため)。
      let node: HTMLElement | null = target;
      for (let i = 0; i < 6 && node; i++) {
        const ev = new MouseEvent('contextmenu', {
          bubbles: true,
          cancelable: true,
          button: 2,
          buttons: 2,
          view: window,
        });
        node.dispatchEvent(ev);
        node = node.parentElement;
      }
    }, folderName);

    // 4. popup menu の "Delete" item (ti-fw ti-trash) を待って click
    await page.waitForFunction(
      () => {
        const btns = Array.from(document.querySelectorAll('button')) as HTMLButtonElement[];
        return btns.some((b) => b.querySelector('i.ti-fw.ti-trash') !== null);
      },
      { timeout: 15_000 },
    );

    const deleteResp = page.waitForResponse(
      (r) => r.url().includes('/api/drive/folders/delete') && r.status() < 300,
      { timeout: 15_000 },
    );
    await page.evaluate(() => {
      const btns = Array.from(document.querySelectorAll('button')) as HTMLButtonElement[];
      const target = btns.find((b) => b.querySelector('i.ti-fw.ti-trash') !== null);
      target?.click();
    });
    await deleteResp;

    // 5. API 経由で削除確認
    const showResp = await callApi(request, 'drive/folders/show', {
      i: root.token,
      folderId,
    });
    expect(showResp.status()).toBeGreaterThanOrEqual(400);
  });
});
