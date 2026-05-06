// #744 Phase 1: REST helper used across spec files.
// Misskey API は POST /api/<endpoint> に JSON body を送る規約 (cy.request
// で dropin cypress 側もそうしている)。Playwright の APIRequestContext を
// 直接使うラッパとして書く。

import type { APIRequestContext, APIResponse } from '@playwright/test';

// MK_BASE_URL is the base origin for the mk-go backend. spec の playwright
// config で baseURL を mkgo container に向けているのでここでは同 env から
// 読む。Playwright runner container の env として供給される。
const baseURL = process.env.MK_BASE_URL ?? 'http://mkgo:3000';

// callApi は POST /api/<endpoint> を JSON body で叩く最小ラッパ。
// failOnStatusCode を切ってあるので 4xx/5xx でも APIResponse が返る。
// 呼び出し側で resp.status() / resp.json() を見る。
export async function callApi(
  request: APIRequestContext,
  endpoint: string,
  body: Record<string, unknown> = {},
): Promise<APIResponse> {
  return request.post(`${baseURL}/api/${endpoint}`, {
    data: body,
    failOnStatusCode: false,
  });
}

// admin/accounts/create は root 作成時のみ通る。upstream Misskey TS は
// 1 回目の signup で root を作成、2 回目以降の同 endpoint は 403。
// signup helper として「最初の呼び出しなら root が作成される」前提。
export async function createRootAccount(
  request: APIRequestContext,
  username: string,
  password: string,
): Promise<{ id: string; token: string }> {
  const resp = await callApi(request, 'admin/accounts/create', { username, password });
  if (resp.status() !== 200) {
    throw new Error(
      `admin/accounts/create failed: ${resp.status()} ${await resp.text()}`,
    );
  }
  const body = await resp.json();
  return { id: body.id, token: body.token };
}
