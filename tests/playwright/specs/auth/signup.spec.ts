// #744 Phase 1 PR-2: signup-flow で複数 user を作成できることを確認する。
// admin/accounts/create と違って /api/signup は 2 人目以降も通る (= captcha /
// invitation などの制限がない default config を前提)。
//
// 本 spec は drop-in 互換の重要 path: mk-go が upstream Misskey TS と同じ
// signup endpoint を提供し、token / id を期待 shape で返すことを assert
// する。

import { expect, test } from '@playwright/test';
import { callApi } from '../../fixtures/api';
import { signupUser } from '../../fixtures/auth';

test.describe('auth: signup-flow', () => {
  test('multiple users can sign up and each gets a working token', async ({ request }) => {
    const stamp = Date.now();
    const usernames = [`user_a_${stamp}`, `user_b_${stamp}`];

    const principals = await Promise.all(
      usernames.map((u) => signupUser(request, u, 'password1234')),
    );

    // それぞれ別 id / 別 token を持つこと。
    expect(principals[0].id).not.toBe(principals[1].id);
    expect(principals[0].token).not.toBe(principals[1].token);

    // 各 token が /api/i で正しい user を返すこと。
    for (const p of principals) {
      const resp = await callApi(request, 'i', { i: p.token });
      expect(resp.status()).toBe(200);
      const body = await resp.json();
      expect(body.id).toBe(p.id);
      expect(body.username).toBe(p.username);
    }
  });

  // 注: duplicate username の test は signup endpoint を 2 連続で叩くため、
  // mk-go の signup rate limit (1h 5 回) と signin_invalid spec / signin
  // spec の signup と合わせて 6 回目で 429 になる。後続 PR で beforeEach
  // の Redis flush helper を整備してから duplicate spec を追加する。
});
