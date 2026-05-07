// Phase 3 #833: renote-mute CRUD round-trip。
//
// upstream Misskey TS と mk-go は両方とも:
//   - renote-mute/create { userId } で mutee を登録 (204)
//   - renote-mute/list で自分の mutee 一覧 (= UserDetailed embed)
//   - renote-mute/delete { userId } で削除 (204)
//
// 通常の mute (= 元 note ごと非表示) と区別するための実 timeline 検証は
// 別 spec で扱う想定。本 spec は CRUD round-trip + duplicate / self-mute
// の error path に集中する。

import { expect, test } from '@playwright/test';
import { callApi } from '../../fixtures/api';
import { randomUsername, signupUser } from '../../fixtures/auth';
import { resetRateLimit } from '../../fixtures/rate_limit';

interface RenoteMuteEntry {
  id: string;
  createdAt: string;
  muteeId: string;
}

test.describe('renote-mute: CRUD round-trip', () => {
  test.beforeAll(() => {
    resetRateLimit();
  });

  test('create / list / delete round-trip + error paths', async ({ request }) => {
    const A = await signupUser(request, randomUsername('rmA'));
    const B = await signupUser(request, randomUsername('rmB'));

    // create
    const createResp = await callApi(request, 'renote-mute/create', {
      i: A.token,
      userId: B.id,
    });
    expect([200, 204]).toContain(createResp.status());

    // list で B が含まれる
    const listResp = await callApi(request, 'renote-mute/list', { i: A.token });
    expect(listResp.status()).toBe(200);
    const list = (await listResp.json()) as RenoteMuteEntry[];
    expect(Array.isArray(list)).toBe(true);
    expect(list.find((m) => m.muteeId === B.id)).toBeTruthy();

    // 重複 create は 4xx (ALREADY_MUTING)
    const dupResp = await callApi(request, 'renote-mute/create', {
      i: A.token,
      userId: B.id,
    });
    expect(dupResp.status()).toBeGreaterThanOrEqual(400);
    expect(dupResp.status()).toBeLessThan(500);

    // self-mute は 4xx (MUTEE_IS_YOURSELF)
    const selfResp = await callApi(request, 'renote-mute/create', {
      i: A.token,
      userId: A.id,
    });
    expect(selfResp.status()).toBeGreaterThanOrEqual(400);
    expect(selfResp.status()).toBeLessThan(500);

    // delete
    const deleteResp = await callApi(request, 'renote-mute/delete', {
      i: A.token,
      userId: B.id,
    });
    expect([200, 204]).toContain(deleteResp.status());

    // list から消える
    const listAfter = await callApi(request, 'renote-mute/list', { i: A.token });
    expect(listAfter.status()).toBe(200);
    const listAfterBody = (await listAfter.json()) as RenoteMuteEntry[];
    expect(listAfterBody.find((m) => m.muteeId === B.id)).toBeFalsy();

    // 解除済の delete は 4xx (NOT_MUTING)
    const dupDelResp = await callApi(request, 'renote-mute/delete', {
      i: A.token,
      userId: B.id,
    });
    expect(dupDelResp.status()).toBeGreaterThanOrEqual(400);
    expect(dupDelResp.status()).toBeLessThan(500);
  });
});
