// /admin/abuses で abuse report の "Resolve (accept)" button (ti-check) を
// click → /api/admin/resolve-abuse-user-report が round-trip する write-flow
// spec。
//
// MkAbuseReport.vue:20-22 で各 report に 3 つの resolve button (accept /
// reject / other) が並ぶ。accept は ti-check icon、reject は ti-x、other
// は ti-slash で識別可能。click すると os.apiWithDialog で
// admin/resolve-abuse-user-report を叩く (line 119)。
//
// setup: signupUser 2 名 (reporter + target) → reporter が target を
// report → root として /admin/abuses で resolve。

import { readFileSync } from 'node:fs';
import { expect, test } from '@playwright/test';
import { callApi } from '../../fixtures/api';
import { signupUser } from '../../fixtures/auth';
import { type RootFixture, uiSigninAsRoot } from '../../fixtures/ui_auth';

test.describe('UI: /admin/abuses report resolve flow', () => {
  let root: RootFixture;
  test.beforeAll(() => {
    root = JSON.parse(readFileSync('.auth/root.json', 'utf-8'));
  });
  test.setTimeout(90_000);

  test('signup reporter+target → report → /admin/abuses → Resolve (accept) → /api/admin/resolve-abuse-user-report', async ({
    page,
    baseURL,
    request,
  }) => {
    // 1. reporter / target user を signup
    const reporter = await signupUser(request, `pwrep${Date.now().toString().slice(-9)}`);
    const target = await signupUser(request, `pwtgt${Date.now().toString().slice(-9)}`);
    expect(reporter.id).toBeTruthy();
    expect(target.id).toBeTruthy();

    // 2. reporter が target を abuse report
    const comment = `pw-abuse-resolve-${Date.now()}`;
    const reportResp = await callApi(request, 'users/report-abuse', {
      i: reporter.token,
      userId: target.id,
      comment,
    });
    expect(reportResp.status()).toBeLessThan(400);

    // 3. /admin/abuses を root として開く
    await uiSigninAsRoot(page, baseURL, root);
    await page.goto(`${baseURL}/admin/abuses`, {
      waitUntil: 'domcontentloaded',
    });

    // report の comment が body に出るまで待つ (= list 反映)
    await page.waitForFunction(
      (c) => document.body.textContent?.includes(c) ?? false,
      comment,
      { timeout: 20_000 },
    );

    // 4. 該当 report の "Resolve (accept)" button (= ti-check icon を持つ
    // button) を click。複数 report があり得るので comment を含む section
    // 内の button を絞る (= MkAbuseReport の root 内に ti-check button)。
    await page.waitForFunction(
      (c) => {
        const els = Array.from(document.querySelectorAll('div')) as HTMLDivElement[];
        return els.some((el) => {
          if (!(el.textContent ?? '').includes(c)) return false;
          return el.querySelector('button i.ti-check') !== null;
        });
      },
      comment,
      { timeout: 15_000 },
    );

    const resolveResp = page.waitForResponse(
      (r) =>
        r.url().includes('/api/admin/resolve-abuse-user-report') &&
        r.status() < 300,
      { timeout: 15_000 },
    );
    await page.evaluate((c) => {
      const els = Array.from(document.querySelectorAll('div')) as HTMLDivElement[];
      const target = els.find((el) => {
        if (!(el.textContent ?? '').includes(c)) return false;
        return el.querySelector('button i.ti-check') !== null;
      });
      if (!target) return;
      const acceptBtn = Array.from(target.querySelectorAll('button')).find(
        (b) => b.querySelector('i.ti-check') !== null,
      ) as HTMLButtonElement | undefined;
      acceptBtn?.click();
    }, comment);
    await resolveResp;
  });
});
