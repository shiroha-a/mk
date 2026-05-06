// #744 Phase 1: signup helper.
// upstream Misskey TS の signup-flow / signin-flow と互換な request を投げ、
// 取得した access token を spec から再利用できる形にする。

import type { APIRequestContext } from '@playwright/test';
import { createRootAccount } from './api';

export interface Principal {
  id: string;
  token: string;
  username: string;
}

// signupRoot creates the very first account on the freshly-bootstrapped
// instance via admin/accounts/create. upstream Misskey TS と互換挙動で、
// 2 回目以降の同 endpoint は 403。
//
// **冪等性の前提**: 本 helper を 2 回呼ぶには間に DB volume の clean が
// 必要 (= `make playwright-down` → `make playwright-up`)。Phase 1 では
// stack を毎回 fresh で立てる前提なので問題ない。複数 spec が同一 stack
// で走る後続 PR では globalSetup で DB reset を組み込むか、signup-flow
// 経路 (= 通常 user 作成) の helper を追加する想定。
export async function signupRoot(
  request: APIRequestContext,
  username = 'alice',
  password = 'password1234',
): Promise<Principal> {
  const created = await createRootAccount(request, username, password);
  return { ...created, username };
}
