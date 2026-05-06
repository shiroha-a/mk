// #744 Phase 1 PR-2: Playwright globalSetup.
//
// 全 spec が start する前に 1 度だけ実行される。本 globalSetup は instance
// を test runnable な状態に整える 3 つの仕事を行う:
//
//   1. Redis FLUSHDB — mk-go の signup endpoint は IP base 1h 5 回の rate
//      limit が hardcoded (internal/server/middleware/ratelimit_defs.go)
//      で、test 累積で 429 になるため毎 run で counter をゼロから始める
//   2. admin/accounts/create で root (alice) を作成 — admin/update-meta
//      など admin 専用 API を叩くために必須
//   3. admin/update-meta で disableRegistration=false に切替 — model/meta.go
//      の default は true で、initial state の signup endpoint は invitation
//      コード必須。Phase 1 spec は signup-flow で fresh user を作るので
//      registration を open にしておく
//
// root の credentials は `.auth/root.json` に書き出して spec から読める
// ようにする。複数 spec で root token を再利用するための fixture。

import { mkdirSync, writeFileSync } from 'node:fs';
import { request as createRequest } from '@playwright/test';
import { resetRateLimit } from './fixtures/rate_limit';

const baseURL = process.env.MK_BASE_URL ?? 'http://mkgo:3000';

export default async function globalSetup(): Promise<void> {
  // 1. Redis flush (#744 PR-2). 失敗は throw して fail-fast にする —
  // silent warn だと後段の signup spec が 429 で fail して原因が見えない。
  resetRateLimit();
  // eslint-disable-next-line no-console
  console.log('[globalSetup] redis FLUSHDB done');

  const ctx = await createRequest.newContext();
  try {
    // 2. Root 作成 (admin/accounts/create は 1 回目だけ通る)
    const createResp = await ctx.post(`${baseURL}/api/admin/accounts/create`, {
      data: { username: 'alice', password: 'password1234' },
      failOnStatusCode: false,
    });
    if (createResp.status() !== 200) {
      throw new Error(
        `globalSetup admin/accounts/create failed: ${createResp.status()} ${await createResp.text()}`,
      );
    }
    const root = await createResp.json();

    // 3. disableRegistration=false に切り替え
    const metaResp = await ctx.post(`${baseURL}/api/admin/update-meta`, {
      data: { i: root.token, disableRegistration: false },
      failOnStatusCode: false,
    });
    if (metaResp.status() !== 200 && metaResp.status() !== 204) {
      throw new Error(
        `globalSetup admin/update-meta failed: ${metaResp.status()} ${await metaResp.text()}`,
      );
    }

    // root credentials を spec から読めるように file 出力
    mkdirSync('.auth', { recursive: true });
    writeFileSync(
      '.auth/root.json',
      JSON.stringify({
        id: root.id,
        token: root.token,
        username: 'alice',
      }),
    );
    // eslint-disable-next-line no-console
    console.log('[globalSetup] root account ready, registration opened');
  } finally {
    await ctx.dispose();
  }
}
