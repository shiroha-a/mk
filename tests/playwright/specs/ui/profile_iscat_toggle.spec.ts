// /settings/profile の "advancedSettings" MkFolder を expand → isCat
// switch を toggle → /api/i/update が走る write-flow spec。
//
// profile.vue の isCat / isBot は collapsed な MkFolder の中にいるため、
// folder header を click して expand しないと switch が DOM に出ない。
// expand 後の最初の checkbox が isCat、その次が isBot。
//
// page には deep watcher (= profile object 全体) が居り、isCat の v-model
// 変化で `save()` が呼ばれて os.apiWithDialog('i/update', { isCat,... })
// が走る (profile.vue:207-211)。

import { readFileSync } from 'node:fs';
import { expect, test } from '@playwright/test';
import { type RootFixture, uiSigninAsRoot } from '../../fixtures/ui_auth';

test.describe('UI: /settings/profile isCat toggle flow', () => {
  let root: RootFixture;
  test.beforeAll(() => {
    root = JSON.parse(readFileSync('.auth/root.json', 'utf-8'));
  });
  test.setTimeout(60_000);

  test('expand advancedSettings folder → toggle isCat → /api/i/update', async ({
    page,
    baseURL,
  }) => {
    await uiSigninAsRoot(page, baseURL, root);
    await page.goto(`${baseURL}/settings/profile`, {
      waitUntil: 'domcontentloaded',
    });

    // 最初の MkInput (= name) が hydrate するまで待つ
    await page.waitForFunction(
      () => document.querySelectorAll('input').length >= 2,
      { timeout: 20_000 },
    );

    // 折りたたみ folder ("advancedSettings") を expand。folder 内には
    // isCat / isBot の 2 個の MkSwitch (= input[type=checkbox]) があり、
    // 折りたたみ状態では DOM に出ない (Vue の v-if 相当)。
    // 「click 前 checkbox 数」を測ってから folder を click し、checkbox
    // 数が 2 増えたことで expand 成功を verify する。
    const beforeCheckboxes = await page.evaluate(
      () => document.querySelectorAll('input[type="checkbox"]').length,
    );

    // settings/profile の MkFolder header は唯一: advancedSettings。
    // [data-cy-folder-header] で取れる (MkFolder の標準 attribute)。
    await page.evaluate(() => {
      const headers = Array.from(
        document.querySelectorAll('[data-cy-folder-header]'),
      ) as HTMLElement[];
      headers[0]?.click();
    });
    await page.waitForFunction(
      (n) => document.querySelectorAll('input[type="checkbox"]').length > n,
      beforeCheckboxes,
      { timeout: 10_000 },
    );

    // folder 内最初の switch (= isCat) を click → i/update 走る
    const updateResp = page.waitForResponse(
      (r) => r.url().includes('/api/i/update') && r.status() < 300,
      { timeout: 15_000 },
    );
    await page.evaluate((before) => {
      const cbs = Array.from(
        document.querySelectorAll('input[type="checkbox"]'),
      ) as HTMLInputElement[];
      // expand 前に居なかった checkbox 群の先頭が isCat
      cbs[before]?.click();
    }, beforeCheckboxes);
    const update = await updateResp;
    const body = await update.json();
    // i/update は MeDetailed shape を返す (#969 で fix 済) 前提の strict
    // assert: isCat field が boolean で含まれる。クリック前 false 期待
    // だが prior run 累積で逆向きにもなり得るので boolean 型のみ確認。
    expect(body.id).toBeTruthy();
    expect(typeof body.isCat).toBe('boolean');
  });
});
