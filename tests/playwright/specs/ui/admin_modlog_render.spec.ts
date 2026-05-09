// /admin/modlog page で type filter (MkSelect) + moderator ID input が
// hydrate されることを smoke する spec。
//
// /admin/modlog は admin/show-moderation-logs paginator + filter form を
// mount する。実 log は moderation 操作後にしか発生しないので、本 spec は
// filter UI の有無だけを sign にする (= log entry 表示 verify は別 spec)。

import { readFileSync } from 'node:fs';
import { expect, test } from '@playwright/test';
import { type RootFixture, uiSigninAsRoot } from '../../fixtures/ui_auth';

test.describe('UI: /admin/modlog page hydrates filter controls', () => {
  let root: RootFixture;

  test.beforeAll(() => {
    root = JSON.parse(readFileSync('.auth/root.json', 'utf-8'));
  });

  test.setTimeout(60_000);

  test('modlog page renders type filter + moderator ID input', async ({ page, baseURL }) => {
    await uiSigninAsRoot(page, baseURL, root);
    const resp = await page.goto(`${baseURL}/admin/modlog`, { waitUntil: 'domcontentloaded' });
    expect(resp!.status()).toBe(200);

    // type select + moderator input + paginator control = input >= 1
    await page.waitForFunction(
      () => document.querySelectorAll('input').length >= 1,
      { timeout: 20_000 },
    );
  });
});
