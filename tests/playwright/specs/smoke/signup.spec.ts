// #744 Phase 1 smoke: 最初の root signup が通り、取得した token で /api/i が
// 自分の user 情報を返すことを確認する。本 spec は upstream Misskey TS の
// API 規約を期待値とし、mk-go が同じ shape のレスポンスを返すか検証する。
//
// API 互換が崩れたら本 spec が fail する (= drop-in 互換 regression を
// 検出)。

import { expect, test } from '@playwright/test';
import { callApi } from '../../fixtures/api';
import { signupRoot } from '../../fixtures/auth';

test.describe('smoke: signup + i', () => {
  test('first signup creates a root user and /api/i returns it', async ({ request }) => {
    const me = await signupRoot(request, 'alice', 'password1234');
    expect(me.id).toBeTruthy();
    expect(me.token).toBeTruthy();
    expect(me.username).toBe('alice');

    // /api/i は token を body.i に乗せて取得。upstream Misskey TS と同 shape:
    // { id, username, isAdmin?, ... } を期待する。
    const resp = await callApi(request, 'i', { i: me.token });
    expect(resp.status()).toBe(200);
    const body = await resp.json();
    expect(body.id).toBe(me.id);
    expect(body.username).toBe('alice');
  });
});
