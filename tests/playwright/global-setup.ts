// #744 Phase 1 PR-2: Playwright globalSetup.
//
// 全 spec が start する前に 1 度だけ実行される。本 globalSetup は instance
// を test runnable な状態に整える 3 つの仕事を行う:
//
//   1. Redis FLUSHDB — mk-go の signup endpoint は IP base 1h 5 回の rate
//      limit が hardcoded (internal/server/middleware/ratelimit_defs.go)
//      で、test 累積で 429 になるため毎 run で counter をゼロから始める
//   2. root (alice) を idempotent に確保 — admin/accounts/create は初回のみ
//      通り、再実行時は signin-flow で既存 alice の token を再取得する
//   3. admin/update-meta で disableRegistration=false に切替 — model/meta.go
//      の default は true で、initial state の signup endpoint は invitation
//      コード必須。Phase 1 spec は signup-flow で fresh user を作るので
//      registration を open にしておく
//
// root の credentials は `.auth/root.json` に書き出して spec から読める
// ようにする。複数 spec で root token を再利用するための fixture。

import { mkdirSync, writeFileSync } from 'node:fs';
import { type APIRequestContext, request as createRequest } from '@playwright/test';
import { resetRateLimit } from './fixtures/rate_limit';

const baseURL = process.env.MK_BASE_URL ?? 'http://mkgo:3000';

// root credentials は globalSetup と spec 全体で共有する。username / password
// 値は spec 内で hardcode されている箇所 (= signin form 入力等) があるが、
// 本 file では「create / signin の payload」と「.auth/root.json の username」
// の整合性を維持する責務として 1 箇所に集約する。
const ROOT_USERNAME = 'alice';
const ROOT_PASSWORD = 'password1234';

interface RootCreds {
  id: string;
  token: string;
  username: string;
}

// admin/accounts/create を試みる。初回 (alice 未存在) は 200 で credentials
// を返す。既存の場合は null を返して caller に signin fallback を委譲する。
async function tryCreateRoot(ctx: APIRequestContext): Promise<RootCreds | null> {
  const resp = await ctx.post(`${baseURL}/api/admin/accounts/create`, {
    data: { username: ROOT_USERNAME, password: ROOT_PASSWORD },
    failOnStatusCode: false,
  });
  if (resp.status() === 200) {
    const body = await resp.json();
    if (typeof body.id !== 'string' || typeof body.token !== 'string') {
      throw new Error(
        `globalSetup admin/accounts/create returned malformed body: ${JSON.stringify(body)}`,
      );
    }
    return { id: body.id, token: body.token, username: ROOT_USERNAME };
  }
  // 既存 alice (= 403 ACCESS_DENIED) は idempotent re-run の正常系。
  // それ以外 (5xx 等) は本当に失敗しているので throw する。
  if (resp.status() !== 403) {
    throw new Error(
      `globalSetup admin/accounts/create unexpected status: ${resp.status()} ${await resp.text()}`,
    );
  }
  return null;
}

// 既存 alice として signin-flow で token を再取得する。fresh DB から alice
// が作られた直後の re-run / postgres volume が残っているケースで使う。
async function signinAsExistingRoot(ctx: APIRequestContext): Promise<RootCreds> {
  // 2FA なし user は signin-flow に { username, password } を送ると
  // { finished: true, id, i: token } が返る (internal/api/signin/handler.go:118)。
  const resp = await ctx.post(`${baseURL}/api/signin-flow`, {
    data: { username: ROOT_USERNAME, password: ROOT_PASSWORD },
    failOnStatusCode: false,
  });
  if (resp.status() !== 200) {
    throw new Error(
      `globalSetup signin-flow failed for existing alice: ${resp.status()} ${await resp.text()}`,
    );
  }
  const body = await resp.json();
  if (!body.finished || typeof body.id !== 'string' || typeof body.i !== 'string') {
    throw new Error(
      `globalSetup signin-flow returned non-final / malformed state: ${JSON.stringify(body)}`,
    );
  }
  return { id: body.id, token: body.i, username: ROOT_USERNAME };
}

export default async function globalSetup(): Promise<void> {
  // 1. Redis flush (#744 PR-2). 失敗は throw して fail-fast にする —
  // silent warn だと後段の signup spec が 429 で fail して原因が見えない。
  resetRateLimit();
  // eslint-disable-next-line no-console
  console.log('[globalSetup] redis FLUSHDB done');

  // self-signed cert を accept する。playwright.config.ts の use 設定は
  // spec scope のみなので globalSetup には届かない (= globalSetup は
  // request context を独立に作るため明示指定が必要、#817 part2 で HTTPS 化)。
  const ctx = await createRequest.newContext({ ignoreHTTPSErrors: true });
  try {
    // 2. Root を idempotent に確保。create が通ればそれを使い、既に alice
    // が存在する場合 (= postgres volume が残った状態の re-run) は signin-flow
    // で token を再取得する。
    let root = await tryCreateRoot(ctx);
    if (root === null) {
      root = await signinAsExistingRoot(ctx);
      // eslint-disable-next-line no-console
      console.log('[globalSetup] reusing existing root (signin-flow path)');
    }

    // 3. disableRegistration=false に切り替え (再実行時も idempotent)
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
    writeFileSync('.auth/root.json', JSON.stringify(root));
    // eslint-disable-next-line no-console
    console.log('[globalSetup] root account ready, registration opened');
  } finally {
    await ctx.dispose();
  }
}
