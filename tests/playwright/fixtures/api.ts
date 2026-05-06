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
