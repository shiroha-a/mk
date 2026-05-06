// #817 part1: 2FA (TOTP) signin path。
//
// upstream Misskey TS と mk-go は 2FA 有効ユーザの signin-flow で
// step 1 (= username + password) に対し \`next: 'totp'\` を返し、step 2 で
// \`token\` (TOTP 6 桁) を送って \`finished: true, i: <token>\` を返す
// (#705 で TS 互換化)。
//
// 本 spec は両 backend 共通で:
//   1. signup user → /api/i/2fa/register で TOTP secret 取得
//   2. otplib で TOTP code 生成 → /api/i/2fa/done で 2FA enable
//   3. /api/signin-flow (step 1: username + password) → next: 'totp' assert
//   4. otplib で TOTP code 再生成 → /api/signin-flow (step 2: token) →
//      finished: true, i: <token> assert
//
// を strict に検証する。WebAuthn (passkey) 経路は別 spec (#817 part2 で
// 個別 PR 予定、`@simplewebauthn/server` で credential を programmatically
// 生成する設計)。

import { expect, test } from '@playwright/test';
import { authenticator } from 'otplib';
import { callApi } from '../../fixtures/api';
import { randomUsername, signupUser } from '../../fixtures/auth';
import { resetRateLimit } from '../../fixtures/rate_limit';

test.describe('auth: 2FA (TOTP)', () => {
  test.beforeAll(() => {
    resetRateLimit();
  });

  test('signin-flow returns next:totp for 2FA-enabled user and finishes with TOTP token', async ({ request }) => {
    const username = randomUsername('tfa');
    const password = 'password1234';
    const me = await signupUser(request, username, password);

    // 2FA registration: secret 取得。`password` は self confirmation 用。
    const regResp = await callApi(request, 'i/2fa/register', { i: me.token, password });
    expect(regResp.status()).toBe(200);
    const regBody = await regResp.json();
    expect(typeof regBody.secret).toBe('string');
    expect(regBody.secret.length).toBeGreaterThan(0);

    // 2FA done: secret から TOTP code を生成して送信、2FA enable。
    // upstream / mk-go ともに 200 + { backupCodes: [...] } を返す。
    const enableToken = authenticator.generate(regBody.secret);
    const doneResp = await callApi(request, 'i/2fa/done', { i: me.token, token: enableToken });
    expect(doneResp.status()).toBe(200);
    const doneBody = await doneResp.json();
    expect(Array.isArray(doneBody.backupCodes)).toBe(true);
    expect(doneBody.backupCodes.length).toBeGreaterThan(0);

    // signin-flow step 1: username + password → next: 'totp'。
    const step1Resp = await callApi(request, 'signin-flow', { username, password });
    expect(step1Resp.status()).toBe(200);
    const step1Body = await step1Resp.json();
    expect(step1Body.finished).toBe(false);
    expect(step1Body.next).toBe('totp');

    // signin-flow step 2: TOTP token を送信 → finished: true。
    // upstream Misskey TS は signin rate limit に minInterval: 1000ms が
    // ある (SigninApiService.ts) ので、step 1 と step 2 を 1 秒以上
    // 空けて呼ぶ必要がある。1.1s 待機で margin。
    // generate() は 30s window 単位なので、step 1 と step 2 で同じ code が
    // 出ることが多い (= 1.1s 程度の delay では window 跨ぎは起きにくい)。
    await new Promise((resolve) => setTimeout(resolve, 1100));
    const signinToken = authenticator.generate(regBody.secret);
    const step2Resp = await callApi(request, 'signin-flow', { username, password, token: signinToken });
    expect(step2Resp.status()).toBe(200);
    const step2Body = await step2Resp.json();
    expect(step2Body.finished).toBe(true);
    expect(typeof step2Body.i).toBe('string');
    expect(step2Body.i.length).toBeGreaterThan(0);
  });
});
