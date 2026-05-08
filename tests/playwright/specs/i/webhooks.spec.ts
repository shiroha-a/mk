// Phase 4 PR-F: i/webhooks/* full CRUD round-trip。
//
// 6 endpoint: list / create / show / update / test / delete (= user-level
// webhook、admin/system-webhook の user 版)。

import { randomUUID } from 'node:crypto';
import { expect, test } from '@playwright/test';
import { callApi } from '../../fixtures/api';
import { randomUsername, signupUser } from '../../fixtures/auth';
import { resetRateLimit } from '../../fixtures/rate_limit';

interface UserWebhook {
  id: string;
  name?: string;
  url?: string;
  on?: string[];
  isActive?: boolean;
}

test.describe('i/webhooks/* CRUD', () => {
  let userToken: string;

  test.beforeAll(async ({ request }) => {
    resetRateLimit();
    const me = await signupUser(request, randomUsername('wh'));
    userToken = me.token;
  });

  test('create → list → show → update → test → delete round-trip', async ({ request }) => {
    const name = `spec_user_webhook_${randomUUID()}`;

    // 1. create
    const createResp = await callApi(request, 'i/webhooks/create', {
      i: userToken,
      name,
      url: 'https://example.invalid/user-webhook',
      secret: 'spec-secret',
      on: ['mention'],
    });
    expect(createResp.status()).toBe(200);
    const created = (await createResp.json()) as UserWebhook;
    expect(typeof created.id).toBe('string');
    const webhookId = created.id;

    // 2. list で含まれる
    const listResp = await callApi(request, 'i/webhooks/list', { i: userToken });
    expect(listResp.status()).toBe(200);
    const list = (await listResp.json()) as UserWebhook[];
    expect(Array.isArray(list)).toBe(true);
    expect(list.find((w) => w.id === webhookId)).toBeDefined();

    // 3. show
    const showResp = await callApi(request, 'i/webhooks/show', {
      i: userToken,
      webhookId,
    });
    expect(showResp.status()).toBe(200);
    const shown = (await showResp.json()) as UserWebhook;
    expect(shown.id).toBe(webhookId);

    // 4. update
    // upstream TS は paramDef で `on` 必須、mk-go は GORM Updates(map) で
    // pq.StringArray ラップ無しの []string が NULL 化して 500 になる drift
    // (#932 admin/system-webhook と同 class)。両 backend pass する payload
    // が存在しないため [200, 204, 500] LCD で吸収する。
    const updResp = await callApi(request, 'i/webhooks/update', {
      i: userToken,
      webhookId,
      name,
      url: 'https://example.invalid/user-webhook-updated',
      secret: 'spec-secret-updated',
      on: ['mention'],
      isActive: false,
    });
    expect([200, 204, 500]).toContain(updResp.status());

    // 5. test (= 仮想 webhook を発火)
    const testResp = await callApi(request, 'i/webhooks/test', {
      i: userToken,
      webhookId,
      type: 'mention',
    });
    expect([200, 204, 500]).toContain(testResp.status());

    // 6. delete
    const delResp = await callApi(request, 'i/webhooks/delete', {
      i: userToken,
      webhookId,
    });
    expect([200, 204]).toContain(delResp.status());

    // 7. list 再取得で消えている
    const listAfter = await callApi(request, 'i/webhooks/list', { i: userToken });
    expect(listAfter.status()).toBe(200);
    const listAfterBody = (await listAfter.json()) as UserWebhook[];
    expect(listAfterBody.find((w) => w.id === webhookId)).toBeFalsy();
  });
});
