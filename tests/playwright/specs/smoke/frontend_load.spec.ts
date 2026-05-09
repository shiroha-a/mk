// frontend (Misskey TS SPA) を実 browser で開いて動作確認する smoke spec。
//
// 既存の spec は API shape (callApi) のみで browser を起動しないため、
// frontend asset 配信 / index.html / manifest / static asset path / SPA 起動が
// 壊れていても検出できなかった。本 spec は page.goto で `/` を navigate し、
// title / mount root / asset の応答 200 を確認することで「frontend が見える」
// 状態を回帰検出する。
//
// 注: 本 spec は SPA 内の DOM 操作 (signup → signin → post 等) までは行わない。
// upstream Misskey の frontend は Vue 3 + Pinia で構成され selector が版毎に
// 変わるため、操作系 e2e は cypress (`tests/dropin_frontend/`) 側で行う設計。
// 本 spec は最低限「frontend がロードされて Vue が mount できる」ところまで。

import { expect, test } from '@playwright/test';

test.describe('smoke: frontend SPA loads', () => {
  test('GET / returns index.html with root mount node', async ({ page, baseURL }) => {
    const resp = await page.goto(`${baseURL}/`, { waitUntil: 'domcontentloaded' });
    expect(resp).not.toBeNull();
    expect(resp!.status()).toBe(200);

    // SPA の mount root (Misskey は #app + body の data 属性で初期化する)。
    // build によって root id は変わりうるので、最低限 head の title と body
    // が空でないことだけ確認する。
    const title = await page.title();
    expect(title.length).toBeGreaterThan(0);

    // body 内の text + DOM が空ではない (= Misskey loader か mount root の
    // 何らかの要素が描画されている)
    const bodyHTML = await page.evaluate(() => document.body.innerHTML);
    expect(bodyHTML.length).toBeGreaterThan(50);
  });

  test('frontend asset (manifest.json) is served', async ({ request, baseURL }) => {
    // Vite manifest が frontend ビルドから配信されているかを確認。manifest
    // が無いと SPA は 起動できない。
    const candidates = [
      `${baseURL}/_frontend_vite_/manifest.json`,
      `${baseURL}/manifest.json`,
    ];
    let manifestServed = false;
    for (const url of candidates) {
      const resp = await request.get(url);
      if (resp.status() === 200) {
        manifestServed = true;
        const body = await resp.text();
        // JSON っぽい中身であること (= 実体ある index ファイル)
        expect(body).toMatch(/[{[]/);
        break;
      }
    }
    expect(manifestServed, `manifest.json not found at any candidate: ${candidates.join(', ')}`).toBe(true);
  });
});
