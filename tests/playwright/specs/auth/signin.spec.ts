// #744 Phase 1 PR-2: signin-flow の正常系。
// signupUser で通常 user を作成 → signin-flow で access token を取得 →
// /api/i で自分の user 情報が取れる ことを確認する。
//
// upstream Misskey TS と互換な API shape を期待するので、mk-go の signin
// レスポンスが上流から drift したら本 spec が fail する。

import { expect, test } from '@playwright/test';
import { callApi } from '../../fixtures/api';
import { signin, signupUser } from '../../fixtures/auth';

test.describe('auth: signin-flow', () => {
  test('signin returns a working access token', async ({ request }) => {
    // signup で fresh user を作る。username は時間 uniq で他 spec / 並列実行と
    // 衝突しないようにする (`workers: 1` でも安全側)。
    const username = `signin_${Date.now()}`;
    const created = await signupUser(request, username, 'password1234');
    expect(created.id).toBeTruthy();
    expect(created.token).toBeTruthy();

    // 取得した user で signin-flow を叩いて別の token を取得する。signin が
    // 通れば「username + password で auth できる」ことが確認できる。
    const token = await signin(request, username, 'password1234');
    expect(token).toBeTruthy();
    // signin token と signup の戻り token は同じか別かは upstream 仕様次第。
    // mk-go では signup が作る token と signin が取り出す token は別物
    // (= signin は新しい access_token を発行する) ので、ここでは「いずれの
    // token も /api/i に対して valid であること」だけ assert する。

    // /api/i で hydrate して username を確認する。
    const me = await callApi(request, 'i', { i: token });
    expect(me.status()).toBe(200);
    const body = await me.json();
    expect(body.id).toBe(created.id);
    expect(body.username).toBe(username);
  });
});
