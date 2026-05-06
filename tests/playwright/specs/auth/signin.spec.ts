// #744 Phase 1 PR-2: signin-flow の正常系。
// signupUser で通常 user を作成 → signin-flow で access token を取得 →
// /api/i で自分の user 情報が取れる ことを確認する。
//
// upstream Misskey TS と互換な API shape を期待するので、mk-go の signin
// レスポンスが上流から drift したら本 spec が fail する。

import { expect, test } from '@playwright/test';
import { callApi } from '../../fixtures/api';
import { signin, signupUser } from '../../fixtures/auth';
import { resetRateLimit } from '../../fixtures/rate_limit';

test.describe('auth: signin-flow', () => {
  test.beforeAll(() => {
    // signup endpoint の 1h 5 回 rate limit (#744) を毎 spec 開始前に
    // ゼロから始める。複数 spec で連続して signup する設計を支える。
    resetRateLimit();
  });

  test('signin returns a working access token', async ({ request }) => {
    // signup で fresh user を作る。username は時間 uniq で他 spec / 並列実行と
    // 衝突しないようにする (`workers: 1` でも安全側)。
    const username = `signin_${Date.now()}`;
    const created = await signupUser(request, username, 'password1234');
    expect(created.id).toBeTruthy();
    expect(created.token).toBeTruthy();

    // 取得した user で signin-flow を叩いて access token を取得し、その
    // token で /api/i が hydrate できることを確認する。signup が返す
    // token と signin が返す token の同一性は upstream 仕様の動く部分
    // (= mk-go / TS で異なる可能性) なので assert しない。
    const token = await signin(request, username, 'password1234');
    expect(token).toBeTruthy();

    const me = await callApi(request, 'i', { i: token });
    expect(me.status()).toBe(200);
    const body = await me.json();
    expect(body.id).toBe(created.id);
    expect(body.username).toBe(username);
  });
});
