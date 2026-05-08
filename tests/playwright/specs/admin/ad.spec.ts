// Phase 4 PR-D: admin/ad/* CRUD round-trip。
//
// 4 endpoint: list / create / update / delete。
// 全 admin 権限要、root token を再利用する。

import { randomUUID } from 'node:crypto';
import { readFileSync } from 'node:fs';
import { expect, test } from '@playwright/test';
import { callApi } from '../../fixtures/api';
import { resetRateLimit } from '../../fixtures/rate_limit';

interface RootFixture {
  id: string;
  token: string;
  username: string;
}

interface Ad {
  id: string;
  url?: string;
  imageUrl?: string;
  memo?: string;
}

test.describe('admin/ad/* CRUD round-trip', () => {
  let root: RootFixture;

  test.beforeAll(() => {
    resetRateLimit();
    root = JSON.parse(readFileSync('.auth/root.json', 'utf-8'));
  });

  test('list (empty/seeded) → create → list で含まれる → update → delete round-trip', async ({
    request,
  }) => {
    const memo = `spec_ad_${randomUUID()}`;
    const expiresAt = Date.now() + 60 * 60 * 1000; // 1時間後

    // 1. create
    const createResp = await callApi(request, 'admin/ad/create', {
      i: root.token,
      url: 'https://example.invalid/ad',
      imageUrl: 'https://example.invalid/ad.png',
      place: 'square',
      priority: 'middle',
      ratio: 1,
      dayOfWeek: 0,
      memo,
      expiresAt,
      startsAt: Date.now(),
    });
    expect([200, 204]).toContain(createResp.status());

    // 2. list で memo が含まれる
    const listResp = await callApi(request, 'admin/ad/list', { i: root.token });
    expect(listResp.status()).toBe(200);
    const list = (await listResp.json()) as Ad[];
    expect(Array.isArray(list)).toBe(true);
    const found = list.find((a) => a.memo === memo);
    expect(found, 'created ad should appear in list').toBeDefined();
    const adId = found!.id;

    // 3. update で memo を変更
    const updResp = await callApi(request, 'admin/ad/update', {
      i: root.token,
      id: adId,
      url: 'https://example.invalid/ad-updated',
      imageUrl: 'https://example.invalid/ad-updated.png',
      place: 'square',
      priority: 'middle',
      ratio: 2,
      dayOfWeek: 0,
      memo: `${memo}_updated`,
      expiresAt,
      startsAt: Date.now(),
    });
    expect([200, 204]).toContain(updResp.status());

    // 4. delete
    const delResp = await callApi(request, 'admin/ad/delete', {
      i: root.token,
      id: adId,
    });
    expect([200, 204]).toContain(delResp.status());

    // 5. list 再取得で消えている
    const listAfter = await callApi(request, 'admin/ad/list', { i: root.token });
    expect(listAfter.status()).toBe(200);
    const listAfterBody = (await listAfter.json()) as Ad[];
    expect(listAfterBody.find((a) => a.id === adId)).toBeFalsy();
  });
});
