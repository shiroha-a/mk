// Phase 4 PR-B: hashtags/* shape spec。
//
// 5 endpoint すべて anonymous (= requireCredential: false)、shape verify のみ:
//   - hashtags/list / search / trend: 配列
//   - hashtags/show: 非存在 tag → 4xx or null (LCD)
//   - hashtags/users: 非存在 tag → 空配列
//
// 本 spec では papering over した drift が #925 として記録されている:
// upstream は paramDef で sort 必須、tag 長さ validate を行うが mk-go は
// permissive。両 backend が動く params (= sort 込み / 短い tag) を渡すこと
// で spec 自体は両環境で pass するが、drop-in 互換性は厳密には完全ではない。

import { randomUUID } from 'node:crypto';
import { expect, test } from '@playwright/test';
import { callApi } from '../../fixtures/api';
import { resetRateLimit } from '../../fixtures/rate_limit';

test.describe('hashtags/* shape compat', () => {
  test.beforeAll(() => {
    resetRateLimit();
  });

  // upstream Misskey TS の paramDef は厳格で、hashtags/list は sort 必須、
  // hashtags/users は tag + sort 必須、hashtags/show / users の tag は
  // alphanumeric のみ accept する。両 backend で動く params で固定する。
  const cleanTag = `spec${randomUUID().replace(/-/g, '')}`;

  test('hashtags/list returns array shape', async ({ request }) => {
    const resp = await callApi(request, 'hashtags/list', {
      limit: 5,
      sort: '+mentionedUsers',
    });
    expect(resp.status()).toBe(200);
    expect(Array.isArray(await resp.json())).toBe(true);
  });

  test('hashtags/search returns array shape', async ({ request }) => {
    const resp = await callApi(request, 'hashtags/search', {
      query: 'spec',
      limit: 5,
    });
    expect(resp.status()).toBe(200);
    expect(Array.isArray(await resp.json())).toBe(true);
  });

  test('hashtags/trend returns array shape', async ({ request }) => {
    const resp = await callApi(request, 'hashtags/trend', {});
    expect(resp.status()).toBe(200);
    expect(Array.isArray(await resp.json())).toBe(true);
  });

  test('hashtags/users returns empty array for unknown tag', async ({ request }) => {
    const resp = await callApi(request, 'hashtags/users', {
      tag: cleanTag,
      sort: '+follower',
      limit: 5,
    });
    expect(resp.status()).toBe(200);
    expect(await resp.json()).toEqual([]);
  });

  test('hashtags/show returns negative for unknown tag', async ({ request }) => {
    // 実観測した backend 別 status:
    //   - upstream Misskey TS: 400 (= paramDef tag length/format validation)
    //   - mk-go: 200 (= 空集計 result) または 404 NO_SUCH_HASHTAG
    // どちらも frontend 側で「not found」扱い。drift 詳細は #925。
    const resp = await callApi(request, 'hashtags/show', { tag: cleanTag });
    expect([200, 204, 400, 404]).toContain(resp.status());
  });
});
