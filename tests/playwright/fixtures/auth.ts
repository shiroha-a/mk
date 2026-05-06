// #744 Phase 1: signup / signin helper.
// upstream Misskey TS と互換な /api/signup と /api/signin-flow を叩き、
// 取得した access token を spec から再利用できる形にする。
//
// 注: root account (admin/accounts/create) の作成と
// disableRegistration=false の切替は globalSetup で行う (spec 開始前に
// 1 度だけ)。本 fixture では signup endpoint と signin-flow の通常経路
// のみ提供する。

import { randomBytes } from 'node:crypto';
import type { APIRequestContext } from '@playwright/test';
import { callApi } from './api';

// upstream Misskey TS の username 仕様: 1〜20 文字、`[a-zA-Z0-9_]`。
// 本 helper はこの制約を必ず満たす uniq username を生成する。
const USERNAME_MAX = 20;

// randomUsername returns a uniq, validation-safe username for use in spec.
// `Date.now()` は 13 桁 epoch なので prefix と合わせて簡単に 20 文字を
// 超えるが、本関数は full 8 文字 hex random suffix を使い、prefix を
// 短くスライスして必ず 20 文字以内に収める。
//
// upstream の username regex は `^\w{1,20}$` で underscore 1 つは可。
// suffix は hex (= alphanumeric) のみなので追加の sanitize 不要。
export function randomUsername(prefix: string): string {
  // 8 hex chars = 32 bit random で test 内 uniq には十分 (collision は
  // 1 / 2^32 ~ 10^-10 オーダー)。
  const suffix = randomBytes(4).toString('hex');
  // prefix + "_" + 8 hex = max 20 → prefix は 11 chars までに切る。
  const safePrefix = prefix.slice(0, USERNAME_MAX - suffix.length - 1);
  return `${safePrefix}_${suffix}`;
}

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
