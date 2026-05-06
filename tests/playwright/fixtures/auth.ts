// #744 Phase 1: signup / signin helper.
// upstream Misskey TS の signup-flow / signin-flow と互換な request を投げ、
// 取得した access token を spec から再利用できる形にする。

import type { APIRequestContext } from '@playwright/test';
import { callApi, createRootAccount } from './api';

export interface Principal {
  id: string;
  token: string;
  username: string;
}

// signup creates the very first account on the freshly-bootstrapped instance
// via admin/accounts/create. mk-go が 2 回目以降に同 endpoint を 403 にする
// ことは upstream と同じ挙動なので、複数 user が必要な spec では別経路
// (signup-flow) を使う。Phase 1 smoke では root 1 人で十分なので最小実装。
export async function signupRoot(
  request: APIRequestContext,
  username = 'alice',
  password = 'password1234',
): Promise<Principal> {
  const created = await createRootAccount(request, username, password);
  return { ...created, username };
}

// signin posts /api/signin-flow with the password and returns the token.
// /api/i で hydrate するのは fetch 側の責任 (Misskey の signin-flow は
// `{finished: true, i: <token>}` を返すが id を含まない、 Phase 1 では
// id まで必要なら createRootAccount の戻り値を持ち回せばよい)。
export async function signin(
  request: APIRequestContext,
  username: string,
  password: string,
): Promise<string> {
  const resp = await callApi(request, 'signin-flow', { username, password });
  if (resp.status() !== 200) {
    throw new Error(`signin-flow failed: ${resp.status()} ${await resp.text()}`);
  }
  const body = await resp.json();
  const token = body.i ?? body.token;
  if (!token) {
    throw new Error(`signin-flow returned no token: ${JSON.stringify(body)}`);
  }
  return token as string;
}
