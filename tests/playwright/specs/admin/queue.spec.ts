// Phase 4 PR-C: admin/queue/* spec。
//
// 11 endpoint:
//   read 系 (smoke shape):
//     - queues / queue-stats / stats: queue 集計の object/array
//     - jobs / deliver-delayed / inbox-delayed: 配列
//     - show-job-logs: 配列 (= asynq では履歴を保持しないので空)
//     - show-job: 不明 id で 4xx
//   mutation:
//     - clear → 204 (= 全 queue の pending を消す、test 環境で empty なので no-op)
//     - promote-jobs → 200 + { promoted: 0 } (= scheduled/retry を即実行、空なので 0)
//     - retry-job / remove-job: 不明 id で 4xx (= 既存 job 不要)
//
// 全 endpoint admin/moderator 権限要、root token を再利用する。

import { readFileSync } from 'node:fs';
import { expect, test } from '@playwright/test';
import { callApi } from '../../fixtures/api';
import { resetRateLimit } from '../../fixtures/rate_limit';

interface RootFixture {
  id: string;
  token: string;
  username: string;
}

test.describe('admin/queue/* shape compat', () => {
  let root: RootFixture;

  test.beforeAll(() => {
    resetRateLimit();
    root = JSON.parse(readFileSync('.auth/root.json', 'utf-8'));
  });

  // upstream Misskey TS は paramDef で queue + state 必須、mk-go は permissive。
  // 両 backend で動く params (= queue: 'deliver', state: ['active'] 等) を
  // 渡して shape compat を verify する LCD strategy。
  test('admin/queue/jobs returns array shape', async ({ request }) => {
    const resp = await callApi(request, 'admin/queue/jobs', {
      i: root.token,
      queue: 'deliver',
      state: ['active'],
      limit: 5,
    });
    expect(resp.status()).toBe(200);
    expect(Array.isArray(await resp.json())).toBe(true);
  });

  test('admin/queue/deliver-delayed returns array shape', async ({ request }) => {
    const resp = await callApi(request, 'admin/queue/deliver-delayed', {
      i: root.token,
      limit: 5,
    });
    expect(resp.status()).toBe(200);
    expect(Array.isArray(await resp.json())).toBe(true);
  });

  test('admin/queue/inbox-delayed returns array shape', async ({ request }) => {
    const resp = await callApi(request, 'admin/queue/inbox-delayed', {
      i: root.token,
      limit: 5,
    });
    expect(resp.status()).toBe(200);
    expect(Array.isArray(await resp.json())).toBe(true);
  });

  test('admin/queue/show-job-logs returns array shape', async ({ request }) => {
    const resp = await callApi(request, 'admin/queue/show-job-logs', {
      i: root.token,
      queue: 'deliver',
      jobId: 'spec-bogus-id',
    });
    expect(resp.status()).toBe(200);
    expect(Array.isArray(await resp.json())).toBe(true);
  });

  test('admin/queue/queues returns array shape', async ({ request }) => {
    const resp = await callApi(request, 'admin/queue/queues', { i: root.token });
    expect(resp.status()).toBe(200);
    expect(Array.isArray(await resp.json())).toBe(true);
  });

  test('admin/queue/queue-stats returns object shape', async ({ request }) => {
    const resp = await callApi(request, 'admin/queue/queue-stats', {
      i: root.token,
      queue: 'deliver',
    });
    expect(resp.status()).toBe(200);
    const body = await resp.json();
    expect(typeof body).toBe('object');
  });

  test('admin/queue/stats returns object with deliver/inbox keys', async ({ request }) => {
    const resp = await callApi(request, 'admin/queue/stats', { i: root.token });
    expect(resp.status()).toBe(200);
    const body = (await resp.json()) as Record<string, unknown>;
    expect(body.deliver).toBeDefined();
    expect(body.inbox).toBeDefined();
  });

  test('admin/queue/clear succeeds (empty test queue)', async ({ request }) => {
    const resp = await callApi(request, 'admin/queue/clear', {
      i: root.token,
      queue: 'deliver',
      state: 'wait',
    });
    // 両 backend ともに 204 No Content (= mk-go: c.NoContent / TS: handler
    // null return → Endpoint base 204) を返すので strict 化。
    expect(resp.status()).toBe(204);
  });

  test('admin/queue/promote-jobs succeeds (empty test queue)', async ({ request }) => {
    const resp = await callApi(request, 'admin/queue/promote-jobs', {
      i: root.token,
      queue: 'deliver',
    });
    // mk-go: 200 + { promoted: 0 } / TS: 204 (handler null return) の差を許容。
    expect([200, 204]).toContain(resp.status());
  });

  test('admin/queue/show-job returns 4xx for unknown id', async ({ request }) => {
    const resp = await callApi(request, 'admin/queue/show-job', {
      i: root.token,
      queue: 'deliver',
      id: 'nonexistent-job-id',
    });
    expect([400, 404]).toContain(resp.status());
  });

  // retry-job / remove-job: 不明 id の挙動は backend 間で差あり。
  //   - mk-go: asynq DeleteTask / RunTask が idempotent な場合 204 を返すこと
  //     がある (= 内部 task store が「該当 id 無し」を success 扱い)
  //   - TS (BullMQ): 通常 4xx
  // どちらも frontend 側は「削除済」扱いするため LCD で吸収。
  test('admin/queue/retry-job returns 2xx/4xx for unknown id', async ({ request }) => {
    const resp = await callApi(request, 'admin/queue/retry-job', {
      i: root.token,
      queue: 'deliver',
      id: 'nonexistent-job-id',
    });
    expect([200, 204, 400, 404]).toContain(resp.status());
  });

  test('admin/queue/remove-job returns 2xx/4xx for unknown id', async ({ request }) => {
    const resp = await callApi(request, 'admin/queue/remove-job', {
      i: root.token,
      queue: 'deliver',
      id: 'nonexistent-job-id',
    });
    expect([200, 204, 400, 404]).toContain(resp.status());
  });
});
