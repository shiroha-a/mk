// /my/follow-requests で list tab → Accept button click →
// /api/following/requests/accept round-trip する write-flow spec。
//
// follow-requests.vue:21 の accept button は ti-check icon + "Accept" text、
// list tab (= 自分宛の request) で表示される。本 spec は:
//   1. root を isLocked=true に設定
//   2. signupUser で requester を作る
//   3. requester → root の follow を作る (鍵だから request 化)
//   4. uiSigninAsRoot → /my/follow-requests に navigate
//   5. Accept click → /api/following/requests/accept
//   6. cleanup: root の isLocked を false に戻す
// で round-trip を verify する。

import { readFileSync } from 'node:fs';
import { expect, test } from '@playwright/test';
import { callApi } from '../../fixtures/api';
import { signupUser } from '../../fixtures/auth';
import { type RootFixture, uiSigninAsRoot } from '../../fixtures/ui_auth';

test.describe('UI: /my/follow-requests accept button flow', () => {
  let root: RootFixture;
  test.beforeAll(() => {
    root = JSON.parse(readFileSync('.auth/root.json', 'utf-8'));
  });
  test.setTimeout(90_000);

  test('lock root + signup requester + follow → Accept click → /api/following/requests/accept', async ({
    page,
    baseURL,
    request,
  }) => {
    // 1. root を locked にする (= follow request が来るようにする)
    await callApi(request, 'i/update', { i: root.token, isLocked: true });

    // 2. requester を signup
    const requester = await signupUser(request, `pwfra${Date.now().toString().slice(-9)}`);

    // 3. requester → root を follow (locked なので request 化)
    const followResp = await callApi(request, 'following/create', {
      i: requester.token,
      userId: root.id,
    });
    expect(followResp.status()).toBe(200);

    // 4. /my/follow-requests を root として開く
    await uiSigninAsRoot(page, baseURL, root);
    await page.goto(`${baseURL}/my/follow-requests`, {
      waitUntil: 'domcontentloaded',
    });

    // requester username が body に出るまで待つ (= list 反映)
    await page.waitForFunction(
      (u) => document.body.textContent?.includes(u) ?? false,
      requester.username,
      { timeout: 20_000 },
    );

    // 5. Accept button (= "Accept" text + ti-check icon) hydrate を待つ
    await page.waitForFunction(
      () => {
        const btns = Array.from(document.querySelectorAll('button')) as HTMLButtonElement[];
        return btns.some(
          (b) =>
            (b.textContent ?? '').includes('Accept') &&
            b.querySelector('i.ti-check') !== null,
        );
      },
      { timeout: 15_000 },
    );

    // 6. Accept click → following/requests/accept round-trip
    const acceptResp = page.waitForResponse(
      (r) =>
        r.url().includes('/api/following/requests/accept') && r.status() < 300,
      { timeout: 15_000 },
    );
    await page.evaluate(() => {
      const btns = Array.from(document.querySelectorAll('button')) as HTMLButtonElement[];
      const target = btns.find(
        (b) =>
          (b.textContent ?? '').includes('Accept') &&
          b.querySelector('i.ti-check') !== null,
      );
      target?.click();
    });
    await acceptResp;

    // 7. API 経由で関係性 verify: requester is now following root.
    // 注: mk-go の users/relation (handler_extra.go:34) は現状 stub で
    // `isFollowing: false` を hardcoded で返す drift があるため、
    // users/followers list で実 follow 関係を確認する path に変更。
    // `relationItem.followerId` で row を identify (handler.go:579-584)。
    // 別 PR で users/relation の実装を fix する予定 (drift tracking)。
    //
    // limit=100 は test isolation 前提 (= root の follower は本 spec 内で
    // signup する 1 user だけ)。他 spec が global state を残して root の
    // follower が 100 件を超えると flake する余地があるが、現状の test
    // 構成 (= signup 系 spec は self-create user で root を follow しない)
    // では問題ない。
    const followersResp = await callApi(request, 'users/followers', {
      i: root.token,
      userId: root.id,
      limit: 100,
    });
    expect(followersResp.status()).toBe(200);
    const followers = await followersResp.json();
    expect(Array.isArray(followers)).toBe(true);
    expect(
      followers.some(
        (f: { followerId?: string }) => f.followerId === requester.id,
      ),
    ).toBe(true);

    // 8. cleanup: root の isLocked を false に戻す (他 spec への副作用回避)
    await callApi(request, 'i/update', { i: root.token, isLocked: false });
  });
});
