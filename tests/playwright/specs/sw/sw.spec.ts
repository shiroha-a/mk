// Phase 3 #832: sw/* (service worker push subscription) round-trip。
//
// upstream Misskey TS と mk-go は両方とも:
//   - sw/register: { endpoint, auth, publickey, sendReadMessage } で
//     subscription を作成 → { state: 'subscribed', userId, endpoint, ... }
//   - sw/show-registration: { endpoint } で取得 → 該当 row なしなら null
//   - sw/update-registration: sendReadMessage を切替
//   - sw/unregister: 削除 (auth 不要)
//
// 実 push 配信は browser-side / push server 必要なので scope 外。本 spec は
// CRUD round-trip の shape 整合のみ verify する。

import { expect, test } from '@playwright/test';
import { callApi } from '../../fixtures/api';
import { randomUsername, signupUser } from '../../fixtures/auth';
import { resetRateLimit } from '../../fixtures/rate_limit';

test.describe('sw/* push subscription round-trip', () => {
  test.beforeAll(() => {
    resetRateLimit();
  });

  test('register / show / update / unregister round-trip', async ({ request }) => {
    const me = await signupUser(request, randomUsername('sw'));
    // dummy push endpoint + key (= 実 VAPID 検証は backend で行わないので、
    // 構造化された non-empty string を入れれば shape 互換 verify には十分)。
    const endpoint = `https://push.example.test/${Math.random().toString(16).slice(2, 10)}`;
    const auth = 'spec-auth-key';
    const publickey = 'spec-publickey-base64url';

    // 1. register
    const regResp = await callApi(request, 'sw/register', {
      i: me.token,
      endpoint,
      auth,
      publickey,
      sendReadMessage: false,
    });
    expect(regResp.status()).toBe(200);
    const regBody = (await regResp.json()) as {
      state?: string;
      userId?: string;
      endpoint?: string;
      sendReadMessage?: boolean;
    };
    expect(regBody.state).toBe('subscribed');
    expect(regBody.userId).toBe(me.id);
    expect(regBody.endpoint).toBe(endpoint);
    expect(regBody.sendReadMessage).toBe(false);

    // 2. show-registration: 同 endpoint で取得
    const showResp = await callApi(request, 'sw/show-registration', {
      i: me.token,
      endpoint,
    });
    expect(showResp.status()).toBe(200);
    const showBody = (await showResp.json()) as {
      userId?: string;
      endpoint?: string;
      sendReadMessage?: boolean;
    };
    expect(showBody.userId).toBe(me.id);
    expect(showBody.endpoint).toBe(endpoint);
    expect(showBody.sendReadMessage).toBe(false);

    // 3. update-registration: sendReadMessage を true に
    const updResp = await callApi(request, 'sw/update-registration', {
      i: me.token,
      endpoint,
      sendReadMessage: true,
    });
    expect(updResp.status()).toBe(200);
    const updBody = (await updResp.json()) as { sendReadMessage?: boolean };
    expect(updBody.sendReadMessage).toBe(true);

    // 4. show 再取得で update 反映確認
    const showAfter = await callApi(request, 'sw/show-registration', {
      i: me.token,
      endpoint,
    });
    expect(showAfter.status()).toBe(200);
    const showAfterBody = (await showAfter.json()) as { sendReadMessage?: boolean };
    expect(showAfterBody.sendReadMessage).toBe(true);

    // 5. unregister: auth 不要 (= browser 側 deactivate でも叩ける)
    const unregResp = await callApi(request, 'sw/unregister', {
      endpoint,
    });
    expect([200, 204]).toContain(unregResp.status());

    // 6. unregister 後の show は「該当 row なし」を意味する empty response。
    // upstream Misskey TS は null return を 204 No Content に変換、mk-go は
    // 200 + JSON null を返す drift がある (#918)。両者を LCD で許容、drift
    // 解消後に 204 strict に絞る予定。
    const showFinal = await callApi(request, 'sw/show-registration', {
      i: me.token,
      endpoint,
    });
    expect([200, 204]).toContain(showFinal.status());
    if (showFinal.status() === 200) {
      expect(await showFinal.json()).toBeNull();
    }
  });

  test('register same endpoint twice returns already-subscribed', async ({ request }) => {
    const me = await signupUser(request, randomUsername('sw2'));
    const endpoint = `https://push.example.test/${Math.random().toString(16).slice(2, 10)}`;
    const params = {
      i: me.token,
      endpoint,
      auth: 'spec-auth-key',
      publickey: 'spec-publickey-base64url',
      sendReadMessage: false,
    };

    // 初回 register
    const first = await callApi(request, 'sw/register', params);
    expect(first.status()).toBe(200);
    expect(((await first.json()) as { state?: string }).state).toBe('subscribed');

    // 同 endpoint で再 register → already-subscribed
    const second = await callApi(request, 'sw/register', params);
    expect(second.status()).toBe(200);
    expect(((await second.json()) as { state?: string }).state).toBe('already-subscribed');
  });
});
