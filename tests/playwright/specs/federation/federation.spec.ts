// Phase 3 #831: federation/* round-trip。
//
// Playwright stack は federation 設定なし (= local 単一 instance)。実 federation
// delivery / remote resolve は drop-in pytest が cover するので、本 spec は
// **API レベル shape** の drop-in 互換のみ確認する LCD strategy:
//   - federation/instances: list shape (= 配列)
//   - federation/show-instance: unknown host → 404
//   - federation/followers / following / users: unknown host → 空配列 shape
//   - federation/stats: 集計 4 field の shape (= 配列 + number)

import { expect, test } from '@playwright/test';
import { callApi } from '../../fixtures/api';
import { resetRateLimit } from '../../fixtures/rate_limit';

test.describe('federation/* shape compat', () => {
  test.beforeAll(() => {
    resetRateLimit();
  });

  test('federation/instances returns array shape', async ({ request }) => {
    // requireCredential: false で anonymous でも accept される。
    const resp = await callApi(request, 'federation/instances', {});
    expect(resp.status()).toBe(200);
    const body = await resp.json();
    expect(Array.isArray(body)).toBe(true);
  });

  test('federation/show-instance returns no-content for unknown host', async ({ request }) => {
    // upstream Misskey TS は 204 No Content (= 該当 instance 行なし)、mk-go は
    // 現状 404 + error body を返す drift がある (#915)。両者を LCD で許容し、
    // drift 解消後に 204 strict に絞る予定。
    const resp = await callApi(request, 'federation/show-instance', {
      host: 'no-such-host-spec.invalid',
    });
    expect([204, 404]).toContain(resp.status());
  });

  test('federation/followers returns empty array for unknown host', async ({ request }) => {
    const resp = await callApi(request, 'federation/followers', {
      host: 'no-such-host-spec.invalid',
    });
    expect(resp.status()).toBe(200);
    const body = await resp.json();
    expect(Array.isArray(body)).toBe(true);
    expect(body.length).toBe(0);
  });

  test('federation/following returns empty array for unknown host', async ({ request }) => {
    const resp = await callApi(request, 'federation/following', {
      host: 'no-such-host-spec.invalid',
    });
    expect(resp.status()).toBe(200);
    const body = await resp.json();
    expect(Array.isArray(body)).toBe(true);
    expect(body.length).toBe(0);
  });

  test('federation/users returns empty array for unknown host', async ({ request }) => {
    const resp = await callApi(request, 'federation/users', {
      host: 'no-such-host-spec.invalid',
    });
    expect(resp.status()).toBe(200);
    const body = await resp.json();
    expect(Array.isArray(body)).toBe(true);
    expect(body.length).toBe(0);
  });

  test('federation/stats returns aggregated counter shape', async ({ request }) => {
    const resp = await callApi(request, 'federation/stats', { limit: 5 });
    expect(resp.status()).toBe(200);
    const body = (await resp.json()) as Record<string, unknown>;
    // upstream Misskey TS / mk-go 共通の必須 field
    expect(Array.isArray(body.topSubInstances)).toBe(true);
    expect(Array.isArray(body.topPubInstances)).toBe(true);
    expect(typeof body.otherFollowersCount).toBe('number');
    expect(typeof body.otherFollowingCount).toBe('number');
  });
});
