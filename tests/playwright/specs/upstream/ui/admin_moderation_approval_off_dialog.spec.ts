/*
 * SPDX-FileCopyrightText: mk-go project
 * SPDX-License-Identifier: AGPL-3.0-only
 */

// /admin/moderation で「登録を承認制にする」を OFF にしたときの確認ダイアログを
// verify する write-flow spec (#2803)。
//
// **承認制を外すとゲートが 1 つも無くなる。** 承認制を入れる更新はアカウント作成を
// 開放する (#2565) ので、外す更新で何もしないと「招待制 → 承認制 ON → 承認制 OFF」
// の 3 操作で無警告の全開状態が残る。サーバー側は明示が無ければ閉じる側へ倒すが、
// 管理画面は**どちらにするかを聞いて明示的に送る**。
//
// ここは fork frontend の分岐を通す唯一のテスト。CI に frontend の unit test job は
// 無く (`make frontend-check` は vue-tsc のみ)、Playwright を通さないと
// `else if (enableRegistration.value)` を潰しても全部緑になる。
//
// **3 分岐すべてを通す。** 「閉じる」だけ見ていると、開けたままにする選択肢が
// 消えても (= サーバー側の既定に飲まれても) 気づけない。

import { readFileSync } from 'node:fs';
import { expect, test } from '@playwright/test';
import { callApi } from '../../../fixtures/api';
import { type RootFixture, uiSigninAsRoot } from '../../../fixtures/ui_auth';
import { clickButtonByText, clickSwitchByLabel } from '../../../fixtures/ui_click';

// moderation.vue の mk-go 独自 UI はハードコードされた日本語なので、
// playwright.config.ts が locale を en-US に固定していてもこの表記で引ける。
const APPROVAL_LABEL = '登録を承認制にする';
const CLOSE_BUTTON = 'アカウント作成も閉じる';
const KEEP_BUTTON = 'アカウント作成は開けたままにする';
const CANCEL_BUTTON = 'やめる';

test.describe('UI: /admin/moderation approvalRequiredForSignup OFF dialog', () => {
  let root: RootFixture;
  test.beforeAll(() => {
    root = JSON.parse(readFileSync('.auth/root.json', 'utf-8'));
  });
  test.setTimeout(60_000);

  // 承認制 ON + アカウント作成オープン = ダイアログが出る条件。
  test.beforeEach(async ({ request }) => {
    const resp = await callApi(request, 'admin/update-meta', {
      i: root.token,
      approvalRequiredForSignup: true,
      disableRegistration: false,
    });
    // 前提が崩れたまま本題に進まないよう、setup 自体の成否も見る。
    expect(resp.status()).toBeLessThan(300);
  });

  // **cleanup は必ず両方戻す。** approvalRequiredForSignup が残ると以降の signup
  // spec が APPROVAL_REQUIRED (403)、disableRegistration が残ると登録が閉じたまま
  // で全滅する (#2620 と同じ壊れ方)。
  //
  // **try/finally では守れない。** Playwright はテスト timeout でテスト関数を
  // 放棄して再開しないので finally が走らない (afterEach は走る)。この spec は
  // 承認制を ON にする唯一の spec なので、放棄されると後続が巻き添えになる。
  // 同居の admin_moderation_email_required_signup_toggle は setup が全部 false
  // なので try/finally のままで害が無く、そちらの形は真似できない。
  test.afterEach(async ({ request }) => {
    await callApi(request, 'admin/update-meta', {
      i: root.token,
      approvalRequiredForSignup: false,
      disableRegistration: false,
    });
  });

  const readMeta = async (request: Parameters<typeof callApi>[0]) => {
    const resp = await callApi(request, 'admin/meta', { i: root.token });
    expect(resp.status()).toBe(200);
    return resp.json();
  };

  for (const variant of [
    { button: CLOSE_BUTTON, wantDisableRegistration: true },
    { button: KEEP_BUTTON, wantDisableRegistration: false },
  ]) {
    test(`OFF → 「${variant.button}」 → /api/admin/update-meta`, async ({
      page,
      baseURL,
      request,
    }) => {
      await uiSigninAsRoot(page, baseURL, root);
      await page.goto(`${baseURL}/admin/moderation`, {
        waitUntil: 'domcontentloaded',
      });

      const updateResp = page.waitForResponse(
        (r) => r.url().includes('/api/admin/update-meta') && r.status() < 300,
        { timeout: 15_000 },
      );
      await clickSwitchByLabel(page, APPROVAL_LABEL);
      // ダイアログを出さずに送っていたら、ここで待っている応答が先に来て
      // ボタンの click が「見つからない」で落ちる。
      await clickButtonByText(page, variant.button);
      await updateResp;

      // **どちらの列がどう動いたかまで見る。** update-meta が返ってきたことしか
      // 見ないと、どのボタンを押しても緑になる (#2620 と同じ壊れ方)。
      const meta = await readMeta(request);
      expect(meta.approvalRequiredForSignup).toBe(false);
      expect(meta.disableRegistration).toBe(variant.wantDisableRegistration);
    });
  }

  test(`OFF → 「${CANCEL_BUTTON}」 → 何も送らない`, async ({ page, baseURL, request }) => {
    await uiSigninAsRoot(page, baseURL, root);
    await page.goto(`${baseURL}/admin/moderation`, {
      waitUntil: 'domcontentloaded',
    });

    const sent: string[] = [];
    page.on('request', (r) => {
      if (r.url().includes('/api/admin/update-meta')) sent.push(r.url());
    });

    await clickSwitchByLabel(page, APPROVAL_LABEL);
    await clickButtonByText(page, CANCEL_BUTTON);
    // ダイアログが閉じるのを待つ。閉じる前に meta を読むと、送っていても
    // まだ届いていないだけの状態を「送っていない」と読み違える。
    await page.waitForFunction(
      (t) =>
        !Array.from(document.querySelectorAll('button')).some(
          (b) => (b.textContent ?? '').trim() === t,
        ),
      CANCEL_BUTTON,
      { timeout: 15_000 },
    );

    expect(sent).toEqual([]);
    const meta = await readMeta(request);
    expect(meta.approvalRequiredForSignup).toBe(true);
    expect(meta.disableRegistration).toBe(false);
  });
});
