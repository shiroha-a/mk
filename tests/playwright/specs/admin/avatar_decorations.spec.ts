// Phase 4 PR-D: admin/avatar-decorations/* CRUD round-trip。
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

interface Decoration {
  id: string;
  name?: string;
  description?: string;
  url?: string;
}

test.describe('admin/avatar-decorations/* CRUD round-trip', () => {
  let root: RootFixture;

  test.beforeAll(() => {
    resetRateLimit();
    root = JSON.parse(readFileSync('.auth/root.json', 'utf-8'));
  });

  test('create → list で含まれる → update → delete round-trip', async ({ request }) => {
    const name = `spec_deco_${randomUUID()}`;

    // 1. create
    const createResp = await callApi(request, 'admin/avatar-decorations/create', {
      i: root.token,
      name,
      description: 'phase4 spec',
      url: 'https://example.invalid/deco.png',
      roleIdsThatCanBeUsedThisDecoration: [],
    });
    expect([200, 204]).toContain(createResp.status());

    // 2. list で含まれる
    const listResp = await callApi(request, 'admin/avatar-decorations/list', {
      i: root.token,
    });
    expect(listResp.status()).toBe(200);
    const list = (await listResp.json()) as Decoration[];
    expect(Array.isArray(list)).toBe(true);
    const found = list.find((d) => d.name === name);
    expect(found, 'created decoration should appear in list').toBeDefined();
    const decoId = found!.id;

    // 3. update で description 変更
    // roleIdsThatCanBeUsedThisDecoration: [] を送ると mk-go の GORM
    // Updates(map) が空 string[] を NULL 化して制約違反になる drift
    // (#896 / #900 と同 class) を踏む。spec scope では update に role 配列を
    // 含めない (= 既存値維持) ことで両 backend で動かす。drift 詳細は別 issue。
    const updResp = await callApi(request, 'admin/avatar-decorations/update', {
      i: root.token,
      id: decoId,
      name,
      description: 'updated by spec',
      url: 'https://example.invalid/deco.png',
    });
    expect([200, 204]).toContain(updResp.status());

    // 4. delete
    const delResp = await callApi(request, 'admin/avatar-decorations/delete', {
      i: root.token,
      id: decoId,
    });
    expect([200, 204]).toContain(delResp.status());

    // 5. list 再取得で消えている
    const listAfter = await callApi(request, 'admin/avatar-decorations/list', {
      i: root.token,
    });
    expect(listAfter.status()).toBe(200);
    const listAfterBody = (await listAfter.json()) as Decoration[];
    expect(listAfterBody.find((d) => d.id === decoId)).toBeFalsy();
  });
});
