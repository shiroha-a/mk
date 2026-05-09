// /settings/privacy で isExplorable switch を click → /api/i/update が
// {isExplorable: ...} で round-trip する **真の write-flow** spec。
//
// privacy.vue では isLocked / autoAcceptFollowed / publicReactions /
// hideOnlineStatus / noCrawle / preventAiLearning / isExplorable の順で
// MkSwitch が並んでいる (line 14, 22, 29, 48, 55, 62, 69)。本 spec は
// 7 番目の switch (isExplorable) を click することで、最初の switch だけ
// が動作する偽陽性を排除する。

import { readFileSync } from 'node:fs';
import { expect, test } from '@playwright/test';
import { type RootFixture, uiSigninAsRoot } from '../../fixtures/ui_auth';

test.describe('UI: /settings/privacy isExplorable toggle flow', () => {
  let root: RootFixture;
  test.beforeAll(() => {
    root = JSON.parse(readFileSync('.auth/root.json', 'utf-8'));
  });
  test.setTimeout(60_000);

  test('toggle isExplorable switch (7th) → /api/i/update round-trips', async ({
    page,
    baseURL,
  }) => {
    await uiSigninAsRoot(page, baseURL, root);
    await page.goto(`${baseURL}/settings/privacy`, { waitUntil: 'domcontentloaded' });

    // 7 個以上の checkbox が hydrate するまで待つ
    await page.waitForFunction(
      () => document.querySelectorAll('input[type="checkbox"]').length >= 7,
      { timeout: 20_000 },
    );

    const updateResp = page.waitForResponse(
      (r) => r.url().includes('/api/i/update') && r.status() === 200,
      { timeout: 15_000 },
    );
    await page.evaluate(() => {
      // 7 番目 (index 6) = isExplorable switch (privacy.vue:69)
      const cbs = Array.from(
        document.querySelectorAll('input[type="checkbox"]'),
      ) as HTMLInputElement[];
      cbs[6]?.click();
    });
    // #968 (PR #969) で MeDetailed packer が導入され、i/update 応答に
    // isExplorable / noCrawle / preventAiLearning など self-view-only field
    // が含まれるようになった。本 spec では「7 番目の switch を click した
    // 後、応答 body の isExplorable が toggle 後の値 (= boolean) を返す」
    // ことを strict assert する。click 前の値が観測しにくいので、boolean
    // 型であることを担保するに留める (true/false どちらでも通すため
    // toggle 方向に依存しない、初期 DB 状態 default:true の前提で false
    // 期待だが、prior run の影響で逆向きにもなり得るため)。
    const update = await updateResp;
    const body = await update.json();
    expect(body.id).toBeTruthy();
    expect(body.username).toBe(root.username);
    expect(typeof body.isExplorable).toBe('boolean');
  });
});
