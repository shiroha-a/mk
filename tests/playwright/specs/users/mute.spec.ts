// Phase 2 #820: mute CRUD round-trip。
//
// upstream Misskey TS と mk-go は両方とも:
//   - mute/create { userId } で mute 関係を作成 (204)
//   - mute/list で mute 中の user 一覧を取得 ({ id, muteeId, mutee? } 配列)
//   - mute/delete { userId } で mute を解除 (204)
//   - mute/list 再取得で対象 user が消える
//
// 本 spec は両 backend 共通で CRUD round-trip のみ cover する。timeline
// filter 連動 (= mute 後に対象 user の note が timeline から除外) は
// 両 backend で挙動が分散しており別 spec scope (本 PR では扱わない)。

import { expect, test } from '@playwright/test';
import { callApi } from '../../fixtures/api';
import { randomUsername, signupUser } from '../../fixtures/auth';
import { resetRateLimit } from '../../fixtures/rate_limit';

interface MuteEntry {
  id: string;
  muteeId: string;
}

test.describe('users: mute CRUD round-trip', () => {
  test.beforeAll(() => {
    resetRateLimit();
  });

  test('A mutes then unmutes B; mute/list reflects both transitions', async ({
    request,
  }) => {
    const me = await signupUser(request, randomUsername('mtA'));
    const target = await signupUser(request, randomUsername('mtB'));

    // create mute → 204。
    const createResp = await callApi(request, 'mute/create', {
      i: me.token,
      userId: target.id,
    });
    expect(createResp.status()).toBe(204);

    // list で対象 user を含むこと。
    const listAfterCreate = await callApi(request, 'mute/list', {
      i: me.token,
      limit: 100,
    });
    expect(listAfterCreate.status()).toBe(200);
    const listed = (await listAfterCreate.json()) as MuteEntry[];
    expect(listed.some((e) => e.muteeId === target.id)).toBe(true);

    // delete mute → 204。
    const deleteResp = await callApi(request, 'mute/delete', {
      i: me.token,
      userId: target.id,
    });
    expect(deleteResp.status()).toBe(204);

    // list から消えていること。
    const listAfterDelete = await callApi(request, 'mute/list', {
      i: me.token,
      limit: 100,
    });
    expect(listAfterDelete.status()).toBe(200);
    const listed2 = (await listAfterDelete.json()) as MuteEntry[];
    expect(listed2.some((e) => e.muteeId === target.id)).toBe(false);
  });
});
