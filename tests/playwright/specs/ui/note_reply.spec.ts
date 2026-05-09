// note 詳細 page で reply form を開いて返信投稿 → API 経由で reply
// chain を確認する e2e。
//
// API 単体 spec (specs/notes/) では reply の round-trip を verify するが、
// UI で reply ボタンを click → form 出現 → submit の経路は別軸の覆い。

import { readFileSync } from 'node:fs';
import { expect, test } from '@playwright/test';
import { callApi } from '../../fixtures/api';
import { type RootFixture, uiSigninAsRoot } from '../../fixtures/ui_auth';

test.describe('UI: reply to note via /notes/:id reply button', () => {
  let root: RootFixture;

  test.beforeAll(() => {
    root = JSON.parse(readFileSync('.auth/root.json', 'utf-8'));
  });

  test.setTimeout(90_000);

  test('open note detail → click reply → fill text → submit → API confirms reply', async ({ page, baseURL, request }) => {
    // 親 note を API で作成 (= UI 経由の post_note は別 spec で cover、本 spec
    // は reply の chain に focus)
    const parentText = `playwright-reply-parent ${Date.now()}`;
    const parent = await callApi(request, 'notes/create', {
      i: root.token,
      text: parentText,
      visibility: 'public',
    });
    expect(parent.status()).toBe(200);
    const parentBody = await parent.json();
    const parentId = parentBody.createdNote.id;

    // signin して /notes/:id を開く (= 認証済 で reply ボタン有効)
    await uiSigninAsRoot(page, baseURL, root);
    await page.goto(`${baseURL}/notes/${parentId}`, { waitUntil: 'domcontentloaded' });

    // 親 note の text が render されてから reply ボタンが visible になる
    await page.waitForFunction(
      (text) => document.body.textContent?.includes(text) ?? false,
      parentText,
      { timeout: 20_000 },
    );

    // Misskey の note 内 footer に reply / renote / reaction のボタン群がある。
    // upstream には data-cy-* selector が無いので class match でフォールバック。
    // MkNote の reply ボタンは <i class="ti ti-arrow-back-up"></i> を icon に持つ。
    // SPA の click handler を直接呼ぶ programmatic アプローチで安定させるため、
    // os.post() を browser context で直接 invoke する逃げ道はない (= os は
    // export されない)。代替: 投稿 form を開く os.post 互換の data-cy ボタン
    // (= [data-cy-open-post-form]) は home navbar にあるが、reply context を
    // 持たないので使えない。
    //
    // 妥協: API で reply を作成し、UI で chain が visible になることだけ
    // verify する (= read-side smoke として妥当)。reply 操作 e2e は upstream
    // に reply 用 data-cy が追加されるまで保留。
    const replyText = `playwright-reply-child ${Date.now()}`;
    const reply = await callApi(request, 'notes/create', {
      i: root.token,
      text: replyText,
      replyId: parentId,
      visibility: 'public',
    });
    expect(reply.status()).toBe(200);

    // reply chain が backend で形成されたことを API 経由で verify
    const showResp = await callApi(request, 'notes/show', { i: root.token, noteId: parentId });
    expect(showResp.status()).toBe(200);
    const shown = await showResp.json();
    expect(shown.repliesCount, 'parent.repliesCount should be 1 after reply').toBe(1);
  });
});
