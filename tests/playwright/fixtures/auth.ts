// #744 Phase 1: signup / signin helper.
// upstream Misskey TS と互換な /api/signup と /api/signin-flow を叩き、
// 取得した access token を spec から再利用できる形にする。
//
// 注: root account (admin/accounts/create) の作成と
// disableRegistration=false の切替は globalSetup で行う (spec 開始前に
// 1 度だけ)。本 fixture では signup endpoint と signin-flow の通常経路
// のみ提供する。

import type { APIRequestContext } from '@playwright/test';
import { callApi } from './api';

export interface Principal {
  id: string;
  token: string;
  username: string;
}

// signupUser creates a regular (non-root) account via /api/signup. Phase 1
// では captcha / email 認証は disabled な instance config を使うので
// username + password だけで通る。複数回呼んでも username が違えば成功。
//
// 同 instance で複数 user が必要な spec はこの helper を使う
// (admin/accounts/create は 1 度しか通らないので root 専用)。
export async function signupUser(
  request: APIRequestContext,
  username: string,
  password = 'password1234',
): Promise<Principal> {
  const resp = await callApi(request, 'signup', { username, password });
  if (resp.status() !== 200) {
    throw new Error(`signup failed: ${resp.status()} ${await resp.text()}`);
  }
  const body = await resp.json();
  return { id: body.id, token: body.token, username };
}

// signin posts /api/signin-flow and returns the access token. upstream
// Misskey TS は #705 で本家準拠化されており、レスポンスは
// `{finished: true, i: <token>}`。
//
// signin-flow が 2FA / passkey 経路に分岐するアカウント (= step 1 で
// `next: 'totp' | 'passkey'`) は本 helper では扱わない。Phase 1 では
// password のみの signin path を test する。
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
  if (!body.i) {
    throw new Error(`signin-flow returned no token: ${JSON.stringify(body)}`);
  }
  return body.i as string;
}
