// /admin/abuses page で users/report-abuse 経由の abuse report が
// MkPagination + MkAbuseReport で render されることを verify する spec。
//
// 通報する relation を作る: B が A (root) を report する。/admin/abuses は
// admin/abuse-user-reports paginator で list を取得し、各 report を
// XAbuseReport (MkAbuseReport) で render する。XAbuseReport 内の
// reporter username が body に出るのを hydration sign にする。

import { readFileSync } from 'node:fs';
import { expect, test } from '@playwright/test';
import { callApi } from '../../fixtures/api';
import { DEFAULT_TEST_PASSWORD, signupUser } from '../../fixtures/auth';
import { resetRateLimit } from '../../fixtures/rate_limit';
import { type RootFixture, uiSigninAsRoot } from '../../fixtures/ui_auth';

test.describe('UI: /admin/abuses renders abuse reports', () => {
  let root: RootFixture;

  test.beforeAll(() => {
    resetRateLimit();
    root = JSON.parse(readFileSync('.auth/root.json', 'utf-8'));
  });

  test.setTimeout(60_000);

  test('a fresh abuse report appears in /admin/abuses', async ({ page, baseURL, request }) => {
    // reporter user を作る (= root を report するので別 user 必要)
    const reporterName = `rep${Date.now().toString().slice(-9)}`;
    const reporter = await signupUser(request, reporterName, DEFAULT_TEST_PASSWORD);

    // root を spam として report
    const reportComment = `pwabuse-${Date.now().toString().slice(-9)}`;
    const reportResp = await callApi(request, 'users/report-abuse', {
      i: reporter.token,
      userId: root.id,
      comment: reportComment,
    });
    expect(reportResp.status()).toBe(204);

    await uiSigninAsRoot(page, baseURL, root);
    const resp = await page.goto(`${baseURL}/admin/abuses`, { waitUntil: 'domcontentloaded' });
    expect(resp!.status()).toBe(200);

    // reporter username + comment 両方を verify (= XAbuseReport が
    // reporter info + comment を render する)
    await page.waitForFunction(
      (n) => document.body.textContent?.includes(n) ?? false,
      reporterName,
      { timeout: 20_000 },
    );
    await page.waitForFunction(
      (c) => document.body.textContent?.includes(c) ?? false,
      reportComment,
      { timeout: 20_000 },
    );
  });
});
